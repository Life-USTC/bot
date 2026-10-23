package store

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSchemaMaintenanceUpgradesV7WithoutChangingExistingData(t *testing.T) {
	s, path := schemaV7Fixture(t)
	before := snapshotSchemaDatabase(t, s)
	if len(before.Tables) != 19 { // 18 application tables and sqlite_sequence.
		t.Fatalf("v7 fixture has %d tables", len(before.Tables))
	}
	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("normal startup implicitly upgraded v7")
	}
	assertSchemaVersion(t, s, 7)
	if after := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected startup changed the v7 database")
	}

	if err := s.PrepareSchemaForMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, s, 9)
	if err := s.VerifySchema(); err != nil {
		t.Fatal(err)
	}
	after := snapshotSchemaDatabase(t, s)
	if _, ok := after.Schema["rain_settings"]; !ok || len(after.Tables["rain_settings"]) != 0 {
		t.Fatal("upgrade did not add an empty rain settings table")
	}
	delete(after.Schema, "rain_settings")
	delete(after.Tables, "rain_settings")
	if _, ok := after.Schema["recurring_reminders"]; !ok || len(after.Tables["recurring_reminders"]) != 0 {
		t.Fatal("upgrade did not add an empty recurring reminders table")
	}
	delete(after.Schema, "recurring_reminders")
	delete(after.Tables, "recurring_reminders")
	for _, index := range []string{"idx_recurring_reminders_due", "idx_recurring_reminders_owner"} {
		if _, ok := after.Schema[index]; !ok {
			t.Fatalf("upgrade did not add %s", index)
		}
		delete(after.Schema, index)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed existing tables, indexes, sequences, or data")
	}
	ident := Identity{Platform: "napcat", UserID: "legacy-user", ConversationType: "private", ConversationID: "legacy-user"}
	settings, err := s.RainSettings(t.Context(), ident)
	if err != nil || settings.Enabled || settings.Campus != "ustc-main" {
		t.Fatalf("rain settings after upgrade = %#v, %v", settings, err)
	}
	if err := s.SaveRainSettings(t.Context(), RainSettings{Identity: ident, Enabled: true, Campus: "ustc-gaoxin"}); err != nil {
		t.Fatal(err)
	}
	beforeRepeat := snapshotSchemaDatabase(t, s)
	if err := s.PrepareSchemaForMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if afterRepeat := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(beforeRepeat, afterRepeat) {
		t.Fatal("repeated maintenance changed data or the user's rain subscription")
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("start upgraded database: %v", err)
	}
	_ = opened.Close()
}

func TestSchemaMaintenanceV7FailureRollsBackTableAndVersion(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
		want string
	}{
		{"missing legacy index", "DROP INDEX idx_users_platform_external_user", "missing required index"},
		{"existing rain table", "CREATE TABLE rain_settings (sentinel TEXT); INSERT INTO rain_settings VALUES ('preserve')", "add rain settings table"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := schemaV7Fixture(t)
			if err := s.db.Exec(test.sql).Error; err != nil {
				t.Fatal(err)
			}
			before := snapshotSchemaDatabase(t, s)
			err := s.PrepareSchemaForMaintenance(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("maintenance error = %v, want %q", err, test.want)
			}
			assertSchemaVersion(t, s, 7)
			if after := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(before, after) {
				t.Fatal("failed upgrade changed tables, indexes, sequences, or data")
			}
		})
	}
}

func TestSchemaMaintenanceRejectsUnsupportedVersionsWithoutMutation(t *testing.T) {
	for _, version := range []int{0, 1, 6, 10} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, _ := schemaV7Fixture(t)
			if err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)).Error; err != nil {
				t.Fatal(err)
			}
			before := snapshotSchemaDatabase(t, s)
			if err := s.PrepareSchemaForMaintenance(t.Context()); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("unsupported schema %d: %v", version, err)
			}
			assertSchemaVersion(t, s, version)
			if after := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected maintenance changed the database")
			}
		})
	}
}

