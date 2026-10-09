// Package web provides the browser control panel: JSON APIs plus an SSE
// conversation stream and an embedded single-page UI. Pure net/http, no CGO.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"localaw/internal/config"
	"localaw/internal/engine"
	"localaw/internal/local"
)

//go:embed ui/index.html
var indexHTML []byte

// Server is the HTTP control panel.
type Server struct {
	eng  *engine.Engine
	file *local.FileModule

	cwdMu sync.Mutex
	cwd   string

	mux *http.ServeMux
	srv *http.Server
}

// New creates the web server.
func New(cfg *config.Config, eng *engine.Engine, file *local.FileModule) *Server {
	wd, _ := os.Getwd()
	s := &Server{eng: eng, file: file, cwd: wd, mux: http.NewServeMux()}
	s.routes()
	s.srv = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Web.ListenHost, cfg.Web.ListenPort),
		Handler:           s.originGuard(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// originGuard rejects cross-origin requests so a malicious page cannot drive
// the panel (CSRF), even though it listens on loopback by default.
func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin accepts the request when the Origin host exactly matches the
// request host, or when both are loopback aliases (localhost / 127.0.0.1 /
// [::1]) with the same port. Requiring an equal port stops a page served from
// another loopback port from driving the panel.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	return isLoopbackHost(u.Hostname()) && isLoopbackHost(hostnameOnly(host)) &&
		u.Port() == hostPort(host)
}

func hostnameOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func hostPort(hostport string) string {
	if _, p, err := net.SplitHostPort(hostport); err == nil {
		return p
	}
	return ""
}

func isLoopbackHost(h string) bool {
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1"
}

// ListenAndServe starts the panel.
func (s *Server) ListenAndServe() error { return s.srv.ListenAndServe() }

// Shutdown gracefully stops the panel.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /", s.handleIndex)
	m.HandleFunc("GET /api/config", s.handleConfig)
	m.HandleFunc("GET /api/sessions", s.handleListSessions)
	m.HandleFunc("POST /api/sessions/new", s.handleNewSession)
	m.HandleFunc("POST /api/sessions/close", s.handleCloseSession)
	m.HandleFunc("POST /api/sessions/activate", s.handleActivateSession)
	m.HandleFunc("GET /api/history", s.handleHistory)
	m.HandleFunc("POST /api/send", s.handleSend)
	m.HandleFunc("GET /api/chat-stream", s.handleChatStream)
	m.HandleFunc("POST /api/set-auth", s.handleSetAuth)
	m.HandleFunc("POST /api/authorize-execute", s.handleAuthorize)
	m.HandleFunc("POST /api/stop", s.handleStop)
	m.HandleFunc("POST /api/reset", s.handleReset)
	m.HandleFunc("POST /api/exec-cmd", s.handleExecCmd)
	m.HandleFunc("GET /api/files/list", s.handleFileList)
	m.HandleFunc("POST /api/files/parent", s.handleFileParent)
	m.HandleFunc("POST /api/files/chdir", s.handleFileChdir)
	m.HandleFunc("POST /api/files/new", s.handleFileNew)
	m.HandleFunc("POST /api/files/mkdir", s.handleFileMkdir)
	m.HandleFunc("POST /api/files/delete", s.handleFileDelete)
	m.HandleFunc("POST /api/files/copy", s.handleFileCopy)
	m.HandleFunc("POST /api/files/move", s.handleFileMove)
	m.HandleFunc("GET /api/files/download", s.handleFileDownload)
	m.HandleFunc("POST /api/files/upload", s.handleFileUpload)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"auth_mode":      s.eng.AuthMode(),
		"active_session": s.eng.ActiveSessionID(),
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, sess := range s.eng.ListSessions() {
		out = append(out, map[string]any{
			"id": sess.ID, "title": sess.Title, "running": sess.Running(),
			"phase": sess.Phase(),
		})
	}
	writeJSON(w, 200, map[string]any{"sessions": out, "active": s.eng.ActiveSessionID()})
}

func (s *Server) handleNewSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	_ = readJSON(r, &req)
	sess, err := s.eng.NewSession(req.Title)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	s.eng.SetActiveSession(sess.ID)
	writeJSON(w, 200, map[string]any{"session": sess.ID})
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	clean := s.eng.CloseSession(req.SessionID)
	writeJSON(w, 200, map[string]any{"success": true, "stopped": clean})
}

