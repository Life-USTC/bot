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

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
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

type Bot struct {
	AppID      string
	AppSecret  string
	BotToken   string
	BotID      string
	APIBaseURL string
	TokenURL   string
	GatewayURL string
	Intents    uint64
	Handler    commands.Handler
	Agent      *agent.Service
	HTTPClient *http.Client
	Dialer     *websocket.Dialer
	Logger     *log.Logger
	Renderer   responses.Renderer
	MediaStore *responses.MediaStore

	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt time.Time
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
	ID          string           `json:"id"`
	Content     string           `json:"content"`
	Timestamp   string           `json:"timestamp"`
	GroupOpenID string           `json:"group_openid"`
	GroupID     string           `json:"group_id"`
	ChannelID   string           `json:"channel_id"`
	GuildID     string           `json:"guild_id"`
	Author      messageAuthor    `json:"author"`
	Attachments []map[string]any `json:"attachments"`
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
	ID       string
	EventID  string
	Type     string
	Text     string
	Identity store.Identity

	replySeq uint64
}

type sendMessageRequest struct {
	Content string     `json:"content,omitempty"`
	MsgType int        `json:"msg_type"`
	MsgID   string     `json:"msg_id,omitempty"`
	EventID string     `json:"event_id,omitempty"`
	MsgSeq  int        `json:"msg_seq,omitempty"`
	Media   *mediaInfo `json:"media,omitempty"`
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
}

