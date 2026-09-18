package napcat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/botapp"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Bridge struct {
	APIURL      string
	AccessToken string
	WSURL       string
	App         botapp.Processor
	HTTPClient  *http.Client
	Logger      *log.Logger
	// AllowLocalMediaPaths lets a co-located NapCat hand back local download
	// paths from get_file. The attachment parser must be opted in separately;
	// without both flags the path is dropped.
	AllowLocalMediaPaths bool

	reverseMu      sync.Mutex
	reverseConn    *websocket.Conn
	reverseWriteMu *sync.Mutex
	reverseSeq     uint64
	reverseConns   map[uint64]reverseConnection
	reversePending map[string]reversePendingAction
	reverseTimeout time.Duration
}

const (
	defaultReverseActionTimeout = 10 * time.Second
	forwardEventQueueSize       = 32
	forwardEventWorkerCount     = 4
	reverseEventQueueSize       = 32
)

type napcatActionResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
	Echo    json.RawMessage `json:"echo"`
}

type napcatHTTPStatusError struct {
	endpoint string
	status   int
	message  string
}

func (e napcatHTTPStatusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("napcat %s returned %d", e.endpoint, e.status)
	}
	return fmt.Sprintf("napcat %s returned %d: %s", e.endpoint, e.status, e.message)
}

type napcatActionRejectedError struct {
	retCode int
	message string
}

func (e napcatActionRejectedError) Error() string {
	if e.retCode == 0 {
		return "send failed: " + e.message
	}
	return fmt.Sprintf("send failed with retcode %d: %s", e.retCode, e.message)
}

type reverseActionResult struct {
	response napcatActionResponse
	err      error
}

type reversePendingAction struct {
	conn   *websocket.Conn
	result chan reverseActionResult
}

type reverseConnection struct {
	conn    *websocket.Conn
	writeMu *sync.Mutex
}

type uncertainSendError struct {
	err error
}

func (e uncertainSendError) Error() string {
	return e.err.Error()
}

func (e uncertainSendError) Unwrap() error {
	return e.err
}

type messageEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	RawMessage  string `json:"raw_message"`
	Message     any    `json:"message"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	SelfID      int64  `json:"self_id"`
	Time        int64  `json:"time"`
	MessageID   int64  `json:"message_id"`
	Sender      struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`

	// Images extracted from 合并转发 payloads (not present on the top-level message).
	forwardImageURLs []string
	// Structured input extracted from 合并转发 payloads. The raw event remains
	// available for routing, while the typed tree preserves source speakers,
	// times, media and nested forwards for the agent/history layer.
	forwardedMessages  []message.ForwardedMessage
	resolvedMedia      []message.InputMedia
	sourceText         *string
	reverseTransportID string
	receivedAt         time.Time
}

func (b *Bridge) Run(ctx context.Context) error {
	if b.WSURL == "" {
		return errors.New("NAPCAT_WS_URL is empty")
	}
	return b.run(ctx, retry.Backoff{
		Initial: 3 * time.Second,
		Max:     5 * time.Minute,
		Jitter:  0.2,
	})
}

func (b *Bridge) run(ctx context.Context, backoff retry.Backoff) error {
	failures := 0
	for {
		received, err := b.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if received {
			failures = 0
		}
		delay := backoff.Duration(failures)
		b.logf("NapCat bridge stopped: %v; reconnecting in %s", err, delay)
		if !retry.Wait(ctx, delay) {
			return nil
		}
		failures++
	}
}

func (b *Bridge) runOnce(ctx context.Context) (bool, error) {
	dialer := websocket.DefaultDialer
	header := http.Header{}
	accessToken := b.accessToken()
	if accessToken != "" {
		header.Set("Authorization", "Bearer "+accessToken)
	}
	conn, _, err := dialer.DialContext(ctx, b.WSURL, header)
	if err != nil {
		return false, err
	}
	connCtx, cancel := context.WithCancel(ctx)
	events := make(chan messageEvent, forwardEventQueueSize)
	var workers sync.WaitGroup
	workers.Add(forwardEventWorkerCount)
	for range forwardEventWorkerCount {
		go func() {
			defer workers.Done()
			b.handleForwardEvents(connCtx, events)
		}()
	}
	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()
	defer func() {
		cancel()
		_ = conn.Close()
		workers.Wait()
	}()

	received := false
	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			return received, err
		}
		received = true
		b.handleIncomingEvent(connCtx, raw, func(_ context.Context, event messageEvent) {
			enqueueForwardEvent(connCtx, events, event)
		})
	}
}

