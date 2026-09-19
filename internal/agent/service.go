package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/lifedata"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

type Config struct {
	Enabled bool
	APIKey  string
	BaseURL string
	Model   string
	Logger  *log.Logger

	PremiumAPIKey  string
	PremiumBaseURL string
	PremiumModel   string
	// AttachmentAPIKey and AttachmentBaseURL address the Kimi file-extract API.
	// They default to the premium pair when unset, so the chat model can move to
	// an endpoint that serves no /files route without disabling attachments.
	AttachmentAPIKey  string
	AttachmentBaseURL string
	MCPBaseURL        string
	AuthManager       *auth.Manager
}

type Service struct {
	handler          commands.Handler
	model            *einoopenai.ChatModel
	modelName        string
	premiumModel     *einoopenai.ChatModel
	premiumName      string
	enabled          bool
	logger           *log.Logger
	httpClient       *http.Client
	attachmentParser *AttachmentParser

	mcpClient     *botmcp.Client
	auth          *auth.Manager
	campusCatalog *campusCatalogCache
}

type Input struct {
	ActorDisplayName string
	SentAt           time.Time
	ReceivedAt       time.Time
	ReplyContext     *message.QuotedMessage
	Text             string
	ImageURLs        []string
	Media            []message.InputMedia
	Forwarded        []message.ForwardedMessage
	Identity         store.Identity
	JobID            int64
	// JobRevision and JobLeaseToken are the exact claim held by the
	// conversation coordinator. Checkpoint writes and acknowledgements are
	// rejected unless both still identify the same worker revision.
	JobRevision   int
	JobLeaseToken string
	// SendResponse lets a local tool hand an already formatted host response
	// directly to the application when its presentation cannot be reproduced
	// from plain model text (for example, an image).
	SendResponse func(context.Context, store.Identity, commands.Response) error

	attachmentContext  string
	preparedUserEvent  *store.ConversationEvent
	imageDataURLs      []string
	skippedImages      int
	skippedImageErrors []string
	runState           *RunState
	runErr             *error
}

type RunState string

const (
	RunStateCompleted   RunState = "completed"
	RunStateInterrupted RunState = "interrupted"
	RunStateWaitingAuth RunState = "waiting_auth"
	RunStateIgnored     RunState = "ignored"
	RunStateFailed      RunState = "failed"
)

type Result struct {
	Response commands.Response
	Handled  bool
	State    RunState
	Err      error
}

func New(ctx context.Context, cfg Config, handler commands.Handler, httpClient *http.Client) (*Service, error) {
	authManager := cfg.AuthManager
	if authManager == nil {
		authManager = handler.Auth
	}
	var mcpClient *botmcp.Client
	if mcpBaseURL := strings.TrimSpace(cfg.MCPBaseURL); mcpBaseURL != "" {
		mcpClient = botmcp.New(mcpBaseURL, httpClient)
	}
	if !cfg.Enabled {
		return &Service{handler: handler, logger: cfg.Logger, mcpClient: mcpClient, auth: authManager, campusCatalog: newCampusCatalogCache()}, nil
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("agent enabled but OPENAI_API_KEY is empty")
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	baseURL := textutil.TrimTrailingSlash(cfg.BaseURL)
	agentHTTPClient := newAgentHTTPClient(httpClient, cfg.Logger)
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Model:      modelName,
		HTTPClient: agentHTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	service := &Service{
		handler:       handler,
		model:         chatModel,
		modelName:     modelName,
		enabled:       true,
		logger:        cfg.Logger,
		httpClient:    httpClient,
		mcpClient:     mcpClient,
		auth:          authManager,
		campusCatalog: newCampusCatalogCache(),
	}
	if premiumAPIKey := strings.TrimSpace(cfg.PremiumAPIKey); premiumAPIKey != "" {
		premiumName := strings.TrimSpace(cfg.PremiumModel)
		if premiumName == "" {
			premiumName = "kimi-k3"
		}
		premiumModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
			APIKey:          premiumAPIKey,
			BaseURL:         textutil.TrimTrailingSlash(cfg.PremiumBaseURL),
			Model:           premiumName,
			HTTPClient:      agentHTTPClient,
			ReasoningEffort: einoopenai.ReasoningEffortLevelLow,
		})
		if err != nil {
			return nil, fmt.Errorf("create premium chat model: %w", err)
		}
		service.premiumModel = premiumModel
		service.premiumName = premiumName
	}
	attachmentAPIKey := strings.TrimSpace(cfg.AttachmentAPIKey)
	if attachmentAPIKey == "" {
		attachmentAPIKey = strings.TrimSpace(cfg.PremiumAPIKey)
	}
	attachmentBaseURL := strings.TrimSpace(cfg.AttachmentBaseURL)
	if attachmentBaseURL == "" {
		attachmentBaseURL = cfg.PremiumBaseURL
	}
	if IsCompatibleKimiBaseURL(attachmentBaseURL) && attachmentAPIKey != "" {
		service.attachmentParser, err = NewAttachmentParser(AttachmentParserConfig{APIKey: attachmentAPIKey, BaseURL: attachmentBaseURL, HTTPClient: httpClient, Logger: cfg.Logger})
		if err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) Handle(ctx context.Context, input Input) (string, bool) {
	response, ok := s.HandleResponse(ctx, input)
	return response.Text, ok
}

func (s *Service) runOnce(ctx context.Context, input Input) Result {
	state := RunStateCompleted
	var runErr error
	input.runState = &state
	input.runErr = &runErr
	response, handled := s.HandleResponse(ctx, input)
	if !handled && state != RunStateInterrupted && state != RunStateFailed {
		state = RunStateIgnored
	}
	return Result{Response: response, Handled: handled, State: state, Err: runErr}
}

func markAgentInfrastructureFailure(input Input, err error) {
	if err == nil || input.JobID <= 0 {
		return
	}
	if input.runState != nil {
		*input.runState = RunStateFailed
	}
	if input.runErr != nil {
		*input.runErr = err
	}
}

