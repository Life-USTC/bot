package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
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
	Timeout time.Duration
	Logger  *log.Logger
}

type Service struct {
	handler commands.Handler
	model   *einoopenai.ChatModel
	enabled bool
	timeout time.Duration
	logger  *log.Logger
}

type Input struct {
	Text       string
	Identity   store.Identity
	SendUpdate func(context.Context, store.Identity, string) error
}

func New(ctx context.Context, cfg Config, handler commands.Handler, httpClient *http.Client) (*Service, error) {
	timeout := normalizedAgentTimeout(cfg.Timeout)
	if !cfg.Enabled {
		return &Service{handler: handler, timeout: timeout, logger: cfg.Logger}, nil
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
		clone.Timeout = timeout
		agentHTTPClient = &clone
	}
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
	return &Service{handler: handler, model: chatModel, enabled: true, timeout: timeout, logger: cfg.Logger}, nil
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
	tools, err := s.toolsFor(input.Identity, trace, input.SendUpdate)
	if err != nil {
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
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
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

type dateInput struct {
	Date string `json:"date" jsonschema_description:"Target date, such as 2026-06-23, 6.23, or 6月23日"`
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

type feedbackInput struct {
	Category string `json:"category,omitempty" jsonschema_description:"Short category for the feedback, such as missing_tool, bad_result, typo, or api_gap"`
	Content  string `json:"content" jsonschema_description:"Concrete feedback about missing tools, wrong behavior, tool/API gaps, or user interaction problems"`
	Context  string `json:"context,omitempty" jsonschema_description:"Relevant user message, tool result, or short context that explains why this feedback matters"`
}

type messagePartInput struct {
	Content string `json:"content" jsonschema_description:"One intermediate QQ message to send before the final response"`
}

type searchCoursesInput struct {
	Keyword          string `json:"keyword,omitempty" jsonschema_description:"Search keyword, course name, code, or teacher name"`
	EducationLevelID int64  `json:"education_level_id,omitempty" jsonschema_description:"Optional education level ID filter"`
	CategoryID       int64  `json:"category_id,omitempty" jsonschema_description:"Optional course category ID filter"`
	ClassTypeID      int64  `json:"class_type_id,omitempty" jsonschema_description:"Optional class type ID filter"`
	Limit            int    `json:"limit,omitempty" jsonschema_description:"Optional result limit (default 5)"`
}

type searchSectionsInput struct {
	Keyword      string `json:"keyword,omitempty" jsonschema_description:"Search keyword, course name, section code, or teacher name"`
	CourseID     int64  `json:"course_id,omitempty" jsonschema_description:"Optional course ID filter"`
	CourseJwID   int64  `json:"course_jw_id,omitempty" jsonschema_description:"Optional course JW ID filter"`
	SemesterID   int64  `json:"semester_id,omitempty" jsonschema_description:"Optional semester ID filter"`
	SemesterJwID int64  `json:"semester_jw_id,omitempty" jsonschema_description:"Optional semester JW ID filter"`
	CampusID     int64  `json:"campus_id,omitempty" jsonschema_description:"Optional campus ID filter"`
	DepartmentID int64  `json:"department_id,omitempty" jsonschema_description:"Optional department ID filter"`
	TeacherID    int64  `json:"teacher_id,omitempty" jsonschema_description:"Optional teacher ID filter"`
	TeacherCode  string `json:"teacher_code,omitempty" jsonschema_description:"Optional teacher code filter"`
	Limit        int    `json:"limit,omitempty" jsonschema_description:"Optional result limit (default 5)"`
}

type searchTeachersInput struct {
	Keyword      string `json:"keyword,omitempty" jsonschema_description:"Search keyword or teacher name"`
	DepartmentID int64  `json:"department_id,omitempty" jsonschema_description:"Optional department ID filter"`
	Limit        int    `json:"limit,omitempty" jsonschema_description:"Optional result limit (default 5)"`
}

type jwIdInput struct {
	JwID int64 `json:"jw_id" jsonschema_description:"JW ID of the teaching section or course"`
}

type idInput struct {
	ID int64 `json:"id" jsonschema_description:"ID of the teacher"`
}

type sectionJwIdDateRangeInput struct {
	SectionJwID int64  `json:"section_jw_id" jsonschema_description:"Teaching section JW ID"`
	DateFrom    string `json:"date_from" jsonschema_description:"Start date (YYYY-MM-DD)"`
	DateTo      string `json:"date_to" jsonschema_description:"End date (YYYY-MM-DD)"`
}

type busRouteInput struct {
	From string `json:"from,omitempty" jsonschema_description:"Optional origin campus name, such as 东区"`
	To   string `json:"to,omitempty" jsonschema_description:"Optional destination campus name, such as 西区"`
}

type dayLimitInput struct {
	DayLimit int `json:"day_limit,omitempty" jsonschema_description:"Number of days to look ahead (default 7)"`
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

func (s *Service) toolsFor(ident store.Identity, trace *toolTraceNotifier, sendUpdate func(context.Context, store.Identity, string) error) ([]tool.BaseTool, error) {
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
	tools, err = appendCommandBackedTool(s, specByName, tools, "course_search", "search_courses", "Search courses by keyword with optional filters (education level, category, class type).", trace, func(ctx context.Context, input searchCoursesInput) (string, error) {
		return s.runCommand(ctx, ident, searchCoursesCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section_search", "search_sections", "Search teaching sections by keyword with optional filters (course, semester, campus, department, teacher). Use this before subscribing by natural language.", trace, func(ctx context.Context, input searchSectionsInput) (string, error) {
		return s.runCommand(ctx, ident, searchSectionsCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "teacher_search", "search_teachers", "Search teachers by keyword with optional department filter.", trace, func(ctx context.Context, input searchTeachersInput) (string, error) {
		return s.runCommand(ctx, ident, searchTeachersCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "list_semesters", "list_semesters", "List available Life USTC semesters.", trace, func(ctx context.Context, _ emptyInput) (string, error) {
		return s.runCommand(ctx, ident, "学期列表")
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "course_by_jw_id", "get_course_by_jw_id", "Get a course by its JW ID.", trace, func(ctx context.Context, input jwIdInput) (string, error) {
		if input.JwID <= 0 {
			return "", errors.New("jw_id is required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("课程编号 %d", input.JwID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section_by_jw_id", "get_section_by_jw_id", "Get a teaching section by its JW ID.", trace, func(ctx context.Context, input jwIdInput) (string, error) {
		if input.JwID <= 0 {
			return "", errors.New("jw_id is required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("教学班编号 %d", input.JwID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "teacher_by_id", "get_teacher_by_id", "Get a teacher by ID.", trace, func(ctx context.Context, input idInput) (string, error) {
		if input.ID <= 0 {
			return "", errors.New("id is required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("老师编号 %d", input.ID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "bus_routes", "list_bus_routes", "List campus bus routes with optional origin and destination campus filters.", trace, func(ctx context.Context, input busRouteInput) (string, error) {
		return s.runCommand(ctx, ident, busRouteCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "unsubscribe_section_by_jw_id", "unsubscribe_section_by_jw_id", "Unsubscribe from a teaching section by its JW ID. Requires user confirmation.", trace, func(ctx context.Context, input jwIdInput) (string, error) {
		if input.JwID <= 0 {
			return "", errors.New("jw_id is required")
		}
		return s.prepareConfirmation(ctx, ident, "需要确认", fmt.Sprintf("退订教学班 %d", input.JwID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "my_subscribed_sections", "list_my_subscribed_sections", "List the user's current subscribed teaching sections.", trace, func(ctx context.Context, _ emptyInput) (string, error) {
		return s.runCommand(ctx, ident, "我的订阅")
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section_schedules", "list_schedules_by_section", "List schedules for a teaching section by JW ID and date range.", trace, func(ctx context.Context, input sectionJwIdDateRangeInput) (string, error) {
		if input.SectionJwID <= 0 {
			return "", errors.New("section_jw_id is required")
		}
		if strings.TrimSpace(input.DateFrom) == "" || strings.TrimSpace(input.DateTo) == "" {
			return "", errors.New("date_from and date_to are required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("教学班课表 %d %s %s", input.SectionJwID, input.DateFrom, input.DateTo))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section_exams", "list_exams_by_section", "List exams for a teaching section by JW ID.", trace, func(ctx context.Context, input jwIdInput) (string, error) {
		if input.JwID <= 0 {
			return "", errors.New("jw_id is required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("教学班考试 %d", input.JwID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "section_homeworks", "list_homeworks_by_section", "List homeworks for a teaching section by JW ID.", trace, func(ctx context.Context, input jwIdInput) (string, error) {
		if input.JwID <= 0 {
			return "", errors.New("jw_id is required")
		}
		return s.runCommand(ctx, ident, fmt.Sprintf("教学班作业 %d", input.JwID))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "dashboard", "get_my_dashboard", "Get the logged-in user's dashboard overview (classes, todos, homework, exams).", trace, func(ctx context.Context, _ emptyInput) (string, error) {
		return s.runCommand(ctx, ident, "概览")
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "upcoming_deadlines", "get_upcoming_deadlines", "Get the user's upcoming deadlines (todos, homework, exams) within a number of days.", trace, func(ctx context.Context, input dayLimitInput) (string, error) {
		return s.runCommand(ctx, ident, upcomingDeadlinesCommandText(input))
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "schedule", "get_curriculum_for_date", "Get the user's curriculum for a specific date.", trace, requiredCommandTool(s, ident, "date", "课表 ", func(input dateInput) string {
		return input.Date
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
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "add_todo", "Prepare a todo creation command. This does not create the todo until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "title", "待办 add ", func(input todoInput) string {
		return todoCreateCommandSuffix(input)
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "complete_todo", "Prepare a todo completion command. This does not modify the todo until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "待办 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "undo_todo_completion", "Prepare a todo completion undo command. This does not modify the todo until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "待办 undo ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "update_todo", "Prepare a todo update command. This does not modify the todo until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "待办 update ", func(input todoUpdateInput) string {
		return todoUpdateCommandSuffix(input)
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "todo", "delete_todo", "Prepare a todo delete command. This does not delete the todo until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "待办 delete ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "complete_homework", "Prepare a homework completion command. This does not modify homework until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "作业 done ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "homework", "undo_homework_completion", "Prepare a homework completion undo command. This does not modify homework until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "target", "作业 undo ", func(input targetInput) string {
		return input.Target
	}))
	if err != nil {
		return nil, err
	}
	tools, err = appendCommandBackedTool(s, specByName, tools, "subscription", "bulk_subscribe_sections", "Prepare a bulk subscription import command. This does not change subscriptions until the user confirms by replying ok or sending the command.", trace, requiredConfirmationTool(s, ident, "text", "订阅 导入 ", func(input bulkSubscribeInput) string {
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
		return s.prepareConfirmation(ctx, ident, "通知设置", "通知 "+kind+" "+state)
	})
	if err != nil {
		return nil, err
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

func requiredConfirmationTool[I any](s *Service, ident store.Identity, argName, commandPrefix string, value func(I) string) func(context.Context, I) (string, error) {
	return func(ctx context.Context, input I) (string, error) {
		arg, err := requiredToolArg(argName, value(input))
		if err != nil {
			return "", err
		}
		return s.prepareConfirmation(ctx, ident, "需要确认", commandPrefix+arg)
	}
}

func (s *Service) prepareConfirmation(ctx context.Context, ident store.Identity, title, command string) (string, error) {
	stored := false
	if s.handler.Store != nil && store.HasConversationIdentity(ident) {
		if _, err := s.handler.Store.SavePendingConfirmation(ctx, ident, command, "agent", pendingConfirmationTTL); err != nil {
			return "", err
		}
		stored = true
	}
	return confirmationRequired(title, command, stored), nil
}

func confirmationRequired(title, command string, stored bool) string {
	lines := []string{
		title + "：不会自动执行。",
	}
	if stored {
		lines = append(lines, "回复 ok 确认，或单独发送这一条：")
	} else {
		lines = append(lines, "请单独发送这一条：")
	}
	lines = append(lines, command)
	return strings.Join(lines, "\n")
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

func searchCoursesCommandText(input searchCoursesInput) string {
	parts := []string{"课程搜索"}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		parts = append(parts, "keyword", keyword)
	}
	if input.EducationLevelID > 0 {
		parts = append(parts, "education_level_id", strconv.FormatInt(input.EducationLevelID, 10))
	}
	if input.CategoryID > 0 {
		parts = append(parts, "category_id", strconv.FormatInt(input.CategoryID, 10))
	}
	if input.ClassTypeID > 0 {
		parts = append(parts, "class_type_id", strconv.FormatInt(input.ClassTypeID, 10))
	}
	if input.Limit > 0 {
		parts = append(parts, "limit", strconv.Itoa(input.Limit))
	}
	return strings.Join(parts, " ")
}

func searchSectionsCommandText(input searchSectionsInput) string {
	parts := []string{"教学班搜索"}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		parts = append(parts, "keyword", keyword)
	}
	if input.CourseID > 0 {
		parts = append(parts, "course_id", strconv.FormatInt(input.CourseID, 10))
	}
	if input.CourseJwID > 0 {
		parts = append(parts, "course_jw_id", strconv.FormatInt(input.CourseJwID, 10))
	}
	if input.SemesterID > 0 {
		parts = append(parts, "semester_id", strconv.FormatInt(input.SemesterID, 10))
	}
	if input.SemesterJwID > 0 {
		parts = append(parts, "semester_jw_id", strconv.FormatInt(input.SemesterJwID, 10))
	}
	if input.CampusID > 0 {
		parts = append(parts, "campus_id", strconv.FormatInt(input.CampusID, 10))
	}
	if input.DepartmentID > 0 {
		parts = append(parts, "department_id", strconv.FormatInt(input.DepartmentID, 10))
	}
	if input.TeacherID > 0 {
		parts = append(parts, "teacher_id", strconv.FormatInt(input.TeacherID, 10))
	}
	if code := strings.TrimSpace(input.TeacherCode); code != "" {
		parts = append(parts, "teacher_code", code)
	}
	if input.Limit > 0 {
		parts = append(parts, "limit", strconv.Itoa(input.Limit))
	}
	return strings.Join(parts, " ")
}

func searchTeachersCommandText(input searchTeachersInput) string {
	parts := []string{"老师搜索"}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		parts = append(parts, "keyword", keyword)
	}
	if input.DepartmentID > 0 {
		parts = append(parts, "department_id", strconv.FormatInt(input.DepartmentID, 10))
	}
	if input.Limit > 0 {
		parts = append(parts, "limit", strconv.Itoa(input.Limit))
	}
	return strings.Join(parts, " ")
}

func busRouteCommandText(input busRouteInput) string {
	parts := []string{"校车路线"}
	if from := strings.TrimSpace(input.From); from != "" {
		parts = append(parts, "from", from)
	}
	if to := strings.TrimSpace(input.To); to != "" {
		parts = append(parts, "to", to)
	}
	return strings.Join(parts, " ")
}

func upcomingDeadlinesCommandText(input dayLimitInput) string {
	if input.DayLimit <= 0 {
		return "近期截止"
	}
	return fmt.Sprintf("近期截止 %d", input.DayLimit)
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
const maxHistoryTextRunes = 1200
const pendingConfirmationTTL = 15 * time.Minute

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
