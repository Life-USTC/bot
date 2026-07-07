package commands

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Handler struct {
	Life                   *life.Client
	Auth                   *auth.Manager
	Store                  *store.Store
	Prefix                 string
	Logger                 *log.Logger
	FeedbackUsers          []string
	FeedbackGroups         []string
	FeedbackSend           func(context.Context, store.Identity, string) error
	AllowGroupPersonalInfo bool
}

var ErrFeedbackSenderUnavailable = errors.New("feedback sender unavailable")

type AgentToolSpec struct {
	Name        string
	Description string
	CommandText string
}

type CommandSpec struct {
	Name       string
	Aliases    []string
	HasHelp    bool
	NeedsLife  bool
	NeedsStore bool
	NeedsAuth  bool
	Normalize  func([]string) []string
	AgentTools []AgentToolSpec
	Run        func(Handler, context.Context, store.Identity, []string) string
}

func CommandSpecs() []CommandSpec {
	specs := append([]CommandSpec(nil), commandSpecs...)
	for i := range specs {
		specs[i].Aliases = append([]string(nil), specs[i].Aliases...)
		specs[i].AgentTools = append([]AgentToolSpec(nil), specs[i].AgentTools...)
	}
	return specs
}

type Input struct {
	Text        string
	Identity    store.Identity
	SuppressLog bool
}

func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	if isConfirmationOK(input.Text) {
		if reply, ok := h.confirmPending(ctx, input); ok {
			return reply, true
		}
	}
	cmd, ok := h.parse(input.Text)
	if !ok && store.IsGroupConversation(input.Identity) {
		cmd, ok = parseGroupBus(input.Text)
	}
	if !ok {
		return "", false
	}
	if store.IsGroupConversation(input.Identity) && !h.groupCommandAllowed(cmd) {
		return "", false
	}
	if h.hasAdditionalCommandLine(input.Text) {
		reply := "检测到多条命令。为避免误操作，一次只处理一条；请分开发送。"
		if !input.SuppressLog {
			h.recordState(ctx, input.Identity, cmd)
			h.recordInteraction(ctx, input.Identity, cmd, reply)
		}
		return reply, true
	}
	if !input.SuppressLog {
		h.recordState(ctx, input.Identity, cmd)
	}
	var reply string
	if cmd.Name == "help" {
		reply = h.help()
	} else {
		spec, ok := commandSpec(cmd.Name)
		if !ok || spec.Run == nil {
			reply = h.help()
		} else if firstArgIs(cmd.Args, "help") && !spec.HasHelp {
			reply = h.help()
		} else if spec.NeedsLife && h.Life == nil && !firstArgIs(cmd.Args, "help") {
			reply = "Life @ USTC API unavailable: not configured."
		} else if spec.NeedsAuth && (h.Auth == nil || h.Auth.Store == nil) && !firstArgIs(cmd.Args, "help") {
			reply = "登录未配置。"
		} else if spec.NeedsStore && h.Store == nil && !firstArgIs(cmd.Args, "help") {
			reply = "存储未配置。"
		} else {
			reply = spec.Run(h, ctx, input.Identity, cmd.Args)
		}
	}
	if !input.SuppressLog {
		h.recordInteraction(ctx, input.Identity, cmd, reply)
	}
	return reply, true
}

type parsedCommand struct {
	Name string
	Args []string
	Raw  string
}

var scheduleAliases = []string{"schedule", "sched", "rc", "kb", "日程", "课表", "课标"}
var attachedFeedbackAliases = []string{"feedback", "fb", "反馈", "意见", "建议", "吐槽"}
var attachedNotifyAliases = []string{"notify", "notice", "push", "提醒", "通知", "推送", "设置"}

const (
	feedbackContextLimit     = 5
	feedbackContextLookback  = 12
	feedbackContextTextRunes = 220
)

func (h Handler) groupCommandAllowed(cmd parsedCommand) bool {
	if groupCommandAlwaysAllowed(cmd.Name) {
		return true
	}
	if !h.AllowGroupPersonalInfo {
		return false
	}
	return groupReadOnlyCommandAllowed(cmd)
}

func groupCommandAlwaysAllowed(name string) bool {
	return name == "bus" || name == "feedback"
}

func groupReadOnlyCommandAllowed(cmd parsedCommand) bool {
	switch cmd.Name {
	case "help", "me", "overview", "status", "semester", "course", "section", "teacher", "schedule", "nextclass", "exam":
		return true
	case "todo":
		return groupTodoReadOnlyArgs(cmd.Args)
	case "homework":
		return groupHomeworkReadOnlyArgs(cmd.Args)
	case "subscription":
		return !hasArgs(cmd.Args) || firstArgIs(cmd.Args, "help")
	default:
		return false
	}
}

func groupTodoReadOnlyArgs(args []string) bool {
	if !hasArgs(args) {
		return true
	}
	switch args[0] {
	case "help", "list", "all", "pending", "completed":
		return true
	default:
		_, ok := normalizeTodoPriority(args[0])
		return ok
	}
}

func groupHomeworkReadOnlyArgs(args []string) bool {
	if !hasArgs(args) {
		return true
	}
	switch args[0] {
	case "help", "pending", "all":
		return true
	default:
		return false
	}
}

