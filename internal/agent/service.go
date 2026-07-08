package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
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

	MCPBaseURL  string
	AuthManager *auth.Manager
}

type Service struct {
	handler commands.Handler
	model   *einoopenai.ChatModel
	enabled bool
	timeout time.Duration
	logger  *log.Logger

	mcpClient *botmcp.Client
	auth      *auth.Manager
}

type Input struct {
	Text       string
	Identity   store.Identity
	SendUpdate func(context.Context, store.Identity, string) error
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
		return &Service{handler: handler, timeout: timeout, logger: cfg.Logger, mcpClient: mcpClient, auth: authManager}, nil
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
	return &Service{handler: handler, model: chatModel, enabled: true, timeout: timeout, logger: cfg.Logger, mcpClient: mcpClient, auth: authManager}, nil
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) Handle(ctx context.Context, input Input) (string, bool) {
	inputText := strings.TrimSpace(input.Text)
	if !s.Enabled() || inputText == "" || store.IsGroupConversation(input.Identity) {
		return "", false
	}
	runID := s.recordAgentRun(ctx, input)
	traceEnabled, err := s.toolTraceEnabled(ctx, input.Identity)
	if err != nil {
		reply := "AI 工具设置读取失败：" + err.Error()
		s.finishAgentRun(ctx, runID, store.AgentRunStatusFailed, reply, err)
		return reply, true
	}
	var trace *toolTraceNotifier
	if traceEnabled {
		trace = &toolTraceNotifier{ident: input.Identity, send: input.SendUpdate}
	}
	tools, err := s.toolsFor(ctx, input.Identity, trace, input.SendUpdate)
	if err != nil {
		if errors.Is(err, auth.ErrNotLoggedIn) {
			reply := "需要先登录。发送：登录"
			s.finishAgentRun(ctx, runID, store.AgentRunStatusCompleted, reply, nil)
			return reply, true
		}
		reply := "AI 工具初始化失败：" + err.Error()
		s.finishAgentRun(ctx, runID, store.AgentRunStatusFailed, reply, err)
		return reply, true
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "life_ustc_assistant",
		Description:   "Life @ USTC QQ assistant",
		Instruction:   currentInstruction(),
		Model:         s.model,
		MaxIterations: agentMaxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
				UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
					return fmt.Sprintf("未知工具：%s", name), nil
				},
				ToolCallMiddlewares: []compose.ToolMiddleware{{
					Invokable:  toolErrorCatchingMiddleware,
					Streamable: streamToolErrorCatchingMiddleware,
				}},
			},
		},
	})
	if err != nil {
		reply := "AI 助手初始化失败：" + err.Error()
		s.finishAgentRun(ctx, runID, store.AgentRunStatusFailed, reply, err)
		return reply, true
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	messages, err := s.messagesFor(ctx, input)
	if err != nil {
		reply := "AI 历史记录读取失败：" + err.Error()
		s.finishAgentRun(ctx, runID, store.AgentRunStatusFailed, reply, err)
		return reply, true
	}
	iter := runner.Run(ctx, messages)
	reply := ""
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			reply := agentFailureReply(runID, event.Err)
			s.finishAgentRun(ctx, runID, store.AgentRunStatusFailed, reply, event.Err)
			return reply, true
		}
		msg, _, err := adk.GetMessage(event)
		if err != nil || msg == nil {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content != "" {
			reply = content
		}
	}
	if reply == "" {
		s.finishAgentRun(ctx, runID, store.AgentRunStatusIgnored, "", nil)
		return "", false
	}
	reply = cleanQQReply(reply)
	s.finishAgentRun(ctx, runID, store.AgentRunStatusCompleted, reply, nil)
	return reply, true
}

func (s *Service) messagesFor(ctx context.Context, input Input) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, historyTurnLimit*2+1)
	if s.handler.Store != nil {
		history, err := s.handler.Store.RecentHandledInteractions(ctx, input.Identity, historyTurnLimit)
		if err != nil {
			return nil, err
		}
		for _, turn := range history {
			rawText := compactHistoryText(turn.RawText)
			if rawText == "" {
				continue
			}
			messages = append(messages, schema.UserMessage(rawText))
			if reply := compactHistoryText(turn.Reply); reply != "" {
				messages = append(messages, schema.AssistantMessage(reply, nil))
			}
		}
	}
	messages = append(messages, schema.UserMessage(strings.TrimSpace(input.Text)))
	return messages, nil
}

