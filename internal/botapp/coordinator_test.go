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

func describeTestInvocation(_ context.Context, _ commands.Input, id commands.CapabilityID, args []string) (commands.CapabilityInvocationDescription, error) {
	invocation, ok := commands.NewInvocation(id, args)
	if !ok {
		return commands.CapabilityInvocationDescription{}, fmt.Errorf("invalid test invocation %q", id)
	}
	receipt := commands.ReceiptForInvocation(invocation)
	var receiptPtr *store.CapabilityReceipt
	if receipt.Action != "" && receipt.Resource != "" && receipt.Subject != "" {
		receiptPtr = &receipt
	}
	return commands.CapabilityInvocationDescription{Invocation: invocation, Receipt: receiptPtr}, nil
}

func (fn commandFunc) DescribeInvocation(ctx context.Context, input commands.Input, id commands.CapabilityID, args []string) (commands.CapabilityInvocationDescription, error) {
	return describeTestInvocation(ctx, input, id, args)
}

type agentFunc func(context.Context, agent.Input) (commands.Response, bool)

func (fn agentFunc) Run(ctx context.Context, input agent.Input) agent.Result {
	response, handled := fn(ctx, input)
	return agent.Result{Response: response, Handled: handled, State: agent.RunStateCompleted}
}

func (fn agentFunc) Acknowledge(context.Context, int64, int, string) error { return nil }

type agentResultFunc func(context.Context, agent.Input) agent.Result

func (fn agentResultFunc) Run(ctx context.Context, input agent.Input) agent.Result {
	return fn(ctx, input)
}

func (fn agentResultFunc) Acknowledge(context.Context, int64, int, string) error { return nil }

type rendererFunc func(*responses.Image) ([]byte, int, int, error)

func (fn rendererFunc) RenderPNG(image *responses.Image) ([]byte, int, int, error) {
	return fn(image)
}

