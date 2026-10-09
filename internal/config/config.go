// Package config loads and validates the Localaw configuration.
//
// Localaw is a purely local agent: every action runs on the machine hosting
// the process. The configuration therefore has no remote endpoints — only the
// LLM connection, the web listener and the scheduling/authorization policy.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const defaultSystemPrompt = `You are Localaw, a local AI assistant that can inspect and manage the user's own computer through the provided tools.
CURRENT OPERATING SYSTEM: {system_name}

Rules:
- Accomplish the task exclusively through the provided tools; never fabricate tool output.
- Prefer absolute paths. Shell commands do not share state between calls.
- Read-only tools (get_cwd, list_dir, read_file) are low risk; other tools may require user authorization.
- To change an existing file, use edit_file with an exact old_text snippet copied verbatim from read_file (include enough surrounding context to make it unique). Use write_file only to create a file or fully overwrite it.
- For long-running tasks, start them detached (e.g. nohup ... & / start "" /b ...) so the tool returns promptly.
`

// Config is the root configuration object.
type Config struct {
	LLM    LLM    `json:"llm"`
	Web    Web    `json:"web"`
	Policy Policy `json:"policy"`
}

// LLM holds the OpenAI-compatible endpoint settings.
type LLM struct {
	APIBase      string  `json:"api_base"`
	APIKey       string  `json:"api_key"`
	Model        string  `json:"model"`
	Temperature  float64 `json:"temperature"`
	Stream       bool    `json:"stream"`
	SystemPrompt string  `json:"system_prompt"`
}

// Web holds the browser panel listener settings.
type Web struct {
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
}

// Policy holds authorization / scheduling limits.
type Policy struct {
	AuthMode      int `json:"auth_mode"`      // 0=N-Auto 1=H-Auto 2=F-Auto
	RoundLimit    int `json:"round_limit"`    // max LLM rounds per turn
	CmdTimeout    int `json:"cmd_timeout"`    // execution timeout (seconds)
	MaxSessions   int `json:"max_sessions"`   // concurrent session cap
	QueueCapacity int `json:"queue_capacity"` // linear command queue depth
}

// Default returns a Config populated with built-in defaults.
func Default() *Config {
	return &Config{
		LLM: LLM{
			APIBase:      "http://localhost:11434/v1",
			APIKey:       "deepseek",
			Model:        "deepseek",
			Temperature:  0.7,
			Stream:       true,
			SystemPrompt: defaultSystemPrompt,
		},
		Web: Web{ListenHost: "127.0.0.1", ListenPort: 8880},
		Policy: Policy{
			AuthMode:      0,
			RoundLimit:    20,
			CmdTimeout:    60,
			MaxSessions:   8,
			QueueCapacity: 256,
		},
	}
}

// Load reads a JSON config file, applying defaults for missing fields, then
// validates the result. A nonexistent path yields defaults (with a validation
// pass) so the binary can still start for local experiments.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read config: %w", err)
			}
		} else if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize repairs / validates fields and returns an error on fatal problems.
func (c *Config) normalize() error {
	if c.Web.ListenHost == "" {
		c.Web.ListenHost = "127.0.0.1"
	}
	if c.Web.ListenPort == 0 {
		c.Web.ListenPort = 8880
	}
	if c.Web.ListenPort < 1 || c.Web.ListenPort > 65535 {
		return fmt.Errorf("web.listen_port out of range: %d", c.Web.ListenPort)
	}
	if c.Policy.RoundLimit <= 0 {
		c.Policy.RoundLimit = 20
	}
	if c.Policy.CmdTimeout <= 0 {
		c.Policy.CmdTimeout = 60
	}
	if c.Policy.MaxSessions <= 0 {
		c.Policy.MaxSessions = 8
	}
	if c.Policy.QueueCapacity <= 0 {
		c.Policy.QueueCapacity = 256
	}
	if c.Policy.AuthMode < 0 || c.Policy.AuthMode > 2 {
		c.Policy.AuthMode = 0
	}
	if strings.TrimSpace(c.LLM.APIBase) == "" {
		c.LLM.APIBase = "http://localhost:11434/v1"
	}
	if strings.TrimSpace(c.LLM.APIKey) == "" {
		c.LLM.APIKey = "deepseek"
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		c.LLM.Model = "deepseek"
	}
	if c.LLM.SystemPrompt == "" {
		c.LLM.SystemPrompt = defaultSystemPrompt
	}
	return nil
}
