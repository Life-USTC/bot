package napcat

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/message"
)

type requestEvent struct {
	PostType    string `json:"post_type"`
	RequestType string `json:"request_type"`
	SubType     string `json:"sub_type"`
	Flag        string `json:"flag"`
	UserID      int64  `json:"user_id"`
	GroupID     int64  `json:"group_id"`
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
		event.receivedAt = time.Now().UTC()
		if !isPrivateOrGroupMessage(event.MessageType) {
			return
		}
		if isGroupMessageType(event.MessageType) && event.SelfID > 0 && event.UserID == event.SelfID {
			return
		}
		// Local-only prep here. Network enrichment (get_forward_msg) must not run on
		// the websocket read loop — reverse action replies need that loop free.
		normalizeMessageText(&event)
		if isFriendRequestTipMessage(event.RawMessage) {
			// QQ often surfaces pending friend requests as private tip text instead of
			// (or in addition to) post_type=request. Handle off the read loop.
			go b.handleFriendRequestTip(context.WithoutCancel(ctx), event)
			return
		}
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
		// Approve off the read loop so reverse-WS action replies can complete.
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
	if strings.EqualFold(strings.TrimSpace(event.RequestType), "group") {
		if !strings.EqualFold(strings.TrimSpace(event.SubType), "invite") {
			return
		}
		flag := strings.TrimSpace(event.Flag)
		if flag == "" {
			b.logf("group invitation missing flag: group_id=%d user_id=%d", event.GroupID, event.UserID)
			return
		}
		if _, err := b.callNapCatAction(ctx, "set_group_add_request", map[string]any{
			"flag": flag, "sub_type": "invite", "approve": true,
		}); err != nil {
			b.logf("auto-approve group invitation failed: group_id=%d user_id=%d error=%v", event.GroupID, event.UserID, err)
			return
		}
		b.logf("auto-approved group invitation: group_id=%d user_id=%d", event.GroupID, event.UserID)
		return
	}
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

func isFriendRequestTipMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	switch trimmed {
	case "请求添加你为好友", "请求添加您为好友":
		return true
	default:
		return strings.Contains(trimmed, "请求添加你为好友") || strings.Contains(trimmed, "请求添加您为好友")
	}
}

func (b *Bridge) handleFriendRequestTip(ctx context.Context, event messageEvent) {
	b.logf("friend request tip message: user_id=%d time=%d", event.UserID, event.Time)
	if event.Time > 0 {
		if ok := b.tryApproveFriendRequestFlags(ctx, event.UserID, friendRequestFlagCandidates(event.Time)); ok {
			return
		}
	}
	// Tip text alone is not enough when time is missing; refresh the doubt queue too.
	b.approveDoubtFriendRequests(ctx)
}

func friendRequestFlagCandidates(center int64) []string {
	if center <= 0 {
		return nil
	}
	const window = 3
	out := make([]string, 0, window*2+1)
	for delta := int64(-window); delta <= window; delta++ {
		out = append(out, fmt.Sprintf("%d", center+delta))
	}
	return out
}

func (b *Bridge) tryApproveFriendRequestFlags(ctx context.Context, userID int64, flags []string) bool {
	for _, flag := range flags {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			continue
		}
		if err := b.setFriendAddRequest(ctx, flag, true); err != nil {
			continue
		}
		b.logf("auto-approved friend request: user_id=%d flag=%s", userID, flag)
		return true
	}
	b.logf("friend request tip approve missed: user_id=%d candidates=%d", userID, len(flags))
	return false
}

func (b *Bridge) setFriendAddRequest(ctx context.Context, flag string, approve bool) error {
	_, err := b.callNapCatAction(ctx, "set_friend_add_request", map[string]any{
		"flag":    flag,
		"approve": approve,
	})
	return err
}

type doubtFriendRequest struct {
	UserID   int64  `json:"user_id"`
	Uin      int64  `json:"uin"`
	Nickname string `json:"nickname"`
	Nick     string `json:"nick"`
	Flag     string `json:"flag"`
	Reason   string `json:"reason"`
}

func (r doubtFriendRequest) displayUserID() int64 {
	if r.UserID != 0 {
		return r.UserID
	}
	return r.Uin
}

func (r doubtFriendRequest) displayNickname() string {
	if strings.TrimSpace(r.Nickname) != "" {
		return r.Nickname
	}
	return r.Nick
}