func (s *Service) HandleResponse(ctx context.Context, input Input) (commands.Response, bool) {
	runStarted := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if input.JobID > 0 && strings.TrimSpace(input.JobLeaseToken) != "" {
		ctx = store.WithConversationJobLease(ctx, input.JobID, input.JobLeaseToken)
	}
	inputText := strings.TrimSpace(input.Text)
	if !s.Enabled() || (inputText == "" && len(input.ImageURLs) == 0 && len(input.Media) == 0 && len(input.Forwarded) == 0) {
		return commands.Response{}, false
	}
	parentCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	metrics := newRunMetrics()
	ctx = withRunMetrics(ctx, metrics)
	model, provider, modelName := s.modelFor()
	usage := &usageAccumulator{}
	ctx = withUsageAccumulator(ctx, usage)
	runID, runRecordErr := s.recordAgentRun(ctx, input, provider, modelName)
	if runRecordErr != nil && input.JobID > 0 {
		runRecordErr = fmt.Errorf("persist durable agent run: %w", runRecordErr)
		markAgentInfrastructureFailure(input, runRecordErr)
		return commands.Response{}, true
	}
	if runID > 0 && s.handler.Store != nil {
		ctx = withModelAttemptRecorder(ctx, func(attemptCtx context.Context) error {
			return s.handler.Store.RecordAgentModelAttempt(attemptCtx, runID)
		})
		ctx = withUsagePersister(ctx, func(usageCtx context.Context, actual tokenUsage) error {
			spending := spendingFor(provider, modelName, actual)
			return s.handler.Store.RecordAgentUsage(usageCtx, runID, spending)
		})
	}
	finishRun := func(status, reply string, runErr error) {
		runErr = normalizeAgentRunError(ctx, runErr)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parentCtx), agentRunCleanupTimeout)
		defer cleanupCancel()
		finishCtx := withRunMetrics(withUsageAccumulator(cleanupCtx, usage), metrics)
		s.finishAgentRun(finishCtx, runID, input.Identity, status, reply, runErr, provider, modelName, usage.snapshot(), time.Since(runStarted))
	}
	if err := callerContextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if asksForCapabilityInventory(input.Text) {
		if err := s.persistCurrentUserEvent(ctx, input); err != nil {
			markAgentInfrastructureFailure(input, err)
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		reply := commands.HelpOverview(store.IsSharedConversation(input.Identity))
		var err error
		if strings.Contains(strings.ToLower(input.Text), "mcp") || strings.Contains(input.Text, "内部工具") {
			reply, err = s.capabilityInventory(ctx, input.Identity)
		}
		if err != nil {
			err = normalizeAgentRunError(ctx, err)
			if errors.Is(err, context.Canceled) {
				finishRun(store.AgentRunStatusIgnored, "", err)
				return commands.Response{}, false
			}
			reply = agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		if err := s.persistAgentMessage(ctx, input, schema.AssistantMessage(reply, nil)); err != nil {
			markAgentInfrastructureFailure(input, err)
			failure := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, failure, err)
			return agentTextResponse(failure), true
		}
		finishRun(store.AgentRunStatusCompleted, reply, nil)
		return agentTextResponse(reply), true
	}
	if sharedConversationNeedsPrivateReply(input.Text, input.Identity) {
		reply := "这个问题涉及个人数据，请私聊 Presto 查询。"
		if err := s.persistCurrentUserEvent(ctx, input); err != nil {
			markAgentInfrastructureFailure(input, err)
			failure := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, failure, err)
			return agentTextResponse(failure), true
		}
		if err := s.persistAgentMessage(ctx, input, schema.AssistantMessage(reply, nil)); err != nil {
			markAgentInfrastructureFailure(input, err)
			failure := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, failure, err)
			return agentTextResponse(failure), true
		}
		finishRun(store.AgentRunStatusCompleted, reply, nil)
		return agentTextResponse(reply), true
	}
	if err := observeRunStage(ctx, "input_images", func() error {
		if err := s.prepareInputAttachments(ctx, &input); err != nil {
			return err
		}
		if input.preparedUserEvent != nil {
			return nil
		}
		return s.prepareInputImages(ctx, &input)
	}); err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := imageFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	var hostResponseDelivered atomic.Bool
	sendResponse := input.SendResponse
	if sendResponse != nil {
		sendResponse = func(ctx context.Context, ident store.Identity, response commands.Response) error {
			if err := input.SendResponse(ctx, ident, response); err != nil {
				return err
			}
			hostResponseDelivered.Store(true)
			return nil
		}
	}
	toolSetup, err := observeRunStageValue(ctx, "tool_setup", func() (struct {
		tools   []tool.BaseTool
		session *lazyMCPSession
	}, error) {
		tools, session, err := s.toolsFor(ctx, input.Identity, input.JobID, sendResponse)
		return struct {
			tools   []tool.BaseTool
			session *lazyMCPSession
		}{tools: tools, session: session}, err
	})
	tools := toolSetup.tools
	mcpSession := toolSetup.session
	if err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		if errors.Is(err, auth.ErrNotLoggedIn) {
			reply := agentLoginRequiredReply(runID)
			finishRun(store.AgentRunStatusCompleted, reply, nil)
			return agentTextResponse(reply), true
		}
		reply := s.mcpFailureReply(ctx, input.Identity, runID, err)
		if isMCPAuthorizationError(err) {
			finishRun(store.AgentRunStatusCompleted, reply, nil)
			return agentTextResponse(reply), true
		}
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if mcpSession != nil {
		defer func() { _ = mcpSession.Close() }()
	}
	if err := callerContextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	ctx = withToolOutcomes(ctx, newToolOutcomeRegistry())
	repeatGuard := newToolRepeatGuard()
	var handlers []adk.ChatModelAgentMiddleware
	if s.handler.Store != nil && store.HasConversationIdentity(input.Identity) {
		handlers = append(handlers, newConversationCompactionMiddleware(s, input.Identity))
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "presto_assistant",
		Description: "Presto, a Life @ USTC QQ assistant",
		Instruction: currentInstruction(),
		Model:       model,
		// ChatModelAgent treats zero as its default of 20 iterations. The
		// library's own unbounded ReAct graph uses math.MaxInt, so use that
		// supported upper bound instead of imposing an application loop cap.
		MaxIterations: math.MaxInt,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools, ExecuteSequentially: true,
				UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
					started := time.Now()
					defer func() { recordRunStage(ctx, "tool_call", time.Since(started)) }()
					unknownInput := &compose.ToolInput{Name: name, Arguments: input}
					if result, handled, err := repeatedUnknownToolResult(ctx, repeatGuard, unknownInput); handled || err != nil {
						return result, err
					}
					if err := admitToolCall(ctx); err != nil {
						return "", err
					}
					recordToolCall(ctx)
					result := toolresult.Encode("host", name, "not_found", time.Now(), nil, fmt.Errorf("未知工具：%s", name))
					toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
					// An unknown tool cannot satisfy the plan; retain its canonical
					// failure so a follow-up cannot retry it forever.
					repeatGuard.recordFailure(unknownInput)
					return result, nil
				},
				ToolCallMiddlewares: []compose.ToolMiddleware{{
					Invokable: s.toolReceiptMiddleware(input.Identity, input.JobID),
				}, {
					Invokable:  repeatGuard.invokableMiddleware,
					Streamable: repeatGuard.streamableMiddleware,
				}, {
					Invokable:  toolResultMiddleware(s.logf),
					Streamable: streamToolResultMiddleware(s.logf),
				}},
			},
		},
		Handlers: handlers,
	})
	if err != nil {
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	checkpointID := agentCheckpointID(input.JobID)
	var checkpointStore adk.CheckPointStore
	if s.handler.Store != nil && checkpointID != "" {
		bound, bindErr := s.handler.Store.AgentCheckpoints().Bind(store.AgentCheckpointClaim{
			JobID: input.JobID, Revision: input.JobRevision, LeaseToken: input.JobLeaseToken,
		})
		if bindErr != nil {
			// An agent job without its coordinator claim cannot safely read or
			// create a checkpoint. Continue without persistence only for callers
			// that are not running a durable conversation job.
			if input.JobID > 0 {
				markAgentInfrastructureFailure(input, bindErr)
				reply := agentFailureReply(runID, bindErr)
				finishRun(store.AgentRunStatusFailed, reply, bindErr)
				return agentTextResponse(reply), true
			}
		} else {
			checkpointStore = bound
		}
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: checkpointStore, EnableStreaming: true})
	resume := false
	if checkpointStore != nil {
		_, resume, err = checkpointStore.Get(ctx, checkpointID)
		if err != nil {
			markAgentInfrastructureFailure(input, err)
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
	}
	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if resume {
		iter, err = runner.Resume(ctx, checkpointID)
	} else {
		var messages []*schema.Message
		err = s.persistCurrentUserEvent(ctx, input)
		if err != nil {
			markAgentInfrastructureFailure(input, err)
		}
		if err == nil {
			messages, err = observeRunStageValue(ctx, "history_messages", func() ([]*schema.Message, error) {
				return s.messagesFor(ctx, input)
			})
			if err != nil {
				markAgentInfrastructureFailure(input, err)
			}
		}
		if err == nil {
			options := make([]adk.AgentRunOption, 0, 1)
			if checkpointID != "" {
				options = append(options, adk.WithCheckPointID(checkpointID))
			}
			iter = runner.Run(ctx, messages, options...)
		}
	}
	if err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		if isDurableAgentStateError(err) {
			markAgentInfrastructureFailure(input, err)
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}

	reply := ""
	repeatGuard.Reset()
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			runErr := normalizeAgentRunError(ctx, event.Err)
			if errors.Is(runErr, context.Canceled) {
				finishRun(store.AgentRunStatusIgnored, "", runErr)
				return commands.Response{}, false
			}
			if isDurableAgentStateError(runErr) {
				markAgentInfrastructureFailure(input, runErr)
				reply := agentFailureReply(runID, runErr)
				finishRun(store.AgentRunStatusFailed, reply, runErr)
				return agentTextResponse(reply), true
			}
			if errors.Is(runErr, auth.ErrNotLoggedIn) {
				reply = agentLoginRequiredReply(runID)
				finishRun(store.AgentRunStatusCompleted, reply, nil)
				return agentTextResponse(reply), true
			}
			reply := agentFailureReply(runID, runErr)
			if isMCPAuthorizationError(runErr) {
				reply = s.mcpFailureReply(ctx, input.Identity, runID, runErr)
				finishRun(store.AgentRunStatusCompleted, reply, nil)
				return agentTextResponse(reply), true
			}
			finishRun(store.AgentRunStatusFailed, reply, runErr)
			return agentTextResponse(reply), true
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			if input.runState != nil {
				*input.runState = RunStateInterrupted
				if capabilityInterruptKind(event.Action.Interrupted) == capabilityInterruptAuth {
					*input.runState = RunStateWaitingAuth
				}
			}
			finishRun(store.AgentRunStatusInterrupted, "", nil)
			return commands.Response{}, true
		}
		msg, _, err := adk.GetMessage(event)
		if err != nil {
			err = normalizeAgentRunError(ctx, err)
			if errors.Is(err, context.Canceled) {
				finishRun(store.AgentRunStatusIgnored, "", err)
				return commands.Response{}, false
			}
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		if msg == nil {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		candidateAnswer := msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 && content != ""
		if err := s.persistAgentMessage(ctx, input, msg); err != nil {
			markAgentInfrastructureFailure(input, err)
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		if candidateAnswer {
			reply = content
		}
	}
	if err := callerContextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if hostResponseDelivered.Load() {
		finishRun(store.AgentRunStatusCompleted, "", nil)
		return commands.Response{}, true
	}
	if reply == "" {
		finishRun(store.AgentRunStatusIgnored, "", nil)
		return commands.Response{}, false
	}
	response := s.responseFor(ctx, input, reply)
	if err := callerContextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if response.Text == "" && len(response.Parts) == 0 {
		if hostResponseDelivered.Load() {
			finishRun(store.AgentRunStatusCompleted, "", nil)
			return commands.Response{}, true
		}
		finishRun(store.AgentRunStatusIgnored, "", nil)
		return commands.Response{}, false
	}
	finishRun(store.AgentRunStatusCompleted, reply, nil)
	return response, true
}

func capabilityInterruptKind(info *adk.InterruptInfo) string {
	if info == nil {
		return ""
	}
	if value, ok := info.Data.(capabilityInterruptInfo); ok {
		return value.Kind
	}
	if value, ok := info.Data.(*capabilityInterruptInfo); ok && value != nil {
		return value.Kind
	}
	for _, interruptContext := range info.InterruptContexts {
		if interruptContext == nil || !interruptContext.IsRootCause {
			continue
		}
		if value, ok := interruptContext.Info.(capabilityInterruptInfo); ok {
			return value.Kind
		}
		if value, ok := interruptContext.Info.(*capabilityInterruptInfo); ok && value != nil {
			return value.Kind
		}
	}
	return ""
}

func agentTextResponse(text string) commands.Response {
	return commands.Response{Text: cleanQQReply(text), Kind: "agent"}
}

func agentLoginRequiredReply(runID int64) string {
	reply := "需要登录 Life @ USTC。请直接发送“登录”，完成后重新发送刚才的请求；本次没有执行任何查询或操作。"
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
}

func (s *Service) responseFor(ctx context.Context, input Input, reply string) commands.Response {
	return s.responseWithImageReferences(ctx, input.Identity, reply)
}

func (s *Service) messagesFor(ctx context.Context, input Input) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, conversationEventPageSize+1)
	hasCurrentEvent := false
	if s.handler.Store != nil {
		compaction, found, err := s.handler.Store.ConversationCompaction(ctx, input.Identity)
		if err != nil {
			return nil, err
		}
		events, err := s.historyEvents(ctx, input.Identity, compaction.CoveredEventID)
		if err != nil {
			return nil, err
		}
		if input.JobID > 0 {
			currentKey := fmt.Sprintf("conversation-job:%d:user", input.JobID)
			for _, event := range events {
				if event.JobID == input.JobID && event.DedupeKey == currentKey {
					hasCurrentEvent = true
					break
				}
			}
		}
		if found && compaction.CoveredEventID > 0 {
			if strings.TrimSpace(compaction.Summary) == "" {
				return nil, errors.New("conversation summary is missing")
			}
			messages = append(messages, conversationSummaryMessage(compaction.Summary, compaction.CoveredEventID))
		}
		messages = append(messages, conversationEventMessages(events)...)
	}
	// The current user event is persisted before this function is called. A
	// checkpoint-less retry therefore restores the persisted turn exactly once
	// instead of appending it to history a second time.
	if hasCurrentEvent {
		return messages, nil
	}
	if input.imageDataURLs == nil && len(input.ImageURLs) > 0 {
		if err := s.prepareInputImages(ctx, &input); err != nil {
			return nil, err
		}
	}
	event := currentUserEvent(input)
	messages = append(messages, messagesFromConversationEvents([]store.ConversationEvent{event})...)
	return messages, nil
}

