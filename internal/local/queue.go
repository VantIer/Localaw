package local

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// JobResult is the outcome of one queued operation.
type JobResult struct {
	Output string
	Err    error
}

// Job is one unit of work on the linear queue.
type Job struct {
	Action    string
	Params    map[string]any
	SessionID string // origin session; empty for operator direct commands
	Result    chan JobResult

	ctx       context.Context
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

// NewJob builds a job bound to ctx, deriving a cancellable context so the
// owning session can cancel it precisely.
func NewJob(ctx context.Context, action string, params map[string]any, sessionID string) *Job {
	j := &Job{
		Action:    action,
		Params:    params,
		SessionID: sessionID,
		Result:    make(chan JobResult, 1),
	}
	j.ctx, j.cancel = context.WithCancel(ctx)
	return j
}

// Cancel marks the job cancelled and cancels its context.
func (j *Job) Cancel() {
	j.cancelled.Store(true)
	if j.cancel != nil {
		j.cancel()
	}
}

// Cancelled reports whether the job was cancelled.
func (j *Job) Cancelled() bool { return j.cancelled.Load() }

// Errors returned by the queue.
var (
	ErrClosed    = errors.New("queue closed")
	ErrCancelled = errors.New("job cancelled")
)

// TimeoutError marks an execution that exceeded cmd_timeout.
type TimeoutError struct{ After time.Duration }

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("execution timed out after %s", e.After)
}

// Queue is a single linear FIFO queue that serializes every action (LLM tool
// calls and operator direct commands) on the local machine. Exactly one
// dispatcher goroutine reads q.jobs; Clear requests are routed through
// q.clearReqs and executed by that same goroutine, so the FIFO is never read
// concurrently.
type Queue struct {
	jobs      chan *Job
	clearReqs chan string
	exec      *Executor
	stop      chan struct{}
	once      sync.Once
	timeout   time.Duration
}

// NewQueue creates the queue and starts its dispatcher goroutine.
func NewQueue(exec *Executor, capacity int, timeout time.Duration) *Queue {
	if capacity <= 0 {
		capacity = 256
	}
	q := &Queue{
		jobs:      make(chan *Job, capacity),
		clearReqs: make(chan string, 16),
		exec:      exec,
		stop:      make(chan struct{}),
		timeout:   timeout,
	}
	go q.run()
	return q
}

// Submit enqueues an action and blocks for its result (convenience for
// session-independent commands such as operator direct commands).
func (q *Queue) Submit(ctx context.Context, action string, params map[string]any) (string, error) {
	return q.Run(NewJob(ctx, action, params, ""))
}

// Run enqueues j and blocks for its result. Queue waiting is not subject to any
// timeout; only execution is bounded by cmd_timeout. If the caller context is
// cancelled (session stop), the job is cancelled and ErrCancelled is returned.
// If the queue is closed after the job is enqueued, ErrClosed is returned so
// the caller can never block forever on a job the dispatcher will not run.
func (q *Queue) Run(j *Job) (string, error) {
	select {
	case q.jobs <- j:
	case <-q.stop:
		return "", ErrClosed
	case <-j.ctx.Done():
		return "", ErrCancelled
	}
	select {
	case r := <-j.Result:
		return r.Output, r.Err
	case <-q.stop:
		select {
		case r := <-j.Result:
			return r.Output, r.Err
		default:
			return "", ErrClosed
		}
	case <-j.ctx.Done():
		j.Cancel()
		return "", ErrCancelled
	}
}

// Clear cancels every job of the given session that is still waiting in the
// queue, so a stopped session leaves no residual command behind. The request is
// handed to the dispatcher goroutine, which is the sole reader of q.jobs; this
// keeps the FIFO intact and preserves other sessions' job order. A session has
// at most one job in flight at a time, so this normally finds nothing.
func (q *Queue) Clear(sessionID string) {
	if sessionID == "" {
		return
	}
	// Best-effort and never blocking: the session's own job is cancelled via
	// its context, so a dropped request cannot leave a command running.
	select {
	case q.clearReqs <- sessionID:
	default:
	}
}

// Close stops the queue and fails every pending job.
func (q *Queue) Close() {
	q.once.Do(func() { close(q.stop) })
}

func (q *Queue) run() {
	for {
		select {
		case <-q.stop:
			q.failPending()
			return
		case sid := <-q.clearReqs:
			// Run the surviving jobs here, in order, instead of pushing them
			// back into q.jobs: re-enqueueing from the sole consumer could
			// deadlock if the channel were full.
			for _, j := range q.clearSession(sid) {
				q.execJob(j)
			}
		case j := <-q.jobs:
			if j == nil {
				continue
			}
			q.execJob(j)
		}
	}
}

// clearSession drains q.jobs, failing the matching session's jobs and returning
// the rest in original order. It runs only on the dispatcher goroutine.
func (q *Queue) clearSession(sessionID string) []*Job {
	var kept []*Job
drain:
	for {
		select {
		case j := <-q.jobs:
			if j == nil {
				continue
			}
			if j.SessionID == sessionID {
				j.Cancel()
				j.Result <- JobResult{Err: ErrCancelled}
			} else {
				kept = append(kept, j)
			}
		default:
			break drain
		}
	}
	return kept
}

func (q *Queue) execJob(j *Job) {
	if j.Cancelled() {
		j.Result <- JobResult{Err: ErrCancelled}
		return
	}
	ctx := j.ctx
	if q.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.timeout)
		defer cancel()
	}
	out, err := q.exec.Execute(ctx, j.Action, j.Params)
	switch {
	case j.Cancelled():
		err = ErrCancelled
	case ctx.Err() == context.DeadlineExceeded:
		err = &TimeoutError{After: q.timeout}
	case ctx.Err() == context.Canceled:
		err = ErrCancelled
	}
	j.Result <- JobResult{Output: out, Err: err}
}

func (q *Queue) failPending() {
	for {
		select {
		case j := <-q.jobs:
			if j != nil {
				j.Result <- JobResult{Err: ErrClosed}
			}
		default:
			return
		}
	}
}
