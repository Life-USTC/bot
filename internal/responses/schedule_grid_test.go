package responses

import (
	"bytes"
	"fmt"
	"image/color"
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

func TestScheduleGridItemTextSizeUsesCourseBlockHeight(t *testing.T) {
	metrics := defaultScheduleGridMetrics(7, 13)
	onePeriod, ok := scheduleGridItemBounds(ScheduleGridItem{Day: 1, StartPeriod: 3, EndPeriod: 3}, metrics)
	if !ok || scheduleGridItemUsesLargeText(onePeriod, metrics) {
		t.Fatalf("one-period block uses large text: rect=%v ok=%v", onePeriod, ok)
	}
	twoPeriods, ok := scheduleGridItemBounds(ScheduleGridItem{Day: 1, StartPeriod: 3, EndPeriod: 4}, metrics)
	if !ok || !scheduleGridItemUsesLargeText(twoPeriods, metrics) {
		t.Fatalf("two-period block does not use large text: rect=%v ok=%v", twoPeriods, ok)
	}
	if scheduleGridCourseFontSize <= 13 || scheduleGridMetaFontSize <= richMetaFontSize ||
		scheduleGridLargeCourseFontSize <= scheduleGridCourseFontSize || scheduleGridLargeMetaFontSize <= scheduleGridMetaFontSize {
		t.Fatal("large item font sizes must exceed compact font sizes")
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

func TestScheduleGridCourseColorUsesStableNormalizedKey(t *testing.T) {
	base := scheduleGridCourseColor(ScheduleGridItem{
		Day:         0,
		StartPeriod: 1,
		EndPeriod:   2,
		Course:      "Computer Networks",
	})
	tests := []struct {
		name string
		item ScheduleGridItem
	}{
		{
			name: "different date and periods",
			item: ScheduleGridItem{Day: 6, StartPeriod: 11, EndPeriod: 13, Course: "Computer Networks"},
		},
		{
			name: "normalized whitespace and case",
			item: ScheduleGridItem{Day: 3, StartPeriod: 4, EndPeriod: 5, Course: "  computer\t networks  "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduleGridCourseColor(tt.item); got != base {
				t.Fatalf("course color = %v, want %v", got, base)
			}
		})
	}
}

func TestScheduleGridCourseColorPrefersSectionKey(t *testing.T) {
	want := scheduleGridCourseColor(ScheduleGridItem{SectionKey: " section-42 ", Course: "数据库系统"})
	got := scheduleGridCourseColor(ScheduleGridItem{SectionKey: "SECTION-42", Course: "Database Systems"})
	if got != want {
		t.Fatalf("same section key colors differ: got %v, want %v", got, want)
	}
}

func TestGeneratedSectionColorsAvoidSmallPaletteCollisions(t *testing.T) {
	colors := make(map[color.RGBA]struct{})
	for i := 0; i < 20; i++ {
		colors[generateSectionColor(fmt.Sprintf("section-%d", i))] = struct{}{}
	}
	if len(colors) < 18 {
		t.Fatalf("generated only %d distinct colors for 20 sections", len(colors))
	}
}

func TestGeneratedSectionColorHasNeutralFallback(t *testing.T) {
	if got, want := generateSectionColor(""), (color.RGBA{226, 232, 240, 255}); got != want {
		t.Fatalf("empty section color = %v, want %v", got, want)
	}
}

func TestScheduleGridCourseColorsDoNotDependOnItemOrder(t *testing.T) {
	items := []ScheduleGridItem{
		{Course: "数据库系统"},
		{Course: "Computer Networks"},
		{Course: "线性代数"},
	}
	want := make(map[string]color.RGBA, len(items))
	for _, item := range items {
		want[item.Course] = scheduleGridCourseColor(item)
	}

	reordered := []ScheduleGridItem{items[2], items[0], items[1]}
	for _, item := range reordered {
		if got := scheduleGridCourseColor(item); got != want[item.Course] {
			t.Fatalf("course %q color after reorder = %v, want %v", item.Course, got, want[item.Course])
		}
	}
}

func TestRendererCreatesScheduleGridPNG(t *testing.T) {
	grid := testScheduleGrid()
	grid.Items[0].Weeks = "2-16 周"
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
