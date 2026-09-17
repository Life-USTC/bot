package botapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/agent"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/routing"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const (
	defaultJobPollInterval     = 250 * time.Millisecond
	defaultJobRecoveryInterval = time.Second
	defaultJobBatchSize        = 4
)

type CommandHandler interface {
	ExecuteCapability(context.Context, commands.Input, commands.CapabilityID, []string) (commands.CapabilityOutcome, error)
	DescribeInvocation(context.Context, commands.Input, commands.CapabilityID, []string) (commands.CapabilityInvocationDescription, error)
}

type AgentHandler interface {
	Run(context.Context, agent.Input) agent.Result
	Acknowledge(context.Context, int64, int, string) error
}

type Recorder interface {
	RecordInteraction(context.Context, store.Identity, store.Interaction) error
}

type Processor interface {
	Process(context.Context, message.Inbound)
}

// JobRepository is the durable source of truth for conversation work. The
// coordinator deliberately depends on this small contract instead of the
// concrete SQLite store so job scheduling and execution can be tested apart.
type JobRepository interface {
	commands.CommandImageStore
	EnqueueConversationJob(context.Context, store.ConversationJobEnqueue) (store.ConversationJob, bool, error)
	ClaimConversationJobs(context.Context, time.Time, int) ([]store.ConversationJob, error)
	CompleteConversationJob(context.Context, int64, string) (bool, error)
	FailConversationJob(context.Context, int64, string, string) (bool, error)
	ResolveCapabilityConfirmation(context.Context, store.Identity, store.CapabilityConfirmationDecision, ...time.Time) (*store.CapabilityExecution, *store.ConversationJob, error)
	PrepareCapabilityExecution(context.Context, store.CapabilityExecutionPrepare) (store.CapabilityExecution, bool, error)
	PrepareCapabilityExecutions(context.Context, []store.CapabilityExecutionPrepare) ([]store.CapabilityExecution, bool, error)
	CapabilityExecutionsForJob(context.Context, int64) ([]store.CapabilityExecution, error)
	UnsentCapabilityExecutionsForJob(context.Context, int64) ([]store.CapabilityExecution, error)
	ClaimCapabilityExecutionForJob(context.Context, string, int64, string) (store.CapabilityExecution, bool, error)
	DeferCapabilityExecutionForAuth(context.Context, string, string) (store.CapabilityExecution, error)
	FinishCapabilityExecution(context.Context, string, string, string, error) (store.CapabilityExecution, error)
	FinishCapabilityExecutionUnknown(context.Context, string, string, string, string) (store.CapabilityExecution, error)
	MarkStaleCapabilityExecutionUnknown(context.Context, string, int64, string, string) (store.CapabilityExecution, bool, error)
	AppendConversationEvent(context.Context, store.ConversationEvent) (store.ConversationEvent, bool, error)
	CommitConversationJobOutput(context.Context, store.ConversationJobOutputCommit) ([]store.ConversationJobCommittedOutput, error)
	RetryConversationJob(context.Context, int64, string, string) (bool, error)
	RecoverConversationJobLeases(context.Context, time.Time, ...time.Duration) error
	RenewConversationJobLease(context.Context, int64, string, time.Time) (bool, error)
	ExpireConversationJobs(context.Context, time.Time) error
}

// OutputSink persists delivery intents. Platform delivery is performed later
// by delivery.Worker; a platform timeout therefore cannot rerun a capability.
type OutputSink interface {
	Enqueue(context.Context, message.Outbound) (delivery.Record, bool, error)
}

type ReplyContextResolver interface {
	ResolveResponseContext(context.Context, message.Conversation, string) (*message.ResponseContext, error)
}

// QuotedMessageResolver is optional so adapters that only support activation
// context can keep implementing ReplyContextResolver. The concrete store
// implementation verifies the accepted Bot outbox and conversation boundary
// before returning quoted content.
type QuotedMessageResolver interface {
	ResolveQuotedMessage(context.Context, message.Conversation, string) (*message.QuotedMessage, error)
}

