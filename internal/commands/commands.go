package commands

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Handler struct {
	Life   *life.Client
	Auth   *auth.Manager
	Store  *store.Store
	Prefix string
	Logger *log.Logger
}

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
	cmd, ok := h.parse(input.Text)
	if !ok && store.IsGroupConversation(input.Identity) {
		cmd, ok = parseGroupBus(input.Text)
	}
	if !ok {
		return "", false
	}
	if store.IsGroupConversation(input.Identity) && cmd.Name != "bus" {
		return "", false
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
		AgentTools: []AgentToolSpec{{
			Name:        "list_homeworks",
			Description: "List the user's homework grouped by overdue, nearby, and future.",
			CommandText: "作业",
		}},
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.homework(ctx, ident, args)
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
		Aliases:    []string{"notify", "notice", "push", "提醒", "通知", "推送"},
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
		Name:      "bus",
		Aliases:   []string{"bus", "xc", "校车", "车"},
		NeedsLife: true,
		Run: func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.bus(ctx, args)
		},
	},
	{
		Name:      "schedule",
		Aliases:   []string{"schedule", "sched", "rc", "kb", "日程", "课表", "课标"},
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
		if name, args, ok := normalizeJoinedCommand(joined, nil); ok {
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
	key := normToken(name)
	if isHelpToken(key) {
		return "help", args
	}
	if normalized, normalizedArgs, ok := normalizeJoinedCommand(name, args); ok {
		return normalized, normalizedArgs
	}
	for _, spec := range commandSpecs {
		for _, alias := range spec.Aliases {
			if key != normToken(alias) {
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
	key := normToken(name)
	if day, ok := joinedScheduleDay(key); ok {
		return "schedule", []string{day}, true
	}
	switch key {
	case "下一节课":
		return "nextclass", nil, true
	}
	return "", args, false
}

func joinedScheduleDay(key string) (string, bool) {
	for _, scheduleToken := range []string{"schedule", "sched", "rc", "kb", "日程", "课表", "课标"} {
		if strings.HasPrefix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[len(scheduleToken):]); ok {
				return day, true
			}
		}
		if strings.HasSuffix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[:len(key)-len(scheduleToken)]); ok {
				return day, true
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
	return args
}

func normalizeNotifyArgs(args []string) []string {
	out := copyArgs(args)
	for i, arg := range out {
		if isHelpToken(arg) {
			out[i] = "help"
			continue
		}
		switch normToken(arg) {
		case "class", "classes", "section", "sections", "schedule", "curriculum", "kb", "课表", "课程", "上课":
			out[i] = "classes"
		case "homework", "hw", "作业":
			out[i] = "homework"
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

func normToken(value string) string {
	return textutil.LowerTrim(value)
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

func parseGroupBus(text string) (parsedCommand, bool) {
	raw := stripCQCodes(text)
	if raw == "" || !containsBusKeyword(raw) {
		return parsedCommand{}, false
	}
	return commandResult(raw, "bus", busArgsFromText(raw)), true
}

func containsBusKeyword(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "校车") || strings.Contains(lower, "班车") {
		return true
	}
	return textutil.IndexASCIIToken(lower, "xc") >= 0 || textutil.IndexASCIIToken(lower, "bus") >= 0
}

var cqCodeRE = regexp.MustCompile(`(?i)\[CQ:[^\]]+\]`)

func stripCQCodes(text string) string {
	return strings.TrimSpace(cqCodeRE.ReplaceAllString(text, " "))
}

func busArgsFromText(text string) []string {
	type match struct {
		index  int
		campus string
	}
	matches := make([]match, 0, 2)
	seen := map[string]bool{}
	lookupText := strings.ToLower(text)
	for _, alias := range campusAliases() {
		if index := campusAliasIndex(lookupText, alias); index >= 0 {
			campus := campusName(alias)
			if campus != "" && !seen[campus] {
				matches = append(matches, match{index: index, campus: campus})
				seen[campus] = true
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].index < matches[j].index
	})
	args := make([]string, 0, 2)
	for _, match := range matches {
		if len(args) >= 2 {
			break
		}
		args = append(args, match.campus)
	}
	if len(args) == 1 && hasDestinationMarkerBefore(lookupText, matches[0].index) {
		return []string{"到", args[0]}
	}
	return args
}

func hasDestinationMarkerBefore(text string, index int) bool {
	if index <= 0 || index > len(text) {
		return false
	}
	before := strings.TrimSpace(text[:index])
	if strings.HasSuffix(before, "从") || textutil.IndexASCIIToken(before, "from") >= 0 {
		return false
	}
	if strings.HasSuffix(before, "到") {
		return true
	}
	fields := strings.Fields(before)
	return len(fields) > 0 && fields[len(fields)-1] == "to"
}

func campusAliasIndex(text, alias string) int {
	alias = strings.ToLower(alias)
	for i := 0; i < len(alias); i++ {
		if alias[i] > 127 {
			if utf8.RuneCountInString(alias) == 1 {
				return singleChineseCampusAliasIndex(text, alias)
			}
			return strings.Index(text, alias)
		}
	}
	return textutil.IndexASCIIToken(text, alias)
}

func singleChineseCampusAliasIndex(text, alias string) int {
	index := strings.Index(text, alias)
	for index >= 0 {
		if singleChineseCampusAliasAt(text, index, len(alias)) {
			return index
		}
		next := strings.Index(text[index+len(alias):], alias)
		if next < 0 {
			return -1
		}
		index += len(alias) + next
	}
	return -1
}

func singleChineseCampusAliasAt(text string, index, length int) bool {
	before := text[:index]
	after := text[index+length:]
	if strings.HasSuffix(before, "从") || strings.HasSuffix(before, "到") || strings.HasSuffix(before, "去") || strings.HasSuffix(before, "往") {
		return true
	}
	if strings.HasPrefix(after, "到") || strings.HasPrefix(after, "去") || strings.HasPrefix(after, "往") {
		return true
	}
	return chineseCampusAliasBoundaryBefore(before) && chineseCampusAliasBoundaryAfter(after)
}

func chineseCampusAliasBoundaryBefore(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text)
	return isChineseCampusAliasBoundaryRune(r)
}

func chineseCampusAliasBoundaryAfter(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text)
	return isChineseCampusAliasBoundaryRune(r)
}

func isChineseCampusAliasBoundaryRune(r rune) bool {
	return !unicode.Is(unicode.Han, r) && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func (h Handler) help() string {
	return strings.Join([]string{
		"可以直接发：",
		"待办 / td",
		"td 写报告",
		"td done 1",
		"作业 / hw",
		"作业 done 1",
		"校车 / xc",
		"xc 东区 西区",
		"今天课表 / 明天课表",
		"下一节课",
		"订阅",
		"通知",
		"状态 / status",
		"我 / me",
		"课程 数学分析",
		"教学班 高等数学",
		"登录 / 登录 状态",
	}, "\n")
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
		}, "\n")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if firstArgIs(args, "add") {
		title := joinedArgs(args[1:])
		if title == "" {
			return "想加什么？例如：待办 add 写报告"
		}
		return h.createTodo(ctx, ident, token, title)
	}
	if firstArgIs(args, "done") {
		target := joinedArgs(args[1:])
		if target == "" {
			return "想完成哪条？例如：td done 1"
		}
		todos, err := h.pendingTodos(ctx, ident, token)
		if err != nil {
			return commandError("待办查不到：", err)
		}
		todo, ok := resolveTodo(todos, target)
		if !ok {
			return "没找到这条待办。发 td 看编号，再试：td done 1"
		}
		id := lifedata.FirstString(todo, "id")
		if id == "" {
			return "这条待办没有可用 ID，暂时完成不了。"
		}
		err = auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.CompleteTodo(ctx, token, id)
		})
		if err != nil {
			return commandError("待办完成失败：", err)
		}
		title := lifedata.FirstString(todo, "title")
		if title == "" {
			return "已完成。"
		}
		return "已完成：" + title
	}
	if hasArgs(args) {
		return h.createTodo(ctx, ident, token, joinedArgs(args))
	}
	todos, err := h.pendingTodos(ctx, ident, token)
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

func (h Handler) createTodo(ctx context.Context, ident store.Identity, token, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "想加什么？例如：td 写报告"
	}
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CreateTodo(ctx, token, title)
	})
	if err != nil {
		return commandError("待办添加失败：", err)
	}
	return "已加待办：" + title
}