var commandSpecs = []CommandSpec{
	{
		Name:      "login",
		Aliases:   []string{"login", "登录", "dl"},
		HasHelp:   true,
		NeedsAuth: true,
		Normalize: normalizeLoginArgs,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.login(ctx, ident, args)
		},
	},
	{
		Name:      "logout",
		Aliases:   []string{"logout", "退出", "登出"},
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.logout(ctx, ident)
		},
	},
	{
		Name:      "me",
		Aliases:   []string{"me", "我", "我的", "profile", "个人"},
		NeedsLife: true,
		NeedsAuth: true,
		AgentTools: []AgentToolSpec{{
			Name:        "get_profile",
			Description: "Get the logged-in user's profile status.",
			CommandText: "我",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.me(ctx, ident)
		},
	},
	{
		Name:      "todo",
		Aliases:   []string{"todo", "td", "待办", "代办", "todo待办"},
		HasHelp:   true,
		NeedsLife: true,
		NeedsAuth: true,
		Normalize: normalizeTodoArgs,
		AgentTools: []AgentToolSpec{{
			Name:        "list_todos",
			Description: "List the user's pending todos.",
			CommandText: "待办",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.todo(ctx, ident, args)
		},
	},
	{
		Name:      "homework",
		Aliases:   []string{"homework", "hw", "作业"},
		HasHelp:   true,
		NeedsLife: true,
		NeedsAuth: true,
		Normalize: normalizeHomeworkArgs,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.homework(ctx, ident, args)
		},
	},
	{
		Name:      "overview",
		Aliases:   []string{"overview", "today", "jr", "ddl", "deadline", "deadlines", "今日", "今天", "安排", "日程安排"},
		NeedsLife: true,
		NeedsAuth: true,
		AgentTools: []AgentToolSpec{{
			Name:        "get_today_overview",
			Description: "Get today's classes plus pending todos, due-soon homework, and upcoming exams.",
			CommandText: "今日",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.overview(ctx, ident)
		},
	},
	{
		Name:      "subscription",
		Aliases:   []string{"订阅", "sub", "subs", "subscription"},
		HasHelp:   true,
		NeedsLife: true,
		NeedsAuth: true,
		Normalize: normalizeSubscriptionArgs,
		AgentTools: []AgentToolSpec{{
			Name:        "list_subscriptions",
			Description: "List the user's current calendar section subscriptions.",
			CommandText: "订阅",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.subscription(ctx, ident, args)
		},
	},
	{
		Name:       "notify",
		Aliases:    []string{"notify", "notice", "push", "提醒", "通知", "推送", "设置"},
		HasHelp:    true,
		NeedsStore: true,
		Normalize:  normalizeNotifyArgs,
		AgentTools: []AgentToolSpec{{
			Name:        "get_notification_settings",
			Description: "Get the user's active push notification settings for upcoming classes and homework reminders.",
			CommandText: "通知",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.notify(ctx, ident, args)
		},
	},
	{
		Name:       "agent",
		Aliases:    []string{"agent", "ai", "llm", "tool", "tools", "工具", "调试"},
		HasHelp:    true,
		NeedsStore: true,
		Normalize:  normalizeAgentArgs,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.agentSettings(ctx, ident, args)
		},
	},
	{
		Name:    "feedback",
		Aliases: []string{"feedback", "fb", "反馈", "意见", "建议", "吐槽"},
		HasHelp: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.feedback(ctx, ident, args)
		},
	},
	{
		Name:      "ping",
		Aliases:   []string{"p", "ping"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			if err := h.Life.Health(ctx); err != nil {
				return "Life @ USTC API unavailable: " + err.Error()
			}
			return "Life @ USTC API is reachable."
		},
	},
	{
		Name:      "status",
		Aliases:   []string{"status", "zt", "状态"},
		NeedsLife: true,
		AgentTools: []AgentToolSpec{{
			Name:        "get_bot_status",
			Description: "Get Life API reachability and login status.",
			CommandText: "状态",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.status(ctx, ident)
		},
	},
	{
		Name:      "semester",
		Aliases:   []string{"semester", "term", "学期", "xq"},
		NeedsLife: true,
		AgentTools: []AgentToolSpec{{
			Name:        "get_current_semester",
			Description: "Get the current Life USTC semester.",
			CommandText: "学期",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.currentSemester(ctx)
		},
	},
	{
		Name:      "course",
		Aliases:   []string{"course", "kc", "课程"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchCourses(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "section",
		Aliases:   []string{"section", "class", "bj", "教学班", "班级"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchSections(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "teacher",
		Aliases:   []string{"teacher", "teachers", "ls", "js", "老师", "教师"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchTeachers(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "bus",
		Aliases:   []string{"bus", "xc", "校车", "车"},
		HasHelp:   true,
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.bus(ctx, ident, args)
		},
	},
	{
		Name:      "schedule",
		Aliases:   scheduleAliases,
		HasHelp:   true,
		NeedsLife: true,
		NeedsAuth: true,
		Normalize: normalizeScheduleArgs,
		AgentTools: []AgentToolSpec{
			{Name: "get_two_day_curriculum", Description: "Get the user's curriculum for today and tomorrow.", CommandText: "课表"},
			{Name: "get_today_curriculum", Description: "Get the user's curriculum for today.", CommandText: "课表 今天"},
			{Name: "get_tomorrow_curriculum", Description: "Get the user's curriculum for tomorrow.", CommandText: "课表 明天"},
		},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.curriculum(ctx, ident, args)
		},
	},
	{
		Name:      "nextclass",
		Aliases:   []string{"nextclass", "next", "下一节", "下节课", "下一节课"},
		NeedsLife: true,
		NeedsAuth: true,
		AgentTools: []AgentToolSpec{{
			Name:        "get_next_class",
			Description: "Get the user's next upcoming class.",
			CommandText: "下一节课",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.nextClass(ctx, ident)
		},
	},
	{
		Name:      "exam",
		Aliases:   []string{"exam", "exams", "ks", "考试"},
		NeedsLife: true,
		NeedsAuth: true,
		AgentTools: []AgentToolSpec{{
			Name:        "list_exams",
			Description: "List exams from the user's subscribed teaching sections.",
			CommandText: "考试",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.exams(ctx, ident)
		},
	},
	{
		Name:      "list_semesters",
		Aliases:   []string{"list_semesters", "学期列表"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.listSemesters(ctx, args)
		},
	},
	{
		Name:      "course_search",
		Aliases:   []string{"course_search", "课程搜索"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchCoursesWithFilters(ctx, args)
		},
	},
	{
		Name:      "section_search",
		Aliases:   []string{"section_search", "教学班搜索"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchSectionsWithFilters(ctx, args)
		},
	},
	{
		Name:      "teacher_search",
		Aliases:   []string{"teacher_search", "老师搜索"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.searchTeachersWithFilters(ctx, args)
		},
	},
	{
		Name:      "course_by_jw_id",
		Aliases:   []string{"course_by_jw_id", "课程编号"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.getCourseByJwID(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "section_by_jw_id",
		Aliases:   []string{"section_by_jw_id", "教学班编号"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.getSectionByJwID(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "teacher_by_id",
		Aliases:   []string{"teacher_by_id", "老师编号"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.getTeacherByID(ctx, joinedArgs(args))
		},
	},
	{
		Name:      "bus_routes",
		Aliases:   []string{"bus_routes", "校车路线"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.busRoutes(ctx, args)
		},
	},
	{
		Name:      "unsubscribe_section_by_jw_id",
		Aliases:   []string{"unsubscribe_section_by_jw_id", "退订教学班"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.unsubscribeSectionByJwID(ctx, ident, joinedArgs(args))
		},
	},
	{
		Name:      "my_subscribed_sections",
		Aliases:   []string{"my_subscribed_sections", "我的订阅"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.mySubscribedSections(ctx, ident)
		},
	},
	{
		Name:      "section_schedules",
		Aliases:   []string{"section_schedules", "教学班课表"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionSchedules(ctx, ident, args)
		},
	},
	{
		Name:      "section_exams",
		Aliases:   []string{"section_exams", "教学班考试"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionExams(ctx, ident, joinedArgs(args))
		},
	},
	{
		Name:      "section_homeworks",
		Aliases:   []string{"section_homeworks", "教学班作业"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionHomeworks(ctx, ident, joinedArgs(args))
		},
	},
	{
		Name:      "dashboard",
		Aliases:   []string{"dashboard", "概览"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.myDashboard(ctx, ident)
		},
	},
	{
		Name:      "upcoming_deadlines",
		Aliases:   []string{"upcoming_deadlines", "近期截止"},
		NeedsLife: true,
		NeedsAuth: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.upcomingDeadlines(ctx, ident, args)
		},
	},
}

func (h Handler) parse(text string) (parsedCommand, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return parsedCommand{}, false
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return parsedCommand{}, false
	}
	prefix := h.Prefix
	if prefix == "" {
		prefix = "/life"
	}

	if fields[0] == prefix {
		if len(fields) == 1 {
			return helpCommand(raw), true
		}
		name, args := normalizeCommand(fields[1], fields[2:])
		if name == "" {
			return helpCommand(raw), true
		}
		return commandResult(raw, name, args), true
	}
	if strings.HasPrefix(fields[0], prefix) {
		name, args := normalizeCommand(strings.TrimPrefix(fields[0], prefix), fields[1:])
		if name == "" {
			return helpCommand(raw), true
		}
		return commandResult(raw, name, args), true
	}

	if isHelpToken(fields[0]) {
		return helpCommand(raw), true
	}

	if len(fields) >= 2 {
		joined := fields[0] + fields[1]
		if name, args, ok := normalizeJoinedCommand(joined, fields[2:]); ok {
			return commandResult(raw, name, args), true
		}
	}

	name, args := normalizeCommand(fields[0], fields[1:])
	if name == "" {
		return parsedCommand{}, false
	}
	return commandResult(raw, name, args), true
}

func helpCommand(raw string) parsedCommand {
	return parsedCommand{Name: "help", Raw: raw}
}

func commandResult(raw, name string, args []string) parsedCommand {
	return parsedCommand{Name: name, Args: args, Raw: raw}
}

func normalizeCommand(name string, args []string) (string, []string) {
	key := commandToken(name)
	if isHelpToken(key) {
		return "help", args
	}
	if normalized, normalizedArgs, ok := normalizeJoinedCommand(name, args); ok {
		return normalized, normalizedArgs
	}
	if tail, ok := splitAttachedAlias(name, attachedFeedbackAliases); ok {
		return "feedback", append([]string{tail}, args...)
	}
	if tail, ok := splitAttachedAlias(name, attachedNotifyAliases); ok {
		return "notify", normalizeNotifyArgs(append([]string{tail}, args...))
	}
	for _, spec := range commandSpecs {
		for _, alias := range spec.Aliases {
			if key != commandToken(alias) {
				continue
			}
			if spec.Normalize != nil {
				args = spec.Normalize(args)
			}
			return spec.Name, args
		}
	}
	return "", args
}

func commandSpec(name string) (CommandSpec, bool) {
	for _, spec := range commandSpecs {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

func normalizeJoinedCommand(name string, args []string) (string, []string, bool) {
	key := commandToken(name)
	if day, ok := joinedScheduleDay(key); ok {
		return "schedule", []string{day}, true
	}
	switch key {
	case "下一节课":
		return "nextclass", nil, true
	case "校车偏好", "校车默认", "车偏好", "车默认", "xc偏好", "xc默认", "buspref", "busprefs", "buspreference", "buspreferences":
		return "bus", []string{"偏好"}, true
	case "校车设置", "车设置", "xc设置", "busset":
		return "bus", append([]string{"设置"}, args...), true
	case "校车已发车", "车已发车", "xc已发车", "busdeparted", "busshow-departed":
		return "bus", append([]string{"已发车"}, args...), true
	}
	return "", args, false
}

func joinedScheduleDay(key string) (string, bool) {
	for _, scheduleToken := range scheduleAliases {
		if strings.HasPrefix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[len(scheduleToken):]); ok {
				return day, true
			}
			if date, ok := normalizeScheduleDateToken(key[len(scheduleToken):]); ok {
				return date, true
			}
		}
		if strings.HasSuffix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[:len(key)-len(scheduleToken)]); ok {
				return day, true
			}
			if date, ok := normalizeScheduleDateToken(key[:len(key)-len(scheduleToken)]); ok {
				return date, true
			}
		}
	}
	return "", false
}

func normalizeScheduleDay(value string) (string, bool) {
	switch value {
	case "today", "今天", "今日":
		return "today", true
	case "tomorrow", "明天", "明日":
		return "tomorrow", true
	default:
		return "", false
	}
}

func normalizeScheduleDateToken(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if _, ok := parseScheduleDateToken(value, chinaNow()); !ok {
		return "", false
	}
	return "date:" + value, true
}

func parseScheduleDateToken(value string, base time.Time) (time.Time, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "date:"))
	value = strings.Trim(value, "，,。")
	if value == "" {
		return time.Time{}, false
	}
	loc := lifedata.ChinaLocation()
	base = base.In(loc)
	if parsed, err := time.ParseInLocation("2006-01-02", value, loc); err == nil {
		return parsed, true
	}
	if parsed, err := time.ParseInLocation("2006/1/2", value, loc); err == nil {
		return parsed, true
	}
	normalized := strings.NewReplacer("月", "-", "日", "", "/", "-", ".", "-").Replace(value)
	parts := strings.Split(normalized, "-")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	month, errMonth := strconv.Atoi(strings.TrimSpace(parts[0]))
	day, errDay := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errMonth != nil || errDay != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	parsed := time.Date(base.Year(), time.Month(month), day, 0, 0, 0, 0, loc)
	if parsed.Month() != time.Month(month) || parsed.Day() != day {
		return time.Time{}, false
	}
	return parsed, true
}

func normalizeSubscriptionArgs(args []string) []string {
	if !hasArgs(args) {
		return args
	}
	if isHelpToken(args[0]) {
		return withFirstArg(args, "help")
	}
	switch normToken(args[0]) {
	case "import", "bulk", "add", "+", "导入", "批量", "添加", "新增":
		return withFirstArg(args, "import")
	}
	return args
}

func normalizeLoginArgs(args []string) []string {
	if !hasArgs(args) {
		return args
	}
	if isHelpToken(args[0]) {
		return withFirstArg(args, "help")
	}
	switch normToken(args[0]) {
	case "status", "check", "完成", "状态", "ok", "好了":
		return withFirstArg(args, "status")
	}
	return args
}

func normalizeTodoArgs(args []string) []string {
	if !hasArgs(args) {
		return args
	}
	if isHelpToken(args[0]) {
		return withFirstArg(args, "help")
	}
	switch normToken(args[0]) {
	case "add", "new", "create", "+", "添加", "新增", "加":
		return withFirstArg(args, "add")
	case "done", "finish", "complete", "ok", "x", "完成", "好了":
		return withFirstArg(args, "done")
	case "undo", "undone", "reopen", "reset", "取消", "撤销":
		return withFirstArg(args, "undo")
	case "delete", "del", "remove", "rm", "删除":
		return withFirstArg(args, "delete")
	case "update", "edit", "set", "修改", "更新":
		return withFirstArg(args, "update")
	case "list", "ls", "查看", "列表":
		return withFirstArg(args, "list")
	case "all", "全部":
		return withFirstArg(args, "all")
	case "pending", "todo", "未完成":
		return withFirstArg(args, "pending")
	case "completed", "finished", "已完成":
		return withFirstArg(args, "completed")
	}
	return args
}

func normalizeHomeworkArgs(args []string) []string {
	if !hasArgs(args) {
		return args
	}
	if isHelpToken(args[0]) {
		return withFirstArg(args, "help")
	}
	switch normToken(args[0]) {
	case "done", "finish", "complete", "ok", "x", "完成", "好了":
		return withFirstArg(args, "done")
	case "undo", "undone", "reset", "取消", "撤销":
		return withFirstArg(args, "undo")
	case "pending", "未完成":
		return withFirstArg(args, "pending")
	case "all", "全部":
		return withFirstArg(args, "all")
	}
	return args
}

func normalizeScheduleArgs(args []string) []string {
	if !hasArgs(args) {
		return args
	}
	if isHelpToken(args[0]) {
		return withFirstArg(args, "help")
	}
	if day, ok := normalizeScheduleDay(normToken(args[0])); ok {
		return withFirstArg(args, day)
	}
	if date, ok := normalizeScheduleDateToken(args[0]); ok {
		return withFirstArg(args, date)
	}
	return args
}

func normalizeNotifyArgs(args []string) []string {
	out := make([]string, 0, len(args)+1)
	for _, arg := range args {
		if kind, state, ok := splitCompactNotificationArg(arg); ok {
			out = append(out, kind, state)
			continue
		}
		if isHelpToken(arg) {
			out = append(out, "help")
			continue
		}
		if kind, ok := NormalizeNotificationKind(arg); ok {
			out = append(out, kind)
			continue
		}
		if state, ok := normalizeNotificationState(arg); ok {
			out = append(out, state)
			continue
		}
		switch normToken(arg) {
		case "status", "状态", "查看":
			out = append(out, "status")
		default:
			out = append(out, arg)
		}
	}
	return out
}

func normalizeAgentArgs(args []string) []string {
	out := copyArgs(args)
	if len(out) > 0 {
		switch normToken(out[0]) {
		case "tool", "tools", "工具", "调用":
			out = out[1:]
		}
	}
	for i, arg := range out {
		if isHelpToken(arg) {
			out[i] = "help"
			continue
		}
		switch normToken(arg) {
		case "on", "enable", "enabled", "open", "开启", "打开", "开":
			out[i] = "on"
		case "off", "disable", "disabled", "close", "关闭", "关":
			out[i] = "off"
		case "status", "状态", "查看":
			out[i] = "status"
		}
	}
	return out
}

func NormalizeNotificationKind(value string) (string, bool) {
	switch normToken(value) {
	case "class", "classes", "section", "sections", "schedule", "curriculum", "kb", "课表", "课程", "上课":
		return "classes", true
	case "homework", "hw", "作业":
		return "homework", true
	default:
		return "", false
	}
}

func normalizeNotificationState(value string) (string, bool) {
	switch normToken(value) {
	case "on", "enable", "enabled", "open", "开启", "打开", "开":
		return "on", true
	case "off", "disable", "disabled", "close", "关闭", "关":
		return "off", true
	default:
		return "", false
	}
}

func splitCompactNotificationArg(value string) (string, string, bool) {
	token := compactCommandToken(value)
	if token == "" {
		return "", "", false
	}
	for _, item := range []struct {
		kind    string
		aliases []string
	}{
		{kind: "classes", aliases: []string{"class", "classes", "section", "sections", "schedule", "curriculum", "kb", "课表", "课程", "上课"}},
		{kind: "homework", aliases: []string{"homework", "hw", "作业"}},
	} {
		for _, alias := range item.aliases {
			aliasToken := compactCommandToken(alias)
			if token == aliasToken || !strings.HasPrefix(token, aliasToken) {
				continue
			}
			state, ok := normalizeCompactNotificationState(strings.TrimPrefix(token, aliasToken))
			if ok {
				return item.kind, state, true
			}
		}
	}
	return "", "", false
}

func normalizeCompactNotificationState(value string) (string, bool) {
	for _, alias := range []string{"on", "enable", "enabled", "open", "开启", "打开", "开"} {
		if value == compactCommandToken(alias) {
			return "on", true
		}
	}
	for _, alias := range []string{"off", "disable", "disabled", "close", "关闭", "关"} {
		if value == compactCommandToken(alias) {
			return "off", true
		}
	}
	return "", false
}

func normToken(value string) string {
	return textutil.LowerTrim(value)
}

func commandToken(value string) string {
	return strings.TrimLeft(normToken(value), "/")
}

func compactCommandToken(value string) string {
	token := commandToken(value)
	replacer := strings.NewReplacer("呃", "", "额", "", "嗯", "", "啊", "")
	return replacer.Replace(token)
}

func splitAttachedAlias(name string, aliases []string) (string, bool) {
	key := commandToken(name)
	display := strings.TrimLeft(strings.TrimSpace(name), "/")
	for _, alias := range aliases {
		aliasKey := commandToken(alias)
		if key == aliasKey || !strings.HasPrefix(key, aliasKey) {
			continue
		}
		displayRunes := []rune(display)
		aliasRunes := []rune(alias)
		if len(displayRunes) <= len(aliasRunes) {
			continue
		}
		tail := strings.TrimSpace(string(displayRunes[len(aliasRunes):]))
		if tail != "" {
			return tail, true
		}
	}
	return "", false
}

func isHelpToken(value string) bool {
	switch normToken(value) {
	case "/help", "/?", "-h", "--help", "help", "?", "？", "帮助", "菜单":
		return true
	default:
		return false
	}
}

func withFirstArg(args []string, value string) []string {
	next := copyArgs(args)
	next[0] = value
	return next
}

func copyArgs(args []string) []string {
	return append([]string(nil), args...)
}

func (h Handler) hasAdditionalCommandLine(text string) bool {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	seenCommand := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := h.parse(line); !ok {
			continue
		}
		if seenCommand {
			return true
		}
		seenCommand = true
	}
	return false
}

func hasArgs(args []string) bool {
	return len(args) > 0
}

func firstArgIs(args []string, value string) bool {
	return hasArgs(args) && args[0] == value
}

func firstArgIn(args []string, values ...string) bool {
	if !hasArgs(args) {
		return false
	}
	for _, value := range values {
		if args[0] == value {
			return true
		}
	}
	return false
}

func joinedArgs(args []string) string {
	return textutil.JoinNonEmpty(" ", args...)
}

func (h Handler) help() string {
	return "可以直接发：" + strings.Join([]string{
		"待办 / td",
		"td 写报告",
		"td done 1",
		"作业 / hw",
		"作业 done 1",
		"今日 / ddl",
		"校车 / xc",
		"xc 东区 西区",
		"今天课表 / 明天课表",
		"下一节课",
		"订阅",
		"通知",
		"AI 工具",
		"状态 / status",
		"我 / me",
		"反馈 你的建议",
		"课程 数学分析",
		"教学班 高等数学",
		"老师 张",
		"考试 / ks",
		"登录 / 登录 状态",
	}, "；")
}

func (h Handler) feedback(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"反馈用法：",
			"反馈 希望校车能显示更多路线",
			"fb 这里写你的建议",
		}, "\n")
	}
	text := strings.TrimSpace(joinedArgs(args))
	if text == "" {
		return "想反馈什么？例如：反馈 校车时间希望更清楚"
	}
	contextText := h.feedbackContext(ctx, ident)
	feedbackID, err := h.recordFeedback(ctx, ident, store.FeedbackRecord{
		Source:   "user",
		Category: "user_feedback",
		Content:  text,
		Context:  contextText,
	})
	if err != nil {
		return commandError("反馈保存失败：", err)
	}
	if h.FeedbackSend == nil || (len(h.FeedbackUsers) == 0 && len(h.FeedbackGroups) == 0) {
		if feedbackID > 0 {
			return "已收到反馈。"
		}
		return "反馈通道还没配置。"
	}
	message := formatFeedbackMessage(ident, text, contextText, feedbackID)
	sent := 0
	for _, userID := range h.FeedbackUsers {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if err := h.FeedbackSend(ctx, store.Identity{
			Platform:         ident.Platform,
			UserID:           userID,
			ConversationType: "private",
			ConversationID:   userID,
		}, message); err != nil {
			h.logf("send feedback failed: id=%d platform=%s target=private:%s error=%v", feedbackID, ident.Platform, userID, err)
			continue
		}
		sent++
	}
	for _, groupID := range h.FeedbackGroups {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if err := h.FeedbackSend(ctx, store.Identity{
			Platform:         ident.Platform,
			ConversationType: "group",
			ConversationID:   groupID,
		}, message); err != nil {
			h.logf("send feedback failed: id=%d platform=%s target=group:%s error=%v", feedbackID, ident.Platform, groupID, err)
			continue
		}
		sent++
	}
	if sent == 0 {
		if feedbackID > 0 {
			return "已收到反馈。"
		}
		return "反馈发送失败，请稍后再试。"
	}
	h.markFeedbackSent(ctx, feedbackID, sent)
	return "已收到反馈，会转给维护者。"
}

func (h Handler) feedbackContext(ctx context.Context, ident store.Identity) string {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return ""
	}
	recent, err := h.Store.RecentHandledInteractions(ctx, ident, feedbackContextLookback)
	if err != nil {
		h.logf("load feedback context failed: %v", err)
		return ""
	}
	return formatFeedbackContext(recent)
}

func formatFeedbackContext(interactions []store.Interaction) string {
	lines := []string{}
	for _, interaction := range interactions {
		if strings.EqualFold(strings.TrimSpace(interaction.Command), "feedback") {
			continue
		}
		raw := compactFeedbackContextText(interaction.RawText)
		reply := compactFeedbackContextText(interaction.Reply)
		if raw != "" {
			lines = append(lines, "用户："+raw)
		}
		if reply != "" {
			lines = append(lines, "Bot："+reply)
		}
		if len(lines) >= feedbackContextLimit*2 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func compactFeedbackContextText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= feedbackContextTextRunes {
		return text
	}
	return string(runes[:feedbackContextTextRunes]) + "..."
}

func formatFeedbackMessage(ident store.Identity, text, contextText string, id int64) string {
	source := ident.ConversationType
	if ident.ConversationID != "" {
		source += ":" + ident.ConversationID
	}
	userID := strings.TrimSpace(ident.UserID)
	if userID == "" {
		userID = "unknown"
	}
	lines := []string{
		"用户反馈",
		"来源：" + source,
		"用户：" + userID,
		"内容：" + text,
	}
	if strings.TrimSpace(contextText) != "" {
		lines = append(lines, "最近对话：", contextText)
	}
	if id > 0 {
		lines = append(lines, fmt.Sprintf("编号：#%d", id))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) login(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"登录用法：",
			"登录",
			"登录 状态",
		}, "\n")
	}
	if h.Auth == nil {
		return "登录未配置。"
	}
	if firstArgIs(args, "status") {
		result, err := h.Auth.PollDeviceLogin(ctx, ident)
		if err != nil {
			return commandError("登录状态查不到：", err)
		}
		return result.Message
	}
	session, err := h.Auth.BeginDeviceLogin(ctx, ident)
	if err != nil {
		return commandError("登录开始失败：", err)
	}
	link := session.VerificationURIComplete
	if link == "" {
		link = session.VerificationURI
	}
	return strings.Join([]string{
		"Life @ USTC 登录：",
		link,
		"验证码：" + session.UserCode,
		"系统将自动检查登录状态。",
		"手动查询：登录 状态",
	}, "\n")
}

func (h Handler) logout(ctx context.Context, ident store.Identity) string {
	if h.Auth == nil {
		return "登录未配置。"
	}
	if err := h.Auth.Logout(ctx, ident); err != nil {
		return commandError("退出失败：", err)
	}
	return "已退出登录。"
}

func (h Handler) me(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	me, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.Me(ctx, token)
	})
	if err != nil {
		return commandError("个人信息查不到：", err)
	}
	name := lifedata.FirstString(me, "name", "username", "preferred_username", "email")
	if name == "" {
		name = lifedata.FirstString(me, "id", "sub")
	}
	return "已登录：" + name
}

