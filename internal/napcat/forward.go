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
		// Local-only prep here. Network enrichment (get_forward_msg) must not run on
		// the websocket read loop — reverse action replies need that loop free.
		normalizeMessageText(&event)
		if strings.TrimSpace(event.RawMessage) == "" && len(event.imageURLs()) == 0 && !hasForwardPayload(event) {
			return
		}
		dispatch(ctx, event)
	case "request":
		var event requestEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			b.logf("napcat ignored invalid request event: %v", err)
			return
		}
		// Approve off the read loop so reverse-WS set_friend_add_request can complete.
		go b.handleRequestEvent(context.WithoutCancel(ctx), event)
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
	_, err := b.callNapCatAction(ctx, "set_friend_add_request", map[string]any{
		"flag":    flag,
		"approve": approve,
	})
	return err
}

func normalizeMessageText(event *messageEvent) {
	if event == nil {
		return
	}
	if strings.TrimSpace(event.RawMessage) == "" {
		event.RawMessage = plainTextFromMessage(event.Message)
	}
}

func hasForwardPayload(event messageEvent) bool {
	return len(forwardIDsFromMessage(event.Message)) > 0 ||
		len(forwardIDsFromCQMessage(event.RawMessage)) > 0 ||
		len(inlineForwardContents(event.Message)) > 0
}

func (b *Bridge) enrichMessageEvent(ctx context.Context, event *messageEvent) {
	if event == nil {
		return
	}
	normalizeMessageText(event)
	text := strings.TrimSpace(event.RawMessage)

	inlineBodies, inlineImages := inlineForwardExpansions(event.Message)
	forwardIDs := forwardIDsFromMessage(event.Message)
	if len(forwardIDs) == 0 && len(inlineBodies) == 0 {
		forwardIDs = forwardIDsFromCQMessage(event.RawMessage)
	}
	if len(forwardIDs) == 0 && len(inlineBodies) == 0 {
		return
	}

	parts := make([]string, 0, len(forwardIDs)+len(inlineBodies)+1)
	images := append([]string{}, inlineImages...)
	if text != "" && !looksLikeForwardOnlyCQ(text) && !isForwardPlaceholder(text) {
		parts = append(parts, text)
	}
	for _, body := range inlineBodies {
		if body == "" {
			continue
		}
		parts = append(parts, "合并转发内容：\n"+body)
	}
	for _, id := range forwardIDs {
		expansion, err := b.fetchForwardMessage(ctx, id)
		if err != nil {
			b.logf("get_forward_msg failed: id=%s error=%v", id, err)
			parts = append(parts, "[合并转发:无法读取]")
			continue
		}
		if expansion.text == "" {
			parts = append(parts, "[合并转发:空内容]")
			continue
		}
		parts = append(parts, "合并转发内容：\n"+expansion.text)
		images = append(images, expansion.imageURLs...)
	}
	event.RawMessage = strings.TrimSpace(strings.Join(parts, "\n\n"))
	event.forwardImageURLs = dedupeImageURLs(images)
}

type forwardExpansion struct {
	text      string
	imageURLs []string
}

func (b *Bridge) fetchForwardMessage(ctx context.Context, id string) (forwardExpansion, error) {
	var lastErr error
	for _, params := range []map[string]any{
		{"id": id},
		{"message_id": id},
	} {
		response, err := b.callNapCatAction(ctx, "get_forward_msg", params)
		if err != nil {
			lastErr = err
			continue
		}
		return parseForwardMessageData(response.Data), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("get_forward_msg returned no data")
	}
	return forwardExpansion{}, lastErr
}

// callNapCatAction prefers the reverse websocket (production), then HTTP.
func (b *Bridge) callNapCatAction(ctx context.Context, action string, params map[string]any) (napcatActionResponse, error) {
	var reverseErr error
	if conn, writeMu := b.activeReverseConn(); conn != nil {
		response, err := b.requestReverseAction(ctx, conn, writeMu, action, params)
		if err == nil {
			if err := napcatActionError(response); err != nil {
				return napcatActionResponse{}, err
			}
			return response, nil
		}
		reverseErr = err
	}
	endpoint := "/" + strings.TrimPrefix(action, "/")
	response, err := b.postActionResponse(ctx, endpoint, params)
	if err != nil {
		if reverseErr != nil {
			return napcatActionResponse{}, fmt.Errorf("%v; http fallback: %w", reverseErr, err)
		}
		return napcatActionResponse{}, err
	}
	if err := napcatActionError(response); err != nil {
		return napcatActionResponse{}, err
	}
	return response, nil
}

const (
	maxForwardNodes = 200
	maxForwardRunes = 50000
)

func formatForwardMessageData(raw json.RawMessage) string {
	return parseForwardMessageData(raw).text
}

