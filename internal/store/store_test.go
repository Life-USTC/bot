package store

import (
	"context"
	"testing"
	"time"
)

func TestCredentialAndConversationStatePersist(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	cred := Credential{
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Scope:        "openid",
		Resource:     "https://life.example",
	}
	if err := s.SaveCredential(context.Background(), ident, cred); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credential(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.AccessToken != "access" || got.RefreshToken != "refresh" {
		t.Fatalf("credential = %#v", got)
	}
	if err := s.RecordConversationState(context.Background(), ident, "todo", "pending"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		RawText: "td done 1",
		Command: "todo",
		Args:    "done 1",
		Handled: true,
		Reply:   "已完成：写报告",
		Status:  "handled",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		Direction: "outbound",
		RawText:   "已完成：写报告",
		Handled:   true,
		Status:    "sent",
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	err = s.db.WithContext(context.Background()).Raw(`SELECT COUNT(*) FROM interactions
		WHERE platform = ? AND user_id = ? AND direction = 'inbound' AND command = ? AND handled = 1 AND status = 'handled'`,
		ident.Platform, ident.UserID, "todo").Scan(&count).Error
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inbound interaction count = %d", count)
	}
	err = s.db.WithContext(context.Background()).Raw(`SELECT COUNT(*) FROM interactions
		WHERE platform = ? AND user_id = ? AND direction = 'outbound' AND raw_text = ? AND status = 'sent'`,
		ident.Platform, ident.UserID, "已完成：写报告").Scan(&count).Error
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbound interaction count = %d", count)
	}
	recent, err := s.RecentHandledInteractions(context.Background(), ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].RawText != "td done 1" || recent[0].Reply != "已完成：写报告" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRecentHandledInteractionsOrdersOldestFirst(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for _, text := range []string{"你好", "你是谁", "我上面说了什么"} {
		if err := s.RecordInteraction(context.Background(), ident, Interaction{
			RawText: text,
			Command: "agent",
			Handled: true,
			Reply:   "reply " + text,
			Status:  "handled",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		Direction: "outbound",
		RawText:   "ignored for history",
		Handled:   true,
		Status:    "sent",
	}); err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentHandledInteractions(context.Background(), ident, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].RawText != "你是谁" || recent[1].RawText != "我上面说了什么" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRecordInteractionAllowsEmptyRawText(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		Handled: false,
		Status:  "ignored",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		Direction: "outbound",
		Handled:   true,
		Status:    "send_failed",
		Error:     "missing message body",
	}); err != nil {
		t.Fatal(err)
	}
	count, err := s.InteractionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestRecordInteractionNormalizesKnownDirection(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.RecordInteraction(ctx, ident, Interaction{
		Direction: " OUTBOUND ",
		RawText:   "sent",
		Handled:   true,
		Status:    "sent",
	}); err != nil {
		t.Fatal(err)
	}
	var direction string
	err = s.db.WithContext(ctx).Raw(`SELECT direction FROM interactions WHERE raw_text = ?`, "sent").Scan(&direction).Error
	if err != nil {
		t.Fatal(err)
	}
	if direction != "outbound" {
		t.Fatalf("direction = %q", direction)
	}
	recent, err := s.RecentHandledInteractions(ctx, ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42"}
	session := LoginSession{
		DeviceCode:              "device",
		UserCode:                "USER-CODE",
		VerificationURI:         "https://life.example/device",
		VerificationURIComplete: "https://life.example/device?user_code=USER-CODE",
		ClientID:                "client",
		ExpiresAt:               time.Now().Add(time.Minute),
		IntervalSeconds:         5,
		Status:                  "pending",
	}
	if err := s.SaveLoginSession(context.Background(), ident, session); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveLoginSession(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.DeviceCode != "device" {
		t.Fatalf("session = %#v", got)
	}
	if err := s.MarkLoginSession(context.Background(), ident, "device", "approved"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ActiveLoginSession(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no active session, got %#v", got)
	}
}

func TestPendingLoginSessionsIncludeNotificationIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(context.Background(), ident, LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.PendingLoginSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	if sessions[0].Identity != ident {
		t.Fatalf("identity = %#v", sessions[0].Identity)
	}
}

func TestNotificationSettingsAndDeliveries(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	settings, err := s.NotificationSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ClassesEnabled || settings.HomeworkEnabled {
		t.Fatalf("default settings = %#v", settings)
	}

	settings.ClassesEnabled = true
	settings.HomeworkEnabled = true
	if err := s.SaveNotificationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.EnabledNotificationSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || !enabled[0].ClassesEnabled || !enabled[0].HomeworkEnabled || enabled[0].Identity != ident {
		t.Fatalf("enabled settings = %#v", enabled)
	}

	recorded, err := s.TryRecordNotificationDelivery(ctx, ident, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("first delivery was not recorded")
	}
	delivered, err := s.NotificationDelivered(ctx, ident, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("delivery was not found")
	}
	recorded, err = s.TryRecordNotificationDelivery(ctx, ident, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("duplicate delivery was recorded")
	}
}
