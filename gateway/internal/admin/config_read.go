// Package admin (config_read.go): GET /admin/primary/config handler. Serves
// the CURRENT ai_gateway.pod_config row (16 hot fields + the numeric min/max
// bound pairs) read via the GetPodConfig sqlc query (Plan 17-01).
//
// This is the read seam the dashboard editor uses for current values, the
// server action re-reads to refetch the LIVE bound during validation, and the
// audit sources oldValue from. It reads pod_config — NOT /admin/operations,
// whose boot-env snapshot DIVERGES from pod_config after any edit
// (D-01). Structural fields (GPU name/num) are NOT pod_config columns (D-02),
// so they are absent here by construction.
//
// Mirrors OperationsHandler: query-interface isolation, dual constructor,
// OpenAI error envelope on query failure, admin-metric increment per branch.
// Threat T-17-23: only pod_config columns are serialized — VAST/MINIO/DSN
// secrets are not columns here, so they cannot leak.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const configReadRoute = "/admin/primary/config"

// ConfigReadResponse is the GET /admin/primary/config contract. The Go struct
// is the source of truth (consumed field-for-field by Plan 17-05).
type ConfigReadResponse struct {
	Config ConfigSection `json:"config"`
	Bounds BoundsSection `json:"bounds"`
	// Structural: boot-env fields (NOT pod_config) shown read-only by the
	// dashboard. Changed only by redeploy/env. Only image tags, GPU shape,
	// MinIO object keys and their sha256 (public-grade integrity digests) —
	// no credential is serialized. Omitted when the handler was built without it.
	Structural *StructuralSection `json:"structural,omitempty"`
}

// StructuralSection mirrors the dashboard's "Configuração estrutural" panel.
type StructuralSection struct {
	Timezone                string   `json:"timezone"`
	GPUNamePrimary          string   `json:"gpu_name_primary"`
	GPUNameFallback         string   `json:"gpu_name_fallback"`
	NumGPUsPrimary          int      `json:"num_gpus_primary"`
	NumGPUsFallback         int      `json:"num_gpus_fallback"`
	TemplateImage           string   `json:"template_image"`
	InfinityImage           string   `json:"infinity_image"`
	DCGMImage               string   `json:"dcgm_image"`
	SpeachesImage           string   `json:"speaches_image"`
	QwenWeightsKey          string   `json:"qwen_weights_key"`
	QwenWeightsSHA256       string   `json:"qwen_weights_sha256"`
	WhisperWeightsKey       string   `json:"whisper_weights_key"`
	WhisperWeightsSHA256    string   `json:"whisper_weights_sha256"`
	BGEM3WeightsKey         string   `json:"bgem3_weights_key"`
	BGEM3WeightsSHA256      string   `json:"bgem3_weights_sha256"`
	ChatterboxWeightsKey    string   `json:"chatterbox_weights_key"`
	ChatterboxWeightsSHA256 string   `json:"chatterbox_weights_sha256"`
	QwenJinjaKey            string   `json:"qwen_jinja_key"`
	QwenJinjaSHA256         string   `json:"qwen_jinja_sha256"`
	LlamaArgs               []string `json:"llama_args"`
	// LlamaArgsDefault: true when PRIMARY_LLAMA_ARGS is empty and LlamaArgs is
	// the code default.
	LlamaArgsDefault bool `json:"llama_args_default"`
}

// StructuralFromConfig builds the read-only structural view from boot config.
// defaultLlamaArgs is the code default used when PRIMARY_LLAMA_ARGS is empty.
func StructuralFromConfig(cfg config.Config, defaultLlamaArgs []string) StructuralSection {
	s := StructuralSection{
		Timezone:                cfg.PrimaryPodScheduleTimezone,
		GPUNamePrimary:          cfg.PrimaryVastGPUNamePrimary,
		GPUNameFallback:         cfg.PrimaryVastGPUNameFallback,
		NumGPUsPrimary:          cfg.PrimaryVastNumGPUsPrimary,
		NumGPUsFallback:         cfg.PrimaryVastNumGPUsFallback,
		TemplateImage:           cfg.PrimaryTemplateImage,
		InfinityImage:           cfg.PrimaryInfinityImage,
		DCGMImage:               cfg.PrimaryDCGMImage,
		SpeachesImage:           cfg.PrimarySpeachesImage,
		QwenWeightsKey:          cfg.PrimaryQwenWeightsKey,
		QwenWeightsSHA256:       cfg.PrimaryQwenWeightsSHA256,
		WhisperWeightsKey:       cfg.PrimaryWhisperWeightsKey,
		WhisperWeightsSHA256:    cfg.PrimaryWhisperWeightsSHA256,
		BGEM3WeightsKey:         cfg.PrimaryBGEM3WeightsKey,
		BGEM3WeightsSHA256:      cfg.PrimaryBGEM3WeightsSHA256,
		ChatterboxWeightsKey:    cfg.PrimaryChatterboxWeightsKey,
		ChatterboxWeightsSHA256: cfg.PrimaryChatterboxWeightsSHA256,
		QwenJinjaKey:            cfg.PrimaryQwenJinjaKey,
		QwenJinjaSHA256:         cfg.PrimaryQwenJinjaSHA256,
		LlamaArgs:               cfg.PrimaryLlamaArgs,
	}
	if len(s.LlamaArgs) == 0 {
		s.LlamaArgs = defaultLlamaArgs
		s.LlamaArgsDefault = true
	}
	return s
}

