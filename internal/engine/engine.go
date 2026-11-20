// Package engine orchestrates multi-session LLM conversations, authorization,
// round limits and event broadcasting on top of the local command queue.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go"

	"localaw/internal/config"
	"localaw/internal/llm"
	"localaw/internal/local"
	"localaw/internal/mcphost"
	"localaw/internal/toolreg"
)

const (
	// sessionGracePeriod bounds how long Close/Reset wait for a running
	// conversation (including non-cancellable file operations).
	sessionGracePeriod = 5 * time.Second
)

// LLMChatter is the subset of llm.Client used by the engine.
type LLMChatter interface {
	Chat(ctx context.Context, messages []llm.Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*llm.ChatResult, error)
}

// Engine owns sessions and drives conversations.
type Engine struct {
	cfg   *config.Config
	llm   LLMChatter
	reg   *toolreg.Registry
	queue *local.Queue
	hub   *eventHub

	mu       sync.RWMutex
	sessions map[string]*Session
	active   string
	seq      uint64
	closed   bool

	authMu   sync.Mutex
	authMode int
}

// NewWithMCP creates an engine with an attached MCP host.
func NewWithMCP(cfg *config.Config, queue *local.Queue, host *mcphost.Manager) *Engine {
	client := llm.New(cfg.LLM.APIBase, cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.Temperature, cfg.LLM.Stream)
	return newWithRegistry(cfg, queue, client, toolreg.New(host))
}

// newWithClient creates an engine with an injected chat client (test seam).
func newWithClient(cfg *config.Config, queue *local.Queue, client LLMChatter) *Engine {
	return newWithRegistry(cfg, queue, client, toolreg.New(nil))
}

// newWithClientAndMCP creates an engine with an injected chat client and MCP
// host (test seam).
func newWithClientAndMCP(cfg *config.Config, queue *local.Queue, client LLMChatter, host *mcphost.Manager) *Engine {
	return newWithRegistry(cfg, queue, client, toolreg.New(host))
}

func newWithRegistry(cfg *config.Config, queue *local.Queue, client LLMChatter, reg *toolreg.Registry) *Engine {
	return &Engine{
		cfg:      cfg,
		llm:      client,
		reg:      reg,
		queue:    queue,
		hub:      newEventHub(),
		sessions: map[string]*Session{},
		authMode: cfg.Policy.AuthMode,
	}
}

// NewSession creates a conversation session and makes it active if none is.
func (e *Engine) NewSession(title string) (*Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.newSessionLocked(title)
}

func (e *Engine) newSessionLocked(title string) (*Session, error) {
	if e.closed {
		return nil, fmt.Errorf("engine closed")
	}
	if len(e.sessions) >= e.cfg.Policy.MaxSessions {
		return nil, fmt.Errorf("session limit reached (%d)", e.cfg.Policy.MaxSessions)
	}
	e.seq++
	id := fmt.Sprintf("s%d-%d", time.Now().UnixNano(), e.seq)
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Session %d", len(e.sessions)+1)
	}
	s := &Session{ID: id, Title: title, CreatedAt: time.Now(), phase: PhaseIdle}
	e.sessions[id] = s
	if e.active == "" {
		e.active = id
	}
	return s, nil
}

// Close stops all sessions, waits for their goroutines with a bounded deadline,
// and closes event subscribers.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	sessions := make([]*Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		sessions = append(sessions, s)
	}
	e.mu.Unlock()

	for _, s := range sessions {
		e.stopSession(s)
	}
	deadline := time.Now().Add(sessionGracePeriod)
	for _, s := range sessions {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		e.waitRun(s, remaining)
	}
	e.hub.close()
}

// EnsureSession returns the active session, creating one if none exists.
func (e *Engine) EnsureSession() (*Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, fmt.Errorf("engine closed")
	}
	if id := e.active; id != "" {
		if s := e.sessions[id]; s != nil {
			return s, nil
		}
	}
	return e.newSessionLocked("")
}

