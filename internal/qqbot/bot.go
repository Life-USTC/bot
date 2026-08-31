package qqbot

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
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/interaction/signature"
	"github.com/tencent-connect/botgo/interaction/webhook"

	"github.com/Life-USTC/Bot/internal/botapp"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const (
	defaultAPIBaseURL = "https://api.sgroup.qq.com"
	defaultTokenURL   = "https://bots.qq.com/app/getAppAccessToken"
	defaultIntents    = uint64(1<<12 | 1<<25 | 1<<26 | 1<<30)

	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opReconnect    = 7
	opInvalid      = 9
	opHello        = 10
	opHeartbeatACK = 11
)

var errGatewayReconnect = errors.New("qq bot gateway requested reconnect")

type permanentGatewayError struct {
	err error
}

func (e *permanentGatewayError) Error() string {
	return e.err.Error()
}

func (e *permanentGatewayError) Unwrap() error {
	return e.err
}

type Bot struct {
	AppID      string
	AppSecret  string
	BotToken   string
	BotID      string
	APIBaseURL string
	TokenURL   string
	GatewayURL string
	Intents    uint64
	App        botapp.Processor
	HTTPClient *http.Client
	Dialer     *websocket.Dialer
	Logger     *log.Logger
	MediaStore *responses.MediaStore

	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt time.Time
	mediaCache     qqMediaCache
	now            func() time.Time
}

