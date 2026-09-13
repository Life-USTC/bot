package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/responses"
)

//go:embed testdata/*.json
var productionData embed.FS

func readSnapshot(name string, target any) {
	data, err := productionData.ReadFile("testdata/" + name)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		panic(fmt.Errorf("decode %s: %w", name, err))
	}
}

type busRouteSnapshot struct {
	ID    int        `json:"id"`
	Stops []string   `json:"stops"`
	Times [][]string `json:"times"`
}

func busSnapshotImage(title, service string, routeIDs ...int) *responses.Image {
	var snapshot map[string][]busRouteSnapshot
	readSnapshot("bus.json", &snapshot)
	var routes []busRouteSnapshot
	for _, route := range snapshot[service] {
		if len(routeIDs) == 0 {
			routes = append(routes, route)
			continue
		}
		for _, id := range routeIDs {
			if route.ID == id {
				routes = append(routes, route)
			}
		}
	}
	// The production image response retains all trips and marks one next
	// departure. Unknown intermediate times remain empty, never interpolated.
	next := ""
	for _, route := range routes {
		for _, row := range route.Times {
			if row[0] >= fixtureNow().Format("15:04") && (next == "" || row[0] < next) {
				next = row[0]
			}
		}
	}
	var lines []string
	for _, route := range routes {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "| "+strings.Join(route.Stops, " | ")+" |")
		lines = append(lines, "|"+strings.Repeat(" --- |", len(route.Stops)))
		for _, row := range route.Times {
			line := "| " + strings.Join(row, " | ") + " |"
			if row[0] == next {
				line += " ✨ |"
				next = ""
			}
			lines = append(lines, line)
		}
	}
	return responses.NewRichTextImage("bus", "# "+title+"\n\n"+strings.Join(lines, "\n"), title)
}

func busSingleImage() *responses.Image {
	return busSnapshotImage("校车 东区 ⇄ 西区", "weekday_routes", 1, 2, 7, 8)
}

func busAllImage() *responses.Image {
	return busSnapshotImage("校车", "weekday_routes")
}

func busWeekendImage() *responses.Image {
	return busSnapshotImage("校车 · 周六", "saturday_routes")
}

type courseSnapshot struct {
	ID       int    `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Campus   string `json:"campus"`
	Sessions []struct {
		Day   int    `json:"day"`
		Start int    `json:"start"`
		End   int    `json:"end"`
		Room  string `json:"room"`
		Weeks string `json:"weeks"`
	} `json:"sessions"`
}

// These public sections are assembled into a sample, not a student's schedule.
// Week 8 is within every selected session's published weeks (including the
// even-week programming lecture and the lab that starts in week 4).
func gridWeekImage() *responses.Image {
	var courses []courseSnapshot
	readSnapshot("courses.json", &courses)
	labels := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	days := make([]responses.ScheduleGridDay, 7)
	for i := range days {
		date := fixtureNow().AddDate(0, 0, i-int(fixtureNow().Weekday()))
		days[i] = responses.ScheduleGridDay{Label: labels[i], Date: date.Format("01-02")}
	}
	var items []responses.ScheduleGridItem
	for _, course := range courses {
		for _, session := range course.Sessions {
			items = append(items, responses.ScheduleGridItem{
				Day: session.Day, StartPeriod: session.Start, EndPeriod: session.End,
				Course: course.Name, Location: course.Campus + " · " + session.Room, Weeks: session.Weeks,
			})
		}
	}
	return responses.NewScheduleGridImage("schedule", "本周课表", &responses.ScheduleGrid{
		Semester: "2026 秋季学期", Week: "第 8 周", DateRange: "10/18-10/24",
		Days: days, Periods: schedulePeriods(), Items: items,
	}, "公开教学班组合示例")
}

func gridRoleImage() *responses.Image {
	base := gridWeekImage()
	grid := *base.Grid
	grid.Items = append([]responses.ScheduleGridItem(nil), base.Grid.Items...)
	if len(grid.Items) >= 1 {
		grid.Items[0].Kind = "teaching_assistant"
	}
	if len(grid.Items) >= 2 {
		grid.Items[1].Kind = "auditor"
	}
	return responses.NewScheduleGridImage("schedule", "本周课表 · 身份 Badge", &grid, "助教与旁听身份 Badge 示例")
}

func gridDayImage() *responses.Image {
	week := gridWeekImage().Grid
	day := int(fixtureNow().Weekday())
	var items []responses.ScheduleGridItem
	for _, item := range week.Items {
		if item.Day == day {
			item.Day = 0
			items = append(items, item)
		}
	}
	return responses.NewScheduleGridImage("schedule", fixtureNow().Format("01-02")+" 课表", &responses.ScheduleGrid{
		Days: []responses.ScheduleGridDay{week.Days[day]}, Periods: week.Periods, Items: items,
	}, "公开教学班组合示例 · 周三")
}

// USTC's official 2026 autumn timetable, also used by the production command.
func schedulePeriods() []responses.ScheduleGridPeriod {
	times := []string{
		"07:50–08:35", "08:40–09:25", "09:45–10:30", "10:35–11:20", "11:25–12:10",
		"14:00–14:45", "14:50–15:35", "15:55–16:40", "16:45–17:30", "17:35–18:20",
		"19:30–20:15", "20:20–21:05", "21:10–21:55",
	}
	periods := make([]responses.ScheduleGridPeriod, len(times))
	for i, lessonTime := range times {
		periods[i] = responses.ScheduleGridPeriod{Label: fmt.Sprintf("第 %d 节", i+1), Time: lessonTime}
	}
	return periods
}
