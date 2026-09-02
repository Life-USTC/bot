package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestTypedOutcomeUsesExplicitDomainMarkers(t *testing.T) {
	ctx := context.Background()
	invocation := Invocation{Name: "example"}

	result := textExecutor(func(h Handler, _ context.Context, _ store.Identity, _ []string) string {
		return h.commandError("领域失败：", errors.New("backend unavailable"))
	})(Handler{execution: &capabilityExecutionState{}}, ctx, store.Identity{}, invocation)
	if result.Status != CapabilityOutcomeFailed || result.Response.Text != "领域失败：服务暂时不可用，请稍后再试" {
		t.Fatalf("explicit failure = %#v", result)
	}

	result = textExecutor(func(Handler, context.Context, store.Identity, []string) string {
		return "失败：这是一条合法的领域结果"
	})(Handler{execution: &capabilityExecutionState{}}, ctx, store.Identity{}, invocation)
	if result.Status != CapabilityOutcomeSuccess || result.Response.Text == "" {
		t.Fatalf("text was used to infer status: %#v", result)
	}
}

func TestHandleOutcomeReportsFailedDomainCallWithoutChangingDirectText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "backend exploded", http.StatusBadGateway)
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	outcome, handled := handler.HandleOutcome(context.Background(), Input{Text: "ping"})
	if !handled || outcome.Status != CapabilityOutcomeFailed {
		t.Fatalf("outcome = %#v, handled = %v", outcome, handled)
	}
	if outcome.Response.Text == "" || outcome.Response.Text == string(outcome.Status) {
		t.Fatalf("domain text was replaced by status: %#v", outcome.Response)
	}
	text, ok := handler.Handle(context.Background(), Input{Text: "ping"})
	if !ok || text != outcome.Response.Text {
		t.Fatalf("direct response = %q, ok = %v; outcome = %#v", text, ok, outcome)
	}
}

func TestCapabilitySearchRanksExactFormsAndFiltersSharedPrivateExamples(t *testing.T) {
	first := SearchCapabilityDocumentation("课表", CapabilitySearchOptions{})
	second := SearchCapabilityDocumentation("课表", CapabilitySearchOptions{})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("search is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(first) == 0 || first[0].ID != CapabilitySchedule {
		t.Fatalf("schedule was not the best match: %#v", first)
	}

	shared := SearchCapabilityDocumentation("校车", CapabilitySearchOptions{SharedConversation: true})
	if len(shared) == 0 || shared[0].ID != CapabilityBus {
		t.Fatalf("shared bus search = %#v", shared)
	}
	for _, result := range shared {
		for _, example := range result.Examples {
			if example.DataScope != DataScopePublic {
				t.Fatalf("private example leaked into shared search: %#v", example)
			}
		}
	}
	for _, result := range SearchCapabilityDocumentation("课表", CapabilitySearchOptions{SharedConversation: true}) {
		if result.DataScope != DataScopePublic {
			t.Fatalf("private capability leaked into shared search: %#v", result)
		}
	}
	docs := SearchCapabilityDocumentation("待办 完成", CapabilitySearchOptions{})
	var todo CapabilityDocumentation
	for _, documentation := range docs {
		if documentation.ID == CapabilityTodo {
			todo = documentation
			break
		}
	}
	if todo.ID == "" {
		t.Fatal("todo mutation documentation missing")
	}
	foundMutation := false
	for _, example := range todo.Examples {
		if strings.Contains(example.Command, "完成") {
			if example.Effect != EffectWrite || example.DataScope != DataScopeUserPrivate {
				t.Fatalf("todo mutation metadata = %#v", example)
			}
			foundMutation = true
		}
	}
	if !foundMutation {
		t.Fatal("todo mutation example missing")
	}
	bus, ok := ParseInvocation("校车 偏好 路线 东区 西区")
	if !ok {
		t.Fatal("bus preference mutation was rejected")
	}
	if policy := bus.Policy(); policy.Effect != EffectWrite || policy.DataScope != DataScopeUserPrivate {
		t.Fatalf("bus mutation metadata = %#v", policy)
	}
}

func TestCapabilitySearchUnderstandsUnsegmentedChineseIntent(t *testing.T) {
	docs := SearchCapabilityDocumentation("请帮我打开作业通知", CapabilitySearchOptions{})
	if len(docs) == 0 || docs[0].ID != CapabilityNotify {
		t.Fatalf("notification search = %#v", docs)
	}

	docs = SearchCapabilityDocumentation("我想取消课程订阅", CapabilitySearchOptions{})
	if len(docs) == 0 || docs[0].ID != CapabilitySubscription {
		t.Fatalf("subscription search = %#v", docs)
	}
	removeExample := false
	for _, example := range docs[0].Examples {
		if len(example.Arguments) == 2 && example.Arguments[0] == "remove" {
			removeExample = example.Capability == CapabilitySubscription && example.Effect == EffectDestructive
		}
	}
	if !removeExample {
		t.Fatalf("subscription search lacks executable destructive remove example: %#v", docs[0])
	}

	docs = SearchCapabilityDocumentation("打开 作业通知 查看 作业 通知 USTC", CapabilitySearchOptions{})
	if len(docs) == 0 || docs[0].ID != CapabilityNotify {
		t.Fatalf("multi-hint notification search = %#v", docs)
	}

	docs = SearchCapabilityDocumentation("作业通知 订阅 提醒 开启 打开 通知设置 homework notification subscribe enable", CapabilitySearchOptions{})
	if len(docs) == 0 || docs[0].ID != CapabilityNotify {
		t.Fatalf("bilingual synonym search = %#v", docs)
	}
}

