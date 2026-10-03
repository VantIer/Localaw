package llm

import (
	"testing"

	"localaw/internal/command"
)

func TestBuildToolsMatchesSpecs(t *testing.T) {
	tools := BuildTools()
	if len(tools) != len(command.All()) {
		t.Fatalf("tool count = %d, want %d", len(tools), len(command.All()))
	}
	var write *struct {
		desc  string
		props map[string]any
	}
	for _, tl := range tools {
		if tl.Function.Name == "write_file" {
			write = &struct {
				desc  string
				props map[string]any
			}{desc: tl.Function.Description.Value, props: tl.Function.Parameters}
		}
	}
	if write == nil {
		t.Fatal("write_file tool missing")
	}
	if write.desc == "" {
		t.Fatal("write_file tool has no description")
	}
	if _, ok := write.props["properties"]; !ok {
		t.Fatal("write_file tool has no properties")
	}
}

func TestToolAccumulatorReusedIndex(t *testing.T) {
	// Endpoint reuses index 0 and only sends id/name on each call's first
	// fragment; the continuation fragments must not leak into the previous call.
	ta := newToolAccumulator()
	ta.add(toolDelta{Index: 0, ID: "idA", Name: "exec_cmd", Args: `{"command":"a`})
	ta.add(toolDelta{Index: 0, Args: `"}`})
	ta.add(toolDelta{Index: 0, ID: "idB", Name: "write_file", Args: `{"path":"b`})
	ta.add(toolDelta{Index: 0, Args: `"}`})

	calls := ta.result()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].ID != "idA" || calls[0].Name != "exec_cmd" || calls[0].Arguments != `{"command":"a"}` {
		t.Fatalf("call 0 = %+v", calls[0])
	}
	if calls[1].ID != "idB" || calls[1].Name != "write_file" || calls[1].Arguments != `{"path":"b"}` {
		t.Fatalf("call 1 = %+v", calls[1])
	}
}

func TestToolAccumulatorDistinctIndices(t *testing.T) {
	ta := newToolAccumulator()
	ta.add(toolDelta{Index: 0, ID: "a", Name: "get_cwd"})
	ta.add(toolDelta{Index: 1, ID: "b", Name: "exec_cmd", Args: `{"command":"ls"}`})
	calls := ta.result()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(calls))
	}
	if calls[1].Name != "exec_cmd" || calls[1].Arguments != `{"command":"ls"}` {
		t.Fatalf("call 1 = %+v", calls[1])
	}
}

func TestAssistantMessageRoundTrip(t *testing.T) {
	calls := []ToolCall{{ID: "call-1", Name: "exec_cmd", Arguments: `{"command":"ls"}`}}
	m := AssistantMessage("hi", calls)
	if m.OfAssistant == nil {
		t.Fatal("expected an assistant message")
	}
	if len(m.OfAssistant.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(m.OfAssistant.ToolCalls))
	}
	if m.OfAssistant.ToolCalls[0].Function.Name != "exec_cmd" {
		t.Fatalf("tool name = %q", m.OfAssistant.ToolCalls[0].Function.Name)
	}
}
