package napcat

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Bridge struct {
	APIURL      string
	AccessToken string
	WSURL       string
	Handler     commands.Handler
	Agent       *agent.Service
	HTTPClient  *http.Client
	Logger      *log.Logger

	reverseMu      sync.Mutex
	reverseConn    *websocket.Conn
	reverseWriteMu *sync.Mutex
	reverseSeq     uint64
}

type messageEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	RawMessage  string `json:"raw_message"`
	Message     any    `json:"message"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	SelfID      int64  `json:"self_id"`
}

func (b *Bridge) Run(ctx context.Context) error {
	if b.WSURL == "" {
		return fmt.Errorf("NAPCAT_WS_URL is empty")
	}
	dialer := websocket.DefaultDialer
	header := http.Header{}
	accessToken := b.accessToken()
	if accessToken != "" {
		header.Set("Authorization", "Bearer "+accessToken)
	}
	conn, _, err := dialer.DialContext(ctx, b.WSURL, header)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	for {
		var event messageEvent
		if err := conn.ReadJSON(&event); err != nil {
			return err
		}
		if event.PostType != "message" {
			continue
		}
		reply, ok := b.handleMessage(ctx, event)
		if !ok {
			continue
		}
		if err := b.Send(ctx, event, reply); err != nil {
			b.logf("send reply failed: %v", err)
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
	defer func() { _ = conn.Close() }()
	writeMu := &sync.Mutex{}
	connID := b.setReverseConn(conn, writeMu)
	defer b.clearReverseConn(connID)
	for {
		var event messageEvent
		if err := conn.ReadJSON(&event); err != nil {
			if ctx.Err() == nil {
				b.logf("reverse websocket read failed: %v", err)
			}
			return
		}
		if event.PostType != "message" {
			continue
		}
		b.logf("reverse websocket message: message_type=%q user_id=%d group_id=%d raw=%q",
			event.MessageType, event.UserID, event.GroupID, trimLogText(event.RawMessage))
		reply, ok := b.handleMessage(ctx, event)
		if !ok {
			b.logf("reverse websocket ignored message from user_id=%d: raw=%q", event.UserID, trimLogText(event.RawMessage))
			continue
		}
		if err := sendReverseReply(conn, writeMu, event, reply); err != nil {
			b.recordOutbound(ctx, event, reply, store.InteractionStatusFailed, err)
			b.logf("reverse websocket send failed: %v", err)
		} else {
			b.recordOutbound(ctx, event, reply, store.InteractionStatusSent, nil)
			b.logf("reverse websocket replied to user_id=%d group_id=%d", event.UserID, event.GroupID)
		}
	}
}

func (b *Bridge) handleMessage(ctx context.Context, event messageEvent) (string, bool) {
	reply, ok := b.Handler.Handle(ctx, commands.Input{
		Text:     event.RawMessage,
		Identity: event.identity(),
	})
	if !ok {
		reply, ok = b.handleAgent(ctx, event)
	}
	if !ok {
		b.recordIgnored(ctx, event)
		return "", false
	}
	return reply, true
}

func (b *Bridge) handleAgent(ctx context.Context, event messageEvent) (string, bool) {
	if b.Agent == nil {
		return "", false
	}
	reply, ok := b.Agent.Handle(ctx, agent.Input{
		Text:     event.RawMessage,
		Identity: event.identity(),
	})
	if !ok {
		return "", false
	}
	b.recordInteraction(ctx, event, store.Interaction{
		RawText: event.RawMessage,
		Command: "agent",
		Handled: true,
		Reply:   reply,
		Status:  store.InteractionStatusHandled,
	}, "agent")
	return reply, true
}

func (b *Bridge) recordIgnored(ctx context.Context, event messageEvent) {
	b.recordInteraction(ctx, event, store.Interaction{
		RawText: event.RawMessage,
		Handled: false,
		Status:  store.InteractionStatusIgnored,
	}, "ignored")
}

func (b *Bridge) recordOutbound(ctx context.Context, event messageEvent, message, status string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	b.recordInteraction(ctx, event, store.Interaction{
		Direction: store.InteractionDirectionOutbound,
		RawText:   message,
		Handled:   true,
		Status:    status,
		Error:     errText,
	}, "outbound")
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

func (b *Bridge) Send(ctx context.Context, event messageEvent, message string) error {
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
	err := b.post(ctx, endpoint, payload)
	if err != nil {
		b.recordOutbound(ctx, event, message, store.InteractionStatusFailed, err)
		return err
	}
	b.recordOutbound(ctx, event, message, store.InteractionStatusSent, nil)
	return nil
}

func (b *Bridge) SendLoginMessage(ctx context.Context, ident store.Identity, message string) error {
	return b.SendMessage(ctx, ident, message)
}

func (b *Bridge) SendMessage(ctx context.Context, ident store.Identity, message string) error {
	event, err := messageEventFromIdentity(ident)
	if err != nil {
		return err
	}
	if conn, writeMu := b.activeReverseConn(); conn != nil {
		if err := sendReverseReply(conn, writeMu, event, message); err == nil {
			b.recordOutbound(ctx, event, message, store.InteractionStatusSent, nil)
			return nil
		} else {
			b.logf("reverse websocket login notification failed: %v", err)
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

var echoCounter uint64

func sendReverseReply(conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, message string) error {
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
	frame := map[string]any{
		"action": action,
		"params": params,
		"echo":   fmt.Sprintf("life-ustc-%d", atomic.AddUint64(&echoCounter, 1)),
	}
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	return conn.WriteJSON(frame)
}

func isGroupMessageType(messageType string) bool {
	return textutil.TrimEqualFold(messageType, "group")
}

func (b *Bridge) post(ctx context.Context, endpoint string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	apiURL := textutil.TrimTrailingSlash(b.APIURL) + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
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
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("napcat %s returned %d: read response body: %w", endpoint, resp.StatusCode, err)
		}
		message := strings.TrimSpace(string(respBody))
		if message != "" {
			return fmt.Errorf("napcat %s returned %d: %s", endpoint, resp.StatusCode, message)
		}
		return fmt.Errorf("napcat %s returned %d", endpoint, resp.StatusCode)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	var result struct {
		Status  string `json:"status"`
		RetCode int    `json:"retcode"`
		Message string `json:"message"`
		Wording string `json:"wording"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("napcat %s returned invalid JSON: %w", endpoint, err)
	}
	if result.Status != "" && result.Status != "ok" {
		return fmt.Errorf("napcat %s failed: %s", endpoint, napcatResultText(result.Message, result.Wording, result.Status))
	}
	if result.RetCode != 0 {
		return fmt.Errorf("napcat %s failed with retcode %d: %s", endpoint, result.RetCode, napcatResultText(result.Message, result.Wording, result.Status))
	}
	return nil
}

func (b *Bridge) accessToken() string {
	return strings.TrimSpace(b.AccessToken)
}

func napcatResultText(message, wording, status string) string {
	return textutil.FirstNonEmpty(message, wording, status, "unknown")
}

func trimLogText(text string) string {
	const max = 160
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
