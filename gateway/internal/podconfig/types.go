// Package podconfig (types.go): the typed in-memory view of the
// ai_gateway.pod_config single-row table (Phase 17 D-01/D-02).
//
// PodConfig holds the 16 HOT config fields the reconciler reads on its
// tick (cap/blocklist/schedule/budgets). PodConfigBounds holds the
// owner-editable min/max gates for the 10 numeric hot fields (D-03).
// ScheduleRule is a pre-parsed mirror of primary.ScheduleRule — a LOCAL
// copy (not an import) because the primary reconciler imports podconfig
// in Plan 17-03, and importing primary back here would form a cycle.
//
// The structural timezone is NOT a pod_config column (D-02): it is
// resolved once at boot from config.Config and held on the Loader, so the
// fail-fast time.LoadLocation can never fire post-boot (D-03a).
package podconfig

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// PodConfig is the immutable typed view of the 16 hot pod_config fields.
// Built fresh from the gen.AiGatewayPodConfig row on every Refresh.
type PodConfig struct {
	VastMachineBlocklist []int64
	VastMachineAllowlist []int64
	CapPrimary           float64
	CapFallback          float64
	HostID               int64
	RejectPrivateIP      bool
	ColdStartBudgetS     int
	PortBindBudgetS      int
	FailureCooldownS     int
	MonthlyBudgetBRL     float64
	ScheduleUpHour       int
	ScheduleDownHour     int
	ScheduleDays         []string
	GraceRampDownS       int
	ProvisionLeadS       int
	ScheduleDisabled     bool
}

// PodConfigBounds holds the owner-editable min/max gates for the 10
// numeric hot fields (D-03). The bounds gate operator-supplied values in
// the admin write endpoint (Plan 17-04); they are themselves editable.
type PodConfigBounds struct {
	CapPrimaryMin       float64
	CapPrimaryMax       float64
	CapFallbackMin      float64
	CapFallbackMax      float64
	ColdStartBudgetSMin int
	ColdStartBudgetSMax int
	PortBindBudgetSMin  int
	PortBindBudgetSMax  int
	FailureCooldownSMin int
	FailureCooldownSMax int
	MonthlyBudgetBRLMin float64
	MonthlyBudgetBRLMax float64
	ScheduleUpHourMin   int
	ScheduleUpHourMax   int
	ScheduleDownHourMin int
	ScheduleDownHourMax int
	GraceRampDownSMin   int
	GraceRampDownSMax   int
	ProvisionLeadSMin   int
	ProvisionLeadSMax   int
}

// ScheduleRule is a LOCAL data mirror of primary.ScheduleRule. It carries
// the pre-parsed schedule built from the snapshot's hot fields + the
// structural timezone. The primary reconciler (Plan 17-03) maps this into
// its own primary.ScheduleRule for the IsInPeak/ShouldBeProvisioned hot
// path — this package deliberately exposes data only (no evaluation
// methods) so it stays import-cycle-free with package primary.
type ScheduleRule struct {
	Timezone       *time.Location
	UpHour         int
	DownHour       int
	Days           map[time.Weekday]bool
	GraceRampDownS int
	ProvisionLeadS int
	Disabled       bool
}

// weekdayFromCSV maps the lowercase 3-letter day tokens used by the
// schedule_days column to time.Weekday values. Mirrors the primary
// package's identical map. Unknown tokens are silently dropped (operator
// misconfiguration surfaces at runtime as "day excluded", not a parse
// error) — matching ParseScheduleEnv semantics.
var weekdayFromCSV = map[string]time.Weekday{
	"sun": time.Sunday,
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
}

