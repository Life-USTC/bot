package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/feedback"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Handler struct {
	Life                 *life.Client
	Auth                 *auth.Manager
	Store                *store.Store
	Logger               *log.Logger
	Feedback             feedback.Recorder
	EnableImageResponses bool
	PublicCache          *PublicCommandCache

	// execution is populated only while a capability executor is running. It
	// lets legacy direct command methods report explicit domain/auth outcomes
	// without changing their user-facing string-returning APIs.
	execution *capabilityExecutionState
}

type Input struct {
	Text        string
	Identity    store.Identity
	SuppressLog bool
}

// ParseStatus is the outcome of parsing one user message. A recognized
// command with invalid arguments is deliberately distinct from unknown text:
// the former must receive a command usage response instead of falling through
// to the Agent.
type ParseStatus string

const (
	ParseStatusUnknown ParseStatus = "unknown"
	ParseStatusValid   ParseStatus = "recognized_valid"
	ParseStatusInvalid ParseStatus = "recognized_invalid"
)

// ParseResult is the normalized command parse contract. Invocation is
// populated for both valid and invalid recognized commands so callers can use
// its descriptor to render policy-aware help.
type ParseResult struct {
	Status     ParseStatus
	Invocation Invocation
}

func (r ParseResult) Valid() bool {
	return r.Status == ParseStatusValid
}

func (r ParseResult) Recognized() bool {
	return r.Status != ParseStatusUnknown
}

func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	response, ok := h.HandleResponse(ctx, input)
	return response.Text, ok
}

func (h Handler) HandleResponse(ctx context.Context, input Input) (Response, bool) {
	outcome, handled := h.HandleOutcome(ctx, input)
	return outcome.Response, handled
}

// HandleOutcome exposes the direct command boundary with the same typed
// runtime status used by structured capability execution. Its Response is the
// unchanged user-facing domain result, preserving Handle/HandleResponse
// behavior for existing callers.
func (h Handler) HandleOutcome(ctx context.Context, input Input) (CapabilityOutcome, bool) {
	input.Text = stripCQCodes(input.Text)
	parsed := h.parseResult(input.Text)
	return h.handleParsedResponse(ctx, input, parsed, time.Now())
}

// HandleInvocationResponse executes the structured invocation selected by the
// routing layer. It is the durable-job entry point and avoids reparsing text or
// applying transport-specific fallbacks during execution.
func (h Handler) HandleInvocationResponse(ctx context.Context, input Input, invocation Invocation) (Response, bool) {
	outcome, handled := h.HandleInvocationOutcome(ctx, input, invocation)
	return outcome.Response, handled
}

// HandleInvocationOutcome executes a normalized invocation at the direct
// boundary and exposes its typed status.
func (h Handler) HandleInvocationOutcome(ctx context.Context, input Input, invocation Invocation) (CapabilityOutcome, bool) {
	invocation, ok := withDescriptor(invocation)
	if !ok {
		return NotFoundOutcome(Response{}), false
	}
	status := ParseStatusValid
	if !invocation.Capability.Accepts(invocation.Args) {
		status = ParseStatusInvalid
	}
	if strings.TrimSpace(input.Text) == "" {
		input.Text = invocation.CanonicalCommand()
	}
	return h.handleParsedResponse(ctx, input, ParseResult{Status: status, Invocation: invocation}, time.Now())
}

func (h Handler) handleParsedResponse(ctx context.Context, input Input, parsed ParseResult, startedAt time.Time) (CapabilityOutcome, bool) {
	if parsed.Status == ParseStatusUnknown {
		return NotFoundOutcome(Response{}), false
	}
	cmd := parsed.Invocation
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(cmd) {
		reply := "此功能涉及个人数据，请私聊 Presto 使用。"
		if !input.SuppressLog {
			h.recordState(ctx, input.Identity, cmd)
			h.recordInteractionWithOutcome(ctx, input.Identity, cmd, reply, CapabilityOutcomeForbidden)
		}
		return ForbiddenOutcome(textResponse(reply)), true
	}
	if parsed.Status == ParseStatusInvalid {
		reply := h.help(cmd.Name)
		if strings.TrimSpace(reply) == "" {
			reply = "命令参数无效。发送“帮助”查看可用命令。"
		}
		if !input.SuppressLog {
			h.recordState(ctx, input.Identity, cmd)
			h.recordInteractionWithOutcome(ctx, input.Identity, cmd, reply, CapabilityOutcomeInvalidInput)
		}
		return InvalidInputOutcome(Response{Text: reply, Kind: cmd.Name}), true
	}
	if h.hasAdditionalCommandLine(input.Text) {
		reply := "检测到多条命令。为避免误操作，一次只处理一条；请分开发送。"
		if !input.SuppressLog {
			h.recordState(ctx, input.Identity, cmd)
			h.recordInteractionWithOutcome(ctx, input.Identity, cmd, reply, CapabilityOutcomeInvalidInput)
		}
		return InvalidInputOutcome(textResponse(reply)), true
	}
	if !input.SuppressLog {
		h.recordState(ctx, input.Identity, cmd)
	}
	outcome, handled := h.executeInvocationOutcome(ctx, input, cmd)
	if !handled {
		return NotFoundOutcome(Response{}), false
	}
	response := outcome.Response
	reply := response.Text
	if !input.SuppressLog {
		h.recordInteractionWithOutcome(ctx, input.Identity, cmd, reply, outcome.Status)
	}
	if cmd.NaturalRoute != "" {
		h.logf("natural command routed: route=%s outcome=%s latency_ms=%d", cmd.NaturalRoute, outcome.Status, time.Since(startedAt).Milliseconds())
	}
	return outcome, true
}

func (h Handler) executeInvocation(ctx context.Context, input Input, cmd Invocation) (Response, bool) {
	outcome, handled := h.executeInvocationOutcome(ctx, input, cmd)
	return outcome.Response, handled
}

func (h Handler) executeInvocationOutcome(ctx context.Context, input Input, cmd Invocation) (CapabilityOutcome, bool) {
	descriptor := cmd.Capability
	if descriptor == nil || descriptor.Execute == nil {
		return NotFoundOutcome(Response{}), false
	}
	execution := &capabilityExecutionState{effect: cmd.Policy().Effect}
	executionHandler := h
	executionHandler.execution = execution
	if cmd.Name != string(CapabilityHelp) && firstArgIsHelp(cmd.Args) {
		text := executionHandler.help(cmd.Name)
		outcome := outcomeFromResponse(executionHandler, Response{Text: text, Kind: cmd.Name})
		outcome.Response.Image = h.imageResponseForOutcome(cmd, outcome)
		return outcome, true
	}
	if descriptor.Requirements.Life && h.Life == nil {
		return FailedOutcome(Response{Text: "Life @ USTC API unavailable: not configured.", Kind: cmd.Name}), true
	}
	if descriptor.Requirements.OAuth && (h.Auth == nil || h.Auth.Store == nil) {
		return FailedOutcome(Response{Text: "登录未配置。", Kind: cmd.Name}), true
	}
	if descriptor.Requirements.Store && h.Store == nil {
		return FailedOutcome(Response{Text: "存储未配置。", Kind: cmd.Name}), true
	}

	var outcome CapabilityOutcome
	if descriptor.Requirements.PublicCache && h.PublicCache != nil {
		outcome = h.PublicCache.GetOrLoadOutcome(ctx, cmd.Name, cmd.Args, func() CapabilityOutcome {
			return descriptor.Execute(executionHandler, ctx, input.Identity, cmd)
		})
		outcome.Response.Kind = cmd.Name
	} else {
		outcome = descriptor.Execute(executionHandler, ctx, input.Identity, cmd)
	}
	outcome = normalizeOutcome(outcome)
	response := outcome.Response
	responseKind := cmd.Name
	if cmd.Name == string(CapabilityLogin) && !firstArgIsHelp(cmd.Args) {
		session, sessionErr := h.Auth.Store.ActiveLoginSession(ctx, input.Identity)
		if sessionErr == nil && session != nil {
			responseKind = ResponseKindAuthWait
			outcome.Status = CapabilityOutcomeAuthRequired
		}
	}
	if cmd.Name != string(CapabilityLogin) && outcome.Status == CapabilityOutcomeAuthRequired && descriptor.AutoLogin && h.Auth != nil && h.Auth.Store != nil && store.IsDirectConversation(input.Identity) {
		loginResponse, loginErr := h.BeginLoginForRequest(ctx, input)
		if loginErr != nil {
			h.logf("start resumable login failed: %v", loginErr)
			response.Text = h.commandError("登录开始失败：", loginErr)
			outcome.Status = CapabilityOutcomeFailed
		} else {
			response.Text = loginResponse.Text
			responseKind = ResponseKindAuthWait
			outcome.Status = CapabilityOutcomeAuthRequired
		}
	}
	response.Kind = responseKind
	if responseKind != ResponseKindAuthWait {
		response.Image = h.imageResponseForOutcome(cmd, outcome)
	} else {
		response.Image = nil
	}
	outcome.Response = response
	return outcome, true
}

const (
	feedbackContextLimit     = 3
	feedbackContextLookback  = 12
	feedbackContextTextRunes = 220
)

var feedbackEmailPattern = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)

func sharedCommandAllowed(cmd Invocation) bool {
	if cmd.Capability == nil {
		return false
	}
	policy := cmd.Capability.PolicyFor(cmd)
	return policy.DataScope == DataScopePublic
}

func (h Handler) parse(text string) (Invocation, bool) {
	result := h.parseResult(text)
	if !result.Valid() {
		return Invocation{}, false
	}
	return result.Invocation, true
}

func (h Handler) parseResult(text string) ParseResult {
	raw := strings.TrimSpace(stripCQCodes(text))
	if raw == "" {
		return ParseResult{Status: ParseStatusUnknown}
	}
	if isNaturalCalendarLinkRequest(raw) {
		result := acceptedCommandResult(raw, "subscription", []string{"link"})
		result.Invocation.NaturalRoute = "calendar_link"
		return result
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ParseResult{Status: ParseStatusUnknown}
	}
	if isHelpToken(fields[0]) {
		return acceptedCommandResult(raw, string(CapabilityHelp), fields[1:])
	}

	if name, args, ok := normalizeHierarchicalCommand(commandToken(fields[0]), fields[1:]); ok {
		return acceptedCommandResult(raw, name, args)
	}

	if len(fields) >= 2 {
		joined := fields[0] + fields[1]
		if name, args, ok := normalizeJoinedCommand(joined, fields[2:]); ok {
			return acceptedCommandResult(raw, name, args)
		}
	}

	name, args := normalizeCommand(fields[0], fields[1:])
	if name == "" {
		return parseNaturalReadIntent(raw)
	}
	result := acceptedCommandResult(raw, name, args)
	if result.Recognized() {
		return result
	}
	return parseNaturalReadIntent(raw)
}