// GetSession returns a session by id.
func (e *Engine) GetSession(id string) *Session {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessions[id]
}

// ListSessions returns all sessions, oldest first.
func (e *Engine) ListSessions() []*Session {
	e.mu.RLock()
	out := make([]*Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, s)
	}
	e.mu.RUnlock()
	// Tie-break by id so sessions created within the same clock tick keep a
	// deterministic order (Go's map iteration is randomized).
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ActiveSession returns the active session.
func (e *Engine) ActiveSession() *Session {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessions[e.active]
}

// ActiveSessionID returns the active session id (possibly empty).
func (e *Engine) ActiveSessionID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.active
}

// SetActiveSession switches the active session.
func (e *Engine) SetActiveSession(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sessions[id]; !ok {
		return false
	}
	e.active = id
	return true
}

// CloseSession stops and removes a session. It returns true when the
// conversation stopped within the grace period, and false when the session was
// removed while a non-cancellable task (e.g. a large file copy) was still
// finishing in the background. The session is removed either way so it never
// lingers in the UI.
func (e *Engine) CloseSession(id string) bool {
	s := e.GetSession(id)
	if s == nil {
		return false
	}
	e.stopSession(s)
	clean := e.waitRun(s, sessionGracePeriod)
	e.mu.Lock()
	delete(e.sessions, id)
	if e.active == id {
		e.active = ""
		var oldest *Session
		for _, cand := range e.sessions {
			if oldest == nil || cand.CreatedAt.Before(oldest.CreatedAt) ||
				(cand.CreatedAt.Equal(oldest.CreatedAt) && cand.ID < oldest.ID) {
				oldest = cand
			}
		}
		if oldest != nil {
			e.active = oldest.ID
		}
	}
	e.mu.Unlock()
	return clean
}

// BeginChat starts the conversation loop for a session with a user message.
func (e *Engine) BeginChat(sessionID, message string) error {
	// Hold the engine read lock through session setup so Close cannot set
	// closed after we checked it but before the session becomes running.
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return fmt.Errorf("engine closed")
	}
	s := e.sessions[sessionID]
	if s == nil {
		return fmt.Errorf("no such session: %s", sessionID)
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("session is already running")
	}
	if s.stop {
		s.mu.Unlock()
		return fmt.Errorf("session is stopped; reset it first")
	}
	s.running = true
	s.runCtx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	s.history = append(s.history, llm.UserMessage(message))
	s.transcript = append(s.transcript, TranscriptEntry{Role: "user", Text: message})
	s.seq++
	s.mu.Unlock()
	go e.runConversation(s)
	return nil
}

// Stop interrupts a running conversation.
func (e *Engine) Stop(sessionID string) {
	if s := e.GetSession(sessionID); s != nil {
		e.stopSession(s)
	}
}

func (e *Engine) stopSession(s *Session) {
	s.mu.Lock()
	if !s.running {
		// Never arm the stop flag on an idle session: only a running
		// conversation's finish() clears it, so stopping an idle session would
		// otherwise wedge it until an explicit reset.
		s.mu.Unlock()
		return
	}
	s.stop = true
	cancel := s.cancel
	job := s.job
	authCh := s.authCh
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if job != nil {
		job.Cancel()
	}
	// Drop any of this session's jobs still waiting in the queue so the local
	// environment is left clean after a stop.
	e.queue.Clear(s.ID)
	if authCh != nil {
		select {
		case authCh <- false:
		default:
		}
	}
}

// ResetConversation clears a session and re-arms it. It returns true when the
// reset was applied immediately. If the session is still finishing a
// non-cancellable task after the grace period, the reset is scheduled
// (resetPending) and applied by finish(), and false is returned.
func (e *Engine) ResetConversation(sessionID string) bool {
	s := e.GetSession(sessionID)
	if s == nil {
		return false
	}
	e.stopSession(s)
	if !e.waitRun(s, sessionGracePeriod) {
		s.mu.Lock()
		s.resetPending = true
		s.mu.Unlock()
		return false
	}
	e.clearConversation(s)
	return true
}

