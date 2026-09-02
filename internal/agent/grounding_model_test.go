package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

type groundingRecordingModel struct {
	options []*model.Options
}

func (m *groundingRecordingModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.record(opts)
	return schema.AssistantMessage("ok", nil), nil
}

func (m *groundingRecordingModel) Stream(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.record(opts)
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func (m *groundingRecordingModel) record(opts []model.Option) {
	options := model.GetCommonOptions(nil, opts...)
	options.AllowedToolNames = append([]string(nil), options.AllowedToolNames...)
	m.options = append(m.options, options)
}

func TestGroundingPolicyTargetsDomainAndVerificationRequests(t *testing.T) {
	private := store.Identity{ConversationType: "private"}
	for _, test := range []struct {
		name  string
		text  string
		ident store.Identity
		want  groundingPolicy
	}{
		{name: "current personal data", text: "我这学期选了哪些课", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "usage", text: "课表怎么用", ident: private, want: groundingPolicy{searchCommands: true}},
		{name: "verification", text: "你确定吗？", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "expanded verification", text: "请核实刚才的回答", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "result correctness", text: "这个结果正确吗", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "result plausibility", text: "这个结果靠谱吗", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "standalone correctness", text: "正确吗", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "standalone plausibility", text: "靠谱吗", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "shared personal data", text: "我这学期选了哪些课", ident: store.Identity{ConversationType: "group"}, want: groundingPolicy{privateUnavailable: true}},
		{name: "shared public course search", text: "帮我搜索数学分析课程", ident: store.Identity{ConversationType: "group"}, want: groundingPolicy{}},
		{name: "public group introduction", text: "介绍一下这个公开服务", ident: store.Identity{ConversationType: "group"}, want: groundingPolicy{}},
		{name: "casual chat", text: "你好", ident: private, want: groundingPolicy{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := groundingPolicyFor(test.text, test.ident); got != test.want {
				t.Fatalf("policy=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestGroundingModelRejectsDirectCapabilityResultWithoutSearchOnConfirmationResume(t *testing.T) {
	inner := &groundingRecordingModel{}
	wrapped := newGroundingModel(inner, groundingPolicy{searchCommands: true, executeCapability: true})
	capabilityCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "invoke-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"notify","arguments":["homework","on"]}`},
	}})
	capabilityResult := schema.ToolMessage("作业提醒：开", "invoke-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{capabilityCall, capabilityResult}); err != nil {
		t.Fatal(err)
	}
	grounder := wrapped.(*groundingModel)
	if grounder.hasRequiredEvidence() {
		t.Fatal("capability result without a command-search allow-list counted as evidence")
	}
	if _, returned := grounder.capabilityResult(); returned {
		t.Fatal("capability outside a command-search allow-list became a fallback result")
	}
}

func TestGroundingModelRecoversSearchAllowListOnConfirmationResume(t *testing.T) {
	inner := &groundingRecordingModel{}
	wrapped := newGroundingModel(inner, groundingPolicy{searchCommands: true, executeCapability: true})
	searchCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "search-1", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{"query":"作业提醒"}`},
	}})
	searchResult := schema.ToolMessage(`[{"id":"notify"}]`, "search-1", schema.WithToolName(commandSearchToolName))
	capabilityCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "invoke-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"notify","arguments":["homework","on"]}`},
	}})
	capabilityResult := schema.ToolMessage("作业提醒：开", "invoke-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{searchCall, searchResult, capabilityCall, capabilityResult}); err != nil {
		t.Fatal(err)
	}
	if !wrapped.(*groundingModel).hasRequiredEvidence() {
		t.Fatal("searched capability was not recovered as evidence on resume")
	}

	wrong := newGroundingModel(&groundingRecordingModel{}, groundingPolicy{searchCommands: true, executeCapability: true})
	wrongCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "wrong-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"semester"}`},
	}})
	wrongResult := schema.ToolMessage("2026年秋季学期", "wrong-1", schema.WithToolName(capabilityToolName))
	if _, err := wrong.Generate(t.Context(), []*schema.Message{searchCall, searchResult, wrongCall, wrongResult}); err != nil {
		t.Fatal(err)
	}
	if wrong.(*groundingModel).hasRequiredEvidence() {
		t.Fatal("wrong resumed capability counted as searched evidence")
	}
}