type CoordinatorConfig struct {
	Jobs         JobRepository
	Commands     CommandHandler
	Agent        AgentHandler
	Outputs      OutputSink
	Replies      ReplyContextResolver
	Recorder     Recorder
	PollInterval time.Duration
	BatchSize    int
	Logger       *log.Logger
}

// Coordinator owns the one durable pipeline used by live messages and
// resumed jobs. It is the only component allowed to turn a capability result
// into user-visible output.
type Coordinator struct {
	jobs           JobRepository
	commands       CommandHandler
	agent          AgentHandler
	outputs        OutputSink
	replies        ReplyContextResolver
	recorder       Recorder
	pollInterval   time.Duration
	batchSize      int
	nextRecoveryAt time.Time
	logger         *log.Logger
	wake           chan struct{}
}

type conversationJobPayload struct {
	Inbound    message.Inbound    `json:"inbound"`
	Route      routing.Action     `json:"route"`
	Activation routing.Activation `json:"activation"`
}

// conversationPersistenceError distinguishes a durable state/output failure
// from a domain or tool failure. Persistence failures must leave the job
// retryable because capability state, transcript events, and the outbox are
// the source of truth for a later idempotent attempt.
type conversationPersistenceError struct {
	err error
}

func (e conversationPersistenceError) Error() string {
	return fmt.Sprintf("persist conversation state: %v", e.err)
}

func (e conversationPersistenceError) Unwrap() error {
	return e.err
}

// conversationJobPersistenceRetryLimit bounds how many times a job may be
// re-claimed after a persistence failure before it is failed outright.
const conversationJobPersistenceRetryLimit = 20

func markConversationPersistenceError(err error) error {
	if err == nil {
		return nil
	}
	return conversationPersistenceError{err: err}
}

func isConversationPersistenceError(err error) bool {
	var target conversationPersistenceError
	return errors.As(err, &target)
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if config.Jobs == nil {
		return nil, errors.New("conversation job repository is unavailable")
	}
	if config.Commands == nil {
		return nil, errors.New("command handler is unavailable")
	}
	if config.Outputs == nil {
		return nil, errors.New("durable output sink is unavailable")
	}
	interval := config.PollInterval
	if interval <= 0 {
		interval = defaultJobPollInterval
	}
	batchSize := config.BatchSize
	if batchSize <= 0 {
		batchSize = defaultJobBatchSize
	}
	return &Coordinator{
		jobs: config.Jobs, commands: config.Commands, agent: config.Agent, outputs: config.Outputs, replies: config.Replies,
		recorder:     config.Recorder,
		pollInterval: interval, batchSize: batchSize, logger: config.Logger, wake: make(chan struct{}, 1),
	}, nil
}

