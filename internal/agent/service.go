package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	botfeedback "github.com/Life-USTC/Bot/internal/feedback"
	"github.com/Life-USTC/Bot/internal/lifedata"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Config struct {
	Enabled bool
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
	Logger  *log.Logger

	PremiumAPIKey  string
	PremiumBaseURL string
	PremiumModel   string
	MCPBaseURL     string
	AuthManager    *auth.Manager
	Feedback       botfeedback.Recorder
}

type Service struct {
	handler      commands.Handler
	model        *einoopenai.ChatModel
	modelName    string
	premiumModel *einoopenai.ChatModel
	premiumName  string
	enabled      bool
	timeout      time.Duration
	logger       *log.Logger
	httpClient   *http.Client

	mcpClient *botmcp.Client
	auth      *auth.Manager
	feedback  botfeedback.Recorder
}

type Input struct {
	Text      string
	ImageURLs []string
	Identity  store.Identity
	JobID     int64
	// SendResponse lets a local tool hand an already formatted host response
	// directly to the application when its presentation cannot be reproduced
	// from plain model text (for example, an image).
	SendResponse func(context.Context, store.Identity, commands.Response) error

	imageDataURLs []string
	runState      *RunState
}

type RunState string

const (
	RunStateCompleted   RunState = "completed"
	RunStateInterrupted RunState = "interrupted"
	RunStateWaitingAuth RunState = "waiting_auth"
	RunStateIgnored     RunState = "ignored"
)

type Result struct {
	Response commands.Response
	Handled  bool
	State    RunState
}

