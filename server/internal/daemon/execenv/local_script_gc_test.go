package execenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// completedScriptIssue models an authoritative done response in filesystem tests.
func completedScriptIssue(context.Context) (bool, error) { return true, nil }

func TestLocalScriptGCReinitializesAndRestoresBranches(t *testing.T) {
	params := scriptRecoveryFixture(t)
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "web"} {
		gitRun(t, filepath.Join(path, name), "switch", "feature/"+name)
	}
	collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue)
	if err != nil || !collected {
		t.Fatalf("collect = %v, %v", collected, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("work directory not reclaimed: %v", err)
	}
	if _, err := os.Stat(scriptRecoveryPath(path)); err != nil {
		t.Fatalf("remote branch record was lost: %v", err)
	}
	restored, err := prepareLocalScript(params, t.TempDir())
	if err != nil || restored != path {
		t.Fatalf("reopen = %s, %v", restored, err)
	}
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(restored, name)
		if branch := gitRun(t, repo, "branch", "--show-current"); branch != "feature/"+name {
			t.Fatalf("%s restored wrong branch: %s", name, branch)
		}
		if got := readFile(t, filepath.Join(repo, "feature.txt")); got != name+" result\n" {
			t.Fatalf("%s lost remote result: %s", name, got)
		}
	}
	if calls := readFile(t, filepath.Join(params.LocalPath, "calls.txt")); calls != "calledcalled" {
		t.Fatalf("expected reinitialization after GC, got %s", calls)
	}
	// A second completion must replace the branch record, not reuse stale data.
	gitRun(t, filepath.Join(path, "api"), "switch", "main")
	if collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue); err != nil || !collected {
		t.Fatalf("second collect = %v, %v", collected, err)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if branch := gitRun(t, filepath.Join(path, "api"), "branch", "--show-current"); branch != "main" {
		t.Fatalf("second reopen used stale branch: %s", branch)
	}
}

func TestLocalScriptGCPreservesWholeDirectoryWhenOneRepositoryIsUnsafe(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, LocalScriptParams, string)
	}{
		{"tracked edits", func(t *testing.T, _ LocalScriptParams, repo string) {
			writeFile(t, filepath.Join(repo, "tracked.txt"), "local edits\n")
		}},
		{"untracked file", func(t *testing.T, _ LocalScriptParams, repo string) {
			writeFile(t, filepath.Join(repo, "untracked.txt"), "local result\n")
		}},
		{"unpushed commit", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "config", "user.name", "Test")
			gitRun(t, repo, "config", "user.email", "test@example.invalid")
			writeFile(t, filepath.Join(repo, "tracked.txt"), "unpushed\n")
			gitRun(t, repo, "commit", "-am", "unpublished")
		}},
		{"no remote branch", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "switch", "-c", "local-only")
		}},
		{"stale remote tracking ref", func(t *testing.T, params LocalScriptParams, repo string) {
			gitRun(t, repo, "switch", "feature/web")
			gitRun(t, filepath.Join(params.LocalPath, "web-remote"), "update-ref", "-d", "refs/heads/feature/web")
		}},
		{"remote unavailable", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
		}},
		{"detached HEAD", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "switch", "--detach")
		}},
		{"unpublished other local branch", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "branch", "local-only")
		}},
		{"non-Git sibling", func(t *testing.T, _ LocalScriptParams, repo string) {
			writeFile(t, filepath.Join(filepath.Dir(repo), "design", "result.txt"), "unique result")
		}},
		{"remote inside deletion scope", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "remote", "set-url", "origin", repo)
		}},
		{"file URL remote inside deletion scope", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "remote", "set-url", "origin", "file://"+repo)
		}},
		{"remote object storage inside deletion scope", func(t *testing.T, params LocalScriptParams, repo string) {
			objects := filepath.Join(params.LocalPath, "web-remote", "objects")
			if err := os.Rename(objects, objects+"-backup"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(repo, ".git", "objects"), objects); err != nil {
				t.Fatal(err)
			}
		}},
		{"assume-unchanged hides edits", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "update-index", "--assume-unchanged", "tracked.txt")
			writeFile(t, filepath.Join(repo, "tracked.txt"), "hidden result")
		}},
		{"unpublished tagged commit", func(t *testing.T, _ LocalScriptParams, repo string) {
			gitRun(t, repo, "config", "user.name", "Test")
			gitRun(t, repo, "config", "user.email", "test@example.invalid")
			gitRun(t, repo, "switch", "-c", "scratch")
			writeFile(t, filepath.Join(repo, "tracked.txt"), "tag-only result")
			gitRun(t, repo, "commit", "-am", "unpublished tag result")
			gitRun(t, repo, "tag", "local-result")
			gitRun(t, repo, "switch", "main")
			gitRun(t, repo, "branch", "-D", "scratch")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := scriptRecoveryFixture(t)
			path, err := prepareLocalScript(params, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, params, filepath.Join(path, "web"))
			collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue)
			if err == nil || collected {
				t.Fatalf("unsafe workspace collected: %v, %v", collected, err)
			}
			for _, name := range []string{"api", "web"} {
				if _, err := os.Stat(filepath.Join(path, name, "tracked.txt")); err != nil {
					t.Fatalf("partially deleted %s: %v", name, err)
				}
			}
			if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
				t.Fatalf("skipped directory lost readiness: %v", err)
			}
		})
	}
}