func (fn rendererFunc) RenderPNGContext(_ context.Context, image *responses.Image) ([]byte, int, int, error) {
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

type capabilityReadFaultStore struct {
	*store.Store
	mu     sync.Mutex
	calls  int
	failOn int
}

type capabilityWriteFaultStore struct {
	*store.Store
	mu        sync.Mutex
	operation string
	failures  int
}

func (s *capabilityWriteFaultStore) take(operation string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation != operation || s.failures <= 0 {
		return false
	}
	s.failures--
	return true
}

func (s *capabilityWriteFaultStore) DeferCapabilityExecutionForAuth(ctx context.Context, id, lease string) (store.CapabilityExecution, error) {
	if s.take("auth") {
		return store.CapabilityExecution{}, errors.New("injected auth deferral persistence failure")
	}
	return s.Store.DeferCapabilityExecutionForAuth(ctx, id, lease)
}

func (s *capabilityWriteFaultStore) FinishCapabilityExecution(ctx context.Context, id, lease, result string, outcome error) (store.CapabilityExecution, error) {
	if s.take("finish") {
		return store.CapabilityExecution{}, errors.New("injected capability finalization persistence failure")
	}
	return s.Store.FinishCapabilityExecution(ctx, id, lease, result, outcome)
}

type fixedOutcomeCommand struct {
	outcome       commands.CapabilityOutcome
	outcomes      []commands.CapabilityOutcome
	describeErr   error
	calls         int
	id            commands.CapabilityID
	args          []string
	allArgs       [][]string
	describedArgs [][]string
	described     []commands.Invocation
}

func (h *fixedOutcomeCommand) ExecuteCapability(_ context.Context, _ commands.Input, id commands.CapabilityID, args []string) (commands.CapabilityOutcome, error) {
	h.calls++
	h.id = id
	h.args = append([]string(nil), args...)
	h.allArgs = append(h.allArgs, append([]string(nil), args...))
	if index := h.calls - 1; index >= 0 && index < len(h.outcomes) {
		return h.outcomes[index], nil
	}
	return h.outcome, nil
}

func (h *fixedOutcomeCommand) DescribeInvocation(ctx context.Context, input commands.Input, id commands.CapabilityID, args []string) (commands.CapabilityInvocationDescription, error) {
	if h.describeErr != nil {
		return commands.CapabilityInvocationDescription{}, h.describeErr
	}
	description, err := describeTestInvocation(ctx, input, id, args)
	if err == nil {
		h.describedArgs = append(h.describedArgs, append([]string(nil), args...))
		h.described = append(h.described, description.Invocation)
	}
	return description, err
}

func (s *capabilityReadFaultStore) CapabilityExecutionsForJob(ctx context.Context, jobID int64) ([]store.CapabilityExecution, error) {
	s.mu.Lock()
	s.calls++
	inject := s.calls == s.failOn
	s.mu.Unlock()
	if inject {
		return nil, errors.New("injected capability loading failure")
	}
	return s.Store.CapabilityExecutionsForJob(ctx, jobID)
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

func acceptCoordinatorOutputs(t *testing.T, db *store.Store, records []delivery.Record) {
	t.Helper()
	for _, record := range records {
		if err := db.Complete(context.Background(), record.ID, delivery.Outcome{
			State:   delivery.OutcomeAccepted,
			Receipt: message.Receipt{AcceptedAt: time.Now().UTC()},
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordinatorPersistsInputAndOutputExactlyOnce(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "帮助结果", Kind: "help"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("event-1", "help")
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	if job.Invocation.Name != "help" || job.Invocation.Command != "help" {
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
	if got.Content.TextContent() != "帮助结果" || got.DedupeKey != "conversation-job:1:revision:1:part:0" || got.ReplyTo == nil || got.ReplyTo.EventID != "event-1" {
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
		Content:   message.Content{Parts: []message.ContentPart{{Text: "周六校车"}}},
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
		Content:   message.Content{Parts: []message.ContentPart{{Text: "公开信息说明"}}},
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

func TestCoordinatorRoutesConfirmationWordNormallyWhenNothingIsPending(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("standalone-confirmation", "确定")
	if err := coordinator.Enqueue(t.Context(), inbound); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	var payload conversationJobPayload
	if err := json.Unmarshal(job.Input.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Route != routing.ActionAgent || payload.Inbound.Text != "确定" {
		t.Fatalf("standalone confirmation payload = %#v", payload)
	}
}

func TestCoordinatorRejectsAgentRouteWithPersistedWriteInvocation(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Text: "已开启", Kind: "notify"})}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db, Commands: handler,
		Agent: agentFunc(func(context.Context, agent.Input) (commands.Response, bool) {
			t.Fatal("mismatched Agent route reached the model")
			return commands.Response{}, false
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbound := jobInbound("agent-route-write-invocation", "帮我开启作业提醒")
	payload, err := json.Marshal(conversationJobPayload{Inbound: inbound, Route: routing.ActionAgent})
	if err != nil {
		t.Fatal(err)
	}
	job, created, err := db.EnqueueConversationJob(t.Context(), store.ConversationJobEnqueue{
		Identity: identityForInbound(inbound), SourceEventID: inbound.Source.EventID,
		Input: store.ConversationJobInput{Text: inbound.Text, Data: payload},
		Invocation: store.ConversationJobInvocation{
			Name: string(commands.CapabilityNotify), Command: "notify 作业 开", Args: []string{"作业", "开"},
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("enqueue mismatched job=%#v created=%v err=%v", job, created, err)
	}
	coordinator.execute(t.Context(), claimOnlyConversationJob(t, db))

	saved, err := db.GetConversationJob(t.Context(), job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateFailed ||
		!strings.Contains(saved.LastError, "Agent route unexpectedly contains") {
		t.Fatalf("mismatched job=%#v err=%v", saved, err)
	}
	if handler.calls != 0 {
		t.Fatalf("mismatched write executed %d times", handler.calls)
	}
	executions, err := db.CapabilityExecutionsForJob(t.Context(), job.ID)
	if err != nil || len(executions) != 0 {
		t.Fatalf("mismatched route executions=%#v err=%v", executions, err)
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
			claimedJob, err := db.GetConversationJob(ctx, input.JobID)
			if err != nil || claimedJob == nil {
				t.Fatalf("read claimed job: job=%#v err=%v", claimedJob, err)
			}
			execution, execute, err := db.ClaimCapabilityExecutionForJob(ctx, executions[0].ID, claimedJob.ID, claimedJob.LeaseToken)
			if err != nil {
				t.Fatal(err)
			}
			if execute {
				mutations++
				execution, err = db.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "已开启", nil)
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
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), confirmationPrompt) {
		t.Fatalf("confirmation output = %#v err=%v", records, err)
	}
	acceptCoordinatorOutputs(t, db, records)
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
	records, err = db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !strings.Contains(records[0].Message.Content.TextContent(), "#通知 homework on（已完成）") {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorDoesNotWaitWhenInterruptedRunHasNoPendingConfirmation(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentResultFunc(func(ctx context.Context, input agent.Input) agent.Result {
			execution, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
				Identity: input.Identity, JobID: input.JobID, LeaseToken: input.JobLeaseToken,
				DedupeKey: "terminal-without-confirmation", ToolCallID: "old-tool-call",
				Capability: "notify", Arguments: []string{"homework", "on"}, Effect: "write",
				Receipt: store.CapabilityReceipt{Action: "设置", Resource: "提醒", Subject: "作业：开"},
			})
			if err != nil || !created {
				t.Fatalf("prepare terminal operation: execution=%#v created=%v err=%v", execution, created, err)
			}
			execution, execute, err := db.ClaimCapabilityExecutionForJob(ctx, execution.ID, input.JobID, input.JobLeaseToken)
			if err != nil || !execute {
				t.Fatalf("claim terminal operation: execution=%#v execute=%v err=%v", execution, execute, err)
			}
			if _, err := db.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "设置提醒失败：测试失败", errors.New("test failure")); err != nil {
				t.Fatal(err)
			}
			return agent.Result{Handled: true, State: agent.RunStateInterrupted}
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("terminal-interrupt", "帮我开启作业通知")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)

	saved, err := db.GetConversationJob(t.Context(), job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("terminal interrupted job=%#v err=%v", saved, err)
	}
	records, err := db.ClaimDue(t.Context(), time.Now().UTC(), 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("terminal interrupted output=%#v err=%v", records, err)
	}
	text := records[0].Message.Content.TextContent()
	if strings.Contains(text, confirmationPrompt) || strings.Contains(text, "#待确认") {
		t.Fatalf("phantom confirmation was sent: %q", text)
	}
	if !strings.Contains(text, "没有可确认的待处理操作") || !strings.Contains(text, "#通知 homework on（失败）") {
		t.Fatalf("terminal interrupted explanation=%q", text)
	}
}

func TestCoordinatorRetriesInterruptedRunWithNonterminalExecution(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentResultFunc(func(ctx context.Context, input agent.Input) agent.Result {
			_, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
				Identity: input.Identity, JobID: input.JobID, LeaseToken: input.JobLeaseToken,
				DedupeKey: "approved-without-confirmation-interrupt", ToolCallID: "tool-call",
				Capability: "notify", Arguments: []string{"homework", "on"}, Effect: "write",
			})
			if err != nil || !created {
				t.Fatalf("prepare nonterminal operation: created=%v err=%v", created, err)
			}
			return agent.Result{Handled: true, State: agent.RunStateInterrupted}
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("nonterminal-interrupt", "帮我开启作业通知")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)

	saved, err := db.GetConversationJob(t.Context(), job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait ||
		!strings.Contains(saved.LastError, "remained nonterminal") {
		t.Fatalf("nonterminal interrupted job=%#v err=%v", saved, err)
	}
	if records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10); err != nil || len(records) != 0 {
		t.Fatalf("nonterminal interrupt produced output=%#v err=%v", records, err)
	}
}

func TestBusRenderFailureHasNoTextFallbackOrBusinessReexecution(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Kind: "bus", Image: responses.NewTextImage("bus", "校车", "仅图卡内容"), Data: map[string]any{"buses": []string{"08:00"}}})}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("bus-render-failure", "校车")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)
	assertCompletedDirectJob(t, db, job.ID)
	records, err := db.ClaimDue(t.Context(), time.Now(), 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("durable image intent missing: len=%d err=%v", len(records), err)
	}
	svc, err := delivery.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRenderer(rendererFunc(func(*responses.Image) ([]byte, int, int, error) { return nil, 0, 0, errors.New("render failed") })); err != nil {
		t.Fatal(err)
	}
	if outcome := svc.DeliverNow(t.Context(), records[0].Message); outcome.State != delivery.OutcomeRetryable || outcome.Code != "render_failed" {
		t.Fatalf("render failure = %#v", outcome)
	}
	assertCompletedDirectJob(t, db, job.ID)
	if handler.calls != 1 {
		t.Fatalf("business query executed %d times", handler.calls)
	}
	if len(records[0].Message.Content.Parts) != 1 || records[0].Message.Content.Parts[0].Attachment == nil || len(records[0].Message.Content.Parts[0].Attachment.RenderPayload) == 0 || records[0].Message.Content.Parts[0].Text != "" {
		t.Fatal("expected durable image-only output")
	}
	events, err := db.RecentConversationEvents(t.Context(), job.Identity, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	var result struct{ Result struct{ Buses []string } }
	if err := json.Unmarshal([]byte(events[1].Content), &result); err != nil || len(result.Result.Buses) != 1 || result.Result.Buses[0] != "08:00" {
		t.Fatalf("bus JSON=%s err=%v", events[1].Content, err)
	}
}

func TestCoordinatorDirectRenderedImageHasNoExecutionReceipt(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{
				Kind:  "bus",
				Image: responses.NewTextImage("bus", "校车", "校车查询结果"),
			}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("rendered-bus-receipt", "校车")); err != nil {
		t.Fatal(err)
	}
	coordinator.execute(t.Context(), claimOnlyConversationJob(t, db))
	records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Message.Content.Parts) != 1 || records[0].Message.Content.Parts[0].Attachment == nil || records[0].Message.Content.Parts[0].Text != "" {
		t.Fatalf("direct image output=%#v", records)
	}
}

func TestCoordinatorInvalidCommandReturnsExactUsageWithoutExecutionReceipt(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db, Commands: commands.Handler{}, Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("invalid-schedule-week", "帮我查询 someday 的课表")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)
	records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 ||
		!strings.Contains(records[0].Message.Content.TextContent(), "课表的参数无法识别") ||
		!strings.Contains(records[0].Message.Content.TextContent(), "课表 第3周") ||
		strings.Contains(records[0].Message.Content.TextContent(), "#查询") {
		t.Fatalf("invalid command output=%#v", records)
	}
	executions, err := db.CapabilityExecutionsForJob(t.Context(), job.ID)
	if err != nil || len(executions) != 0 {
		t.Fatalf("invalid command executions=%#v err=%v", executions, err)
	}
}

func TestCoordinatorOutputCommitFailureRetriesWithoutTerminalizingJob(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &outputCommitFaultStore{Store: db, failures: 1}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "帮助结果", Kind: "help"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Enqueue(ctx, jobInbound("output-commit-retry", "help")); err != nil {
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
	if err != nil || len(records) != 1 || records[0].Message.Content.TextContent() != "帮助结果" {
		t.Fatalf("retried output records=%#v err=%v", records, err)
	}
}

func TestCoordinatorOutputCommitFailureDoesNotReplayDirectMutation(t *testing.T) {
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
	inbound := jobInbound("direct-output-retry", "通知 作业 开")
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
		t.Fatalf("direct output failure did not enter retry: %#v", saved)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("direct operation after output rollback=%#v err=%v", executions, err)
	}

	coordinator.execute(ctx, claimOnlyConversationJob(t, db))
	saved, err = db.GetConversationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("retried direct job = %#v", saved)
	}
	if executions, err = db.CapabilityExecutionsForJob(ctx, job.ID); err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("direct operation was not retained exactly once: %#v err=%v", executions, err)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || records[0].Message.Content.TextContent() != "已执行" {
		t.Fatalf("retried direct output=%#v err=%v", records, err)
	}
}

func TestCoordinatorCapabilityLoadingFailureRetriesWithoutLosingOperationState(t *testing.T) {
	for _, test := range []struct {
		name                  string
		failOn                int
		wantExecutionsOnRetry int
	}{
		{name: "route read", failOn: 1, wantExecutionsOnRetry: 0},
		{name: "final assembly read", failOn: 2, wantExecutionsOnRetry: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newCoordinatorStore(t)
			jobs := &capabilityReadFaultStore{Store: db, failOn: test.failOn}
			coordinator, err := NewCoordinator(CoordinatorConfig{
				Jobs: jobs,
				Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
					return commands.Response{Text: "帮助结果", Kind: "help"}, true
				}),
				Outputs: db,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if err := coordinator.Enqueue(ctx, jobInbound("capability-read-"+test.name, "help")); err != nil {
				t.Fatal(err)
			}
			job := claimOnlyConversationJob(t, db)
			coordinator.execute(ctx, job)
			saved, err := db.GetConversationJob(ctx, job.ID)
			if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait {
				t.Fatalf("read failure terminalized job: job=%#v err=%v", saved, err)
			}
			executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(executions) != test.wantExecutionsOnRetry {
				t.Fatalf("operation state after read failure=%#v err=%v", executions, err)
			}
			if len(executions) == 1 && executions[0].State != store.CapabilityExecutionSucceeded {
				t.Fatalf("finished operation changed state: %#v", executions[0])
			}

			coordinator.execute(ctx, claimOnlyConversationJob(t, db))
			saved, err = db.GetConversationJob(ctx, job.ID)
			if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
				t.Fatalf("retry did not complete: job=%#v err=%v", saved, err)
			}
			executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
				t.Fatalf("operation was not recovered exactly once: %#v err=%v", executions, err)
			}
		})
	}
}

