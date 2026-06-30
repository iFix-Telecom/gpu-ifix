package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeLifecycleQueries is an in-memory lifecycleQueries double — no pgxpool.
type fakeLifecycleQueries struct {
	row    gen.AiGatewayPrimaryLifecycle
	err    error
	called bool
}

func (f *fakeLifecycleQueries) GetOpenPrimaryLifecycle(_ context.Context) (gen.AiGatewayPrimaryLifecycle, error) {
	f.called = true
	if f.err != nil {
		return gen.AiGatewayPrimaryLifecycle{}, f.err
	}
	return f.row, nil
}

// lifecycleResponse is the decode target mirroring the Go handler contract.
type lifecycleResponse struct {
	FSMState       string `json:"fsm_state"`
	Leader         bool   `json:"leader"`
	EmergencyState string `json:"emergency_state"`
	OpenLifecycle  *struct {
		ID                int64           `json:"id"`
		TriggerReason     string          `json:"trigger_reason"`
		StartedAt         string          `json:"started_at"`
		FirstHealthPassAt *string         `json:"first_health_pass_at"`
		DrainStartedAt    *string         `json:"drain_started_at"`
		EndedAt           *string         `json:"ended_at"`
		AcceptedDPH       *float64        `json:"accepted_dph"`
		TotalCostBRL      *float64        `json:"total_cost_brl"`
		ShutdownReason    *string         `json:"shutdown_reason"`
		Events            json.RawMessage `json:"events"`
	} `json:"open_lifecycle"`
}

func doLifecycleRequest(t *testing.T, h *PrimaryLifecycleHandler) (*httptest.ResponseRecorder, lifecycleResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/primary/lifecycle", nil)
	h.ServeHTTP(rec, req)
	var body lifecycleResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, body
}

// TestLifecycleHandler_OpenLifecycle: an open lifecycle row carries its
// event-trail fields. rec nil → fsm_state "unknown" without panic.
func TestLifecycleHandler_OpenLifecycle(t *testing.T) {
	started := time.Now().Add(-30 * time.Minute)
	fake := &fakeLifecycleQueries{
		row: gen.AiGatewayPrimaryLifecycle{
			ID:             77,
			StartedAt:      started,
			TriggerReason:  "schedule_up",
			AcceptedDph:    opNumeric(0.40),
			ShutdownReason: pgtype.Text{String: "", Valid: false},
			Events:         []byte(`[{"t":"started"}]`),
		},
	}
	h := newPrimaryLifecycleHandlerWithQueries(fake, nil, nil, discardLog())

	rec, body := doLifecycleRequest(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.FSMState != "unknown" {
		t.Errorf("fsm_state = %q, want unknown (rec nil)", body.FSMState)
	}
	if body.Leader {
		t.Errorf("leader = true, want false (rec nil)")
	}
	if body.EmergencyState != "unknown" {
		t.Errorf("emergency_state = %q, want unknown (emergFSM nil)", body.EmergencyState)
	}
	if body.OpenLifecycle == nil {
		t.Fatal("open_lifecycle = null, want the open row")
	}
	if body.OpenLifecycle.ID != 77 {
		t.Errorf("open_lifecycle.id = %d, want 77", body.OpenLifecycle.ID)
	}
	if body.OpenLifecycle.TriggerReason != "schedule_up" {
		t.Errorf("trigger_reason = %q, want schedule_up", body.OpenLifecycle.TriggerReason)
	}
	if body.OpenLifecycle.AcceptedDPH == nil || *body.OpenLifecycle.AcceptedDPH != 0.40 {
		t.Errorf("accepted_dph = %v, want 0.40", body.OpenLifecycle.AcceptedDPH)
	}
	// Open lifecycle: ended_at null, shutdown_reason null.
	if body.OpenLifecycle.EndedAt != nil {
		t.Errorf("ended_at = %v, want null (open)", *body.OpenLifecycle.EndedAt)
	}
	if body.OpenLifecycle.ShutdownReason != nil {
		t.Errorf("shutdown_reason = %v, want null (open)", *body.OpenLifecycle.ShutdownReason)
	}
	if len(body.OpenLifecycle.Events) == 0 {
		t.Errorf("events empty, want the jsonb trail")
	}
}

// TestLifecycleHandler_NoOpenLifecycle: ErrNoRows → open_lifecycle null,
// fsm_state still reported.
func TestLifecycleHandler_NoOpenLifecycle(t *testing.T) {
	fake := &fakeLifecycleQueries{err: pgx.ErrNoRows}
	h := newPrimaryLifecycleHandlerWithQueries(fake, nil, nil, discardLog())

	rec, body := doLifecycleRequest(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.OpenLifecycle != nil {
		t.Errorf("open_lifecycle = %+v, want null", body.OpenLifecycle)
	}
	if body.FSMState != "unknown" {
		t.Errorf("fsm_state = %q, want unknown", body.FSMState)
	}
}

// TestLifecycleHandler_QueryError_500: a non-ErrNoRows failure returns a 500
// envelope, never a panic.
func TestLifecycleHandler_QueryError_500(t *testing.T) {
	fake := &fakeLifecycleQueries{err: context.DeadlineExceeded}
	h := newPrimaryLifecycleHandlerWithQueries(fake, nil, nil, discardLog())

	rec, _ := doLifecycleRequest(t, h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
