//go:build !windows

package execenv

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	preparationCleanupTimeout = 5 * time.Second
	preparationCleanupPoll    = 10 * time.Millisecond
)

type preparationProcessController struct {
	pid             int
	cleanupChildren bool
}

func newPreparationProcessController(cmd *exec.Cmd, cleanupChildren bool) (*preparationProcessController, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &preparationProcessController{cleanupChildren: cleanupChildren}, nil
}

func (controller *preparationProcessController) attach(cmd *exec.Cmd) error {
	controller.pid = cmd.Process.Pid
	return nil
}

func (*preparationProcessController) stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Kill the helper and any CLI it spawned. After SIGKILL is pending, a
	// helper blocked in a kernel filesystem call cannot return to Go and
	// perform another write when that call eventually unblocks.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// finish reaps surviving same-group children even when the helper returned an
// ordinary error. A failed Git transport must not outlive the workspace lock.
func (controller *preparationProcessController) finish() error {
	if controller.pid == 0 || !controller.cleanupChildren {
		return nil
	}
	if err := syscall.Kill(-controller.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(preparationCleanupTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-controller.pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(preparationCleanupPoll)
	}
	return errors.New("preparation process group still has live descendants")
}

func (*preparationProcessController) close() {}