type gatewayPayload struct {
	ID string          `json:"id,omitempty"`
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type gatewaySendPayload struct {
	Op int `json:"op"`
	D  any `json:"d,omitempty"`
}

type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type identifyData struct {
	Token      string            `json:"token"`
	Intents    uint64            `json:"intents"`
	Shard      []int             `json:"shard"`
	Properties map[string]string `json:"properties"`
}

type readyData struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"user"`
	Shard []int `json:"shard"`
}

type messageData struct {
	ID               string           `json:"id"`
	Content          string           `json:"content"`
	Timestamp        string           `json:"timestamp"`
	GroupOpenID      string           `json:"group_openid"`
	GroupID          string           `json:"group_id"`
	ChannelID        string           `json:"channel_id"`
	GuildID          string           `json:"guild_id"`
	Author           messageAuthor    `json:"author"`
	Attachments      []map[string]any `json:"attachments"`
	MessageReference struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
}

type interactionData struct {
	ID                string `json:"id"`
	Type              int    `json:"type"`
	Scene             string `json:"scene"`
	ChatType          int    `json:"chat_type"`
	Timestamp         string `json:"timestamp"`
	GuildID           string `json:"guild_id"`
	ChannelID         string `json:"channel_id"`
	UserOpenID        string `json:"user_openid"`
	GroupOpenID       string `json:"group_openid"`
	GroupMemberOpenID string `json:"group_member_openid"`
	Data              struct {
		Type     int `json:"type"`
		Resolved struct {
			ButtonData string `json:"button_data"`
			ButtonID   string `json:"button_id"`
			UserID     string `json:"user_id"`
			FeatureID  string `json:"feature_id"`
			MessageID  string `json:"message_id"`
		} `json:"resolved"`
	} `json:"data"`
	Version int `json:"version"`
}

type messageAuthor struct {
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	ID           string `json:"id"`
}

type incomingMessage struct {
	ID        string
	EventID   string
	ReplyToID string
	Type      string
	Text      string
	ImageURLs []string
	Identity  store.Identity
}

func (m *incomingMessage) inbound() message.Inbound {
	if m == nil {
		return message.Inbound{}
	}
	var replyTo *message.ReplyRef
	if strings.TrimSpace(m.ReplyToID) != "" {
		replyTo = &message.ReplyRef{MessageID: strings.TrimSpace(m.ReplyToID)}
	}
	return message.Inbound{
		Actor: message.Actor{Platform: m.Identity.Platform, UserID: m.Identity.UserID},
		Conversation: message.Conversation{
			Platform: m.Identity.Platform, Type: m.Identity.ConversationType, ID: m.Identity.ConversationID,
		},
		Source:  message.ReplyRef{MessageID: m.ID, EventID: m.EventID},
		ReplyTo: replyTo,
		Text:    m.Text, ImageURLs: append([]string(nil), m.ImageURLs...),
		BotMentioned: strings.Contains(m.Type, "AT_MESSAGE") || strings.HasPrefix(m.Type, "interaction:"),
	}
}

type sendMessageRequest struct {
	Content string     `json:"content,omitempty"`
	MsgType int        `json:"msg_type"`
	MsgID   string     `json:"msg_id,omitempty"`
	EventID string     `json:"event_id,omitempty"`
	MsgSeq  int        `json:"msg_seq,omitempty"`
	Media   *mediaInfo `json:"media,omitempty"`
}

type sendMessageResponse struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

type qqBotHTTPStatusError struct {
	method string
	path   string
	status int
}

func (e qqBotHTTPStatusError) Error() string {
	return fmt.Sprintf("qq bot %s %s returned %d", e.method, e.path, e.status)
}

type uncertainSendError struct {
	err error
}

// preSendError marks a failure that happened before a message-send request was
// issued. Retrying it cannot duplicate a platform message.
type preSendError struct {
	err error
}

func (e preSendError) Error() string { return e.err.Error() }
func (e preSendError) Unwrap() error { return e.err }

func (e uncertainSendError) Error() string {
	return e.err.Error()
}

func (e uncertainSendError) Unwrap() error {
	return e.err
}

type mediaInfo struct {
	FileInfo json.RawMessage `json:"file_info,omitempty"`
}

type richMediaUploadRequest struct {
	FileType   int    `json:"file_type"`
	URL        string `json:"url"`
	SrvSendMsg bool   `json:"srv_send_msg"`
}

type richMediaUploadResponse struct {
	FileInfo json.RawMessage `json:"file_info"`
	TTL      uint            `json:"ttl"`
}

func (b *Bot) Run(ctx context.Context) error {
	return b.run(ctx, retry.Backoff{
		Initial: 3 * time.Second,
		Max:     5 * time.Minute,
		Jitter:  0.2,
	})
}

func (b *Bot) run(ctx context.Context, backoff retry.Backoff) error {
	failures := 0
	for {
		ready, err := b.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var permanent *permanentGatewayError
		if errors.As(err, &permanent) {
			return permanent
		}
		if ready {
			failures = 0
		}
		delay := backoff.Duration(failures)
		b.logf("QQ bot gateway stopped: %v; reconnecting in %s", err, delay)
		if !retry.Wait(ctx, delay) {
			return nil
		}
		failures++
	}
}

func (b *Bot) RunWebhook(ctx context.Context, addr, path string) error {
	if strings.TrimSpace(b.AppSecret) == "" {
		return errors.New("QQ bot webhook requires QQ_BOT_APPSECRET")
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "0.0.0.0:2290"
	}
	path = webhookPath(path)

	mux := http.NewServeMux()
	mux.HandleFunc(path, b.ServeWebhookHTTP)
	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			b.logf("QQ bot webhook shutdown failed: %v", err)
		}
	}()
	err := server.ListenAndServe()
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (b *Bot) ServeWebhookHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		b.logf("QQ bot webhook read failed: %v", err)
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}
	b.logf("QQ bot webhook request: trace_id=%s content_length=%d",
		r.Header.Get("X-Tps-trace-ID"),
		len(body),
	)
	pass, err := signature.Verify(strings.TrimSpace(b.AppSecret), r.Header, body)
	if err != nil || !pass {
		b.logf("QQ bot webhook signature failed: pass=%v error=%v", pass, err)
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	var payload gatewayPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		b.logf("QQ bot webhook decode failed: %v", err)
		http.Error(w, "decode request body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch payload.Op {
	case int(dto.HTTPCallbackValidation):
		b.handleWebhookValidation(w, r, payload)
	case int(dto.WSHeartbeat):
		_, _ = w.Write([]byte(webhook.GenHeartbeatACK(webhookHeartbeatSeq(payload.D))))
	case int(dto.WSDispatchEvent):
		_, _ = w.Write([]byte(webhook.GenDispatchACK(true)))
		go b.handleWebhookDispatch(payload)
	default:
		b.logf("QQ bot webhook ignored opcode %d", payload.Op)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (b *Bot) handleWebhookDispatch(payload gatewayPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b.handleDispatch(ctx, payload)
}

func (b *Bot) handleWebhookValidation(w http.ResponseWriter, r *http.Request, payload gatewayPayload) {
	var req dto.WHValidationReq
	if err := json.Unmarshal(payload.D, &req); err != nil {
		b.logf("QQ bot webhook validation decode failed: %v", err)
		http.Error(w, "decode validation payload", http.StatusBadRequest)
		return
	}
	response := webhook.GenValidationACK(&req, r.Header, strings.TrimSpace(b.AppSecret))
	if len(response) == 0 {
		http.Error(w, "generate validation response", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(response)
}

func webhookHeartbeatSeq(raw json.RawMessage) uint32 {
	var seq uint32
	if err := json.Unmarshal(raw, &seq); err == nil {
		return seq
	}
	var numeric float64
	if err := json.Unmarshal(raw, &numeric); err == nil && numeric > 0 {
		return uint32(numeric)
	}
	return 0
}

func webhookPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/qqbot"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func (b *Bot) runOnce(ctx context.Context) (bool, error) {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return false, err
	}
	gatewayURL, err := b.gatewayURL(ctx, token)
	if err != nil {
		return false, err
	}
	b.logf("QQ bot connecting: app_id=%s configured_bot_id=%s intents=%d gateway=%s",
		maskID(b.AppID),
		textutil.FirstNonEmpty(b.BotID, "<empty>"),
		b.intents(),
		urlHost(gatewayURL),
	)
	dialer := b.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, resp, err := dialer.DialContext(ctx, gatewayURL, nil)
	if err != nil {
		status := "<nil>"
		if resp != nil {
			status = resp.Status
		}
		b.logf("QQ bot websocket dial failed: gateway=%s status=%s error=%v", urlHost(gatewayURL), status, err)
		if resp != nil && isPermanentHTTPStatus(resp.StatusCode) {
			return false, &permanentGatewayError{err: fmt.Errorf("QQ bot websocket rejected credentials with status %d", resp.StatusCode)}
		}
		return false, err
	}
	status := "<nil>"
	if resp != nil {
		status = resp.Status
	}
	b.logf("QQ bot websocket connected: gateway=%s status=%s local=%s remote=%s", urlHost(gatewayURL), status, conn.LocalAddr(), conn.RemoteAddr())
	defer func() { _ = conn.Close() }()

	var hello gatewayPayload
	if err := conn.ReadJSON(&hello); err != nil {
		b.logf("QQ bot websocket read hello failed: %v", err)
		return false, err
	}
	if hello.Op != opHello {
		return false, fmt.Errorf("expected QQ bot hello opcode %d, got %d", opHello, hello.Op)
	}
	var helloBody helloData
	if err := json.Unmarshal(hello.D, &helloBody); err != nil {
		return false, fmt.Errorf("decode QQ bot hello: %w", err)
	}
	interval := time.Duration(helloBody.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 45 * time.Second
	}
	b.logf("QQ bot hello: heartbeat_interval=%s", interval)

	var seq atomic.Int64
	var writeMu sync.Mutex
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go b.heartbeat(ctx, conn, &writeMu, &seq, interval, heartbeatDone)

	identify := gatewaySendPayload{
		Op: opIdentify,
		D: identifyData{
			Token:   "QQBot " + token,
			Intents: b.intents(),
			Shard:   []int{0, 1},
			Properties: map[string]string{
				"$os":      "linux",
				"$browser": "life-ustc-bot",
				"$device":  "life-ustc-bot",
			},
		},
	}
	if err := writeGatewayJSON(conn, &writeMu, identify); err != nil {
		b.logf("QQ bot websocket identify failed: %v", err)
		return false, err
	}

	ready := false
	for {
		var payload gatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			b.logf("QQ bot websocket read failed: %v", err)
			return ready, err
		}
		if payload.S != nil {
			seq.Store(*payload.S)
		}
		switch payload.Op {
		case opDispatch:
			if payload.T == "READY" {
				ready = true
			}
			b.handleDispatch(ctx, payload)
		case opHeartbeat:
			b.logf("QQ bot received heartbeat request: seq=%d", seq.Load())
			if err := b.sendHeartbeat(conn, &writeMu, seq.Load()); err != nil {
				return ready, err
			}
		case opReconnect:
			b.logf("QQ bot received reconnect opcode")
			return ready, errGatewayReconnect
		case opInvalid:
			b.logf("QQ bot received invalid session: data=%s", jsonPreview(payload.D))
			return ready, errors.New("qq bot gateway invalid session")
		case opHeartbeatACK:
		default:
			b.logf("ignored QQ bot gateway opcode %d", payload.Op)
		}
	}
}

func (b *Bot) heartbeat(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, seq *atomic.Int64, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := b.sendHeartbeat(conn, writeMu, seq.Load()); err != nil {
				b.logf("send QQ bot heartbeat failed: %v", err)
				return
			}
		}
	}
}

func (b *Bot) sendHeartbeat(conn *websocket.Conn, writeMu *sync.Mutex, seq int64) error {
	var data any
	if seq > 0 {
		data = seq
	}
	payload := gatewaySendPayload{Op: opHeartbeat, D: data}
	return writeGatewayJSON(conn, writeMu, payload)
}

func writeGatewayJSON(conn *websocket.Conn, writeMu *sync.Mutex, payload gatewaySendPayload) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return conn.WriteJSON(payload)
}

func (b *Bot) handleDispatch(ctx context.Context, payload gatewayPayload) {
	switch payload.T {
	case "READY":
		b.logReady(payload)
	case "RESUMED":
		b.logf("QQ bot gateway resumed")
	case "C2C_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE", "AT_MESSAGE_CREATE", "DIRECT_MESSAGE_CREATE":
		message, err := b.messageFromPayload(payload)
		if err != nil {
			b.logf("decode QQ bot message failed: %v", err)
			return
		}
		b.logf("QQ bot message: event=%s conversation_type=%s user_id=%q conversation_id=%q",
			payload.T,
			message.Identity.ConversationType,
			message.Identity.UserID,
			message.Identity.ConversationID,
		)
		b.processInbound(ctx, message)
	case "INTERACTION_CREATE":
		b.handleInteraction(ctx, payload)
	default:
		b.logf("QQ bot ignored dispatch event=%s", payload.T)
	}
}

func (b *Bot) handleInteraction(ctx context.Context, payload gatewayPayload) {
	message, err := b.interactionFromPayload(payload)
	if err != nil {
		b.logf("decode QQ bot interaction failed: %v", err)
		return
	}
	b.logf("QQ bot interaction: type=%s conversation_type=%s user_id=%q conversation_id=%q",
		message.Type,
		message.Identity.ConversationType,
		message.Identity.UserID,
		message.Identity.ConversationID,
	)
	if err := b.ackInteraction(ctx, message.EventID, 0); err != nil {
		b.logf("ack QQ bot interaction failed: %v", err)
	}
	b.processInbound(ctx, message)
}

func (b *Bot) interactionFromPayload(payload gatewayPayload) (*incomingMessage, error) {
	var data interactionData
	if err := json.Unmarshal(payload.D, &data); err != nil {
		return nil, err
	}
	eventID := textutil.FirstNonEmpty(data.ID, payload.ID)
	if eventID == "" {
		return nil, errors.New("qq bot interaction has empty event id")
	}
	text := strings.TrimSpace(textutil.FirstNonEmpty(data.Data.Resolved.ButtonData, data.Data.Resolved.FeatureID))
	if text == "" && data.Type == 12 {
		text = "help"
	}
	ident, err := interactionIdentity(data)
	if err != nil {
		return nil, err
	}
	return &incomingMessage{
		EventID:   eventID,
		ReplyToID: strings.TrimSpace(data.Data.Resolved.MessageID),
		Type:      fmt.Sprintf("interaction:%d", data.Type),
		Text:      b.cleanContent(text),
		Identity:  ident,
	}, nil
}

func interactionIdentity(data interactionData) (store.Identity, error) {
	switch {
	case strings.EqualFold(data.Scene, "c2c") || data.ChatType == 2:
		userID := strings.TrimSpace(data.UserOpenID)
		if userID == "" {
			return store.Identity{}, errors.New("qq bot c2c interaction has empty user openid")
		}
		return store.Identity{
			Platform:         "qqbot",
			UserID:           userID,
			ConversationType: "private",
			ConversationID:   userID,
		}, nil
	case strings.EqualFold(data.Scene, "group") || data.ChatType == 1:
		userID := strings.TrimSpace(data.GroupMemberOpenID)
		if userID == "" {
			return store.Identity{}, errors.New("qq bot group interaction has empty member openid")
		}
		groupID := strings.TrimSpace(data.GroupOpenID)
		if groupID == "" {
			return store.Identity{}, errors.New("qq bot group interaction has empty group openid")
		}
		return store.Identity{
			Platform:         "qqbot",
			UserID:           userID,
			ConversationType: "group",
			ConversationID:   groupID,
		}, nil
	case strings.EqualFold(data.Scene, "guild") || data.ChatType == 0:
		userID := strings.TrimSpace(data.Data.Resolved.UserID)
		if userID == "" {
			return store.Identity{}, errors.New("qq bot guild interaction has empty user id")
		}
		channelID := strings.TrimSpace(data.ChannelID)
		if channelID == "" {
			return store.Identity{}, errors.New("qq bot guild interaction has empty channel id")
		}
		return store.Identity{
			Platform:         "qqbot",
			UserID:           userID,
			ConversationType: "channel",
			ConversationID:   channelID,
		}, nil
	default:
		return store.Identity{}, fmt.Errorf("unsupported qq bot interaction scene=%q chat_type=%d", data.Scene, data.ChatType)
	}
}

func (b *Bot) logReady(payload gatewayPayload) {
	var ready readyData
	if err := json.Unmarshal(payload.D, &ready); err != nil {
		b.logf("QQ bot gateway ready: decode READY failed: %v", err)
		return
	}
	b.logf("QQ bot gateway ready: user_id=%s username=%q bot=%v session_id=%s shard=%v",
		ready.User.ID,
		ready.User.Username,
		ready.User.Bot,
		maskID(ready.SessionID),
		ready.Shard,
	)
}

func (b *Bot) messageFromPayload(payload gatewayPayload) (*incomingMessage, error) {
	var data messageData
	if err := json.Unmarshal(payload.D, &data); err != nil {
		return nil, err
	}
	messageID := textutil.FirstNonEmpty(data.ID, payload.ID)
	replyToID := strings.TrimSpace(data.MessageReference.MessageID)
	text := b.cleanContent(data.Content)
	imageURLs := attachmentImageURLs(data.Attachments)
	switch payload.T {
	case "C2C_MESSAGE_CREATE":
		userID := textutil.FirstNonEmpty(data.Author.UserOpenID, data.Author.ID)
		if userID == "" {
			return nil, errors.New("qq bot c2c message has empty user openid")
		}
		return &incomingMessage{
			ID:        messageID,
			ReplyToID: replyToID,
			Type:      payload.T,
			Text:      text,
			ImageURLs: imageURLs,
			Identity: store.Identity{
				Platform:         "qqbot",
				UserID:           userID,
				ConversationType: "private",
				ConversationID:   userID,
			},
		}, nil
	case "GROUP_AT_MESSAGE_CREATE":
		userID := textutil.FirstNonEmpty(data.Author.MemberOpenID, data.Author.ID)
		if userID == "" {
			return nil, errors.New("qq bot group message has empty member openid")
		}
		groupID := textutil.FirstNonEmpty(data.GroupOpenID, data.GroupID)
		if strings.TrimSpace(groupID) == "" {
			return nil, errors.New("qq bot group message has empty group openid")
		}
		return &incomingMessage{
			ID:        messageID,
			ReplyToID: replyToID,
			Type:      payload.T,
			Text:      text,
			ImageURLs: imageURLs,
			Identity: store.Identity{
				Platform:         "qqbot",
				UserID:           userID,
				ConversationType: "group",
				ConversationID:   strings.TrimSpace(groupID),
			},
		}, nil
	case "AT_MESSAGE_CREATE":
		userID := strings.TrimSpace(data.Author.ID)
		if userID == "" {
			return nil, errors.New("qq bot channel message has empty author id")
		}
		if strings.TrimSpace(data.ChannelID) == "" {
			return nil, errors.New("qq bot channel message has empty channel id")
		}
		return &incomingMessage{
			ID:        messageID,
			ReplyToID: replyToID,
			Type:      payload.T,
			Text:      text,
			ImageURLs: imageURLs,
			Identity: store.Identity{
				Platform:         "qqbot",
				UserID:           userID,
				ConversationType: "channel",
				ConversationID:   strings.TrimSpace(data.ChannelID),
			},
		}, nil
	case "DIRECT_MESSAGE_CREATE":
		userID := strings.TrimSpace(data.Author.ID)
		if userID == "" {
			return nil, errors.New("qq bot direct message has empty author id")
		}
		if strings.TrimSpace(data.GuildID) == "" {
			return nil, errors.New("qq bot direct message has empty guild id")
		}
		return &incomingMessage{
			ID:        messageID,
			ReplyToID: replyToID,
			Type:      payload.T,
			Text:      text,
			ImageURLs: imageURLs,
			Identity: store.Identity{
				Platform:         "qqbot",
				UserID:           userID,
				ConversationType: "guild_private",
				ConversationID:   strings.TrimSpace(data.GuildID),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported QQ bot event %q", payload.T)
	}
}

func (b *Bot) cleanContent(content string) string {
	content = strings.TrimSpace(content)
	botID := strings.TrimSpace(b.BotID)
	if botID == "" {
		return content
	}
	for _, mention := range []string{"<@!" + botID + ">", "<@" + botID + ">", "@" + botID} {
		content = strings.ReplaceAll(content, mention, "")
	}
	return strings.TrimSpace(content)
}

func (b *Bot) processInbound(ctx context.Context, incoming *incomingMessage) {
	if incoming == nil {
		return
	}
	if b.App == nil {
		b.logf("QQ bot inbound application is unavailable")
		return
	}
	b.App.Process(ctx, incoming.inbound())
}

func attachmentImageURLs(attachments []map[string]any) []string {
	urls := make([]string, 0, min(len(attachments), 4))
	for _, attachment := range attachments {
		contentType := strings.ToLower(strings.TrimSpace(fmt.Sprint(attachment["content_type"])))
		if contentType != "" && !strings.HasPrefix(contentType, "image/") {
			continue
		}
		candidate := strings.TrimSpace(fmt.Sprint(attachment["url"]))
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") || strings.HasPrefix(candidate, "data:image/") {
			urls = append(urls, candidate)
			if len(urls) == 4 {
				break
			}
		}
	}
	return urls
}

func qqBotOutgoingMessage(ident store.Identity, message string) string {
	if !store.IsSharedConversation(ident) {
		return message
	}
	return "\n\n" + strings.TrimLeft(message, "\r\n")
}

func (b *Bot) uploadRichMedia(ctx context.Context, ident store.Identity, imageURL string) (richMediaUploadResponse, error) {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	path, err := richMediaUploadPath(ident)
	if err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	var out richMediaUploadResponse
	startedAt := time.Now()
	err = b.openAPI(ctx, http.MethodPost, path, token, richMediaUploadRequest{
		FileType:   1,
		URL:        imageURL,
		SrvSendMsg: false,
	}, &out)
	if err != nil {
		b.logf("QQ bot media upload failed: conversation_type=%q upload_ms=%d error=%v",
			ident.ConversationType, time.Since(startedAt).Milliseconds(), err)
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	if len(out.FileInfo) == 0 {
		return richMediaUploadResponse{}, preSendError{err: errors.New("qq bot rich media upload missing file_info")}
	}
	b.logf("QQ bot media uploaded: conversation_type=%q ttl_seconds=%d upload_ms=%d",
		ident.ConversationType, out.TTL, time.Since(startedAt).Milliseconds())
	return out, nil
}

func (b *Bot) sendRichMediaContentTo(ctx context.Context, ident store.Identity, fileInfo json.RawMessage, content, msgID, eventID string, msgSeq int) (store.MessageAcceptance, error) {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return store.MessageAcceptance{}, preSendError{err: err}
	}
	path, err := sendPath(ident)
	if err != nil {
		return store.MessageAcceptance{}, preSendError{err: err}
	}
	body := sendMessageRequest{
		Content: strings.TrimSpace(content),
		MsgType: 7,
		Media:   &mediaInfo{FileInfo: fileInfo},
	}
	if strings.TrimSpace(msgID) != "" {
		body.MsgID = strings.TrimSpace(msgID)
		body.MsgSeq = msgSeq
	}
	if strings.TrimSpace(eventID) != "" {
		body.EventID = strings.TrimSpace(eventID)
	}
	return b.sendMessageOpenAPI(ctx, path, token, body)
}

func richMediaUploadPath(ident store.Identity) (string, error) {
	switch textutil.LowerTrim(ident.ConversationType) {
	case "group":
		groupID := strings.TrimSpace(ident.ConversationID)
		if groupID == "" {
			return "", errors.New("qq bot group openid is empty")
		}
		return "/v2/groups/" + url.PathEscape(groupID) + "/files", nil
	case "private", "":
		openID := textutil.FirstNonEmpty(ident.ConversationID, ident.UserID)
		if openID == "" {
			return "", errors.New("qq bot user openid is empty")
		}
		return "/v2/users/" + url.PathEscape(openID) + "/files", nil
	default:
		return "", fmt.Errorf("qq bot rich media unsupported for conversation type %q", ident.ConversationType)
	}
}

func (b *Bot) sendTo(ctx context.Context, ident store.Identity, message, msgID, eventID string, msgSeq int) (store.MessageAcceptance, error) {
	if strings.TrimSpace(message) == "" {
		return store.MessageAcceptance{}, nil
	}
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return store.MessageAcceptance{}, preSendError{err: err}
	}
	path, err := sendPath(ident)
	if err != nil {
		return store.MessageAcceptance{}, preSendError{err: err}
	}
	body := sendMessageRequest{
		Content: message,
		MsgType: 0,
	}
	if strings.TrimSpace(msgID) != "" {
		body.MsgID = strings.TrimSpace(msgID)
		body.MsgSeq = msgSeq
	}
	if strings.TrimSpace(eventID) != "" {
		body.EventID = strings.TrimSpace(eventID)
	}
	return b.sendMessageOpenAPI(ctx, path, token, body)
}

func (b *Bot) sendMessageOpenAPI(ctx context.Context, path, token string, body sendMessageRequest) (store.MessageAcceptance, error) {
	var response sendMessageResponse
	if err := b.openAPI(ctx, http.MethodPost, path, token, body, &response); err != nil {
		var rejected qqBotHTTPStatusError
		if errors.As(err, &rejected) {
			return store.MessageAcceptance{}, err
		}
		return store.MessageAcceptance{}, uncertainSendError{err: err}
	}
	messageID := strings.TrimSpace(response.ID)
	if messageID == "" {
		return store.MessageAcceptance{}, uncertainSendError{err: errors.New("qq bot success response is missing message id")}
	}
	acceptedAt := time.Now().UTC()
	if timestamp := strings.TrimSpace(response.Timestamp); timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			acceptedAt = parsed.UTC()
		}
	}
	return store.MessageAcceptance{PlatformMessageID: messageID, AcceptedAt: acceptedAt}, nil
}

func (b *Bot) ackInteraction(ctx context.Context, interactionID string, code int) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return errors.New("qq bot interaction id is empty")
	}
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return err
	}
	return b.openAPI(ctx, http.MethodPut, "/interactions/"+url.PathEscape(interactionID), token, map[string]int{"code": code}, nil)
}

func sendPath(ident store.Identity) (string, error) {
	conversationType := textutil.LowerTrim(ident.ConversationType)
	switch conversationType {
	case "group":
		groupID := strings.TrimSpace(ident.ConversationID)
		if groupID == "" {
			return "", errors.New("qq bot group openid is empty")
		}
		return "/v2/groups/" + url.PathEscape(groupID) + "/messages", nil
	case "channel":
		channelID := strings.TrimSpace(ident.ConversationID)
		if channelID == "" {
			return "", errors.New("qq bot channel id is empty")
		}
		return "/channels/" + url.PathEscape(channelID) + "/messages", nil
	case "guild_private":
		guildID := strings.TrimSpace(ident.ConversationID)
		if guildID == "" {
			return "", errors.New("qq bot guild id is empty")
		}
		return "/dms/" + url.PathEscape(guildID) + "/messages", nil
	case "private", "":
		openID := textutil.FirstNonEmpty(ident.ConversationID, ident.UserID)
		if openID == "" {
			return "", errors.New("qq bot user openid is empty")
		}
		return "/v2/users/" + url.PathEscape(openID) + "/messages", nil
	default:
		return "", fmt.Errorf("unsupported qq bot conversation type %q", ident.ConversationType)
	}
}

func (b *Bot) gatewayURL(ctx context.Context, token string) (string, error) {
	if url := strings.TrimSpace(b.GatewayURL); url != "" {
		return url, nil
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := b.openAPI(ctx, http.MethodGet, "/gateway", token, nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", errors.New("qq bot gateway URL is empty")
	}
	return strings.TrimSpace(out.URL), nil
}

func (b *Bot) openAPI(ctx context.Context, method, path, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	b.logf("QQ bot openapi request: method=%s path=%s", method, path)
	req, err := http.NewRequestWithContext(ctx, method, b.apiBaseURL()+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "QQBot "+strings.TrimSpace(token))
	resp, err := b.httpClient().Do(req)
	if err != nil {
		b.logf("QQ bot openapi transport failed: method=%s path=%s error=%v", method, path, err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		b.logf("QQ bot openapi response: method=%s path=%s status=%d", method, path, resp.StatusCode)
		return qqBotHTTPStatusError{method: method, path: path, status: resp.StatusCode}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		b.logf("QQ bot openapi response: method=%s path=%s status=%d", method, path, resp.StatusCode)
		return nil
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	b.logf("QQ bot openapi response: method=%s path=%s status=%d", method, path, resp.StatusCode)
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode qq bot %s %s response: %w", method, path, err)
	}
	return nil
}

func (b *Bot) accessTokenForRequest(ctx context.Context) (string, error) {
	if strings.TrimSpace(b.AppID) == "" || strings.TrimSpace(b.AppSecret) == "" {
		token := strings.TrimSpace(b.BotToken)
		if token == "" {
			return "", &permanentGatewayError{err: errors.New("QQ bot credentials are not configured")}
		}
		b.logf("QQ bot token: using static token")
		return token, nil
	}
	b.tokenMu.Lock()
	defer b.tokenMu.Unlock()
	if b.accessToken != "" && time.Now().Before(b.tokenExpiresAt.Add(-60*time.Second)) {
		b.logf("QQ bot token: using cached app access token expires_at=%s", b.tokenExpiresAt.Format(time.RFC3339))
		return b.accessToken, nil
	}
	b.logf("QQ bot token: fetching app access token app_id=%s", maskID(b.AppID))
	token, expiresIn, err := b.fetchAccessToken(ctx)
	if err != nil {
		return "", err
	}
	b.accessToken = token
	b.tokenExpiresAt = time.Now().Add(expiresIn)
	b.logf("QQ bot token: fetched app access token expires_in=%s expires_at=%s", expiresIn, b.tokenExpiresAt.Format(time.RFC3339))
	return token, nil
}

func (b *Bot) fetchAccessToken(ctx context.Context) (string, time.Duration, error) {
	body, err := json.Marshal(map[string]string{
		"appId":        strings.TrimSpace(b.AppID),
		"clientSecret": strings.TrimSpace(b.AppSecret),
	})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.tokenURL(), bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	b.logf("QQ bot token request: endpoint=%s app_id=%s", urlHost(b.tokenURL()), maskID(b.AppID))
	resp, err := b.httpClient().Do(req)
	if err != nil {
		b.logf("QQ bot token transport failed: %v", err)
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		b.logf("QQ bot token response: status=%d", resp.StatusCode)
		err := fmt.Errorf("qq bot token endpoint returned %d", resp.StatusCode)
		if isPermanentHTTPStatus(resp.StatusCode) {
			return "", 0, &permanentGatewayError{err: err}
		}
		return "", 0, err
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	b.logf("QQ bot token response: status=%d", resp.StatusCode)
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", 0, fmt.Errorf("decode qq bot token response: %w", err)
	}
	token := strings.TrimSpace(out.AccessToken)
	if token == "" {
		return "", 0, errors.New("qq bot token response missing access_token")
	}
	expiresIn := parseExpiresIn(out.ExpiresIn)
	if expiresIn <= 0 {
		expiresIn = 2 * time.Hour
	}
	return token, expiresIn, nil
}

func isPermanentHTTPStatus(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

func parseExpiresIn(raw json.RawMessage) time.Duration {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	var secondsInt int64
	if err := json.Unmarshal(raw, &secondsInt); err == nil {
		return time.Duration(secondsInt) * time.Second
	}
	var secondsFloat float64
	if err := json.Unmarshal(raw, &secondsFloat); err == nil {
		return time.Duration(secondsFloat * float64(time.Second))
	}
	var secondsString string
	if err := json.Unmarshal(raw, &secondsString); err == nil {
		if value, err := strconv.ParseFloat(strings.TrimSpace(secondsString), 64); err == nil {
			return time.Duration(value * float64(time.Second))
		}
	}
	return 0
}

func (b *Bot) apiBaseURL() string {
	if value := textutil.TrimTrailingSlash(b.APIBaseURL); value != "" {
		return value
	}
	return defaultAPIBaseURL
}

func (b *Bot) tokenURL() string {
	if value := strings.TrimSpace(b.TokenURL); value != "" {
		return value
	}
	return defaultTokenURL
}

func (b *Bot) intents() uint64 {
	if b.Intents == 0 {
		return defaultIntents
	}
	return b.Intents
}

func (b *Bot) httpClient() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func isUncertainSendError(err error) bool {
	var target uncertainSendError
	return errors.As(err, &target)
}

func (b *Bot) logf(format string, args ...any) {
	if b.Logger != nil {
		b.Logger.Printf(format, args...)
	}
}

func jsonPreview(value any) string {
	var data []byte
	switch v := value.(type) {
	case nil:
		return "<empty>"
	case json.RawMessage:
		data = []byte(v)
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		marshaled, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("<marshal failed: %v>", err)
		}
		data = marshaled
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return "<empty>"
	}
	return trimLogText(string(data))
}

func maskID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<empty>"
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return fmt.Sprintf("len=%d", len(runes))
	}
	return fmt.Sprintf("len=%d,last4=%s", len(runes), string(runes[len(runes)-4:]))
}

func urlHost(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return "<unknown>"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func trimLogText(text string) string {
	const max = 160
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