func parseNaturalReadIntent(raw string) ParseResult {
	if result := parseNaturalScheduleIntent(raw); result.Recognized() {
		return result
	}
	return parseNaturalBusIntent(raw)
}

// ParseCommand exposes the same tri-state parser used by Handler execution to
// integrations that need to persist or inspect a normalized invocation.
func ParseCommand(text string) ParseResult {
	return Handler{}.parseResult(text)
}

// ParseInvocation is the direct command parser used by integrations that need
// the normalized capability and its policy without executing it.
func ParseInvocation(text string) (Invocation, bool) {
	return Handler{}.parse(text)
}

func isNaturalCalendarLinkRequest(raw string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(commandToken(raw)), ""))
	hasCalendar := strings.Contains(compact, "日历") || strings.Contains(compact, "ical")
	hasLink := strings.Contains(compact, "链接") || strings.Contains(compact, "地址") || strings.Contains(compact, "url")
	if hasCalendar && hasLink {
		for _, request := range []string{"给我", "发我", "发一下", "再发", "请发", "请给", "获取", "查看", "生成", "我要", "我想要", "怎么", "如何"} {
			if strings.Contains(compact, request) {
				return true
			}
		}
		for _, noun := range []string{"日历链接", "日历订阅链接", "日历订阅地址", "ical链接", "ical地址"} {
			if compact == noun {
				return true
			}
		}
	}
	if strings.Contains(compact, "课表") && (strings.Contains(compact, "订阅链接") || strings.Contains(compact, "订阅地址")) {
		return true
	}
	if strings.Contains(compact, "课表") && hasCalendar {
		for _, action := range []string{"添加", "导入", "同步", "订阅", "放到", "加到"} {
			if strings.Contains(compact, action) {
				return true
			}
		}
	}
	return compact == "订阅url" || compact == "订阅地址" || compact == "订阅链接" || strings.Contains(compact, "给我订阅url")
}

func normalizeCommand(name string, args []string) (string, []string) {
	key := commandToken(name)
	if isHelpToken(key) {
		return "help", args
	}
	if normalized, normalizedArgs, ok := normalizeHierarchicalCommand(key, args); ok {
		return normalized, normalizedArgs
	}
	if normalized, normalizedArgs, ok := normalizeJoinedCommand(name, args); ok {
		return normalized, normalizedArgs
	}
	if descriptor, ok := descriptorForForm(key); ok {
		return string(descriptor.ID), args
	}
	return "", args
}

func normalizeHierarchicalCommand(name string, args []string) (string, []string, bool) {
	help := func(topic string) (string, []string, bool) {
		return "help", []string{topic}, true
	}
	action := ""
	rest := args
	if len(args) > 0 {
		action = commandToken(args[0])
		rest = args[1:]
	}
	if isHelpToken(action) {
		if canonical, ok := canonicalCommandAlias(name); ok {
			return help(canonical)
		}
		return "", args, false
	}

	switch name {
	case "日程":
		switch action {
		case "":
			return help("日程")
		case "今日", "今天":
			return "calendar", rest, true
		case "概览", "汇总":
			return "overview", rest, true
		case "截止", "近期截止":
			return "upcoming_deadlines", rest, true
		}
	case "课表":
		switch action {
		case "":
			return "schedule", nil, true
		case "下一节", "下一节课", "下节课":
			return "nextclass", rest, true
		case "单日":
			if len(rest) == 0 {
				return "schedule", []string{"today"}, true
			}
			if day, ok := normalizeScheduleDay(commandToken(rest[0])); ok {
				return "schedule", []string{day}, true
			}
		}
		return "schedule", normalizeScheduleArgs(args), true
	case "待办":
		return "todo", normalizeTodoArgs(args), true
	case "作业":
		if action == "列表" || action == "查看" {
			rest = translateCommandFields(rest, homeworkFieldAliases)
			return "homework", normalizeHomeworkArgs(rest), true
		}
		return "homework", normalizeHomeworkArgs(translateCommandFields(args, homeworkFieldAliases)), true
	case "考试":
		if action == "" {
			return "exam", nil, true
		}
	case "课程":
		switch action {
		case "":
			return help("课程")
		case "搜索", "查询":
			return "course_search", translateCommandFields(rest, courseFieldAliases), true
		case "查看", "详情", "编号":
			return "course_by_jw_id", rest, true
		}
	case "教学班", "班级":
		switch action {
		case "":
			return help("教学班")
		case "搜索", "查询":
			return "section_search", translateCommandFields(rest, sectionFieldAliases), true
		case "查看", "详情", "编号":
			return "section_by_jw_id", rest, true
		case "课表":
			return "section_schedules", rest, true
		case "考试":
			return "section_exams", rest, true
		case "作业":
			return "section_homeworks", rest, true
		}
	case "老师", "教师":
		switch action {
		case "":
			return help("老师")
		case "搜索", "查询":
			return "teacher_search", translateCommandFields(rest, teacherFieldAliases), true
		case "查看", "详情", "编号":
			return "teacher_by_id", rest, true
		}
	case "学期":
		switch action {
		case "", "当前":
			return "semester", rest, true
		case "列表":
			return "list_semesters", rest, true
		}
	case "订阅":
		switch action {
		case "":
			return "subscription", nil, true
		case "列表", "查看":
			return "my_subscribed_sections", rest, true
		case "添加", "新增", "导入":
			return "subscription", append([]string{"import"}, rest...), true
		case "删除", "移除", "退订":
			return "unsubscribe_section_by_jw_id", rest, true
		case "链接", "日历":
			return "subscription", []string{"link"}, true
		}
	case "校车":
		switch action {
		case "":
			return "bus", nil, true
		case "查询":
			return "bus", rest, true
		case "路线":
			return "bus_routes", rest, true
		case "偏好":
			if len(rest) == 0 {
				return "bus", []string{"偏好"}, true
			}
			switch commandToken(rest[0]) {
			case "路线":
				return "bus", append([]string{"设置"}, rest[1:]...), true
			case "已发车", "南区":
				return "bus", rest, true
			}
		}
	case "账户", "账号":
		switch action {
		case "":
			return help("账户")
		case "信息", "我":
			return "account", rest, true
		case "登录":
			return "login", normalizeLoginArgs(rest), true
		case "登录状态":
			return "login", []string{"status"}, true
		case "退出", "登出":
			return "logout", rest, true
		case "状态":
			return "status", rest, true
		}
	case "设置":
		switch action {
		case "":
			return "settings", nil, true
		case "通知", "提醒":
			return "notify", normalizeNotifyArgs(rest), true
		}
	case "系统":
		switch action {
		case "":
			return help("系统")
		case "状态":
			return "status", rest, true
		case "检查", "连通性":
			return "ping", rest, true
		}
	}
	return "", args, false
}

var homeworkFieldAliases = map[string]string{
	"学期id":   "semester_id",
	"学期jwid": "semester_jw_id",
}

var courseFieldAliases = map[string]string{
	"关键词":    "keyword",
	"培养层次":   "education_level_id",
	"培养层次id": "education_level_id",
	"类别":     "category_id",
	"课程类别":   "category_id",
	"类别id":   "category_id",
	"课堂类型":   "class_type_id",
	"类型":     "class_type_id",
	"课堂类型id": "class_type_id",
	"数量":     "limit",
}

var sectionFieldAliases = map[string]string{
	"关键词":    "keyword",
	"课程id":   "course_id",
	"课程jwid": "course_jw_id",
	"学期id":   "semester_id",
	"学期jwid": "semester_jw_id",
	"校区id":   "campus_id",
	"院系id":   "department_id",
	"老师id":   "teacher_id",
	"老师代码":   "teacher_code",
	"数量":     "limit",
}

var teacherFieldAliases = map[string]string{
	"关键词":  "keyword",
	"院系id": "department_id",
	"数量":   "limit",
}

func translateCommandFields(args []string, aliases map[string]string) []string {
	out := copyArgs(args)
	for i, arg := range out {
		if translated, ok := aliases[commandToken(arg)]; ok {
			out[i] = translated
		}
	}
	return out
}

func canonicalCommandAlias(name string) (string, bool) {
	if descriptor, ok := descriptorForForm(name); ok {
		return string(descriptor.ID), true
	}
	return "", false
}

func normalizeJoinedCommand(name string, args []string) (string, []string, bool) {
	key := commandToken(name)
	if day, ok := joinedScheduleDay(key); ok {
		return "schedule", []string{day}, true
	}
	switch key {
	case "下一节课":
		return "nextclass", nil, true
	case "课程订阅":
		return "subscription", normalizeSubscriptionArgs(args), true
	case "订阅链接", "日历订阅链接", "subscriptionlink", "calendarlink":
		return "subscription", []string{"link"}, true
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
	for _, scheduleToken := range []string{"schedule", "课表", "kb"} {
		if strings.HasPrefix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[len(scheduleToken):]); ok {
				return day, true
			}
			if week, ok := normalizeScheduleWeekTarget(key[len(scheduleToken):]); ok {
				return week, true
			}
			if date, ok := normalizeScheduleDateToken(key[len(scheduleToken):]); ok {
				return date, true
			}
			if semester, ok := normalizeScheduleSemesterTarget(key[len(scheduleToken):]); ok {
				return semester, true
			}
		}
		if strings.HasSuffix(key, scheduleToken) {
			if day, ok := normalizeScheduleDay(key[:len(key)-len(scheduleToken)]); ok {
				return day, true
			}
			if week, ok := normalizeScheduleWeekTarget(key[:len(key)-len(scheduleToken)]); ok {
				return week, true
			}
			if date, ok := normalizeScheduleDateToken(key[:len(key)-len(scheduleToken)]); ok {
				return date, true
			}
			if semester, ok := normalizeScheduleSemesterTarget(key[:len(key)-len(scheduleToken)]); ok {
				return semester, true
			}
		}
	}
	return "", false
}

func normalizeScheduleDay(value string) (string, bool) {
	switch value {
	case "today", "single-day", "singleday", "今天", "今日", "单日":
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
	return "week-date:" + value, true
}

var scheduleWeekNumberPattern = regexp.MustCompile(`^第?([0-9]+)周$`)
var scheduleSemesterPattern = regexp.MustCompile(`^(\d{2}|\d{4})年?(春|秋)(?:季)?(?:学期)?$`)

func normalizeScheduleSemesterTarget(value string) (string, bool) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "")
	matches := scheduleSemesterPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return "", false
	}
	year, err := strconv.Atoi(matches[1])
	if err != nil {
		return "", false
	}
	if year < 100 {
		year += 2000
	}
	return fmt.Sprintf("semester:%04d-%s", year, matches[2]), true
}

