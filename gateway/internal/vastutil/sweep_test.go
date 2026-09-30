package vastutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

func TestParseLifecycleLabel_Accepts(t *testing.T) {
	id, ok := ParseLifecycleLabel("ifix-primary-lifecycle-494", PrimaryLabelPrefix)
	require.True(t, ok)
	require.Equal(t, int64(494), id)

	id, ok = ParseLifecycleLabel("ifix-emerg-lifecycle-7", EmergLabelPrefix)
	require.True(t, ok)
	require.Equal(t, int64(7), id)
}

func TestParseLifecycleLabel_RejectsForeignAndMalformed(t *testing.T) {
	for _, label := range []string{
		"",
		"ifix-primary-lifecycle-",
		"ifix-primary-lifecycle-0",
		"ifix-primary-lifecycle-007",
		"ifix-primary-lifecycle-12abc",
		"ifix-primary-lifecycle-12x",
		"ifix-primary-lifecycle--1",
		"ifix-primary-lifecycle-+5",
		" ifix-primary-lifecycle-5",
		"ifix-primary-lifecycle-5 ",
		"xifix-primary-lifecycle-5",
		"ifix-primary-lifecycle-5-extra",
		"ifix-primary-lifecycle-99999999999999999999999",
		"stt-tts-rerank-unified",
		"rerank-3060-v2m3",
		"stt-tts-3060-auto",
		"ifix-emerg-lifecycle-5",
	} {
		_, ok := ParseLifecycleLabel(label, PrimaryLabelPrefix)
		require.False(t, ok, "label %q must be rejected with the primary prefix", label)
	}
	_, ok := ParseLifecycleLabel("ifix-primary-lifecycle-5", EmergLabelPrefix)
	require.False(t, ok, "cross-prefix must be rejected")
}

func TestSelectLabelOrphans(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	old := float64(now.Add(-2 * time.Hour).Unix())
	young := float64(now.Add(-2 * time.Minute).Unix())

	instances := []vast.Instance{
		{ID: 53345260, Label: "ifix-primary-lifecycle-494", StartDate: old}, // orphan
		{ID: 600, Label: "ifix-primary-lifecycle-500", StartDate: old},      // live lifecycle
		{ID: 601, Label: "ifix-primary-lifecycle-501", StartDate: old},      // kept instance id
		{ID: 602, Label: "ifix-primary-lifecycle-502", StartDate: young},    // too young
		{ID: 603, Label: "ifix-primary-lifecycle-503", StartDate: 0},        // no start date → DB authoritative
		{ID: 700, Label: "stt-tts-rerank-unified", StartDate: old},          // foreign
		{ID: 701, Label: "rerank-3060-v2m3", StartDate: old},                // foreign
		{ID: 702, Label: "ifix-primary-lifecycle-", StartDate: old},         // malformed
		{ID: 703, Label: "ifix-primary-lifecycle-12x", StartDate: old},      // malformed
		{ID: 800, Label: "ifix-emerg-lifecycle-9", StartDate: old},          // other prefix
		{ID: 801, Label: "", StartDate: old},                                // unlabelled
	}
	live := map[int64]bool{500: true}
	keep := map[int64]bool{601: true}

	got := SelectLabelOrphans(instances, PrimaryLabelPrefix, live, keep, now, 10*time.Minute)
	ids := make([]int64, 0, len(got))
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	require.Equal(t, []int64{53345260, 603}, ids)

	// Emerg prefix only sees the emerg instance.
	got = SelectLabelOrphans(instances, EmergLabelPrefix, nil, nil, now, 10*time.Minute)
	require.Len(t, got, 1)
	require.Equal(t, int64(800), got[0].ID)

	// Nil maps and empty input are tolerated.
	require.Empty(t, SelectLabelOrphans(nil, PrimaryLabelPrefix, nil, nil, now, time.Minute))
}
