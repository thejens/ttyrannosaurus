// Wire protocol types shared between the daemon (Go) and extension (JS).
// Mirrors daemon/protocol/messages.go — keep in sync when adding message types.

/**
 * @typedef {{ name?: string, status?: string, detail?: string, cwd?: string, program?: string }} Meta
 * @typedef {{ id: string, scheme: string, path: string, command: string[], created: string, lastSeen: string, alive: boolean, dormant?: boolean, meta: Meta, favicon?: string }} SessionState
 */

// Terminal WebSocket messages (xterm.js ↔ daemon, per /ws/{sessionID})

/**
 * @typedef {{ type: "meta", name?: string, status?: string, detail?: string, cwd?: string, program?: string, favicon?: string }} MetaMessage
 * @typedef {{ type: "displaced" }} DisplacedMessage
 * @typedef {{ type: "resize", cols: number, rows: number }} ResizeMessage
 * @typedef {MetaMessage | DisplacedMessage} DaemonTerminalMessage
 */

// Sidebar WebSocket messages (sidepanel ↔ daemon, per /api/ws)

/**
 * @typedef {{ type: "sessions", sessions: SessionState[] }} SessionsMessage
 * @typedef {{ type: "session:created", session: SessionState }} SessionCreatedMessage
 * @typedef {{ type: "session:updated", session: SessionState }} SessionUpdatedMessage
 * @typedef {{ type: "session:killed", id: string }} SessionKilledMessage
 * @typedef {SessionsMessage | SessionCreatedMessage | SessionUpdatedMessage | SessionKilledMessage} DaemonSidebarMessage
 */