func (h Handler) pendingTodos(ctx context.Context, ident store.Identity, token string) ([]map[string]any, error) {
	todos, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.Todos(ctx, token, "false")
	})
	return todos, err
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

func (h Handler) homework(ctx context.Context, ident store.Identity, args []string) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"作业用法：",
			"作业",
			"作业 pending",
			"作业 done 1",
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
		homeworks, err := h.homeworks(ctx, ident, token)
		if err != nil {
			return commandError("作业查不到：", err)
		}
		homeworks = filterHomeworks(homeworks, true)
		homework, ok := resolveHomework(homeworks, target)
		if !ok {
			return "没找到这条作业。发 作业 看编号，再试：作业 done 1"
		}
		id := lifedata.FirstString(homework, "id")
		if id == "" {
			return "这条作业没有可用 ID，暂时改不了。"
		}
		completed := args[0] == "done"
		err = auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.SetHomeworkCompletion(ctx, token, id, completed)
		})
		if err != nil {
			return commandError("作业状态更新失败：", err)
		}
		title := lifedata.FirstString(homework, "title")
		if completed {
			return "已完成作业：" + title
		}
		return "已取消完成：" + title
	}
	pendingOnly := true
	if firstArgIs(args, "all") {
		pendingOnly = false
	}
	homeworks, err := h.homeworks(ctx, ident, token)
	if err != nil {
		return commandError("作业查不到：", err)
	}
	homeworks = filterHomeworks(homeworks, pendingOnly)
	if len(homeworks) == 0 {
		if pendingOnly {
			return "没有未完成作业。"
		}
		return "没有作业。"
	}
	return formatHomeworkList(homeworks)
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
	course := lifedata.HomeworkCourseLabel(homework)
	title := lifedata.FirstString(homework, "title")
	due := lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt"))
	parts := []string{}
	if due != "" {
		parts = append(parts, "截止 "+due)
	}
	if course != "" {
		parts = append(parts, course)
	}
	if title != "" {
		parts = append(parts, title)
	}
	if len(parts) == 0 {
		return textutil.MonospaceDigits(lifedata.FirstString(homework, "id"))
	}
	return textutil.MonospaceDigits(strings.Join(parts, " · "))
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
	current, err := h.Life.CurrentSubscription(ctx, token)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		current, err = h.Life.CurrentSubscription(ctx, token)
	}
	if err != nil {
		return commandError("日程订阅查不到：", err)
	}
	matches, err := h.Life.MatchSectionCodes(ctx, token, codes, "")
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		matches, err = h.Life.MatchSectionCodes(ctx, token, codes, "")
	}
	if err != nil {
		return commandError("教学班匹配失败：", err)
	}
	sections := matchSections(matches)
	if len(sections) == 0 {
		return formatBulkSubscriptionResult(matches, sections, codes, 0, 0)
	}
	existing := subscriptionSectionIDInts(current)
	existingSet := make(map[int]bool, len(existing))
	for _, id := range existing {
		existingSet[id] = true
	}
	union := append([]int(nil), existing...)
	added := 0
	already := 0
	for _, section := range sections {
		id := lifedata.FirstInt(section, "id")
		if id == 0 {
			continue
		}
		if existingSet[id] {
			already++
			continue
		}
		existingSet[id] = true
		union = append(union, id)
		added++
	}
	sort.Ints(union)
	_, err = h.Life.ReplaceCalendarSubscription(ctx, token, union)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		_, err = h.Life.ReplaceCalendarSubscription(ctx, token, union)
	}
	if err != nil {
		return commandError("订阅更新失败：", err)
	}
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
		if id != 0 {
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

func (h Handler) logf(format string, args ...any) {
	if h.Logger != nil {
		h.Logger.Printf(format, args...)
	}
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

func (h Handler) bus(ctx context.Context, args []string) string {
	return h.busAt(ctx, args, time.Now())
}

func (h Handler) busAt(ctx context.Context, args []string, now time.Time) string {
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return commandError("校车查不到：", err)
	}
	items := nextBusByRoute(data, args, now)
	if len(items) == 0 {
		return "今天后面没查到校车。"
	}
	return strings.Join(formatBusItemsByDepartureCampus(items, 0), "\n")
}

type busItem struct {
	RouteID          string
	DepartureCampus  string
	ArrivalCampus    string
	Stops            []busStop
	DepartureMinutes int
	DepartureTime    string
	Arrival          string
	Route            string
}

type busStop struct {
	Name string
	Time string
}

func nextBusItems(data map[string]any, args []string, now time.Time) []busItem {
	now = now.In(lifedata.ChinaLocation())
	from, to := busFilter(args)
	dayType := "weekday"
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		dayType = "weekend"
	}
	nowMinutes := now.Hour()*60 + now.Minute()
	routes := busRouteMap(data["routes"])
	trips := lifedata.MapSlice(data["trips"])
	items := make([]busItem, 0, len(trips))
	for _, trip := range trips {
		if lifedata.FirstString(trip, "dayType") != dayType {
			continue
		}
		departure, ok := lifedata.IntValue(trip["departureMinutes"])
		if !ok {
			continue
		}
		if departure < nowMinutes {
			continue
		}
		route := routes[lifedata.FirstString(trip, "routeId")]
		routeStops := route.StopNames
		if len(routeStops) == 0 {
			routeStops = tripStopNames(trip)
		}
		if !routeMatches(routeStops, from, to) {
			continue
		}
		routeID := lifedata.FirstString(trip, "routeId")
		stops := busStops(trip, routeStops, busTime(lifedata.FirstString(trip, "departureTime"), departure), lifedata.FirstString(trip, "arrivalTime"))
		items = append(items, busItem{
			RouteID:          routeID,
			DepartureCampus:  firstStop(stops),
			ArrivalCampus:    lastStop(stops),
			Stops:            stops,
			DepartureMinutes: departure,
			DepartureTime:    busTime(lifedata.FirstString(trip, "departureTime"), departure),
			Arrival:          lifedata.FirstString(trip, "arrivalTime"),
			Route:            busRouteLabel(route, routeStops),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].DepartureMinutes < items[j].DepartureMinutes
	})
	return items
}

