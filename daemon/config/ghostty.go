package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Keybinding maps a key combination to a literal byte sequence sent to the PTY.
// Only the "text:" action is supported (covers the vast majority of use cases).
type Keybinding struct {
	Keys     string `json:"keys"`     // e.g. "shift+enter"
	Sequence string `json:"sequence"` // bytes to send, e.g. "\x1b\r"
}

// TerminalTheme is the subset of Ghostty settings that xterm.js can consume.
type TerminalTheme struct {
	FontFamily          string       `json:"fontFamily"`
	FontSize            float64      `json:"fontSize"`
	FontWeight          string       `json:"fontWeight"`     // e.g. "600"
	FontWeightBold      string       `json:"fontWeightBold"` // e.g. "800"
	Background          string       `json:"background"`
	BackgroundOpacity   float64      `json:"backgroundOpacity"` // 0–1
	Foreground          string       `json:"foreground"`
	Cursor              string       `json:"cursor"`
	CursorAccent        string       `json:"cursorAccent"`       // cursor-text
	CursorStyle         string       `json:"cursorStyle"`        // block|underline|bar
	CursorBlink         bool         `json:"cursorBlink"`
	SelectionBg         string       `json:"selectionBackground"`
	SelectionFg         string       `json:"selectionForeground"`
	BoldIsBright        bool         `json:"boldIsBright"`
	CopyOnSelect        bool         `json:"copyOnSelect"`
	ScrollbackLines     int          `json:"scrollback"`
	Colors              [16]string   `json:"colors"` // ANSI 0–15
	Keybindings         []Keybinding `json:"keybindings"`
}

func DefaultTheme() TerminalTheme {
	return TerminalTheme{
		FontFamily:        `"Menlo", "SF Mono", "Cascadia Code", monospace`,
		FontSize:          13,
		Background:        "#1a1a1a",
		BackgroundOpacity: 1.0,
		Foreground:        "#d4d4d4",
		Cursor:            "#d4d4d4",
		CursorStyle:       "block",
		CursorBlink:       true,
		ScrollbackLines:   10000,
	}
}

func GhosttyConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "com.mitchellh.ghostty", "config")
}

func themeConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ttyrannosaurus", "theme.ghostty")
}

// LoadGhosttyTheme loads the theme from the user-saved paste first, then falls
// back to Ghostty's own config, then falls back to defaults.
func LoadGhosttyTheme() TerminalTheme {
	if data, err := os.ReadFile(themeConfigPath()); err == nil {
		return ParseGhosttyConfig(string(data))
	}
	if data, err := os.ReadFile(GhosttyConfigPath()); err == nil {
		return ParseGhosttyConfig(string(data))
	}
	return DefaultTheme()
}

// ParseGhosttyConfig parses a Ghostty config string and returns a TerminalTheme.
// Unrecognised keys are silently ignored.
func ParseGhosttyConfig(text string) TerminalTheme {
	t := DefaultTheme()
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Ghostty does NOT support inline comments, but we trim trailing
		// whitespace-hash to be lenient with user pastes.
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		applyGhosttyKey(&t, strings.TrimSpace(key), strings.TrimSpace(val))
	}
	return t
}

// SaveThemeConfig persists a pasted Ghostty config string so it survives daemon restarts.
func SaveThemeConfig(text string) error {
	path := themeConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func applyGhosttyKey(t *TerminalTheme, key, val string) {
	colour := normaliseColour

	switch key {
	case "font-family":
		t.FontFamily = fmt.Sprintf("%q, monospace", val)
	case "font-size":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			t.FontSize = f
		}
	case "font-style":
		t.FontWeight = ghosttyStyleToWeight(val)
	case "font-style-bold":
		t.FontWeightBold = ghosttyStyleToWeight(val)
	case "background":
		t.Background = colour(val)
	case "background-opacity":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			t.BackgroundOpacity = f
		}
	case "foreground":
		t.Foreground = colour(val)
	case "cursor-color":
		t.Cursor = colour(val)
	case "cursor-text":
		t.CursorAccent = colour(val)
	case "cursor-style":
		t.CursorStyle = strings.ToLower(val)
	case "cursor-style-blink":
		t.CursorBlink = val == "true"
	case "selection-background":
		t.SelectionBg = colour(val)
	case "selection-foreground":
		t.SelectionFg = colour(val)
	case "bold-is-bright":
		t.BoldIsBright = val == "true"
	case "copy-on-select":
		t.CopyOnSelect = val == "true"
	case "scrollback-limit":
		if n, err := strconv.Atoi(val); err == nil {
			t.ScrollbackLines = n
		}
	case "palette":
		// palette = N=#rrggbb
		idxStr, hexVal, ok := strings.Cut(val, "=")
		if !ok {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(idxStr))
		if err != nil || n < 0 || n > 15 {
			return
		}
		t.Colors[n] = colour(strings.TrimSpace(hexVal))
	case "keybind":
		// keybind = mod+key=text:\xNN...
		combo, action, ok := strings.Cut(val, "=")
		if !ok {
			return
		}
		if !strings.HasPrefix(action, "text:") {
			return // only handle text: actions
		}
		seq := parseTextSequence(strings.TrimPrefix(action, "text:"))
		if seq != "" {
			t.Keybindings = append(t.Keybindings, Keybinding{
				Keys:     strings.ToLower(strings.TrimSpace(combo)),
				Sequence: seq,
			})
		}
	}
}

// ghosttyStyleToWeight maps Ghostty font-style names to CSS font-weight values.
func ghosttyStyleToWeight(style string) string {
	switch strings.ReplaceAll(strings.ToLower(style), " ", "") {
	case "thin", "extralight", "ultralight":
		return "100"
	case "light":
		return "300"
	case "regular", "normal", "book":
		return "400"
	case "medium":
		return "500"
	case "semibold", "demibold":
		return "600"
	case "bold":
		return "700"
	case "extrabold", "ultrabold":
		return "800"
	case "black", "heavy":
		return "900"
	}
	return "400"
}

// normaliseColour ensures a colour value has a leading #.
func normaliseColour(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(v, "#")
	return "#" + v
}

// parseTextSequence converts Ghostty text: escape notation to actual bytes.
// Handles \xNN hex escapes, \n, \r, \t, \\ and bare characters.
func parseTextSequence(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			i++
			continue
		}
		next := s[i+1]
		switch next {
		case 'x', 'X':
			if i+3 < len(s) {
				b, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
				if err == nil {
					out.WriteByte(byte(b))
					i += 4
					continue
				}
			}
			out.WriteByte('\\')
			i++
		case 'n':
			out.WriteByte('\n')
			i += 2
		case 'r':
			out.WriteByte('\r')
			i += 2
		case 't':
			out.WriteByte('\t')
			i += 2
		case '\\':
			out.WriteByte('\\')
			i += 2
		default:
			out.WriteByte('\\')
			i++
		}
	}
	return out.String()
}
