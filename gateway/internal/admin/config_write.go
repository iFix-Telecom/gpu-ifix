// Package admin (config_write.go): PATCH /admin/primary/config handler. The
// ONE net-new mutation verb into ai_gateway.pod_config (Plan 17-04). The
// dashboard owner server action (Plan 17-05) calls this with X-Admin-Key to
// land an edit; the per-column UpdatePodConfigField*/UpdatePodConfigBound*
// query fires the pod_config_changed trigger → the loader reloads live.
//
// Body: { "field": "<hot_field_or_bound_name>", "value": <typed>,
//
//	"kind": "config" | "bound" }. One field per request (clean audit diff).
//
// Defense-in-depth (D-03a, threats T-17-12/13/14):
//   - The field name is resolved against a STATIC allowlist of the 16 hot
//     columns + the bound columns — an unknown name is rejected with 400 and
//     NO dynamic column SQL is ever built (no injection surface).
//   - A config value is validated against the CURRENT bound read from the live
//     loader snapshot before the UPDATE; out-of-range → 400, no write.
//   - A bound write enforces min < max against the live counterpart bound, and
//     the schedule hour cross-field rule (up_hour != down_hour) is enforced.
//   - The allowlist contains ONLY pod_config columns — structural fields (GPU
//     name/num) are not columns here, so a structural edit cannot be smuggled.
//
// There is NO lifecycle-bounce and NO process-exit path (D-01/D-02). Audit +
// confirm are dashboard-side (D-06), not enforced here.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

const configWriteRoute = "/admin/primary/config"

// validScheduleDays is the closed set of day tokens accepted for the
// schedule_days field. Any token outside this set is a 400.
var validScheduleDays = map[string]bool{
	"sun": true, "mon": true, "tue": true, "wed": true,
	"thu": true, "fri": true, "sat": true,
}

