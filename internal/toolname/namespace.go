// Package toolname defines the namespace used by tools contributed by an
// external source (currently MCP). Keeping it in a neutral package lets the
// executor and engine recognize exposed names without importing mcphost.
package toolname

import "strings"

// Prefix is the namespace prefix for externally exposed tools.
const Prefix = "mcp__"

// IsExposed reports whether name belongs to the external tool namespace.
func IsExposed(name string) bool {
	return strings.HasPrefix(name, Prefix)
}
