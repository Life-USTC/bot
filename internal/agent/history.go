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
	conversationHistoryTokenLimit = 48_000
)

// conversationEventMessages restores exact role-bearing history. It never
// turns host receipts, approval state, or generated summaries into user text.
func conversationEventMessages(events []store.ConversationEvent) []*schema.Message {
	events = exactConversationEventWindow(events, conversationHistoryTokenLimit)
	return messagesFromConversationEvents(events)
}

func messagesFromConversationEvents(events []store.ConversationEvent) []*schema.Message {
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

// exactConversationEventWindow bounds history only by removing complete old
// user turns. It never rewrites a tool result or inserts synthetic text into
// the transcript sent to the model.
func exactConversationEventWindow(events []store.ConversationEvent, tokenLimit int) []store.ConversationEvent {
	events = completeConversationEventWindow(events)
	if len(events) == 0 || tokenLimit <= 0 {
		return events
	}
	latestUser := 0
	for index, event := range events {
		if event.Type != store.ConversationEventUser {
			continue
		}
		latestUser = index
		if estimateMessagesTokens(messagesFromConversationEvents(events[index:])) <= tokenLimit {
			return events[index:]
		}
	}
	// A single recent turn may itself exceed the history target. Keep it exact;
	// the run-wide provider budget remains the final hard limit.
	return events[latestUser:]
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

func estimateMessagesTokens(messages []*schema.Message) int {
	tokens := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		tokens += estimateTextTokens(string(message.Role))
		tokens += estimateTextTokens(message.Content)
		tokens += estimateTextTokens(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			tokens += estimateTextTokens(call.Function.Name)
			tokens += estimateTextTokens(call.Function.Arguments)
		}
		for _, part := range message.UserInputMultiContent {
			tokens += estimateTextTokens(part.Text)
		}
		for _, part := range message.AssistantGenMultiContent {
			tokens += estimateTextTokens(part.Text)
		}
	}
	return tokens
}

func limitRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