func TestLocalScriptGCRechecksRefsAndRepositorySetBeforeRemoval(t *testing.T) {
	for _, change := range []string{"branch", "repository", "external worktree"} {
		t.Run(change, func(t *testing.T) {
			params := scriptRecoveryFixture(t)
			path, err := prepareLocalScript(params, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			collected, err := CollectLocalScriptWorkspace(context.Background(), params, func(context.Context) (bool, error) {
				if change == "branch" {
					gitRun(t, filepath.Join(path, "api"), "branch", "new-local-result")
				} else if change == "repository" {
					gitRun(t, path, "init", filepath.Join(path, "new-project"))
				} else {
					gitRun(t, filepath.Join(path, "api"), "worktree", "add", "--detach", filepath.Join(t.TempDir(), "external"), "HEAD")
				}
				return true, nil
			})
			if err == nil || collected {
				t.Fatalf("concurrent %s change was deleted: %v, %v", change, collected, err)
			}
			if _, err := os.Stat(filepath.Join(path, "web", "tracked.txt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestLocalScriptGCPreservesWorkspaceWhenRemoteChangesDuringFetch models a
// force-push after advertisement while the old commit still exists locally.
func TestLocalScriptGCPreservesWorkspaceWhenRemoteChangesDuringFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git wrapper fixture")
	}
	params := scriptRecoveryFixture(t)
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(path, "api")
	previous := gitRun(t, repository, "rev-parse", "HEAD")
	gitRun(t, repository, "config", "user.name", "Test")
	gitRun(t, repository, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(repository, "result.txt"), "published result")
	gitRun(t, repository, "add", "result.txt")
	gitRun(t, repository, "commit", "-m", "publish result")
	gitRun(t, repository, "push", "origin", "main")
	published := gitRun(t, repository, "rev-parse", "HEAD")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("SCRIPT_TEST_REAL_GIT", realGit)
	t.Setenv("SCRIPT_TEST_REMOTE", filepath.Join(params.LocalPath, "api-remote"))
	t.Setenv("SCRIPT_TEST_PREVIOUS", previous)
	t.Setenv("SCRIPT_TEST_CHANGED", filepath.Join(bin, "changed"))
	wrapper := `#!/bin/sh
if [ "$3" = ls-remote ] && [ ! -e "$SCRIPT_TEST_CHANGED" ]; then
  "$SCRIPT_TEST_REAL_GIT" "$@" || exit 1
  "$SCRIPT_TEST_REAL_GIT" --git-dir="$SCRIPT_TEST_REMOTE" update-ref refs/heads/main "$SCRIPT_TEST_PREVIOUS" || exit 1
  printf changed > "$SCRIPT_TEST_CHANGED"
  exit 0
fi
exec "$SCRIPT_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue)
	if err == nil || collected {
		t.Fatalf("remote rewrite authorized workspace removal: collected=%v, error=%v", collected, err)
	}
	if _, err := os.Stat(filepath.Join(bin, "changed")); err != nil {
		t.Fatalf("fixture did not rewrite the advertised branch: %v", err)
	}
	if got := gitRun(t, repository, "rev-parse", "HEAD"); got != published {
		t.Fatalf("preserved HEAD = %s, want %s", got, published)
	}
	if _, err := os.Stat(filepath.Join(path, "web", "tracked.txt")); err != nil {
		t.Fatalf("sibling project was removed: %v", err)
	}
	if refs := gitRun(t, repository, "for-each-ref", "--format=%(refname)", "refs/multica/script-fetch/"); refs != "" {
		t.Fatalf("temporary fetch refs leaked: %s", refs)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatalf("skipped workspace lost readiness: %v", err)
	}
}

func TestLocalScriptQuarantineFailureRetainsExecutionLock(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions under an unprivileged user")
	}
	params := scriptRecoveryFixture(t)
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := ClaimLocalScriptWorkspace(params)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(filepath.Dir(path), localScriptStateDirName)
	t.Cleanup(func() {
		_ = os.Chmod(state, 0o700)
		scriptQuarantine.Lock()
		defer scriptQuarantine.Unlock()
		if lock := scriptQuarantine.locks[path]; lock != nil {
			_ = lock.Close()
		}
		delete(scriptQuarantine.locks, path)
		delete(scriptQuarantine.blocked, path)
	})
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := blockLocalScriptWorkspace(params); err == nil {
		t.Fatal("read-only metadata unexpectedly accepted a quarantine marker")
	}
	release()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// A separate file description models another daemon with no access to
	// this process's in-memory quarantine map.
	lock, err := openLockFile(filepath.Join(state, filepath.Base(path)+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil || locked {
		t.Fatalf("execution lock released after quarantine failure: locked=%v error=%v", locked, err)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil {
		t.Fatal("preparation ignored in-memory quarantine")
	}
}

func TestLocalScriptQuarantineBlocksExecutionAndCollection(t *testing.T) {
	params := scriptRecoveryFixture(t)
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := blockLocalScriptWorkspace(params); err != nil {
		t.Fatal(err)
	}
	if release, err := ClaimLocalScriptWorkspace(params); err == nil {
		release()
		t.Fatal("quarantined workspace allowed execution claim")
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil {
		t.Fatal("quarantined workspace allowed preparation")
	}
	if collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue); err == nil || collected {
		t.Fatalf("quarantined workspace collected: %v, %v", collected, err)
	}
}

func TestLocalScriptGCProtectsRunningAndReopenedIssues(t *testing.T) {
	params := scriptRecoveryFixture(t)
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := ClaimLocalScriptWorkspace(params)
	if err != nil {
		t.Fatal(err)
	}
	collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue)
	release()
	if err == nil || collected {
		t.Fatal("GC entered a running script directory")
	}
	collected, err = CollectLocalScriptWorkspace(context.Background(), params, func(context.Context) (bool, error) {
		return false, nil
	})
	if err != nil || collected {
		t.Fatalf("reopened issue collected: %v, %v", collected, err)
	}
	if _, err := os.Stat(filepath.Join(path, "api", "tracked.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestLocalScriptGCUnregistersLinkedWorktreesAndKeepsBranches(t *testing.T) {
	params := scriptFixture(t, `git -C "$PWD/source-repo" worktree add --detach "$1/api" main
`)
	params.IssueID = "linked-issue"
	source := newTestRepo(t)
	sourceRepo := filepath.Join(params.LocalPath, "source-repo")
	if err := os.CopyFS(sourceRepo, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote")
	gitRun(t, sourceRepo, "clone", "--bare", sourceRepo, remote)
	gitRun(t, sourceRepo, "remote", "add", "origin", remote)
	path, err := prepareLocalScript(params, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(path, "api"), "switch", "-c", "feature")
	gitRun(t, filepath.Join(path, "api"), "push", "-u", "origin", "feature")
	gitRun(t, filepath.Join(path, "api"), "worktree", "lock", filepath.Join(path, "api"))
	if collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue); err == nil || collected {
		t.Fatalf("locked Git worktree collected: %v, %v", collected, err)
	}
	gitRun(t, sourceRepo, "worktree", "unlock", filepath.Join(path, "api"))
	if collected, err := CollectLocalScriptWorkspace(context.Background(), params, completedScriptIssue); err != nil || !collected {
		t.Fatalf("linked worktree collect = %v, %v", collected, err)
	}
	if list := gitRun(t, sourceRepo, "worktree", "list", "--porcelain"); strings.Contains(list, path) {
		t.Fatalf("stale worktree registration: %s", list)
	}
	gitRun(t, sourceRepo, "show-ref", "--verify", "refs/heads/feature")
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, filepath.Join(path, "api"), "branch", "--show-current"); got != "feature" {
		t.Fatalf("linked branch not restored: %s", got)
	}
	if got := gitRun(t, sourceRepo, "branch", "--show-current"); got != "main" {
		t.Fatalf("source branch was changed: %s", got)
	}
}
