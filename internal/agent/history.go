package agent

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	conversationEventPageSize     = 80
	conversationCompactInputLimit = 128_000
	// Reserve room for instructions, tool schemas, new tool results and the
	// completion. Ordinary turns keep their exact prefix for provider caching.
	conversationHistoryTokenLimit = conversationCompactInputLimit - 32_000
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
			parts := inputMessageParts(event.Parts)
			prefix := userHistoryPrefix(event)
			if len(parts) > 0 {
				if prefix != "" {
					parts = prefixInputMessageParts(parts, prefix)
				}
				messages = append(messages, &schema.Message{Role: schema.User, UserInputMultiContent: parts})
			} else if strings.TrimSpace(event.Content) != "" {
				messages = append(messages, schema.UserMessage(prefix+event.Content))
			} else if prefix != "" {
				// Keep the speaker and timestamp visible for an otherwise empty
				// multimodal/user event without inventing a second user turn.
				messages = append(messages, schema.UserMessage(strings.TrimSpace(prefix)))
			}
		case store.ConversationEventAssistant:
			calls := make([]schema.ToolCall, 0, len(event.ToolCalls))
			for _, call := range event.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
					continue
				}
				callType := call.Type
				if callType == "" {
					callType = "function"
				}
				calls = append(calls, schema.ToolCall{
					ID: call.ID, Type: callType, Index: call.Index,
					Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments},
				})
			}
			content, parts := providerAssistantOutput(event.Content, event.Parts)
			// Command outcomes already carry the durable observed_at field in
			// their JSON envelope. Prefixing that content would make it invalid
			// JSON and would hide the structured result from the model.
			prefix := assistantHistoryPrefix(event)
			if event.Source == store.ConversationEventSourceCommand {
				prefix = ""
			}
			content, parts = prefixAssistantOutput(content, parts, prefix)
			if strings.TrimSpace(content) != "" || len(calls) > 0 || len(parts) > 0 {
				message := schema.AssistantMessage(content, calls)
				message.Name = event.Name
				message.AssistantGenMultiContent = parts
				messages = append(messages, message)
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

func eventHistoryTime(event store.ConversationEvent) time.Time {
	return event.OccurredAt
}

func formatHistoryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func historySpeaker(event store.ConversationEvent) string {
	displayName := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(event.ActorDisplayName, "\n", " "), "\r", " "))
	if displayName == "" {
		displayName = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(event.Name, "\n", " "), "\r", " "))
	}
	userID := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(event.Identity.UserID, "\n", " "), "\r", " "))
	switch {
	case displayName != "" && userID != "" && displayName != userID:
		return displayName + " (user:" + userID + ")"
	case displayName != "":
		return displayName
	default:
		return userID
	}
}

func userHistoryPrefix(event store.ConversationEvent) string {
	when := formatHistoryTime(eventHistoryTime(event))
	speaker := historySpeaker(event)
	if when == "" && speaker == "" {
		return ""
	}
	if when == "" {
		return "[" + speaker + "] "
	}
	if speaker == "" {
		return "[" + when + "] "
	}
	return "[" + when + "] [" + speaker + "] "
}

func prefixInputMessageParts(parts []schema.MessageInputPart, prefix string) []schema.MessageInputPart {
	if len(parts) == 0 || prefix == "" {
		return parts
	}
	result := append([]schema.MessageInputPart(nil), parts...)
	for index := range result {
		if result[index].Type != schema.ChatMessagePartTypeText {
			continue
		}
		result[index].Text = prefix + result[index].Text
		return result
	}
	return append([]schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: prefix}}, result...)
}

func assistantHistoryPrefix(event store.ConversationEvent) string {
	when := formatHistoryTime(eventHistoryTime(event))
	if when == "" {
		return ""
	}
	return "[" + when + "] "
}

