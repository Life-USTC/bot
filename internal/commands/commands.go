package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

type Handler struct {
	Life   *life.Client
	Auth   *auth.Manager
	Store  *store.Store
	Prefix string
}

type Input struct {
	Text     string
	Identity store.Identity
}

func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	cmd, ok := h.parse(input.Text)
	if !ok {
		return "", false
	}
	h.recordState(ctx, input.Identity, cmd)
	if cmd.Name == "help" {
		return h.help(), true
	}

	switch cmd.Name {
	case "login":
		return h.login(ctx, input.Identity, cmd.Args), true
	case "logout":
		return h.logout(ctx, input.Identity), true
	case "me":
		return h.me(ctx, input.Identity), true
	case "todo":
		return h.todo(ctx, input.Identity, cmd.Args), true
	case "sub", "subs", "subscription":
		return h.subscription(ctx, input.Identity), true
	case "ping":
		if err := h.Life.Health(ctx); err != nil {
			return "Life @ USTC API unavailable: " + err.Error(), true
		}
		return "Life @ USTC API is reachable.", true
	case "semester":
		return h.currentSemester(ctx), true
	case "course":
		return h.searchCourses(ctx, strings.Join(cmd.Args, " ")), true
	case "section":
		return h.searchSections(ctx, strings.Join(cmd.Args, " ")), true
	case "bus":
		return h.bus(ctx), true
	case "schedule":
		return h.subscription(ctx, input.Identity), true
	default:
		return h.help(), true
	}
}

type parsedCommand struct {
	Name string
	Args []string
	Raw  string
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
			return parsedCommand{Name: "help", Raw: raw}, true
		}
		name, args := normalizeCommand(fields[1], fields[2:])
		return parsedCommand{Name: name, Args: args, Raw: raw}, true
	}

	if fields[0] == "/help" || fields[0] == "/?" || fields[0] == "help" || fields[0] == "帮助" || fields[0] == "？" {
		return parsedCommand{Name: "help", Raw: raw}, true
	}

	name, args := normalizeCommand(fields[0], fields[1:])
	if name == "" {
		return parsedCommand{}, false
	}
	return parsedCommand{Name: name, Args: args, Raw: raw}, true
}

func normalizeCommand(name string, args []string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "-h", "--help", "help", "?", "？", "帮助", "菜单":
		return "help", args
	case "p", "ping", "状态":
		return "ping", args
	case "login", "登录", "dl":
		return "login", normalizeLoginArgs(args)
	case "logout", "退出", "登出":
		return "logout", args
	case "me", "我", "我的", "profile", "个人":
		return "me", args
	case "todo", "td", "待办", "代办", "todo待办":
		return "todo", normalizeTodoArgs(args)
	case "bus", "xc", "校车", "车":
		return "bus", args
	case "schedule", "sched", "rc", "日程", "课表", "订阅", "sub", "subs", "subscription":
		return "schedule", args
	case "semester", "term", "学期", "xq":
		return "semester", args
	case "course", "kc", "课程":
		return "course", args
	case "section", "class", "bj", "教学班", "班级":
		return "section", args
	}
	return "", args
}

func normalizeLoginArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(args[0]) {
	case "status", "check", "完成", "状态", "ok", "好了":
		next := append([]string(nil), args...)
		next[0] = "status"
		return next
	}
	return args
}

func normalizeTodoArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(args[0]) {
	case "-h", "--help", "help", "?", "？", "帮助":
		next := append([]string(nil), args...)
		next[0] = "help"
		return next
	case "add", "new", "create", "+", "添加", "新增", "加":
		next := append([]string(nil), args...)
		next[0] = "add"
		return next
	}
	return args
}

func (h Handler) help() string {
	return strings.Join([]string{
		"可以直接发：",
		"待办 / td",
		"待办 add 写报告",
		"校车 / xc",
		"日程 / rc",
		"我 / me",
		"课程 数学分析",
		"教学班 高等数学",
		"登录 / 登录 状态",
	}, "\n")
}

func (h Handler) login(ctx context.Context, ident store.Identity, args []string) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if len(args) > 0 && args[0] == "status" {
		result, err := h.Auth.PollDeviceLogin(ctx, ident)
		if err != nil {
			return "登录状态查不到：" + friendlyError(err)
		}
		return result.Message
	}
	session, err := h.Auth.BeginDeviceLogin(ctx, ident)
	if err != nil {
		return "登录开始失败：" + friendlyError(err)
	}
	link := session.VerificationURIComplete
	if link == "" {
		link = session.VerificationURI
	}
	return strings.Join([]string{
		"打开链接登录 Life @ USTC：",
		link,
		"验证码：" + session.UserCode,
		"登录完发：登录 状态",
	}, "\n")
}

func (h Handler) logout(ctx context.Context, ident store.Identity) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if err := h.Auth.Logout(ctx, ident); err != nil {
		return "退出失败：" + friendlyError(err)
	}
	return "已退出登录。"
}

func (h Handler) me(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	me, err := h.Life.Me(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			me, err = h.Life.Me(ctx, token)
		}
	}
	if err != nil {
		return "个人信息查不到：" + friendlyError(err)
	}
	name := firstString(me, "name", "username", "preferred_username", "email")
	if name == "" {
		name = firstString(me, "id", "sub")
	}
	return "已登录：" + name
}