// Read all uncompressed events in stable ascending pages. Capacity is handled
// by the model middleware, never by discarding history during database reads.
func (s *Service) historyEvents(ctx context.Context, ident store.Identity, afterID int64) ([]store.ConversationEvent, error) {
	var events []store.ConversationEvent
	for {
		page, err := s.handler.Store.ConversationEventsAfter(ctx, ident, afterID, conversationEventPageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		if len(page) < conversationEventPageSize {
			return events, nil
		}
		afterID = page[len(page)-1].ID
	}
}

func inputOccurredAt(input Input) time.Time {
	if !input.SentAt.IsZero() {
		return input.SentAt
	}
	if !input.ReceivedAt.IsZero() {
		return input.ReceivedAt
	}
	return time.Now().UTC()
}

func currentUserEvent(input Input) store.ConversationEvent {
	if input.preparedUserEvent != nil {
		return *input.preparedUserEvent
	}
	return store.ConversationEvent{
		Identity: input.Identity, ActorDisplayName: input.ActorDisplayName, Source: "agent", OccurredAt: inputOccurredAt(input),
		JobID: input.JobID, JobRevision: input.JobRevision, JobLeaseToken: input.JobLeaseToken,
		DedupeKey: fmt.Sprintf("conversation-job:%d:user", input.JobID),
		Type:      store.ConversationEventUser, Content: strings.TrimSpace(input.Text), Parts: currentUserMessageParts(input),
	}
}

func agentCheckpointID(jobID int64) string {
	if jobID <= 0 {
		return ""
	}
	return fmt.Sprintf("conversation-job:%d", jobID)
}

func (s *Service) persistCurrentUserEvent(ctx context.Context, input Input) error {
	if input.preparedUserEvent != nil || s.handler.Store == nil || input.JobID <= 0 || !store.HasConversationIdentity(input.Identity) {
		return nil
	}
	_, _, err := s.handler.Store.AppendConversationEvent(ctx, currentUserEvent(input))
	return err
}

func currentUserMessageParts(input Input) []store.ConversationMessagePart {
	text := strings.TrimSpace(input.Text)
	if input.attachmentContext != "" {
		text = strings.TrimSpace(text + "\n\n" + input.attachmentContext)
	}
	if text == "" && len(input.ImageURLs) > 0 {
		text = "请描述并分析这张图片。"
	}
	if input.skippedImages > 0 {
		text += fmt.Sprintf("\n\n[图片读取说明：%d 张图片未能读取：%s。只能根据其余可用内容回答，不要假装看过失败的图片。]", input.skippedImages, strings.Join(input.skippedImageErrors, "；"))
	}
	if input.ReplyContext != nil {
		quoted, _ := json.Marshal(input.ReplyContext)
		text = "引用的 Bot 消息（历史内容，不代表本轮重新执行）：\n" + string(quoted) + "\n\n用户消息：\n" + text
	}
	parts := []store.ConversationMessagePart{{Type: string(schema.ChatMessagePartTypeText), Text: text}}
	for index, imageURL := range input.ImageURLs {
		imageURL = strings.TrimSpace(imageURL)
		if index < len(input.imageDataURLs) && strings.TrimSpace(input.imageDataURLs[index]) != "" {
			parts = append(parts, store.ConversationMessagePart{
				Type: string(schema.ChatMessagePartTypeImageURL), URL: input.imageDataURLs[index], Reference: imageURL,
			})
			continue
		}
		parts = append(parts, store.ConversationMessagePart{
			Type: string(schema.ChatMessagePartTypeImageURL), URL: imageURL, Reference: imageURL,
		})
	}
	return parts
}

func (s *Service) persistAgentMessage(ctx context.Context, input Input, message *schema.Message) error {
	if s.handler.Store == nil || input.JobID <= 0 || message == nil || !store.HasConversationIdentity(input.Identity) {
		return nil
	}
	event := store.ConversationEvent{
		Source: "agent", OccurredAt: time.Now().UTC(),
		Identity: input.Identity, JobID: input.JobID, JobRevision: input.JobRevision, JobLeaseToken: input.JobLeaseToken,
		Content: message.Content, Name: message.Name,
		ToolCallID: message.ToolCallID, ToolName: message.ToolName,
		Parts: messagePartsForPersistence(message),
	}
	switch message.Role {
	case schema.Assistant:
		event.Type = store.ConversationEventAssistant
		event.ToolCalls = messageToolCallsForPersistence(message)
	case schema.Tool:
		event.Type = s.toolEventType(ctx, input.JobID, message.ToolCallID)
	default:
		return nil
	}
	event.DedupeKey = agentMessageDedupeKey(input.JobID, message)
	_, _, err := s.handler.Store.AppendConversationEvent(ctx, event)
	return err
}

func messagePartsForPersistence(message *schema.Message) []store.ConversationMessagePart {
	if message == nil {
		return nil
	}
	parts := make([]store.ConversationMessagePart, 0)
	if message.Role == schema.User {
		for _, part := range message.UserInputMultiContent {
			switch part.Type {
			case schema.ChatMessagePartTypeText:
				parts = append(parts, store.ConversationMessagePart{Type: string(part.Type), Text: part.Text})
			case schema.ChatMessagePartTypeImageURL:
				if part.Image == nil {
					continue
				}
				persisted := store.ConversationMessagePart{Type: string(part.Type), Detail: string(part.Image.Detail)}
				persisted.URL, persisted.Base64Data, persisted.MIMEType = messagePartCommonValues(part.Image.MessagePartCommon)
				parts = append(parts, persisted)
			}
		}
		return parts
	}
	if message.Role != schema.Assistant {
		return nil
	}
	for _, part := range message.AssistantGenMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			parts = append(parts, store.ConversationMessagePart{Type: string(part.Type), Text: part.Text})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil {
				continue
			}
			persisted := store.ConversationMessagePart{Type: string(part.Type)}
			persisted.URL, persisted.Base64Data, persisted.MIMEType = messagePartCommonValues(part.Image.MessagePartCommon)
			parts = append(parts, persisted)
		}
	}
	return parts
}

