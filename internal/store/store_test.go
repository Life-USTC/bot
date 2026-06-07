package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestEnsureUserRejectsIncompleteIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	tests := []Identity{
		{},
		{Platform: "napcat"},
		{UserID: "42"},
		{Platform: "   ", UserID: "42"},
		{Platform: "napcat", UserID: "   "},
	}
	for _, ident := range tests {
		_, err := s.EnsureUser(context.Background(), ident)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("EnsureUser(%#v) error = %v", ident, err)
		}
	}
}

func TestEnsureUserTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	first, err := s.EnsureUser(ctx, Identity{Platform: " napcat ", UserID: " 42 "})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureUser(ctx, Identity{Platform: "napcat", UserID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("user ids = %d, %d", first, second)
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user count = %d", count)
	}
}

func TestRecordConversationStateRejectsIncompleteIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	tests := []Identity{
		{},
		{Platform: "napcat", UserID: "42"},
		{Platform: "napcat", UserID: "42", ConversationType: "private"},
		{Platform: "napcat", UserID: "42", ConversationID: "42"},
	}
	for _, ident := range tests {
		err = s.RecordConversationState(context.Background(), ident, "todo", "pending")
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("RecordConversationState(%#v) error = %v", ident, err)
		}
	}
}

func TestRecordInteractionRejectsIncompleteIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	tests := []Identity{
		{},
		{Platform: "napcat", UserID: "42"},
		{Platform: "napcat", UserID: "42", ConversationType: "private"},
		{Platform: "napcat", UserID: "42", ConversationID: "42"},
	}
	for _, ident := range tests {
		err = s.RecordInteraction(context.Background(), ident, Interaction{RawText: "hello"})
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("RecordInteraction(%#v) error = %v", ident, err)
		}
	}
}

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

func TestSaveCredentialTrimsFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveCredential(context.Background(), ident, Credential{
		ClientID:     " client ",
		AccessToken:  " access ",
		RefreshToken: " refresh ",
		TokenType:    " Bearer ",
		ExpiresAt:    time.Now().Add(time.Hour),
		Scope:        " openid ",
		Resource:     " https://life.example ",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credential(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("credential not found")
	}
	if got.ClientID != "client" || got.AccessToken != "access" || got.RefreshToken != "refresh" || got.TokenType != "Bearer" || got.Scope != "openid" || got.Resource != "https://life.example" {
		t.Fatalf("credential = %#v", got)
	}
}

func TestCredentialTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if err := s.SaveCredential(ctx, Identity{Platform: "napcat", UserID: "42"}, Credential{
		ClientID:    "client",
		AccessToken: "access",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credential(ctx, Identity{Platform: " napcat ", UserID: " 42 "})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.AccessToken != "access" {
		t.Fatalf("credential = %#v", got)
	}
}

func TestSaveCredentialRejectsBlankRequiredFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42"}
	base := Credential{
		ClientID:    "client",
		AccessToken: "access",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	tests := []Credential{
		func() Credential { next := base; next.ClientID = " "; return next }(),
		func() Credential { next := base; next.AccessToken = " "; return next }(),
	}
	for _, cred := range tests {
		err := s.SaveCredential(context.Background(), ident, cred)
		if err == nil || !strings.Contains(err.Error(), "credential") {
			t.Fatalf("SaveCredential(%#v) error = %v", cred, err)
		}
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

func TestRecordInteractionTrimsMetadataOnly(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.RecordConversationState(ctx, ident, " todo ", " raw state "); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInteraction(ctx, ident, Interaction{
		Direction: " inbound ",
		RawText:   "  td done 1  ",
		Command:   " todo ",
		Args:      " done 1 ",
		Handled:   true,
		Reply:     "  已完成：写报告  ",
		Status:    " handled ",
		Error:     " warning ",
	}); err != nil {
		t.Fatal(err)
	}
	var state conversationStateRow
	if err := s.db.WithContext(ctx).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.LastCommand != "todo" || state.State != " raw state " {
		t.Fatalf("state = %#v", state)
	}
	var row interactionRow
	if err := s.db.WithContext(ctx).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.RawText != "  td done 1  " || row.Reply != "  已完成：写报告  " {
		t.Fatalf("text fields changed: %#v", row)
	}
	if row.Direction != "inbound" || row.Command != "todo" || row.Args != "done 1" || row.Status != "handled" || row.Error != "warning" {
		t.Fatalf("metadata = %#v", row)
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

func TestSaveLoginSessionTrimsFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveLoginSession(ctx, ident, LoginSession{
		DeviceCode:              " device ",
		UserCode:                " USER-CODE ",
		VerificationURI:         " https://life.example/device ",
		VerificationURIComplete: " https://life.example/device?user_code=USER-CODE ",
		ClientID:                " client ",
		ExpiresAt:               time.Now().Add(time.Minute),
		Status:                  " pending ",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveLoginSession(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("session not found")
	}
	if got.DeviceCode != "device" || got.UserCode != "USER-CODE" || got.VerificationURI != "https://life.example/device" || got.ClientID != "client" || got.Status != "pending" {
		t.Fatalf("session = %#v", got)
	}
}

func TestActiveLoginSessionTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if err := s.SaveLoginSession(ctx, Identity{Platform: "napcat", UserID: "42"}, LoginSession{
		DeviceCode:      "device",
		UserCode:        "USER-CODE",
		VerificationURI: "https://life.example/device",
		ClientID:        "client",
		ExpiresAt:       time.Now().Add(time.Minute),
		Status:          "pending",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveLoginSession(ctx, Identity{Platform: " napcat ", UserID: " 42 "})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.DeviceCode != "device" {
		t.Fatalf("session = %#v", got)
	}
}

func TestSaveLoginSessionRejectsBlankRequiredFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	base := LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}
	tests := []LoginSession{
		func() LoginSession { next := base; next.DeviceCode = " "; return next }(),
		func() LoginSession { next := base; next.ClientID = " "; return next }(),
		func() LoginSession { next := base; next.Status = " "; return next }(),
	}
	for _, session := range tests {
		err := s.SaveLoginSession(ctx, ident, session)
		if err == nil || !strings.Contains(err.Error(), "login session") {
			t.Fatalf("SaveLoginSession(%#v) error = %v", session, err)
		}
	}
}

func TestMarkLoginSessionTrimsUpdateFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	if err := s.SaveLoginSession(ctx, ident, LoginSession{
		DeviceCode: "device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLoginSession(ctx, ident, " device ", " approved "); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveLoginSession(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no active session, got %#v", got)
	}
}

func TestMarkLoginSessionRejectsBlankUpdateFields(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	tests := []struct {
		deviceCode string
		status     string
	}{
		{deviceCode: "", status: "approved"},
		{deviceCode: "   ", status: "approved"},
		{deviceCode: "device", status: ""},
		{deviceCode: "device", status: "   "},
	}
	for _, tt := range tests {
		err := s.MarkLoginSession(ctx, ident, tt.deviceCode, tt.status)
		if err == nil || !strings.Contains(err.Error(), "login session") {
			t.Fatalf("MarkLoginSession(%q, %q) error = %v", tt.deviceCode, tt.status, err)
		}
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

func TestSaveLoginSessionSupersedesFailedNotificationSession(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveLoginSession(ctx, ident, LoginSession{
		DeviceCode: "old-device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLoginSession(ctx, ident, "old-device", "notify_failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLoginSession(ctx, ident, LoginSession{
		DeviceCode: "new-device",
		ClientID:   "client",
		ExpiresAt:  time.Now().Add(time.Minute),
		Status:     "pending",
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.PendingLoginSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].DeviceCode != "new-device" {
		t.Fatalf("sessions = %#v", sessions)
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

func TestSaveNotificationSettingsTrimsConversationIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if err := s.SaveNotificationSettings(ctx, NotificationSettings{
		Identity: Identity{
			Platform:         " napcat ",
			UserID:           " 42 ",
			ConversationType: " private ",
			ConversationID:   " 42 ",
		},
		ClassesEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.EnabledNotificationSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if len(enabled) != 1 || enabled[0].Identity != want {
		t.Fatalf("enabled = %#v", enabled)
	}
}

func TestNotificationSettingsTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	want := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.SaveNotificationSettings(ctx, NotificationSettings{
		Identity:         want,
		HomeworkEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.NotificationSettings(ctx, Identity{
		Platform:         " napcat ",
		UserID:           " 42 ",
		ConversationType: " private ",
		ConversationID:   " 42 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.HomeworkEnabled || got.Identity != want {
		t.Fatalf("settings = %#v", got)
	}
}

func TestSaveNotificationSettingsRejectsEnabledWithoutConversation(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	tests := []NotificationSettings{
		{
			Identity:       Identity{Platform: "napcat", UserID: "42"},
			ClassesEnabled: true,
		},
		{
			Identity:        Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: " "},
			HomeworkEnabled: true,
		},
	}
	for _, settings := range tests {
		err := s.SaveNotificationSettings(ctx, settings)
		if err == nil || !strings.Contains(err.Error(), "notification settings") {
			t.Fatalf("SaveNotificationSettings(%#v) error = %v", settings, err)
		}
	}
	if err := s.SaveNotificationSettings(ctx, NotificationSettings{
		Identity: Identity{Platform: "napcat", UserID: "42"},
	}); err != nil {
		t.Fatalf("disabled settings should remain saveable: %v", err)
	}
}

func TestNotificationDeliveryTrimsKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	recorded, err := s.TryRecordNotificationDelivery(ctx, ident, " class ", " section-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("first delivery was not recorded")
	}
	recorded, err = s.TryRecordNotificationDelivery(ctx, ident, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("trim-equivalent duplicate delivery was recorded")
	}
	delivered, err := s.NotificationDelivered(ctx, ident, " class ", " section-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("trimmed delivery was not found")
	}
}

func TestNotificationDeliveryRejectsBlankKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	tests := []struct {
		kind    string
		itemKey string
	}{
		{kind: "", itemKey: "section-1"},
		{kind: "   ", itemKey: "section-1"},
		{kind: "class", itemKey: ""},
		{kind: "class", itemKey: "   "},
	}
	for _, tt := range tests {
		recorded, err := s.TryRecordNotificationDelivery(ctx, ident, tt.kind, tt.itemKey)
		if err == nil || recorded {
			t.Fatalf("TryRecordNotificationDelivery(%q, %q) = %v, %v", tt.kind, tt.itemKey, recorded, err)
		}
		delivered, err := s.NotificationDelivered(ctx, ident, tt.kind, tt.itemKey)
		if err == nil || delivered {
			t.Fatalf("NotificationDelivered(%q, %q) = %v, %v", tt.kind, tt.itemKey, delivered, err)
		}
	}
}
