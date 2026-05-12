package template

import (
	"os"
	"strings"
	"testing"

	"github.com/thejens/ttyrannosaurus/daemon/config"
)

var testSchemes = map[string]*config.SchemeConfig{
	"claude": {Templates: []config.Template{
		{Pattern: "new", Command: []string{"claude"}, Unique: true},
		{Pattern: "{session}", Command: []string{"claude", "--resume", "{session}"}},
	}},
	"tty": {Templates: []config.Template{
		{Pattern: "new", Command: []string{"$SHELL", "-l"}, Unique: true},
		{Pattern: "{session}", Command: []string{"tmux", "new-session", "-A", "-s", "{session}"}},
	}},
}

func TestResolve_LiteralMatch(t *testing.T) {
	r := New(testSchemes)
	res, err := r.Resolve("claude", "new")
	if err != nil {
		t.Fatal(err)
	}
	if res.Command[0] != "claude" {
		t.Errorf("expected command claude, got %v", res.Command)
	}
	// unique flag → session ID contains a suffix beyond "claude-new-"
	if !strings.HasPrefix(res.SessionID, "claude-new-") {
		t.Errorf("unique session ID should start with claude-new-, got %q", res.SessionID)
	}
}

func TestResolve_VarCapture(t *testing.T) {
	r := New(testSchemes)
	res, err := r.Resolve("claude", "tf")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--resume", "tf"}
	for i, w := range want {
		if res.Command[i] != w {
			t.Errorf("command[%d]: want %q got %q", i, w, res.Command[i])
		}
	}
	if res.SessionID != "claude-tf" {
		t.Errorf("expected session ID claude-tf, got %q", res.SessionID)
	}
}

func TestResolve_ShellExpansion(t *testing.T) {
	os.Setenv("SHELL", "/bin/zsh")
	r := New(testSchemes)
	res, err := r.Resolve("tty", "new")
	if err != nil {
		t.Fatal(err)
	}
	if res.Command[0] != "/bin/zsh" {
		t.Errorf("expected /bin/zsh, got %q", res.Command[0])
	}
}

func TestResolve_UnknownScheme(t *testing.T) {
	r := New(testSchemes)
	_, err := r.Resolve("k8s", "prod")
	if err == nil {
		t.Error("expected error for unknown scheme")
	}
}

func TestResolve_NoMatch(t *testing.T) {
	r := New(testSchemes)
	// Two-segment path against single-segment templates → no match
	_, err := r.Resolve("claude", "a/b")
	if err == nil {
		t.Error("expected error for unmatched path")
	}
}

func TestResolve_ReservedRaw(t *testing.T) {
	r := New(testSchemes)
	_, err := r.Resolve("_raw", "anything")
	if err == nil {
		t.Error("expected error for _raw scheme")
	}
}

func TestResolveRaw(t *testing.T) {
	res := ResolveRaw([]string{"echo", "hello"})
	if res.Scheme != "echo" {
		t.Errorf("expected scheme 'echo', got %q", res.Scheme)
	}
	if !strings.HasPrefix(res.SessionID, "echo-") {
		t.Errorf("expected echo- prefix, got %q", res.SessionID)
	}
	// ResolveRaw starts a plain login shell; the command goes into InitInput.
	if len(res.Command) != 2 || res.Command[1] != "-l" {
		t.Errorf("expected [shell -l], got: %v", res.Command)
	}
	if !strings.Contains(res.InitInput, "echo") {
		t.Errorf("original command not in InitInput: %q", res.InitInput)
	}
}

func TestResolve_UniqueSessionsDiffer(t *testing.T) {
	r := New(testSchemes)
	a, _ := r.Resolve("claude", "new")
	b, _ := r.Resolve("claude", "new")
	if a.SessionID == b.SessionID {
		t.Error("unique=true sessions should have distinct IDs")
	}
}
