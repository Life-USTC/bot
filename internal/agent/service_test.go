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
	if len(tools) != 5 {
		t.Fatalf("tool count = %d", len(tools))
	}
}