func normalizeScheduleWeekTarget(value string) (string, bool) {
	value = normToken(value)
	switch value {
	case "thisweek", "本周", "这周", "本星期", "这个星期":
		return "this-week", true
	case "nextweek", "下周", "下星期", "下个星期":
		return "next-week", true
	}
	plainValue := textutil.PlainDigits(value)
	if matches := scheduleWeekNumberPattern.FindStringSubmatch(plainValue); len(matches) == 2 {
		week, err := strconv.Atoi(matches[1])
		if err == nil && week > 0 {
			return "week-number:" + strconv.Itoa(week), true
		}
	}
	if strings.HasSuffix(value, "周") {
		dateValue := strings.TrimSpace(strings.TrimSuffix(value, "周"))
		if _, ok := parseScheduleDateToken(dateValue, chinaNow()); ok {
			return "week-date:" + dateValue, true
		}
	}
	return "", false
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
	normalized := strings.NewReplacer("年", "-", "月", "-", "日", "", "号", "", "/", "-", ".", "-").Replace(value)
	parts := strings.Split(normalized, "-")
	year := base.Year()
	if len(parts) == 3 {
		parsedYear, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || parsedYear < 1 {
			return time.Time{}, false
		}
		year = parsedYear
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return time.Time{}, false
	}
	month, errMonth := strconv.Atoi(strings.TrimSpace(parts[0]))
	day, errDay := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errMonth != nil || errDay != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, loc)
	if parsed.Year() != year || parsed.Month() != time.Month(month) || parsed.Day() != day {
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
	case "link", "url", "calendar", "ical", "链接", "日历", "订阅链接":
		return withFirstArg(args, "link")
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
	case "undo", "undone", "reopen", "reset", "取消", "撤销", "恢复":
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
	case "undo", "undone", "reset", "取消", "撤销", "恢复":
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
	if semester, ok := normalizeScheduleSemesterTarget(strings.Join(args, "")); ok {
		return []string{semester}
	}
	if day, ok := normalizeScheduleDay(normToken(args[0])); ok {
		return withFirstArg(args, day)
	}
	if week, ok := normalizeScheduleWeekTarget(args[0]); ok {
		return withFirstArg(args, week)
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

func firstArgIsHelp(args []string) bool {
	return hasArgs(args) && isHelpToken(args[0])
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
		return h.invalidInput("想反馈什么？例如：反馈 校车时间希望更清楚")
	}
	contextText := h.feedbackContext(ctx, ident)
	if h.Feedback == nil {
		return h.failed("反馈功能暂不可用。")
	}
	result, err := h.Feedback.Record(ctx, ident, feedback.Submission{
		Source:   feedback.SourceUser,
		Category: "user_feedback",
		Content:  text,
		Context:  contextText,
	})
	if err != nil {
		return h.commandError("反馈保存失败：", err)
	}
	if result.AdminIntents > 0 {
		return "已收到反馈，会转给维护者。"
	}
	return "已收到反馈。"
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
	type contextTurn struct {
		raw   string
		reply string
	}
	selected := make([]contextTurn, 0, feedbackContextLimit)
	for i := len(interactions) - 1; i >= 0; i-- {
		interaction := interactions[i]
		switch strings.ToLower(strings.TrimSpace(interaction.Command)) {
		case "account", "feedback", "login", "status":
			continue
		}
		raw := compactFeedbackContextText(interaction.RawText)
		reply := compactFeedbackContextText(interaction.Reply)
		if raw == "" && reply == "" {
			continue
		}
		selected = append(selected, contextTurn{raw: raw, reply: reply})
		if len(selected) >= feedbackContextLimit {
			break
		}
	}
	lines := make([]string, 0, len(selected)*2)
	for i := len(selected) - 1; i >= 0; i-- {
		if selected[i].raw != "" {
			lines = append(lines, "用户："+selected[i].raw)
		}
		if selected[i].reply != "" {
			lines = append(lines, "Bot："+selected[i].reply)
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
	text = feedbackEmailPattern.ReplaceAllString(text, "[已隐藏邮箱]")
	runes := []rune(text)
	if len(runes) <= feedbackContextTextRunes {
		return text
	}
	return string(runes[:feedbackContextTextRunes]) + "..."
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
		return h.failed("登录未配置。")
	}
	if firstArgIs(args, "status") {
		result, err := h.Auth.PollDeviceLogin(ctx, ident)
		if err != nil {
			return h.commandError("登录状态查不到：", err)
		}
		if result.Pending || result.SlowDown {
			h.markOutcome(CapabilityOutcomeAuthRequired)
		} else if result.Status != "" && result.Status != store.LoginStatusApproved {
			h.markOutcome(CapabilityOutcomeFailed)
		}
		return result.Message
	}
	if _, err := h.Auth.AccessToken(ctx, ident); err == nil {
		return "已登录 Life @ USTC。"
	}
	session, err := h.Auth.Store.ActiveLoginSession(ctx, ident)
	if err != nil {
		return h.commandError("登录状态查不到：", err)
	}
	if session == nil {
		session, err = h.Auth.BeginDeviceLogin(ctx, ident)
		if err != nil {
			return h.commandError("登录开始失败：", err)
		}
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
		return h.failed("登录未配置。")
	}
	if err := h.Auth.Logout(ctx, ident); err != nil {
		return h.commandError("退出失败：", err)
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
		return h.commandError("个人信息查不到：", err)
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
			"待办 列表 第2页",
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
			return h.invalidInput("想加什么？例如：待办 add 写报告")
		}
		return h.createTodo(ctx, ident, token, opts)
	}
	if firstArgIs(args, "done") {
		target := joinedArgs(args[1:])
		if target == "" {
			return h.invalidInput("想完成哪条？例如：td done 1")
		}
		return h.setTodoCompletion(ctx, ident, token, target, true)
	}
	if firstArgIs(args, "undo") {
		target := joinedArgs(args[1:])
		if target == "" {
			return h.invalidInput("想恢复哪条？例如：td undo 1")
		}
		return h.setTodoCompletion(ctx, ident, token, target, false)
	}
	if firstArgIs(args, "delete") {
		target := joinedArgs(args[1:])
		if target == "" {
			return h.invalidInput("想删除哪条？例如：td delete 1")
		}
		return h.deleteTodo(ctx, ident, token, target)
	}
	if firstArgIs(args, "update") {
		if len(args) < 3 {
			return h.invalidInput("想改哪条、改什么？例如：td update 1 title 写报告")
		}
		return h.updateTodo(ctx, ident, token, args[1], args[2:])
	}
	if opts, page, ok, err := todoListQueryFromArgs(args); ok {
		if err != nil {
			return h.invalidInput(err.Error())
		}
		return h.listTodos(ctx, ident, token, opts, page)
	}
	if hasArgs(args) {
		return h.createTodo(ctx, ident, token, parseTodoCreateArgs(args))
	}
	return h.listTodos(ctx, ident, token, life.TodoListOptions{Completed: "false"}, 1)
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
		return h.invalidInput("想加什么？例如：td 写报告")
	}
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CreateTodoWithOptions(ctx, token, opts)
	})
	if err != nil {
		return h.commandError("待办添加失败：", err)
	}
	return "已加待办：" + opts.Title
}

func (h Handler) listTodos(ctx context.Context, ident store.Identity, token string, opts life.TodoListOptions, requestedPage int) string {
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{})
	if err != nil {
		return h.commandError("待办查不到：", err)
	}
	numbered, err := filterNumberedTodos(todos, opts)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	if len(numbered) == 0 {
		return "没有待办。"
	}
	command := func(page int) string {
		return todoListPageCommand(opts, page)
	}
	reply, ok := formatListPage("待办：", len(numbered), requestedPage, command, func(i int) string {
		return formatNumberedLine(numbered[i].number, formatTodo(numbered[i].todo))
	})
	if !ok {
		return h.invalidInput(listPageOutOfRange("待办", len(numbered), command))
	}
	return reply
}

func (h Handler) setTodoCompletion(ctx context.Context, ident store.Identity, token, target string, completed bool) string {
	targets := splitTodoTargets(target)
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{})
	if err != nil {
		return h.commandError("待办查不到：", err)
	}
	if len(targets) > 1 {
		return h.setTodoCompletionBatch(ctx, ident, token, todos, targets, completed)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		if completed {
			return h.notFound("没找到这条待办。发 td 看编号，再试：td done 1")
		}
		return h.notFound("没找到这条已完成待办。发 td completed 看编号，再试：td undo 1")
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
			return h.failed("这条待办没有可用 ID，暂时操作不了。")
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
				return h.commandError("待办完成失败：", err)
			}
			return h.commandError("待办恢复失败：", err)
		}
	}
	if len(done) == 0 {
		if completed {
			return h.notFound("没找到这些待办。发 td 看编号，再试：td done 1,2,3")
		}
		return h.notFound("没找到这些已完成待办。发 td completed 看编号，再试：td undo 1,2,3")
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
		return h.failed("这条待办没有可用 ID，暂时操作不了。")
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.SetTodoCompleted(ctx, token, id, completed)
	})
	if err != nil {
		if completed {
			return h.commandError("待办完成失败：", err)
		}
		return h.commandError("待办恢复失败：", err)
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
		return h.commandError("待办查不到：", err)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		return h.notFound("没找到这条待办。发 td all 看编号，再试：td update 1 title 写报告")
	}
	id := lifedata.FirstString(todo, "id")
	if id == "" {
		return h.failed("这条待办没有可用 ID，暂时修改不了。")
	}
	opts := parseTodoUpdateArgs(args)
	if !hasTodoUpdate(opts) {
		return h.invalidInput("想改什么？例如：td update 1 title 写报告")
	}
	err = auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.UpdateTodo(ctx, token, id, opts)
	})
	if err != nil {
		return h.commandError("待办修改失败：", err)
	}
	return "已修改待办：" + lifedata.FirstString(todo, "title", "id")
}

func (h Handler) deleteTodo(ctx context.Context, ident store.Identity, token, target string) string {
	todos, err := h.todos(ctx, ident, token, life.TodoListOptions{})
	if err != nil {
		return h.commandError("待办查不到：", err)
	}
	targets := splitTodoTargets(target)
	if len(targets) > 1 {
		return h.deleteTodoBatch(ctx, ident, token, todos, targets)
	}
	todo, ok := resolveTodo(todos, target)
	if !ok {
		return h.notFound("没找到这条待办。发 td all 看编号，再试：td delete 1")
	}
	return h.deleteTodoItem(ctx, ident, token, todo)
}

