package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
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

func (s *server) serveTerminalPage(w http.ResponseWriter, sessionID string) {
	s.themeMu.RLock()
	theme := s.theme
	s.themeMu.RUnlock()

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
		Port:       s.cfg.Port,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := termTmpl.Execute(w, data); err != nil {
		log.Printf("template execute: %v", err)
	}
}

func (s *server) serveSplitPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := splitTmpl.Execute(w, map[string]any{"Port": s.cfg.Port}); err != nil {
		log.Printf("split template execute: %v", err)
	}
}
