package responses

import (
	"reflect"
	"testing"
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

func TestNewTextImageRemovesDuplicateHeadingFromRichText(t *testing.T) {
	img := NewTextImage("schedule", "今天课表", "今天课表：\n09:50 数据库系统")
	if img.RichText != "# 今天课表\n\n09:50 数据库系统" {
		t.Fatalf("rich text = %q", img.RichText)
	}
}
