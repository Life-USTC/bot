package agent

import (
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestPruneHistoryNoise(t *testing.T) {
	in := strings.Join([]string{
		"工具调用：search_courses {}",
		"工具结果：",
		"课程列表",
		"合并转发内容：",
		"Alice: hi",
		"Bob: yo",
		"![](校车 东区 西区)",
		"建议提前到站",
	}, "\n")
	got := pruneHistoryNoise(in)
	for _, unwanted := range []string{"工具调用", "工具结果", "Alice:", "Bob:", "![]("} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("noise remained %q in %q", unwanted, got)
		}
	}
	for _, want := range []string{"[合并转发]", "建议提前到站"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestConversationRecentTurnTextPreservesLatestToolResult(t *testing.T) {
	turn := store.Interaction{
		RawText: "查数学分析的课表",
		Reply:   "工具调用：search_courses {\"query\":\"数学分析\"}\n工具结果：\n数学分析 A 班，周一 08:00\n请继续安排。",
	}
	got := conversationRecentTurnText(turn)
	if !strings.Contains(got, "查数学分析的课表") ||
		!strings.Contains(got, "[最近工具结果] 数学分析 A 班，周一 08:00") ||
		!strings.Contains(got, "请继续安排") {
		t.Fatalf("recent turn lost intent/result: %q", got)
	}
	if strings.Contains(got, "工具调用：") {
		t.Fatalf("recent turn retained raw tool call: %q", got)
	}
}