// Process validates and durably accepts an inbound event. Execution always
// happens from the persisted job, never from the transport callback stack.
func (c *Coordinator) Process(ctx context.Context, inbound message.Inbound) {
	if err := c.Enqueue(ctx, inbound); err != nil {
		c.logf("enqueue inbound job failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			inbound.Conversation.Platform, inbound.Conversation.Type, inbound.Conversation.ID, err)
	}
}

func (c *Coordinator) Enqueue(ctx context.Context, inbound message.Inbound) error {
	if err := validateInbound(inbound); err != nil {
		return err
	}
	sourceEventID := inboundSourceEventID(inbound.Source)
	if sourceEventID == "" {
		return errors.New("inbound source event id is empty")
	}
	if decision, isDecision := userConfirmationDecision(inbound.Text); isDecision {
		decision.SourceEventID = sourceEventID
		_, confirmed, err := c.jobs.ResolveCapabilityConfirmation(ctx, identityForInbound(inbound), decision)
		if err != nil {
			return fmt.Errorf("resolve capability confirmation: %w", err)
		}
		if confirmed != nil {
			select {
			case c.wake <- struct{}{}:
			default:
			}
			return nil
		}
		// A confirmation word is mechanics only while an operation is actually
		// pending. Otherwise it remains an ordinary conversation turn.
	}
	var replyContext *message.ResponseContext
	if inbound.ReplyTo != nil {
		if c.replies != nil {
			resolved, err := c.replies.ResolveResponseContext(ctx, inbound.Conversation, inbound.ReplyTo.MessageID)
			if err != nil {
				return fmt.Errorf("resolve replied Bot message: %w", err)
			}
			replyContext = resolved
		}
		quotedResolver, ok := c.replies.(QuotedMessageResolver)
		if !ok {
			quotedResolver, ok = c.jobs.(QuotedMessageResolver)
		}
		if ok {
			quoted, err := quotedResolver.ResolveQuotedMessage(ctx, inbound.Conversation, inbound.ReplyTo.MessageID)
			if err != nil {
				return fmt.Errorf("resolve quoted Bot message: %w", err)
			}
			inbound.ReplyContext = quoted
		}
	}
	routeDecision := routing.Decide(inbound, replyContext)
	if routeDecision.Action == routing.ActionIgnore {
		return nil
	}
	payload, err := json.Marshal(conversationJobPayload{
		Inbound: inbound, Route: routeDecision.Action, Activation: routeDecision.Activation,
	})
	if err != nil {
		return fmt.Errorf("encode conversation job: %w", err)
	}
	var invocation store.ConversationJobInvocation
	if routeDecision.Invocation.Capability != nil {
		invocation = store.ConversationJobInvocation{
			Name:    routeDecision.Invocation.Name,
			Command: routeDecision.Invocation.CanonicalCommand(),
			Args:    append([]string(nil), routeDecision.Invocation.Args...),
		}
	}
	_, created, err := c.jobs.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity:      identityForInbound(inbound),
		SourceEventID: sourceEventID,
		Input: store.ConversationJobInput{
			Text: strings.TrimSpace(inbound.Text),
			Data: payload,
		},
		Invocation: invocation,
	})
	if err != nil {
		return err
	}
	if created {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

func userConfirmationDecision(text string) (store.CapabilityConfirmationDecision, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "ok", "确认", "确定", "是":
		return store.CapabilityConfirmationDecision{Approved: true}, true
	case "no", "取消", "拒绝", "否", "不":
		return store.CapabilityConfirmationDecision{Approved: false, Reason: "用户拒绝执行"}, true
	default:
		return store.CapabilityConfirmationDecision{}, false
	}
}

