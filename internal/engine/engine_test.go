package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go"

	"localaw/internal/config"
	"localaw/internal/llm"
	"localaw/internal/local"
)

// fakeLLM returns a write_file tool call on the first turn and a plain
// completion afterwards.
type fakeLLM struct {
	mu   sync.Mutex
	n    int
	path string
}

func (f *fakeLLM) Chat(ctx context.Context, messages []llm.Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*llm.ChatResult, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == 1 {
		args := `{"path":` + jsonString(f.path) + `,"content":"hi"}`
		calls := []llm.ToolCall{{ID: "c1", Name: "write_file", Arguments: args}}
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

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

func newTestEngine(t *testing.T, authMode int, path string) (*Engine, *local.Queue) {
	t.Helper()
	cfg := config.Default()
	cfg.Policy.AuthMode = authMode
	cfg.Policy.CmdTimeout = 5
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 8, 5*time.Second)
	return NewWithClient(cfg, q, &fakeLLM{path: path}), q
}

func waitIdle(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Running() {
		if time.Now().After(deadline) {
			t.Fatal("session did not become idle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func joinTranscript(s *Session) string {
	var b strings.Builder
	for _, e := range s.Snapshot().Transcript {
		b.WriteString(e.Role)
		b.WriteString(": ")
		b.WriteString(e.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestEngineExecutesToolCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	eng, q := newTestEngine(t, 2, path)
	defer q.Close()

	s, err := eng.NewSession("test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatalf("begin chat: %v", err)
	}
	waitIdle(t, s)

	joined := joinTranscript(s)
	if !strings.Contains(joined, "Successfully wrote") {
		t.Fatalf("missing tool result:\n%s", joined)
	}
	if !strings.Contains(joined, "done") {
		t.Fatalf("missing final assistant message:\n%s", joined)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestEngineDenialNeverExecutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	eng, q := newTestEngine(t, 0, path)
	defer q.Close()

	s, err := eng.NewSession("test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatalf("begin chat: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for s.Snapshot().Pending == nil {
		if time.Now().After(deadline) {
			t.Fatal("authorization was never requested")
		}
		time.Sleep(10 * time.Millisecond)
	}
	eng.SubmitAuth(s.ID, false)
	waitIdle(t, s)

	joined := joinTranscript(s)
	if !strings.Contains(joined, "Denied") {
		t.Fatalf("missing denial marker:\n%s", joined)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("denied action must not execute")
	}
}

// execLLM always requests a long-running exec_cmd tool call.
type execLLM struct{ cmd string }

func (f *execLLM) Chat(ctx context.Context, messages []llm.Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*llm.ChatResult, error) {
	args := `{"command":` + jsonString(f.cmd) + `}`
	calls := []llm.ToolCall{{ID: "c1", Name: "exec_cmd", Arguments: args}}
	return &llm.ChatResult{ToolCalls: calls, Raw: llm.AssistantMessage("", calls)}, nil
}

func TestEngineStopDuringExecution(t *testing.T) {
	cmd := "sleep 5"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 6 127.0.0.1 >NUL"
	}
	cfg := config.Default()
	cfg.Policy.AuthMode = 2
	cfg.Policy.CmdTimeout = 30
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 8, 30*time.Second)
	eng := NewWithClient(cfg, q, &execLLM{cmd: cmd})
	defer q.Close()

	s, err := eng.NewSession("test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatalf("begin chat: %v", err)
	}

	// Wait until the command job is actually in flight.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := s.running
		job := s.job
		s.mu.Unlock()
		if !running {
			t.Fatal("session ended before the command started")
		}
		if job != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	eng.Stop(s.ID)
	waitIdle(t, s)

	tr := joinTranscript(s)
	if !strings.Contains(tr, "Stopped") {
		t.Fatalf("expected a clean stop, got:\n%s", tr)
	}
	if strings.Contains(tr, "context canceled") {
		t.Fatalf("cancellation leaked as an execution error:\n%s", tr)
	}

	// Every assistant tool_calls must have a matching tool result.
	s.mu.Lock()
	hist := append([]llm.Message(nil), s.history...)
	s.mu.Unlock()
	calls, results := 0, 0
	for _, m := range hist {
		if m.OfAssistant != nil {
			calls += len(m.OfAssistant.ToolCalls)
		}
		if m.OfTool != nil {
			results++
		}
	}
	if calls == 0 {
		t.Fatal("expected at least one tool call in history")
	}
	if calls != results {
		t.Fatalf("dangling tool calls: %d calls vs %d results", calls, results)
	}
}

// seqLLM replays a fixed sequence of turns, repeating the last one.
type seqLLM struct {
	mu    sync.Mutex
	n     int
	steps []seqStep
}

type seqStep struct {
	calls   []llm.ToolCall
	content string
}

func (s *seqLLM) Chat(ctx context.Context, messages []llm.Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*llm.ChatResult, error) {
	s.mu.Lock()
	i := s.n
	s.n++
	s.mu.Unlock()
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	st := s.steps[i]
	if onContent != nil && st.content != "" {
		onContent(st.content)
	}
	return &llm.ChatResult{Content: st.content, ToolCalls: st.calls, Raw: llm.AssistantMessage(st.content, st.calls)}, nil
}

func (s *seqLLM) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func historyCounts(s *Session) (toolCalls, toolResults int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.history {
		if m.OfAssistant != nil {
			toolCalls += len(m.OfAssistant.ToolCalls)
		}
		if m.OfTool != nil {
			toolResults++
		}
	}
	return
}

func newSeqEngine(t *testing.T, authMode int, client *seqLLM, timeout time.Duration) (*Engine, *local.Queue) {
	t.Helper()
	cfg := config.Default()
	cfg.Policy.AuthMode = authMode
	cfg.Policy.CmdTimeout = int(timeout / time.Second)
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 8, timeout)
	return NewWithClient(cfg, q, client), q
}

func TestEngineUnknownAction(t *testing.T) {
	client := &seqLLM{steps: []seqStep{
		{calls: []llm.ToolCall{{ID: "c1", Name: "bogus", Arguments: `{}`}}},
		{content: "done"},
	}}
	eng, q := newSeqEngine(t, 2, client, 5*time.Second)
	defer q.Close()
	s, _ := eng.NewSession("t")
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if tr := joinTranscript(s); !strings.Contains(tr, "unknown action") {
		t.Fatalf("missing unknown-action result:\n%s", tr)
	}
}

func TestEngineDenialSkipsRemaining(t *testing.T) {
	client := &seqLLM{steps: []seqStep{
		{calls: []llm.ToolCall{
			{ID: "c1", Name: "exec_cmd", Arguments: `{"command":"echo one"}`},
			{ID: "c2", Name: "exec_cmd", Arguments: `{"command":"echo two"}`},
		}},
		{content: "done"},
	}}
	eng, q := newSeqEngine(t, 0, client, 5*time.Second)
	defer q.Close()
	s, _ := eng.NewSession("t")
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Snapshot().Pending == nil {
		if time.Now().After(deadline) {
			t.Fatal("authorization never requested")
		}
		time.Sleep(10 * time.Millisecond)
	}
	eng.SubmitAuth(s.ID, false)
	waitIdle(t, s)
	if calls, results := historyCounts(s); calls != 2 || results != 2 {
		t.Fatalf("denied multi-call must answer every call: %d calls, %d results", calls, results)
	}
}

func TestEngineRoundLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Policy.AuthMode = 2
	cfg.Policy.RoundLimit = 2
	client := &seqLLM{steps: []seqStep{
		{calls: []llm.ToolCall{{ID: "c1", Name: "get_cwd"}}},
	}}
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 8, 5*time.Second)
	eng := NewWithClient(cfg, q, client)
	defer q.Close()
	s, _ := eng.NewSession("t")
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if n := client.calls(); n != cfg.Policy.RoundLimit {
		t.Fatalf("llm called %d times, want %d", n, cfg.Policy.RoundLimit)
	}
}

func TestEngineExecTimeout(t *testing.T) {
	cmd := "sleep 5"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 6 127.0.0.1 >NUL"
	}
	client := &seqLLM{steps: []seqStep{
		{calls: []llm.ToolCall{{ID: "c1", Name: "exec_cmd", Arguments: `{"command":` + jsonString(cmd) + `}`}}},
	}}
	eng, q := newSeqEngine(t, 2, client, time.Second)
	defer q.Close()
	s, _ := eng.NewSession("t")
	if err := eng.BeginChat(s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if tr := joinTranscript(s); !strings.Contains(tr, "timed out") {
		t.Fatalf("missing timeout error:\n%s", tr)
	}
}

func TestSetAuthModeValidation(t *testing.T) {
	eng, q := newTestEngine(t, 0, "")
	defer q.Close()
	eng.SetAuthMode(2)
	if eng.AuthMode() != 2 {
		t.Fatal("auth mode not set")
	}
	eng.SetAuthMode(9)
	if eng.AuthMode() != 2 {
		t.Fatal("invalid auth mode must be ignored")
	}
}