func parseForwardMessageData(raw json.RawMessage) forwardExpansion {
	if len(raw) == 0 {
		return forwardExpansion{}
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return forwardExpansion{text: "[合并转发:无法解析]"}
	}
	text := strings.TrimSpace(strings.Join(forwardContentLines(payload), "\n"))
	runes := []rune(text)
	if len(runes) > maxForwardRunes {
		text = string(runes[:maxForwardRunes]) + "\n...(合并转发已截断)"
	}
	return forwardExpansion{
		text:      text,
		imageURLs: imageURLsFromForwardPayload(payload),
	}
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

func imageURLsFromForwardPayload(payload any) []string {
	switch value := payload.(type) {
	case map[string]any:
		if messages, ok := value["messages"].([]any); ok {
			return imageURLsFromForwardNodes(messages)
		}
		if message, ok := value["message"].([]any); ok {
			return imageURLsFromForwardNodes(message)
		}
		if content, ok := value["content"].([]any); ok {
			return imageURLsFromForwardNodes(content)
		}
	case []any:
		return imageURLsFromForwardNodes(value)
	}
	return nil
}

func imageURLsFromForwardNodes(nodes []any) []string {
	var urls []string
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data, _ := node["data"].(map[string]any)
		if data == nil {
			data = node
		}
		for _, candidate := range []any{data["content"], data["message"], node["message"], node["content"]} {
			urls = append(urls, imageURLsFromMessage(candidate)...)
		}
	}
	return dedupeImageURLs(urls)
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
		nickname, content := forwardNodeFields(node)
		if content == "" {
			continue
		}
		if nickname != "" {
			lines = append(lines, nickname+": "+content)
		} else {
			lines = append(lines, content)
		}
	}
	return lines
}

func forwardNodeFields(node map[string]any) (nickname, content string) {
	data, _ := node["data"].(map[string]any)
	if data == nil {
		data = node
	}

	nickname = firstNonEmpty(
		stringField(data, "nickname"),
		stringField(data, "name"),
		senderNickname(data["sender"]),
		senderNickname(node["sender"]),
		stringField(data, "user_id"),
		stringField(data, "uin"),
	)

	content = plainTextFromMessage(firstPresent(data["content"], data["message"], node["message"], node["content"]))
	if content == "" {
		content = firstNonEmpty(
			stringField(data, "raw_message"),
			stringField(node, "raw_message"),
			stringField(data, "content"),
		)
	}
	return nickname, content
}

func senderNickname(raw any) string {
	sender, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	return firstNonEmpty(stringField(sender, "nickname"), stringField(sender, "card"), stringField(sender, "user_id"))
}

func inlineForwardContents(message any) []string {
	bodies, _ := inlineForwardExpansions(message)
	return bodies
}

func inlineForwardExpansions(message any) (bodies []string, images []string) {
	for _, rawSegment := range messageSegments(message) {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		if typ != "forward" {
			continue
		}
		data, _ := segment["data"].(map[string]any)
		if data == nil {
			continue
		}
		body := strings.TrimSpace(strings.Join(forwardContentLines(data["content"]), "\n"))
		nodeImages := imageURLsFromForwardPayload(data["content"])
		if body == "" {
			body = strings.TrimSpace(strings.Join(forwardContentLines(data), "\n"))
			nodeImages = imageURLsFromForwardPayload(data)
		}
		if body != "" {
			bodies = append(bodies, body)
		}
		images = append(images, nodeImages...)
	}
	return bodies, dedupeImageURLs(images)
}

func forwardIDsFromMessage(message any) []string {
	segments := messageSegments(message)
	var ids []string
	seen := map[string]struct{}{}
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
		// Prefer fetching by id when content is absent; skip id when inline content
		// already covers the payload so we don't double-append.
		if body := strings.TrimSpace(strings.Join(forwardContentLines(data["content"]), "\n")); body != "" {
			continue
		}
		for _, key := range []string{"id", "message_id"} {
			id := strings.TrimSpace(fmt.Sprint(data[key]))
			if id == "" || id == "<nil>" {
				continue
			}
			if _, ok := seen[id]; ok {
				break
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			break
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

func isForwardPlaceholder(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "[合并转发]" || trimmed == "[转发]"
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
		case "face", "mface":
			parts = append(parts, "[表情]")
		case "reply":
			parts = append(parts, "[回复]")
		case "forward":
			// Prefer expanding nested inline content when present.
			if data != nil {
				if nested := strings.TrimSpace(strings.Join(forwardContentLines(data["content"]), "\n")); nested != "" {
					parts = append(parts, nested)
					continue
				}
			}
			parts = append(parts, "[合并转发]")
		case "json", "lightapp":
			parts = append(parts, "[卡片]")
		case "file":
			parts = append(parts, "[文件]")
		case "record":
			parts = append(parts, "[语音]")
		case "video":
			parts = append(parts, "[视频]")
		case "markdown":
			if data != nil {
				parts = append(parts, firstNonEmpty(stringField(data, "content"), stringField(data, "text")))
			}
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

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value := strings.TrimSpace(fmt.Sprint(m[key]))
	if value == "" || value == "<nil>" {
		return ""
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPresent(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		case []any:
			if len(typed) > 0 {
				return typed
			}
		case []map[string]any:
			if len(typed) > 0 {
				return typed
			}
		case map[string]any:
			if len(typed) > 0 {
				return typed
			}
		default:
			return value
		}
	}
	return nil
}