func (h Handler) deleteTodoBatch(ctx context.Context, ident store.Identity, token string, todos []map[string]any, targets []string) string {
	execution := h.execution
	if execution == nil {
		execution = &capabilityExecutionState{}
		h.execution = execution
	}
	deleted := make([]string, 0, len(targets))
	missing := []string{}
	for _, target := range targets {
		todo, ok := resolveTodo(todos, target)
		if !ok {
			missing = append(missing, target)
			continue
		}
		reply := h.deleteTodoItem(ctx, ident, token, todo)
		if execution.status == CapabilityOutcomeFailed || execution.status == CapabilityOutcomeAuthRequired {
			return reply
		}
		deleted = append(deleted, strings.TrimPrefix(reply, "已删除："))
	}
	if len(deleted) == 0 {
		return h.notFound("没找到这些待办。发 td all 看编号，再试：td delete 1,2,3")
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
		h.markOutcome(CapabilityOutcomeFailed)
		return "这条待办没有可用 ID，暂时删除不了。"
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.DeleteTodo(ctx, token, id)
	})
	if err != nil {
		return h.commandError("待办删除失败：", err)
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
	if due != "" {
		due = "截止 " + due
	}
	if due == "" && title == "" {
		return lifedata.FirstString(todo, "id")
	}
	return strings.TrimRight(strings.Join([]string{due, title}, "\t"), "\t")
}

type numberedTodo struct {
	number int
	todo   map[string]any
}

func filterNumberedTodos(todos []map[string]any, opts life.TodoListOptions) ([]numberedTodo, error) {
	var dueBefore, dueAfter time.Time
	var beforeSet, afterSet bool
	if opts.DueBefore != "" {
		dueBefore, beforeSet = parseTodoFilterTime(opts.DueBefore)
		if !beforeSet {
			return nil, errors.New("截止前日期格式不正确。例：2026-06-10")
		}
	}
	if opts.DueAfter != "" {
		dueAfter, afterSet = parseTodoFilterTime(opts.DueAfter)
		if !afterSet {
			return nil, errors.New("截止后日期格式不正确。例：2026-06-10")
		}
	}
	out := make([]numberedTodo, 0, len(todos))
	for i, todo := range todos {
		completed, _ := todo["completed"].(bool)
		switch opts.Completed {
		case "false":
			if completed {
				continue
			}
		case "true":
			if !completed {
				continue
			}
		}
		if opts.Priority != "" && lifedata.FirstString(todo, "priority") != opts.Priority {
			continue
		}
		if beforeSet || afterSet {
			due, ok := lifedata.ParseAPITime(lifedata.FirstString(todo, "dueAt"))
			if !ok || beforeSet && !due.Before(dueBefore) || afterSet && due.Before(dueAfter) {
				continue
			}
		}
		out = append(out, numberedTodo{number: i + 1, todo: todo})
	}
	return out, nil
}

func parseTodoFilterTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed, true
	}
	return lifedata.ParseAPITime(value)
}

func todoListQueryFromArgs(args []string) (life.TodoListOptions, int, bool, error) {
	if _, ok, err := todoListOptionsFromArgs(args); !ok || err != nil {
		return life.TodoListOptions{}, 1, ok, err
	}
	listArgs, page, err := extractListPage(args)
	if err != nil {
		return life.TodoListOptions{}, page, true, err
	}
	opts, _, err := todoListOptionsFromArgs(listArgs)
	return opts, page, true, err
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

func todoListPageCommand(opts life.TodoListOptions, page int) string {
	parts := []string{"待办", "列表"}
	switch opts.Completed {
	case "":
		parts = append(parts, "全部")
	case "true":
		parts = append(parts, "已完成")
	default:
		parts = append(parts, "未完成")
	}
	if opts.Priority != "" {
		priority := map[string]string{"low": "低", "medium": "中", "high": "高"}[opts.Priority]
		if priority == "" {
			priority = opts.Priority
		}
		parts = append(parts, "优先级", priority)
	}
	if opts.DueBefore != "" {
		parts = append(parts, "截止前", opts.DueBefore)
	}
	if opts.DueAfter != "" {
		parts = append(parts, "截止后", opts.DueAfter)
	}
	return strings.Join(append(parts, fmt.Sprintf("第%d页", page)), " ")
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
		return h.commandError("今日安排查不到：", err)
	}
	todos, err := h.pendingTodos(ctx, ident, token)
	if err != nil {
		return h.commandError("今日安排查不到：", err)
	}
	homeworks, err := h.homeworks(ctx, ident, token)
	if err != nil {
		return h.commandError("今日安排查不到：", err)
	}
	subscription, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return h.commandError("今日安排查不到：", err)
	}
	exams := upcomingSubscriptionExams(subscriptionExams(subscription), now)
	return formatOverview(now, schedules, todos, dueSoonHomeworks(homeworks, now), exams)
}

func formatOverview(now time.Time, schedules []map[string]any, todos []map[string]any, homeworks []map[string]any, exams []subscriptionExam) string {
	lines := []string{textutil.MonospaceDigits(now.In(lifedata.ChinaLocation()).Format("01-02")) + " 安排："}
	lines = appendOverviewSection(lines, "今日课表", schedules, formatSchedule, "课表 单日 今天")
	lines = appendOverviewSection(lines, "待办", todos, formatTodo, "待办")
	lines = appendOverviewSection(lines, "近期作业", homeworks, formatHomework, "作业")
	lines = appendOverviewSection(lines, "考试", exams, formatExam, "考试")
	if len(lines) == 1 {
		return lines[0] + "\n暂无安排。"
	}
	reply := strings.Join(lines, "\n")
	if len(schedules) > 0 || len(exams) > 0 {
		return withCalendarSubscriptionHint(reply)
	}
	return reply
}

func appendOverviewSection[T any](lines []string, title string, items []T, format func(T) string, command string) []string {
	if len(items) == 0 {
		return lines
	}
	if len(lines) > 1 {
		lines = append(lines, "")
	}
	lines = append(lines, fmt.Sprintf("%s (%d)：", title, len(items)))
	shown := min(len(items), 3)
	for i, item := range items[:shown] {
		lines = append(lines, formatNumberedLine(i+1, format(item)))
	}
	return appendListOverflow(lines, len(items), shown, command)
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
			"作业 列表 第2页",
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
			return h.invalidInput("想改哪条作业？例如：作业 done 1")
		}
		completed := args[0] == "done"
		homeworks, err := h.homeworks(ctx, ident, token)
		if err != nil {
			return h.commandError("作业查不到：", err)
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
			return h.notFound("没找到这条作业。发 作业 看编号，再试：作业 done 1")
		}
		return h.setHomeworkCompletionItem(ctx, ident, token, homework, completed)
	}
	listArgs, err := parseHomeworkListArgs(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	homeworks, err := h.homeworks(ctx, ident, token)
	if err != nil {
		return h.commandError("作业查不到：", err)
	}
	numbered := filterNumberedHomeworks(homeworks, listArgs)
	if len(numbered) == 0 {
		if listArgs.all {
			return "没有作业。"
		}
		return "没有未完成作业。"
	}
	command := func(page int) string {
		return homeworkListPageCommand(listArgs, page)
	}
	page, ok := listPageFor(len(numbered), listArgs.page)
	if !ok {
		return h.invalidInput(listPageOutOfRange("作业", len(numbered), command))
	}
	return formatNumberedHomeworkListAt(
		numbered[page.start:page.end],
		chinaNow(),
		pageNavigationLine(page, command),
	)
}

type homeworkListArgs struct {
	all          bool
	semesterID   int64
	semesterJwID int64
	page         int
}

func parseHomeworkListArgs(args []string) (homeworkListArgs, error) {
	listArgs, page, err := extractListPage(args)
	if err != nil {
		return homeworkListArgs{}, err
	}
	args = listArgs
	out := homeworkListArgs{page: page}
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
	return out, nil
}

func homeworkListPageCommand(args homeworkListArgs, page int) string {
	parts := []string{"作业", "列表"}
	if args.all {
		parts = append(parts, "全部")
	} else {
		parts = append(parts, "未完成")
	}
	if args.semesterID > 0 {
		parts = append(parts, "学期ID", strconv.FormatInt(args.semesterID, 10))
	}
	if args.semesterJwID > 0 {
		parts = append(parts, "学期JWID", strconv.FormatInt(args.semesterJwID, 10))
	}
	return strings.Join(append(parts, fmt.Sprintf("第%d页", page)), " ")
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

type numberedHomework struct {
	number   int
	homework map[string]any
}

func filterNumberedHomeworks(homeworks []map[string]any, args homeworkListArgs) []numberedHomework {
	out := make([]numberedHomework, 0, len(homeworks))
	for i, homework := range homeworks {
		if !args.all && lifedata.HomeworkCompleted(homework) {
			continue
		}
		if args.semesterID > 0 || args.semesterJwID > 0 {
			id, jwID := homeworkSemesterIDs(homework)
			if args.semesterID > 0 && id != args.semesterID ||
				args.semesterJwID > 0 && jwID != args.semesterJwID {
				continue
			}
		}
		out = append(out, numberedHomework{number: i + 1, homework: homework})
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
			return h.failed("这条作业没有可用 ID，暂时改不了。")
		}
		items = append(items, life.HomeworkCompletionItem{HomeworkID: id, Completed: completed})
		done = append(done, lifedata.FirstString(homework, "title"))
	}
	if len(items) > 0 {
		err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.SetHomeworkCompletions(ctx, token, items)
		})
		if err != nil {
			return h.commandError("作业状态更新失败：", err)
		}
	}
	if len(done) == 0 {
		return h.notFound("没找到这些作业。发 作业 看编号，再试：作业 done 1,2,3")
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
		return h.failed("这条作业没有可用 ID，暂时改不了。")
	}
	err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
		return h.Life.SetHomeworkCompletion(ctx, token, id, completed)
	})
	if err != nil {
		return h.commandError("作业状态更新失败：", err)
	}
	return homeworkCompletionReply(completed, lifedata.FirstString(homework, "title"))
}

func (h Handler) homeworks(ctx context.Context, ident store.Identity, token string) ([]map[string]any, error) {
	homeworks, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.SubscribedHomeworks(ctx, token)
	})
	sortHomeworksForDisplay(homeworks, chinaNow())
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
	due := lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt"))
	if due != "" {
		due = "截止 " + due
	}
	cells := []string{
		due,
		lifedata.HomeworkCourseLabel(homework),
		lifedata.FirstString(homework, "title"),
	}
	if strings.Trim(strings.Join(cells, ""), " \t") == "" {
		return lifedata.FirstString(homework, "id")
	}
	return textutil.MonospaceDigits(strings.TrimRight(strings.Join(cells, "\t"), "\t"))
}

func formatHomeworkListAt(homeworks []map[string]any, now time.Time) string {
	ordered := append([]map[string]any(nil), homeworks...)
	sortHomeworksForDisplay(ordered, now)
	numbered := make([]numberedHomework, len(ordered))
	for i, homework := range ordered {
		numbered[i] = numberedHomework{number: i + 1, homework: homework}
	}
	return formatNumberedHomeworkListAt(numbered, now, "")
}

