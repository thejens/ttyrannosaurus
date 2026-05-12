package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"sync"

	"github.com/thejens/ttyrannosaurus/daemon/config"
)

//go:embed static
var staticFiles embed.FS

var termTmpl  = template.Must(template.ParseFS(staticFiles, "static/index.html"))
var splitTmpl = template.Must(template.ParseFS(staticFiles, "static/split.html"))

type terminalPageData struct {
	SessionID  string
	ThemeJSON  template.JS  // raw JSON, not HTML-escaped
	Background template.CSS // safe CSS colour value
	Port       int
}

func serveTerminalPage(w http.ResponseWriter, sessionID string, _ config.TerminalTheme) {
	s_themeMu.RLock()
	theme := s_theme
	s_themeMu.RUnlock()

	themeJSON, err := json.Marshal(theme)
	if err != nil {
		http.Error(w, "theme marshal error", http.StatusInternalServerError)
		return
	}
	bg := theme.Background
	if bg == "" {
		bg = "#1a1a1a"
	}
	data := terminalPageData{
		SessionID:  sessionID,
		ThemeJSON:  template.JS(themeJSON),
		Background: template.CSS(bg),
		Port:       s_port, // set in main
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := termTmpl.Execute(w, data); err != nil {
		log.Printf("template execute: %v", err)
	}
}

func serveSplitPage(w http.ResponseWriter, port int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := splitTmpl.Execute(w, map[string]any{"Port": port}); err != nil {
		log.Printf("split template execute: %v", err)
	}
}

// s_port and s_theme are set by main() and updated by handlePutTheme so that
// serveTerminalPage always uses the latest values.
var s_port  int
var s_theme config.TerminalTheme
var s_themeMu sync.RWMutex

func setTheme(t config.TerminalTheme) {
	s_themeMu.Lock()
	s_theme = t
	s_themeMu.Unlock()
}