func inboundSourceEventID(source message.ReplyRef) string {
	for _, candidate := range []string{source.EventID, source.MessageID, source.TransportID} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func (c *Coordinator) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.tick(ctx)
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			c.tick(ctx)
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

func (c *Coordinator) tick(ctx context.Context) {
	now := time.Now().UTC()
	if c.nextRecoveryAt.IsZero() || !now.Before(c.nextRecoveryAt) {
		c.nextRecoveryAt = now.Add(defaultJobRecoveryInterval)
		if err := c.jobs.RecoverConversationJobLeases(ctx, now); err != nil {
			c.logf("recover conversation jobs failed: %v", err)
		}
	}
	if err := c.jobs.ExpireConversationJobs(ctx, now); err != nil {
		c.logf("expire conversation jobs failed: %v", err)
		return
	}
	jobs, err := c.jobs.ClaimConversationJobs(ctx, now, c.batchSize)
	if err != nil {
		c.logf("claim conversation jobs failed: %v", err)
		return
	}
	for i := range jobs {
		job := jobs[i]
		go c.execute(ctx, job)
	}
}

func (c *Coordinator) execute(ctx context.Context, job store.ConversationJob) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = store.WithConversationJobLease(ctx, job.ID, job.LeaseToken)
	renewed, err := c.jobs.RenewConversationJobLease(ctx, job.ID, job.LeaseToken, time.Now())
	if err != nil {
		c.fail(ctx, job, markConversationPersistenceError(err))
		return
	}
	if !renewed {
		return
	}
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(store.ConversationJobLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				live, err := c.jobs.RenewConversationJobLease(ctx, job.ID, job.LeaseToken, now)
				if err != nil || !live {
					if err != nil {
						c.logf("conversation job %d heartbeat failed: %v", job.ID, err)
					}
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	payload, err := decodeConversationJobPayload(job)
	if err != nil {
		c.fail(ctx, job, err)
		return
	}
	inbound := payload.Inbound
	part := 0
	var outputMu sync.Mutex
	commit := func(ctx context.Context, response commands.Response, receiptIDs []string, transition store.ConversationJobTransition) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		next, err := c.commitResponse(ctx, job, inbound, response, part, receiptIDs, transition)
		if err == nil {
			part = next
		}
		return err
	}
	switch payload.Route {
	case routing.ActionCommand:
		if strings.TrimSpace(job.Invocation.Name) == "" || strings.TrimSpace(job.Invocation.Command) == "" || len(job.Invocation.Data) > 0 {
			c.fail(ctx, job, errors.New("persisted command route has an incomplete invocation"))
			return
		}
		invocation, restored := commands.RestoreInvocation(commands.CapabilityID(job.Invocation.Name), job.Invocation.Args)
		if !restored {
			c.fail(ctx, job, fmt.Errorf("restore routed capability %q", job.Invocation.Name))
			return
		}
		if strings.TrimSpace(job.Invocation.Command) != invocation.CanonicalCommand() {
			c.fail(ctx, job, errors.New("persisted command route does not match its invocation"))
			return
		}
		c.executeCommandRoute(ctx, job, inbound, invocation, commit)
		return
	case routing.ActionAgent:
		if strings.TrimSpace(job.Invocation.Name) != "" || strings.TrimSpace(job.Invocation.Command) != "" || len(job.Invocation.Args) > 0 || len(job.Invocation.Data) > 0 {
			c.fail(ctx, job, errors.New("persisted Agent route unexpectedly contains a command invocation"))
			return
		}
	default:
		c.fail(ctx, job, fmt.Errorf("unsupported persisted route %q", payload.Route))
		return
	}
	if c.agent == nil {
		c.recordIgnoredJob(ctx, job, inbound)
		c.complete(ctx, job)
		return
	}
	var waitingAuth bool
	var hostResponses []commands.Response
	result := c.agent.Run(ctx, agent.Input{
		ActorDisplayName: inbound.Actor.DisplayName, SentAt: inbound.SentAt, ReceivedAt: inbound.ReceivedAt,
		Media: inbound.Media, Forwarded: inbound.Forwarded,
		ReplyContext: inbound.ReplyContext, Text: inbound.Text, ImageURLs: append([]string(nil), inbound.ImageURLs...), Identity: job.Identity, JobID: job.ID,
		JobRevision: job.Revision, JobLeaseToken: job.LeaseToken,
		SendResponse: func(ctx context.Context, _ store.Identity, response commands.Response) error {
			_ = ctx
			outputMu.Lock()
			hostResponses = append(hostResponses, response)
			if response.Kind == commands.ResponseKindAuthWait {
				waitingAuth = true
			}
			outputMu.Unlock()
			return nil
		},
	})
	if result.State == agent.RunStateFailed || result.Err != nil {
		cause := result.Err
		if cause == nil {
			cause = errors.New("agent run failed before producing a durable result")
		}
		c.fail(ctx, job, markConversationPersistenceError(cause))
		return
	}
	if !result.Handled {
		c.recordIgnoredJob(ctx, job, inbound)
		if c.complete(ctx, job) {
			c.acknowledgeAgent(ctx, job)
		}
		return
	}
	if result.State == agent.RunStateWaitingAuth {
		outputMu.Lock()
		response := combineResponses(hostResponses...)
		outputMu.Unlock()
		if err := commit(ctx, response, nil, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
		}); err != nil {
			c.fail(ctx, job, err)
			return
		}
		c.recordJob(ctx, job, inbound, response, store.InteractionStatusWaitingAuth)
		return
	}
	if result.State == agent.RunStateInterrupted {
		executions, err := c.jobs.CapabilityExecutionsForJob(ctx, job.ID)
		if err != nil {
			c.fail(ctx, job, markConversationPersistenceError(err))
			return
		}
		if !hasAwaitingConfirmation(executions) {
			if !allCapabilityExecutionsTerminal(executions) {
				c.fail(ctx, job, markConversationPersistenceError(errors.New("agent interrupted while a capability execution remained nonterminal")))
				return
			}
			receipts, err := c.unsentExecutionReceipts(ctx, job.ID, false)
			if err != nil {
				c.fail(ctx, job, err)
				return
			}
			reply := appendReceiptLines(commands.Response{
				Kind: "agent_error",
				Text: "这次请求没有可确认的待处理操作，系统已停止本次流程；没有执行新的操作。请重新发送原请求。",
			}, "", receipts)
			if err := commit(ctx, reply, receipts.IDs, store.ConversationJobTransition{State: store.ConversationJobStateCompleted}); err != nil {
				c.fail(ctx, job, err)
				return
			}
			c.acknowledgeAgent(ctx, job)
			c.recordJob(ctx, job, inbound, reply, store.InteractionStatusHandled)
			return
		}
		receipts, err := c.unsentExecutionReceipts(ctx, job.ID, true)
		if err != nil {
			c.fail(ctx, job, err)
			return
		}
		confirmation := appendReceiptLines(commands.Response{Kind: "agent_confirmation"}, confirmationPrompt, receipts)
		outputMu.Lock()
		confirmation = combineResponses(append(append([]commands.Response(nil), hostResponses...), confirmation)...)
		outputMu.Unlock()
		if err := commit(ctx, confirmation, receipts.IDs, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingConfirmation, WaitReason: store.ConversationJobWaitReasonConfirmation,
		}); err != nil {
			c.fail(ctx, job, err)
			return
		}
		c.recordJob(ctx, job, inbound, confirmation, store.InteractionStatusWaitingConfirmation)
		return
	}
	outputMu.Lock()
	reply := combineResponses(append(append([]commands.Response(nil), hostResponses...), result.Response)...)
	outputMu.Unlock()
	receipts, err := c.unsentExecutionReceipts(ctx, job.ID, false)
	if err != nil {
		c.fail(ctx, job, err)
		return
	}
	reply = appendReceiptLines(reply, "", receipts)
	outputMu.Lock()
	authRequired := waitingAuth || reply.Kind == commands.ResponseKindAuthWait
	outputMu.Unlock()
	if authRequired {
		if err := commit(ctx, reply, receipts.IDs, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
		}); err != nil {
			c.fail(ctx, job, err)
			return
		}
		c.recordJob(ctx, job, inbound, reply, store.InteractionStatusWaitingAuth)
		return
	}
	if err := commit(ctx, reply, receipts.IDs, store.ConversationJobTransition{State: store.ConversationJobStateCompleted}); err != nil {
		c.fail(ctx, job, err)
		return
	}
	c.acknowledgeAgent(ctx, job)
	c.recordJob(ctx, job, inbound, reply, store.InteractionStatusHandled)
}

