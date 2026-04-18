package audit

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeFlusher replaces the real DB flush. Records each batch it receives so
// tests can assert batch sizes and timing without a real Postgres.
type fakeFlusher struct {
	calls   atomic.Int64
	batches [][]Event
	// Optional per-batch callback.
	afterFlush func(batch []Event)
}

func (f *fakeFlusher) Flush(ctx context.Context, batch []Event) error {
	cp := make([]Event, len(batch))
	copy(cp, batch)
	f.batches = append(f.batches, cp)
	f.calls.Add(1)
	if f.afterFlush != nil {
		f.afterFlush(cp)
	}
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWriter_EnqueueNeverBlocks(t *testing.T) {
	ff := &fakeFlusher{}
	w := newTestWriter(ff, 5)

	// Enqueue 100 events in tight loop; all must return quickly.
	start := time.Now()
	for i := 0; i < 100; i++ {
		w.Enqueue(Event{TS: time.Now(), Route: "/v1/chat/completions", DataClass: "normal"})
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("Enqueue took %v — expected <100ms (non-blocking property)", elapsed)
	}
	dropped := w.Dropped()
	// Buffer size 5, sender enqueued 100, flusher not running → ~95 dropped.
	if dropped < 90 {
		t.Fatalf("expected at least 90 dropped events with buffer=5; got %d", dropped)
	}
}

func TestWriter_FlushOn500Rows(t *testing.T) {
	ff := &fakeFlusher{}
	w := newTestWriter(ff, 2000)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	for i := 0; i < 500; i++ {
		w.Enqueue(Event{TS: time.Now(), RequestID: uuid.New(), DataClass: "normal"})
	}

	// Wait up to 2s for the first flush.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ff.calls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if ff.calls.Load() == 0 {
		t.Fatalf("expected flush to fire within 2s for 500 rows; got 0 calls")
	}
	if len(ff.batches[0]) != 500 {
		t.Fatalf("expected batch size 500; got %d", len(ff.batches[0]))
	}

	cancel()
	<-done
}

func TestWriter_FlushOn1sTicker(t *testing.T) {
	ff := &fakeFlusher{}
	w := newTestWriter(ff, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	for i := 0; i < 10; i++ {
		w.Enqueue(Event{TS: time.Now(), RequestID: uuid.New(), DataClass: "normal"})
	}

	// Ticker fires at 1s; give it 1.5s wall time.
	time.Sleep(1500 * time.Millisecond)
	if ff.calls.Load() == 0 {
		t.Fatalf("expected 1s ticker to flush; got 0 calls")
	}
	if len(ff.batches[0]) != 10 {
		t.Fatalf("expected batch size 10; got %d", len(ff.batches[0]))
	}

	cancel()
	<-done
}

func TestWriter_DrainsOnCtxCancel(t *testing.T) {
	ff := &fakeFlusher{}
	w := newTestWriter(ff, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	for i := 0; i < 3; i++ {
		w.Enqueue(Event{TS: time.Now(), RequestID: uuid.New(), DataClass: "normal"})
	}

	// Give the writer a moment to read from the channel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Run did not return within 500ms after ctx cancel")
	}

	if ff.calls.Load() == 0 {
		t.Fatalf("expected final drain flush; got 0 calls")
	}
	// Cumulative batch across all flushes should be ≥3.
	total := 0
	for _, b := range ff.batches {
		total += len(b)
	}
	if total != 3 {
		t.Fatalf("expected 3 events drained; got %d across %d batches", total, len(ff.batches))
	}
}

func TestWriter_NormalClassGetsContentRow(t *testing.T) {
	var contentInserts atomic.Int32
	ff := &fakeFlusher{afterFlush: func(batch []Event) {
		for _, e := range batch {
			if e.DataClass == "normal" && (len(e.Prompt) > 0 || len(e.Response) > 0) {
				contentInserts.Add(1)
			}
		}
	}}
	w := newTestWriter(ff, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	w.Enqueue(Event{
		TS:        time.Now(),
		RequestID: uuid.New(),
		DataClass: "normal",
		Prompt:    []byte(`{"hi":1}`),
		Response:  []byte(`{"ok":1}`),
	})

	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done

	if contentInserts.Load() != 1 {
		t.Fatalf("expected 1 content insert for normal; got %d", contentInserts.Load())
	}
}

func TestWriter_SensitiveClassSkipsContent(t *testing.T) {
	var contentInserts atomic.Int32
	ff := &fakeFlusher{afterFlush: func(batch []Event) {
		for _, e := range batch {
			if e.DataClass == "normal" && (len(e.Prompt) > 0 || len(e.Response) > 0) {
				contentInserts.Add(1)
			}
		}
	}}
	w := newTestWriter(ff, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	w.Enqueue(Event{
		TS:        time.Now(),
		RequestID: uuid.New(),
		DataClass: "sensitive",
		Prompt:    nil,
		Response:  nil,
	})

	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done

	if contentInserts.Load() != 0 {
		t.Fatalf("expected 0 content inserts for sensitive; got %d", contentInserts.Load())
	}
}