func (h Handler) todo(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"待办用法：",
			"待办",
			"td",
			"待办 add 写报告",
			"td 写报告",
			"td + 买咖啡",
			"td done 1",
			"td done 1,2,3",
			"td undo 1",
			"td delete 1",
			"td update 1 title 写报告 priority high due 2026-06-10",
			"td all / td high / td completed",
		}, "\n")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if firstArgIs(args, "add") {
		opts := parseTodoCreateArgs(args[1:])
		if opts.Title == "" {
			return "想加什么？例如：待办 add 写报告"
		}
		return h.createTodo(ctx, ident, token, opts)
	}
	if firstArgIs(args, "done") {
		target := joinedArgs(args[1:])
		if target == "" {
			return "想完成哪条？例如：td done 1"
		}
		return h.setTodoCompletion(ctx, ident, token, target, true)
	}
	if firstArgIs(args, "undo") {
		target := joinedArgs(args[1:])
		if target == "" {
			return "想恢复哪条？例如：td undo 1"
		}
		return h.setTodoCompletion(ctx, ident, token, target, false)
	}
	if firstArgIs(args, "delete") {
		target := joinedArgs(args[1:])
		if target == "" {
			return "想删除哪条？例如：td delete 1"
		}
		return h.deleteTodo(ctx, ident, token, target)
	}
	if firstArgIs(args, "update") {
		if len(args) < 3 {
			return "想改哪条、改什么？例如：td update 1 title 写报告"
		}
		return h.updateTodo(ctx, ident, token, args[1], args[2:])
	}
	if opts, ok, err := todoListOptionsFromArgs(args); ok {
		if err != nil {
			return err.Error()
		}
		return h.listTodos(ctx, ident, token, opts)
	}
	if hasArgs(args) {
		return h.createTodo(ctx, ident, token, parseTodoCreateArgs(args))
	}
	return h.listTodos(ctx, ident, token, life.TodoListOptions{Completed: "false"})
}

func todoCompletionReply(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "已完成。"
	}
	return "已完成：" + title
}

func todoUndoReply(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "已恢复。"
	}
	return "已恢复：" + title
}

func (h Handler) createTodo(ctx context.Context, ident store.Identity, token string, opts life.TodoCreateOptions) string {
	opts.Title = strings.TrimSpace(opts.Title)
	if opts.Title == "" {
		return "想加什么？例如：td 写报告"
	}
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CreateTodoWithOptions(ctx, token, opts)
	})
	if err != nil {
		return commandError("待办添加失败：", err)
	}
	return "已加待办：" + opts.Title
}

func (h Handler) listTodos(ctx context.Context, ident store.Identity, token string, opts life.TodoListOptions) string {
	todos, err := h.todos(ctx, ident, token, opts)
	if err != nil {
		return commandError("待办查不到：", err)
	}
	if len(todos) == 0 {
		return "没有待办。"
	}
	lines := []string{"待办："}
	for i, todo := range todos {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(todos)-i, false))
			break
		}
		lines = append(lines, formatNumberedLine(i+1, formatTodo(todo)))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) setTodoCompletion(ctx context.Context, ident store.Identity, token, target string, completed bool) string {
	completedFilter := "false"
	if !completed {
		completedFilter = "true"
	}
	targets := splitTodoTargets(target)
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{Completed: completedFilter})
	if err != nil {
		return commandError("待办查不到：", err)
	}
	if len(targets) > 1 {
		return h.setTodoCompletionBatch(ctx, ident, token, todos, targets, completed)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		if completed {
			return "没找到这条待办。发 td 看编号，再试：td done 1"
		}
		return "没找到这条已完成待办。发 td completed 看编号，再试：td undo 1"
	}
	return h.setTodoCompletionItem(ctx, ident, token, todo, completed)
}

func (h Handler) setTodoCompletionBatch(ctx context.Context, ident store.Identity, token string, todos []map[string]any, targets []string, completed bool) string {
	items := make([]life.TodoCompletionItem, 0, len(targets))
	done := make([]string, 0, len(targets))
	missing := []string{}
	for _, target := range targets {
		todo, ok := resolveTodo(todos, target)
		if !ok {
			missing = append(missing, target)
			continue
		}
		id := lifedata.FirstString(todo, "id")
		if id == "" {
			return "这条待办没有可用 ID，暂时操作不了。"
		}
		items = append(items, life.TodoCompletionItem{TodoID: id, Completed: completed})
		done = append(done, lifedata.FirstString(todo, "title"))
	}
	if len(items) > 0 {
		err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.SetTodoCompletions(ctx, token, items)
		})
		if err != nil {
			if completed {
				return commandError("待办完成失败：", err)
			}
			return commandError("待办恢复失败：", err)
		}
	}
	if len(done) == 0 {
		if completed {
			return "没找到这些待办。发 td 看编号，再试：td done 1,2,3"
		}
		return "没找到这些已完成待办。发 td completed 看编号，再试：td undo 1,2,3"
	}
	action := "完成"
	if !completed {
		action = "恢复"
	}
	lines := []string{fmt.Sprintf("已%s %d 条：", action, len(done))}
	for _, title := range done {
		if strings.TrimSpace(title) == "" {
			title = "待办"
		}
		lines = append(lines, "- "+title)
	}
	if len(missing) > 0 {
		lines = append(lines, "没找到："+strings.Join(missing, ", "))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) setTodoCompletionItem(ctx context.Context, ident store.Identity, token string, todo map[string]any, completed bool) string {
	id := lifedata.FirstString(todo, "id")
	if id == "" {
		return "这条待办没有可用 ID，暂时操作不了。"
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.SetTodoCompleted(ctx, token, id, completed)
	})
	if err != nil {
		if completed {
			return commandError("待办完成失败：", err)
		}
		return commandError("待办恢复失败：", err)
	}
	title := lifedata.FirstString(todo, "title")
	if completed {
		return todoCompletionReply(title)
	}
	return todoUndoReply(title)
}