func enqueueForwardEvent(ctx context.Context, events chan<- messageEvent, event messageEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *Bridge) handleForwardEvents(ctx context.Context, events <-chan messageEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			b.enrichMessageEvent(ctx, &event)
			if strings.TrimSpace(event.RawMessage) == "" && len(event.imageURLs()) == 0 {
				continue
			}
			b.processInbound(ctx, event)
		}
	}
}

func (b *Bridge) RunReverse(ctx context.Context, addr, path string) error {
	if path == "" {
		path = "/ws"
	}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	server := &http.Server{Addr: addr, Handler: mux}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			b.logf("upgrade reverse websocket failed: %v", err)
			return
		}
		go b.handleReverseConn(ctx, conn)
	})

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (b *Bridge) handleReverseConn(ctx context.Context, conn *websocket.Conn) {
	writeMu := &sync.Mutex{}
	connID := b.setReverseConn(conn, writeMu)
	defer b.clearReverseConn(connID)
	connCtx, cancel := context.WithCancel(ctx)
	defer b.failReversePending(conn, errors.New("napcat reverse websocket closed"))
	events := make(chan messageEvent, reverseEventQueueSize)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		b.handleReverseEvents(connCtx, connID, events)
	}()
	// Drain QQ's pending/doubt friend-request queue off the read loop.
	go b.approvePendingFriendRequests(connCtx)
	defer func() {
		cancel()
		_ = conn.Close()
		<-eventsDone
	}()
	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			if ctx.Err() == nil {
				b.logf("reverse websocket read failed: %v", err)
			}
			return
		}
		if b.resolveReverseAction(raw) {
			continue
		}
		b.handleIncomingEvent(connCtx, raw, func(ctx context.Context, event messageEvent) {
			select {
			case events <- event:
			case <-connCtx.Done():
			}
		})
	}
}

func (b *Bridge) handleReverseEvents(ctx context.Context, connID uint64, events <-chan messageEvent) {
	for {
		var event messageEvent
		select {
		case <-ctx.Done():
			return
		case event = <-events:
		}
		// Expand 合并转发 here (worker goroutine), not on the read loop, so
		// get_forward_msg reverse-WS replies can be delivered by resolveReverseAction.
		b.enrichMessageEvent(ctx, &event)
		event.reverseTransportID = strconv.FormatUint(connID, 10)
		if strings.TrimSpace(event.RawMessage) == "" && len(event.imageURLs()) == 0 {
			continue
		}
		b.logf("reverse websocket message: message_type=%q user_id=%d group_id=%d",
			event.MessageType, event.UserID, event.GroupID)
		b.processInbound(ctx, event)
	}
}

func (b *Bridge) processInbound(ctx context.Context, event messageEvent) {
	if b.App == nil {
		b.logf("napcat inbound application is unavailable")
		return
	}
	if isGroupMessageType(event.MessageType) && event.SelfID > 0 && event.UserID == event.SelfID {
		b.logf("napcat ignored bot's own group message: group_id=%d message_id=%d", event.GroupID, event.MessageID)
		return
	}
	b.App.Process(ctx, event.inbound())
}

func isUncertainSendError(err error) bool {
	var target uncertainSendError
	return errors.As(err, &target)
}

func (b *Bridge) logf(format string, args ...any) {
	if b.Logger != nil {
		b.Logger.Printf(format, args...)
	}
}

func messageMentionsBot(raw string, selfID int64) bool {
	if selfID <= 0 {
		return false
	}
	target := strconv.FormatInt(selfID, 10)
	for _, value := range cqAttrValues(strings.ToLower(raw), "[cq:at,", "qq=") {
		if value == target {
			return true
		}
	}
	return false
}