func TestGroundingModelOffersOnlySearchThenCapabilityThroughModelOptions(t *testing.T) {
	inner := &groundingRecordingModel{}
	wrapped := newGroundingModel(inner, groundingPolicy{searchCommands: true, executeCapability: true})
	user := schema.UserMessage("我这学期选了哪些课")
	toolOptions := []model.Option{model.WithTools([]*schema.ToolInfo{
		{Name: commandSearchToolName}, {Name: capabilityToolName}, {Name: "unrelated_tool"},
	})}
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertOnlyAllowedTool(t, inner.options[0], commandSearchToolName)

	searchCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "search-1", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{"query":"本学期课程"}`},
	}})
	// Leave ToolName empty to cover provider/tool-node transcripts that only
	// preserve the call ID. The wrapper resolves the honest prior tool call.
	searchResult := schema.ToolMessage(`[{"id":"schedule"}]`, "search-1")
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchCall, searchResult}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertOnlyAllowedTool(t, inner.options[1], capabilityToolName)

	capabilityCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "invoke-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"schedule"}`},
	}})
	capabilityResult := schema.ToolMessage("真实查询结果", "invoke-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchCall, searchResult, capabilityCall, capabilityResult}); err != nil {
		t.Fatal(err)
	}
	assertNoForcedTool(t, inner.options[2])
	grounder := wrapped.(*groundingModel)
	if !grounder.hasRequiredEvidence() {
		t.Fatal("capability tool result was not recorded as grounding evidence")
	}
}

func TestGroundingModelRejectsWrongOrFailedCapabilityAsEvidence(t *testing.T) {
	inner := &groundingRecordingModel{}
	wrapped := newGroundingModel(inner, groundingPolicy{searchCommands: true, executeCapability: true})
	grounder := wrapped.(*groundingModel)
	user := schema.UserMessage("你确定吗")
	searchCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "search-1", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{"query":"本学期课表"}`},
	}})
	searchResult := schema.ToolMessage(`[{"id":"schedule"}]`, "search-1", schema.WithToolName(commandSearchToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchCall, searchResult}); err != nil {
		t.Fatal(err)
	}

	wrongCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "wrong-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"semester"}`},
	}})
	wrongResult := schema.ToolMessage("2026年秋季学期", "wrong-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchCall, searchResult, wrongCall, wrongResult}); err != nil {
		t.Fatal(err)
	}
	if grounder.hasRequiredEvidence() {
		t.Fatal("a capability outside the command-search result counted as evidence")
	}

	outcomes := newToolOutcomeRegistry()
	outcomes.markError("failed-1")
	failedCtx := withToolOutcomes(t.Context(), outcomes)
	failedCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "failed-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"schedule"}`},
	}})
	failedResult := schema.ToolMessage("课表查不到：服务返回错误", "failed-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(failedCtx, []*schema.Message{user, searchCall, searchResult, failedCall, failedResult}); err != nil {
		t.Fatal(err)
	}
	if grounder.hasRequiredEvidence() {
		t.Fatal("a failed capability counted as evidence")
	}
	if result, returned := grounder.capabilityResult(); !returned || result != "课表查不到：服务返回错误" {
		t.Fatalf("failed capability fallback=(%q,%v)", result, returned)
	}

	successCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "success-1", Type: "function", Function: schema.FunctionCall{Name: capabilityToolName, Arguments: `{"capability":"schedule"}`},
	}})
	successResult := schema.ToolMessage("真实课表", "success-1", schema.WithToolName(capabilityToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchCall, searchResult, successCall, successResult}); err != nil {
		t.Fatal(err)
	}
	if !grounder.hasRequiredEvidence() {
		t.Fatal("the successful searched capability did not count as evidence")
	}
}

