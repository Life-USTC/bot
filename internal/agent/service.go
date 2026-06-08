package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Config struct {
	Enabled bool
	APIKey  string
	BaseURL string
	Model   string
}

type Service struct {
	handler commands.Handler
	model   *einoopenai.ChatModel
	enabled bool
}

type Input struct {
	Text       string
	Identity   store.Identity
	SendUpdate func(context.Context, store.Identity, string) error
}

func New(ctx context.Context, cfg Config, handler commands.Handler, httpClient *http.Client) (*Service, error) {
	if !cfg.Enabled {
		return &Service{handler: handler}, nil
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
	agentHTTPClient := httpClient
	if httpClient != nil {
		clone := *httpClient
		clone.Timeout = agentHTTPTimeout
		agentHTTPClient = &clone
	}
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     apiKey,
		BaseURL:    baseURL,
		Model:      modelName,
		HTTPClient: agentHTTPClient,
		Timeout:    agentHTTPTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	return &Service{handler: handler, model: chatModel, enabled: true}, nil
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) Handle(ctx context.Context, input Input) (string, bool) {
	inputText := strings.TrimSpace(input.Text)
	if !s.Enabled() || inputText == "" || store.IsGroupConversation(input.Identity) {
		return "", false
	}
	traceEnabled, err := s.toolTraceEnabled(ctx, input.Identity)
	if err != nil {
		return "AI 工具设置读取失败：" + err.Error(), true
	}
	var trace *toolTraceNotifier
	if traceEnabled {
		trace = &toolTraceNotifier{ident: input.Identity, send: input.SendUpdate}
	}
	tools, err := s.toolsFor(input.Identity, trace)
	if err != nil {
		return "AI 工具初始化失败：" + err.Error(), true
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "life_ustc_assistant",
		Description:   "Life @ USTC QQ assistant",
		Instruction:   currentInstruction(),
		Model:         s.model,
		MaxIterations: agentMaxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return "AI 助手初始化失败：" + err.Error(), true
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	messages, err := s.messagesFor(ctx, input)
	if err != nil {
		return "AI 历史记录读取失败：" + err.Error(), true
	}
	iter := runner.Run(ctx, messages)
	reply := ""
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "AI 助手出错：" + event.Err.Error(), true
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
		return "", false
	}
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
			rawText := strings.TrimSpace(turn.RawText)
			if rawText == "" {
				continue
			}
			messages = append(messages, schema.UserMessage(rawText))
			if reply := strings.TrimSpace(turn.Reply); reply != "" {
				messages = append(messages, schema.AssistantMessage(reply, nil))
			}
		}
	}
	messages = append(messages, schema.UserMessage(strings.TrimSpace(input.Text)))
	return messages, nil
}

type emptyInput struct{}

type busInput struct {
	From  string `json:"from,omitempty" jsonschema_description:"Optional origin campus, such as 东区, 西区, 南区, 高新区"`
	To    string `json:"to,omitempty" jsonschema_description:"Optional destination campus, such as 东区, 西区, 南区, 高新区"`
	After string `json:"after,omitempty" jsonschema_description:"Optional earliest departure time, such as 09:25, 2026-06-09 09:25, or RFC3339"`
}

type bulkSubscribeInput struct {
	Text string `json:"text" jsonschema_description:"Pasted section codes or text containing section codes, for example CONT5103P.01 CONT6104P.01"`
}

type keywordInput struct {
	Keyword string `json:"keyword" jsonschema_description:"Search keyword, course name, teacher name, or section code"`
}

type todoInput struct {
	Title    string `json:"title" jsonschema_description:"Todo title to create"`
	Content  string `json:"content,omitempty" jsonschema_description:"Optional todo content or note"`
	Priority string `json:"priority,omitempty" jsonschema_description:"Optional priority: low, medium, or high"`
	DueAt    string `json:"due_at,omitempty" jsonschema_description:"Optional due date, such as 2026-06-10 or an RFC3339 datetime"`
}

type todoListInput struct {
	Status    string `json:"status,omitempty" jsonschema_description:"Optional status filter: pending, completed, or all"`
	Priority  string `json:"priority,omitempty" jsonschema_description:"Optional priority filter: low, medium, or high"`
	DueBefore string `json:"due_before,omitempty" jsonschema_description:"Optional due-before date, such as 2026-06-10"`
	DueAfter  string `json:"due_after,omitempty" jsonschema_description:"Optional due-after date, such as 2026-06-10"`
}

type todoUpdateInput struct {
	Target   string `json:"target" jsonschema_description:"Todo number, ID, or title shown in the latest list response"`
	Title    string `json:"title,omitempty" jsonschema_description:"Optional new todo title"`
	Content  string `json:"content,omitempty" jsonschema_description:"Optional new content or note"`
	Priority string `json:"priority,omitempty" jsonschema_description:"Optional new priority: low, medium, or high"`
	DueAt    string `json:"due_at,omitempty" jsonschema_description:"Optional new due date, such as 2026-06-10 or an RFC3339 datetime"`
}

type targetInput struct {
	Target string `json:"target" jsonschema_description:"Item number, ID, or title shown in the latest list response"`
}

