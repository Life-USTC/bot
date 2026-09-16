package store

import (
	"context"
	"strings"
	"testing"
)

func TestMaintenanceAddsTodoOptInFromPreviousSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := Identity{
		Platform: "napcat", UserID: "todo-maintenance",
		ConversationType: "private", ConversationID: "todo-maintenance",
	}
	if err := s.SaveNotificationSettings(ctx, NotificationSettings{
		Identity: ident, ClassesEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.NotificationSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ClassesEnabled || !before.ReauthRequired {
		t.Fatalf("pre-migration settings = %#v", before)
	}
	if err := s.db.Migrator().DropColumn(&notificationSettingRow{}, "TodosEnabled"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("PRAGMA user_version = 4").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("startup silently migrated the previous schema version")
	} else if !strings.Contains(err.Error(), "unsupported database schema version") {
		t.Fatalf("startup error = %v", err)
	}

	maintenance, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := maintenance.PrepareSchemaForMaintenance(ctx); err != nil {
			_ = maintenance.Close()
			t.Fatal(err)
		}
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	after, err := s.NotificationSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ClassesEnabled || after.TodosEnabled || !after.ReauthRequired {
		t.Fatalf("post-migration settings = %#v", after)
	}
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion)
	}
}