func formatNumberedHomeworkListAt(homeworks []numberedHomework, now time.Time, pageLine string) string {
	now = now.In(lifedata.ChinaLocation())
	groups := []struct {
		title string
		items []numberedHomework
	}{
		{title: "已逾期"},
		{title: "近期"},
		{title: "未来"},
		{title: "已完成"},
	}
	for _, homework := range homeworks {
		bucket := homeworkDisplayBucket(homework.homework, now)
		groups[bucket].items = append(groups[bucket].items, homework)
	}
	lines := []string{"作业："}
	for _, group := range groups {
		if len(group.items) == 0 {
			continue
		}
		if len(lines) > 1 {
			lines = append(lines, "")
		}
		lines = append(lines, group.title+"：")
		for _, homework := range group.items {
			lines = append(lines, formatNumberedLine(homework.number, formatHomework(homework.homework)))
		}
	}
	if pageLine != "" {
		lines = append(lines, pageLine)
	}
	return strings.Join(lines, "\n")
}

func sortHomeworksForDisplay(homeworks []map[string]any, now time.Time) {
	lifedata.SortHomeworksByDue(homeworks)
	sort.SliceStable(homeworks, func(i, j int) bool {
		return homeworkDisplayBucket(homeworks[i], now) < homeworkDisplayBucket(homeworks[j], now)
	})
}

func homeworkDisplayBucket(homework map[string]any, now time.Time) int {
	if lifedata.HomeworkCompleted(homework) {
		return 3
	}
	due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
	switch {
	case ok && due.Before(now):
		return 0
	case !ok || !due.After(now.Add(7*24*time.Hour)):
		return 1
	default:
		return 2
	}
}

var sectionCodePattern = regexp.MustCompile(`[A-Za-z0-9_.-]+\.[A-Za-z0-9]{2}`)

func (h Handler) subscription(ctx context.Context, ident store.Identity, args []string) string {
	if hasArgs(args) {
		switch args[0] {
		case "help":
			return subscriptionHelp()
		case "link":
			return h.subscriptionCalendarLink(ctx, ident)
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
		"订阅：查看当前教学班订阅",
		"订阅 链接：查看私有日历订阅链接",
		"这是 iCalendar 链接，可在日历应用的“通过 URL 订阅/网络日历”中添加，并自动同步更新",
		"订阅 导入 <教学班代码...>：批量添加教学班",
		"例：订阅 导入 CONT5103P.01 CONT6104P.01",
	}, "\n")
}

const calendarSubscriptionHint = "提示：想把已订阅的课表和考试同步到日历应用，可私聊发送“订阅 链接”获取 iCalendar 链接，再选择“通过 URL 订阅/网络日历”添加。"

func withCalendarSubscriptionHint(reply string) string {
	reply = strings.TrimSpace(reply)
	if reply == "" || strings.Contains(reply, calendarSubscriptionHint) {
		return reply
	}
	return reply + "\n\n" + calendarSubscriptionHint
}

func (h Handler) subscriptionCalendarLink(ctx context.Context, ident store.Identity) string {
	if !store.IsDirectConversation(ident) {
		return h.forbidden("订阅链接只能在私聊里查看。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return h.commandError("订阅链接查不到：", err)
	}
	calendarURL := lifedata.NestedString(data, "subscription", "calendarUrl")
	if calendarURL == "" {
		if hasCurrentScopes, scopeErr := h.Auth.HasCurrentScopes(ctx, ident); scopeErr == nil && !hasCurrentScopes {
			if logoutErr := h.Auth.Logout(ctx, ident); logoutErr != nil {
				h.logf("clear credential missing current OAuth scopes failed: %v", logoutErr)
			}
			h.markOutcome(CapabilityOutcomeAuthRequired)
			return "当前登录未获得私有日历链接权限。请发送：登录\n重新登录后再发送：订阅 链接"
		}
		return h.notFound("当前订阅没有可用的私有日历链接。")
	}
	return strings.Join([]string{
		"日历订阅链接：",
		calendarURL,
		"使用方法：复制链接，在日历应用中选择“通过 URL 添加/订阅日历（iCalendar）”，粘贴并保存。",
		"iCalendar 订阅会自动更新，不需要反复导入。",
		"链接包含私密凭证，请勿公开或转发。",
	}, "\n")
}

func (h Handler) settings(ctx context.Context, ident store.Identity, args []string) string {
	if !hasArgs(args) || firstArgIs(args, "help") {
		return formatHelpTopic("settings")
	}
	switch settingsTopic(args[0]) {
	case "notify":
		return h.notify(ctx, ident, normalizeNotifyArgs(args[1:]))
	default:
		return h.invalidInput("未知设置项。\n" + formatHelpTopic("settings"))
	}
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
		return h.failed("存储未配置。")
	}
	if !store.IsDirectConversation(ident) {
		return h.forbidden("通知只能在私聊里设置。")
	}
	settings, err := h.Store.NotificationSettings(ctx, ident)
	if err != nil {
		return h.commandError("通知设置查不到：", err)
	}
	settings.Identity = ident
	if !hasArgs(args) || firstArgIs(args, "status") {
		return formatNotificationSettings(settings)
	}
	if len(args) < 2 {
		switch args[0] {
		case "classes", "homework":
			return h.invalidInput("想打开还是关闭？例如：通知 作业 开")
		default:
			return h.invalidInput("支持：课表、作业。")
		}
	}
	enabled := args[1] == "on"
	if args[1] != "on" && args[1] != "off" {
		return h.invalidInput("想打开还是关闭？例如：通知 作业 开")
	}
	switch args[0] {
	case "classes":
		settings.ClassesEnabled = enabled
	case "homework":
		settings.HomeworkEnabled = enabled
	default:
		return h.invalidInput("支持：课表、作业。")
	}
	if err := h.Store.SaveNotificationSettings(ctx, settings); err != nil {
		return h.commandError("通知设置保存失败：", err)
	}
	settings, err = h.Store.NotificationSettings(ctx, ident)
	if err != nil {
		return h.commandError("通知设置查不到：", err)
	}
	return formatNotificationSettings(settings)
}

func formatNotificationSettings(settings store.NotificationSettings) string {
	lines := []string{
		"通知设置：",
		"课前提醒：" + onOffText(settings.ClassesEnabled),
		"作业提醒：" + onOffText(settings.HomeworkEnabled),
	}
	if settings.ReauthRequired && (settings.ClassesEnabled || settings.HomeworkEnabled) {
		lines = append(lines, "状态：已暂停，请发送“登录”；登录成功后会自动恢复。")
	}
	return strings.Join(lines, "\n")
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
		return h.commandError("日程查不到：", err)
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
	return withCalendarSubscriptionHint(strings.Join(lines, "\n"))
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
		return h.invalidInput("没找到教学班代码。把网页课表里的教学班代码粘过来，例如：\n订阅 导入 CONT5103P.01 CONT6104P.01")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	matches, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.BulkSubscribeSections(ctx, token, codes)
	})
	if err != nil {
		return h.commandError("订阅更新失败：", err)
	}
	sections := matchSections(matches)
	added := lifedata.FirstInt(matches, "addedCount")
	already := lifedata.FirstInt(matches, "alreadySubscribedCount")
	reply := formatBulkSubscriptionResult(matches, sections, nil, added, already)
	if len(sections) == 0 {
		h.markOutcome(CapabilityOutcomeNotFound)
	}
	return reply
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
	return withCalendarSubscriptionHint(strings.Join(lines, "\n"))
}

func (h Handler) curriculum(ctx context.Context, ident store.Identity, args []string) string {
	return h.curriculumAt(ctx, ident, args, chinaNow())
}

func (h Handler) curriculumAt(ctx context.Context, ident store.Identity, args []string, day time.Time) string {
	if firstArgIs(args, "help") {
		return strings.Join([]string{
			"课表用法：",
			"课表 / 课表 本周：查看本周（周日至周六）",
			"课表 下周",
			"课表 第3周",
			"课表 2026 秋季学期 / 课表 26秋：查看整学期",
			"课表 7.20周",
			"课表 6.23：查看该日期所在周",
			"课表 2022.05.03：查看该日期所在周",
			"今日课表 / 单日课表：只看今天",
			"下一节课",
		}, "\n")
	}
	target := "this-week"
	if hasArgs(args) {
		target = args[0]
	}
	day = day.In(lifedata.ChinaLocation())
	switch {
	case target == "this-week":
		return h.curriculumWeek(ctx, ident, weekStartSunday(day))
	case target == "next-week":
		return h.curriculumWeek(ctx, ident, weekStartSunday(day).AddDate(0, 0, 7))
	case strings.HasPrefix(target, "week-date:"):
		parsed, ok := parseScheduleDateToken(strings.TrimPrefix(target, "week-date:"), day)
		if !ok {
			return h.invalidInput("日期格式不太对。可以发：课表 7.20周")
		}
		return h.curriculumWeek(ctx, ident, weekStartSunday(parsed))
	case strings.HasPrefix(target, "week-number:"):
		week, err := strconv.Atoi(strings.TrimPrefix(target, "week-number:"))
		if err != nil || week < 1 {
			return h.invalidInput("周次格式不太对。可以发：课表 第3周")
		}
		start, err := h.academicWeekStart(ctx, week)
		if err != nil {
			return h.commandError("学期周次查不到：", err)
		}
		return h.curriculumWeek(ctx, ident, start)
	case strings.HasPrefix(target, "semester:"):
		return h.curriculumSemester(ctx, ident, target)
	}
	title := "今天 " + textutil.MonospaceDigits(day.Format("01-02")) + " 课表："
	if target == "tomorrow" {
		day = day.AddDate(0, 0, 1)
		title = "明天 " + textutil.MonospaceDigits(day.Format("01-02")) + " 课表："
	} else if strings.HasPrefix(target, "date:") {
		parsed, ok := parseScheduleDateToken(target, day)
		if !ok {
			return h.invalidInput("日期格式不太对。可以发：课表 6.23")
		}
		day = parsed
		title = textutil.MonospaceDigits(day.Format("01-02")) + " 课表："
	} else if target != "today" {
		return h.invalidInput("日期格式不太对。可以发：课表 6.23 或课表 2022.05.03")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	schedules, token, err := h.schedulesForDay(ctx, ident, token, day)
	if err != nil {
		return h.commandError("课表查不到：", err)
	}
	if len(schedules) == 0 {
		hasSubscriptions, subscriptionErr := h.hasSubscribedSections(ctx, ident, token)
		if subscriptionErr != nil {
			return h.failed("没有查到课程，但订阅状态校验失败，暂时无法确认当天是否真的没课。")
		}
		if !hasSubscriptions {
			return h.notFound("没有查到已关注的班级，无法确认当天是否有课。请先恢复或关注对应学期的课程。")
		}
		if target == "tomorrow" {
			return "明天没有课。"
		}
		if strings.HasPrefix(target, "date:") {
			return textutil.MonospaceDigits(day.Format("01-02")) + " 没有课。"
		}
		return "今天没有课。"
	}
	lines := []string{title}
	for _, schedule := range schedules {
		lines = append(lines, formatSchedule(schedule))
	}
	return strings.Join(lines, "\n")
}

func weekStartSunday(day time.Time) time.Time {
	day = day.In(lifedata.ChinaLocation())
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	return start.AddDate(0, 0, -int(start.Weekday()))
}

func (h Handler) academicWeekStart(ctx context.Context, week int) (time.Time, error) {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return time.Time{}, err
	}
	start, ok := lifedata.ParseAPITime(lifedata.FirstString(semester, "startDate"))
	if !ok {
		return time.Time{}, errors.New("当前学期缺少开始日期")
	}
	return weekStartSunday(start).AddDate(0, 0, (week-1)*7), nil
}

