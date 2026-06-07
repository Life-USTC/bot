package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	Text     string
	Identity store.Identity
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
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
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
	if !s.Enabled() || strings.TrimSpace(input.Text) == "" || isGroupConversation(input.Identity) {
		return "", false
	}
	tools, err := s.toolsFor(input.Identity)
	if err != nil {
		return "AI 工具初始化失败：" + err.Error(), true
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "life_ustc_assistant",
		Description:   "Life @ USTC QQ assistant",
		Instruction:   currentInstruction(),
		Model:         s.model,
		MaxIterations: 6,
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
		if strings.TrimSpace(msg.Content) != "" {
			reply = msg.Content
		}
	}
	if strings.TrimSpace(reply) == "" {
		return "", false
	}
	return strings.TrimSpace(reply), true
}

func isGroupConversation(ident store.Identity) bool {
	return textutil.TrimEqualFold(ident.ConversationType, "group")
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
	From string `json:"from,omitempty" jsonschema_description:"Optional origin campus, such as 东区, 西区, 南区, 高新区"`
	To   string `json:"to,omitempty" jsonschema_description:"Optional destination campus, such as 东区, 西区, 南区, 高新区"`
}

type bulkSubscribeInput struct {
	Text string `json:"text" jsonschema_description:"Pasted section codes or text containing section codes, for example CONT5103P.01 CONT6104P.01"`
}

type keywordInput struct {
	Keyword string `json:"keyword" jsonschema_description:"Search keyword, course name, teacher name, or section code"`
}

type todoInput struct {
	Title string `json:"title" jsonschema_description:"Todo title to create"`
}

type targetInput struct {
	Target string `json:"target" jsonschema_description:"Item number, ID, or title shown in the latest list response"`
}

type notificationInput struct {
	Kind    string `json:"kind" jsonschema_description:"Notification type to change: classes for upcoming class reminders, homework for homework due reminders"`
	Enabled bool   `json:"enabled" jsonschema_description:"Whether to enable this notification type"`
}

func (s *Service) toolsFor(ident store.Identity) ([]tool.BaseTool, error) {
	commandSpecs := commands.CommandSpecs()
	specByName := commandSpecsByName(commandSpecs)
	tools := make([]tool.BaseTool, 0, countAgentCommandTools(commandSpecs)+extraAgentToolCount)
	var err error
	for _, commandSpec := range commandSpecs {
		if !s.commandDependenciesAvailable(commandSpec) {
			continue
		}
		for _, toolSpec := range commandSpec.AgentTools {
			toolSpec := toolSpec
			tools, err = appendInferredTool(tools, toolSpec.Name, toolSpec.Description, func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, toolSpec.CommandText)
			})
			if err != nil {
				return nil, err
			}
		}
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "bus", "get_next_bus", "Get next shuttle bus departures. Origin and destination are optional.", func(ctx context.Context, input busInput) (string, error) {
		return s.runCommand(ctx, ident, busCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "course", "search_courses", "Search courses by name, code, or teacher keyword.", requiredCommandTool(s, ident, "keyword", "课程 ", func(input keywordInput) string {
		return input.Keyword
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section", "search_sections", "Search teaching sections by course name, section code, or teacher keyword. Use this before subscribing by natural language.", requiredCommandTool(s, ident, "keyword", "教学班 ", func(input keywordInput) string {
		return input.Keyword
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "add_todo", "Create a new todo for the user.", requiredCommandTool(s, ident, "title", "待办 add ", func(input todoInput) string {
		return input.Title
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "complete_todo", "Mark a pending todo complete by number, ID, or title from the todo list.", requiredCommandTool(s, ident, "target", "待办 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "complete_homework", "Mark a pending homework complete by number, ID, or title from the homework list.", requiredCommandTool(s, ident, "target", "作业 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "undo_homework_completion", "Undo completion for a homework by number, ID, or title from the homework list.", requiredCommandTool(s, ident, "target", "作业 undo ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "subscription", "bulk_subscribe_sections", "Bulk add teaching sections to the user's calendar subscription from pasted section codes.", requiredCommandTool(s, ident, "text", "订阅 导入 ", func(input bulkSubscribeInput) string {
		return input.Text
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "notify", "set_notification_settings", "Enable or disable one active push notification type. Use kind=classes for upcoming class reminders or kind=homework for homework reminders.", func(ctx context.Context, input notificationInput) (string, error) {
		kind, err := notificationKindCommandArg(input.Kind)
		if err != nil {
			return "", err
		}
		state := "关"
		if input.Enabled {
			state = "开"
		}
		return s.runCommand(ctx, ident, "通知 "+kind+" "+state)
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, "get_current_time", "Get the current local time in Asia/Shanghai.", func(_ context.Context, _ emptyInput) (string, error) {
		return currentTimeMessage(), nil
	})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func appendCommandBackedTool[I any](s *Service, specByName map[string]commands.CommandSpec, tools []tool.BaseTool, commandName, name, description string, fn func(context.Context, I) (string, error)) ([]tool.BaseTool, error) {
	spec, ok := specByName[commandName]
	if !ok {
		return nil, fmt.Errorf("agent tool %q references unknown command %q", name, commandName)
	}
	if !s.commandDependenciesAvailable(spec) {
		return tools, nil
	}
	return appendInferredTool(tools, name, description, fn)
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

func appendInferredTool[I any](tools []tool.BaseTool, name, description string, fn func(context.Context, I) (string, error)) ([]tool.BaseTool, error) {
	t, err := utils.InferTool(name, description, fn)
	if err != nil {
		return nil, err
	}
	return append(tools, t), nil
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
	switch textutil.LowerTrim(value) {
	case "class", "classes", "section", "sections", "schedule", "curriculum", "kb", "课表", "课程", "上课":
		return "课表", nil
	case "homework", "hw", "作业":
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
	return strings.Join(parts, " ")
}

func (s *Service) runCommand(ctx context.Context, ident store.Identity, text string) (string, error) {
	reply, ok := s.handler.Handle(ctx, commands.Input{Text: text, Identity: ident, SuppressLog: true})
	if !ok {
		return "", fmt.Errorf("command %q was not handled", text)
	}
	return reply, nil
}

const historyTurnLimit = 8
const agentHTTPTimeout = 60 * time.Second
const extraAgentToolCount = 10

var shanghaiLocation = lifedata.ChinaLocation()

func currentInstruction() string {
	return currentInstructionAt(time.Now())
}

func currentInstructionAt(now time.Time) string {
	return fmt.Sprintf(`You are SiGNAL_BOT, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
Use tools for Life @ USTC facts instead of guessing.
Current local time is %s.
You can answer questions about prior messages using the chat history provided in this run.
You can manage private-chat notification settings with tools when the user asks to turn class or homework reminders on or off.
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
