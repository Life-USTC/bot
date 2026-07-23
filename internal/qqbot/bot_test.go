package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tencent-connect/botgo/interaction/signature"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/retry"
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

func TestSendToReturnsPlatformAcceptance(t *testing.T) {
	acceptedAt := "2026-07-18T01:02:03.456+08:00"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        "message-123",
			"timestamp": acceptedAt,
		})
	}))
	defer server.Close()

	bot := &Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}
	receipt, err := bot.sendTo(context.Background(), store.Identity{
		Platform:         "qqbot",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}, "hello", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTime, err := time.Parse(time.RFC3339Nano, acceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PlatformMessageID != "message-123" || !receipt.AcceptedAt.Equal(wantTime) {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestSendToTreatsIncompleteSuccessAsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	bot := &Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}
	_, err := bot.sendTo(context.Background(), store.Identity{
		Platform:         "qqbot",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}, "hello", "", "", 0)
	if err == nil || !isUncertainSendError(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestSendToTreatsPlatformRejectionAsFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()

	bot := &Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}
	_, err := bot.sendTo(context.Background(), store.Identity{
		Platform:         "qqbot",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}, "hello", "", "", 0)
	if err == nil || isUncertainSendError(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAPILogsMetadataWithoutPayloads(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["content"] != "private-request" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "private-response"})
	}))
	defer server.Close()

	var out map[string]string
	bot := &Bot{
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		Logger:     log.New(&logs, "", 0),
	}
	if err := bot.openAPI(
		context.Background(),
		http.MethodPost,
		"/messages",
		"token",
		map[string]string{"content": "private-request"},
		&out,
	); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "private-response" {
		t.Fatalf("response = %#v", out)
	}
	if strings.Contains(logs.String(), "private-request") || strings.Contains(logs.String(), "private-response") {
		t.Fatalf("logs contain request or response payload: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "QQ bot openapi request: method=POST path=/messages") ||
		!strings.Contains(logs.String(), "QQ bot openapi response: method=POST path=/messages status=200") {
		t.Fatalf("logs missing request metadata: %q", logs.String())
	}
}

func TestDispatchLogsMetadataWithoutMessageText(t *testing.T) {
	var logs bytes.Buffer
	bot := &Bot{
		Handler: commands.Handler{Prefix: "/life"},
		Logger:  log.New(&logs, "", 0),
	}
	bot.handleDispatch(context.Background(), gatewayPayload{
		Op: opDispatch,
		T:  "C2C_MESSAGE_CREATE",
		D: json.RawMessage(`{
			"id":"message-id",
			"content":"private-query",
			"author":{"user_openid":"user-openid"}
		}`),
	})

	if strings.Contains(logs.String(), "private-query") {
		t.Fatalf("logs contain private message text: %q", logs.String())
	}
	if !strings.Contains(logs.String(), `QQ bot message: event=C2C_MESSAGE_CREATE conversation_type=private user_id="user-openid" conversation_id="user-openid"`) {
		t.Fatalf("logs missing message metadata: %q", logs.String())
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

func TestSendRichMessageUploadsAndSendsProactiveC2CImage(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/users/user-openid/files":
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

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	image := responses.NewTextImage("homework_reminder", "作业提醒", "作业提醒：\nProblem Set 1")
	err := bot.SendRichMessage(context.Background(), store.Identity{
		Platform:         "qqbot",
		UserID:           "user-openid",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}, image.AltText, image)
	if err != nil {
		t.Fatal(err)
	}
	if sent["msg_type"].(float64) != 7 {
		t.Fatalf("sent body = %#v", sent)
	}
	if _, ok := sent["msg_id"]; ok {
		t.Fatalf("proactive message should not contain msg_id: %#v", sent)
	}
}

func TestSendRichMessageFallsBackToProactiveText(t *testing.T) {
	var sent sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/users/user-openid/files":
			http.Error(w, "upload rejected", http.StatusBadRequest)
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

	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	image := responses.NewTextImage("homework_reminder", "作业提醒", "作业提醒：\nProblem Set 1")
	err := bot.SendRichMessage(context.Background(), store.Identity{
		Platform:         "qqbot",
		UserID:           "user-openid",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}, image.AltText, image)
	if err != nil {
		t.Fatal(err)
	}
	if sent.MsgType != 0 || sent.Content != image.AltText || sent.MsgID != "" || sent.MsgSeq != 0 {
		t.Fatalf("fallback body = %#v", sent)
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

func TestSendResponseReusesCachedQQFileInfo(t *testing.T) {
	uploads := 0
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/user-openid/files":
			uploads++
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token", "ttl": 300})
		case "/v2/users/user-openid/messages":
			sends++
			var body sendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Media == nil || string(body.Media.FileInfo) != `"file-token"` {
				t.Fatalf("media = %#v", body.Media)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	message := &incomingMessage{
		ID: "incoming",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表").WithURL(server.URL + "/same.png"),
	}

	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || sends != 2 {
		t.Fatalf("uploads = %d sends = %d, want 1 and 2", uploads, sends)
	}
}

func TestSendResponseReuploadsExpiredQQFileInfo(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/user-openid/files":
			uploads++
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token", "ttl": 10})
		case "/v2/users/user-openid/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
		now:        func() time.Time { return now },
	}
	message := &incomingMessage{
		ID: "incoming",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表").WithURL(server.URL + "/same.png"),
	}

	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	if uploads != 2 {
		t.Fatalf("uploads = %d, want 2", uploads)
	}
}

func TestSendResponseReuploadsRejectedCachedQQFileInfo(t *testing.T) {
	uploads := 0
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/user-openid/files":
			uploads++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_info": "file-token-" + strconv.Itoa(uploads),
				"ttl":       300,
			})
		case "/v2/users/user-openid/messages":
			sends++
			if sends == 2 {
				http.Error(w, "cached media expired", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	message := &incomingMessage{
		ID: "incoming",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表").WithURL(server.URL + "/same.png"),
	}

	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	if uploads != 2 || sends != 3 {
		t.Fatalf("uploads = %d sends = %d, want 2 and 3", uploads, sends)
	}
}

func TestQQMediaCacheCoalescesConcurrentUploads(t *testing.T) {
	var uploads atomic.Int32
	uploadStarted := make(chan struct{})
	releaseUpload := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/users/user-openid/files" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if uploads.Add(1) == 1 {
			close(uploadStarted)
		}
		<-releaseUpload
		_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token", "ttl": 300})
	}))
	defer server.Close()

	bot := &Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}
	ident := store.Identity{
		Platform:         "qqbot",
		UserID:           "user-openid",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}
	key, err := qqMediaCacheKey(ident, server.URL+"/same.png")
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := bot.cachedOrUploadRichMedia(context.Background(), ident, key, server.URL+"/same.png")
		errs <- err
	}()
	<-uploadStarted
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := bot.cachedOrUploadRichMedia(context.Background(), ident, key, server.URL+"/same.png")
		errs <- err
	}()
	time.Sleep(10 * time.Millisecond)
	close(releaseUpload)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if uploads.Load() != 1 {
		t.Fatalf("uploads = %d, want 1", uploads.Load())
	}
}

