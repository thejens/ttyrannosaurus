// Package vt provides a stateful VT/ANSI terminal sequence parser.
//
// The parser maintains state across Write calls, so OSC sequences that span
// chunk boundaries (e.g. a 4 KB PTY read) are handled correctly.
package vt

import "strings"

// EventKind identifies the type of a parsed terminal event.
type EventKind int

const (
	// OSCEvent is emitted when a complete OSC sequence is parsed.
	OSCEvent EventKind = iota
	// LineEvent is emitted when a newline is encountered.
	// The Line field contains the accumulated text with control sequences removed.
	LineEvent
)

// Event is emitted by the Parser for each parseable unit it extracts.
type Event struct {
	Kind EventKind

	// OSCEvent fields
	Code    string // numeric code before the first ";" (e.g. "2", "7", "7773")
	Payload string // everything after the first ";"

	// LineEvent fields
	Line string // ANSI-stripped text content of the line
}

// Handler receives events from a Parser.
type Handler interface {
	HandleVTEvent(ev Event)
}

type parserState int

const (
	stateNormal parserState = iota
	stateESC                // saw \x1b, waiting for next byte
	stateOSC                // inside \x1b] ... BEL/ST
	stateOSCST              // inside OSC, saw \x1b — may be ST (\x1b\)
	stateCSI                // inside \x1b[ ... final byte — strips SGR/cursor/etc
)

// Parser is a stateful VT sequence parser. It is not goroutine-safe —
// call Write from a single goroutine (the PTY readLoop).
type Parser struct {
	handler Handler
	state   parserState
	oscBuf  strings.Builder
	lineBuf strings.Builder
}

// New creates a Parser that delivers events to h.
func New(h Handler) *Parser {
	return &Parser{handler: h}
}

// Write processes the next chunk of PTY output.
// Events are delivered synchronously to the Handler before Write returns.
func (p *Parser) Write(data []byte) {
	for _, b := range data {
		switch p.state {
		case stateNormal:
			switch b {
			case '\x1b':
				p.state = stateESC
			case '\n', '\r':
				// Both \n and \r flush the current line. This means:
				//   - \r\n (CRLF): \r emits the line, \n finds empty buf → no-op.
				//   - \r alone (spinner overwrite): each frame is emitted, letting
				//     the monitor see in-progress status lines from TUIs like Claude.
				//   - \n alone: emits normally.
				if p.lineBuf.Len() > 0 {
					p.handler.HandleVTEvent(Event{Kind: LineEvent, Line: p.lineBuf.String()})
					p.lineBuf.Reset()
				}
			default:
				// Skip C0/C1 control bytes except printable ASCII and UTF-8 continuation.
				// Bytes >= 0x20 are either printable ASCII, high UTF-8, or continuation bytes.
				if b >= 0x20 {
					p.lineBuf.WriteByte(b)
				}
			}

		case stateESC:
			switch b {
			case ']':
				p.oscBuf.Reset()
				p.state = stateOSC
			case '[':
				// CSI sequence — drain it in stateCSI to prevent parameter bytes
				// (e.g. "39m", "1X") leaking into lineBuf as plain text.
				p.state = stateCSI
			default:
				// Two-byte ESC sequence (SS2, SS3, RI, etc.) — consume and skip.
				p.state = stateNormal
			}

		case stateCSI:
			// Final byte range 0x40–0x7E ends the CSI sequence.
			// Parameter bytes (0x30–0x3F) and intermediate bytes (0x20–0x2F)
			// are consumed silently — none should appear in lineBuf.
			if b >= 0x40 && b <= 0x7E {
				p.state = stateNormal
			}

		case stateOSC:
			switch b {
			case '\x07': // BEL terminator
				p.emitOSC()
				p.state = stateNormal
			case '\x1b': // possible ST (\x1b\) — wait for next byte
				p.state = stateOSCST
			default:
				p.oscBuf.WriteByte(b)
			}

		case stateOSCST:
			switch b {
			case '\\': // confirmed ST
				p.emitOSC()
				p.state = stateNormal
			default:
				// False alarm — \x1b followed by something other than \.
				// Push both bytes back into oscBuf and stay in OSC.
				p.oscBuf.WriteByte('\x1b')
				p.oscBuf.WriteByte(b)
				p.state = stateOSC
			}
		}
	}
}

func (p *Parser) emitOSC() {
	raw := p.oscBuf.String()
	p.oscBuf.Reset()
	code, payload, _ := strings.Cut(raw, ";")
	p.handler.HandleVTEvent(Event{Kind: OSCEvent, Code: code, Payload: payload})
}
