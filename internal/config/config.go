// Package config loads and validates the Localaw configuration.
//
// Localaw is a purely local agent: every action runs on the machine hosting
// the process. The configuration covers the LLM connection, the web listener,
// the scheduling/authorization policy and optional external MCP servers.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	DefaultMCPInitTimeout         = 15
	DefaultMCPListTimeout         = 10
	DefaultMCPMaxTools            = 128
	DefaultMCPMaxResultChars      = 51200
	DefaultMCPMaxDescriptionChars = 2000
	DefaultMCPMaxSchemaBytes      = 32 << 10
)

// Non-MCP defaults shared by Default and normalize so the two paths cannot
// drift apart.
const (
	defaultAPIBase       = "http://localhost:11434/v1"
	defaultAPIKey        = "deepseek"
	defaultModel         = "deepseek"
	defaultListenHost    = "127.0.0.1"
	defaultListenPort    = 8880
	defaultRoundLimit    = 20
	defaultCmdTimeout    = 60
	defaultMaxSessions   = 8
	defaultQueueCapacity = 256
)

const defaultSystemPrompt = `You are Localaw, a local AI assistant that can inspect and manage the user's own computer through the provided tools.
CURRENT OPERATING SYSTEM: {system_name}

Rules:
- Accomplish the task exclusively through the provided tools; never fabricate tool output.
- Prefer absolute paths. Shell commands do not share state between calls.
- Read-only tools (get_cwd, list_dir, read_file) are low risk; other tools may require user authorization.
- MCP tools are named mcp__<server>__<tool> and may require authorization like other high-risk tools.
- To change an existing file, use edit_file with an exact old_text snippet copied verbatim from read_file (include enough surrounding context to make it unique). Use write_file only to create a file or fully overwrite it.
- For long-running tasks, start them detached (e.g. nohup ... & / start "" /b ...) so the tool returns promptly.
`

