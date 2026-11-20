package mcphost

import (
	"encoding/json"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
)

// Tool is one MCP tool ready to be exposed to the LLM.
type Tool struct {
	Exposed     string
	Server      string
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	LowRisk     bool
	Timeout     int // seconds; 0 = inherit policy.cmd_timeout
}

// resolveTool applies allow/deny lists, risk policy and schema normalization to
// one tool advertised by an MCP server. ok is false when the tool is filtered
// out or unusable.
func (m *Manager) resolveTool(sc *config.MCPServer, t *mcpsdk.Tool) (Tool, bool) {
	if t == nil || t.Name == "" {
		return Tool{}, false
	}
	if containsString(sc.DenyTools, t.Name) {
		return Tool{}, false
	}
	if len(sc.AllowTools) > 0 && !containsString(sc.AllowTools, t.Name) {
		return Tool{}, false
	}

	risk := sc.DefaultRisk
	if risk == "" {
		risk = m.cfg.DefaultRisk
	}
	timeout := sc.CallTimeout
	if timeout == 0 {
		timeout = m.cfg.DefaultTimeout
	}
	explicitRisk := false
	if p, ok := sc.Tools[t.Name]; ok {
		if p.Risk != "" {
			risk = p.Risk
			explicitRisk = true
		}
		if p.Timeout > 0 {
			timeout = p.Timeout
		}
	}

	lowRisk := risk == "low"
	if !lowRisk && !explicitRisk && sc.TrustAnnotations && isReadOnlyClosedWorld(t.Annotations) {
		lowRisk = true
	}

	schema, err := normalizeInputSchema(t.InputSchema, m.cfg.MaxSchemaBytes)
	if err != nil {
		m.warnf("mcp server %s: skipping tool %q: %v", sc.Name, SafeName(t.Name), err)
		return Tool{}, false
	}

	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = "MCP tool " + t.Name + " from server " + sc.Name + "."
	}
	desc = truncateDescription("[MCP:"+sc.Name+"] "+desc, m.cfg.MaxDescriptionChars)

	title := strings.TrimSpace(t.Title)
	if title == "" && t.Annotations != nil {
		title = strings.TrimSpace(t.Annotations.Title)
	}

	return Tool{
		Server:      sc.Name,
		Name:        t.Name,
		Title:       title,
		Description: desc,
		InputSchema: schema,
		LowRisk:     lowRisk,
		Timeout:     timeout,
	}, true
}

// isReadOnlyClosedWorld conservatively infers a read-only tool from MCP
// annotations. It is only consulted when the operator opts in with
// trust_annotations=true: annotations are server-provided and untrusted by
// default. Missing destructive/open-world hints default to the unsafe value.
func isReadOnlyClosedWorld(a *mcpsdk.ToolAnnotations) bool {
	if a == nil || !a.ReadOnlyHint {
		return false
	}
	if a.DestructiveHint == nil || *a.DestructiveHint {
		return false
	}
	if a.OpenWorldHint == nil || *a.OpenWorldHint {
		return false
	}
	return true
}

func normalizeInputSchema(raw any, maxBytes int) (map[string]any, error) {
	if raw == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("input schema is not JSON: %w", err)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, fmt.Errorf("input schema is %d bytes (limit %d)", len(data), maxBytes)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("input schema is not an object: %w", err)
	}
	if schema == nil {
		schema = map[string]any{}
	}
	delete(schema, "$schema")
	delete(schema, "$id")

	if typ, _ := schema["type"].(string); typ != "" && typ != "object" {
		return nil, fmt.Errorf("input schema root type must be object, got %q", typ)
	}
	schema["type"] = "object"
	if _, ok := schema["properties"].(map[string]any); !ok {
		schema["properties"] = map[string]any{}
	}
	return schema, nil
}

// formatCallResult flattens MCP content blocks into the single text tool result
// the current chat loop can feed back to the model. Content is accumulated
// against maxChars while iterating, so an oversized server response cannot
// create an unbounded intermediate string.
func formatCallResult(res *mcpsdk.CallToolResult, maxChars int) string {
	if res == nil {
		return "(no output)"
	}
	limit := -1
	if maxChars > 0 {
		limit = maxChars
		if res.IsError {
			limit -= len("Error: ")
			if limit < 0 {
				limit = 0
			}
		}
	}
	var b strings.Builder
	written := 0
	truncated := false
	hasContent := false

	write := func(s string) {
		if truncated || s == "" {
			return
		}
		n, cut := writeRunesLimited(&b, s, limit-written)
		written += n
		if cut {
			truncated = true
		}
	}
	add := func(s string) {
		if truncated || s == "" {
			return
		}
		if hasContent {
			write("\n")
			if truncated {
				return
			}
		}
		write(s)
		hasContent = true
	}

	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcpsdk.TextContent:
			add(v.Text)
		case *mcpsdk.ImageContent:
			add(fmt.Sprintf("[image: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *mcpsdk.AudioContent:
			add(fmt.Sprintf("[audio: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *mcpsdk.ResourceLink:
			name := v.Name
			if name == "" {
				name = v.Title
			}
			if name != "" {
				add(fmt.Sprintf("[resource: %s (%s)]", v.URI, name))
			} else {
				add("[resource: " + v.URI + "]")
			}
		case *mcpsdk.EmbeddedResource:
			add(formatEmbeddedResource(v))
		default:
			add(fmt.Sprintf("[unsupported content: %T]", c))
		}
	}
	if res.StructuredContent != nil && !truncated {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			add("[structured] " + string(data))
		}
	}

	out := b.String()
	if out == "" {
		out = "(no output)"
	}
	if res.IsError {
		out = "Error: " + out
	}
	if truncated {
		out += fmt.Sprintf("\n[Output truncated at %d characters]", maxChars)
	}
	return out
}

// writeRunesLimited appends at most limit runes. A negative limit means
// unlimited. It returns the number of runes written and whether text was cut.
func writeRunesLimited(b *strings.Builder, s string, limit int) (int, bool) {
	if limit < 0 {
		b.WriteString(s)
		return 0, false
	}
	n := 0
	for _, r := range s {
		if n >= limit {
			return n, true
		}
		b.WriteRune(r)
		n++
	}
	return n, false
}

func formatEmbeddedResource(v *mcpsdk.EmbeddedResource) string {
	if v == nil || v.Resource == nil {
		return "[resource: empty]"
	}
	r := v.Resource
	if r.Text != "" {
		return r.Text
	}
	if len(r.Blob) > 0 {
		return fmt.Sprintf("[resource blob: %s, %s, %d bytes]", r.URI, r.MIMEType, len(r.Blob))
	}
	return "[resource: " + r.URI + "]"
}

// limitRunes returns s limited to max runes and whether it was cut. A
// non-positive max disables the limit.
func limitRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]), true
}

// truncateDescription limits a tool description to max runes, reserving room
// for the ellipsis marker.
func truncateDescription(s string, max int) string {
	if max <= 0 {
		return s
	}
	if max <= 3 {
		cut, _ := limitRunes(s, max)
		return cut
	}
	cut, truncated := limitRunes(s, max-3)
	if !truncated {
		return s
	}
	return cut + "..."
}

// truncateRunes limits text to max runes, adding a marker when it was cut.
func truncateRunes(s string, max int) string {
	cut, truncated := limitRunes(s, max)
	if !truncated {
		return s
	}
	return cut + fmt.Sprintf("\n[Output truncated at %d characters]", max)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