func nextBusByRoute(data map[string]any, args []string, now time.Time) []busItem {
	items := nextBusItems(data, args, now)
	byRoute := make(map[string]busItem)
	for _, item := range items {
		key := item.RouteID
		if key == "" {
			key = item.Route
		}
		if _, exists := byRoute[key]; !exists {
			byRoute[key] = item
		}
	}
	out := make([]busItem, 0, len(byRoute))
	for _, item := range byRoute {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if campusRank(out[i].DepartureCampus) != campusRank(out[j].DepartureCampus) {
			return campusRank(out[i].DepartureCampus) < campusRank(out[j].DepartureCampus)
		}
		if out[i].DepartureMinutes == out[j].DepartureMinutes {
			return out[i].Route < out[j].Route
		}
		return out[i].DepartureMinutes < out[j].DepartureMinutes
	})
	return out
}

func formatBusItemsByDepartureCampus(items []busItem, limit int) []string {
	lines := make([]string, 0, len(items)+4)
	lastCampus := ""
	for i, item := range items {
		if limit > 0 && i >= limit {
			break
		}
		campus := item.DepartureCampus
		if campus == "" {
			campus = "其他"
		}
		if campus != lastCampus {
			if lastCampus != "" {
				lines = append(lines, "")
			}
			lastCampus = campus
		}
		lines = append(lines, formatBusItem(item))
	}
	return lines
}

