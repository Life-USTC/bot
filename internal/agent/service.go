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
	"github.com/Life-USTC/Bot/internal/store"
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
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("agent enabled but OPENAI_API_KEY is empty")
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	agentHTTPClient := httpClient
	if httpClient != nil {
		clone := *httpClient
		clone.Timeout = agentHTTPTimeout
		agentHTTPClient = &clone
	}
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		BaseURL:    cfg.BaseURL,
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
	if !s.Enabled() || strings.TrimSpace(input.Text) == "" || input.Identity.ConversationType == "group" {
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

func (s *Service) messagesFor(ctx context.Context, input Input) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, 17)
	if s.handler.Store != nil {
		history, err := s.handler.Store.RecentHandledInteractions(ctx, input.Identity, historyTurnLimit)
		if err != nil {
			return nil, err
		}
		for _, turn := range history {
			if strings.TrimSpace(turn.RawText) == "" {
				continue
			}
			messages = append(messages, schema.UserMessage(turn.RawText))
			if strings.TrimSpace(turn.Reply) != "" {
				messages = append(messages, schema.AssistantMessage(turn.Reply, nil))
			}
		}
	}
	messages = append(messages, schema.UserMessage(input.Text))
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

func (s *Service) toolsFor(ident store.Identity) ([]tool.BaseTool, error) {
	specs := []struct {
		name string
		desc string
		fn   func(context.Context, emptyInput) (string, error)
	}{
		{
			name: "get_two_day_curriculum",
			desc: "Get the user's curriculum for today and tomorrow.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "课表")
			},
		},
		{
			name: "get_today_curriculum",
			desc: "Get the user's curriculum for today.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "课表 今天")
			},
		},
		{
			name: "get_tomorrow_curriculum",
			desc: "Get the user's curriculum for tomorrow.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "课表 明天")
			},
		},
		{
			name: "get_next_class",
			desc: "Get the user's next upcoming class.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "下一节课")
			},
		},
		{
			name: "list_homeworks",
			desc: "List the user's homework grouped by overdue, nearby, and future.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "作业")
			},
		},
		{
			name: "list_todos",
			desc: "List the user's pending todos.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "待办")
			},
		},
		{
			name: "list_subscriptions",
			desc: "List the user's current calendar section subscriptions.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "订阅")
			},
		},
		{
			name: "get_current_semester",
			desc: "Get the current Life USTC semester.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "学期")
			},
		},
		{
			name: "get_profile",
			desc: "Get the logged-in user's profile status.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "我")
			},
		},
		{
			name: "get_bot_status",
			desc: "Get Life API reachability and login status.",
			fn: func(ctx context.Context, _ emptyInput) (string, error) {
				return s.runCommand(ctx, ident, "状态")
			},
		},
	}
	tools := make([]tool.BaseTool, 0, len(specs)+9)
	for _, spec := range specs {
		t, err := utils.InferTool(spec.name, spec.desc, spec.fn)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	busTool, err := utils.InferTool("get_next_bus", "Get next shuttle bus departures. Origin and destination are optional.", func(ctx context.Context, input busInput) (string, error) {
		parts := []string{"校车"}
		if strings.TrimSpace(input.From) != "" {
			parts = append(parts, input.From)
		}
		if strings.TrimSpace(input.To) != "" {
			parts = append(parts, input.To)
		}
		return s.runCommand(ctx, ident, strings.Join(parts, " "))
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, busTool)
	courseTool, err := utils.InferTool("search_courses", "Search courses by name, code, or teacher keyword.", func(ctx context.Context, input keywordInput) (string, error) {
		return s.runCommand(ctx, ident, "课程 "+input.Keyword)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, courseTool)
	sectionTool, err := utils.InferTool("search_sections", "Search teaching sections by course name, section code, or teacher keyword. Use this before subscribing by natural language.", func(ctx context.Context, input keywordInput) (string, error) {
		return s.runCommand(ctx, ident, "教学班 "+input.Keyword)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, sectionTool)
	addTodoTool, err := utils.InferTool("add_todo", "Create a new todo for the user.", func(ctx context.Context, input todoInput) (string, error) {
		return s.runCommand(ctx, ident, "待办 add "+input.Title)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, addTodoTool)
	completeTodoTool, err := utils.InferTool("complete_todo", "Mark a pending todo complete by number, ID, or title from the todo list.", func(ctx context.Context, input targetInput) (string, error) {
		return s.runCommand(ctx, ident, "待办 done "+input.Target)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, completeTodoTool)
	completeHomeworkTool, err := utils.InferTool("complete_homework", "Mark a pending homework complete by number, ID, or title from the homework list.", func(ctx context.Context, input targetInput) (string, error) {
		return s.runCommand(ctx, ident, "作业 done "+input.Target)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, completeHomeworkTool)
	undoHomeworkTool, err := utils.InferTool("undo_homework_completion", "Undo completion for a homework by number, ID, or title from the homework list.", func(ctx context.Context, input targetInput) (string, error) {
		return s.runCommand(ctx, ident, "作业 undo "+input.Target)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, undoHomeworkTool)
	bulkSubscribeTool, err := utils.InferTool("bulk_subscribe_sections", "Bulk add teaching sections to the user's calendar subscription from pasted section codes.", func(ctx context.Context, input bulkSubscribeInput) (string, error) {
		return s.runCommand(ctx, ident, "订阅 导入 "+input.Text)
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, bulkSubscribeTool)
	currentTimeTool, err := utils.InferTool("get_current_time", "Get the current local time in Asia/Shanghai.", func(_ context.Context, _ emptyInput) (string, error) {
		return time.Now().In(shanghaiLocation).Format("现在是 2006-01-02 15:04，Asia/Shanghai。"), nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, currentTimeTool)
	return tools, nil
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

var shanghaiLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

func currentInstruction() string {
	return fmt.Sprintf(`You are SiGNAL_BOT, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
Use tools for Life @ USTC facts instead of guessing.
Current local time is %s.
You can answer questions about prior messages using the chat history provided in this run.
Do not expose private profile, homework, todo, or curriculum data unless the user asks in this private chat.
For group chats, this agent is disabled by the host application.
When a tool returns login-required text, tell the user to log in with 登录.`, time.Now().In(shanghaiLocation).Format("2006-01-02 15:04 MST"))
}

var _ = schema.Assistant