func (e messageEvent) identity() store.Identity {
	conversationID := fmt.Sprint(e.UserID)
	if isGroupMessageType(e.MessageType) {
		conversationID = fmt.Sprint(e.GroupID)
	}
	return store.Identity{
		Platform:         "napcat",
		UserID:           fmt.Sprint(e.UserID),
		ConversationType: e.MessageType,
		ConversationID:   conversationID,
	}
}

func (e messageEvent) inbound() message.Inbound {
	ident := e.identity()
	rawText := e.RawMessage
	mentionText := rawText
	if e.sourceText != nil {
		mentionText = *e.sourceText
		if len(e.forwardedMessages) > 0 {
			rawText = *e.sourceText
		}
	}
	var replyTo *message.ReplyRef
	if messageID := napcatReplyMessageID(e.Message, e.RawMessage); messageID != "" {
		replyTo = &message.ReplyRef{MessageID: messageID}
	}
	parts := inputPartsFromNapCatMessage(e.Message)
	mentioned := messageMentionsBot(mentionText, e.SelfID)
	if segments := messageSegments(e.Message); len(segments) > 0 {
		mentioned = false
		var outerText []string
		for _, raw := range segments {
			segment, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			data, _ := segment["data"].(map[string]any)
			if segment["type"] == "at" && e.SelfID > 0 && stringField(data, "qq") == fmt.Sprint(e.SelfID) {
				mentioned = true
			}
			if segment["type"] == "text" {
				outerText = append(outerText, stringField(data, "text"))
			}
		}
		if len(e.forwardedMessages) > 0 {
			rawText = strings.Join(outerText, "")
		}
	}
	media := inputMediaFromNapCatMessage(e.Message)
	if len(parts) == 0 {
		parts = inputPartsFromCQMessage(e.RawMessage)
	}
	media = append(media, inputMediaFromCQMessage(e.RawMessage)...)
	for _, forwarded := range e.forwardedMessages {
		media = append(media, inputMediaFromForwardedMessage(forwarded)...)
	}
	media = dedupeInputMedia(media)
	if e.resolvedMedia != nil {
		media = e.resolvedMedia
	}
	return message.Inbound{
		Actor:        message.Actor{Platform: ident.Platform, UserID: ident.UserID, DisplayName: e.displayName()},
		Conversation: message.Conversation{Platform: ident.Platform, Type: ident.ConversationType, ID: ident.ConversationID},
		Source:       message.ReplyRef{MessageID: napcatEventMessageID(e.MessageID), EventID: e.sourceEventID(), TransportID: e.reverseTransportID},
		ReplyTo:      replyTo,
		SentAt:       napcatEventTime(e.Time), ReceivedAt: e.receivedTimestamp(),
		Text: cleanNapCatMessageText(rawText), ImageURLs: e.imageURLs(),
		Parts: parts, Media: media, Forwarded: append([]message.ForwardedMessage(nil), e.forwardedMessages...),
		BotMentioned: mentioned,
	}
}

// displayName prefers the per-group card over the global nickname, which is
// how the same person appears to everyone else in that group.
func (e messageEvent) displayName() string {
	if card := strings.TrimSpace(e.Sender.Card); card != "" {
		return card
	}
	return strings.TrimSpace(e.Sender.Nickname)
}

func napcatEventTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func (e messageEvent) receivedTimestamp() time.Time {
	if !e.receivedAt.IsZero() {
		return e.receivedAt.UTC()
	}
	return time.Now().UTC()
}

var napcatCQCodeRE = regexp.MustCompile(`(?i)\[CQ:[^\]]+\]`)

func cleanNapCatMessageText(raw string) string {
	return strings.TrimSpace(napcatCQCodeRE.ReplaceAllString(raw, " "))
}

func napcatReplyMessageID(value any, raw string) string {
	for _, rawSegment := range messageSegments(value) {
		segment, ok := rawSegment.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(fmt.Sprint(segment["type"])), "reply") {
			continue
		}
		data, _ := segment["data"].(map[string]any)
		if id := stringField(data, "id"); id != "" {
			return id
		}
	}
	values := cqAttrValues(raw, "[CQ:reply,", "id=")
	if len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

