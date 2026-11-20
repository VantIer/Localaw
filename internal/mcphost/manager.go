// Package mcphost implements the Model Context Protocol client side used by
// Localaw. Localaw consumes the tools exposed by configured MCP servers; it
// does not expose its own tools as an MCP server. Only the tools capability is
// supported (no resources, prompts, sampling or elicitation).
package mcphost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
)

const closeWait = 6 * time.Second

type serverState int

const (
	stateConnecting serverState = iota
	stateReady
	stateDegraded
	stateFailed
	stateClosed
)

func (s serverState) String() string {
	switch s {
	case stateConnecting:
		return "connecting"
	case stateReady:
		return "ready"
	case stateDegraded:
		return "degraded"
	case stateFailed:
		return "failed"
	case stateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// stageError records which connection stage failed, so the public status
// endpoint can expose a category instead of a potentially sensitive error
// string. The wrapped error is only written to local logs.
type stageError struct {
	kind string
	err  error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

func connectErrorKind(err error) string {
	var se *stageError
	if errors.As(err, &se) {
		return se.kind
	}
	return "connection failed"
}

// ServerStatus is a safe, credential-free view of one MCP server for the web
// status endpoint and the CLI.
type ServerStatus struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	State     string `json:"state"`
	Protocol  string `json:"protocol,omitempty"`
	Tools     int    `json:"tools"`
	// Error is a coarse category (connection failed, handshake failed,
	// tool list failed, tool call failed), never a raw error string.
	Error string `json:"error,omitempty"`
}

// Manager owns every configured MCP server, their tool snapshots and the
// exposed-name index shared by all Localaw sessions.
type Manager struct {
	cfg    config.MCP
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	servers []*serverConn
	index   map[string]Tool
	order   []Tool
	started bool
	closed  bool
	wg      sync.WaitGroup

	transportFactory func(config.MCPServer) (mcpsdk.Transport, io.Closer, error)
}

// NewManager creates a manager. It does not connect until Start is called.
func NewManager(cfg config.MCP, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:    cfg,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
		index:  map[string]Tool{},
	}
	m.transportFactory = func(sc config.MCPServer) (mcpsdk.Transport, io.Closer, error) {
		return defaultTransportFactory(sc, logger)
	}
	return m
}

// Start connects every enabled server in parallel and waits up to
// mcp.init_timeout (or ctx cancellation) for the initial attempts. Failures
// are non-fatal.
func (m *Manager) Start(ctx context.Context) {
	if !m.cfg.Enabled || len(m.cfg.Servers) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	var servers []*serverConn
	for i := range m.cfg.Servers {
		sc := m.cfg.Servers[i]
		if sc.Enabled != nil && !*sc.Enabled {
			continue
		}
		s := &serverConn{
			cfg:       sc,
			mgr:       m,
			state:     stateConnecting,
			firstTry:  make(chan struct{}),
			refreshCh: make(chan struct{}, 1),
		}
		m.servers = append(m.servers, s)
		m.wg.Add(1)
		servers = append(servers, s)
	}
	m.mu.Unlock()

	for _, s := range servers {
		go func(s *serverConn) {
			defer m.wg.Done()
			m.runServer(s)
		}(s)
	}

	deadline := time.Now().Add(time.Duration(m.cfg.InitTimeout) * time.Second)
	for _, s := range servers {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		select {
		case <-s.firstTry:
		case <-ctx.Done():
			return
		case <-time.After(remaining):
			return
		}
	}
}

// Close stops reconnection, closes every MCP session and waits for stdio
// processes to exit (with the transport's own terminate timeout).
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	servers := append([]*serverConn(nil), m.servers...)
	m.mu.Unlock()

	m.cancel()
	for _, s := range servers {
		s.closeCurrent()
	}
	// Give reconnection/refresh goroutines a bounded chance to observe the
	// cancellation and release their sessions.
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
	}
}

// Tools returns a snapshot of every currently exposed MCP tool, sorted by
// server then tool name.
func (m *Manager) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Tool, len(m.order))
	copy(out, m.order)
	return out
}

