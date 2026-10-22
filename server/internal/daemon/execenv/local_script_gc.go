package execenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

// scriptGCRepository retains the administrative paths used to remove a linked
// worktree without deleting its shared repository or its durable local branches.
type scriptGCRepository struct {
	Recovery  localScriptRepository
	Path      string
	CommonDir string
	Linked    bool
	Head      string
	Refs      string
	GitDir    string
}

// CollectLocalScriptWorkspace reclaims one fully remote-backed issue directory.
// It holds the execution lock throughout validation/removal and rechecks the
// server's done state immediately before publishing the recovery record.
// Any uncertain repository or failed check keeps the entire directory intact.
func CollectLocalScriptWorkspace(ctx context.Context, params LocalScriptParams, stillDone func(context.Context) (bool, error)) (collected bool, resultErr error) {
	if stillDone == nil {
		return false, errors.New("script removal requires an eligibility check")
	}
	release, err := ClaimLocalScriptWorkspace(params)
	if err != nil {
		return false, err
	}
	defer func() {
		if errors.Is(resultErr, processtree.ErrCleanupUnconfirmed) {
			resultErr = errors.Join(resultErr, blockLocalScriptWorkspace(params))
		}
		release()
	}()
	workDir, err := params.WorkspacePath()
	if err != nil {
		return false, err
	}
	if err := requireScriptRealDirectory(workDir); err != nil {
		return false, err
	}
	original, err := os.Lstat(workDir)
	if err != nil {
		return false, err
	}
	owner, err := params.owner()
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(owner)
	if err != nil {
		return false, err
	}
	readyPath := filepath.Join(filepath.Dir(workDir), localScriptStateDirName, filepath.Base(workDir)+".json")
	ready, err := readScriptRecord(readyPath)
	if err != nil || !bytes.Equal(ready, expected) {
		return false, errors.New("script GC requires matching, successful initialization ownership")
	}
	ReportWorkdirProgress(ctx, "checking", workDir, 0)
	paths, err := scriptRepositoryPaths(workDir)
	if err != nil {
		return false, err
	}
	if len(paths) == 0 {
		return false, errors.New("script GC cannot verify remote recovery for a directory without Git repositories")
	}
	if err := scriptFilesCoveredByRepositories(workDir, paths); err != nil {
		return false, err
	}
	repositories := make([]scriptGCRepository, 0, len(paths))
	recovery := localScriptRecovery{Owner: owner}
	for _, path := range paths {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		ReportWorkdirProgress(ctx, "checking", path, 0)
		repository, err := inspectScriptGCRepository(ctx, workDir, path)
		if err != nil {
			return false, err
		}
		repositories = append(repositories, repository)
		recovery.Repositories = append(recovery.Repositories, repository.Recovery)
	}
	done, err := stillDone(ctx)
	if err != nil || !done {
		return false, err
	}
	// The initializer/agent lock prevents Multica mutations; also notice a
	// directory replacement or late human edit before committing to removal.
	current, err := os.Lstat(workDir)
	if err != nil || !os.SameFile(original, current) {
		return false, errors.New("script workspace changed during GC inspection")
	}
	currentPaths, err := scriptRepositoryPaths(workDir)
	if err != nil || !slices.Equal(paths, currentPaths) {
		return false, errors.New("script repository set changed during GC inspection")
	}
	if err := scriptFilesCoveredByRepositories(workDir, paths); err != nil {
		return false, err
	}
	if err := recheckRemoteRepositories(ctx, workDir, repositories); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	data, err := json.Marshal(recovery)
	if err != nil {
		return false, err
	}
	ReportWorkdirProgress(ctx, "saving", workDir, 0)
	if err := writeScriptRecord(scriptRecoveryPath(workDir), data); err != nil {
		return false, err
	}
	// Invalidate readiness before removing any files. If removal is interrupted,
	// the next run must stop on the preserved partial directory, never execute
	// the agent in it or rerun a destructive initializer over it.
	if err := os.Remove(readyPath); err != nil {
		return false, err
	}
	if err := syncScriptMetadataDir(filepath.Dir(readyPath)); err != nil {
		return false, err
	}
	// Nested worktrees must unregister before their enclosing directory goes.
	slices.Reverse(repositories)
	for _, repository := range repositories {
		if !repository.Linked {
			continue
		}
		ReportWorkdirProgress(ctx, "deleting", repository.Path, 0)
		if _, err := scriptGit(ctx, repository.Path, "--git-dir="+repository.CommonDir,
			"worktree", "remove", "--force", "--", repository.Path); err != nil {
			return false, fmt.Errorf("script GC removal incomplete; inspect preserved directory %s: %w", workDir, err)
		}
	}
	if err := RemoveWorkdirWithProgress(ctx, workDir); err != nil {
		return false, fmt.Errorf("script GC removal incomplete; inspect preserved directory %s: %w", workDir, err)
	}
	return true, nil
}

