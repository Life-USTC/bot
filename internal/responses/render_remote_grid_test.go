package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteRendererGridPayload(t *testing.T) {
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

	grid := testScheduleGrid()
	grid.Items = append(grid.Items,
		ScheduleGridItem{
			Day:         2,
			StartPeriod: 1,
			EndPeriod:   2,
			Course:      "Introduction to Computational Thinking and Programming Methodology",
			Location:    "东区",
		},
		ScheduleGridItem{Day: -1, StartPeriod: 1, EndPeriod: 1, Course: "invalid day"},
		ScheduleGridItem{Day: 1, StartPeriod: 0, EndPeriod: 1, Course: "invalid period"},
	)
	img := NewScheduleGridImage("schedule", "  ", grid, "本周课表")
	if img == nil {
		t.Fatal("NewScheduleGridImage returned nil")
	}
	renderer := RemoteRenderer{
		Endpoint: server.URL + "/render",
		Now: func() time.Time {
			return time.Date(2026, 7, 17, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
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
	if got.Title != "课表" {
		t.Fatalf("title = %q, want fallback title", got.Title)
	}
	if got.Summary != "周日–周六 · 第 1–13 节" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if got.DayWidth != 156 || got.LabelWidth != 120 || got.RowHeight != 56 || got.HeaderHeight != 54 {
		t.Fatalf("metrics = %d/%d/%d/%d, want 156/120/56/54", got.DayWidth, got.LabelWidth, got.RowHeight, got.HeaderHeight)
	}
	if len(got.Days) != 7 || !got.Days[5].Today || !strings.HasSuffix(got.Days[5].Label, " · 今天") {
		t.Fatalf("today day = %#v", got.Days[5])
	}
	if len(got.Dividers) != 2 || got.Dividers[0] != 5 || got.Dividers[1] != 10 {
		t.Fatalf("dividers = %#v, want [5 10]", got.Dividers)
	}
	if len(got.Items) != 3 {
		t.Fatalf("items = %d, want 3 valid items", len(got.Items))
	}
	merged := got.Items[0]
	if merged.Day != 0 || merged.Start != 3 || merged.End != 4 || !merged.Large {
		t.Fatalf("merged item = %#v", merged)
	}
	if merged.Course != "数据库系统" || merged.Location != "高新区 · GT-B112" {
		t.Fatalf("merged text = %#v", merged)
	}
	if merged.CourseSize != scheduleGridLargeCourseFontSize || merged.MetaSize != scheduleGridLargeMetaFontSize {
		t.Fatalf("merged sizes = %d/%d, want %d/%d", merged.CourseSize, merged.MetaSize, scheduleGridLargeCourseFontSize, scheduleGridLargeMetaFontSize)
	}
	if merged.Color != scheduleGridColorHex(scheduleGridCourseColor(grid.Items[0])) {
		t.Fatalf("merged color = %q", merged.Color)
	}
	long := got.Items[2]
	if long.CourseSize != scheduleGridCourseFontSize || long.MetaSize != scheduleGridLargeMetaFontSize {
		t.Fatalf("long item sizes = %d/%d, want %d/%d", long.CourseSize, long.MetaSize, scheduleGridCourseFontSize, scheduleGridLargeMetaFontSize)
	}
	if !strings.HasSuffix(long.Course, "…") || richTextWidth(long.Course, long.CourseSize) > 144 {
		t.Fatalf("long course = %q, want truncated to <= 144px", long.Course)
	}
	if len(got.Footer) != 2 || got.Footer[1] != "Life @ USTC" {
		t.Fatalf("footer = %#v", got.Footer)
	}
}