func TestCoordinatorRetriesCapabilityPersistenceBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		outcome   commands.CapabilityOutcome
		wantState store.ConversationJobState
		wantOp    store.CapabilityExecutionState
	}{
		{
			name: "authorization deferral", operation: "auth",
			outcome: commands.CapabilityOutcome{
				Status:   commands.CapabilityOutcomeAuthRequired,
				Response: commands.Response{Text: "请先登录", Kind: commands.ResponseKindAuthWait},
			},
			wantState: store.ConversationJobStateWaitingAuth, wantOp: store.CapabilityExecutionWaitingAuth,
		},
		{
			name: "execution finalization", operation: "finish",
			outcome: commands.CapabilityOutcome{
				Status:   commands.CapabilityOutcomeSuccess,
				Response: commands.Response{Text: "帮助结果", Kind: "help"},
			},
			wantState: store.ConversationJobStateCompleted, wantOp: store.CapabilityExecutionSucceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newCoordinatorStore(t)
			jobs := &capabilityWriteFaultStore{Store: db, operation: test.operation, failures: 1}
			handler := &fixedOutcomeCommand{outcome: test.outcome}
			coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: jobs, Commands: handler, Outputs: db})
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if err := coordinator.Enqueue(ctx, jobInbound("capability-write-"+test.operation, "help")); err != nil {
				t.Fatal(err)
			}
			job := claimOnlyConversationJob(t, db)
			coordinator.execute(ctx, job)
			saved, err := db.GetConversationJob(ctx, job.ID)
			if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait {
				t.Fatalf("persistence failure terminalized job: job=%#v err=%v", saved, err)
			}
			executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionRunning {
				t.Fatalf("operation did not remain resumable: executions=%#v err=%v", executions, err)
			}

			coordinator.execute(ctx, claimOnlyConversationJob(t, db))
			saved, err = db.GetConversationJob(ctx, job.ID)
			if err != nil || saved == nil || saved.State != test.wantState {
				t.Fatalf("retry state: job=%#v err=%v", saved, err)
			}
			executions, err = db.CapabilityExecutionsForJob(ctx, job.ID)
			if err != nil || len(executions) != 1 || executions[0].State != test.wantOp {
				t.Fatalf("retry operation state=%#v err=%v", executions, err)
			}
			if handler.calls != 2 {
				t.Fatalf("read capability calls=%d want=2", handler.calls)
			}
		})
	}
}

