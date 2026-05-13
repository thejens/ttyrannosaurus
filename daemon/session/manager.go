package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thejens/ttyrannosaurus/daemon/monitor"
	"github.com/thejens/ttyrannosaurus/daemon/protocol"
	"github.com/thejens/ttyrannosaurus/daemon/template"
	"github.com/thejens/ttyrannosaurus/daemon/vt"
)

const ringSize = 65536

// shellWorkerName lists process names that shell frameworks (Oh My Zsh, fish,
// etc.) spawn as background workers. When scanning a shell's children to find
// the user's foreground program, processes with these names are skipped.
var shellWorkerName = map[string]bool{
	"zsh": true, "bash": true, "sh": true, "fish": true,
	"ksh": true, "dash": true, "csh": true, "tcsh": true,
}

// Meta is the mutable metadata the sidebar and tab title display for a session.
// It is a copy of monitor.Meta so callers don't need to import both packages.
type Meta = monitor.Meta

// SchemeResolver maps an executable name to a (favicon, monitor) pair.
// The server provides this so the session can switch monitors when the
// foreground process changes without depending on the config package.
type SchemeResolver func(execName string) (favicon string, mon monitor.Monitor)

// Session represents a live terminal session with a persistent PTY.
type Session struct {
	ID       string    `json:"id"`
	Scheme   string    `json:"scheme"`
	Path     string    `json:"path"`
	Command  []string  `json:"command"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Alive    bool      `json:"alive"`
	Meta     Meta      `json:"meta"`
	Favicon  string    `json:"favicon,omitempty"`

	ptmx         *os.File
	cmd          *exec.Cmd
	tmux         *TmuxSession // non-nil when backed by tmux
	mon          monitor.Monitor
	resolver     SchemeResolver
	buf          *ringBuffer
	vtParser     *vt.Parser
	metaCh       chan protocol.MetaMessage
	clients      sync.Map // clientID → clientChans
	publishEvent func(SessionEvent)
	// recentLines holds the last 20 clean (ANSI-stripped) non-empty lines from
	// the PTY. Used by the sidepanel to generate AI session names.
	recentLines []string
	mu          sync.Mutex
}

// SessionEventKind identifies the type of a session lifecycle event.
type SessionEventKind string

const (
	EvSessionCreated SessionEventKind = "session:created"
	EvSessionUpdated SessionEventKind = "session:updated"
	EvSessionKilled  SessionEventKind = "session:killed"
)

// SessionEvent is published to all sidebar WebSocket subscribers when a session
// is created, updated (metadata/alive state changed), or removed.
type SessionEvent struct {
	Kind    SessionEventKind
	Session *Session // nil when Kind == EvSessionKilled
	ID      string   // populated when Kind == EvSessionKilled
}

type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	eventsMu  sync.RWMutex
	eventSubs []chan SessionEvent
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

// SubscribeEvents returns a channel that receives session lifecycle events and
// an unsubscribe function that must be called when the subscriber exits.
// The channel has capacity 64; events are dropped (not blocked) on overflow.
func (m *Manager) SubscribeEvents() (<-chan SessionEvent, func()) {
	ch := make(chan SessionEvent, 64)
	m.eventsMu.Lock()
	m.eventSubs = append(m.eventSubs, ch)
	m.eventsMu.Unlock()
	return ch, func() {
		m.eventsMu.Lock()
		defer m.eventsMu.Unlock()
		for i, sub := range m.eventSubs {
			if sub == ch {
				m.eventSubs = append(m.eventSubs[:i], m.eventSubs[i+1:]...)
				close(ch)
				return
			}
		}
	}
}

func (m *Manager) publishEvent(ev SessionEvent) {
	m.eventsMu.RLock()
	defer m.eventsMu.RUnlock()
	for _, ch := range m.eventSubs {
		select {
		case ch <- ev:
		default: // slow subscriber — drop rather than stall the caller
		}
	}
}

// GetOrCreate returns an existing live session or spawns a new one.
// ts is non-nil when the session should be backed by tmux.
func (m *Manager) GetOrCreate(res template.ResolveResult, mon monitor.Monitor, resolver SchemeResolver, ts *TmuxSession) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return a live in-memory session.
	if sess, ok := m.sessions[res.SessionID]; ok {
		if sess.Alive {
			sess.mu.Lock()
			sess.LastSeen = time.Now()
			sess.mu.Unlock()
			return sess, nil
		}
		// Dormant (restored from disk but no PTY yet) — spawn now.
		if sess.ptmx == nil {
			return m.spawnDormant(sess)
		}
	}

	// Brand-new session.
	spawnCmd := res.Command
	if ts != nil {
		spawnCmd = WrapInTmux(res.Command, *ts)
	}

	ptmx, cmd, err := spawnPTY(spawnCmd, res.Dir)
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:       res.SessionID,
		Scheme:   res.Scheme,
		Path:     res.Path,
		Command:  res.Command,
		Created:  time.Now(),
		LastSeen: time.Now(),
		Alive:    true,
		ptmx:     ptmx,
		cmd:      cmd,
		tmux:     ts,
		mon:      mon,
		resolver: resolver,
		buf:      newRingBuffer(ringSize),
		metaCh:   make(chan protocol.MetaMessage, 32),
	}
	m.sessions[res.SessionID] = sess
	m.startSession(sess)
	if res.InitInput != "" {
		go sess.writeAfterPrompt(res.InitInput)
	}
	m.publishEvent(SessionEvent{Kind: EvSessionCreated, Session: sess})

	// Persist so the daemon can restore it after restart.
	ps := PersistedSession{
		ID: sess.ID, Scheme: sess.Scheme, Path: sess.Path,
		Command: sess.Command, Created: sess.Created,
		Tmux: ts,
	}
	PersistSession(ps) //nolint:errcheck

	return sess, nil
}

// Restore registers a session that existed before a daemon restart.
// The session has no PTY yet; it will be spawned on first WebSocket connect.
func (m *Manager) Restore(ps PersistedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[ps.ID]; exists {
		return
	}
	sess := &Session{
		ID:       ps.ID,
		Scheme:   ps.Scheme,
		Path:     ps.Path,
		Command:  ps.Command,
		Created:  ps.Created,
		LastSeen: ps.Created,
		Alive: false, // dormant until PTY spawned
		tmux:  ps.Tmux,
		buf:      newRingBuffer(ringSize),
		metaCh:   make(chan protocol.MetaMessage, 32),
	}
	m.sessions[ps.ID] = sess
}

// spawnDormant spawns the PTY for a session that was restored from disk.
// Must be called with m.mu held.
func (m *Manager) spawnDormant(sess *Session) (*Session, error) {
	var spawnCmd []string
	if sess.tmux != nil {
		spawnCmd = AttachCommand(*sess.tmux)
	} else {
		spawnCmd = sess.Command
	}
	ptmx, cmd, err := spawnPTY(spawnCmd, "")
	if err != nil {
		return nil, err
	}
	sess.ptmx = ptmx
	sess.cmd = cmd
	sess.Alive = true
	sess.LastSeen = time.Now()
	m.startSession(sess)
	m.publishEvent(SessionEvent{Kind: EvSessionCreated, Session: sess})
	return sess, nil
}

func (m *Manager) startSession(sess *Session) {
	// Wire event publishing so applyMeta/watchDeath can reach the sidebar bus.
	// Done here (not in GetOrCreate/spawnDormant) so restored dormant sessions
	// also get the hook when they are first woken up.
	sess.publishEvent = m.publishEvent
	// Always create a fresh parser — stale state from a previous PTY must not carry over.
	sess.vtParser = vt.New(sess)
	go sess.readLoop()
	go sess.detectLoop()
	go m.watchDeath(sess)
}

func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// GetOrWake returns the session with the given ID, waking it from dormancy
// if it is a tmux-backed session that was restored from disk without a PTY.
func (m *Manager) GetOrWake(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[id]
	if !ok {
		return nil, nil
	}
	if sess.ptmx == nil {
		return m.spawnDormant(sess)
	}
	return sess, nil
}

func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	RemovePersistedSession(id)
	// Kill the underlying tmux session so it doesn't linger.
	if sess.tmux != nil {
		exec.Command("tmux", "-L", sess.tmux.Socket, "kill-session", "-t", sess.tmux.Name).Run() //nolint:errcheck
	}
	if sess.ptmx != nil {
		return sess.ptmx.Close()
	}
	// Dormant session — just remove from registry.
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	m.publishEvent(SessionEvent{Kind: EvSessionKilled, ID: id})
	return nil
}

// UpdateMeta allows external callers (e.g. PUT /api/sessions/{id}/meta) to
// push metadata updates.
func (m *Manager) UpdateMeta(id string, meta Meta) error {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	sess.applyMeta(meta)
	return nil
}

func (m *Manager) watchDeath(sess *Session) {
	sess.cmd.Wait() //nolint:errcheck
	sess.mu.Lock()
	sess.Alive = false
	sess.mu.Unlock()
	m.publishEvent(SessionEvent{Kind: EvSessionUpdated, Session: sess})
	// Closing metaCh signals all WebSocket goroutines to exit cleanly.
	// They call Unsubscribe on exit, so we don't touch clients here.
	// Do NOT close cc.Data channels — readLoop may still be broadcasting
	// to them concurrently, and a send on a closed channel panics.
	close(sess.metaCh)
	time.AfterFunc(30*time.Second, func() {
		m.mu.Lock()
		delete(m.sessions, sess.ID)
		m.mu.Unlock()
		m.publishEvent(SessionEvent{Kind: EvSessionKilled, ID: sess.ID})
		// Only remove persisted record if this session is not tmux-backed.
		// Tmux-backed sessions can be restored after the process exits.
		if sess.tmux == nil {
			RemovePersistedSession(sess.ID)
		}
	})
}

// readLoop reads PTY output and drives three pipeline stages:
//
//	(a) ring buffer — keeps last 64 KB for scrollback replay on new WS connect
//	(b) VT parser   — extracts OSC sequences and line boundaries for metadata/monitor
//	(c) broadcast   — fans out raw bytes to all connected WebSocket clients
func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.buf.write(chunk)      // stage a
			s.vtParser.Write(chunk) // stage b — calls HandleVTEvent synchronously
			s.broadcast(chunk)      // stage c
		}
		if err != nil {
			if err != io.EOF {
				_ = err
			}
			return
		}
	}
}

// broadcast fans out a PTY chunk to all connected WebSocket clients.
// Non-blocking: slow clients silently drop chunks rather than stalling the read loop.
func (s *Session) broadcast(chunk []byte) {
	s.clients.Range(func(_, v any) bool {
		cc := v.(clientChans)
		select {
		case cc.Data <- chunk:
		default:
		}
		return true
	})
}

// HandleVTEvent implements vt.Handler. Called synchronously from vtParser.Write
// in the readLoop goroutine — no locking needed between the two.
func (s *Session) HandleVTEvent(ev vt.Event) {
	switch ev.Kind {
	case vt.OSCEvent:
		s.handleOSC(ev.Code, ev.Payload)
	case vt.LineEvent:
		if ev.Line != "" {
			// Keep a rolling window of clean lines for AI session naming.
			s.mu.Lock()
			s.recentLines = append(s.recentLines, ev.Line)
			if len(s.recentLines) > 20 {
				s.recentLines = s.recentLines[len(s.recentLines)-20:]
			}
			s.mu.Unlock()

			if s.mon != nil {
				if meta := s.mon.Feed(ev.Line); meta != nil {
					s.applyMeta(*meta)
				}
			}
		}
	}
}

// RecentLines returns the last up to 20 clean (ANSI-stripped) lines from the PTY.
func (s *Session) RecentLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.recentLines))
	copy(out, s.recentLines)
	return out
}

// handleOSC reacts to OSC escape sequences extracted by the VT parser.
//
//	OSC 2  ; title BEL       → window title → set Meta.Name
//	OSC 7  ; file:// BEL     → current working directory
//	OSC 7773 ; json BEL      → ttyrannosaurus-specific full Meta update
func (s *Session) handleOSC(code, payload string) {
	switch code {
	case "2": // window title
		if payload != "" {
			s.mu.Lock()
			s.Meta.Name = payload
			s.mu.Unlock()
			s.notifyMeta()
		}

	case "7": // current working directory (shell-integration, e.g. oh-my-zsh)
		// Format: file://hostname/path  or  file:///path
		cwd := payload
		if after, ok := strings.CutPrefix(cwd, "file://"); ok {
			if slash := strings.Index(after, "/"); slash >= 0 {
				cwd = after[slash:]
			} else {
				cwd = "/" + after
			}
		}
		if cwd != "" {
			s.mu.Lock()
			s.Meta.CWD = cwd
			s.mu.Unlock()
			s.notifyMeta()
		}

	case "9":
		// ConEmu progress bar protocol — OSC 9;4;<state>[;<pct>]
		// Supported by Windows Terminal, Ghostty, WezTerm, Konsole, mintty.
		// Emitted by Claude Code v2.0.56+ and other TUIs as the canonical
		// way to signal running/idle/waiting state without line-parsing.
		//
		//   0 = clear (tool finished → idle)
		//   1 = progress at <pct>% (busy, determinate)
		//   2 = error at <pct>% (error)
		//   3 = indeterminate progress (busy)
		//   4 = paused / needs attention (waiting for user)
		s.handleOSCProgress(payload)

	case "7773": // ttyrannosaurus custom full-Meta JSON blob
		var m Meta
		if json.Unmarshal([]byte(payload), &m) == nil {
			s.applyMeta(m)
		}
	}
}

// handleOSCProgress parses the ConEmu OSC 9;4 progress subprotocol and maps
// it to our status field. Keeping this separate makes it easy to unit-test and
// to extend (e.g. expose the percentage in a future progress indicator).
func (s *Session) handleOSCProgress(payload string) {
	// payload is everything after the first ";", i.e. "4;3" or "4;1;75"
	parts := strings.SplitN(payload, ";", 3)
	if len(parts) < 2 || strings.TrimSpace(parts[0]) != "4" {
		return
	}
	var status string
	switch strings.TrimSpace(parts[1]) {
	case "0":
		status = "idle"
	case "1", "3":
		status = "busy"
	case "2":
		status = "error"
	case "4":
		status = "waiting"
	}
	if status == "" {
		return
	}
	s.mu.Lock()
	s.Meta.Status = status
	s.mu.Unlock()
	s.notifyMeta()
}

func (s *Session) applyMeta(m Meta) {
	s.mu.Lock()
	if m.Name != ""    { s.Meta.Name = m.Name }
	if m.Status != ""  { s.Meta.Status = m.Status }
	if m.CWD != ""     { s.Meta.CWD = m.CWD }
	if m.Program != "" { s.Meta.Program = m.Program }
	s.Meta.Detail = m.Detail
	s.mu.Unlock()
	s.notifyMeta()
}

// notifyMeta pushes the current metadata to connected terminal tabs via the
// WebSocket channel AND notifies the sidebar event bus. Use this everywhere
// metadata changes; pushMeta alone only reaches the terminal tab.
func (s *Session) notifyMeta() {
	s.pushMeta()
	if s.publishEvent != nil {
		s.publishEvent(SessionEvent{Kind: EvSessionUpdated, Session: s})
	}
}

func (s *Session) pushMeta() {
	s.mu.Lock()
	frame := protocol.MetaMessage{
		Type:    "meta",
		Name:    s.Meta.Name,
		Status:  s.Meta.Status,
		Detail:  s.Meta.Detail,
		CWD:     s.Meta.CWD,
		Program: s.Meta.Program,
		Favicon: s.Favicon,
	}
	s.mu.Unlock()
	// Guard against send on closed channel — watchDeath may close metaCh
	// concurrently while readLoop or detectLoop are still calling pushMeta.
	defer func() { recover() }() //nolint:errcheck
	select {
	case s.metaCh <- frame:
	default:
	}
}

// detectLoop periodically polls the shell's foreground program and CWD,
// updating session metadata when either changes.
func (s *Session) detectLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	var lastProgram, lastCWD string
	for range ticker.C {
		s.mu.Lock()
		alive := s.Alive
		s.mu.Unlock()
		if !alive {
			return
		}

		shellPid, cwd := s.shellState()
		program := foregroundProgram(shellPid)

		changed := false

		if program != lastProgram {
			lastProgram = program
			s.mu.Lock()
			s.Meta.Program = program
			// Switch monitor + favicon when the foreground program changes.
			// When the program has no scheme entry (or the shell is idle), revert
			// to the session's base favicon so the tab icon tracks what is running.
			if s.resolver != nil {
				var favicon string
				var mon monitor.Monitor
				if program != "" {
					favicon, mon = s.resolver(program)
				}
				s.mon = mon
				s.Favicon = favicon
			}
			s.mu.Unlock()
			changed = true
		}

		// Only update CWD from polling if the shell reported a non-empty path
		// and it differs from what OSC 7 may have already set.
		if cwd != "" && cwd != lastCWD {
			lastCWD = cwd
			s.mu.Lock()
			s.Meta.CWD = cwd
			s.mu.Unlock()
			changed = true
		}

		if changed {
			s.notifyMeta()
		}
	}
}

// shellState returns the PID of the shell running in the session's PTY and
// the current working directory of that shell.
//
// For daemon-managed tmux sessions, a single tmux query fetches both fields
// cheaply. For plain PTY sessions (no tmux or user-started tmux inside the
// shell), the shell PID comes directly from s.cmd and CWD is read via lsof —
// the universal fallback that requires no special shell integration.
func (s *Session) shellState() (shellPid int, cwd string) {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0, ""
	}
	shellPid = s.cmd.Process.Pid

	if s.tmux != nil {
		// One tmux call gets both pane_pid (the shell inside tmux) and its CWD.
		// "|" is safe as a field separator because tmux paths use "/" not "|".
		out, err := exec.Command("tmux", "-L", s.tmux.Socket,
			"list-panes", "-t", s.tmux.Name, "-F", "#{pane_pid}|#{pane_current_path}").Output()
		if err != nil {
			return 0, ""
		}
		line := strings.TrimSpace(string(out))
		pidStr, path, _ := strings.Cut(line, "|")
		pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
		if err != nil {
			return 0, ""
		}
		return pid, strings.TrimSpace(path)
	}

	// No daemon-managed tmux: read CWD via lsof. Works on macOS and Linux
	// without requiring any shell integration or config on the user's part.
	cwd = cwdViaLSOF(shellPid)
	return shellPid, cwd
}

// cwdViaLSOF returns the current working directory of pid using lsof.
// This is the portable fallback for non-tmux sessions.
func cwdViaLSOF(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

// foregroundProgram returns the name of the non-shell child process of shellPid,
// or "" if the shell is idle. Skips shell worker processes (e.g. Oh My Zsh
// background workers that appear as "zsh" children of the main shell).
func foregroundProgram(shellPid int) string {
	if shellPid == 0 {
		return ""
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPid)).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return ""
	}
	pids := strings.Fields(string(out))
	for i := len(pids) - 1; i >= 0; i-- {
		nameOut, err := exec.Command("ps", "-o", "comm=", "-p", pids[i]).Output()
		if err != nil {
			continue
		}
		name := filepath.Base(strings.TrimSpace(string(nameOut)))
		if !shellWorkerName[name] {
			return name
		}
	}
	return ""
}

// clientChans bundles the channels a single WebSocket client listens on.
type clientChans struct {
	Data       chan []byte
	Displaced  chan struct{} // closed when another client opens the same session
}

// Subscribe registers a WebSocket client. If other clients are already
// connected, their displacedCh is closed so they can show the "opened
// elsewhere" overlay. Returns replay bytes plus the client's channels.
func (s *Session) Subscribe(clientID string) ([]byte, clientChans) {
	cc := clientChans{
		Data:      make(chan []byte, 256),
		Displaced: make(chan struct{}),
	}

	// Signal existing clients that a new tab has taken the session.
	s.clients.Range(func(_, v any) bool {
		existing := v.(clientChans)
		select {
		case <-existing.Displaced: // already closed
		default:
			close(existing.Displaced)
		}
		return true
	})

	s.clients.Store(clientID, cc)
	return s.buf.snapshot(), cc
}

func (s *Session) Unsubscribe(clientID string) {
	s.clients.Delete(clientID)
}

// writeAfterPrompt waits until the shell has emitted its first interactive
// prompt, then writes cmd as keyboard input. This ensures the shell is fully
// initialised (zshrc, nvm, etc.) before the command runs.
//
// Prompt detection: we watch for a run of printable bytes preceded by a
// newline (or start of output) that has been quiet for 80 ms — a reliable
// heuristic for "shell is waiting for input". We cap the wait at 8 s so a
// broken shell does not block indefinitely.
func (s *Session) writeAfterPrompt(cmd string) {
	const quietDuration = 80 * time.Millisecond
	const maxWait = 8 * time.Second

	clientID := "init-writer"
	_, cc := s.Subscribe(clientID)
	defer s.Unsubscribe(clientID)

	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	quiet := time.NewTimer(quietDuration)
	defer quiet.Stop()

	for {
		select {
		case <-deadline.C:
			// Give up waiting; write anyway.
			s.Write([]byte(cmd + "\n"))
			return
		case _, ok := <-cc.Data:
			if !ok {
				return
			}
			// Reset the quiet timer each time output arrives.
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(quietDuration)
		case <-quiet.C:
			// 80 ms of silence after output — shell is ready at prompt.
			s.Write([]byte(cmd + "\n"))
			return
		}
	}
}

// MetaCh exposes the shared meta broadcast channel for the WebSocket handler.
func (s *Session) MetaCh() <-chan protocol.MetaMessage { return s.metaCh }

// SendCurrentMeta sends the current metadata as a JSON WebSocket text frame.
// The conn parameter is a *websocket.Conn but typed as any to avoid an import
// cycle — the caller (websocket.go) already has the import.
func (s *Session) SendCurrentMeta(conn interface {
	WriteMessage(messageType int, data []byte) error
}) {
	s.mu.Lock()
	frame := protocol.MetaMessage{
		Type:    "meta",
		Name:    s.Meta.Name,
		Status:  s.Meta.Status,
		Detail:  s.Meta.Detail,
		CWD:     s.Meta.CWD,
		Program: s.Meta.Program,
		Favicon: s.Favicon,
	}
	s.mu.Unlock()
	b, _ := json.Marshal(frame)
	conn.WriteMessage(1, b) //nolint:errcheck
}

func (s *Session) Write(data []byte) {
	s.ptmx.Write(data) //nolint:errcheck
}

func (s *Session) Resize(cols, rows uint16) {
	resize(s.ptmx, cols, rows)
}

func (s *Session) MarshalJSON() ([]byte, error) {
	type pub struct {
		ID       string    `json:"id"`
		Scheme   string    `json:"scheme"`
		Path     string    `json:"path"`
		Command  []string  `json:"command"`
		Created  time.Time `json:"created"`
		LastSeen time.Time `json:"lastSeen"`
		Alive    bool      `json:"alive"`
		Dormant  bool      `json:"dormant,omitempty"`
		Meta     Meta      `json:"meta"`
		Favicon  string    `json:"favicon,omitempty"`
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Marshal(pub{
		ID:       s.ID,
		Scheme:   s.Scheme,
		Path:     s.Path,
		Command:  s.Command,
		Created:  s.Created,
		LastSeen: s.LastSeen,
		Alive:    s.Alive,
		Dormant:  !s.Alive && s.tmux != nil,
		Meta:     s.Meta,
		Favicon:  s.Favicon,
	})
}

