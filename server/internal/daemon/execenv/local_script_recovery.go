package execenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

const (
	localScriptSourcesDir     = ".script-sources"
	localScriptRecoverySuffix = ".recovery.json"
	localScriptBlockedSuffix  = ".blocked"
	localScriptGitWaitDelay   = 5 * time.Second
	localScriptFetchRefPrefix = "refs/multica/script-fetch/"
)

// scriptQuarantine retains OS locks when neither quarantine nor readiness
// invalidation can be trusted on disk. Other daemon processes remain excluded;
// this process also refuses direct preparation until operator recovery/restart.
var scriptQuarantine = struct {
	sync.Mutex
	blocked map[string]bool
	locks   map[string]*os.File
}{blocked: make(map[string]bool), locks: make(map[string]*os.File)}

// LocalScriptSource identifies a source whose owned issue directories GC may
// inspect. It is registered before preparation, independently of task env GC.
type LocalScriptSource struct {
	LocalPath   string `json:"local_path"`
	WorkspaceID string `json:"workspace_id"`
}

// localScriptRepository records the remote branch needed after initialization.
// URLs are intentionally not persisted: they may contain credentials, and the
// initializer is responsible for recreating each repository's remote settings.
type localScriptRepository struct {
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	Remote        string `json:"remote"`
	RemoteBranch  string `json:"remote_branch"`
	RemoteURLHash string `json:"remote_url_hash"`
}

// localScriptRecovery survives removal of the work directory. It contains only
// branch identities, not copies of the files that GC is intended to reclaim.
type localScriptRecovery struct {
	Owner        localScriptOwner        `json:"owner"`
	Repositories []localScriptRepository `json:"repositories"`
}

// RegisterLocalScriptSource persists the canonical source/workspace pair for
// scans. Automatic GC still selects only issue directories; the settings page
// also discovers chat and standalone directories for explicit manual removal.
func RegisterLocalScriptSource(root string, params LocalScriptParams) error {
	if !filepath.IsAbs(root) {
		return errors.New("script GC registration requires an absolute workspaces root")
	}
	workDir, err := params.WorkspacePath()
	if err != nil {
		return err
	}
	source := LocalScriptSource{
		LocalPath: filepath.Dir(filepath.Dir(workDir)), WorkspaceID: params.WorkspaceID,
	}
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, localScriptSourcesDir)
	if err := ensureScriptRealDirectory(dir); err != nil {
		return err
	}
	return writeScriptRecord(filepath.Join(dir, scriptSourceName(data)), data)
}

// scriptSourceName binds a registry filename to its exact source identity.
func scriptSourceName(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) + ".json"
}