func (h Handler) pendingTodos(ctx context.Context, ident store.Identity, token string) ([]map[string]any, error) {
	return h.todos(ctx, ident, token, life.TodoListOptions{Completed: "false"})
}

func (h Handler) todos(ctx context.Context, ident store.Identity, token string, opts life.TodoListOptions) ([]map[string]any, error) {
	todos, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.TodosWithOptions(ctx, token, opts)
	})
	return todos, err
}

func (h Handler) updateTodo(ctx context.Context, ident store.Identity, token, target string, args []string) string {
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{})
	if err != nil {
		return commandError("待办查不到：", err)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		return "没找到这条待办。发 td all 看编号，再试：td update 1 title 写报告"
	}
	id := lifedata.FirstString(todo, "id")
	if id == "" {
		return "这条待办没有可用 ID，暂时修改不了。"
	}
	opts := parseTodoUpdateArgs(args)
	if !hasTodoUpdate(opts) {
		return "想改什么？例如：td update 1 title 写报告"
	}
	err = auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.UpdateTodo(ctx, token, id, opts)
	})
	if err != nil {
		return commandError("待办修改失败：", err)
	}
	return "已修改待办：" + lifedata.FirstString(todo, "title", "id")
}

func (h Handler) deleteTodo(ctx context.Context, ident store.Identity, token, target string) string {
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{})
	if err != nil {
		return commandError("待办查不到：", err)
	}
	targets := splitTodoTargets(target)
	if len(targets) > 1 {
		return h.deleteTodoBatch(ctx, ident, token, todos, targets)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		return "没找到这条待办。发 td all 看编号，再试：td delete 1"
	}
	return h.deleteTodoItem(ctx, ident, token, todo)
}

func (h Handler) deleteTodoBatch(ctx context.Context, ident store.Identity, token string, todos []map[string]any, targets []string) string {
	deleted := make([]string, 0, len(targets))
	missing := []string{}
	for _, target := range targets {
		todo, ok := resolveTodo(todos, target)
		if !ok {
			missing = append(missing, target)
			continue
		}
		reply := h.deleteTodoItem(ctx, ident, token, todo)
		if strings.HasPrefix(reply, "待办删除失败：") || strings.Contains(reply, "没有可用 ID") {
			return reply
		}
		deleted = append(deleted, strings.TrimPrefix(reply, "已删除："))
	}
	if len(deleted) == 0 {
		return "没找到这些待办。发 td all 看编号，再试：td delete 1,2,3"
	}
	lines := []string{fmt.Sprintf("已删除 %d 条：", len(deleted))}
	for _, title := range deleted {
		if strings.TrimSpace(title) == "" {
			title = "待办"
		}
		lines = append(lines, "- "+title)
	}
	if len(missing) > 0 {
		lines = append(lines, "没找到："+strings.Join(missing, ", "))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) deleteTodoItem(ctx context.Context, ident store.Identity, token string, todo map[string]any) string {
	id := lifedata.FirstString(todo, "id")
	if id == "" {
		return "这条待办没有可用 ID，暂时删除不了。"
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.DeleteTodo(ctx, token, id)
	})
	if err != nil {
		return commandError("待办删除失败：", err)
	}
	title := strings.TrimSpace(lifedata.FirstString(todo, "title"))
	if title == "" {
		return "已删除。"
	}
	return "已删除：" + title
}

func splitTodoTargets(target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if !strings.ContainsAny(target, ",，;；") {
		return []string{target}
	}
	parts := strings.FieldsFunc(target, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})
	targets := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			targets = append(targets, part)
		}
	}
	return targets
}

func resolveTodo(todos []map[string]any, target string) (map[string]any, bool) {
	return resolveByTarget(todos, target)
}

func formatTodo(todo map[string]any) string {
	title := lifedata.FirstString(todo, "title")
	due := lifedata.FormatAPITime(lifedata.FirstString(todo, "dueAt"))
	parts := []string{}
	if due != "" {
		parts = append(parts, "截止 "+due)
	}
	if title != "" {
		parts = append(parts, title)
	}
	if len(parts) == 0 {
		return lifedata.FirstString(todo, "id")
	}
	return strings.Join(parts, " ")
}

func todoListOptionsFromArgs(args []string) (life.TodoListOptions, bool, error) {
	if len(args) == 0 {
		return life.TodoListOptions{}, false, nil
	}
	opts := life.TodoListOptions{Completed: "false"}
	start := 0
	switch args[0] {
	case "list":
		start = 1
	case "all":
		opts.Completed = ""
		start = 1
	case "pending":
		start = 1
	case "completed":
		opts.Completed = "true"
		start = 1
	default:
		if priority, ok := normalizeTodoPriority(args[0]); ok {
			opts.Priority = priority
			return opts, true, nil
		}
		return life.TodoListOptions{}, false, nil
	}
	for i := start; i < len(args); i++ {
		token := normToken(args[i])
		switch token {
		case "all", "全部":
			opts.Completed = ""
		case "pending", "未完成":
			opts.Completed = "false"
		case "completed", "finished", "done", "已完成":
			opts.Completed = "true"
		case "before", "duebefore", "截止前":
			i++
			if i >= len(args) {
				return opts, true, errors.New("缺少日期。例如：td list before 2026-06-10")
			}
			opts.DueBefore = args[i]
		case "after", "dueafter", "截止后":
			i++
			if i >= len(args) {
				return opts, true, errors.New("缺少日期。例如：td list after 2026-06-10")
			}
			opts.DueAfter = args[i]
		case "priority", "p", "优先级":
			i++
			if i >= len(args) {
				return opts, true, errors.New("缺少优先级。可用：low / medium / high")
			}
			priority, ok := normalizeTodoPriority(args[i])
			if !ok {
				return opts, true, fmt.Errorf("优先级不认识：%s", args[i])
			}
			opts.Priority = priority
		default:
			if priority, ok := normalizeTodoPriority(args[i]); ok {
				opts.Priority = priority
			}
		}
	}
	return opts, true, nil
}

func parseTodoCreateArgs(args []string) life.TodoCreateOptions {
	fields := parseTodoFields(args)
	return life.TodoCreateOptions{
		Title:    fields.title,
		Content:  fields.content,
		Priority: fields.priority,
		DueAt:    fields.dueAt,
	}
}

func parseTodoUpdateArgs(args []string) life.TodoUpdateOptions {
	fields := parseTodoFields(args)
	return life.TodoUpdateOptions{
		Title:     fields.title,
		Content:   fields.content,
		Priority:  fields.priority,
		DueAt:     fields.dueAt,
		Completed: fields.completed,
	}
}

type todoFields struct {
	title     string
	content   string
	priority  string
	dueAt     string
	completed *bool
}

func parseTodoFields(args []string) todoFields {
	var fields todoFields
	var titleParts []string
	for i := 0; i < len(args); i++ {
		token := normToken(args[i])
		switch token {
		case "title", "标题":
			value, next := collectTodoFieldValue(args, i+1)
			titleParts = append(titleParts, value...)
			i = next - 1
		case "content", "note", "notes", "body", "内容", "备注":
			value, next := collectTodoFieldValue(args, i+1)
			fields.content = joinedArgs(value)
			i = next - 1
		case "due", "duetime", "ddl", "deadline", "截止", "到期":
			if i+1 < len(args) {
				fields.dueAt = args[i+1]
				i++
			}
		case "priority", "prio", "p", "优先级":
			if i+1 < len(args) {
				if priority, ok := normalizeTodoPriority(args[i+1]); ok {
					fields.priority = priority
				}
				i++
			}
		case "completed", "complete", "done", "完成":
			completed := true
			fields.completed = &completed
		case "not-completed", "pending", "undo", "reopen", "未完成", "取消", "撤销":
			completed := false
			fields.completed = &completed
		default:
			if priority, ok := normalizeTodoPriority(args[i]); ok {
				fields.priority = priority
			} else {
				titleParts = append(titleParts, args[i])
			}
		}
	}
	fields.title = joinedArgs(titleParts)
	return fields
}

func collectTodoFieldValue(args []string, start int) ([]string, int) {
	var value []string
	for i := start; i < len(args); i++ {
		if len(value) > 0 && isTodoFieldKey(args[i]) {
			return value, i
		}
		value = append(value, args[i])
	}
	return value, len(args)
}

func isTodoFieldKey(arg string) bool {
	switch normToken(arg) {
	case "title", "标题", "content", "note", "notes", "body", "内容", "备注", "due", "duetime", "ddl", "deadline", "截止", "到期", "priority", "prio", "p", "优先级", "completed", "complete", "done", "完成", "not-completed", "pending", "undo", "reopen", "未完成", "取消", "撤销":
		return true
	default:
		return false
	}
}

func normalizeTodoPriority(value string) (string, bool) {
	switch normToken(value) {
	case "low", "l", "低":
		return "low", true
	case "medium", "mid", "m", "normal", "普通", "中":
		return "medium", true
	case "high", "h", "urgent", "重要", "高":
		return "high", true
	default:
		return "", false
	}
}

func hasTodoUpdate(opts life.TodoUpdateOptions) bool {
	return strings.TrimSpace(opts.Title) != "" ||
		strings.TrimSpace(opts.Content) != "" ||
		strings.TrimSpace(opts.Priority) != "" ||
		strings.TrimSpace(opts.DueAt) != "" ||
		opts.Completed != nil
}

func (h Handler) overview(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	now := chinaNow()
	schedules, token, err := h.schedulesForDay(ctx, ident, token, now)
	if err != nil {
		return commandError("今日安排查不到：", err)
	}
	todos, err := h.pendingTodos(ctx, ident, token)
	if err != nil {
		return commandError("今日安排查不到：", err)
	}
	homeworks, err := h.homeworks(ctx, ident, token)
	if err != nil {
		return commandError("今日安排查不到：", err)
	}
	subscription, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return commandError("今日安排查不到：", err)
	}
	exams := upcomingSubscriptionExams(subscriptionExams(subscription), now)
	return formatOverview(now, schedules, todos, dueSoonHomeworks(homeworks, now), exams)
}

func formatOverview(now time.Time, schedules []map[string]any, todos []map[string]any, homeworks []map[string]any, exams []subscriptionExam) string {
	lines := []string{textutil.MonospaceDigits(now.In(lifedata.ChinaLocation()).Format("01-02")) + " 安排："}
	lines = appendOverviewSection(lines, "今日课表", schedules, formatSchedule)
	lines = appendOverviewSection(lines, "待办", todos, formatTodo)
	lines = appendOverviewSection(lines, "近期作业", homeworks, formatHomework)
	lines = appendOverviewSection(lines, "考试", exams, formatExam)
	if len(lines) == 1 {
		return lines[0] + "\n暂无安排。"
	}
	return strings.Join(lines, "\n")
}

func appendOverviewSection[T any](lines []string, title string, items []T, format func(T) string) []string {
	if len(items) == 0 {
		return lines
	}
	if len(lines) > 1 {
		lines = append(lines, "")
	}
	lines = append(lines, title+"：")
	for i, item := range items {
		if i >= 3 {
			lines = append(lines, moreLine(len(items)-i, true))
			break
		}
		lines = append(lines, formatNumberedLine(i+1, format(item)))
	}
	return lines
}

func dueSoonHomeworks(homeworks []map[string]any, now time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(homeworks))
	for _, homework := range homeworks {
		if lifedata.HomeworkCompleted(homework) {
			continue
		}
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
		if !ok || due.Before(now) || !due.After(now.Add(7*24*time.Hour)) {
			out = append(out, homework)
		}
	}
	lifedata.SortHomeworksByDue(out)
	return out
}

