package responses

import (
	"image"
	"reflect"
	"strings"
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

func TestParseRichTextBuildsSectionBlocks(t *testing.T) {
	doc := parseRichText(`# 今日安排

## 今日课表
09:50 数据库系统

## 待办
18:00 写报告`)

	if len(doc.Blocks) != 2 {
		t.Fatalf("blocks = %#v", doc.Blocks)
	}
	if doc.Blocks[0].Heading != "今日课表" || !reflect.DeepEqual(doc.Blocks[0].Lines, []string{"09:50 数据库系统"}) {
		t.Fatalf("first block = %#v", doc.Blocks[0])
	}
	if doc.Blocks[1].Heading != "待办" || !reflect.DeepEqual(doc.Blocks[1].Lines, []string{"18:00 写报告"}) {
		t.Fatalf("second block = %#v", doc.Blocks[1])
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

func TestLayoutRichTextCompactsHelpIntroBelowTitle(t *testing.T) {
	doc := parseRichText(`# Bot 帮助
发送「帮助 课表」可以查看「课表」命令的具体用法。

## 常用
| 命令 | 说明 |
| --- | --- |
| 日程 | 查看日程 |`)
	layout := layoutRichText(doc, time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if len(layout.Nodes) < 2 {
		t.Fatalf("layout nodes = %#v", layout.Nodes)
	}
	intro := layout.Nodes[0]
	if intro.Bounds.Min.Y != 58 || intro.Bounds.Dy() != 24 || intro.RowHeight != 24 || !intro.Compact ||
		!reflect.DeepEqual(intro.Lines, []string{
			"发送「帮助 课表」可以查看「课表」命令的具体用法。",
		}) {
		t.Fatalf("intro node = %#v", intro)
	}
	if gap := layout.Nodes[1].Bounds.Min.Y - intro.Bounds.Max.Y; gap != layout.Metrics.BlockGap {
		t.Fatalf("intro-to-table gap = %d, want %d", gap, layout.Metrics.BlockGap)
	}
	if got, want := richTitleX(layout), intro.Bounds.Min.X+layout.Metrics.TextPaddingX; got != want {
		t.Fatalf("title x = %d, content x = %d", got, want)
	}
	regular := layoutRichText(parseRichText("# 待办\n\n买咖啡"), time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if got := richTitleX(regular); got != regular.Metrics.MarginX {
		t.Fatalf("regular title x = %d, want margin %d", got, regular.Metrics.MarginX)
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
	shortDoc := parseRichText("# 待办\n\n买咖啡")
	wideDoc := parseRichText("# 待办\n\n完成一份包含数据库实验结果和性能分析的课程报告以及相关的文献综述与实验数据整理工作")
	short := layoutRichText(shortDoc, now)
	wide := layoutRichText(wideDoc, now)
	tall := layoutRichText(parseRichText("# 待办\n\n买咖啡\n提交报告\n参加会议"), now)

	if short.Space.Width >= wide.Space.Width {
		t.Fatalf("short width = %d, wide width = %d", short.Space.Width, wide.Space.Width)
	}
	if short.Space.Height >= tall.Space.Height {
		t.Fatalf("short height = %d, tall height = %d", short.Space.Height, tall.Space.Height)
	}
	if got, want := short.Space.Width, short.Metrics.MinContentWidth+2*short.Metrics.MarginX; got != want {
		t.Fatalf("short width = %d, want min content width + margins = %d", got, want)
	}
	if got, want := wide.Space.Width, measureRichDocument(wideDoc, wide.Metrics)+2*wide.Metrics.MarginX; got != want {
		t.Fatalf("wide width = %d, measured width = %d", got, want)
	}
}

func TestRichTextAndTableRowsShareGridMetrics(t *testing.T) {
	metrics := defaultRichRenderMetrics()
	if metrics.TextPaddingX != metrics.TableCellPaddingX {
		t.Fatalf("text padding = %d, table padding = %d", metrics.TextPaddingX, metrics.TableCellPaddingX)
	}
	if metrics.TextRowHeight != metrics.TableRowHeight {
		t.Fatalf("text row = %d, table row = %d", metrics.TextRowHeight, metrics.TableRowHeight)
	}
	if metrics.BlockGap != metrics.TableRowGap {
		t.Fatalf("block gap = %d, table gap = %d", metrics.BlockGap, metrics.TableRowGap)
	}
}

func TestLayoutRichTextUsesTableHeaderHeightForSections(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 今日安排\n\n## 今日课表\n09:50 数据库系统\n14:00 计算机网络"), now)
	node := layout.Nodes[0]
	wantHeight := layout.Metrics.TableHeaderHeight + 2*layout.Metrics.TextRowHeight
	if node.Heading != "今日课表" || node.Bounds.Dy() != wantHeight {
		t.Fatalf("node = %#v, want height %d", node, wantHeight)
	}
}

func TestLayoutRichTextMeasuresTableColumns(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	narrow := layoutRichText(parseRichText("# 表格\n\n| A | B |\n| --- | --- |\n| 1 | 2 |"), now)
	wide := layoutRichText(parseRichText("# 表格\n\n| 一个非常非常长的站点名称 | 另一个同样很长的站点名称 |\n| --- | --- |\n| 14:30 | 14:45 |"), now)

	// Both tables are narrower than the minimum content width, so they are
	// padded out to the same clamped width while keeping their own column
	// proportions.
	if narrow.Nodes[0].Bounds.Dx() != wide.Nodes[0].Bounds.Dx() {
		t.Fatalf("narrow table = %d, wide table = %d, want equal clamped widths", narrow.Nodes[0].Bounds.Dx(), wide.Nodes[0].Bounds.Dx())
	}
	if narrow.Nodes[0].ColumnWidths[0] >= wide.Nodes[0].ColumnWidths[0] {
		t.Fatalf("narrow first column = %d, wide first column = %d", narrow.Nodes[0].ColumnWidths[0], wide.Nodes[0].ColumnWidths[0])
	}
}

func TestLayoutRichTextUsesIntrinsicTableWidths(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 北区 | 西区 |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |\n\n| 南区 | 东区 |\n| --- | --- |\n| 15:00 | 15:15 |"), now)

	if got, want := layout.Nodes[0].Bounds.Dx(), sumRichWidths(layout.Nodes[0].ColumnWidths); got != want {
		t.Fatalf("first table width = %d, column widths = %d", got, want)
	}
	if got, want := layout.Nodes[1].Bounds.Dx(), sumRichWidths(layout.Nodes[1].ColumnWidths); got != want {
		t.Fatalf("second table width = %d, column widths = %d", got, want)
	}
	if layout.Nodes[0].Bounds.Dx() <= layout.Nodes[1].Bounds.Dx() {
		t.Fatalf("three-column table = %d, two-column table = %d", layout.Nodes[0].Bounds.Dx(), layout.Nodes[1].Bounds.Dx())
	}
	available := layout.Space.Width - 2*layout.Metrics.MarginX
	if layout.Nodes[1].Bounds.Dx() >= available {
		t.Fatalf("narrow table width = %d, available = %d", layout.Nodes[1].Bounds.Dx(), available)
	}
}

func TestLayoutRichTextPacksBusTablesAcrossRows(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 西区 |\n| --- | --- |\n| 09:00 | 09:15 |\n\n| 西区 | 东区 |\n| --- | --- |\n| 09:20 | 09:35 |\n\n| 南区 | 东区 |\n| --- | --- |\n| 09:30 | 09:45 |"), now)

	if len(layout.Nodes) != 3 {
		t.Fatalf("nodes = %d", len(layout.Nodes))
	}
	canvas := image.Rect(0, 0, layout.Space.Width, layout.Space.Height)
	for i, node := range layout.Nodes {
		if !node.Bounds.In(canvas) {
			t.Fatalf("table %d outside canvas: %v in %v", i, node.Bounds, canvas)
		}
		for j := i + 1; j < len(layout.Nodes); j++ {
			if node.Bounds.Overlaps(layout.Nodes[j].Bounds) {
				t.Fatalf("table %d overlaps table %d: %v and %v", i, j, node.Bounds, layout.Nodes[j].Bounds)
			}
		}
	}
	widestRow := 0
	for _, node := range layout.Nodes {
		widestRow = max(widestRow, node.Bounds.Max.X-layout.Metrics.MarginX)
	}
	if layout.Space.Width < widestRow+layout.Metrics.MarginX {
		t.Fatalf("canvas width = %d, content right edge = %d", layout.Space.Width, widestRow)
	}
}

func TestLayoutRichTextGroupsHighTechBusTablesOnFirstRow(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 西区 |\n| --- | --- |\n| 09:00 | 09:15 |\n\n| 高新区 | 先研院 | 东区 |\n| --- | --- | --- |\n| 09:20 | 09:30 | 10:00 |\n\n| 西区 | 东区 |\n| --- | --- |\n| 10:10 | 10:25 |\n\n| 东区 | 先研院 | 高新区 |\n| --- | --- | --- |\n| 10:30 | 11:00 | 11:10 |"), now)

	if len(layout.Nodes) != 4 {
		t.Fatalf("nodes = %d", len(layout.Nodes))
	}
	firstRowY := layout.Nodes[0].Bounds.Min.Y
	secondRowY := layout.Nodes[2].Bounds.Min.Y
	if layout.Nodes[1].Bounds.Min.Y != firstRowY || secondRowY <= firstRowY || layout.Nodes[3].Bounds.Min.Y != secondRowY {
		t.Fatalf("row positions = %v, %v, %v, %v", layout.Nodes[0].Bounds, layout.Nodes[1].Bounds, layout.Nodes[2].Bounds, layout.Nodes[3].Bounds)
	}
	for i, node := range layout.Nodes {
		servesHighTech := richBusTableServesCampus(node.Table, "高新区")
		if i < 2 && !servesHighTech || i >= 2 && servesHighTech {
			t.Fatalf("table %d grouped incorrectly: %#v", i, node.Table.Header)
		}
	}
	widestRightEdge := max(layout.Nodes[1].Bounds.Max.X, layout.Nodes[3].Bounds.Max.X)
	if got := layout.Space.Width - widestRightEdge; got != layout.Metrics.MarginX {
		t.Fatalf("right margin = %d, want %d", got, layout.Metrics.MarginX)
	}
}

func TestLayoutRichTextKeepsReverseBusRoutesTogether(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 南区 |\n| --- | --- |\n| 09:00 | 09:15 |\n\n| 西区 | 南区 |\n| --- | --- |\n| 09:20 | 09:35 |\n\n| 南区 | 东区 |\n| --- | --- |\n| 09:40 | 09:55 |\n\n| 南区 | 西区 |\n| --- | --- |\n| 10:00 | 10:15 |"), now)

	got := make([]string, 0, len(layout.Nodes))
	for _, node := range layout.Nodes {
		got = append(got, node.Label)
	}
	want := []string{"东区→南区", "南区→东区", "西区→南区", "南区→西区"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route order = %#v, want %#v", got, want)
	}
}

func TestLayoutRichTextKeepsNonBusTablesStacked(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 今明两日课表\n\n## 今天\n| 校区 | 教室 | 时间 | 课程 |\n| --- | --- | --- | --- |\n| 西区 | 3A204 | 09:50-11:25 | 数据库系统 |\n\n## 明天\n| 校区 | 教室 | 时间 | 课程 |\n| --- | --- | --- | --- |\n| 先研院 | 1A201 | 16:00-17:35 | Machine Learning |"), now)

	if len(layout.Nodes) != 2 || layout.Nodes[1].Bounds.Min.Y <= layout.Nodes[0].Bounds.Max.Y {
		t.Fatalf("nodes = %#v", layout.Nodes)
	}
}

func TestLayoutRichTextMeasuresColumnsIndependently(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 表格\n\n| A | 一个很长的站点名称 |\n| --- | --- |\n| 1 | 14:30 |"), now)
	widths := layout.Nodes[0].ColumnWidths

	if len(widths) != 2 || widths[0] >= widths[1] {
		t.Fatalf("column widths = %v", widths)
	}
}

func TestLayoutRichTextClampsWideTables(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	doc := parseRichText("# 表格\n\n| 一个非常非常非常非常非常非常非常非常长的站点名称 | 另一个非常非常非常非常非常非常非常非常长的站点名称 | 第三个非常非常非常非常非常非常非常非常长的站点名称 |\n| --- | --- | --- |\n| 14:30 | 14:45 | 15:00 |")
	layout := layoutRichText(doc, now)
	node := layout.Nodes[0]
	natural := sumRichWidths(measureRichTableColumnWidths(*doc.Blocks[0].Table, layout.Metrics))
	want := layout.Metrics.MaxContentWidth

	if natural <= want {
		t.Fatalf("fixture table should exceed the max content width: natural = %d, max = %d", natural, want)
	}
	if node.Bounds.Dx() != want || sumRichWidths(node.ColumnWidths) != want {
		t.Fatalf("table width = %d, columns = %v, want clamped width %d", node.Bounds.Dx(), node.ColumnWidths, want)
	}
	if got := layout.Space.Width; got != want+2*layout.Metrics.MarginX {
		t.Fatalf("canvas width = %d, want %d", got, want+2*layout.Metrics.MarginX)
	}
}

func TestLayoutRichTextPadsNarrowTablesToContentWidth(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	doc := parseRichText("# 表格\n\n| 命令 | 说明 |\n| --- | --- |\n| 日程 | 查看日程 |")
	layout := layoutRichText(doc, now)
	node := layout.Nodes[0]
	natural := measureRichTableColumnWidths(*doc.Blocks[0].Table, layout.Metrics)
	want := layout.Space.Width - 2*layout.Metrics.MarginX

	if want != layout.Metrics.MinContentWidth {
		t.Fatalf("content width = %d, want min content width %d", want, layout.Metrics.MinContentWidth)
	}
	if got := sumRichWidths(node.ColumnWidths); got != want {
		t.Fatalf("padded table width = %d, want content width %d", got, want)
	}
	last := len(natural) - 1
	if node.ColumnWidths[last] <= natural[last] {
		t.Fatalf("last column = %d, natural width = %d, want padding", node.ColumnWidths[last], natural[last])
	}
	if node.ColumnWidths[0] != natural[0] {
		t.Fatalf("first column = %d, natural width = %d, want unchanged", node.ColumnWidths[0], natural[0])
	}
}

func TestLayoutRichTextWrapsLongLinesWithinMaxWidth(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	longLine := strings.Repeat("这是一段需要换行的长文本，", 8)
	layout := layoutRichText(parseRichText("# 待办\n\n"+longLine), now)

	if got, want := layout.Space.Width, layout.Metrics.MaxContentWidth+2*layout.Metrics.MarginX; got != want {
		t.Fatalf("canvas width = %d, want clamped width %d", got, want)
	}
	if len(layout.Nodes) != 1 {
		t.Fatalf("nodes = %#v", layout.Nodes)
	}
	node := layout.Nodes[0]
	wrapWidth := layout.Metrics.MaxContentWidth - 2*layout.Metrics.TextPaddingX
	if len(node.Lines) < 2 {
		t.Fatalf("wrapped lines = %#v, want at least 2", node.Lines)
	}
	for _, line := range node.Lines {
		if width := richTextWidth(line, 13); width > wrapWidth {
			t.Fatalf("line %q width = %d, exceeds wrap width %d", line, width, wrapWidth)
		}
	}
	if got, want := node.Bounds.Dy(), len(node.Lines)*layout.Metrics.TextRowHeight; got != want {
		t.Fatalf("node height = %d, want %d for %d wrapped lines", got, want, len(node.Lines))
	}
	canvas := image.Rect(0, 0, layout.Space.Width, layout.Space.Height)
	if !node.Bounds.In(canvas) {
		t.Fatalf("node bounds %v outside canvas %v", node.Bounds, canvas)
	}
}

func TestWrapRichText(t *testing.T) {
	if got := wrapRichText("短行", 100, 13); len(got) != 1 || got[0] != "短行" {
		t.Fatalf("short line wrapped to %#v", got)
	}
	got := wrapRichText("aaa bbb ccc", 70, 13)
	if len(got) != 2 || got[0] != "aaa bbb" || got[1] != "ccc" {
		t.Fatalf("latin text broke at %#v, want break at space", got)
	}
	got = wrapRichText(strings.Repeat("字", 20), 10*13, 13)
	if len(got) != 2 {
		t.Fatalf("unbroken CJK wrapped to %#v, want 2 lines", got)
	}
	for _, line := range got {
		if width := richTextWidth(line, 13); width > 10*13 {
			t.Fatalf("line %q width = %d exceeds max", line, width)
		}
	}
}

func TestFitRichTableColumnWidths(t *testing.T) {
	widths := []int{300, 100}
	fitRichTableColumnWidths(widths, 280)
	if widths[0] != 180 || widths[1] != 100 {
		t.Fatalf("shrunk widths = %v, want [180 100]", widths)
	}

	widths = []int{100, 100}
	fitRichTableColumnWidths(widths, 300)
	if widths[0] != 100 || widths[1] != 200 {
		t.Fatalf("padded widths = %v, want [100 200]", widths)
	}

	widths = []int{60, 60, 60}
	fitRichTableColumnWidths(widths, 100)
	for _, width := range widths {
		if width < minRichTableColumnWidth {
			t.Fatalf("column shrunk below floor: %v", widths)
		}
	}
}

func TestFitRichTableColumnWidthsSparesStructuralColumns(t *testing.T) {
	// Mirrors the 考试 table: short structural columns around one verbose
	// free-text course column.
	widths := []int{25, 59, 111, 299, 111, 44, 133}
	fitRichTableColumnWidths(widths, 640)
	want := []int{25, 59, 111, 157, 111, 44, 133}
	for i := range want {
		if widths[i] != want[i] {
			t.Fatalf("widths = %v, want %v (only the verbose column may shrink)", widths, want)
		}
	}

	// When no column is verbose, all columns share the clamp down to the floor.
	widths = []int{150, 150, 150, 150}
	fitRichTableColumnWidths(widths, 400)
	if sumRichWidths(widths) != 400 {
		t.Fatalf("widths = %v, want total 400", widths)
	}
	for _, width := range widths {
		if width != 100 {
			t.Fatalf("widths = %v, want [100 100 100 100]", widths)
		}
	}
}

func TestLayoutRichTextKeepsExamStructuralColumnsReadable(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	doc := parseRichText(`# 未来 7 天截止

## 考试 (3)
| # | 日期 | 时间 | 课程 | 教学班 | 方式 | 教室 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 07-29 | 08:30-10:30 | 数学分析（B1） | MATH1001.01 | 闭卷 | 东区 五教 5102 |
| 2 | 08-02 | 14:30-16:30 | 数据结构 | CS2001.03 | 开卷 | 西区 三教 3A204 |
| 3 | 08-04 | 19:00-21:00 | 量子力学与统计物理综合考试（荣誉学位项目） | PHYS3001.01 | 闭卷 | 高新区 GT-B112 |`)
	layout := layoutRichText(doc, now)
	node := layout.Nodes[0]
	natural := measureRichTableColumnWidths(*doc.Blocks[0].Table, layout.Metrics)

	if got := sumRichWidths(node.ColumnWidths); got != layout.Metrics.MaxContentWidth {
		t.Fatalf("table width = %d, want clamped width %d", got, layout.Metrics.MaxContentWidth)
	}
	// Only the verbose 课程 column (index 3) may shrink.
	for i := range natural {
		if i == 3 {
			if node.ColumnWidths[i] >= natural[i] {
				t.Fatalf("verbose column should absorb the clamp: widths = %v, natural = %v", node.ColumnWidths, natural)
			}
			continue
		}
		if node.ColumnWidths[i] != natural[i] {
			t.Fatalf("structural column %d shrunk: widths = %v, natural = %v", i, node.ColumnWidths, natural)
		}
	}
	// Every structural cell still fits its column without ellipsizing.
	for _, row := range doc.Blocks[0].Table.Rows {
		for i, cell := range row.Cells {
			if i == 3 {
				continue
			}
			if width := richTextWidth(cell, 14); width > node.ColumnWidths[i]-2*layout.Metrics.TableCellPaddingX {
				t.Fatalf("cell %q (col %d) width %d exceeds column %d", cell, i, width, node.ColumnWidths[i])
			}
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

func TestLayoutRichTextDoesNotEmphasizeAllRoutesHeaders(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| 东区 | 北区 | 西区 |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |"), now)
	for _, header := range layout.Nodes[0].Header {
		if header.Emphasize {
			t.Fatalf("all-routes header emphasized: %#v", layout.Nodes[0].Header)
		}
	}
}

func TestLayoutRichTextEmphasizesOnlyMarkedBusEndpoints(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 校车\n\n| **东区** | 北区 | **西区** |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |"), now)
	headers := layout.Nodes[0].Header
	if len(headers) != 3 || headers[0].Text != "东区" || !headers[0].Emphasize || headers[1].Emphasize || headers[2].Text != "西区" || !headers[2].Emphasize {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestLayoutRichTextKeepsBusSemanticsOutOfOtherTables(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	layout := layoutRichText(parseRichText("# 课表\n\n| 时间 | 课程 | 教室 |\n| --- | --- | --- |\n| 14:30 | Database Systems | 3A204 |"), now)
	if layout.NextTime != "" || layout.NextWait != "" {
		t.Fatalf("next bus metadata = %q %q", layout.NextTime, layout.NextWait)
	}
	for _, header := range layout.Nodes[0].Header {
		if header.Emphasize {
			t.Fatalf("non-bus header emphasized: %#v", layout.Nodes[0].Header)
		}
	}
}

func TestRichFooterOnlyShowsTimeDayTypeAndSource(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 34, 0, 0, time.FixedZone("CST", 8*60*60))
	if got, want := richFooterLines(now), [2]string{"12:34 · 工作日", "Life@USTC"}; got != want {
		t.Fatalf("footer = %#v, want %#v", got, want)
	}
}

func TestRichNextBusTextIsTwoFontSizesLargerThanMetadata(t *testing.T) {
	if richNextFontSize != richMetaFontSize+2 {
		t.Fatalf("next font size = %d, metadata = %d", richNextFontSize, richMetaFontSize)
	}
}

func TestRichTextWidthTreatsASCIIWhitespaceAsMonospace(t *testing.T) {
	if got, want := richTextWidth("A A", 14), richTextWidth("AAA", 14); got != want {
		t.Fatalf("spaced width = %d, dense width = %d", got, want)
	}
}
