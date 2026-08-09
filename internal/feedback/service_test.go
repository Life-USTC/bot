package feedback

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

func TestUserAndLLMFeedbackShareRecorderAndDurablePath(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	service, err := New(db, Config{Targets: []Target{{
		Platform: "napcat", ConversationType: "private", ConversationID: "admin",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ident := store.Identity{Platform: "qqbot", UserID: "user", ConversationType: "private", ConversationID: "user"}
	userResult, err := service.Record(ctx, ident, Submission{
		Source: SourceUser, Category: "user_feedback", Content: "课表颜色会变化", Context: "用户：今天课表",
	})
	if err != nil {
		t.Fatal(err)
	}
	llmResult, err := service.Record(ctx, ident, Submission{
		Source: SourceLLM, Category: "missing_tool", Content: "缺少食堂价格工具", Context: "模型发现工具缺口",
	})
	if err != nil {
		t.Fatal(err)
	}
	if userResult.ID <= 0 || llmResult.ID <= userResult.ID || userResult.AdminIntents != 1 || llmResult.AdminIntents != 1 {
		t.Fatalf("results = %#v, %#v", userResult, llmResult)
	}
	due, err := db.ClaimDue(ctx, time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("outgoing messages = %#v", due)
	}
	if !strings.Contains(due[0].Message.Content.Text, "用户反馈") || !strings.Contains(due[1].Message.Content.Text, "LLM 反馈") {
		t.Fatalf("messages = %q, %q", due[0].Message.Content.Text, due[1].Message.Content.Text)
	}
}

func TestDuplicateTargetsCreateOneIntent(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	target := Target{Platform: "napcat", ConversationType: "private", ConversationID: "admin"}
	service, err := New(db, Config{Targets: []Target{target, target, {
		Platform: " NAPCAT ", ConversationType: " PRIVATE ", ConversationID: " admin ",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Record(context.Background(), store.Identity{
		Platform: "qqbot", UserID: "user", ConversationType: "private", ConversationID: "user",
	}, Submission{Source: SourceUser, Content: "重复目标不应重复通知"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AdminIntents != 1 {
		t.Fatalf("admin intents = %d", result.AdminIntents)
	}
	due, err := db.ClaimDue(context.Background(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("outgoing messages = %#v", due)
	}
}

func TestMultipleAdminsHaveIndependentMessages(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	service, err := New(db, Config{Targets: []Target{
		{Platform: "napcat", ConversationType: "private", ConversationID: "admin-a"},
		{Platform: "napcat", ConversationType: "private", ConversationID: "admin-b"},
		{Platform: "qqbot", ConversationType: "group", ConversationID: "maintainers"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Record(context.Background(), store.Identity{
		Platform: "napcat", UserID: "user", ConversationType: "group", ConversationID: "group",
	}, Submission{Source: SourceLLM, Content: "待办查询偶发失败"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AdminIntents != 3 {
		t.Fatalf("admin intents = %d", result.AdminIntents)
	}
	due, err := db.ClaimDue(context.Background(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 3 {
		t.Fatalf("outgoing messages = %#v", due)
	}
	keys := map[string]bool{}
	for _, record := range due {
		if keys[record.Message.DedupeKey] {
			t.Fatalf("duplicate dedupe key %q", record.Message.DedupeKey)
		}
		keys[record.Message.DedupeKey] = true
	}
}