func (h Handler) homework(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"作业用法：",
			"作业",
			"作业 all",
			"作业 pending",
			"作业 semester_id <学期ID>",
			"作业 semester_jw_id <学期JW ID>",
			"作业 done 1",
			"作业 done 1,2,3",
			"作业 undo 1",
		}, "\n")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if firstArgIn(args, "done", "undo") {
		target := joinedArgs(args[1:])
		if target == "" {
			return "想改哪条作业？例如：作业 done 1"
		}
		completed := args[0] == "done"
		homeworks, err := h.homeworks(ctx, ident, token)
		if err != nil {
			return commandError("作业查不到：", err)
		}
		if completed {
			homeworks = filterHomeworks(homeworks, true)
		}
		targets := splitTodoTargets(target)
		if len(targets) > 1 {
			return h.setHomeworkCompletionBatch(ctx, ident, token, homeworks, targets, completed)
		}
		homework, ok := resolveHomework(homeworks, target)
		if !ok {
			return "没找到这条作业。发 作业 看编号，再试：作业 done 1"
		}
		return h.setHomeworkCompletionItem(ctx, ident, token, homework, completed)
	}
	listArgs := parseHomeworkListArgs(args)
	homeworks, err := h.homeworks(ctx, ident, token)
	if err != nil {
		return commandError("作业查不到：", err)
	}
	if listArgs.semesterID > 0 || listArgs.semesterJwID > 0 {
		homeworks = filterHomeworksBySemester(homeworks, listArgs.semesterID, listArgs.semesterJwID)
	}
	homeworks = filterHomeworks(homeworks, !listArgs.all)
	if len(homeworks) == 0 {
		if listArgs.all {
			return "没有作业。"
		}
		return "没有未完成作业。"
	}
	return formatHomeworkList(homeworks)
}

type homeworkListArgs struct {
	all          bool
	semesterID   int64
	semesterJwID int64
}

func parseHomeworkListArgs(args []string) homeworkListArgs {
	var out homeworkListArgs
	i := 0
	if i < len(args) {
		switch normToken(args[i]) {
		case "all":
			out.all = true
			i++
		case "pending":
			i++
		}
	}
	for i < len(args) {
		key := normToken(args[i])
		if i+1 >= len(args) {
			break
		}
		value := args[i+1]
		switch key {
		case "semester_id":
			if v, ok := parseIntArg(value); ok {
				out.semesterID = v
				i += 2
				continue
			}
		case "semester_jw_id":
			if v, ok := parseIntArg(value); ok {
				out.semesterJwID = v
				i += 2
				continue
			}
		}
		i++
	}
	return out
}

func homeworkSemesterIDs(homework map[string]any) (id, jwID int64) {
	section, _ := homework["section"].(map[string]any)
	if section == nil {
		return
	}
	semester, _ := section["semester"].(map[string]any)
	if semester == nil {
		return
	}
	return int64(lifedata.FirstInt(semester, "id")), int64(lifedata.FirstInt(semester, "jwId"))
}

func filterHomeworksBySemester(homeworks []map[string]any, semesterID, semesterJwID int64) []map[string]any {
	out := make([]map[string]any, 0, len(homeworks))
	for _, homework := range homeworks {
		id, jwID := homeworkSemesterIDs(homework)
		if semesterID > 0 && id == semesterID {
			out = append(out, homework)
			continue
		}
		if semesterJwID > 0 && jwID == semesterJwID {
			out = append(out, homework)
			continue
		}
	}
	return out
}

func homeworkCompletionReply(completed bool, title string) string {
	title = strings.TrimSpace(title)
	if completed {
		if title == "" {
			return "已完成作业。"
		}
		return "已完成作业：" + title
	}
	if title == "" {
		return "已取消完成。"
	}
	return "已取消完成：" + title
}

func (h Handler) setHomeworkCompletionBatch(ctx context.Context, ident store.Identity, token string, homeworks []map[string]any, targets []string, completed bool) string {
	items := make([]life.HomeworkCompletionItem, 0, len(targets))
	done := make([]string, 0, len(targets))
	missing := []string{}
	for _, target := range targets {
		homework, ok := resolveHomework(homeworks, target)
		if !ok {
			missing = append(missing, target)
			continue
		}
		id := lifedata.FirstString(homework, "id")
		if id == "" {
			return "这条作业没有可用 ID，暂时改不了。"
		}
		items = append(items, life.HomeworkCompletionItem{HomeworkID: id, Completed: completed})
		done = append(done, lifedata.FirstString(homework, "title"))
	}
	if len(items) > 0 {
		err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.SetHomeworkCompletions(ctx, token, items)
		})
		if err != nil {
			return commandError("作业状态更新失败：", err)
		}
	}
	if len(done) == 0 {
		return "没找到这些作业。发 作业 看编号，再试：作业 done 1,2,3"
	}
	action := "完成"
	if !completed {
		action = "取消完成"
	}
	lines := []string{fmt.Sprintf("已%s %d 条作业：", action, len(done))}
	for _, title := range done {
		if strings.TrimSpace(title) == "" {
			title = "作业"
		}
		lines = append(lines, "- "+title)
	}
	if len(missing) > 0 {
		lines = append(lines, "没找到："+strings.Join(missing, ", "))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) setHomeworkCompletionItem(ctx context.Context, ident store.Identity, token string, homework map[string]any, completed bool) string {
	id := lifedata.FirstString(homework, "id")
	if id == "" {
		return "这条作业没有可用 ID，暂时改不了。"
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.SetHomeworkCompletion(ctx, token, id, completed)
	})
	if err != nil {
		return commandError("作业状态更新失败：", err)
	}
	return homeworkCompletionReply(completed, lifedata.FirstString(homework, "title"))
}

func (h Handler) homeworks(ctx context.Context, ident store.Identity, token string) ([]map[string]any, error) {
	homeworks, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.SubscribedHomeworks(ctx, token)
	})
	lifedata.SortHomeworksByDue(homeworks)
	return homeworks, err
}

func filterHomeworks(homeworks []map[string]any, pendingOnly bool) []map[string]any {
	if !pendingOnly {
		return homeworks
	}
	out := make([]map[string]any, 0, len(homeworks))
	for _, homework := range homeworks {
		if !lifedata.HomeworkCompleted(homework) {
			out = append(out, homework)
		}
	}
	return out
}

func resolveHomework(homeworks []map[string]any, target string) (map[string]any, bool) {
	return resolveByTarget(homeworks, target)
}

func resolveByTarget(items []map[string]any, target string) (map[string]any, bool) {
	target = strings.TrimSpace(target)
	if index, err := strconv.Atoi(textutil.PlainDigits(target)); err == nil && index >= 1 && index <= len(items) {
		return items[index-1], true
	}
	needle := normalizedLookupText(target)
	if needle == "" {
		return nil, false
	}
	for _, item := range items {
		id := normalizedLookupText(lifedata.FirstString(item, "id"))
		title := normalizedLookupText(lifedata.FirstString(item, "title"))
		if needle == id || needle == title || strings.Contains(title, needle) {
			return item, true
		}
	}
	return nil, false
}

func normalizedLookupText(value string) string {
	return textutil.LowerTrim(textutil.PlainDigits(value))
}

func formatHomework(homework map[string]any) string {
	return textutil.MonospaceDigits(lifedata.HomeworkLabel(homework))
}

func formatHomeworkList(homeworks []map[string]any) string {
	return formatHomeworkListAt(homeworks, chinaNow())
}

func formatHomeworkListAt(homeworks []map[string]any, now time.Time) string {
	now = now.In(lifedata.ChinaLocation())
	groups := []struct {
		title string
		items []map[string]any
	}{
		{title: "已逾期"},
		{title: "近期"},
		{title: "未来"},
	}
	for _, homework := range homeworks {
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
		switch {
		case ok && due.Before(now):
			groups[0].items = append(groups[0].items, homework)
		case !ok || !due.After(now.Add(7*24*time.Hour)):
			groups[1].items = append(groups[1].items, homework)
		default:
			groups[2].items = append(groups[2].items, homework)
		}
	}
	lines := []string{"作业："}
	index := 1
	shown := 0
	for _, group := range groups {
		if len(group.items) == 0 {
			continue
		}
		if len(lines) > 1 {
			lines = append(lines, "")
		}
		lines = append(lines, group.title+"：")
		for _, homework := range group.items {
			if shown >= listDisplayLimit {
				lines = append(lines, moreLine(len(homeworks)-shown, true))
				return strings.Join(lines, "\n")
			}
			lines = append(lines, formatNumberedLine(index, formatHomework(homework)))
			index++
			shown++
		}
	}
	return strings.Join(lines, "\n")
}

var sectionCodePattern = regexp.MustCompile(`[A-Za-z0-9_.-]+\.[A-Za-z0-9]{2}`)

func (h Handler) subscription(ctx context.Context, ident store.Identity, args []string) string {
	if hasArgs(args) {
		switch args[0] {
		case "help":
			return subscriptionHelp()
		case "import":
			return h.bulkSubscribeSections(ctx, ident, joinedArgs(args[1:]))
		default:
			raw := joinedArgs(args)
			if len(extractSectionCodes(raw)) > 0 {
				return h.bulkSubscribeSections(ctx, ident, raw)
			}
		}
	}
	return h.subscriptionList(ctx, ident)
}

func subscriptionHelp() string {
	return strings.Join([]string{
		"订阅用法：",
		"订阅：查看当前日程订阅",
		"订阅 导入 <教学班代码...>：批量添加教学班",
		"例：订阅 导入 CONT5103P.01 CONT6104P.01",
	}, "\n")
}

func (h Handler) notify(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"通知用法：",
			"通知：查看设置",
			"通知 课表 开 / 通知 课表 关",
			"通知 作业 开 / 通知 作业 关",
		}, "\n")
	}
	if h.Store == nil {
		return "存储未配置。"
	}
	if !store.IsPrivateConversation(ident) {
		return "通知只能在私聊里设置。"
	}
	settings, err := h.Store.NotificationSettings(ctx, ident)
	if err != nil {
		return commandError("通知设置查不到：", err)
	}
	settings.Identity = ident
	if !hasArgs(args) || firstArgIs(args, "status") {
		return formatNotificationSettings(settings)
	}
	if len(args) < 2 {
		switch args[0] {
		case "classes", "homework":
			return "想打开还是关闭？例如：通知 作业 开"
		default:
			return "支持：课表、作业。"
		}
	}
	enabled := args[1] == "on"
	if args[1] != "on" && args[1] != "off" {
		return "想打开还是关闭？例如：通知 作业 开"
	}
	switch args[0] {
	case "classes":
		settings.ClassesEnabled = enabled
	case "homework":
		settings.HomeworkEnabled = enabled
	default:
		return "支持：课表、作业。"
	}
	if err := h.Store.SaveNotificationSettings(ctx, settings); err != nil {
		return commandError("通知设置保存失败：", err)
	}
	return formatNotificationSettings(settings)
}

func formatNotificationSettings(settings store.NotificationSettings) string {
	return strings.Join([]string{
		"通知设置：",
		"课前提醒：" + onOffText(settings.ClassesEnabled),
		"作业提醒：" + onOffText(settings.HomeworkEnabled),
	}, "\n")
}

func (h Handler) agentSettings(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"AI 工具用法：",
			"AI 工具：查看设置",
			"AI 工具 开：显示 LLM 工具调用",
			"AI 工具 关：隐藏 LLM 工具调用",
		}, "\n")
	}
	if h.Store == nil {
		return "存储未配置。"
	}
	if !store.IsPrivateConversation(ident) {
		return "AI 工具设置只能在私聊里设置。"
	}
	settings, err := h.Store.AgentSettings(ctx, ident)
	if err != nil {
		return commandError("AI 工具设置查不到：", err)
	}
	settings.Identity = ident
	if !hasArgs(args) || firstArgIs(args, "status") {
		return formatAgentSettings(settings)
	}
	switch args[0] {
	case "on":
		settings.ExposeToolCalls = true
	case "off":
		settings.ExposeToolCalls = false
	default:
		return "想打开还是关闭？例如：AI 工具 开"
	}
	if err := h.Store.SaveAgentSettings(ctx, settings); err != nil {
		return commandError("AI 工具设置保存失败：", err)
	}
	return formatAgentSettings(settings)
}

func formatAgentSettings(settings store.AgentSettings) string {
	return "AI 工具调用展示：" + onOffText(settings.ExposeToolCalls)
}

func onOffText(enabled bool) string {
	if enabled {
		return "开"
	}
	return "关"
}

func (h Handler) subscriptionList(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return commandError("日程查不到：", err)
	}
	sections := lifedata.SubscriptionSections(data)
	if len(sections) == 0 {
		return "还没有订阅课程。"
	}
	grouped := subscriptionSectionsBySemester(sections)
	lines := []string{"日程订阅："}
	for _, group := range grouped {
		if len(lines) > 1 {
			lines = append(lines, "")
		}
		lines = append(lines, group.semester+"：")
		for _, section := range group.sections {
			lines = append(lines, formatSection(section))
		}
	}
	return strings.Join(lines, "\n")
}

type subscriptionSemesterGroup struct {
	semester string
	sections []map[string]any
}

