package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

var (
	errCapabilityReceiptTargetNotFound  = errors.New("capability receipt target not found")
	errCapabilityReceiptTargetAmbiguous = errors.New("capability receipt target is ambiguous")
	errCapabilityReceiptIncomplete      = errors.New("capability receipt target details are incomplete")
	errCapabilityReceiptInconsistent    = errors.New("capability receipt target conflicts with current semester")
	errCapabilityReceiptUnavailable     = errors.New("capability receipt unavailable")
	errCapabilityInvalidInput           = errors.New("capability invocation invalid input")
	errCapabilityMutationMustExpand     = errors.New("capability mutation requires expansion")
	errCapabilityPersonalTarget         = errors.New("capability personal target is not unique or is missing")
	errCapabilityForbidden              = errors.New("capability invocation forbidden")
)

const (
	ReceiptActionSubscribe   = "订阅"
	ReceiptActionUnsubscribe = "取消"
	ReceiptResourceSection   = "课程"
)

// CapabilityInvocationDescription is the host preflight contract for a
// mutation confirmation. It freezes the normalized invocation and its
// user-visible subject before any side effect occurs.
type CapabilityInvocationDescription struct {
	Invocation Invocation               `json:"invocation"`
	Receipt    *store.CapabilityReceipt `json:"receipt,omitempty"`
}

// DescribeInvocation validates and describes a normalized capability without
// executing its mutation. Subscription targets are resolved through the
// public catalog so receipts contain authoritative course, teacher and
// semester fields before a confirmation prompt is shown.
func (h Handler) DescribeInvocation(ctx context.Context, input Input, id CapabilityID, args []string) (CapabilityInvocationDescription, error) {
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return CapabilityInvocationDescription{}, fmt.Errorf("%w for capability %q", errCapabilityInvalidInput, id)
	}
	if err := validateReceiptInvocation(invocation); err != nil {
		return CapabilityInvocationDescription{Invocation: invocation}, err
	}
	description := CapabilityInvocationDescription{Invocation: invocation}
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return description, errCapabilityForbidden
	}
	if personalMutationNeedsTarget(invocation) {
		resolved, receipt, err := h.resolvePersonalMutation(ctx, input.Identity, invocation)
		return CapabilityInvocationDescription{Invocation: resolved, Receipt: receipt}, err
	}
	resolved, receipt, err := h.resolveInvocationReceipt(ctx, invocation)
	if err != nil {
		return description, err
	}
	description.Invocation = resolved
	description.Receipt = receipt
	return description, nil
}

// CapabilityPreflightFailure returns literal, user-actionable evidence for a
// semantic invocation error discovered before an Agent mutation is persisted.
// Infrastructure failures remain errors so they cannot be mistaken for a
// domain result.
func CapabilityPreflightFailure(id CapabilityID, err error) (string, bool) {
	switch {
	case errors.Is(err, errCapabilityPersonalTarget):
		return "没有找到唯一的操作目标，因此没有执行。请先查看列表，再使用准确编号或 id:<完整ID>。", true
	case errors.Is(err, errCapabilityReceiptTargetNotFound):
		return "没有找到要操作的课程或教学班，因此没有执行任何操作。请先发送“教学班 搜索 <课程名或代码>”，再使用查询结果中的准确教学班代码。", true
	case errors.Is(err, errCapabilityReceiptTargetAmbiguous):
		return "这个教学班代码在当前学期对应多个结果，系统无法安全确定要操作哪一个，因此没有执行任何操作。请稍后重试或联系管理员核对教学班数据。", true
	case errors.Is(err, errCapabilityReceiptIncomplete):
		return "没有取得完整的课程名称、教师和学期信息，因此没有执行任何操作。请稍后重试；如果问题持续，请联系管理员核对教学班数据。", true
	case errors.Is(err, errCapabilityReceiptInconsistent):
		return "查询到的教学班与当前学期不一致，因此没有执行任何操作。请稍后重试；如果问题持续，请联系管理员核对教学班数据。", true
	case errors.Is(err, errCapabilityInvalidInput), errors.Is(err, errCapabilityMutationMustExpand):
		return invalidCapabilityUsageResponse(id).Text, true
	case errors.Is(err, errCapabilityForbidden):
		return "此功能只能在私聊使用。", true
	default:
		return "", false
	}
}