func messageToolCallsForPersistence(message *schema.Message) []store.ConversationToolCall {
	if message == nil || message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
		return nil
	}
	calls := make([]store.ConversationToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		calls = append(calls, store.ConversationToolCall{
			ID: call.ID, Type: call.Type, Index: call.Index, Name: call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return calls
}

func messagePartCommonValues(common schema.MessagePartCommon) (url, base64Data, mimeType string) {
	if common.URL != nil {
		url = *common.URL
	}
	if common.Base64Data != nil {
		base64Data = *common.Base64Data
	}
	return url, base64Data, common.MIMEType
}

func (s *Service) toolEventType(ctx context.Context, jobID int64, toolCallID string) store.ConversationEventType {
	executions, err := s.handler.Store.CapabilityExecutionsForJob(ctx, jobID)
	if err != nil {
		s.logf("classify tool transcript event failed: job_id=%d error=%v", jobID, err)
		return store.ConversationEventToolResult
	}
	denied := false
	for _, execution := range executions {
		if execution.ToolCallID != toolCallID {
			continue
		}
		switch execution.State {
		case store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
			return store.ConversationEventToolError
		case store.CapabilityExecutionDenied:
			denied = true
		}
	}
	if denied {
		return store.ConversationEventToolDenial
	}
	if toolOutcomesFromContext(ctx).isError(toolCallID) {
		return store.ConversationEventToolError
	}
	return store.ConversationEventToolResult
}

func agentMessageDedupeKey(jobID int64, message *schema.Message) string {
	if message != nil && message.Role == schema.Tool && strings.TrimSpace(message.ToolCallID) != "" {
		return agentToolResultDedupeKey(jobID, message.ToolCallID)
	}
	if id := strings.TrimSpace(adk.GetMessageID(message)); id != "" {
		return fmt.Sprintf("conversation-job:%d:message:%s", jobID, id)
	}
	payload, _ := json.Marshal(struct {
		Role       schema.RoleType                 `json:"role"`
		Content    string                          `json:"content"`
		Name       string                          `json:"name,omitempty"`
		ToolCalls  []store.ConversationToolCall    `json:"toolCalls,omitempty"`
		ToolCallID string                          `json:"toolCallID,omitempty"`
		ToolName   string                          `json:"toolName,omitempty"`
		Parts      []store.ConversationMessagePart `json:"parts,omitempty"`
	}{message.Role, message.Content, message.Name, messageToolCallsForPersistence(message), message.ToolCallID, message.ToolName, messagePartsForPersistence(message)})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("conversation-job:%d:message:%x", jobID, digest[:16])
}

func agentToolResultDedupeKey(jobID int64, toolCallID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(toolCallID)))
	return fmt.Sprintf("conversation-job:%d:tool-result:%x", jobID, digest[:16])
}