func (h Handler) curriculumWeek(ctx context.Context, ident store.Identity, start time.Time) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	start = weekStartSunday(start)
	end := start.AddDate(0, 0, 6)
	schedules, _, err := h.schedulesForRange(ctx, ident, token, start, end)
	if err != nil {
		return h.commandError("课表查不到：", err)
	}
	lines := []string{start.Format("01-02") + " 至 " + end.Format("01-02") + " 课表："}
	weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	for offset := 0; offset < 7; offset++ {
		if offset > 0 {
			lines = append(lines, "")
		}
		day := start.AddDate(0, 0, offset)
		daySchedules := lifedata.FilterSchedulesForDay(schedules, day)
		lifedata.SortSchedulesByStart(daySchedules)
		lines = append(lines, formatScheduleDay(weekdays[day.Weekday()]+" "+day.Format("01-02"), daySchedules)...)
	}
	return withCalendarSubscriptionHint(strings.Join(lines, "\n"))
}

type semesterScheduleEntry struct {
	day       int
	startTime string
	endTime   string
	course    string
	place     string
	weeks     map[int]bool
}

func (h Handler) curriculumSemester(ctx context.Context, ident store.Identity, target string) string {
	semester, err := h.matchScheduleSemester(ctx, target)
	if err != nil {
		return h.commandError("学期查不到：", err)
	}
	if semester == nil {
		return h.notFound("没有找到 " + scheduleSemesterTargetLabel(target) + "。可以发「学期 列表」查看可用学期。")
	}
	start, okStart := lifedata.ParseAPITime(lifedata.FirstString(semester, "startDate"))
	end, okEnd := lifedata.ParseAPITime(lifedata.FirstString(semester, "endDate"))
	if !okStart || !okEnd {
		return h.failed("该学期缺少起止日期，暂时无法生成整学期课表。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	schedules, _, err := h.schedulesForSemester(ctx, ident, token, semester, start, end)
	if err != nil {
		return h.commandError("课表查不到：", err)
	}
	entries := aggregateSemesterSchedules(schedules, start, end)
	name := lifedata.FirstString(semester, "nameCn", "namePrimary", "name", "code")
	if name == "" {
		name = scheduleSemesterTargetLabel(target)
	}
	if len(entries) == 0 {
		return h.notFound(name + "没有查到已关注课程。")
	}
	return withCalendarSubscriptionHint(formatSemesterSchedule(name, entries))
}

func (h Handler) matchScheduleSemester(ctx context.Context, target string) (map[string]any, error) {
	semesters, err := h.Life.ListSemesters(ctx, 1, 100)
	if err != nil {
		return nil, err
	}
	for _, semester := range semesters {
		for _, field := range []string{"nameCn", "namePrimary", "name", "code"} {
			candidate, ok := normalizeScheduleSemesterTarget(lifedata.FirstString(semester, field))
			if ok && candidate == target {
				return semester, nil
			}
		}
	}
	return nil, nil
}

func scheduleSemesterTargetLabel(target string) string {
	value := strings.TrimPrefix(target, "semester:")
	parts := strings.SplitN(value, "-", 2)
	if len(parts) != 2 {
		return "指定学期"
	}
	return parts[0] + "年" + parts[1] + "季学期"
}

func (h Handler) schedulesForSemester(ctx context.Context, ident store.Identity, token string, semester map[string]any, start, end time.Time) ([]map[string]any, string, error) {
	dateFrom, _ := lifedata.DayRFC3339Range(start)
	_, dateTo := lifedata.DayRFC3339Range(end)
	subscription, err := h.Life.CurrentSubscription(ctx, token)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		subscription, err = h.Life.CurrentSubscription(ctx, token)
	}
	if err != nil {
		return nil, token, err
	}
	sections := subscribedSectionsForSemester(subscription, semester)
	if len(sections) == 0 {
		return nil, token, nil
	}
	all, err := h.fetchSemesterSchedulesForSections(ctx, token, sections, dateFrom, dateTo)
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		all, err = h.fetchSemesterSchedulesForSections(ctx, token, sections, dateFrom, dateTo)
	}
	return all, token, err
}

func subscribedSectionsForSemester(subscription, semester map[string]any) []map[string]any {
	semesterID := lifedata.FirstString(semester, "id")
	semesterJwID := lifedata.FirstString(semester, "jwId")
	sections := make([]map[string]any, 0)
	for _, section := range lifedata.SubscriptionSections(subscription) {
		sectionSemester, _ := section["semester"].(map[string]any)
		if sectionSemester == nil {
			continue
		}
		matchesID := semesterID != "" && lifedata.FirstString(sectionSemester, "id") == semesterID
		matchesJwID := semesterJwID != "" && lifedata.FirstString(sectionSemester, "jwId") == semesterJwID
		if matchesID || matchesJwID {
			sections = append(sections, section)
		}
	}
	return sections
}

func (h Handler) fetchSemesterSchedulesForSections(ctx context.Context, token string, sections []map[string]any, dateFrom, dateTo string) ([]map[string]any, error) {
	var mu sync.Mutex
	var firstErr error
	all := make([]map[string]any, 0)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
scheduleLoop:
	for _, section := range sections {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break scheduleLoop
		}
		section := section
		sectionJwID := int64(lifedata.FirstInt(section, "jwId"))
		if sectionJwID <= 0 {
			<-sem
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			schedules, err := h.Life.ListSchedulesBySection(ctx, token, sectionJwID, dateFrom, dateTo)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for _, schedule := range schedules {
				if _, ok := schedule["section"].(map[string]any); !ok {
					schedule["section"] = section
				}
				all = append(all, schedule)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return all, ctx.Err()
}

func aggregateSemesterSchedules(schedules []map[string]any, semesterStart, semesterEnd time.Time) []semesterScheduleEntry {
	loc := lifedata.ChinaLocation()
	semesterStart = semesterStart.In(loc)
	semesterEnd = semesterEnd.In(loc)
	semesterStartDate := semesterStart.Format("2006-01-02")
	semesterEndDate := semesterEnd.Format("2006-01-02")
	weekOneStart := weekStartSunday(semesterStart)
	entries := make([]semesterScheduleEntry, 0)
	indexByKey := make(map[string]int)
	for _, schedule := range schedules {
		date, hasDate := lifedata.ParseAPITime(lifedata.FirstString(schedule, "date"))
		date = date.In(loc)
		dateValue := date.Format("2006-01-02")
		if hasDate && (dateValue < semesterStartDate || dateValue > semesterEndDate) {
			continue
		}
		course := lifedata.ScheduleCourseLabel(schedule)
		startTime := lifedata.FirstString(schedule, "startTime")
		endTime := lifedata.FirstString(schedule, "endTime")
		if course == "" || startTime == "" || endTime == "" {
			continue
		}
		sectionKey := lifedata.NestedString(schedule, "section", "id", "jwId", "code")
		if sectionKey == "" {
			sectionKey = course
		}
		place := strings.TrimSpace(lifedata.SchedulePlaceLabel(schedule))
		day := -1
		if hasDate {
			day = int(date.Weekday())
		} else if weekday := lifedata.FirstInt(schedule, "weekday"); weekday >= 1 && weekday <= 7 {
			day = weekday % 7
		}
		if day < 0 {
			continue
		}
		key := strings.Join([]string{strconv.Itoa(day), startTime, endTime, sectionKey, course, place}, "\x00")
		index, exists := indexByKey[key]
		if !exists {
			index = len(entries)
			indexByKey[key] = index
			entries = append(entries, semesterScheduleEntry{
				day: day, startTime: startTime, endTime: endTime,
				course: course, place: place, weeks: make(map[int]bool),
			})
		}
		week := lifedata.FirstInt(schedule, "weekIndex")
		if week <= 0 && hasDate {
			week = int(date.Sub(weekOneStart).Hours()/24)/7 + 1
		}
		if week > 0 {
			entries[index].weeks[week] = true
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].day != entries[j].day {
			return entries[i].day < entries[j].day
		}
		if entries[i].startTime != entries[j].startTime {
			return entries[i].startTime < entries[j].startTime
		}
		return entries[i].course < entries[j].course
	})
	return entries
}

func formatSemesterSchedule(name string, entries []semesterScheduleEntry) string {
	lines := []string{strings.TrimSpace(name) + "课表："}
	entryIndex := 0
	for day := 0; day < len(weeklyScheduleDayLabels); day++ {
		if day > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, weeklyScheduleDayLabels[day]+"：")
		startIndex := entryIndex
		for entryIndex < len(entries) && entries[entryIndex].day == day {
			entry := entries[entryIndex]
			cells := []string{
				textutil.MonospaceASCII(entry.place),
				textutil.MonospaceDigits(strings.TrimSpace(entry.startTime + "-" + entry.endTime)),
				entry.course,
				textutil.MonospaceDigits(formatTeachingWeeks(entry.weeks)),
			}
			lines = append(lines, strings.Join(cells, "\t"))
			entryIndex++
		}
		if entryIndex == startIndex {
			lines = append(lines, "没有课。")
		}
	}
	return strings.Join(lines, "\n")
}

func formatTeachingWeeks(weeks map[int]bool) string {
	values := make([]int, 0, len(weeks))
	for week := range weeks {
		values = append(values, week)
	}
	sort.Ints(values)
	if len(values) == 0 {
		return ""
	}
	ranges := make([]string, 0)
	for start := 0; start < len(values); {
		end := start
		for end+1 < len(values) && values[end+1] == values[end]+1 {
			end++
		}
		if start == end {
			ranges = append(ranges, strconv.Itoa(values[start]))
		} else {
			ranges = append(ranges, strconv.Itoa(values[start])+"-"+strconv.Itoa(values[end]))
		}
		start = end + 1
	}
	return strings.Join(ranges, "、") + " 周"
}

