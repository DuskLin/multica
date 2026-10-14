package execenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func scriptFixture(t *testing.T, body string) LocalScriptParams {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Bash fixture uses POSIX paths")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash is unavailable")
	}
	source := filepath.Join(t.TempDir(), "source with spaces")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	// No executable permission is needed; the daemon invokes Bash explicitly.
	script := "init $(literal); workspace.sh"
	if err := os.WriteFile(filepath.Join(source, script), []byte("#!/usr/bin/env bash\nset -eu\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LocalScriptParams{LocalPath: source, InitScript: script, WorkspaceID: "script-workspace", TaskID: "script-task"}
}

func TestLocalScriptParallelNonGit(t *testing.T) {
	params := scriptFixture(t, "printf '%s' \"$PWD\" > \"$1/source.txt\"\nprintf 'ready\\n'\n")
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	for _, taskID := range []string{"first", "second"} {
		root := t.TempDir()
		params := params
		params.TaskID = taskID
		go func() {
			path, err := prepareLocalScript(params, root)
			results <- result{path, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.path == second.path {
		t.Fatalf("parallel initialization: %#v, %#v", first, second)
	}
	source, _ := filepath.EvalSymlinks(params.LocalPath)
	for _, dir := range []string{first.path, second.path} {
		if filepath.Dir(dir) != filepath.Join(source, ".worktrees") {
			t.Fatalf("unexpected destination %s", dir)
		}
		content, err := os.ReadFile(filepath.Join(dir, "source.txt"))
		if err != nil || string(content) != source {
			t.Fatalf("wrong cwd or argument: %q, %v", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(params.LocalPath, "source.txt")); !os.IsNotExist(err) {
		t.Fatal("initializer wrote into the source instead of the destination")
	}
}

// TestLocalScriptFollowupsPreserveEdits verifies that later runs and agents of
// one issue reuse its directory without executing the initializer again.
func TestLocalScriptFollowupsPreserveEdits(t *testing.T) {
	params := scriptFixture(t, "printf initialized > \"$1/result.txt\"\nprintf called >> \"$PWD/calls.txt\"\n")
	params.IssueID = "issue-one"
	first, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "result.txt"), []byte("agent edits"), 0o600); err != nil {
		t.Fatal(err)
	}
	params.TaskID = "followup-run"
	second, err := prepareLocalScript(params, t.TempDir())
	if err != nil || second != first {
		t.Fatalf("followup lost its workspace: %q, %v", second, err)
	}
	content, err := os.ReadFile(filepath.Join(second, "result.txt"))
	if err != nil || string(content) != "agent edits" {
		t.Fatalf("followup overwrote edits: %q, %v", content, err)
	}
	calls, err := os.ReadFile(filepath.Join(params.LocalPath, "calls.txt"))
	if err != nil || string(calls) != "called" {
		t.Fatalf("initializer ran again: %q, %v", calls, err)
	}
}

// TestLocalScriptConversationIsolation checks the workspace and conversation
// boundaries, including chat sessions and runs with no durable conversation.
func TestLocalScriptConversationIsolation(t *testing.T) {
	params := scriptFixture(t, "printf ready > \"$1/result.txt\"\n")
	params.IssueID = "conversation"
	first, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		params LocalScriptParams
	}{
		{name: "other workspace", params: LocalScriptParams{WorkspaceID: "other", IssueID: "conversation"}},
		{name: "other issue", params: LocalScriptParams{WorkspaceID: params.WorkspaceID, IssueID: "other"}},
		{name: "chat with matching id", params: LocalScriptParams{WorkspaceID: params.WorkspaceID, ChatSessionID: "conversation"}},
		{name: "standalone run", params: LocalScriptParams{WorkspaceID: params.WorkspaceID, TaskID: "conversation"}},
	}
	seen := map[string]bool{first: true}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolated := tc.params
			isolated.LocalPath, isolated.InitScript = params.LocalPath, params.InitScript
			path, err := prepareLocalScript(isolated, t.TempDir())
			if err != nil || seen[path] {
				t.Fatalf("conversation shared another workspace: %q, %v", path, err)
			}
			seen[path] = true
			isolated.TaskID = "another-run"
			if isolated.ChatSessionID != "" {
				continued, err := prepareLocalScript(isolated, t.TempDir())
				if err != nil || continued != path {
					t.Fatalf("chat did not continue: %q, %v", continued, err)
				}
			}
		})
	}
}

// TestLocalScriptFailedInitializationIsNotReplayed protects partial work from
// destructive retries and ensures failures cannot allocate extra directories.
func TestLocalScriptFailedInitializationIsNotReplayed(t *testing.T) {
	params := scriptFixture(t, "printf partial >> \"$1/result.txt\"\nexit 7\n")
	params.IssueID = "failed-issue"
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil {
		t.Fatal("initializer unexpectedly succeeded")
	}
	params.TaskID = "retry"
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("partial initialization was silently retried: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(params.LocalPath, ".worktrees", "multica-*", "result.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retry created extra workspaces: %v, %v", files, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil || string(content) != "partial" {
		t.Fatalf("retry modified partial work: %q, %v", content, err)
	}
}

