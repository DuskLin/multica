package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunTaskScriptIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixtures")
	}
	for _, failInit := range []bool{false, true} {
		name := "success"
		if failInit {
			name = "initializer failure"
		}
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			body := "printf initialized > \"$1/initialized.txt\"\n"
			if failInit {
				body += "exit 7\n"
			}
			if err := os.WriteFile(filepath.Join(source, "init.sh"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			fakeBin := filepath.Join(t.TempDir(), "claude")
			writeTestExecutable(t, fakeBin, []byte(`#!/bin/sh
IFS= read -r _
printf 'agent result' > result.txt
printf '%s\n' '{"type":"system","session_id":"script-session"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"script-session","result":"done"}'
`))
			var starts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/start") {
					starts.Add(1)
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			d := &Daemon{
				client: NewClient(srv.URL), logger: logger,
				workspaces:     make(map[string]*workspaceState),
				runtimeIndex:   map[string]Runtime{"rt-script": {ID: "rt-script", Provider: "claude"}},
				activeEnvRoots: make(map[string]int),
				cfg: Config{DaemonID: "script-daemon", WorkspacesRoot: t.TempDir(), AgentTimeout: 5 * time.Second,
					ServerBaseURL: srv.URL, Agents: map[string]AgentEntry{"claude": {Path: fakeBin}}},
			}
			ref, _ := json.Marshal(localDirectoryRef{LocalPath: source, DaemonID: d.cfg.DaemonID, ExecutionMode: "script", InitScript: "init.sh"})
			task := Task{ID: "script-task", WorkspaceID: "script-workspace", RuntimeID: "rt-script", IssueID: "script-issue",
				AuthToken: "mat_script_fixture", AgentID: "script-agent", Agent: &AgentData{ID: "script-agent", Name: "Script agent"},
				ProjectResources: []ProjectResourceData{{ID: "script-resource", ResourceType: "local_directory", ResourceRef: ref}},
			}
			result, err := d.runTask(context.Background(), task, "claude", 0, logger)
			if failInit {
				if err == nil || starts.Load() != 0 {
					t.Fatalf("failed initializer launched agent: starts=%d, err=%v", starts.Load(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if starts.Load() != 1 || result.WorkDir == source || result.DurableWorkDir != result.WorkDir {
				t.Fatalf("wrong script result: %+v, starts=%d", result, starts.Load())
			}
			for _, file := range []string{"initialized.txt", "result.txt"} {
				if _, err := os.Stat(filepath.Join(result.WorkDir, file)); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(source, file)); !os.IsNotExist(err) {
					t.Fatalf("source modified: %s", file)
				}
			}
		})
	}
}

func TestScriptWorkspacePrompt(t *testing.T) {
	task := Task{IssueID: "script-issue"}
	prompt := BuildPrompt(task, "claude", WithScriptWorkspace())
	if !strings.Contains(prompt, "local_path is the source directory") || !strings.Contains(prompt, "do not assume earlier runs' edits") {
		t.Fatal("script workspace prompt does not distinguish the fresh destination from the source")
	}
	if strings.Contains(BuildPrompt(task, "claude"), "## Script workspace") {
		t.Fatal("script guidance leaked into a different execution mode")
	}
}
