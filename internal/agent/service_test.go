package agent

import (
	"context"
	"testing"

	"github.com/Life-USTC/Bot/internal/commands"
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
	reply, ok := svc.Handle(context.Background(), Input{
		Text:     "帮我看看今天有什么课",
		Identity: store.Identity{ConversationType: "group"},
	})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgentToolConstruction(t *testing.T) {
	svc := &Service{}
	tools, err := svc.toolsFor(store.Identity{ConversationType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 19 {
		t.Fatalf("tool count = %d", len(tools))
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
		RawText: "你好",
		Command: "agent",
		Handled: true,
		Reply:   "你好！有什么可以帮你的吗？",
		Status:  "handled",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{handler: commands.Handler{Store: db}}
	messages, err := svc.messagesFor(ctx, Input{Text: "我上面说了什么？", Identity: ident})
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