// Lookup resolves an exposed (OpenAI-safe) name.
func (m *Manager) Lookup(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.index[name]
	return t, ok
}

// Has reports whether an exposed tool currently exists.
func (m *Manager) Has(name string) bool {
	_, ok := m.Lookup(name)
	return ok
}

// Call invokes an MCP tool. MCP business/protocol failures are returned as an
// "Error: ..." result string with a nil error so the model can react; only
// context cancellation is returned as an error (so stop/timeout semantics stay
// consistent with the local queue).
func (m *Manager) Call(ctx context.Context, exposed string, args map[string]any) (string, error) {
	t, ok := m.Lookup(exposed)
	if !ok {
		return "Error: unknown MCP tool: " + exposed, nil
	}
	s := m.serverByName(t.Server)
	if s == nil {
		return "Error: MCP server " + t.Server + " is unavailable", nil
	}
	h, ok := s.currentSession()
	if !ok {
		return "Error: MCP server " + t.Server + " is unavailable", nil
	}

	res, err := h.sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.Name,
		Arguments: args,
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		s.setErrorKind("tool call failed")
		m.warnf("mcp server %s: tool %s failed: %v", t.Server, SafeName(t.Name), err)
		return fmt.Sprintf("Error: MCP server %s/%s failed: %v", t.Server, SafeName(t.Name), err), nil
	}
	s.clearLastErrorKind()
	return formatCallResult(res, m.cfg.MaxResultChars), nil
}

// Servers returns the status of every configured (enabled) server.
func (m *Manager) Servers() []ServerStatus {
	m.mu.RLock()
	servers := append([]*serverConn(nil), m.servers...)
	m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) serverByName(name string) *serverConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if s.cfg.Name == name {
			return s
		}
	}
	return nil
}

