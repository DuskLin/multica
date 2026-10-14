package execenv

import (
	"bytes"
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
)

const localScriptStateDirName = ".multica-script"

// LocalScriptParams configures a user-owned Bash initializer. The script runs
// once per workspace and issue/chat, receiving an empty destination as $1.
type LocalScriptParams struct {
	LocalPath     string
	InitScript    string
	WorkspaceID   string
	IssueID       string
	ChatSessionID string
	TaskID        string
}

// localScriptOwner binds a directory to a workspace and durable conversation.
// Agent identity is excluded so collaborating agents continue the same files.
type localScriptOwner struct {
	WorkspaceID string `json:"workspace_id"`
	Kind        string `json:"kind"`
	ID          string `json:"id"`
}

// owner returns the issue/chat identity, or the run identity for standalone work.
// Missing workspace or run identity is rejected rather than sharing a directory.
func (params LocalScriptParams) owner() (localScriptOwner, error) {
	owner := localScriptOwner{WorkspaceID: params.WorkspaceID, Kind: "run", ID: params.TaskID}
	if params.IssueID != "" {
		owner.Kind, owner.ID = "issue", params.IssueID
	} else if params.ChatSessionID != "" {
		owner.Kind, owner.ID = "chat", params.ChatSessionID
	}
	if owner.WorkspaceID == "" || owner.ID == "" {
		return localScriptOwner{}, errors.New("local_directory script: workspace and conversation/run identity are required")
	}
	return owner, nil
}

// WorkspacePath returns the canonical destination for params without writing it.
// Hashing the typed identity avoids unsafe path segments and survives restarts.
func (params LocalScriptParams) WorkspacePath() (string, error) {
	source, err := filepath.EvalSymlinks(params.LocalPath)
	if err != nil || !filepath.IsAbs(source) {
		return "", fmt.Errorf("local_directory script: invalid source directory %q", params.LocalPath)
	}
	owner, err := params.owner()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return filepath.Join(source, ".worktrees", "multica-"+owner.Kind+"-"+hex.EncodeToString(digest[:])), nil
}

// localScriptStateDir creates real parent/state directories for workDir and
// rejects symlinks so initialization and locks cannot escape .worktrees.
func localScriptStateDir(workDir string) (string, error) {
	parent := filepath.Dir(workDir)
	stateDir := filepath.Join(parent, localScriptStateDirName)
	for _, dir := range []string{parent, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("local_directory script: create workspace metadata: %w", err)
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("local_directory script: %s must be a directory, not a symlink", dir)
		}
	}
	return stateDir, nil
}

// ClaimLocalScriptWorkspace excludes other daemon processes until release is
// called. The caller holds its cancellable conversation mutex before claiming.
// The OS releases this lock if the daemon exits, including during initialization.
func ClaimLocalScriptWorkspace(params LocalScriptParams) (func(), error) {
	workDir, err := params.WorkspacePath()
	if err != nil {
		return nil, err
	}
	stateDir, err := localScriptStateDir(workDir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(stateDir, filepath.Base(workDir)+".lock")
	if info, statErr := os.Lstat(lockPath); statErr == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local_directory script: lock must be a regular file: %s", lockPath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	lock, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil || !locked {
		_ = lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("local_directory script: workspace is in use by another daemon; retry after it finishes: %s", workDir)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = lock.Close() }) }, nil
}

// prepareLocalScript runs inside the killable preparation helper. Its process
// group and deadline also cover Bash and its children. Work stays outside the
// daemon's garbage-collected env root. A ready owner record enables reuse;
// incomplete initialization is preserved and must be recovered by the user.
func prepareLocalScript(params LocalScriptParams, envRoot string) (string, error) {
	workDir, err := params.WorkspacePath()
	if err != nil {
		return "", err
	}
	source := filepath.Dir(filepath.Dir(workDir))
	stateDir, err := localScriptStateDir(workDir)
	if err != nil {
		return "", err
	}
	owner, err := params.owner()
	if err != nil {
		return "", err
	}
	ownerRecord, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	readyPath := filepath.Join(stateDir, filepath.Base(workDir)+".json")
	if info, err := os.Lstat(readyPath); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("local_directory script: owner record must be a regular file: %s", readyPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if existing, err := os.Lstat(workDir); err == nil {
		if !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("local_directory script: workspace must be a directory, not a symlink: %s", workDir)
		}
		record, err := os.ReadFile(readyPath)
		if err != nil || !bytes.Equal(record, ownerRecord) {
			return "", fmt.Errorf("local_directory script: initialization is incomplete or ownership is invalid; inspect and move the preserved directory before retrying: %s (owner record: %s)", workDir, readyPath)
		}
		return workDir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	script := strings.TrimSpace(params.InitScript)
	if !strings.HasSuffix(script, ".sh") {
		return "", fmt.Errorf("local_directory script: init_script must name a .sh file")
	}
	if !filepath.IsAbs(script) {
		script = filepath.Join(source, script)
	}
	info, err := os.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("local_directory script: initializer is not a regular file: %s", script)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return "", fmt.Errorf("local_directory script: Bash is required: %w", err)
	}
	// An old ready record cannot attest to a recreated, partially initialized tree.
	if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Mkdir(workDir, 0o755); err != nil {
		return "", fmt.Errorf("local_directory script: allocate workspace: %w", err)
	}
	original, err := os.Stat(workDir)
	if err != nil {
		return "", err
	}
	logs := filepath.Join(envRoot, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return "", err
	}
	logPath := filepath.Join(logs, "init-workspace.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	// Pass arguments directly; paths never become shell source code.
	cmd := exec.Command(bash, "--", script, workDir)
	cmd.Dir = source
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("local_directory script: initializer failed: %w; workspace preserved at %s; log: %s", err, workDir, logPath)
	}
	actual, err := os.Lstat(workDir)
	if err != nil || !actual.IsDir() || !os.SameFile(original, actual) {
		return "", fmt.Errorf("local_directory script: initializer replaced or removed the destination %s; refusing to run", workDir)
	}
	if err := os.WriteFile(readyPath, ownerRecord, 0o600); err != nil {
		return "", fmt.Errorf("local_directory script: record initialized workspace %s: %w", workDir, err)
	}
	return workDir, nil
}