// ConfigSection is the 16 hot pod_config fields, typed.
type ConfigSection struct {
	VastMachineBlocklist []int64  `json:"vast_machine_blocklist"`
	VastMachineAllowlist []int64  `json:"vast_machine_allowlist"`
	CapPrimary           float64  `json:"cap_primary"`
	CapFallback          float64  `json:"cap_fallback"`
	HostID               int64    `json:"host_id"`
	ForceMachineID       int64    `json:"force_machine_id"`
	RejectPrivateIP      bool     `json:"reject_private_ip"`
	ColdstartBudgetS     int      `json:"coldstart_budget_s"`
	PortBindBudgetS      int      `json:"port_bind_budget_s"`
	CreatedBudgetS       int      `json:"created_budget_s"`
	ProgressStallBudgetS int      `json:"progress_stall_budget_s"`
	FailureCooldownS     int      `json:"failure_cooldown_s"`
	MonthlyBudgetBRL     float64  `json:"monthly_budget_brl"`
	ScheduleUpHour       int      `json:"schedule_up_hour"`
	ScheduleDownHour     int      `json:"schedule_down_hour"`
	ScheduleDays         []string `json:"schedule_days"`
	GraceRampDownS       int      `json:"grace_ramp_down_s"`
	ProvisionLeadS       int      `json:"provision_lead_s"`
	ScheduleDisabled     bool     `json:"schedule_disabled"`
	// quick-261001-qdd: primary bid/on-demand offer policy.
	OfferMode            string  `json:"offer_mode"`
	BidMargin            float64 `json:"bid_margin"`
	MaxPreemptionsPerDay int     `json:"max_preemptions_per_day"`
	MinReliability       float64 `json:"min_reliability"`
}

// BoundsSection is the owner-editable min/max gate pairs for the numeric hot
// fields (D-03). All bound columns are NOT NULL (Plan 17-01).
type BoundsSection struct {
	CapPrimaryMin           float64 `json:"cap_primary_min"`
	CapPrimaryMax           float64 `json:"cap_primary_max"`
	CapFallbackMin          float64 `json:"cap_fallback_min"`
	CapFallbackMax          float64 `json:"cap_fallback_max"`
	ColdstartBudgetSMin     int     `json:"coldstart_budget_s_min"`
	ColdstartBudgetSMax     int     `json:"coldstart_budget_s_max"`
	PortBindBudgetSMin      int     `json:"port_bind_budget_s_min"`
	PortBindBudgetSMax      int     `json:"port_bind_budget_s_max"`
	CreatedBudgetSMin       int     `json:"created_budget_s_min"`
	CreatedBudgetSMax       int     `json:"created_budget_s_max"`
	ProgressStallBudgetSMin int     `json:"progress_stall_budget_s_min"`
	ProgressStallBudgetSMax int     `json:"progress_stall_budget_s_max"`
	FailureCooldownSMin     int     `json:"failure_cooldown_s_min"`
	FailureCooldownSMax     int     `json:"failure_cooldown_s_max"`
	MonthlyBudgetBRLMin     float64 `json:"monthly_budget_brl_min"`
	MonthlyBudgetBRLMax     float64 `json:"monthly_budget_brl_max"`
	ScheduleUpHourMin       int     `json:"schedule_up_hour_min"`
	ScheduleUpHourMax       int     `json:"schedule_up_hour_max"`
	ScheduleDownHourMin     int     `json:"schedule_down_hour_min"`
	ScheduleDownHourMax     int     `json:"schedule_down_hour_max"`
	GraceRampDownSMin       int     `json:"grace_ramp_down_s_min"`
	GraceRampDownSMax       int     `json:"grace_ramp_down_s_max"`
	ProvisionLeadSMin       int     `json:"provision_lead_s_min"`
	ProvisionLeadSMax       int     `json:"provision_lead_s_max"`
}

// podConfigReadQueries isolates the sqlc surface used by the handler.
type podConfigReadQueries interface {
	GetPodConfig(ctx context.Context) (gen.AiGatewayPodConfig, error)
}

// PrimaryConfigReadHandler serves GET /admin/primary/config.
type PrimaryConfigReadHandler struct {
	q          podConfigReadQueries
	log        *slog.Logger
	structural *StructuralSection
}

// WithStructural attaches the boot-env structural view to every response.
func (h *PrimaryConfigReadHandler) WithStructural(s StructuralSection) *PrimaryConfigReadHandler {
	h.structural = &s
	return h
}

// NewPrimaryConfigReadHandler wires the production dependency (the concrete
// *gen.Queries).
func NewPrimaryConfigReadHandler(q *gen.Queries, log *slog.Logger) *PrimaryConfigReadHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryConfigReadHandler{q: q, log: log.With("module", "ADMIN_PRIMARY_CONFIG_READ")}
}

