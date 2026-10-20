package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScriptPreparationStreamsBeforeCompletion(t *testing.T) {
	messages := make(chan TaskMessageData, 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []TaskMessageData `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, msg := range body.Messages {
			messages <- msg
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{client: NewClient(srv.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var seq atomic.Int32
	finish := d.streamScriptPreparation("task", root, &seq)
	receive := func() TaskMessageData {
		t.Helper()
		select {
		case msg := <-messages:
			return msg
		case <-time.After(5 * time.Second):
			t.Fatal("no live preparation message")
			return TaskMessageData{}
		}
	}
	start := receive()
	if start.Type != "tool_use" {
		t.Fatalf("missing initial state: %+v", start)
	}
	// The file appears only after the preparation helper starts.
	path := filepath.Join(root, "logs", "init-workspace.log")
	if err := os.WriteFile(path, []byte("第一步\nstderr: diagnostic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := receive()
	if !strings.Contains(output.Output, "第一步\nstderr: diagnostic") {
		t.Fatalf("missing live output: %+v", output)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("last line without newline")
	_ = f.Close()
	finish(errors.New("exit status 7"))
	last, failed := receive(), receive()
	if last.Output != "last line without newline" || !strings.Contains(failed.Output, "exit status 7") || failed.CallID != start.CallID {
		t.Fatalf("lost final output: %+v %+v", last, failed)
	}
	if start.Seq != 1 || output.Seq != 2 || last.Seq != 3 || failed.Seq != 4 || seq.Add(1) != 5 {
		t.Fatal("preparation and agent sequence overlap")
	}
	// The reporter is joined and cannot emit after completion.
	select {
	case msg := <-messages:
		t.Fatalf("unexpected message: %+v", msg)
	default:
	}
}

func TestScriptPreparationBoundsOutput(t *testing.T) {
	var total atomic.Int64
	var limited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []TaskMessageData `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, msg := range body.Messages {
			if strings.Contains(msg.Output, "Live output limit reached") {
				limited.Store(true)
			} else if msg.Tool == "Initialization output" {
				total.Add(int64(len(msg.Output)))
			}
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0755)
	_ = os.WriteFile(filepath.Join(root, "logs", "init-workspace.log"), []byte(strings.Repeat("x", 1024*1024)), 0600)
	d := &Daemon{client: NewClient(srv.URL), logger: slog.Default()}
	var seq atomic.Int32
	finish := d.streamScriptPreparation("task", root, &seq)
	finish(context.Canceled)
	if total.Load() != 256*1024 || !limited.Load() {
		t.Fatalf("unbounded or silently truncated output: %d %v", total.Load(), limited.Load())
	}
}
