package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/thejens/ttyrannosaurus/daemon/protocol"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" ||
			strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1") ||
			strings.HasPrefix(origin, "chrome-extension://")
	},
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	// GetOrWake wakes tmux-backed dormant sessions that were restored from
	// disk but haven't had their PTY spawned yet.
	sess, err := s.mgr.GetOrWake(sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("wake session %q: %v", sessionID, err), http.StatusInternalServerError)
		return
	}
	if sess == nil {
		// Upgrade the WebSocket so the frontend receives a close frame instead of
		// a raw 404 page, which lets it show a "session not found" overlay.
		conn, upgradeErr := upgrader.Upgrade(w, r, nil)
		if upgradeErr != nil {
			return
		}
		conn.WriteMessage(websocket.CloseMessage, //nolint:errcheck
			websocket.FormatCloseMessage(4002, "session not found"))
		conn.Close()
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Unique client key so fan-out broadcaster can address this connection.
	clientID := fmt.Sprintf("%p", conn)
	replay, cc := sess.Subscribe(clientID)
	defer sess.Unsubscribe(clientID)

	sess.SendCurrentMeta(conn)

	if len(replay) > 0 {
		if err := conn.WriteMessage(websocket.BinaryMessage, replay); err != nil {
			return
		}
	}
	// Always send replay-end so the frontend knows it is safe to forward
	// user input and xterm.js-generated protocol responses to the PTY.
	// Without this, DA responses from processing the replay buffer leak into
	// the shell's readline as literal characters (e.g. "1;2c0;276;0c").
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"replay-end"}`)) //nolint:errcheck

	displaced, _ := json.Marshal(protocol.DisplacedMessage{Type: "displaced"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case chunk, ok := <-cc.Data:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					return
				}
			case frame, ok := <-sess.MetaCh():
				if !ok {
					// Session process exited cleanly. Send a distinguished close code
					// so the frontend can show "session ended" instead of reconnecting.
					conn.WriteMessage(websocket.CloseMessage, //nolint:errcheck
						websocket.FormatCloseMessage(4001, "session ended"))
					return
				}
				b, _ := json.Marshal(frame)
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			case <-cc.Displaced:
				conn.WriteMessage(websocket.TextMessage, displaced) //nolint:errcheck
				// Keep the goroutine running — the tab still shows the terminal
				// (read-only) and the overlay lets the user decide what to do.
			}
		}
	}()

	// Main loop: WebSocket → PTY (keyboard input + resize from browser).
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch msgType {
		case websocket.TextMessage:
			// Resize message: {"type":"resize","cols":N,"rows":M}
			var msg protocol.ResizeMessage
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				sess.Resize(msg.Cols, msg.Rows)
			}
		case websocket.BinaryMessage:
			sess.Write(data)
		}
	}
	<-done
}