// approvePendingFriendRequests drains NapCat pending friend-request queues.
// Regular request events are handled by handleRequestEvent; tip messages are
// handled by handleFriendRequestTip. This covers the backlog that still sits
// in QQ when those paths were missed.
func (b *Bridge) approvePendingFriendRequests(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}

	b.approveDoubtFriendRequests(ctx)
}

func (b *Bridge) approveDoubtFriendRequests(ctx context.Context) {
	response, err := b.callNapCatAction(ctx, "get_doubt_friends_add_request", map[string]any{
		"count": 500,
	})
	if err != nil {
		b.logf("list pending friend requests failed: %v", err)
		return
	}
	b.logf("doubt friend request raw: %s", string(response.Data))
	items, err := parseDoubtFriendRequests(response.Data)
	if err != nil {
		b.logf("parse pending friend requests failed: %v data=%s", err, string(response.Data))
		return
	}
	if len(items) == 0 {
		b.logf("no pending doubt friend requests")
		return
	}
	b.logf("pending doubt friend requests: count=%d", len(items))
	for _, item := range items {
		flag := strings.TrimSpace(item.Flag)
		if flag == "" {
			b.logf("pending friend request missing flag: user_id=%d", item.displayUserID())
			continue
		}
		if err := b.approveDoubtFriendRequest(ctx, flag); err != nil {
			b.logf("auto-approve pending friend request failed: user_id=%d nickname=%q error=%v", item.displayUserID(), item.displayNickname(), err)
			continue
		}
		b.logf("auto-approved pending friend request: user_id=%d nickname=%q", item.displayUserID(), item.displayNickname())
	}
}

func (b *Bridge) approveDoubtFriendRequest(ctx context.Context, flag string) error {
	_, err := b.callNapCatAction(ctx, "set_doubt_friends_add_request", map[string]any{
		"flag":    flag,
		"approve": true,
	})
	if err == nil {
		return nil
	}
	// Some NapCat builds only expose the classic OneBot approve action.
	if fallbackErr := b.setFriendAddRequest(ctx, flag, true); fallbackErr != nil {
		return fmt.Errorf("%v; set_friend_add_request: %w", err, fallbackErr)
	}
	return nil
}

