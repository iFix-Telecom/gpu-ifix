package primary

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// Quick 260930-uru: each sweep run emits exactly one INFO summary line.
func TestPrimarySweep_LogsSummaryCounters(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 53345260, Label: "ifix-primary-lifecycle-494", StartDate: oldStart()},
		{ID: 600, Label: "ifix-primary-lifecycle-500", StartDate: oldStart()},
		{ID: 700, Label: "stt-tts-rerank-unified", StartDate: oldStart()},
		{ID: 800, Label: "ifix-emerg-lifecycle-9", StartDate: oldStart()},
	}}
	q := &stubSweepQuerier{row: gen.AiGatewayPrimaryLifecycle{
		ID: 500, VastInstanceID: pgtype.Int8{Int64: 600, Valid: true},
	}}
	r := newSweepReconciler(fv, q)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r.sweepOrphanInstances(context.Background(), log)

	out := buf.String()
	require.Equal(t, 1, strings.Count(out, "primary orphan sweep done"), out)
	for _, kv := range []string{"listed=4", "matched_label=2", "orphans=1", "destroyed=1", "failed=0"} {
		require.Contains(t, out, kv)
	}
}