func subscriptionSectionsBySemester(sections []map[string]any) []subscriptionSemesterGroup {
	groups := make([]subscriptionSemesterGroup, 0, len(sections))
	indexBySemester := make(map[string]int, len(sections))
	for _, section := range sections {
		semester := lifedata.NestedString(section, "semester", "namePrimary", "nameCn", "name")
		if semester == "" {
			semester = "未标注学期"
		}
		index, ok := indexBySemester[semester]
		if !ok {
			index = len(groups)
			indexBySemester[semester] = index
			groups = append(groups, subscriptionSemesterGroup{semester: semester})
		}
		groups[index].sections = append(groups[index].sections, section)
	}
	return groups
}

func (h Handler) bulkSubscribeSections(ctx context.Context, ident store.Identity, raw string) string {
	codes := extractSectionCodes(raw)
	if len(codes) == 0 {
		return "没找到教学班代码。把网页课表里的教学班代码粘过来，例如：\n订阅 导入 CONT5103P.01 CONT6104P.01"
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	matches, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.BulkSubscribeSections(ctx, token, codes)
	})
	if err != nil {
		return commandError("订阅更新失败：", err)
	}
	sections := matchSections(matches)
	added := lifedata.FirstInt(matches, "addedCount")
	already := lifedata.FirstInt(matches, "alreadySubscribedCount")
	return formatBulkSubscriptionResult(matches, sections, nil, added, already)
}

func extractSectionCodes(raw string) []string {
	matches := sectionCodePattern.FindAllString(raw, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		code := strings.ToUpper(match)
		if seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func matchSections(matches map[string]any) []map[string]any {
	return lifedata.MapSlice(matches["sections"])
}

func subscriptionSectionIDInts(data map[string]any) []int {
	sections := lifedata.SubscriptionSections(data)
	ids := make([]int, 0, len(sections))
	for _, section := range sections {
		id := lifedata.FirstInt(section, "id")
		if id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func formatBulkSubscriptionResult(matches map[string]any, sections []map[string]any, fallbackUnmatched []string, added, already int) string {
	unmatched := lifedata.StringSlice(matches["unmatchedCodes"])
	if len(unmatched) == 0 {
		unmatched = fallbackUnmatched
	}
	if len(sections) == 0 {
		lines := []string{"没匹配到教学班。"}
		if len(unmatched) > 0 {
			lines = append(lines, "未匹配：")
			for _, code := range unmatched {
				lines = append(lines, "- "+textutil.MonospaceASCII(code))
			}
		}
		return strings.Join(lines, "\n")
	}
	semester := lifedata.SemesterLabel(matches)
	lines := []string{textutil.MonospaceDigits(fmt.Sprintf("已订阅 %d 个教学班（新增 %d 个，已存在 %d 个）。", len(sections), added, already))}
	if semester != "" {
		lines = append(lines, "学期："+semester)
	}
	lines = append(lines, "", "已匹配：")
	for _, section := range sections {
		lines = append(lines, formatSection(section))
	}
	if len(unmatched) > 0 {
		lines = append(lines, "", "未匹配：")
		for _, code := range unmatched {
			lines = append(lines, "- "+textutil.MonospaceASCII(code))
		}
	}
	return strings.Join(lines, "\n")
}

func (h Handler) curriculum(ctx context.Context, ident store.Identity, args []string) string {
	return h.curriculumAt(ctx, ident, args, chinaNow())
}

func (h Handler) curriculumAt(ctx context.Context, ident store.Identity, args []string, day time.Time) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"课表用法：",
			"课表：查看今明两日",
			"今天课表",
			"明天课表",
			"课表 6.23",
			"下一节课",
		}, "\n")
	}
	target := "two-day"
	if hasArgs(args) {
		target = args[0]
	}
	day = day.In(lifedata.ChinaLocation())
	if target == "two-day" {
		return h.curriculumTwoDays(ctx, ident, day)
	}
	title := "今天课表："
	if target == "tomorrow" {
		day = day.AddDate(0, 0, 1)
		title = "明天课表："
	} else if strings.HasPrefix(target, "date:") {
		parsed, ok := parseScheduleDateToken(target, day)
		if !ok {
			return "日期格式不太对。可以发：课表 6.23"
		}
		day = parsed
		title = textutil.MonospaceDigits(day.Format("01-02")) + " 课表："
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	schedules, _, err := h.schedulesForDay(ctx, ident, token, day)
	if err != nil {
		return commandError("课表查不到：", err)
	}
	if len(schedules) == 0 {
		if target == "tomorrow" {
			return "明天没有课。"
		}
		if strings.HasPrefix(target, "date:") {
			return textutil.MonospaceDigits(day.Format("01-02")) + " 没有课。"
		}
		return "今天没有课。"
	}
	lines := []string{title}
	for i, schedule := range schedules {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(schedules)-i, true))
			break
		}
		lines = append(lines, formatSchedule(schedule))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) curriculumTwoDays(ctx context.Context, ident store.Identity, today time.Time) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	todaySchedules, token, err := h.schedulesForDay(ctx, ident, token, today)
	if err != nil {
		return commandError("课表查不到：", err)
	}
	tomorrowSchedules, _, err := h.schedulesForDay(ctx, ident, token, today.AddDate(0, 0, 1))
	if err != nil {
		return commandError("课表查不到：", err)
	}
	lines := []string{"今明两日课表："}
	lines = append(lines, formatScheduleDay("今天", todaySchedules)...)
	lines = append(lines, "")
	lines = append(lines, formatScheduleDay("明天", tomorrowSchedules)...)
	return strings.Join(lines, "\n")
}

func formatScheduleDay(title string, schedules []map[string]any) []string {
	lines := []string{title + "："}
	if len(schedules) == 0 {
		return append(lines, "没有课。")
	}
	for i, schedule := range schedules {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(schedules)-i, true))
			break
		}
		lines = append(lines, formatSchedule(schedule))
	}
	return lines
}

func (h Handler) nextClass(ctx context.Context, ident store.Identity) string {
	return h.nextClassAt(ctx, ident, chinaNow())
}

func (h Handler) nextClassAt(ctx context.Context, ident store.Identity, now time.Time) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	loc := lifedata.ChinaLocation()
	now = now.In(loc)
	for offset := 0; offset < 8; offset++ {
		day := now.AddDate(0, 0, offset)
		schedules, refreshed, err := h.schedulesForDay(ctx, ident, token, day)
		token = refreshed
		if err != nil {
			return commandError("下一节课查不到：", err)
		}
		for _, schedule := range schedules {
			start := lifedata.ScheduleStartTime(schedule, day, loc)
			if start.IsZero() || start.Before(now) {
				continue
			}
			prefix := "下一节课："
			if offset == 1 {
				prefix = "明天下一节："
			} else if offset > 1 {
				prefix = textutil.MonospaceDigits(day.Format("01-02")) + " 下一节："
			}
			return prefix + "\n" + formatSchedule(schedule)
		}
	}
	return "接下来一周没查到课。"
}

func (h Handler) schedulesForDay(ctx context.Context, ident store.Identity, token string, day time.Time) ([]map[string]any, string, error) {
	all, err := h.subscribedSchedulesForDay(ctx, token, day)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		all, err = h.subscribedSchedulesForDay(ctx, token, day)
	}
	if err == nil {
		all = lifedata.FilterSchedulesForDay(all, day)
		lifedata.SortSchedulesByStart(all)
		return all, token, nil
	}
	if !subscribedSchedulesFallbackError(err) {
		return nil, token, err
	}
	return h.schedulesForDayBySections(ctx, ident, token, day)
}

func (h Handler) subscribedSchedulesForDay(ctx context.Context, token string, day time.Time) ([]map[string]any, error) {
	dateFrom, dateTo := lifedata.DayRFC3339Range(day)
	return h.Life.SubscribedSchedules(ctx, token, life.SubscribedScheduleQuery(dateFrom, dateTo))
}

func subscribedSchedulesFallbackError(err error) bool {
	var httpErr life.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed
	}
	return false
}

func (h Handler) schedulesForDayBySections(ctx context.Context, ident store.Identity, token string, day time.Time) ([]map[string]any, string, error) {
	sub, err := h.Life.CurrentSubscription(ctx, token)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		sub, err = h.Life.CurrentSubscription(ctx, token)
	}
	if err != nil {
		return nil, token, err
	}
	sectionIDs := lifedata.SubscriptionSectionIDsForDay(sub, day)
	if len(sectionIDs) == 0 {
		return nil, token, nil
	}
	all, err := h.fetchSchedulesForSections(ctx, token, sectionIDs, day)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		all, err = h.fetchSchedulesForSections(ctx, token, sectionIDs, day)
	}
	if err != nil {
		return nil, token, err
	}
	all = lifedata.FilterSchedulesForDay(all, day)
	lifedata.SortSchedulesByStart(all)
	return all, token, nil
}

func (h Handler) fetchSchedulesForSections(ctx context.Context, token string, sectionIDs []string, day time.Time) ([]map[string]any, error) {
	dateFrom, dateTo := lifedata.DayRFC3339Range(day)

	var mu sync.Mutex
	var firstErr error
	all := make([]map[string]any, 0, len(sectionIDs))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
scheduleLoop:
	for _, sectionID := range sectionIDs {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break scheduleLoop
		}
		sectionID := sectionID
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			values := life.ScheduleQuery(sectionID, dateFrom, dateTo)
			schedules, err := h.Life.Schedules(ctx, token, values)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			all = append(all, schedules...)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return all, ctx.Err()
}

func formatSchedule(schedule map[string]any) string {
	timeRange := lifedata.ScheduleTimeRange(schedule)
	course := lifedata.ScheduleCourseLabel(schedule)
	place := lifedata.SchedulePlaceLabel(schedule)
	columns := []string{}
	place = strings.TrimSpace(place)
	if place != "" {
		columns = append(columns, textutil.PadRightDisplay(textutil.MonospaceASCII(place), schedulePlaceColumnWidth))
	}
	if timeRange != "" {
		columns = append(columns, textutil.PadRightDisplay(textutil.MonospaceDigits(timeRange), scheduleTimeColumnWidth))
	}
	if course != "" {
		columns = append(columns, course)
	}
	if len(columns) == 0 {
		return textutil.MonospaceDigits(lifedata.ScheduleFallbackLabel(schedule))
	}
	return strings.TrimRight(strings.Join(columns, "\t"), " ")
}

func (h Handler) accessToken(ctx context.Context, ident store.Identity) (string, bool) {
	if h.Auth == nil {
		return "", false
	}
	token, err := h.Auth.AccessToken(ctx, ident)
	if err == nil {
		return token, true
	}
	return "", false
}

func (h Handler) loginRequired() string {
	return "需要先登录。发送：登录"
}

func (h Handler) recordState(ctx context.Context, ident store.Identity, cmd parsedCommand) {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return
	}
	if err := h.Store.RecordConversationState(ctx, ident, cmd.Name, strconv.Quote(cmd.Raw)); err != nil {
		h.logf("record conversation state failed: %v", err)
	}
}

func (h Handler) recordInteraction(ctx context.Context, ident store.Identity, cmd parsedCommand, reply string) {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return
	}
	if err := h.Store.RecordInteraction(ctx, ident, store.Interaction{
		RawText: cmd.Raw,
		Command: cmd.Name,
		Args:    joinedArgs(cmd.Args),
		Handled: true,
		Reply:   reply,
		Status:  store.InteractionStatusHandled,
	}); err != nil {
		h.logf("record command interaction failed: %v", err)
	}
}

func (h Handler) recordFeedback(ctx context.Context, ident store.Identity, feedback store.FeedbackRecord) (int64, error) {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return 0, nil
	}
	return h.Store.RecordFeedback(ctx, ident, feedback)
}

func (h Handler) markFeedbackSent(ctx context.Context, feedbackID int64, sent int) {
	if feedbackID <= 0 || sent <= 0 || h.Store == nil {
		return
	}
	if err := h.Store.MarkFeedbackSent(ctx, feedbackID); err != nil {
		h.logf("mark feedback sent failed: %v", err)
	}
}

func isConfirmationOK(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "ok")
}

func (h Handler) confirmPending(ctx context.Context, input Input) (string, bool) {
	if h.Store == nil || !store.HasConversationIdentity(input.Identity) {
		return "", false
	}
	pending, err := h.Store.ActivePendingConfirmation(ctx, input.Identity)
	if err != nil {
		reply := commandError("确认读取失败：", err)
		if !input.SuppressLog {
			h.recordInteraction(ctx, input.Identity, parsedCommand{Name: "confirm", Raw: strings.TrimSpace(input.Text)}, reply)
		}
		return reply, true
	}
	if pending == nil {
		return "", false
	}
	reply, handled := h.Handle(ctx, Input{
		Text:        pending.Command,
		Identity:    input.Identity,
		SuppressLog: true,
	})
	status := store.PendingConfirmationStatusConfirmed
	if !handled {
		status = store.PendingConfirmationStatusFailed
		reply = "确认命令无法执行：" + pending.Command
	}
	if err := h.Store.MarkPendingConfirmation(ctx, pending.ID, status); err != nil {
		h.logf("mark pending confirmation failed: %v", err)
	}
	if !input.SuppressLog {
		h.recordInteraction(ctx, input.Identity, parsedCommand{
			Name: "confirm",
			Args: []string{pending.Command},
			Raw:  strings.TrimSpace(input.Text),
		}, reply)
	}
	return reply, true
}

