package engine

import (
	"encoding/json"

	"localaw/internal/mcphost"
	"localaw/internal/toolname"
	"localaw/internal/toolreg"
)

// actionDetail renders the parameter detail shown next to an action's name.
func actionDetail(action string, params map[string]any) string {
	if v, ok := params["command"].(string); ok && v != "" {
		return " [" + v + "]"
	}
	if v, ok := params["path"].(string); ok && v != "" {
		return " [" + v + "]"
	}
	if v, ok := params["src"].(string); ok && v != "" {
		return " [" + v + "]"
	}
	if toolname.IsExposed(action) {
		if data, err := json.Marshal(params); err == nil {
			detail := string(data)
			if r := []rune(detail); len(r) > 200 {
				detail = string(r[:200]) + "..."
			}
			return " " + detail
		}
	}
	return ""
}

func (e *Engine) displayName(action string) string {
	if d, ok := e.reg.Get(action); ok {
		return d.DisplayName()
	}
	return action
}

// toolEvent annotates an event with display-friendly MCP metadata without
// changing the stable `action` field used by the API.
func (e *Engine) toolEvent(action string, ev Event) Event {
	if d, ok := e.reg.Get(action); ok {
		ev["source"] = d.Source
		ev["display"] = d.DisplayName()
		if d.Source == toolreg.SourceMCP {
			ev["server"] = mcphost.SafeName(d.Server)
			ev["tool"] = mcphost.SafeName(d.MCPName)
			if d.Title != "" {
				ev["title"] = d.Title
			}
		}
	}
	return ev
}

func (e *Engine) pendingFor(action string, params map[string]any) *PendingCommand {
	p := &PendingCommand{Action: action, Params: params}
	if d, ok := e.reg.Get(action); ok {
		p.Source = d.Source
		p.Server = mcphost.SafeName(d.Server)
		p.Tool = mcphost.SafeName(d.MCPName)
		p.Title = d.Title
	}
	return p
}