// ExpandMutationInvocations splits a normalized mutation into independent
// invocations suitable for one-at-a-time confirmation and execution. The
// helper is deterministic and keeps each resulting invocation bound to the
// registry's normal validation.
func ExpandMutationInvocations(invocation Invocation) []Invocation {
	invocation, ok := withDescriptor(invocation)
	if !ok {
		return nil
	}
	if invocation.Policy().Effect == EffectRead {
		return []Invocation{invocation}
	}

	switch invocation.ID() {
	case CapabilitySubscription:
		if !firstArgIn(invocation.Args, "import", "remove") {
			return []Invocation{invocation}
		}
		codes := subscriptionMutationTargets(invocation.Args[1:])
		if len(codes) <= 1 {
			return []Invocation{invocation}
		}
		out := make([]Invocation, 0, len(codes))
		for _, code := range codes {
			if expanded, ok := NewInvocation(CapabilitySubscription, []string{invocation.Args[0], code}); ok {
				expanded.Raw = invocation.Raw
				expanded.NaturalRoute = invocation.NaturalRoute
				out = append(out, expanded)
			}
		}
		return out
	case CapabilityTodo:
		if !firstArgIn(invocation.Args, "done", "undo", "delete") || len(invocation.Args) < 2 {
			return []Invocation{invocation}
		}
		return splitMutationTargets(invocation, 1)
	case CapabilityHomework:
		if !firstArgIn(invocation.Args, "done", "undo") || len(invocation.Args) < 2 {
			return []Invocation{invocation}
		}
		return splitMutationTargets(invocation, 1)
	default:
		return []Invocation{invocation}
	}
}

func subscriptionMutationTargets(args []string) []string {
	targetArgs, _, ok := splitSubscriptionMutationArgs(args)
	if !ok {
		return nil
	}
	raw := joinedArgs(targetArgs)
	if raw == "" {
		return nil
	}
	return extractSectionCodes(raw)
}

func splitMutationTargets(invocation Invocation, targetIndex int) []Invocation {
	if targetIndex < 0 || targetIndex >= len(invocation.Args) {
		return []Invocation{invocation}
	}
	targets := splitTodoTargets(strings.Join(invocation.Args[targetIndex:], " "))
	if len(targets) <= 1 {
		return []Invocation{invocation}
	}
	out := make([]Invocation, 0, len(targets))
	for _, target := range targets {
		args := append([]string{}, invocation.Args[:targetIndex]...)
		args = append(args, target)
		if expanded, ok := NewInvocation(invocation.ID(), args); ok {
			expanded.Raw = invocation.Raw
			expanded.NaturalRoute = invocation.NaturalRoute
			out = append(out, expanded)
		}
	}
	return out
}

