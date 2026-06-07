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

func TestSortHomeworksByDueUsesParsedTimes(t *testing.T) {
	homeworks := []map[string]any{
		{"id": "late", "submissionDueAt": "2026-06-07T10:00:00+08:00"},
		{"id": "early", "submissionDueAt": "2026-06-07 09:00:00"},
		{"id": "invalid", "submissionDueAt": "not-a-date"},
	}

	SortHomeworksByDue(homeworks)
	got := []string{FirstString(homeworks[0], "id"), FirstString(homeworks[1], "id"), FirstString(homeworks[2], "id")}
	want := []string{"early", "late", "invalid"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}

func TestSortSchedulesByStartIsStable(t *testing.T) {
	schedules := []map[string]any{
		{"id": "second", "startTime": "10:00"},
		{"id": "first", "startTime": "08:00"},
		{"id": "same", "startTime": "10:00"},
	}

	SortSchedulesByStart(schedules)
	got := []string{FirstString(schedules[0], "id"), FirstString(schedules[1], "id"), FirstString(schedules[2], "id")}
	want := []string{"first", "second", "same"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}