// newPrimaryConfigReadHandlerWithQueries is the test constructor.
func newPrimaryConfigReadHandlerWithQueries(q podConfigReadQueries, log *slog.Logger) *PrimaryConfigReadHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryConfigReadHandler{q: q, log: log.With("module", "ADMIN_PRIMARY_CONFIG_READ")}
}

func (h *PrimaryConfigReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, err := h.q.GetPodConfig(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Boot seed (Plan 17-03) guarantees a row — an empty table is an
			// invariant violation, surfaced as a typed envelope (not a panic).
			h.log.Error("GetPodConfig returned no rows — pod_config unseeded")
			httpx.WriteOpenAIError(w, http.StatusInternalServerError,
				"api_error", "pod_config_unseeded", "pod_config has no row — boot seed missing")
			obs.GatewayAdminRequests.WithLabelValues(configReadRoute, "5xx").Inc()
			return
		}
		h.log.Error("GetPodConfig failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "pod_config_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(configReadRoute, "5xx").Inc()
		return
	}

	resp := ConfigReadResponse{
		Structural: h.structural,
		Config: ConfigSection{
			VastMachineBlocklist: row.VastMachineBlocklist,
			VastMachineAllowlist: row.VastMachineAllowlist,
			CapPrimary:           numericFloat(row.CapPrimary),
			CapFallback:          numericFloat(row.CapFallback),
			HostID:               row.HostID,
			ForceMachineID:       row.ForceMachineID,
			RejectPrivateIP:      row.RejectPrivateIp,
			ColdstartBudgetS:     int(row.ColdstartBudgetS),
			PortBindBudgetS:      int(row.PortBindBudgetS),
			CreatedBudgetS:       int(row.CreatedBudgetS),
			ProgressStallBudgetS: int(row.ProgressStallBudgetS),
			FailureCooldownS:     int(row.FailureCooldownS),
			MonthlyBudgetBRL:     numericFloat(row.MonthlyBudgetBrl),
			ScheduleUpHour:       int(row.ScheduleUpHour),
			ScheduleDownHour:     int(row.ScheduleDownHour),
			ScheduleDays:         row.ScheduleDays,
			GraceRampDownS:       int(row.GraceRampDownS),
			ProvisionLeadS:       int(row.ProvisionLeadS),
			ScheduleDisabled:     row.ScheduleDisabled,
			OfferMode:            row.OfferMode,
			BidMargin:            numericFloat(row.BidMargin),
			MaxPreemptionsPerDay: int(row.MaxPreemptionsPerDay),
			MinReliability:       numericFloat(row.MinReliability),
		},
		Bounds: BoundsSection{
			CapPrimaryMin:           numericFloat(row.CapPrimaryMin),
			CapPrimaryMax:           numericFloat(row.CapPrimaryMax),
			CapFallbackMin:          numericFloat(row.CapFallbackMin),
			CapFallbackMax:          numericFloat(row.CapFallbackMax),
			ColdstartBudgetSMin:     int(row.ColdstartBudgetSMin),
			ColdstartBudgetSMax:     int(row.ColdstartBudgetSMax),
			PortBindBudgetSMin:      int(row.PortBindBudgetSMin),
			PortBindBudgetSMax:      int(row.PortBindBudgetSMax),
			CreatedBudgetSMin:       int(row.CreatedBudgetSMin),
			CreatedBudgetSMax:       int(row.CreatedBudgetSMax),
			ProgressStallBudgetSMin: int(row.ProgressStallBudgetSMin),
			ProgressStallBudgetSMax: int(row.ProgressStallBudgetSMax),
			FailureCooldownSMin:     int(row.FailureCooldownSMin),
			FailureCooldownSMax:     int(row.FailureCooldownSMax),
			MonthlyBudgetBRLMin:     numericFloat(row.MonthlyBudgetBrlMin),
			MonthlyBudgetBRLMax:     numericFloat(row.MonthlyBudgetBrlMax),
			ScheduleUpHourMin:       int(row.ScheduleUpHourMin),
			ScheduleUpHourMax:       int(row.ScheduleUpHourMax),
			ScheduleDownHourMin:     int(row.ScheduleDownHourMin),
			ScheduleDownHourMax:     int(row.ScheduleDownHourMax),
			GraceRampDownSMin:       int(row.GraceRampDownSMin),
			GraceRampDownSMax:       int(row.GraceRampDownSMax),
			ProvisionLeadSMin:       int(row.ProvisionLeadSMin),
			ProvisionLeadSMax:       int(row.ProvisionLeadSMax),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues(configReadRoute, "2xx").Inc()
}

// numericFloat converts a NOT-NULL pod_config numeric column into a float64,
// returning 0 for a NULL/invalid value (defensive — all columns are NOT NULL).
func numericFloat(n pgtype.Numeric) float64 {
	if !n.Valid {
		return 0
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0
	}
	return f.Float64
}
