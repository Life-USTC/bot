package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func captureRemoteGridPayload(t *testing.T, grid *ScheduleGrid, title string, now time.Time) remoteGridPayload {
	t.Helper()
	var gotRequest remoteRenderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Image-Width", "8")
		w.Header().Set("X-Image-Height", "6")
		_, _ = w.Write(testRemotePNG(t))
	}))
	defer server.Close()

	img := NewScheduleGridImage("schedule", title, grid, "课表")
	if img == nil {
		t.Fatal("NewScheduleGridImage returned nil")
	}
	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return now
		},
	}
	if _, width, height, err := renderer.RenderPNG(img); err != nil {
		t.Fatalf("RenderPNG: %v", err)
	} else if width != 8 || height != 6 {
		t.Fatalf("dimensions = %dx%d, want 8x6", width, height)
	}
	if gotRequest.Kind != "grid" {
		t.Fatalf("request kind = %q, want grid", gotRequest.Kind)
	}
	var got remoteGridPayload
	if err := json.Unmarshal(gotRequest.Payload, &got); err != nil {
		t.Fatalf("decode grid payload: %v", err)
	}
	return got
}

func remoteSchedulePeriods() []ScheduleGridPeriod {
	return []ScheduleGridPeriod{
		{Label: "第 1 节", Time: "08:00–08:45"},
		{Label: "第 2 节", Time: "08:50–09:35"},
		{Label: "第 3 节", Time: "09:55–10:40"},
		{Label: "第 4 节", Time: "10:45–11:30"},
	}
}

func remoteScheduleDays() []ScheduleGridDay {
	return []ScheduleGridDay{
		{Label: "周日", Date: "07-12"},
		{Label: "周一", Date: "07-13"},
		{Label: "周二", Date: "07-14"},
		{Label: "周三", Date: "07-15"},
		{Label: "周四", Date: "07-16"},
		{Label: "今天", Date: "07-17"},
		{Label: "周六", Date: "07-18"},
	}
}

