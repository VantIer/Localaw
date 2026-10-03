// Package command is the single source of truth for the actions the LLM can
// invoke: their parameters, risk level, description and safety checks. The LLM
// tool schema is generated from this table.
package command

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Param describes one action parameter.
type Param struct {
	Name        string
	Required    bool
	Description string
}

// Spec describes an action.
type Spec struct {
	Name        string
	LowRisk     bool
	Description string
	Params      []Param
}

var specs = []Spec{
	{
		Name: "get_cwd", LowRisk: true,
		Description: "Get the absolute current working directory of the local system.",
	},
	{
		Name: "list_dir", LowRisk: true,
		Description: "List the contents of a directory on the local system.",
		Params:      []Param{{Name: "path", Description: "Directory path (default '.')."}},
	},
	{
		Name: "read_file", LowRisk: true,
		Description: "Read a text file, optionally a line range. Whole-file reads are truncated to 51200 characters.",
		Params: []Param{
			{Name: "path", Required: true, Description: "File path."},
			{Name: "start_line", Description: "1-based first line (0/empty = from start)."},
			{Name: "end_line", Description: "1-based last line (0/empty = to end)."},
		},
	},
	{
		Name:        "write_file",
		Description: "Create or overwrite a file with the given content.",
		Params: []Param{
			{Name: "path", Required: true, Description: "File path."},
			{Name: "content", Required: true, Description: "Full file content."},
		},
	},
	{
		Name:        "create_file",
		Description: "Create an empty file.",
		Params:      []Param{{Name: "path", Required: true, Description: "File path."}},
	},
	{
		Name:        "delete_file",
		Description: "Delete a file.",
		Params:      []Param{{Name: "path", Required: true, Description: "File path."}},
	},
	{
		Name:        "delete_dir",
		Description: "Delete a directory recursively.",
		Params:      []Param{{Name: "path", Required: true, Description: "Directory path."}},
	},
	{
		Name:        "make_dir",
		Description: "Create a directory (parents included).",
		Params:      []Param{{Name: "path", Required: true, Description: "Directory path."}},
	},
	{
		Name:        "rename_file",
		Description: "Rename a file.",
		Params: []Param{
			{Name: "path", Required: true, Description: "File path."},
			{Name: "new_name", Required: true, Description: "New file name (no directory)."},
		},
	},
	{
		Name:        "rename_dir",
		Description: "Rename a directory.",
		Params: []Param{
			{Name: "path", Required: true, Description: "Directory path."},
			{Name: "new_name", Required: true, Description: "New directory name."},
		},
	},
	{
		Name:        "edit_file",
		Description: "Edit a text file by inserting, deleting or replacing lines.",
		Params: []Param{
			{Name: "path", Required: true, Description: "File path."},
			{Name: "operation", Required: true, Description: "One of: add, del, modify."},
			{Name: "start_line", Description: "1-based start line."},
			{Name: "end_line", Description: "1-based end line (del/modify)."},
			{Name: "content", Description: "Content for add/modify."},
		},
	},
	{
		Name:        "copy",
		Description: "Copy a file or directory.",
		Params: []Param{
			{Name: "src", Required: true, Description: "Source path."},
			{Name: "dest", Required: true, Description: "Destination path."},
		},
	},
	{
		Name:        "move",
		Description: "Move a file or directory.",
		Params: []Param{
			{Name: "src", Required: true, Description: "Source path."},
			{Name: "dest", Required: true, Description: "Destination path."},
		},
	},
	{
		Name:        "exec_cmd",
		Description: "Execute a shell command on the local system and return its output.",
		Params:      []Param{{Name: "command", Required: true, Description: "The shell command to run."}},
	},
}

// All returns a deep copy of every action spec. Mutating the returned slice or
// any nested parameter does not affect the command table.
func All() []Spec {
	out := make([]Spec, len(specs))
	for i, s := range specs {
		if len(s.Params) > 0 {
			params := make([]Param, len(s.Params))
			copy(params, s.Params)
			s.Params = params
		}
		out[i] = s
	}
	return out
}

var byName = map[string]Spec{}

func init() {
	for _, s := range specs {
		byName[s.Name] = s
	}
}

// SpecByName returns the spec for an action.
func SpecByName(name string) (Spec, bool) {
	s, ok := byName[name]
	return s, ok
}

// IsLowRisk reports whether an action is read-only / listing.
func IsLowRisk(name string) bool {
	s, ok := byName[name]
	return ok && s.LowRisk
}

// RequiresAuth decides whether an action needs explicit user authorization
// under the given auth_mode (0=N-Auto, 1=H-Auto, 2=F-Auto).
func RequiresAuth(mode int, name string) bool {
	switch mode {
	case 2:
		return false
	case 1:
		return !IsLowRisk(name)
	default:
		return true
	}
}

// Forbidden substrings / patterns for shell commands.
var (
	forbiddenPatterns = []string{"format c:", "mkfs.", "dd if=/dev/zero"}
	// rm with any flags, capturing the target operand (handles separated flags
	// like `rm -r -f /` and long flags like `--recursive`).
	rmArgRe = regexp.MustCompile(`(?i)\brm\b(?:\s+-{1,2}[a-z]+)*\s+(\S+)`)
)

// CheckSafety blocks clearly destructive shell commands. This is a best-effort
// blacklist, not a security boundary.
func CheckSafety(action string, params map[string]any) bool {
	if action != "exec_cmd" {
		return true
	}
	cmd := String(params["command"])
	// Collapse runs of whitespace so e.g. "format  c:" is still caught.
	norm := strings.ToLower(strings.Join(strings.Fields(cmd), " "))
	for _, p := range forbiddenPatterns {
		if strings.Contains(norm, p) {
			return false
		}
	}
	return !rmHitsRoot(cmd)
}

// rmHitsRoot reports whether an `rm` targets the filesystem root or `*`.
func rmHitsRoot(cmd string) bool {
	for _, m := range rmArgRe.FindAllStringSubmatch(cmd, -1) {
		arg := strings.Trim(m[1], `"'`)
		if arg == "/" || arg == "/*" || arg == "*" {
			return true
		}
	}
	return false
}

// String renders an action parameter value as the string used for execution.
func String(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
