// Command render-parity renders a fixed set of fixtures with both the legacy
// Go renderer (responses.Renderer) and the typst sidecar renderer
// (responses.RemoteRenderer against renderd), writing side-by-side PNGs and a
// one-line size report per fixture. It is the acceptance tool for the sidecar
// template-replication tasks.
//
// Usage:
//
//	go run ./cmd/render-parity -endpoint http://127.0.0.1:9123/render -out parity/
//	go run ./cmd/render-parity -only bus -out parity/
//
// Legacy renders at 2x, the sidecar at 3x by default, so remote width divided
// by legacy width should be ~= 1.5; a deviation over 2% prints a WARN.
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

// legacyScale and remoteScale are the rasterization factors used by the two
// renderers; the expected remote/legacy size ratio is their quotient.
const (
	legacyScale = 2.0
	remoteScale = 3.0
)

type fixture struct {
	name  string
	build func() *responses.Image
}

func fixtureNow() time.Time {
	return time.Date(2026, 9, 2, 15, 4, 0, 0, time.FixedZone("CST", 8*60*60))
}

// fixtures mirrors the sample data used by internal/responses tests; test
// helpers live in _test.go files so the literals are copied here.
func fixtures() []fixture {
	return []fixture{
		{"bus-single", busSingleImage},
		{"bus-all", busAllImage},
		{"rich-table", richTableImage},
		{"rich-text", richTextImage},
		{"grid-week", gridWeekImage},
		{"grid-day", gridDayImage},
		{"weather", weatherImage},
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

// gridWeekImage is a 7-day × 12-period schedule with merged multi-period
// blocks, today's column highlighted at the fixture date, and an overlong
// course name that triggers truncation.
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
		Days:    days,
		Periods: schedulePeriods(),
		Items: []responses.ScheduleGridItem{
			{Day: 1, StartPeriod: 3, EndPeriod: 4, Course: "数据库系统", Location: "西区 · 3A204", Weeks: "2-16 周"},
			{Day: 2, StartPeriod: 6, EndPeriod: 7, Course: "Computer Networks", Location: "西区 · 3A204"},
			{Day: 3, StartPeriod: 1, EndPeriod: 2, Course: "Introduction to Computational Thinking and Programming Methodology", Location: "东区 · 5教5201"},
			{Day: 4, StartPeriod: 9, EndPeriod: 10, Course: "线性代数", Location: "东区 · 2教2210"},
			{Day: todayIndex, StartPeriod: 11, EndPeriod: 12, Course: "体育（太极拳）", Location: "中区 · 体育馆"},
		},
	}, "本周课表")
}

// gridDayImage is a single-day schedule (DayWidth=360 layout branch).
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

func schedulePeriods() []responses.ScheduleGridPeriod {
	periods := make([]responses.ScheduleGridPeriod, 12)
	for i := range periods {
		periods[i] = responses.ScheduleGridPeriod{Label: "第 " + strconv.Itoa(i+1) + " 节", Time: "09:00–09:45"}
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
	out := flag.String("out", "parity", "output directory for side-by-side PNGs")
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
	legacy := responses.Renderer{Now: fixtureNow}
	remote := responses.RemoteRenderer{Endpoint: endpoint, Now: fixtureNow}
	expectedRatio := remoteScale / legacyScale
	var failures []error
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

		report := f.name
		legacyW, legacyH := 0, 0
		png, w, h, err := legacy.RenderPNG(img)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s legacy: %w", f.name, err))
		} else {
			legacyW, legacyH = w, h
			failures = append(failures, writePNG(out, f.name+"-legacy.png", png))
			report += fmt.Sprintf(" legacy=%dx%d", w, h)
		}

		png, w, h, err = remote.RenderPNG(img)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s remote: %w", f.name, err))
		} else {
			failures = append(failures, writePNG(out, f.name+"-remote.png", png))
			report += fmt.Sprintf(" remote=%dx%d", w, h)
			if legacyW > 0 && legacyH > 0 {
				ratio := float64(w) / float64(legacyW)
				if math.Abs(ratio-expectedRatio)/expectedRatio > 0.02 {
					report += fmt.Sprintf(" WARN ratio=%.3f want≈%.1f", ratio, expectedRatio)
				}
			}
		}
		fmt.Println(report)
	}
	if matched == 0 {
		return fmt.Errorf("no fixtures match %q", only)
	}
	return errors.Join(failures...)
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