// scriptRepositoryPaths walks real directories and finds repository roots at
// any depth, including multiple linked worktrees. Git administrative trees and
// symlink targets are never traversed.
func scriptRepositoryPaths(workDir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(workDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() != ".git" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("script GC refuses linked Git metadata: %s", path)
		}
		paths = append(paths, filepath.Dir(path))
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return paths, err
}

// inspectScriptGCRepository proves that the current branch and all locally
// owned history survive removal. Live fetches avoid trusting stale origin refs.
func inspectScriptGCRepository(ctx context.Context, workDir, path string) (scriptGCRepository, error) {
	var repository scriptGCRepository
	if err := requireScriptRepositoryRoot(ctx, path); err != nil {
		return repository, err
	}
	relative, err := filepath.Rel(workDir, path)
	if err != nil {
		return repository, err
	}
	if err := scriptRepositoryClean(ctx, path); err != nil {
		return repository, err
	}
	refs, err := scriptLocalRefs(ctx, path)
	if err != nil {
		return repository, err
	}
	branch, err := scriptGit(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return repository, errors.Join(fmt.Errorf("script GC skipped %s: detached or unborn branch", relative), err)
	}
	remote, remoteBranch, err := scriptBranchRemote(ctx, path, branch)
	if err != nil {
		return repository, fmt.Errorf("script GC skipped %s: %w", relative, err)
	}
	if err := scriptRemoteOutsideWorkspace(ctx, workDir, path, remote); err != nil {
		return repository, err
	}
	remoteCommit, err := fetchScriptRemoteBranch(ctx, path, remote, remoteBranch)
	if err != nil {
		return repository, fmt.Errorf("script GC skipped %s: remote branch could not be verified: %w", relative, err)
	}
	head, err := scriptGit(ctx, path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return repository, err
	}
	if _, err := scriptGit(ctx, path, "merge-base", "--is-ancestor", head, remoteCommit); err != nil {
		return repository, fmt.Errorf("script GC skipped %s: branch contains commits absent from its remote: %w", relative, err)
	}
	common, err := scriptGit(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repository, err
	}
	gitDir, err := scriptGit(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return repository, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return repository, err
	}
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		return repository, err
	}
	if err := scriptWorktreeRemovable(ctx, path, common, workDir); err != nil {
		return repository, err
	}
	if relativeCommon, err := filepath.Rel(workDir, common); err == nil && filepath.IsLocal(relativeCommon) {
		// A clone's object database will disappear too. Verify every local
		// branch separately and preserve stashes instead of losing hidden work.
		if stash, err := scriptGit(ctx, path, "stash", "list"); err != nil || stash != "" {
			return repository, errors.Join(fmt.Errorf("script GC skipped %s: stash exists or cannot be inspected", relative), err)
		}
		branches, err := scriptGit(ctx, path, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
		if err != nil {
			return repository, err
		}
		for _, other := range strings.Split(branches, "\n") {
			if other == "" || other == branch {
				continue
			}
			otherRemote, otherRemoteBranch, err := scriptBranchRemote(ctx, path, other)
			if err != nil {
				return repository, fmt.Errorf("script GC skipped %s: local branch %s has no verified remote: %w", relative, other, err)
			}
			if err := scriptRemoteOutsideWorkspace(ctx, workDir, path, otherRemote); err != nil {
				return repository, err
			}
			otherCommit, err := fetchScriptRemoteBranch(ctx, path, otherRemote, otherRemoteBranch)
			if err != nil {
				return repository, err
			}
			if _, err := scriptGit(ctx, path, "merge-base", "--is-ancestor", "refs/heads/"+other, otherCommit); err != nil {
				return repository, fmt.Errorf("script GC skipped %s: unpushed local branch %s: %w", relative, other, err)
			}
		}
		if err := scriptOtherRefsPublished(ctx, workDir, path); err != nil {
			return repository, err
		}
	}
	remoteHash, err := scriptRemoteURLHash(ctx, path, remote)
	if err != nil {
		return repository, err
	}
	repository = scriptGCRepository{
		Path: path, CommonDir: common, Linked: common != gitDir, Head: head, Refs: refs, GitDir: gitDir,
		Recovery: localScriptRepository{Path: relative, Branch: branch, Remote: remote, RemoteBranch: remoteBranch, RemoteURLHash: remoteHash},
	}
	return repository, nil
}

