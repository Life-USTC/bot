package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

var (
	errCapabilityReceiptTargetNotFound = errors.New("capability receipt target not found")
	errCapabilityReceiptUnavailable    = errors.New("capability receipt unavailable")
	errCapabilityInvalidInput          = errors.New("capability invocation invalid input")
	errCapabilityMutationMustExpand    = errors.New("capability mutation requires expansion")
	errCapabilityForbidden             = errors.New("capability invocation forbidden")
)

const (
	ReceiptActionSubscribe   = "订阅"
	ReceiptActionUnsubscribe = "取消"
	ReceiptResourceSection   = "课程"
)

// CapabilityInvocationDescription is the host preflight contract. A caller
// can freeze this value when asking for confirmation and reuse its Receipt
// for the approved execution result.
type CapabilityInvocationDescription struct {
	Invocation           Invocation               `json:"invocation"`
	Policy               CapabilityPolicy         `json:"policy"`
	ConfirmationRequired bool                     `json:"confirmationRequired"`
	Receipt              *store.CapabilityReceipt `json:"receipt,omitempty"`
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
		return CapabilityInvocationDescription{Invocation: invocation, Policy: invocation.Policy()}, err
	}
	description := CapabilityInvocationDescription{
		Invocation:           invocation,
		Policy:               invocation.Policy(),
		ConfirmationRequired: invocation.Policy().Confirmation == ConfirmUser,
	}
	if store.IsSharedConversation(input.Identity) && !sharedCommandAllowed(invocation) {
		return description, errCapabilityForbidden
	}
	receipt, err := h.receiptForInvocation(ctx, input.Identity, invocation)
	if err != nil {
		return description, err
	}
	description.Receipt = receipt
	return description, nil
}

// DescribeCapabilityInvocations expands a mutation and preflights each
// independent invocation in registry order. Callers can persist these
// descriptions as confirmation items and later pass each one to
// ExecuteApprovedInvocation without re-resolving its subject.
func (h Handler) DescribeCapabilityInvocations(ctx context.Context, input Input, id CapabilityID, args []string) ([]CapabilityInvocationDescription, error) {
	invocation, ok := NewInvocation(id, args)
	if !ok {
		return nil, fmt.Errorf("invalid arguments for capability %q", id)
	}
	expanded := ExpandMutationInvocations(invocation)
	descriptions := make([]CapabilityInvocationDescription, 0, len(expanded))
	for _, expandedInvocation := range expanded {
		description, err := h.DescribeInvocation(ctx, input, expandedInvocation.ID(), expandedInvocation.Args)
		if err != nil {
			return descriptions, err
		}
		descriptions = append(descriptions, description)
	}
	return descriptions, nil
}

