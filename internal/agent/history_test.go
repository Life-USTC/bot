package agent

import (
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestConversationRetainedTurnCountUsesTokenBudget(t *testing.T) {
	turns := make([]store.Interaction, conversationRecentTurnLimit)
	for i := range turns {
		turns[i] = store.Interaction{
			RawText: strings.Repeat("问", maxHistoryTextRunes),
			Reply:   strings.Repeat("答", maxHistoryTextRunes),
		}
	}
	if !conversationHistoryNeedsCompaction("", turns) {
		t.Fatal("large recent turns did not trigger compaction")
	}
	if got := conversationRetainedTurnCount("", turns); got >= conversationRecentTurnLimit || got < 2 {
		t.Fatalf("retained turns = %d", got)
	}
}

func TestEstimateTextTokensTreatsChineseConservatively(t *testing.T) {
	if got := estimateTextTokens("abcd课程"); got != 3 {
		t.Fatalf("estimated tokens = %d", got)
	}
}
