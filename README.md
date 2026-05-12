# ttyrannosaurus 🦖

Terminal sessions in Chrome tabs. A Go daemon manages PTY sessions; a Chrome extension gives you an omnibox shortcut, a live sidebar, and tab-tracked sessions that survive browser restarts.

---

## How it works

```
! claude          ← type in Chrome address bar
   │
   ▼
daemon (http://127.0.0.1:7071)
   │  spawns PTY, optionally wraps in tmux
   │
   ▼
/connect/<session-id>    ← Chrome tab with xterm.js terminal
   │
   ▼
WebSocket ws://127.0.0.1:7071/ws/<id>
   │  bidirectional: keystrokes → PTY, PTY output → xterm.js
   ▼
Sidebar panel  ← live session list, metadata, CWD, status
```

Sessions are backed by tmux by default, so they survive daemon restarts, browser crashes, and tab closes. Reopen the tab, get your session back.

---

## Features

- **Omnibox shortcut** — type `!` in the address bar to open a terminal or run a command
- **tmux backing** — sessions persist across daemon/browser restarts (configurable)
- **Live sidebar** — session cards with CWD, foreground program, and status indicators
- **Claude Code integration** — status tracking via OSC 9;4 (busy / waiting / error / idle), favicon switching, activity-based metadata
- **AI session naming** — on-device Gemini Nano (Chrome 138+) labels sessions from their output; renames when new activity is detected
- **Split view** — Alt+click a link to snap the terminal left, open URL right
- **Ghostty theme** — paste your Ghostty config in settings; colours apply to new tabs immediately
- **Link handling** — `file://` links open in your configured editor at the right line

---

## Requirements

- **macOS** (Ventura 13+ recommended)
- **Go 1.22+**
- **Google Chrome 120+** (Chrome 138+ for AI naming)
- **tmux** — optional but strongly recommended (`brew install tmux`)
- **Xcode Command Line Tools** — for the URL handler: `xcode-select --install`

---

## Installation

```bash
git clone https://github.com/thejens/ttyrannosaurus.git
cd ttyrannosaurus
make install
```

`make install` does four things:

1. Downloads xterm.js assets from jsDelivr
2. Compiles the daemon binary → `/usr/local/bin/ttyrannosaurus`
3. Builds and registers the `ttyrannosaurus://` URL handler app
4. Installs a launchd agent so the daemon starts automatically at login

Verify the daemon is running:

```bash
curl http://127.0.0.1:7071/api/health
# → {"ok":true}
```

### Chrome extension

The extension is not on the Chrome Web Store. Load it unpacked:

1. Open `chrome://extensions`
2. Enable **Developer mode** (top-right toggle)
3. Click **Load unpacked**
4. Select the `extension/` folder from this repo

Pin the ttyrannosaurus icon to your toolbar for quick sidebar access.

---

## Usage

### Opening terminals

Type `!` in the Chrome address bar followed by any command:

| What you type | What happens |
|---|---|
| `! claude` | New terminal running `claude` |
| `! ls -la` | New terminal, runs `ls -la` after shell init |
| `! zsh` | Plain interactive shell |
| `!` (blank) | New shell in home directory |

The session inherits the CWD of whatever terminal tab is currently focused.

### Sidebar

Click the ttyrannosaurus toolbar icon to open the sidebar. From there you can:

- Click a session card to navigate to it
- Right-click to rename a session
- Click × to kill a session
- Use the **+** button to open a new terminal in the current CWD

### Settings

Open `chrome://extensions` → ttyrannosaurus → **Extension options**, or click the gear icon in the sidebar footer.

- **Sidebar** — keep open always, or auto-show only when a terminal tab is active
- **tmux** — auto / always on / disabled, custom socket name, extra `new-session` flags
- **Session names** — enable/disable AI naming, configure rename interval
- **Terminal theme** — paste a Ghostty config block; colours apply immediately

### Config file

`~/.config/ttyrannosaurus/config.yaml` is created on first run:

```yaml
port: 7071

tmux:
  enabled: auto      # auto | true | false
  socket: ttyrannosaurus

schemes:
  claude:
    favicon: "https://claude.ai/favicon.ico"
    monitor: claude-code
```

Add a `schemes` entry to give any command a custom favicon and status monitor:

```yaml
schemes:
  nvim:
    favicon: "https://neovim.io/favicon.ico"
  python:
    favicon: "https://python.org/favicon.ico"
```

---

## Development

```bash
# Build (downloads assets if needed)
make build

# Run tests
go test ./...

# Start/restart the daemon (logs to /tmp/ttyrannosaurus.log)
pkill -f bin/ttyrannosaurus 2>/dev/null
nohup ./bin/ttyrannosaurus &

# After changing extension files: reload at chrome://extensions
# After changing manifest.json: remove + re-add the extension
```

### Project layout

```
daemon/
  main.go          entry point, config loading, session restore
  server.go        HTTP router (chi), REST + WebSocket endpoints
  websocket.go     PTY fan-out, displaced-tab signalling, replay
  session/
    manager.go     session registry, ring buffer, VT event handling, detectLoop
    tmux.go        tmux session lifecycle, persistent session index
    pty.go         PTY spawn helpers
  vt/
    parser.go      stateful VT/ANSI parser (OSC, CSI, CRLF, spinners)
  monitor/
    monitor.go     line-based metadata extraction (Claude Code built-in + config rules)
  protocol/
    messages.go    shared wire types for daemon ↔ sidebar WebSocket
  config/
    config.go      YAML config, scheme/monitor/tmux structs
    ghostty.go     Ghostty config parser for terminal themes
  template/
    resolver.go    URL path → session ID + command resolution
  static/
    index.html     xterm.js terminal frontend
    split.html     side-by-side split view

extension/
  background.js    service worker: omnibox, tab tracking, sidebar port management
  content.js       page bridge: close-tab, open-in-split postMessage relay
  sidepanel/
    sidepanel.js   session list, AI naming, metadata display
  options/
    options.js     settings: sidebar mode, tmux config, AI naming, theme
```

---

## Uninstall

```bash
make uninstall
```

This stops the daemon, removes the launchd agent, binary, and URL handler app. Your config and session state in `~/.config/ttyrannosaurus/` are left intact.

---

## License

MIT
