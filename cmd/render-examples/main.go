// Command render-examples renders all card fixtures through Typst.
// It checks content-sized dimensions, writes PNGs, and builds a gallery that
// shows each card at its natural height.
//
// Usage:
//
//	go run ./cmd/render-examples -endpoint http://127.0.0.1:9123/render -out examples/
package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/responses"
)

// The fixtures range from an intrinsic bus table to a full seven-day grid.
// These bounds include their paper margins at the default 3x raster scale.
const (
	exampleMinWidth  = 268 * 3
	exampleMaxWidth  = 864 * 3
	exampleMinHeight = 120 * 3
)

type fixture struct {
	name  string
	title string
	build func() *responses.Image
}

func fixtureNow() time.Time {
	return time.Date(2026, 9, 2, 15, 4, 0, 0, time.FixedZone("CST", 8*60*60))
}

// fixtures mirrors the sample data used by internal/responses tests; test
// helpers live in _test.go files so the literals are copied here.
func fixtures() []fixture {
	return []fixture{
		{"bus-single", "校车 · 单条路线", busSingleImage},
		{"bus-all", "校车 · 全部路线", busAllImage},
		{"rich-table", "待办表格", richTableImage},
		{"rich-text", "帮助与长文本", richTextImage},
		{"grid-week", "本周课表", gridWeekImage},
		{"grid-day", "今日课表", gridDayImage},
		{"weather", "多城市天气", weatherImage},
	}
}

// busSingleImage mirrors testBusImage in internal/responses/render_test.go.
func busSingleImage() *responses.Image {
	return responses.NewTextImage("bus", "校车 东区 → 西区", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
	}, "\n"))
}

// busAllImage mirrors testBusAllImage in internal/responses/render_test.go.
func busAllImage() *responses.Image {
	return responses.NewTextImage("bus", "校车", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
		"",
		"西区\t北区\t东区",
		"15:40\t15:45\t16:00",
		"16:10\t16:15\t16:30",
	}, "\n"))
}

// richTableImage is a non-bus rich card: todo kind with a section heading, a
// table, and a ✨-highlighted row.
func richTableImage() *responses.Image {
	return responses.NewRichTextImage("todo", `# 待办

## 本周待办
| 任务 | 截止 |
| --- | --- |
| 提交数据库实验报告 | 07-10 |
| 写完文献综述初稿 | 07-12 | ✨ |
| 还图书馆的书 | 07-15 |`, "待办：提交数据库实验报告等 3 项")
}

// richTextImage is a pure-text rich card (help style): multiple lines plus a
// long line that triggers wrapping.
func richTextImage() *responses.Image {
	return responses.NewRichTextImage("help", `# Bot 帮助

发送「帮助 课表」可以查看「课表」命令的具体用法。
直接发送「课表」即可查看今天的课程安排，发送「校车」查看校车时刻表。
`+strings.Repeat("这是一段用于触发自动换行的较长的说明文字，", 8), "Bot 帮助")
}

// gridWeekImage exercises seven day sections, full English course names,
// multiple classes on one day, current-day emphasis, and empty days.
func gridWeekImage() *responses.Image {
	now := fixtureNow()
	todayIndex := int(now.Weekday()) // index 0 is 周日, matching day labels below
	labels := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	days := make([]responses.ScheduleGridDay, 7)
	for i := range days {
		date := now.AddDate(0, 0, i-todayIndex)
		days[i] = responses.ScheduleGridDay{Label: labels[i], Date: date.Format("01-02")}
	}
	return responses.NewScheduleGridImage("schedule", "本周课表", &responses.ScheduleGrid{
		Semester:  "2026 秋季学期",
		Week:      "第 1 周",
		DateRange: "08/30-09/05",
		Days:      days,
		Periods:   schedulePeriods(),
		Items: []responses.ScheduleGridItem{
			{Day: 1, StartPeriod: 3, EndPeriod: 4, Course: "数据库系统", Location: "西区 · 3A204", Weeks: "2-16 周"},
			{Day: 2, StartPeriod: 6, EndPeriod: 7, Course: "Computer Networks", Location: "西区 · 3A204"},
			{Day: 3, StartPeriod: 1, EndPeriod: 2, Course: "Introduction to Computational Thinking and Programming Methodology", Location: "东区 · 5教5201"},
			{Day: 4, StartPeriod: 9, EndPeriod: 10, Course: "线性代数", Location: "东区 · 2教2210"},
			{Day: todayIndex, StartPeriod: 11, EndPeriod: 12, Course: "体育（太极拳）", Location: "中区 · 体育馆"},
		},
	}, "本周课表")
}

// gridDayImage exercises a compact single-day agenda.
func gridDayImage() *responses.Image {
	now := fixtureNow()
	return responses.NewScheduleGridImage("schedule", now.Format("01-02")+" 课表", &responses.ScheduleGrid{
		Days: []responses.ScheduleGridDay{
			{Label: "今天", Date: now.Format("01-02")},
		},
		Periods: schedulePeriods(),
		Items: []responses.ScheduleGridItem{
			{Day: 0, StartPeriod: 3, EndPeriod: 4, Course: "数据库系统", Location: "西区 · 3A204", Weeks: "2-16 周"},
			{Day: 0, StartPeriod: 8, EndPeriod: 9, Course: "计算机网络实验", Location: "西区 · 电三楼 314"},
		},
	}, "今日课表")
}

