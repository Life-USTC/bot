package agent

import (
	"context"
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
	names := agentToolNames(t, svc)
	wantNames := []string{
		"add_todo",
		"bulk_subscribe_sections",
		"complete_homework",
		"complete_todo",
		"get_bot_status",
		"get_current_semester",
		"get_current_time",
		"get_next_bus",
		"get_next_class",
		"get_notification_settings",
		"get_profile",
		"get_today_curriculum",
		"get_tomorrow_curriculum",
		"get_two_day_curriculum",
		"list_homeworks",
		"list_subscriptions",
		"list_todos",
		"search_courses",
		"search_sections",
		"set_notification_settings",
		"undo_homework_completion",
	}
	if len(names) != len(wantNames) {
		t.Fatalf("tool count = %d, want %d; tools = %#v", len(names), len(wantNames), names)
	}
	for _, name := range wantNames {
		if !names[name] {
			t.Fatalf("missing tool %q; tools = %#v", name, names)
		}
	}
}

func TestAgentToolConstructionSkipsUnavailableCommandTools(t *testing.T) {
	names := agentToolNames(t, &Service{})
	wantNames := []string{
		"get_current_time",
	}
	if len(names) != len(wantNames) {
		t.Fatalf("tool count = %d, want %d; tools = %#v", len(names), len(wantNames), names)
	}
	for _, name := range wantNames {
		if !names[name] {
			t.Fatalf("missing tool %q; tools = %#v", name, names)
		}
	}
}

func TestAppendCommandBackedToolRejectsUnknownCommand(t *testing.T) {
	_, err := appendCommandBackedTool(&Service{}, map[string]commands.CommandSpec{}, nil, "missing", "bad_tool", "Bad tool.", func(context.Context, emptyInput) (string, error) {
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), `unknown command "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

func agentToolNames(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	tools, err := svc.toolsFor(store.Identity{ConversationType: "private"})
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

func TestCurrentTimeHelpersUseShanghaiTime(t *testing.T) {
	now := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	if got := currentTimeMessageAt(now); got != "现在是 2026-06-07 18:30，Asia/Shanghai。" {
		t.Fatalf("currentTimeMessageAt = %q", got)
	}
	instruction := currentInstructionAt(now)
	if !strings.Contains(instruction, "Current local time is 2026-06-07 18:30 CST.") {
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
