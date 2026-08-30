package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestCommandAutomaticallyReusesLoginAndSavesOriginalRequest(t *testing.T) {
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
	handler := Handler{Life: &life.Client{}, Auth: &auth.Manager{Store: db}, Store: db}

	reply, ok := handler.Handle(context.Background(), Input{Text: "订阅 链接", Identity: ident})
	if !ok || !strings.Contains(reply, "完成后我会自动继续") || strings.Contains(reply, "发送：登录") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	pending, err := db.ActivePendingRequest(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Text != "订阅 链接" {
		t.Fatalf("pending = %#v", pending)
	}
	recent, err := db.RecentHandledInteractions(context.Background(), ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("waiting-auth request leaked into model history: %#v", recent)
	}
}
