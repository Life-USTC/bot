package commands

import (
	"context"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// CapabilityID is the stable identifier used by the command, agent, audit and
// confirmation paths. It is deliberately independent from the user-facing
// command forms in a descriptor.
type CapabilityID string

const (
	CapabilityHelp                     CapabilityID = "help"
	CapabilityLogin                    CapabilityID = "login"
	CapabilityLogout                   CapabilityID = "logout"
	CapabilityAccount                  CapabilityID = "account"
	CapabilityTodo                     CapabilityID = "todo"
	CapabilityHomework                 CapabilityID = "homework"
	CapabilityCalendar                 CapabilityID = "calendar"
	CapabilitySubscription             CapabilityID = "subscription"
	CapabilityNotify                   CapabilityID = "notify"
	CapabilitySettings                 CapabilityID = "settings"
	CapabilityFeedback                 CapabilityID = "feedback"
	CapabilityPing                     CapabilityID = "ping"
	CapabilityStatus                   CapabilityID = "status"
	CapabilitySemester                 CapabilityID = "semester"
	CapabilityCourse                   CapabilityID = "course"
	CapabilitySection                  CapabilityID = "section"
	CapabilityTeacher                  CapabilityID = "teacher"
	CapabilityBus                      CapabilityID = "bus"
	CapabilitySchedule                 CapabilityID = "schedule"
	CapabilityNextClass                CapabilityID = "nextclass"
	CapabilityExam                     CapabilityID = "exam"
	CapabilityListSemesters            CapabilityID = "list_semesters"
	CapabilityCourseSearch             CapabilityID = "course_search"
	CapabilitySectionSearch            CapabilityID = "section_search"
	CapabilityTeacherSearch            CapabilityID = "teacher_search"
	CapabilityCourseByJWID             CapabilityID = "course_by_jw_id"
	CapabilitySectionByJWID            CapabilityID = "section_by_jw_id"
	CapabilityTeacherByID              CapabilityID = "teacher_by_id"
	CapabilityBusRoutes                CapabilityID = "bus_routes"
	CapabilityUnsubscribeSectionByJWID CapabilityID = "unsubscribe_section_by_jw_id"
	CapabilityMySubscribedSections     CapabilityID = "my_subscribed_sections"
	CapabilitySectionSchedules         CapabilityID = "section_schedules"
	CapabilitySectionExams             CapabilityID = "section_exams"
	CapabilitySectionHomeworks         CapabilityID = "section_homeworks"
	CapabilityOverview                 CapabilityID = "overview"
	CapabilityUpcomingDeadlines        CapabilityID = "upcoming_deadlines"
	CapabilityWeather                  CapabilityID = "weather"
)

// CapabilityEffect describes the state transition allowed by an invocation.
type CapabilityEffect string

const (
	EffectRead        CapabilityEffect = "read"
	EffectWrite       CapabilityEffect = "write"
	EffectDestructive CapabilityEffect = "destructive"
)

// DataScope is the privacy boundary of a capability result. Read-only user
// data is still private; only Public capabilities may execute in a shared
// conversation.
type DataScope string

const (
	DataScopePublic      DataScope = "public"
	DataScopeUserPrivate DataScope = "user_private"
)

// ResultExposure controls what an agent may receive from a host command.
type ResultExposure string

const (
	ExposureModel    ResultExposure = "model"
	ExposureHostOnly ResultExposure = "host_only"
)

// ConfirmationPolicy makes confirmation an explicit descriptor policy instead
// of a command-name switch. Login is a write but intentionally starts its
// resumable OAuth flow without a confirmation; all other write policies opt in.
type ConfirmationPolicy string

const (
	ConfirmNever ConfirmationPolicy = "never"
	ConfirmUser  ConfirmationPolicy = "user"
)

// CapabilityRequirements contains host dependencies and the audience gate for
// a capability. PublicCache is a host execution optimization, not a policy
// decision, but lives here so the descriptor remains the sole command source.
type CapabilityRequirements struct {
	Life        bool      `json:"life,omitempty"`
	Store       bool      `json:"store,omitempty"`
	OAuth       bool      `json:"oauth,omitempty"`
	DataScope   DataScope `json:"dataScope"`
	PublicCache bool      `json:"publicCache,omitempty"`
}

// CapabilityPolicy is the invocation-level policy. Descriptors provide the
// default values and may resolve argument-sensitive values with ResolvePolicy.
type CapabilityPolicy struct {
	Effect       CapabilityEffect   `json:"effect"`
	DataScope    DataScope          `json:"dataScope"`
	Exposure     ResultExposure     `json:"exposure"`
	Confirmation ConfirmationPolicy `json:"confirmation"`
}

// HelpMetadata is the complete usage contract owned by a descriptor. Overview
// marks one descriptor per visible top-level family; Shortcuts are rendered in
// the dedicated shortcut topic. Each example also carries its normalized
// capability and arguments for non-text integrations.
type HelpMetadata struct {
	Topic     string
	Title     string
	Summary   string
	Overview  bool
	Examples  []HelpExample
	Shortcuts []HelpExample
}

// Invocation is the normalized result of parsing one command. Name is the
// canonical ID string used by durable conversation/audit storage; Capability
// points at the descriptor that owns all requirements and execution policy.
type Invocation struct {
	Capability   *CapabilityDescriptor `json:"-"`
	Name         string                `json:"name"`
	Args         []string              `json:"args"`
	Raw          string                `json:"raw,omitempty"`
	NaturalRoute string                `json:"naturalRoute,omitempty"`
}

func (i Invocation) Descriptor() *CapabilityDescriptor { return i.Capability }

func (i Invocation) ID() CapabilityID {
	if i.Capability != nil {
		return i.Capability.ID
	}
	return CapabilityID(i.Name)
}

func (i Invocation) CanonicalCommand() string {
	parts := make([]string, 0, len(i.Args)+1)
	parts = append(parts, string(i.ID()))
	parts = append(parts, i.Args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// CapabilityInputPolicy validates normalized arguments. A nil policy is not
// valid in the registry; allowArgs is used explicitly for open search inputs.
type CapabilityInputPolicy func([]string) bool

// CapabilityNormalizer translates supported forms to the executor's stable
// argument vocabulary.
type CapabilityNormalizer func([]string) []string

// CapabilityExecutor runs one normalized invocation and returns the typed
// host/domain outcome. Response inside the outcome remains the actual domain
// response and is intentionally separate from the runtime status.
type CapabilityExecutor func(Handler, context.Context, store.Identity, Invocation) CapabilityOutcome

// CapabilityPresenter maps a host response to the agent-facing result. It is
// called after the descriptor policy has been resolved.
type CapabilityPresenter func(Invocation, Response, CapabilityPolicy) CapabilityPresentation

// CapabilityPresentation is the result-exposure decision for a host invocation.
type CapabilityPresentation struct {
	Text            string
	DeliveredByHost bool
	Response        Response
}

// CapabilityDescriptor is the single declarative capability contract. Forms,
// input validation, normalization, execution, presentation, requirements,
// effects, exposure and help all live here.
type CapabilityDescriptor struct {
	ID            CapabilityID
	Forms         []string
	Requirements  CapabilityRequirements
	Effect        CapabilityEffect
	Exposure      ResultExposure
	Input         CapabilityInputPolicy
	Normalize     CapabilityNormalizer
	Execute       CapabilityExecutor
	Present       CapabilityPresenter
	ResolvePolicy func(Invocation) CapabilityPolicy
	AutoLogin     bool
	Help          HelpMetadata
}

func (d CapabilityDescriptor) Accepts(args []string) bool {
	return d.Input != nil && d.Input(args)
}

func (d CapabilityDescriptor) PolicyFor(inv Invocation) CapabilityPolicy {
	policy := CapabilityPolicy{
		Effect:       d.Effect,
		DataScope:    d.Requirements.DataScope,
		Exposure:     d.Exposure,
		Confirmation: ConfirmNever,
	}
	if d.ResolvePolicy != nil {
		policy = d.ResolvePolicy(inv)
	}
	return policy
}

func (i Invocation) Policy() CapabilityPolicy {
	inv, ok := withDescriptor(i)
	if !ok {
		return CapabilityPolicy{}
	}
	return inv.Capability.PolicyFor(inv)
}

func withDescriptor(inv Invocation) (Invocation, bool) {
	if inv.Capability != nil {
		return inv, true
	}
	descriptor, ok := descriptorForID(inv.Name)
	if !ok {
		return Invocation{}, false
	}
	inv.Capability = descriptor
	return inv, true
}

// RestoreInvocation reconstructs the exact normalized invocation persisted by
// the router. It intentionally does not validate or renormalize arguments;
// Handler owns the final validity and policy checks at execution time.
func RestoreInvocation(id CapabilityID, args []string) (Invocation, bool) {
	descriptor, ok := descriptorForID(string(id))
	if !ok {
		return Invocation{}, false
	}
	return Invocation{
		Capability: descriptor,
		Name:       string(descriptor.ID),
		Args:       append([]string(nil), args...),
	}, true
}

func allowArgs([]string) bool { return true }

func textExecutor(run func(Handler, context.Context, store.Identity, []string) string) CapabilityExecutor {
	return func(h Handler, ctx context.Context, ident store.Identity, inv Invocation) CapabilityOutcome {
		if h.execution == nil {
			h.execution = &capabilityExecutionState{}
		}
		return outcomeFromResponse(h, Response{Text: run(h, ctx, ident, inv.Args), Kind: inv.Name})
	}
}

func defaultCapabilityPresenter(inv Invocation, response Response, policy CapabilityPolicy) CapabilityPresentation {
	presentation := CapabilityPresentation{Response: response, Text: response.Text}
	switch policy.Exposure {
	case ExposureHostOnly:
		presentation.Text = ""
		presentation.DeliveredByHost = true
	}
	if response.Image != nil || len(response.Parts) > 0 {
		presentation.DeliveredByHost = true
	}
	return presentation
}

func policyFor(inv Invocation, effect CapabilityEffect, scope DataScope, exposure ResultExposure, confirmation ConfirmationPolicy) CapabilityPolicy {
	return CapabilityPolicy{Effect: effect, DataScope: scope, Exposure: exposure, Confirmation: confirmation}
}

func readPolicy(inv Invocation, scope DataScope) CapabilityPolicy {
	return policyFor(inv, EffectRead, scope, ExposureModel, ConfirmNever)
}

func privateWritePolicy(inv Invocation, effect CapabilityEffect) CapabilityPolicy {
	return policyFor(inv, effect, DataScopeUserPrivate, ExposureModel, ConfirmUser)
}

func helpPolicy(inv Invocation) CapabilityPolicy {
	return readPolicy(inv, DataScopePublic)
}

func loginPolicy(inv Invocation) CapabilityPolicy {
	return policyFor(inv, EffectWrite, DataScopeUserPrivate, ExposureHostOnly, ConfirmNever)
}

func subscriptionPolicy(inv Invocation) CapabilityPolicy {
	if firstArgIs(inv.Args, "link") {
		// Private conversations may expose the user's own calendar URL to the
		// model. The URL is already stored in private state; group policy still
		// prevents this capability from being invoked on a shared surface.
		return policyFor(inv, EffectRead, DataScopeUserPrivate, ExposureModel, ConfirmNever)
	}
	if firstArgIs(inv.Args, "import") {
		return privateWritePolicy(inv, EffectWrite)
	}
	return readPolicy(inv, DataScopeUserPrivate)
}

func todoPolicy(inv Invocation) CapabilityPolicy {
	if !todoMutationArgs(inv.Args) {
		return readPolicy(inv, DataScopeUserPrivate)
	}
	return privateWritePolicy(inv, todoMutationEffect(inv.Args))
}

func homeworkPolicy(inv Invocation) CapabilityPolicy {
	if firstArgIn(inv.Args, "done", "undo") {
		return privateWritePolicy(inv, EffectWrite)
	}
	return readPolicy(inv, DataScopeUserPrivate)
}

func notifyPolicy(inv Invocation) CapabilityPolicy {
	if len(inv.Args) >= 2 && firstArgIn(inv.Args[1:], "on", "off") {
		return privateWritePolicy(inv, EffectWrite)
	}
	return readPolicy(inv, DataScopeUserPrivate)
}

func feedbackPolicy(inv Invocation) CapabilityPolicy {
	if hasArgs(inv.Args) && !firstArgIs(inv.Args, "help") {
		return policyFor(inv, EffectWrite, DataScopePublic, ExposureModel, ConfirmUser)
	}
	return readPolicy(inv, DataScopePublic)
}

func busPolicy(inv Invocation) CapabilityPolicy {
	if busPreferenceMutationArgs(inv.Args) {
		return privateWritePolicy(inv, EffectWrite)
	}
	if busPreferenceArgs(inv.Args) {
		return readPolicy(inv, DataScopeUserPrivate)
	}
	return readPolicy(inv, DataScopePublic)
}

func todoMutationArgs(args []string) bool {
	if !hasArgs(args) || firstArgIs(args, "help") {
		return false
	}
	_, _, list, _ := todoListQueryFromArgs(args)
	return !list
}

func todoMutationEffect(args []string) CapabilityEffect {
	if firstArgIs(args, "delete") {
		return EffectDestructive
	}
	return EffectWrite
}

func busPreferenceMutationArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	first := normToken(args[0])
	if first == "set" || first == "设置" {
		return true
	}
	if _, ok := parseBusShowDeparted(args); ok {
		return true
	}
	if _, ok := parseBusShowSouth(args); ok {
		return true
	}
	// A preference route is a write even when the user uses the natural
	// "偏好 路线 东区 西区" form instead of the explicit "设置" prefix.
	if busPreferenceArgs(args) && len(args) > 1 {
		return true
	}
	return false
}

func descriptor(id CapabilityID, forms []string, requirements CapabilityRequirements, effect CapabilityEffect, exposure ResultExposure, input CapabilityInputPolicy, normalize CapabilityNormalizer, run func(Handler, context.Context, store.Identity, []string) string, resolve func(Invocation) CapabilityPolicy, help HelpMetadata) CapabilityDescriptor {
	return CapabilityDescriptor{
		ID: id, Forms: forms, Requirements: requirements, Effect: effect,
		Exposure: exposure, Input: input, Normalize: normalize,
		Execute: textExecutor(run), Present: defaultCapabilityPresenter,
		ResolvePolicy: resolve, AutoLogin: true, Help: help,
	}
}

func helpMeta(topic, title, summary string, overview bool, examples []HelpExample, shortcuts []HelpExample) HelpMetadata {
	return HelpMetadata{Topic: topic, Title: title, Summary: summary, Overview: overview, Examples: examples, Shortcuts: shortcuts}
}

func example(command, description string) HelpExample {
	return HelpExample{Command: command, Description: description}
}

func exampleFor(capability CapabilityID, command, description string, args ...string) HelpExample {
	return HelpExample{
		Command: command, Description: description, Capability: capability,
		Arguments: append([]string{}, args...),
	}
}

var capabilityDescriptors []CapabilityDescriptor

func init() {
	capabilityDescriptors = []CapabilityDescriptor{
		descriptor(CapabilityHelp, []string{"help", "帮助", "菜单"}, CapabilityRequirements{DataScope: DataScopePublic}, EffectRead, ExposureModel, allowArgs, nil, nil, helpPolicy, helpMeta("help", "帮助", "查看 Bot 命令总览与专题用法", false, nil, nil)),
		descriptor(CapabilityLogin, []string{"login", "登录"}, CapabilityRequirements{OAuth: true, DataScope: DataScopeUserPrivate}, EffectWrite, ExposureHostOnly, func(args []string) bool { return !hasArgs(args) || firstArgIsHelp(args) || firstArgIs(args, "status") }, normalizeLoginArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.login(ctx, ident, args)
		}, loginPolicy, helpMeta("account", "账户", "登录、退出与查看账户信息", true, []HelpExample{example("账户 登录", "开始 Life @ USTC 登录"), example("账户 登录状态", "查询当前登录流程")}, []HelpExample{example("登录", "相当于“账户 登录”")})),
		descriptor(CapabilityLogout, []string{"logout", "退出"}, CapabilityRequirements{OAuth: true, DataScope: DataScopeUserPrivate}, EffectDestructive, ExposureModel, func(args []string) bool { return !hasArgs(args) || firstArgIsHelp(args) }, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string {
			return h.logout(ctx, ident)
		}, func(inv Invocation) CapabilityPolicy { return privateWritePolicy(inv, EffectDestructive) }, helpMeta("account", "账户", "登录、退出与查看账户信息", false, []HelpExample{example("账户 退出", "退出并清除登录状态")}, []HelpExample{example("退出", "相当于“账户 退出”")})),
		descriptor(CapabilityAccount, []string{"account", "账户", "我的"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, func(args []string) bool { return !hasArgs(args) || firstArgIsHelp(args) }, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string { return h.me(ctx, ident) }, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("account", "账户", "登录、退出与查看账户信息", false, []HelpExample{example("账户 信息", "查看当前登录用户")}, []HelpExample{example("我的", "相当于“账户 信息")})),
		descriptor(CapabilityTodo, []string{"todo", "待办", "td"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, allowArgs, normalizeTodoArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.todo(ctx, ident, args)
		}, todoPolicy, helpMeta("todo", "待办", "查看和管理待办", true, []HelpExample{example("待办", "查看未完成待办，每页 30 条"), example("待办 列表 第2页", "查看未完成待办的第 2 页"), example("待办 添加 写报告", "新增待办"), example("待办 完成 1", "完成第 1 条待办"), example("待办 恢复 1", "恢复已完成待办"), example("待办 删除 1", "删除第 1 条待办"), example("待办 更新 1 标题 新标题", "修改待办标题")}, []HelpExample{example("待办（td）", "直接查看未完成待办")})),
		descriptor(CapabilityHomework, []string{"homework", "作业", "hw"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, homeworkOrTodoInput(homeworkArgsAcceptable), normalizeHomeworkArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.homework(ctx, ident, args)
		}, homeworkPolicy, helpMeta("homework", "作业", "查看和管理作业", true, []HelpExample{example("作业", "查看未完成作业，每页 30 条"), example("作业 列表 第2页", "查看作业的第 2 页"), example("作业 完成 1", "完成第 1 条作业"), example("作业 恢复 1", "取消第 1 条作业的完成状态")}, []HelpExample{example("作业（hw）", "直接查看未完成作业")})),
		descriptor(CapabilityCalendar, []string{"calendar", "日程", "今日", "ddl"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string {
			return h.overview(ctx, ident)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("agenda", "日程", "今日安排、综合概览与近期截止", true, []HelpExample{example("日程 今日", "汇总今日课程、待办和作业")}, []HelpExample{example("今日（ddl）", "相当于“日程 今日")})),
		descriptor(CapabilitySubscription, []string{"subscription", "订阅", "课程订阅"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, subscriptionArgsAcceptable, normalizeSubscriptionArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.subscription(ctx, ident, args)
		}, subscriptionPolicy, helpMeta("subscription", "订阅", "查看、导入和管理教学班订阅", false, []HelpExample{example("订阅", "查看已订阅教学班"), example("订阅 添加 CONT5103P.01", "批量订阅教学班"), example("订阅 链接", "查看私有日历订阅链接")}, nil)),
		descriptor(CapabilityNotify, []string{"notify", "通知", "提醒"}, CapabilityRequirements{Store: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, notifyArgsAcceptable, normalizeNotifyArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.notify(ctx, ident, args)
		}, notifyPolicy, helpMeta("settings", "设置", "管理通知等偏好", false, []HelpExample{example("设置 通知", "查看通知设置"), example("设置 通知 课表 开", "开启课前提醒"), example("设置 通知 作业 开", "开启作业提醒"), example("设置 通知 作业 关", "关闭作业提醒")}, []HelpExample{example("通知", "相当于“设置 通知")})),
		descriptor(CapabilitySettings, []string{"settings", "设置"}, CapabilityRequirements{DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, settingsArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.settings(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("settings", "设置", "管理通知等偏好", true, []HelpExample{example("设置", "查看设置命令")}, nil)),
		descriptor(CapabilityFeedback, []string{"feedback", "反馈"}, CapabilityRequirements{DataScope: DataScopePublic}, EffectWrite, ExposureModel, allowArgs, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.feedback(ctx, ident, args)
		}, feedbackPolicy, helpMeta("feedback", "反馈", "向管理员提交反馈", true, []HelpExample{exampleFor(CapabilityFeedback, "反馈 <你的建议>", "向管理员提交反馈", "请增加这个功能")}, nil)),
		descriptor(CapabilityPing, []string{"ping"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, _ store.Identity, _ []string) string {
			if err := h.Life.Health(ctx); err != nil {
				h.markOutcome(CapabilityOutcomeFailed)
				return "Life @ USTC API unavailable: " + err.Error()
			}
			return "Life @ USTC API is reachable."
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("system", "系统", "查看服务状态与检查连通性", false, []HelpExample{example("系统 检查", "检查 Life @ USTC API 是否可用")}, nil)),
		descriptor(CapabilityStatus, []string{"status", "状态"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, _ store.Identity, _ []string) string {
			return h.status(ctx)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("system", "系统", "查看服务状态与检查连通性", true, []HelpExample{example("系统 状态", "查看公开服务状态")}, []HelpExample{example("状态（status）", "相当于“系统 状态")})),
		descriptor(CapabilitySemester, []string{"semester", "学期"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, semesterInput, nil, func(h Handler, ctx context.Context, _ store.Identity, _ []string) string {
			return h.currentSemester(ctx)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("semester", "学期", "只查看公开的当前学期名称和日期，不返回个人选课", false, []HelpExample{example("学期", "查看当前学期"), example("学期 当前", "查看当前学期")}, nil)),
		{
			// Weather builds a structured card image during execution, so it
			// cannot use the text-only public cache; the server API reads from
			// KV and is cheap to call per invocation.
			ID: CapabilityWeather, Forms: []string{"weather", "天气"},
			Requirements: CapabilityRequirements{Life: true, DataScope: DataScopePublic},
			Effect:       EffectRead, Exposure: ExposureModel,
			Input: weatherInput, Execute: weatherExecutor, Present: defaultCapabilityPresenter,
			ResolvePolicy: func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) },
			AutoLogin:     true,
			Help:          helpMeta("weather", "天气", "查看本部与高新校区的天气", true, []HelpExample{example("天气", "查看本部与高新校区的天气"), example("天气 高新", "只看高新校区")}, nil),
		},
		descriptor(CapabilityCourse, []string{"course", "课程"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, allowArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchCourses(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("course", "课程", "搜索课程和查看课程详情", false, []HelpExample{example("课程 数学分析", "按关键词快速搜索课程")}, nil)),
		descriptor(CapabilitySection, []string{"section", "教学班"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, allowArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchSections(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{example("教学班 高等数学", "按关键词快速搜索教学班")}, nil)),
		descriptor(CapabilityTeacher, []string{"teacher", "老师"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, allowArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchTeachers(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("teacher", "老师", "搜索老师和查看老师详情", true, []HelpExample{example("老师 张", "按关键词快速搜索老师")}, nil)),
		descriptor(CapabilityBus, []string{"bus", "校车", "xc"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic}, EffectRead, ExposureModel, busCommandArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.bus(ctx, ident, args)
		}, busPolicy, helpMeta("bus", "校车", "按日期、服务日或路线查询班次并设置偏好", true, []HelpExample{example("校车", "查看今天接下来各路线的校车"), example("校车 周六 周日", "分别查询最近周六和周日的完整时刻"), example("校车 周六 太湖路园区 东区", "查询周六指定路线的完整时刻"), example("校车 2026-09-06 东区 太湖路园区", "查询指定日期的完整时刻"), example("校车 工作日 东区 西区", "查询周一至周五的时刻"), example("校车 偏好", "查看校车偏好"), example("校车 偏好 路线 东区 西区", "设置偏好路线")}, []HelpExample{example("校车（xc）", "直接查询今天接下来的校车")})),
		descriptor(CapabilitySchedule, []string{"schedule", "课表", "kb"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, scheduleArgsAcceptable, normalizeScheduleArgs, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.curriculum(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("schedule", "课表", "周课表、单日课表与下一节课", true, []HelpExample{example("课表", "查看本周课表"), example("课表 本周", "查看本周课表"), example("课表 下周", "查看下周课表"), example("课表 第3周", "查看指定教学周"), example("课表 2026 秋季学期", "查看整学期课表与教学周范围"), example("课表 单日 今天", "只查看今天的课表"), example("课表 单日 明天", "只查看明天的课表")}, []HelpExample{example("今日课表（单日课表）", "相当于“课表 单日 今天")})),
		descriptor(CapabilityNextClass, []string{"nextclass", "下一节课"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string {
			return h.nextClass(ctx, ident)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("schedule", "课表", "周课表、单日课表与下一节课", false, []HelpExample{example("课表 下一节", "查看最近一节课")}, []HelpExample{example("下一节课", "相当于“课表 下一节")})),
		descriptor(CapabilityExam, []string{"exam", "考试", "ks"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, examArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.exams(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("exam", "考试", "查看已订阅课程的考试", true, []HelpExample{example("考试", "查看已订阅课程的考试，每页 30 条"), example("考试 第2页", "查看考试的第 2 页")}, nil)),
		descriptor(CapabilityListSemesters, []string{"list_semesters", "学期列表"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, semesterListInput, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.listSemesters(ctx, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("semester", "学期", "查看当前学期和学期列表", false, []HelpExample{example("学期 列表", "列出最近 20 个学期"), example("学期 列表 10", "指定返回数量")}, nil)),
		descriptor(CapabilityCourseSearch, []string{"course_search", "课程搜索"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, allowEffect(), ExposureModel, courseSearchArgsAcceptable, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchCoursesWithFilters(ctx, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("course", "课程", "搜索课程和查看课程详情", false, []HelpExample{example("课程 搜索 数学分析", "搜索课程"), exampleFor(CapabilityCourseSearch, "课程 搜索 培养层次ID <ID>", "按培养层次筛选", "education_level_id", "1"), exampleFor(CapabilityCourseSearch, "课程 搜索 类别ID <ID>", "按课程类别筛选", "category_id", "1"), exampleFor(CapabilityCourseByJWID, "课程 查看 <JW ID>", "按 JW ID 查看详情", "12345")}, nil)),
		descriptor(CapabilitySectionSearch, []string{"section_search", "教学班搜索"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, sectionSearchArgsAcceptable, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchSectionsWithFilters(ctx, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{example("教学班 搜索 高等数学", "搜索教学班"), exampleFor(CapabilitySectionByJWID, "教学班 查看 <JW ID>", "按 JW ID 查看详情", "12345")}, nil)),
		descriptor(CapabilityTeacherSearch, []string{"teacher_search", "老师搜索"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, teacherSearchArgsAcceptable, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.searchTeachersWithFilters(ctx, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("teacher", "老师", "搜索老师和查看老师详情", false, []HelpExample{example("老师 搜索 张", "搜索老师"), exampleFor(CapabilityTeacherByID, "老师 查看 <ID>", "按 ID 查看详情", "12345")}, nil)),
		descriptor(CapabilityCourseByJWID, []string{"course_by_jw_id", "课程编号"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, positiveIDArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.getCourseByJwID(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("course", "课程", "搜索课程和查看课程详情", false, []HelpExample{exampleFor(CapabilityCourseByJWID, "课程 查看 <JW ID>", "按 JW ID 查看详情", "12345")}, nil)),
		descriptor(CapabilitySectionByJWID, []string{"section_by_jw_id", "教学班编号"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, positiveIDArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.getSectionByJwID(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{exampleFor(CapabilitySectionByJWID, "教学班 查看 <JW ID>", "按 JW ID 查看详情", "12345")}, nil)),
		descriptor(CapabilityTeacherByID, []string{"teacher_by_id", "老师编号"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, positiveIDArgs, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.getTeacherByID(ctx, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("teacher", "老师", "搜索老师和查看老师详情", false, []HelpExample{exampleFor(CapabilityTeacherByID, "老师 查看 <ID>", "按 ID 查看详情", "12345")}, nil)),
		descriptor(CapabilityBusRoutes, []string{"bus_routes", "校车路线"}, CapabilityRequirements{Life: true, DataScope: DataScopePublic, PublicCache: true}, EffectRead, ExposureModel, busRouteArgsAcceptable, nil, func(h Handler, ctx context.Context, _ store.Identity, args []string) string {
			return h.busRoutes(ctx, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopePublic) }, helpMeta("bus", "校车", "查询班次、路线与设置偏好", false, []HelpExample{example("校车 路线", "列出全部校车路线")}, nil)),
		descriptor(CapabilityUnsubscribeSectionByJWID, []string{"unsubscribe_section_by_jw_id", "退订教学班"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectDestructive, ExposureModel, positiveIDArgs, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.unsubscribeSectionByJwID(ctx, ident, joinedArgs(args))
		}, func(inv Invocation) CapabilityPolicy { return privateWritePolicy(inv, EffectDestructive) }, helpMeta("subscription", "订阅", "查看、导入和管理教学班订阅", false, []HelpExample{exampleFor(CapabilityUnsubscribeSectionByJWID, "订阅 删除 <JW ID>", "按 JW ID 退订教学班", "12345")}, nil)),
		descriptor(CapabilityMySubscribedSections, []string{"my_subscribed_sections", "我的订阅", "选课列表", "已选课程"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string {
			return h.mySubscribedSections(ctx, ident)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("subscription", "订阅", "查看个人已订阅教学班列表，用于回答选了哪些课", false, []HelpExample{example("订阅 列表", "查看已订阅教学班")}, nil)),
		descriptor(CapabilitySectionSchedules, []string{"section_schedules", "教学班课表"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, sectionScheduleArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionSchedules(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{exampleFor(CapabilitySectionSchedules, "教学班 课表 <JW ID> <开始日期> <结束日期>", "查看日期范围内的课表", "12345", "2026-09-01", "2026-09-30")}, nil)),
		descriptor(CapabilitySectionExams, []string{"section_exams", "教学班考试"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, sectionPagedIDArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionExams(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{exampleFor(CapabilitySectionExams, "教学班 考试 <JW ID>", "查看指定教学班考试，每页 30 条", "12345")}, nil)),
		descriptor(CapabilitySectionHomeworks, []string{"section_homeworks", "教学班作业"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, sectionPagedIDArgsAcceptable, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.sectionHomeworks(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("section", "教学班", "搜索教学班及其课表、考试、作业", false, []HelpExample{exampleFor(CapabilitySectionHomeworks, "教学班 作业 <JW ID>", "查看指定教学班作业，每页 30 条", "12345")}, nil)),
		descriptor(CapabilityOverview, []string{"overview", "概览"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, noArgsOrHelp, nil, func(h Handler, ctx context.Context, ident store.Identity, _ []string) string {
			return h.myDashboard(ctx, ident)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("agenda", "日程", "今日安排、综合概览与近期截止", false, []HelpExample{example("日程 概览", "汇总待办、作业和考试")}, []HelpExample{example("概览", "相当于“日程 概览")})),
		descriptor(CapabilityUpcomingDeadlines, []string{"upcoming_deadlines", "近期截止"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, func(args []string) bool {
			if !hasArgs(args) {
				return true
			}
			if firstArgIsHelp(args) {
				return len(args) == 1
			}
			_, ok := parseIntArg(args[0])
			return len(args) == 1 && ok
		}, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.upcomingDeadlines(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("agenda", "日程", "今日安排、综合概览与近期截止", false, []HelpExample{example("日程 截止", "查看未来 7 天的截止事项"), example("日程 截止 14", "指定未来天数")}, []HelpExample{example("近期截止 14", "相当于“日程 截止 14")})),
	}
	bindCapabilityUsage(capabilityDescriptors)
}

func init() {
	capabilityDescriptors[0].Execute = textExecutor(func(h Handler, _ context.Context, _ store.Identity, args []string) string {
		return h.help(args...)
	})
}

func allowEffect() CapabilityEffect { return EffectRead }

func homeworkOrTodoInput(policy func([]string) bool) CapabilityInputPolicy { return policy }

func noArgsOrHelp(args []string) bool { return !hasArgs(args) || firstArgIsHelp(args) }

func semesterInput(args []string) bool { return noArgsOrHelp(args) }

func semesterListInput(args []string) bool {
	if !hasArgs(args) {
		return true
	}
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	if len(args) != 1 {
		return false
	}
	_, ok := parseIntArg(args[0])
	return ok
}

// CapabilityDescriptors returns a copy suitable for inspection by integration
// code. Mutable slices are copied so callers cannot alter parsing policy.
func CapabilityDescriptors() []CapabilityDescriptor {
	result := make([]CapabilityDescriptor, len(capabilityDescriptors))
	for i, descriptor := range capabilityDescriptors {
		result[i] = descriptor
		result[i].Forms = append([]string(nil), descriptor.Forms...)
		result[i].Help.Examples = copyUsageExamples(descriptor.Help.Examples)
		result[i].Help.Shortcuts = copyUsageExamples(descriptor.Help.Shortcuts)
	}
	return result
}

// CapabilityDescriptorFor returns a copy of one descriptor by stable ID.
func CapabilityDescriptorFor(id CapabilityID) (CapabilityDescriptor, bool) {
	for _, descriptor := range capabilityDescriptors {
		if descriptor.ID != id {
			continue
		}
		copy := descriptor
		copy.Forms = append([]string(nil), descriptor.Forms...)
		copy.Help.Examples = copyUsageExamples(descriptor.Help.Examples)
		copy.Help.Shortcuts = copyUsageExamples(descriptor.Help.Shortcuts)
		return copy, true
	}
	return CapabilityDescriptor{}, false
}

// NewInvocation validates structured agent arguments against the same
// descriptor policy used by direct command parsing.
func NewInvocation(id CapabilityID, args []string) (Invocation, bool) {
	return acceptedCommand("", string(id), args)
}

func descriptorForID(name string) (*CapabilityDescriptor, bool) {
	for i := range capabilityDescriptors {
		if capabilityDescriptors[i].ID == CapabilityID(name) {
			return &capabilityDescriptors[i], true
		}
	}
	return nil, false
}

func descriptorForForm(form string) (*CapabilityDescriptor, bool) {
	key := commandToken(form)
	for _, descriptor := range capabilityDescriptors {
		for _, candidate := range descriptor.Forms {
			if key == commandToken(candidate) {
				copy := descriptor
				return &copy, true
			}
		}
	}
	return nil, false
}
