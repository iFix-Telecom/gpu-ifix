package podconfig

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// snapshot is the immutable view of pod_config served from the hot path.
// Built fresh on every Refresh, atomically swapped via atomic.Pointer so
// reads are lock-free. Mirror of upstreams.snapshot (no override layer —
// pod_config has none).
type snapshot struct {
	cfg    PodConfig
	rule   ScheduleRule
	bounds PodConfigBounds
}

// loaderQueries isolates the sqlc surface so tests can stub it without a
// real Postgres pool. Mirrors upstreams.loaderQueries.
type loaderQueries interface {
	GetPodConfig(ctx context.Context) (gen.AiGatewayPodConfig, error)
}

// Loader holds the in-memory authoritative snapshot of ai_gateway.pod_config.
// Readers call Cfg/Rule/Bounds on the hot path (atomic.Pointer — lock-free).
// Refresh is called at boot + on each LISTEN/NOTIFY from pod_config_changed.
type Loader struct {
	pool *pgxpool.Pool
	q    loaderQueries
	snap atomic.Pointer[snapshot]
	log  *slog.Logger
	tz   *time.Location
}

// NewLoader is stubbed in the RED phase.
func NewLoader(ctx context.Context, pool *pgxpool.Pool, tz string, log *slog.Logger) (*Loader, error) {
	return nil, errors.New("podconfig: NewLoader not implemented")
}

// Refresh is stubbed in the RED phase.
func (l *Loader) Refresh(ctx context.Context) error {
	return errors.New("podconfig: Refresh not implemented")
}

// Load is stubbed in the RED phase.
func (l *Loader) Load() *snapshot { return nil }

// Cfg is stubbed in the RED phase.
func (l *Loader) Cfg() PodConfig { return PodConfig{} }

// Rule is stubbed in the RED phase.
func (l *Loader) Rule() ScheduleRule { return ScheduleRule{} }

// Bounds is stubbed in the RED phase.
func (l *Loader) Bounds() PodConfigBounds { return PodConfigBounds{} }
