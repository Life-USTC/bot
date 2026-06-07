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

func TestIntValueAcceptsCommonAPIShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  int
	}{
		{name: "int", value: 12, want: 12},
		{name: "int64", value: int64(12), want: 12},
		{name: "float64", value: 12.9, want: 12},
		{name: "string", value: "12", want: 12},
	} {
		got, ok := IntValue(tc.value)
		if !ok || got != tc.want {
			t.Fatalf("%s: IntValue(%#v) = %d, %t; want %d, true", tc.name, tc.value, got, ok, tc.want)
		}
	}
}

func TestIntValueRejectsInvalidValues(t *testing.T) {
	for _, value := range []any{"", "abc", nil, true} {
		if got, ok := IntValue(value); ok || got != 0 {
			t.Fatalf("IntValue(%#v) = %d, %t; want 0, false", value, got, ok)
		}
	}
}

func TestMapSliceSkipsNonMaps(t *testing.T) {
	items := MapSlice([]any{
		map[string]any{"id": "one"},
		"ignored",
		nil,
		map[string]any{"id": "two"},
	})

	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2: %#v", len(items), items)
	}
	got := []string{FirstString(items[0], "id"), FirstString(items[1], "id")}
	want := []string{"one", "two"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %#v, want ids %#v", items, want)
		}
	}
}

func TestMapSliceReturnsEmptyForNonSlice(t *testing.T) {
	if got := MapSlice(map[string]any{"id": "one"}); len(got) != 0 {
		t.Fatalf("MapSlice(non-slice) = %#v", got)
	}
}
