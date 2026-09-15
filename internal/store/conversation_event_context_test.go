package store

import (
	"context"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
)

func TestResolveQuotedMessageUsesOnlyAcceptedOutboxInSameConversation(t *testing.T) {
	db, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	conversation := message.Conversation{Platform: "napcat", Type: "group", ID: "100"}
	createdAt := time.Now().UTC()
	record, created, err := db.Enqueue(ctx, message.Outbound{
		Kind: "agent", Target: conversation, Content: message.Content{Parts: []message.ContentPart{{Text: "原始 Bot 内容"}}}, DedupeKey: "quoted-message",
	})
	if err != nil || !created {
		t.Fatalf("enqueue quote=%#v created=%v err=%v", record, created, err)
	}
	if quoted, err := db.ResolveQuotedMessage(ctx, conversation, "bot-message"); err != nil || quoted != nil {
		t.Fatalf("pending quote=%#v err=%v", quoted, err)
	}
	due, err := db.ClaimDue(ctx, time.Now().UTC().Add(time.Second), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim quote=%#v err=%v", due, err)
	}
	acceptedAt := createdAt.Add(time.Second)
	if err := db.Complete(ctx, record.ID, delivery.Outcome{
		State:   delivery.OutcomeAccepted,
		Receipt: message.Receipt{PlatformMessageID: "bot-message", AcceptedAt: acceptedAt},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	quoted, err := db.ResolveQuotedMessage(ctx, conversation, "bot-message")
	if err != nil || quoted == nil {
		t.Fatalf("accepted quote=%#v err=%v", quoted, err)
	}
	if quoted.MessageID != "bot-message" || quoted.Actor.Platform != "napcat" || quoted.Actor.UserID != "bot" ||
		quoted.Actor.DisplayName != "Presto" || quoted.Content != "原始 Bot 内容" || !quoted.SentAt.Equal(record.CreatedAt) {
		t.Fatalf("quoted message=%#v want original outbox data", quoted)
	}
	crossConversation := conversation
	crossConversation.ID = "other"
	if cross, err := db.ResolveQuotedMessage(ctx, crossConversation, "bot-message"); err != nil || cross != nil {
		t.Fatalf("cross-conversation quote=%#v err=%v", cross, err)
	}
}