func New(ctx context.Context, cfg Config, handler commands.Handler, httpClient *http.Client) (*Service, error) {
	timeout := normalizedAgentTimeout(cfg.Timeout)
	authManager := cfg.AuthManager
	if authManager == nil {
		authManager = handler.Auth
	}
	var mcpClient *botmcp.Client
	if mcpBaseURL := strings.TrimSpace(cfg.MCPBaseURL); mcpBaseURL != "" {
		mcpClient = botmcp.New(mcpBaseURL, httpClient)
	}
	if !cfg.Enabled {
		return &Service{handler: handler, timeout: timeout, logger: cfg.Logger, mcpClient: mcpClient, auth: authManager, feedback: cfg.Feedback}, nil
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
	agentHTTPClient := newAgentHTTPClient(httpClient, timeout, cfg.Logger)
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Model:      modelName,
		HTTPClient: agentHTTPClient,
		Timeout:    timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	service := &Service{
		handler:    handler,
		model:      chatModel,
		modelName:  modelName,
		enabled:    true,
		timeout:    timeout,
		logger:     cfg.Logger,
		httpClient: agentHTTPClient,
		mcpClient:  mcpClient,
		auth:       authManager,
		feedback:   cfg.Feedback,
	}
	if premiumAPIKey := strings.TrimSpace(cfg.PremiumAPIKey); premiumAPIKey != "" {
		premiumName := strings.TrimSpace(cfg.PremiumModel)
		if premiumName == "" {
			premiumName = "kimi-k3"
		}
		premiumModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
			APIKey:              premiumAPIKey,
			BaseURL:             textutil.TrimTrailingSlash(cfg.PremiumBaseURL),
			Model:               premiumName,
			HTTPClient:          agentHTTPClient,
			Timeout:             timeout,
			MaxCompletionTokens: intPointer(kimiMaxCompletionTokens),
			ReasoningEffort:     einoopenai.ReasoningEffortLevelLow,
		})
		if err != nil {
			return nil, fmt.Errorf("create premium chat model: %w", err)
		}
		service.premiumModel = premiumModel
		service.premiumName = premiumName
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

// Run exposes the durable state transition needed by the coordinator while
// HandleResponse remains the response-level API used by focused callers.
func (s *Service) Run(ctx context.Context, input Input) Result {
	state := RunStateCompleted
	input.runState = &state
	response, handled := s.HandleResponse(ctx, input)
	if !handled && state != RunStateInterrupted {
		state = RunStateIgnored
	}
	return Result{Response: response, Handled: handled, State: state}
}

func (s *Service) HandleResponse(ctx context.Context, input Input) (commands.Response, bool) {
	runStarted := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	inputText := strings.TrimSpace(input.Text)
	if !s.Enabled() || (inputText == "" && len(input.ImageURLs) == 0) {
		return commands.Response{}, false
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(parentCtx, agentRunDeadline)
	defer cancel()
	metrics := newRunMetrics()
	priorModelAttempts := int64(0)
	if s.handler.Store != nil && input.JobID > 0 {
		prior, err := s.handler.Store.AgentJobSpending(ctx, input.JobID)
		if err != nil {
			return agentTextResponse(agentFailureReply(0, fmt.Errorf("read prior agent budget: %w", err))), true
		}
		priorModelAttempts = prior.ModelRequests
	}
	budget := newRunBudgetWithAttempts(runStarted, metrics, priorModelAttempts)
	ctx = withRunBudget(ctx, budget)
	ctx = withRunMetrics(ctx, metrics)
	model, provider, modelName := s.modelFor()
	usage := &usageAccumulator{}
	ctx = withUsageAccumulator(ctx, usage)
	runID := s.recordAgentRun(ctx, input, provider, modelName)
	finishRun := func(status, reply string, runErr error) {
		runErr = normalizeAgentRunError(ctx, budget, runErr)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parentCtx), agentRunCleanupTimeout)
		defer cleanupCancel()
		finishCtx := withRunMetrics(withUsageAccumulator(cleanupCtx, usage), metrics)
		s.finishAgentRun(finishCtx, runID, input.Identity, status, reply, runErr, provider, modelName, usage.snapshot(), time.Since(runStarted))
	}
	if err := budget.contextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, budget, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if err := observeRunStage(ctx, "input_images", func() error {
		return s.prepareInputImages(ctx, &input)
	}); err != nil {
		err = normalizeAgentRunError(ctx, budget, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		if isAgentBudgetError(err) {
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
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
		err = normalizeAgentRunError(ctx, budget, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
		}
		if isAgentBudgetError(err) {
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		if errors.Is(err, auth.ErrNotLoggedIn) {
			reply := s.beginLoginForInput(ctx, input)
			finishRun(store.AgentRunStatusCompleted, reply, nil)
			return agentLoginResponse(reply), true
		}
		reply := s.mcpFailureReply(ctx, input.Identity, runID, err)
		if isMCPAuthorizationError(err) {
			if strings.HasPrefix(reply, "登录权限已失效。") {
				reply = s.beginLoginForInput(ctx, input)
				finishRun(store.AgentRunStatusCompleted, reply, nil)
				return agentLoginResponse(reply), true
			}
			finishRun(store.AgentRunStatusCompleted, reply, nil)
			return agentTextResponse(reply), true
		}
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	if mcpSession != nil {
		defer func() { _ = mcpSession.Close() }()
	}
	if err := budget.contextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, budget, err)
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
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "presto_assistant",
		Description:   "Presto, a Life @ USTC QQ assistant",
		Instruction:   currentInstruction(),
		Model:         model,
		MaxIterations: agentMaxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools, ExecuteSequentially: true,
				UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
					started := time.Now()
					defer func() { recordRunStage(ctx, "tool_call", time.Since(started)) }()
					unknownInput := &compose.ToolInput{Name: name, Arguments: input}
					if err := repeatGuard.admit(unknownInput); err != nil {
						return "", err
					}
					if err := admitToolCall(ctx); err != nil {
						return "", err
					}
					recordToolCall(ctx)
					result := fmt.Sprintf("未知工具：%s", name)
					toolOutcomesFromContext(ctx).markError(compose.GetToolCallID(ctx))
					// An unknown tool cannot satisfy the plan; retain its canonical
					// failure so a follow-up cannot retry it forever.
					repeatGuard.recordFailure(unknownInput)
					return result, nil
				},
				ToolCallMiddlewares: []compose.ToolMiddleware{{
					Invokable:  repeatGuard.invokableMiddleware,
					Streamable: repeatGuard.streamableMiddleware,
				}, {
					Invokable:  toolResultMiddleware(s.logf),
					Streamable: streamToolResultMiddleware(s.logf),
				}},
			},
		},
	})
	if err != nil {
		reply := agentFailureReply(runID, err)
		finishRun(store.AgentRunStatusFailed, reply, err)
		return agentTextResponse(reply), true
	}
	checkpointID := agentCheckpointID(input.JobID)
	var checkpointStore adk.CheckPointStore
	if s.handler.Store != nil && checkpointID != "" {
		checkpointStore = s.handler.Store.AgentCheckpoints()
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: checkpointStore})
	resume := false
	if checkpointStore != nil {
		_, resume, err = checkpointStore.Get(ctx, checkpointID)
		if err != nil {
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
		messages, err = observeRunStageValue(ctx, "history_messages", func() ([]*schema.Message, error) {
			return s.messagesFor(ctx, input)
		})
		if err == nil {
			err = s.persistCurrentUserEvent(ctx, input)
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
		err = normalizeAgentRunError(ctx, budget, err)
		if errors.Is(err, context.Canceled) {
			finishRun(store.AgentRunStatusIgnored, "", err)
			return commands.Response{}, false
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
			runErr := normalizeAgentRunError(ctx, budget, event.Err)
			if errors.Is(runErr, context.Canceled) {
				finishRun(store.AgentRunStatusIgnored, "", runErr)
				return commands.Response{}, false
			}
			if errors.Is(runErr, auth.ErrNotLoggedIn) {
				reply = s.beginLoginForInput(ctx, input)
				finishRun(store.AgentRunStatusCompleted, reply, nil)
				return agentLoginResponse(reply), true
			}
			reply := agentFailureReply(runID, runErr)
			if isMCPAuthorizationError(runErr) {
				reply = s.mcpFailureReply(ctx, input.Identity, runID, runErr)
				if strings.HasPrefix(reply, "登录权限已失效。") {
					reply = s.beginLoginForInput(ctx, input)
					finishRun(store.AgentRunStatusCompleted, reply, nil)
					return agentLoginResponse(reply), true
				}
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
		if err != nil || msg == nil {
			continue
		}
		if err := s.persistAgentMessage(ctx, input, msg); err != nil {
			reply := agentFailureReply(runID, err)
			finishRun(store.AgentRunStatusFailed, reply, err)
			return agentTextResponse(reply), true
		}
		content := strings.TrimSpace(msg.Content)
		if content != "" {
			reply = content
		}
	}
	if err := budget.contextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, budget, err)
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
	if err := budget.contextError(ctx); err != nil {
		err = normalizeAgentRunError(ctx, budget, err)
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
	finishRun(store.AgentRunStatusCompleted, response.Text, nil)
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

func (s *Service) beginLoginForInput(ctx context.Context, input Input) string {
	response, err := s.handler.BeginLoginForRequest(ctx, commands.Input{
		Text: input.Text, Identity: input.Identity, SuppressLog: true,
	})
	if err != nil {
		s.logf("start resumable agent login failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, err)
		return "登录暂时无法开始，请稍后重试。"
	}
	return response.Text
}

func agentTextResponse(text string) commands.Response {
	return commands.Response{Text: cleanQQReply(text), Kind: "agent"}
}

func agentLoginResponse(text string) commands.Response {
	return commands.Response{Text: cleanQQReply(text), Kind: commands.ResponseKindAuthWait}
}

func (s *Service) responseFor(ctx context.Context, input Input, reply string) commands.Response {
	_ = ctx
	_ = input
	return commands.Response{Text: cleanQQReply(reply), Kind: "agent"}
}

func (s *Service) messagesFor(ctx context.Context, input Input) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, conversationEventLimit+1)
	if s.handler.Store != nil {
		events, err := s.handler.Store.RecentConversationEvents(ctx, input.Identity, conversationEventLimit)
		if err != nil {
			return nil, err
		}
		messages = append(messages, conversationEventMessages(events)...)
	}
	currentText := strings.TrimSpace(input.Text)
	if len(input.ImageURLs) == 0 {
		messages = append(messages, schema.UserMessage(withCurrentTimePrefix(currentText)))
		return messages, nil
	}
	if currentText == "" {
		currentText = "请描述并分析这张图片。"
	}
	currentText = withCurrentTimePrefix(currentText)
	parts := []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText,
		Text: currentText,
	}}
	imageDataURLs := input.imageDataURLs
	if imageDataURLs == nil {
		imageDataURLs = make([]string, 0, len(input.ImageURLs))
		for _, imageURL := range input.ImageURLs {
			dataURL, err := s.loadImageDataURL(ctx, imageURL)
			if err != nil {
				return nil, err
			}
			imageDataURLs = append(imageDataURLs, dataURL)
		}
	}
	for _, dataURL := range imageDataURLs {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &dataURL},
			},
		})
	}
	messages = append(messages, &schema.Message{Role: schema.User, UserInputMultiContent: parts})
	return messages, nil
}