func (m *Manager) runServer(s *serverConn) {
	backoff := initialBackoff(s.cfg)
	maxBackoff := time.Duration(s.cfg.Restart.MaxBackoff) * time.Second
	if maxBackoff < backoff {
		maxBackoff = backoff
	}
	restart := s.cfg.Restart.Enabled == nil || *s.cfg.Restart.Enabled
	attempts := 0

	for {
		if m.ctx.Err() != nil {
			s.markClosed()
			return
		}
		s.setState(stateConnecting, "")
		h, err := m.connect(s)
		if err != nil {
			attempts++
			s.setState(stateFailed, connectErrorKind(err))
			s.signalFirstTry()
			if !restart || attempts >= s.cfg.Restart.MaxAttempts {
				m.warnf("mcp server %s: giving up after %d attempt(s): %v", s.cfg.Name, attempts, err)
				return
			}
			m.warnf("mcp server %s: connect failed (%v); retrying in %s", s.cfg.Name, err, backoff)
			if !sleepCtx(m.ctx, backoff) {
				s.markClosed()
				return
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		attempts = 0
		backoff = initialBackoff(s.cfg)
		s.setSession(h)

		if err := m.refreshTools(s, h.sess); err != nil {
			s.setState(stateFailed, "tool list failed")
			s.signalFirstTry()
			s.clearSession(h)
			h.close()
			attempts++
			m.warnf("mcp server %s: tools/list failed: %v", s.cfg.Name, err)
			if !restart || attempts >= s.cfg.Restart.MaxAttempts {
				return
			}
			if !sleepCtx(m.ctx, backoff) {
				s.markClosed()
				return
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		s.setState(stateReady, "")
		s.signalFirstTry()
		m.logf("mcp server %s: ready (%d tool(s), protocol %s)", s.cfg.Name, len(s.toolSnapshot()), s.protocolVersion())

		disconnected := false
		for !disconnected {
			select {
			case <-m.ctx.Done():
				s.clearSession(h)
				h.close()
				s.markClosed()
				return
			case <-h.done:
				disconnected = true
			case <-s.refreshCh:
				if err := m.refreshTools(s, h.sess); err != nil {
					s.setErrorKind("tool list failed")
					m.warnf("mcp server %s: tools/list refresh failed: %v", s.cfg.Name, err)
				}
			}
		}

		s.clearSession(h)
		s.setState(stateDegraded, "connection closed")
		s.clearTools()
		h.close()
		m.logf("mcp server %s: disconnected; reconnecting", s.cfg.Name)

		attempts++
		if !restart || attempts >= s.cfg.Restart.MaxAttempts {
			s.setState(stateFailed, "connection closed")
			return
		}
		if !sleepCtx(m.ctx, backoff) {
			s.markClosed()
			return
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

// connect builds and initializes one MCP client session.
func (m *Manager) connect(s *serverConn) (*sessionHandle, error) {
	// The context stays alive for the lifetime of the session: the legacy SSE
	// transport binds its long-lived GET stream to the context passed to
	// Connect, so cancelling it after the handshake would close the session.
	ctx, cancel := context.WithCancel(m.ctx)
	timer := time.AfterFunc(time.Duration(s.cfg.ConnectTimeout)*time.Second, cancel)

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "localaw", Version: "1.0.0"},
		&mcpsdk.ClientOptions{
			// Do not advertise sampling/elicitation/roots: MCP servers must not
			// be able to drive Localaw beyond exposing tools.
			Capabilities: &mcpsdk.ClientCapabilities{},
			// The SDK logger may include full tool arguments; keep it quiet and
			// rely on the manager's own credential-free diagnostics.
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
				s.requestRefresh()
			},
		},
	)
	transport, closer, err := m.transportFactory(s.cfg)
	if err != nil {
		timer.Stop()
		cancel()
		return nil, &stageError{kind: "connection failed", err: err}
	}
	var sessOpts *mcpsdk.ClientSessionOptions
	if s.cfg.Transport == "sse" {
		// Legacy HTTP+SSE predates the 2026 protocol revisions; negotiate the
		// stable 2025-06-18 revision for maximum compatibility.
		sessOpts = &mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-06-18"}
	}
	sess, err := client.Connect(ctx, transport, sessOpts)
	if stopped := timer.Stop(); !stopped && err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		if closer != nil {
			_ = closer.Close()
		}
		return nil, &stageError{kind: "handshake failed", err: err}
	}
	h := &sessionHandle{sess: sess, closer: closer, done: make(chan struct{}), cancel: cancel}
	go func() {
		_ = sess.Wait()
		close(h.done)
	}()
	return h, nil
}

// refreshTools re-lists every tool of one server and atomically replaces the
// server's snapshot. The global index is rebuilt afterwards so collisions are
// resolved consistently.
func (m *Manager) refreshTools(s *serverConn, sess *mcpsdk.ClientSession) error {
	ctx, cancel := context.WithTimeout(m.ctx, time.Duration(m.cfg.ListTimeout)*time.Second)
	defer cancel()

	maxTools := m.cfg.MaxTools
	if maxTools <= 0 {
		maxTools = config.DefaultMCPMaxTools
	}
	var tools []Tool
	for t, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		if len(tools) >= maxTools {
			m.warnf("mcp server %s: tool cap %d reached; ignoring extra tools", s.cfg.Name, maxTools)
			break
		}
		if resolved, ok := m.resolveTool(&s.cfg, t); ok {
			tools = append(tools, resolved)
		}
	}
	s.setTools(tools)
	s.clearLastErrorKind()
	if init := sess.InitializeResult(); init != nil {
		s.setProtocol(init.ProtocolVersion)
	}
	m.rebuildIndex()
	return nil
}

// rebuildIndex computes stable, collision-free exposed names for every tool of
// every ready server and caps the total tool count. Lock order: m.mu then
// s.mu; callers must not hold s.mu.
func (m *Manager) rebuildIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()

	type candidate struct {
		tool Tool
		base string
	}
	var all []candidate
	for _, s := range m.servers {
		for _, t := range s.toolSnapshot() {
			all = append(all, candidate{tool: t, base: baseName(t.Server, t.Name)})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].tool.Server != all[j].tool.Server {
			return all[i].tool.Server < all[j].tool.Server
		}
		return all[i].tool.Name < all[j].tool.Name
	})

	counts := make(map[string]int, len(all))
	for _, c := range all {
		counts[c.base]++
	}
	maxTools := m.cfg.MaxTools
	if maxTools <= 0 {
		maxTools = config.DefaultMCPMaxTools
	}
	if len(all) > maxTools {
		m.warnf("mcp: total tool cap %d reached; ignoring %d tool(s)", maxTools, len(all)-maxTools)
		all = all[:maxTools]
	}

	index := make(map[string]Tool, len(all))
	order := make([]Tool, 0, len(all))
	used := make(map[string]bool, len(all))
	for i, c := range all {
		name := fitName(c.base, c.tool.Server, c.tool.Name)
		if counts[c.base] > 1 {
			name = hashedName(c.base, c.tool.Server, c.tool.Name)
		}
		if used[name] {
			name = hashedName(c.base+"#"+strconv.Itoa(i), c.tool.Server, c.tool.Name)
		}
		used[name] = true
		c.tool.Exposed = name
		index[name] = c.tool
		order = append(order, c.tool)
	}
	m.index = index
	m.order = order
}

