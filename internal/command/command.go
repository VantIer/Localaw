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
		Description: "Edit an existing text file by replacing an exact snippet. old_text is matched literally against the file (including indentation and line breaks) and must occur exactly once unless replace_all is true. Prefer this over rewriting the whole file; use write_file only to create or fully overwrite a file. If old_text is missing or ambiguous, the edit is rejected and you should read the file again and add surrounding context.",
		Params: []Param{
			{Name: "path", Required: true, Description: "File path."},
			{Name: "old_text", Required: true, Description: "Exact existing text to replace. Copy it verbatim from read_file, including indentation and newlines; include surrounding lines if the snippet is not unique."},
			{Name: "new_text", Required: true, Description: "Replacement text (use an empty string to delete the matched text)."},
			{Name: "replace_all", Description: "Set to true to replace every occurrence instead of requiring a unique match (default false)."},
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

// Schema renders the JSON Schema object used for this action's OpenAI tool
// parameters. It is shared by the built-in tool builder and the unified
// registry so both stay in sync.
func Schema(s Spec) map[string]any {
	props := make(map[string]any, len(s.Params))
	required := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		props[p.Name] = map[string]any{"type": "string", "description": p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
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

// RequiresAuthRisk is the single implementation of the three authorization
// modes. Dynamic tool registries should use this with their resolved risk flag.
func RequiresAuthRisk(mode int, lowRisk bool) bool {
	switch mode {
	case 2:
		return false
	case 1:
		return !lowRisk
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
