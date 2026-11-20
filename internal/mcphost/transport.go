package mcphost

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localaw/internal/config"
	"localaw/internal/procutil"
)

// defaultTransportFactory builds the transport and (for stdio) owns the child
// process so it can be terminated as a whole process tree.
func defaultTransportFactory(sc config.MCPServer, logger *slog.Logger) (mcpsdk.Transport, io.Closer, error) {
	switch sc.Transport {
	case "stdio":
		return newStdioTransport(sc, logger)
	case "http":
		hc, err := headerClient(sc.URL, sc.Headers)
		if err != nil {
			return nil, nil, err
		}
		return &mcpsdk.StreamableClientTransport{
			Endpoint:   sc.URL,
			HTTPClient: hc,
		}, nil, nil
	case "sse":
		hc, err := headerClient(sc.URL, sc.Headers)
		if err != nil {
			return nil, nil, err
		}
		return &mcpsdk.SSEClientTransport{
			Endpoint:   sc.URL,
			HTTPClient: hc,
		}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported MCP transport %q", sc.Transport)
	}
}

func newStdioTransport(sc config.MCPServer, logger *slog.Logger) (mcpsdk.Transport, io.Closer, error) {
	cmd := exec.Command(sc.Command, sc.Args...)
	if sc.Cwd != "" {
		cmd.Dir = sc.Cwd
	}
	cmd.Env = buildEnv(sc)
	cmd.WaitDelay = 5 * time.Second
	stderr := newTailBuffer(32 << 10)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdio stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("stdio stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, fmt.Errorf("stdio stderr: %w", err)
	}
	procutil.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderrPipe.Close()
		return nil, nil, fmt.Errorf("start %s: %w", sc.Command, err)
	}
	go func() {
		_, _ = io.Copy(stderr, stderrPipe)
	}()

	// IOTransport only wraps the pipes; the process lifecycle stays here so a
	// timeout/shutdown can kill the whole tree (npx -> node, etc.).
	return &mcpsdk.IOTransport{Reader: stdout, Writer: stdin}, &stdioCloser{
		name:       sc.Name,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		stderrPipe: stderrPipe,
		stderr:     stderr,
		logger:     logger,
	}, nil
}

// headerRT injects static headers (e.g. Authorization) only for requests to
// the configured MCP origin. It also refuses HTTPS-to-HTTP redirects so
// credentials are never carried across a security downgrade.
type headerRT struct {
	base    http.RoundTripper
	headers map[string]string
	scheme  string
	host    string
}

func (t *headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil {
		return nil, fmt.Errorf("mcp: nil request URL")
	}
	if strings.EqualFold(t.scheme, "https") && strings.EqualFold(r.URL.Scheme, "http") {
		return nil, fmt.Errorf("mcp: refusing HTTPS-to-HTTP redirect to %s", r.URL.Redacted())
	}
	req := r.Clone(r.Context())
	if sameOrigin(req.URL, t.scheme, t.host) {
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
	} else {
		// Never forward configured credentials to a different origin, even if
		// net/http copied them from the original request.
		for k := range t.headers {
			req.Header.Del(k)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func sameOrigin(u *url.URL, scheme, host string) bool {
	if u == nil {
		return false
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, host)
}

// headerClient returns an HTTP client that pins static headers to the MCP
// endpoint's origin. The returned client is also used when no headers are
// configured so the HTTPS-to-HTTP downgrade guard always applies.
func headerClient(endpoint string, headers map[string]string) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid MCP endpoint %q", endpoint)
	}
	return &http.Client{Transport: &headerRT{
		base:    http.DefaultTransport,
		headers: headers,
		scheme:  u.Scheme,
		host:    u.Host,
	}}, nil
}

// buildEnv returns the child environment. By default only a minimal base
// environment is passed so third-party MCP servers cannot read Localaw's
// secrets; set inherit_env=true to opt into the full parent environment.
func buildEnv(sc config.MCPServer) []string {
	inherit := sc.InheritEnv != nil && *sc.InheritEnv
	var base []string
	if inherit {
		base = os.Environ()
	} else {
		base = minimalEnv()
	}
	env := make(map[string]string, len(base)+len(sc.Env))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range sc.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func minimalEnv() []string {
	keep := []string{
		"PATH", "HOME", "USERPROFILE", "SystemRoot", "SYSTEMROOT",
		"TEMP", "TMP", "APPDATA", "LOCALAPPDATA", "LANG", "LC_ALL",
		"SHELL", "COMSPEC", "PATHEXT", "NUMBER_OF_PROCESSORS", "OS",
	}
	out := make([]string, 0, len(keep))
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// stdioCloser waits for a stdio server to exit after its pipes are closed and
// falls back to killing the process tree.
type stdioCloser struct {
	name       string
	cmd        *exec.Cmd
	stdin      io.Closer
	stdout     io.Closer
	stderrPipe io.Closer
	stderr     *tailBuffer
	logger     *slog.Logger

	once sync.Once
	err  error
}

func (c *stdioCloser) Close() error {
	c.once.Do(func() {
		// Ask the server to exit by closing its stdin, then wait briefly before
		// killing the whole tree and closing the remaining pipes.
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case c.err = <-done:
		case <-time.After(5 * time.Second):
			_ = procutil.KillTree(c.cmd)
			if c.stdout != nil {
				_ = c.stdout.Close()
			}
			if c.stderrPipe != nil {
				_ = c.stderrPipe.Close()
			}
			select {
			case c.err = <-done:
			case <-time.After(2 * time.Second):
				c.err = fmt.Errorf("mcp stdio server %s did not exit after kill", c.name)
			}
		}
		if tail := c.stderr.String(); tail != "" && c.logger != nil {
			if c.err != nil {
				c.logger.Warn("mcp stdio server exited with output", "server", c.name, "stderr", truncateRunes(tail, 2000))
			} else {
				c.logger.Debug("mcp stdio server output", "server", c.name, "stderr", truncateRunes(tail, 2000))
			}
		}
	})
	return c.err
}

// tailBuffer keeps the last max bytes of stderr for diagnostics.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
