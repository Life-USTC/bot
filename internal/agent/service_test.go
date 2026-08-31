package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botfeedback "github.com/Life-USTC/Bot/internal/feedback"
	"github.com/Life-USTC/Bot/internal/life"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestDisabledAgentDoesNotHandle(t *testing.T) {
	svc, err := New(context.Background(), Config{}, commands.Handler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := svc.Handle(context.Background(), Input{
		Text:     "帮我看看今天有什么课",
		Identity: store.Identity{ConversationType: "private"},
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgentIgnoresGroupMessages(t *testing.T) {
	svc := &Service{enabled: true}
	for _, conversationType := range []string{"group", " group ", "GROUP"} {
		reply, ok := svc.Handle(context.Background(), Input{
			Text:     "帮我看看今天有什么课",
			Identity: store.Identity{ConversationType: conversationType},
		})
		if ok || reply != "" {
			t.Fatalf("conversationType %q reply = %q, ok = %v", conversationType, reply, ok)
		}
	}
}

func TestAgentIgnoresBlankMessages(t *testing.T) {
	svc := &Service{enabled: true}
	reply, ok := svc.Handle(context.Background(), Input{
		Text:     " \t\n ",
		Identity: store.Identity{ConversationType: "private"},
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestNewNormalizesModelCredentials(t *testing.T) {
	var gotAuth string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  " test-key ",
		BaseURL: " " + server.URL + "/ ",
		Model:   " test-model ",
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Fatalf("reply = %q", reply.Content)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestNewUsesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 20 * time.Millisecond,
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err == nil || !isTimeoutError(err) {
		t.Fatalf("Generate error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("configured timeout was not used, elapsed = %s", elapsed)
	}
}

func TestNewRetriesTransientChatCompletionTransportError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if attempts.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	svc, err := New(context.Background(), Config{
		Enabled: true,
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: time.Second,
	}, commands.Handler{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Fatalf("reply = %q", reply.Content)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestAgentToolConstruction(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	mcpURL, mcpHTTPClient, closeMCP := newAgentMCPTestServer(t)
	defer closeMCP()

	svc := &Service{
		handler:   commands.Handler{Store: db},
		auth:      &auth.Manager{Store: db},
		mcpClient: botmcp.New(mcpURL, mcpHTTPClient),
	}
	assertAgentToolNames(t, svc,
		"get_current_semester",
		"get_current_time",
		"invoke_bot_capability",
		"list_my_homeworks",
		"record_bot_feedback",
		"send_message_part",
		"search_courses",
	)
}

func TestAgentToolConstructionSkipsUnavailableCommandTools(t *testing.T) {
	assertAgentToolNames(t, &Service{},
		"get_current_time",
		"invoke_bot_capability",
		"send_message_part",
	)
}

func TestAgentToolConstructionKeepsStoreOnlyCommandTools(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	assertAgentToolNames(t, &Service{handler: commands.Handler{Store: db}},
		"get_current_time",
		"invoke_bot_capability",
		"record_bot_feedback",
		"send_message_part",
	)
}

func TestToolsForKeepsHostCapabilitiesWhenMCPTokenMissing(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", UserCode: "ABCD", VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		handler:   commands.Handler{Store: db, Auth: &auth.Manager{Store: db}},
		auth:      &auth.Manager{Store: db},
		mcpClient: botmcp.New("http://127.0.0.1:1/api/mcp", http.DefaultClient),
	}
	var logs bytes.Buffer
	svc.logger = log.New(&logs, "", 0)
	names := agentToolNames(t, svc)
	if !names["invoke_bot_capability"] {
		t.Fatalf("host capability tool is missing: %#v", names)
	}
	if !strings.Contains(logs.String(), "MCP tools unavailable") {
		t.Fatalf("MCP failure was not logged: %q", logs.String())
	}
}

func TestToolsForKeepsHostCapabilitiesWhenMCPResourceIsNotApproved(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": serverURL, "token_endpoint": serverURL + "/token",
			})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"resource not approved"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(context.Background(), ident, store.Credential{
		ClientID: "client", AccessToken: "expired", RefreshToken: "refresh",
		ExpiresAt: time.Now().Add(-time.Hour), Resource: server.URL + " " + server.URL + "/api/mcp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLoginSession(context.Background(), ident, store.LoginSession{
		DeviceCode: "device", UserCode: "ABCD", VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := &Service{
		enabled:   true,
		handler:   commands.Handler{Store: db, Auth: &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db}},
		auth:      &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		mcpClient: botmcp.New(server.URL+"/api/mcp", server.Client()),
		logger:    log.New(&logs, "", 0),
	}

	names := agentToolNames(t, svc)
	if !names["invoke_bot_capability"] {
		t.Fatalf("host capability tool is missing: %#v", names)
	}
	if !strings.Contains(logs.String(), "MCP tools unavailable") || !strings.Contains(logs.String(), "invalid_target") {
		t.Fatalf("logs = %q", logs.String())
	}
	credential, err := db.Credential(context.Background(), ident)
	if err != nil || credential != nil {
		t.Fatalf("credential = %#v, err = %v; want deleted", credential, err)
	}
}

func TestMCPAuthorizationFailureDoesNotLoopReauthorizationForCurrentScopes(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	currentScopes := strings.Join([]string{
		"openid", "profile", "email", "offline_access", "account.profile:read", "account.client-activity:read",
		"workspace.todo:read", "workspace.todo:write", "workspace.homework:read", "workspace.homework:write",
		"workspace.subscription:read", "workspace.subscription:write", "workspace.calendar-feed:read", "workspace.calendar:read",
		"community.comment:read", "community.comment:write", "community.description:read", "community.description:write",
		"community.user:read", "community.section-homework:read", "community.section-homework:write",
		"workspace.upload:read", "workspace.upload:write", "workspace.overview:read", "workspace.link-pin:read", "workspace.link-pin:write",
		"catalog.bus:read", "workspace.bus-preferences:read", "workspace.bus-preferences:write",
		"catalog.course:read", "catalog.section:read", "catalog.teacher:read", "catalog.schedule:read", "workspace.schedule:read",
		"catalog.exam:read", "workspace.exam:read",
	}, " ")
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), Scope: currentScopes,
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{auth: &auth.Manager{Store: db}}
	reply := svc.mcpFailureReply(ctx, ident, 0, transport.ErrAuthorizationRequired)
	if strings.Contains(reply, "请发送：登录") || !strings.Contains(reply, "请稍后重试") {
		t.Fatalf("reply = %q", reply)
	}
	if credential, err := db.Credential(ctx, ident); err != nil || credential == nil {
		t.Fatalf("credential = %#v, err = %v; want preserved", credential, err)
	}
}

func agentToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	tools, session, err := svc.toolsFor(context.Background(), store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}, nil, func(context.Context, store.Identity, string) error {
		return nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		defer func() { _ = session.Close() }()
	}
	names := map[string]bool{}
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if names[info.Name] {
			t.Fatalf("duplicate tool %q", info.Name)
		}
		names[info.Name] = true
	}
	return names
}

func newAgentMCPTestServer(t *testing.T) (string, *http.Client, func()) {
	t.Helper()
	mcpServer := mcpserver.NewMCPServer("agent-test", "1.0.0")
	for _, tool := range []mcpgo.Tool{
		mcpgo.NewTool("list_my_homeworks", mcpgo.WithDescription("List my homeworks.")),
		mcpgo.NewTool("search_courses", mcpgo.WithDescription("Search courses.")),
		mcpgo.NewTool("get_current_semester", mcpgo.WithDescription("Get current semester.")),
		mcpgo.NewTool("delete_my_homework", mcpgo.WithDescription("Delete a homework.")),
	} {
		tool := tool
		readOnly := tool.Name != "delete_my_homework"
		tool.Annotations.ReadOnlyHint = &readOnly
		mcpServer.AddTool(tool, func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(`{"ok":true}`), nil
		})
	}
	handler := mcpserver.NewStreamableHTTPServer(mcpServer)
	server := httptest.NewServer(handler)
	return server.URL, server.Client(), server.Close
}

func assertAgentToolNames(t *testing.T, svc *Service, wantNames ...string) {
	t.Helper()
	names := agentToolNames(t, svc)
	if len(names) != len(wantNames) {
		t.Fatalf("tool count = %d, want %d; tools = %#v", len(names), len(wantNames), names)
	}
	seen := map[string]bool{}
	for _, name := range wantNames {
		if seen[name] {
			t.Fatalf("duplicate expected tool %q", name)
		}
		seen[name] = true
		if !names[name] {
			t.Fatalf("missing tool %q; tools = %#v", name, names)
		}
	}
}

func TestToolResultMiddlewarePropagatesErrors(t *testing.T) {
	ctx := context.Background()
	input := &compose.ToolInput{Name: "test_tool", Arguments: "{}", CallID: "call-1"}
	var logs bytes.Buffer
	logf := log.New(&logs, "", 0).Printf

	failing := toolResultMiddleware(logf)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, errors.New("bad args")
	})
	out, err := failing(ctx, input)
	if err == nil || out != nil || !strings.Contains(err.Error(), "bad args") {
		t.Fatalf("result = %#v, err = %v", out, err)
	}
	if !strings.Contains(logs.String(), "bad args") || !strings.Contains(logs.String(), "test_tool") {
		t.Fatalf("logs = %q", logs.String())
	}

	recoverableErr := recoverableMCPToolError(t)
	recoverable := toolResultMiddleware(logf)(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, recoverableErr
	})
	out, err = recoverable(ctx, input)
	if err != nil || out == nil || !strings.Contains(out.Result, `"ok":false`) ||
		!strings.Contains(out.Result, "semesterJwId") {
		t.Fatalf("recoverable result = %#v, err = %v", out, err)
	}

	ok := toolResultMiddleware(nil)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	out, err = ok(ctx, input)
	if err != nil || out.Result != "ok" {
		t.Fatalf("ok result = %q, err = %v", out.Result, err)
	}
}

func recoverableMCPToolError(t *testing.T) error {
	t.Helper()
	mcpServer := mcpserver.NewMCPServer("agent-test", "1.0.0")
	mcpServer.AddTool(mcpgo.NewTool("search_courses"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultError("semesterJwId must be greater than 0"), nil
	})
	server := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	t.Cleanup(server.Close)
	session, err := botmcp.New(server.URL, server.Client()).OpenSession(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	_, err = session.Call(context.Background(), "search_courses", nil)
	if err == nil {
		t.Fatal("MCP error result was not classified")
	}
	return err
}

func TestToolTraceNotifierSendsCallAndResultTogether(t *testing.T) {
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var messages []string
	trace := &toolTraceNotifier{
		ident: ident,
		send: func(ctx context.Context, gotIdent store.Identity, message string) error {
			if gotIdent != ident {
				t.Fatalf("identity = %#v", gotIdent)
			}
			messages = append(messages, message)
			return nil
		},
	}

	trace.Notify(context.Background(), "search_courses", struct {
		Keyword string `json:"keyword"`
	}{Keyword: "数学分析"}, "课程：数学分析\n教师：张三", nil)
	trace.Notify(context.Background(), "get_current_time", emptyInput{}, "", nil)
	trace.Notify(context.Background(), "search_courses", struct{}{}, "", errors.New("token=secret upstream exploded"))
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0] != "工具调用：search_courses {\"keyword\":\"数学分析\"}\n工具结果：\n课程：数学分析\n教师：张三" {
		t.Fatalf("message 0 = %q", messages[0])
	}
	if messages[1] != "工具调用：get_current_time\n工具结果：" {
		t.Fatalf("message 1 = %q", messages[1])
	}
	if strings.Contains(messages[2], "token=secret") || !strings.Contains(messages[2], "工具暂时不可用") {
		t.Fatalf("message 2 exposes error = %q", messages[2])
	}
}

func TestRecordBotFeedbackStoresFeedback(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	recorder, err := botfeedback.New(db, botfeedback.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{feedback: recorder}

	reply, err := svc.recordBotFeedback(context.Background(), ident, feedbackInput{
		Category: "missing_tool",
		Content:  "需要一个考试地点工具",
		Context:  "用户问考试在哪里",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已记录反馈") {
		t.Fatalf("reply = %q", reply)
	}
	count, err := db.FeedbackCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
}

func TestRecordBotFeedbackQueuesConfiguredAdmins(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "qqbot", UserID: "openid-user", ConversationType: "private", ConversationID: "openid-user"}
	recorder, err := botfeedback.New(db, botfeedback.Config{Targets: []botfeedback.Target{
		{Platform: "napcat", ConversationType: "private", ConversationID: "admin-openid"},
		{Platform: "napcat", ConversationType: "group", ConversationID: "group-openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{feedback: recorder}

	reply, err := svc.recordBotFeedback(context.Background(), ident, feedbackInput{
		Category: "api_gap",
		Content:  "需要按日期查询课表",
		Context:  "用户问 6.23 课表",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已转给维护者") {
		t.Fatalf("reply = %q", reply)
	}
	due, err := db.ClaimDue(context.Background(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("intents = %#v", due)
	}
	if due[0].Message.Target.Platform != "napcat" || due[0].Message.Target.Type != "private" || due[0].Message.Target.ID != "admin-openid" {
		t.Fatalf("private target = %#v", due[0].Message.Target)
	}
	if due[1].Message.Target.Platform != "napcat" || due[1].Message.Target.Type != "group" || due[1].Message.Target.ID != "group-openid" {
		t.Fatalf("group target = %#v", due[1].Message.Target)
	}
	if !strings.Contains(due[0].Message.Content.Text, "LLM 反馈") || !strings.Contains(due[0].Message.Content.Text, "需要按日期查询课表") || !strings.Contains(due[0].Message.Content.Text, "编号：#1") {
		t.Fatalf("message = %q", due[0].Message.Content.Text)
	}
}

func TestRecordBotFeedbackStoresWithoutAdminTargets(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "qqbot", UserID: "openid-user", ConversationType: "private", ConversationID: "openid-user"}
	recorder, err := botfeedback.New(db, botfeedback.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{feedback: recorder}

	reply, err := svc.recordBotFeedback(context.Background(), ident, feedbackInput{Content: "需要按日期查询课表"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已记录反馈 #1") || strings.Contains(reply, "已转给维护者") {
		t.Fatalf("reply = %q", reply)
	}
	count, err := db.FeedbackCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
	due, err := db.ClaimDue(context.Background(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("unexpected intents = %#v", due)
	}
}

func TestSendMessagePartUsesSender(t *testing.T) {
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var gotIdent store.Identity
	var gotMessage string
	reply, err := sendMessagePart(context.Background(), ident, func(ctx context.Context, ident store.Identity, message string) error {
		gotIdent = ident
		gotMessage = message
		return nil
	}, messagePartInput{Content: "第一段"})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "已发送。" || gotIdent != ident || gotMessage != "第一段" {
		t.Fatalf("reply = %q ident = %#v message = %q", reply, gotIdent, gotMessage)
	}
}

func TestFormatToolResultTruncatesLongText(t *testing.T) {
	got := formatToolResult(strings.Repeat("好", 1001))
	if !strings.HasSuffix(got, "\n...") {
		t.Fatalf("result was not truncated: %q", got)
	}
}

func TestCleanQQReplyRemovesMarkdownTables(t *testing.T) {
	input := `---

## 明天行程
| 时间 | 事项 | 地点 |
|------|------|------|
| **07:50-09:25** | 组合数学 | 高新区 **GT-B112** |

> 建议下课后出发
详情见 [校车](https://example.test/bus)`
	got := cleanQQReply(input)
	want := "明天行程\n时间  事项  地点\n07:50-09:25  组合数学  高新区 GT-B112\n\n建议下课后出发\n详情见 校车"
	if got != want {
		t.Fatalf("cleanQQReply = %q", got)
	}
}

func TestCurrentTimeHelpersUseShanghaiTime(t *testing.T) {
	now := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	if got := currentTimeMessageAt(now); got != "现在是 2026-06-07 18:30，Asia/Shanghai。" {
		t.Fatalf("currentTimeMessageAt = %q", got)
	}
	instruction := currentInstructionAt(now)
	if !strings.HasPrefix(instruction, "You are Presto,") || strings.Contains(strings.ToLower(instruction), "signal_bot") {
		t.Fatalf("instruction identity = %q", instruction)
	}
	if strings.Contains(instruction, "Current local time is") {
		t.Fatalf("instruction should not embed wall-clock time (cache stability): %q", instruction)
	}
	if !strings.Contains(instruction, "prepare and confirm them one at a time") || !strings.Contains(instruction, "Avoid emojis") {
		t.Fatalf("instruction = %q", instruction)
	}
	if !strings.Contains(instruction, "Never invent prices, menus, locations, schedules, bus times, or service availability") {
		t.Fatalf("instruction lacks grounding rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Preserve every user constraint") || !strings.Contains(instruction, "dates, times, filters, targets, and direction") || !strings.Contains(instruction, "never replace a requested value with a default") {
		t.Fatalf("instruction lacks universal argument-preservation rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never ask whether to record feedback") {
		t.Fatalf("instruction lacks automatic feedback rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never use Markdown tables") {
		t.Fatalf("instruction lacks QQ plain-text rule: %q", instruction)
	}
	if !strings.Contains(instruction, "invoke_bot_capability") || !strings.Contains(instruction, "confirmation_required") || !strings.Contains(instruction, "suggestedCalls") {
		t.Fatalf("instruction lacks capability workflow: %q", instruction)
	}
	if !strings.Contains(strings.ToLower(instruction), "personal icalendar subscription url") || strings.Contains(strings.ToLower(instruction), "caldav") {
		t.Fatalf("instruction lacks accurate calendar subscription guidance: %q", instruction)
	}
	for _, obsolete := range []string{"execute_bot_command", "resolve_image_command", "![]("} {
		if strings.Contains(instruction, obsolete) {
			t.Fatalf("instruction retained obsolete protocol %q: %q", obsolete, instruction)
		}
	}
}

func TestHostCapabilityToolDeliversPrivateCalendarURLWithoutModelExposure(t *testing.T) {
	ctx := context.Background()
	calendarURL := "https://life.example/api/calendar-feeds/user-1:private-token.ics"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"subscription":{"calendarUrl":%q}}`, calendarURL)
	}))
	t.Cleanup(server.Close)

	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveCredential(ctx, ident, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	authManager := &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db}
	svc := &Service{handler: commands.Handler{
		Life: life.NewClient(server.URL, server.Client()), Auth: authManager, Store: db,
	}, auth: authManager}

	var delivered commands.Response
	tools, session, err := svc.toolsFor(ctx, ident, nil, nil, func(_ context.Context, _ store.Identity, response commands.Response) error {
		delivered = response
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	var hostTool einotool.InvokableTool
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "invoke_bot_capability" {
			var ok bool
			hostTool, ok = candidate.(einotool.InvokableTool)
			if !ok {
				t.Fatalf("host capability tool is not invokable: %T", candidate)
			}
			break
		}
	}
	if hostTool == nil {
		t.Fatal("invoke_bot_capability tool is missing")
	}
	result, err := hostTool.InvokableRun(ctx, `{"capability":"subscription","arguments":["link"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delivered.Text, calendarURL) || !strings.Contains(delivered.Text, "通过 URL 添加/订阅日历") {
		t.Fatalf("delivered response = %#v", delivered)
	}
	if strings.Contains(result, calendarURL) || !strings.Contains(result, `"ok":true`) || !strings.Contains(result, `"status":"success"`) || !strings.Contains(result, `"deliveredByHost":true`) {
		t.Fatalf("model-facing tool result = %q", result)
	}
}

func TestHostCapabilityDeliversAuthWaitWithoutModelExposure(t *testing.T) {
	ctx := context.Background()
	const userCode = "ABCD-SECRET"
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.SaveLoginSession(ctx, ident, store.LoginSession{
		DeviceCode: "device", UserCode: userCode, VerificationURI: "https://login.example/device",
		ClientID: "client", ExpiresAt: time.Now().Add(10 * time.Minute), IntervalSeconds: 5, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	authManager := &auth.Manager{Store: db}
	svc := &Service{handler: commands.Handler{
		Life: life.NewClient("http://life.invalid", nil), Auth: authManager, Store: db,
	}, auth: authManager}

	var delivered commands.Response
	tools, session, err := svc.toolsFor(ctx, ident, nil, nil, func(_ context.Context, _ store.Identity, response commands.Response) error {
		delivered = response
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	var hostTool einotool.InvokableTool
	for _, candidate := range tools {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "invoke_bot_capability" {
			var ok bool
			hostTool, ok = candidate.(einotool.InvokableTool)
			if !ok {
				t.Fatalf("host capability tool is not invokable: %T", candidate)
			}
			break
		}
	}
	if hostTool == nil {
		t.Fatal("invoke_bot_capability tool is missing")
	}
	result, err := hostTool.InvokableRun(ctx, `{"capability":"schedule","arguments":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Kind != commands.ResponseKindAuthWait || !strings.Contains(delivered.Text, userCode) {
		t.Fatalf("delivered auth response = %#v", delivered)
	}
	if strings.Contains(result, userCode) || strings.Contains(result, "login.example") || !strings.Contains(result, `"ok":false`) || !strings.Contains(result, `"status":"auth_required"`) {
		t.Fatalf("model-facing auth result = %q", result)
	}
}

func TestHostCapabilityDescriptionUsesStructuredCallsAndDefersDynamicPolicy(t *testing.T) {
	description := hostCapabilityToolDescription()
	if !strings.Contains(description, `{"capability":"subscription","arguments":["link"]}`) {
		t.Fatalf("description lacks structured subscription call: %q", description)
	}
	if !strings.Contains(description, `{"capability":"course_by_jw_id","arguments":["12345"]}`) || !strings.Contains(description, `{"capability":"section_schedules","arguments":["12345","2026-09-01","2026-09-30"]}`) {
		t.Fatalf("description lacks exact structured identifier/date calls: %q", description)
	}
	if !strings.Contains(description, `{"capability":"bus","arguments":["2026-09-06","东区","太湖路园区"]}`) {
		t.Fatalf("description lacks exact dated bus call: %q", description)
	}
	if !strings.Contains(description, `capability subscription and arguments ["link"] exactly`) || !strings.Contains(description, "no MCP tool can provide that private URL") {
		t.Fatalf("description lacks private calendar URL routing rule: %q", description)
	}
	for _, status := range []string{"ok", "success", "invalid_input", "forbidden", "confirmation_required", "auth_required", "not_found", "ok:false"} {
		if !strings.Contains(description, status) {
			t.Fatalf("description lacks status guidance %q: %q", status, description)
		}
	}
	if !strings.Contains(description, "never ask the user to type or copy a command") || !strings.Contains(description, "do not expose, repeat, or request any verification code") {
		t.Fatalf("description lacks host/auth workflow: %q", description)
	}
	if strings.Contains(description, "confirmation=never") || strings.Contains(description, "订阅 链接") {
		t.Fatalf("description exposes misleading policy or command syntax: %q", description)
	}
}

func TestMessagesForIncludesRecentHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := db.RecordInteraction(ctx, ident, store.Interaction{
		Direction: store.InteractionDirectionOutbound,
		RawText:   "工具调用：search_courses {\"keyword\":\"数学分析\"}\n工具结果：\n课程：数学分析",
		Handled:   true,
		Status:    store.InteractionStatusSent,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordInteraction(ctx, ident, store.Interaction{
		RawText: "  你好  ",
		Command: "agent",
		Handled: true,
		Reply:   "  你好！\n[已发送图片：课表 2026-09-04]\n有什么可以帮你的吗？  ",
		Status:  "handled",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	messages, err := svc.messagesFor(ctx, Input{Text: "  我上面说了什么？  ", Identity: ident})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d", len(messages))
	}
	if messages[0].Content != "你好" ||
		messages[1].Content != "你好！\n[已发送图片：课表 2026-09-04]\n有什么可以帮你的吗？" ||
		!strings.Contains(messages[2].Content, "我上面说了什么？") ||
		!strings.HasPrefix(messages[2].Content, "现在是 ") {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestMessagesForCompactsLongHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	longReply := strings.Repeat("课", maxHistoryTextRunes+20)
	if err := db.RecordInteraction(ctx, ident, store.Interaction{
		RawText: "课表",
		Command: "schedule",
		Handled: true,
		Reply:   longReply,
		Status:  store.InteractionStatusHandled,
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	messages, err := svc.messagesFor(ctx, Input{Text: "总结一下", Identity: ident})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d", len(messages))
	}
	if got := []rune(messages[1].Content); len(got) <= maxHistoryTextRunes || len(got) > maxHistoryTextRunes+20 {
		t.Fatalf("compacted length = %d", len(got))
	}
	if !strings.Contains(messages[1].Content, "历史内容已截断") {
		t.Fatalf("reply was not marked compacted: %q", messages[1].Content)
	}
}

func TestConversationCompactionKeepsSummaryAndRecentTurns(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for i := 1; i <= conversationCompactTurnLimit+1; i++ {
		if err := db.RecordInteraction(ctx, ident, store.Interaction{
			RawText: fmt.Sprintf("turn-%02d %s", i, strings.Repeat("问", maxHistoryTextRunes)),
			Handled: true,
			Reply:   fmt.Sprintf("reply-%02d %s", i, strings.Repeat("答", maxHistoryTextRunes)),
			Status:  store.InteractionStatusHandled,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var summaryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaryRequests.Add(1)
		var request struct {
			Messages []map[string]any `json:"messages"`
			Tools    []any            `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 0 {
			t.Fatalf("summary request unexpectedly included tools: %#v", request.Tools)
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-summary",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"用户此前逐轮测试了对话记忆。"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
		Logger: log.New(&logs, "", 0),
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.compactConversationHistory(ctx, ident, svc.model, 77); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"llm compaction started: run_id=77",
		"compacted_turns=21 retained_turns=4",
		"estimated_input_tokens=",
		"llm compaction completed: run_id=77",
		"summary_runes=14",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("compaction logs missing %q: %q", want, logs.String())
		}
	}
	if summaryRequests.Load() != 1 {
		t.Fatalf("summary requests = %d", summaryRequests.Load())
	}
	summary, found, err := db.ConversationSummary(ctx, ident)
	if err != nil || !found {
		t.Fatalf("summary found = %v, err = %v", found, err)
	}
	if summary.Summary != "用户此前逐轮测试了对话记忆。" {
		t.Fatalf("summary = %#v", summary)
	}
	messages, err := svc.messagesFor(ctx, Input{Text: "continue", Identity: ident})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1+4*2+1 {
		t.Fatalf("message count = %d, messages = %#v", len(messages), messages)
	}
	if !strings.HasPrefix(messages[0].Content, conversationSummaryPrefix) ||
		!strings.Contains(messages[1].Content, "turn-22") ||
		!strings.Contains(messages[len(messages)-1].Content, "continue") ||
		!strings.HasPrefix(messages[len(messages)-1].Content, "现在是 ") {
		t.Fatalf("messages = %#v", messages)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "turn-01") {
			t.Fatalf("compacted raw turn leaked into recent history: %#v", messages)
		}
	}
}

func TestHandleResponseCompactsHistoryBeforeAnswering(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for i := 1; i <= conversationCompactTurnLimit+1; i++ {
		if err := db.RecordInteraction(ctx, ident, store.Interaction{
			RawText: fmt.Sprintf("turn-%02d", i),
			Handled: true,
			Reply:   fmt.Sprintf("reply-%02d", i),
			Status:  store.InteractionStatusHandled,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests.Add(1) {
		case 1:
			if bytes.Contains(body, []byte(`"tools"`)) {
				t.Fatalf("summary request included tools: %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-summary",
				"object":"chat.completion",
				"created":0,
				"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"earlier summary"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
			}`))
		case 2:
			if !bytes.Contains(body, []byte("earlier summary")) ||
				!bytes.Contains(body, []byte("turn-16")) ||
				bytes.Contains(body, []byte("turn-01")) {
				t.Fatalf("agent request history = %s", body)
			}
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-answer",
				"object":"chat.completion",
				"created":0,
				"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"continued"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
			}`))
		default:
			t.Fatalf("unexpected model request %d", requests.Load())
		}
	}))
	defer server.Close()
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{Text: "continue", Identity: ident})
	if !ok || response.Text != "continued" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	total, err := db.ConversationSpending(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if total.ModelRequests != 2 || total.PromptTokens != 8 || total.CompletionTokens != 2 {
		t.Fatalf("spending = %#v", total)
	}
}

func TestHandleResponseRejectsHardLimitImageBeforeCompaction(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "admin", ConversationType: "private", ConversationID: "admin"}
	for i := 1; i <= conversationCompactTurnLimit+1; i++ {
		if err := db.RecordInteraction(ctx, ident, store.Interaction{
			RawText: fmt.Sprintf("turn-%02d", i), Handled: true, Reply: "reply", Status: store.InteractionStatusHandled,
		}); err != nil {
			t.Fatal(err)
		}
	}
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxImageDownloadBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer imageServer.Close()
	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer modelServer.Close()
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "default", BaseURL: modelServer.URL, Model: "default",
		PremiumAPIKey: "premium", PremiumBaseURL: modelServer.URL, PremiumModel: "premium",
	}, commands.Handler{Store: db}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{
		Text: "看图", ImageURLs: []string{imageServer.URL + "/too-large.png"}, Identity: ident,
	})
	if !ok || !strings.Contains(response.Text, "AI 图片处理失败：图片超过 25 MiB 安全上限") {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if modelRequests.Load() != 0 {
		t.Fatalf("model requests before image validation = %d", modelRequests.Load())
	}
	if _, found, err := db.ConversationSummary(ctx, ident); err != nil || found {
		t.Fatalf("summary found = %v, err = %v", found, err)
	}
}

func TestHandleResponseStopsAtModelIterationLimit(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-loop",
			"object":"chat.completion",
			"created":0,
			"model":"test-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call-loop",
						"type":"function",
						"function":{"name":"get_current_time","arguments":"{}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: server.URL, Model: "test-model",
	}, commands.Handler{Store: db}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, ok := svc.HandleResponse(ctx, Input{Text: "loop", Identity: ident})
	if !ok {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if !strings.Contains(response.Text, "重复调用了相同工具") {
		t.Fatalf("response = %#v", response)
	}
	if got := int(requests.Load()); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
	total, err := db.ConversationSpending(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if total.ModelRequests != 2 || total.ToolCalls != 1 {
		t.Fatalf("spending = %#v", total)
	}
}

func TestHandleResponseTreatsSuccessfulHostDeliveryAsHandledWhenModelReplyIsEmpty(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var authServerURL string
	authMux := http.NewServeMux()
	authMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": authServerURL + "/device",
			"token_endpoint":                authServerURL + "/token",
			"registration_endpoint":         authServerURL + "/register",
		})
	})
	authMux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	authMux.HandleFunc("/device", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "device", "user_code": "USER-CODE",
			"verification_uri": authServerURL + "/verify", "expires_in": 300, "interval": 5,
		})
	})
	authServer := httptest.NewServer(authMux)
	defer authServer.Close()
	authServerURL = authServer.URL
	manager := &auth.Manager{Server: authServer.URL, HTTPClient: authServer.Client(), Store: db}

	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		request := modelRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-host-tool","object":"chat.completion","created":0,"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-host","type":"function","function":{"name":"invoke_bot_capability","arguments":"{\"capability\":\"login\",\"arguments\":[]}"}
				}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-empty","object":"chat.completion","created":0,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}
		}`))
	}))
	defer modelServer.Close()

	svc, err := New(ctx, Config{
		Enabled: true, APIKey: "test-key", BaseURL: modelServer.URL, Model: "test-model",
	}, commands.Handler{Auth: manager, Store: db}, modelServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	var deliveries atomic.Int32
	response, ok := svc.HandleResponse(ctx, Input{
		Text: "帮我登录", Identity: ident,
		SendResponse: func(_ context.Context, got store.Identity, delivered commands.Response) error {
			if got != ident || delivered.Kind != commands.ResponseKindAuthWait || !strings.Contains(delivered.Text, "USER-CODE") {
				t.Fatalf("host response identity=%#v response=%#v", got, delivered)
			}
			deliveries.Add(1)
			return nil
		},
	})
	if !ok || response.Text != "" || response.Image != nil || response.Kind != "" || len(response.Parts) != 0 {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if got := deliveries.Load(); got != 1 {
		t.Fatalf("host deliveries = %d, want 1", got)
	}
	if got := modelRequests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}
}

func TestHandleResponsePropagatesCancellationToModelAndCaller(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "cancel-user", ConversationType: "private", ConversationID: "cancel-user"}
	client := &http.Client{Transport: blockingRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
		return nil, r.Context().Err()
	})}

	svc, err := New(context.Background(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: "http://model.test", Model: "test-model",
	}, commands.Handler{Store: db}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan struct {
		response commands.Response
		ok       bool
	}, 1)
	go func() {
		response, ok := svc.HandleResponse(ctx, Input{
			Text: "等待取消", Identity: ident,
		})
		result <- struct {
			response commands.Response
			ok       bool
		}{response: response, ok: ok}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("model request context was not canceled")
	}
	select {
	case got := <-result:
		if got.ok || got.response.Text != "" || got.response.Image != nil || got.response.Kind != "" || len(got.response.Parts) != 0 {
			t.Fatalf("canceled response = %#v, ok = %v", got.response, got.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled agent run did not return")
	}
	if interrupted, err := db.InterruptStartedAgentRuns(context.Background()); err != nil {
		t.Fatal(err)
	} else if interrupted != 0 {
		t.Fatalf("canceled run remained started: interrupted=%d", interrupted)
	}
}

func TestHandleResponseFinalizesExpiredRunContext(t *testing.T) {
	requestStarted := make(chan struct{})
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "timeout-user", ConversationType: "private", ConversationID: "timeout-user"}
	client := &http.Client{Transport: blockingRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	svc, err := New(context.Background(), Config{
		Enabled: true, APIKey: "test-key", BaseURL: "http://model.test", Model: "test-model",
	}, commands.Handler{Store: db}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentRunCleanupTimeout+100*time.Millisecond)
	defer cancel()
	result := make(chan struct {
		response commands.Response
		ok       bool
	}, 1)
	go func() {
		response, ok := svc.HandleResponse(ctx, Input{Text: "等待超时", Identity: ident})
		result <- struct {
			response commands.Response
			ok       bool
		}{response: response, ok: ok}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	select {
	case got := <-result:
		if !got.ok || !strings.Contains(got.response.Text, "AI 响应超时") {
			t.Fatalf("timed out response = %#v, ok = %v", got.response, got.ok)
		}
	case <-time.After(agentRunCleanupTimeout + 3*time.Second):
		t.Fatal("timed out agent run did not return")
	}
	if interrupted, err := db.InterruptStartedAgentRuns(context.Background()); err != nil {
		t.Fatal(err)
	} else if interrupted != 0 {
		t.Fatalf("timed out run remained started: interrupted=%d", interrupted)
	}
}

type blockingRoundTripper func(*http.Request) (*http.Response, error)

func (f blockingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestToolResultMiddlewareLimitsLargeResults(t *testing.T) {
	accumulator := &usageAccumulator{}
	next := func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: strings.Repeat("课", maxToolResultRunes+10)}, nil
	}
	out, err := toolResultMiddleware(nil)(next)(
		withUsageAccumulator(context.Background(), accumulator),
		&compose.ToolInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(out.Result)); got <= maxToolResultRunes || !strings.Contains(out.Result, "工具结果过长") {
		t.Fatalf("limited result length = %d, result suffix missing", got)
	}
	if got := accumulator.snapshot().ToolCalls; got != 1 {
		t.Fatalf("tool calls = %d", got)
	}
}

func TestAgentFailureReplyHidesProviderTimeoutAndIncludesTrace(t *testing.T) {
	reply := agentFailureReply(42, context.DeadlineExceeded)
	if !strings.Contains(reply, "AI 响应超时") || !strings.Contains(reply, "记录 #42") {
		t.Fatalf("reply = %q", reply)
	}
	if strings.Contains(reply, "deadline") {
		t.Fatalf("reply exposes provider error: %q", reply)
	}
}

func TestImageFailureReplyOnlyExposesInputValidation(t *testing.T) {
	internal := imageFailureReply(7, errors.New("GET https://secret.example: dial tcp 10.0.0.1: refused"))
	if !strings.Contains(internal, "图片处理失败，请稍后重试") || !strings.Contains(internal, "记录 #7") {
		t.Fatalf("internal reply = %q", internal)
	}
	if strings.Contains(internal, "secret.example") || strings.Contains(internal, "10.0.0.1") {
		t.Fatalf("internal details exposed: %q", internal)
	}

	validation := imageFailureReply(8, newImageInputError("图片超过 25 MiB 安全上限"))
	if !strings.Contains(validation, "图片超过 25 MiB 安全上限") || !strings.Contains(validation, "记录 #8") {
		t.Fatalf("validation reply = %q", validation)
	}
}

func TestFinishAgentRunLogsErrors(t *testing.T) {
	var logs bytes.Buffer
	svc := &Service{logger: log.New(&logs, "", 0)}
	svc.finishAgentRun(context.Background(), 0, store.Identity{}, store.AgentRunStatusFailed, "", context.DeadlineExceeded, "deepseek", "test", tokenUsage{}, time.Second)
	if !strings.Contains(logs.String(), "agent run failed") || !strings.Contains(logs.String(), "context deadline exceeded") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestFinishAgentRunLogsUsageAndTotals(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs bytes.Buffer
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc := &Service{logger: log.New(&logs, "", 0), handler: commands.Handler{Store: db}}
	id := svc.recordAgentRun(ctx, Input{Text: "hello", Identity: ident}, "kimi", "kimi-k3")
	svc.finishAgentRun(ctx, id, ident, store.AgentRunStatusCompleted, "ok", nil, "kimi", "kimi-k3", tokenUsage{
		PromptTokens: 100, CachedTokens: 10, CacheMissTokens: 90, CompletionTokens: 20,
		TotalTokens: 120, ModelRequests: 2, ToolCalls: 1,
	}, 1500*time.Millisecond)
	for _, want := range []string{
		"llm run started: id=1 provider=kimi model=kimi-k3",
		"llm run completed: id=1 status=completed provider=kimi model=kimi-k3",
		"prompt_tokens=100 cached_tokens=10 completion_tokens=20 total_tokens=120",
		"model_requests=2 tool_calls=1 estimated_cost_cny=0.003820 duration_ms=1500",
		"llm spending totals: id=1 conversation_cost_cny=0.003820 user_cost_cny=0.003820",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("usage logs missing %q: %q", want, logs.String())
		}
	}
}