func parseDoubtFriendRequests(raw json.RawMessage) ([]doubtFriendRequest, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []doubtFriendRequest
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var wrapped struct {
		Requests []doubtFriendRequest `json:"requests"`
		List     []doubtFriendRequest `json:"list"`
		Data     []doubtFriendRequest `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	switch {
	case len(wrapped.Requests) > 0:
		return wrapped.Requests, nil
	case len(wrapped.List) > 0:
		return wrapped.List, nil
	default:
		return wrapped.Data, nil
	}
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
	if event.sourceText == nil {
		raw := event.RawMessage
		event.sourceText = &raw
	}
	defer b.resolveEventMedia(ctx, event)
	text := strings.TrimSpace(event.RawMessage)

	inlineBodies, inlineImages, inlineForwarded := inlineForwardExpansions(event.Message)
	forwardIDs := forwardIDsFromMessage(event.Message)
	if len(forwardIDs) == 0 && len(inlineBodies) == 0 {
		forwardIDs = forwardIDsFromCQMessage(event.RawMessage)
	}
	if len(forwardIDs) == 0 && len(inlineBodies) == 0 {
		return
	}

	parts := make([]string, 0, len(forwardIDs)+len(inlineBodies)+1)
	images := append([]string{}, inlineImages...)
	forwarded := append([]message.ForwardedMessage(nil), inlineForwarded...)
	b.resolveForwardReferences(ctx, forwarded, map[string]bool{}, 0)
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
		forwarded = append(forwarded, expansion.forwarded...)
	}
	event.RawMessage = strings.TrimSpace(strings.Join(parts, "\n\n"))
	event.forwardImageURLs = dedupeImageURLs(images)
	event.forwardedMessages = forwarded
}

type forwardExpansion struct {
	text      string
	imageURLs []string
	forwarded []message.ForwardedMessage
}

func (b *Bridge) fetchForwardMessage(ctx context.Context, id string) (forwardExpansion, error) {
	return b.fetchForwardAtDepth(ctx, id, map[string]bool{}, 0)
}

func (b *Bridge) fetchForwardAtDepth(ctx context.Context, id string, seen map[string]bool, depth int) (forwardExpansion, error) {
	if depth > maxForwardDepth || seen[id] {
		return forwardExpansion{}, fmt.Errorf("forward cycle or nesting limit")
	}
	seen[id] = true
	defer delete(seen, id)
	response, err := b.callNapCatAction(ctx, "get_forward_msg", map[string]any{"message_id": id})
	if err != nil {
		return forwardExpansion{}, err
	}
	expansion := parseForwardMessageData(response.Data)
	b.resolveForwardReferences(ctx, expansion.forwarded, seen, depth)
	expansion.imageURLs = nil
	for _, forward := range expansion.forwarded {
		expansion.imageURLs = append(expansion.imageURLs, imageURLsFromForwardedMessage(forward)...)
	}
	return expansion, nil
}

func (b *Bridge) resolveForwardReferences(ctx context.Context, forwards []message.ForwardedMessage, seen map[string]bool, depth int) {
	for index := range forwards {
		var parts []message.InputPart
		for _, part := range forwards[index].Parts {
			if part.ForwardID != "" {
				expansion, err := b.fetchForwardAtDepth(ctx, part.ForwardID, seen, depth+1)
				if err != nil {
					part.Text = "[合并转发:无法读取]"
					parts = append(parts, part)
					continue
				}
				for _, nested := range expansion.forwarded {
					node := nested
					parts = append(parts, message.InputPart{Type: "forward", Forward: &node})
				}
				continue
			}
			if part.Forward != nil {
				nested := []message.ForwardedMessage{*part.Forward}
				b.resolveForwardReferences(ctx, nested, seen, depth+1)
				part.Forward = &nested[0]
			}
			parts = append(parts, part)
		}
		forwards[index].Parts = parts
	}
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
	maxForwardDepth = 8
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
	return forwardExpansion{
		text:      text,
		imageURLs: imageURLsFromForwardPayload(payload),
		forwarded: forwardedMessagesFromPayload(payload),
	}
}

// forwardedMessagesFromPayload converts all known get_forward_msg shapes into
// the durable, platform-neutral tree used by the agent. It deliberately keeps
// each node's speaker and source time beside its content; flattening this here
// would make group context ambiguous and would lose nested forwards.
func forwardedMessagesFromPayload(payload any) []message.ForwardedMessage {
	return forwardedMessagesFromPayloadAtDepth(payload, 0)
}

func forwardedMessagesFromPayloadAtDepth(payload any, depth int) []message.ForwardedMessage {
	if depth > maxForwardDepth {
		return nil
	}
	nodes := forwardPayloadNodes(payload)
	if len(nodes) == 0 {
		return nil
	}
	result := make([]message.ForwardedMessage, 0, len(nodes))
	for _, rawNode := range nodes {
		if node, ok := parseForwardedNode(rawNode, depth); ok {
			result = append(result, node)
		}
	}
	return result
}

func forwardPayloadNodes(payload any) []any {
	switch value := payload.(type) {
	case []any:
		return value
	case []map[string]any:
		result := make([]any, len(value))
		for index := range value {
			result[index] = value[index]
		}
		return result
	case map[string]any:
		if value["sender"] != nil || value["user_id"] != nil || value["type"] == "node" {
			return []any{value}
		}
		for _, key := range []string{"messages", "message", "content"} {
			if nested, ok := value[key]; ok {
				if nodes := forwardPayloadNodes(nested); len(nodes) > 0 {
					return nodes
				}
			}
		}
		// An individual node is also a valid payload when a nested forward
		// stores its content directly under data.
		if _, hasContent := value["content"]; hasContent || value["type"] != nil || value["sender"] != nil || value["user_id"] != nil {
			return []any{value}
		}
	}
	return nil
}

func parseForwardedNode(raw any, depth int) (message.ForwardedMessage, bool) {
	node, ok := raw.(map[string]any)
	if !ok {
		return message.ForwardedMessage{}, false
	}
	data, _ := node["data"].(map[string]any)
	if data == nil {
		data = node
	}
	content := firstPresent(data["content"], data["message"], node["message"], node["content"])
	parts := inputPartsFromValue(content, depth+1)
	if len(parts) == 0 {
		fallback := firstNonEmpty(stringField(data, "raw_message"), stringField(node, "raw_message"))
		if fallback != "" {
			parts = inputPartsFromValue(fallback, depth+1)
		}
	}
	text := strings.TrimSpace(inputPartsText(parts))
	if text == "" {
		text = strings.TrimSpace(plainTextFromMessage(content))
	}
	actor := forwardActor(node, data)
	sentAt := forwardNodeTime(firstPresent(data["time"], node["time"]))
	if actor.UserID == "" && actor.DisplayName == "" && sentAt.IsZero() && len(parts) == 0 {
		return message.ForwardedMessage{}, false
	}
	return message.ForwardedMessage{Speaker: actor, SentAt: sentAt, Text: text, Parts: parts}, true
}

func forwardActor(node, data map[string]any) message.Actor {
	sender := firstMap(data["sender"], node["sender"])
	userID := firstNonEmpty(
		stringField(data, "user_id"), stringField(data, "uin"),
		stringField(node, "user_id"), stringField(node, "uin"),
		stringField(sender, "user_id"), stringField(sender, "uin"), stringField(sender, "id"),
	)
	displayName := firstNonEmpty(
		stringField(data, "card"), stringField(data, "nickname"), stringField(data, "name"),
		stringField(sender, "card"), stringField(sender, "nickname"), stringField(sender, "name"),
	)
	return message.Actor{Platform: "napcat", UserID: userID, DisplayName: displayName}
}

func firstMap(values ...any) map[string]any {
	for _, value := range values {
		if result, ok := value.(map[string]any); ok && result != nil {
			return result
		}
	}
	return nil
}

func forwardNodeTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case json.Number:
		if seconds, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			return napcatEventTime(seconds)
		}
	case float64:
		return napcatEventTime(int64(typed))
	case float32:
		return napcatEventTime(int64(typed))
	case int:
		return napcatEventTime(int64(typed))
	case int64:
		return napcatEventTime(typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
		if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
			return napcatEventTime(seconds)
		}
	}
	return time.Time{}
}

func inputPartsText(parts []message.InputPart) string {
	var text strings.Builder
	for _, part := range parts {
		if part.Text != "" {
			text.WriteString(part.Text)
			continue
		}
		if part.Media != nil {
			switch part.Media.Kind {
			case message.InputMediaImage:
				text.WriteString("[图片]")
			case message.InputMediaSticker:
				text.WriteString("[表情]")
			case message.InputMediaFile:
				text.WriteString("[文件]")
			case message.InputMediaAudio:
				text.WriteString("[语音]")
			case message.InputMediaVideo:
				text.WriteString("[视频]")
			default:
				text.WriteString("[附件]")
			}
		}
		if part.Forward != nil {
			text.WriteString(part.Forward.Text)
		}
	}
	return text.String()
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
	var urls []string
	for _, forwarded := range forwardedMessagesFromPayload(payload) {
		urls = append(urls, imageURLsFromForwardedMessage(forwarded)...)
	}
	return dedupeImageURLs(urls)
}

func imageURLsFromForwardedMessage(forwarded message.ForwardedMessage) []string {
	var urls []string
	for _, part := range forwarded.Parts {
		if part.Media != nil && (part.Media.Kind == message.InputMediaImage || part.Media.Kind == message.InputMediaSticker) {
			if isInputMediaURL(part.Media.URL) {
				urls = append(urls, part.Media.URL)
			}
		}
		if part.Forward != nil {
			urls = append(urls, imageURLsFromForwardedMessage(*part.Forward)...)
		}
	}
	return dedupeImageURLs(urls)
}

func inputPartsFromNapCatMessage(value any) []message.InputPart {
	return inputPartsFromValue(value, 0)
}

func inputPartsFromValue(value any, depth int) []message.InputPart {
	if depth > maxForwardDepth {
		return []message.InputPart{{Type: "forward", Text: "[合并转发:嵌套层级过深]"}}
	}
	segments := messageSegments(value)
	if len(segments) == 0 {
		if text, ok := value.(string); ok {
			return inputPartsFromCQMessage(text)
		}
		return nil
	}
	parts := make([]message.InputPart, 0, len(segments))
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		data, _ := segment["data"].(map[string]any)
		if data == nil {
			data = segment
		}
		switch typ {
		case "text":
			if text := stringField(data, "text"); text != "" {
				parts = append(parts, message.InputPart{Type: "text", Text: text})
			}
		case "image", "mface", "face", "file", "record", "video":
			media := inputMediaFromNapCatSegment(typ, data)
			parts = append(parts, message.InputPart{Type: string(media.Kind), Media: &media})
		case "forward", "node":
			nested := firstPresent(data["content"], data["message"], segment["content"], segment["message"])
			forwarded := forwardedMessagesFromPayloadAtDepth(nested, depth+1)
			if len(forwarded) == 0 {
				parts = append(parts, message.InputPart{Type: "forward", Text: "[合并转发]", ForwardID: firstNonEmpty(stringField(data, "id"), stringField(data, "message_id"))})
				continue
			}
			for index := range forwarded {
				forward := forwarded[index]
				parts = append(parts, message.InputPart{Type: "forward", Text: forward.Text, Forward: &forward})
			}
		default:
			// Cards and future segment types are still user input. Keep a neutral
			// marker and any safe media reference; never treat their payload as
			// trusted instructions.
			media := inputMediaFromNapCatSegment(typ, data)
			if media.URL != "" || media.FileID != "" {
				parts = append(parts, message.InputPart{Type: string(media.Kind), Media: &media})
			} else if typ != "" {
				parts = append(parts, message.InputPart{Type: typ, Text: "[" + typ + "]"})
			}
		}
	}
	return parts
}

func inputMediaFromNapCatMessage(value any) []message.InputMedia {
	parts := inputPartsFromNapCatMessage(value)
	media := make([]message.InputMedia, 0, len(parts))
	for _, part := range parts {
		if part.Media != nil {
			media = append(media, *part.Media)
		}
		if part.Forward != nil {
			media = append(media, inputMediaFromForwardedMessage(*part.Forward)...)
		}
	}
	return dedupeInputMedia(media)
}

func inputMediaFromForwardedMessage(forwarded message.ForwardedMessage) []message.InputMedia {
	var media []message.InputMedia
	for _, part := range forwarded.Parts {
		if part.Media != nil {
			media = append(media, *part.Media)
		}
		if part.Forward != nil {
			media = append(media, inputMediaFromForwardedMessage(*part.Forward)...)
		}
	}
	return media
}

func inputMediaFromNapCatSegment(segmentType string, data map[string]any) message.InputMedia {
	segmentType = strings.ToLower(strings.TrimSpace(segmentType))
	kind := message.InputMediaUnknown
	switch segmentType {
	case "image":
		kind = message.InputMediaImage
	case "mface", "face":
		kind = message.InputMediaSticker
	case "file":
		kind = message.InputMediaFile
	case "record":
		kind = message.InputMediaAudio
	case "video":
		kind = message.InputMediaVideo
	}
	media := message.InputMedia{Kind: kind}
	media.MIMEType = normalizeInputMIME(firstNonEmpty(stringField(data, "content_type"), stringField(data, "mime_type"), stringField(data, "type")))
	if media.MIMEType == "" {
		media.MIMEType = inputMIMEFromName(stringField(data, "name"), stringField(data, "filename"), stringField(data, "file"))
	}
	if candidate := firstNonEmpty(stringField(data, "url"), stringField(data, "path"), stringField(data, "file")); isInputMediaURL(candidate) {
		media.URL = candidate
	}
	media.Name = firstNonEmpty(stringField(data, "name"), stringField(data, "filename"))
	media.FileID = inputFileID(data)
	if segmentType == "face" && media.Name == "" {
		media.Name = "QQ 表情 #" + media.FileID
	}
	media.Size = inputMediaSize(firstPresent(data["size"], data["file_size"], data["bytes"]))
	return media
}

func inputFileID(data map[string]any) string {
	for _, key := range []string{"file_id", "emoji_id", "id"} {
		value := stringField(data, key)
		if value == "" || strings.ContainsAny(value, "/\\") || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			continue
		}
		return value
	}
	// A local NapCat `file` is a path and must never be copied into the
	// durable envelope. Its basename is useful as an attachment label.
	return ""
}

func inputMediaSize(value any) int64 {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return int64(typed)
		}
	case int64:
		if typed > 0 {
			return typed
		}
	case float64:
		if typed > 0 {
			return int64(typed)
		}
	case json.Number:
		if parsed, err := strconv.ParseInt(string(typed), 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func normalizeInputMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		return mediaType
	}
	return value
}

func inputMIMEFromName(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		ext := strings.ToLower(path.Ext(value))
		switch ext {
		case ".jpg", ".jpeg":
			return "image/jpeg"
		case ".png":
			return "image/png"
		case ".gif":
			return "image/gif"
		case ".webp":
			return "image/webp"
		case ".mp4":
			return "video/mp4"
		case ".mp3", ".wav", ".ogg":
			return "audio/" + strings.TrimPrefix(ext, ".")
		case ".pdf":
			return "application/pdf"
		case ".txt":
			return "text/plain"
		}
	}
	return ""
}

// inputPartsFromCQMessage handles the string form emitted by older OneBot
// adapters. It keeps ordinary text in order with CQ media tags and avoids
// copying local file paths into the durable envelope.
func inputPartsFromCQMessage(raw string) []message.InputPart {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	matches := napcatCQCodeRE.FindAllStringIndex(raw, -1)
	if len(matches) == 0 {
		return []message.InputPart{{Type: "text", Text: raw}}
	}
	parts := make([]message.InputPart, 0, len(matches)*2+1)
	last := 0
	for _, match := range matches {
		if match[0] > last {
			if text := strings.TrimSpace(raw[last:match[0]]); text != "" {
				parts = append(parts, message.InputPart{Type: "text", Text: text})
			}
		}
		segment := parseCQInputSegment(raw[match[0]:match[1]])
		if segment.Type != "" {
			parts = append(parts, segment)
		}
		last = match[1]
	}
	if last < len(raw) {
		if text := strings.TrimSpace(raw[last:]); text != "" {
			parts = append(parts, message.InputPart{Type: "text", Text: text})
		}
	}
	return parts
}

func inputMediaFromCQMessage(raw string) []message.InputMedia {
	parts := inputPartsFromCQMessage(raw)
	media := make([]message.InputMedia, 0, len(parts))
	for _, part := range parts {
		if part.Media != nil {
			media = append(media, *part.Media)
		}
	}
	return dedupeInputMedia(media)
}

func parseCQInputSegment(raw string) message.InputPart {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "[CQ:"), "]"))
	if inner == "" {
		return message.InputPart{}
	}
	fields := strings.Split(inner, ",")
	segmentType := strings.ToLower(strings.TrimSpace(fields[0]))
	attrs := make(map[string]string, len(fields)-1)
	for _, field := range fields[1:] {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		attrs[key] = unescapeCQValue(strings.TrimSpace(value))
	}
	if segmentType == "text" {
		return message.InputPart{Type: "text", Text: firstNonEmpty(attrs["text"], attrs["content"])}
	}
	data := make(map[string]any, len(attrs))
	for key, value := range attrs {
		data[key] = value
	}
	switch segmentType {
	case "image", "mface", "face", "file", "record", "video":
		media := inputMediaFromNapCatSegment(segmentType, data)
		return message.InputPart{Type: string(media.Kind), Media: &media}
	case "forward", "node":
		return message.InputPart{Type: "forward", Text: "[合并转发]"}
	case "reply":
		return message.InputPart{Type: "reply", Text: "[回复]"}
	case "at":
		if qq := strings.TrimSpace(attrs["qq"]); qq != "" {
			return message.InputPart{Type: "at", Text: "@" + qq}
		}
		return message.InputPart{Type: "at", Text: "[提及]"}
	default:
		if segmentType == "" {
			return message.InputPart{}
		}
		return message.InputPart{Type: segmentType, Text: "[" + segmentType + "]"}
	}
}

func isInputMediaURL(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "data:image/")
}

func dedupeInputMedia(media []message.InputMedia) []message.InputMedia {
	if len(media) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(media))
	result := make([]message.InputMedia, 0, len(media))
	for _, item := range media {
		item.Kind = message.InputMediaKind(strings.ToLower(strings.TrimSpace(string(item.Kind))))
		if item.Kind == "" {
			item.Kind = message.InputMediaUnknown
		}
		item.URL = strings.TrimSpace(item.URL)
		item.Name = strings.TrimSpace(item.Name)
		item.FileID = strings.TrimSpace(item.FileID)
		item.MIMEType = normalizeInputMIME(item.MIMEType)
		key := strings.Join([]string{string(item.Kind), item.URL, item.FileID, item.Name}, "\x00")
		if key == string(item.Kind)+"\x00\x00\x00" {
			// An unknown segment with no reference carries no durable value.
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func forwardNodeLines(nodes []any) []string {
	lines := make([]string, 0, len(nodes))
	for _, raw := range nodes {
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
	bodies, _, _ := inlineForwardExpansions(message)
	return bodies
}

func inlineForwardExpansions(message any) (bodies []string, images []string, forwarded []message.ForwardedMessage) {
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
		forwarded = append(forwarded, forwardedMessagesFromPayload(data["content"])...)
		if len(forwarded) == 0 {
			forwarded = append(forwarded, forwardedMessagesFromPayload(data)...)
		}
	}
	return bodies, dedupeImageURLs(images), forwarded
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
