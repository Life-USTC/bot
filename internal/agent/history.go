package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	conversationEventPageSize = 80
	// Start compaction before the context becomes large. This is a compaction
	// trigger, not a provider capacity or request rejection limit.
	conversationHistoryTokenLimit = 96_000
)

// conversationEventMessages restores exact role-bearing history. It never
// turns host receipts, approval state, or generated summaries into user text.
func conversationEventMessages(events []store.ConversationEvent) []*schema.Message {
	events = completeConversationEventWindow(events)
	return messagesFromConversationEvents(events)
}

func messagesFromConversationEvents(events []store.ConversationEvent) []*schema.Message {
	messages := make([]*schema.Message, 0, len(events))
	var pendingMetadata *schema.Message
	pendingCalls := make(map[string]bool)
	for _, event := range events {
		before := len(messages)
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
			if strings.TrimSpace(content) != "" || len(calls) > 0 || len(parts) > 0 {
				message := schema.AssistantMessage(content, calls)
				message.Name = event.Name
				message.AssistantGenMultiContent = parts
				messages = append(messages, message)
				pendingMetadata = assistantHistoryMetadata(event)
				for _, call := range calls {
					pendingCalls[call.ID] = true
				}
			}
		case store.ConversationEventToolResult, store.ConversationEventToolError, store.ConversationEventToolDenial:
			if strings.TrimSpace(event.ToolCallID) == "" {
				continue
			}
			messages = append(messages, schema.ToolMessage(event.Content, event.ToolCallID, schema.WithToolName(event.ToolName)))
			delete(pendingCalls, event.ToolCallID)
		}
		if event.ID > 0 && len(messages) > before {
			messages[len(messages)-1].Extra = map[string]any{conversationEventIDKey: fmt.Sprint(event.ID)}
		}
		// Never insert a metadata message between an assistant tool call and
		// its results. Appending after the exchange also preserves its prefix.
		if pendingMetadata != nil && len(pendingCalls) == 0 {
			messages = append(messages, pendingMetadata)
			pendingMetadata = nil
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
	return value.In(shanghaiLocation).Format(time.RFC3339)
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

// These fields describe the user message; they are not user-authored text.
func userHistoryPrefix(event store.ConversationEvent) string {
	when, speaker := formatHistoryTime(eventHistoryTime(event)), historySpeaker(event)
	if when == "" && speaker == "" {
		return ""
	}
	data, _ := json.Marshal(struct {
		OccurredAt string `json:"occurred_at,omitempty"`
		Timezone   string `json:"timezone"`
		Speaker    string `json:"speaker,omitempty"`
	}{when, "Asia/Shanghai", speaker})
	return "<message_metadata>" + string(data) + "</message_metadata>\n"
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

const historyMetadataKey = "bot_history_metadata"

func assistantHistoryMetadata(event store.ConversationEvent) *schema.Message {
	when := formatHistoryTime(eventHistoryTime(event))
	if when == "" {
		return nil
	}
	data, _ := json.Marshal(struct {
		OccurredAt string `json:"occurred_at"`
		Timezone   string `json:"timezone"`
	}{when, "Asia/Shanghai"})
	// Host-generated metadata contains no assistant or user-authored content.
	// Its position identifies the preceding assistant message / tool exchange.
	m := schema.SystemMessage("<assistant_message_metadata>" + string(data) + "</assistant_message_metadata>")
	m.Extra = map[string]any{historyMetadataKey: "assistant"}
	return m
}

func isHistoryMetadata(m *schema.Message) bool {
	return m != nil && m.Role == schema.System && m.Extra[historyMetadataKey] == "assistant"
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