func formatBusItem(item busItem) string {
	if len(item.Stops) > 0 {
		parts := make([]string, 0, len(item.Stops))
		for _, stop := range item.Stops {
			parts = append(parts, formatBusStop(stop))
		}
		return strings.Join(parts, "  →  ")
	}
	line := strings.ReplaceAll(item.Route, " -> ", " → ") + "：" + textutil.MonospaceDigits(item.DepartureTime)
	if item.Arrival != "" {
		line += "（到 " + textutil.MonospaceDigits(item.Arrival) + "）"
	}
	return line
}

func formatBusStop(stop busStop) string {
	name := textutil.PadRightDisplayWide(stop.Name, busStopNameColumnWidth)
	timeText := busMissingTimePlaceholder
	if stop.Time != "" {
		timeText = textutil.MonospaceDigits(stop.Time)
	}
	return name + " " + timeText
}

type busRoute struct {
	Name      string
	StopNames []string
}

func busRouteMap(raw any) map[string]busRoute {
	routes := lifedata.MapSlice(raw)
	out := make(map[string]busRoute, len(routes))
	for _, route := range routes {
		rawStops := lifedata.MapSlice(route["stops"])
		stops := make([]string, 0, len(rawStops))
		for _, stop := range rawStops {
			name := campusName(lifedata.FirstString(stop, "nameCn", "name", "namePrimary"))
			if name == "" {
				name = campusName(lifedata.NestedString(stop, "campus", "nameCn", "namePrimary", "name"))
			}
			if name != "" {
				stops = append(stops, name)
			}
		}
		id := lifedata.FirstString(route, "id")
		if id == "" {
			continue
		}
		out[id] = busRoute{
			Name:      lifedata.FirstString(route, "nameCn", "namePrimary", "name"),
			StopNames: stops,
		}
	}
	return out
}

