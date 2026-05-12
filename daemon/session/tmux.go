package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TmuxCfg is the subset of config.TmuxConfig needed here (avoids import cycle).
type TmuxCfg struct {
	Enabled   string   // "true" | "false" | "auto"
	Socket    string   // tmux -L argument, defaults to "ttyrannosaurus"
	ExtraArgs []string // extra args appended to tmux new-session
}

// tmuxAvailable reports whether tmux is on PATH.
var tmuxAvailable = func() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}()

// UseTmux returns true if tmux backing should be used given the config.
func UseTmux(cfg TmuxCfg) bool {
	switch strings.ToLower(cfg.Enabled) {
	case "true", "yes", "1":
		return tmuxAvailable
	case "false", "no", "0":
		return false
	default: // "auto" or empty
		return tmuxAvailable
	}
}

func tmuxSocket(cfg TmuxCfg) string {
	if cfg.Socket != "" {
		return cfg.Socket
	}
	return "ttyrannosaurus"
}

// TmuxSession bundles what the daemon needs to know about a tmux-backed session.
type TmuxSession struct {
	Name      string   `json:"name"`                // tmux session name
	Socket    string   `json:"socket"`              // -L value
	ExtraArgs []string `json:"extraArgs,omitempty"` // extra args for new-session
}

var unsafeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeName(id string) string {
	return unsafeRe.ReplaceAllString(id, "-")
}

// EnsureServerOptions applies daemon-required global options to the tmux server
// on the given socket. Safe to call on every session creation — tmux set-option
// is idempotent and fast. Using our isolated socket means this never touches the
// user's own ~/.tmux.conf or main tmux server.
func EnsureServerOptions(socket string) {
	// focus-events: forward terminal focus-in/focus-out sequences (ESC[I / ESC[O)
	// through tmux to the running application. Without this, editors like Neovim
	// and Helix warn that focus tracking is disabled and skip autoread/cursor-shape.
	exec.Command("tmux", "-L", socket, "set-option", "-g", "focus-events", "on").Run() //nolint:errcheck
}

// WrapInTmux wraps command so it runs inside a named tmux session.
// `tmux new-session -A` creates if absent, attaches if the session is already live.
// ts.ExtraArgs are inserted before the shell command, e.g. "-e TERM=xterm-256color".
func WrapInTmux(command []string, ts TmuxSession) []string {
	args := []string{"tmux", "-L", ts.Socket, "new-session", "-A", "-s", ts.Name}
	args = append(args, ts.ExtraArgs...)
	args = append(args, shellJoinTmux(command))
	return args
}

// AttachCommand returns the command to reattach to an existing tmux session.
func AttachCommand(ts TmuxSession) []string {
	return []string{"tmux", "-L", ts.Socket, "attach-session", "-t", ts.Name}
}

// IsAlive returns true if the tmux session is currently running.
func IsAlive(ts TmuxSession) bool {
	err := exec.Command("tmux", "-L", ts.Socket, "has-session", "-t", ts.Name).Run()
	return err == nil
}

// LiveNames returns the names of all live sessions on the given socket.
func LiveNames(socket string) []string {
	out, err := exec.Command("tmux", "-L", socket, "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names
}

func shellJoinTmux(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

// ── Persistent session index ───────────────────────────────────────────────

// PersistedSession is the on-disk record written at session creation and
// deleted at kill, enabling the daemon to restore sessions on restart.
type PersistedSession struct {
	ID      string       `json:"id"`
	Scheme  string       `json:"scheme"`
	Path    string       `json:"path"`
	Command []string     `json:"command"`
	Created time.Time    `json:"created"`
	Tmux    *TmuxSession `json:"tmux,omitempty"` // nil when not tmux-backed
}

func sessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ttyrannosaurus", "sessions")
}

func sessionFilePath(id string) string {
	return filepath.Join(sessionsDir(), sanitizeName(id)+".json")
}

// PersistSession writes the session record to disk.
func PersistSession(ps PersistedSession) error {
	if err := os.MkdirAll(sessionsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(ps)
	if err != nil {
		return err
	}
	return os.WriteFile(sessionFilePath(ps.ID), b, 0o644)
}

// RemovePersistedSession deletes the on-disk record.
func RemovePersistedSession(id string) {
	os.Remove(sessionFilePath(id)) //nolint:errcheck
}

// LoadPersistedSessions reads all session records from disk.
func LoadPersistedSessions() []PersistedSession {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return nil
	}
	var out []PersistedSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionsDir(), e.Name()))
		if err != nil {
			continue
		}
		var ps PersistedSession
		if json.Unmarshal(data, &ps) == nil {
			out = append(out, ps)
		}
	}
	return out
}

// NewTmuxSession creates a TmuxSession for the given session ID and config.
func NewTmuxSession(id string, cfg TmuxCfg) TmuxSession {
	return TmuxSession{
		Name:      "tyr-" + sanitizeName(id),
		Socket:    tmuxSocket(cfg),
		ExtraArgs: cfg.ExtraArgs,
	}
}
