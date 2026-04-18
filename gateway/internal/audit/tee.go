package audit

import (
	"bytes"
	"io"
	"sync"
)

// TeeBody wraps an upstream response body so the proxy can pass chunks
// through to the client AND capture up to contentCapBytes (128 KB) for
// the audit row. Implements io.ReadCloser. Safe for single-reader use
// (io.ReadCloser contract; concurrent Reads not supported by net/http).
//
// Design — no goroutines spawned: all tee work happens synchronously in
// Read and Close. See Failure-mode table in 02-05-PLAN.md for the 5
// termination modes this supports (normal close, client abort, upstream
// 5xx mid-stream, buffer cap exceeded, flusher full). Codex review
// [HIGH/MEDIUM] 02-05 goleak regression guard uses this property.
type TeeBody struct {
	upstream io.ReadCloser
	mu       sync.Mutex
	buf      *bytes.Buffer
	capLeft  int
	trunc    bool
	closed   bool
	onClose  func() // fires exactly once on first Close
}

// NewTeeBody constructs the tee. onClose may be nil.
func NewTeeBody(upstream io.ReadCloser, onClose func()) *TeeBody {
	return &TeeBody{
		upstream: upstream,
		buf:      &bytes.Buffer{},
		capLeft:  contentCapBytes,
		onClose:  onClose,
	}
}

// Read copies from the upstream into p and tees into the internal buffer
// up to contentCapBytes. Beyond the cap, Truncated is set and no more
// bytes are buffered — the underlying Read still completes so the client
// receives the full stream.
func (t *TeeBody) Read(p []byte) (int, error) {
	n, err := t.upstream.Read(p)
	if n > 0 {
		t.mu.Lock()
		if t.capLeft > 0 {
			take := n
			if take > t.capLeft {
				take = t.capLeft
				t.trunc = true
			}
			t.buf.Write(p[:take])
			t.capLeft -= take
		} else {
			t.trunc = true
		}
		t.mu.Unlock()
	}
	return n, err
}

// Close fires onClose exactly once and closes the upstream body. Second
// and subsequent calls return nil without re-firing onClose.
func (t *TeeBody) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cb := t.onClose
	t.mu.Unlock()
	if cb != nil {
		cb()
	}
	return t.upstream.Close()
}

// Captured returns a copy of the accumulated bytes plus the truncated
// flag. Safe to call at any time; snapshot is consistent under the mutex.
func (t *TeeBody) Captured() ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]byte, t.buf.Len())
	copy(out, t.buf.Bytes())
	return out, t.trunc
}
