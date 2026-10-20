package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestScriptIssueDoneOnlyAcceptsConfirmedCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result IssueGCCheckResult
		want   bool
	}{
		{"done", IssueGCCheckResult{Found: true, Category: "done"}, true},
		{"legacy done", IssueGCCheckResult{Found: true, Status: "done"}, true},
		{"custom completed", IssueGCCheckResult{Found: true, Category: "done", Status: "custom_done"}, true},
		{"reopened", IssueGCCheckResult{Found: true, Category: "started", Status: "done"}, false},
		{"closed", IssueGCCheckResult{Found: true, Category: "closed"}, false},
		{"cancelled", IssueGCCheckResult{Found: true, Status: "cancelled"}, false},
		{"missing", IssueGCCheckResult{Category: "done"}, false},
		{"unknown category", IssueGCCheckResult{Found: true, Category: "future", Status: "done"}, false},
		{"lookup error", IssueGCCheckResult{Found: true, Category: "done", Err: errors.New("unavailable")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scriptIssueDone(tc.result); got != tc.want {
				t.Fatalf("scriptIssueDone = %v, want %v", got, tc.want)
			}
		})
	}
}

// scriptGCDaemonFixture creates a registered script workspace backed exclusively
// by a local bare remote. It never initializes real agent credentials.
func scriptGCDaemonFixture(t *testing.T, d *Daemon) (execenv.LocalScriptParams, string) {
	t.Helper()
	source := t.TempDir()
	params := execenv.LocalScriptParams{LocalPath: source, WorkspaceID: "script-workspace", IssueID: "script-issue"}
	path, err := params.WorkspacePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git failed: %v: %s", err, output)
		}
	}
	run(path, "init", "-b", "main")
	run(path, "config", "user.name", "Test")
	run(path, "config", "user.email", "test@example.invalid")
	run(path, "config", "gc.auto", "0")
	run(path, "commit", "--allow-empty", "-m", "remote result")
	remote := filepath.Join(source, "remote")
	run(path, "clone", "--bare", path, remote)
	run(path, "remote", "add", "origin", remote)
	state := filepath.Join(filepath.Dir(path), ".multica-script")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	record := struct {
		WorkspaceID string `json:"workspace_id"`
		Kind        string `json:"kind"`
		ID          string `json:"id"`
	}{params.WorkspaceID, "issue", params.IssueID}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, filepath.Base(path)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := execenv.RegisterLocalScriptSource(d.cfg.WorkspacesRoot, params); err != nil {
		t.Fatal(err)
	}
	return params, path
}

func TestRunGCScriptDirectoriesFollowLiveIssueState(t *testing.T) {
	for _, scenario := range []string{"done", "reopened", "unavailable", "running"} {
		t.Run(scenario, func(t *testing.T) {
			checks := 0
			d := newGCTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !strings.HasSuffix(request.URL.Path, "/issues/gc-check") {
					http.NotFound(w, request)
					return
				}
				checks++
				if scenario == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				category := "done"
				if scenario == "reopened" && checks > 1 {
					category = "started"
				}
				response := issueGCBatchResponse{Issues: []IssueGCCheckResult{
					{ID: "script-issue", Found: true, Category: category},
				}}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			params, path := scriptGCDaemonFixture(t, d)
			if scenario == "running" {
				release, err := execenv.ClaimLocalScriptWorkspace(params)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			d.runGC(context.Background())
			_, err := os.Stat(path)
			if scenario == "done" {
				if !os.IsNotExist(err) || checks != 2 {
					t.Fatalf("completed issue retained: stat=%v checks=%d", err, checks)
				}
			} else if err != nil {
				t.Fatalf("unsafe deletion for %s: %v", scenario, err)
			}
		})
	}
}