func agentCheckpointID(jobID int64) string {
	if jobID <= 0 {
		return ""
	}
	return fmt.Sprintf("conversation-job:%d", jobID)
}

func (s *Service) persistCurrentUserEvent(ctx context.Context, input Input) error {
	if s.handler.Store == nil || input.JobID <= 0 || !store.HasConversationIdentity(input.Identity) {
		return nil
	}
	content := strings.TrimSpace(input.Text)
	if content == "" && len(input.ImageURLs) > 0 {
		content = "[image]"
	}
	_, _, err := s.handler.Store.AppendConversationEvent(ctx, store.ConversationEvent{
		Identity: input.Identity, JobID: input.JobID,
		DedupeKey: fmt.Sprintf("conversation-job:%d:user", input.JobID),
		Type:      store.ConversationEventUser, Content: content,
	})
	return err
}

func (s *Service) persistAgentMessage(ctx context.Context, input Input, message *schema.Message) error {
	if s.handler.Store == nil || input.JobID <= 0 || message == nil || !store.HasConversationIdentity(input.Identity) {
		return nil
	}
	event := store.ConversationEvent{
		Identity: input.Identity, JobID: input.JobID, Content: message.Content,
		ToolCallID: message.ToolCallID, ToolName: message.ToolName,
	}
	switch message.Role {
	case schema.Assistant:
		event.Type = store.ConversationEventAssistant
		event.ToolCalls = make([]store.ConversationToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			event.ToolCalls = append(event.ToolCalls, store.ConversationToolCall{
				ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
	case schema.Tool:
		event.Type = s.toolEventType(ctx, input.JobID, message.ToolCallID)
	default:
		return nil
	}
	event.DedupeKey = agentMessageDedupeKey(input.JobID, message)
	_, _, err := s.handler.Store.AppendConversationEvent(ctx, event)
	return err
}

func (s *Service) toolEventType(ctx context.Context, jobID int64, toolCallID string) store.ConversationEventType {
	if toolOutcomesFromContext(ctx).isError(toolCallID) {
		return store.ConversationEventToolError
	}
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
	return store.ConversationEventToolResult
}

func agentMessageDedupeKey(jobID int64, message *schema.Message) string {
	if id := strings.TrimSpace(adk.GetMessageID(message)); id != "" {
		return fmt.Sprintf("conversation-job:%d:message:%s", jobID, id)
	}
	payload, _ := json.Marshal(struct {
		Role       schema.RoleType   `json:"role"`
		Content    string            `json:"content"`
		ToolCalls  []schema.ToolCall `json:"toolCalls,omitempty"`
		ToolCallID string            `json:"toolCallID,omitempty"`
		ToolName   string            `json:"toolName,omitempty"`
	}{message.Role, message.Content, message.ToolCalls, message.ToolCallID, message.ToolName})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("conversation-job:%d:message:%x", jobID, digest[:16])
}

// Acknowledge removes a completed checkpoint only after the coordinator has
// atomically persisted the final output and terminal job state.
func (s *Service) Acknowledge(ctx context.Context, jobID int64) error {
	checkpointID := agentCheckpointID(jobID)
	if s.handler.Store == nil || checkpointID == "" {
		return nil
	}
	return s.handler.Store.AgentCheckpoints().Delete(ctx, checkpointID)
}

type emptyInput struct{}

type feedbackInput struct {
	Category string `json:"category,omitempty" jsonschema_description:"Short category for the feedback, such as missing_tool, bad_result, typo, or api_gap"`
	Content  string `json:"content" jsonschema_description:"Concrete feedback about missing tools, wrong behavior, tool/API gaps, or user interaction problems"`
	Context  string `json:"context,omitempty" jsonschema_description:"Relevant user message, tool result, or short context that explains why this feedback matters"`
}

type hostCapabilityInput struct {
	Capability string   `json:"capability" jsonschema_description:"Exact stable capability ID returned by search_bot_commands"`
	Arguments  []string `json:"arguments,omitempty" jsonschema_description:"Capability arguments only; do not repeat the capability name"`
}

type commandSearchInput struct {
	Query string `json:"query" jsonschema_description:"Concrete user intent, command name, or capability ID to search for"`
}

func hostCapabilityToolDescription(shared bool) string {
	description := "Invoke one Bot capability using the exact capability ID and arguments returned by search_bot_commands. The result is the actual domain result, without a status wrapper. The host pauses before each independently reversible mutation, asks the user for confirmation, and resumes this tool after the decision; never simulate approval or announce success before the resumed result. Authentication is also host-owned and resumes automatically."
	if shared {
		description += " This is a shared conversation; private capabilities are unavailable."
	}
	return description
}

func searchCommandDocumentation(ident store.Identity, input commandSearchInput) (string, error) {
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
		tools, err = mcpSession.appendTools(tools)
		if err != nil {
			_ = mcpSession.Close()
			return nil, nil, err
		}
	}

	if s.handler.Store != nil {
		tools, err = appendInferredTool(tools, "record_bot_feedback", "Record feedback about missing LLM tools, bad tool results, typo handling gaps, API gaps, or user interaction problems for maintainers to review.", func(ctx context.Context, input feedbackInput) (string, error) {
			return s.recordBotFeedback(ctx, ident, input)
		})
		if err != nil {
			if mcpSession != nil {
				_ = mcpSession.Close()
			}
			return nil, nil, err
		}
	}
	tools, err = appendInferredTool(tools, "search_bot_commands", "Search the Bot command registry for exact capability IDs, arguments, examples, confirmation policy, and audience scope. Search before invoking a capability; shared conversations return public commands only.", func(_ context.Context, input commandSearchInput) (string, error) {
		return searchCommandDocumentation(ident, input)
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	tools, err = appendInferredTool(tools, "invoke_bot_capability", hostCapabilityToolDescription(store.IsSharedConversation(ident)), func(ctx context.Context, input hostCapabilityInput) (string, error) {
		return s.invokeHostCapability(ctx, input, ident, jobID, sendResponse)
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	tools, err = appendInferredTool(tools, "get_current_time", "Get the current local time in Asia/Shanghai.", func(_ context.Context, _ emptyInput) (string, error) {
		return currentTimeMessage(), nil
	})
	if err != nil {
		if mcpSession != nil {
			_ = mcpSession.Close()
		}
		return nil, nil, err
	}
	return tools, mcpSession, nil
}

func (s *Service) recordAgentRun(ctx context.Context, input Input, provider, model string) int64 {
	if s.handler.Store == nil || !store.HasConversationIdentity(input.Identity) {
		return 0
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
		return 0
	}
	s.logf("llm run started: id=%d provider=%s model=%s platform=%s conversation_type=%s conversation_id=%s images=%d",
		id, provider, model, input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, len(input.ImageURLs))
	return id
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
		if s.auth != nil {
			if logoutErr := s.auth.Logout(ctx, ident); logoutErr != nil {
				s.logf("clear credential requiring reauthorization failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
					ident.Platform, ident.ConversationType, ident.ConversationID, logoutErr)
			}
		}
		return "登录权限已失效。\n系统将重新申请校园工具所需的正确权限；本次没有执行任何查询或操作。"
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
	s.logf("llm run completed: id=%d status=%s provider=%s model=%s prompt_tokens=%d cached_tokens=%d completion_tokens=%d total_tokens=%d model_requests=%d tool_calls=%d estimated_cost_cny=%.6f duration_ms=%d failure_class=%s context_tokens=%d stage_input_images_ms=%d stage_tool_setup_ms=%d stage_history_messages_ms=%d stage_model_request_ms=%d stage_tool_call_ms=%d",
		id, status, provider, model, spending.PromptTokens, spending.CachedTokens, spending.CompletionTokens, spending.TotalTokens,
		spending.ModelRequests, spending.ToolCalls, float64(spending.CostNanoCNY)/1_000_000_000, duration.Milliseconds(), failureClass,
		metrics.contextTokens, metrics.stageMilliseconds["input_images"], metrics.stageMilliseconds["tool_setup"], metrics.stageMilliseconds["history_messages"], metrics.stageMilliseconds["model_request"], metrics.stageMilliseconds["tool_call"])
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

func (s *Service) recordBotFeedback(ctx context.Context, ident store.Identity, input feedbackInput) (string, error) {
	if s.feedback == nil {
		return "", errors.New("feedback service is unavailable")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return "", errors.New("feedback content is required")
	}
	result, err := s.feedback.Record(ctx, ident, botfeedback.Submission{
		Source:   botfeedback.SourceLLM,
		Category: input.Category,
		Content:  content,
		Context:  input.Context,
	})
	if err != nil {
		return "", err
	}
	if result.AdminIntents > 0 {
		return fmt.Sprintf("已记录反馈 #%d，并已转给维护者。", result.ID), nil
	}
	return fmt.Sprintf("已记录反馈 #%d。", result.ID), nil
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

const agentMaxIterations = 32
const agentHTTPTimeout = 60 * time.Second
const kimiMaxCompletionTokens = 8_192

var shanghaiLocation = lifedata.ChinaLocation()

func currentInstruction() string {
	return currentInstructionAt(time.Now())
}

func currentInstructionAt(now time.Time) string {
	_ = now // kept for tests/call sites; wall-clock time is injected per-turn, not here (prompt-cache stable).
	return `You are Presto, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
QQ does not render Markdown. Never use Markdown tables, horizontal rules (---), blockquotes (>), heading markers (#), bold/italic markers (** __), or backtick code fences. Prefer short plain-text lines, tab-separated columns when helpful, and compact numbered lists (1. 2. 3.).
Avoid emojis, cheerleading, and overly human filler.
Use tools for Life @ USTC facts and actions instead of guessing. Never invent prices, menus, locations, schedules, bus times, service availability, or operation results.
Search search_bot_commands with the concrete intent before using invoke_bot_capability. Use the exact capability ID and arguments it returns, preserving every user constraint such as dates, times, filters, targets, and direction. Call tools yourself; never ask the user to type or repeat a command.
Tool results are literal evidence. The capability tool returns the actual domain result, not a success envelope. Do not add facts, infer completion, or claim a lookup or mutation happened beyond that exact result.
The host owns confirmations. A mutation tool call pauses while the host asks the real user, then resumes with the operation result or an explicit denial. Never ask for or simulate confirmation yourself. Grouped mutations are confirmed one operation at a time.
The host also owns authentication. If a tool pauses for login, wait for the automatic resume; never request, repeat, or invent a verification code.
Private URLs returned by a tool may be used and repeated in a direct chat and stored in private conversation history. Never invent, transform, or expose private URLs, credentials, tokens, personal profile, homework, todo, curriculum, subscriptions, authentication, or settings in a group or channel.
MCP tools are read-only supplements. Prefer a Bot capability when both layers cover the request. If no capability supports a requested mutation, say so and record concrete feedback; never improvise a write through another tool.
You can answer questions about prior messages using the exact chat history in this run. Treat multiple paragraphs in the latest user turn as one turn.
If you notice a missing tool, bad result, typo handling gap, API gap, or recurring interaction problem, call record_bot_feedback with concrete context in the same turn. Never ask whether to record feedback.
In a group or channel, answer only the addressed public request and ask the user to continue privately for personal requests.`
}

func currentTimeMessage() string {
	return currentTimeMessageAt(time.Now())
}

func currentTimeMessageAt(now time.Time) string {
	return now.In(shanghaiLocation).Format("现在是 2006-01-02 15:04，Asia/Shanghai。")
}

func withCurrentTimePrefix(text string) string {
	text = strings.TrimSpace(text)
	prefix := currentTimeMessage()
	if text == "" {
		return prefix
	}
	return prefix + "\n\n" + text
}

var _ = schema.Assistant

func normalizedAgentTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return agentHTTPTimeout
	}
	return timeout
}

func intPointer(value int) *int {
	return &value
}

func agentFailureReply(runID int64, err error) string {
	reply := "AI 助手出错，请稍后重试。"
	if errors.Is(err, errAgentRunDeadline) {
		reply = "AI 运行超过 60 秒，已停止。请缩小请求范围后重试。"
	} else if errors.Is(err, errAgentContextBudget) {
		reply = "AI 上下文过长，已停止。请缩短历史或拆分问题后重试。"
	} else if errors.Is(err, errAgentToolCallBudget) {
		reply = "AI 工具调用次数达到上限，已停止。请缩小请求范围后重试。"
	} else if errors.Is(err, errAgentModelAttemptBudget) {
		reply = "AI 服务仍然繁忙，5 次尝试后已停止。请稍后重试。"
	} else if errors.Is(err, errAgentNonProgress) {
		reply = "AI 工具计划没有取得进展，已停止。请换一种说法或缩小请求范围后重试。"
	} else if errors.Is(err, errRepeatedToolCall) {
		reply = "AI 重复调用了相同工具，已停止。请换一种说法或缩小请求范围后重试。"
	} else if isAgentIterationLimitError(err) {
		reply = "AI 工具调用过多，已停止。请缩小请求范围后重试。"
	} else if isTimeoutError(err) {
		reply = "AI 响应超时，请稍后重试。"
	}
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
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

func isAgentIterationLimitError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "max iterations")
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
					logf("agent tool call failed: name=%s call_id=%s error=%v", input.Name, input.CallID, err)
				}
				if result, ok := botmcp.ModelToolErrorResult(err); ok {
					toolOutcomesFromContext(ctx).markError(input.CallID)
					return &compose.ToolOutput{Result: result}, nil
				}
				return nil, err
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
					logf("agent streaming tool call failed: name=%s call_id=%s error=%v", input.Name, input.CallID, err)
				}
				if result, ok := botmcp.ModelToolErrorResult(err); ok {
					toolOutcomesFromContext(ctx).markError(input.CallID)
					return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{result})}, nil
				}
				return nil, err
			}
			return out, nil
		}
	}
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
		s.logger.Printf(format, args...)
	}
}