func TestCapabilityNotFoundIsLiteralFailureEvidence(t *testing.T) {
	if !capabilityOutcomeIsToolError(commands.CapabilityOutcomeNotFound) {
		t.Fatal("not-found capability result could be rewritten as a successful operation")
	}
}

func TestGroundedMessageForPersistenceDropsProvisionalTextButKeepsToolCall(t *testing.T) {
	message := schema.AssistantMessage("未经工具核实", []schema.ToolCall{{
		ID: "search-1", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{"query":"课表"}`},
	}})
	message.AssistantGenMultiContent = []schema.MessageOutputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "另一段未经核实的文字"},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{}},
	}
	sanitized := groundedMessageForPersistence(message, true)
	if sanitized == message || sanitized.Content != "" || len(sanitized.ToolCalls) != 1 || len(sanitized.AssistantGenMultiContent) != 1 ||
		sanitized.AssistantGenMultiContent[0].Type != schema.ChatMessagePartTypeImageURL {
		t.Fatalf("sanitized message=%#v", sanitized)
	}
	if message.Content == "" || len(message.AssistantGenMultiContent) != 2 {
		t.Fatal("grounded persistence mutated the provider message")
	}
}

func TestCommandSearchCapabilityIDsIncludeNestedExamples(t *testing.T) {
	ids := commandSearchCapabilityIDs(`[{
		"id":"course_search",
		"examples":[{"capability":"course_by_jw_id"}],
		"shortcuts":[{"capability":"teacher_by_id"}]
	}]`)
	for _, id := range []string{"course_search", "course_by_jw_id", "teacher_by_id"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("nested capability %q missing from %#v", id, ids)
		}
	}
}

func TestGroundingModelDoesNotForceExecutionForUsageOrEmptySearch(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy groundingPolicy
		result string
	}{
		{name: "usage", policy: groundingPolicy{searchCommands: true}, result: `[{"id":"schedule"}]`},
		{name: "empty search", policy: groundingPolicy{searchCommands: true, executeCapability: true}, result: `[]`},
		{name: "malformed search", policy: groundingPolicy{searchCommands: true, executeCapability: true}, result: `not-json`},
	} {
		t.Run(test.name, func(t *testing.T) {
			inner := &groundingRecordingModel{}
			wrapped := newGroundingModel(inner, test.policy)
			user := schema.UserMessage("课表怎么用")
			toolOptions := []model.Option{model.WithTools([]*schema.ToolInfo{{Name: commandSearchToolName}, {Name: capabilityToolName}})}
			if _, err := wrapped.Stream(t.Context(), []*schema.Message{user}, toolOptions...); err != nil {
				t.Fatal(err)
			}
			assertOnlyAllowedTool(t, inner.options[0], commandSearchToolName)
			searchResult := schema.ToolMessage(test.result, "search-1", schema.WithToolName(commandSearchToolName))
			if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, searchResult}); err != nil {
				t.Fatal(err)
			}
			assertNoForcedTool(t, inner.options[1])
		})
	}
}

func assertOnlyAllowedTool(t *testing.T, options *model.Options, name string) {
	t.Helper()
	toolNames := make([]string, 0, len(options.Tools))
	for _, tool := range options.Tools {
		if tool != nil {
			toolNames = append(toolNames, tool.Name)
		}
	}
	if options.ToolChoice == nil || *options.ToolChoice != schema.ToolChoiceAllowed || !slices.Equal(toolNames, []string{name}) || len(options.AllowedToolNames) != 0 {
		t.Fatalf("tool choice=%#v tools=%#v allowed=%#v want auto with only %q", options.ToolChoice, toolNames, options.AllowedToolNames, name)
	}
}

func assertNoForcedTool(t *testing.T, options *model.Options) {
	t.Helper()
	if options.ToolChoice != nil || len(options.AllowedToolNames) != 0 {
		t.Fatalf("unexpected tool choice=%#v allowed=%#v", options.ToolChoice, options.AllowedToolNames)
	}
}
