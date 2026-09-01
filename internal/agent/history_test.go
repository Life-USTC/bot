package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestConversationEventMessagesPreserveExactRolesAndToolEvidence(t *testing.T) {
	events := []store.ConversationEvent{
		{Type: store.ConversationEventUser, Content: "查数学分析"},
		{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{ID: "call-1", Name: "invoke_bot_capability", Arguments: `{"capability":"course_search","arguments":["数学分析"]}`}}},
		{Type: store.ConversationEventToolResult, ToolCallID: "call-1", ToolName: "invoke_bot_capability", Content: "数学分析（程艺，2026春）"},
		{Type: store.ConversationEventAssistant, Content: "程艺老师在 2026 春开课。"},
	}
	messages := conversationEventMessages(events)
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != schema.User || messages[1].Role != schema.Assistant || len(messages[1].ToolCalls) != 1 ||
		messages[2].Role != schema.Tool || messages[2].ToolCallID != "call-1" || messages[2].Content != "数学分析（程艺，2026春）" ||
		messages[3].Role != schema.Assistant {
		t.Fatalf("typed transcript = %#v", messages)
	}
}

func TestConversationEventMessagesDropsIncompleteLeadingToolExchange(t *testing.T) {
	events := []store.ConversationEvent{
		{Type: store.ConversationEventToolResult, ToolCallID: "old", Content: "orphan"},
		{Type: store.ConversationEventAssistant, Content: "orphan reply"},
		{Type: store.ConversationEventUser, Content: "new turn"},
		{Type: store.ConversationEventAssistant, Content: "new reply"},
	}
	messages := conversationEventMessages(events)
	if len(messages) != 2 || messages[0].Content != "new turn" || messages[1].Content != "new reply" {
		t.Fatalf("messages = %#v", messages)
	}
}
