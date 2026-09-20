package responses

import (
	"strings"
	"testing"
)

func TestBusRenderTitleParse(t *testing.T) {
	if got := busRenderTitle(NewTextImage("bus", "校车 东区 → 西区", "x")); got != "东区 → 西区" {
		t.Fatalf("title = %q", got)
	}
	if got := busRenderTitle(NewTextImage("bus", "校车", "x")); got != "校车" {
		t.Fatalf("title = %q", got)
	}
}

// Endpoint emphasis is a bus-timetable signal carried from the Markdown
// header into the render payload. It used to be asserted through the deleted
// layout engine; it is parsing plus the bus check, so assert those.
func TestParseRichTextEmphasizesOnlyMarkedBusEndpoints(t *testing.T) {
	doc := parseRichText("# 校车\n\n| **东区** | 北区 | **西区** |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |")
	table := doc.Blocks[0].Table
	if got := table.Header; len(got) != 3 || got[0] != "东区" || got[1] != "北区" || got[2] != "西区" {
		t.Fatalf("header = %#v", got)
	}
	if got := table.HeaderEmphasis; len(got) != 3 || !got[0] || got[1] || !got[2] {
		t.Fatalf("header emphasis = %#v, want only the endpoints", got)
	}
}

func TestParseRichTextLeavesAllRoutesHeadersUnemphasized(t *testing.T) {
	doc := parseRichText("# 校车\n\n| 东区 | 北区 | 西区 |\n| --- | --- | --- |\n| 14:30 | 14:35 | 14:45 |")
	for index, emphasized := range doc.Blocks[0].Table.HeaderEmphasis {
		if emphasized {
			t.Fatalf("all-routes header %d emphasized", index)
		}
	}
}

// Only a bus document gets the bus timetable treatment: a class table that
// happens to hold clock values must not gain next-bus metadata or an even
// column split.
func TestRichDocumentIsBusOnlyForBusTables(t *testing.T) {
	bus := parseRichText("# 校车\n\n| **东区** | 西区 |\n| --- | --- |\n| 14:30 | 14:40 |")
	if !richDocumentIsBus(bus) {
		t.Fatal("bus timetable not recognized")
	}
	schedule := parseRichText("# 课表\n\n| 时间 | 课程 | 教室 |\n| --- | --- | --- |\n| 14:30 | Database Systems | 3A204 |")
	if richDocumentIsBus(schedule) {
		t.Fatal("class table treated as a bus timetable")
	}
}
func testBusImage() *Image {
	return NewTextImage("bus", "校车 东区 → 西区", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
	}, "\n"))
}

func testBusAllImage() *Image {
	return NewTextImage("bus", "校车", strings.Join([]string{
		"东区	西区	先研院	高新区",
		"14:30	14:40	14:52	15:05",
		"16:00	16:10	16:22	16:35",
		"",
		"东区	北区	西区",
		"15:30	15:35	15:40	✨",
		"15:50	15:55	16:00",
		"",
		"西区	北区	东区",
		"15:40	15:45	16:00",
		"16:10	16:15	16:30",
	}, "\n"))
}
