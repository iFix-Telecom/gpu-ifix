package emerg

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
func TestEmergSweep_LogsSummaryCounters(t *testing.T) {
	fv := &sweepFakeVast{list: []vast.Instance{
		{ID: 10, Label: "ifix-emerg-lifecycle-1", StartDate: sweepOld()},
		{ID: 11, Label: "ifix-emerg-lifecycle-2", StartDate: sweepOld()},
		{ID: 12, Label: "ifix-primary-lifecycle-2", StartDate: sweepOld()},
	}}
	q := &stubEmergSweepQuerier{rows: []gen.ListLiveEmergencyLifecyclesRow{
		{ID: 1, VastInstanceID: pgtype.Int8{Int64: 10, Valid: true}},
	}}
	r := newEmergSweepReconciler(fv, q)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r.sweepOrphanInstances(context.Background(), log)

	out := buf.String()
	require.Equal(t, 1, strings.Count(out, "emerg orphan sweep done"), out)
	for _, kv := range []string{"listed=3", "matched_label=2", "orphans=1", "destroyed=1", "failed=0"} {
		require.Contains(t, out, kv)
	}
}
