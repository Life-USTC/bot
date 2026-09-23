package store

import (
	"context"
	"testing"
	"time"
)

func TestNotificationDefaultsStartAfterLoginAndPreserveOptOut(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "new-user", ConversationType: "private", ConversationID: "new-user"}
	settings, err := s.NotificationSettings(ctx, ident)
	if err != nil || !allNotificationsEnabled(settings) || !settings.ReauthRequired {
		t.Fatalf("defaults before login = %#v, %v", settings, err)
	}
	if enabled, err := s.EnabledNotificationSettings(ctx); err != nil || len(enabled) != 0 {
		t.Fatalf("notifications before login = %#v, %v", enabled, err)
	}

	credential := Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.SaveCredential(ctx, ident, credential); err != nil {
		t.Fatal(err)
	}
	settings, err = s.NotificationSettings(ctx, ident)
	if err != nil || !allNotificationsEnabled(settings) || settings.ReauthRequired {
		t.Fatalf("defaults after login = %#v, %v", settings, err)
	}
	if enabled, err := s.EnabledNotificationSettings(ctx); err != nil || len(enabled) != 1 {
		t.Fatalf("notifications after login = %#v, %v", enabled, err)
	}

	settings.YoungEnabled = false
	if err := s.SaveNotificationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	credential.AccessToken = "refreshed"
	if err := s.SaveCredential(ctx, ident, credential); err != nil {
		t.Fatal(err)
	}
	settings, err = s.NotificationSettings(ctx, ident)
	if err != nil || settings.YoungEnabled || !settings.ClassesEnabled || !settings.HomeworkEnabled || !settings.TodosEnabled {
		t.Fatalf("settings after refresh = %#v, %v", settings, err)
	}
}

func allNotificationsEnabled(settings NotificationSettings) bool {
	return settings.ClassesEnabled && settings.HomeworkEnabled && settings.YoungEnabled && settings.TodosEnabled
}
