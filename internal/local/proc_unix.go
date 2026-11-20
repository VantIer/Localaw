//go:build !windows

package local

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"localaw/internal/procutil"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	procutil.Prepare(c)
	return c
}

func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// decodeConsole is a no-op on non-Windows platforms, whose child processes
// already emit bytes in the locale encoding.
func decodeConsole(b []byte) []byte { return b }
