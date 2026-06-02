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
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		BaseURL:    cfg.BaseURL,
		Model:      modelName,
		HTTPClient: httpClient,
		Timeout:    20 * time.Second,
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
		Instruction:   systemInstruction,
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
	iter := runner.Query(ctx, input.Text)
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

type emptyInput struct{}

type busInput struct {
	From string `json:"from,omitempty" jsonschema_description:"Optional origin campus, such as 东区, 西区, 南区, 高新区"`
	To   string `json:"to,omitempty" jsonschema_description:"Optional destination campus, such as 东区, 西区, 南区, 高新区"`
}

func (s *Service) toolsFor(ident store.Identity) ([]tool.BaseTool, error) {
	specs := []struct {
		name string
		desc string
		fn   func(context.Context, emptyInput) (string, error)
	}{
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
	}
	tools := make([]tool.BaseTool, 0, len(specs)+1)
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
	return tools, nil
}

func (s *Service) runCommand(ctx context.Context, ident store.Identity, text string) (string, error) {
	reply, ok := s.handler.Handle(ctx, commands.Input{Text: text, Identity: ident})
	if !ok {
		return "", fmt.Errorf("command %q was not handled", text)
	}
	return reply, nil
}

const systemInstruction = `You are SiGNAL_BOT, a casual Life @ USTC assistant in QQ.
Answer in the user's language, usually concise Chinese.
Use tools for Life @ USTC facts instead of guessing.
Do not expose private profile, homework, todo, or curriculum data unless the user asks in this private chat.
For group chats, this agent is disabled by the host application.
When a tool returns login-required text, tell the user to log in with 登录.`

var _ = schema.Assistant
