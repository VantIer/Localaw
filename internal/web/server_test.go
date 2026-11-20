package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	eng := engine.NewWithMCP(cfg, q, nil)
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

func TestMCPServersEndpointEmpty(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/mcp/servers", "")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"servers":[]`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
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

func TestSetAuthValidation(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/set-auth", `{"mode":5}`)
	if w.Code != 400 {
		t.Fatalf("invalid mode status = %d, want 400: %s", w.Code, w.Body.String())
	}
	w = do(t, s, http.MethodPost, "/api/set-auth", `{"mode":2}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"auth_mode":2`) {
		t.Fatalf("valid mode: %d %s", w.Code, w.Body.String())
	}
}

func TestSendRejectsEmptyMessage(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/send", `{"message":"   "}`)
	if w.Code != 400 {
		t.Fatalf("empty message status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestOriginGuardPortMismatch(t *testing.T) {
	s := newTestServer(t)
	h := s.originGuard(s.mux)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:8880"
	req.Header.Set("Origin", "http://localhost:9999")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("loopback alias with a different port status = %d, want 403", w.Code)
	}
}

func TestNewSessionRejectsInvalidJSON(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/sessions/new", `{`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400", w.Code)
	}
}

func TestUnknownSessionEndpoints(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/history?session_id=missing", ""},
		{http.MethodGet, "/api/chat-stream?session_id=missing", ""},
		{http.MethodPost, "/api/sessions/close", `{"session_id":"missing"}`},
		{http.MethodPost, "/api/send", `{"session_id":"missing","message":"hi"}`},
		{http.MethodPost, "/api/authorize-execute", `{"session_id":"missing","authorized":true}`},
		{http.MethodPost, "/api/stop", `{"session_id":"missing"}`},
		{http.MethodPost, "/api/reset", `{"session_id":"missing"}`},
	}
	for _, tc := range cases {
		w := do(t, s, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"err"`) {
			t.Fatalf("%s %s: missing err field: %s", tc.method, tc.path, w.Body.String())
		}
	}
}

func TestParameterValidationUses400(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		path string
		body string
	}{
		{"/api/files/new", `{"filename":" "}`},
		{"/api/files/mkdir", `{"dirname":""}`},
		{"/api/files/chdir", `{"dirname":" "}`},
		{"/api/exec-cmd", `{"command":"  "}`},
	}
	for _, tc := range cases {
		w := do(t, s, http.MethodPost, tc.path, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"err"`) {
			t.Fatalf("%s: missing err field: %s", tc.path, w.Body.String())
		}
	}
}

func TestFileAPIClassifiesErrors(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	s.setDir(dir)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if w := do(t, s, http.MethodPost, "/api/files/new", `{"filename":"subdir"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("new over directory status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := do(t, s, http.MethodPost, "/api/files/mkdir", `{"dirname":"plain.txt"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("mkdir over file status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := do(t, s, http.MethodPost, "/api/files/chdir", `{"dirname":"plain.txt"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("chdir to file status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := do(t, s, http.MethodGet, "/api/files/download?path=subdir", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("download directory status = %d, want 400: %s", w.Code, w.Body.String())
	}

	s.setDir(filepath.Join(dir, "does-not-exist"))
	if w := do(t, s, http.MethodGet, "/api/files/list", ""); w.Code != http.StatusNotFound {
		t.Fatalf("list missing directory status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestFileAPIFindsMissingPath(t *testing.T) {
	s := newTestServer(t)
	s.setDir(t.TempDir())
	w := do(t, s, http.MethodPost, "/api/files/delete", `{"path":"does-not-exist"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"err"`) {
		t.Fatalf("missing err field: %s", w.Body.String())
	}
}

func TestFileOpsRejectEmptyPaths(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		path string
		body string
	}{
		{"/api/files/delete", `{"path":""}`},
		{"/api/files/delete", `{"path":"  "}`},
		{"/api/files/copy", `{"src":"","dest":"x"}`},
		{"/api/files/copy", `{"src":"x","dest":""}`},
		{"/api/files/move", `{"src":"","dest":"x"}`},
		{"/api/files/move", `{"src":"x","dest":""}`},
	}
	for _, tc := range cases {
		w := do(t, s, http.MethodPost, tc.path, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: status = %d, want 400: %s", tc.path, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestFileUpload(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	s.setDir(dir)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("upload-body")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "upload-body" {
		t.Fatalf("uploaded content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".localaw-tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestFileUploadRejectsEmptyName(t *testing.T) {
	s := newTestServer(t)
	s.setDir(t.TempDir())

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("empty filename status = %d, want 400: %s", w.Code, w.Body.String())
	}
}
