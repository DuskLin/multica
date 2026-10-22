package execenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

const workdirRecoveryDir = ".workdir-recovery"

// RemoveManagedWorkDirectory removes a daemon-owned directory only after every
// repository is remotely recoverable. The caller reserves the root in its daemon
// and supplies a final ownership check. The OS lock excludes other processes.
// A branch manifest is saved outside the removed directory for manual recovery.
func RemoveManagedWorkDirectory(ctx context.Context, root, directory string, validate func() error) (resultErr error) {
	if err := validate(); err != nil {
		return err
	}
	if err := requireScriptRealDirectory(directory); err != nil {
		return err
	}
	// Git reports canonical paths (not macOS's /var or /tmp aliases).
	// Validate ownership before resolving and never accept a linked task root.
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	directory = canonical
	lockPath := filepath.Join(directory, envRootLockFile)
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return errors.New("execution lock is not a regular file")
	}
	lock, err := openLockFile(lockPath)
	if err != nil {
		return err
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil || !locked {
		lock.Close()
		return errors.New("directory is in use by another execution")
	}
	defer func() {
		if errors.Is(resultErr, processtree.ErrCleanupUnconfirmed) {
			// Retain the OS lock if process termination cannot be proved. Removing the
			// ownership record also makes future startup reject the preserved content.
			_ = os.Remove(filepath.Join(directory, envRootOwnerFile))
			scriptQuarantine.Lock()
			scriptQuarantine.blocked[directory] = true
			scriptQuarantine.Unlock()
		}
		releaseLocalScriptLock(directory, lock)
	}()
	if err := validate(); err != nil {
		return err
	}
	original, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	ReportWorkdirProgress(ctx, "checking", directory, 0)
	paths, err := scriptRepositoryPaths(directory)
	if err != nil {
		return err
	}
	if err := managedFilesCovered(directory, paths); err != nil {
		return err
	}
	repositories := make([]scriptGCRepository, 0, len(paths))
	recovery := make([]localScriptRepository, 0, len(paths))
	for _, path := range paths {
		ReportWorkdirProgress(ctx, "checking", path, 0)
		repository, err := inspectScriptGCRepository(ctx, directory, path)
		if err != nil {
			return err
		}
		repositories = append(repositories, repository)
		recovery = append(recovery, repository.Recovery)
	}
	current, err := os.Lstat(directory)
	if err != nil || !os.SameFile(original, current) {
		return errors.New("directory changed during inspection")
	}
	currentPaths, err := scriptRepositoryPaths(directory)
	if err != nil || !slices.Equal(paths, currentPaths) {
		return errors.New("repository set changed during inspection")
	}
	if err := managedFilesCovered(directory, paths); err != nil {
		return err
	}
	if err := recheckRemoteRepositories(ctx, directory, repositories); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	owner, err := ReadEnvRootOwner(directory)
	if err != nil {
		return err
	}
	record := struct {
		Path         string                  `json:"path"`
		Owner        *EnvRootOwner           `json:"owner"`
		Repositories []localScriptRepository `json:"repositories"`
	}{directory, owner, recovery}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	state := filepath.Join(root, workdirRecoveryDir)
	if err := ensureScriptRealDirectory(state); err != nil {
		return err
	}
	ReportWorkdirProgress(ctx, "saving", directory, 0)
	if err := writeScriptRecord(filepath.Join(state, scriptSourceName([]byte(directory))), data); err != nil {
		return err
	}
	// Invalidate ownership before partial deletion; initialization refuses a
	// nonempty directory without its matching owner rather than overwriting it.
	if err := os.Remove(filepath.Join(directory, envRootOwnerFile)); err != nil {
		return err
	}
	if err := syncScriptMetadataDir(directory); err != nil {
		return err
	}
	slices.Reverse(repositories)
	for _, repository := range repositories {
		if repository.Linked {
			ReportWorkdirProgress(ctx, "deleting", repository.Path, 0)
			if _, err := scriptGit(ctx, repository.Path, "--git-dir="+repository.CommonDir, "worktree", "remove", "--force", "--", repository.Path); err != nil {
				return err
			}
		}
	}
	return RemoveWorkdirWithProgress(ctx, directory)
}

// managedFilesCovered permits only known control files, logs and regenerable
// caches outside repositories. Arbitrary files (including output artifacts)
// remain protected even when every repository is clean.
func managedFilesCovered(directory string, repositories []string) error {
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(repositories, path) {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if relative == "logs" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("cannot verify linked content outside repositories: %s", relative)
		}
		if slices.Contains(ManagedReclaimableArtifactSubpaths(), relative) && entry.IsDir() {
			return filepath.SkipDir
		}
		if isReclaimableFinderMetadata(entry) {
			return nil
		}
		// Only the regenerable cache fingerprint is disposable, not Codex sessions.
		if entry.Type().IsRegular() && relative == filepath.Join(codexHomeDirName, codexModelsCacheBindingFile) {
			return nil
		}
		if !entry.IsDir() && !slices.Contains([]string{envRootOwnerFile, envRootLockFile, gcMetaFile, managedEnvProvenanceFile, sidecarManifestFile}, relative) {
			return fmt.Errorf("file outside remote-backed repositories must be preserved: %s", relative)
		}
		return nil
	})
}