// ListLocalScriptSources reads only regular, identity-matching source records.
// Invalid entries stop this scan rather than granting deletion authority.
func ListLocalScriptSources(root string) ([]LocalScriptSource, error) {
	dir := filepath.Join(root, localScriptSourcesDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := requireScriptRealDirectory(dir); err != nil {
		return nil, err
	}
	var sources []LocalScriptSource
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readScriptRecord(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var source LocalScriptSource
		if err := json.Unmarshal(data, &source); err != nil {
			return nil, err
		}
		if scriptSourceName(data) != entry.Name() || !filepath.IsAbs(source.LocalPath) || source.WorkspaceID == "" {
			return nil, fmt.Errorf("invalid script source record: %s", entry.Name())
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// ListLocalScriptIssues discovers both existing and new stable issue directories
// under a registered source. Legacy directories without ownership are excluded.
func ListLocalScriptIssues(source LocalScriptSource) ([]LocalScriptParams, error) {
	all, err := ListLocalScriptWorkspaces(source)
	var issues []LocalScriptParams
	for _, params := range all {
		if params.IssueID != "" {
			issues = append(issues, params)
		}
	}
	return issues, err
}

// ListLocalScriptWorkspaces includes issue, chat and standalone owned directories.
func ListLocalScriptWorkspaces(source LocalScriptSource) ([]LocalScriptParams, error) {
	canonical, err := filepath.EvalSymlinks(source.LocalPath)
	if err != nil || canonical != source.LocalPath {
		return nil, errors.New("script source no longer has its registered canonical path")
	}
	parent := filepath.Join(source.LocalPath, ".worktrees")
	state := filepath.Join(parent, localScriptStateDirName)
	if _, err := os.Lstat(state); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	for _, dir := range []string{source.LocalPath, parent, state} {
		if err := requireScriptRealDirectory(dir); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		return nil, err
	}
	var issues []LocalScriptParams
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "multica-") ||
			!strings.HasSuffix(entry.Name(), ".json") ||
			strings.HasSuffix(entry.Name(), localScriptRecoverySuffix) {
			continue
		}
		data, err := readScriptRecord(filepath.Join(state, entry.Name()))
		if err != nil {
			continue
		}
		var owner localScriptOwner
		if json.Unmarshal(data, &owner) != nil ||
			owner.WorkspaceID != source.WorkspaceID || owner.ID == "" {
			continue
		}
		params := LocalScriptParams{LocalPath: source.LocalPath, WorkspaceID: source.WorkspaceID}
		switch owner.Kind {
		case "issue":
			params.IssueID = owner.ID
		case "chat":
			params.ChatSessionID = owner.ID
		case "run":
			params.TaskID = owner.ID
		default:
			continue
		}
		path, err := params.WorkspacePath()
		if err != nil || filepath.Base(path)+".json" != entry.Name() {
			continue
		}
		if err := requireScriptRealDirectory(path); err != nil {
			continue
		}
		issues = append(issues, params)
	}
	return issues, nil
}

// restoreLocalScriptBranches switches freshly initialized repositories back to
// their recorded remote branches. Any mismatch leaves initialization incomplete.
func restoreLocalScriptBranches(workDir string, owner localScriptOwner) error {
	recordPath := scriptRecoveryPath(workDir)
	data, err := readScriptRecord(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var recovery localScriptRecovery
	if err := json.Unmarshal(data, &recovery); err != nil {
		return fmt.Errorf("read script recovery record: %w", err)
	}
	if recovery.Owner != owner {
		return errors.New("script recovery ownership does not match this issue")
	}
	for _, repo := range recovery.Repositories {
		path, err := scriptRepositoryPath(workDir, repo.Path)
		if err != nil {
			return err
		}
		for _, branch := range []string{repo.Branch, repo.RemoteBranch} {
			if _, err := scriptGit(nil, path, "check-ref-format", "refs/heads/"+branch); err != nil {
				return fmt.Errorf("invalid recovery branch for %s", repo.Path)
			}
		}
		if repo.Remote == "" || strings.HasPrefix(repo.Remote, "-") || repo.Remote == "." {
			return fmt.Errorf("invalid recovery remote for %s", repo.Path)
		}
		if err := requireScriptRepositoryRoot(nil, path); err != nil {
			return err
		}
		remoteHash, err := scriptRemoteURLHash(nil, path, repo.Remote)
		if err != nil || remoteHash != repo.RemoteURLHash {
			return fmt.Errorf("restore %s: initializer changed or removed remote %s", repo.Path, repo.Remote)
		}
		commit, err := fetchScriptRemoteBranch(nil, path, repo.Remote, repo.RemoteBranch)
		if err != nil {
			return fmt.Errorf("restore %s: remote branch %s is unavailable: %w", repo.Path, repo.RemoteBranch, err)
		}
		// A branch left behind in the shared source repository may contain
		// newer local work. Never reset it to the remote tip.
		if _, err := scriptGit(nil, path, "show-ref", "--verify", "--quiet", "refs/heads/"+repo.Branch); err == nil {
			if _, err := scriptGit(nil, path, "switch", "--", repo.Branch); err != nil {
				return fmt.Errorf("restore %s: cannot switch to %s: %w", repo.Path, repo.Branch, err)
			}
			if _, err := scriptGit(nil, path, "merge", "--ff-only", commit); err != nil {
				return fmt.Errorf("restore %s: local and remote branches diverged: %w", repo.Path, err)
			}
		} else if _, err := scriptGit(nil, path, "switch", "--no-track", "-c", repo.Branch, commit); err != nil {
			return fmt.Errorf("restore %s: cannot recreate %s: %w", repo.Path, repo.Branch, err)
		}
		if _, err := scriptGit(nil, path, "config", "branch."+repo.Branch+".remote", repo.Remote); err != nil {
			return err
		}
		if _, err := scriptGit(nil, path, "config", "branch."+repo.Branch+".merge", "refs/heads/"+repo.RemoteBranch); err != nil {
			return err
		}
		// Single-branch clones need an explicit fetch mapping for this branch.
		// Keep the initializer's mappings and append only when not represented.
		tracking := "refs/remotes/" + repo.Remote + "/" + repo.RemoteBranch
		if _, err := scriptGit(nil, path, "rev-parse", "--verify", repo.Branch+"@{upstream}"); err != nil {
			mapping := "+refs/heads/" + repo.RemoteBranch + ":" + tracking
			if _, err := scriptGit(nil, path, "config", "--add", "remote."+repo.Remote+".fetch", mapping); err != nil {
				return err
			}
		}
		if _, err := scriptGit(nil, path, "update-ref", tracking, commit); err != nil {
			return err
		}
		if _, err := scriptGit(nil, path, "rev-parse", "--verify", repo.Branch+"@{upstream}"); err != nil {
			return fmt.Errorf("restore %s: cannot establish upstream: %w", repo.Path, err)
		}
	}
	return nil
}

// scriptRemoteURLHash binds recovery to the original repository without
// writing remote URLs or embedded credentials into the recovery manifest.
func scriptRemoteURLHash(ctx context.Context, path, remote string) (string, error) {
	url, err := scriptGit(ctx, path, "remote", "get-url", "--", remote)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(url))
	return hex.EncodeToString(digest[:]), nil
}

// scriptRecoveryPath locates a branch manifest outside its reclaimable workdir.
func scriptRecoveryPath(workDir string) string {
	return filepath.Join(filepath.Dir(workDir), localScriptStateDirName, filepath.Base(workDir)+localScriptRecoverySuffix)
}

// blockLocalScriptWorkspace prevents execution and GC after unconfirmed helper
// cleanup. The marker requires manual inspection before it can be removed.
func blockLocalScriptWorkspace(params LocalScriptParams) (resultErr error) {
	workDir, err := params.WorkspacePath()
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			scriptQuarantine.Lock()
			scriptQuarantine.blocked[workDir] = true
			scriptQuarantine.Unlock()
		}
	}()
	state, err := localScriptStateDir(workDir)
	if err != nil {
		return err
	}
	blocked := filepath.Join(state, filepath.Base(workDir)+localScriptBlockedSuffix)
	if err := writeScriptRecord(blocked, []byte("Process cleanup unconfirmed; inspect remaining processes before removing this marker.\n")); err != nil {
		// Invalidating readiness also protects an existing directory when the
		// filesystem cannot allocate a quarantine marker (for example ENOSPC).
		removeErr := os.Remove(filepath.Join(state, filepath.Base(workDir)+".json"))
		return errors.Join(err, removeErr)
	}
	return nil
}

