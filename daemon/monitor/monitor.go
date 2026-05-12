// Package monitor provides session metadata extraction from PTY output.
//
// A Monitor watches stripped terminal lines and emits Meta updates when it
// detects status changes. The system is deliberately generic: add a new tool
// by writing a MonitorConfig in config.yaml — no code required unless you
// want a named built-in with richer heuristics.
package monitor

import (
	"regexp"
	"strings"

	"github.com/thejens/ttyrannosaurus/daemon/config"
)

// Meta is the mutable metadata the sidebar displays for a session.
type Meta struct {
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"` // busy | idle | error | waiting | ""
	Detail  string `json:"detail,omitempty"` // one-line description of current activity
	CWD     string `json:"cwd,omitempty"`    // current working directory (from OSC 7)
	Program string `json:"program,omitempty"` // foreground executable name
}

// Monitor watches stripped terminal lines and returns a Meta pointer when
// something changed, or nil when nothing notable happened.
type Monitor interface {
	Feed(line string) *Meta
}

// New builds a Monitor from a SchemeConfig.
// Returns nil if no monitor is configured for the scheme.
func New(cfg config.MonitorConfig) Monitor {
	if cfg.Named != "" {
		switch cfg.Named {
		case "claude-code":
			return &claudeCode{}
		}
	}
	if len(cfg.Patterns) > 0 {
		return newLineParser(cfg.Patterns)
	}
	return nil
}

// ── ANSI stripping ─────────────────────────────────────────────────────────

var ansiRe = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-9;]*[A-Za-z]|\][^\x07]*\x07)`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ── Claude Code built-in ───────────────────────────────────────────────────

// claudeCode extracts activity detail from Claude Code's terminal output.
//
// Design: OSC 9;4 (ConEmu progress protocol) is the primary source of Status —
// the session handler sets it directly when that escape is received. This monitor
// is responsible for:
//   - Detail text: what Claude is currently doing ("Analyzing files…", tool name)
//   - Status as fallback: for older Claude Code versions that don't emit OSC 9;4
//
// Adding support for a new TUI follows the same pattern: implement Monitor,
// return a Meta with Detail (and optionally Status as fallback), register it in New().
type claudeCode struct {
	last Meta
}

// Spinner runes Claude Code cycles through while thinking.
const spinners = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func (c *claudeCode) Feed(line string) *Meta {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return nil
	}

	r := []rune(line)
	first := string(r[0])
	rest := strings.TrimSpace(string(r[1:]))

	var next Meta
	switch {
	case strings.ContainsRune(spinners, r[0]):
		next.Status = "busy"
		next.Detail = rest
	case first == "⎿":
		next.Status = "busy"
		next.Detail = rest
	case first == "✓":
		next.Status = "idle"
	case first == "✗":
		next.Status = "error"
		next.Detail = rest
	case strings.HasPrefix(line, "● "):
		next.Status = "busy"
		next.Detail = strings.TrimPrefix(line, "● ")
	default:
		return nil
	}

	if next == c.last {
		return nil
	}
	c.last = next
	return &next
}

// ── Generic line-parser ────────────────────────────────────────────────────

type compiledPattern struct {
	re     *regexp.Regexp
	status string
	detail string // may contain "$1", "$2" etc.
	name   string
}

type lineParser struct {
	patterns []compiledPattern
	last     Meta
}

func newLineParser(cfgPatterns []config.MonitorPattern) *lineParser {
	lp := &lineParser{}
	for _, p := range cfgPatterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			continue // skip invalid patterns silently
		}
		lp.patterns = append(lp.patterns, compiledPattern{
			re:     re,
			status: p.Status,
			detail: p.Detail,
			name:   p.Name,
		})
	}
	return lp
}

func (l *lineParser) Feed(line string) *Meta {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return nil
	}
	for _, p := range l.patterns {
		m := p.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		next := Meta{
			Status: p.status,
			Detail: expandCaptures(p.detail, m),
			Name:   expandCaptures(p.name, m),
		}
		if next == l.last {
			return nil
		}
		l.last = next
		return &next
	}
	return nil
}

func expandCaptures(tmpl string, matches []string) string {
	for i := len(matches) - 1; i >= 1; i-- {
		tmpl = strings.ReplaceAll(tmpl, "$"+strings.Repeat("0", len(matches)-i)+string(rune('0'+i)), matches[i])
		// simple form: $1, $2, ...
		tmpl = strings.ReplaceAll(tmpl, "$"+string(rune('0'+i)), matches[i])
	}
	return tmpl
}
