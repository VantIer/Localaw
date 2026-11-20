package local

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestQueueRunAfterCloseReturnsClosed(t *testing.T) {
	q := NewQueue(NewExecutor(NewFileModule()), 4, time.Second)
	q.Close()
	if _, err := q.Submit(context.Background(), "get_cwd", nil); err != ErrClosed {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestQueueCloseCancelsCurrentJob(t *testing.T) {
	cmd := "sleep 5"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 6 127.0.0.1 >NUL"
	}
	q := NewQueue(NewExecutor(NewFileModule()), 4, 10*time.Second)
	job := NewJob(context.Background(), "exec_cmd", map[string]any{"command": cmd}, "")

	done := make(chan struct{})
	go func() {
		_, _ = q.Run(job)
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		q.mu.Lock()
		current := q.current
		q.mu.Unlock()
		if current != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	q.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queue did not return after Close")
	}
	if !job.Cancelled() {
		t.Fatal("Close did not cancel the current job")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Close took too long: %s", elapsed)
	}
}
