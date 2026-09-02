package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

type groundingRecordingModel struct {
	options []*model.Options
}

type groundingRetryModel struct {
	calls  int
	inputs [][]*schema.Message
}

func (m *groundingRetryModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.response(input, opts), nil
}

func (m *groundingRetryModel) Stream(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response(input, opts)}), nil
}

func (m *groundingRetryModel) response(input []*schema.Message, opts []model.Option) *schema.Message {
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.calls == 1 {
		return schema.AssistantMessage("我直接回答", nil)
	}
	name := onlyOfferedTool(opts)
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "retry-call", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`},
	}})
}

func (m *groundingRecordingModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.record(opts)
	if name := onlyOfferedTool(opts); name != "" {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`},
		}}), nil
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (m *groundingRecordingModel) Stream(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.record(opts)
	message := schema.AssistantMessage("ok", nil)
	if name := onlyOfferedTool(opts); name != "" {
		message = schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: `{}`},
		}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func onlyOfferedTool(opts []model.Option) string {
	options := model.GetCommonOptions(nil, opts...)
	if options.ToolChoice == nil || *options.ToolChoice != schema.ToolChoiceAllowed || len(options.Tools) != 1 || options.Tools[0] == nil {
		return ""
	}
	return options.Tools[0].Name
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
		{name: "shared public course search", text: "帮我搜索数学分析课程", ident: store.Identity{ConversationType: "group"}, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "shared public course query with fuzzy private matches", text: "帮我查数学分析课程", ident: store.Identity{ConversationType: "group"}, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "public bus facts", text: "高新区到东区校车", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true}},
		{name: "second classroom platform", text: "你能查询第二课堂平台的活动吗", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}},
		{name: "second classroom event list uses MCP", text: "请查询目前可以报名的第二课堂活动，列出前 3 个", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}},
		{name: "second classroom short name", text: "但是你现在是不是能搜二课了", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}},
		{name: "verify second classroom result", text: "请核实刚才第二课堂的结果", ident: private, want: groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}},
		{name: "second period is not young event", text: "二课几点开始", ident: private, want: groundingPolicy{}},
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