func (h Handler) resolveInvocationReceipt(ctx context.Context, invocation Invocation) (Invocation, *store.CapabilityReceipt, error) {
	if requiresReceiptResolution(invocation) && h.Life == nil {
		return invocation, nil, fmt.Errorf("%w: Life @ USTC API unavailable: not configured", errCapabilityReceiptUnavailable)
	}
	if h.Life == nil {
		receipt := ReceiptForInvocation(invocation)
		return invocation, receiptPointer(receipt), nil
	}
	switch invocation.ID() {
	case CapabilitySubscription:
		if !firstArgIn(invocation.Args, "import", "remove") {
			receipt := ReceiptForInvocation(invocation)
			return invocation, receiptPointer(receipt), nil
		}
		codes := subscriptionMutationTargets(invocation.Args[1:])
		if len(codes) != 1 {
			return invocation, nil, nil
		}
		semester, err := h.Life.CurrentSemester(ctx)
		if err != nil {
			return invocation, nil, fmt.Errorf("resolve current semester for subscription target %s: %w", codes[0], err)
		}
		options := life.SearchSectionsOptions{Keyword: codes[0], Limit: 5}
		options.SemesterID = int64(lifedata.FirstInt(semester, "id"))
		if options.SemesterID == 0 {
			return invocation, nil, fmt.Errorf("%w: current semester has no identifier", errCapabilityReceiptUnavailable)
		}
		sections, err := h.Life.SearchSectionsWithFilters(ctx, options)
		if err != nil {
			return invocation, nil, fmt.Errorf("resolve subscription target %s: %w", codes[0], err)
		}
		section, ambiguous := matchingSectionCode(sections, codes[0])
		if ambiguous {
			return invocation, nil, fmt.Errorf("%w: %s", errCapabilityReceiptTargetAmbiguous, codes[0])
		}
		if section == nil {
			return invocation, nil, fmt.Errorf("%w: %s", errCapabilityReceiptTargetNotFound, codes[0])
		}
		if !sectionMatchesSemesterID(section, options.SemesterID) {
			return invocation, nil, fmt.Errorf("%w: %s", errCapabilityReceiptInconsistent, codes[0])
		}
		action := ReceiptActionSubscribe
		if firstArgIs(invocation.Args, "remove") {
			action = ReceiptActionUnsubscribe
		}
		receipt, err := sectionReceipt(action, section, semester)
		if err != nil {
			return invocation, nil, err
		}
		invocation.Args = []string{invocation.Args[0], codes[0], subscriptionSemesterIDArg, strconv.FormatInt(options.SemesterID, 10)}
		return invocation, receipt, nil
	default:
		receipt := ReceiptForInvocation(invocation)
		return invocation, receiptPointer(receipt), nil
	}
}

func validateReceiptInvocation(invocation Invocation) error {
	switch invocation.ID() {
	case CapabilitySubscription:
		if !firstArgIn(invocation.Args, "import", "remove") {
			return nil
		}
		if len(subscriptionMutationTargets(invocation.Args[1:])) == 0 {
			return fmt.Errorf("%w: subscription mutation requires a section code", errCapabilityInvalidInput)
		}
		if len(subscriptionMutationTargets(invocation.Args[1:])) != 1 {
			return fmt.Errorf("%w: subscription mutation targets must be expanded before confirmation", errCapabilityMutationMustExpand)
		}
	}
	return nil
}

func requiresReceiptResolution(invocation Invocation) bool {
	switch invocation.ID() {
	case CapabilitySubscription:
		return firstArgIn(invocation.Args, "import", "remove") && len(subscriptionMutationTargets(invocation.Args[1:])) == 1
	default:
		return false
	}
}

func matchingSectionCode(sections []map[string]any, code string) (map[string]any, bool) {
	var match map[string]any
	for _, section := range sections {
		if strings.EqualFold(strings.TrimSpace(sectionCode(section)), code) {
			if match != nil {
				return nil, true
			}
			match = section
		}
	}
	return match, false
}

func sectionMatchesSemesterID(section map[string]any, semesterID int64) bool {
	if semesterID <= 0 {
		return false
	}
	ids := []int64{int64(lifedata.FirstInt(section, "semesterId"))}
	if semester, ok := section["semester"].(map[string]any); ok {
		ids = append(ids, int64(lifedata.FirstInt(semester, "id")))
	}
	for _, id := range ids {
		if id > 0 && id != semesterID {
			return false
		}
	}
	return true
}