// Acknowledge removes a completed checkpoint only after the coordinator has
// atomically persisted the final output and terminal job state. The claim is
// required so a stale worker cannot delete a newer revision's checkpoint.
func (s *Service) Acknowledge(ctx context.Context, jobID int64, revision int, leaseToken string) error {
	checkpointID := agentCheckpointID(jobID)
	if s.handler.Store == nil || checkpointID == "" {
		return nil
	}
	bound, err := s.handler.Store.AgentCheckpoints().Bind(store.AgentCheckpointClaim{
		JobID: jobID, Revision: revision, LeaseToken: leaseToken,
	})
	if err != nil {
		return err
	}
	if err := bound.Delete(ctx, checkpointID); err != nil {
		return err
	}
	return bound.Delete(ctx, completedOutputKey(jobID))
}

const (
	commandSearchToolName = "search_bot_commands"
	capabilityToolName    = "run_bot_command"
	campusSearchToolName  = "search_campus_tools"
	campusCallToolName    = "call_campus_tool"
)

type emptyInput struct{}

type hostCapabilityInput struct {
	Capability string   `json:"capability" jsonschema_description:"Exact stable capability ID returned by search_bot_commands"`
	Arguments  []string `json:"arguments,omitempty" jsonschema_description:"Capability arguments only; do not repeat the capability name"`
}

type commandSearchInput struct {
	Query string `json:"query" jsonschema_description:"Concrete user intent, command name, or capability ID to search for"`
}

type botCommandInput struct {
	Command string `json:"command" jsonschema_description:"Complete Bot command exactly as a user would type it, following the command manual"`
}

func hostCapabilityToolDescription(shared bool) string {
	boundary := ""
	if shared {
		boundary = "This is a shared conversation; private capabilities are unavailable.\n"
	}
	return boundary + "Execute one Bot command through the same parser used by users. Returns formatted structured JSON business data and saved image IDs in images[].id. Images are not automatically sent: include ![](id) in the final answer to select an image, using its exact returned ID. The markup never executes commands. Dangerous operations require user confirmation. Searching first is optional.\n\n" + commands.CommandManual(shared)
}

func (s *Service) runBotCommand(ctx context.Context, input botCommandInput, ident store.Identity, jobID int64, sendResponse func(context.Context, store.Identity, commands.Response) error) (string, error) {
	// A resumed call must resolve the saved operation, never reparse model input.
	if interrupted, _, _ := tool.GetInterruptState[capabilityInterruptState](ctx); interrupted {
		return s.invokeHostCapability(ctx, hostCapabilityInput{}, ident, jobID, sendResponse)
	}
	if commands.HasAdditionalCommandLine(input.Command) {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
		return toolresult.Encode("bot", input.Command, "invalid_input", time.Now(), nil, errors.New("send exactly one command per tool call")), nil
	}
	parsed := commands.ParseCommand(input.Command)
	if !parsed.Recognized() {
		toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
		return toolresult.Encode("bot", input.Command, "invalid_input", time.Now(), nil, errors.New("unknown command; consult the command manual or search_bot_commands")), nil
	}
	return s.invokeHostCapability(ctx, hostCapabilityInput{Capability: string(parsed.Invocation.ID()), Arguments: parsed.Invocation.Args}, ident, jobID, sendResponse)
}

