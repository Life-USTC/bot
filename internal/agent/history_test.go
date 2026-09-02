package agent

import (
	"strings"
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

func TestConversationEventMessagesBoundsHistoryByDroppingWholeOldTurns(t *testing.T) {
	oldResult := strings.Repeat("旧", 200)
	newResult := strings.Repeat("新", 20)
	events := []store.ConversationEvent{
		{Type: store.ConversationEventUser, Content: "old question"},
		{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{ID: "old-call", Name: "search", Arguments: `{}`}}},
		{Type: store.ConversationEventToolResult, ToolCallID: "old-call", ToolName: "search", Content: oldResult},
		{Type: store.ConversationEventAssistant, Content: "old answer"},
		{Type: store.ConversationEventUser, Content: "new question"},
		{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{ID: "new-call", Name: "search", Arguments: `{}`}}},
		{Type: store.ConversationEventToolResult, ToolCallID: "new-call", ToolName: "search", Content: newResult},
		{Type: store.ConversationEventAssistant, Content: "new answer"},
	}

	window := exactConversationEventWindow(events, 80)
	messages := messagesFromConversationEvents(window)
	if len(messages) != 4 || messages[0].Content != "new question" || messages[2].Content != newResult {
		t.Fatalf("exact bounded messages = %#v", messages)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "omitted") || strings.Contains(message.Content, "截断") {
			t.Fatalf("synthetic history content reached model: %q", message.Content)
		}
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

func TestConversationEventMessagesDropsHistoricalTurnWithUnansweredToolCall(t *testing.T) {
	events := []store.ConversationEvent{
		{JobID: 1, Type: store.ConversationEventUser, Content: "执行一个操作"},
		{JobID: 1, Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{
			ID: "orphaned-call", Name: "invoke_bot_capability", Arguments: `{"capability":"notify","arguments":["homework","on"]}`,
		}}},
		{JobID: 2, Type: store.ConversationEventUser, Content: "你还在吗"},
	}

	messages := conversationEventMessages(events)
	if len(messages) != 1 || messages[0].Role != schema.User || messages[0].Content != "你还在吗" {
		t.Fatalf("provider transcript retained an unanswered historical tool call: %#v", messages)
	}
}

func TestConversationEventMessagesPreservesCompleteParallelToolExchange(t *testing.T) {
	events := []store.ConversationEvent{
		{Type: store.ConversationEventUser, Content: "查两项数据"},
		{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{
			{ID: "call-a", Name: "search_a", Arguments: `{}`},
			{ID: "call-b", Name: "search_b", Arguments: `{}`},
		}},
		{Type: store.ConversationEventToolResult, ToolCallID: "call-b", ToolName: "search_b", Content: "B"},
		{Type: store.ConversationEventToolResult, ToolCallID: "call-a", ToolName: "search_a", Content: "A"},
		{Type: store.ConversationEventAssistant, Content: "完成"},
	}

	messages := conversationEventMessages(events)
	if len(messages) != 5 || messages[1].Role != schema.Assistant || len(messages[1].ToolCalls) != 2 ||
		messages[2].ToolCallID != "call-b" || messages[3].ToolCallID != "call-a" || messages[4].Content != "完成" {
		t.Fatalf("complete parallel exchange was changed: %#v", messages)
	}
}

func TestConversationEventMessagesKeepsLatestUserAndDropsItsIncompleteToolSuffix(t *testing.T) {
	events := []store.ConversationEvent{
		{Type: store.ConversationEventUser, Content: "继续处理"},
		{Type: store.ConversationEventAssistant, ToolCalls: []store.ConversationToolCall{{
			ID: "unfinished", Name: "invoke_bot_capability", Arguments: `{}`,
		}}},
	}

	messages := conversationEventMessages(events)
	if len(messages) != 1 || messages[0].Role != schema.User || messages[0].Content != "继续处理" {
		t.Fatalf("latest user input was not recovered exactly: %#v", messages)
	}
}

func TestConversationEventMessagesReplaysProviderCompatibleParts(t *testing.T) {
	inputURL := "data:image/png;base64,AAAA"
	outputURL := "https://cdn.example/image.png"
	events := []store.ConversationEvent{
		{Type: store.ConversationEventUser, Parts: []store.ConversationMessagePart{
			{Type: "text", Text: "请看图"},
			{Type: "image_url", URL: inputURL, Reference: "https://source.example/image.png", Detail: "high", MIMEType: "image/png"},
		}},
		{Type: store.ConversationEventAssistant, Content: "图中有一只猫。", Parts: []store.ConversationMessagePart{
			{Type: "image_url", URL: outputURL, MIMEType: "image/png"},
			// Provider reasoning is intentionally not a supported transcript part.
			{Type: "reasoning", Text: "private chain of thought"},
		}},
		{Type: store.ConversationEventAssistant, Parts: []store.ConversationMessagePart{{Type: "reasoning", Text: "private chain of thought"}}},
	}
	messages := messagesFromConversationEvents(events)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if len(messages[0].UserInputMultiContent) != 2 || messages[0].UserInputMultiContent[0].Text != "请看图" {
		t.Fatalf("input parts = %#v", messages[0].UserInputMultiContent)
	}
	image := messages[0].UserInputMultiContent[1].Image
	if image == nil || image.URL == nil || *image.URL != inputURL || image.Detail != schema.ImageURLDetailHigh || image.MIMEType != "image/png" {
		t.Fatalf("input image = %#v", image)
	}
	if messages[1].Content != "图中有一只猫。" || len(messages[1].AssistantGenMultiContent) != 0 {
		t.Fatalf("assistant parts = %#v", messages[1])
	}
	if strings.Contains(messages[1].Content, outputURL) {
		t.Fatalf("unsupported assistant image leaked into provider transcript: %#v", messages[1])
	}
}

func TestProviderAssistantOutputPreservesContentAndDistinctTextParts(t *testing.T) {
	content, parts := providerAssistantOutput("主文本", []store.ConversationMessagePart{
		{Type: "text", Text: "补充文本"},
		{Type: "image_url", URL: "https://private.example/output.png"},
	})
	if content != "" || len(parts) != 2 || parts[0].Text != "主文本" || parts[1].Text != "补充文本" {
		t.Fatalf("provider output: content=%q parts=%#v", content, parts)
	}
	for _, part := range parts {
		if part.Type != schema.ChatMessagePartTypeText || part.Image != nil {
			t.Fatalf("unsupported output part = %#v", part)
		}
	}
}