// scriptRepositoryClean includes untracked files, while ignored dependencies
// and generated artifacts are rebuilt by the initializer after reclamation.
func scriptRepositoryClean(ctx context.Context, path string) error {
	index, err := scriptGit(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(index, "\x00") {
		if entry != "" && (entry[0] == 'S' || (entry[0] >= 'a' && entry[0] <= 'z')) {
			return fmt.Errorf("script GC skipped %s: index hides files with assume-unchanged or skip-worktree", path)
		}
	}
	status, err := scriptGit(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return errors.Join(fmt.Errorf("script GC skipped %s: uncommitted/untracked files or unreadable status", path), err)
	}
	return nil
}

// scriptFilesCoveredByRepositories prevents a Git subproject from authorizing
// deletion of an adjacent non-Git project or an untracked container-level file.
func scriptFilesCoveredByRepositories(workDir string, repositories []string) error {
	return filepath.WalkDir(workDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(repositories, path) {
			return filepath.SkipDir
		}
		if isReclaimableFinderMetadata(entry) {
			return nil
		}
		if !entry.IsDir() {
			return fmt.Errorf("script GC skipped file outside verified repositories: %s", path)
		}
		return nil
	})
}

// scriptLocalRefs snapshots all non-remote refs, including stashes and tags.
// Remote-tracking refs are omitted because live validation refreshes them.
func scriptLocalRefs(ctx context.Context, path string) (string, error) {
	output, err := scriptGit(ctx, path, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return "", err
	}
	var local []string
	for _, line := range strings.Split(output, "\n") {
		if line != "" && !strings.HasPrefix(line, "refs/remotes/") {
			local = append(local, line)
		}
	}
	return strings.Join(local, "\n"), nil
}