type notificationInput struct {
	Kind    string `json:"kind" jsonschema_description:"Notification type to change: classes for upcoming class reminders, homework for homework due reminders"`
	Enabled bool   `json:"enabled" jsonschema_description:"Whether to enable this notification type"`
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

func (s *Service) toolsFor(ident store.Identity, trace *toolTraceNotifier) ([]tool.BaseTool, error) {
	commandSpecs := commands.CommandSpecs()
	specByName := commandSpecsByName(commandSpecs)
	tools := make([]tool.BaseTool, 0, countAgentCommandTools(commandSpecs))
	var err error
	for _, commandSpec := range commandSpecs {
		if !s.commandDependenciesAvailable(commandSpec) {
			continue
		}
		for _, toolSpec := range commandSpec.AgentTools {
			toolSpec := toolSpec
			tools, err = appendInferredTool(tools, toolSpec.Name, toolSpec.Description, trace, func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, toolSpec.CommandText)
			})
			if err != nil {
				return nil, err
			}
		}
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "bus", "get_next_bus", "Get next shuttle bus departures. Origin, destination, and earliest departure time are optional. Use after when planning after a class or event.", trace, func(ctx context.Context, input busInput) (string, error) {
		return s.runCommand(ctx, ident, busCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "course", "search_courses", "Search courses by name, code, or teacher keyword.", trace, requiredCommandTool(s, ident, "keyword", "课程 ", func(input keywordInput) string {
		return input.Keyword
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section", "search_sections", "Search teaching sections by course name, section code, or teacher keyword. Use this before subscribing by natural language.", trace, requiredCommandTool(s, ident, "keyword", "教学班 ", func(input keywordInput) string {
		return input.Keyword
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "teacher", "search_teachers", "Search teachers by name or teacher code.", trace, requiredCommandTool(s, ident, "keyword", "老师 ", func(input keywordInput) string {
		return input.Keyword
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "list_filtered_todos", "List todos with optional status, priority, and due-date filters.", trace, func(ctx context.Context, input todoListInput) (string, error) {
		return s.runCommand(ctx, ident, todoListCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "add_todo", "Prepare a todo creation command. This does not create the todo until the user confirms by sending the command.", trace, requiredConfirmationTool("title", "待办 add ", func(input todoInput) string {
		return todoCreateCommandSuffix(input)
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "complete_todo", "Prepare a todo completion command. This does not modify the todo until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "待办 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "undo_todo_completion", "Prepare a todo completion undo command. This does not modify the todo until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "待办 undo ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "update_todo", "Prepare a todo update command. This does not modify the todo until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "待办 update ", func(input todoUpdateInput) string {
		return todoUpdateCommandSuffix(input)
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "delete_todo", "Prepare a todo delete command. This does not delete the todo until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "待办 delete ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "complete_homework", "Prepare a homework completion command. This does not modify homework until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "作业 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "undo_homework_completion", "Prepare a homework completion undo command. This does not modify homework until the user confirms by sending the command.", trace, requiredConfirmationTool("target", "作业 undo ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "subscription", "bulk_subscribe_sections", "Prepare a bulk subscription import command. This does not change subscriptions until the user confirms by sending the command.", trace, requiredConfirmationTool("text", "订阅 导入 ", func(input bulkSubscribeInput) string {
		return input.Text
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "notify", "set_notification_settings", "Prepare a notification setting command. This does not change notification settings until the user confirms by sending the command.", trace, func(ctx context.Context, input notificationInput) (string, error) {
		kind, err := notificationKindCommandArg(input.Kind)
		if err != nil {
			return "", err
		}
		state := "关"
		if input.Enabled {
			state = "开"
		}
		return confirmationRequired("通知设置", "通知 "+kind+" "+state), nil
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, "get_current_time", "Get the current local time in Asia/Shanghai.", trace, func(_ context.Context, _ emptyInput) (string, error) {
		return currentTimeMessage(), nil
	})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func appendCommandBackedTool[I any](s *Service, specByName map[string]commands.CommandSpec, tools []tool.BaseTool, commandName, name, description string, trace *toolTraceNotifier, fn func(context.Context, I) (string, error)) ([]tool.BaseTool, error) {
	spec, ok := specByName[commandName]
	if !ok {
		return nil, fmt.Errorf("agent tool %q references unknown command %q", name, commandName)
	}
	if !s.commandDependenciesAvailable(spec) {
		return tools, nil
	}
	return appendInferredTool(tools, name, description, trace, fn)
}

func requiredCommandTool[I any](s *Service, ident store.Identity, argName, commandPrefix string, value func(I) string) func(context.Context, I) (string, error) {
	return func(ctx context.Context, input I) (string, error) {
		arg, err := requiredToolArg(argName, value(input))
		if err != nil {
			return "", err
		}
		return s.runCommand(ctx, ident, commandPrefix+arg)
	}
}

func requiredConfirmationTool[I any](argName, commandPrefix string, value func(I) string) func(context.Context, I) (string, error) {
	return func(_ context.Context, input I) (string, error) {
		arg, err := requiredToolArg(argName, value(input))
		if err != nil {
			return "", err
		}
		return confirmationRequired("需要确认", commandPrefix+arg), nil
	}
}

func confirmationRequired(title, command string) string {
	return strings.Join([]string{
		title + "：不会自动执行。",
		"确认请发送：",
		command,
	}, "\n")
}

func commandSpecsByName(specs []commands.CommandSpec) map[string]commands.CommandSpec {
	out := make(map[string]commands.CommandSpec, len(specs))
	for _, spec := range specs {
		out[spec.Name] = spec
	}
	return out
}

func (s *Service) commandDependenciesAvailable(spec commands.CommandSpec) bool {
	if spec.NeedsLife && s.handler.Life == nil {
		return false
	}
	if spec.NeedsAuth && (s.handler.Auth == nil || s.handler.Auth.Store == nil) {
		return false
	}
	if spec.NeedsStore && s.handler.Store == nil {
		return false
	}
	return true
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

func countAgentCommandTools(commandSpecs []commands.CommandSpec) int {
	count := 0
	for _, commandSpec := range commandSpecs {
		count += len(commandSpec.AgentTools)
	}
	return count
}

func requiredToolArg(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func notificationKindCommandArg(value string) (string, error) {
	kind, ok := commands.NormalizeNotificationKind(value)
	if !ok {
		return "", fmt.Errorf("unsupported notification kind %q; use classes or homework", value)
	}
	switch kind {
	case "classes":
		return "课表", nil
	case "homework":
		return "作业", nil
	default:
		return "", fmt.Errorf("unsupported notification kind %q; use classes or homework", value)
	}
}

func busCommandText(input busInput) string {
	parts := []string{"校车"}
	if from := strings.TrimSpace(input.From); from != "" {
		parts = append(parts, from)
	}
	if to := strings.TrimSpace(input.To); to != "" {
		if len(parts) == 1 {
			parts = append(parts, "到")
		}
		parts = append(parts, to)
	}
	if after := strings.TrimSpace(input.After); after != "" {
		parts = append(parts, "after", after)
	}
	return strings.Join(parts, " ")
}

func todoListCommandText(input todoListInput) string {
	parts := []string{"待办", "list"}
	if status := strings.TrimSpace(input.Status); status != "" {
		parts = append(parts, status)
	}
	if priority := strings.TrimSpace(input.Priority); priority != "" {
		parts = append(parts, "priority", priority)
	}
	if dueBefore := strings.TrimSpace(input.DueBefore); dueBefore != "" {
		parts = append(parts, "before", dueBefore)
	}
	if dueAfter := strings.TrimSpace(input.DueAfter); dueAfter != "" {
		parts = append(parts, "after", dueAfter)
	}
	return strings.Join(parts, " ")
}

func todoCreateCommandSuffix(input todoInput) string {
	parts := []string{strings.TrimSpace(input.Title)}
	if dueAt := strings.TrimSpace(input.DueAt); dueAt != "" {
		parts = append(parts, "due", dueAt)
	}
	if priority := strings.TrimSpace(input.Priority); priority != "" {
		parts = append(parts, "priority", priority)
	}
	if content := strings.TrimSpace(input.Content); content != "" {
		parts = append(parts, "content", content)
	}
	return strings.Join(parts, " ")
}

func todoUpdateCommandSuffix(input todoUpdateInput) string {
	parts := []string{strings.TrimSpace(input.Target)}
	if title := strings.TrimSpace(input.Title); title != "" {
		parts = append(parts, "title", title)
	}
	if dueAt := strings.TrimSpace(input.DueAt); dueAt != "" {
		parts = append(parts, "due", dueAt)
	}
	if priority := strings.TrimSpace(input.Priority); priority != "" {
		parts = append(parts, "priority", priority)
	}
	if content := strings.TrimSpace(input.Content); content != "" {
		parts = append(parts, "content", content)
	}
	return strings.Join(parts, " ")
}

func (s *Service) runCommand(ctx context.Context, ident store.Identity, text string) (string, error) {
	reply, ok := s.handler.Handle(ctx, commands.Input{Text: text, Identity: ident, SuppressLog: true})
	if !ok {
		return "", fmt.Errorf("command %q was not handled", text)
	}
	return reply, nil
}

const historyTurnLimit = 20
const agentMaxIterations = 12
const agentHTTPTimeout = 60 * time.Second

var shanghaiLocation = lifedata.ChinaLocation()

func currentInstruction() string {
	return currentInstructionAt(time.Now())
}

func currentInstructionAt(now time.Time) string {
	return fmt.Sprintf(`You are SiGNAL_BOT, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
QQ does not render Markdown tables well. Prefer short plain-text lines and compact bullet lists; avoid Markdown tables unless the user explicitly asks for a table.
Use tools for Life @ USTC facts instead of guessing.
Current local time is %s.
You can answer questions about prior messages using the chat history provided in this run.
For bus planning after a class or event, pass the class/event end time to get_next_bus.after so the bus result is after that time.
Tools that create, update, delete, complete, subscribe, or change notification settings only prepare confirmation commands. Do not claim those changes are done until the user sends the confirmation command.
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