func prefixAssistantOutput(content string, parts []schema.MessageOutputPart, prefix string) (string, []schema.MessageOutputPart) {
	if prefix == "" {
		return content, parts
	}
	if len(parts) == 0 {
		return prefix + content, parts
	}
	result := append([]schema.MessageOutputPart(nil), parts...)
	for index := range result {
		if result[index].Type != schema.ChatMessagePartTypeText {
			continue
		}
		result[index].Text = prefix + result[index].Text
		return content, result
	}
	return content, append([]schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeText, Text: prefix}}, result...)
}

func inputMessageParts(parts []store.ConversationMessagePart) []schema.MessageInputPart {
	result := make([]schema.MessageInputPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case string(schema.ChatMessagePartTypeText):
			result = append(result, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: part.Text})
		case string(schema.ChatMessagePartTypeImageURL):
			image := &schema.MessageInputImage{Detail: schema.ImageURLDetail(part.Detail), MessagePartCommon: schema.MessagePartCommon{MIMEType: part.MIMEType}}
			if part.Base64Data != "" {
				data := part.Base64Data
				image.Base64Data = &data
			}
			if part.URL != "" {
				url := part.URL
				image.URL = &url
			}
			result = append(result, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: image})
		}
	}
	return result
}

// providerAssistantOutput retains every persisted text byte while excluding
// assistant output modalities the configured OpenAI-compatible adapter cannot
// encode. In particular, that adapter rejects assistant image parts and
// ignores Content whenever AssistantGenMultiContent is present. Unsupported
// parts remain in the private event store; they are simply not replayed to the
// provider on resume.
func providerAssistantOutput(content string, persisted []store.ConversationMessagePart) (string, []schema.MessageOutputPart) {
	parts := assistantTextOutputParts(persisted)
	if len(parts) == 0 {
		return content, nil
	}
	var combined strings.Builder
	for _, part := range parts {
		combined.WriteString(part.Text)
	}
	if content != "" && combined.String() == content {
		return content, nil
	}
	if content != "" {
		parts = append([]schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeText, Text: content}}, parts...)
	}
	return "", parts
}

func assistantTextOutputParts(parts []store.ConversationMessagePart) []schema.MessageOutputPart {
	result := make([]schema.MessageOutputPart, 0, len(parts))
	for _, part := range parts {
		if part.Type == string(schema.ChatMessagePartTypeText) {
			result = append(result, schema.MessageOutputPart{Type: schema.ChatMessagePartTypeText, Text: part.Text})
		}
	}
	return result
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

// A bounded tail may begin halfway through a tool exchange, and an expired or
// failed job may leave an assistant tool call without its result. Providers
// reject both shapes. Keep only complete historical user turns. For the latest
// turn, retain its user input even if a crashed suffix is incomplete so a new
// job can proceed without replaying malformed tool protocol.
func completeConversationEventWindow(events []store.ConversationEvent) []store.ConversationEvent {
	firstUser := -1
	for index, event := range events {
		if event.Type == store.ConversationEventUser {
			firstUser = index
			break
		}
	}
	if firstUser < 0 {
		return nil
	}
	events = events[firstUser:]
	result := make([]store.ConversationEvent, 0, len(events))
	for start := 0; start < len(events); {
		end := start + 1
		for end < len(events) && events[end].Type != store.ConversationEventUser {
			end++
		}
		turn := events[start:end]
		if conversationTurnProviderCompatible(turn) {
			result = append(result, turn...)
		} else if end == len(events) {
			result = append(result, turn[0])
		}
		start = end
	}
	return result
}

func conversationTurnProviderCompatible(events []store.ConversationEvent) bool {
	messages := messagesFromConversationEvents(events)
	if len(messages) == 0 || messages[0].Role != schema.User {
		return false
	}
	pending := make(map[string]struct{})
	for _, message := range messages {
		if len(pending) > 0 {
			if message.Role != schema.Tool {
				return false
			}
			if _, found := pending[message.ToolCallID]; !found {
				return false
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if message.Role == schema.Tool {
			return false
		}
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				return false
			}
			if _, duplicate := pending[call.ID]; duplicate {
				return false
			}
			pending[call.ID] = struct{}{}
		}
	}
	return len(pending) == 0
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