// TestLocalScriptWorkspaceClaim protects the whole run across daemon processes
// and permits a new holder after release without replacing the lock file.
func TestLocalScriptWorkspaceClaim(t *testing.T) {
	params := scriptFixture(t, "printf ready > \"$1/result.txt\"\n")
	params.IssueID = "shared-issue"
	release, err := ClaimLocalScriptWorkspace(params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	params.TaskID = "another-run"
	if duplicateRelease, err := ClaimLocalScriptWorkspace(params); err == nil {
		duplicateRelease()
		t.Fatal("another daemon claimed a live conversation")
	}
	release()
	release()
	nextRelease, err := ClaimLocalScriptWorkspace(params)
	if err != nil {
		t.Fatalf("released workspace remained busy: %v", err)
	}
	nextRelease()
}

// TestLocalScriptRejectsReusedSymlink prevents a ready workspace redirected by
// a later actor from pointing an agent at the user's source directory.
func TestLocalScriptRejectsReusedSymlink(t *testing.T) {
	params := scriptFixture(t, "printf ready > \"$1/result.txt\"\n")
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(params.LocalPath, path); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("reused workspace followed a symlink: %v", err)
	}
}

// TestLocalScriptRecreatedWorkspaceCannotReuseOldReadyRecord checks that a
// failed reinitialization does not inherit a deleted directory's ready state.
func TestLocalScriptRecreatedWorkspaceCannotReuseOldReadyRecord(t *testing.T) {
	params := scriptFixture(t, "printf ready > \"$1/result.txt\"\n")
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(params.LocalPath, params.InitScript), []byte("printf partial > \"$1/result.txt\"\nexit 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil {
		t.Fatal("reinitialization unexpectedly succeeded")
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("old ready record accepted partial reinitialization: %v", err)
	}
}

func TestLocalScriptFailurePreservesLogAndPartialWork(t *testing.T) {
	params := scriptFixture(t, "printf 'partial' > \"$1/result.txt\"\nprintf 'initialization failed\\n' >&2\nexit 7\n")
	root := t.TempDir()
	_, err := prepareLocalScript(params, root)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("expected initializer failure, got %v", err)
	}
	log, readErr := os.ReadFile(filepath.Join(root, "logs", "init-workspace.log"))
	if readErr != nil || !strings.Contains(string(log), "initialization failed") {
		t.Fatalf("initializer output missing: %q, %v", log, readErr)
	}
	files, _ := filepath.Glob(filepath.Join(params.LocalPath, ".worktrees", "*", "result.txt"))
	if len(files) != 1 {
		t.Fatalf("partial work lost: %v", files)
	}
}

func TestLocalScriptRejectsDestinationSymlink(t *testing.T) {
	params := scriptFixture(t, "rmdir \"$1\"\nln -s \"$PWD\" \"$1\"\n")
	_, err := prepareLocalScript(params, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("destination redirected to source: %v", err)
	}
}

func TestLocalScriptPreparePreservesResultAfterCleanup(t *testing.T) {
	params := scriptFixture(t, "printf 'initialized' > \"$1/result.txt\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env, err := PrepareIsolated(ctx, preparationHelperTestCommand(), PrepareParams{
		WorkspacesRoot: t.TempDir(), WorkspaceID: "ws-script", TaskID: "script-run",
		Provider: "claude", LocalScript: &params,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !env.LocalDirectory || env.LocalWorktree != nil {
		t.Fatalf("wrong lifecycle: %+v", env)
	}
	if err := CleanupRuntimeConfig(env.WorkDir, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := CleanupSidecars(env.RootDir); err != nil {
		t.Fatal(err)
	}
	env.Cleanup(false)
	content, err := os.ReadFile(filepath.Join(env.WorkDir, "result.txt"))
	if err != nil || string(content) != "initialized" {
		t.Fatalf("result lost: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(params.LocalPath, ".multica")); !os.IsNotExist(err) {
		t.Fatal("runtime sidecars leaked into source directory")
	}
}

func TestLocalScriptCancellationStopsInitializerChildren(t *testing.T) {
	params := scriptFixture(t, "(sleep 0.5; printf late > \"$1/late.txt\") &\nprintf started > \"$1/started.txt\"\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := PrepareIsolated(ctx, preparationHelperTestCommand(), PrepareParams{
			WorkspacesRoot: root, WorkspaceID: "ws-cancel-script", TaskID: "cancel-script",
			Provider: "claude", LocalScript: &params,
		}, nil)
		done <- err
	}()
	var started []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		started, _ = filepath.Glob(filepath.Join(params.LocalPath, ".worktrees", "*", "started.txt"))
		if len(started) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled initialization succeeded")
	}
	if len(started) != 1 {
		t.Fatal("initializer did not start")
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(filepath.Dir(started[0]), "late.txt")); !os.IsNotExist(err) {
		t.Fatal("initializer child survived cancellation")
	}
}
