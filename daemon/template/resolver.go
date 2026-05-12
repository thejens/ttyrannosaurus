package template

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thejens/ttyrannosaurus/daemon/config"
)

type ResolveResult struct {
	SessionID string
	Scheme    string
	Path      string
	Command   []string
	// InitInput is written to the PTY as keyboard input once the shell is ready.
	// Used for raw commands so they run inside a fully-initialised interactive
	// shell rather than a non-interactive -c subshell.
	InitInput string
	// Dir is the working directory for the PTY process.
	// Empty means use the user's home directory.
	Dir string
}

type Resolver struct {
	schemes map[string]*config.SchemeConfig
}

func New(schemes map[string]*config.SchemeConfig) *Resolver {
	return &Resolver{schemes: schemes}
}

func (r *Resolver) Update(schemes map[string]*config.SchemeConfig) {
	r.schemes = schemes
}

// Resolve matches scheme+path against configured templates and returns a
// ResolveResult with the expanded command and stable session ID.
func (r *Resolver) Resolve(scheme, path string) (ResolveResult, error) {
	sc, ok := r.schemes[scheme]
	if !ok || sc == nil {
		return ResolveResult{}, fmt.Errorf("unknown scheme %q", scheme)
	}
	templates := sc.Templates

	pathSegs := splitPath(path)

	for _, tmpl := range templates {
		patSegs := splitPath(tmpl.Pattern)
		if len(patSegs) != len(pathSegs) {
			continue
		}
		vars, matched := matchSegments(patSegs, pathSegs)
		if !matched {
			continue
		}
		cmd := expandCommand(tmpl.Command, vars)
		if tmpl.Shell {
			cmd = wrapInShell(cmd)
		}
		sessionID := buildSessionID(scheme, path, tmpl.Unique)
		return ResolveResult{
			SessionID: sessionID,
			Scheme:    scheme,
			Path:      path,
			Command:   cmd,
		}, nil
	}
	return ResolveResult{}, fmt.Errorf("no template matches %s/%s", scheme, path)
}

// ResolveRaw creates a session that starts a normal interactive login shell
// and types the command as input once the shell is ready. This guarantees the
// shell is fully initialised (zshrc, plugins, PATH, etc.) before the command
// runs — unlike -c which can race with slow startup files.
func ResolveRaw(command []string) ResolveResult {
	exe := "shell"
	if len(command) > 0 {
		exe = filepath.Base(command[0])
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	id := exe + "-" + shortID()
	return ResolveResult{
		SessionID: id,
		Scheme:    exe,
		Path:      strings.Join(command[1:], " "),
		Command:   []string{shell, "-l"},
		InitInput: shellJoin(command),
	}
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}
	return strings.Split(p, "/")
}

// matchSegments returns captured variables and whether the segments match.
func matchSegments(pattern, path []string) (map[string]string, bool) {
	vars := map[string]string{}
	for i, seg := range pattern {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := seg[1 : len(seg)-1]
			vars[name] = path[i]
		} else if seg != path[i] {
			return nil, false
		}
	}
	return vars, true
}

// expandCommand substitutes {name} placeholders and $SHELL in command args.
func expandCommand(tmpl []string, vars map[string]string) []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	out := make([]string, len(tmpl))
	for i, arg := range tmpl {
		if arg == "$SHELL" {
			out[i] = shell
			continue
		}
		for name, val := range vars {
			arg = strings.ReplaceAll(arg, "{"+name+"}", val)
		}
		out[i] = arg
	}
	return out
}

func buildSessionID(scheme, path string, unique bool) string {
	slug := scheme + "-" + strings.NewReplacer("/", "-", " ", "-").Replace(path)
	if unique {
		return slug + "-" + shortID()
	}
	return slug
}

// wrapInShell runs cmd inside a login shell so that exiting the command
// drops the user into a live shell rather than killing the PTY.
// Produces: $SHELL -l -c "original-cmd args...; exec $SHELL"
func wrapInShell(cmd []string) []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// Shell-quote each arg and join, then append "; exec $SHELL" so the
	// terminal stays alive when the command exits.
	inner := shellJoin(cmd) + "; exec " + shell
	// -i makes zsh source ~/.zshrc so the user's PATH, aliases, and
	// environment managers (nvm, pyenv, etc.) are available immediately.
	return []string{shell, "-l", "-i", "-c", inner}
}

// shellJoin single-quotes each argument (safe for alphanumeric + common chars).
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func shortID() string {
	b := make([]byte, 4)
	rand.Read(b) //nolint:errcheck — crypto/rand.Read never errors
	return hex.EncodeToString(b)
}
