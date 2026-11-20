//go:build windows

// Package procutil provides small, OS-specific helpers for managing child
// process trees. It is shared by the local shell executor and MCP stdio
// servers.
package procutil

import (
	"os/exec"
	"strconv"
)

// Prepare configures cmd for tree termination. Windows has no process-group
// equivalent here, so termination uses taskkill /T.
func Prepare(cmd *exec.Cmd) {}

// KillTree terminates cmd and every descendant it spawned.
func KillTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	return cmd.Process.Kill()
}