// requestRefresh schedules a tools/list refresh without blocking the SDK
// notification handler.
func (s *serverConn) requestRefresh() {
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

type sessionHandle struct {
	sess   *mcpsdk.ClientSession
	closer io.Closer
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

func (h *sessionHandle) close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		_ = h.sess.Close()
		if h.closer != nil {
			_ = h.closer.Close()
		}
		if h.cancel != nil {
			h.cancel()
		}
	})
}

type serverConn struct {
	cfg config.MCPServer
	mgr *Manager

	mu            sync.RWMutex
	state         serverState
	session       *sessionHandle
	tools         []Tool
	protocol      string
	lastErrorKind string

	firstTry  chan struct{}
	firstOnce sync.Once
	refreshCh chan struct{}
}

func (s *serverConn) signalFirstTry() {
	s.firstOnce.Do(func() { close(s.firstTry) })
}

func (s *serverConn) setState(st serverState, errorKind string) {
	s.mu.Lock()
	s.state = st
	s.lastErrorKind = errorKind
	s.mu.Unlock()
}

func (s *serverConn) markClosed() {
	s.mu.Lock()
	s.state = stateClosed
	s.mu.Unlock()
}

func (s *serverConn) setErrorKind(kind string) {
	s.mu.Lock()
	s.lastErrorKind = kind
	s.mu.Unlock()
}

func (s *serverConn) clearLastErrorKind() {
	s.mu.Lock()
	s.lastErrorKind = ""
	s.mu.Unlock()
}

func (s *serverConn) setSession(h *sessionHandle) {
	s.mu.Lock()
	s.session = h
	s.state = stateReady
	s.mu.Unlock()
}

func (s *serverConn) clearSession(h *sessionHandle) {
	s.mu.Lock()
	if s.session == h {
		s.session = nil
	}
	s.mu.Unlock()
}

func (s *serverConn) closeCurrent() {
	s.mu.RLock()
	h := s.session
	s.mu.RUnlock()
	h.close()
}

func (s *serverConn) currentSession() (*sessionHandle, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.session == nil || s.state != stateReady {
		return nil, false
	}
	return s.session, true
}

func (s *serverConn) setTools(tools []Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

func (s *serverConn) clearTools() {
	s.mu.Lock()
	s.tools = nil
	s.mu.Unlock()
	s.mgr.rebuildIndex()
}

func (s *serverConn) toolSnapshot() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

func (s *serverConn) setProtocol(p string) {
	s.mu.Lock()
	s.protocol = p
	s.mu.Unlock()
}

func (s *serverConn) protocolVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.protocol
}

func (s *serverConn) status() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ServerStatus{
		Name:      s.cfg.Name,
		Transport: s.cfg.Transport,
		State:     s.state.String(),
		Protocol:  s.protocol,
		Tools:     len(s.tools),
		Error:     s.lastErrorKind,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func initialBackoff(cfg config.MCPServer) time.Duration {
	d := time.Duration(cfg.Restart.MinBackoff) * time.Second
	if d <= 0 {
		return time.Second
	}
	return d
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

func (m *Manager) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Info(fmt.Sprintf(format, args...))
	}
}

func (m *Manager) warnf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Warn(fmt.Sprintf(format, args...))
	}
}
