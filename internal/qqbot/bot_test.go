package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/interaction/signature"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestSendMessageFetchesTokenAndSendsGroupMessage(t *testing.T) {
	requests := 0
	var gotAuth string
	var gotBody sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			requests++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   "7200",
			})
		case "/v2/groups/group-openid/messages":
			gotAuth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
	}
	err := bot.SendMessage(context.Background(), store.Identity{
		Platform:         "qqbot",
		ConversationType: "group",
		ConversationID:   "group-openid",
	}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("token requests = %d, want 1", requests)
	}
	if gotAuth != "QQBot access-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBody.Content != "\n\nhello" || gotBody.MsgType != 0 || gotBody.MsgID != "" || gotBody.MsgSeq != 0 {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestSendResponseUploadsAndSendsC2CImage(t *testing.T) {
	var uploaded map[string]any
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/users/user-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token"})
		case "/v2/users/user-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	message := &incomingMessage{
		ID:   "message-id",
		Type: "C2C_MESSAGE_CREATE",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表：\n数据库系统",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表：\n数据库系统"),
	}

	err := bot.SendResponse(context.Background(), message, response)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded["file_type"].(float64) != 1 || uploaded["srv_send_msg"].(bool) {
		t.Fatalf("upload body = %#v", uploaded)
	}
	if !strings.HasPrefix(uploaded["url"].(string), server.URL+"/media/") {
		t.Fatalf("upload url = %q", uploaded["url"])
	}
	if sent["msg_type"].(float64) != 7 {
		t.Fatalf("sent body = %#v", sent)
	}
	media := sent["media"].(map[string]any)
	if media["file_info"] != "file-token" {
		t.Fatalf("media = %#v", media)
	}
	if sent["msg_id"] != "message-id" || sent["msg_seq"].(float64) != 1 {
		t.Fatalf("passive fields = %#v", sent)
	}
}

func TestSendResponseFallsBackToTextWhenQQImageUploadFails(t *testing.T) {
	var textBody sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/groups/group-openid/files":
			http.Error(w, "upload rejected", http.StatusBadRequest)
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&textBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	message := &incomingMessage{
		ID:   "message-id",
		Type: "GROUP_AT_MESSAGE_CREATE",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "member-openid",
			ConversationType: "group",
			ConversationID:   "group-openid",
		},
	}
	response := commands.Response{
		Text:  "待办：\n1. 写报告",
		Image: responses.NewTextImage("todo", "待办", "待办：\n1. 写报告"),
	}

	err := bot.SendResponse(context.Background(), message, response)
	if err != nil {
		t.Fatal(err)
	}
	if textBody.MsgType != 0 || textBody.Content != "\n\n"+response.Text {
		t.Fatalf("text fallback body = %#v", textBody)
	}
}

func TestQQBotOutgoingMessageAddsLeadingBlankLineForGroups(t *testing.T) {
	got := qqBotOutgoingMessage(store.Identity{ConversationType: " group "}, "\nhello")
	if got != "\n\nhello" {
		t.Fatalf("group message = %q", got)
	}

	got = qqBotOutgoingMessage(store.Identity{ConversationType: "private"}, "hello")
	if got != "hello" {
		t.Fatalf("private message = %q", got)
	}
}

func testResponseFontPath(t *testing.T) string {
	t.Helper()
	for _, path := range responses.DefaultFontPathsForTest() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no CJK font found")
	return ""
}

func TestHandleDispatchSendsPassiveC2CReplyAndRecordsInteractions(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var gotBody sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
			})
		case "/v2/users/user-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		Handler: commands.Handler{
			Prefix: "/life",
			Store:  db,
		},
		HTTPClient: server.Client(),
	}
	data := json.RawMessage(`{
		"id":"message-id",
		"content":"/help",
		"author":{"user_openid":"user-openid"},
		"timestamp":"2026-06-21T12:00:00+08:00"
	}`)
	bot.handleDispatch(context.Background(), gatewayPayload{
		ID: "event-id",
		Op: opDispatch,
		T:  "C2C_MESSAGE_CREATE",
		D:  data,
	})

	if gotBody.MsgID != "message-id" || gotBody.MsgSeq != 1 {
		t.Fatalf("passive reply fields = msg_id %q msg_seq %d", gotBody.MsgID, gotBody.MsgSeq)
	}
	if !strings.Contains(gotBody.Content, "校车 / xc") {
		t.Fatalf("reply content = %q", gotBody.Content)
	}
	waitInteractionCount(t, db, 2)
}

