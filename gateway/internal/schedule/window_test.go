package schedule_test

import (
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/schedule"
)

func tt(h, m int) time.Time {
	return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC)
}

func TestInWindow_NormalRange(t *testing.T) {
	cases := []struct {
		name            string
		now, start, end time.Time
		want            bool
	}{
		{"mid-window", tt(9, 0), tt(8, 0), tt(22, 0), true},
		{"before-start", tt(7, 59), tt(8, 0), tt(22, 0), false},
		{"start-inclusive", tt(8, 0), tt(8, 0), tt(22, 0), true},
		{"last-minute-before-end", tt(21, 59), tt(8, 0), tt(22, 0), true},
		{"end-exclusive", tt(22, 0), tt(8, 0), tt(22, 0), false},
		{"after-end", tt(23, 0), tt(8, 0), tt(22, 0), false},
	}
	for _, c := range cases {
		if got := schedule.InWindow(c.now, c.start, c.end); got != c.want {
			t.Errorf("[%s] InWindow(%s, %s, %s) = %v, want %v",
				c.name,
				c.now.Format("15:04"), c.start.Format("15:04"), c.end.Format("15:04"),
				got, c.want)
		}
	}
}

func TestInWindow_WrapAround(t *testing.T) {
	cases := []struct {
		name            string
		now, start, end time.Time
		want            bool
	}{
		{"late-night-inside", tt(2, 0), tt(22, 0), tt(8, 0), true},
		{"afternoon-outside", tt(15, 0), tt(22, 0), tt(8, 0), false},
		{"start-inclusive", tt(22, 0), tt(22, 0), tt(8, 0), true},
		{"end-exclusive", tt(8, 0), tt(22, 0), tt(8, 0), false},
		{"minute-before-end", tt(7, 59), tt(22, 0), tt(8, 0), true},
		{"late-evening-inside", tt(23, 59), tt(22, 0), tt(8, 0), true},
	}
	for _, c := range cases {
		if got := schedule.InWindow(c.now, c.start, c.end); got != c.want {
			t.Errorf("[%s] InWindow(%s, %s, %s) = %v, want %v",
				c.name,
				c.now.Format("15:04"), c.start.Format("15:04"), c.end.Format("15:04"),
				got, c.want)
		}
	}
}
