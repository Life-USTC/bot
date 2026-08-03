package agent

import (
	"strings"
	"testing"
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
	for _, want := range []string{"[合并转发]", "[图片卡片:校车 东区 西区]", "建议提前到站"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