func TestSendResponseDoesNotRetryUncertainCachedQQSend(t *testing.T) {
	uploads := 0
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/user-openid/files":
			uploads++
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token", "ttl": 300})
		case "/v2/users/user-openid/messages":
			sends++
			if sends == 2 {
				_, _ = w.Write([]byte(`{`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &Bot{
		BotToken:   "token",
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		MediaStore: responses.NewMediaStore(server.URL+"/media", time.Minute),
	}
	message := &incomingMessage{
		ID: "incoming",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表").WithURL(server.URL + "/same.png"),
	}

	if err := bot.SendResponse(context.Background(), message, response); err != nil {
		t.Fatal(err)
	}
	err := bot.SendResponse(context.Background(), message, response)
	if err == nil || !isUncertainSendError(err) {
		t.Fatalf("error = %v", err)
	}
	if uploads != 1 || sends != 2 {
		t.Fatalf("uploads = %d sends = %d, want 1 and 2", uploads, sends)
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
	if !strings.Contains(gotBody.Content, "校车（xc）\t查询班次、路线与设置偏好") {
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
	if !strings.Contains(gotBody.Content, "校车（xc）\t查询班次、路线与设置偏好") {
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
	if !strings.Contains(gotBody.Content, "校车（xc）\t查询班次、路线与设置偏好") {
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

func TestRunStopsAfterPermanentTokenFailure(t *testing.T) {
	requests := 0
	const privateValue = "revoked secret credential"
	const privateBody = `{"message":"` + privateValue + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, privateBody, http.StatusUnauthorized)
	}))
	defer server.Close()

	var logs bytes.Buffer
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		TokenURL:   server.URL,
		HTTPClient: server.Client(),
		Logger:     log.New(&logs, "", 0),
	}
	err := bot.run(context.Background(), retry.Backoff{Initial: time.Millisecond, Max: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Run error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("token requests = %d, want 1", requests)
	}
	if strings.Contains(logs.String(), privateValue) || strings.Contains(err.Error(), privateValue) {
		t.Fatalf("private token response leaked: error=%v logs=%s", err, logs.String())
	}
}

func TestFetchAccessTokenDoesNotLogMalformedResponse(t *testing.T) {
	const privateValue = "private-access-token"
	const privateBody = `{"access_token":"` + privateValue + `"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(privateBody))
	}))
	defer server.Close()

	var logs bytes.Buffer
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		TokenURL:   server.URL,
		HTTPClient: server.Client(),
		Logger:     log.New(&logs, "", 0),
	}
	_, _, err := bot.fetchAccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode qq bot token response") {
		t.Fatalf("fetchAccessToken error = %v", err)
	}
	if strings.Contains(logs.String(), privateValue) || strings.Contains(err.Error(), privateValue) {
		t.Fatalf("private malformed token response leaked: error=%v logs=%s", err, logs.String())
	}
	if !strings.Contains(logs.String(), "QQ bot token response: status=200") {
		t.Fatalf("logs missing response status: %s", logs.String())
	}
}

func TestRunRetriesTransientTokenFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests >= 3 {
			cancel()
		}
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	bot := &Bot{AppID: "appid", AppSecret: "secret", TokenURL: server.URL, HTTPClient: server.Client()}
	if err := bot.run(ctx, retry.Backoff{Initial: time.Millisecond, Max: 2 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if requests < 3 {
		t.Fatalf("token requests = %d, want at least 3", requests)
	}
}

func TestRunResetsBackoffAfterReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()

		attempt := connections.Add(1)
		if err := conn.WriteJSON(gatewayPayload{
			Op: opHello,
			D:  json.RawMessage(`{"heartbeat_interval":3600000}`),
		}); err != nil {
			t.Error(err)
			return
		}
		var identify gatewaySendPayload
		if err := conn.ReadJSON(&identify); err != nil {
			t.Error(err)
			return
		}
		if identify.Op != opIdentify {
			t.Errorf("identify opcode = %d", identify.Op)
			return
		}
		if attempt == 3 {
			if err := conn.WriteJSON(gatewayPayload{
				Op: opDispatch,
				T:  "READY",
				D:  json.RawMessage(`{}`),
			}); err != nil {
				t.Error(err)
				return
			}
		}
		if attempt == 4 {
			cancel()
		}
	}))
	defer server.Close()

	var logs bytes.Buffer
	bot := &Bot{
		BotToken:   "static-token",
		GatewayURL: "ws" + server.URL[len("http"):],
		Logger:     log.New(&logs, "", 0),
	}
	if err := bot.run(ctx, retry.Backoff{Initial: 5 * time.Millisecond, Max: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := connections.Load(); got != 4 {
		t.Fatalf("connections = %d, want 4", got)
	}
	if got := strings.Count(logs.String(), "reconnecting in 5ms"); got != 2 {
		t.Fatalf("initial-delay logs = %d, want 2:\n%s", got, logs.String())
	}
	if got := strings.Count(logs.String(), "reconnecting in 10ms"); got != 1 {
		t.Fatalf("grown-delay logs = %d, want 1:\n%s", got, logs.String())
	}
}

func TestRateLimitedTokenFailureRemainsRetryable(t *testing.T) {
	if isPermanentHTTPStatus(http.StatusTooManyRequests) {
		t.Fatal("429 classified as permanent")
	}
	if !isPermanentHTTPStatus(http.StatusUnauthorized) {
		t.Fatal("401 classified as retryable")
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
