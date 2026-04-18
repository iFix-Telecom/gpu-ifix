package audit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"
)

// errReader returns a fixed error from Read.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

// readCloser wraps an io.Reader into io.ReadCloser with a Close that can
// record how many times it was called.
type readCloser struct {
	io.Reader
	closes atomic.Int32
}

func (r *readCloser) Close() error { r.closes.Add(1); return nil }

func TestTee_PassesThroughFullBodyToReader(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 200*1024) // 200 KB
	src := &readCloser{Reader: bytes.NewReader(payload)}

	tee := NewTeeBody(src, nil)
	out, err := io.ReadAll(tee)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != len(payload) {
		t.Fatalf("read %d bytes; expected %d (must pass through full body)", len(out), len(payload))
	}
	cap, trunc := tee.Captured()
	if len(cap) != contentCapBytes {
		t.Fatalf("captured %d bytes; expected %d (cap)", len(cap), contentCapBytes)
	}
	if !trunc {
		t.Fatalf("expected truncated=true for 200KB body")
	}
}

func TestTee_CapturedMatchesForSmallBody(t *testing.T) {
	payload := []byte(strings.Repeat("x", 4*1024))
	src := &readCloser{Reader: bytes.NewReader(payload)}

	tee := NewTeeBody(src, nil)
	_, _ = io.ReadAll(tee)
	cap, trunc := tee.Captured()
	if trunc {
		t.Fatalf("expected truncated=false for 4KB body")
	}
	if !bytes.Equal(cap, payload) {
		t.Fatalf("captured bytes differ from source (len cap=%d src=%d)", len(cap), len(payload))
	}
}

func TestTee_OnCloseFiresOnce(t *testing.T) {
	var calls atomic.Int32
	src := &readCloser{Reader: bytes.NewReader([]byte("hello"))}

	tee := NewTeeBody(src, func() { calls.Add(1) })
	_ = tee.Close()
	_ = tee.Close()
	_ = tee.Close()
	if calls.Load() != 1 {
		t.Fatalf("onClose fired %d times; expected 1", calls.Load())
	}
}

func TestTee_OnCloseFiresEvenOnReadError(t *testing.T) {
	var calls atomic.Int32
	src := &readCloser{Reader: errReader{err: errors.New("boom")}}

	tee := NewTeeBody(src, func() { calls.Add(1) })
	buf := make([]byte, 16)
	_, _ = tee.Read(buf)
	_ = tee.Close()
	if calls.Load() != 1 {
		t.Fatalf("onClose fired %d times after read-error; expected 1", calls.Load())
	}
}

func TestTee_CapturedBeforeClose(t *testing.T) {
	payload := []byte("partial")
	src := &readCloser{Reader: bytes.NewReader(payload)}

	tee := NewTeeBody(src, nil)
	_, _ = io.ReadAll(tee)
	// Do NOT Close.
	cap, _ := tee.Captured()
	if !bytes.Equal(cap, payload) {
		t.Fatalf("captured %q; expected %q before Close", cap, payload)
	}
}

func TestTee_NoGoroutineLeakOnClientDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// Simulate upstream body: reader that blocks briefly, then returns data.
	payload := bytes.Repeat([]byte("B"), 4*1024)
	src := &readCloser{Reader: bytes.NewReader(payload)}

	tee := NewTeeBody(src, nil)
	buf := make([]byte, 256)
	// Partial read: simulate client abort mid-stream.
	_, _ = tee.Read(buf)
	// Client disconnect → proxy writer closes the body.
	if err := tee.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// No goroutines were spawned; goleak at defer should pass.
}
