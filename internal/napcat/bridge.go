package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

type Bridge struct {
	APIURL      string
	AccessToken string
	WSURL       string
	Handler     commands.Handler
	Agent       *agent.Service
	HTTPClient  *http.Client
	Logger      *log.Logger

	LoginPollInterval time.Duration
	LoginPollTimeout  time.Duration

	loginPollsMu sync.Mutex
	loginPolls   map[string]loginPoll
	loginPollSeq uint64
}

type loginPoll struct {
	id     uint64
	cancel context.CancelFunc
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
		result, ok := b.Handler.HandleResult(ctx, commands.Input{
			Text:     event.RawMessage,
			Identity: event.identity(),
		})
		reply := result.Reply
		if !ok {
			reply, ok = b.handleAgent(ctx, event)
		}
		if !ok {
			b.recordIgnored(ctx, event)
			continue
		}
		if err := b.Send(ctx, event, reply); err != nil {
			if b.Logger != nil {
				b.Logger.Printf("send reply failed: %v", err)
			}
		}
		if result.StartLoginPoll {
			b.startLoginPoll(ctx, event, func(ctx context.Context, event messageEvent, message string) error {
				return b.Send(ctx, event, message)
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
		result, ok := b.Handler.HandleResult(ctx, commands.Input{
			Text:     event.RawMessage,
			Identity: event.identity(),
		})
		reply := result.Reply
		if !ok {
			reply, ok = b.handleAgent(ctx, event)
		}
		if !ok {
			b.recordIgnored(ctx, event)
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
		if result.StartLoginPoll {
			b.startLoginPoll(ctx, event, func(ctx context.Context, event messageEvent, message string) error {
				err := sendReverseReply(conn, writeMu, event, message)
				if err != nil {
					b.recordOutbound(ctx, event, message, "failed", err)
					return err
				}
				b.recordOutbound(ctx, event, message, "sent", nil)
				return nil
			})
		}
	}
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

func (b *Bridge) startLoginPoll(ctx context.Context, event messageEvent, send func(context.Context, messageEvent, string) error) {
	if b.Handler.Auth == nil {
		return
	}
	ident := event.identity()
	key := identityKey(ident)
	pollCtx, cancel := context.WithCancel(ctx)
	b.loginPollsMu.Lock()
	if b.loginPolls == nil {
		b.loginPolls = make(map[string]loginPoll)
	}
	if existing := b.loginPolls[key]; existing.cancel != nil {
		existing.cancel()
	}
	pollID := b.loginPollSeq + 1
	b.loginPollSeq = pollID
	b.loginPolls[key] = loginPoll{id: pollID, cancel: cancel}
	b.loginPollsMu.Unlock()

	go func() {
		defer b.clearLoginPoll(key, pollID)
		interval := b.LoginPollInterval
		if interval <= 0 {
			interval = 10 * time.Second
		}
		timeout := b.LoginPollTimeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-timer.C:
				return
			case <-ticker.C:
				result, err := b.Handler.Auth.PollDeviceLogin(pollCtx, ident)
				if err != nil {
					if b.Logger != nil {
						b.Logger.Printf("login poll failed: %v", err)
					}
					continue
				}
				if result.Pending || result.SlowDown {
					continue
				}
				if result.Authorized {
					if err := send(pollCtx, event, "登录成功。"); err != nil && b.Logger != nil {
						b.Logger.Printf("login completion notify failed: %v", err)
					}
					return
				}
				if result.Message != "" {
					if err := send(pollCtx, event, result.Message); err != nil && b.Logger != nil {
						b.Logger.Printf("login status notify failed: %v", err)
					}
				}
				return
			}
		}
	}()
}

func (b *Bridge) clearLoginPoll(key string, pollID uint64) {
	b.loginPollsMu.Lock()
	defer b.loginPollsMu.Unlock()
	if b.loginPolls[key].id == pollID {
		delete(b.loginPolls, key)
	}
}

func identityKey(ident store.Identity) string {
	return ident.Platform + "\x00" + ident.UserID + "\x00" + ident.ConversationType + "\x00" + ident.ConversationID
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
		return fmt.Errorf("napcat %s returned %d", endpoint, resp.StatusCode)
	}
	return nil
}

func trimLogText(text string) string {
	const max = 160
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}