func TestRemoteRendererGridPayloadPreservesReadableAgendaData(t *testing.T) {
	longCourse := "Introduction to Computational Thinking and Programming Methodology 数据库系统"
	longLocation := "东区教学楼与高新区 GT-B112 之间的综合教学地点"
	longWeeks := "第 1–16 周（单周与双周均有安排）"
	grid := &ScheduleGrid{
		Days:    remoteScheduleDays(),
		Periods: remoteSchedulePeriods(),
		Items: []ScheduleGridItem{
			// Deliberately unsorted: the payload should be chronological within
			// each vertically stacked day section.
			{Day: 2, StartPeriod: 3, EndPeriod: 4, Course: longCourse, Location: longLocation, Weeks: longWeeks},
			{Day: 0, StartPeriod: 2, EndPeriod: 2, Course: "线性代数", Location: "东区 · 3A204", Weeks: "第 1–16 周"},
			{Day: 0, StartPeriod: 1, EndPeriod: 1, Course: "微积分", Location: "东区 · 3A204", Weeks: "第 1–16 周"},
			{Day: -1, StartPeriod: 1, EndPeriod: 1, Course: "invalid day"},
			{Day: 1, StartPeriod: 0, EndPeriod: 1, Course: "invalid period"},
			{Day: 1, StartPeriod: 1, EndPeriod: 8, Course: "invalid end"},
		},
	}

	got := captureRemoteGridPayload(t, grid, "  ", time.Date(2026, 7, 17, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if got.Title != "课表" {
		t.Fatalf("title = %q, want fallback title", got.Title)
	}
	if got.Summary != "周日–周六 · 第 1–4 节" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if len(got.Days) != 7 || !got.Days[5].Today {
		t.Fatalf("days = %#v, want day 5 highlighted", got.Days)
	}
	if got.Days[5].Label != "今天" {
		t.Fatalf("today label = %q, want no duplicate 今天", got.Days[5].Label)
	}
	if len(got.Items) != 3 {
		t.Fatalf("items = %d, want 3 valid items", len(got.Items))
	}
	if got.Items[0].Course != "微积分" || got.Items[1].Course != "线性代数" || got.Items[2].Course != longCourse {
		t.Fatalf("item order/courses = %#v", got.Items)
	}
	merged := got.Items[2]
	if merged.Period != "第 3 节–第 4 节" || merged.Time != "09:55–11:30" {
		t.Fatalf("merged period/time = %q / %q", merged.Period, merged.Time)
	}
	if merged.Course != longCourse || merged.Location != longLocation || merged.Weeks != longWeeks {
		t.Fatalf("long text was changed: %#v", merged)
	}
	if merged.Color != scheduleGridColorHex(scheduleGridCourseColor(grid.Items[0])) {
		t.Fatalf("color = %q, want semantic course color", merged.Color)
	}
	if len(got.Footer) != 2 || got.Footer[1] != "Life @ USTC" {
		t.Fatalf("footer = %#v", got.Footer)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"day_width", "label_width", "row_height", "header_height", "dividers"} {
		if _, ok := fields[obsolete]; ok {
			t.Fatalf("payload still contains obsolete geometry field %q", obsolete)
		}
	}
	var itemFields map[string]json.RawMessage
	if err := json.Unmarshal(encodedItem(t, got.Items[0]), &itemFields); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"large", "course_size", "meta_size"} {
		if _, ok := itemFields[obsolete]; ok {
			t.Fatalf("item payload still contains obsolete styling field %q", obsolete)
		}
	}
}

func encodedItem(t *testing.T, item remoteGridItem) []byte {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRemoteRendererGridSummaryModesAndTodayLabels(t *testing.T) {
	periods := remoteSchedulePeriods()[:2]
	location := time.FixedZone("CST", 8*60*60)
	renderer := RemoteRenderer{Now: func() time.Time {
		return time.Date(2026, 7, 17, 13, 0, 0, 0, location)
	}}

	daily := &ScheduleGrid{
		Days:    []ScheduleGridDay{{Label: "今天", Date: "07-17"}},
		Periods: periods,
	}
	dailyReq := renderer.buildGridRequest(NewScheduleGridImage("schedule", "今天", daily, "今天课表"))
	if dailyReq.Summary != "今天 · 第 1–2 节" {
		t.Fatalf("daily summary = %q", dailyReq.Summary)
	}
	if dailyReq.Days[0].Label != "今天" {
		t.Fatalf("daily label = %q, want no duplicate", dailyReq.Days[0].Label)
	}

	weeklyNoDates := &ScheduleGrid{
		Days: []ScheduleGridDay{
			{Label: "周日"}, {Label: "周一"}, {Label: "周二"}, {Label: "周三"},
			{Label: "周四"}, {Label: "周五"}, {Label: "周六"},
		},
		Periods: periods,
	}
	weeklyReq := renderer.buildGridRequest(NewScheduleGridImage("schedule", "整学期", weeklyNoDates, "整学期课表"))
	if weeklyReq.Summary != "整学期 · 第 1–2 节" {
		t.Fatalf("whole-semester summary = %q", weeklyReq.Summary)
	}
	if len(weeklyReq.Days) != 7 {
		t.Fatalf("whole-semester days = %d, want 7", len(weeklyReq.Days))
	}

	dated := &ScheduleGrid{
		Days:    []ScheduleGridDay{{Label: "周五", Date: "07-17"}},
		Periods: periods,
	}
	datedReq := renderer.buildGridRequest(NewScheduleGridImage("schedule", "本周", dated, "本周课表"))
	if datedReq.Days[0].Label != "周五 · 今天" {
		t.Fatalf("dated today label = %q", datedReq.Days[0].Label)
	}
}

func TestScheduleGridTimeRangePreservesSuppliedPeriodTimes(t *testing.T) {
	tests := []struct {
		name        string
		first, last string
		want        string
	}{
		{name: "same period range", first: "08:00–08:45", last: "08:00–08:45", want: "08:00–08:45"},
		{name: "different period ranges", first: "08:00–08:45", last: "08:50–09:35", want: "08:00–09:35"},
		{name: "plain labels", first: "第一时段", last: "第二时段", want: "第一时段–第二时段"},
		{name: "empty first", first: "", last: "09:35", want: "09:35"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduleGridTimeRange(tt.first, tt.last); got != tt.want {
				t.Fatalf("scheduleGridTimeRange(%q, %q) = %q, want %q", tt.first, tt.last, got, tt.want)
			}
		})
	}
}

func TestRemoteRendererGridPreservesWhitespaceAroundContentOnly(t *testing.T) {
	grid := &ScheduleGrid{
		Days:    []ScheduleGridDay{{Label: " 周一 ", Date: " 09-07 "}},
		Periods: []ScheduleGridPeriod{{Label: " 第 1 节 ", Time: " 08:00–08:45 "}},
		Items: []ScheduleGridItem{{
			Day: 0, StartPeriod: 1, EndPeriod: 1,
			Course: "  Long English Course Name  ", Location: "  教室  ", Weeks: "  第 1 周  ",
		}},
	}
	got := (RemoteRenderer{
		Now: func() time.Time {
			return time.Date(2026, 7, 17, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}).buildGridRequest(NewScheduleGridImage("schedule", "课表", grid, "课表"))
	if got.Days[0].Label != "周一" || got.Days[0].Date != "09-07" {
		t.Fatalf("day whitespace = %#v", got.Days[0])
	}
	if got.Periods[0].Label != "第 1 节" || got.Periods[0].Time != "08:00–08:45" {
		t.Fatalf("period whitespace = %#v", got.Periods[0])
	}
	if got.Items[0].Course != "Long English Course Name" || got.Items[0].Location != "教室" || got.Items[0].Weeks != "第 1 周" {
		t.Fatalf("item whitespace = %#v", got.Items[0])
	}
	if !strings.Contains(got.Items[0].Course, "Long English Course Name") {
		t.Fatal("course content was not preserved")
	}
}