func sectionReceipt(action string, section, currentSemester map[string]any) (*store.CapabilityReceipt, error) {
	teacher := lifedata.NestedString(section, "teacher", "namePrimary", "nameCn", "name")
	if teacher == "" {
		teacher = lifedata.NestedString(section, "instructor", "namePrimary", "nameCn", "name")
	}
	if teacher == "" {
		teachers := lifedata.MapSlice(section["teachers"])
		if len(teachers) > 0 {
			teacher = lifedata.FirstString(teachers[0], "namePrimary", "nameCn", "name")
		}
	}
	if teacher == "" {
		teacher = lifedata.FirstString(section, "teacherName", "teacherNamePrimary", "teacherNameCn", "teacher_name")
	}
	course := lifedata.NestedString(section, "course", "namePrimary", "nameCn", "name")
	if course == "" {
		course = lifedata.NestedString(section, "courseInfo", "namePrimary", "nameCn", "name")
	}
	if course == "" {
		course = lifedata.FirstString(section, "courseName", "courseNamePrimary", "courseNameCn", "course_name")
	}
	semester := lifedata.NestedString(section, "semester", "namePrimary", "nameCn", "name")
	if semester == "" {
		semester = lifedata.NestedString(section, "term", "namePrimary", "nameCn", "name")
	}
	if semester == "" {
		semester = lifedata.FirstString(section, "semesterName", "semesterNamePrimary", "semesterNameCn", "semester_name")
	}
	if semester == "" {
		semester = lifedata.FirstString(currentSemester, "namePrimary", "nameCn", "name")
	}
	if course == "" || teacher == "" || semester == "" {
		return nil, fmt.Errorf("%w: course=%t teacher=%t semester=%t", errCapabilityReceiptIncomplete, course != "", teacher != "", semester != "")
	}
	subject := course + "（" + teacher + "，" + semester + "）"
	return &store.CapabilityReceipt{Action: action, Resource: ReceiptResourceSection, Subject: subject}, nil
}

// ReceiptForInvocation returns the stable, host-owned label used for user
// confirmations and execution receipts. Meta capabilities deliberately return
// an empty receipt. Subscription mutations are replaced with catalog-derived
// course, teacher, and semester details by DescribeInvocation.
func ReceiptForInvocation(invocation Invocation) store.CapabilityReceipt {
	invocation, ok := withDescriptor(invocation)
	if !ok {
		return store.CapabilityReceipt{}
	}
	args := strings.TrimSpace(strings.Join(invocation.Args, " "))
	if args == "" {
		args = "全部"
	}
	query := func(resource string) store.CapabilityReceipt {
		return store.CapabilityReceipt{Action: "查询", Resource: resource, Subject: args}
	}
	mutation := func(action, resource string, skip int) store.CapabilityReceipt {
		subject := strings.TrimSpace(strings.Join(invocation.Args[min(skip, len(invocation.Args)):], " "))
		if subject == "" {
			subject = args
		}
		return store.CapabilityReceipt{Action: action, Resource: resource, Subject: subject}
	}

	switch invocation.ID() {
	case CapabilityHelp:
		return store.CapabilityReceipt{}
	case CapabilityLogin:
		if invocation.Policy().Effect == EffectRead {
			return query("登录状态")
		}
		return store.CapabilityReceipt{Action: "登录", Resource: "账户", Subject: "当前账户"}
	case CapabilityLogout:
		return store.CapabilityReceipt{Action: "退出", Resource: "账户", Subject: "当前账户"}
	case CapabilityAccount:
		return query("账户")
	case CapabilityTodo:
		if invocation.Policy().Effect == EffectRead {
			return query("待办")
		}
		action := "更新"
		if firstArgIs(invocation.Args, "delete") {
			action = "删除"
		} else if firstArgIs(invocation.Args, "done") {
			action = "完成"
		} else if firstArgIs(invocation.Args, "undo") {
			action = "恢复"
		} else if firstArgIs(invocation.Args, "add") {
			action = "添加"
		}
		return mutation(action, "待办", 1)
	case CapabilityHomework:
		if invocation.Policy().Effect == EffectRead {
			return query("作业")
		}
		action := "完成"
		if firstArgIs(invocation.Args, "undo") {
			action = "恢复"
		}
		return mutation(action, "作业", 1)
	case CapabilitySubscription:
		if invocation.Policy().Effect == EffectRead {
			return query("课程")
		}
		if firstArgIs(invocation.Args, "remove") {
			return mutation(ReceiptActionUnsubscribe, ReceiptResourceSection, 1)
		}
		return mutation(ReceiptActionSubscribe, ReceiptResourceSection, 1)
	case CapabilityNotify:
		if invocation.Policy().Effect == EffectRead {
			return query("提醒")
		}
		target := "提醒"
		if len(invocation.Args) > 0 {
			switch invocation.Args[0] {
			case "homework":
				target = "作业"
			case "classes":
				target = "课表"
			}
		}
		state := ""
		if len(invocation.Args) > 1 {
			switch invocation.Args[1] {
			case "on":
				state = "开"
			case "off":
				state = "关"
			}
		}
		if state != "" {
			target += "：" + state
		}
		return store.CapabilityReceipt{Action: "设置", Resource: "提醒", Subject: target}
	case CapabilityFeedback:
		if invocation.Policy().Effect == EffectRead {
			return store.CapabilityReceipt{}
		}
		return mutation("提交", "反馈", 0)
	case CapabilitySemester, CapabilityListSemesters:
		return query("学期")
	case CapabilityCourse, CapabilityCourseSearch, CapabilityCourseByJWID,
		CapabilitySection, CapabilitySectionSearch, CapabilitySectionByJWID,
		CapabilityMySubscribedSections, CapabilitySectionSchedules,
		CapabilitySectionExams, CapabilitySectionHomeworks:
		return query("课程")
	case CapabilityTeacher, CapabilityTeacherSearch, CapabilityTeacherByID:
		return query("教师")
	case CapabilityBus, CapabilityBusRoutes:
		if invocation.Policy().Effect != EffectRead {
			return mutation("设置", "校车偏好", 0)
		}
		return query("校车")
	case CapabilitySchedule, CapabilityNextClass:
		return query("课表")
	case CapabilityExam:
		return query("考试")
	case CapabilityCalendar, CapabilityOverview:
		return query("日程")
	case CapabilityUpcomingDeadlines:
		return query("截止事项")
	default:
		return store.CapabilityReceipt{}
	}
}

