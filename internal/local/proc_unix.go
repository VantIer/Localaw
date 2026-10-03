//go:build !windows

package local

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative PID targets the whole process group.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// decodeConsole is a no-op on non-Windows platforms, whose child processes
// already emit bytes in the locale encoding.
func decodeConsole(b []byte) []byte { return b }