// ExpandMutationInvocations splits a normalized mutation into independent
// invocations suitable for one-at-a-time confirmation and execution. Read
// capabilities are returned unchanged. The helper is deterministic and keeps
// each resulting invocation bound to the registry's normal validation.
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
		if !firstArgIs(invocation.Args, "import") {
			return []Invocation{invocation}
		}
		codes := subscriptionImportTargets(invocation.Args[1:])
		if len(codes) <= 1 {
			return []Invocation{invocation}
		}
		out := make([]Invocation, 0, len(codes))
		for _, code := range codes {
			if expanded, ok := NewInvocation(CapabilitySubscription, []string{"import", code}); ok {
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

func subscriptionImportTargets(args []string) []string {
	raw := joinedArgs(args)
	if raw == "" {
		return nil
	}
	// Prefer the command's canonical section-code extractor when the input
	// contains ordinary Life section codes. It ignores surrounding prose while
	// preserving the deterministic order and de-duplication of the direct
	// command path.
	if codes := extractSectionCodes(raw); len(codes) > 0 {
		return codes
	}
	// Structured callers may use opaque code fixtures (for example CODE1 and
	// CODE2) that do not have the catalog's dotted suffix yet. Split those
	// arguments independently as well; the normal execution boundary still
	// validates the resulting invocation before it can mutate anything.
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	targets := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		part = strings.ToUpper(strings.TrimSpace(strings.Trim(part, "，,;；")))
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		targets = append(targets, part)
	}
	return targets
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

func (h Handler) receiptForInvocation(ctx context.Context, ident store.Identity, invocation Invocation) (*store.CapabilityReceipt, error) {
	if requiresReceiptResolution(invocation) && h.Life == nil {
		return nil, fmt.Errorf("%w: Life @ USTC API unavailable: not configured", errCapabilityReceiptUnavailable)
	}
	if h.Life == nil {
		receipt := ReceiptForInvocation(invocation)
		return receiptPointer(receipt), nil
	}
	switch invocation.ID() {
	case CapabilitySubscription:
		if !firstArgIs(invocation.Args, "import") {
			receipt := ReceiptForInvocation(invocation)
			return receiptPointer(receipt), nil
		}
		codes := subscriptionImportTargets(invocation.Args[1:])
		if len(codes) != 1 {
			return nil, nil
		}
		sections, err := h.Life.SearchSections(ctx, codes[0], 5)
		if err != nil {
			return nil, fmt.Errorf("resolve subscription target %s: %w", codes[0], err)
		}
		section := matchingSectionCode(sections, codes[0])
		if section == nil {
			return nil, fmt.Errorf("%w: %s", errCapabilityReceiptTargetNotFound, codes[0])
		}
		return sectionReceipt(ReceiptActionSubscribe, section), nil
	case CapabilityUnsubscribeSectionByJWID:
		if len(invocation.Args) != 1 {
			return nil, nil
		}
		jwID, ok := parseIntArg(invocation.Args[0])
		if !ok {
			return nil, nil
		}
		section, err := h.Life.GetSectionByJwID(ctx, jwID)
		if err != nil {
			return nil, fmt.Errorf("resolve unsubscribe target %s: %w", invocation.Args[0], err)
		}
		if section == nil {
			return nil, fmt.Errorf("%w: %s", errCapabilityReceiptTargetNotFound, invocation.Args[0])
		}
		return sectionReceipt(ReceiptActionUnsubscribe, section), nil
	default:
		receipt := ReceiptForInvocation(invocation)
		return receiptPointer(receipt), nil
	}
}

func validateReceiptInvocation(invocation Invocation) error {
	switch invocation.ID() {
	case CapabilitySubscription:
		if !firstArgIs(invocation.Args, "import") {
			return nil
		}
		if len(subscriptionImportTargets(invocation.Args[1:])) == 0 {
			return fmt.Errorf("%w: subscription import requires a section code", errCapabilityInvalidInput)
		}
		if len(subscriptionImportTargets(invocation.Args[1:])) != 1 {
			return fmt.Errorf("%w: subscription import targets must be expanded before confirmation", errCapabilityMutationMustExpand)
		}
	case CapabilityUnsubscribeSectionByJWID:
		if len(invocation.Args) != 1 {
			return fmt.Errorf("%w: unsubscribe requires one section ID", errCapabilityInvalidInput)
		}
	}
	return nil
}

func requiresReceiptResolution(invocation Invocation) bool {
	switch invocation.ID() {
	case CapabilitySubscription:
		return firstArgIs(invocation.Args, "import") && len(subscriptionImportTargets(invocation.Args[1:])) == 1
	case CapabilityUnsubscribeSectionByJWID:
		return len(invocation.Args) == 1
	default:
		return false
	}
}

func matchingSectionCode(sections []map[string]any, code string) map[string]any {
	for _, section := range sections {
		if strings.EqualFold(strings.TrimSpace(sectionCode(section)), code) {
			return section
		}
	}
	return nil
}

func sectionReceipt(action string, section map[string]any) *store.CapabilityReceipt {
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
	if course == "" {
		course = sectionCode(section)
	}
	qualifiers := make([]string, 0, 2)
	if teacher != "" {
		qualifiers = append(qualifiers, teacher)
	}
	if semester != "" {
		qualifiers = append(qualifiers, semester)
	}
	subject := course
	if len(qualifiers) > 0 {
		subject += "（" + strings.Join(qualifiers, "，") + "）"
	}
	return &store.CapabilityReceipt{Action: action, Resource: ReceiptResourceSection, Subject: subject}
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
	case CapabilityHelp, CapabilityLogin, CapabilityPing, CapabilityStatus:
		return store.CapabilityReceipt{}
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
		return mutation(ReceiptActionSubscribe, ReceiptResourceSection, 1)
	case CapabilityUnsubscribeSectionByJWID:
		return mutation(ReceiptActionUnsubscribe, ReceiptResourceSection, 0)
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
	case CapabilitySettings:
		if invocation.Policy().Effect == EffectRead {
			return query("设置")
		}
		return mutation("更新", "设置", 0)
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
