package napcat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type requestEvent struct {
	PostType    string `json:"post_type"`
	RequestType string `json:"request_type"`
	SubType     string `json:"sub_type"`
	Flag        string `json:"flag"`
	UserID      int64  `json:"user_id"`
	Comment     string `json:"comment"`
}

func (b *Bridge) handleIncomingEvent(ctx context.Context, raw json.RawMessage, dispatch func(context.Context, messageEvent)) {
	var envelope struct {
		PostType string `json:"post_type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		b.logf("napcat ignored invalid event: %v", err)
		return
	}
	switch envelope.PostType {
	case "message":
		var event messageEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			b.logf("napcat ignored invalid message event: %v", err)
			return
		}
		if !isPrivateOrGroupMessage(event.MessageType) {
			return
		}
		b.enrichMessageEvent(ctx, &event)
		if strings.TrimSpace(event.RawMessage) == "" && len(event.imageURLs()) == 0 {
			return
		}
		dispatch(ctx, event)
	case "request":
		var event requestEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			b.logf("napcat ignored invalid request event: %v", err)
			return
		}
		b.handleRequestEvent(ctx, event)
	}
}

func isPrivateOrGroupMessage(messageType string) bool {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "private", "group":
		return true
	default:
		return false
	}
}

func (b *Bridge) handleRequestEvent(ctx context.Context, event requestEvent) {
	if !strings.EqualFold(strings.TrimSpace(event.RequestType), "friend") {
		return
	}
	flag := strings.TrimSpace(event.Flag)
	if flag == "" {
		b.logf("friend request missing flag: user_id=%d", event.UserID)
		return
	}
	if err := b.setFriendAddRequest(ctx, flag, true); err != nil {
		b.logf("auto-approve friend request failed: user_id=%d error=%v", event.UserID, err)
		return
	}
	b.logf("auto-approved friend request: user_id=%d", event.UserID)
}

func (b *Bridge) setFriendAddRequest(ctx context.Context, flag string, approve bool) error {
	params := map[string]any{
		"flag":    flag,
		"approve": approve,
	}
	if conn, writeMu := b.activeReverseConn(); conn != nil {
		_, err := b.requestReverseAction(ctx, conn, writeMu, "set_friend_add_request", params)
		return err
	}
	_, err := b.postActionResponse(ctx, "/set_friend_add_request", params)
	return err
}

func (b *Bridge) enrichMessageEvent(ctx context.Context, event *messageEvent) {
	if event == nil {
		return
	}
	text := strings.TrimSpace(event.RawMessage)
	forwardIDs := forwardIDsFromMessage(event.Message)
	if len(forwardIDs) == 0 {
		forwardIDs = forwardIDsFromCQMessage(event.RawMessage)
	}
	if len(forwardIDs) == 0 {
		if text == "" {
			event.RawMessage = plainTextFromMessage(event.Message)
		}
		return
	}
	parts := make([]string, 0, len(forwardIDs)+1)
	if text != "" && !looksLikeForwardOnlyCQ(text) {
		parts = append(parts, text)
	}
	for _, id := range forwardIDs {
		body, err := b.fetchForwardMessage(ctx, id)
		if err != nil {
			b.logf("get_forward_msg failed: id=%s error=%v", id, err)
			parts = append(parts, "[合并转发:无法读取]")
			continue
		}
		if body == "" {
			parts = append(parts, "[合并转发:空内容]")
			continue
		}
		parts = append(parts, "合并转发内容：\n"+body)
	}
	event.RawMessage = strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func (b *Bridge) fetchForwardMessage(ctx context.Context, id string) (string, error) {
	// Always use HTTP for forward expansion. Calling requestReverseAction from the
	// reverse read loop would deadlock waiting for an echo that same loop must deliver.
	params := map[string]any{"message_id": id}
	response, err := b.postActionResponse(ctx, "/get_forward_msg", params)
	if err != nil {
		// Some NapCat builds expect `id` instead of `message_id`.
		response, err = b.postActionResponse(ctx, "/get_forward_msg", map[string]any{"id": id})
		if err != nil {
			return "", err
		}
	}
	return formatForwardMessageData(response.Data), nil
}

const (
	maxForwardNodes = 40
	maxForwardRunes = 4000
)

func formatForwardMessageData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "[合并转发:无法解析]"
	}
	text := strings.TrimSpace(strings.Join(forwardContentLines(payload), "\n"))
	runes := []rune(text)
	if len(runes) > maxForwardRunes {
		return string(runes[:maxForwardRunes]) + "\n...(合并转发已截断)"
	}
	return text
}

func forwardContentLines(payload any) []string {
	switch value := payload.(type) {
	case map[string]any:
		if messages, ok := value["messages"].([]any); ok {
			return forwardNodeLines(messages)
		}
		if message, ok := value["message"].([]any); ok {
			return forwardNodeLines(message)
		}
		if content, ok := value["content"].([]any); ok {
			return forwardNodeLines(content)
		}
	case []any:
		return forwardNodeLines(value)
	}
	return nil
}

func forwardNodeLines(nodes []any) []string {
	lines := make([]string, 0, len(nodes))
	for _, raw := range nodes {
		if len(lines) >= maxForwardNodes {
			lines = append(lines, "...(合并转发已截断)")
			break
		}
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data, _ := node["data"].(map[string]any)
		if data == nil {
			data = node
		}
		nickname := strings.TrimSpace(fmt.Sprint(data["nickname"]))
		if nickname == "" {
			nickname = strings.TrimSpace(fmt.Sprint(data["user_id"]))
		}
		content := plainTextFromMessage(data["content"])
		if content == "" {
			content = strings.TrimSpace(fmt.Sprint(data["content"]))
		}
		if content == "" {
			continue
		}
		if nickname != "" && nickname != "<nil>" {
			lines = append(lines, nickname+": "+content)
		} else {
			lines = append(lines, content)
		}
	}
	return lines
}

func forwardIDsFromMessage(message any) []string {
	segments := messageSegments(message)
	var ids []string
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		if typ != "forward" && typ != "node" {
			continue
		}
		data, _ := segment["data"].(map[string]any)
		if data == nil {
			continue
		}
		for _, key := range []string{"id", "message_id"} {
			id := strings.TrimSpace(fmt.Sprint(data[key]))
			if id != "" && id != "<nil>" {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids
}

func forwardIDsFromCQMessage(message string) []string {
	var ids []string
	for _, prefix := range []string{"[CQ:forward,", "[CQ:node,"} {
		ids = append(ids, cqAttrValues(message, prefix, "id=", "message_id=")...)
	}
	return ids
}

func looksLikeForwardOnlyCQ(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "[CQ:forward,") || strings.HasPrefix(trimmed, "[CQ:node,")
}

func plainTextFromMessage(message any) string {
	segments := messageSegments(message)
	if len(segments) == 0 {
		switch value := message.(type) {
		case string:
			return strings.TrimSpace(value)
		default:
			return ""
		}
	}
	parts := make([]string, 0, len(segments))
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		data, _ := segment["data"].(map[string]any)
		switch typ {
		case "text":
			if data != nil {
				parts = append(parts, fmt.Sprint(data["text"]))
			}
		case "at":
			if data != nil {
				qq := strings.TrimSpace(fmt.Sprint(data["qq"]))
				if qq != "" && qq != "<nil>" {
					parts = append(parts, "@"+qq)
				}
			}
		case "image":
			parts = append(parts, "[图片]")
		case "face":
			parts = append(parts, "[表情]")
		case "reply":
			parts = append(parts, "[回复]")
		case "forward":
			parts = append(parts, "[合并转发]")
		case "json":
			parts = append(parts, "[卡片]")
		case "file":
			parts = append(parts, "[文件]")
		case "record":
			parts = append(parts, "[语音]")
		case "video":
			parts = append(parts, "[视频]")
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func messageSegments(message any) []any {
	switch value := message.(type) {
	case []any:
		return value
	case []map[string]any:
		segments := make([]any, len(value))
		for i := range value {
			segments[i] = value[i]
		}
		return segments
	default:
		return nil
	}
}
