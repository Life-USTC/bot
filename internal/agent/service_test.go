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

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
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

func TestResponseForExpandsImageDirectivesInOrder(t *testing.T) {
	svc := newImageDirectiveTestService(t)
	response := svc.responseFor(context.Background(), Input{
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "private",
			ConversationID:   "42",
		},
	}, "先看图：\n\n![](校车 东区 西区)\n\n建议提前到站。")

	if len(response.Parts) != 3 {
		t.Fatalf("parts = %#v", response.Parts)
	}
	if response.Parts[0].Text != "先看图：" {
		t.Fatalf("first part = %#v", response.Parts[0])
	}
	if response.Parts[1].Image == nil || response.Parts[1].Kind != "bus" {
		t.Fatalf("image part = %#v", response.Parts[1])
	}
	if response.Parts[2].Text != "建议提前到站。" {
		t.Fatalf("last part = %#v", response.Parts[2])
	}
	if !strings.Contains(response.Text, "![](校车 东区 西区)") {
		t.Fatalf("history text = %q", response.Text)
	}
}

func TestResponseForRejectsMutationAndLimitsImageDirectives(t *testing.T) {
	svc := newImageDirectiveTestService(t)
	lines := []string{"开始", "![](待办 添加 不应执行)"}
	for i := 0; i < maxImageDirectives+1; i++ {
		lines = append(lines, "![](校车 东区 西区)")
	}
	lines = append(lines, "结束")
	response := svc.responseFor(context.Background(), Input{
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "private",
			ConversationID:   "42",
		},
	}, strings.Join(lines, "\n"))

	if len(response.Parts) != maxImageDirectives+2 {
		t.Fatalf("parts = %#v", response.Parts)
	}
	imageCount := 0
	for _, part := range response.Parts {
		if part.Image != nil {
			imageCount++
		}
		if strings.Contains(part.Text, "不应执行") || strings.Contains(part.Text, "![](") {
			t.Fatalf("unsafe directive leaked into part %#v", part)
		}
	}
	if imageCount != maxImageDirectives {
		t.Fatalf("image count = %d, parts = %#v", imageCount, response.Parts)
	}
	if got := strings.Count(response.Text, "![](校车 东区 西区)"); got != maxImageDirectives {
		t.Fatalf("history directive count = %d, want %d: %q", got, maxImageDirectives, response.Text)
	}
}

func TestResponseForAcceptsLegacyImageAnnotation(t *testing.T) {
	svc := newImageDirectiveTestService(t)
	response := svc.responseFor(context.Background(), Input{
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "private",
			ConversationID:   "42",
		},
	}, "[已发送图片：校车 东区 西区]")

	if len(response.Parts) != 1 || response.Parts[0].Image == nil {
		t.Fatalf("parts = %#v", response.Parts)
	}
	if response.Text != "![](校车 东区 西区)" {
		t.Fatalf("history text = %q", response.Text)
	}
}

