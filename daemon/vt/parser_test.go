package vt_test

import (
	"strings"
	"testing"

	"github.com/thejens/ttyrannosaurus/daemon/vt"
)

// collector implements vt.Handler and records all emitted events.
type collector struct{ events []vt.Event }

func (c *collector) HandleVTEvent(ev vt.Event) { c.events = append(c.events, ev) }

func (c *collector) osc() []vt.Event {
	var out []vt.Event
	for _, e := range c.events {
		if e.Kind == vt.OSCEvent {
			out = append(out, e)
		}
	}
	return out
}

func (c *collector) lines() []string {
	var out []string
	for _, e := range c.events {
		if e.Kind == vt.LineEvent {
			out = append(out, e.Line)
		}
	}
	return out
}

func write(p *vt.Parser, s string) { p.Write([]byte(s)) }

// TestOSCBEL verifies BEL-terminated OSC sequences are emitted correctly.
func TestOSCBEL(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;hello world\x07")
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	if oscs[0].Code != "2" || oscs[0].Payload != "hello world" {
		t.Errorf("got Code=%q Payload=%q", oscs[0].Code, oscs[0].Payload)
	}
}

// TestOSCST verifies ST-terminated (ESC \) OSC sequences are emitted correctly.
func TestOSCST(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;my title\x1b\\")
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	if oscs[0].Code != "2" || oscs[0].Payload != "my title" {
		t.Errorf("got Code=%q Payload=%q", oscs[0].Code, oscs[0].Payload)
	}
}

// TestCrossChunkOSC is the core regression: sequence split across two Write calls.
func TestCrossChunkOSC(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;hel")     // chunk 1: sequence not yet terminated
	write(p, "lo world\x07")   // chunk 2: rest of payload + terminator
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event after two chunks, got %d", len(oscs))
	}
	if oscs[0].Code != "2" || oscs[0].Payload != "hello world" {
		t.Errorf("got Code=%q Payload=%q", oscs[0].Code, oscs[0].Payload)
	}
}

// TestFalseST checks that \x1b followed by a non-\ byte does not terminate the OSC.
func TestFalseST(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	// \x1b X is not ST — X gets pushed into oscBuf
	write(p, "\x1b]2;before\x1b X after\x07")
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	want := "before\x1b X after"
	if oscs[0].Payload != want {
		t.Errorf("got Payload=%q, want %q", oscs[0].Payload, want)
	}
}

// TestOSC7773JSON verifies JSON payloads in OSC 7773 are captured verbatim.
func TestOSC7773JSON(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	payload := `{"name":"claude","status":"busy"}`
	write(p, "\x1b]7773;"+payload+"\x07")
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	if oscs[0].Code != "7773" || oscs[0].Payload != payload {
		t.Errorf("got Code=%q Payload=%q", oscs[0].Code, oscs[0].Payload)
	}
}

// TestEmptyOSCPayload verifies an OSC with no content after the semicolon.
func TestEmptyOSCPayload(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;\x07")
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	if oscs[0].Code != "2" || oscs[0].Payload != "" {
		t.Errorf("got Code=%q Payload=%q", oscs[0].Code, oscs[0].Payload)
	}
}

// TestMultipleOSCsInOneChunk verifies emission order.
func TestMultipleOSCsInOneChunk(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;first\x07\x1b]7;file:///tmp\x07\x1b]7773;{}\x07")
	oscs := c.osc()
	if len(oscs) != 3 {
		t.Fatalf("want 3 OSC events, got %d", len(oscs))
	}
	codes := []string{oscs[0].Code, oscs[1].Code, oscs[2].Code}
	want := []string{"2", "7", "7773"}
	for i := range want {
		if codes[i] != want[i] {
			t.Errorf("event[%d]: got Code=%q, want %q", i, codes[i], want[i])
		}
	}
}

// TestLineEvents verifies that newlines flush the lineBuf as LineEvents.
func TestLineEvents(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "hello\nworld\n")
	got := c.lines()
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Errorf("got lines %v", got)
	}
}

// TestMultipleLinesInOneChunk verifies multiple newlines in a single Write.
func TestMultipleLinesInOneChunk(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "a\nb\nc\n")
	got := c.lines()
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %v", len(got), got)
	}
}

// TestControlBytesStripped verifies that C0 control bytes do not appear in
// line content. \r flushes the current line; \x08 (backspace) is stripped.
func TestControlBytesStripped(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	// \r flushes "he" as one line; "llo" (with \x08 stripped) flushed by \n.
	write(p, "he\rllo\x08\n")
	lines := c.lines()
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(lines), lines)
	}
	for _, l := range lines {
		if strings.ContainsAny(l, "\r\x08") {
			t.Errorf("control bytes leaked into line: %q", l)
		}
	}
}

// TestCrossChunkST verifies ST split across two chunks (\x1b in one, \ in next).
func TestCrossChunkST(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "\x1b]2;title\x1b") // ST opener at end of chunk
	write(p, "\\")               // ST closer in next chunk
	oscs := c.osc()
	if len(oscs) != 1 {
		t.Fatalf("want 1 OSC event, got %d", len(oscs))
	}
	if oscs[0].Payload != "title" {
		t.Errorf("got Payload=%q", oscs[0].Payload)
	}
}

// TestCSIStripped verifies that CSI sequences (ANSI SGR etc.) do not appear in
// LineEvents — root cause of "39m"/"1XC" garbage in Claude Code detail text.
func TestCSIStripped(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	// \x1b[39m = reset fg colour, \x1b[1X = erase char, \x1b[C = cursor right
	write(p, "\x1b[39mHello\x1b[1X\x1b[C world\n")
	lines := c.lines()
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %v", len(lines), lines)
	}
	if lines[0] != "Hello world" {
		t.Errorf("got %q, want %q", lines[0], "Hello world")
	}
}

// TestCRLF verifies that \r\n is treated as a single line ending (content is
// not lost). This was broken when \r unconditionally reset the lineBuf.
func TestCRLF(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "hello\r\nworld\r\n")
	lines := c.lines()
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world" {
		t.Errorf("got %v", lines)
	}
}

// TestOverwriteThenNewline verifies that \r-separated frames are each emitted
// as a LineEvent so the monitor can see in-progress spinner/status updates
// (e.g. Claude Code's thinking indicator). The final \n on an empty buffer is
// a no-op; it does not produce a duplicate empty line.
func TestOverwriteThenNewline(t *testing.T) {
	c := &collector{}
	p := vt.New(c)
	write(p, "frame1\rframe2\rframe3\n")
	lines := c.lines()
	want := []string{"frame1", "frame2", "frame3"}
	if len(lines) != len(want) {
		t.Fatalf("want %d lines, got %d: %v", len(want), len(lines), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line[%d]: got %q, want %q", i, lines[i], w)
		}
	}
}