func TestSchemaMaintenanceUpgradesV8WithoutChangingExistingData(t *testing.T) {
	s, path := schemaV7Fixture(t)
	if err := s.db.Migrator().CreateTable(&rainSettingRow{}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("PRAGMA user_version = 8").Error; err != nil {
		t.Fatal(err)
	}
	ident := Identity{Platform: "napcat", UserID: "legacy-user", ConversationType: "private", ConversationID: "legacy-user"}
	if err := s.SaveRainSettings(t.Context(), RainSettings{Identity: ident, Enabled: true, Campus: "ustc-gaoxin"}); err != nil {
		t.Fatal(err)
	}
	before := snapshotSchemaDatabase(t, s)
	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("normal startup implicitly upgraded v8")
	}
	assertSchemaVersion(t, s, 8)
	if after := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected startup changed v8 data")
	}
	if err := s.PrepareSchemaForMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, s, 9)
	after := snapshotSchemaDatabase(t, s)
	if _, ok := after.Schema["recurring_reminders"]; !ok || len(after.Tables["recurring_reminders"]) != 0 {
		t.Fatal("upgrade did not add an empty recurring reminders table")
	}
	delete(after.Schema, "recurring_reminders")
	delete(after.Tables, "recurring_reminders")
	for _, index := range []string{"idx_recurring_reminders_due", "idx_recurring_reminders_owner"} {
		if _, ok := after.Schema[index]; !ok {
			t.Fatalf("upgrade did not add %s", index)
		}
		delete(after.Schema, index)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed existing tables, indexes, sequences, or data")
	}
	beforeRepeat := snapshotSchemaDatabase(t, s)
	if err := s.PrepareSchemaForMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if afterRepeat := snapshotSchemaDatabase(t, s); !reflect.DeepEqual(beforeRepeat, afterRepeat) {
		t.Fatal("repeated v9 maintenance changed data")
	}
}

func schemaV7Fixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := t.TempDir() + "/v7.db"
	s, err := OpenForSchemaMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	schema, err := os.ReadFile("testdata/schema_v7.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(string(schema)).Error; err != nil {
		t.Fatal(err)
	}
	// Synthetic account, credential, subscription, history, durable image, and
	// delivery state exercise values that must survive the explicit upgrade.
	if err := s.db.Exec(`
INSERT INTO users (id, platform, external_user_id, display_name) VALUES (42, 'napcat', 'legacy-user', 'Existing Student');
INSERT INTO credentials (user_id, client_id, access_token, refresh_token, expires_at) VALUES (42, 'test-client', 'synthetic-access', 'synthetic-refresh', '2030-01-01 00:00:00');
INSERT INTO notification_settings (user_id, platform, external_user_id, conversation_type, conversation_id, classes_enabled, homework_enabled, young_enabled, todos_enabled, reauth_required) VALUES (42, 'napcat', 'legacy-user', 'private', 'legacy-user', 1, 0, 1, 0, 1);
INSERT INTO bus_settings (user_id, platform, external_user_id, show_south_campus) VALUES (42, 'napcat', 'legacy-user', 1);
INSERT INTO login_sessions (user_id, device_code, expires_at, status) VALUES (42, 'synthetic-device', '2030-01-01 00:00:00', 'pending');
INSERT INTO conversation_events (platform, conversation_type, conversation_id, external_user_id, dedupe_key, type, content) VALUES ('napcat', 'private', 'legacy-user', 'legacy-user', 'event-1', 'message', 'keep history');
INSERT INTO conversation_compactions (platform, conversation_type, conversation_id, covered_event_id, summary) VALUES ('napcat', 'private', 'legacy-user', 1, 'keep summary');
INSERT INTO command_images (id, platform, user_id, conversation_type, conversation_id, key, image_json) VALUES ('image-1', 'napcat', 'legacy-user', 'private', 'legacy-user', 'account', '{"kind":"account","title":"账户信息"}');
INSERT INTO agent_checkpoints (id, payload) VALUES ('checkpoint-1', X'0001FF');
INSERT INTO outgoing_messages (dedupe_key, kind, platform, conversation_type, conversation_id, payload_json, status, attempts) VALUES ('outbound-1', 'notification.classes', 'napcat', 'private', 'legacy-user', '{"text":"pending reminder"}', 'retry_wait', 2);
`).Error; err != nil {
		t.Fatal(err)
	}
	return s, path
}

type schemaDatabaseSnapshot struct {
	Schema map[string]string
	Tables map[string][]string
}

func snapshotSchemaDatabase(t *testing.T, s *Store) schemaDatabaseSnapshot {
	t.Helper()
	var objects []struct{ Type, Name, SQL string }
	if err := s.db.Raw("SELECT type, name, sql FROM sqlite_master ORDER BY type, name").Scan(&objects).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := schemaDatabaseSnapshot{Schema: map[string]string{}, Tables: map[string][]string{}}
	for _, object := range objects {
		snapshot.Schema[object.Name] = object.Type + ":" + object.SQL
		if object.Type != "table" {
			continue
		}
		rows, err := s.db.Raw("SELECT * FROM " + quoteSQLiteIdentifier(object.Name) + " ORDER BY rowid").Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		snapshot.Tables[object.Name] = nil
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			snapshot.Tables[object.Name] = append(snapshot.Tables[object.Name], string(encoded))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func assertSchemaVersion(t *testing.T, s *Store, want int) {
	t.Helper()
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil || version != want {
		t.Fatalf("schema version = %d, want %d: %v", version, want, err)
	}
}
