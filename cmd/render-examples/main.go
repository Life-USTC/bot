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
	now   func() time.Time
}

func fixtureNow() time.Time {
	return time.Date(2026, 10, 21, 15, 4, 0, 0, time.FixedZone("CST", 8*60*60))
}

// Public-data snapshots are embedded so local and CI renders are reproducible.
func fixtures() []fixture {
	return []fixture{
		{"bus-single", "校车 · 东西区往返", busSingleImage, fixtureNow},
		{"bus-all", "校车 · 工作日全部路线", busAllImage, fixtureNow},
		{"bus-weekend", "校车 · 周六全部路线", busWeekendImage, func() time.Time { return fixtureNow().AddDate(0, 0, 3) }},
		{"rich-table", "待办表格 · 合成数据", richTableImage, fixtureNow},
		{"rich-text", "帮助与长文本", richTextImage, fixtureNow},
		{"grid-week", "周课表 · 公开教学班组合", gridWeekImage, fixtureNow},
		{"grid-day", "日课表 · 与周课表相同课程", gridDayImage, fixtureNow},
		{"weather", "多城市天气 · 合成数据", weatherImage, fixtureNow},
	}
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
	renderer := responses.RemoteRenderer{Endpoint: endpoint}
	var failures []error
	var examples []example
	matched := 0
	for _, f := range fixtures() {
		if only != "" && !strings.HasPrefix(f.name, only) {
			continue
		}
		matched++
		renderer.Now = f.now
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