func TestGroundingModelForcesEmptyBotSearchThenCampusSearchAndCall(t *testing.T) {
	inner := &groundingRecordingModel{}
	policy := groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}
	wrapped := newGroundingModel(inner, policy)
	grounder := wrapped.(*groundingModel)
	user := schema.UserMessage("查询第二课堂活动")
	toolOptions := []model.Option{model.WithTools([]*schema.ToolInfo{
		{Name: commandSearchToolName}, {Name: capabilityToolName},
		{Name: campusSearchToolName}, {Name: campusCallToolName},
	})}

	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertOnlyAllowedTool(t, inner.options[0], commandSearchToolName)

	commandSearchCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "bot-search", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{"query":"第二课堂活动"}`},
	}})
	commandSearchResult := schema.ToolMessage(`[]`, "bot-search", schema.WithToolName(commandSearchToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, commandSearchCall, commandSearchResult}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertOnlyAllowedTool(t, inner.options[1], campusSearchToolName)
	if grounder.hasRequiredEvidence() {
		t.Fatal("an empty Bot search alone satisfied the campus-data requirement")
	}

	campusSearchCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "campus-search", Type: "function", Function: schema.FunctionCall{Name: campusSearchToolName, Arguments: `{"query":"二课活动"}`},
	}})
	campusSearchResult := schema.ToolMessage(`[{"name":"catalog_young_event_list"}]`, "campus-search", schema.WithToolName(campusSearchToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, commandSearchCall, commandSearchResult, campusSearchCall, campusSearchResult}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertOnlyAllowedTool(t, inner.options[2], campusCallToolName)
	if grounder.hasRequiredEvidence() {
		t.Fatal("campus documentation without a read result satisfied grounding")
	}

	campusCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "campus-call", Type: "function", Function: schema.FunctionCall{Name: campusCallToolName, Arguments: `{"name":"catalog_young_event_list","arguments":{"active":true}}`},
	}})
	campusResult := schema.ToolMessage(`{"data":[{"name":"第二课堂示例活动"}]}`, "campus-call", schema.WithToolName(campusCallToolName))
	if _, err := wrapped.Generate(t.Context(), []*schema.Message{user, commandSearchCall, commandSearchResult, campusSearchCall, campusSearchResult, campusCall, campusResult}, toolOptions...); err != nil {
		t.Fatal(err)
	}
	assertNoForcedTool(t, inner.options[3])
	if !grounder.hasRequiredEvidence() {
		t.Fatal("approved campus tool result did not satisfy grounding")
	}
	if result, returned := grounder.groundedToolResult(); !returned || !strings.Contains(result, "第二课堂示例活动") {
		t.Fatalf("campus fallback result = (%q, %v)", result, returned)
	}
}

func TestGroundingModelRetriesPlainTextOnceWithTransientToolOnlyInstruction(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "generate"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			inner := &groundingRetryModel{}
			wrapped := newGroundingModel(inner, groundingPolicy{searchCommands: true, executeCapability: true})
			input := []*schema.Message{schema.UserMessage("高新区到东区校车")}
			options := []model.Option{model.WithTools([]*schema.ToolInfo{{Name: commandSearchToolName}, {Name: capabilityToolName}})}
			var response *schema.Message
			var err error
			if stream {
				var output *schema.StreamReader[*schema.Message]
				output, err = wrapped.Stream(t.Context(), input, options...)
				if err == nil {
					response, err = schema.ConcatMessageStream(output)
				}
			} else {
				response, err = wrapped.Generate(t.Context(), input, options...)
			}
			if err != nil {
				t.Fatal(err)
			}
			if inner.calls != 2 || !callsRequiredTool(response, commandSearchToolName) {
				t.Fatalf("calls=%d response=%#v", inner.calls, response)
			}
			if len(input) != 1 || len(inner.inputs[1]) != 2 || inner.inputs[1][0].Role != schema.System ||
				!strings.Contains(inner.inputs[1][0].Content, commandSearchToolName) || inner.inputs[1][1] != input[0] {
				t.Fatalf("original=%#v retry=%#v", input, inner.inputs[1])
			}
		})
	}
}

func TestGroundingRetryInputKeepsToolCallAdjacentToItsResult(t *testing.T) {
	user := schema.UserMessage("高新区到东区校车")
	call := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "search", Type: "function", Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: `{}`},
	}})
	result := schema.ToolMessage(`[{"id":"bus"}]`, "search", schema.WithToolName(commandSearchToolName))
	input := []*schema.Message{schema.SystemMessage("base"), user, call, result}
	retry := groundingRetryInput(input, capabilityToolName)
	if len(retry) != 5 || retry[0] != input[0] || retry[1].Role != schema.System || retry[2] != user || retry[3] != call || retry[4] != result {
		t.Fatalf("retry input broke current-turn tool transcript: %#v", retry)
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
	if _, returned := grounder.groundedToolResult(); returned {
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
	if result, returned := grounder.groundedToolResult(); !returned || result != "课表查不到：服务返回错误" {
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
	ids, valid := commandSearchCapabilityIDs(`[{
		"id":"course_search",
		"examples":[{"capability":"course_by_jw_id"}],
		"shortcuts":[{"capability":"teacher_by_id"}]
	}]`)
	if !valid {
		t.Fatal("valid command-search result was rejected")
	}
	for _, id := range []string{"course_search", "course_by_jw_id", "teacher_by_id"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("nested capability %q missing from %#v", id, ids)
		}
	}
}

func TestGroundingModelDoesNotForceExecutionForUsageOrEmptySearch(t *testing.T) {
	for _, test := range []struct {
		name         string
		policy       groundingPolicy
		result       string
		wantEvidence bool
	}{
		{name: "usage", policy: groundingPolicy{searchCommands: true}, result: `[{"id":"schedule"}]`, wantEvidence: true},
		{name: "empty search", policy: groundingPolicy{searchCommands: true, executeCapability: true}, result: `[]`, wantEvidence: true},
		{name: "malformed search", policy: groundingPolicy{searchCommands: true, executeCapability: true}, result: `not-json`, wantEvidence: false},
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
			if got := wrapped.(*groundingModel).hasRequiredEvidence(); got != test.wantEvidence {
				t.Fatalf("hasRequiredEvidence() = %v, want %v", got, test.wantEvidence)
			}
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
