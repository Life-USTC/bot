package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	conversationEventLimit        = 80
	conversationCompactInputLimit = 128_000
)

// conversationEventMessages restores exact role-bearing history. It never
// turns host receipts, approval state, or generated summaries into user text.
func conversationEventMessages(events []store.ConversationEvent) []*schema.Message {
	events = completeConversationEventWindow(events)
	messages := make([]*schema.Message, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case store.ConversationEventUser:
			if strings.TrimSpace(event.Content) != "" {
				messages = append(messages, schema.UserMessage(event.Content))
			}
		case store.ConversationEventAssistant:
			calls := make([]schema.ToolCall, 0, len(event.ToolCalls))
			for _, call := range event.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
					continue
				}
				calls = append(calls, schema.ToolCall{
					ID: call.ID, Type: "function",
					Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments},
				})
			}
			if strings.TrimSpace(event.Content) != "" || len(calls) > 0 {
				messages = append(messages, schema.AssistantMessage(event.Content, calls))
			}
		case store.ConversationEventToolResult, store.ConversationEventToolError, store.ConversationEventToolDenial:
			if strings.TrimSpace(event.ToolCallID) == "" {
				continue
			}
			messages = append(messages, schema.ToolMessage(event.Content, event.ToolCallID, schema.WithToolName(event.ToolName)))
		}
	}
	return messages
}

// A bounded tail may begin halfway through a tool exchange, which providers
// reject. Start at the first retained user event so every restored tool result
// has its assistant call in the same window.
func completeConversationEventWindow(events []store.ConversationEvent) []store.ConversationEvent {
	for i, event := range events {
		if event.Type == store.ConversationEventUser {
			return events[i:]
		}
	}
	return nil
}

func estimateTextTokens(text string) int {
	ascii := 0
	other := 0
	for _, r := range text {
		if r <= utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

func limitRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