func TestServeWebhookValidationUsesSignedCallback(t *testing.T) {
	bot := &Bot{AppSecret: "123456abcdef"}
	body := `{"op":13,"d":{"plain_token":"plain-token","event_ts":"1728981195"}}`
	req := signedWebhookRequest(t, bot.AppSecret, body)
	rec := httptest.NewRecorder()

	bot.ServeWebhookHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"plain_token":"plain-token"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"signature"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestServeWebhookRoutesSignedC2CMessageAndAcksDispatch(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	gotBodyCh := make(chan sendMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
			})
		case "/v2/users/user-openid/messages":
			var gotBody sendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			gotBodyCh <- gotBody
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "123456abcdef",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		Handler: commands.Handler{
			Prefix: "/life",
			Store:  db,
		},
		HTTPClient: server.Client(),
	}
	body := `{
		"op":0,
		"id":"event-id",
		"t":"C2C_MESSAGE_CREATE",
		"d":{
			"id":"message-id",
			"content":"/help",
			"author":{"id":"author-id","user_openid":"user-openid"}
		}
	}`
	req := signedWebhookRequest(t, bot.AppSecret, body)
	rec := httptest.NewRecorder()

	bot.ServeWebhookHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"op":12,"d":0}` {
		t.Fatalf("ack body = %s", rec.Body.String())
	}
	var gotBody sendMessageRequest
	select {
	case gotBody = <-gotBodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook reply")
	}
	if gotBody.MsgID != "message-id" || gotBody.MsgSeq != 1 {
		t.Fatalf("passive reply fields = msg_id %q msg_seq %d", gotBody.MsgID, gotBody.MsgSeq)
	}
	if !strings.Contains(gotBody.Content, "校车 / xc") {
		t.Fatalf("reply content = %q", gotBody.Content)
	}
	waitInteractionCount(t, db, 2)
}

func TestHandleDispatchAcksInteractionAndRepliesWithEventID(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	acked := false
	var gotBody sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
			})
		case "/interactions/interaction-id":
			if r.Method != http.MethodPut {
				t.Fatalf("ack method = %s", r.Method)
			}
			var body map[string]int
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != 0 {
				t.Fatalf("ack body = %#v", body)
			}
			acked = true
		case "/v2/users/user-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		Handler: commands.Handler{
			Prefix: "/life",
			Store:  db,
		},
		HTTPClient: server.Client(),
	}
	bot.handleDispatch(context.Background(), gatewayPayload{
		ID: "payload-id",
		Op: opDispatch,
		T:  "INTERACTION_CREATE",
		D: json.RawMessage(`{
			"id":"interaction-id",
			"type":12,
			"scene":"c2c",
			"chat_type":2,
			"user_openid":"user-openid",
			"data":{"resolved":{"button_data":"/help"}}
		}`),
	})

	if !acked {
		t.Fatal("interaction was not acknowledged")
	}
	if gotBody.EventID != "interaction-id" || gotBody.MsgID != "" || gotBody.MsgSeq != 0 {
		t.Fatalf("reply fields = %#v", gotBody)
	}
	if !strings.Contains(gotBody.Content, "校车 / xc") {
		t.Fatalf("reply content = %q", gotBody.Content)
	}
}

func TestInteractionFromPayloadDefaultsQuickMenuToHelp(t *testing.T) {
	bot := &Bot{}
	message, err := bot.interactionFromPayload(gatewayPayload{
		ID: "payload-id",
		T:  "INTERACTION_CREATE",
		D: json.RawMessage(`{
			"id":"interaction-id",
			"type":12,
			"scene":"c2c",
			"chat_type":2,
			"user_openid":"user-openid",
			"data":{"resolved":{"feature_id":" "}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "help" {
		t.Fatalf("text = %q", message.Text)
	}
	if message.EventID != "interaction-id" {
		t.Fatalf("event id = %q", message.EventID)
	}
}

func TestMessageFromPayloadNormalizesGroupIdentityAndMention(t *testing.T) {
	bot := &Bot{BotID: "bot-id"}
	message, err := bot.messageFromPayload(gatewayPayload{
		ID: "event-id",
		T:  "GROUP_AT_MESSAGE_CREATE",
		D: json.RawMessage(`{
			"id":"message-id",
			"content":" <@!bot-id> 校车 ",
			"group_openid":"group-openid",
			"author":{"member_openid":"member-openid"}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "校车" {
		t.Fatalf("text = %q", message.Text)
	}
	want := store.Identity{
		Platform:         "qqbot",
		UserID:           "member-openid",
		ConversationType: "group",
		ConversationID:   "group-openid",
	}
	if message.Identity != want {
		t.Fatalf("identity = %#v", message.Identity)
	}
}

func TestMessageFromPayloadNormalizesChannelIdentityAndMention(t *testing.T) {
	bot := &Bot{BotID: "bot-id"}
	message, err := bot.messageFromPayload(gatewayPayload{
		ID: "event-id",
		T:  "AT_MESSAGE_CREATE",
		D: json.RawMessage(`{
			"id":"message-id",
			"content":" <@bot-id> /help ",
			"channel_id":"channel-id",
			"guild_id":"guild-id",
			"author":{"id":"author-id"}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "/help" {
		t.Fatalf("text = %q", message.Text)
	}
	want := store.Identity{
		Platform:         "qqbot",
		UserID:           "author-id",
		ConversationType: "channel",
		ConversationID:   "channel-id",
	}
	if message.Identity != want {
		t.Fatalf("identity = %#v", message.Identity)
	}
}

func TestSendPathSupportsOfficialBotTargets(t *testing.T) {
	tests := []struct {
		name  string
		ident store.Identity
		want  string
	}{
		{
			name:  "c2c",
			ident: store.Identity{ConversationType: "private", ConversationID: "user-openid"},
			want:  "/v2/users/user-openid/messages",
		},
		{
			name:  "group",
			ident: store.Identity{ConversationType: "group", ConversationID: "group-openid"},
			want:  "/v2/groups/group-openid/messages",
		},
		{
			name:  "channel",
			ident: store.Identity{ConversationType: "channel", ConversationID: "channel-id"},
			want:  "/channels/channel-id/messages",
		},
		{
			name:  "guild private",
			ident: store.Identity{ConversationType: "guild_private", ConversationID: "guild-id"},
			want:  "/dms/guild-id/messages",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sendPath(tt.ident)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccessTokenUsesStaticTokenWithoutSecret(t *testing.T) {
	bot := &Bot{BotToken: " static-token "}
	token, err := bot.accessTokenForRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "static-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestParseExpiresIn(t *testing.T) {
	tests := map[string]time.Duration{
		`7200`:   7200 * time.Second,
		`"7200"`: 7200 * time.Second,
		`1.5`:    1500 * time.Millisecond,
		`"bad"`:  0,
	}
	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			if got := parseExpiresIn(json.RawMessage(raw)); got != want {
				t.Fatalf("parseExpiresIn(%s) = %s, want %s", raw, got, want)
			}
		})
	}
}

func signedWebhookRequest(t *testing.T, secret, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/qqbot", strings.NewReader(body))
	req.Header.Set(signature.HeaderTimestamp, "1728981195")
	sig, err := signature.Generate(secret, req.Header, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(signature.HeaderSig, sig)
	return req
}

func waitInteractionCount(t *testing.T, db *store.Store, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last int64
	for time.Now().Before(deadline) {
		count, err := db.InteractionCount(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		last = count
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("interaction count = %d, want %d", last, want)
}
