package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientChatOnce(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "key", "m", 0.2, false)
	res, err := c.Chat(context.Background(), []Message{UserMessage("hi")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" {
		t.Fatalf("content = %q", res.Content)
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