func (h Handler) logf(format string, args ...any) {
	if h.Logger != nil {
		h.Logger.Printf(format, args...)
	}
}

func parseIntArg(value string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return v, err == nil && v > 0
}

func parseKeywordSearchArgs(args []string, keywordKey string, filterKeys map[string]bool) (string, map[string]string) {
	values := make(map[string]string, len(filterKeys))
	var keywordParts []string
	for i := 0; i < len(args); i++ {
		key := normToken(args[i])
		if key == keywordKey {
			j := i + 1
			for ; j < len(args); j++ {
				if filterKeys[normToken(args[j])] {
					break
				}
			}
			keywordParts = append(keywordParts, args[i+1:j]...)
			i = j - 1
			continue
		}
		if filterKeys[key] {
			if i+1 < len(args) {
				values[key] = args[i+1]
				i++
			}
			continue
		}
		keywordParts = append(keywordParts, args[i])
	}
	return joinedArgs(keywordParts), values
}

func parseSearchCoursesArgs(args []string) life.SearchCoursesOptions {
	filterKeys := map[string]bool{
		"education_level_id": true,
		"category_id":        true,
		"class_type_id":      true,
		"limit":              true,
	}
	keyword, values := parseKeywordSearchArgs(args, "keyword", filterKeys)
	opts := life.SearchCoursesOptions{Keyword: keyword}
	if v, ok := parseIntArg(values["education_level_id"]); ok {
		opts.EducationLevelID = v
	}
	if v, ok := parseIntArg(values["category_id"]); ok {
		opts.CategoryID = v
	}
	if v, ok := parseIntArg(values["class_type_id"]); ok {
		opts.ClassTypeID = v
	}
	if v, ok := parseIntArg(values["limit"]); ok {
		opts.Limit = int(v)
	}
	return opts
}

func parseSearchSectionsArgs(args []string) life.SearchSectionsOptions {
	filterKeys := map[string]bool{
		"course_id":      true,
		"course_jw_id":   true,
		"semester_id":    true,
		"semester_jw_id": true,
		"campus_id":      true,
		"department_id":  true,
		"teacher_id":     true,
		"teacher_code":   true,
		"limit":          true,
	}
	keyword, values := parseKeywordSearchArgs(args, "keyword", filterKeys)
	opts := life.SearchSectionsOptions{Keyword: keyword}
	if v, ok := parseIntArg(values["course_id"]); ok {
		opts.CourseID = v
	}
	if v, ok := parseIntArg(values["course_jw_id"]); ok {
		opts.CourseJwID = v
	}
	if v, ok := parseIntArg(values["semester_id"]); ok {
		opts.SemesterID = v
	}
	if v, ok := parseIntArg(values["semester_jw_id"]); ok {
		opts.SemesterJwID = v
	}
	if v, ok := parseIntArg(values["campus_id"]); ok {
		opts.CampusID = v
	}
	if v, ok := parseIntArg(values["department_id"]); ok {
		opts.DepartmentID = v
	}
	if v, ok := parseIntArg(values["teacher_id"]); ok {
		opts.TeacherID = v
	}
	if code := strings.TrimSpace(values["teacher_code"]); code != "" {
		opts.TeacherCode = code
	}
	if v, ok := parseIntArg(values["limit"]); ok {
		opts.Limit = int(v)
	}
	return opts
}

func parseSearchTeachersArgs(args []string) life.SearchTeachersOptions {
	filterKeys := map[string]bool{
		"department_id": true,
		"limit":         true,
	}
	keyword, values := parseKeywordSearchArgs(args, "keyword", filterKeys)
	opts := life.SearchTeachersOptions{Keyword: keyword}
	if v, ok := parseIntArg(values["department_id"]); ok {
		opts.DepartmentID = v
	}
	if v, ok := parseIntArg(values["limit"]); ok {
		opts.Limit = int(v)
	}
	return opts
}

func (h Handler) listSemesters(ctx context.Context, args []string) string {
	page, limit := 1, 20
	if v, ok := parseIntArg(joinedArgs(args)); ok {
		limit = int(v)
	}
	semesters, err := h.Life.ListSemesters(ctx, page, limit)
	if err != nil {
		return commandError("学期查不到：", err)
	}
	if len(semesters) == 0 {
		return "没有学期数据。"
	}
	lines := []string{"学期："}
	for _, semester := range semesters {
		name := lifedata.FirstString(semester, "namePrimary", "nameCn", "name")
		if name == "" {
			name = lifedata.FirstString(semester, "id")
		}
		start := lifedata.FormatAPITime(lifedata.FirstString(semester, "startDate"))
		end := lifedata.FormatAPITime(lifedata.FirstString(semester, "endDate"))
		label := name
		if start != "" || end != "" {
			label += "（" + strings.TrimSpace(start+" ~ "+end) + "）"
		}
		lines = append(lines, "- "+label)
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchCoursesWithFilters(ctx context.Context, args []string) string {
	opts := parseSearchCoursesArgs(args)
	if opts.Keyword == "" && opts.EducationLevelID == 0 && opts.CategoryID == 0 && opts.ClassTypeID == 0 {
		return "请输入搜索条件，例如：课程搜索 数学分析"
	}
	courses, err := h.Life.SearchCoursesWithFilters(ctx, opts)
	if err != nil {
		return commandError("课程查不到：", err)
	}
	if len(courses) == 0 {
		return "没找到课程。"
	}
	lines := []string{"课程："}
	for _, course := range courses {
		lines = append(lines, formatCourse(course))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchSectionsWithFilters(ctx context.Context, args []string) string {
	opts := parseSearchSectionsArgs(args)
	if opts.Keyword == "" && opts.CourseID == 0 && opts.CourseJwID == 0 && opts.SemesterID == 0 &&
		opts.SemesterJwID == 0 && opts.CampusID == 0 && opts.DepartmentID == 0 &&
		opts.TeacherID == 0 && opts.TeacherCode == "" {
		return "请输入搜索条件，例如：教学班搜索 高等数学"
	}
	sections, err := h.Life.SearchSectionsWithFilters(ctx, opts)
	if err != nil {
		return commandError("教学班查不到：", err)
	}
	if len(sections) == 0 {
		return "没找到教学班。"
	}
	lines := []string{"教学班："}
	for _, section := range sections {
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchTeachersWithFilters(ctx context.Context, args []string) string {
	opts := parseSearchTeachersArgs(args)
	if opts.Keyword == "" && opts.DepartmentID == 0 {
		return "请输入搜索条件，例如：老师搜索 张"
	}
	teachers, err := h.Life.SearchTeachersWithFilters(ctx, opts)
	if err != nil {
		return commandError("老师查不到：", err)
	}
	if len(teachers) == 0 {
		return "没找到老师。"
	}
	lines := []string{"老师："}
	for _, teacher := range teachers {
		lines = append(lines, formatTeacher(teacher))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) getCourseByJwID(ctx context.Context, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return "需要提供课程 JW ID。"
	}
	course, err := h.Life.GetCourseByJwID(ctx, jwId)
	if err != nil {
		return commandError("课程查不到：", err)
	}
	return "课程：\n" + formatCourse(course)
}

func (h Handler) getSectionByJwID(ctx context.Context, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return "需要提供教学班 JW ID。"
	}
	section, err := h.Life.GetSectionByJwID(ctx, jwId)
	if err != nil {
		return commandError("教学班查不到：", err)
	}
	return "教学班：\n" + formatSection(section)
}

func (h Handler) getTeacherByID(ctx context.Context, raw string) string {
	id, ok := parseIntArg(raw)
	if !ok {
		return "需要提供老师 ID。"
	}
	teacher, err := h.Life.GetTeacherByID(ctx, id)
	if err != nil {
		return commandError("老师查不到：", err)
	}
	return "老师：\n" + formatTeacher(teacher)
}

type busRouteQuery struct {
	From string
	To   string
}

func parseBusRouteArgs(args []string) busRouteQuery {
	var out busRouteQuery
	for i := 0; i < len(args); i++ {
		switch normToken(args[i]) {
		case "from", "从":
			if i+1 < len(args) {
				out.From = campusName(args[i+1])
				i++
			}
		case "to", "到":
			if i+1 < len(args) {
				out.To = campusName(args[i+1])
				i++
			}
		default:
			if campus := campusName(args[i]); campus != "" {
				if out.From == "" {
					out.From = campus
				} else if out.To == "" {
					out.To = campus
				}
			}
		}
	}
	return out
}

func (h Handler) busRoutes(ctx context.Context, args []string) string {
	opts := parseBusRouteArgs(args)
	var originID, destID int64
	if opts.From != "" || opts.To != "" {
		data, err := h.Life.Bus(ctx)
		if err != nil {
			return commandError("校车路线查不到：", err)
		}
		if opts.From != "" {
			id, ok := campusIDByName(data, opts.From)
			if !ok {
				return "没找到出发校区：" + opts.From
			}
			originID = int64(id)
		}
		if opts.To != "" {
			id, ok := campusIDByName(data, opts.To)
			if !ok {
				return "没找到到达校区：" + opts.To
			}
			destID = int64(id)
		}
	}
	routes, err := h.Life.ListBusRoutes(ctx, originID, destID)
	if err != nil {
		return commandError("校车路线查不到：", err)
	}
	return formatBusRoutes(routes)
}

func formatBusRoutes(data map[string]any) string {
	routes := lifedata.MapSlice(data["routes"])
	if len(routes) == 0 {
		return "没查到校车路线。"
	}
	lines := []string{"校车路线："}
	for _, route := range routes {
		name := lifedata.FirstString(route, "nameCn", "namePrimary", "name")
		origin := lifedata.NestedString(route, "originCampus", "namePrimary", "nameCn", "name")
		dest := lifedata.NestedString(route, "destinationCampus", "namePrimary", "nameCn", "name")
		stops := make([]string, 0)
		for _, stop := range lifedata.MapSlice(route["stops"]) {
			campus := lifedata.NestedString(stop, "campus", "namePrimary", "nameCn", "name")
			if campus != "" {
				stops = append(stops, campus)
			}
		}
		routeLabel := textutil.JoinNonEmpty(" → ", origin, dest)
		line := "- " + name
		if routeLabel != "" && routeLabel != name {
			line += "（" + routeLabel + "）"
		}
		if len(stops) > 0 {
			line += " 经停 " + strings.Join(stops, "、")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (h Handler) unsubscribeSectionByJwID(ctx context.Context, ident store.Identity, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return "需要提供教学班 JW ID。"
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.UnsubscribeSectionByJwID(ctx, token, jwId)
	})
	if err != nil {
		return commandError("退订失败：", err)
	}
	return "已退订教学班。"
}

func (h Handler) mySubscribedSections(ctx context.Context, ident store.Identity) string {
	return h.subscriptionList(ctx, ident)
}

func (h Handler) sectionSchedules(ctx context.Context, ident store.Identity, args []string) string {
	if len(args) < 3 {
		return "用法：教学班课表 <JW ID> <开始日期> <结束日期>"
	}
	jwId, ok := parseIntArg(args[0])
	if !ok {
		return "JW ID 无效。"
	}
	dateFrom, dateTo := args[1], args[2]
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	schedules, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.ListSchedulesBySection(ctx, token, jwId, dateFrom, dateTo)
	})
	if err != nil {
		return commandError("课表查不到：", err)
	}
	if len(schedules) == 0 {
		return "该时间段没有课。"
	}
	lifedata.SortSchedulesByStart(schedules)
	lines := []string{"教学班课表："}
	for _, schedule := range schedules {
		lines = append(lines, formatSchedule(schedule))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) sectionExams(ctx context.Context, ident store.Identity, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return "需要提供教学班 JW ID。"
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	section, err := h.Life.GetSectionByJwID(ctx, jwId)
	if err != nil {
		return commandError("教学班查不到：", err)
	}
	exams, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.ListExamsBySection(ctx, token, jwId)
	})
	if err != nil {
		return commandError("考试查不到：", err)
	}
	if len(exams) == 0 {
		return "该教学班没有考试。"
	}
	wrapped := make([]subscriptionExam, len(exams))
	for i, exam := range exams {
		wrapped[i] = subscriptionExam{exam: exam, section: section}
	}
	sortSubscriptionExams(wrapped)
	lines := []string{"考试："}
	for i, item := range wrapped {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(wrapped)-i, true))
			break
		}
		lines = append(lines, formatNumberedLine(i+1, formatExam(item)))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) sectionHomeworks(ctx context.Context, ident store.Identity, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return "需要提供教学班 JW ID。"
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	homeworks, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.ListHomeworksBySection(ctx, token, jwId)
	})
	if err != nil {
		return commandError("作业查不到：", err)
	}
	lifedata.SortHomeworksByDue(homeworks)
	if len(homeworks) == 0 {
		return "该教学班没有作业。"
	}
	lines := []string{"作业："}
	for i, homework := range homeworks {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(homeworks)-i, true))
			break
		}
		lines = append(lines, formatNumberedLine(i+1, formatHomework(homework)))
	}
	return strings.Join(lines, "\n")
}

