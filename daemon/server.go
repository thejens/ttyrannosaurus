package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"
	"github.com/thejens/ttyrannosaurus/daemon/config"
	"github.com/thejens/ttyrannosaurus/daemon/monitor"
	"github.com/thejens/ttyrannosaurus/daemon/protocol"
	"github.com/thejens/ttyrannosaurus/daemon/session"
	"github.com/thejens/ttyrannosaurus/daemon/template"
)

type server struct {
	cfg      *config.Config
	resolver *template.Resolver
	mgr      *session.Manager
	theme    config.TerminalTheme
	themeMu  sync.RWMutex
}

func newServer(cfg *config.Config, resolver *template.Resolver, mgr *session.Manager, theme config.TerminalTheme) http.Handler {
	s := &server{cfg: cfg, resolver: resolver, mgr: mgr, theme: theme}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"http://localhost:*", "http://127.0.0.1:*", "chrome-extension://*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type"},
	}))

	// Embedded xterm.js static assets
	sub, _ := fs.Sub(staticFiles, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// Terminal UI — create/attach by scheme+path template
	r.Get("/s/*", s.handleSession)

	// Terminal UI — reconnect to an existing session by ID directly
	r.Get("/connect/{sessionID}", s.handleConnect)

	// WebSocket I/O
	r.Get("/ws/{sessionID}", s.handleWebSocket)

	// Split view
	r.Get("/split", s.handleSplit)

	// REST API
	r.Get("/api/favicon", s.handleFavicon)
	r.Get("/api/health", s.handleHealth)
	r.Get("/api/sessions", s.handleListSessions)
	r.Delete("/api/sessions/{id}", s.handleKillSession)
	r.Get("/api/config", s.handleGetConfig)
	r.Put("/api/config", s.handlePutConfig)
	r.Get("/api/ws", s.handleEventsWS)
	r.Get("/api/sessions/{id}/lines", s.handleGetSessionLines)
	r.Put("/api/sessions/{id}/meta", s.handlePutMeta)
	r.Post("/api/open", s.handleOpen)
	r.Get("/api/theme", s.handleGetTheme)
	r.Put("/api/theme", s.handlePutTheme)

	return r
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]bool{"ok": true})
}

// handleFavicon proxies an external favicon URL through the daemon so the
// terminal page can set it as a same-origin <link rel="icon"> without hitting
// mixed-content blocks or CORS restrictions.
// Usage: GET /api/favicon?url=https://example.com/favicon.ico
func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	resp, err := http.Get(rawURL) //nolint:noctx,gosec
	if err != nil {
		http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	io.Copy(w, resp.Body) //nolint:errcheck
}

func (s *server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, s.mgr.List())
}

func (s *server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.Kill(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, s.cfg)
}

