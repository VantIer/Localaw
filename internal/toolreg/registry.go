// Package toolreg is the single registry of tools exposed to the LLM: the
// built-in local actions plus the tools currently provided by MCP servers.
package toolreg

import (
	"github.com/openai/openai-go"

	"localaw/internal/command"
	"localaw/internal/mcphost"
)

// Source values for Def.Source.
const (
	SourceBuiltin = "builtin"
	SourceMCP     = "mcp"
)

// Def is one tool definition, independent of where it comes from.
type Def struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	LowRisk     bool
	Source      string
	Server      string // MCP only
	MCPName     string // MCP only (original tool name)
	Timeout     int    // seconds; 0 = inherit policy.cmd_timeout
}

// DisplayName is the human-readable label used in the transcript and UI.
func (d Def) DisplayName() string {
	if d.Source == SourceMCP {
		return "mcp:" + mcphost.SafeName(d.Server) + "/" + mcphost.SafeName(d.MCPName)
	}
	return d.Name
}

// Registry merges the static command table with the live MCP tool snapshot.
type Registry struct {
	host    *mcphost.Manager
	builtin []Def
	byName  map[string]Def
}

// New builds a registry. host may be nil (MCP disabled / tests).
func New(host *mcphost.Manager) *Registry {
	r := &Registry{host: host, byName: map[string]Def{}}
	for _, s := range command.All() {
		d := Def{
			Name:        s.Name,
			Description: s.Description,
			Schema:      command.Schema(s),
			LowRisk:     s.LowRisk,
			Source:      SourceBuiltin,
		}
		r.builtin = append(r.builtin, d)
		r.byName[d.Name] = d
	}
	return r
}

// Tools returns built-ins followed by the current MCP snapshot.
func (r *Registry) Tools() []Def {
	out := make([]Def, 0, len(r.builtin)+16)
	out = append(out, r.builtin...)
	if r.host != nil {
		for _, t := range r.host.Tools() {
			out = append(out, defFromMCP(t))
		}
	}
	return out
}

func defFromMCP(t mcphost.Tool) Def {
	return Def{
		Name:        t.Exposed,
		Title:       t.Title,
		Description: t.Description,
		Schema:      t.InputSchema,
		LowRisk:     t.LowRisk,
		Source:      SourceMCP,
		Server:      t.Server,
		MCPName:     t.Name,
		Timeout:     t.Timeout,
	}
}

// Get resolves a tool by exposed name.
func (r *Registry) Get(name string) (Def, bool) {
	if d, ok := r.byName[name]; ok {
		return d, true
	}
	if r.host != nil {
		if t, ok := r.host.Lookup(name); ok {
			return defFromMCP(t), true
		}
	}
	return Def{}, false
}

// OpenAITools builds the per-round tool schema for the chat request.
func (r *Registry) OpenAITools() []openai.ChatCompletionToolParam {
	defs := r.Tools()
	out := make([]openai.ChatCompletionToolParam, 0, len(defs))
	for _, d := range defs {
		out = append(out, openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        d.Name,
				Description: openai.String(d.Description),
				Parameters:  openai.FunctionParameters(d.Schema),
			},
		})
	}
	return out
}

// RequiresAuth mirrors the three authorization modes using each tool's
// resolved risk level.
func (r *Registry) RequiresAuth(mode int, name string) bool {
	d, ok := r.Get(name)
	return command.RequiresAuthRisk(mode, ok && d.LowRisk)
}

// MCPStatus returns MCP server status for the web panel / CLI.
func (r *Registry) MCPStatus() []mcphost.ServerStatus {
	if r.host == nil {
		return nil
	}
	return r.host.Servers()
}