// ParseScheduleFromSnapshot builds a ScheduleRule from the hot schedule
// fields of a PodConfig using the pre-resolved structural timezone (loc).
//
// loc is resolved ONCE at boot by NewLoader (fail-fast time.LoadLocation);
// it never changes at runtime (D-03a), so this function never re-runs
// LoadLocation. The only error paths are a nil location (programmer error)
// or an out-of-range up/down hour — both keep the loader's last-good
// snapshot (never swap a broken rule into the live path, T-17-06).
func ParseScheduleFromSnapshot(cfg PodConfig, loc *time.Location) (ScheduleRule, error) {
	if loc == nil {
		return ScheduleRule{}, fmt.Errorf("nil structural timezone location")
	}
	if cfg.ScheduleUpHour < 0 || cfg.ScheduleUpHour > 23 {
		return ScheduleRule{}, fmt.Errorf("schedule up_hour out of range [0,23]: %d", cfg.ScheduleUpHour)
	}
	if cfg.ScheduleDownHour < 0 || cfg.ScheduleDownHour > 23 {
		return ScheduleRule{}, fmt.Errorf("schedule down_hour out of range [0,23]: %d", cfg.ScheduleDownHour)
	}
	days := make(map[time.Weekday]bool, 7)
	for _, raw := range cfg.ScheduleDays {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		wd, ok := weekdayFromCSV[token]
		if !ok {
			continue
		}
		days[wd] = true
	}
	return ScheduleRule{
		Timezone:       loc,
		UpHour:         cfg.ScheduleUpHour,
		DownHour:       cfg.ScheduleDownHour,
		Days:           days,
		GraceRampDownS: cfg.GraceRampDownS,
		ProvisionLeadS: cfg.ProvisionLeadS,
		Disabled:       cfg.ScheduleDisabled,
	}, nil
}

// numericToFloat converts a pgtype.Numeric to float64, returning 0 for a
// NULL/invalid value. Mirrors admin.numericPtr's Float64Value path. All
// pod_config numeric columns are NOT NULL (Plan 17-01), so the zero
// fallback is defensive only.
func numericToFloat(n pgtype.Numeric) float64 {
	if !n.Valid {
		return 0
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0
	}
	return f.Float64
}

// rowToPodConfig maps a generated single-row pod_config record into the
// typed 16-field PodConfig view.
func rowToPodConfig(r gen.AiGatewayPodConfig) PodConfig {
	return PodConfig{
		VastMachineBlocklist: r.VastMachineBlocklist,
		VastMachineAllowlist: r.VastMachineAllowlist,
		CapPrimary:           numericToFloat(r.CapPrimary),
		CapFallback:          numericToFloat(r.CapFallback),
		HostID:               r.HostID,
		RejectPrivateIP:      r.RejectPrivateIp,
		ColdStartBudgetS:     int(r.ColdstartBudgetS),
		PortBindBudgetS:      int(r.PortBindBudgetS),
		FailureCooldownS:     int(r.FailureCooldownS),
		MonthlyBudgetBRL:     numericToFloat(r.MonthlyBudgetBrl),
		ScheduleUpHour:       int(r.ScheduleUpHour),
		ScheduleDownHour:     int(r.ScheduleDownHour),
		ScheduleDays:         r.ScheduleDays,
		GraceRampDownS:       int(r.GraceRampDownS),
		ProvisionLeadS:       int(r.ProvisionLeadS),
		ScheduleDisabled:     r.ScheduleDisabled,
	}
}

// rowToBounds maps the 20 generated min/max bound columns into the typed
// PodConfigBounds view (D-03).
func rowToBounds(r gen.AiGatewayPodConfig) PodConfigBounds {
	return PodConfigBounds{
		CapPrimaryMin:       numericToFloat(r.CapPrimaryMin),
		CapPrimaryMax:       numericToFloat(r.CapPrimaryMax),
		CapFallbackMin:      numericToFloat(r.CapFallbackMin),
		CapFallbackMax:      numericToFloat(r.CapFallbackMax),
		ColdStartBudgetSMin: int(r.ColdstartBudgetSMin),
		ColdStartBudgetSMax: int(r.ColdstartBudgetSMax),
		PortBindBudgetSMin:  int(r.PortBindBudgetSMin),
		PortBindBudgetSMax:  int(r.PortBindBudgetSMax),
		FailureCooldownSMin: int(r.FailureCooldownSMin),
		FailureCooldownSMax: int(r.FailureCooldownSMax),
		MonthlyBudgetBRLMin: numericToFloat(r.MonthlyBudgetBrlMin),
		MonthlyBudgetBRLMax: numericToFloat(r.MonthlyBudgetBrlMax),
		ScheduleUpHourMin:   int(r.ScheduleUpHourMin),
		ScheduleUpHourMax:   int(r.ScheduleUpHourMax),
		ScheduleDownHourMin: int(r.ScheduleDownHourMin),
		ScheduleDownHourMax: int(r.ScheduleDownHourMax),
		GraceRampDownSMin:   int(r.GraceRampDownSMin),
		GraceRampDownSMax:   int(r.GraceRampDownSMax),
		ProvisionLeadSMin:   int(r.ProvisionLeadSMin),
		ProvisionLeadSMax:   int(r.ProvisionLeadSMax),
	}
}
