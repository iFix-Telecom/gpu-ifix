//go:build load

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeQueriesAlwaysMiss models the attacker-flooding-unknown-keys scenario.
type fakeQueriesAlwaysMiss struct{}

func (f *fakeQueriesAlwaysMiss) GetActiveKeyByLookupHash(ctx context.Context, lookup []byte) (gen.GetActiveKeyByLookupHashRow, error) {
	return gen.GetActiveKeyByLookupHashRow{}, pgx.ErrNoRows
}

func (f *fakeQueriesAlwaysMiss) TouchKeyLastUsed(ctx context.Context, id uuid.UUID) error {
	return nil
}

// TestVerifyUnderLoad measures Verify throughput under adversarial cache-miss
// with 10k random well-formed-but-unknown keys. Target: ≥500 req/s on a
// single-core dev laptop. Codex review [HIGH] 02-03.
func TestVerifyUnderLoad(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tb := NewTouchBuffer(func(ctx context.Context, id uuid.UUID) error { return nil }, time.Hour, log, nil, nil)
	v := NewVerifierWithQueries(&fakeQueriesAlwaysMiss{}, rdb, log, tb)

	const N = 10_000
	keys := make([]string, N)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for i := range keys {
		b := make([]byte, 20)
		_, _ = rand.Read(b)
		keys[i] = KeyPrefix + strings.ToLower(enc.EncodeToString(b))
	}

	var wg sync.WaitGroup
	const workers = 4
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(off int) {
			defer wg.Done()
			ctx := context.Background()
			for i := off; i < N; i += workers {
				_, _ = v.Verify(ctx, keys[i])
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	throughput := float64(N) / elapsed.Seconds()
	t.Logf("TestVerifyUnderLoad: %d requests in %s = %.0f req/s (workers=%d)", N, elapsed, throughput, workers)
	if throughput < 500 {
		t.Fatalf("throughput %.0f req/s < 500 req/s (Codex review [HIGH] 02-03 acceptance)", throughput)
	}
}
