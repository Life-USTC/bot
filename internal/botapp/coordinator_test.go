package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/routing"
	"github.com/Life-USTC/Bot/internal/store"
)

type commandFunc func(context.Context, commands.Input) (commands.Response, bool)

func (fn commandFunc) DescribeCapabilityInvocations(_ context.Context, _ commands.Input, id commands.CapabilityID, args []string) ([]commands.CapabilityInvocationDescription, error) {
	invocation, ok := commands.NewInvocation(id, args)
	if !ok {
		return nil, errors.New("invalid test capability")
	}
	invocations := commands.ExpandMutationInvocations(invocation)
	descriptions := make([]commands.CapabilityInvocationDescription, 0, len(invocations))
	for _, item := range invocations {
		receipt := commands.ReceiptForInvocation(item)
		descriptions = append(descriptions, commands.CapabilityInvocationDescription{
			Invocation: item, Policy: item.Policy(), ConfirmationRequired: item.Policy().Confirmation == commands.ConfirmUser,
			Receipt: &receipt,
		})
	}
	return descriptions, nil
}

func (fn commandFunc) ExecuteCapability(ctx context.Context, input commands.Input, _ commands.CapabilityID, _ []string) (commands.CapabilityOutcome, error) {
	response, handled := fn(ctx, input)
	if !handled {
		return commands.NotFoundOutcome(response), nil
	}
	status := commands.CapabilityOutcomeSuccess
	if response.Kind == commands.ResponseKindAuthWait {
		status = commands.CapabilityOutcomeAuthRequired
	}
	return commands.CapabilityOutcome{Status: status, Response: response}, nil
}

func (fn commandFunc) ExecuteApprovedInvocation(ctx context.Context, input commands.Input, _ commands.CapabilityInvocationDescription) (commands.CapabilityOutcome, error) {
	return fn.ExecuteCapability(ctx, input, "", nil)
}

type agentFunc func(context.Context, agent.Input) (commands.Response, bool)

func (fn agentFunc) Run(ctx context.Context, input agent.Input) agent.Result {
	response, handled := fn(ctx, input)
	return agent.Result{Response: response, Handled: handled, State: agent.RunStateCompleted}
}

func (fn agentFunc) Acknowledge(context.Context, int64) error { return nil }

type agentResultFunc func(context.Context, agent.Input) agent.Result

func (fn agentResultFunc) Run(ctx context.Context, input agent.Input) agent.Result {
	return fn(ctx, input)
}

func (fn agentResultFunc) Acknowledge(context.Context, int64) error { return nil }

type rendererFunc func(*responses.Image) ([]byte, int, int, error)

func (fn rendererFunc) RenderPNG(image *responses.Image) ([]byte, int, int, error) {
	return fn(image)
}

type outputCommitFaultStore struct {
	*store.Store
	mu       sync.Mutex
	failures int
}

func (s *outputCommitFaultStore) CommitConversationJobOutput(ctx context.Context, commit store.ConversationJobOutputCommit) ([]store.ConversationJobCommittedOutput, error) {
	s.mu.Lock()
	inject := s.failures > 0
	if inject {
		s.failures--
	}
	s.mu.Unlock()
	if inject {
		commit.Messages = append(commit.Messages, message.Outbound{DedupeKey: "injected-invalid-output"})
	}
	return s.Store.CommitConversationJobOutput(ctx, commit)
}

type receiptReadFaultStore struct {
	*store.Store
	mu       sync.Mutex
	failures int
}

func (s *receiptReadFaultStore) UnsentCapabilityExecutionsForJob(ctx context.Context, jobID int64) ([]store.CapabilityExecution, error) {
	s.mu.Lock()
	inject := s.failures > 0
	if inject {
		s.failures--
	}
	s.mu.Unlock()
	if inject {
		return nil, errors.New("injected receipt loading failure")
	}
	return s.Store.UnsentCapabilityExecutionsForJob(ctx, jobID)
}

type periodicRecoveryStore struct {
	*store.Store
	mu    sync.Mutex
	calls int
}

func (s *periodicRecoveryStore) RecoverConversationJobLeases(ctx context.Context, now time.Time, _ ...time.Duration) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		return nil
	}
	return s.Store.RecoverConversationJobLeases(ctx, now, time.Nanosecond)
}

