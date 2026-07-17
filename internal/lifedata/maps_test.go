package lifedata

import (
	"testing"
	"time"
)

func TestParseAPITimeAcceptsCommonServerFormats(t *testing.T) {
	for _, value := range []string{
		"2026-06-07T08:00:00+08:00",
		"2026-06-07T00:00:00.000Z",
		"2026-06-07T08:00:00",
		"2026-06-07T08:00",
		"2026-06-07 08:00:00",
		"2026-06-07",
		" 2026-06-07T08:00:00+08:00 ",
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

func TestFirstStringAcceptsScalarValues(t *testing.T) {
	data := map[string]any{
		"empty":     "",
		"blank":     "   ",
		"spaced":    " value ",
		"int":       12,
		"int64":     int64(13),
		"float":     float64(14),
		"floatFrac": float64(14.5),
	}
	if got := FirstString(data, "empty", "int"); got != "12" {
		t.Fatalf("FirstString int = %q", got)
	}
	if got := FirstString(data, "blank", "spaced"); got != "value" {
		t.Fatalf("FirstString trims and skips blank = %q", got)
	}
	if got := FirstString(data, "int64"); got != "13" {
		t.Fatalf("FirstString int64 = %q", got)
	}
	if got := FirstString(data, "float"); got != "14" {
		t.Fatalf("FirstString float = %q", got)
	}
	if got := FirstString(data, "floatFrac"); got != "14.5" {
		t.Fatalf("FirstString fractional float = %q", got)
	}
}

func TestFirstIntAcceptsDisplayDigits(t *testing.T) {
	data := map[string]any{"id": " 𝟷𝟸 "}
	if got := FirstInt(data, "id"); got != 12 {
		t.Fatalf("FirstInt display digits = %d", got)
	}
}

func TestLifeDataLabelsUseFallbacks(t *testing.T) {
	homework := map[string]any{
		"section": map[string]any{
			"course": map[string]any{"code": "MATH1001"},
		},
	}
	if got := HomeworkCourseLabel(homework); got != "MATH1001" {
		t.Fatalf("HomeworkCourseLabel = %q", got)
	}

	schedule := map[string]any{
		"section": map[string]any{"code": "CS1001"},
		"room":    map[string]any{"code": "3A101"},
	}
	if got := ScheduleCourseLabel(schedule); got != "CS1001" {
		t.Fatalf("ScheduleCourseLabel = %q", got)
	}
	if got := SchedulePlaceLabel(schedule); got != "3A101" {
		t.Fatalf("SchedulePlaceLabel = %q", got)
	}
}

func TestHomeworkLabel(t *testing.T) {
	homework := map[string]any{
		"title":           "Problem Set 1",
		"submissionDueAt": "2026-06-08T10:00:00+08:00",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "数据库系统"},
		},
	}
	want := "截止 06-08 10:00 · 数据库系统 · Problem Set 1"
	if got := HomeworkLabel(homework); got != want {
		t.Fatalf("HomeworkLabel = %q, want %q", got, want)
	}
}

func TestHomeworkLabelFallsBackToID(t *testing.T) {
	if got := HomeworkLabel(map[string]any{"id": "hw-1"}); got != "hw-1" {
		t.Fatalf("HomeworkLabel fallback = %q", got)
	}
}

func TestSchedulePlaceLabelPrefersCustomPlace(t *testing.T) {
	schedule := map[string]any{
		"customPlace": "线上",
		"room":        map[string]any{"nameCn": "3A101"},
	}
	if got := SchedulePlaceLabel(schedule); got != "线上" {
		t.Fatalf("SchedulePlaceLabel = %q", got)
	}
}

func TestSchedulePlaceLabelFallsBackFromBlankCustomPlace(t *testing.T) {
	schedule := map[string]any{
		"customPlace": "   ",
		"room":        map[string]any{"nameCn": "3A101"},
	}
	if got := SchedulePlaceLabel(schedule); got != "3A101" {
		t.Fatalf("SchedulePlaceLabel = %q", got)
	}
}

func TestSchedulePlaceLabelIncludesCampus(t *testing.T) {
	schedule := map[string]any{
		"room": map[string]any{
			"namePrimary": "GT-B112",
			"building": map[string]any{
				"campus": map[string]any{"namePrimary": "高新区"},
			},
		},
	}
	if got := SchedulePlaceLabel(schedule); got != "高新区 GT-B112" {
		t.Fatalf("SchedulePlaceLabel = %q", got)
	}

	schedule["room"].(map[string]any)["namePrimary"] = "高新区 GT-B112"
	if got := SchedulePlaceLabel(schedule); got != "高新区 GT-B112" {
		t.Fatalf("SchedulePlaceLabel duplicated campus: %q", got)
	}
}

