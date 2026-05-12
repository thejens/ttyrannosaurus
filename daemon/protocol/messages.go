package protocol

import "time"

// Terminal WebSocket messages — sent between daemon and xterm.js frontend.

// MetaMessage is sent as a JSON text frame when session metadata changes.
type MetaMessage struct {
	Type    string `json:"type"` // always "meta"
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"`
	Detail  string `json:"detail,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Program string `json:"program,omitempty"`
	Favicon string `json:"favicon"` // always present; empty string means "revert to default"
}

// DisplacedMessage is sent when another client opens the same session.
type DisplacedMessage struct {
	Type string `json:"type"` // always "displaced"
}

// ResizeMessage is sent by the browser to resize the PTY.
type ResizeMessage struct {
	Type string `json:"type"` // "resize"
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Sidebar WebSocket messages — sent from daemon to sidepanel on /api/ws.

// Meta is the public wire representation of session metadata.
// It mirrors monitor.Meta without creating an import dependency.
type Meta struct {
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"`
	Detail  string `json:"detail,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Program string `json:"program,omitempty"`
}

// SessionState is the public JSON shape of a session sent to the sidebar.
type SessionState struct {
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

// SessionsMessage is the initial snapshot sent on sidebar WebSocket connect.
type SessionsMessage struct {
	Type     string         `json:"type"` // "sessions"
	Sessions []SessionState `json:"sessions"`
}

// SessionCreatedMessage is sent when a new session is started.
type SessionCreatedMessage struct {
	Type    string       `json:"type"` // "session:created"
	Session SessionState `json:"session"`
}

// SessionUpdatedMessage is sent when a session's metadata or alive state changes.
type SessionUpdatedMessage struct {
	Type    string       `json:"type"` // "session:updated"
	Session SessionState `json:"session"`
}

// SessionKilledMessage is sent when a session is removed from the registry.
type SessionKilledMessage struct {
	Type string `json:"type"` // "session:killed"
	ID   string `json:"id"`
}
