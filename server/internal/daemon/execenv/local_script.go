package execenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LocalScriptParams configures a user-owned Bash initializer. The script runs
// in LocalPath and receives a unique, empty destination as its first argument.
type LocalScriptParams struct {
	LocalPath  string
	InitScript string
}

// prepareLocalScript runs inside the killable preparation helper. Its process
// group and deadline also cover Bash and its children. Work stays outside the
// daemon's garbage-collected env root, including on failure or cancellation.
func prepareLocalScript(params LocalScriptParams, envRoot string) (string, error) {
	source, err := filepath.EvalSymlinks(params.LocalPath)
	if err != nil || !filepath.IsAbs(source) {
		return "", fmt.Errorf("local_directory script: invalid source directory %q", params.LocalPath)
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
	parent := filepath.Join(source, ".worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("local_directory script: create workspace parent: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("local_directory script: .worktrees must be a directory, not a symlink")
	}
	workDir, err := os.MkdirTemp(parent, "multica-"+time.Now().Format("20060102")+"-")
	if err != nil {
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
	return workDir, nil
}
