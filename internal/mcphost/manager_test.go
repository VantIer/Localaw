package mcphost

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

func echoHandler(_ context.Context, _ *mcpsdk.CallToolRequest, in echoInput) (*mcpsdk.CallToolResult, any, error) {
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: in.Text}},
	}, nil, nil
}

func testManager(t *testing.T, transport mcpsdk.Transport) *Manager {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	body := `{"mcp":{"enabled":true,"init_timeout":2,"servers":[{"name":"test","command":"test-server","connect_timeout":2,"call_timeout":5}]}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg.MCP, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.transportFactory = func(config.MCPServer) (mcpsdk.Transport, io.Closer, error) {
		return transport, nil, nil
	}
	return m
}

func TestManagerListsAndCallsTools(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echo text"}, echoHandler)
	serverT, clientT := mcpsdk.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx, serverT) }()

	m := testManager(t, clientT)
	m.Start(context.Background())
	defer m.Close()

	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Exposed != "mcp__test__echo" {
		t.Fatalf("exposed name = %q", tools[0].Exposed)
	}
	if tools[0].LowRisk {
		t.Fatal("MCP tool must default to high risk")
	}

	out, err := m.Call(context.Background(), tools[0].Exposed, map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hi" {
		t.Fatalf("call output = %q", out)
	}

	// Adding a tool on the server must trigger tools/list_changed.
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo2", Description: "echo text twice"}, echoHandler)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := m.Lookup("mcp__test__echo2"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tools/list_changed was not applied")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestManagerLegacySSE(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "sse-test", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echo text"}, echoHandler)
	httpSrv := httptest.NewServer(mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil))
	defer httpSrv.Close()

	p := filepath.Join(t.TempDir(), "c.json")
	body := `{"mcp":{"enabled":true,"init_timeout":5,"servers":[{"name":"legacy","transport":"sse","url":"` + httpSrv.URL + `","connect_timeout":5}]}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg.MCP, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start(context.Background())
	defer m.Close()

	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1; status=%+v", len(tools), m.Servers())
	}
	out, err := m.Call(context.Background(), tools[0].Exposed, map[string]any{"text": "sse"})
	if err != nil || out != "sse" {
		t.Fatalf("call = %q, err = %v", out, err)
	}
}

func TestManagerHTTPHeaders(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "auth-test", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echo text"}, echoHandler)
	base := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer httpSrv.Close()

	p := filepath.Join(t.TempDir(), "c.json")
	body := `{"mcp":{"enabled":true,"init_timeout":5,"servers":[{"name":"auth","transport":"http","url":"` + httpSrv.URL + `","connect_timeout":5,"headers":{"Authorization":"Bearer secret"}}]}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg.MCP, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start(context.Background())
	defer m.Close()

	if tools := m.Tools(); len(tools) != 1 {
		t.Fatalf("tools = %d, want 1; status=%+v", len(tools), m.Servers())
	}
}

func TestRebuildIndexCollisionsAndCap(t *testing.T) {
	m := &Manager{cfg: config.MCP{MaxTools: 2}, index: map[string]Tool{}}
	m.servers = []*serverConn{
		{cfg: config.MCPServer{Name: "s"}, tools: []Tool{
			{Server: "s", Name: "a.b"},
			{Server: "s", Name: "a_b"},
			{Server: "s", Name: "z"},
		}},
	}
	m.rebuildIndex()
	if len(m.order) != 2 {
		t.Fatalf("tool cap not applied: %d", len(m.order))
	}
	seen := map[string]bool{}
	for _, tool := range m.order {
		if seen[tool.Exposed] {
			t.Fatalf("duplicate exposed name: %q", tool.Exposed)
		}
		seen[tool.Exposed] = true
		if len(tool.Exposed) > maxToolName {
			t.Fatalf("too long: %q", tool.Exposed)
		}
	}
}

func TestManagerUnavailableToolCall(t *testing.T) {
	m := NewManager(config.MCP{}, nil)
	out, err := m.Call(context.Background(), "mcp__missing__tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatal("expected an error result string")
	}
}
