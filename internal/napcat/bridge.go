package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Bridge struct {
	APIURL      string
	AccessToken string
	WSURL       string
	Handler     commands.Handler
	Agent       *agent.Service
	Dispatcher  *agent.Dispatcher
	HTTPClient  *http.Client
	Logger      *log.Logger
	Renderer    responses.Renderer
	MediaStore  *responses.MediaStore

	reverseMu      sync.Mutex
	reverseConn    *websocket.Conn
	reverseWriteMu *sync.Mutex
	reverseSeq     uint64
	reversePending map[string]reversePendingAction
	reverseTimeout time.Duration
	mediaCache     napcatMediaCache
	now            func() time.Time
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

type reverseActionResult struct {
	response napcatActionResponse
	err      error
}

type reversePendingAction struct {
	conn   *websocket.Conn
	result chan reverseActionResult
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

	// Images extracted from 合并转发 payloads (not present on the top-level message).
	forwardImageURLs []string
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
			b.dispatchMessage(ctx, event, func(ctx context.Context, event messageEvent, reply commands.Response) {
				if err := b.SendResponse(ctx, event, reply); err != nil {
					b.logf("send reply failed: %v", err)
				}
			})
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
		b.handleReverseEvents(connCtx, conn, writeMu, events)
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

func (b *Bridge) handleReverseEvents(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, events <-chan messageEvent) {
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
		if strings.TrimSpace(event.RawMessage) == "" && len(event.imageURLs()) == 0 {
			continue
		}
		b.logf("reverse websocket message: message_type=%q user_id=%d group_id=%d",
			event.MessageType, event.UserID, event.GroupID)
		b.dispatchMessage(ctx, event, func(ctx context.Context, event messageEvent, reply commands.Response) {
			if err := b.sendReverseResponse(ctx, conn, writeMu, event, reply); err != nil {
				b.logf("reverse websocket send failed: %v", err)
			} else {
				b.logf("reverse websocket replied to user_id=%d group_id=%d", event.UserID, event.GroupID)
			}
		})
	}
}

func (b *Bridge) dispatchMessage(ctx context.Context, event messageEvent, send func(context.Context, messageEvent, commands.Response)) {
	if b.Dispatcher == nil {
		reply, ok := b.handleMessage(ctx, event)
		if !ok {
			b.logf("reverse websocket ignored message from user_id=%d", event.UserID)
			return
		}
		send(ctx, event, reply)
		return
	}
	reply, ok := b.Handler.HandleResponse(ctx, commands.Input{
		Text:         event.RawMessage,
		Identity:     event.identity(),
		BotMentioned: messageMentionsBot(event.RawMessage, event.SelfID),
	})
	if ok {
		send(ctx, event, reply)
		return
	}
	b.Dispatcher.Submit(b.agentInput(event), func(ctx context.Context, input agent.Input, reply commands.Response, ok bool) {
		mergedEvent := event
		mergedEvent.RawMessage = input.Text
		if !ok {
			b.recordIgnored(ctx, mergedEvent)
			b.logf("reverse websocket ignored message from user_id=%d", mergedEvent.UserID)
			return
		}
		b.recordAgentResponse(ctx, mergedEvent, reply)
		send(ctx, mergedEvent, reply)
	})
}

func (b *Bridge) handleMessage(ctx context.Context, event messageEvent) (commands.Response, bool) {
	reply, ok := b.Handler.HandleResponse(ctx, commands.Input{
		Text:         event.RawMessage,
		Identity:     event.identity(),
		BotMentioned: messageMentionsBot(event.RawMessage, event.SelfID),
	})
	if !ok {
		agentReply, agentOK := b.handleAgent(ctx, event)
		if agentOK {
			return agentReply, true
		}
	}
	if !ok {
		b.recordIgnored(ctx, event)
		return commands.Response{}, false
	}
	return reply, true
}

func (b *Bridge) handleAgent(ctx context.Context, event messageEvent) (commands.Response, bool) {
	if b.Agent == nil {
		return commands.Response{}, false
	}
	reply, ok := b.Agent.HandleResponse(ctx, b.agentInput(event))
	if !ok {
		return commands.Response{}, false
	}
	b.recordAgentResponse(ctx, event, reply)
	return reply, true
}

func (b *Bridge) agentInput(event messageEvent) agent.Input {
	return agent.Input{
		Text:       event.RawMessage,
		ImageURLs:  event.imageURLs(),
		Identity:   event.identity(),
		SendUpdate: b.SendMessage,
	}
}

func (b *Bridge) recordAgentResponse(ctx context.Context, event messageEvent, reply commands.Response) {
	b.recordInteraction(ctx, event, store.Interaction{
		RawText: event.RawMessage,
		Command: "agent",
		Handled: true,
		Reply:   reply.Text,
		Status:  store.InteractionStatusHandled,
	}, "agent")
}

func (b *Bridge) recordIgnored(ctx context.Context, event messageEvent) {
	b.recordInteraction(ctx, event, store.Interaction{
		RawText: event.RawMessage,
		Handled: false,
		Status:  store.InteractionStatusIgnored,
	}, "ignored")
}

func (b *Bridge) recordOutbound(ctx context.Context, event messageEvent, message string, receipt store.MessageAcceptance, err error) {
	errText := ""
	status := store.InteractionStatusAccepted
	if err != nil {
		errText = err.Error()
		status = store.InteractionStatusFailed
		if isUncertainSendError(err) {
			status = store.InteractionStatusUnknown
		}
	}
	b.recordInteraction(ctx, event, store.Interaction{
		Direction:         store.InteractionDirectionOutbound,
		RawText:           message,
		Handled:           true,
		Status:            status,
		Error:             errText,
		PlatformMessageID: receipt.PlatformMessageID,
		DeliveryMethod:    receipt.DeliveryMethod,
		SourceMessageID:   receipt.SourceMessageID,
		AcceptedAt:        receipt.AcceptedAt,
	}, "outbound")
	if err != nil {
		b.logf("napcat message %s: message_type=%q user_id=%d group_id=%d error=%v",
			status, event.MessageType, event.UserID, event.GroupID, err)
		return
	}
	b.logf("napcat message accepted: message_type=%q user_id=%d group_id=%d message_id=%q delivery_method=%q source_message_id=%q",
		event.MessageType, event.UserID, event.GroupID, receipt.PlatformMessageID, receipt.DeliveryMethod, receipt.SourceMessageID)
}

func isUncertainSendError(err error) bool {
	var target uncertainSendError
	return errors.As(err, &target)
}

func (b *Bridge) recordInteraction(ctx context.Context, event messageEvent, interaction store.Interaction, label string) {
	if b.Handler.Store == nil {
		return
	}
	if err := b.Handler.Store.RecordInteraction(ctx, event.identity(), interaction); err != nil {
		b.logf("record %s interaction failed: %v", label, err)
	}
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
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "[cq:at,qq=all]") {
		return true
	}
	target := fmt.Sprintf("[cq:at,qq=%d]", selfID)
	return strings.Contains(lower, target)
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

func imageURLsFromCQMessage(message string) []string {
	var urls []string
	for _, candidate := range cqAttrValues(message, "[CQ:image,", "url=", "file=") {
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") || strings.HasPrefix(candidate, "data:image/") {
			urls = append(urls, candidate)
		}
	}
	return urls
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

func (b *Bridge) Send(ctx context.Context, event messageEvent, message string) error {
	receipt, err := b.sendPayload(ctx, event, message)
	b.recordOutbound(ctx, event, message, receipt, err)
	return err
}

func (b *Bridge) SendResponse(ctx context.Context, event messageEvent, response commands.Response) error {
	if len(response.Parts) > 0 {
		for _, part := range response.Parts {
			if err := b.SendResponse(ctx, event, part); err != nil {
				return err
			}
		}
		return nil
	}
	if response.Image != nil && b.MediaStore != nil {
		if imageURL, err := b.prepareImageURL(response.Image); err == nil {
			conn, writeMu := b.activeReverseConn()
			if receipt, err := b.sendCachedImage(ctx, conn, writeMu, event, imageURL); err == nil {
				b.recordOutbound(ctx, event, response.Text, receipt, nil)
				return nil
			} else if isUncertainSendError(err) {
				b.recordOutbound(ctx, event, response.Text, store.MessageAcceptance{}, err)
				return err
			} else {
				b.logf("napcat image send failed: %v", err)
			}
		} else {
			b.logf("prepare napcat image failed: %v", err)
		}
	}
	if conn, writeMu := b.activeReverseConn(); conn != nil {
		receipt, err := b.sendReverseReply(ctx, conn, writeMu, event, response.Text)
		b.recordOutbound(ctx, event, response.Text, receipt, err)
		return err
	}
	return b.Send(ctx, event, response.Text)
}

func (b *Bridge) sendReverseResponse(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, response commands.Response) error {
	if len(response.Parts) > 0 {
		for _, part := range response.Parts {
			if err := b.sendReverseResponse(ctx, conn, writeMu, event, part); err != nil {
				return err
			}
		}
		return nil
	}
	if response.Image != nil && b.MediaStore != nil {
		if imageURL, err := b.prepareImageURL(response.Image); err == nil {
			if receipt, err := b.sendCachedImage(ctx, conn, writeMu, event, imageURL); err == nil {
				b.recordOutbound(ctx, event, response.Text, receipt, nil)
				return nil
			} else if isUncertainSendError(err) {
				b.recordOutbound(ctx, event, response.Text, store.MessageAcceptance{}, err)
				return err
			} else {
				b.logf("reverse websocket image send failed: %v", err)
			}
		} else {
			b.logf("prepare napcat image failed: %v", err)
		}
	}
	receipt, err := b.sendReverseReply(ctx, conn, writeMu, event, response.Text)
	b.recordOutbound(ctx, event, response.Text, receipt, err)
	return err
}

func (b *Bridge) prepareImageURL(img *responses.Image) (string, error) {
	if img.URL != "" {
		return img.URL, nil
	}
	data, _, _, err := b.Renderer.RenderPNG(img)
	if err != nil {
		return "", err
	}
	return b.MediaStore.PutImagePNG(img, data)
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

func (b *Bridge) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	return b.SendMessage(ctx, ident, message)
}

func (b *Bridge) SendRichMessage(ctx context.Context, ident store.Identity, message string, image *responses.Image) error {
	event, err := messageEventFromIdentity(ident)
	if err != nil {
		return err
	}
	return b.SendResponse(ctx, event, commands.Response{Text: message, Image: image})
}

func (b *Bridge) SendMessage(ctx context.Context, ident store.Identity, message string) error {
	event, err := messageEventFromIdentity(ident)
	if err != nil {
		return err
	}
	if conn, writeMu := b.activeReverseConn(); conn != nil {
		if receipt, err := b.sendReverseReply(ctx, conn, writeMu, event, message); err == nil {
			b.recordOutbound(ctx, event, message, receipt, nil)
			return nil
		} else {
			b.logf("reverse websocket login notification failed: %v", err)
			if isUncertainSendError(err) {
				b.recordOutbound(ctx, event, message, store.MessageAcceptance{}, err)
				return err
			}
		}
	}
	return b.Send(ctx, event, message)
}

func messageEventFromIdentity(ident store.Identity) (messageEvent, error) {
	event := messageEvent{MessageType: ident.ConversationType}
	if isGroupMessageType(ident.ConversationType) {
		groupID, err := parseNapCatID(ident.ConversationID, "group")
		if err != nil {
			return messageEvent{}, err
		}
		event.GroupID = groupID
		return event, nil
	}
	userID, err := parseNapCatID(textutil.FirstNonEmpty(ident.ConversationID, ident.UserID), "user")
	if err != nil {
		return messageEvent{}, err
	}
	event.UserID = userID
	return event, nil
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
	return b.reverseSeq
}

func (b *Bridge) clearReverseConn(connID uint64) {
	b.reverseMu.Lock()
	defer b.reverseMu.Unlock()
	if b.reverseSeq == connID {
		b.reverseConn = nil
		b.reverseWriteMu = nil
	}
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
		return fmt.Errorf("send failed: %s", napcatResultText(response.Message, response.Wording, response.Status))
	}
	if response.RetCode != 0 {
		return fmt.Errorf("send failed with retcode %d: %s", response.RetCode, napcatResultText(response.Message, response.Wording, response.Status))
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
	body, err := json.Marshal(payload)
	if err != nil {
		return napcatActionResponse{}, err
	}
	apiURL := textutil.TrimTrailingSlash(b.APIURL) + endpoint
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
			return napcatActionResponse{}, fmt.Errorf("napcat %s returned %d: read response body: %w", endpoint, resp.StatusCode, err)
		}
		message := strings.TrimSpace(string(respBody))
		if message != "" {
			return napcatActionResponse{}, fmt.Errorf("napcat %s returned %d: %s", endpoint, resp.StatusCode, message)
		}
		return napcatActionResponse{}, fmt.Errorf("napcat %s returned %d", endpoint, resp.StatusCode)
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