func TestMutationExpansionSplitsIndependentTargets(t *testing.T) {
	subscription := expandTestMutation(t, CapabilitySubscription, []string{"import", "CODE1.01", "CODE2.02"})
	if len(subscription) != 2 {
		t.Fatalf("subscription expansion = %#v", subscription)
	}
	if got := subscription[0].CanonicalCommand(); got != "subscription import CODE1.01" {
		t.Fatalf("first subscription invocation = %q", got)
	}
	if got := subscription[1].CanonicalCommand(); got != "subscription import CODE2.02" {
		t.Fatalf("second subscription invocation = %q", got)
	}
	removals := expandTestMutation(t, CapabilitySubscription, []string{"remove", "CODE1.01", "CODE2.02"})
	if len(removals) != 2 || removals[0].CanonicalCommand() != "subscription remove CODE1.01" || removals[1].CanonicalCommand() != "subscription remove CODE2.02" {
		t.Fatalf("subscription removal expansion = %#v", removals)
	}
	todo := expandTestMutation(t, CapabilityTodo, []string{"delete", "1,2,3"})
	if len(todo) != 3 || todo[0].CanonicalCommand() != "todo delete 1" || todo[2].CanonicalCommand() != "todo delete 3" {
		t.Fatalf("todo expansion = %#v", todo)
	}
	read := expandTestMutation(t, CapabilityCourse, []string{"数学分析"})
	if len(read) != 1 || read[0].CanonicalCommand() != "course 数学分析" {
		t.Fatalf("read expansion = %#v", read)
	}
}

func expandTestMutation(t *testing.T, id CapabilityID, args []string) []Invocation {
	t.Helper()
	invocation, ok := NewInvocation(id, args)
	if !ok {
		t.Fatalf("invalid test invocation %s %#v", id, args)
	}
	return ExpandMutationInvocations(invocation)
}

func TestMutationReceiptPreflightRequiresOneResolvedTarget(t *testing.T) {
	handler := Handler{}
	_, err := handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"import", "CODE1.01", "CODE2.02"})
	if !errors.Is(err, errCapabilityMutationMustExpand) {
		t.Fatalf("unexpanded receipt preflight error = %v", err)
	}

	_, err = handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"import", "CODE1.01"})
	if !errors.Is(err, errCapabilityReceiptUnavailable) {
		t.Fatalf("unresolved receipt preflight error = %v", err)
	}

	_, err = handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"remove", "CODE1.01"})
	if !errors.Is(err, errCapabilityReceiptUnavailable) {
		t.Fatalf("unresolved remove receipt preflight error = %v", err)
	}
}

func TestMutationExamplesCarryWriteEffects(t *testing.T) {
	tests := []struct {
		command string
		effect  CapabilityEffect
	}{
		{command: "订阅 导入 CODE1.01", effect: EffectWrite},
		{command: "订阅 取消 CODE1.01", effect: EffectDestructive},
		{command: "待办 添加 写报告", effect: EffectWrite},
		{command: "待办 完成 1", effect: EffectWrite},
		{command: "待办 恢复 1", effect: EffectWrite},
		{command: "待办 删除 1", effect: EffectDestructive},
		{command: "待办 更新 1 标题 新标题", effect: EffectWrite},
		{command: "作业 完成 1", effect: EffectWrite},
		{command: "作业 恢复 1", effect: EffectWrite},
		{command: "通知 作业 开", effect: EffectWrite},
		{command: "校车 偏好 路线 东区 西区", effect: EffectWrite},
		{command: "账户 退出", effect: EffectDestructive},
	}
	for _, test := range tests {
		invocation, ok := ParseInvocation(test.command)
		if !ok {
			t.Fatalf("%q did not parse", test.command)
		}
		policy := invocation.Policy()
		if policy.Effect != test.effect || policy.DataScope != DataScopeUserPrivate {
			t.Errorf("%q policy = %#v", test.command, policy)
		}
	}
}

func TestBareSubscriptionCodeNormalizesToConfirmedImport(t *testing.T) {
	invocation, ok := NewInvocation(CapabilitySubscription, []string{"CONT5103P.01"})
	if !ok {
		t.Fatal("bare subscription code was rejected")
	}
	if invocation.CanonicalCommand() != "subscription import CONT5103P.01" || invocation.Policy().Effect != EffectWrite {
		t.Fatalf("bare subscription invocation=%#v policy=%#v", invocation, invocation.Policy())
	}
}