func napcatEventMessageID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (e messageEvent) sourceEventID() string {
	if e.MessageID != 0 {
		return "napcat:" + strconv.FormatInt(e.MessageID, 10)
	}
	// Older NapCat events may omit message_id. Hash the immutable event fields
	// so reconnect/replay is still idempotent without using a connection ID.
	value := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s", e.PostType, e.MessageType, e.GroupID, e.UserID, e.Time, e.RawMessage)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("napcat:%x", sum[:16])
}

func (e messageEvent) imageURLs() []string {
	urls := append([]string{}, imageURLsFromMessage(e.Message)...)
	urls = append(urls, e.forwardImageURLs...)
	urls = append(urls, imageURLsFromCQMessage(e.RawMessage)...)
	urls = dedupeImageURLs(urls)
	if len(urls) > 4 {
		urls = urls[:4]
	}
	return urls
}

func imageURLsFromMessage(message any) []string {
	return imageURLsFromSegments(messageSegments(message))
}

func imageURLsFromSegments(segments []any) []string {
	if len(segments) == 0 {
		return nil
	}
	var urls []string
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(segment["type"])))
		if typ != "image" && typ != "mface" {
			continue
		}
		data, ok := segment["data"].(map[string]any)
		if !ok {
			continue
		}
		if url := imageURLFromData(data); url != "" {
			urls = append(urls, url)
		}
	}
	return urls
}

func imageURLFromData(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"url", "file", "path"} {
		candidate := strings.TrimSpace(fmt.Sprint(data[key]))
		if candidate == "" || candidate == "<nil>" {
			continue
		}
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") || strings.HasPrefix(candidate, "data:image/") {
			return candidate
		}
		if strings.HasPrefix(strings.ToLower(candidate), "base64://") {
			payload := strings.TrimSpace(candidate[len("base64://"):])
			if payload != "" {
				return "data:image/jpeg;base64," + payload
			}
		}
	}
	return ""
}

func dedupeImageURLs(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, url)
	}
	return out
}

func imageURLsFromCQMessage(raw string) []string {
	var urls []string
	for _, media := range inputMediaFromCQMessage(raw) {
		if media.Kind != message.InputMediaImage && media.Kind != message.InputMediaSticker {
			continue
		}
		if isInputMediaURL(media.URL) {
			urls = append(urls, media.URL)
		}
	}
	return dedupeImageURLs(urls)
}

func cqAttrValues(message, prefix string, keys ...string) []string {
	var values []string
	for _, part := range strings.Split(message, prefix)[1:] {
		end := strings.IndexByte(part, ']')
		if end < 0 {
			continue
		}
		fields := strings.Split(part[:end], ",")
		for _, key := range keys {
			for _, field := range fields {
				if !strings.HasPrefix(field, key) {
					continue
				}
				value := unescapeCQValue(strings.TrimSpace(strings.TrimPrefix(field, key)))
				if value != "" {
					values = append(values, value)
				}
			}
		}
	}
	return values
}

func unescapeCQValue(value string) string {
	return strings.NewReplacer(
		"&#44;", ",",
		"&#91;", "[",
		"&#93;", "]",
		"&amp;", "&",
	).Replace(value)
}

func napcatImageMessage(url string) []map[string]any {
	return []map[string]any{{
		"type": "image",
		"data": map[string]any{"file": url},
	}}
}

func (b *Bridge) sendPayload(ctx context.Context, event messageEvent, message any) (store.MessageAcceptance, error) {
	endpoint := "/send_private_msg"
	payload := map[string]any{
		"user_id": event.UserID,
		"message": message,
	}
	if isGroupMessageType(event.MessageType) {
		endpoint = "/send_group_msg"
		payload["group_id"] = event.GroupID
		delete(payload, "user_id")
	}
	return b.post(ctx, endpoint, payload)
}

func parseNapCatID(value, kind string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid napcat %s id %q: %w", kind, value, err)
	}
	return id, nil
}

