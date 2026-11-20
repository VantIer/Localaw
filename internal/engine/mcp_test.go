package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go"

	"localaw/internal/config"
	"localaw/internal/llm"
	"localaw/internal/local"
	"localaw/internal/mcphost"
)

type mcpEchoInput struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

func mcpEchoHandler(_ context.Context, _ *mcpsdk.CallToolRequest, in mcpEchoInput) (*mcpsdk.CallToolResult, any, error) {
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo:" + in.Text}},
	}, nil, nil
}

type mcpFakeLLM struct {
	mu   sync.Mutex
	n    int
	name string
}

func (f *mcpFakeLLM) Chat(ctx context.Context, messages []llm.Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*llm.ChatResult, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == 1 {
		found := false
		for _, t := range tools {
			if t.Function.Name == f.name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("tool %s was not advertised", f.name)
		}
		calls := []llm.ToolCall{{
			ID:        "call-mcp-1",
			Name:      f.name,
			Arguments: `{"text":"hello"}`,
		}}
		if onContent != nil {
			onContent("working")
		}
		return &llm.ChatResult{Content: "working", ToolCalls: calls, Raw: llm.AssistantMessage("working", calls)}, nil
	}
	if onContent != nil {
		onContent("done")
	}
	return &llm.ChatResult{Content: "done", Raw: llm.AssistantMessage("done", nil)}, nil
}

func TestEngineExecutesMCPTool(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echo text"}, mcpEchoHandler)

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	p := filepath.Join(t.TempDir(), "c.json")
	body := `{"mcp":{"enabled":true,"init_timeout":5,"servers":[{"name":"test","transport":"http","url":"` + httpSrv.URL + `","connect_timeout":5,"call_timeout":10}]},"policy":{"auth_mode":2}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}

	host := mcphost.NewManager(cfg.MCP, slog.New(slog.NewTextHandler(io.Discard, nil)))
	host.Start(context.Background())
	defer host.Close()

	file := local.NewFileModule()
	q := local.NewQueue(local.NewExecutor(file, host), 8, 5*time.Second)
	defer q.Close()

	client := &mcpFakeLLM{name: "mcp__test__echo"}
	eng := newWithClientAndMCP(cfg, q, client, host)
	s, err := eng.NewSession("mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.BeginChat(s.ID, "use mcp"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	joined := joinTranscript(s)
	if !strings.Contains(joined, "echo:hello") {
		t.Fatalf("MCP result missing:\n%s", joined)
	}
	if !strings.Contains(joined, "done") {
		t.Fatalf("final assistant message missing:\n%s", joined)
	}
}
