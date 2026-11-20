package mcphost

import (
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestNormalizeInputSchema(t *testing.T) {
	schema, err := normalizeInputSchema(map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}, 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Fatalf("type = %v", schema["type"])
	}
	if _, ok := schema["$schema"]; ok {
		t.Fatal("$schema was not removed")
	}

	nullSchema, err := normalizeInputSchema(json.RawMessage("null"), 32<<10)
	if err != nil || nullSchema["type"] != "object" {
		t.Fatalf("null schema = %+v, err = %v", nullSchema, err)
	}

	if _, err := normalizeInputSchema(map[string]any{"type": "string"}, 32<<10); err == nil {
		t.Fatal("expected non-object schema to be rejected")
	}
	if _, err := normalizeInputSchema(map[string]any{"type": "object"}, 10); err == nil {
		t.Fatal("expected oversized schema to be rejected")
	}
}

func TestFormatCallResult(t *testing.T) {
	res := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "hello"},
			&mcpsdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
		},
		StructuredContent: map[string]any{"ok": true},
	}
	out := formatCallResult(res, 0)
	if !strings.Contains(out, "hello") || !strings.Contains(out, "[image: image/png, 3 bytes]") {
		t.Fatalf("unexpected output: %q", out)
	}
	if !strings.Contains(out, "[structured]") {
		t.Fatalf("structured content missing: %q", out)
	}

	errRes := &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom"}}}
	if got := formatCallResult(errRes, 0); got != "Error: boom" {
		t.Fatalf("isError output = %q", got)
	}

	if got := formatCallResult(&mcpsdk.CallToolResult{}, 0); got != "(no output)" {
		t.Fatalf("empty output = %q", got)
	}
	if got := formatCallResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strings.Repeat("x", 20)}}}, 5); !strings.Contains(got, "truncated") {
		t.Fatalf("truncation marker missing: %q", got)
	}
}

func TestResolveToolRiskAndFilters(t *testing.T) {
	m := &Manager{cfg: config.MCP{DefaultRisk: "high", DefaultTimeout: 90, MaxSchemaBytes: 32 << 10, MaxDescriptionChars: 100}}
	sc := config.MCPServer{
		Name:        "s",
		DefaultRisk: "high",
		AllowTools:  []string{"read", "write"},
		DenyTools:   []string{"write"},
		Tools: map[string]config.MCPToolPolicy{
			"read": {Risk: "low"},
		},
	}
	read := &mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}
	got, ok := m.resolveTool(&sc, read)
	if !ok || !got.LowRisk {
		t.Fatalf("read should be allowed and low risk: %+v ok=%v", got, ok)
	}
	if got.Timeout != 90 {
		t.Fatalf("default timeout not inherited: %d", got.Timeout)
	}
	if _, ok := m.resolveTool(&sc, &mcpsdk.Tool{Name: "write"}); ok {
		t.Fatal("deny_tools did not filter the tool")
	}
	if _, ok := m.resolveTool(&sc, &mcpsdk.Tool{Name: "other"}); ok {
		t.Fatal("allow_tools did not filter the tool")
	}

	sc.TrustAnnotations = true
	ro := &mcpsdk.Tool{
		Name:        "ro",
		InputSchema: map[string]any{"type": "object"},
		Annotations: &mcpsdk.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		},
	}
	sc.AllowTools = nil
	sc.Tools = nil
	got, ok = m.resolveTool(&sc, ro)
	if !ok || !got.LowRisk {
		t.Fatalf("trusted read-only annotation should be low risk: %+v ok=%v", got, ok)
	}
}

func TestTruncateHelpers(t *testing.T) {
	if got := truncateDescription("abcdef", 4); got != "a..." {
		t.Fatalf("truncateDescription(4) = %q", got)
	}
	if got := truncateDescription("abcdef", 2); got != "ab" {
		t.Fatalf("truncateDescription(2) = %q", got)
	}
	if got := truncateDescription("abc", 3); got != "abc" {
		t.Fatalf("truncateDescription(exact) = %q", got)
	}
	if got := truncateRunes("abcdef", 2); got != "ab\n[Output truncated at 2 characters]" {
		t.Fatalf("truncateRunes(2) = %q", got)
	}
}
