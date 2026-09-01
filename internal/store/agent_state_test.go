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
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "checkpoint-round-trip", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue checkpoint job: job=%#v created=%v err=%v", job, created, err)
	}
	claimed, err := s.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim checkpoint job: job=%#v err=%v", claimed, err)
	}
	checkpoints := s.AgentCheckpoints()
	bound, err := checkpoints.Bind(AgentCheckpointClaim{JobID: claimed.ID, Revision: claimed.Revision, LeaseToken: claimed.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := bound.Set(ctx, "job:7", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := bound.Set(ctx, "job:7", []byte("second")); err != nil {
		t.Fatal(err)
	}
	payload, found, err := bound.Get(ctx, "job:7")
	if err != nil || !found || string(payload) != "second" {
		t.Fatalf("checkpoint: payload=%q found=%v err=%v", payload, found, err)
	}
	if ok, err := s.CompleteConversationJob(ctx, claimed.ID, claimed.LeaseToken); err != nil || !ok {
		t.Fatalf("complete checkpoint job: ok=%v err=%v", ok, err)
	}
	if err := bound.Delete(ctx, "job:7"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := checkpoints.Get(ctx, "job:7"); err != nil || found {
		t.Fatalf("deleted checkpoint: found=%v err=%v", found, err)
	}
}

func TestAgentCheckpointClaimRejectsStaleWorkersAndTransfersOnResume(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "checkpoint-stale", ConversationType: "private", ConversationID: "checkpoint-stale"}
	job, created, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "checkpoint-stale", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: job=%#v created=%v err=%v", job, created, err)
	}
	first, err := s.ClaimConversationJob(ctx, ident)
	if err != nil || first == nil {
		t.Fatalf("first claim: job=%#v err=%v", first, err)
	}
	firstBound, err := s.AgentCheckpoints().Bind(AgentCheckpointClaim{
		JobID: first.ID, Revision: first.Revision, LeaseToken: first.LeaseToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := "conversation-job:checkpoint-stale"
	if err := firstBound.Set(ctx, checkpointID, []byte("first")); err != nil {
		t.Fatal(err)
	}
	recoveredAt := first.ClaimedAt.Add(time.Minute)
	if err := s.RecoverConversationJobLeases(ctx, recoveredAt, time.Nanosecond); err != nil {
		t.Fatalf("recover first claim: %v", err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, recoveredAt)
	if err != nil || second == nil {
		t.Fatalf("second claim: job=%#v err=%v", second, err)
	}
	secondBound, err := s.AgentCheckpoints().Bind(AgentCheckpointClaim{
		JobID: second.ID, Revision: second.Revision, LeaseToken: second.LeaseToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, found, err := secondBound.Get(ctx, checkpointID)
	if err != nil || !found || string(payload) != "first" {
		t.Fatalf("second worker checkpoint transfer: payload=%q found=%v err=%v", payload, found, err)
	}
	if _, found, err := firstBound.Get(ctx, checkpointID); !errors.Is(err, ErrAgentCheckpointClaimMismatch) || found {
		t.Fatalf("stale checkpoint rebind: found=%v err=%v", found, err)
	}
	if err := firstBound.Set(ctx, checkpointID, []byte("stale")); !errors.Is(err, ErrAgentCheckpointClaimMismatch) {
		t.Fatalf("stale write error = %v", err)
	}
	if err := secondBound.Set(ctx, checkpointID, []byte("second")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteConversationJob(ctx, second.ID, second.LeaseToken); err != nil || !ok {
		t.Fatalf("complete second claim: ok=%v err=%v", ok, err)
	}
	if err := firstBound.Delete(ctx, checkpointID); !errors.Is(err, ErrAgentCheckpointClaimMismatch) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := secondBound.Delete(ctx, checkpointID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.AgentCheckpoints().Get(ctx, checkpointID); err != nil || found {
		t.Fatalf("checkpoint after acknowledgement: found=%v err=%v", found, err)
	}
	if err := firstBound.Set(ctx, checkpointID, []byte("recreate")); !errors.Is(err, ErrAgentCheckpointClaimMismatch) {
		t.Fatalf("stale recreation error = %v", err)
	}
}

func TestRecoveredCheckpointCanOnlyBeDeletedByTerminalizingLease(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := t.Context()
	ident := Identity{Platform: "napcat", UserID: "checkpoint-terminal-lease", ConversationType: "private", ConversationID: "checkpoint-terminal-lease"}
	job, _, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "checkpoint-terminal-lease", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimConversationJob(ctx, ident)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	firstBound, err := s.AgentCheckpoints().Bind(AgentCheckpointClaim{JobID: job.ID, Revision: first.Revision, LeaseToken: first.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	const checkpointID = "conversation-job:terminal-lease-race"
	if err := firstBound.Set(ctx, checkpointID, []byte("first")); err != nil {
		t.Fatal(err)
	}
	recoveredAt := first.ClaimedAt.Add(time.Minute)
	if err := s.RecoverConversationJobLeases(ctx, recoveredAt, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimConversationJob(ctx, ident, recoveredAt)
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	secondBound, err := s.AgentCheckpoints().Bind(AgentCheckpointClaim{JobID: job.ID, Revision: second.Revision, LeaseToken: second.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	// The recovered worker finishes before reading/adopting the old payload.
	if ok, err := s.CompleteConversationJob(ctx, second.ID, second.LeaseToken); err != nil || !ok {
		t.Fatalf("complete recovered job ok=%v err=%v", ok, err)
	}
	if err := firstBound.Delete(ctx, checkpointID); !errors.Is(err, ErrAgentCheckpointClaimMismatch) {
		t.Fatalf("stale lease deleted recovered checkpoint: %v", err)
	}
	if err := secondBound.Delete(ctx, checkpointID); err != nil {
		t.Fatalf("terminalizing lease could not delete predecessor checkpoint: %v", err)
	}
}

func TestExternalJobTerminationDeletesPrivateCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name      string
		terminate func(*Store, context.Context, ConversationJob, time.Time) error
	}{
		{
			name: "cancel",
			terminate: func(s *Store, ctx context.Context, job ConversationJob, _ time.Time) error {
				cancelled, err := s.CancelConversationJob(ctx, job.ID, "用户取消")
				if err == nil && !cancelled {
					return errors.New("job was not cancelled")
				}
				return err
			},
		},
		{
			name: "expire",
			terminate: func(s *Store, ctx context.Context, _ ConversationJob, expiresAt time.Time) error {
				return s.ExpireConversationJobs(ctx, expiresAt.Add(time.Second))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := Open(t.TempDir() + "/bot.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			ctx := t.Context()
			expiresAt := time.Now().UTC().Add(time.Minute)
			ident := Identity{Platform: "napcat", UserID: "checkpoint-" + test.name, ConversationType: "private", ConversationID: "checkpoint-" + test.name}
			job, _, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
				Identity: ident, SourceEventID: "checkpoint-" + test.name, ExpiresAt: expiresAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimConversationJob(ctx, ident)
			if err != nil || claimed == nil {
				t.Fatalf("claim=%#v err=%v", claimed, err)
			}
			bound, err := s.AgentCheckpoints().Bind(AgentCheckpointClaim{
				JobID: claimed.ID, Revision: claimed.Revision, LeaseToken: claimed.LeaseToken,
			})
			if err != nil {
				t.Fatal(err)
			}
			checkpointID := "conversation-job:external-" + test.name
			if err := bound.Set(ctx, checkpointID, []byte("private transcript")); err != nil {
				t.Fatal(err)
			}
			if err := test.terminate(s, ctx, job, expiresAt); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.AgentCheckpoints().Get(ctx, checkpointID); err != nil || found {
				t.Fatalf("checkpoint survived %s: found=%v err=%v", test.name, found, err)
			}
		})
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
	claimedExecution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, first.ID, claimed.ID, claimed.LeaseToken)
	if err != nil || !execute {
		t.Fatalf("claim approved operation: execute=%v err=%v", execute, err)
	}
	if _, err := s.FinishCapabilityExecution(ctx, first.ID, claimedExecution.LeaseToken, "已订阅", nil); err != nil {
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
	if _, execute, err := s.ClaimCapabilityExecutionForJob(ctx, second.ID, claimed.ID, claimed.LeaseToken); err != nil || execute {
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
		`CREATE TABLE agent_settings (user_id integer primary key, expose_tool_calls numeric not null default 0)`,
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
	for _, table := range []string{"conversation_summaries", "pending_confirmations", "pending_requests", "notification_deliveries", "agent_settings"} {
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

func TestSchemaMigrationRejectsIntermediateVersionBeforeChangingTables(t *testing.T) {
	path := t.TempDir() + "/intermediate.db"
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("intermediate schema version was accepted")
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
		t.Fatal("intermediate database was modified before rejection")
	}
}

func TestCurrentSchemaRejectsMalformedOrObsoleteShape(t *testing.T) {
	t.Run("missing required tables", func(t *testing.T) {
		path := t.TempDir() + "/missing.db"
		db, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("malformed current schema was accepted")
		}
	})

	t.Run("obsolete table", func(t *testing.T) {
		path := t.TempDir() + "/obsolete.db"
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("CREATE TABLE conversation_summaries (id integer primary key)").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("obsolete current schema was silently repaired")
		}
	})

	t.Run("missing required index", func(t *testing.T) {
		path := t.TempDir() + "/missing-index.db"
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("DROP INDEX idx_outgoing_messages_dedupe_key").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("current schema missing an idempotency index was silently repaired")
		}
	})

	t.Run("missing runtime column", func(t *testing.T) {
		path := t.TempDir() + "/missing-column.db"
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("ALTER TABLE outgoing_messages DROP COLUMN payload_json").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("current schema missing a runtime column was silently repaired")
		}
	})

	t.Run("wrong index definition", func(t *testing.T) {
		path := t.TempDir() + "/wrong-index.db"
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("DROP INDEX idx_outgoing_messages_dedupe_key").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("CREATE INDEX idx_outgoing_messages_dedupe_key ON outgoing_messages (dedupe_key)").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("current schema with a non-unique idempotency index was accepted")
		}
	})

	t.Run("partial unique index", func(t *testing.T) {
		path := t.TempDir() + "/partial-index.db"
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("DROP INDEX idx_outgoing_messages_dedupe_key").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("CREATE UNIQUE INDEX idx_outgoing_messages_dedupe_key ON outgoing_messages (dedupe_key) WHERE status = 'accepted'").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(path); err == nil {
			_ = opened.Close()
			t.Fatal("current schema with a partial idempotency index was accepted")
		}
	})
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
	if _, err := s.FinishCapabilityExecution(context.Background(), execution.ID, "not-running", "", errors.New("should not run")); err == nil {
		t.Fatal("awaiting confirmation execution was finalized")
	}
}

func TestCapabilityExecutionWaitsForAuthWithoutLosingApproval(t *testing.T) {
	s, err := Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	ident := Identity{Platform: "napcat", UserID: "42", ConversationType: "private", ConversationID: "42"}
	job, _, err := s.EnqueueConversationJob(ctx, ConversationJobEnqueue{
		Identity: ident, SourceEventID: "auth-state", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, _, err := s.PrepareCapabilityExecution(ctx, CapabilityExecutionPrepare{
		Identity: ident, JobID: job.ID, DedupeKey: "auth-state", Capability: "subscription", Effect: "write",
		RequiresConfirmation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimConversationJob(ctx, ident)
	if err != nil || claimed == nil {
		t.Fatalf("claim auth job: job=%#v err=%v", claimed, err)
	}
	if err := s.db.WithContext(ctx).Model(&capabilityExecutionRow{}).Where("id = ?", execution.ID).
		Update("state", string(CapabilityExecutionApproved)).Error; err != nil {
		t.Fatal(err)
	}
	execution, execute, err := s.ClaimCapabilityExecutionForJob(ctx, execution.ID, claimed.ID, claimed.LeaseToken)
	if err != nil || !execute || execution.State != CapabilityExecutionRunning {
		t.Fatalf("initial claim: execution=%#v claimed=%v err=%v", execution, execute, err)
	}
	execution, err = s.DeferCapabilityExecutionForAuth(ctx, execution.ID, execution.LeaseToken)
	if err != nil || execution.State != CapabilityExecutionWaitingAuth || execution.StartedAt != nil {
		t.Fatalf("defer for auth: execution=%#v err=%v", execution, err)
	}
	execution, execute, err = s.ClaimCapabilityExecutionForJob(ctx, execution.ID, claimed.ID, claimed.LeaseToken)
	if err != nil || !execute || execution.State != CapabilityExecutionRunning {
		t.Fatalf("claim after auth: execution=%#v claimed=%v err=%v", execution, execute, err)
	}
}