type emptyInput struct{}

type feedbackInput struct {
	Category string `json:"category,omitempty" jsonschema_description:"Short category for the feedback, such as missing_tool, bad_result, typo, or api_gap"`
	Content  string `json:"content" jsonschema_description:"Concrete feedback about missing tools, wrong behavior, tool/API gaps, or user interaction problems"`
	Context  string `json:"context,omitempty" jsonschema_description:"Relevant user message, tool result, or short context that explains why this feedback matters"`
}

type messagePartInput struct {
	Content string `json:"content" jsonschema_description:"One intermediate QQ message to send before the final response"`
}

type toolTraceNotifier struct {
	mu    sync.Mutex
	ident store.Identity
	send  func(context.Context, store.Identity, string) error
}

func (r *toolTraceNotifier) Notify(ctx context.Context, name string, input any, result string, err error) {
	if r == nil {
		return
	}
	message := "工具调用：" + name
	if args := formatToolArgs(input); args != "" {
		message += " " + args
	}
	message += "\n工具结果："
	if err != nil {
		message += "\n失败：" + formatToolResult(err.Error())
	} else if formatted := formatToolResult(result); formatted != "" {
		message += "\n" + formatted
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.send != nil {
		_ = r.send(ctx, r.ident, message)
	}
}

func (s *Service) toolTraceEnabled(ctx context.Context, ident store.Identity) (bool, error) {
	if s.handler.Store == nil {
		return false, nil
	}
	settings, err := s.handler.Store.AgentSettings(ctx, ident)
	if err != nil {
		return false, err
	}
	return settings.ExposeToolCalls, nil
}

func (s *Service) toolsFor(ctx context.Context, ident store.Identity, trace *toolTraceNotifier, sendUpdate func(context.Context, store.Identity, string) error) ([]tool.BaseTool, error) {
	tools := make([]tool.BaseTool, 0)
	var err error
	if s.mcpClient != nil && s.auth != nil {
		token, err := s.auth.AccessToken(ctx, ident)
		if err != nil {
			return nil, fmt.Errorf("get MCP access token: %w", err)
		}
		mcpTools, err := s.mcpClient.Tools(ctx, token)
		if err != nil {
			return nil, err
		}
		einoTools, err := botmcp.ToEinoTools(mcpTools, func(ctx context.Context, name string, args map[string]any) (string, error) {
			token, err := s.auth.AccessToken(ctx, ident)
			if err != nil {
				if trace != nil {
					trace.Notify(ctx, name, args, "", err)
				}
				if errors.Is(err, auth.ErrNotLoggedIn) {
					return "需要先登录。发送：登录", nil
				}
				return "", err
			}
			result, err := s.mcpClient.Call(ctx, token, name, args)
			if trace != nil {
				trace.Notify(ctx, name, args, result, err)
			}
			return result, err
		})
		if err != nil {
			return nil, err
		}
		tools = append(tools, einoTools...)
	}

	if s.handler.Store != nil {
		tools, err = appendInferredTool(tools, "record_bot_feedback", "Record feedback about missing LLM tools, bad tool results, typo handling gaps, API gaps, or user interaction problems for maintainers to review.", trace, func(ctx context.Context, input feedbackInput) (string, error) {
			return s.recordBotFeedback(ctx, ident, input)
		})
		if err != nil {
			return nil, err
		}
	}
	if sendUpdate != nil {
		tools, err = appendInferredTool(tools, "send_message_part", "Send one intermediate QQ message when a long answer should be split. After using this, put only the remaining content in the final answer.", trace, func(ctx context.Context, input messagePartInput) (string, error) {
			return sendMessagePart(ctx, ident, sendUpdate, input)
		})
		if err != nil {
			return nil, err
		}
	}
	tools, err = appendInferredTool(tools, "get_current_time", "Get the current local time in Asia/Shanghai.", trace, func(_ context.Context, _ emptyInput) (string, error) {
		return currentTimeMessage(), nil
	})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func (s *Service) recordAgentRun(ctx context.Context, input Input) int64 {
	if s.handler.Store == nil || !store.HasConversationIdentity(input.Identity) {
		return 0
	}
	id, err := s.handler.Store.RecordAgentRun(ctx, input.Identity, input.Text)
	if err != nil {
		s.logf("record agent run failed: platform=%s conversation_type=%s conversation_id=%s error=%v",
			input.Identity.Platform, input.Identity.ConversationType, input.Identity.ConversationID, err)
		return 0
	}
	return id
}

func (s *Service) finishAgentRun(ctx context.Context, id int64, status, reply string, err error) {
	if err != nil {
		s.logf("agent run failed: id=%d status=%s error=%v", id, status, err)
	}
	if s.handler.Store == nil || id <= 0 {
		return
	}
	if finishErr := s.handler.Store.FinishAgentRun(ctx, id, status, reply, err); finishErr != nil {
		s.logf("finish agent run failed: id=%d status=%s error=%v", id, status, finishErr)
	}
}

func (s *Service) recordBotFeedback(ctx context.Context, ident store.Identity, input feedbackInput) (string, error) {
	if s.handler.Store == nil {
		return "", errors.New("feedback store is unavailable")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return "", errors.New("feedback content is required")
	}
	id, err := s.handler.Store.RecordFeedback(ctx, ident, store.FeedbackRecord{
		Source:   "llm",
		Category: input.Category,
		Content:  content,
		Context:  input.Context,
	})
	if err != nil {
		return "", err
	}
	sent := s.sendFeedbackToAdmins(ctx, ident, id, input)
	if sent > 0 {
		if id > 0 {
			return fmt.Sprintf("已记录反馈 #%d，并已转给维护者。", id), nil
		}
		return "已记录反馈，并已转给维护者。", nil
	}
	if id > 0 {
		return fmt.Sprintf("已记录反馈 #%d。", id), nil
	}
	return "已记录反馈。", nil
}

func (s *Service) sendFeedbackToAdmins(ctx context.Context, ident store.Identity, id int64, input feedbackInput) int {
	if s.handler.FeedbackSend == nil || (len(s.handler.FeedbackUsers) == 0 && len(s.handler.FeedbackGroups) == 0) {
		return 0
	}
	message := formatAgentFeedbackMessage(ident, id, input)
	sent := 0
	for _, userID := range s.handler.FeedbackUsers {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if err := s.handler.FeedbackSend(ctx, store.Identity{
			Platform:         ident.Platform,
			UserID:           userID,
			ConversationType: "private",
			ConversationID:   userID,
		}, message); err != nil {
			s.logf("send llm feedback failed: id=%d platform=%s target=private:%s error=%v", id, ident.Platform, userID, err)
			continue
		}
		sent++
	}
	for _, groupID := range s.handler.FeedbackGroups {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if err := s.handler.FeedbackSend(ctx, store.Identity{
			Platform:         ident.Platform,
			ConversationType: "group",
			ConversationID:   groupID,
		}, message); err != nil {
			s.logf("send llm feedback failed: id=%d platform=%s target=group:%s error=%v", id, ident.Platform, groupID, err)
			continue
		}
		sent++
	}
	if sent > 0 && s.handler.Store != nil && id > 0 {
		if err := s.handler.Store.MarkFeedbackSent(ctx, id); err != nil {
			s.logf("mark llm feedback sent failed: id=%d error=%v", id, err)
		}
	}
	return sent
}

func formatAgentFeedbackMessage(ident store.Identity, id int64, input feedbackInput) string {
	source := strings.TrimSpace(ident.ConversationType)
	if ident.ConversationID != "" {
		source += ":" + ident.ConversationID
	}
	if source == "" {
		source = "unknown"
	}
	userID := strings.TrimSpace(ident.UserID)
	if userID == "" {
		userID = "unknown"
	}
	lines := []string{
		"LLM 反馈",
		"来源：" + source,
		"用户：" + userID,
	}
	if category := strings.TrimSpace(input.Category); category != "" {
		lines = append(lines, "分类："+category)
	}
	lines = append(lines, "内容："+strings.TrimSpace(input.Content))
	if contextText := strings.TrimSpace(input.Context); contextText != "" {
		lines = append(lines, "上下文："+contextText)
	}
	if id > 0 {
		lines = append(lines, fmt.Sprintf("编号：#%d", id))
	}
	return strings.Join(lines, "\n")
}

func sendMessagePart(ctx context.Context, ident store.Identity, send func(context.Context, store.Identity, string) error, input messagePartInput) (string, error) {
	if send == nil {
		return "", errors.New("message sender is unavailable")
	}
	content := cleanQQReply(input.Content)
	if strings.TrimSpace(content) == "" {
		return "", errors.New("message content is required")
	}
	if err := send(ctx, ident, content); err != nil {
		return "", err
	}
	return "已发送。", nil
}

func appendInferredTool[I any](tools []tool.BaseTool, name, description string, trace *toolTraceNotifier, fn func(context.Context, I) (string, error)) ([]tool.BaseTool, error) {
	wrapped := func(ctx context.Context, input I) (string, error) {
		result, err := fn(ctx, input)
		if trace != nil {
			trace.Notify(ctx, name, input, result, err)
		}
		return result, err
	}
	t, err := utils.InferTool(name, description, wrapped)
	if err != nil {
		return nil, err
	}
	return append(tools, t), nil
}

func formatToolArgs(input any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%v", input)
	}
	args := strings.TrimSpace(string(data))
	if args == "" || args == "{}" || args == "null" {
		return ""
	}
	const maxRunes = 300
	runes := []rune(args)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return args
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
		trimmed = strings.NewReplacer("**", "", "__", "", "`", "").Replace(trimmed)
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
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

func formatToolResult(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	const maxRunes = 1000
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "\n..."
	}
	return value
}

const historyTurnLimit = 20
const agentMaxIterations = 12
const agentHTTPTimeout = 60 * time.Second
const maxHistoryTextRunes = 1200

var shanghaiLocation = lifedata.ChinaLocation()

func currentInstruction() string {
	return currentInstructionAt(time.Now())
}

func currentInstructionAt(now time.Time) string {
	return fmt.Sprintf(`You are SiGNAL_BOT, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
QQ does not render Markdown tables well. Do not use Markdown tables, horizontal rules, blockquotes, or heading markers. Use short plain-text lines and compact numbered lists.
Avoid emojis, cheerleading, and overly human filler.
Use tools for Life @ USTC facts instead of guessing.
Current local time is %s.
You can answer questions about prior messages using the chat history provided in this run.
For bus planning after a class or event, pass the class/event end time to get_next_bus.after so the bus result is after that time.
Tools that create, update, delete, complete, subscribe, or change notification settings only prepare confirmation commands. Do not claim those changes are done until the user replies ok or sends the confirmation command.
When multiple confirmation commands are needed, tell the user to confirm one at a time with ok, or send exactly one command per QQ message. Do not ask the user to paste multiple commands in one message.
If you notice a missing tool, bad result, typo handling gap, API gap, or recurring interaction problem, call record_bot_feedback with concrete context.
For long replies, you may call send_message_part once, then put only the remaining content in the final answer.
Do not expose private profile, homework, todo, or curriculum data unless the user asks in this private chat.
For group chats, this agent is disabled by the host application.
When a tool returns login-required text, tell the user to log in with 登录.`, now.In(shanghaiLocation).Format("2006-01-02 15:04 MST"))
}

func currentTimeMessage() string {
	return currentTimeMessageAt(time.Now())
}

func currentTimeMessageAt(now time.Time) string {
	return now.In(shanghaiLocation).Format("现在是 2006-01-02 15:04，Asia/Shanghai。")
}

var _ = schema.Assistant

func normalizedAgentTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return agentHTTPTimeout
	}
	return timeout
}

func compactHistoryText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxHistoryTextRunes {
		return text
	}
	return string(runes[:maxHistoryTextRunes]) + "\n...(历史内容已截断)"
}

func agentFailureReply(runID int64, err error) string {
	reply := "AI 助手出错，请稍后重试。"
	if isTimeoutError(err) {
		reply = "AI 响应超时，请稍后重试。"
	}
	if runID > 0 {
		reply += fmt.Sprintf("\n记录 #%d", runID)
	}
	return reply
}

func toolErrorCatchingMiddleware(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		out, err := next(ctx, input)
		if err != nil {
			return &compose.ToolOutput{Result: "工具调用失败：" + err.Error()}, nil
		}
		return out, nil
	}
}

func streamToolErrorCatchingMiddleware(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		out, err := next(ctx, input)
		if err != nil {
			return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"工具调用失败：" + err.Error()})}, nil
		}
		return out, nil
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
