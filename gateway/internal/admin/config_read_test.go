package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeConfigReadQueries is an in-memory podConfigReadQueries double.
type fakeConfigReadQueries struct {
	row    gen.AiGatewayPodConfig
	err    error
	called bool
}

func (f *fakeConfigReadQueries) GetPodConfig(_ context.Context) (gen.AiGatewayPodConfig, error) {
	f.called = true
	if f.err != nil {
		return gen.AiGatewayPodConfig{}, f.err
	}
	return f.row, nil
}

// configReadResponse mirrors the GET /admin/primary/config contract.
type configReadResponse struct {
	Config struct {
		VastMachineBlocklist []int64  `json:"vast_machine_blocklist"`
		VastMachineAllowlist []int64  `json:"vast_machine_allowlist"`
		CapPrimary           float64  `json:"cap_primary"`
		CapFallback          float64  `json:"cap_fallback"`
		HostID               int64    `json:"host_id"`
		RejectPrivateIP      bool     `json:"reject_private_ip"`
		ColdstartBudgetS     int      `json:"coldstart_budget_s"`
		PortBindBudgetS      int      `json:"port_bind_budget_s"`
		FailureCooldownS     int      `json:"failure_cooldown_s"`
		MonthlyBudgetBRL     float64  `json:"monthly_budget_brl"`
		ScheduleUpHour       int      `json:"schedule_up_hour"`
		ScheduleDownHour     int      `json:"schedule_down_hour"`
		ScheduleDays         []string `json:"schedule_days"`
		GraceRampDownS       int      `json:"grace_ramp_down_s"`
		ProvisionLeadS       int      `json:"provision_lead_s"`
		ScheduleDisabled     bool     `json:"schedule_disabled"`
	} `json:"config"`
	Bounds struct {
		CapPrimaryMin     float64 `json:"cap_primary_min"`
		CapPrimaryMax     float64 `json:"cap_primary_max"`
		ScheduleUpHourMin int     `json:"schedule_up_hour_min"`
		ScheduleUpHourMax int     `json:"schedule_up_hour_max"`
	} `json:"bounds"`
}

// errEnvelope is the OpenAI error decode target.
type errEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestConfigReadHandler_CurrentRow(t *testing.T) {
	row := gen.AiGatewayPodConfig{
		ID:                   true,
		VastMachineBlocklist: []int64{111, 222},
		VastMachineAllowlist: []int64{333},
		CapPrimary:           opNumeric(0.60),
		CapFallback:          opNumeric(1.20),
		HostID:               99,
		RejectPrivateIp:      true,
		ColdstartBudgetS:     3600,
		PortBindBudgetS:      300,
		FailureCooldownS:     120,
		MonthlyBudgetBrl:     opNumeric(800.0),
		ScheduleUpHour:       9,
		ScheduleDownHour:     17,
		ScheduleDays:         []string{"mon", "tue", "wed", "thu", "fri"},
		GraceRampDownS:       300,
		ProvisionLeadS:       600,
		ScheduleDisabled:     false,
		CapPrimaryMin:        opNumeric(0.10),
		CapPrimaryMax:        opNumeric(2.20),
		ScheduleUpHourMin:    0,
		ScheduleUpHourMax:    23,
	}
	fake := &fakeConfigReadQueries{row: row}
	h := newPrimaryConfigReadHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/primary/config", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var body configReadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if body.Config.CapPrimary != 0.60 {
		t.Errorf("cap_primary = %v, want 0.60", body.Config.CapPrimary)
	}
	if len(body.Config.VastMachineBlocklist) != 2 || body.Config.VastMachineBlocklist[0] != 111 {
		t.Errorf("blocklist = %v, want [111 222]", body.Config.VastMachineBlocklist)
	}
	if body.Config.ScheduleUpHour != 9 || body.Config.ScheduleDownHour != 17 {
		t.Errorf("schedule hours = %d/%d, want 9/17", body.Config.ScheduleUpHour, body.Config.ScheduleDownHour)
	}
	if len(body.Config.ScheduleDays) != 5 {
		t.Errorf("schedule_days = %v, want 5 days", body.Config.ScheduleDays)
	}
	if !body.Config.RejectPrivateIP {
		t.Errorf("reject_private_ip = false, want true")
	}
	if body.Bounds.CapPrimaryMax != 2.20 {
		t.Errorf("cap_primary_max bound = %v, want 2.20", body.Bounds.CapPrimaryMax)
	}
	if body.Bounds.ScheduleUpHourMax != 23 {
		t.Errorf("schedule_up_hour_max bound = %d, want 23", body.Bounds.ScheduleUpHourMax)
	}
}

func TestConfigReadHandler_Unseeded(t *testing.T) {
	fake := &fakeConfigReadQueries{err: pgx.ErrNoRows}
	h := newPrimaryConfigReadHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/primary/config", nil)
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200, want non-200 for unseeded table")
	}
	var env errEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error.Code != "pod_config_unseeded" {
		t.Errorf("error.code = %q, want pod_config_unseeded", env.Error.Code)
	}
}

func TestConfigReadHandler_QueryError_500(t *testing.T) {
	fake := &fakeConfigReadQueries{err: context.DeadlineExceeded}
	h := newPrimaryConfigReadHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/primary/config", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
