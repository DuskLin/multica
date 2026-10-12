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
	return LocalScriptParams{LocalPath: source, InitScript: script}
}

func TestLocalScriptParallelNonGit(t *testing.T) {
	params := scriptFixture(t, "printf '%s' \"$PWD\" > \"$1/source.txt\"\nprintf 'ready\\n'\n")
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		root := t.TempDir()
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
