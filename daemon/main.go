package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/thejens/ttyrannosaurus/daemon/config"
	"github.com/thejens/ttyrannosaurus/daemon/session"
	"github.com/thejens/ttyrannosaurus/daemon/template"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	theme := config.LoadGhosttyTheme()
	resolver := template.New(cfg.Schemes)
	mgr := session.NewManager()

	// Restore sessions that were alive before this daemon instance started.
	// Tmux-backed sessions that are still running in tmux become "dormant"
	// and reconnect seamlessly when a terminal tab opens.
	tmuxCfg := session.TmuxCfg{Enabled: cfg.Tmux.Enabled, Socket: cfg.Tmux.Socket}
	for _, ps := range session.LoadPersistedSessions() {
		if ps.Tmux != nil && session.IsAlive(*ps.Tmux) {
			mgr.Restore(ps)
			log.Printf("restored tmux session %s (%s)", ps.ID, ps.Tmux.Name)
		} else if ps.Tmux == nil {
			// Non-tmux session: PTY died with the daemon, clean up the record.
			session.RemovePersistedSession(ps.ID)
		} else if !session.UseTmux(tmuxCfg) {
			session.RemovePersistedSession(ps.ID)
		}
	}

	srv := newServer(cfg, resolver, mgr, theme)

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("ttyrannosaurus listening on http://%s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server: %v", err)
	}
}