func receiptPointer(receipt store.CapabilityReceipt) *store.CapabilityReceipt {
	if strings.TrimSpace(receipt.Action) == "" || strings.TrimSpace(receipt.Resource) == "" || strings.TrimSpace(receipt.Subject) == "" {
		return nil
	}
	return &receipt
}

func sectionCode(section map[string]any) string {
	return lifedata.FirstString(section, "code", "sectionCode", "section_code")
}

func personalMutationNeedsTarget(invocation Invocation) bool {
	if len(invocation.Args) < 2 {
		return false
	}
	switch invocation.ID() {
	case CapabilityTodo:
		return firstArgIn(invocation.Args, "done", "undo", "delete", "update")
	case CapabilityHomework:
		return firstArgIn(invocation.Args, "done", "undo")
	}
	return false
}

func (h Handler) resolvePersonalMutation(ctx context.Context, ident store.Identity, invocation Invocation) (Invocation, *store.CapabilityReceipt, error) {
	if h.Auth == nil {
		return invocation, nil, auth.ErrNotLoggedIn
	}
	token, err := h.Auth.AccessToken(ctx, ident)
	if err != nil {
		return invocation, nil, err
	}
	if h.Life == nil {
		return invocation, nil, errCapabilityReceiptUnavailable
	}
	if invocation.ID() == CapabilityTodo && len(invocation.Args) >= 2 {
		if id, explicit := todoTargetID(invocation.Args[1]); explicit {
			if id == "" {
				return invocation, nil, errCapabilityPersonalTarget
			}
			receipt := ReceiptForInvocation(invocation)
			receipt.Subject = id
			return invocation, &receipt, nil
		}
	}
	var items []map[string]any
	if invocation.ID() == CapabilityTodo {
		items, err = h.todos(ctx, ident, token, life.TodoListOptions{})
	} else {
		items, err = h.homeworks(ctx, ident, token)
	}
	if err != nil {
		return invocation, nil, err
	}
	target, ok := resolveByTarget(items, invocation.Args[1])
	if !ok || lifedata.FirstString(target, "id") == "" {
		return invocation, nil, errCapabilityPersonalTarget
	}
	id := lifedata.FirstString(target, "id")
	invocation.Args = append([]string(nil), invocation.Args...)
	invocation.Args[1] = "id:" + id
	receipt := ReceiptForInvocation(invocation)
	receipt.Subject = lifedata.FirstString(target, "title")
	if receipt.Subject == "" {
		receipt.Subject = id
	} else {
		receipt.Subject += " (" + id + ")"
	}
	return invocation, &receipt, nil
}