func (s *periodicRecoveryStore) recoveryCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newCoordinatorStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func jobInbound(eventID, text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: "napcat", UserID: "42"},
		Conversation: message.Conversation{Platform: "napcat", Type: "private", ID: "42"},
		Source:       message.ReplyRef{EventID: eventID, MessageID: "message-" + eventID},
		Text:         text,
	}
}

func groupJobInbound(eventID, userID, text string) message.Inbound {
	return message.Inbound{
		Actor:        message.Actor{Platform: "napcat", UserID: userID},
		Conversation: message.Conversation{Platform: "napcat", Type: "group", ID: "100"},
		Source:       message.ReplyRef{EventID: eventID, MessageID: "message-" + eventID},
		Text:         text,
	}
}

func claimOnlyConversationJob(t *testing.T, db *store.Store) store.ConversationJob {
	t.Helper()
	job, err := db.ClaimNextConversationJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("expected one claimed conversation job")
	}
	return *job
}

func TestCoordinatorPersistsInputAndOutputExactlyOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "ping"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-1", "ping")
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != "ping" || job.Invocation.Command != "ping" {
		t.Fatalf("invocation = %#v", job.Invocation)
	}
	coordinator.execute(context.Background(), job)

	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("job = %#v", saved)
	}
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox records = %#v", records)
	}
	got := records[0].Message
	if got.Content.Text != "pong" || got.DedupeKey != "conversation-job:1:revision:1:part:0" || got.ReplyTo == nil || got.ReplyTo.EventID != "event-1" {
		t.Fatalf("outbound = %#v", got)
	}
}

func TestCoordinatorFiltersAmbientGroupTextBeforePersistence(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			t.Fatal("ambient group text reached command execution")
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(context.Context, agent.Input) (commands.Response, bool) {
			t.Fatal("ambient group text reached Agent execution")
			return commands.Response{}, false
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"今天校车好挤", "大家周六坐校车去聚餐", "周六校车还有调整通知"} {
		if err := coordinator.Enqueue(t.Context(), groupJobInbound(fmt.Sprintf("ambient-%d", i), "42", text)); err != nil {
			t.Fatal(err)
		}
	}
	job, err := db.ClaimNextConversationJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("ambient group job persisted: %#v", job)
	}

	if err := coordinator.Enqueue(t.Context(), groupJobInbound("public-bus", "42", "校车 西区 高新区")); err != nil {
		t.Fatal(err)
	}
	claimed := claimOnlyConversationJob(t, db)
	if claimed.Invocation.Name != string(commands.CapabilityBus) || claimed.Invocation.Command != "bus 西区 高新区" {
		t.Fatalf("public group invocation = %#v", claimed.Invocation)
	}
}

func TestCoordinatorResolvesAcceptedBotReplyIntoBusFollowUp(t *testing.T) {
	db := newCoordinatorStore(t)
	ctx := t.Context()
	conversation := message.Conversation{Platform: "napcat", Type: "group", ID: "100"}
	_, created, err := db.Enqueue(ctx, message.Outbound{
		Kind:   "bus",
		Target: conversation,
		Context: &message.ResponseContext{
			Capability: string(commands.CapabilityBus),
			Arguments:  []string{"周六", "西区", "高新区"},
		},
		Content:   message.Content{Text: "周六校车"},
		DedupeKey: "reply-context-source",
	})
	if err != nil || !created {
		t.Fatalf("enqueue source output: created=%v err=%v", created, err)
	}
	due, err := db.ClaimDue(ctx, time.Now().UTC(), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim source output: records=%#v err=%v", due, err)
	}
	if err := db.Complete(ctx, due[0].ID, delivery.Outcome{
		State: delivery.OutcomeAccepted,
		Receipt: message.Receipt{
			PlatformMessageID: "bot-message-1",
			AcceptedAt:        time.Now().UTC(),
		},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db, Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}), Outputs: db, Replies: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := groupJobInbound("follow-up", "77", "周日呢")
	inbound.ReplyTo = &message.ReplyRef{MessageID: "bot-message-1"}
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != string(commands.CapabilityBus) || job.Invocation.Command != "bus 西区 高新区 周日" {
		t.Fatalf("follow-up invocation = %#v", job.Invocation)
	}
}

