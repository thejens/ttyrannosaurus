package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Template struct {
	Pattern string   `yaml:"pattern" json:"pattern"`
	Command []string `yaml:"command"  json:"command"`
	Unique  bool     `yaml:"unique,omitempty" json:"unique,omitempty"`
	Shell   bool     `yaml:"shell,omitempty"  json:"shell,omitempty"`
}

// MonitorPattern is a single rule in an inline rule-based monitor.
type MonitorPattern struct {
	Regex  string `yaml:"regex"            json:"regex"`
	Status string `yaml:"status,omitempty" json:"status,omitempty"` // busy|idle|error|waiting
	Detail string `yaml:"detail,omitempty" json:"detail,omitempty"` // "$1" expands capture groups
	Name   string `yaml:"name,omitempty"   json:"name,omitempty"`
}

// MonitorConfig is either a named built-in ("claude-code") or an inline
// rule set. In YAML, both forms are supported:
//
//	monitor: claude-code         # string → named built-in
//	monitor:                     # map    → inline rules
//	  patterns:
//	    - regex: '^Thinking'
//	      status: busy
type MonitorConfig struct {
	Named    string           `yaml:"name,omitempty"     json:"name,omitempty"`
	Patterns []MonitorPattern `yaml:"patterns,omitempty" json:"patterns,omitempty"`
}

func (m *MonitorConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		m.Named = value.Value
		return nil
	}
	type plain MonitorConfig
	return value.Decode((*plain)(m))
}

// SchemeConfig bundles templates, an optional monitor, and an optional favicon
// for a single scheme (e.g. "claude", "tty").
//
// Backward-compatible: if the YAML value is a plain list it is treated as
// the templates field only (old format).
type SchemeConfig struct {
	Favicon   string        `yaml:"favicon,omitempty"   json:"favicon,omitempty"`
	Monitor   MonitorConfig `yaml:"monitor,omitempty"   json:"monitor,omitempty"`
	Templates []Template    `yaml:"templates"           json:"templates"`
}

func (s *SchemeConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode {
		// Legacy format: bare list of templates
		return value.Decode(&s.Templates)
	}
	type plain SchemeConfig
	return value.Decode((*plain)(s))
}

// TmuxConfig controls whether sessions are backed by tmux for persistence
// across daemon restarts. When enabled, every session runs inside a dedicated
// tmux session so reconnecting after a restart is seamless.
type TmuxConfig struct {
	// Enabled: true always uses tmux, false never does, "auto" uses tmux when
	// the tmux binary is available on PATH (default).
	Enabled string `yaml:"enabled,omitempty" json:"enabled,omitempty"` // true | false | auto
	// Socket is the tmux server socket name (-L flag). Defaults to "ttyrannosaurus"
	// which isolates our sessions from the user's own tmux server.
	Socket string `yaml:"socket,omitempty" json:"socket,omitempty"`
	// ExtraArgs are appended to the `tmux new-session` invocation, allowing
	// per-environment options like `-e TERM=xterm-256color` or `-x 220 -y 50`.
	ExtraArgs []string `yaml:"extra-args,omitempty" json:"extraArgs,omitempty"`
}

// EditorConfig controls how file:// links and bare file paths are opened.
// If omitted, the macOS `open` command is used (respects per-type defaults).
type EditorConfig struct {
	// Command is the editor executable, e.g. "zed", "code", "vim".
	// Leave empty to use the OS default app for each file type.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// LineArg is a format string for opening at a specific line.
	// Use {path} and {line} as placeholders.
	// e.g. Zed: "{path}:{line}"   VS Code: "--goto {path}:{line}"
	// If absent, the line number is appended as :{line} after the path.
	LineArg string `yaml:"line-arg,omitempty" json:"lineArg,omitempty"`
}

type Config struct {
	Port    int                       `yaml:"port"    json:"port"`
	Tmux    TmuxConfig                `yaml:"tmux,omitempty"   json:"tmux,omitempty"`
	Editor  EditorConfig              `yaml:"editor,omitempty" json:"editor,omitempty"`
	Schemes map[string]*SchemeConfig  `yaml:"schemes" json:"schemes"`
}

func DefaultConfig() *Config {
	return &Config{
		Port: 7071,
		Schemes: map[string]*SchemeConfig{
			"claude": {
				Favicon: "https://claude.ai/favicon.ico",
				Monitor: MonitorConfig{Named: "claude-code"},
				Templates: []Template{
					{Pattern: "new", Command: []string{"claude"}, Unique: true, Shell: true},
					{Pattern: "{session}", Command: []string{"claude", "--resume", "{session}"}, Shell: true},
				},
			},
			"tty": {
				Templates: []Template{
					{Pattern: "new", Command: []string{"$SHELL", "-l"}, Unique: true},
					{Pattern: "{session}", Command: []string{"tmux", "new-session", "-A", "-s", "{session}"}},
				},
			},
		},
	}
}

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ttyrannosaurus", "config.yaml")
}

func Load() (*Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := DefaultConfig()
		if saveErr := Save(cfg); saveErr != nil {
			return nil, fmt.Errorf("create default config: %w", saveErr)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Port == 0 {
		cfg.Port = 7071
	}
	if cfg.Schemes == nil {
		cfg.Schemes = map[string]*SchemeConfig{}
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