func dashboardItemSlice(data map[string]any, key string) []map[string]any {
	container, _ := data[key].(map[string]any)
	return lifedata.MapSlice(container["items"])
}

func (h Handler) myDashboard(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.GetMyDashboard(ctx, token)
	})
	if err != nil {
		return commandError("概览查不到：", err)
	}
	return formatDashboard(data, "我的概览")
}

func (h Handler) upcomingDeadlines(ctx context.Context, ident store.Identity, args []string) string {
	dayLimit := 7
	if len(args) > 0 {
		if v, ok := parseIntArg(args[0]); ok {
			dayLimit = int(v)
		}
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.GetUpcomingDeadlines(ctx, token, dayLimit)
	})
	if err != nil {
		return commandError("近期截止查不到：", err)
	}
	return formatDashboard(data, fmt.Sprintf("未来 %d 天截止", dayLimit))
}

func formatDashboard(data map[string]any, title string) string {
	counts, _ := data["counts"].(map[string]any)
	lines := []string{title + "："}
	if counts != nil {
		parts := []string{}
		if n := lifedata.FirstInt(counts, "todaySchedules"); n > 0 {
			parts = append(parts, fmt.Sprintf("今日课表 %d", n))
		}
		if n := lifedata.FirstInt(counts, "pendingHomeworks"); n > 0 {
			parts = append(parts, fmt.Sprintf("待交作业 %d", n))
		}
		if n := lifedata.FirstInt(counts, "dueSoonHomeworks"); n > 0 {
			parts = append(parts, fmt.Sprintf("近期作业 %d", n))
		}
		if n := lifedata.FirstInt(counts, "upcomingExams"); n > 0 {
			parts = append(parts, fmt.Sprintf("考试 %d", n))
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	dueTodos := dashboardItemSlice(data, "dueTodos")
	if len(dueTodos) > 0 {
		lines = append(lines, "", "待办：")
		for i, todo := range dueTodos {
			if i >= listDisplayLimit {
				lines = append(lines, moreLine(len(dueTodos)-i, true))
				break
			}
			lines = append(lines, formatNumberedLine(i+1, formatTodo(todo)))
		}
	}
	homeworks := dashboardItemSlice(data, "homeworks")
	if len(homeworks) > 0 {
		lines = append(lines, "", "作业：")
		for i, homework := range homeworks {
			if i >= listDisplayLimit {
				lines = append(lines, moreLine(len(homeworks)-i, true))
				break
			}
			lines = append(lines, formatNumberedLine(i+1, formatHomework(homework)))
		}
	}
	exams := dashboardItemSlice(data, "exams")
	if len(exams) > 0 {
		lines = append(lines, "", "考试：")
		for i, exam := range exams {
			if i >= listDisplayLimit {
				lines = append(lines, moreLine(len(exams)-i, true))
				break
			}
			sectionMap, _ := exam["section"].(map[string]any)
			lines = append(lines, formatNumberedLine(i+1, formatExam(subscriptionExam{exam: exam, section: sectionMap})))
		}
	}
	if len(lines) == 1 {
		return title + "\n暂无近期截止。"
	}
	return strings.Join(lines, "\n")
}

func (h Handler) currentSemester(ctx context.Context) string {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return commandError("学期查不到：", err)
	}
	name := lifedata.FirstString(semester, "name", "nameCn", "namePrimary")
	if name == "" {
		name = lifedata.FirstString(semester, "id")
	}
	if name == "" {
		return "当前学期未知。"
	}
	return "当前学期：" + name
}

func (h Handler) searchCourses(ctx context.Context, keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "想查哪门课？例如：课程 数学分析"
	}
	courses, err := h.Life.SearchCourses(ctx, keyword, 5)
	if err != nil {
		return commandError("课程查不到：", err)
	}
	if len(courses) == 0 {
		return "没找到课程。"
	}
	lines := []string{"课程："}
	for _, course := range courses {
		lines = append(lines, formatCourse(course))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchSections(ctx context.Context, keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "想查哪个教学班？例如：教学班 高等数学"
	}
	sections, err := h.Life.SearchSections(ctx, keyword, 5)
	if err != nil {
		return commandError("教学班查不到：", err)
	}
	if len(sections) == 0 {
		return "没找到教学班。"
	}
	lines := []string{"教学班："}
	for _, section := range sections {
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchTeachers(ctx context.Context, keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "想查哪位老师？例如：老师 张"
	}
	teachers, err := h.Life.SearchTeachers(ctx, keyword, 5)
	if err != nil {
		return commandError("老师查不到：", err)
	}
	if len(teachers) == 0 {
		return "没找到老师。"
	}
	lines := []string{"老师："}
	for _, teacher := range teachers {
		lines = append(lines, formatTeacher(teacher))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) exams(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return commandError("考试查不到：", err)
	}
	exams := subscriptionExams(data)
	if len(exams) == 0 {
		return "没有订阅课程考试。"
	}
	sortSubscriptionExams(exams)
	lines := []string{"考试："}
	for i, exam := range exams {
		if i >= listDisplayLimit {
			lines = append(lines, moreLine(len(exams)-i, true))
			break
		}
		lines = append(lines, formatNumberedLine(i+1, formatExam(exam)))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) status(ctx context.Context, ident store.Identity) string {
	api := "OK"
	if err := h.Life.Health(ctx); err != nil {
		api = friendlyError(err)
	}
	login := "未登录"
	if h.Auth != nil {
		if _, err := h.Auth.AccessToken(ctx, ident); err == nil {
			login = "已登录"
		}
	}
	return strings.Join([]string{
		"状态：",
		"Life API：" + api,
		"登录：" + login,
	}, "\n")
}

func formatNumberedLine(index int, text string) string {
	prefix := textutil.PadRightDisplay(textutil.MonospaceDigits(fmt.Sprintf("%d.", index)), numberedColumnWidth)
	if text == "" {
		return prefix
	}
	return prefix + "\t" + textutil.MonospaceDigits(text)
}

func formatCourse(course map[string]any) string {
	code := textutil.MonospaceASCII(lifedata.FirstString(course, "code"))
	name := lifedata.FirstString(course, "namePrimary", "nameCn", "name")
	return formatCodeLabelLine(code, name)
}

func formatSection(section map[string]any) string {
	code := textutil.MonospaceASCII(lifedata.FirstString(section, "code"))
	course := lifedata.NestedString(section, "course", "namePrimary", "nameCn", "name")
	semester := lifedata.NestedString(section, "semester", "name")
	return formatCodeLabelLine(code, textutil.JoinNonEmpty(" ", course, semester))
}

func formatTeacher(teacher map[string]any) string {
	code := textutil.MonospaceASCII(lifedata.FirstString(teacher, "code", "teacherId", "id"))
	name := lifedata.FirstString(teacher, "namePrimary", "nameCn", "name")
	department := lifedata.NestedString(teacher, "department", "namePrimary", "nameCn", "name")
	title := lifedata.NestedString(teacher, "teacherTitle", "namePrimary", "nameCn", "name")
	return formatCodeLabelLine(code, textutil.JoinNonEmpty(" ", name, department, title))
}

type subscriptionExam struct {
	exam    map[string]any
	section map[string]any
}

func subscriptionExams(data map[string]any) []subscriptionExam {
	sections := lifedata.SubscriptionSections(data)
	out := make([]subscriptionExam, 0, len(sections))
	for _, section := range sections {
		for _, exam := range lifedata.MapSlice(section["exams"]) {
			out = append(out, subscriptionExam{exam: exam, section: section})
		}
	}
	return out
}

func sortSubscriptionExams(exams []subscriptionExam) {
	sort.SliceStable(exams, func(i, j int) bool {
		left, leftOK := lifedata.ParseAPITime(lifedata.FirstString(exams[i].exam, "examDate", "date"))
		right, rightOK := lifedata.ParseAPITime(lifedata.FirstString(exams[j].exam, "examDate", "date"))
		switch {
		case leftOK && rightOK && !left.Equal(right):
			return left.Before(right)
		case leftOK != rightOK:
			return leftOK
		}
		return examTimeKey(exams[i].exam) < examTimeKey(exams[j].exam)
	})
}

func upcomingSubscriptionExams(exams []subscriptionExam, now time.Time) []subscriptionExam {
	out := make([]subscriptionExam, 0, len(exams))
	loc := lifedata.ChinaLocation()
	today := now.In(loc).Format("2006-01-02")
	for _, exam := range exams {
		date, ok := lifedata.ParseAPITime(lifedata.FirstString(exam.exam, "examDate", "date"))
		if !ok || date.In(loc).Format("2006-01-02") >= today {
			out = append(out, exam)
		}
	}
	sortSubscriptionExams(out)
	return out
}

func formatExam(item subscriptionExam) string {
	date := formatExamDate(item.exam)
	timeRange := formatExamTimeRange(item.exam)
	course := lifedata.NestedString(item.section, "course", "namePrimary", "nameCn", "name", "code")
	sectionCode := lifedata.FirstString(item.section, "code")
	mode := lifedata.FirstString(item.exam, "examMode")
	rooms := formatExamRooms(item.exam)
	parts := textutil.NonEmpty(date, timeRange, course, sectionCode, mode, rooms)
	if len(parts) == 0 {
		return lifedata.FirstString(item.exam, "id")
	}
	return textutil.MonospaceASCII(textutil.MonospaceDigits(strings.Join(parts, " · ")))
}

func formatExamDate(exam map[string]any) string {
	value := lifedata.FirstString(exam, "examDate", "date")
	if value == "" {
		return "日期待定"
	}
	parsed, ok := lifedata.ParseAPITime(value)
	if !ok {
		return value
	}
	return parsed.In(lifedata.ChinaLocation()).Format("01-02")
}

func formatExamTimeRange(exam map[string]any) string {
	start := examClock(lifedata.FirstString(exam, "startTime"))
	end := examClock(lifedata.FirstString(exam, "endTime"))
	switch {
	case start != "" && end != "":
		return start + "-" + end
	case start != "":
		return start
	case end != "":
		return end
	default:
		return ""
	}
}

func examClock(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, ":") {
		return value
	}
	n, err := strconv.Atoi(textutil.PlainDigits(value))
	if err != nil || n < 0 {
		return value
	}
	return fmt.Sprintf("%02d:%02d", n/100, n%100)
}

func examTimeKey(exam map[string]any) string {
	return examClock(lifedata.FirstString(exam, "startTime"))
}

func formatExamRooms(exam map[string]any) string {
	rooms := lifedata.MapSlice(exam["examRooms"])
	labels := make([]string, 0, len(rooms))
	for _, room := range rooms {
		label := lifedata.FirstString(room, "room", "namePrimary", "nameCn", "name")
		if label != "" {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, "、")
}

func formatCodeLabelLine(code, label string) string {
	if code == "" {
		return "- " + label
	}
	if label == "" {
		return "- " + code
	}
	return "- " + paddedCourseCode(code) + "\t" + label
}

func paddedCourseCode(code string) string {
	return textutil.PadRightDisplay(code, courseCodeColumnWidth)
}

const courseCodeColumnWidth = 14
const numberedColumnWidth = 3
const listDisplayLimit = 8
const schedulePlaceColumnWidth = 8
const scheduleTimeColumnWidth = 11

func moreLine(count int, monospace bool) string {
	line := fmt.Sprintf("...and %d more", count)
	if monospace {
		return textutil.MonospaceDigits(line)
	}
	return line
}

func chinaNow() time.Time {
	return time.Now().In(lifedata.ChinaLocation())
}

func friendlyError(err error) string {
	text := err.Error()
	lower := textutil.LowerTrim(text)
	if strings.Contains(lower, "unauthorized_client") {
		return "登录方式未被授权，请联系管理员。"
	}
	if life.IsUnauthorized(err) || strings.Contains(lower, "unauthorized") {
		return "登录已过期。发送：登录"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "timeout") {
		return "网络超时，等会儿再试"
	}
	return text
}

func commandError(prefix string, err error) string {
	return prefix + friendlyError(err)
}