func hasAwaitingConfirmation(executions []store.CapabilityExecution) bool {
	for _, execution := range executions {
		if execution.State == store.CapabilityExecutionAwaitingConfirmation {
			return true
		}
	}
	return false
}

func allCapabilityExecutionsTerminal(executions []store.CapabilityExecution) bool {
	for _, execution := range executions {
		switch execution.State {
		case store.CapabilityExecutionDenied,
			store.CapabilityExecutionCancelled,
			store.CapabilityExecutionExpired,
			store.CapabilityExecutionSucceeded,
			store.CapabilityExecutionFailed,
			store.CapabilityExecutionUnknown:
			continue
		default:
			return false
		}
	}
	return true
}

func decodeConversationJobPayload(job store.ConversationJob) (conversationJobPayload, error) {
	if len(job.Input.Data) == 0 {
		return conversationJobPayload{}, errors.New("conversation job payload is empty")
	}
	var payload conversationJobPayload
	if err := json.Unmarshal(job.Input.Data, &payload); err != nil {
		return conversationJobPayload{}, fmt.Errorf("decode conversation job: %w", err)
	}
	if err := validateInbound(payload.Inbound); err != nil {
		return conversationJobPayload{}, err
	}
	return payload, nil
}

func (c *Coordinator) complete(ctx context.Context, job store.ConversationJob) bool {
	ok, err := c.jobs.CompleteConversationJob(ctx, job.ID, job.LeaseToken)
	if err != nil {
		c.logf("complete conversation job %d failed: %v", job.ID, err)
	} else if !ok {
		c.logf("complete conversation job %d lost lease", job.ID)
	}
	return err == nil && ok
}

