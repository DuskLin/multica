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
	"sync"
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
			body := "printf initialized >> \"$1/initialized.txt\"\n"
			body += "printf 'init stdout\\n'\n printf 'init stderr\\n' >&2\n"
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
			var messageMu sync.Mutex
			var messages []TaskMessageData
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/messages") {
					var body struct {
						Messages []TaskMessageData `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					messageMu.Lock()
					messages = append(messages, body.Messages...)
					messageMu.Unlock()
				}
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
			messageMu.Lock()
			var initializationOutput string
			for i, message := range messages {
				if message.Seq != i+1 {
					t.Errorf("overlapping message sequence: %+v", messages)
				}
				if message.Tool == "Initialization output" {
					initializationOutput += message.Output
				}
			}
			messageMu.Unlock()
			if !strings.Contains(initializationOutput, "init stdout\n") || !strings.Contains(initializationOutput, "init stderr\n") {
				t.Fatalf("initializer stdout/stderr were not reported: %q", initializationOutput)
			}
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
			if err := os.WriteFile(filepath.Join(result.WorkDir, "followup.txt"), []byte("first agent's work"), 0o600); err != nil {
				t.Fatal(err)
			}
			task.ID, task.AgentID = "script-followup", "other-agent"
			task.Agent = &AgentData{ID: task.AgentID, Name: "Other agent"}
			continued, err := d.runTask(context.Background(), task, "claude", 0, logger)
			if err != nil || continued.WorkDir != result.WorkDir || continued.DurableWorkDir != result.WorkDir {
				t.Fatalf("another agent did not continue in the same workspace: %+v, %v", continued, err)
			}
			content, err := os.ReadFile(filepath.Join(continued.WorkDir, "followup.txt"))
			if err != nil || string(content) != "first agent's work" {
				t.Fatalf("followup lost edits after cleanup: %q, %v", content, err)
			}
			content, err = os.ReadFile(filepath.Join(continued.WorkDir, "initialized.txt"))
			if err != nil || string(content) != "initialized" {
				t.Fatalf("initializer ran on followup: %q, %v", content, err)
			}
		})
	}
}

func TestScriptWorkspacePrompt(t *testing.T) {
	task := Task{IssueID: "script-issue"}
	prompt := BuildPrompt(task, "claude", WithScriptWorkspace())
	if !strings.Contains(prompt, "local_path is the source directory") || !strings.Contains(prompt, "Follow-up runs reuse this directory") || !strings.Contains(prompt, "execute one at a time") {
		t.Fatal("script workspace prompt does not explain continuation and serialization")
	}
	if strings.Contains(BuildPrompt(task, "claude"), "## Script workspace") {
		t.Fatal("script guidance leaked into a different execution mode")
	}
}

// TestScriptConversationRunsSerialize verifies that issue agents and chat turns
// wait for their shared workspace, while other issues/workspaces remain runnable.
func TestScriptConversationRunsSerialize(t *testing.T) {
	for _, kind := range []string{"issue", "chat"} {
		t.Run(kind, func(t *testing.T) {
			const daemonID = "script-daemon"
			source := t.TempDir()
			ref, err := json.Marshal(localDirectoryRef{LocalPath: source, DaemonID: daemonID, ExecutionMode: localDirectoryModeScript, InitScript: "init.sh"})
			if err != nil {
				t.Fatal(err)
			}
			waited := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/wait-local-directory") {
					waited <- struct{}{}
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			d := &Daemon{cfg: Config{DaemonID: daemonID}, client: NewClient(srv.URL),
				localPathLocks: NewLocalPathLocker(), logger: slog.Default(), cancelPollInterval: time.Hour}
			first := Task{ID: "first-run", WorkspaceID: "script-workspace", AgentID: "first-agent",
				ProjectResources: []ProjectResourceData{{ID: "resource", ResourceType: localDirectoryResourceType, ResourceRef: ref}}}
			if kind == "issue" {
				first.IssueID = "shared-issue"
			} else {
				first.ChatSessionID = "shared-chat"
			}
			release, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), first, slog.Default())
			if abort || release == nil {
				t.Fatalf("first conversation run failed to claim: %v", abort)
			}
			t.Cleanup(release)
			for _, boundary := range []string{"other conversation", "other workspace"} {
				other := first
				other.ID = boundary
				if boundary == "other workspace" {
					other.WorkspaceID = "other-workspace"
				} else {
					other.IssueID, other.ChatSessionID = "other-issue", ""
				}
				otherRelease, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), other, slog.Default())
				if abort || otherRelease == nil {
					t.Fatalf("%s was blocked by unrelated run: %v", boundary, abort)
				}
				otherRelease()
			}
			// A separate daemon has no shared in-memory mutex, but must still
			// refuse to touch the live workspace through its advisory file lock.
			otherDaemon := &Daemon{cfg: Config{DaemonID: daemonID}, client: NewClient(srv.URL),
				localPathLocks: NewLocalPathLocker(), logger: slog.Default()}
			duplicate := first
			duplicate.ID = "duplicate-run"
			duplicateRelease, abort := otherDaemon.acquireLocalDirectoryLockIfNeeded(context.Background(), duplicate, slog.Default())
			if duplicateRelease != nil {
				duplicateRelease()
			}
			if !abort || duplicateRelease != nil {
				t.Fatal("another daemon entered the live workspace")
			}
			second := first
			second.ID, second.AgentID = "second-run", "second-agent"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan bool, 1)
			go func() {
				secondRelease, abort := d.acquireLocalDirectoryLockIfNeeded(ctx, second, slog.Default())
				if secondRelease != nil {
					secondRelease()
				}
				done <- abort
			}()
			select {
			case <-waited:
			case <-done:
				t.Fatal("second agent/chat turn did not wait for its conversation")
			case <-time.After(5 * time.Second):
				t.Fatal("waiting run did not report waiting_local_directory")
			}
			// Cancellation must free the waiter without releasing the first run.
			cancel()
			select {
			case abort := <-done:
				if !abort {
					t.Fatal("cancelled waiter proceeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled waiter did not exit")
			}
			release()
			thirdRelease, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), second, slog.Default())
			if abort || thirdRelease == nil {
				t.Fatalf("workspace stayed locked after release: %v", abort)
			}
			thirdRelease()
		})
	}
}
