---
name: install
description: Interactive installation guide for ttyrannosaurus. Walks through prerequisites, building, daemon setup, and Chrome extension loading. Use when the user asks how to install, set up, or get ttyrannosaurus running.
allowed-tools: Bash Read Write
---

Guide the user through a complete installation of ttyrannosaurus. Follow each phase in order. After each shell command, check the output and report clearly whether it succeeded or failed before moving on.

---

## Phase 1 — Prerequisites

Check each prerequisite and tell the user what's missing:

```bash
echo "=== Go ===" && go version 2>/dev/null || echo "MISSING"
echo "=== tmux ===" && tmux -V 2>/dev/null || echo "NOT FOUND (optional but recommended)"
echo "=== curl ===" && curl --version | head -1
echo "=== Xcode CLI ===" && xcode-select -p 2>/dev/null || echo "MISSING — run: xcode-select --install"
echo "=== Chrome ===" && ls /Applications/Google\ Chrome.app 2>/dev/null && echo "found" || echo "MISSING"
```

If Go is missing, tell the user to install it from https://go.dev/dl/ and stop — nothing else will work without it.

If Xcode CLI tools are missing, they only affect the URL handler (`ttyrannosaurus://` scheme). Tell the user they can still proceed; the URL handler is optional.

If tmux is missing, note it's optional but strongly recommended for session persistence. They can install it later with `brew install tmux`.

---

## Phase 2 — Build

```bash
cd "$(git rev-parse --show-toplevel)" && make build 2>&1
```

This downloads xterm.js from jsDelivr and compiles the Go binary. It takes 30–60 seconds on a cold cache.

If `make build` fails:
- Check that `go version` is 1.22 or newer
- Check for network errors downloading xterm.js assets — the CDN URLs are in the Makefile

---

## Phase 3 — Install daemon + URL handler

```bash
cd "$(git rev-parse --show-toplevel)" && make install 2>&1
```

This:
1. Copies the binary to `/usr/local/bin/ttyrannosaurus`
2. Builds and registers the `ttyrannosaurus://` URL handler (needs Xcode CLI tools)
3. Installs a launchd agent so the daemon starts at login

If the URL handler step fails (Swift compiler not found), offer to run just the daemon install:

```bash
sudo cp bin/ttyrannosaurus /usr/local/bin/ttyrannosaurus
mkdir -p ~/Library/LaunchAgents
cp daemon/com.ttyrannosaurus.daemon.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.ttyrannosaurus.daemon.plist
```

---

## Phase 4 — Verify the daemon

```bash
sleep 1 && curl -s http://127.0.0.1:7071/api/health
```

Expected output: `{"ok":true}`

If you get `Connection refused`:
- Check logs: `cat /tmp/ttyrannosaurus.log`
- Try starting manually: `ttyrannosaurus &` and retry the health check
- Check if another process is on port 7071: `lsof -i :7071`

---

## Phase 5 — Chrome extension

The extension must be loaded unpacked (it's not on the Web Store yet). Give these exact instructions:

1. Open Chrome and go to `chrome://extensions`
2. Enable **Developer mode** using the toggle in the top-right corner
3. Click **Load unpacked**
4. In the file picker, navigate to this repo's `extension/` folder and click **Open**

The ttyrannosaurus icon (a green T-Rex) should appear in the Chrome toolbar. If it doesn't appear immediately, click the puzzle-piece icon to find it and pin it.

Tell the user: if they see "Manifest file is missing or unreadable", they selected the wrong folder — it must be the `extension/` subfolder, not the repo root.

---

## Phase 6 — First terminal

Ask the user to try it:

1. Click in the Chrome address bar
2. Type `!` followed by a space
3. Type `zsh` and press Enter

A new tab should open with an interactive shell. The sidebar should appear on the right showing the session.

If the tab opens but shows "Daemon offline", the extension can't reach the daemon — verify `curl http://127.0.0.1:7071/api/health` works in a terminal.

---

## Phase 7 — Optional: config

Show the user where the config lives:

```bash
cat ~/.config/ttyrannosaurus/config.yaml
```

Key things to mention:
- `tmux.enabled: auto` — uses tmux if available, which gives session persistence across restarts
- To add a favicon/monitor for a command, add it under `schemes:`
- The settings page in the extension (`chrome://extensions` → ttyrannosaurus → Extension options) controls sidebar mode, tmux options, AI naming, and the terminal colour theme

---

## Wrap-up

Confirm everything works:

```bash
curl -s http://127.0.0.1:7071/api/sessions | python3 -m json.tool 2>/dev/null || curl -s http://127.0.0.1:7071/api/sessions
```

If there are sessions in the list, setup is complete. Tell the user:

- `!` in the address bar opens terminals
- The sidebar icon toggles the session panel
- `make uninstall` cleanly removes everything if needed
- Logs are at `/tmp/ttyrannosaurus.log`