func TestCapabilityDocumentationExamplesAreExecutableAndPolicyBound(t *testing.T) {
	for _, usage := range CapabilityUsages() {
		for _, example := range usage.Examples {
			invocation, ok := example.Invocation()
			if !ok {
				t.Fatalf("%s example is not executable: %#v", usage.ID, example)
			}
			policy := invocation.Policy()
			if example.Effect != policy.Effect || example.DataScope != policy.DataScope {
				t.Fatalf("%s example policy = %#v, invocation = %#v", usage.ID, example, policy)
			}
		}
	}
}

func TestDescribeInvocationBuildsHostReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"id":42,"jwId":202602,"nameCn":"2026年秋季学期"}`))
		case "/api/catalog/sections":
			if r.URL.Query().Get("search") != "CODE1.01" || r.URL.Query().Get("semesterId") != "42" {
				t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{"code":"CODE1.01","id":12,"course":{"namePrimary":"线性代数"},"teacher":{"namePrimary":"张老师"},"semester":{"namePrimary":"2026年秋季学期"}}]}`))
		default:
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	description, err := handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"import", "CODE1.01"})
	if err != nil {
		t.Fatal(err)
	}
	if description.Receipt == nil {
		t.Fatal("receipt = nil")
	}
	if description.Receipt.Action != ReceiptActionSubscribe || description.Receipt.Resource != ReceiptResourceSection {
		t.Fatalf("receipt = %#v", description.Receipt)
	}
	if got := description.Receipt.Subject; got != "线性代数（张老师，2026年秋季学期）" {
		t.Fatalf("receipt subject = %#v", got)
	}
	if got := description.Invocation.CanonicalCommand(); got != "subscription import CODE1.01 semester_id 42" {
		t.Fatalf("frozen subscription invocation = %q", got)
	}
	description, err = handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"remove", "CODE1.01"})
	if err != nil || description.Receipt == nil || description.Receipt.Action != ReceiptActionUnsubscribe ||
		description.Receipt.Subject != "线性代数（张老师，2026年秋季学期）" {
		t.Fatalf("unsubscribe description=%#v err=%v", description, err)
	}
	if got := description.Invocation.CanonicalCommand(); got != "subscription remove CODE1.01 semester_id 42" {
		t.Fatalf("frozen unsubscribe invocation = %q", got)
	}
}

func TestDescribeInvocationRejectsAmbiguousCurrentSemesterCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"id":42,"nameCn":"2026年秋季学期"}`))
		case "/api/catalog/sections":
			_, _ = w.Write([]byte(`{"data":[
				{"code":"CODE1.01","id":12,"course":{"namePrimary":"线性代数"}},
				{"code":"code1.01","id":13,"course":{"namePrimary":"另一门课"}}
			]}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	_, err := handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"remove", "CODE1.01"})
	if !errors.Is(err, errCapabilityReceiptTargetAmbiguous) {
		t.Fatalf("ambiguous receipt error=%v", err)
	}
	result, ok := CapabilityPreflightFailure(CapabilitySubscription, err)
	if !ok || !strings.Contains(result, "当前学期对应多个结果") || !strings.Contains(result, "没有执行任何操作") {
		t.Fatalf("ambiguous receipt result=%q ok=%v", result, ok)
	}
}

func TestDescribeInvocationRejectsIncompleteSectionReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"id":42,"nameCn":"2026年秋季学期"}`))
		case "/api/catalog/sections":
			_, _ = w.Write([]byte(`{"data":[{"code":"CODE1.01","id":12,"course":{"namePrimary":"线性代数"},"teachers":[]}]}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	_, err := handler.DescribeInvocation(context.Background(), Input{}, CapabilitySubscription, []string{"remove", "CODE1.01"})
	if !errors.Is(err, errCapabilityReceiptIncomplete) {
		t.Fatalf("incomplete receipt error=%v", err)
	}
	result, ok := CapabilityPreflightFailure(CapabilitySubscription, err)
	if !ok || !strings.Contains(result, "完整的课程名称、教师和学期信息") || !strings.Contains(result, "没有执行任何操作") {
		t.Fatalf("incomplete receipt result=%q ok=%v", result, ok)
	}
}

func TestSectionSemesterMustNotConflictWithFrozenTarget(t *testing.T) {
	if sectionMatchesSemesterID(map[string]any{"semesterId": 43}, 42) {
		t.Fatal("mismatched top-level semester was accepted")
	}
	if sectionMatchesSemesterID(map[string]any{"semester": map[string]any{"id": 43}}, 42) {
		t.Fatal("mismatched nested semester was accepted")
	}
	if !sectionMatchesSemesterID(map[string]any{"semesterId": 42}, 42) ||
		!sectionMatchesSemesterID(map[string]any{}, 42) {
		t.Fatal("current or filter-constrained semester was rejected")
	}
	result, ok := CapabilityPreflightFailure(CapabilitySubscription, errCapabilityReceiptInconsistent)
	if !ok || !strings.Contains(result, "与当前学期不一致") || !strings.Contains(result, "没有执行任何操作") {
		t.Fatalf("inconsistent semester result=%q ok=%v", result, ok)
	}
}
