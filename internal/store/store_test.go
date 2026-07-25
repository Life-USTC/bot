package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicCommandCacheUsesVersionAndExpiration(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	entry := PublicCommandCacheEntry{
		Version:   " version-a ",
		Command:   " course ",
		Args:      " math ",
		Response:  "first",
		ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := s.SavePublicCommandCache(ctx, entry); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.PublicCommandCache(ctx, "version-a", "course", "math", now)
	if err != nil || !ok || got.Response != "first" {
		t.Fatalf("cache hit = %#v, ok = %v, err = %v", got, ok, err)
	}
	if _, ok, err := s.PublicCommandCache(ctx, "version-b", "course", "math", now); err != nil || ok {
		t.Fatalf("different-version cache ok = %v, err = %v", ok, err)
	}
	if _, ok, err := s.PublicCommandCache(ctx, "version-a", "course", "math", now.Add(5*time.Minute)); err != nil || ok {
		t.Fatalf("expired cache ok = %v, err = %v", ok, err)
	}

	entry.Response = "updated"
	entry.ExpiresAt = now.Add(10 * time.Minute)
	if err := s.SavePublicCommandCache(ctx, entry); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.PublicCommandCache(ctx, "version-a", "course", "math", now)
	if err != nil || !ok || got.Response != "updated" {
		t.Fatalf("updated cache = %#v, ok = %v, err = %v", got, ok, err)
	}

	if err := s.SavePublicCommandCache(ctx, PublicCommandCacheEntry{
		Version: "version-b", Command: "teacher", Args: "zhang", Response: "teacher", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgePublicCommandCache(ctx, "version-b", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.PublicCommandCache(ctx, "version-a", "course", "math", now); err != nil || ok {
		t.Fatalf("old-version cache survived purge: ok = %v, err = %v", ok, err)
	}
	if got, ok, err := s.PublicCommandCache(ctx, "version-b", "teacher", "zhang", now); err != nil || !ok || got.Response != "teacher" {
		t.Fatalf("current-version cache = %#v, ok = %v, err = %v", got, ok, err)
	}
}

func TestPublicCommandCacheRejectsIncompleteEntries(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SavePublicCommandCache(context.Background(), PublicCommandCacheEntry{}); err == nil {
		t.Fatal("incomplete cache entry was accepted")
	}
	if _, _, err := s.PublicCommandCache(context.Background(), "", "course", "", time.Now()); err == nil {
		t.Fatal("blank cache version was accepted")
	}
	if err := s.PurgePublicCommandCache(context.Background(), "", time.Now()); err == nil {
		t.Fatal("blank purge version was accepted")
	}
}

func TestOpenConfiguresBusyTimeout(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	var timeout int
	if err := s.db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", timeout)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at %q: %v", path, err)
	}
}

func TestConcurrentStoreWaitsForWriter(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	tx := first.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec(
		"INSERT INTO users (platform, external_user_id, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"napcat", "locked-writer", time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	errCh := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		defer wg.Done()
		close(started)
		_, err := second.EnsureUser(context.Background(), Identity{Platform: "napcat", UserID: "waiting-writer"})
		errCh <- err
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatalf("concurrent writer returned before lock release: %v", err)
	default:
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-errCh; err != nil {
		t.Fatalf("concurrent writer failed after lock release: %v", err)
	}
}

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

func TestRecordInteractionStoresPlatformAcceptanceReceipt(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{
		Platform:         "qqbot",
		UserID:           "user-openid",
		ConversationType: "private",
		ConversationID:   "user-openid",
	}
	acceptedAt := time.Date(2026, 7, 18, 1, 2, 3, 0, time.FixedZone("CST", 8*60*60))
	if err := s.RecordInteraction(ctx, ident, Interaction{
		Direction:         InteractionDirectionOutbound,
		RawText:           "课程结果",
		Handled:           true,
		Status:            InteractionStatusAccepted,
		PlatformMessageID: " message-123 ",
		DeliveryMethod:    " media_cache ",
		SourceMessageID:   " source-456 ",
		AcceptedAt:        acceptedAt,
	}); err != nil {
		t.Fatal(err)
	}

	var row interactionRow
	if err := s.db.WithContext(ctx).Where("platform_message_id = ?", "message-123").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != InteractionStatusAccepted || row.AcceptedAt == nil {
		t.Fatalf("receipt row = %#v", row)
	}
	if !row.AcceptedAt.Equal(acceptedAt.UTC()) {
		t.Fatalf("accepted_at = %s, want %s", row.AcceptedAt, acceptedAt.UTC())
	}
	if row.DeliveryMethod != DeliveryMethodMediaCache || row.SourceMessageID != "source-456" {
		t.Fatalf("delivery metadata = method %q source %q", row.DeliveryMethod, row.SourceMessageID)
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

func TestIdentityCompletenessHelpersTrimFields(t *testing.T) {
	full := Identity{Platform: " napcat ", UserID: " 42 ", ConversationType: " private ", ConversationID: " 42 "}
	if !HasUserIdentity(full) {
		t.Fatal("HasUserIdentity rejected padded user identity")
	}
	if !HasConversationTarget(full) {
		t.Fatal("HasConversationTarget rejected padded conversation target")
	}
	if !HasConversationIdentity(full) {
		t.Fatal("HasConversationIdentity rejected padded full identity")
	}
	if !IsPrivateConversation(full) {
		t.Fatal("IsPrivateConversation rejected padded private conversation type")
	}
	if !IsGroupConversation(Identity{ConversationType: " GROUP "}) {
		t.Fatal("IsGroupConversation rejected padded group conversation type")
	}
	if IsPrivateConversation(Identity{ConversationType: "group"}) {
		t.Fatal("IsPrivateConversation accepted group conversation type")
	}
	if IsGroupConversation(Identity{ConversationType: "private"}) {
		t.Fatal("IsGroupConversation accepted private conversation type")
	}
	if HasUserIdentity(Identity{Platform: "napcat", UserID: " "}) {
		t.Fatal("HasUserIdentity accepted blank user id")
	}
	if HasConversationTarget(Identity{ConversationType: " ", ConversationID: "42"}) {
		t.Fatal("HasConversationTarget accepted blank conversation type")
	}
	if HasConversationIdentity(Identity{Platform: "napcat", UserID: "42", ConversationType: "private"}) {
		t.Fatal("HasConversationIdentity accepted missing conversation id")
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
	var row credentialRow
	if err := s.db.WithContext(context.Background()).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.UpdatedAt.IsZero() {
		t.Fatal("credential UpdatedAt is zero")
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

func TestCredentialRejectsIncompleteIdentity(t *testing.T) {
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
		_, err := s.Credential(context.Background(), ident)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("Credential(%#v) error = %v", ident, err)
		}
	}
}

func TestDeleteCredentialDoesNotCreateMissingUser(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if err := s.DeleteCredential(ctx, Identity{Platform: "napcat", UserID: "42"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after missing credential delete = %d", count)
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
	var count int64
	if err := s.db.WithContext(context.Background()).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after invalid credential = %d", count)
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

func TestRecentHandledInteractionsBreaksCreatedAtTiesByID(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	for _, text := range []string{"first", "second", "third"} {
		if err := s.RecordInteraction(ctx, ident, Interaction{
			RawText: text,
			Command: "agent",
			Handled: true,
			Reply:   "reply " + text,
			Status:  "handled",
		}); err != nil {
			t.Fatal(err)
		}
	}
	sameTime := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if err := s.db.WithContext(ctx).Model(&interactionRow{}).Where("1 = 1").Update("created_at", sameTime).Error; err != nil {
		t.Fatal(err)
	}

	recent, err := s.RecentHandledInteractions(ctx, ident, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].RawText != "second" || recent[1].RawText != "third" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRecentHandledInteractionsTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	paddedIdent := Identity{Platform: " napcat ", UserID: " 42 ", ConversationType: " private ", ConversationID: " 42 "}
	if err := s.RecordInteraction(ctx, paddedIdent, Interaction{
		RawText: "我上面说了什么",
		Command: "agent",
		Handled: true,
		Reply:   "reply",
		Status:  "handled",
	}); err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentHandledInteractions(ctx, ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].RawText != "我上面说了什么" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRecentHandledInteractionsNormalizesIdentityCase(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	recordedIdent := Identity{Platform: " NapCat ", UserID: "42", ConversationType: " GROUP ", ConversationID: "100"}
	lookupIdent := Identity{Platform: "napcat", UserID: "42", ConversationType: "group", ConversationID: "100"}
	if err := s.RecordInteraction(ctx, recordedIdent, Interaction{
		RawText: "校车",
		Command: "bus",
		Handled: true,
		Reply:   "reply",
		Status:  "handled",
	}); err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentHandledInteractions(ctx, lookupIdent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Command != "bus" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRecentHandledInteractionsRejectsIncompleteIdentity(t *testing.T) {
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
		_, err := s.RecentHandledInteractions(context.Background(), ident, 0)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("RecentHandledInteractions(%#v) error = %v", ident, err)
		}
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

func TestRecordInteractionTrimsUnknownDirection(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.RecordInteraction(ctx, ident, Interaction{
		Direction: " Custom ",
		RawText:   "custom",
		Handled:   true,
		Status:    "handled",
	}); err != nil {
		t.Fatal(err)
	}
	var direction string
	err = s.db.WithContext(ctx).Raw(`SELECT direction FROM interactions WHERE raw_text = ?`, "custom").Scan(&direction).Error
	if err != nil {
		t.Fatal(err)
	}
	if direction != "Custom" {
		t.Fatalf("direction = %q", direction)
	}
}

func TestBusSettingsDefaultAndSave(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	settings, err := s.BusSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ShowSouthCampus {
		t.Fatalf("default settings = %#v", settings)
	}

	settings.ShowSouthCampus = true
	if err := s.SaveBusSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	got, err := s.BusSettings(ctx, Identity{Platform: " NapCat ", UserID: " 42 "})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ShowSouthCampus {
		t.Fatalf("settings = %#v", got)
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
	if state.CreatedAt.IsZero() {
		t.Fatal("state CreatedAt is zero")
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
	if row.CreatedAt.IsZero() {
		t.Fatal("interaction CreatedAt is zero")
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
	var row loginSessionRow
	if err := s.db.WithContext(context.Background()).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatalf("session timestamps are zero: %#v", row)
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
	if err := s.db.WithContext(context.Background()).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "approved" || row.UpdatedAt.IsZero() {
		t.Fatalf("marked session = %#v", row)
	}
	got, err = s.ActiveLoginSession(context.Background(), ident)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no active session, got %#v", got)
	}
}

func TestRecordConversationStateTrimsIdentityKeys(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	paddedIdent := Identity{Platform: " napcat ", UserID: " 42 ", ConversationType: " private ", ConversationID: " 42 "}
	if err := s.RecordConversationState(ctx, paddedIdent, " todo ", "pending"); err != nil {
		t.Fatal(err)
	}
	var row conversationStateRow
	if err := s.db.WithContext(ctx).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	want := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	got := Identity{Platform: row.Platform, UserID: row.UserID, ConversationType: row.ConversationType, ConversationID: row.ConversationID}
	if got != want {
		t.Fatalf("identity = %#v", got)
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

func TestActiveLoginSessionRejectsIncompleteIdentity(t *testing.T) {
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
		_, err := s.ActiveLoginSession(context.Background(), ident)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("ActiveLoginSession(%#v) error = %v", ident, err)
		}
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
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after invalid login session = %d", count)
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

func TestMarkLoginSessionDoesNotCreateMissingUser(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42"}
	if err := s.MarkLoginSession(ctx, ident, "device", "approved"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after missing login session update = %d", count)
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
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after invalid login session update = %d", count)
	}
}

func TestPendingLoginSessionsIncludeNotificationIdentity(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	paddedIdent := Identity{Platform: " napcat ", UserID: " 42 ", ConversationType: " private ", ConversationID: " 42 "}
	if err := s.SaveLoginSession(context.Background(), paddedIdent, LoginSession{
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
	var settingRow notificationSettingRow
	if err := s.db.WithContext(ctx).First(&settingRow, "conversation_type = ? AND conversation_id = ?", "private", "42").Error; err != nil {
		t.Fatal(err)
	}
	if settingRow.UpdatedAt.IsZero() {
		t.Fatal("notification settings updated_at was not set")
	}

	recorded, err := s.TryRecordNotificationDelivery(ctx, ident, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("first delivery was not recorded")
	}
	var delivery notificationDeliveryRow
	if err := s.db.WithContext(ctx).First(&delivery, "kind = ? AND item_key = ?", "class", "section-1").Error; err != nil {
		t.Fatal(err)
	}
	if delivery.CreatedAt.IsZero() {
		t.Fatal("delivery created_at was not set")
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
		Identity:        want,
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

func TestAgentSettings(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	settings, err := s.AgentSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ExposeToolCalls {
		t.Fatalf("default settings = %#v", settings)
	}

	settings.ExposeToolCalls = true
	if err := s.SaveAgentSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentSettings(ctx, Identity{
		Platform:         " napcat ",
		UserID:           " 42 ",
		ConversationType: " private ",
		ConversationID:   " 42 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExposeToolCalls || got.Identity != ident {
		t.Fatalf("settings = %#v", got)
	}
	var row agentSettingRow
	if err := s.db.WithContext(ctx).First(&row, "conversation_type = ? AND conversation_id = ?", "private", "42").Error; err != nil {
		t.Fatal(err)
	}
	if row.UpdatedAt.IsZero() {
		t.Fatal("agent settings updated_at was not set")
	}
}

func TestNotificationSettingsRejectsIncompleteIdentity(t *testing.T) {
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
		_, err := s.NotificationSettings(context.Background(), ident)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("NotificationSettings(%#v) error = %v", ident, err)
		}
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
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after invalid notification settings = %d", count)
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
	recorded, err := s.TryRecordNotificationDelivery(ctx, ident, " CLASS ", " section-1 ")
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
		t.Fatal("normalized duplicate delivery was recorded")
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
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after invalid notification delivery = %d", count)
	}
}

func TestNotificationDeliveredDoesNotCreateMissingUser(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	delivered, err := s.NotificationDelivered(ctx, Identity{Platform: "napcat", UserID: "42"}, "class", "section-1")
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Fatal("missing user delivery reported true")
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&userRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("user count after delivery lookup = %d", count)
	}
}

func TestAgentRunLifecycle(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	id, err := s.RecordAgentRun(ctx, ident, AgentRun{
		RawText:  "帮我查一下",
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		Currency: SpendingCurrencyCNY,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("agent run id was not set")
	}
	spending := AgentSpending{
		PromptTokens:     100,
		CachedTokens:     40,
		CompletionTokens: 20,
		TotalTokens:      120,
		CostNanoCNY:      301_000,
		Currency:         SpendingCurrencyCNY,
	}
	if err := s.FinishAgentRun(ctx, id, AgentRunStatusCompleted, "查到了", nil, spending); err != nil {
		t.Fatal(err)
	}
	var row agentRunRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != AgentRunStatusCompleted || row.RawText != "帮我查一下" || row.Reply != "查到了" ||
		row.Provider != "deepseek" || row.Model != "deepseek-v4-pro" || row.CostNanoCNY != spending.CostNanoCNY {
		t.Fatalf("agent run row = %#v", row)
	}
	if err := s.FinishAgentRun(ctx, id, AgentRunStatusFailed, "失败了", errors.New("deepseek timeout"), spending); err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != AgentRunStatusFailed || row.Reply != "失败了" || row.Error != "deepseek timeout" {
		t.Fatalf("failed agent run row = %#v", row)
	}
}

func TestAgentSpendingTotalsByConversationAndUser(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	first := Identity{Platform: "qqbot", UserID: "admin", ConversationType: "private", ConversationID: "conversation-1"}
	second := Identity{Platform: "qqbot", UserID: "admin", ConversationType: "private", ConversationID: "conversation-2"}
	other := Identity{Platform: "qqbot", UserID: "other", ConversationType: "private", ConversationID: "conversation-3"}
	record := func(ident Identity, cost int64) {
		t.Helper()
		id, err := s.RecordAgentRun(ctx, ident, AgentRun{
			RawText: "hello", Provider: "premium", Model: "premium-model", Currency: SpendingCurrencyCNY,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishAgentRun(ctx, id, AgentRunStatusCompleted, "ok", nil, AgentSpending{
			PromptTokens: 10, CachedTokens: 2, CompletionTokens: 3, TotalTokens: 13,
			CostNanoCNY: cost, Currency: SpendingCurrencyCNY,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(first, 100)
	record(second, 200)
	record(other, 400)

	conversation, err := s.ConversationSpending(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.CostNanoCNY != 100 || conversation.TotalTokens != 13 || conversation.Currency != SpendingCurrencyCNY {
		t.Fatalf("conversation spending = %#v", conversation)
	}
	user, err := s.UserSpending(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if user.CostNanoCNY != 300 || user.TotalTokens != 26 || user.Currency != SpendingCurrencyCNY {
		t.Fatalf("user spending = %#v", user)
	}
}

func TestFeedbackRecordAndMarkSent(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "qqbot", UserID: "u", ConversationType: "private", ConversationID: "u"}
	id, err := s.RecordFeedback(ctx, ident, FeedbackRecord{
		Source:   "llm",
		Category: "missing_tool",
		Content:  "需要考试地点查询工具",
		Context:  "用户问考试地点",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFeedbackSent(ctx, id); err != nil {
		t.Fatal(err)
	}
	var row feedbackRecordRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	if row.Source != "llm" || row.Category != "missing_tool" || row.Status != FeedbackStatusOpen || !row.SentToAdmin || row.SentAt == nil {
		t.Fatalf("feedback row = %#v", row)
	}
}

func TestPendingConfirmationSupersedesAndReadsLatest(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	first, err := s.SavePendingConfirmation(ctx, ident, "待办 add A", "agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SavePendingConfirmation(ctx, ident, "待办 add B", "agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ActivePendingConfirmation(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.ID != second || pending.Command != "待办 add B" {
		t.Fatalf("pending = %#v", pending)
	}
	var firstRow pendingConfirmationRow
	if err := s.db.WithContext(ctx).First(&firstRow, first).Error; err != nil {
		t.Fatal(err)
	}
	if firstRow.Status != PendingConfirmationStatusSuperseded {
		t.Fatalf("first status = %q", firstRow.Status)
	}
	if err := s.MarkPendingConfirmation(ctx, second, PendingConfirmationStatusConfirmed); err != nil {
		t.Fatal(err)
	}
	pending, err = s.ActivePendingConfirmation(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatalf("confirmed pending still active = %#v", pending)
	}
}
