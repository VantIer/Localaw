package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go"

	"localaw/internal/config"
	"localaw/internal/llm"
	"localaw/internal/local"
)

type blockingLLM struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingLLM) Chat(ctx context.Context, _ []llm.Message, _ []openai.ChatCompletionToolParam, _ func(string)) (*llm.ChatResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEngineCloseStopsRunningSession(t *testing.T) {
	cfg := config.Default()
	cfg.Policy.AuthMode = 2
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 4, time.Second)
	client := &blockingLLM{started: make(chan struct{})}
	eng := newWithClient(cfg, q, client)
	defer q.Close()

	s, err := eng.NewSession("running")
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.BeginChat(s.ID, "wait"); err != nil {
		t.Fatal(err)
	}
	<-client.started

	done := make(chan struct{})
	go func() {
		eng.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Engine.Close did not stop a running session")
	}
	if s.Running() {
		t.Fatal("session is still running after Engine.Close")
	}
}

func TestEngineCloseRejectsNewWork(t *testing.T) {
	eng, q := newTestEngine(t, 2, "")
	defer q.Close()

	s, err := eng.NewSession("test")
	if err != nil {
		t.Fatal(err)
	}
	eng.Close()

	if _, err := eng.NewSession("after-close"); err == nil {
		t.Fatal("NewSession after Close should fail")
	}
	if err := eng.BeginChat(s.ID, "hello"); err == nil {
		t.Fatal("BeginChat after Close should fail")
	}
}
