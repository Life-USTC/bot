package lifedata

import (
	"testing"
	"time"
)

func TestParseAPITimeAcceptsCommonServerFormats(t *testing.T) {
	for _, value := range []string{
		"2026-06-07T08:00:00+08:00",
		"2026-06-07T00:00:00.000Z",
		"2026-06-07 08:00:00",
		"2026-06-07",
	} {
		if _, ok := ParseAPITime(value); !ok {
			t.Fatalf("ParseAPITime(%q) failed", value)
		}
	}
}

func TestFormatAPITimeKeepsLocalDateOnlyAtMidnight(t *testing.T) {
	if got := FormatAPITime("2026-06-07"); got != "06-07 00:00" {
		t.Fatalf("FormatAPITime(date) = %q", got)
	}
	if got := FormatAPITime("2026-06-07 08:30:00"); got != "06-07 08:30" {
		t.Fatalf("FormatAPITime(local datetime) = %q", got)
	}
}

func TestSubscriptionSectionIDsForDayFiltersBySemester(t *testing.T) {
	data := map[string]any{
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{
					"id": "current",
					"semester": map[string]any{
						"startDate": "2026-02-16",
						"endDate":   "2026-07-01",
					},
				},
				map[string]any{
					"id": "old",
					"semester": map[string]any{
						"startDate": "2025-09-01",
						"endDate":   "2026-01-20",
					},
				},
			},
		},
	}

	day := time.Date(2026, 6, 7, 12, 0, 0, 0, ChinaLocation())
	ids := SubscriptionSectionIDsForDay(data, day)
	if len(ids) != 1 || ids[0] != "current" {
		t.Fatalf("ids = %#v", ids)
	}
}
