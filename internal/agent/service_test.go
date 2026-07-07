package agent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
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

func TestAgentToolConstruction(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	svc := &Service{handler: commands.Handler{
		Life:  life.NewClient("https://life.example", &http.Client{}),
		Auth:  &auth.Manager{Store: db},
		Store: db,
	}}
	assertAgentToolNames(t, svc,
		"add_todo",
		"bulk_subscribe_sections",
		"complete_homework",
		"complete_todo",
		"delete_todo",
		"get_bot_status",
		"get_course_by_jw_id",
		"get_current_semester",
		"get_current_time",
		"get_curriculum_for_date",
		"get_my_dashboard",
		"get_next_bus",
		"get_next_class",
		"get_notification_settings",
		"get_profile",
		"get_section_by_jw_id",
		"get_teacher_by_id",
		"get_today_curriculum",
		"get_today_overview",
		"get_tomorrow_curriculum",
		"get_two_day_curriculum",
		"get_upcoming_deadlines",
		"list_bus_routes",
		"list_exams",
		"list_exams_by_section",
		"list_filtered_todos",
		"list_homeworks",
		"list_homeworks_by_section",
		"list_my_subscribed_sections",
		"list_schedules_by_section",
		"list_semesters",
		"list_subscriptions",
		"list_todos",
		"record_bot_feedback",
		"search_courses",
		"search_sections",
		"search_teachers",
		"send_message_part",
		"set_notification_settings",
		"undo_homework_completion",
		"undo_todo_completion",
		"unsubscribe_section_by_jw_id",
		"update_todo",
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
		"get_notification_settings",
		"record_bot_feedback",
		"send_message_part",
		"set_notification_settings",
	)
}

func TestAppendCommandBackedToolRejectsUnknownCommand(t *testing.T) {
	_, err := appendCommandBackedTool(&Service{}, map[string]commands.CommandSpec{}, nil, "missing", "bad_tool", "Bad tool.", nil, func(context.Context, emptyInput) (string, error) {
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), `unknown command "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

func agentToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	tools, err := svc.toolsFor(store.Identity{ConversationType: "private"}, nil, func(context.Context, store.Identity, string) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
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

func TestRequiredToolArgTrimsAndRejectsBlank(t *testing.T) {
	got, err := requiredToolArg("keyword", "  计算机网络  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "计算机网络" {
		t.Fatalf("arg = %q", got)
	}
	_, err = requiredToolArg("keyword", " \t\n ")
	if err == nil || !strings.Contains(err.Error(), "keyword") {
		t.Fatalf("blank arg error = %v", err)
	}
}

func TestRequiredCommandToolTrimsArgAndRunsCommand(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc := &Service{handler: commands.Handler{Store: db}}
	fn := requiredCommandTool[targetInput](svc, ident, "target", "通知 ", func(input targetInput) string {
		return input.Target
	})
	reply, err := fn(context.Background(), targetInput{Target: " 作业 开 "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "作业提醒：开") {
		t.Fatalf("reply = %q", reply)
	}
	settings, err := db.NotificationSettings(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HomeworkEnabled {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestBusCommandTextTrimsOptionalCampuses(t *testing.T) {
	if got := busCommandText(busInput{From: " 东区 ", To: " 西区 "}); got != "校车 东区 西区" {
		t.Fatalf("busCommandText = %q", got)
	}
	if got := busCommandText(busInput{From: "  ", To: "\t"}); got != "校车" {
		t.Fatalf("blank busCommandText = %q", got)
	}
	if got := busCommandText(busInput{To: " 西区 "}); got != "校车 到 西区" {
		t.Fatalf("to-only busCommandText = %q", got)
	}
	if got := busCommandText(busInput{From: "高新区", To: "东区", After: "2026-06-09T09:25:00+08:00"}); got != "校车 高新区 东区 after 2026-06-09T09:25:00+08:00" {
		t.Fatalf("after busCommandText = %q", got)
	}
}

func TestListHomeworksCommandTextBuildsSemesterFilter(t *testing.T) {
	if got := listHomeworksCommandText(listHomeworksInput{}); got != "作业" {
		t.Fatalf("default = %q", got)
	}
	if got := listHomeworksCommandText(listHomeworksInput{IncludeCompleted: true, SemesterID: 7}); got != "作业 all semester_id 7" {
		t.Fatalf("semester_id = %q", got)
	}
}

func TestResolveHomeworkSemesterInputResolvesNameToID(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/semesters" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":2,"jwId":202501,"namePrimary":"2026年春季学期"}]}`))
	}))
	defer server.Close()

	svc := &Service{handler: commands.Handler{Life: life.NewClient(server.URL, server.Client())}}
	resolved, err := svc.resolveHomeworkSemesterInput(ctx, listHomeworksInput{SemesterName: "2026年春季学期"})
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolved.SemesterID != 2 || resolved.SemesterName != "" {
		t.Fatalf("resolved = %+v", resolved)
	}

	_, err = svc.resolveHomeworkSemesterInput(ctx, listHomeworksInput{SemesterName: "不存在的学期"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing semester error = %v", err)
	}
}

func TestRequiredConfirmationToolDoesNotRunCommand(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ident := store.Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	svc := &Service{handler: commands.Handler{Store: db}}
	fn := requiredConfirmationTool(svc, ident, "target", "待办 delete ", func(input targetInput) string {
		return input.Target
	})
	reply, err := fn(context.Background(), targetInput{Target: " 测试 "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "不会自动执行") || !strings.Contains(reply, "回复 ok 确认") || !strings.Contains(reply, "待办 delete 测试") {
		t.Fatalf("reply = %q", reply)
	}
	pending, err := db.ActivePendingConfirmation(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Command != "待办 delete 测试" {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestNotificationKindCommandArgAcceptsCommandAliases(t *testing.T) {
	tests := map[string]string{
		" classes ": "课表",
		"kb":        "课表",
		"课表":        "课表",
		"作业":        "作业",
		"HW":        "作业",
	}
	for input, want := range tests {
		got, err := notificationKindCommandArg(input)
		if err != nil {
			t.Fatalf("%q error = %v", input, err)
		}
		if got != want {
			t.Fatalf("%q = %q, want %q", input, got, want)
		}
	}
	_, err := notificationKindCommandArg("bus")
	if err == nil || !strings.Contains(err.Error(), "unsupported notification kind") {
		t.Fatalf("unsupported kind error = %v", err)
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

	trace.Notify(context.Background(), "search_courses", keywordInput{Keyword: "数学分析"}, "课程：数学分析\n教师：张三", nil)
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
		Store:          db,
		FeedbackUsers:  []string{"admin-openid"},
		FeedbackGroups: []string{"group-openid"},
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
	if targets[0].ConversationType != "private" || targets[0].ConversationID != "admin-openid" {
		t.Fatalf("private target = %#v", targets[0])
	}
	if targets[1].ConversationType != "group" || targets[1].ConversationID != "group-openid" {
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
		Reply:   "  你好！有什么可以帮你的吗？  ",
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
	if messages[0].Content != "你好" || messages[1].Content != "你好！有什么可以帮你的吗？" || messages[2].Content != "我上面说了什么？" {
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
	svc.finishAgentRun(context.Background(), 0, store.AgentRunStatusFailed, "", context.DeadlineExceeded)
	if !strings.Contains(logs.String(), "agent run failed") || !strings.Contains(logs.String(), "context deadline exceeded") {
		t.Fatalf("logs = %q", logs.String())
	}
}