// checkLocalScriptBlocked refuses both execution and reclamation of quarantine.
func checkLocalScriptBlocked(workDir string) error {
	scriptQuarantine.Lock()
	inMemory := scriptQuarantine.blocked[workDir]
	scriptQuarantine.Unlock()
	if inMemory {
		return fmt.Errorf("script workspace quarantined in memory; process cleanup and metadata repair are required before restarting the daemon: %s", workDir)
	}
	blocked := filepath.Join(filepath.Dir(workDir), localScriptStateDirName, filepath.Base(workDir)+localScriptBlockedSuffix)
	if _, err := os.Lstat(blocked); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("script workspace blocked until remaining processes are inspected: %s", blocked)
}

// releaseLocalScriptLock keeps the actual OS lock alive if disk quarantine
// failed. Holding a strong reference prevents os.File's finalizer releasing it.
func releaseLocalScriptLock(workDir string, lock *os.File) {
	scriptQuarantine.Lock()
	defer scriptQuarantine.Unlock()
	if scriptQuarantine.blocked[workDir] {
		scriptQuarantine.locks[workDir] = lock
		return
	}
	_ = lock.Close()
}

// scriptRepositoryPath rejects traversal and symlinked repository ancestors.
func scriptRepositoryPath(workDir, relative string) (string, error) {
	if relative != "." && (!filepath.IsLocal(relative) || filepath.Clean(relative) != relative) {
		return "", fmt.Errorf("invalid script repository path: %q", relative)
	}
	path := filepath.Join(workDir, relative)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", fmt.Errorf("script repository path is missing or linked: %s", path)
	}
	return path, nil
}