// scriptOtherRefsPublished keeps unique tagged commits and unknown local refs.
// Tags must be advertised with the same object ID by an accessible remote.
func scriptOtherRefsPublished(ctx context.Context, workDir, path string) error {
	refs, err := scriptLocalRefs(ctx, path)
	if err != nil {
		return err
	}
	tags := make(map[string]string)
	for _, line := range strings.Split(refs, "\n") {
		ref, object, _ := strings.Cut(line, " ")
		switch {
		case ref == "", strings.HasPrefix(ref, "refs/heads/"):
		case strings.HasPrefix(ref, "refs/tags/"):
			tags[ref] = object
		default:
			return fmt.Errorf("script GC skipped repository with unverified local ref %s", ref)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	remotes, err := scriptGit(ctx, path, "remote")
	if err != nil {
		return err
	}
	for _, remote := range strings.Split(remotes, "\n") {
		if remote == "" {
			continue
		}
		if err := scriptRemoteOutsideWorkspace(ctx, workDir, path, remote); err != nil {
			return err
		}
		output, err := scriptGit(ctx, path, "ls-remote", "--tags", "--refs", "--", remote)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && tags[fields[1]] == fields[0] {
				delete(tags, fields[1])
			}
		}
	}
	if len(tags) != 0 {
		return errors.New("script GC skipped repository with unpublished local tags")
	}
	return nil
}

// scriptRemoteOutsideWorkspace rejects local remotes whose storage would be
// deleted along with the workspace, including file URLs and shared clones.
func scriptRemoteOutsideWorkspace(ctx context.Context, workDir, path, remote string) error {
	address, err := scriptGit(ctx, path, "remote", "get-url", "--", remote)
	if err != nil {
		return err
	}
	remotePath := address
	if strings.HasPrefix(address, "file:") {
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Host != "" && parsed.Host != "localhost") {
			return errors.New("script GC cannot validate local remote URL")
		}
		remotePath = parsed.Path
	} else if !filepath.IsAbs(address) && strings.Contains(address, ":") {
		return nil // Network transport (including scp-style SSH).
	}
	if strings.HasPrefix(remotePath, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		remotePath = filepath.Join(userHome, strings.TrimPrefix(remotePath, "~/"))
	}
	if !filepath.IsAbs(remotePath) {
		remotePath = filepath.Join(path, remotePath)
	}
	common, err := scriptGit(ctx, remotePath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("script GC cannot validate local remote repository: %w", err)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(workDir, common)
	if err != nil || relative == "." || filepath.IsLocal(relative) {
		return errors.New("script GC remote repository is inside the directory being reclaimed")
	}
	objects, err := filepath.EvalSymlinks(filepath.Join(common, "objects"))
	if err != nil {
		return err
	}
	relativeObjects, err := filepath.Rel(workDir, objects)
	if err != nil || relativeObjects == "." || filepath.IsLocal(relativeObjects) {
		return errors.New("script GC remote object storage is inside the directory being reclaimed")
	}
	if alternate, err := os.ReadFile(filepath.Join(common, "objects", "info", "alternates")); err == nil && len(bytes.TrimSpace(alternate)) > 0 {
		return errors.New("script GC cannot prove shared local remote object storage is independent")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// scriptBranchRemote prefers the configured upstream. Without one, exactly one
// remote must advertise a branch with the same name; ambiguity preserves data.
func scriptBranchRemote(ctx context.Context, path, branch string) (string, string, error) {
	upstream, err := scriptGit(ctx, path, "for-each-ref", "--format=%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/"+branch)
	if err != nil {
		return "", "", err
	}
	remote, ref, hasSeparator := strings.Cut(upstream, "\x00")
	if hasSeparator && remote != "" {
		if remote == "." || strings.HasPrefix(remote, "-") || !strings.HasPrefix(ref, "refs/heads/") {
			return "", "", errors.New("branch upstream is not a remote branch")
		}
		return remote, strings.TrimPrefix(ref, "refs/heads/"), nil
	}
	remotes, err := scriptGit(ctx, path, "remote")
	if err != nil {
		return "", "", err
	}
	var match string
	for _, name := range strings.Split(remotes, "\n") {
		if name == "" || name == "." || strings.HasPrefix(name, "-") {
			continue
		}
		output, err := scriptGit(ctx, path, "ls-remote", "--heads", "--", name, "refs/heads/"+branch)
		if err != nil {
			return "", "", fmt.Errorf("remote branch lookup failed: %w", err)
		}
		if output == "" {
			continue
		}
		if match != "" {
			return "", "", errors.New("branch exists on multiple remotes without an upstream")
		}
		match = name
	}
	if match == "" {
		return "", "", errors.New("branch has no corresponding remote branch")
	}
	return match, branch, nil
}

// scriptWorktreeRemovable rejects explicitly locked trees and clones whose
// object database is still needed by another worktree.
func scriptWorktreeRemovable(ctx context.Context, path, common, workDir string) error {
	output, err := scriptGit(ctx, path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	currentPath := ""
	commonRelative, err := filepath.Rel(workDir, common)
	ownsCommon := err == nil && filepath.IsLocal(commonRelative)
	for _, field := range strings.Split(output, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			currentPath = strings.TrimPrefix(field, "worktree ")
			canonical, err := filepath.EvalSymlinks(currentPath)
			if err == nil {
				currentPath = canonical
			}
			if ownsCommon && currentPath != path {
				return errors.New("script GC skipped repository with additional worktrees")
			}
		}
		if currentPath == path && (field == "locked" || strings.HasPrefix(field, "locked ")) {
			return errors.New("script GC skipped locked Git worktree")
		}
	}
	return nil
}

// recheckRemoteRepositories rejects edits or new worktree dependencies after inspection.
func recheckRemoteRepositories(ctx context.Context, workDir string, repositories []scriptGCRepository) error {
	for _, repository := range repositories {
		if err := requireScriptRepositoryRoot(ctx, repository.Path); err != nil {
			return err
		}
		if err := scriptWorktreeRemovable(ctx, repository.Path, repository.CommonDir, workDir); err != nil {
			return err
		}
		if err := scriptRepositoryClean(ctx, repository.Path); err != nil {
			return err
		}
		head, err := scriptGit(ctx, repository.Path, "rev-parse", "--verify", "HEAD")
		branch, branchErr := scriptGit(ctx, repository.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil || branchErr != nil || head != repository.Head || branch != repository.Recovery.Branch {
			return errors.Join(errors.New("script repository changed during GC inspection"), err, branchErr)
		}
		refs, err := scriptLocalRefs(ctx, repository.Path)
		gitDir, gitDirErr := scriptGit(ctx, repository.Path, "rev-parse", "--absolute-git-dir")
		if err != nil || gitDirErr != nil || refs != repository.Refs || gitDir != repository.GitDir {
			return errors.Join(errors.New("script repository refs or metadata changed during GC inspection"), err, gitDirErr)
		}
	}
	return ctx.Err()
}
