package session

import "sync"

// ringBuffer is a fixed-size circular byte buffer used to keep the last N bytes
// of PTY output for scrollback replay when a new WebSocket client connects.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	pos int
	n   int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size)}
}

func (r *ringBuffer) write(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range data {
		r.buf[r.pos%len(r.buf)] = b
		r.pos++
		if r.n < len(r.buf) {
			r.n++
		}
	}
}

func (r *ringBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return nil
	}
	out := make([]byte, r.n)
	start := (r.pos - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}