func (s *server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var incoming struct {
		Schemes map[string]*config.SchemeConfig `json:"schemes"`
		Port    int                             `json:"port,omitempty"`
		Tmux    *config.TmuxConfig              `json:"tmux,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if incoming.Schemes != nil {
		s.cfg.Schemes = incoming.Schemes
	}
	if incoming.Port != 0 {
		s.cfg.Port = incoming.Port
	}
	if incoming.Tmux != nil {
		s.cfg.Tmux = *incoming.Tmux
	}
	if err := config.Save(s.cfg); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.resolver.Update(s.cfg.Schemes)
	jsonOK(w, s.cfg)
}

// handleSession serves the xterm.js terminal page for a given scheme/path.
//
// Resolution order:
//  1. ?cmd=... query param → run that command directly (shell-wrapped)
//  2. scheme/path → template lookup
//  3. anything else → treat the whole URL path as the command to run
//
// Everything ends up in a login shell, so the terminal stays alive after
// the command exits.
func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	rawPath := strings.Trim(chi.URLParam(r, "*"), "/")

	var res template.ResolveResult

	var schemeName string
	if cmdStr := r.URL.Query().Get("cmd"); cmdStr != "" {
		res = template.ResolveRaw(strings.Fields(cmdStr))
	} else if scheme, path, _ := strings.Cut(rawPath, "/"); scheme != "" {
		schemeName = scheme
		var err error
		res, err = s.resolver.Resolve(scheme, path)
		if err != nil {
			res = template.ResolveRaw(strings.Fields(rawPath))
			schemeName = ""
		}
	} else {
		res = template.ResolveRaw(strings.Fields(rawPath))
	}

	// If the caller provides a working directory, honour it.
	if cwd := r.URL.Query().Get("cwd"); cwd != "" {
		res.Dir = cwd
	}

	// Look up scheme-level monitor for the session's launch command.
	var mon monitor.Monitor
	lookupKey := schemeName
	if lookupKey == "" && len(res.Command) > 0 {
		lookupKey = filepath.Base(res.Command[0])
	}
	if lookupKey != "" {
		if sc := s.cfg.Schemes[lookupKey]; sc != nil {
			mon = monitor.New(sc.Monitor)
		}
	}

	// Build tmux session backing if configured.
	var ts *session.TmuxSession
	tmuxCfg := session.TmuxCfg{Enabled: s.cfg.Tmux.Enabled, Socket: s.cfg.Tmux.Socket, ExtraArgs: s.cfg.Tmux.ExtraArgs}
	if session.UseTmux(tmuxCfg) {
		t := session.NewTmuxSession(res.SessionID, tmuxCfg)
		ts = &t
		session.EnsureServerOptions(t.Socket)
	}

	resolver := s.buildSchemeResolver()
	sess, err := s.mgr.GetOrCreate(res, mon, resolver, ts)
	if err != nil {
		http.Error(w, "spawn failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to the stable /connect/{id} URL so the browser's address bar
	// and the extension's tab tracker always see a unique, bookmarkable URL.
	// Skip redirect if the request is already on /connect/ (avoids loop).
	if !strings.HasPrefix(r.URL.Path, "/connect/") {
		http.Redirect(w, r, "/connect/"+sess.ID, http.StatusFound)
		return
	}
	serveTerminalPage(w, sess.ID, s.theme)
}

// handleConnect serves the terminal page for an already-running session by ID.
// Used by the sidebar to reconnect without going through template resolution —
// avoids the raw-session ?cmd= problem entirely.
func (s *server) handleConnect(w http.ResponseWriter, r *http.Request) {
	// Always serve the terminal page — if the session is gone the WebSocket
	// handler sends close(4002) and the frontend shows "session not found".
	id := chi.URLParam(r, "sessionID")
	serveTerminalPage(w, id, s.theme)
}

func (s *server) handleSplit(w http.ResponseWriter, r *http.Request) {
	serveSplitPage(w, s.cfg.Port)
}

// handleEventsWS upgrades the sidebar connection to WebSocket and streams
// session lifecycle events. The client receives a full snapshot on connect,
// then incremental session:created/updated/killed messages as they occur.
func (s *server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	evCh, unsub := s.mgr.SubscribeEvents()
	defer unsub()

	// Send initial snapshot so the sidebar renders immediately on connect.
	snapshot := protocol.SessionsMessage{Type: "sessions", Sessions: toStates(s.mgr.List())}
	if b, err := json.Marshal(snapshot); err == nil {
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}

	// Pump reads to detect client disconnect (gorilla requires a reader).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			var msg any
			switch ev.Kind {
			case session.EvSessionCreated:
				msg = protocol.SessionCreatedMessage{Type: "session:created", Session: toState(ev.Session)}
			case session.EvSessionUpdated:
				msg = protocol.SessionUpdatedMessage{Type: "session:updated", Session: toState(ev.Session)}
			case session.EvSessionKilled:
				msg = protocol.SessionKilledMessage{Type: "session:killed", ID: ev.ID}
			}
			if msg != nil {
				b, _ := json.Marshal(msg)
				if conn.WriteMessage(websocket.TextMessage, b) != nil {
					return
				}
			}
		}
	}
}

// toState converts a session to its protocol wire representation by going
// through MarshalJSON — this reuses the locking and field selection already
// defined there without duplicating the logic.
func toState(sess *session.Session) protocol.SessionState {
	b, _ := json.Marshal(sess)
	var s protocol.SessionState
	json.Unmarshal(b, &s) //nolint:errcheck
	return s
}

func toStates(sessions []*session.Session) []protocol.SessionState {
	out := make([]protocol.SessionState, len(sessions))
	for i, s := range sessions {
		out[i] = toState(s)
	}
	return out
}

// handleOpen opens a file in the configured editor (or OS default).
// Body: {"path": "/abs/path.py", "line": 42, "col": 1}
// path may also be a file:// URI.
func (s *server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Col  int    `json:"col"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Strip file:// URI prefix → plain path
	path := req.Path
	if after, ok := strings.CutPrefix(path, "file://"); ok {
		path = after
	}
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	var cmd *exec.Cmd
	editor := s.cfg.Editor

	if editor.Command == "" {
		// No editor configured — use macOS `open` which respects per-type defaults.
		// Line numbers aren't supported by `open`, so just open the file.
		cmd = exec.Command("open", path)
	} else if req.Line > 0 {
		lineArg := editor.LineArg
		if lineArg == "" {
			lineArg = "{path}:{line}" // sensible default (works for Zed)
		}
		lineStr := strings.NewReplacer(
			"{path}", path,
			"{line}", fmt.Sprintf("%d", req.Line),
			"{col}", fmt.Sprintf("%d", req.Col),
		).Replace(lineArg)
		// lineArg may produce a single arg or multiple (e.g. "--goto path:line")
		args := strings.Fields(lineStr)
		cmd = exec.Command(editor.Command, args...)
	} else {
		cmd = exec.Command(editor.Command, path)
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "open failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go cmd.Wait() //nolint:errcheck — fire and forget
	w.WriteHeader(http.StatusNoContent)
}

// buildSchemeResolver returns a function the session can call with an
// executable name to get the matching scheme's favicon and monitor.
func (s *server) buildSchemeResolver() session.SchemeResolver {
	return func(execName string) (string, monitor.Monitor) {
		if sc := s.cfg.Schemes[execName]; sc != nil {
			return sc.Favicon, monitor.New(sc.Monitor)
		}
		return "", nil
	}
}

// handleGetSessionLines returns the last ~20 clean (ANSI-stripped) text lines
// from the session's PTY output. Used by the sidepanel to generate AI session names.
func (s *server) handleGetSessionLines(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess := s.mgr.Get(id)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string][]string{"lines": sess.RecentLines()})
}

func (s *server) handlePutMeta(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var m session.Meta
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mgr.UpdateMeta(id, m); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleGetTheme(w http.ResponseWriter, _ *http.Request) {
	s.themeMu.RLock()
	t := s.theme
	s.themeMu.RUnlock()
	jsonOK(w, t)
}

func (s *server) handlePutTheme(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	theme := config.ParseGhosttyConfig(string(body))
	if err := config.SaveThemeConfig(string(body)); err != nil {
		http.Error(w, "save theme: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.themeMu.Lock()
	s.theme = theme
	s.themeMu.Unlock()
	// Also update the global port-visible copy used by serveTerminalPage.
	setTheme(theme)
	jsonOK(w, theme)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