// Config is the root configuration object.
type Config struct {
	LLM    LLM    `json:"llm"`
	Web    Web    `json:"web"`
	Policy Policy `json:"policy"`
	MCP    MCP    `json:"mcp"`
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

// MCP configures external Model Context Protocol servers. Localaw acts as an
// MCP client/host: it uses the tools exposed by the configured servers. Only
// the tools capability is supported; resources, prompts, sampling and
// elicitation are intentionally not implemented.
type MCP struct {
	Enabled             bool        `json:"enabled"`
	InitTimeout         int         `json:"init_timeout"`          // startup connect budget (seconds)
	ListTimeout         int         `json:"list_timeout"`          // tools/list timeout (seconds)
	DefaultRisk         string      `json:"default_risk"`          // low | high
	DefaultTimeout      int         `json:"default_timeout"`       // 0 = inherit policy.cmd_timeout
	MaxTools            int         `json:"max_tools"`             // total exposed MCP tool cap
	MaxResultChars      int         `json:"max_result_chars"`      // tool result truncation
	MaxDescriptionChars int         `json:"max_description_chars"` // tool description truncation
	MaxSchemaBytes      int         `json:"max_schema_bytes"`      // per-tool input schema cap
	Servers             []MCPServer `json:"servers"`
}

// MCPServer describes one stdio, Streamable HTTP or legacy SSE MCP server.
type MCPServer struct {
	Name             string                   `json:"name"`
	Transport        string                   `json:"transport"` // stdio | http | sse
	Command          string                   `json:"command,omitempty"`
	Args             []string                 `json:"args,omitempty"`
	Cwd              string                   `json:"cwd,omitempty"`
	Env              map[string]string        `json:"env,omitempty"`
	InheritEnv       *bool                    `json:"inherit_env,omitempty"`
	URL              string                   `json:"url,omitempty"`
	Headers          map[string]string        `json:"headers,omitempty"`
	Enabled          *bool                    `json:"enabled,omitempty"`
	ConnectTimeout   int                      `json:"connect_timeout,omitempty"`
	CallTimeout      int                      `json:"call_timeout,omitempty"`
	DefaultRisk      string                   `json:"default_risk,omitempty"`
	TrustAnnotations bool                     `json:"trust_annotations,omitempty"`
	AllowTools       []string                 `json:"allow_tools,omitempty"`
	DenyTools        []string                 `json:"deny_tools,omitempty"`
	Tools            map[string]MCPToolPolicy `json:"tools,omitempty"`
	Restart          RestartPolicy            `json:"restart,omitempty"`
}

// MCPToolPolicy overrides risk / timeout for one MCP tool.
type MCPToolPolicy struct {
	Risk    string `json:"risk,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// RestartPolicy controls reconnection backoff for one MCP server.
type RestartPolicy struct {
	Enabled     *bool `json:"enabled,omitempty"`
	MaxAttempts int   `json:"max_attempts,omitempty"`
	MinBackoff  int   `json:"min_backoff,omitempty"`
	MaxBackoff  int   `json:"max_backoff,omitempty"`
}

// Default returns a Config populated with built-in defaults.
func Default() *Config {
	return &Config{
		LLM: LLM{
			APIBase:      defaultAPIBase,
			APIKey:       defaultAPIKey,
			Model:        defaultModel,
			Temperature:  0.7,
			Stream:       true,
			SystemPrompt: defaultSystemPrompt,
		},
		Web: Web{ListenHost: defaultListenHost, ListenPort: defaultListenPort},
		Policy: Policy{
			AuthMode:      0,
			RoundLimit:    defaultRoundLimit,
			CmdTimeout:    defaultCmdTimeout,
			MaxSessions:   defaultMaxSessions,
			QueueCapacity: defaultQueueCapacity,
		},
		MCP: MCP{
			Enabled:             false,
			InitTimeout:         DefaultMCPInitTimeout,
			ListTimeout:         DefaultMCPListTimeout,
			DefaultRisk:         "high",
			DefaultTimeout:      0,
			MaxTools:            DefaultMCPMaxTools,
			MaxResultChars:      DefaultMCPMaxResultChars,
			MaxDescriptionChars: DefaultMCPMaxDescriptionChars,
			MaxSchemaBytes:      DefaultMCPMaxSchemaBytes,
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
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
			if err := dec.Decode(&struct{}{}); err != io.EOF {
				return nil, fmt.Errorf("parse config %s: trailing data", path)
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
	host := strings.TrimSpace(c.Web.ListenHost)
	if host == "" {
		host = defaultListenHost
	}
	// Accept both "::1" and "[::1]" in the config; normalize IPv6 literals to
	// the bracketed form expected by net/http's Host:Port address.
	if !(strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")) && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	c.Web.ListenHost = host
	if c.Web.ListenPort == 0 {
		c.Web.ListenPort = defaultListenPort
	}
	if c.Web.ListenPort < 1 || c.Web.ListenPort > 65535 {
		return fmt.Errorf("web.listen_port out of range: %d", c.Web.ListenPort)
	}
	if c.Policy.RoundLimit <= 0 {
		c.Policy.RoundLimit = defaultRoundLimit
	}
	if c.Policy.CmdTimeout <= 0 {
		c.Policy.CmdTimeout = defaultCmdTimeout
	}
	if c.Policy.MaxSessions <= 0 {
		c.Policy.MaxSessions = defaultMaxSessions
	}
	if c.Policy.QueueCapacity <= 0 {
		c.Policy.QueueCapacity = defaultQueueCapacity
	}
	if c.Policy.AuthMode < 0 || c.Policy.AuthMode > 2 {
		c.Policy.AuthMode = 0
	}
	if strings.TrimSpace(c.LLM.APIBase) == "" {
		c.LLM.APIBase = defaultAPIBase
	}
	if strings.TrimSpace(c.LLM.APIKey) == "" {
		c.LLM.APIKey = defaultAPIKey
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		c.LLM.Model = defaultModel
	}
	if c.LLM.SystemPrompt == "" {
		c.LLM.SystemPrompt = defaultSystemPrompt
	}
	if err := c.normalizeMCP(); err != nil {
		return err
	}
	return nil
}

var mcpServerNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// normalizeMCP applies defaults and validates the MCP section. A disabled or
// empty section is valid and leaves Localaw's behaviour unchanged.
func (c *Config) normalizeMCP() error {
	m := &c.MCP
	// A disabled section is ignored entirely: stale or experimental MCP
	// settings must not prevent Localaw from starting.
	if !m.Enabled {
		return nil
	}
	if m.InitTimeout <= 0 {
		m.InitTimeout = DefaultMCPInitTimeout
	}
	if m.ListTimeout <= 0 {
		m.ListTimeout = DefaultMCPListTimeout
	}
	if m.DefaultRisk == "" {
		m.DefaultRisk = "high"
	}
	m.DefaultRisk = strings.ToLower(strings.TrimSpace(m.DefaultRisk))
	if m.DefaultRisk != "low" && m.DefaultRisk != "high" {
		return fmt.Errorf("mcp.default_risk must be low or high: %q", m.DefaultRisk)
	}
	if m.DefaultTimeout < 0 {
		return fmt.Errorf("mcp.default_timeout must be >= 0")
	}
	if m.MaxTools <= 0 {
		m.MaxTools = DefaultMCPMaxTools
	}
	if m.MaxResultChars <= 0 {
		m.MaxResultChars = DefaultMCPMaxResultChars
	}
	if m.MaxDescriptionChars <= 0 {
		m.MaxDescriptionChars = DefaultMCPMaxDescriptionChars
	}
	if m.MaxSchemaBytes <= 0 {
		m.MaxSchemaBytes = DefaultMCPMaxSchemaBytes
	}

	seen := make(map[string]struct{}, len(m.Servers))
	for i := range m.Servers {
		s := &m.Servers[i]
		s.Name = strings.TrimSpace(s.Name)
		if !mcpServerNameRe.MatchString(s.Name) {
			return fmt.Errorf("mcp.servers[%d].name must match [A-Za-z0-9_-]{1,32}: %q", i, s.Name)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("mcp.servers: duplicate server name %q", s.Name)
		}
		seen[s.Name] = struct{}{}

		// Expand URLs before validation/inference so an unresolved variable is
		// reported as a configuration error instead of failing at connect time.
		s.URL = os.ExpandEnv(s.URL)

		s.Transport = strings.ToLower(strings.TrimSpace(s.Transport))
		if s.Transport == "" {
			switch {
			case s.Command != "":
				s.Transport = "stdio"
			case s.URL != "":
				s.Transport = "http"
			}
		}
		switch s.Transport {
		case "stdio":
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("mcp server %q: command is required for stdio", s.Name)
			}
		case "http", "sse":
			if strings.TrimSpace(s.URL) == "" {
				return fmt.Errorf("mcp server %q: url is required for %s", s.Name, s.Transport)
			}
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("mcp server %q: url must be a valid http(s) URL", s.Name)
			}
		default:
			return fmt.Errorf("mcp server %q: unknown transport %q (use stdio|http|sse)", s.Name, s.Transport)
		}

		if s.ConnectTimeout <= 0 {
			s.ConnectTimeout = 10
		}
		if s.CallTimeout < 0 {
			return fmt.Errorf("mcp server %q: call_timeout must be >= 0", s.Name)
		}
		s.DefaultRisk = strings.ToLower(strings.TrimSpace(s.DefaultRisk))
		if s.DefaultRisk == "" {
			s.DefaultRisk = m.DefaultRisk
		}
		if s.DefaultRisk != "low" && s.DefaultRisk != "high" {
			return fmt.Errorf("mcp server %q: default_risk must be low or high", s.Name)
		}
		for tool, p := range s.Tools {
			if p.Risk != "" {
				p.Risk = strings.ToLower(strings.TrimSpace(p.Risk))
				if p.Risk != "low" && p.Risk != "high" {
					return fmt.Errorf("mcp server %q tool %q: risk must be low or high", s.Name, tool)
				}
			}
			if p.Timeout < 0 {
				return fmt.Errorf("mcp server %q tool %q: timeout must be >= 0", s.Name, tool)
			}
			s.Tools[tool] = p
		}

		// Expand ${VAR} for secrets kept outside the config file.
		for k, v := range s.Env {
			s.Env[k] = os.ExpandEnv(v)
		}
		for k, v := range s.Headers {
			s.Headers[k] = os.ExpandEnv(v)
		}

		if s.Restart.Enabled == nil {
			on := true
			s.Restart.Enabled = &on
		}
		if s.Restart.MaxAttempts < 0 {
			s.Restart.MaxAttempts = 0
		}
		if s.Restart.MaxAttempts == 0 {
			s.Restart.MaxAttempts = 10
		}
		if s.Restart.MinBackoff <= 0 {
			s.Restart.MinBackoff = 1
		}
		if s.Restart.MaxBackoff <= 0 {
			s.Restart.MaxBackoff = 60
		}
		if s.Restart.MaxBackoff < s.Restart.MinBackoff {
			return fmt.Errorf("mcp server %q: restart.max_backoff must be >= min_backoff", s.Name)
		}
		if s.Enabled == nil {
			on := true
			s.Enabled = &on
		}
		if s.InheritEnv == nil {
			off := false
			s.InheritEnv = &off
		}
	}
	return nil
}
