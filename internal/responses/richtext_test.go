package responses

import (
	"image"
	"reflect"
	"testing"
	"time"
)

func TestParseRichTextBuildsTextAndTableBlocks(t *testing.T) {
	doc := parseRichText(`# 校车 · 东区 → 西区

工作日时刻表

| 东区 | 北区 | 西区 |
| --- | --- | --- |
| 15:30 | 15:35 | 15:40 | ✨ |`)

	if doc.Title != "校车 · 东区 → 西区" {
		t.Fatalf("title = %q", doc.Title)
	}
	if len(doc.Blocks) != 2 || !reflect.DeepEqual(doc.Blocks[0].Lines, []string{"工作日时刻表"}) {
		t.Fatalf("blocks = %#v", doc.Blocks)
	}
	table := doc.Blocks[1].Table
	if table == nil || !reflect.DeepEqual(table.Header, []string{"东区", "北区", "西区"}) {
		t.Fatalf("table = %#v", table)
	}
	if len(table.Rows) != 1 || !table.Rows[0].Highlight {
		t.Fatalf("rows = %#v", table.Rows)
	}
}

func TestLayoutRichTextKeepsNodesInsideCanvasWithoutOverlap(t *testing.T) {
	doc := parseRichText(`# 今日安排

09:50 数据库系统 · 西区 3A204
14:00 计算机网络 · 东区 5教5201

| 项目 | 时间 |
| --- | --- |
| 提交课程作业 | 18:00 |`)
	layout := layoutRichText(doc, time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	canvas := image.Rect(0, 0, layout.Space.Width, layout.Space.Height)
	for i, node := range layout.Nodes {
		if !node.Bounds.In(canvas) {
			t.Fatalf("node %d bounds %v outside %v", i, node.Bounds, canvas)
		}
		for j := i + 1; j < len(layout.Nodes); j++ {
			if node.Bounds.Overlaps(layout.Nodes[j].Bounds) {
				t.Fatalf("node %d overlaps node %d", i, j)
			}
		}
	}
}

func TestNewTextImageRemovesDuplicateHeadingFromRichText(t *testing.T) {
	img := NewTextImage("schedule", "今天课表", "今天课表：\n09:50 数据库系统")
	if img.RichText != "# 今天课表\n\n09:50 数据库系统" {
		t.Fatalf("rich text = %q", img.RichText)
	}
}

func TestRendererLayoutDependsOnRichTextNotResponseKind(t *testing.T) {
	renderer := Renderer{FontPath: testFontPath(t)}
	richText := "# 今日安排\n\n09:50 数据库系统\n14:00 计算机网络"
	first := NewRichTextImage("schedule", richText, "schedule")
	second := NewRichTextImage("dashboard", richText, "dashboard")
	_, firstWidth, firstHeight, err := renderer.RenderPNG(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondWidth, secondHeight, err := renderer.RenderPNG(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstWidth != secondWidth || firstHeight != secondHeight {
		t.Fatalf("schedule=%dx%d dashboard=%dx%d", firstWidth, firstHeight, secondWidth, secondHeight)
	}
}

func TestLayoutRichTextSizesCanvasFromContent(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	short := layoutRichText(parseRichText("# 待办\n\n买咖啡"), now)
	wide := layoutRichText(parseRichText("# 待办\n\n完成一份包含数据库实验结果和性能分析的课程报告"), now)
	tall := layoutRichText(parseRichText("# 待办\n\n买咖啡\n提交报告\n参加会议"), now)

	if short.Space.Width >= wide.Space.Width {
		t.Fatalf("short width = %d, wide width = %d", short.Space.Width, wide.Space.Width)
	}
	if short.Space.Height >= tall.Space.Height {
		t.Fatalf("short height = %d, tall height = %d", short.Space.Height, tall.Space.Height)
	}
	if wide.Space.Width > wide.Metrics.MaxWidth {
		t.Fatalf("wide width = %d, max = %d", wide.Space.Width, wide.Metrics.MaxWidth)
	}
}

func TestLayoutRichTextMeasuresTableColumns(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	narrow := layoutRichText(parseRichText("# 表格\n\n| A | B |\n| --- | --- |\n| 1 | 2 |"), now)
	wide := layoutRichText(parseRichText("# 表格\n\n| 一个非常非常长的站点名称 | 另一个同样很长的站点名称 |\n| --- | --- |\n| 14:30 | 14:45 |"), now)

	if narrow.Nodes[0].Bounds.Dx() >= wide.Nodes[0].Bounds.Dx() {
		t.Fatalf("narrow table = %d, wide table = %d", narrow.Nodes[0].Bounds.Dx(), wide.Nodes[0].Bounds.Dx())
	}
}

func TestLayoutRichTextStretchesTablesAcrossSharedGrid(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 北区 | 西区 |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |\n\n| 南区 | 东区 |\n| --- | --- |\n| 15:00 | 15:15 |"), now)
	want := layout.Space.Width - 2*layout.Metrics.MarginX
	for i, node := range layout.Nodes {
		if node.Bounds.Dx() != want {
			t.Fatalf("table %d width = %d, want %d", i, node.Bounds.Dx(), want)
		}
	}
}

func TestLayoutRichTextUsesEqualOuterMargins(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 表格\n\n| 东区始发站 | 西区中转站 | 先研院站点 | 高新区终点 |\n| --- | --- | --- | --- |\n| 14:30 | 14:40 | 14:52 | 15:05 |"), now)
	want := layout.Metrics.MarginX

	if top := layout.Metrics.TitleBaseline - 18; top != want {
		t.Fatalf("top margin = %d, want %d", top, want)
	}
	if left := layout.Nodes[0].Bounds.Min.X; left != want {
		t.Fatalf("left margin = %d, want %d", left, want)
	}
	if right := layout.Space.Width - layout.Nodes[0].Bounds.Max.X; right != want {
		t.Fatalf("right margin = %d, want %d", right, want)
	}
	if bottom := layout.Space.Height - layout.FooterY - layout.Metrics.FooterLineGap; bottom != want {
		t.Fatalf("bottom margin = %d, want %d", bottom, want)
	}
}

func TestLayoutRichTextEmphasizesEndpointNamesWithoutPrefixes(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 北区 | 西区 |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |"), now)
	headers := layout.Nodes[0].Header
	if len(headers) != 3 || headers[0].Text != "东区" || !headers[0].Emphasize || headers[1].Emphasize || headers[2].Text != "西区" || !headers[2].Emphasize {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestRichFooterOnlyShowsTimeDayTypeAndSource(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 34, 0, 0, time.FixedZone("CST", 8*60*60))
	if got, want := richFooterLines(now), [2]string{"12:34 · 工作日", "Life@USTC"}; got != want {
		t.Fatalf("footer = %#v, want %#v", got, want)
	}
}
