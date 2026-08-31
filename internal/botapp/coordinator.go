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
)

const (
	defaultJobPollInterval    = 250 * time.Millisecond
	defaultJobBatchSize       = 4
	defaultImageRenderTimeout = 5 * time.Second
)

type CommandHandler interface {
	HandleInvocationResponse(context.Context, commands.Input, commands.Invocation) (commands.Response, bool)
}

type AgentHandler interface {
	HandleResponse(context.Context, agent.Input) (commands.Response, bool)
}

type Recorder interface {
	RecordInteraction(context.Context, store.Identity, store.Interaction) error
}

type Renderer interface {
	RenderPNG(*responses.Image) ([]byte, int, int, error)
}

type Processor interface {
	Process(context.Context, message.Inbound)
}

type renderedImage struct {
	data []byte
	err  error
}

func normalizedImageRenderTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultImageRenderTimeout
	}
	return timeout
}

// JobRepository is the durable source of truth for conversation work. The
// coordinator deliberately depends on this small contract instead of the
// concrete SQLite store so job scheduling and execution can be tested apart.
type JobRepository interface {
	EnqueueConversationJob(context.Context, store.ConversationJobEnqueue) (store.ConversationJob, bool, error)
	ClaimConversationJobs(context.Context, time.Time, int) ([]store.ConversationJob, error)
	CompleteConversationJob(context.Context, int64, string) (bool, error)
	FailConversationJob(context.Context, int64, string, string) (bool, error)
	TransitionConversationJob(context.Context, int64, string, store.ConversationJobTransition) (bool, error)
	ConsumeConversationJobConfirmation(context.Context, store.Identity, ...time.Time) (*store.ConversationJob, error)
	RecoverConversationJobLeases(context.Context, time.Time, ...time.Duration) error
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

type CoordinatorConfig struct {
	Jobs               JobRepository
	Commands           CommandHandler
	Agent              AgentHandler
	Outputs            OutputSink
	Replies            ReplyContextResolver
	Recorder           Recorder
	Renderer           Renderer
	ImageRenderTimeout time.Duration
	PollInterval       time.Duration
	BatchSize          int
	Logger             *log.Logger
}

// Coordinator owns the one durable pipeline used by live messages and
// resumed jobs. It is the only component allowed to turn a capability result
// into user-visible output.
type Coordinator struct {
	jobs               JobRepository
	commands           CommandHandler
	agent              AgentHandler
	outputs            OutputSink
	replies            ReplyContextResolver
	recorder           Recorder
	renderer           Renderer
	imageRenderTimeout time.Duration
	pollInterval       time.Duration
	batchSize          int
	logger             *log.Logger
	wake               chan struct{}
}

type conversationJobPayload struct {
	Inbound    message.Inbound    `json:"inbound"`
	Route      routing.Action     `json:"route"`
	Activation routing.Activation `json:"activation"`
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
		recorder: config.Recorder, renderer: config.Renderer,
		imageRenderTimeout: normalizedImageRenderTimeout(config.ImageRenderTimeout),
		pollInterval:       interval, batchSize: batchSize, logger: config.Logger, wake: make(chan struct{}, 1),
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
	if isUserConfirmation(inbound.Text) {
		confirmed, err := c.jobs.ConsumeConversationJobConfirmation(ctx, identityForInbound(inbound))
		if err != nil {
			return fmt.Errorf("consume conversation confirmation: %w", err)
		}
		if confirmed != nil {
			select {
			case c.wake <- struct{}{}:
			default:
			}
			return nil
		}
	}
	var replyContext *message.ResponseContext
	if inbound.ReplyTo != nil && c.replies != nil {
		resolved, err := c.replies.ResolveResponseContext(ctx, inbound.Conversation, inbound.ReplyTo.MessageID)
		if err != nil {
			return fmt.Errorf("resolve replied Bot message: %w", err)
		}
		replyContext = resolved
	}
	decision := routing.Decide(inbound, replyContext)
	if decision.Action == routing.ActionIgnore {
		return nil
	}
	payload, err := json.Marshal(conversationJobPayload{
		Inbound: inbound, Route: decision.Action, Activation: decision.Activation,
	})
	if err != nil {
		return fmt.Errorf("encode conversation job: %w", err)
	}
	var invocation store.ConversationJobInvocation
	if decision.Invocation.Capability != nil {
		invocation = store.ConversationJobInvocation{
			Name:    decision.Invocation.Name,
			Command: decision.Invocation.CanonicalCommand(),
			Args:    append([]string(nil), decision.Invocation.Args...),
		}
	}
	_, created, err := c.jobs.EnqueueConversationJob(ctx, store.ConversationJobEnqueue{
		Identity:      identityForInbound(inbound),
		SourceEventID: sourceEventID,
		Input: store.ConversationJobInput{
			Text: strings.TrimSpace(inbound.Text),
			Data: payload,
		},
		Invocation:  invocation,
		MaxAttempts: 2,
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

func isUserConfirmation(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "ok", "确认", "确定", "是":
		return true
	default:
		return false
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
	now := time.Now().UTC()
	if err := c.jobs.RecoverConversationJobLeases(ctx, now); err != nil {
		c.logf("recover conversation jobs failed: %v", err)
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
	payload, err := decodeConversationJobPayload(job)
	if err != nil {
		c.fail(ctx, job, err)
		return
	}
	inbound := payload.Inbound
	commandText := inbound.Text
	if strings.TrimSpace(job.Invocation.Command) != "" {
		commandText = job.Invocation.Command
	}
	part := 0
	var outputMu sync.Mutex
	enqueue := func(ctx context.Context, response commands.Response) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		next, err := c.enqueueResponse(ctx, job, inbound, response, part)
		part = next
		return err
	}
	commandRoute := strings.TrimSpace(job.Invocation.Command) != "" || payload.Route == routing.ActionCommand
	if commandRoute {
		invocation, restored := commands.RestoreInvocation(commands.CapabilityID(job.Invocation.Name), job.Invocation.Args)
		if !restored {
			c.fail(ctx, job, fmt.Errorf("restore routed capability %q", job.Invocation.Name))
			return
		}
		reply, ok := c.commands.HandleInvocationResponse(ctx, commands.Input{
			Text: commandText, Identity: job.Identity, SuppressLog: true,
		}, invocation)
		if !ok {
			c.fail(ctx, job, errors.New("routed command was not handled"))
			return
		}
		if err := enqueue(ctx, reply); err != nil {
			c.fail(ctx, job, err)
			return
		}
		if reply.Kind == commands.ResponseKindAuthWait {
			c.recordJob(ctx, job, inbound, reply, store.InteractionStatusWaitingAuth)
			c.transition(ctx, job, store.ConversationJobTransition{
				State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
			})
			return
		}
		c.recordJob(ctx, job, inbound, reply, store.InteractionStatusHandled)
		c.complete(ctx, job)
		return
	}
	if payload.Route != routing.ActionAgent {
		c.fail(ctx, job, fmt.Errorf("unsupported persisted route %q", payload.Route))
		return
	}
	if c.agent == nil {
		c.recordIgnoredJob(ctx, job, inbound)
		c.complete(ctx, job)
		return
	}
	var confirmationCommand string
	var waitingAuth bool
	reply, ok := c.agent.HandleResponse(ctx, agent.Input{
		Text: inbound.Text, ImageURLs: append([]string(nil), inbound.ImageURLs...), Identity: job.Identity,
		SendUpdate: func(ctx context.Context, _ store.Identity, text string) error {
			return enqueue(ctx, commands.Response{Text: text, Kind: "agent_update"})
		},
		SendResponse: func(ctx context.Context, _ store.Identity, response commands.Response) error {
			if err := enqueue(ctx, response); err != nil {
				return err
			}
			if response.Kind == commands.ResponseKindAuthWait {
				outputMu.Lock()
				waitingAuth = true
				outputMu.Unlock()
			}
			return nil
		},
		WaitForConfirmation: func(_ context.Context, _ store.Identity, command string) error {
			outputMu.Lock()
			defer outputMu.Unlock()
			command = strings.TrimSpace(command)
			if command == "" {
				return errors.New("confirmation command is empty")
			}
			if confirmationCommand != "" && confirmationCommand != command {
				return errors.New("only one confirmation may be requested per job")
			}
			confirmationCommand = command
			return nil
		},
	})
	if !ok {
		c.recordIgnoredJob(ctx, job, inbound)
		c.complete(ctx, job)
		return
	}
	if reply.Text != "" || reply.Image != nil || len(reply.Parts) > 0 {
		if err := enqueue(ctx, reply); err != nil {
			c.fail(ctx, job, err)
			return
		}
	}
	if reply.Kind == commands.ResponseKindAuthWait {
		c.recordJob(ctx, job, inbound, reply, store.InteractionStatusWaitingAuth)
		c.transition(ctx, job, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
		})
		return
	}
	outputMu.Lock()
	authRequired := waitingAuth
	confirmationRequired := confirmationCommand
	outputMu.Unlock()
	if authRequired {
		c.recordJob(ctx, job, inbound, reply, store.InteractionStatusWaitingAuth)
		c.transition(ctx, job, store.ConversationJobTransition{
			State: store.ConversationJobStateWaitingAuth, WaitReason: store.ConversationJobWaitReasonAuth,
		})
		return
	}
	if confirmationRequired != "" {
		c.recordJob(ctx, job, inbound, reply, store.InteractionStatusWaitingConfirmation)
		c.transition(ctx, job, store.ConversationJobTransition{
			State:      store.ConversationJobStateWaitingConfirmation,
			WaitReason: store.ConversationJobWaitReasonConfirmation,
			Invocation: persistedInvocation(confirmationRequired),
		})
		return
	}
	c.recordJob(ctx, job, inbound, reply, store.InteractionStatusHandled)
	c.complete(ctx, job)
}

func persistedInvocation(command string) store.ConversationJobInvocation {
	result := store.ConversationJobInvocation{Command: strings.TrimSpace(command)}
	if parsed, ok := commands.ParseInvocation(command); ok {
		result.Name = parsed.Name
		result.Command = parsed.CanonicalCommand()
		result.Args = append([]string(nil), parsed.Args...)
	}
	return result
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

func (c *Coordinator) complete(ctx context.Context, job store.ConversationJob) {
	ok, err := c.jobs.CompleteConversationJob(ctx, job.ID, job.LeaseToken)
	if err != nil {
		c.logf("complete conversation job %d failed: %v", job.ID, err)
	} else if !ok {
		c.logf("complete conversation job %d lost lease", job.ID)
	}
}

func (c *Coordinator) fail(ctx context.Context, job store.ConversationJob, cause error) {
	ok, err := c.jobs.FailConversationJob(ctx, job.ID, job.LeaseToken, cause.Error())
	if err != nil {
		c.logf("fail conversation job %d failed: %v", job.ID, err)
	} else if !ok {
		c.logf("fail conversation job %d lost lease", job.ID)
	}
	c.logf("conversation job %d failed: %v", job.ID, cause)
}

func (c *Coordinator) transition(ctx context.Context, job store.ConversationJob, transition store.ConversationJobTransition) {
	ok, err := c.jobs.TransitionConversationJob(ctx, job.ID, job.LeaseToken, transition)
	if err != nil {
		c.logf("transition conversation job %d failed: %v", job.ID, err)
	} else if !ok {
		c.logf("transition conversation job %d lost lease", job.ID)
	}
}

func (c *Coordinator) enqueueResponse(ctx context.Context, job store.ConversationJob, inbound message.Inbound, response commands.Response, start int) (int, error) {
	parts := response.Parts
	if len(parts) == 0 {
		parts = []commands.Response{response}
	}
	part := start
	for _, item := range parts {
		content, err := c.presentationContent(ctx, item)
		if err != nil {
			return part, err
		}
		replyTo := inbound.Source
		replyTo.Sequence = part + 1
		_, created, err := c.outputs.Enqueue(ctx, message.Outbound{
			Kind: item.Kind, Target: inbound.Conversation, ReplyTo: &replyTo, Content: content,
			Context:   responseContextForJob(job),
			DedupeKey: fmt.Sprintf("conversation-job:%d:revision:%d:part:%d", job.ID, job.Revision, part),
		})
		if err != nil {
			return part, fmt.Errorf("persist response part %d: %w", part, err)
		}
		if created && c.recorder != nil {
			if recordErr := c.recorder.RecordInteraction(ctx, job.Identity, store.Interaction{
				Direction: store.InteractionDirectionOutbound,
				RawText:   content.Text,
				Command:   item.Kind,
				Handled:   true,
				Status:    store.InteractionStatusSent,
			}); recordErr != nil {
				c.logf("record conversation job %d output failed: %v", job.ID, recordErr)
			}
		}
		part++
	}
	return part, nil
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
	if response.Image == nil {
		return message.Content{Text: response.Text}, nil
	}
	attachment, err := c.renderAttachment(ctx, response.Image)
	if err == nil {
		return message.Content{Attachment: attachment}, nil
	}
	c.logf("render response image failed; persist text fallback: %v", err)
	text := strings.TrimSpace(response.Text)
	if text == "" {
		text = strings.TrimSpace(response.Image.AltText)
	}
	if text == "" {
		return message.Content{}, err
	}
	return message.Content{Text: text}, nil
}

func (c *Coordinator) renderAttachment(ctx context.Context, image *responses.Image) (*message.Attachment, error) {
	if image == nil {
		return nil, errors.New("response image is nil")
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		return &message.Attachment{MIMEType: "image/png", URL: url}, nil
	}
	if c.renderer == nil {
		return nil, errors.New("response renderer is unavailable")
	}
	renderCtx, cancel := context.WithTimeout(ctx, c.imageRenderTimeout)
	defer cancel()
	result := make(chan renderedImage, 1)
	go func() {
		data, _, _, err := c.renderer.RenderPNG(image)
		result <- renderedImage{data: data, err: err}
	}()
	select {
	case <-renderCtx.Done():
		return nil, renderCtx.Err()
	case rendered := <-result:
		if rendered.err != nil {
			return nil, rendered.err
		}
		return &message.Attachment{MIMEType: "image/png", Data: rendered.data}, nil
	}
}

func (c *Coordinator) logf(format string, args ...any) {
	if c != nil && c.logger != nil {
		c.logger.Printf(format, args...)
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