func (b *Bot) Run(ctx context.Context) error {
	backoff := 3 * time.Second
	for {
		if err := b.runOnce(ctx); err != nil && ctx.Err() == nil {
			b.logf("QQ bot gateway stopped: %v; reconnecting in %s", err, backoff)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
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

func (b *Bot) runOnce(ctx context.Context) error {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return err
	}
	gatewayURL, err := b.gatewayURL(ctx, token)
	if err != nil {
		return err
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
		return err
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
		return err
	}
	if hello.Op != opHello {
		return fmt.Errorf("expected QQ bot hello opcode %d, got %d", opHello, hello.Op)
	}
	var helloBody helloData
	if err := json.Unmarshal(hello.D, &helloBody); err != nil {
		return fmt.Errorf("decode QQ bot hello: %w", err)
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
		return err
	}

	for {
		var payload gatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			b.logf("QQ bot websocket read failed: %v", err)
			return err
		}
		if payload.S != nil {
			seq.Store(*payload.S)
		}
		switch payload.Op {
		case opDispatch:
			b.handleDispatch(ctx, payload)
		case opHeartbeat:
			b.logf("QQ bot received heartbeat request: seq=%d", seq.Load())
			if err := b.sendHeartbeat(conn, &writeMu, seq.Load()); err != nil {
				return err
			}
		case opReconnect:
			b.logf("QQ bot received reconnect opcode")
			return errGatewayReconnect
		case opInvalid:
			b.logf("QQ bot received invalid session: data=%s", jsonPreview(payload.D))
			return errors.New("qq bot gateway invalid session")
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
		b.logf("QQ bot message: event=%s conversation_type=%s user_id=%q conversation_id=%q text=%q",
			payload.T,
			message.Identity.ConversationType,
			message.Identity.UserID,
			message.Identity.ConversationID,
			trimLogText(message.Text),
		)
		reply, ok := b.handleMessage(ctx, message)
		if !ok {
			b.logf("QQ bot ignored message: event=%s text=%q", payload.T, trimLogText(message.Text))
			return
		}
		if err := b.SendResponse(ctx, message, reply); err != nil {
			b.recordOutbound(ctx, message.Identity, reply.Text, store.InteractionStatusFailed, err)
			b.logf("send QQ bot reply failed: %v", err)
			return
		}
		b.logf("QQ bot replied: event=%s conversation_type=%s conversation_id=%q", payload.T, message.Identity.ConversationType, message.Identity.ConversationID)
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
	b.logf("QQ bot interaction: type=%s conversation_type=%s user_id=%q conversation_id=%q text=%q",
		message.Type,
		message.Identity.ConversationType,
		message.Identity.UserID,
		message.Identity.ConversationID,
		trimLogText(message.Text),
	)
	if err := b.ackInteraction(ctx, message.EventID, 0); err != nil {
		b.logf("ack QQ bot interaction failed: %v", err)
	}
	reply, ok := b.handleMessage(ctx, message)
	if !ok {
		b.logf("QQ bot ignored interaction: type=%s text=%q", message.Type, trimLogText(message.Text))
		return
	}
	if err := b.SendResponse(ctx, message, reply); err != nil {
		b.recordOutbound(ctx, message.Identity, reply.Text, store.InteractionStatusFailed, err)
		b.logf("send QQ bot interaction reply failed: %v", err)
		return
	}
	b.logf("QQ bot replied to interaction: type=%s conversation_type=%s conversation_id=%q", message.Type, message.Identity.ConversationType, message.Identity.ConversationID)
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
		EventID:  eventID,
		Type:     fmt.Sprintf("interaction:%d", data.Type),
		Text:     b.cleanContent(text),
		Identity: ident,
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
	text := b.cleanContent(data.Content)
	switch payload.T {
	case "C2C_MESSAGE_CREATE":
		userID := textutil.FirstNonEmpty(data.Author.UserOpenID, data.Author.ID)
		if userID == "" {
			return nil, errors.New("qq bot c2c message has empty user openid")
		}
		return &incomingMessage{
			ID:   messageID,
			Type: payload.T,
			Text: text,
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
			ID:   messageID,
			Type: payload.T,
			Text: text,
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
			ID:   messageID,
			Type: payload.T,
			Text: text,
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
			ID:   messageID,
			Type: payload.T,
			Text: text,
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

func (b *Bot) handleMessage(ctx context.Context, message *incomingMessage) (commands.Response, bool) {
	reply, ok := b.Handler.HandleResponse(ctx, commands.Input{
		Text:     message.Text,
		Identity: message.Identity,
	})
	if !ok {
		agentReply, agentOK := b.handleAgent(ctx, message)
		if agentOK {
			return commands.Response{Text: agentReply, Kind: "agent"}, true
		}
	}
	if !ok {
		b.recordIgnored(ctx, message)
		return commands.Response{}, false
	}
	return reply, true
}

func (b *Bot) handleAgent(ctx context.Context, message *incomingMessage) (string, bool) {
	if b.Agent == nil {
		return "", false
	}
	reply, ok := b.Agent.Handle(ctx, agent.Input{
		Text:     message.Text,
		Identity: message.Identity,
		SendUpdate: func(ctx context.Context, _ store.Identity, update string) error {
			return b.Send(ctx, message, update)
		},
	})
	if !ok {
		return "", false
	}
	b.recordInteraction(ctx, message.Identity, store.Interaction{
		RawText: message.Text,
		Command: "agent",
		Handled: true,
		Reply:   reply,
		Status:  store.InteractionStatusHandled,
	}, "agent")
	return reply, true
}

func (b *Bot) Send(ctx context.Context, message *incomingMessage, text string) error {
	if message == nil {
		return errors.New("qq bot message is nil")
	}
	outgoing := qqBotOutgoingMessage(message.Identity, text)
	err := b.sendTo(ctx, message.Identity, outgoing, message.ID, message.EventID, message.nextReplySeq())
	if err != nil {
		return err
	}
	b.recordOutbound(ctx, message.Identity, outgoing, store.InteractionStatusSent, nil)
	return nil
}

func (b *Bot) SendResponse(ctx context.Context, message *incomingMessage, response commands.Response) error {
	if message == nil {
		return errors.New("qq bot message is nil")
	}
	if response.Image != nil && b.MediaStore != nil {
		if err := b.sendImageResponse(ctx, message, response); err == nil {
			b.recordOutbound(ctx, message.Identity, response.Text, store.InteractionStatusSent, nil)
			return nil
		} else {
			b.logf("QQ bot image response failed: %v", err)
		}
	}
	return b.Send(ctx, message, response.Text)
}

func (b *Bot) sendImageResponse(ctx context.Context, message *incomingMessage, response commands.Response) error {
	imageURL, err := b.prepareImageURL(response.Image)
	if err != nil {
		return err
	}
	fileInfo, err := b.uploadRichMedia(ctx, message.Identity, imageURL)
	if err != nil {
		return err
	}
	return b.sendRichMediaTo(ctx, message.Identity, fileInfo, message.ID, message.EventID, message.nextReplySeq())
}

func (b *Bot) prepareImageURL(img *responses.Image) (string, error) {
	if img.URL != "" {
		return img.URL, nil
	}
	data, _, _, err := b.Renderer.RenderPNG(img)
	if err != nil {
		return "", err
	}
	return b.MediaStore.PutPNG(data)
}

func (b *Bot) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	return b.SendMessage(ctx, ident, message)
}

func (b *Bot) SendRichMessage(ctx context.Context, ident store.Identity, message string, image *responses.Image) error {
	if image != nil && b.MediaStore != nil {
		imageURL, err := b.prepareImageURL(image)
		if err == nil {
			var fileInfo json.RawMessage
			fileInfo, err = b.uploadRichMedia(ctx, ident, imageURL)
			if err == nil {
				err = b.sendRichMediaTo(ctx, ident, fileInfo, "", "", 0)
			}
		}
		if err == nil {
			b.recordOutbound(ctx, ident, message, store.InteractionStatusSent, nil)
			return nil
		}
		b.logf("QQ bot proactive image response failed: %v", err)
	}
	return b.SendMessage(ctx, ident, message)
}

func (b *Bot) SendMessage(ctx context.Context, ident store.Identity, message string) error {
	outgoing := qqBotOutgoingMessage(ident, message)
	err := b.sendTo(ctx, ident, outgoing, "", "", 0)
	if err != nil {
		b.recordOutbound(ctx, ident, outgoing, store.InteractionStatusFailed, err)
		return err
	}
	b.recordOutbound(ctx, ident, outgoing, store.InteractionStatusSent, nil)
	return nil
}

func (m *incomingMessage) nextReplySeq() int {
	return int(atomic.AddUint64(&m.replySeq, 1))
}

func qqBotOutgoingMessage(ident store.Identity, message string) string {
	if !store.IsGroupConversation(ident) {
		return message
	}
	return "\n\n" + strings.TrimLeft(message, "\r\n")
}

func (b *Bot) uploadRichMedia(ctx context.Context, ident store.Identity, imageURL string) (json.RawMessage, error) {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	path, err := richMediaUploadPath(ident)
	if err != nil {
		return nil, err
	}
	var out richMediaUploadResponse
	err = b.openAPI(ctx, http.MethodPost, path, token, richMediaUploadRequest{
		FileType:   1,
		URL:        imageURL,
		SrvSendMsg: false,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.FileInfo) == 0 {
		return nil, errors.New("qq bot rich media upload missing file_info")
	}
	return out.FileInfo, nil
}

func (b *Bot) sendRichMediaTo(ctx context.Context, ident store.Identity, fileInfo json.RawMessage, msgID, eventID string, msgSeq int) error {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return err
	}
	path, err := sendPath(ident)
	if err != nil {
		return err
	}
	body := sendMessageRequest{
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
	return b.openAPI(ctx, http.MethodPost, path, token, body, nil)
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

func (b *Bot) sendTo(ctx context.Context, ident store.Identity, message, msgID, eventID string, msgSeq int) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return err
	}
	path, err := sendPath(ident)
	if err != nil {
		return err
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
	return b.openAPI(ctx, http.MethodPost, path, token, body, nil)
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
	bodyPreview := "<empty>"
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyPreview = jsonPreview(data)
		reader = bytes.NewReader(data)
	}
	b.logf("QQ bot openapi request: method=%s path=%s body=%s", method, path, bodyPreview)
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
		responseText := readBodyText(resp.Body)
		b.logf("QQ bot openapi response: method=%s path=%s status=%d body=%s", method, path, resp.StatusCode, responseText)
		return fmt.Errorf("qq bot %s %s returned %d: %s", method, path, resp.StatusCode, responseText)
	}
	if out == nil {
		responseText := readBodyText(resp.Body)
		b.logf("QQ bot openapi response: method=%s path=%s status=%d body=%s", method, path, resp.StatusCode, responseText)
		return nil
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	b.logf("QQ bot openapi response: method=%s path=%s status=%d body=%s", method, path, resp.StatusCode, jsonPreview(respBody))
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode qq bot %s %s response: %w", method, path, err)
	}
	return nil
}

func (b *Bot) accessTokenForRequest(ctx context.Context) (string, error) {
	if strings.TrimSpace(b.AppID) == "" || strings.TrimSpace(b.AppSecret) == "" {
		token := strings.TrimSpace(b.BotToken)
		if token == "" {
			return "", errors.New("QQ bot credentials are not configured")
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
		responseText := readBodyText(resp.Body)
		b.logf("QQ bot token response: status=%d body=%s", resp.StatusCode, responseText)
		return "", 0, fmt.Errorf("qq bot token endpoint returned %d: %s", resp.StatusCode, responseText)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	b.logf("QQ bot token response: status=%d body=%s", resp.StatusCode, redactTokenJSON(respBody))
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

func (b *Bot) recordIgnored(ctx context.Context, message *incomingMessage) {
	if message == nil {
		return
	}
	b.recordInteraction(ctx, message.Identity, store.Interaction{
		RawText: message.Text,
		Handled: false,
		Status:  store.InteractionStatusIgnored,
	}, "ignored")
}

func (b *Bot) recordOutbound(ctx context.Context, ident store.Identity, message, status string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	b.recordInteraction(ctx, ident, store.Interaction{
		Direction: store.InteractionDirectionOutbound,
		RawText:   message,
		Handled:   true,
		Status:    status,
		Error:     errText,
	}, "outbound")
}

func (b *Bot) recordInteraction(ctx context.Context, ident store.Identity, interaction store.Interaction, label string) {
	if b.Handler.Store == nil {
		return
	}
	if err := b.Handler.Store.RecordInteraction(ctx, ident, interaction); err != nil {
		b.logf("record QQ bot %s interaction failed: %v", label, err)
	}
}

func (b *Bot) logf(format string, args ...any) {
	if b.Logger != nil {
		b.Logger.Printf(format, args...)
	}
}

func readBodyText(body io.Reader) string {
	data, err := io.ReadAll(body)
	if err != nil {
		return "read response body: " + err.Error()
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "empty response body"
	}
	return text
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

func redactTokenJSON(data []byte) string {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return jsonPreview(data)
	}
	redactTokenFields(value)
	return jsonPreview(value)
}

func redactTokenFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
				typed[key] = "<redacted>"
				continue
			}
			redactTokenFields(child)
		}
	case []any:
		for _, child := range typed {
			redactTokenFields(child)
		}
	}
}

func seqText(seq *int64) string {
	if seq == nil {
		return "<nil>"
	}
	return strconv.FormatInt(*seq, 10)
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