func (h Handler) hasSubscribedSections(ctx context.Context, ident store.Identity, token string) (bool, error) {
	subscription, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return false, err
	}
	return len(lifedata.SubscriptionSectionIDs(subscription)) > 0, nil
}

func formatScheduleDay(title string, schedules []map[string]any) []string {
	lines := []string{title + "："}
	if len(schedules) == 0 {
		return append(lines, "没有课。")
	}
	for _, schedule := range schedules {
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
			return h.commandError("下一节课查不到：", err)
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
	return h.notFound("接下来一周没查到课。")
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

func (h Handler) schedulesForRange(ctx context.Context, ident store.Identity, token string, start, end time.Time) ([]map[string]any, string, error) {
	dateFrom, _ := lifedata.DayRFC3339Range(start)
	_, dateTo := lifedata.DayRFC3339Range(end)
	all, err := h.Life.SubscribedSchedules(ctx, token, life.SubscribedScheduleQuery(dateFrom, dateTo))
	if refreshed, ok := h.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		all, err = h.Life.SubscribedSchedules(ctx, token, life.SubscribedScheduleQuery(dateFrom, dateTo))
	}
	if err == nil {
		return all, token, nil
	}
	if !subscribedSchedulesFallbackError(err) {
		return nil, token, err
	}
	all = nil
	for day := weekStartSunday(start); !day.After(end); day = day.AddDate(0, 0, 1) {
		daySchedules, refreshed, fetchErr := h.schedulesForDayBySections(ctx, ident, token, day)
		token = refreshed
		if fetchErr != nil {
			return nil, token, fetchErr
		}
		all = append(all, daySchedules...)
	}
	return all, token, nil
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
	place := strings.TrimSpace(lifedata.SchedulePlaceLabel(schedule))
	if place == "" && timeRange == "" && course == "" {
		return textutil.MonospaceDigits(lifedata.ScheduleFallbackLabel(schedule))
	}
	cells := []string{
		textutil.MonospaceASCII(place),
		textutil.MonospaceDigits(timeRange),
		course,
	}
	return strings.TrimRight(strings.Join(cells, "\t"), "\t")
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
	h.markOutcome(CapabilityOutcomeAuthRequired)
	return "需要先登录。发送：登录"
}

func (h Handler) invalidInput(text string) string {
	h.markOutcome(CapabilityOutcomeInvalidInput)
	return text
}

func (h Handler) notFound(text string) string {
	h.markOutcome(CapabilityOutcomeNotFound)
	return text
}

func (h Handler) forbidden(text string) string {
	h.markOutcome(CapabilityOutcomeForbidden)
	return text
}

func (h Handler) failed(text string) string {
	h.markOutcome(CapabilityOutcomeFailed)
	return text
}

func (h Handler) commandError(prefix string, err error) string {
	if life.IsUnauthorized(err) || errors.Is(err, auth.ErrNotLoggedIn) || errors.Is(err, auth.ErrReauthorizationRequired) {
		h.markOutcome(CapabilityOutcomeAuthRequired)
	} else if h.execution != nil && h.execution.effect != EffectRead && mutationResultUnknown(err) {
		h.markOutcome(CapabilityOutcomeUnknown)
		return prefix + "网络响应中断，无法确认操作是否完成；系统不会自动重试。"
	} else {
		h.markOutcome(CapabilityOutcomeFailed)
	}
	return commandError(prefix, err)
}

func (h Handler) recordState(ctx context.Context, ident store.Identity, cmd Invocation) {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return
	}
	if err := h.Store.RecordConversationState(ctx, ident, cmd.Name, strconv.Quote(cmd.Raw)); err != nil {
		h.logf("record conversation state failed: %v", err)
	}
}

func (h Handler) recordInteraction(ctx context.Context, ident store.Identity, cmd Invocation, reply string) {
	h.recordInteractionWithOutcome(ctx, ident, cmd, reply, CapabilityOutcomeSuccess)
}

func (h Handler) recordInteractionWithOutcome(ctx context.Context, ident store.Identity, cmd Invocation, reply string, outcome CapabilityOutcomeStatus) {
	if h.Store == nil || !store.HasConversationIdentity(ident) {
		return
	}
	status := store.InteractionStatusHandled
	if outcome == CapabilityOutcomeAuthRequired {
		status = store.InteractionStatusWaitingAuth
	}
	if err := h.Store.RecordInteraction(ctx, ident, store.Interaction{
		RawText: cmd.Raw,
		Command: cmd.Name,
		Args:    joinedArgs(cmd.Args),
		Handled: true,
		Reply:   interactionReply(cmd, reply),
		Status:  status,
	}); err != nil {
		h.logf("record command interaction failed: %v", err)
	}
}

func interactionReply(cmd Invocation, reply string) string {
	if cmd.Name == "subscription" && firstArgIs(cmd.Args, "link") && strings.HasPrefix(reply, "日历订阅链接：\n") {
		return "[私有日历订阅链接已发送]"
	}
	return reply
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
		return h.commandError("学期查不到：", err)
	}
	if len(semesters) == 0 {
		return h.notFound("没有学期数据。")
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
		return h.invalidInput("请输入搜索条件，例如：课程搜索 数学分析")
	}
	courses, err := h.Life.SearchCoursesWithFilters(ctx, opts)
	if err != nil {
		return h.commandError("课程查不到：", err)
	}
	if len(courses) == 0 {
		return h.notFound("没找到课程。")
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
		return h.invalidInput("请输入搜索条件，例如：教学班搜索 高等数学")
	}
	sections, err := h.Life.SearchSectionsWithFilters(ctx, opts)
	if err != nil {
		return h.commandError("教学班查不到：", err)
	}
	if len(sections) == 0 {
		return h.notFound("没找到教学班。")
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
		return h.invalidInput("请输入搜索条件，例如：老师搜索 张")
	}
	teachers, err := h.Life.SearchTeachersWithFilters(ctx, opts)
	if err != nil {
		return h.commandError("老师查不到：", err)
	}
	if len(teachers) == 0 {
		return h.notFound("没找到老师。")
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
		return h.invalidInput("需要提供课程 JW ID。")
	}
	course, err := h.Life.GetCourseByJwID(ctx, jwId)
	if err != nil {
		return h.commandError("课程查不到：", err)
	}
	return "课程：\n" + formatCourse(course)
}

func (h Handler) getSectionByJwID(ctx context.Context, raw string) string {
	jwId, ok := parseIntArg(raw)
	if !ok {
		return h.invalidInput("需要提供教学班 JW ID。")
	}
	section, err := h.Life.GetSectionByJwID(ctx, jwId)
	if err != nil {
		return h.commandError("教学班查不到：", err)
	}
	return "教学班：\n" + formatSection(section)
}

func (h Handler) getTeacherByID(ctx context.Context, raw string) string {
	id, ok := parseIntArg(raw)
	if !ok {
		return h.invalidInput("需要提供老师 ID。")
	}
	teacher, err := h.Life.GetTeacherByID(ctx, id)
	if err != nil {
		return h.commandError("老师查不到：", err)
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
			return h.commandError("校车路线查不到：", err)
		}
		if opts.From != "" {
			id, ok := campusIDByName(data, opts.From)
			if !ok {
				return h.notFound("没找到出发校区：" + opts.From)
			}
			originID = int64(id)
		}
		if opts.To != "" {
			id, ok := campusIDByName(data, opts.To)
			if !ok {
				return h.notFound("没找到到达校区：" + opts.To)
			}
			destID = int64(id)
		}
	}
	routes, err := h.Life.ListBusRoutes(ctx, originID, destID)
	if err != nil {
		return h.commandError("校车路线查不到：", err)
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
		return h.invalidInput("需要提供教学班 JW ID。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	_, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.UnsubscribeSectionByJwID(ctx, token, jwId)
	})
	if err != nil {
		return h.commandError("退订失败：", err)
	}
	return "已退订教学班。"
}

func (h Handler) mySubscribedSections(ctx context.Context, ident store.Identity) string {
	return h.subscriptionList(ctx, ident)
}

func (h Handler) sectionSchedules(ctx context.Context, ident store.Identity, args []string) string {
	if len(args) < 3 {
		return h.invalidInput("用法：教学班课表 <JW ID> <开始日期> <结束日期>")
	}
	jwId, ok := parseIntArg(args[0])
	if !ok {
		return h.invalidInput("JW ID 无效。")
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
		return h.commandError("课表查不到：", err)
	}
	if len(schedules) == 0 {
		return "该时间段没有课。"
	}
	lifedata.SortSchedulesByStart(schedules)
	lines := []string{"教学班课表："}
	for _, schedule := range schedules {
		lines = append(lines, formatSchedule(schedule))
	}
	return withCalendarSubscriptionHint(strings.Join(lines, "\n"))
}

func (h Handler) sectionExams(ctx context.Context, ident store.Identity, args []string) string {
	listArgs, requestedPage, err := extractListPage(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	jwId, ok := parseIntArg(joinedArgs(listArgs))
	if !ok {
		return h.invalidInput("需要提供教学班 JW ID。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	section, err := h.Life.GetSectionByJwID(ctx, jwId)
	if err != nil {
		return h.commandError("教学班查不到：", err)
	}
	exams, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.ListExamsBySection(ctx, token, jwId)
	})
	if err != nil {
		return h.commandError("考试查不到：", err)
	}
	if len(exams) == 0 {
		return "该教学班没有考试。"
	}
	wrapped := make([]subscriptionExam, len(exams))
	for i, exam := range exams {
		wrapped[i] = subscriptionExam{exam: exam, section: section}
	}
	sortSubscriptionExams(wrapped)
	command := func(page int) string {
		return fmt.Sprintf("教学班 考试 %d 第%d页", jwId, page)
	}
	reply, ok := formatListPage("考试：", len(wrapped), requestedPage, command, func(i int) string {
		return formatNumberedLine(i+1, formatExam(wrapped[i]))
	})
	if !ok {
		return h.invalidInput(listPageOutOfRange("考试", len(wrapped), command))
	}
	return reply
}

func (h Handler) sectionHomeworks(ctx context.Context, ident store.Identity, args []string) string {
	listArgs, requestedPage, err := extractListPage(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	jwId, ok := parseIntArg(joinedArgs(listArgs))
	if !ok {
		return h.invalidInput("需要提供教学班 JW ID。")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	homeworks, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return h.Life.ListHomeworksBySection(ctx, token, jwId)
	})
	if err != nil {
		return h.commandError("作业查不到：", err)
	}
	lifedata.SortHomeworksByDue(homeworks)
	if len(homeworks) == 0 {
		return "该教学班没有作业。"
	}
	command := func(page int) string {
		return fmt.Sprintf("教学班 作业 %d 第%d页", jwId, page)
	}
	reply, ok := formatListPage("作业：", len(homeworks), requestedPage, command, func(i int) string {
		return formatNumberedLine(i+1, formatHomework(homeworks[i]))
	})
	if !ok {
		return h.invalidInput(listPageOutOfRange("作业", len(homeworks), command))
	}
	return reply
}

func dashboardItemSlice(data map[string]any, key string) []map[string]any {
	container, _ := data[key].(map[string]any)
	return lifedata.MapSlice(container["items"])
}

func dashboardItemTotal(data map[string]any, key string, itemCount int) int {
	container, _ := data[key].(map[string]any)
	total := lifedata.FirstInt(container, "total")
	if total < itemCount {
		return itemCount
	}
	return total
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
		return h.commandError("概览查不到：", err)
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
		return h.commandError("近期截止查不到：", err)
	}
	return formatDashboard(data, fmt.Sprintf("未来 %d 天截止", dayLimit))
}