func TestCoordinatorRetriesAgentInfrastructureFailure(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentResultFunc(func(context.Context, agent.Input) agent.Result {
			return agent.Result{Handled: true, State: agent.RunStateFailed, Err: errors.New("agent run row unavailable")}
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("agent-infrastructure-retry", "帮我看看")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	coordinator.execute(t.Context(), job)
	saved, err := db.GetConversationJob(t.Context(), job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateRetryWait {
		t.Fatalf("agent infrastructure failure was not retryable: job=%#v err=%v", saved, err)
	}
	if records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10); err != nil || len(records) != 0 {
		t.Fatalf("agent infrastructure failure produced output=%#v err=%v", records, err)
	}
}

func TestCoordinatorRecoversRunningLeaseDuringLiveRun(t *testing.T) {
	db := newCoordinatorStore(t)
	jobs := &periodicRecoveryStore{Store: db}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: jobs,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "recovered", Kind: "help"}, true
		}),
		Outputs: db, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := coordinator.Enqueue(ctx, jobInbound("periodic-recovery", "help")); err != nil {
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
	if len(records) != 1 || records[0].Message.Content.TextContent() != "private-link" {
		t.Fatalf("outbox records = %#v", records)
	}
}

func TestCoordinatorSlowAgentEmitsOnlyFinalResponse(t *testing.T) {
	db := newCoordinatorStore(t)
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{}, false
		}),
		Agent: agentFunc(func(context.Context, agent.Input) (commands.Response, bool) {
			close(started)
			<-release
			return commands.Response{Text: "最终回复", Kind: "agent"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(t.Context(), jobInbound("slow-agent", "请仔细想想这个问题")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.execute(t.Context(), job)
	}()
	<-started
	if records, claimErr := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10); claimErr != nil {
		t.Fatal(claimErr)
	} else if len(records) != 0 {
		t.Fatalf("slow agent emitted interim output=%#v", records)
	}
	close(release)
	<-done
	records, err := db.ClaimDue(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content.TextContent() != "最终回复" || records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" {
		t.Fatalf("final output=%#v", records)
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
	reply := records[0].Message.Content.TextContent()
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
	if len(records) != 2 || records[0].Message.Content.TextContent() != "请登录" || records[1].Message.Content.TextContent() != "查询完成" {
		t.Fatalf("resumed outbox = %#v", records)
	}
	if records[0].Message.DedupeKey != "conversation-job:1:revision:1:part:0" || records[1].Message.DedupeKey != "conversation-job:1:revision:2:part:0" {
		t.Fatalf("resumed dedupe keys = %q, %q", records[0].Message.DedupeKey, records[1].Message.DedupeKey)
	}
}

func TestCoordinatorExecutesDirectMutationWithoutConfirmationOrReceipt(t *testing.T) {
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
	if saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("completed job = %#v", saved)
	}
	settings, err := db.NotificationSettings(ctx, job.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HomeworkEnabled {
		t.Fatal("direct mutation did not execute")
	}
	initial, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial output: records=%#v err=%v", initial, err)
	}
	if got := initial[0].Message.Content.TextContent(); !strings.Contains(got, "作业提醒：开") || strings.Contains(got, confirmationPrompt) || strings.Contains(got, "#") {
		t.Fatalf("direct output = %q", got)
	}
	events, err := db.RecentConversationEvents(ctx, job.Identity, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != store.ConversationEventUser || events[0].Content != "通知 作业 开" || events[1].Type != store.ConversationEventAssistant {
		t.Fatalf("conversation events = %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Content, "待确认") || strings.Contains(event.Content, "#已") {
			t.Fatalf("host mechanic leaked into history: %#v", event)
		}
	}
}

func TestCoordinatorResumesDirectMutationPreparedBeforeClaim(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := commands.Handler{Store: db}
	coordinator, err := NewCoordinator(CoordinatorConfig{Jobs: db, Commands: handler, Outputs: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := coordinator.Enqueue(ctx, jobInbound("notify-prepared", "通知 作业 开")); err != nil {
		t.Fatal(err)
	}
	job := claimOnlyConversationJob(t, db)
	execution, created, err := db.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: job.Identity, JobID: job.ID, LeaseToken: job.LeaseToken,
		DedupeKey:  fmt.Sprintf("conversation-job:%d:command:operation:0", job.ID),
		Capability: string(commands.CapabilityNotify), Arguments: []string{"作业", "开"},
		Effect: string(commands.EffectWrite),
	})
	if err != nil || !created || execution.State != store.CapabilityExecutionApproved {
		t.Fatalf("prepared direct operation=%#v created=%v err=%v", execution, created, err)
	}

	coordinator.execute(ctx, job)

	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("resumed direct job=%#v err=%v", saved, err)
	}
	settings, err := db.NotificationSettings(ctx, job.Identity)
	if err != nil || !settings.HomeworkEnabled {
		t.Fatalf("resumed direct mutation settings=%#v err=%v", settings, err)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 1 || executions[0].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("resumed direct execution=%#v err=%v", executions, err)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || strings.Contains(records[0].Message.Content.TextContent(), "#") {
		t.Fatalf("resumed direct output=%#v err=%v", records, err)
	}
}

func TestCoordinatorPassesGroupedDirectMutationThroughWithoutConfirmation(t *testing.T) {
	db := newCoordinatorStore(t)
	handler := &fixedOutcomeCommand{outcome: commands.SuccessOutcome(commands.Response{Text: "批量订阅完成", Kind: "subscription"})}
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
	if handler.calls != 2 || handler.id != commands.CapabilitySubscription || strings.Join(handler.args, " ") != "import CODE2.02" {
		t.Fatalf("direct invocation: calls=%d id=%s args=%#v", handler.calls, handler.id, handler.args)
	}
	saved, err := db.GetConversationJob(ctx, job.ID)
	if err != nil || saved == nil || saved.State != store.ConversationJobStateCompleted {
		t.Fatalf("grouped direct job=%#v err=%v", saved, err)
	}
	records, err := db.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil || len(records) != 1 || records[0].Message.Content.TextContent() != "批量订阅完成\n\n批量订阅完成" || strings.Contains(records[0].Message.Content.TextContent(), "#") {
		t.Fatalf("grouped direct output=%#v err=%v", records, err)
	}
	executions, err := db.CapabilityExecutionsForJob(ctx, job.ID)
	if err != nil || len(executions) != 2 || executions[0].State != store.CapabilityExecutionSucceeded || executions[1].State != store.CapabilityExecutionSucceeded {
		t.Fatalf("grouped direct execution=%#v err=%v", executions, err)
	}
}

// A persistence failure that can never succeed must reach a terminal state.
// RetryConversationJob sets retry_at to now, so an unbounded retry re-claims
// the job immediately and spins until the conversation expires.
func TestCoordinatorStopsRetryingAPermanentPersistenceFailure(t *testing.T) {
	db := newCoordinatorStore(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Jobs: db,
		Commands: commandFunc(func(context.Context, commands.Input) (commands.Response, bool) {
			return commands.Response{Text: "帮助结果", Kind: "help"}, true
		}),
		Outputs: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Enqueue(context.Background(), jobInbound("poison-1", "help")); err != nil {
		t.Fatal(err)
	}

	cause := markConversationPersistenceError(errors.New("agent run raw text is empty"))
	var jobID int64
	claims := 0
	for claims < conversationJobPersistenceRetryLimit+5 {
		job, err := db.ClaimNextConversationJob(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			break
		}
		jobID = job.ID
		claims++
		coordinator.fail(context.Background(), *job, cause)
	}
	if claims == 0 {
		t.Fatal("job was never claimed")
	}
	if claims > conversationJobPersistenceRetryLimit+1 {
		t.Fatalf("job was re-claimed %d times; the retry bound did not apply", claims)
	}
	saved, err := db.GetConversationJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.State != store.ConversationJobStateFailed {
		t.Fatalf("job = %#v, want state failed", saved)
	}
}