func (c *Coordinator) acknowledgeAgent(ctx context.Context, job store.ConversationJob) {
	if c.agent == nil {
		return
	}
	if err := c.agent.Acknowledge(ctx, job.ID, job.Revision, job.LeaseToken); err != nil {
		c.logf("acknowledge agent checkpoint for conversation job %d failed: %v", job.ID, err)
	}
}

func (c *Coordinator) fail(ctx context.Context, job store.ConversationJob, cause error) {
	// A persistence failure is retried because it is usually transient. A
	// permanently unacceptable row is not: without this bound the job returns to
	// retry_wait immediately, is re-claimed at once, and spins until the
	// conversation expires.
	if isConversationPersistenceError(cause) && job.Attempts < conversationJobPersistenceRetryLimit {
		ok, err := c.jobs.RetryConversationJob(ctx, job.ID, job.LeaseToken, cause.Error())
		if err != nil {
			c.logf("retry conversation job %d after persistence failure failed: %v", job.ID, err)
		} else if !ok {
			c.logf("retry conversation job %d after persistence failure lost lease", job.ID)
		} else {
			c.logf("conversation job %d persistence failed; moved to retry_wait: %v", job.ID, cause)
		}
		return
	}
	ok, err := c.jobs.FailConversationJob(ctx, job.ID, job.LeaseToken, cause.Error())
	if err != nil {
		c.logf("fail conversation job %d failed: %v", job.ID, err)
	} else if !ok {
		c.logf("fail conversation job %d lost lease", job.ID)
	} else {
		c.acknowledgeAgent(ctx, job)
	}
	c.logf("conversation job %d failed: %v", job.ID, cause)
}

func (c *Coordinator) commitResponse(
	ctx context.Context,
	job store.ConversationJob,
	inbound message.Inbound,
	response commands.Response,
	start int,
	receiptIDs []string,
	transition store.ConversationJobTransition,
) (int, error) {
	messages, next, err := c.responseOutbounds(ctx, job, inbound, response, start)
	if err != nil {
		return start, markConversationPersistenceError(err)
	}
	committed, err := c.jobs.CommitConversationJobOutput(ctx, store.ConversationJobOutputCommit{
		JobID: job.ID, LeaseToken: job.LeaseToken, Messages: messages,
		ReceiptIDs: receiptIDs, Transition: transition,
	})
	if err != nil {
		return start, markConversationPersistenceError(err)
	}
	for index, output := range committed {
		if output.Created && index < len(messages) {
			c.recordOutbound(ctx, job, messages[index])
		}
	}
	return next, nil
}