func (h Handler) todo(ctx context.Context, ident store.Identity, args []string) string {
	if len(args) > 0 && args[0] == "help" {
		return strings.Join([]string{
			"待办用法：",
			"待办",
			"td",
			"待办 add 写报告",
			"td + 买咖啡",
		}, "\n")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if len(args) > 0 && args[0] == "add" {
		title := strings.TrimSpace(strings.Join(args[1:], " "))
		if title == "" {
			return "想加什么？例如：待办 add 写报告"
		}
		created, err := h.Life.CreateTodo(ctx, token, title)
		if err != nil && strings.Contains(err.Error(), " returned 401:") {
			token, refreshErr := h.Auth.Refresh(ctx, ident)
			if refreshErr == nil {
				created, err = h.Life.CreateTodo(ctx, token, title)
			}
		}
		if err != nil {
			return "待办添加失败：" + friendlyError(err)
		}
		id := firstString(created, "id")
		if id == "" {
			id = fmt.Sprint(created["id"])
		}
		if id != "" {
			return "已加待办：" + title
		}
		return "已加待办"
	}
	todos, err := h.Life.Todos(ctx, token, "false")
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			todos, err = h.Life.Todos(ctx, token, "false")
		}
	}
	if err != nil {
		return "待办查不到：" + friendlyError(err)
	}
	if len(todos) == 0 {
		return "没有待办。"
	}
	lines := []string{"待办："}
	for i, todo := range todos {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(todos)-i))
			break
		}
		lines = append(lines, "- "+firstString(todo, "title"))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) subscription(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := h.Life.CurrentSubscription(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			data, err = h.Life.CurrentSubscription(ctx, token)
		}
	}
	if err != nil {
		return "日程查不到：" + friendlyError(err)
	}
	sub, _ := data["subscription"].(map[string]any)
	sections, _ := sub["sections"].([]any)
	if len(sections) == 0 {
		return "还没有订阅课程。"
	}
	lines := []string{"日程订阅："}
	for i, item := range sections {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(sections)-i))
			break
		}
		section, _ := item.(map[string]any)
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) accessToken(ctx context.Context, ident store.Identity) (string, bool) {
	if h.Auth == nil {
		return "", false
	}
	token, err := h.Auth.AccessToken(ctx, ident)
	if err == nil {
		return token, true
	}
	return "", !errors.Is(err, auth.ErrNotLoggedIn) && false
}

func (h Handler) loginRequired() string {
	return "这个需要先登录。发：登录"
}

func (h Handler) recordState(ctx context.Context, ident store.Identity, cmd parsedCommand) {
	if h.Store == nil || ident.Platform == "" || ident.UserID == "" {
		return
	}
	_ = h.Store.RecordConversationState(ctx, ident, cmd.Name, strconv.Quote(cmd.Raw))
}

func (h Handler) currentSemester(ctx context.Context) string {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return "学期查不到：" + friendlyError(err)
	}
	name := firstString(semester, "name", "nameCn", "namePrimary")
	if name == "" {
		name = fmt.Sprint(semester["id"])
	}
	return "当前学期：" + name
}

func (h Handler) searchCourses(ctx context.Context, keyword string) string {
	if keyword == "" {
		return "想查哪门课？例如：课程 数学分析"
	}
	courses, err := h.Life.SearchCourses(ctx, keyword, 5)
	if err != nil {
		return "课程查不到：" + friendlyError(err)
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
	if keyword == "" {
		return "想查哪个教学班？例如：教学班 高等数学"
	}
	sections, err := h.Life.SearchSections(ctx, keyword, 5)
	if err != nil {
		return "教学班查不到：" + friendlyError(err)
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

func (h Handler) bus(ctx context.Context) string {
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return "校车查不到：" + friendlyError(err)
	}
	trips, _ := data["trips"].([]any)
	routes, _ := data["routes"].([]any)
	return fmt.Sprintf("校车数据已加载：%d 条线路，%d 班车。之后可以继续做成“下一班校车”。", len(routes), len(trips))
}

func formatCourse(course map[string]any) string {
	code := firstString(course, "code")
	name := firstString(course, "namePrimary", "nameCn", "name")
	if code == "" {
		return "- " + name
	}
	return "- " + code + " " + name
}

func formatSection(section map[string]any) string {
	code := firstString(section, "code")
	course := nestedString(section, "course", "namePrimary", "nameCn", "name")
	semester := nestedString(section, "semester", "name")
	parts := []string{code, course, semester}
	return "- " + strings.Join(nonEmpty(parts), " ")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func nestedString(m map[string]any, key string, nestedKeys ...string) string {
	child, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	return firstString(child, nestedKeys...)
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func friendlyError(err error) string {
	text := err.Error()
	if strings.Contains(text, " returned 401:") || strings.Contains(strings.ToLower(text), "unauthorized") {
		return "登录过期了，发：登录"
	}
	if strings.Contains(strings.ToLower(text), "timeout") {
		return "网络超时，等会儿再试"
	}
	return text
}
