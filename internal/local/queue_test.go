package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestQueueSubmit(t *testing.T) {
	f := NewFileModule()
	q := NewQueue(NewExecutor(f), 4, 5*time.Second)
	defer q.Close()

	path := filepath.Join(t.TempDir(), "x.txt")
	out, err := q.Submit(context.Background(), "write_file", map[string]any{"path": path, "content": "hi"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if out == "" {
		t.Fatal("expected a non-empty result")
	}
}

func TestQueueTimeoutKillsProcess(t *testing.T) {
	cmd := "sleep 5"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 6 127.0.0.1 >NUL"
	}
	q := NewQueue(NewExecutor(NewFileModule()), 4, time.Second)
	defer q.Close()

	start := time.Now()
	_, err := q.Submit(context.Background(), "exec_cmd", map[string]any{"command": cmd})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if _, ok := err.(*TimeoutError); !ok {
		t.Fatalf("expected *TimeoutError, got %T: %v", err, err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

// A completed file operation must not be relabelled as timed out or cancelled
// just because the job's context expired while it ran (file ops ignore ctx).
func TestExecJobDoesNotMaskCompletedFileOp(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(NewExecutor(NewFileModule()), 4, time.Second)
	defer q.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx is done, but the Job itself is never marked cancelled.

	p := filepath.Join(dir, "x.txt")
	j := NewJob(ctx, "write_file", map[string]any{"path": p, "content": "ok"}, "")
	q.execJob(j)

	r := <-j.Result
	if r.Err != nil {
		t.Fatalf("completed file op reported %v", r.Err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != "ok" {
		t.Fatalf("file content = %q, err = %v", got, err)
	}
}
