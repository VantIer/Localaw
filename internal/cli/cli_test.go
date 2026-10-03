package cli

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"localaw/internal/config"
	"localaw/internal/engine"
	"localaw/internal/local"
)

func newTestCLI(t *testing.T) *CLI {
	t.Helper()
	cfg := config.Default()
	q := local.NewQueue(local.NewExecutor(local.NewFileModule()), 4, time.Second)
	t.Cleanup(q.Close)
	eng := engine.New(cfg, q)
	return &CLI{cfg: cfg, eng: eng, reader: bufio.NewReader(strings.NewReader(""))}
}

func TestCommandQuit(t *testing.T) {
	if !newTestCLI(t).command("/quit") {
		t.Fatal("/quit should stop the loop")
	}
}

func TestCommandHelpAndUnknown(t *testing.T) {
	c := newTestCLI(t)
	if c.command("/help") {
		t.Fatal("/help should not stop the loop")
	}
	if c.command("/nonsense") {
		t.Fatal("unknown command should not stop the loop")
	}
}

func TestCommandAuth(t *testing.T) {
	c := newTestCLI(t)
	c.command("/auth 2")
	if c.eng.AuthMode() != 2 {
		t.Fatalf("auth mode = %d", c.eng.AuthMode())
	}
}

func TestCommandNewAndUseSession(t *testing.T) {
	c := newTestCLI(t)
	c.command("/new first")
	first := c.eng.ActiveSession()
	if first == nil {
		t.Fatal("session was not created")
	}
	c.command("/new second")
	second := c.eng.ActiveSession()
	if second == nil || second.ID == first.ID {
		t.Fatal("second session was not activated")
	}
	c.command("/use 0")
	if got := c.eng.ActiveSession(); got == nil || got.ID != first.ID {
		t.Fatal("/use 0 did not switch back to the first session")
	}
}
