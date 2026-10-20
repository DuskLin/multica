package execenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// scriptRecoveryFixture creates two local remotes and an initializer that
// clones them. No test consults network credentials or a user-installed agent.
func scriptRecoveryFixture(t *testing.T) LocalScriptParams {
	t.Helper()
	params := scriptFixture(t, `git clone "$PWD/api-remote" "$1/api"
git clone "$PWD/web-remote" "$1/web"
printf called >> "$PWD/calls.txt"
`)
	params.IssueID = "recoverable-issue"
	for _, name := range []string{"api", "web"} {
		source := newTestRepo(t)
		gitRun(t, source, "switch", "-c", "feature/"+name)
		writeFile(t, filepath.Join(source, "feature.txt"), name+" result\n")
		gitRun(t, source, "add", "feature.txt")
		gitRun(t, source, "commit", "-m", "feature result")
		gitRun(t, source, "switch", "main")
		gitRun(t, source, "clone", "--bare", source, filepath.Join(params.LocalPath, name+"-remote"))
	}
	return params
}

// writeTestScriptRecovery records remote branch identities without retaining
// any project files, just as the collection phase does before deletion.
func writeTestScriptRecovery(t *testing.T, params LocalScriptParams, repositories []localScriptRepository) string {
	t.Helper()
	path, err := params.WorkspacePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localScriptStateDir(path); err != nil {
		t.Fatal(err)
	}
	owner, err := params.owner()
	if err != nil {
		t.Fatal(err)
	}
	for index := range repositories {
		digest := sha256.Sum256([]byte(filepath.Join(filepath.Dir(filepath.Dir(path)), repositories[index].Path+"-remote")))
		repositories[index].RemoteURLHash = hex.EncodeToString(digest[:])
	}
	data, err := json.Marshal(localScriptRecovery{Owner: owner, Repositories: repositories})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeScriptRecord(scriptRecoveryPath(path), data); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalScriptRestoresEachRemoteBranch(t *testing.T) {
	params := scriptRecoveryFixture(t)
	path := writeTestScriptRecovery(t, params, []localScriptRepository{
		{Path: "api", Branch: "local-api", Remote: "origin", RemoteBranch: "feature/api"},
		{Path: "web", Branch: "feature/web", Remote: "origin", RemoteBranch: "feature/web"},
	})
	restored, err := prepareLocalScript(params, t.TempDir())
	if err != nil || restored != path {
		t.Fatalf("restore = %s, %v", restored, err)
	}
	for _, tc := range []struct{ name, branch string }{{"api", "local-api"}, {"web", "feature/web"}} {
		repo := filepath.Join(restored, tc.name)
		if got := gitRun(t, repo, "branch", "--show-current"); got != tc.branch {
			t.Fatalf("%s branch = %s, want %s", tc.name, got, tc.branch)
		}
		if got := readFile(t, filepath.Join(repo, "feature.txt")); got != tc.name+" result\n" {
			t.Fatalf("%s lost its remote result: %s", tc.name, got)
		}
		if got := gitRun(t, repo, "config", "branch."+tc.branch+".merge"); got != "refs/heads/feature/"+tc.name {
			t.Fatalf("%s lost its upstream: %s", tc.name, got)
		}
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if calls := readFile(t, filepath.Join(params.LocalPath, "calls.txt")); calls != "called" {
		t.Fatalf("followup reran initialization: %s", calls)
	}
}

func TestLocalScriptRecoveryFailurePreservesIncompleteWorkspace(t *testing.T) {
	params := scriptRecoveryFixture(t)
	path := writeTestScriptRecovery(t, params, []localScriptRepository{
		{Path: "api", Branch: "missing", Remote: "origin", RemoteBranch: "deleted"},
	})
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil || !strings.Contains(err.Error(), "branch recovery failed") {
		t.Fatalf("missing remote branch accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "api", "tracked.txt")); err != nil {
		t.Fatalf("recovery failure removed initialized files: %v", err)
	}
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("failed recovery silently reinitialized: %v", err)
	}
	if _, err := os.Stat(scriptRecoveryPath(path)); err != nil {
		t.Fatalf("recovery record lost: %v", err)
	}
}

func TestLocalScriptRecoveryRejectsOutsideRepositories(t *testing.T) {
	params := scriptRecoveryFixture(t)
	writeTestScriptRecovery(t, params, []localScriptRepository{
		{Path: "../outside", Branch: "main", Remote: "origin", RemoteBranch: "main"},
	})
	if _, err := prepareLocalScript(params, t.TempDir()); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestLocalScriptSourceRegistryFindsExistingOwnedIssues(t *testing.T) {
	params := scriptFixture(t, "printf ready > \"$1/result.txt\"\n")
	params.IssueID = "old-issue"
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	params.IssueID = "new-issue"
	if err := RegisterLocalScriptSource(root, params); err != nil {
		t.Fatal(err)
	}
	if err := RegisterLocalScriptSource(root, params); err != nil {
		t.Fatal(err)
	}
	sources, err := ListLocalScriptSources(root)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %+v, %v", sources, err)
	}
	issues, err := ListLocalScriptIssues(sources[0])
	if err != nil || len(issues) != 1 || issues[0].IssueID != "old-issue" {
		t.Fatalf("existing issue discovery = %+v, %v", issues, err)
	}
	sources[0].WorkspaceID = "another-workspace"
	issues, err = ListLocalScriptIssues(sources[0])
	if err != nil || len(issues) != 0 {
		t.Fatalf("cross-workspace issue adopted: %+v, %v", issues, err)
	}
}

func TestLocalScriptRecoverySupportsSingleBranchClones(t *testing.T) {
	params := scriptRecoveryFixture(t)
	if err := os.WriteFile(filepath.Join(params.LocalPath, params.InitScript),
		[]byte("git clone --single-branch --branch main \"$PWD/api-remote\" \"$1/api\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeTestScriptRecovery(t, params, []localScriptRepository{
		{Path: "api", Branch: "feature/api", Remote: "origin", RemoteBranch: "feature/api"},
	})
	if _, err := prepareLocalScript(params, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if upstream := gitRun(t, filepath.Join(path, "api"), "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/feature/api" {
		t.Fatalf("single-branch clone has no usable upstream: %s", upstream)
	}
}

func TestLocalScriptRecoveryRejectsAliasedGitMetadata(t *testing.T) {
	for _, kind := range []string{"symlink", "gitfile"} {
		t.Run(kind, func(t *testing.T) {
			source := newTestRepo(t)
			alias := t.TempDir()
			alias, err := filepath.EvalSymlinks(alias)
			if err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(alias, ".git")
			if kind == "symlink" {
				if err := os.Symlink(filepath.Join(source, ".git"), metadata); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, metadata, "gitdir: "+filepath.Join(source, ".git")+"\n")
			}
			if err := requireScriptRepositoryRoot(context.Background(), alias); err == nil {
				t.Fatal("aliased Git metadata accepted")
			}
			if branch := gitRun(t, source, "branch", "--show-current"); branch != "main" {
				t.Fatalf("source branch changed: %s", branch)
			}
		})
	}
}

func TestLocalScriptGitTimeoutStopsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "late")
	t.Setenv("SCRIPT_TEST_MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n(sleep 0.6; printf late > \"$SCRIPT_TEST_MARKER\") &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := scriptGit(ctx, t.TempDir(), "status"); err == nil {
		t.Fatal("Git timeout did not fail")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git descendant survived timeout: %v", err)
	}
}

func TestLocalScriptRecoveryHelperStopsGitChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	for _, outcome := range []string{"cancel", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			params := scriptRecoveryFixture(t)
			writeTestScriptRecovery(t, params, []localScriptRepository{
				{Path: "api", Branch: "feature/api", Remote: "origin", RemoteBranch: "feature/api"},
			})
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			started := filepath.Join(bin, "started")
			late := filepath.Join(bin, "late")
			body := `#!/bin/sh
case "$*" in
  *check-ref-format*)
    printf started > "$SCRIPT_TEST_STARTED"
    (sleep 0.8; printf late > "$SCRIPT_TEST_MARKER") >/dev/null 2>&1 &
    if [ "$SCRIPT_TEST_OUTCOME" = failure ]; then exit 1; fi
    wait
    ;;
  *) exec "$SCRIPT_TEST_REAL_GIT" "$@" ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			command := preparationHelperTestCommand(
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SCRIPT_TEST_STARTED="+started, "SCRIPT_TEST_MARKER="+late,
				"SCRIPT_TEST_OUTCOME="+outcome, "SCRIPT_TEST_REAL_GIT="+realGit,
			)
			go func() {
				_, err := PrepareIsolated(ctx, command, PrepareParams{
					WorkspacesRoot: t.TempDir(), WorkspaceID: params.WorkspaceID,
					TaskID: params.TaskID, Provider: "claude", LocalScript: &params,
					Task: TaskContextForEnv{IssueID: params.IssueID},
				}, nil)
				done <- err
			}()
			if outcome == "cancel" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(started); err == nil {
						cancel()
						break
					}
					if time.Now().After(deadline) {
						cancel()
						t.Fatal("Git child did not start")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if err := <-done; err == nil {
				t.Fatal("incomplete Git recovery succeeded")
			}
			time.Sleep(time.Second)
			if _, err := os.Stat(late); !os.IsNotExist(err) {
				t.Fatalf("Git descendant survived helper %s: %v", outcome, err)
			}
		})
	}
}