// Distinct example lesson times, not a live university timetable.
func schedulePeriods() []responses.ScheduleGridPeriod {
	times := []string{
		"08:00–08:45", "08:50–09:35", "09:55–10:40", "10:45–11:30",
		"11:35–12:20", "14:00–14:45", "14:50–15:35", "15:55–16:40",
		"16:45–17:30", "17:35–18:20", "19:30–20:15", "20:20–21:05",
	}
	periods := make([]responses.ScheduleGridPeriod, len(times))
	for i, lessonTime := range times {
		periods[i] = responses.ScheduleGridPeriod{Label: "第 " + strconv.Itoa(i+1) + " 节", Time: lessonTime}
	}
	return periods
}

// weatherImage is a two-location weather card: the first location has 24
// hourly points, 5 daily entries and one alert; the second exercises the
// no-precipitation-data branch (all zero probabilities, no alerts).
func weatherImage() *responses.Image {
	hourly := make([]responses.WeatherCardHourPoint, 24)
	for i := range hourly {
		temperature := 26.0 + 6*math.Sin(float64(i-9)/24*2*math.Pi)
		probability := 0.0
		if i >= 14 && i <= 19 {
			probability = 60.0
		}
		hourly[i] = responses.WeatherCardHourPoint{
			Label:                    fmt.Sprintf("%02d:00", (15+i)%24),
			Temperature:              math.Round(temperature*10) / 10,
			PrecipitationProbability: probability,
		}
	}
	dryHourly := make([]responses.WeatherCardHourPoint, 24)
	for i := range dryHourly {
		dryHourly[i] = responses.WeatherCardHourPoint{
			Label:       fmt.Sprintf("%02d:00", (15+i)%24),
			Temperature: math.Round((18.0+5*math.Sin(float64(i-9)/24*2*math.Pi))*10) / 10,
		}
	}
	card := &responses.WeatherCard{
		Meta: "更新于 15:04 · 数据来源：和风天气",
		Locations: []responses.WeatherCardLocation{
			{
				Name: "合肥",
				Current: responses.WeatherCardCurrent{
					Temperature:   32.4,
					ConditionText: "晴",
					Icon:          "sun",
					High:          35,
					Low:           25,
					HasRange:      true,
					HumidityText:  "62%",
					WindText:      "东南风 3 级",
				},
				Hourly: hourly,
				Daily: []responses.WeatherCardDayPoint{
					{Label: "今天", Low: 25, High: 35, ConditionText: "晴"},
					{Label: "周四", Low: 24, High: 33, ConditionText: "多云"},
					{Label: "周五", Low: 23, High: 31, ConditionText: "雷阵雨"},
					{Label: "周六", Low: 22, High: 30, ConditionText: "小雨"},
					{Label: "周日", Low: 23, High: 32, ConditionText: "多云"},
				},
				Alerts: []string{"高温黄色预警"},
			},
			{
				Name: "北京",
				Current: responses.WeatherCardCurrent{
					Temperature:   21.8,
					ConditionText: "多云",
					Icon:          "cloud-sun",
					High:          26,
					Low:           16,
					HasRange:      true,
					HumidityText:  "38%",
					WindText:      "北风 2 级",
				},
				Hourly: dryHourly,
				Daily: []responses.WeatherCardDayPoint{
					{Label: "今天", Low: 16, High: 26, ConditionText: "多云"},
					{Label: "周四", Low: 15, High: 25, ConditionText: "晴"},
					{Label: "周五", Low: 16, High: 27, ConditionText: "晴"},
					{Label: "周六", Low: 17, High: 28, ConditionText: "多云"},
					{Label: "周日", Low: 16, High: 26, ConditionText: "阴"},
				},
			},
		},
	}
	return responses.NewWeatherCardImage(card, "合肥：晴 32°（25~35°）\n北京：多云 22°（16~26°）")
}

func main() {
	endpoint := flag.String("endpoint", envOr("BOT_RENDER_ENDPOINT", "http://127.0.0.1:9123/render"), "renderd endpoint URL")
	out := flag.String("out", "examples", "output directory for PNGs and a comparison gallery")
	only := flag.String("only", "", "only run fixtures whose name has this prefix (e.g. -only bus)")
	flag.Parse()

	if err := run(*endpoint, *out, *only); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(endpoint, out, only string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	renderer := responses.RemoteRenderer{Endpoint: endpoint, Now: fixtureNow}
	var failures []error
	var examples []example
	matched := 0
	for _, f := range fixtures() {
		if only != "" && !strings.HasPrefix(f.name, only) {
			continue
		}
		matched++
		img := f.build()
		if img == nil {
			failures = append(failures, fmt.Errorf("%s fixture build failed", f.name))
			continue
		}
		png, width, height, err := renderer.RenderPNG(img)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", f.name, err))
			continue
		}
		if width < exampleMinWidth || width > exampleMaxWidth {
			failures = append(failures, fmt.Errorf("%s: image width %d, want between %d and %d (at default 3x scale)", f.name, width, exampleMinWidth, exampleMaxWidth))
			continue
		}
		if height < exampleMinHeight {
			failures = append(failures, fmt.Errorf("%s: image height %d, want at least %d (a card is never this short)", f.name, height, exampleMinHeight))
			continue
		}
		filename := f.name + ".png"
		if err := writePNG(out, filename, png); err != nil {
			failures = append(failures, err)
			continue
		}
		examples = append(examples, example{Name: f.name, Title: f.title, File: filename, Width: width, Height: height})
		fmt.Printf("%s: %dx%d (%d bytes)\n", f.name, width, height, len(png))
	}
	if matched == 0 {
		return fmt.Errorf("no fixtures match %q", only)
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	return writeGallery(out, examples)
}

func writePNG(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
