package responses

import (
	"bytes"
	"image/png"
	"strconv"
	"testing"
	"time"
)

func TestScheduleGridItemBoundsMergePeriods(t *testing.T) {
	grid := testScheduleGrid()
	metrics := defaultScheduleGridMetrics(len(grid.Days), len(grid.Periods))
	rect, ok := scheduleGridItemBounds(grid.Items[0], metrics)
	if !ok {
		t.Fatal("scheduleGridItemBounds returned false")
	}
	if got, want := rect.Dy(), metrics.RowHeight*2; got != want {
		t.Fatalf("merged height = %d, want %d", got, want)
	}
	if got, want := rect.Dx(), metrics.DayWidth; got != want {
		t.Fatalf("cell width = %d, want %d", got, want)
	}
}

func TestScheduleGridUsesStrongDayPartDividers(t *testing.T) {
	grid := testScheduleGrid()
	metrics := defaultScheduleGridMetrics(len(grid.Days), len(grid.Periods))
	for _, boundary := range []int{5, 10} {
		rect, ok := scheduleGridDividerBounds(boundary, metrics)
		if !ok {
			t.Fatalf("boundary %d was rejected", boundary)
		}
		if rect.Dy() < 4 || rect.Dx() != metrics.gridWidth() {
			t.Fatalf("boundary %d bounds = %v", boundary, rect)
		}
	}
}

func TestScheduleGridFindsTodayColumn(t *testing.T) {
	grid := testScheduleGrid()
	location := time.FixedZone("CST", 8*60*60)
	if got := scheduleGridTodayIndex(grid, time.Date(2026, 7, 17, 12, 0, 0, 0, location)); got != 5 {
		t.Fatalf("today index = %d, want 5", got)
	}
	if got := scheduleGridTodayIndex(grid, time.Date(2026, 7, 20, 12, 0, 0, 0, location)); got != -1 {
		t.Fatalf("outside week today index = %d, want -1", got)
	}
}

func TestRendererCreatesScheduleGridPNG(t *testing.T) {
	grid := testScheduleGrid()
	image := NewScheduleGridImage("schedule", "07-12 至 07-18 课表", grid, "本周课表")
	if image == nil || image.Grid == nil {
		t.Fatalf("image = %#v", image)
	}
	data, width, height, err := (Renderer{FontPath: testFontPath(t)}).RenderPNG(image)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || width <= 0 || height <= 0 || width <= height {
		t.Fatalf("len=%d size=%dx%d", len(data), width, height)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Fatalf("bounds = %v size=%dx%d", decoded.Bounds(), width, height)
	}
}

func TestFitScheduleGridText(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxWidth int
		fontSize int
		want     string
	}{
		{name: "fits unchanged", value: "数据库系统", maxWidth: 144, fontSize: 13, want: "数据库系统"},
		{name: "normalizes whitespace", value: "  数据库   系统  ", maxWidth: 144, fontSize: 13, want: "数据库 系统"},
		{name: "latin cut at word boundary", value: "Computer Networks Laboratory", maxWidth: 200, fontSize: 13, want: "Computer Networks…"},
		{name: "latin long name keeps whole words", value: "Introduction to Computational Thinking and Programming Methodology", maxWidth: 144, fontSize: 13, want: "Introduction to…"},
		{name: "latin single word falls back to rune cut", value: "Supercalifragilisticexpialidocious", maxWidth: 80, fontSize: 13, want: "Supercal…"},
		{name: "cjk trailing parenthetical stripped when head fits", value: "中国近现代史纲要（上）", maxWidth: 110, fontSize: 13, want: "中国近现代史纲要"},
		{name: "ascii trailing parenthetical stripped when head fits", value: "Data Structures (Honors)", maxWidth: 130, fontSize: 13, want: "Data Structures"},
		{name: "cut backs out of unclosed bracket", value: "物理（上）电磁学与光学", maxWidth: 47, fontSize: 13, want: "物理…"},
		{name: "no dangling merged-slot separator", value: "中国近现代史纲要 / 军事理论", maxWidth: 120, fontSize: 13, want: "中国近现代史纲要…"},
		{name: "location not clipped mid-word", value: "西区 3A204", maxWidth: 55, fontSize: 13, want: "西区…"},
		{name: "nothing fits", value: "数据库系统", maxWidth: 5, fontSize: 13, want: "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitScheduleGridText(tt.value, tt.maxWidth, tt.fontSize)
			if got != tt.want {
				t.Fatalf("fitScheduleGridText(%q, %d, %d) = %q, want %q", tt.value, tt.maxWidth, tt.fontSize, got, tt.want)
			}
			if got != "…" {
				if width := richTextWidth(got, tt.fontSize); width > tt.maxWidth {
					t.Fatalf("result width %d exceeds maxWidth %d", width, tt.maxWidth)
				}
			}
		})
	}
}

func testScheduleGrid() *ScheduleGrid {
	days := []ScheduleGridDay{
		{Label: "周日", Date: "07-12"},
		{Label: "周一", Date: "07-13"},
		{Label: "周二", Date: "07-14"},
		{Label: "周三", Date: "07-15"},
		{Label: "周四", Date: "07-16"},
		{Label: "周五", Date: "07-17"},
		{Label: "周六", Date: "07-18"},
	}
	periods := make([]ScheduleGridPeriod, 13)
	for i := range periods {
		periods[i] = ScheduleGridPeriod{Label: "第 " + strconv.Itoa(i+1) + " 节", Time: "09:00–09:45"}
	}
	return &ScheduleGrid{
		Days:    days,
		Periods: periods,
		Items: []ScheduleGridItem{
			{Day: 0, StartPeriod: 3, EndPeriod: 4, Course: "数据库系统", Location: "高新区 · GT-B112"},
			{Day: 1, StartPeriod: 6, EndPeriod: 7, Course: "Computer Networks", Location: "西区 · 3A204"},
		},
	}
}
