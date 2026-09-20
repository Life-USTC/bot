package responses

import (
	"testing"
	"time"
)

func TestRichFooterShowsDateWeekdayTimeRequestNumberAndSource(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 34, 0, 0, time.FixedZone("CST", 8*60*60))
	want := [2]string{"2026-07-15（周三）12:34", "#1284 · Life @ USTC"}
	if got := richFooterLines(now, "1284"); got != want {
		t.Fatalf("footer = %#v, want %#v", got, want)
	}
}

// A card rendered outside the durable outbox has no record to name, and an
// empty number must not leave a dangling "#" in the footer.

// A card rendered outside the durable outbox has no record to name, and an
// empty number must not leave a dangling "#" in the footer.
func TestRichFooterOmitsAnAbsentRequestNumber(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 34, 0, 0, time.FixedZone("CST", 8*60*60))
	want := [2]string{"2026-07-15（周三）12:34", "Life @ USTC"}
	if got := richFooterLines(now, "  "); got != want {
		t.Fatalf("footer = %#v, want %#v", got, want)
	}
}

func TestFooterWeekdayLabelsCoverEveryDay(t *testing.T) {
	start := time.Date(2026, 7, 13, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	want := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	for i, label := range want {
		if got := weekdayLabel(start.AddDate(0, 0, i)); got != label {
			t.Fatalf("weekday %d = %q, want %q", i, got, label)
		}
	}
}
