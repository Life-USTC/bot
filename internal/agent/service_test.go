package agent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
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
		"record_bot_feedback",
		"send_message_part",
		"search_courses",
	)
}

func TestAgentToolConstructionSkipsUnavailableCommandTools(t *testing.T) {
	assertAgentToolNames(t, &Service{},
		"get_current_time",
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
		"record_bot_feedback",
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

	failing := toolErrorCatchingMiddleware(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, errors.New("bad args")
	})
	out, err := failing(ctx, input)
	if err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	if out == nil || !strings.Contains(out.Result, "bad args") {
		t.Fatalf("result = %q", out.Result)
	}

	ok := toolErrorCatchingMiddleware(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
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
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0] != "工具调用：search_courses {\"keyword\":\"数学分析\"}\n工具结果：\n课程：数学分析\n教师：张三" {
		t.Fatalf("message 0 = %q", messages[0])
	}
	if messages[1] != "工具调用：get_current_time\n工具结果：" {
		t.Fatalf("message 1 = %q", messages[1])
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

> 建议下课后出发`
	got := cleanQQReply(input)
	want := "明天行程\n时间  事项  地点\n07:50-09:25  组合数学  高新区 GT-B112\n\n建议下课后出发"
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
	if !strings.Contains(instruction, "Current local time is 2026-06-07 18:30 CST.") {
		t.Fatalf("instruction = %q", instruction)
	}
	if !strings.Contains(instruction, "one command per QQ message") || !strings.Contains(instruction, "Avoid emojis") {
		t.Fatalf("instruction = %q", instruction)
	}
	if !strings.Contains(instruction, "Never invent prices, menus, locations, schedules, or service availability") {
		t.Fatalf("instruction lacks grounding rule: %q", instruction)
	}
	if !strings.Contains(instruction, "Never ask whether to record feedback") {
		t.Fatalf("instruction lacks automatic feedback rule: %q", instruction)
	}
	for _, want := range []string{
		"Image rendering protocol:",
		"![](校车 查询 东区 西区)",
		"![](校车 查询 东区 西区 之后 14:00 已发车)",
		"![](今天课表)",
		"![](课表 第3周)",
		"![](课表 2026年5月6日)",
		"![](课表 2026-09-04)",
		"第N周 uses the current semester only",
		"![](下一节课)",
		"![](待办)",
		"![](待办 列表 未完成 优先级 高 截止前 2026-06-10 第2页)",
		"![](作业)",
		"![](作业 列表 全部 学期JWID 123 第2页)",
		"![](考试)",
		"![](概览)",
		"![](近期截止 14)",
		"![](教学班作业 654)",
		"![](教学班考试 321 第2页)",
		"The only valid image output is a standalone ![](command) directive.",
		"MUST include one supported directive",
		"By default, proactively include one directive",
		"Do not merely tell the user that an image is available; emit the directive.",
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
		messages[2].Content != "我上面说了什么？" {
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

func TestAgentFailureReplyHidesProviderTimeoutAndIncludesTrace(t *testing.T) {
	reply := agentFailureReply(42, context.DeadlineExceeded)
	if !strings.Contains(reply, "AI 响应超时") || !strings.Contains(reply, "记录 #42") {
		t.Fatalf("reply = %q", reply)
	}
	if strings.Contains(reply, "deadline") {
		t.Fatalf("reply exposes provider error: %q", reply)
	}
}

func TestFinishAgentRunLogsErrors(t *testing.T) {
	var logs bytes.Buffer
	svc := &Service{logger: log.New(&logs, "", 0)}
	svc.finishAgentRun(context.Background(), 0, store.AgentRunStatusFailed, "", context.DeadlineExceeded, "deepseek", "test", tokenUsage{})
	if !strings.Contains(logs.String(), "agent run failed") || !strings.Contains(logs.String(), "context deadline exceeded") {
		t.Fatalf("logs = %q", logs.String())
	}
}