func searchCommandDocumentation(ident store.Identity, input commandSearchInput) (string, error) {
	if strings.TrimSpace(input.Query) == "" {
		return "", botmcp.NewRecoverableToolError(commandSearchToolName, "请提供具体查询意图，例如：本学期已选课程、明天课表、关闭作业提醒。")
	}
	documentation := commands.SearchCapabilityDocumentation(input.Query, commands.CapabilitySearchOptions{
		SharedConversation: store.IsSharedConversation(ident),
		Limit:              5,
	})
	encoded, err := json.Marshal(documentation)
	if err != nil {
		return "", fmt.Errorf("encode command documentation: %w", err)
	}
	return string(encoded), nil
}

func (s *Service) toolsFor(
	ctx context.Context,
	ident store.Identity,
	jobID int64,
	sendResponse func(context.Context, store.Identity, commands.Response) error,
) ([]tool.BaseTool, *lazyMCPSession, error) {
	tools := make([]tool.BaseTool, 0)
	var err error
	var mcpSession *lazyMCPSession
	if !store.IsSharedConversation(ident) && s.mcpClient != nil && s.auth != nil {
		mcpSession = newLazyMCPSession(s, ident, jobID)
		mcpSession.sendResponse = sendResponse
		tools, err = mcpSession.appendTools(tools)
		if err != nil {
			_ = mcpSession.Close()
			return nil, nil, err
		}
	}

	tools, err = appendInferredTool(tools, "search_bot_commands", "Optionally search command names, aliases, descriptions, parameters and examples. Returns multiple ranked candidates with executable examples. Use the command manual to call run_bot_command directly when its syntax is known. Shared conversations return public documentation only.", func(_ context.Context, input commandSearchInput) (string, error) {
		return searchCommandDocumentation(ident, input)
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	tools, err = appendInferredTool(tools, capabilityToolName, hostCapabilityToolDescription(store.IsSharedConversation(ident)), func(ctx context.Context, input botCommandInput) (string, error) {
		return s.runBotCommand(ctx, input, ident, jobID, sendResponse)
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	tools, err = appendInferredTool(tools, "get_current_time", "Get the current local time in Asia/Shanghai.", func(_ context.Context, _ emptyInput) (string, error) {
		now := time.Now()
		return toolresult.Encode("host", "get_current_time", "succeeded", now, map[string]any{"time": now.In(shanghaiLocation), "timezone": "Asia/Shanghai"}, nil), nil
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	return tools, mcpSession, nil
}

func (s *Service) recordAgentRun(ctx context.Context, input Input, provider, model string) (int64, error) {
	if s.handler.Store == nil {
		if input.JobID > 0 {
			return 0, errors.New("durable agent run store is unavailable")
		}
		return 0, nil
	}
	if !store.HasConversationIdentity(input.Identity) {
		if input.JobID > 0 {
			return 0, errors.New("durable agent run identity is incomplete")
		}
		return 0, nil
	}
	rawText := strings.TrimSpace(input.Text)
	if rawText == "" && len(input.ImageURLs) > 0 {
		rawText = "[image]"
	}
	id, err := s.handler.Store.RecordAgentRun(ctx, input.Identity, store.AgentRun{
		JobID:    input.JobID,
		RawText:  rawText,
		Provider: provider,
		Model:    model,
		Currency: store.SpendingCurrencyCNY,
	})
	if err != nil {
		s.logf("record agent run failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, err)
		return 0, err
	}
	s.logf("llm run started: id=%d provider=%s model=%s platform=%s conversation_type=%s conversation_id=%s images=%d",
		id, provider, model, input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, len(input.ImageURLs))
	return id, nil
}

func (s *Service) mcpFailureReply(ctx context.Context, ident store.Identity, runID int64, err error) string {
	if isMCPAuthorizationError(err) {
		if !errors.Is(err, auth.ErrReauthorizationRequired) && s.auth != nil {
			hasCurrentScopes, scopeErr := s.auth.HasCurrentScopes(ctx, ident)
			if scopeErr != nil {
				s.logf("check OAuth scopes after MCP authorization failure failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
					ident.Platform, ident.ConversationType, ident.ConversationID, scopeErr)
			} else if hasCurrentScopes {
				return "校园工具拒绝了当前登录权限，请稍后重试。本次没有执行任何查询或操作。"
			}
		}
		return "登录权限已失效。请直接发送“登录”重新授权；本次没有执行任何查询或操作。"
	}
	reply := "校园工具暂时不可用，请稍后重试。本次没有执行任何查询或操作。"
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
}

func isMCPAuthorizationError(err error) bool {
	return errors.Is(err, auth.ErrReauthorizationRequired) || botmcp.IsAuthorizationRequired(err)
}

func (s *Service) finishAgentRun(ctx context.Context, id int64, ident store.Identity, status, reply string, err error, provider, model string, usage tokenUsage, duration time.Duration) {
	spending := spendingFor(provider, model, usage)
	metrics := runMetricsFromContext(ctx).snapshot()
	if metrics.modelRequests > spending.ModelRequests {
		spending.ModelRequests = metrics.modelRequests
	}
	failureClass := "none"
	if err != nil {
		failureClass = agentFailureClass(err)
	}
	s.logf("llm run completed: id=%d status=%s provider=%s model=%s prompt_tokens=%d cached_tokens=%d completion_tokens=%d total_tokens=%d model_requests=%d tool_calls=%d estimated_cost_cny=%.6f duration_ms=%d failure_class=%s context_tokens=%d stage_input_images_ms=%d stage_tool_setup_ms=%d stage_history_messages_ms=%d stage_model_request_ms=%d stage_tool_call_ms=%d stage_model_response_headers_ms=%d stage_model_response_body_ms=%d stage_history_compaction_ms=%d stage_model_first_text_ms=%d",
		id, status, provider, model, spending.PromptTokens, spending.CachedTokens, spending.CompletionTokens, spending.TotalTokens,
		spending.ModelRequests, spending.ToolCalls, float64(spending.CostNanoCNY)/1_000_000_000, duration.Milliseconds(), failureClass,
		metrics.contextTokens, metrics.stageMilliseconds["input_images"], metrics.stageMilliseconds["tool_setup"], metrics.stageMilliseconds["history_messages"], metrics.stageMilliseconds["model_request"], metrics.stageMilliseconds["tool_call"], metrics.stageMilliseconds["model_response_headers"], metrics.stageMilliseconds["model_response_body"], metrics.stageMilliseconds["history_compaction"], metrics.stageMilliseconds["model_first_text"])
	if err != nil {
		s.logf("agent run failed: id=%d status=%s error=%v", id, status, err)
	}
	if s.handler.Store == nil || id <= 0 {
		return
	}
	if finishErr := s.handler.Store.FinishAgentRun(ctx, id, status, reply, err, spending); finishErr != nil {
		s.logf("finish agent run failed: id=%d status=%s error=%v", id, status, finishErr)
		return
	}
	conversationTotal, conversationErr := s.handler.Store.ConversationSpending(ctx, ident)
	userTotal, userErr := s.handler.Store.UserSpending(ctx, ident)
	if conversationErr != nil || userErr != nil {
		s.logf("read llm spending totals failed: id=%d conversation_error=%v user_error=%v", id, conversationErr, userErr)
		return
	}
	s.logf("llm spending totals: id=%d conversation_cost_cny=%.6f user_cost_cny=%.6f",
		id, float64(conversationTotal.CostNanoCNY)/1_000_000_000, float64(userTotal.CostNanoCNY)/1_000_000_000)
}

func (s *Service) modelFor() (*einoopenai.ChatModel, string, string) {
	if s.premiumModel != nil {
		return s.premiumModel, "kimi", s.premiumName
	}
	return s.model, "openai-compatible", s.modelName
}

func appendInferredTool[I any](tools []tool.BaseTool, name, description string, fn func(context.Context, I) (string, error)) ([]tool.BaseTool, error) {
	t, err := utils.InferTool(name, description, fn)
	if err != nil {
		return nil, err
	}
	return append(tools, t), nil
}

func cleanQQReply(reply string) string {
	lines := strings.Split(reply, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 {
				blank = true
			}
			continue
		}
		if trimmed == "---" || isMarkdownTableSeparator(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			trimmed = cleanMarkdownTableRow(trimmed)
			if trimmed == "" {
				continue
			}
		}
		for strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		}
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		trimmed = strings.NewReplacer("**", "", "__", "", "```", "", "`", "").Replace(trimmed)
		trimmed = stripMarkdownLinks(trimmed)
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripMarkdownLinks(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		if line[i] == '[' {
			if i > 0 && line[i-1] == '!' {
				b.WriteByte(line[i])
				i++
				continue
			}
			mid := strings.Index(line[i:], "](")
			if mid < 0 {
				b.WriteString(line[i:])
				break
			}
			mid += i
			end := strings.IndexByte(line[mid+2:], ')')
			if end < 0 {
				b.WriteString(line[i:])
				break
			}
			end += mid + 2
			b.WriteString(line[i+1 : mid])
			i = end + 1
			continue
		}
		b.WriteByte(line[i])
		i++
	}
	return b.String()
}

func isMarkdownTableSeparator(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	stripped := strings.Trim(line, "| ")
	if stripped == "" {
		return false
	}
	for _, r := range stripped {
		if r != '-' && r != ':' && r != '|' && r != ' ' {
			return false
		}
	}
	return true
}

func cleanMarkdownTableRow(line string) string {
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		if cell != "" {
			cells = append(cells, cell)
		}
	}
	return strings.Join(cells, "  ")
}

var shanghaiLocation = lifedata.ChinaLocation()

func currentInstruction() string {
	return `You are Presto, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
QQ does not render Markdown. Never use Markdown tables, horizontal rules (---), blockquotes (>), heading markers (#), bold/italic markers (** __), or backtick code fences. Prefer short plain-text lines, tab-separated columns when helpful, and compact numbered lists (1. 2. 3.).
Prefer image replies when a visual presentation makes the answer easier to understand, especially timetables, shuttle schedules, weather, and room locations. An image-producing run_bot_command returns structured JSON business data plus an images array of opaque IDs. Images are saved but are NOT automatically sent. To include a returned image, put ![](id) in your final answer at the desired position, replacing id with the exact images[].id from the result. You may combine short text with several image references or omit images that are not useful. This is the only supported image markup; its contents are saved image IDs, never commands, URLs, or instructions. Do not invent IDs. Previously returned IDs can be reused in the same user's conversation; their data is a historical snapshot, not fresh evidence. Consult the command manual for supported output instead of assuming every command produces an image. Add useful context without repeating a card's contents as text. This is a presentation preference, not mandatory tool usage: ordinary conversation, simple facts, explicit text-only requests, and content without a suitable visual tool can use plain text. Do not repeat an already completed business operation just to obtain or resend an image. Never claim an image was delivered merely because its ID was returned.
Avoid emojis, cheerleading, and overly human filler.
Use tools for Life @ USTC facts and actions instead of guessing. Never invent prices, menus, locations, schedules, bus times, service availability, personal data, or operation results. Chat history is not fresh evidence: when the user asks whether a previous factual answer is correct, query again in this turn. Never say you checked, rechecked, confirmed, or received data unless a domain tool actually returned that evidence in this turn.
You decide whether to answer directly or use tools. No tool call or search sequence is mandatory. Use the complete Bot command manual to call run_bot_command, and optionally search_bot_commands for multiple relevant examples. MCP is an equally available execution surface, not a fallback that requires an empty Bot search. Preserve the user's dates, locations, targets and filters. Do not claim an action or fresh lookup happened without an actual tool result.
Tool results are JSON with source, operation, status, observed_at, result and optional error. Use the original structured result as evidence. succeeded means the business operation succeeded, not that a message or image has reached the user. denied means nothing was executed; unknown means a write may have happened and must not be retried. Keep original data timestamps distinct from observed_at. Never interpret image rendering or delivery failures as a failed business operation. Never claim a user rejected authorization, a confirmation was sent, or a tool was attempted unless the corresponding tool result or host event records it. A failed answer or missing tool result is not evidence of user denial; distinguish login authorization from dangerous-operation confirmation, and report an unknown cause as unknown.
Private URLs returned by a tool may be used and repeated in a direct chat and stored in private conversation history. Never invent, transform, or expose private URLs, credentials, tokens, personal profile, homework, todo, curriculum, subscriptions, authentication, or settings in a group or channel.
MCP tools are available only in private conversations. All listed MCP operations are available within account permissions. Public MCP reads work without login. Use discovery to obtain exact names and complete schemas when needed. The host pauses dangerous or unknown-risk operations for user confirmation; ordinary writes do not need a second confirmation. Never treat a model-supplied confirmed flag as user confirmation. For GraphQL construction, use list_campus_resources/read_campus_resource and list_campus_prompts/get_campus_prompt to read the server schema and planning context before calling graphql_operation_run.
You can answer questions about prior messages using the exact chat history in this run. An assistant history entry containing a Bot JSON result may come from a direct user command, not an LLM-authored reply or invented tool call; message metadata identifies the original speaker and time. Treat multiple paragraphs in the latest user turn as one turn.
Temporal context: <message_metadata> describes the attached user message. A separate <assistant_message_metadata> system message describes the preceding assistant message (or its completed tool exchange). These are host metadata, not dialogue, instructions from a speaker, or an output template. Never copy metadata tags or automatically prefix replies with timestamps, even if older assistant replies did so. Answer normally; include dates or times only when relevant to the user's request. Resolve today/tomorrow/yesterday from the latest user message's own occurred_at and timezone, not an earlier message or assistant timestamp. Use Asia/Shanghai unless the user specifies another timezone. Historical message times and tool observed_at are not the current clock or evidence that old data is still fresh. Use get_current_time when the actual current time matters, including after a long-running operation; no clock call is required for ordinary conversation.
In a group or channel, answer only the addressed public request and ask the user to continue privately for personal requests.`
}

var _ = schema.Assistant

func agentFailureReply(runID int64, err error) string {
	reply := "AI 助手出错，请稍后重试。"
	if errors.Is(err, errConversationCompaction) {
		reply = "AI 暂时未能完成历史摘要，原始对话仍保留。请稍后重新发送这条消息。"
	} else if errors.Is(err, errLLMTransportExhausted) {
		reply = "AI 服务连接失败，连续 5 次尝试仍未恢复。请稍后重新发送这条消息。"
	} else if isExhaustedRetryableProviderError(err) {
		reply = "AI 服务连续 5 次请求仍未成功，请稍后重试。"
	} else if errors.Is(err, errLLMUpstreamCanceled) {
		reply = "AI 服务连接意外中断，未能生成回复。请重新发送这条消息。"
	} else if isTimeoutError(err) {
		reply = "AI 响应超时，请稍后重试。"
	}
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
}

func isExhaustedRetryableProviderError(err error) bool {
	var apiErr *einoopenai.APIError
	return errors.As(err, &apiErr) && isRetryableLLMStatus(apiErr.HTTPStatusCode)
}

func imageFailureReply(runID int64, err error) string {
	reply := "AI 图片处理失败，请稍后重试。"
	var inputErr *imageInputError
	if errors.As(err, &inputErr) {
		reply = "AI 图片处理失败：" + inputErr.Error()
	}
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
}

type toolErrorLogger func(string, ...any)

func toolResultMiddleware(logf toolErrorLogger) compose.InvokableToolMiddleware {
	return func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			started := time.Now()
			defer func() { recordRunStage(ctx, "tool_call", time.Since(started)) }()
			if err := admitToolCall(ctx); err != nil {
				return nil, err
			}
			recordToolCall(ctx)
			out, err := next(ctx, input)
			if err != nil {
				if logf != nil {
					logf("agent tool call failed: name=%s call_id=%s error=%s", input.Name, input.CallID, textutil.SafeLogError(err))
				}
				if result, ok := botmcp.ModelToolErrorResult(err); ok {
					toolOutcomesFromContext(ctx).markError(input.CallID)
					return &compose.ToolOutput{Result: toolresult.Encode(toolResultSource(input.Name), input.Name, "failed", time.Now(), nil, errors.New(result))}, nil
				}
				return nil, err
			}
			if out != nil && !toolresult.IsEncoded(out.Result) {
				status := "succeeded"
				var resultErr error
				data := toolresult.Data(out.Result)
				if toolOutcomesFromContext(ctx).isError(input.CallID) {
					status, data, resultErr = "failed", nil, errors.New(out.Result)
				}
				out.Result = toolresult.Encode(toolResultSource(input.Name), input.Name, status, time.Now(), data, resultErr)
			}
			return out, nil
		}
	}
}

func streamToolResultMiddleware(logf toolErrorLogger) compose.StreamableToolMiddleware {
	return func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			started := time.Now()
			defer func() { recordRunStage(ctx, "tool_call", time.Since(started)) }()
			if err := admitToolCall(ctx); err != nil {
				return nil, err
			}
			recordToolCall(ctx)
			out, err := next(ctx, input)
			if err != nil {
				if logf != nil {
					logf("agent streaming tool call failed: name=%s call_id=%s error=%s", input.Name, input.CallID, textutil.SafeLogError(err))
				}
				if result, ok := botmcp.ModelToolErrorResult(err); ok {
					toolOutcomesFromContext(ctx).markError(input.CallID)
					return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{toolresult.Encode(toolResultSource(input.Name), input.Name, "failed", time.Now(), nil, errors.New(result))})}, nil
				}
				return nil, err
			}
			if out == nil || out.Result == nil {
				return out, nil
			}
			var result strings.Builder
			for {
				chunk, recvErr := out.Result.Recv()
				if errors.Is(recvErr, io.EOF) {
					break
				}
				if recvErr != nil {
					out.Result.Close()
					if logf != nil {
						logf("agent streaming tool result failed: name=%s call_id=%s error=%s", input.Name, input.CallID, textutil.SafeLogError(recvErr))
					}
					if safe, ok := botmcp.ModelToolErrorResult(recvErr); ok {
						toolOutcomesFromContext(ctx).markError(input.CallID)
						return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{toolresult.Encode(toolResultSource(input.Name), input.Name, "failed", time.Now(), nil, errors.New(safe))})}, nil
					}
					return nil, recvErr
				}
				result.WriteString(chunk)
			}
			out.Result.Close()
			raw := result.String()
			if !toolresult.IsEncoded(raw) {
				status := "succeeded"
				var resultErr error
				data := toolresult.Data(raw)
				if toolOutcomesFromContext(ctx).isError(input.CallID) {
					status, data, resultErr = "failed", nil, errors.New(raw)
				}
				raw = toolresult.Encode(toolResultSource(input.Name), input.Name, status, time.Now(), data, resultErr)
			}
			return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{raw})}, nil
		}
	}
}

// repeatedUnknownToolResult applies the same repeat/idempotence policy as the
// regular tool middleware. UnknownToolsHandler runs outside that middleware,
// so it translates duplicate calls into the shared structured result itself.
func repeatedUnknownToolResult(ctx context.Context, guard *toolRepeatGuard, input *compose.ToolInput) (string, bool, error) {
	if err := guard.admit(input); err == nil {
		return "", false, nil
	} else {
		if input != nil {
			toolOutcomesFromContext(ctx).markError(input.CallID)
		}
		return repeatGuardToolResult(input, err), true, nil
	}
}

func toolResultSource(name string) string {
	if name == capabilityToolName || name == commandSearchToolName {
		return "bot"
	}
	if strings.Contains(name, "campus") {
		return "mcp"
	}
	return "host"
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded")
}

func (s *Service) logf(format string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Print(textutil.SafeLogText(fmt.Sprintf(format, args...)))
	}
}
