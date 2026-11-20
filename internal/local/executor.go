package local

import (
	"bytes"
	"context"
	"strings"
	"time"

	"localaw/internal/command"
	"localaw/internal/procutil"
	"localaw/internal/toolname"
)

// MCPCaller is the subset of the MCP host used by the executor. Keeping it as
// an interface avoids a dependency from local to the MCP implementation.
type MCPCaller interface {
	Has(name string) bool
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

// Executor runs a single action on the local machine.
type Executor struct {
	file *FileModule
	mcp  MCPCaller
}

// NewExecutor creates an executor bound to a file module, optionally with an
// MCP host for namespaced MCP tools.
func NewExecutor(file *FileModule, mcp ...MCPCaller) *Executor {
	e := &Executor{file: file}
	if len(mcp) > 0 {
		e.mcp = mcp[0]
	}
	return e
}

// Execute runs an action. Business failures (missing file, bad parameters, ...)
// are returned as an "Error: ..." result string with a nil error, so they can
// be fed back to the model as a normal tool result. A non-nil error is only
// returned for transport/cancellation failures.
func (e *Executor) Execute(ctx context.Context, action string, params map[string]any) (string, error) {
	if toolname.IsExposed(action) {
		if e.mcp == nil {
			return "Error: MCP is not enabled", nil
		}
		if !e.mcp.Has(action) {
			return "Error: MCP tool is no longer available: " + action, nil
		}
		return e.mcp.Call(ctx, action, params)
	}
	if action == "exec_cmd" {
		return e.runShell(ctx, command.String(params["command"]))
	}
	out, err := e.file.Dispatch(action, params)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	return out, nil
}

func (e *Executor) runShell(ctx context.Context, shellCmd string) (string, error) {
	if strings.TrimSpace(shellCmd) == "" {
		return "Error: Empty command", nil
	}
	cmd := shellCommand(ctx, shellCmd)
	// Kill the whole process tree (not just the shell) on timeout / stop.
	cmd.Cancel = func() error { return procutil.KillTree(cmd) }
	cmd.WaitDelay = 5 * time.Second

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// Child processes write bytes in the platform's console/OEM code page
	// (e.g. CP936 on Simplified Chinese Windows), so decode before treating the
	// output as UTF-8 text.
	result := strings.ToValidUTF8(string(decodeConsole(buf.Bytes())), "\uFFFD")
	// Strip at most one trailing newline, preserving intentional blank lines.
	result = strings.TrimSuffix(result, "\n")
	result = strings.TrimSuffix(result, "\r")
	if result == "" {
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		return "Command executed successfully (no output)", nil
	}
	return result, nil
}