func TestScheduleTimeRange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		schedule map[string]any
		want     string
	}{
		{name: "full", schedule: map[string]any{"startTime": "07:50", "endTime": "09:25"}, want: "07:50-09:25"},
		{name: "start only", schedule: map[string]any{"startTime": "07:50"}, want: "07:50-"},
		{name: "end only", schedule: map[string]any{"endTime": "09:25"}, want: "-09:25"},
		{name: "empty", schedule: map[string]any{}, want: ""},
	} {
		if got := ScheduleTimeRange(tc.schedule); got != tc.want {
			t.Fatalf("%s: ScheduleTimeRange = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScheduleFallbackLabel(t *testing.T) {
	if got := ScheduleFallbackLabel(map[string]any{"id": " 101 "}); got != "101" {
		t.Fatalf("ScheduleFallbackLabel id = %q", got)
	}
	if got := ScheduleFallbackLabel(map[string]any{
		"section": map[string]any{"id": float64(102)},
	}); got != "102" {
		t.Fatalf("ScheduleFallbackLabel section id = %q", got)
	}
}

func TestDayRFC3339RangeUsesLocalDay(t *testing.T) {
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, ChinaLocation())
	from, to := DayRFC3339Range(day)
	if from != "2026-06-06T16:00:00Z" {
		t.Fatalf("from = %q", from)
	}
	if to != "2026-06-07T15:59:59Z" {
		t.Fatalf("to = %q", to)
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

func TestSubscriptionSectionsHandlesMissingSubscription(t *testing.T) {
	for _, data := range []map[string]any{
		{},
		{"subscription": "bad"},
		{"subscription": map[string]any{"sections": "bad"}},
	} {
		if sections := SubscriptionSections(data); len(sections) != 0 {
			t.Fatalf("sections = %#v", sections)
		}
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

func TestSortSchedulesByStartUsesClockOrder(t *testing.T) {
	schedules := []map[string]any{
		{"id": "ten", "startTime": "10:00"},
		{"id": "missing"},
		{"id": "nine", "startTime": "9:05"},
		{"id": "eight", "startTime": "08:30:00"},
	}

	SortSchedulesByStart(schedules)
	got := []string{FirstString(schedules[0], "id"), FirstString(schedules[1], "id"), FirstString(schedules[2], "id"), FirstString(schedules[3], "id")}
	want := []string{"eight", "nine", "ten", "missing"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}

func TestScheduleStartTimeUsesProvidedLocation(t *testing.T) {
	loc := time.FixedZone("TEST", 9*60*60)
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	got := ScheduleStartTime(map[string]any{"startTime": "09:45"}, day, loc)
	if got.IsZero() {
		t.Fatal("ScheduleStartTime returned zero")
	}
	if got.Location() != loc || got.Format("2006-01-02 15:04") != "2026-06-07 09:45" {
		t.Fatalf("start = %s (%s)", got.Format(time.RFC3339), got.Location())
	}
}

func TestScheduleStartTimeAcceptsSeconds(t *testing.T) {
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, ChinaLocation())
	got := ScheduleStartTime(map[string]any{"startTime": "09:45:30"}, day, nil)
	if got.IsZero() || got.Format("2006-01-02 15:04:05") != "2026-06-07 09:45:30" {
		t.Fatalf("start = %s", got.Format(time.RFC3339))
	}
}

func TestScheduleStartTimeAcceptsUnpaddedClock(t *testing.T) {
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, ChinaLocation())
	got := ScheduleStartTime(map[string]any{"startTime": "9:05"}, day, nil)
	if got.IsZero() || got.Format("2006-01-02 15:04") != "2026-06-07 09:05" {
		t.Fatalf("start = %s", got.Format(time.RFC3339))
	}
}

func TestScheduleStartTimeRejectsMissingOrInvalidStart(t *testing.T) {
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, ChinaLocation())
	for _, schedule := range []map[string]any{
		{},
		{"startTime": "bad"},
		{"startTime": "09:99"},
		{"startTime": "09:45:99"},
	} {
		if got := ScheduleStartTime(schedule, day, nil); !got.IsZero() {
			t.Fatalf("ScheduleStartTime(%#v) = %s", schedule, got)
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
		{name: "float64", value: 12.0, want: 12},
		{name: "string", value: "12", want: 12},
		{name: "string with spaces", value: " 12 ", want: 12},
	} {
		got, ok := IntValue(tc.value)
		if !ok || got != tc.want {
			t.Fatalf("%s: IntValue(%#v) = %d, %t; want %d, true", tc.name, tc.value, got, ok, tc.want)
		}
	}
}

func TestIntValueRejectsInvalidValues(t *testing.T) {
	for _, value := range []any{"", "abc", nil, true, 12.9} {
		if got, ok := IntValue(value); ok || got != 0 {
			t.Fatalf("IntValue(%#v) = %d, %t; want 0, false", value, got, ok)
		}
	}
}

func TestStringSliceAcceptsStringSlices(t *testing.T) {
	got := StringSlice([]string{" one ", "", "   ", "two"})
	want := []string{"one", "two"}
	if len(got) != len(want) {
		t.Fatalf("StringSlice([]string) = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StringSlice([]string) = %#v, want %#v", got, want)
		}
	}
}

func TestStringSliceAcceptsDecodedJSONSlices(t *testing.T) {
	got := StringSlice([]any{"one", "", " two ", "   ", 3})
	want := []string{"one", "two"}
	if len(got) != len(want) {
		t.Fatalf("StringSlice([]any) = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StringSlice([]any) = %#v, want %#v", got, want)
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

func TestMapSliceAcceptsTypedMapSlices(t *testing.T) {
	items := MapSlice([]map[string]any{
		{"id": "one"},
		nil,
		{"id": "two"},
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
