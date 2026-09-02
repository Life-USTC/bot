package textutil

import "testing"

func TestMeaningfulSearchTokenMatchesUnsegmentedChineseWithoutGenericVerbs(t *testing.T) {
	corpus := "List second-classroom (第二课堂) signup events"
	if got := MeaningfulSearchTokenMatches("查询第二课堂平台活动", corpus); got != 3 {
		t.Fatalf("young-event matches = %d, want 3", got)
	}
	if got := MeaningfulSearchTokenMatches("查询", "查询校车班次"); got != 0 {
		t.Fatalf("generic query matches = %d, want 0", got)
	}
	if got := MeaningfulSearchTokenMatches("course", "Search courses"); got != 1 {
		t.Fatalf("English matches = %d, want 1", got)
	}
	if got := MeaningfulSearchTokenMatches("3", "第 3 周，30 条"); got != 0 {
		t.Fatalf("numeric matches = %d, want 0", got)
	}
}
