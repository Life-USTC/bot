package responses

import (
	"bytes"
	"image/png"
	"strconv"
	"testing"
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
