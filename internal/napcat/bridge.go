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
	if b.AccessToken != "" {
		header.Set("Authorization", "Bearer "+b.AccessToken)
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
			if b.Logger != nil {
				b.Logger.Printf("send reply failed: %v", err)
			}
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
			if b.Logger != nil {
				b.Logger.Printf("upgrade reverse websocket failed: %v", err)
			}
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
			if b.Logger != nil && ctx.Err() == nil {
				b.Logger.Printf("reverse websocket read failed: %v", err)
			}
			return
		}
		if event.PostType != "message" {
			continue
		}
		if b.Logger != nil {
			b.Logger.Printf("reverse websocket message: message_type=%q user_id=%d group_id=%d raw=%q",
				event.MessageType, event.UserID, event.GroupID, trimLogText(event.RawMessage))
		}
		reply, ok := b.handleMessage(ctx, event)
		if !ok {
			if b.Logger != nil {
				b.Logger.Printf("reverse websocket ignored message from user_id=%d: raw=%q", event.UserID, trimLogText(event.RawMessage))
			}
			continue
		}
		if err := sendReverseReply(conn, writeMu, event, reply); err != nil {
			b.recordOutbound(ctx, event, reply, "failed", err)
			if b.Logger != nil {
				b.Logger.Printf("reverse websocket send failed: %v", err)
			}
		} else {
			b.recordOutbound(ctx, event, reply, "sent", nil)
			if b.Logger != nil {
				b.Logger.Printf("reverse websocket replied to user_id=%d group_id=%d", event.UserID, event.GroupID)
			}
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
	if b.Handler.Store != nil {
		_ = b.Handler.Store.RecordInteraction(ctx, event.identity(), store.Interaction{
			RawText: event.RawMessage,
			Command: "agent",
			Handled: true,
			Reply:   reply,
			Status:  "handled",
		})
	}
	return reply, true
}

func (b *Bridge) recordIgnored(ctx context.Context, event messageEvent) {
	if b.Handler.Store == nil {
		return
	}
	_ = b.Handler.Store.RecordInteraction(ctx, event.identity(), store.Interaction{
		RawText: event.RawMessage,
		Handled: false,
		Status:  "ignored",
	})
}

func (b *Bridge) recordOutbound(ctx context.Context, event messageEvent, message, status string, err error) {
	if b.Handler.Store == nil {
		return
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	_ = b.Handler.Store.RecordInteraction(ctx, event.identity(), store.Interaction{
		Direction: "outbound",
		RawText:   message,
		Handled:   true,
		Status:    status,
		Error:     errText,
	})
}

func (e messageEvent) identity() store.Identity {
	conversationID := fmt.Sprint(e.UserID)
	if e.MessageType == "group" {
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
	if event.MessageType == "group" {
		endpoint = "/send_group_msg"
		payload["group_id"] = event.GroupID
		delete(payload, "user_id")
	}
	err := b.post(ctx, endpoint, payload)
	if err != nil {
		b.recordOutbound(ctx, event, message, "failed", err)
		return err
	}
	b.recordOutbound(ctx, event, message, "sent", nil)
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
			b.recordOutbound(ctx, event, message, "sent", nil)
			return nil
		} else if b.Logger != nil {
			b.Logger.Printf("reverse websocket login notification failed: %v", err)
		}
	}
	return b.Send(ctx, event, message)
}

func messageEventFromIdentity(ident store.Identity) (messageEvent, error) {
	event := messageEvent{MessageType: ident.ConversationType}
	userID, err := strconv.ParseInt(ident.UserID, 10, 64)
	if err != nil {
		return messageEvent{}, fmt.Errorf("invalid napcat user id %q: %w", ident.UserID, err)
	}
	event.UserID = userID
	if ident.ConversationType == "group" {
		groupID, err := strconv.ParseInt(ident.ConversationID, 10, 64)
		if err != nil {
			return messageEvent{}, fmt.Errorf("invalid napcat group id %q: %w", ident.ConversationID, err)
		}
		event.GroupID = groupID
	}
	return event, nil
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
	if event.MessageType == "group" {
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

func (b *Bridge) post(ctx context.Context, endpoint string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	apiURL := b.APIURL + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.AccessToken)
		q := url.Values{"access_token": []string{b.AccessToken}}
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
		respBody, _ := io.ReadAll(resp.Body)
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
		return fmt.Errorf("napcat %s failed: %s", endpoint, textutil.FirstNonEmpty(result.Message, result.Wording, result.Status))
	}
	if result.RetCode != 0 {
		return fmt.Errorf("napcat %s failed with retcode %d: %s", endpoint, result.RetCode, textutil.FirstNonEmpty(result.Message, result.Wording))
	}
	return nil
}

func trimLogText(text string) string {
	const max = 160
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
