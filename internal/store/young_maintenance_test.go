package store

import (
	"context"
	"testing"
	"time"
)

func TestMaintenanceAddsYoungOptInWithoutChangingExistingData(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveCredential(ctx, ident, Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNotificationSettings(ctx, NotificationSettings{Identity: ident, ClassesEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Migrator().DropColumn(&notificationSettingRow{}, "YoungEnabled"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("startup silently repaired missing Young column")
	}
	m, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.PrepareSchemaForMaintenance(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	settings, err := s.NotificationSettings(ctx, ident)
	if err != nil || !settings.ClassesEnabled || settings.YoungEnabled {
		t.Fatalf("preserved settings: %#v, %v", settings, err)
	}
	credential, err := s.Credential(ctx, ident)
	if err != nil || credential == nil || credential.AccessToken != "access" {
		t.Fatalf("preserved credential: %#v, %v", credential, err)
	}
	settings.YoungEnabled = true
	if err := s.SaveNotificationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.EnabledNotificationSettings(ctx)
	if err != nil || len(enabled) != 1 || !enabled[0].YoungEnabled {
		t.Fatalf("Young opt-in: %#v, %v", enabled, err)
	}
}