// podConfigWriteQueries isolates the per-column write surface. *gen.Queries
// satisfies it; tests inject a fake that records call-counts. Every method is
// a static, parameterized UPDATE — there is no dynamic column SQL.
type podConfigWriteQueries interface {
	UpdatePodConfigFieldBlocklist(ctx context.Context, v []int64) error
	UpdatePodConfigFieldAllowlist(ctx context.Context, v []int64) error
	UpdatePodConfigFieldCapPrimary(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigFieldCapFallback(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigFieldHostID(ctx context.Context, v int64) error
	UpdatePodConfigFieldRejectPrivateIP(ctx context.Context, v bool) error
	UpdatePodConfigFieldColdstartBudgetS(ctx context.Context, v int32) error
	UpdatePodConfigFieldPortBindBudgetS(ctx context.Context, v int32) error
	UpdatePodConfigFieldFailureCooldownS(ctx context.Context, v int32) error
	UpdatePodConfigFieldMonthlyBudgetBRL(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigFieldScheduleUpHour(ctx context.Context, v int32) error
	UpdatePodConfigFieldScheduleDownHour(ctx context.Context, v int32) error
	UpdatePodConfigFieldScheduleDays(ctx context.Context, v []string) error
	UpdatePodConfigFieldGraceRampDownS(ctx context.Context, v int32) error
	UpdatePodConfigFieldProvisionLeadS(ctx context.Context, v int32) error
	UpdatePodConfigFieldScheduleDisabled(ctx context.Context, v bool) error

	UpdatePodConfigBoundCapPrimaryMin(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundCapPrimaryMax(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundCapFallbackMin(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundCapFallbackMax(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundColdstartBudgetSMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundColdstartBudgetSMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundPortBindBudgetSMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundPortBindBudgetSMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundFailureCooldownSMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundFailureCooldownSMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundMonthlyBudgetBRLMin(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundMonthlyBudgetBRLMax(ctx context.Context, v pgtype.Numeric) error
	UpdatePodConfigBoundScheduleUpHourMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundScheduleUpHourMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundScheduleDownHourMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundScheduleDownHourMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundGraceRampDownSMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundGraceRampDownSMax(ctx context.Context, v int32) error
	UpdatePodConfigBoundProvisionLeadSMin(ctx context.Context, v int32) error
	UpdatePodConfigBoundProvisionLeadSMax(ctx context.Context, v int32) error
}

// PrimaryConfigWriteHandler serves PATCH /admin/primary/config.
type PrimaryConfigWriteHandler struct {
	q      podConfigWriteQueries
	loader *podconfig.Loader
	log    *slog.Logger
}

// NewPrimaryConfigWriteHandler wires the production deps (the concrete
// *gen.Queries + the live loader the validation reads the current bound from).
func NewPrimaryConfigWriteHandler(q *gen.Queries, loader *podconfig.Loader, log *slog.Logger) *PrimaryConfigWriteHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryConfigWriteHandler{q: q, loader: loader, log: log.With("module", "ADMIN_PRIMARY_CONFIG_WRITE")}
}

// newPrimaryConfigWriteHandlerWithQueries is the test constructor.
func newPrimaryConfigWriteHandlerWithQueries(q podConfigWriteQueries, loader *podconfig.Loader, log *slog.Logger) *PrimaryConfigWriteHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryConfigWriteHandler{q: q, loader: loader, log: log.With("module", "ADMIN_PRIMARY_CONFIG_WRITE")}
}

type writeRequest struct {
	Field string          `json:"field"`
	Value json.RawMessage `json:"value"`
	Kind  string          `json:"kind"`
}

func (h *PrimaryConfigWriteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body writeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.bad(w, "invalid_body", "request body is not valid JSON")
		return
	}
	bounds := h.loader.Bounds()
	cur := h.loader.Cfg()

	switch body.Kind {
	case "config":
		h.writeConfig(ctx, w, body.Field, body.Value, bounds, cur)
	case "bound":
		h.writeBound(ctx, w, body.Field, body.Value, bounds)
	default:
		h.bad(w, "invalid_kind", `kind must be "config" or "bound"`)
	}
}

// writeConfig validates + writes ONE hot field. Numeric/int fields are gated
// against the CURRENT bound from the live loader snapshot (D-03a).
func (h *PrimaryConfigWriteHandler) writeConfig(ctx context.Context, w http.ResponseWriter,
	field string, raw json.RawMessage, b podconfig.PodConfigBounds, cur podconfig.PodConfig) {
	switch field {
	case "blocklist":
		v, err := decodeInt64Array(raw)
		if err != nil {
			h.badValue(w)
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldBlocklist(ctx, v))
	case "allowlist":
		v, err := decodeInt64Array(raw)
		if err != nil {
			h.badValue(w)
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldAllowlist(ctx, v))
	case "cap_primary":
		h.writeNumericConfig(ctx, w, raw, b.CapPrimaryMin, b.CapPrimaryMax, h.q.UpdatePodConfigFieldCapPrimary)
	case "cap_fallback":
		h.writeNumericConfig(ctx, w, raw, b.CapFallbackMin, b.CapFallbackMax, h.q.UpdatePodConfigFieldCapFallback)
	case "host_id":
		v, err := decodeInt64(raw)
		if err != nil {
			h.badValue(w)
			return
		}
		if v < 0 {
			h.validationErr(w, "host_id must be >= 0")
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldHostID(ctx, v))
	case "reject_private_ip":
		v, err := decodeBool(raw)
		if err != nil {
			h.badValue(w)
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldRejectPrivateIP(ctx, v))
	case "coldstart_budget_s":
		h.writeIntConfig(ctx, w, raw, b.ColdStartBudgetSMin, b.ColdStartBudgetSMax, h.q.UpdatePodConfigFieldColdstartBudgetS)
	case "port_bind_budget_s":
		h.writeIntConfig(ctx, w, raw, b.PortBindBudgetSMin, b.PortBindBudgetSMax, h.q.UpdatePodConfigFieldPortBindBudgetS)
	case "failure_cooldown_s":
		h.writeIntConfig(ctx, w, raw, b.FailureCooldownSMin, b.FailureCooldownSMax, h.q.UpdatePodConfigFieldFailureCooldownS)
	case "monthly_budget_brl":
		h.writeNumericConfig(ctx, w, raw, b.MonthlyBudgetBRLMin, b.MonthlyBudgetBRLMax, h.q.UpdatePodConfigFieldMonthlyBudgetBRL)
	case "schedule_up_hour":
		v, ok := h.decodeHour(w, raw, b.ScheduleUpHourMin, b.ScheduleUpHourMax)
		if !ok {
			return
		}
		if v == cur.ScheduleDownHour {
			h.validationErr(w, "schedule_up_hour must differ from schedule_down_hour")
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldScheduleUpHour(ctx, int32(v)))
	case "schedule_down_hour":
		v, ok := h.decodeHour(w, raw, b.ScheduleDownHourMin, b.ScheduleDownHourMax)
		if !ok {
			return
		}
		if v == cur.ScheduleUpHour {
			h.validationErr(w, "schedule_down_hour must differ from schedule_up_hour")
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldScheduleDownHour(ctx, int32(v)))
	case "schedule_days":
		v, err := decodeStrArray(raw)
		if err != nil || len(v) == 0 {
			h.validationErr(w, "schedule_days must be a non-empty array of day tokens")
			return
		}
		for _, d := range v {
			if !validScheduleDays[d] {
				h.validationErr(w, "schedule_days contains an unknown day token: "+d)
				return
			}
		}
		h.finish(w, h.q.UpdatePodConfigFieldScheduleDays(ctx, v))
	case "grace_ramp_down_s":
		h.writeIntConfig(ctx, w, raw, b.GraceRampDownSMin, b.GraceRampDownSMax, h.q.UpdatePodConfigFieldGraceRampDownS)
	case "provision_lead_s":
		h.writeIntConfig(ctx, w, raw, b.ProvisionLeadSMin, b.ProvisionLeadSMax, h.q.UpdatePodConfigFieldProvisionLeadS)
	case "schedule_disabled":
		v, err := decodeBool(raw)
		if err != nil {
			h.badValue(w)
			return
		}
		h.finish(w, h.q.UpdatePodConfigFieldScheduleDisabled(ctx, v))
	default:
		h.unknownField(w, field)
	}
}

// writeBound validates + writes ONE bound. min < max is enforced against the
// CURRENT counterpart bound from the live snapshot.
func (h *PrimaryConfigWriteHandler) writeBound(ctx context.Context, w http.ResponseWriter,
	field string, raw json.RawMessage, b podconfig.PodConfigBounds) {
	switch field {
	case "cap_primary_min":
		h.writeNumericBoundMin(ctx, w, raw, b.CapPrimaryMax, h.q.UpdatePodConfigBoundCapPrimaryMin)
	case "cap_primary_max":
		h.writeNumericBoundMax(ctx, w, raw, b.CapPrimaryMin, h.q.UpdatePodConfigBoundCapPrimaryMax)
	case "cap_fallback_min":
		h.writeNumericBoundMin(ctx, w, raw, b.CapFallbackMax, h.q.UpdatePodConfigBoundCapFallbackMin)
	case "cap_fallback_max":
		h.writeNumericBoundMax(ctx, w, raw, b.CapFallbackMin, h.q.UpdatePodConfigBoundCapFallbackMax)
	case "coldstart_budget_s_min":
		h.writeIntBoundMin(ctx, w, raw, b.ColdStartBudgetSMax, h.q.UpdatePodConfigBoundColdstartBudgetSMin)
	case "coldstart_budget_s_max":
		h.writeIntBoundMax(ctx, w, raw, b.ColdStartBudgetSMin, h.q.UpdatePodConfigBoundColdstartBudgetSMax)
	case "port_bind_budget_s_min":
		h.writeIntBoundMin(ctx, w, raw, b.PortBindBudgetSMax, h.q.UpdatePodConfigBoundPortBindBudgetSMin)
	case "port_bind_budget_s_max":
		h.writeIntBoundMax(ctx, w, raw, b.PortBindBudgetSMin, h.q.UpdatePodConfigBoundPortBindBudgetSMax)
	case "failure_cooldown_s_min":
		h.writeIntBoundMin(ctx, w, raw, b.FailureCooldownSMax, h.q.UpdatePodConfigBoundFailureCooldownSMin)
	case "failure_cooldown_s_max":
		h.writeIntBoundMax(ctx, w, raw, b.FailureCooldownSMin, h.q.UpdatePodConfigBoundFailureCooldownSMax)
	case "monthly_budget_brl_min":
		h.writeNumericBoundMin(ctx, w, raw, b.MonthlyBudgetBRLMax, h.q.UpdatePodConfigBoundMonthlyBudgetBRLMin)
	case "monthly_budget_brl_max":
		h.writeNumericBoundMax(ctx, w, raw, b.MonthlyBudgetBRLMin, h.q.UpdatePodConfigBoundMonthlyBudgetBRLMax)
	case "schedule_up_hour_min":
		h.writeIntBoundMin(ctx, w, raw, b.ScheduleUpHourMax, h.q.UpdatePodConfigBoundScheduleUpHourMin)
	case "schedule_up_hour_max":
		h.writeIntBoundMax(ctx, w, raw, b.ScheduleUpHourMin, h.q.UpdatePodConfigBoundScheduleUpHourMax)
	case "schedule_down_hour_min":
		h.writeIntBoundMin(ctx, w, raw, b.ScheduleDownHourMax, h.q.UpdatePodConfigBoundScheduleDownHourMin)
	case "schedule_down_hour_max":
		h.writeIntBoundMax(ctx, w, raw, b.ScheduleDownHourMin, h.q.UpdatePodConfigBoundScheduleDownHourMax)
	case "grace_ramp_down_s_min":
		h.writeIntBoundMin(ctx, w, raw, b.GraceRampDownSMax, h.q.UpdatePodConfigBoundGraceRampDownSMin)
	case "grace_ramp_down_s_max":
		h.writeIntBoundMax(ctx, w, raw, b.GraceRampDownSMin, h.q.UpdatePodConfigBoundGraceRampDownSMax)
	case "provision_lead_s_min":
		h.writeIntBoundMin(ctx, w, raw, b.ProvisionLeadSMax, h.q.UpdatePodConfigBoundProvisionLeadSMin)
	case "provision_lead_s_max":
		h.writeIntBoundMax(ctx, w, raw, b.ProvisionLeadSMin, h.q.UpdatePodConfigBoundProvisionLeadSMax)
	default:
		h.unknownField(w, field)
	}
}

// ---- typed write helpers ----

func (h *PrimaryConfigWriteHandler) writeNumericConfig(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, min, max float64, fn func(context.Context, pgtype.Numeric) error) {
	v, err := decodeFloat(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v < min || v > max {
		h.validationErr(w, "value out of bound")
		return
	}
	num, err := toNumeric(v)
	if err != nil {
		h.badValue(w)
		return
	}
	h.finish(w, fn(ctx, num))
}

func (h *PrimaryConfigWriteHandler) writeIntConfig(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, min, max int, fn func(context.Context, int32) error) {
	v, err := decodeInt(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v < min || v > max {
		h.validationErr(w, "value out of bound")
		return
	}
	h.finish(w, fn(ctx, int32(v)))
}

// decodeHour decodes an hour, enforces 0-23 AND the live bound. Returns ok=false
// (and writes the 400) on any failure.
func (h *PrimaryConfigWriteHandler) decodeHour(w http.ResponseWriter, raw json.RawMessage, min, max int) (int, bool) {
	v, err := decodeInt(raw)
	if err != nil {
		h.badValue(w)
		return 0, false
	}
	if v < 0 || v > 23 {
		h.validationErr(w, "hour must be in [0,23]")
		return 0, false
	}
	if v < min || v > max {
		h.validationErr(w, "hour out of bound")
		return 0, false
	}
	return v, true
}

func (h *PrimaryConfigWriteHandler) writeNumericBoundMin(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, curMax float64, fn func(context.Context, pgtype.Numeric) error) {
	v, err := decodeFloat(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v >= curMax {
		h.validationErr(w, "bound min must be < max")
		return
	}
	num, err := toNumeric(v)
	if err != nil {
		h.badValue(w)
		return
	}
	h.finish(w, fn(ctx, num))
}

func (h *PrimaryConfigWriteHandler) writeNumericBoundMax(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, curMin float64, fn func(context.Context, pgtype.Numeric) error) {
	v, err := decodeFloat(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v <= curMin {
		h.validationErr(w, "bound max must be > min")
		return
	}
	num, err := toNumeric(v)
	if err != nil {
		h.badValue(w)
		return
	}
	h.finish(w, fn(ctx, num))
}

func (h *PrimaryConfigWriteHandler) writeIntBoundMin(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, curMax int, fn func(context.Context, int32) error) {
	v, err := decodeInt(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v >= curMax {
		h.validationErr(w, "bound min must be < max")
		return
	}
	h.finish(w, fn(ctx, int32(v)))
}

func (h *PrimaryConfigWriteHandler) writeIntBoundMax(ctx context.Context, w http.ResponseWriter,
	raw json.RawMessage, curMin int, fn func(context.Context, int32) error) {
	v, err := decodeInt(raw)
	if err != nil {
		h.badValue(w)
		return
	}
	if v <= curMin {
		h.validationErr(w, "bound max must be > min")
		return
	}
	h.finish(w, fn(ctx, int32(v)))
}

// ---- terminal responses ----

// finish writes 200 on a nil query error, else a 500 envelope.
func (h *PrimaryConfigWriteHandler) finish(w http.ResponseWriter, err error) {
	if err != nil {
		h.log.Error("pod_config write failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "pod_config_write_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(configWriteRoute, "5xx").Inc()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	obs.GatewayAdminRequests.WithLabelValues(configWriteRoute, "2xx").Inc()
}

func (h *PrimaryConfigWriteHandler) bad(w http.ResponseWriter, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(configWriteRoute, "4xx").Inc()
}

func (h *PrimaryConfigWriteHandler) badValue(w http.ResponseWriter) {
	h.bad(w, "invalid_value", "value has the wrong type for this field")
}

func (h *PrimaryConfigWriteHandler) validationErr(w http.ResponseWriter, msg string) {
	h.bad(w, "validation_error", msg)
}

func (h *PrimaryConfigWriteHandler) unknownField(w http.ResponseWriter, field string) {
	h.bad(w, "unknown_field", "unknown field: "+field)
}

// ---- value decoders ----

func decodeFloat(raw json.RawMessage) (float64, error) {
	var v float64
	err := json.Unmarshal(raw, &v)
	return v, err
}

func decodeInt(raw json.RawMessage) (int, error) {
	var v int
	err := json.Unmarshal(raw, &v)
	return v, err
}

func decodeInt64(raw json.RawMessage) (int64, error) {
	var v int64
	err := json.Unmarshal(raw, &v)
	return v, err
}

func decodeInt64Array(raw json.RawMessage) ([]int64, error) {
	v := []int64{}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func decodeStrArray(raw json.RawMessage) ([]string, error) {
	v := []string{}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func decodeBool(raw json.RawMessage) (bool, error) {
	var v bool
	err := json.Unmarshal(raw, &v)
	return v, err
}

// toNumeric converts a float64 into a pgtype.Numeric via its string Scan path
// (the same canonical conversion the rest of the gateway uses for numeric
// columns). Returns an error only on a non-finite value the formatter rejects.
func toNumeric(v float64) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(v, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}