// clearConversation wipes a session's history and re-arms it.
func (e *Engine) clearConversation(s *Session) {
	s.mu.Lock()
	s.history = nil
	s.transcript = nil
	s.stop = false
	s.phase = PhaseIdle
	s.text = ""
	s.pending = nil
	s.resetPending = false
	s.mu.Unlock()
}

// waitRun waits until a session's running goroutine (if any) finishes.
func (e *Engine) waitRun(s *Session, timeout time.Duration) bool {
	s.mu.Lock()
	runDone := s.done
	s.mu.Unlock()
	if runDone == nil {
		return true
	}
	select {
	case <-runDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// SubmitAuth delivers a user's authorization decision for a waiting session.
func (e *Engine) SubmitAuth(sessionID string, ok bool) bool {
	s := e.GetSession(sessionID)
	if s == nil {
		return false
	}
	s.mu.Lock()
	ch := s.authCh
	s.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- ok:
		return true
	default:
		return false
	}
}

// authWaitTimeout bounds how long a session waits for an authorization
// decision before treating it as denied.
const authWaitTimeout = 5 * time.Minute

// awaitAuth blocks until the user decides, the mode no longer requires
// authorization, the session stops, or the wait times out.
func (e *Engine) awaitAuth(s *Session, ch chan bool) bool {
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	deadline := time.NewTimer(authWaitTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		pc := s.pending
		stopped := s.stop
		s.mu.Unlock()
		if stopped || pc == nil {
			return false
		}
		if !e.reg.RequiresAuth(e.AuthMode(), pc.Action) {
			return true
		}
		select {
		case ok := <-ch:
			return ok
		case <-tick.C:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// AuthMode returns the current authorization mode.
func (e *Engine) AuthMode() int {
	e.authMu.Lock()
	defer e.authMu.Unlock()
	return e.authMode
}

// SetAuthMode switches the authorization mode.
func (e *Engine) SetAuthMode(mode int) {
	if mode < 0 || mode > 2 {
		return
	}
	e.authMu.Lock()
	e.authMode = mode
	e.authMu.Unlock()
}

// RequiresAuth reports whether an action needs authorization right now.
func (e *Engine) RequiresAuth(action string) bool {
	return e.reg.RequiresAuth(e.AuthMode(), action)
}

// MCPStatus returns the status of configured MCP servers (nil when MCP is off).
func (e *Engine) MCPStatus() []mcphost.ServerStatus {
	return e.reg.MCPStatus()
}

// RenderSystemPrompt fills {system_name} with the local OS.
func (e *Engine) RenderSystemPrompt() string {
	return strings.ReplaceAll(e.cfg.LLM.SystemPrompt, "{system_name}", osName())
}

func osName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	default:
		return runtime.GOOS
	}
}

// GetTranscript returns the display transcript of a session.
func (e *Engine) GetTranscript(sessionID string) []TranscriptEntry {
	s := e.GetSession(sessionID)
	if s == nil {
		return nil
	}
	return s.transcriptCopy()
}

// Subscribe registers an event subscriber for a session (empty matches all).
// The returned dropped channel is closed if the hub drops the subscriber.
func (e *Engine) Subscribe(sessionID string) (<-chan Event, <-chan struct{}, func()) {
	sub := e.hub.subscribe(sessionID)
	return sub.ch, sub.done, func() { e.hub.unsubscribe(sub) }
}

// RunDirect executes an operator shell command through the shared queue.
func (e *Engine) RunDirect(cmd string) (string, error) {
	return e.queue.Submit(context.Background(), "exec_cmd", map[string]any{"command": cmd})
}

func parseArgs(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}
