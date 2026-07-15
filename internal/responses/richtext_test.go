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