func (c *Coordinator) responseOutbounds(ctx context.Context, job store.ConversationJob, inbound message.Inbound, response commands.Response, start int) ([]message.Outbound, int, error) {
	var outbounds []message.Outbound
	var ordinary []commands.Response
	var groups []commands.Response
	flush := func() {
		if len(ordinary) > 0 {
			groups = append(groups, commands.Response{Kind: response.Kind, Parts: ordinary})
			ordinary = nil
		}
	}
	for _, part := range flattenResponseParts(response) {
		if part.Kind == "agent_confirmation" {
			flush()
			groups = append(groups, part)
		} else {
			ordinary = append(ordinary, part)
		}
	}
	flush()
	for _, group := range groups {
		// A routed command and every host response are synthetic output. Agent
		// turns may carry model-authored prose, but only text explicitly marked
		// by the model response is allowed to remain text at this boundary.
		allowLLMText := strings.TrimSpace(job.Invocation.Name) == ""
		content, err := c.presentationContentFor(ctx, group, allowLLMText)
		if err != nil {
			return nil, start, err
		}
		if !content.HasContent() {
			continue
		}
		kind := group.Kind
		if kind == "" {
			for _, part := range flattenResponseParts(group) {
				if part.Kind != "" {
					kind = part.Kind
					break
				}
			}
		}
		contents := []message.Content{content}
		if inbound.Conversation.Platform == "qqbot" {
			contents = content.SingleImageMessages()
		}
		for _, item := range contents {
			index := start + len(outbounds)
			ref := inbound.Source
			ref.Sequence = index + 1
			textPolicy := message.TextPolicyImageOnly
			if allowLLMText && responseContainsLLMText(group) {
				textPolicy = message.TextPolicyLLM
			}
			outbounds = append(outbounds, message.Outbound{
				Kind: kind, TextPolicy: textPolicy, Target: inbound.Conversation, ReplyTo: &ref, Content: item,
				Context: responseContextForJob(job), DedupeKey: fmt.Sprintf("conversation-job:%d:revision:%d:part:%d", job.ID, job.Revision, index),
			})
		}
	}
	return outbounds, start + len(outbounds), nil
}

func (c *Coordinator) recordOutbound(ctx context.Context, job store.ConversationJob, outbound message.Outbound) {
	if c.recorder == nil {
		return
	}
	if err := c.recorder.RecordInteraction(ctx, job.Identity, store.Interaction{
		Direction: store.InteractionDirectionOutbound,
		RawText:   outbound.Content.TextContent(),
		Command:   outbound.Kind,
		Handled:   true,
		Status:    store.InteractionStatusSent,
	}); err != nil {
		c.logf("record conversation job %d output failed: %v", job.ID, err)
	}
}

func responseContextForJob(job store.ConversationJob) *message.ResponseContext {
	invocation, ok := commands.NewInvocation(commands.CapabilityID(job.Invocation.Name), job.Invocation.Args)
	if !ok || invocation.Policy().DataScope != commands.DataScopePublic {
		return nil
	}
	return &message.ResponseContext{
		Capability: string(invocation.ID()),
		Arguments:  append([]string(nil), invocation.Args...),
	}
}

func (c *Coordinator) recordJob(ctx context.Context, job store.ConversationJob, inbound message.Inbound, response commands.Response, status string) {
	if c.recorder == nil {
		return
	}
	command := job.Invocation.Name
	if command == "" {
		command = "agent"
	}
	if err := c.recorder.RecordInteraction(ctx, job.Identity, store.Interaction{
		RawText: inbound.Text, Command: command, Handled: true, Reply: response.Text, Status: status,
	}); err != nil {
		c.logf("record conversation job %d failed: %v", job.ID, err)
	}
}

func (c *Coordinator) recordIgnoredJob(ctx context.Context, job store.ConversationJob, inbound message.Inbound) {
	if c.recorder == nil {
		return
	}
	if err := c.recorder.RecordInteraction(ctx, job.Identity, store.Interaction{
		RawText: inbound.Text, Handled: false, Status: store.InteractionStatusIgnored,
	}); err != nil {
		c.logf("record ignored conversation job %d failed: %v", job.ID, err)
	}
}

func (c *Coordinator) presentationContent(ctx context.Context, response commands.Response) (message.Content, error) {
	return c.presentationContentFor(ctx, response, false)
}

func (c *Coordinator) presentationContentFor(ctx context.Context, response commands.Response, allowLLMText bool) (message.Content, error) {
	parts := flattenResponseParts(response)
	content := message.Content{Parts: make([]message.ContentPart, 0, len(parts)*2)}
	for _, item := range parts {
		if text := strings.TrimSpace(item.Text); text != "" {
			if allowLLMText && item.TextOrigin == commands.ResponseTextOriginLLM {
				content.Parts = append(content.Parts, message.ContentPart{Text: text})
			} else if item.Image == nil {
				attachment, err := c.renderTextCard(item.Kind, text)
				if err != nil {
					return message.Content{}, err
				}
				content.Parts = append(content.Parts, message.ContentPart{Attachment: attachment})
			}
		}
		if item.Image == nil {
			if strings.TrimSpace(item.Text) == "" && (item.Data != nil || strings.TrimSpace(item.Kind) != "") {
				attachment, err := c.renderTextCard(item.Kind, "没有可显示的结果。")
				if err != nil {
					return message.Content{}, err
				}
				content.Parts = append(content.Parts, message.ContentPart{Attachment: attachment})
			}
			continue
		}
		attachment, err := c.renderAttachment(item.Image)
		if err == nil {
			content.Parts = append(content.Parts, message.ContentPart{Attachment: attachment})
			continue
		}
		return message.Content{}, err
	}
	return content, nil
}

