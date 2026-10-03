package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localaw/internal/config"
	"localaw/internal/engine"
	"localaw/internal/local"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	file := local.NewFileModule()
	q := local.NewQueue(local.NewExecutor(file), 8, 5*time.Second)
	t.Cleanup(q.Close)
	eng := engine.New(cfg, q)
	return New(cfg, eng, file)
}

func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestConfigEndpoint(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/config", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["auth_mode"]; !ok {
		t.Fatalf("auth_mode missing: %s", w.Body.String())
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestServer(t)

	w := do(t, s, http.MethodPost, "/api/sessions/new", "{}")
	if w.Code != 200 {
		t.Fatalf("new session status = %d", w.Code)
	}
	var created map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["session"]
	if id == "" {
		t.Fatal("no session id returned")
	}

	w = do(t, s, http.MethodGet, "/api/sessions", "")
	if !strings.Contains(w.Body.String(), id) {
		t.Fatalf("session not listed: %s", w.Body.String())
	}

	w = do(t, s, http.MethodPost, "/api/reset", `{"session_id":"`+id+`"}`)
	if !strings.Contains(w.Body.String(), "true") {
		t.Fatalf("reset failed: %s", w.Body.String())
	}
}

func TestExecCmd(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/exec-cmd", `{"command":"echo localaw-test"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "localaw-test") {
		t.Fatalf("unexpected exec result: %s", w.Body.String())
	}
}

func TestOriginGuard(t *testing.T) {
	s := newTestServer(t)
	h := s.originGuard(s.mux)

	// Cross-origin request is rejected.
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:8880"
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", w.Code)
	}

	// Same-origin request is allowed.
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:8880"
	req.Header.Set("Origin", "http://127.0.0.1:8880")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("same-origin status = %d", w.Code)
	}

	// Loopback alias (localhost vs 127.0.0.1) is allowed.
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:8880"
	req.Header.Set("Origin", "http://localhost:8880")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("loopback-alias status = %d", w.Code)
	}
}

func TestFileList(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/files/list", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var out struct {
		CurrentPath string `json:"current_path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CurrentPath == "" {
		t.Fatalf("current_path empty: %s", w.Body.String())
	}
}
