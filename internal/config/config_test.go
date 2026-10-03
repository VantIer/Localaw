package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Web.ListenPort != 8880 {
		t.Fatalf("default port = %d", c.Web.ListenPort)
	}
	if c.Policy.RoundLimit != 20 || c.Policy.CmdTimeout != 60 || c.Policy.MaxSessions != 8 {
		t.Fatalf("unexpected policy defaults: %+v", c.Policy)
	}
	if c.LLM.Model == "" || c.LLM.SystemPrompt == "" {
		t.Fatalf("unexpected llm defaults: %+v", c.LLM)
	}
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Policy.AuthMode != 0 {
		t.Fatalf("auth mode = %d", c.Policy.AuthMode)
	}
}

func TestLoadNormalizes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	body := `{"llm":{"api_base":"","api_key":"","model":""},"web":{"listen_port":9999},"policy":{"auth_mode":7,"round_limit":0,"cmd_timeout":-1}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.LLM.APIBase == "" || c.LLM.APIKey == "" || c.LLM.Model == "" {
		t.Fatalf("empty llm fields were not defaulted: %+v", c.LLM)
	}
	if c.Policy.AuthMode != 0 {
		t.Fatalf("invalid auth mode not clamped: %d", c.Policy.AuthMode)
	}
	if c.Policy.RoundLimit != 20 || c.Policy.CmdTimeout != 60 {
		t.Fatalf("invalid policy values not repaired: %+v", c.Policy)
	}
	if c.Web.ListenPort != 9999 {
		t.Fatalf("listen port override lost: %d", c.Web.ListenPort)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(`{"web":{"listen_port":70000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected an error for an out-of-range port")
	}
}
