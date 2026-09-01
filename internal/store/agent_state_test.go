package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestAgentCheckpointStoreRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	checkpoints := s.AgentCheckpoints()
	if err := checkpoints.Set(ctx, "job:7", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := checkpoints.Set(ctx, "job:7", []byte("second")); err != nil {
		t.Fatal(err)
	}
	payload, found, err := checkpoints.Get(ctx, "job:7")
	if err != nil || !found || string(payload) != "second" {
		t.Fatalf("checkpoint: payload=%q found=%v err=%v", payload, found, err)
	}
	if err := checkpoints.Delete(ctx, "job:7"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := checkpoints.Get(ctx, "job:7"); err != nil || found {
		t.Fatalf("deleted checkpoint: found=%v err=%v", found, err)
	}
}

func TestCapabilityConfirmationResolvesGroupedOperationsOneAtATime(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "grouped", Input: ConversationJobInput{Text: "订阅两门课"},
		State: ConversationJobStateWaitingConfirmation, WaitReason: ConversationJobWaitReasonConfirmation,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	for i, subject := range []string{"数学分析（程艺，2026春）", "线性代数（李明，2026春）"} {
		_, created, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
			Identity: ident, JobID: job.ID, Sequence: i, DedupeKey: "job:grouped:" + subject,
			Capability: "subscription", Arguments: []string{"import", subject}, Effect: "write",
			Receipt: CapabilityReceipt{Action: "订阅", Resource: "课程", Subject: subject}, RequiresConfirmation: true,
		})
		if err != nil || !created {
			t.Fatalf("prepare %d: created=%v err=%v", i, created, err)
		}
	}

	first, released, err := s.ResolveCapabilityConfirmation(ctx, ident, CapabilityConfirmationDecision{Approved: true})
	if err != nil || first == nil || released == nil {
		t.Fatalf("approve first: operation=%#v job=%#v err=%v", first, released, err)
	}
	if first.Sequence != 0 || first.State != CapabilityExecutionApproved || released.State != ConversationJobStateQueued {
		t.Fatalf("first resolution: operation=%#v job=%#v", first, released)
	}
	claimed, err := s.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim released job: %#v err=%v", claimed, err)
	}
	if _, execute, err := s.ClaimCapabilityExecution(ctx, first.ID); err != nil || !execute {
		t.Fatalf("claim approved operation: execute=%v err=%v", execute, err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, first.ID, "已订阅", nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TransitionConversationJob(ctx, claimed.ID, claimed.LeaseToken, ConversationJobTransition{
		State: ConversationJobStateWaitingConfirmation, WaitReason: ConversationJobWaitReasonConfirmation,
	}); err != nil || !ok {
		t.Fatalf("return to confirmation: ok=%v err=%v", ok, err)
	}

	second, released, err := s.ResolveCapabilityConfirmation(ctx, ident, CapabilityConfirmationDecision{Reason: "不想订阅"})
	if err != nil || second == nil || released == nil {
		t.Fatalf("deny second: operation=%#v job=%#v err=%v", second, released, err)
	}
	if second.Sequence != 1 || second.State != CapabilityExecutionDenied || second.Error != "不想订阅" {
		t.Fatalf("second resolution = %#v", second)
	}
	if _, execute, err := s.ClaimCapabilityExecution(ctx, second.ID); err != nil || execute {
		t.Fatalf("denied operation became executable: execute=%v err=%v", execute, err)
	}
}

func TestSchemaMigrationBackfillsRawHistoryAndDropsSemanticSummaries(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	if err := s.RecordInteraction(context.Background(), ident, Interaction{
		RawText: "原始问题", Reply: "原始回答", Handled: true, Status: InteractionStatusHandled,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"PRAGMA user_version = 0",
		`CREATE TABLE conversation_summaries (id integer primary key, platform text, conversation_type text, conversation_id text, summary text, through_interaction_id integer, created_at datetime, updated_at datetime)`,
		`INSERT INTO conversation_summaries(platform, conversation_type, conversation_id, summary, through_interaction_id) VALUES ('napcat','private','42','模型生成的错误摘要',1)`,
		`CREATE TABLE pending_confirmations (id integer primary key)`,
		`CREATE TABLE pending_requests (id integer primary key)`,
		`CREATE TABLE notification_deliveries (id integer primary key)`,
		"DELETE FROM conversation_events",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	events, err := s.RecentConversationEvents(context.Background(), ident, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Content != "原始问题" || events[1].Content != "原始回答" {
		t.Fatalf("migrated events = %#v", events)
	}
	for _, table := range []string{"conversation_summaries", "pending_confirmations", "pending_requests", "notification_deliveries"} {
		if s.db.Migrator().HasTable(table) {
			t.Fatalf("obsolete table %s survived migration", table)
		}
	}
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d", version)
	}
}

func TestSchemaMigrationRejectsFutureVersionBeforeChangingTables(t *testing.T) {
	path := t.TempDir() + "/future.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("future schema version was accepted")
	}
	db, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("future database was modified before rejection")
	}
}

func TestFinishCapabilityExecutionRequiresRunningState(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, _, err := s.EnqueueConversationJob(context.Background(), ConversationJobEnqueue{
		Identity: ident, SourceEventID: "finish-state", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, _, err := s.PrepareCapabilityExecution(context.Background(), CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, DedupeKey: "finish-state", Capability: "logout", Effect: "destructive", RequiresConfirmation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishCapabilityExecution(context.Background(), execution.ID, "", errors.New("should not run")); err == nil {
		t.Fatal("awaiting confirmation execution was finalized")
	}
}