func formatDashboard(data map[string]any, title string) string {
	lines := []string{title + "："}
	dueTodos := dashboardItemSlice(data, "dueTodos")
	dueTodoTotal := dashboardItemTotal(data, "dueTodos", len(dueTodos))
	if dueTodoTotal > 0 {
		lines = append(lines, "", fmt.Sprintf("待办 (%d)：", dueTodoTotal))
		for i, todo := range dueTodos[:min(len(dueTodos), summaryDisplayLimit)] {
			lines = append(lines, formatNumberedLine(i+1, formatTodo(todo)))
		}
		lines = appendListOverflow(lines, dueTodoTotal, min(len(dueTodos), summaryDisplayLimit), "待办")
	}
	homeworks := dashboardItemSlice(data, "homeworks")
	homeworkTotal := dashboardItemTotal(data, "homeworks", len(homeworks))
	if homeworkTotal > 0 {
		lines = append(lines, "", fmt.Sprintf("作业 (%d)：", homeworkTotal))
		for i, homework := range homeworks[:min(len(homeworks), summaryDisplayLimit)] {
			lines = append(lines, formatNumberedLine(i+1, formatHomework(homework)))
		}
		lines = appendListOverflow(lines, homeworkTotal, min(len(homeworks), summaryDisplayLimit), "作业")
	}
	exams := dashboardItemSlice(data, "exams")
	examTotal := dashboardItemTotal(data, "exams", len(exams))
	if examTotal > 0 {
		lines = append(lines, "", fmt.Sprintf("考试 (%d)：", examTotal))
		for i, exam := range exams[:min(len(exams), summaryDisplayLimit)] {
			sectionMap, _ := exam["section"].(map[string]any)
			lines = append(lines, formatNumberedLine(i+1, formatExam(subscriptionExam{exam: exam, section: sectionMap})))
		}
		lines = appendListOverflow(lines, examTotal, min(len(exams), summaryDisplayLimit), "考试")
	}
	if len(lines) == 1 {
		return title + "\n暂无近期截止。"
	}
	return strings.Join(lines, "\n")
}

func appendListOverflow(lines []string, total, shown int, command string) []string {
	if total <= shown {
		return lines
	}
	line := fmt.Sprintf("另有 %d 条，发送「%s」查看完整列表。", total-shown, command)
	return append(lines, textutil.MonospaceDigits(line))
}

func (h Handler) currentSemester(ctx context.Context) string {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return h.commandError("学期查不到：", err)
	}
	name := lifedata.FirstString(semester, "name", "nameCn", "namePrimary")
	if name == "" {
		name = lifedata.FirstString(semester, "id")
	}
	if name == "" {
		return h.notFound("当前学期未知。")
	}
	return "当前学期：" + name
}

func (h Handler) searchCourses(ctx context.Context, keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return h.invalidInput("想查哪门课？例如：课程 数学分析")
	}
	courses, err := h.Life.SearchCourses(ctx, keyword, 5)
	if err != nil {
		return h.commandError("课程查不到：", err)
	}
	if len(courses) == 0 {
		return h.notFound("没找到课程。")
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
		return h.invalidInput("想查哪个教学班？例如：教学班 高等数学")
	}
	sections, err := h.Life.SearchSections(ctx, keyword, 5)
	if err != nil {
		return h.commandError("教学班查不到：", err)
	}
	if len(sections) == 0 {
		return h.notFound("没找到教学班。")
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
		return h.invalidInput("想查哪位老师？例如：老师 张")
	}
	teachers, err := h.Life.SearchTeachers(ctx, keyword, 5)
	if err != nil {
		return h.commandError("老师查不到：", err)
	}
	if len(teachers) == 0 {
		return h.notFound("没找到老师。")
	}
	lines := []string{"老师："}
	for _, teacher := range teachers {
		lines = append(lines, formatTeacher(teacher))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) exams(ctx context.Context, ident store.Identity, args []string) string {
	listArgs, requestedPage, pageErr := extractListPage(args)
	if pageErr != nil {
		return h.invalidInput(pageErr.Error())
	}
	if len(listArgs) > 0 {
		return h.invalidInput("考试分页用法：考试 第2页")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return h.commandError("考试查不到：", err)
	}
	exams := subscriptionExams(data)
	if len(exams) == 0 {
		return "没有订阅课程考试。"
	}
	sortSubscriptionExams(exams)
	command := func(page int) string {
		return fmt.Sprintf("考试 第%d页", page)
	}
	reply, ok := formatListPage("考试：", len(exams), requestedPage, command, func(i int) string {
		return formatNumberedLine(i+1, formatExam(exams[i]))
	})
	if !ok {
		return h.invalidInput(listPageOutOfRange("考试", len(exams), command))
	}
	return reply
}

func (h Handler) status(ctx context.Context) string {
	api := "OK"
	if err := h.Life.Health(ctx); err != nil {
		h.markOutcome(CapabilityOutcomeFailed)
		api = friendlyError(err)
	}
	return strings.Join([]string{
		"状态：",
		"Life @ USTC：" + api,
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
	cells := []string{date, timeRange, course, sectionCode, mode, rooms}
	if strings.Trim(strings.Join(cells, ""), " \t") == "" {
		return lifedata.FirstString(item.exam, "id")
	}
	return textutil.MonospaceASCII(textutil.MonospaceDigits(strings.TrimRight(strings.Join(cells, "\t"), "\t")))
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
	if prefix, ok := sharedRoomPrefix(labels); ok {
		suffixes := make([]string, len(labels))
		for i, label := range labels {
			suffixes[i] = label[len(prefix):]
		}
		return prefix + strings.Join(suffixes, "、")
	}
	return strings.Join(labels, "、")
}

// sharedRoomPrefix returns the common building prefix of multi-room labels
// like "东区 五教 5102" / "东区 五教 5103" so the join can drop the repeated
// prefix ("东区 五教 5102、5103") instead of overflowing the card column.
func sharedRoomPrefix(labels []string) (string, bool) {
	if len(labels) < 2 {
		return "", false
	}
	prefix := ""
	for i, label := range labels {
		idx := strings.LastIndex(label, " ")
		if idx < 0 || strings.TrimSpace(label[idx+1:]) == "" {
			return "", false
		}
		if i == 0 {
			prefix = label[:idx+1]
		} else if label[:idx+1] != prefix {
			return "", false
		}
	}
	return prefix, prefix != ""
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
const fullListPageSize = 30
const summaryDisplayLimit = 8

type listPage struct {
	number int
	total  int
	start  int
	end    int
}

func extractListPage(args []string) ([]string, int, error) {
	remaining := make([]string, 0, len(args))
	page := 1
	found := false
	for i := 0; i < len(args); i++ {
		token := normToken(args[i])
		rawPage := ""
		switch {
		case token == "page" || token == "页":
			if i+1 >= len(args) {
				return nil, page, errors.New("缺少页码。例如：第2页")
			}
			i++
			rawPage = args[i]
		case strings.HasPrefix(token, "第") && strings.HasSuffix(token, "页"):
			rawPage = strings.TrimSuffix(strings.TrimPrefix(token, "第"), "页")
		case strings.HasSuffix(token, "页"):
			rawPage = strings.TrimSuffix(token, "页")
		default:
			remaining = append(remaining, args[i])
			continue
		}
		if found {
			return nil, page, errors.New("一次只能指定一个页码。")
		}
		parsed, err := strconv.Atoi(textutil.PlainDigits(strings.TrimSpace(rawPage)))
		if err != nil || parsed < 1 {
			return nil, page, errors.New("页码必须是大于 0 的整数。例如：第2页")
		}
		page = parsed
		found = true
	}
	return remaining, page, nil
}

func listPageFor(itemCount, requestedPage int) (listPage, bool) {
	total := (itemCount + fullListPageSize - 1) / fullListPageSize
	if total < 1 {
		total = 1
	}
	if requestedPage < 1 || requestedPage > total {
		return listPage{number: requestedPage, total: total}, false
	}
	start := (requestedPage - 1) * fullListPageSize
	end := min(start+fullListPageSize, itemCount)
	return listPage{number: requestedPage, total: total, start: start, end: end}, true
}

func formatListPage(title string, itemCount, requestedPage int, command func(int) string, line func(int) string) (string, bool) {
	page, ok := listPageFor(itemCount, requestedPage)
	if !ok {
		return "", false
	}
	lines := []string{title}
	for i := page.start; i < page.end; i++ {
		lines = append(lines, line(i))
	}
	if navigation := pageNavigationLine(page, command); navigation != "" {
		lines = append(lines, navigation)
	}
	return strings.Join(lines, "\n"), true
}

func pageNavigationLine(page listPage, command func(int) string) string {
	if page.total <= 1 {
		return ""
	}
	parts := []string{fmt.Sprintf("第 %d/%d 页", page.number, page.total)}
	if page.number > 1 {
		parts = append(parts, "上一页：发送「"+command(page.number-1)+"」")
	}
	if page.number < page.total {
		parts = append(parts, "下一页：发送「"+command(page.number+1)+"」")
	}
	return textutil.MonospaceDigits(strings.Join(parts, " · "))
}

func listPageOutOfRange(label string, itemCount int, command func(int) string) string {
	page, _ := listPageFor(itemCount, 1)
	return fmt.Sprintf("%s只有 %d 页。发送「%s」查看最后一页。", label, page.total, command(page.total))
}

func chinaNow() time.Time {
	return time.Now().In(lifedata.ChinaLocation())
}

func friendlyError(err error) string {
	if err == nil {
		return "未知错误"
	}
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
	var httpErr life.HTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("服务返回错误（HTTP %d）", httpErr.StatusCode)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "网络请求失败，等会儿再试"
	}
	return text
}

func mutationResultUnknown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var httpErr life.HTTPError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode >= http.StatusInternalServerError)
}

func commandError(prefix string, err error) string {
	return prefix + friendlyError(err)
}