func (b *Bridge) setReverseConn(conn *websocket.Conn, writeMu *sync.Mutex) uint64 {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	b.reverseSeq++
	b.reverseConn = conn
	b.reverseWriteMu = writeMu
	if b.reverseConns == nil {
		b.reverseConns = make(map[uint64]reverseConnection)
	}
	b.reverseConns[b.reverseSeq] = reverseConnection{conn: conn, writeMu: writeMu}
	return b.reverseSeq
}

func (b *Bridge) clearReverseConn(connID uint64) {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	delete(b.reverseConns, connID)
	if b.reverseSeq == connID {
		b.reverseConn = nil
		b.reverseWriteMu = nil
	}
}

func (b *Bridge) reverseConnByTransportID(transportID string) (*websocket.Conn, *sync.Mutex) {
	id, err := strconv.ParseUint(strings.TrimSpace(transportID), 10, 64)
	if err != nil || id == 0 {
		return nil, nil
	}
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	connection := b.reverseConns[id]
	return connection.conn, connection.writeMu
}

func (b *Bridge) reverseConnForReply(ref *message.ReplyRef) (*websocket.Conn, *sync.Mutex) {
	if ref != nil {
		if conn, writeMu := b.reverseConnByTransportID(ref.TransportID); conn != nil {
			return conn, writeMu
		}
	}
	return b.activeReverseConn()
}

func (b *Bridge) activeReverseConn() (*websocket.Conn, *sync.Mutex) {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	return b.reverseConn, b.reverseWriteMu
}

func (b *Bridge) addReversePending(echo string, conn *websocket.Conn, result chan reverseActionResult) {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	if b.reversePending == nil {
		b.reversePending = map[string]reversePendingAction{}
	}
	b.reversePending[echo] = reversePendingAction{conn: conn, result: result}
}

func (b *Bridge) removeReversePending(echo string, result chan reverseActionResult) {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	pending, ok := b.reversePending[echo]
	if ok && pending.result == result {
		delete(b.reversePending, echo)
	}
}

func (b *Bridge) resolveReverseAction(raw json.RawMessage) bool {
	var response napcatActionResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return false
	}
	echo := napcatEcho(response.Echo)
	if echo == "" {
		return false
	}
	b.reverseMu.Lock()
	pending, ok := b.reversePending[echo]
	if ok {
		delete(b.reversePending, echo)
	}
	b.reverseMu.Unlock()
	if ok {
		pending.result <- reverseActionResult{response: response}
	}
	return true
}

func (b *Bridge) failReversePending(conn *websocket.Conn, err error) {
	b.reverseMu.Lock()
	pending := make([]reversePendingAction, 0)
	for echo, item := range b.reversePending {
		if item.conn == conn {
			pending = append(pending, item)
			delete(b.reversePending, echo)
		}
	}
	b.reverseMu.Unlock()
	for _, item := range pending {
		item.result <- reverseActionResult{err: err}
	}
}

func napcatEcho(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

var echoCounter uint64

func (b *Bridge) sendReversePayload(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, message any) (store.MessageAcceptance, error) {
	action := "send_private_msg"
	params := map[string]any{
		"user_id": event.UserID,
		"message": message,
	}
	if isGroupMessageType(event.MessageType) {
		action = "send_group_msg"
		params = map[string]any{
			"group_id": event.GroupID,
			"message":  message,
		}
	}
	response, err := b.requestReverseAction(ctx, conn, writeMu, action, params)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	return napcatAcceptance(response)
}

func (b *Bridge) requestReverseAction(
	ctx context.Context,
	conn *websocket.Conn,
	writeMu *sync.Mutex,
	action string,
	params map[string]any,
) (napcatActionResponse, error) {
	echo := fmt.Sprintf("life-ustc-%d", atomic.AddUint64(&echoCounter, 1))
	frame := map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	result := make(chan reverseActionResult, 1)
	b.addReversePending(echo, conn, result)
	defer b.removeReversePending(echo, result)
	if err := writeReverseAction(conn, writeMu, frame); err != nil {
		return napcatActionResponse{}, uncertainSendError{err: fmt.Errorf("write napcat reverse action: %w", err)}
	}

	timeout := b.reverseTimeout
	if timeout <= 0 {
		timeout = defaultReverseActionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return napcatActionResponse{}, uncertainSendError{err: fmt.Errorf("wait for napcat reverse action: %w", ctx.Err())}
	case <-timer.C:
		return napcatActionResponse{}, uncertainSendError{err: errors.New("napcat reverse action response timeout")}
	case outcome := <-result:
		if outcome.err != nil {
			return napcatActionResponse{}, uncertainSendError{err: outcome.err}
		}
		return outcome.response, nil
	}
}

func writeReverseAction(conn *websocket.Conn, writeMu *sync.Mutex, frame any) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	return conn.WriteJSON(frame)
}

