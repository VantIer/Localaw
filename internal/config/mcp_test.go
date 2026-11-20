package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadMCP(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestMCPDefaults(t *testing.T) {
	c := Default()
	if c.MCP.Enabled {
		t.Fatal("MCP must be disabled by default")
	}
	if c.MCP.InitTimeout != 15 || c.MCP.ListTimeout != 10 || c.MCP.DefaultRisk != "high" {
		t.Fatalf("unexpected MCP defaults: %+v", c.MCP)
	}
	if c.MCP.MaxTools != 128 || c.MCP.MaxResultChars != 51200 || c.MCP.MaxSchemaBytes != 32<<10 {
		t.Fatalf("unexpected MCP limits: %+v", c.MCP)
	}
}

func TestMCPNormalizesStdioServer(t *testing.T) {
	c, err := loadMCP(t, `{"mcp":{"enabled":true,"servers":[{"name":"fs","command":"npx","args":["-y","server"]}]}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := c.MCP.Servers[0]
	if s.Transport != "stdio" {
		t.Fatalf("transport = %q", s.Transport)
	}
	if s.ConnectTimeout != 10 || s.DefaultRisk != "high" {
		t.Fatalf("server defaults not applied: %+v", s)
	}
	if s.Enabled == nil || !*s.Enabled {
		t.Fatalf("enabled default not applied: %+v", s)
	}
	if s.InheritEnv == nil || *s.InheritEnv {
		t.Fatalf("inherit_env must default to false: %+v", s)
	}
	if s.Restart.Enabled == nil || !*s.Restart.Enabled || s.Restart.MaxAttempts != 10 {
		t.Fatalf("restart defaults not applied: %+v", s.Restart)
	}
}

func TestMCPExpandsEnv(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "secret")
	c, err := loadMCP(t, `{"mcp":{"enabled":true,"servers":[{"name":"r","transport":"http","url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${MCP_TEST_TOKEN}"}}]}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := c.MCP.Servers[0].Headers["Authorization"]; got != "Bearer secret" {
		t.Fatalf("header = %q", got)
	}
}

func TestMCPExpandsURLBeforeValidation(t *testing.T) {
	t.Setenv("MCP_TEST_URL", "https://example.com/mcp")
	c, err := loadMCP(t, `{"mcp":{"enabled":true,"servers":[{"name":"r","transport":"http","url":"${MCP_TEST_URL}"}]}}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := c.MCP.Servers[0].URL; got != "https://example.com/mcp" {
		t.Fatalf("url = %q", got)
	}
}

func TestMCPValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"duplicate", `{"mcp":{"enabled":true,"servers":[{"name":"a","command":"x"},{"name":"a","command":"y"}]}}`, "duplicate"},
		{"bad name", `{"mcp":{"enabled":true,"servers":[{"name":"bad name","command":"x"}]}}`, "name must match"},
		{"unknown transport", `{"mcp":{"enabled":true,"servers":[{"name":"a","transport":"ws","url":"ws://x"}]}}`, "unknown transport"},
		{"missing command", `{"mcp":{"enabled":true,"servers":[{"name":"a","transport":"stdio"}]}}`, "command is required"},
		{"missing url", `{"mcp":{"enabled":true,"servers":[{"name":"a","transport":"http"}]}}`, "url is required"},
		{"bad risk", `{"mcp":{"enabled":true,"default_risk":"medium"}}`, "default_risk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadMCP(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	if _, err := loadMCP(t, `{"llm":{"api_key":"x"},"mcp":{"enabled":false,"unknown_option":true}}`); err == nil {
		t.Fatal("expected unknown config field to be rejected")
	}
}

func TestLoadRejectsTrailingData(t *testing.T) {
	if _, err := loadMCP(t, `{"llm":{"api_key":"x"}} {"llm":{"api_key":"y"}}`); err == nil {
		t.Fatal("expected trailing config data to be rejected")
	}
}

func TestMCPDisabledSkipsValidation(t *testing.T) {
	body := `{"mcp":{"enabled":false,"default_risk":"medium","default_timeout":-1,"servers":[{"name":"bad name","transport":"ws"}]}}`
	if _, err := loadMCP(t, body); err != nil {
		t.Fatalf("disabled MCP should not validate its section: %v", err)
	}
}