func TestCoordinatorTreatsReplyToAcceptedAgentOutputAsAddressed(t *testing.T) {
	db := newCoordinatorStore(t)
	ctx := t.Context()
	conversation := message.Conversation{Platform: "napcat", Type: "group", ID: "100"}
	_, created, err := db.Enqueue(ctx, message.Outbound{
		Kind:      "agent",
		Target:    conversation,
		Content:   message.Content{Text: "公开信息说明"},
		DedupeKey: "agent-reply-source",
	})
	if err != nil || !created {
		t.Fatalf("enqueue source output: created=%v err=%v", created, err)
	}
	due, err := db.ClaimDue(ctx, time.Now().UTC(), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim source output: records=%#v err=%v", due, err)
	}
	if err := db.Complete(ctx, due[0].ID, delivery.Outcome{
		State: delivery.OutcomeAccepted,
		Receipt: message.Receipt{
			PlatformMessageID: "bot-agent-message-1",
			AcceptedAt:        time.Now().UTC(),
		},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db, Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}), Outputs: db, Replies: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := groupJobInbound("agent-follow-up", "77", "这个结果是什么意思")
	inbound.ReplyTo = &message.ReplyRef{MessageID: "bot-agent-message-1"}
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	var payload conversationJobPayload
	if err := json.Unmarshal(job.Input.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Route != routing.ActionAgent || payload.Activation != routing.ActivationReply {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestCoordinatorConfirmationResumesCheckpointedOperationOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	mutations := 0
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentResultFunc(func(ctx context.Context, input agent.Input) agent.Result {
			executions, err := db.CapabilityExecutionsForJob(ctx, input.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if len(executions) == 0 {
				_, _, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
					Identity: input.Identity, JobID: input.JobID, DedupeKey: "notify-confirm", ToolCallID: "call-notify",
					Capability: "notify", Arguments: []string{"homework", "on"}, Effect: "write",
					Receipt:              store.CapabilityReceipt{Action: "执行", Resource: "操作", Subject: "开启作业通知"},
					RequiresConfirmation: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				return agent.Result{Handled: true, State: agent.RunStateInterrupted}
			}
			execution, execute, err := db.ClaimCapabilityExecution(ctx, executions[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if execute {
				mutations++
				execution, err = db.FinishCapabilityExecution(ctx, execution.ID, "已开启", nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			return agent.Result{Response: commands.Response{Text: execution.Result, Kind: "agent"}, Handled: true, State: agent.RunStateCompleted}
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("event-confirm", "帮我开启作业通知")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation {
		t.Fatalf("waiting job = %#v", saved)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("event-ok", "ok")); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("event-ok-duplicate", "ok")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	if mutations != 1 {
		t.Fatalf("mutation executions = %d", mutations)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 ||
		!strings.Contains(records[0].Message.Content.Text, "#待确认执行操作{开启作业通知}") ||
		!strings.Contains(records[1].Message.Content.Text, "#已执行操作{开启作业通知}") {
		t.Fatalf("outbox records = %#v", records)
	}
}
func TestCoordinatorPersistsTextFallbackBeforeCompletingJob(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{
				Text: "fallback", Kind: "bus",
				Image: responses.NewTextImage("bus", "校车", "fallback"),
			}, true
		}),
		Outputs: db,
		Renderer: rendererFunc(func(*responses.Image) ([]byte, int, int, error) {
			return nil, 0, 0, errors.New("render failed")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-2", "校车")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content.Text != "fallback\n\n#已查询校车{全部}" || records[0].Message.Content.Attachment != nil {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorLeaseRetryReusesOutputRevision(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "ping"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-retry-dedupe", "ping")
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if _, err := coordinator.enqueueResponse(context.Background(), job, inbound, commands.Response{Text: "pong", Kind: "ping"}, 0); err != nil {
		t.Fatal(err)
	}
	recoveryAt := time.Now().UTC().Add(3 * time.Minute)
	if err := db.RecoverConversationJobLeases(context.Background(), recoveryAt, time.Minute); err != nil {
		t.Fatal(err)
	}
	retried, err := db.ClaimConversationJob(context.Background(), job.Identity, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if retried == nil || retried.Revision != job.Revision || retried.Attempts != 2 {
		t.Fatalf("retried job = %#v", retried)
	}
	coordinator.execute(context.Background(), *retried)
	records, err := db.ClaimDue(context.Background(), recoveryAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorOutputCommitFailureRetriesWithoutTerminalizingJob(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &outputCommitFaultStore{Store: db, failures: 1}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "pong", Kind: "ping"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("output-commit-retry", "ping")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateRetryWait || saved.LastError == "" {
		t.Fatalf("failed output commit terminalized job: %#v", saved)
	}
	if records, err := db.ClaimDue(ctx, time.Now().UTC(), 10); err != nil {
		t.Fatal(err)
	} else if len(records) != 0 {
		t.Fatalf("rolled-back output records = %#v", records)
	}

	retried := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, retried)
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("retried job = %#v", saved)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || records[0].Message.Content.Text != "pong" {
		t.Fatalf("retried output records=%#v err=%v", records, err)
	}
}

func TestCoordinatorOutputCommitFailureLeavesConfirmationResumable(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &outputCommitFaultStore{Store: db, failures: 1}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "已执行", Kind: "settings"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inbound := jobInbound("confirmation-output-retry", "通知 作业 开")
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateRetryWait {
		t.Fatalf("confirmation output failure did not enter retry: %#v", saved)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionAwaitingConfirmation || executions[0].ReceiptState != "" {
		t.Fatalf("confirmation operation after rollback=%#v err=%v", executions, err)
	}

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation {
		t.Fatalf("confirmation was not resumed: %#v", saved)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("confirmation-output-retry-ok", "ok")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("approved confirmation job = %#v", saved)
	}
	if executions, err = db.CapabilityExecutionsForJob(ctx, job.ID); err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("confirmation operation was not executed once: %#v err=%v", executions, err)
	}
}

func TestCoordinatorReceiptLoadingFailureRetriesAwaitingConfirmation(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &receiptReadFaultStore{Store: db, failures: 1}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "已执行", Kind: "settings"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("receipt-read-retry", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateRetryWait || saved.LastError == "" {
		t.Fatalf("receipt loading failure terminalized job: %#v", saved)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionAwaitingConfirmation || executions[0].ReceiptState != "" {
		t.Fatalf("awaiting confirmation after receipt read failure=%#v err=%v", executions, err)
	}

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation {
		t.Fatalf("awaiting confirmation was not resumed: job=%#v err=%v", saved, err)
	}
	if err := coordinator.Enqueue(ctx, jobInbound("receipt-read-retry-ok", "ok")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("approved confirmation did not complete: job=%#v err=%v", saved, err)
	}
}

func TestCoordinatorRecoversRunningLeaseDuringLiveRun(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &periodicRecoveryStore{Store: db}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "recovered", Kind: "ping"}, true
		}),
		Outputs: db, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Enqueue(ctx, jobInbound("periodic-recovery", "ping")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	go coordinator.Run(ctx)

	var saved *store.ConversationJob
	for ctx.Err() == nil {
		saved, err = db.GetConversationJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved != nil && saved.State == store.ConversationJobStateCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("periodic recovery did not requeue stuck job: %#v", saved)
	}
	if calls := jobs.recoveryCalls(); calls < 2 {
		t.Fatalf("recovery only ran at startup: calls=%d", calls)
	}
}

func TestCoordinatorHostOnlyResponseIsQueuedOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.SendResponse(ctx, input.Identity, commands.Response{Text: "private-link", Kind: "subscription"}); err != nil {
				t.Fatal(err)
			}
			return commands.Response{}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-3", "给我订阅链接")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content.Text != "private-link" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorSendsOneProgressMessageOnlyWhenAgentIsSlow(t *testing.T) {
	for _, test := range []struct {
		name      string
		delay     time.Duration
		wantTexts []string
		wantKeys  []string
	}{
		{name: "slow", delay: 30 * time.Millisecond, wantTexts: []string{"稍等一下", "最终回复"}, wantKeys: []string{"conversation-job:1:progress", "conversation-job:1:revision:1:part:0"}},
		{name: "fast", delay: 0, wantTexts: []string{"最终回复"}, wantKeys: []string{"conversation-job:1:revision:1:part:0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newCoordinatorStore(t)
			coordinator, err := NewCoordinator(CoordinatorConfig{
				Jobs: db,
				Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
					return commands.Response{}, false
				}),
				Agent: agentFunc(func(context.Context, agent.Input) (commands.Response, bool) {
					if test.delay > 0 {
						time.Sleep(test.delay)
					}
					return commands.Response{Text: "最终回复", Kind: "agent"}, true
				}),
				Outputs: db, ProgressDelay: 5 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Enqueue(t.Context(), jobInbound("progress-"+test.name, "请仔细想想这个问题")); err != nil {
				t.Fatal(err)
			}
			coordinator.execute(t.Context(), claimOnlyConversationJob(t, db))
			records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10)
			if err != nil {
				t.Fatal(err)
			}
			texts := make([]string, 0, len(records))
			keys := make([]string, 0, len(records))
			for _, record := range records {
				texts = append(texts, record.Message.Content.Text)
				keys = append(keys, record.Message.DedupeKey)
			}
			if fmt.Sprint(texts) != fmt.Sprint(test.wantTexts) {
				t.Fatalf("outbound texts=%#v want=%#v", texts, test.wantTexts)
			}
			if fmt.Sprint(keys) != fmt.Sprint(test.wantKeys) {
				t.Fatalf("outbound keys=%#v want=%#v", keys, test.wantKeys)
			}
		})
	}
}

func TestCoordinatorNaturalCalendarLinkRequestDeliversUsablePrivateURL(t *testing.T) {
	ctx := context.Background()
	db := newCoordinatorStore(t)
	const calendarURL = "https://calendar.example/private-token.ics"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"` + calendarURL + `"}}`))
	}))
	defer server.Close()

	identity := identityForInbound(jobInbound("identity", "ignored"))
	if err := db.SaveCredential(ctx, identity, store.Credential{
		ClientID: "client", AccessToken: "access", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour), Resource: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	handler := commands.Handler{
		Life:  life.NewClient(server.URL, server.Client()),
		Auth:  &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: db},
		Store: db,
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-natural-calendar-link", "能再给我发一下日历的链接吗")
	if err := coordinator.Enqueue(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != string(commands.CapabilitySubscription) || job.Invocation.Command != "subscription link" {
		t.Fatalf("invocation = %#v", job.Invocation)
	}
	coordinator.execute(ctx, job)

	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("outbox records = %#v", records)
	}
	reply := records[0].Message.Content.Text
	for _, want := range []string{calendarURL, "使用方法：复制链接", "通过 URL 添加/订阅日历", "iCalendar", "自动更新"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(strings.ToLower(reply), "caldav") {
		t.Fatalf("reply contains obsolete CalDAV wording: %q", reply)
	}
}

func TestCoordinatorHostCapabilityLoginWaitsOnAgentJob(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(ctx context.Context, input agent.Input) (commands.Response, bool) {
			if err := input.SendResponse(ctx, input.Identity, commands.Response{Text: "请登录", Kind: commands.ResponseKindAuthWait}); err != nil {
				t.Fatal(err)
			}
			return commands.Response{}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-agent-auth", "给我订阅链接")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(context.Background(), job)
	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingAuth {
		t.Fatalf("agent auth job = %#v", saved)
	}
}

func TestCoordinatorLoginWaitsOnSameJob(t *testing.T) {
	db := newCoordinatorStore(t)
	authorized := false
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			if authorized {
				return commands.Response{Text: "查询完成", Kind: "schedule"}, true
			}
			return commands.Response{Text: "请登录", Kind: commands.ResponseKindAuthWait}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("event-4", "查询课表")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(context.Background(), job)
	saved, err := db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingAuth || saved.WaitReason != store.ConversationJobWaitReasonAuth {
		t.Fatalf("job = %#v", saved)
	}
	authorized = true
	if err := db.UnblockConversationJobsAfterAuth(context.Background(), saved.Identity); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(context.Background(), claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted || saved.Revision != 2 || saved.Attempts != 2 {
		t.Fatalf("resumed job = %#v", saved)
	}
	records, err := db.ClaimDue(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Message.Content.Text != "请登录" || records[1].Message.Content.Text != "查询完成\n\n#已查询课表{全部}" {
		t.Fatalf("resumed outbox = %#v", records)
	}
	if records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" || records[1].Message.DedupeKey != "conversation-job:1:revision:2:part:0" {
		t.Fatalf("resumed dedupe keys = %q, %q", records[0].Message.DedupeKey, records[1].Message.DedupeKey)
	}
}

func TestCoordinatorConfirmsDirectMutationBeforeExecutionAndExcludesMechanicsFromHistory(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := commands.Handler{Store: db}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("notify-confirm", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)

	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateWaitingConfirmation {
		t.Fatalf("waiting job = %#v", saved)
	}
	settings, err := db.NotificationSettings(ctx, job.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if settings.HomeworkEnabled {
		t.Fatal("mutation executed before confirmation")
	}
	initial, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial output: records=%#v err=%v", initial, err)
	}
	if got := initial[0].Message.Content.Text; !strings.Contains(got, confirmationPrompt) || !strings.Contains(got, "#待确认设置提醒{作业：开}") {
		t.Fatalf("confirmation output = %q", got)
	}

	if err := coordinator.Enqueue(ctx, jobInbound("notify-approve", "ok")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("completed job = %#v", saved)
	}
	settings, err = db.NotificationSettings(ctx, job.Identity)
	if err != nil || !settings.HomeworkEnabled {
		t.Fatalf("settings after approval = %#v err=%v", settings, err)
	}
	terminal, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(terminal) != 1 {
		t.Fatalf("terminal output: records=%#v err=%v", terminal, err)
	}
	if got := terminal[0].Message.Content.Text; !strings.Contains(got, "作业提醒：开") || !strings.Contains(got, "#已设置提醒{作业：开}") {
		t.Fatalf("terminal output = %q", got)
	}
	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != store.ConversationEventUser || events[0].Content != "通知 作业 开" || events[1].Type != store.ConversationEventAssistant {
		t.Fatalf("conversation events = %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Content, "ok") || strings.Contains(event.Content, "待确认") || strings.Contains(event.Content, "#已") {
			t.Fatalf("host mechanic leaked into history: %#v", event)
		}
	}
}

func TestCoordinatorConfirmsGroupedCourseMutationsOneAtATimeWithFrozenDetails(t *testing.T) {
	sections := map[string]string{
		"CODE1.01": `{"code":"CODE1.01","jwId":11,"course":{"namePrimary":"线性代数"},"teacher":{"namePrimary":"张老师"},"semester":{"namePrimary":"2026年秋季学期"}}`,
		"CODE2.02": `{"code":"CODE2.02","jwId":22,"course":{"namePrimary":"离散数学"},"teacher":{"namePrimary":"李老师"},"semester":{"namePrimary":"2026年秋季学期"}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/sections" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		section, ok := sections[r.URL.Query().Get("search")]
		if !ok {
			t.Fatalf("unexpected query %q", r.URL.Query().Get("search"))
		}
		_, _ = fmt.Fprintf(w, `{"data":[%s]}`, section)
	}))
	defer server.Close()

	db := newCoordinatorStore(t)
	handler := commands.Handler{Store: db, Life: life.NewClient(server.URL, server.Client())}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("course-group", "subscription import CODE1.01 CODE2.02")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(ctx, job)
	first, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first confirmation: records=%#v err=%v", first, err)
	}
	firstText := first[0].Message.Content.Text
	if !strings.Contains(firstText, "#待确认订阅课程{线性代数（张老师，2026年秋季学期）}") || strings.Contains(firstText, "离散数学") {
		t.Fatalf("first confirmation = %q", firstText)
	}

	if err := coordinator.Enqueue(ctx, jobInbound("course-deny-1", "取消")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	second, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("second confirmation: records=%#v err=%v", second, err)
	}
	secondText := second[0].Message.Content.Text
	for _, want := range []string{
		"#订阅课程失败{线性代数（张老师，2026年秋季学期）：用户拒绝执行}",
		"#待确认订阅课程{离散数学（李老师，2026年秋季学期）}",
	} {
		if !strings.Contains(secondText, want) {
			t.Fatalf("second confirmation missing %q: %q", want, secondText)
		}
	}

	if err := coordinator.Enqueue(ctx, jobInbound("course-deny-2", "取消")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	terminal, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(terminal) != 1 {
		t.Fatalf("terminal receipt: records=%#v err=%v", terminal, err)
	}
	if got := terminal[0].Message.Content.Text; !strings.Contains(got, "#订阅课程失败{离散数学（李老师，2026年秋季学期）：用户拒绝执行}") {
		t.Fatalf("terminal receipt = %q", got)
	}
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted || saved.Attempts != 3 || saved.MaxAttempts != 0 {
		t.Fatalf("grouped job = %#v", saved)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 || executions[0].State != store.CapabilityExecutionDenied || executions[1].State != store.CapabilityExecutionDenied {
		t.Fatalf("grouped executions = %#v err=%v", executions, err)
	}
	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != store.ConversationEventUser || events[1].Content != "用户拒绝执行该操作。" || events[2].Content != "用户拒绝执行该操作。" {
		t.Fatalf("denial history = %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Content, "取消") || strings.Contains(event.Content, "#待确认") || strings.Contains(event.Content, "#订阅") {
			t.Fatalf("confirmation mechanic leaked into history: %#v", event)
		}
	}
}