func tripStopNames(trip map[string]any) []string {
	rawStops := lifedata.MapSlice(trip["stopTimes"])
	stops := make([]string, 0, len(rawStops))
	for _, stop := range rawStops {
		name := campusName(lifedata.FirstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, name)
		}
	}
	return stops
}

func busStops(trip map[string]any, routeStops []string, departureTime, arrivalTime string) []busStop {
	rawStops := lifedata.MapSlice(trip["stopTimes"])
	stops := make([]busStop, 0, len(rawStops))
	for _, stop := range rawStops {
		name := campusName(lifedata.FirstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, busStop{Name: name, Time: lifedata.FirstString(stop, "time")})
		}
	}
	if len(stops) > 0 {
		return stops
	}
	for i, name := range routeStops {
		stop := busStop{Name: name}
		if i == 0 {
			stop.Time = departureTime
		}
		if i == len(routeStops)-1 {
			stop.Time = arrivalTime
		}
		stops = append(stops, stop)
	}
	return stops
}

func busFilter(args []string) (string, string) {
	if len(args) == 1 {
		return campusName(args[0]), ""
	}
	if len(args) < 2 {
		return "", ""
	}
	switch normToken(args[0]) {
	case "to", "到":
		return "", campusName(args[1])
	}
	return campusName(args[0]), campusName(args[1])
}

func routeMatches(stops []string, from, to string) bool {
	if from == "" || to == "" {
		if from == "" {
			if to == "" {
				return true
			}
			for i, stop := range stops {
				if stop == to {
					return i > 0
				}
			}
			return false
		}
		for _, stop := range stops {
			if stop == from {
				return true
			}
		}
		return false
	}
	fromIndex := -1
	for i, stop := range stops {
		if stop == from && fromIndex == -1 {
			fromIndex = i
		}
		if stop == to && fromIndex >= 0 && i > fromIndex {
			return true
		}
	}
	return false
}

func busRouteLabel(route busRoute, stops []string) string {
	if len(stops) > 0 {
		return strings.Join(stops, " → ")
	}
	if route.Name != "" {
		return strings.ReplaceAll(route.Name, " -> ", " → ")
	}
	return "校车"
}

func formatNumberedLine(index int, text string) string {
	prefix := textutil.PadRightDisplay(textutil.MonospaceDigits(fmt.Sprintf("%d.", index)), numberedColumnWidth)
	if text == "" {
		return prefix
	}
	return prefix + "\t" + textutil.MonospaceDigits(text)
}

func firstStop(stops []busStop) string {
	if len(stops) == 0 {
		return ""
	}
	return stops[0].Name
}

func lastStop(stops []busStop) string {
	if len(stops) == 0 {
		return ""
	}
	return stops[len(stops)-1].Name
}

func campusRank(campus string) int {
	switch campus {
	case "东区":
		return 0
	case "西区":
		return 1
	case "中区":
		return 2
	case "北区":
		return 3
	case "南区":
		return 4
	case "高新区":
		return 5
	case "":
		return 99
	default:
		return 50
	}
}

func campusAliases() []string {
	return []string{
		"east campus", "west campus", "central campus", "north campus", "south campus",
		"高新区", "高新园区", "高新",
		"先研院",
		"东区", "西区", "中区", "北区", "南区",
		"east", "west", "center", "central", "north", "south", "gx",
		"东", "西", "中", "北", "南",
	}
}

func campusName(value string) string {
	switch textutil.LowerTrim(value) {
	case "东", "东区", "east", "east campus":
		return "东区"
	case "西", "西区", "west", "west campus":
		return "西区"
	case "中", "中区", "center", "central", "central campus":
		return "中区"
	case "北", "北区", "north", "north campus":
		return "北区"
	case "南", "南区", "south", "south campus":
		return "南区"
	case "高新", "高新区", "高新园区", "gx":
		return "高新区"
	case "先研院":
		return "先研院"
	}
	return strings.TrimSpace(value)
}

func busTime(value string, minutes int) string {
	if value != "" {
		return value
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
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
const busStopNameColumnWidth = 3
const busMissingTimePlaceholder = "———"

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