func (c *Coordinator) renderTextCard(kind, text string) (*message.Attachment, error) {
	image := responses.NewTextCardImage(kind, text)
	if image == nil {
		return nil, errors.New("host text is empty")
	}
	payload, err := responses.EncodeImageIntent(image)
	if err != nil {
		return nil, err
	}
	return &message.Attachment{MIMEType: "image/png", AltText: strings.TrimSpace(text), RenderPayload: payload}, nil
}

func responseContainsLLMText(response commands.Response) bool {
	if strings.TrimSpace(response.Text) != "" && response.TextOrigin == commands.ResponseTextOriginLLM {
		return true
	}
	for _, part := range response.Parts {
		if responseContainsLLMText(part) {
			return true
		}
	}
	return false
}

// flattenResponseParts keeps every user-visible field in its original order.
// A response with Parts may still carry leading text or an image, so those
// fields are emitted before its nested parts instead of being discarded.
func flattenResponseParts(response commands.Response) []commands.Response {
	parts := make([]commands.Response, 0, max(1, len(response.Parts)+1))
	if strings.TrimSpace(response.Text) != "" || response.Image != nil ||
		(len(response.Parts) == 0 && (response.Data != nil || strings.TrimSpace(response.Kind) != "")) {
		parts = append(parts, commands.Response{Text: response.Text, TextOrigin: response.TextOrigin, Data: response.Data, Image: response.Image, Kind: response.Kind})
	}
	for _, nested := range response.Parts {
		parts = append(parts, flattenResponseParts(nested)...)
	}
	if len(parts) == 0 && len(response.Parts) == 0 {
		return []commands.Response{response}
	}
	return parts
}

func (c *Coordinator) renderAttachment(image *responses.Image) (*message.Attachment, error) {
	if image == nil {
		return nil, errors.New("response image is nil")
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		return &message.Attachment{MIMEType: "image/png", URL: url, AltText: image.AltText}, nil
	}
	payload, err := responses.EncodeImageIntent(image)
	if err != nil {
		return nil, err
	}
	return &message.Attachment{MIMEType: "image/png", AltText: image.AltText, RenderPayload: payload}, nil
}

func (c *Coordinator) logf(format string, args ...any) {
	if c != nil && c.logger != nil {
		c.logger.Print(textutil.SafeLogText(fmt.Sprintf(format, args...)))
	}
}

func identityForInbound(inbound message.Inbound) store.Identity {
	return store.Identity{
		Platform: inbound.Actor.Platform, UserID: inbound.Actor.UserID,
		ConversationType: inbound.Conversation.Type, ConversationID: inbound.Conversation.ID,
	}
}

func validateInbound(inbound message.Inbound) error {
	if strings.TrimSpace(inbound.Actor.Platform) == "" || strings.TrimSpace(inbound.Actor.UserID) == "" {
		return errors.New("inbound actor is incomplete")
	}
	if strings.TrimSpace(inbound.Conversation.Platform) == "" || strings.TrimSpace(inbound.Conversation.Type) == "" || strings.TrimSpace(inbound.Conversation.ID) == "" {
		return errors.New("inbound conversation is incomplete")
	}
	if !strings.EqualFold(strings.TrimSpace(inbound.Actor.Platform), strings.TrimSpace(inbound.Conversation.Platform)) {
		return errors.New("inbound actor and conversation platforms differ")
	}
	return nil
}

var _ Processor = (*Coordinator)(nil)