func newImageDirectiveTestService(t *testing.T) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"西区"}}]}],
			"trips":[
				{"routeId":1,"dayType":"weekday","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[{"campusName":"东区","time":"23:59"},{"campusName":"西区","time":"23:59"}]},
				{"routeId":1,"dayType":"weekend","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[{"campusName":"东区","time":"23:59"},{"campusName":"西区","time":"23:59"}]}
			]
		}`))
	}))
	t.Cleanup(server.Close)
	return &Service{handler: commands.Handler{
		Life:                 life.NewClient(server.URL, server.Client()),
		EnableImageResponses: true,
	}}
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
		"list_my_homeworks",
		"lookup_bot_help",
		"record_bot_feedback",
		"resolve_image_command",
		"send_message_part",
		"search_courses",
	)
}

func TestAgentToolConstructionSkipsUnavailableCommandTools(t *testing.T) {
	assertAgentToolNames(t, &Service{},
		"get_current_time",
		"lookup_bot_help",
		"resolve_image_command",
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
		"lookup_bot_help",
		"record_bot_feedback",
		"resolve_image_command",
		"send_message_part",
	)
}

func TestHandlePromptsLoginWhenMCPTokenMissing(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc := &Service{
		enabled:   true,
		handler:   commands.Handler{Store: db},
		auth:      &auth.Manager{Store: db},
		mcpClient: botmcp.New("http://127.0.0.1:1/api/mcp", http.DefaultClient),
	}
	reply, ok := svc.Handle(context.Background(), Input{Text: "帮我看看作业", Identity: ident})
	if !ok || reply != "需要先登录。发送：登录" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func agentToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	tools, session, err := svc.toolsFor(context.Background(), store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}, nil, func(context.Context, store.Identity, string) error {
		return nil
	})
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
	} {
		tool := tool
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

func TestToolErrorCatchingMiddlewareReturnsErrorAsResult(t *testing.T) {
	ctx := context.Background()
	input := &compose.ToolInput{Name: "test_tool", Arguments: "{}", CallID: "call-1"}
	var logs bytes.Buffer
	logf := log.New(&logs, "", 0).Printf

	failing := toolErrorCatchingMiddleware(logf)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, errors.New("bad args")
	})
	out, err := failing(ctx, input)
	if err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if out == nil || strings.Contains(out.Result, "bad args") || !strings.Contains(out.Result, "请检查参数") {
		t.Fatalf("result = %q", out.Result)
	}
	if !strings.Contains(logs.String(), "bad args") || !strings.Contains(logs.String(), "test_tool") {
		t.Fatalf("logs = %q", logs.String())
	}

	ok := toolErrorCatchingMiddleware(nil)(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	out, err = ok(ctx, input)
	if err != nil || out.Result != "ok" {
		t.Fatalf("ok result = %q, err = %v", out.Result, err)
	}
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
	svc := &Service{handler: commands.Handler{Store: db}}

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

func TestRecordBotFeedbackSendsToConfiguredAdmins(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "qqbot", UserID: "openid-user", ConversationType: "private", ConversationID: "openid-user"}
	var targets []store.Identity
	var messages []string
	svc := &Service{handler: commands.Handler{
		Store:            db,
		FeedbackPlatform: "napcat",
		FeedbackUsers:    []string{"admin-openid"},
		FeedbackGroups:   []string{"group-openid"},
		FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
			targets = append(targets, target)
			messages = append(messages, message)
			return nil
		},
	}}

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
	if len(targets) != 2 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].Platform != "napcat" || targets[0].ConversationType != "private" || targets[0].ConversationID != "admin-openid" {
		t.Fatalf("private target = %#v", targets[0])
	}
	if targets[1].Platform != "napcat" || targets[1].ConversationType != "group" || targets[1].ConversationID != "group-openid" {
		t.Fatalf("group target = %#v", targets[1])
	}
	if !strings.Contains(messages[0], "LLM 反馈") || !strings.Contains(messages[0], "需要按日期查询课表") || !strings.Contains(messages[0], "编号：#1") {
		t.Fatalf("message = %q", messages[0])
	}
}

func TestRecordBotFeedbackStoresWhenAdminSendFails(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs bytes.Buffer
	ident := store.Identity{Platform: "qqbot", UserID: "openid-user", ConversationType: "private", ConversationID: "openid-user"}
	svc := &Service{
		logger: log.New(&logs, "", 0),
		handler: commands.Handler{
			Store:         db,
			FeedbackUsers: []string{"bad-openid"},
			FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
				return errors.New("qq bot invalid request")
			},
		},
	}

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
	if !strings.Contains(logs.String(), "send llm feedback failed") || !strings.Contains(logs.String(), "qq bot invalid request") {
		t.Fatalf("logs = %q", logs.String())
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
	if strings.Contains(instruction, "Current local time is") {
		t.Fatalf("instruction should not embed wall-clock time (cache stability): %q", instruction)
	}
	if !strings.Contains(instruction, "one command per QQ message") || !strings.Contains(instruction, "Avoid emojis") {
		t.Fatalf("instruction = %q", instruction)
	}
	if !strings.Contains(instruction, "Never invent prices, menus, locations, schedules, bus times, or service availability") {
		t.Fatalf("instruction lacks grounding rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never ask whether to record feedback") {
		t.Fatalf("instruction lacks automatic feedback rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Course / section subscribe-by-name flow") {
		t.Fatalf("instruction lacks subscribe-by-name flow: %q", instruction)
	}
	if !strings.Contains(instruction, "follow-ups sent while tools were running") {
		t.Fatalf("instruction lacks multi-paragraph follow-up rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never use Markdown tables") {
		t.Fatalf("instruction lacks QQ plain-text rule: %q", instruction)
	}
	for _, want := range []string{
		"Image rendering protocol:",
		"Image-only rewrite",
		"resolve_image_command",
		"lookup_bot_help",
		"![](校车 查询 东区 西区)",
		"![](今日课表)",
		"![](下一节课)",
		"![](待办)",
		"![](作业)",
		"![](考试)",
		"![](概览)",
		"![](近期截止 14)",
		"第N周 is current semester only",
		"reply with ONLY that ![](command) line",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction lacks image rendering guidance %q: %q", want, instruction)
		}
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
		messages[1].Content != "你好！\n![](课表 2026-09-04)\n有什么可以帮你的吗？" ||
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

func TestToolResultMiddlewareLimitsLargeResults(t *testing.T) {
	accumulator := &usageAccumulator{}
	next := func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: strings.Repeat("课", maxToolResultRunes+10)}, nil
	}
	out, err := toolErrorCatchingMiddleware(nil)(next)(
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
