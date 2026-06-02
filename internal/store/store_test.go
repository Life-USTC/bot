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
	err = s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM interactions
		WHERE platform = ? AND user_id = ? AND direction = 'inbound' AND command = ? AND handled = 1 AND status = 'handled'`,
		ident.Platform, ident.UserID, "todo").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inbound interaction count = %d", count)
	}
	err = s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM interactions
		WHERE platform = ? AND user_id = ? AND direction = 'outbound' AND raw_text = ? AND status = 'sent'`,
		ident.Platform, ident.UserID, "已完成：写报告").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbound interaction count = %d", count)
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