// requireScriptRepositoryRoot ensures Git cannot fall through to a parent repo.
func requireScriptRepositoryRoot(ctx context.Context, path string) error {
	gitMetadata, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || gitMetadata.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("script repository has missing or symlinked Git metadata: %s", path)
	}
	top, err := scriptGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(top)
	if err != nil || canonical != path {
		return fmt.Errorf("initializer did not recreate repository %s", path)
	}
	listing, err := scriptGit(ctx, path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			registered, err := filepath.EvalSymlinks(strings.TrimPrefix(field, "worktree "))
			if err == nil && registered == path {
				return nil
			}
		}
	}
	return fmt.Errorf("script repository aliases another checkout's Git metadata: %s", path)
}

// fetchScriptRemoteBranch binds the advertisement to this fetch's private ref.
// Existing local objects cannot prove what was fetched, and sibling worktrees
// share FETCH_HEAD. A changed branch or failed ref cleanup must block deletion.
func fetchScriptRemoteBranch(ctx context.Context, path, remote, branch string) (commit string, resultErr error) {
	ReportWorkdirProgress(ctx, "remote", path, 0)
	ref := "refs/heads/" + branch
	output, err := scriptGit(ctx, path, "ls-remote", "--exit-code", "--refs", "--", remote, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != ref {
		return "", errors.New("remote did not advertise exactly the requested branch")
	}
	commit = fields[0]
	if _, err := hex.DecodeString(commit); err != nil {
		return "", errors.New("remote advertised an invalid object ID")
	}
	temporaryRef := localScriptFetchRefPrefix + rand.Text()
	defer func() {
		cleanupCtx := ctx
		if ctx != nil {
			cleanupCtx = context.WithoutCancel(ctx)
		}
		if _, err := scriptGit(cleanupCtx, path, "update-ref", "-d", temporaryRef); err != nil {
			commit = ""
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary remote verification ref: %w", err))
		}
	}()
	if _, err := scriptGit(ctx, path, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", remote, ref+":"+temporaryRef); err != nil {
		return "", err
	}
	fetched, err := scriptGit(ctx, path, "rev-parse", "--verify", temporaryRef+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("remote branch changed while fetching; retry on the next scan: %w", err)
	}
	if fetched != commit {
		return "", errors.New("remote branch changed while fetching; retry on the next scan")
	}
	return commit, nil
}

// scriptGit runs noninteractive Git operations and returns stdout only. A nil
// context is reserved for recovery inside PrepareIsolated: commands inherit
// its process group and five-minute budget. GC supplies a context and owns a
// separate bounded process tree. Errors omit potentially sensitive diagnostics.
func scriptGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_TERMINAL_PROMPT":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = localScriptGitWaitDelay
	var output []byte
	var err error
	if ctx == nil {
		output, err = cmd.Output()
	} else {
		bounded, cancel := context.WithTimeout(ctx, gitTimeout)
		defer cancel()
		output, err = processtree.Output(bounded, cmd, localScriptGitWaitDelay)
	}
	if err != nil {
		return "", fmt.Errorf("script workspace git operation failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// readScriptRecord refuses symlinks and special files before reading metadata.
func readScriptRecord(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("script record must be a regular file: %s", path)
	}
	return os.ReadFile(path)
}

// ensureScriptRealDirectory creates metadata directories without adopting links.
func ensureScriptRealDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return requireScriptRealDirectory(path)
}

// requireScriptRealDirectory rejects linked or missing directory boundaries.
func requireScriptRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("script path must be a real directory: %s", path)
	}
	return nil
}

// writeScriptRecord publishes metadata atomically so a failed write cannot
// authorize reclamation with a truncated recovery record.
func writeScriptRecord(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".script-record-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncScriptMetadataDir(filepath.Dir(path))
}