func (s *Server) handleActivateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if !s.eng.SetActiveSession(req.SessionID) {
		writeJSON(w, 404, map[string]any{"error": "session not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"history": s.eng.GetTranscript(r.URL.Query().Get("session_id"))})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, 400, map[string]any{"error": "message is required"})
		return
	}
	if req.SessionID == "" {
		sess, err := s.eng.EnsureSession()
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		req.SessionID = sess.ID
	}
	if err := s.eng.BeginChat(req.SessionID, req.Message); err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error(), "session_id": req.SessionID})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "session_id": req.SessionID})
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	sess := s.eng.GetSession(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, dropped, unsub := s.eng.Subscribe(sessionID)
	defer unsub()

	snap := sess.Snapshot()
	sendSSE(w, flusher, map[string]any{"type": "snapshot", "snapshot": snap})
	if !snap.Running {
		sendSSE(w, flusher, map[string]any{"type": "idle"})
	}

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-dropped:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			sendSSE(w, flusher, ev)
		}
	}
}

func sendSSE(w http.ResponseWriter, f http.Flusher, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", data)
	f.Flush()
}

func (s *Server) handleSetAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode *int `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil || req.Mode == nil {
		writeJSON(w, 400, map[string]any{"error": "mode required"})
		return
	}
	if *req.Mode < 0 || *req.Mode > 2 {
		writeJSON(w, 400, map[string]any{"error": "mode must be 0, 1 or 2"})
		return
	}
	s.eng.SetAuthMode(*req.Mode)
	writeJSON(w, 200, map[string]any{"success": true, "auth_mode": s.eng.AuthMode()})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID  string `json:"session_id"`
		Authorized bool   `json:"authorized"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	ok := s.eng.SubmitAuth(req.SessionID, req.Authorized)
	writeJSON(w, 200, map[string]any{"success": ok})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	s.eng.Stop(req.SessionID)
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	ok := s.eng.ResetConversation(req.SessionID)
	writeJSON(w, 200, map[string]any{"success": ok})
}

func (s *Server) handleExecCmd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	out, err := s.eng.RunDirect(req.Command)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"result": out})
}

// ----- file manager -----

func (s *Server) currentDir() string {
	s.cwdMu.Lock()
	defer s.cwdMu.Unlock()
	return s.cwd
}

func (s *Server) setDir(p string) {
	s.cwdMu.Lock()
	s.cwd = p
	s.cwdMu.Unlock()
}

func (s *Server) resolve(p string) string {
	if p == "" {
		return s.currentDir()
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(s.currentDir(), p)
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	dir := s.currentDir()
	items, err := s.file.ListEntries(dir)
	if err != nil {
		writeJSON(w, 200, map[string]any{"current_path": dir, "items": []any{}, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"current_path": dir, "items": items})
}

func (s *Server) handleFileParent(w http.ResponseWriter, r *http.Request) {
	dir := s.currentDir()
	parent := filepath.Dir(dir)
	if parent != dir {
		s.setDir(parent)
	}
	writeJSON(w, 200, map[string]any{"current_path": s.currentDir()})
}

func (s *Server) handleFileChdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dirname string `json:"dirname"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	target := s.resolve(req.Dirname)
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		writeJSON(w, 200, map[string]any{"error": "directory not found"})
		return
	}
	s.setDir(target)
	writeJSON(w, 200, map[string]any{"current_path": s.currentDir()})
}

func (s *Server) handleFileNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filename string `json:"filename"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.file.CreateFile(filepath.Join(s.currentDir(), req.Filename))
	s.fileResult(w, result, err)
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dirname string `json:"dirname"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.file.CreateDir(filepath.Join(s.currentDir(), req.Dirname))
	s.fileResult(w, result, err)
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.file.Delete(s.resolve(req.Path))
	s.fileResult(w, result, err)
}

func (s *Server) handleFileCopy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Src  string `json:"src"`
		Dest string `json:"dest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.file.Copy(s.resolve(req.Src), s.resolve(req.Dest))
	s.fileResult(w, result, err)
}

func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Src  string `json:"src"`
		Dest string `json:"dest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.file.Move(s.resolve(req.Src), s.resolve(req.Dest))
	s.fileResult(w, result, err)
}

func (s *Server) fileResult(w http.ResponseWriter, result string, err error) {
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"result": result})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, 400, map[string]any{"error": "path required"})
		return
	}
	full := s.resolve(path)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		writeJSON(w, 404, map[string]any{"error": "file not found"})
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(full)}))
	http.ServeFile(w, r, full)
}

// maxUploadBytes bounds the whole upload request body to 64 MiB.
const maxUploadBytes = 64 << 20

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	defer file.Close()

	name := filepath.Base(header.Filename)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, 400, map[string]any{"error": "invalid file name"})
		return
	}
	dest := filepath.Join(s.currentDir(), name)
	if err := s.file.WriteStream(dest, file, 0o644); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"result": "Successfully uploaded: " + header.Filename})
}
