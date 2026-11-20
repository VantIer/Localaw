package mcphost

import (
	"fmt"
	"hash/fnv"
	"strings"

	"localaw/internal/toolname"
)

// maxToolName matches the OpenAI-compatible function-name limit. MCP allows
// longer names (and more characters), so MCP tool names must be normalized.
const maxToolName = 64

// SafeName maps an arbitrary MCP name onto the display/log-safe alphabet
// [A-Za-z0-9_-]. It is used for user-visible labels so a malicious server
// cannot inject terminal escapes or control characters.
func SafeName(s string) string { return sanitizeSegment(s) }

// sanitizeSegment maps an arbitrary MCP name segment onto [A-Za-z0-9_-],
// collapsing runs of invalid characters into a single underscore.
func sanitizeSegment(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-'
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		// Collapse runs of underscores and invalid characters into one '_'.
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "tool"
	}
	return out
}

// shortHash returns a stable 8-hex-digit digest used to disambiguate names.
func shortHash(parts ...string) string {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// baseName builds the canonical namespace prefix before collision handling.
func baseName(server, tool string) string {
	return toolname.Prefix + sanitizeSegment(server) + "__" + sanitizeSegment(tool)
}

// fitName truncates a long base name via hashedName so the result stays
// deterministic and within maxToolName bytes. Sanitized names are ASCII, so
// byte length equals character length.
func fitName(base, server, tool string) string {
	if len(base) <= maxToolName {
		return base
	}
	return hashedName(base, server, tool)
}

// hashedName forces a hash suffix for collision resolution.
func hashedName(base, server, tool string) string {
	suffix := "__" + shortHash(server, tool)
	keep := maxToolName - len(suffix)
	if keep < 3 {
		keep = 3
	}
	if len(base) > keep {
		base = base[:keep]
	}
	return base + suffix
}