func (b *Bridge) sendReverseReply(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, message string) (store.MessageAcceptance, error) {
	return b.sendReversePayload(ctx, conn, writeMu, event, message)
}

func napcatAcceptance(response napcatActionResponse) (store.MessageAcceptance, error) {
	if err := napcatActionError(response); err != nil {
		return store.MessageAcceptance{}, err
	}
	messageID := napcatMessageID(response.Data)
	if messageID == "" {
		return store.MessageAcceptance{}, uncertainSendError{err: errors.New("napcat success response is missing message_id")}
	}
	return store.MessageAcceptance{
		PlatformMessageID: messageID,
		AcceptedAt:        time.Now().UTC(),
	}, nil
}

func napcatActionError(response napcatActionResponse) error {
	if response.Status != "" && response.Status != "ok" {
		return napcatActionRejectedError{message: napcatResultText(response.Message, response.Wording, response.Status)}
	}
	if response.RetCode != 0 {
		return napcatActionRejectedError{retCode: response.RetCode, message: napcatResultText(response.Message, response.Wording, response.Status)}
	}
	return nil
}

func napcatMessageID(data json.RawMessage) string {
	var payload struct {
		MessageID json.RawMessage `json:"message_id"`
	}
	if len(bytes.TrimSpace(data)) == 0 || json.Unmarshal(data, &payload) != nil {
		return ""
	}
	raw := bytes.TrimSpace(payload.MessageID)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

func isGroupMessageType(messageType string) bool {
	return textutil.TrimEqualFold(messageType, "group")
}

func (b *Bridge) post(ctx context.Context, endpoint string, payload map[string]any) (store.MessageAcceptance, error) {
	result, err := b.postActionResponse(ctx, endpoint, payload)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	receipt, err := napcatAcceptance(result)
	if err != nil {
		return store.MessageAcceptance{}, fmt.Errorf("napcat %s: %w", endpoint, err)
	}
	return receipt, nil
}

func (b *Bridge) postActionResponse(ctx context.Context, endpoint string, payload map[string]any) (napcatActionResponse, error) {
	baseURL := textutil.TrimTrailingSlash(b.APIURL)
	if baseURL == "" {
		return napcatActionResponse{}, errors.New("napcat HTTP API is not configured and reverse websocket is unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return napcatActionResponse{}, err
	}
	apiURL := baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return napcatActionResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	accessToken := b.accessToken()
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		q := url.Values{"access_token": []string{accessToken}}
		req.URL.RawQuery = q.Encode()
	}
	client := b.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return napcatActionResponse{}, uncertainSendError{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return napcatActionResponse{}, napcatHTTPStatusError{
				endpoint: endpoint,
				status:   resp.StatusCode,
				message:  "read response body: " + err.Error(),
			}
		}
		message := strings.TrimSpace(string(respBody))
		return napcatActionResponse{}, napcatHTTPStatusError{endpoint: endpoint, status: resp.StatusCode, message: message}
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return napcatActionResponse{}, uncertainSendError{err: err}
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return napcatActionResponse{}, uncertainSendError{err: fmt.Errorf("napcat %s returned an empty success response", endpoint)}
	}
	var result napcatActionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return napcatActionResponse{}, uncertainSendError{err: fmt.Errorf("napcat %s returned invalid JSON: %w", endpoint, err)}
	}
	return result, nil
}

func (b *Bridge) accessToken() string {
	return strings.TrimSpace(b.AccessToken)
}

func napcatResultText(message, wording, status string) string {
	return textutil.FirstNonEmpty(message, wording, status, "unknown")
}
