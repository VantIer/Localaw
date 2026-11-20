package mcphost

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
)

// TestStdioHelper is a self-exec MCP server used by TestManagerStdio. It only
// runs when started by the manager with the helper environment variable.
func TestStdioHelper(t *testing.T) {
	if os.Getenv("LOCALAW_MCP_STDIO_HELPER") != "1" {
		t.Skip("stdio helper")
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-helper", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "echo text"}, echoHandler)
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestManagerStdio(t *testing.T) {
	cfg := config.MCP{
		Enabled:             true,
		InitTimeout:         5,
		ListTimeout:         5,
		DefaultRisk:         "high",
		MaxTools:            128,
		MaxResultChars:      51200,
		MaxDescriptionChars: 2000,
		MaxSchemaBytes:      32 << 10,
		Servers: []config.MCPServer{{
			Name:           "stdio",
			Transport:      "stdio",
			Command:        os.Args[0],
			Args:           []string{"-test.run=TestStdioHelper"},
			Env:            map[string]string{"LOCALAW_MCP_STDIO_HELPER": "1"},
			ConnectTimeout: 5,
			DefaultRisk:    "high",
			Restart: config.RestartPolicy{
				MaxAttempts: 1,
				MinBackoff:  1,
				MaxBackoff:  1,
			},
		}},
	}
	m := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start(context.Background())
	defer m.Close()

	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Exposed != "mcp__stdio__echo" {
		t.Fatalf("exposed = %q", tools[0].Exposed)
	}
	out, err := m.Call(context.Background(), tools[0].Exposed, map[string]any{"text": "stdio"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "stdio" {
		t.Fatalf("output = %q", out)
	}

	// Closing must terminate the child process without waiting for the test
	// timeout.
	start := time.Now()
	m.Close()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("close took %s", elapsed)
	}
}
