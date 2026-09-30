package vastutil

// Label sweep primitives (ClickUp 86akr57nj, 2026-09-30).
//
// The primary and emerg leaders periodically list every instance on the
// Vast account and destroy the ones whose label names a gateway
// lifecycle that is no longer live in the DB. This file holds the pure,
// I/O-free half: a STRICT label parser and the orphan selector.
//
// NEVER loosen the match to strings.HasPrefix / strings.Contains: other
// flows share the same Vast account (the 3060 pod "stt-tts-rerank-unified",
// "rerank-3060-v2m3", ...) and must never be touched by the gateway. A
// label is accepted only when it is exactly <prefix><positive base-10
// integer> — no sign, no leading zero, no spaces, no trailing chars.

import (
	"strconv"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

const (
	// PrimaryLabelPrefix matches primary/lifecycle.go's
	// fmt.Sprintf("ifix-primary-lifecycle-%d", lifecycleID).
	PrimaryLabelPrefix = "ifix-primary-lifecycle-"
	// EmergLabelPrefix matches emerg/lifecycle.go's
	// fmt.Sprintf("ifix-emerg-lifecycle-%d", lifecycleID).
	EmergLabelPrefix = "ifix-emerg-lifecycle-"
)

// ParseLifecycleLabel returns the lifecycle id encoded in label when the
// label is exactly prefix + a positive decimal integer. Any deviation
// (empty id, sign, leading zero, whitespace, extra chars, overflow)
// returns (0, false).
func ParseLifecycleLabel(label, prefix string) (int64, bool) {
	if prefix == "" {
		return 0, false
	}
	rest, ok := strings.CutPrefix(label, prefix)
	if !ok || rest == "" || rest[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// SelectLabelOrphans returns the instances whose label parses with
// prefix and that are NOT protected by any guard:
//
//   - lifecycle id present in liveLifecycleIDs → kept
//   - instance id present in keepInstanceIDs → kept
//   - StartDate > 0 and younger than minAge → kept (race belt; when
//     StartDate is 0 the DB is authoritative and no age guard applies)
//
// Instances with foreign or malformed labels are never selected. Nil
// maps are treated as empty. Input order is preserved.
func SelectLabelOrphans(
	instances []vast.Instance,
	prefix string,
	liveLifecycleIDs map[int64]bool,
	keepInstanceIDs map[int64]bool,
	now time.Time,
	minAge time.Duration,
) []vast.Instance {
	var out []vast.Instance
	for _, inst := range instances {
		lcID, ok := ParseLifecycleLabel(inst.Label, prefix)
		if !ok {
			continue
		}
		if liveLifecycleIDs[lcID] || keepInstanceIDs[inst.ID] {
			continue
		}
		if inst.StartDate > 0 {
			started := time.Unix(0, int64(inst.StartDate*float64(time.Second)))
			if now.Sub(started) < minAge {
				continue
			}
		}
		out = append(out, inst)
	}
	return out
}
