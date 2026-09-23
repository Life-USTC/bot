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

func TestNotificationMaintenanceEnablesExistingUsersOnlyOnce(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	credential := Credential{ClientID: "client", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}
	withSetting := Identity{Platform: "napcat", UserID: "existing-setting", ConversationType: "private", ConversationID: "existing-setting"}
	withoutSetting := Identity{Platform: "qqbot", UserID: "missing-setting", ConversationType: "private", ConversationID: "missing-setting"}
	withoutCredential := Identity{Platform: "napcat", UserID: "not-logged-in", ConversationType: "private", ConversationID: "not-logged-in"}
	for _, ident := range []Identity{withSetting, withoutSetting} {
		if err := s.SaveCredential(ctx, ident, credential); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnsureUser(ctx, withoutCredential); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`UPDATE notification_settings SET
		classes_enabled = 0, homework_enabled = 0, young_enabled = 0, todos_enabled = 0,
		reauth_required = 1 WHERE external_user_id = ?`, withSetting.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("DELETE FROM notification_settings WHERE external_user_id = ?", withoutSetting.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("PRAGMA user_version = 5").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("normal startup silently changed the previous database")
	}
	maintenance, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.PrepareSchemaForMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var settingCount int64
	if err := s.db.Model(&notificationSettingRow{}).Count(&settingCount).Error; err != nil || settingCount != 3 {
		t.Fatalf("migrated setting rows = %d, %v", settingCount, err)
	}
	for _, tc := range []struct {
		ident  Identity
		paused bool
	}{
		{withSetting, true},
		{withoutSetting, false},
		{withoutCredential, true},
	} {
		settings, err := s.NotificationSettings(ctx, tc.ident)
		if err != nil || !allNotificationsEnabled(settings) || settings.ReauthRequired != tc.paused {
			t.Fatalf("migrated settings for %s = %#v, %v", tc.ident.UserID, settings, err)
		}
	}
	enabled, err := s.EnabledNotificationSettings(ctx)
	if err != nil || len(enabled) != 1 || enabled[0].Identity != withoutSetting {
		t.Fatalf("active migrated reminders = %#v, %v", enabled, err)
	}
	settings, err := s.NotificationSettings(ctx, withoutSetting)
	if err != nil {
		t.Fatal(err)
	}
	settings.TodosEnabled = false
	if err := s.SaveNotificationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSchemaForMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err = s.NotificationSettings(ctx, withoutSetting)
	if err != nil || settings.TodosEnabled {
		t.Fatalf("opt-out after repeated maintenance = %#v, %v", settings, err)
	}
}

func allNotificationsEnabled(settings NotificationSettings) bool {
	return settings.ClassesEnabled && settings.HomeworkEnabled && settings.YoungEnabled && settings.TodosEnabled
}
