package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

const (
	commandSearchToolName = "search_bot_commands"
	capabilityToolName    = "invoke_bot_capability"
	campusSearchToolName  = "search_campus_tools"
	campusCallToolName    = "call_campus_tool"
)

type groundingPolicy struct {
	searchCommands     bool
	executeCapability  bool
	requireCampusTool  bool
	privateUnavailable bool
}

func groundingInstruction(base string, policy groundingPolicy) string {
	base = strings.TrimSpace(base)
	if !policy.searchCommands {
		return base
	}
	requirement := "CURRENT TURN REQUIREMENT: Call search_bot_commands before giving the user an answer. Do not answer from chat history or general knowledge."
	if policy.executeCapability {
		requirement += " If the search returns documentation, call invoke_bot_capability with one exact documented capability and use only its result. A plain-text factual answer before that capability result is invalid. If the search returns an empty JSON array, never substitute an unrelated Bot capability; for a read-only campus lookup, search search_campus_tools before deciding that the overall request is unsupported."
	}
	if policy.requireCampusTool {
		requirement += " For this known supplementary campus-data request, an empty Bot search must be followed by search_campus_tools and, when that search returns documentation, call_campus_tool. Do not give a factual or availability answer without that evidence."
	}
	return base + "\n" + requirement
}

func groundingPolicyFor(text string, ident store.Identity) groundingPolicy {
	text = strings.TrimSpace(text)
	if text == "" {
		return groundingPolicy{}
	}
	if !store.IsSharedConversation(ident) && asksForYoungEventLookup(text) {
		return groundingPolicy{searchCommands: true, executeCapability: true, requireCampusTool: true}
	}
	verification := asksToVerifyPreviousAnswer(text)
	if verification {
		return groundingPolicy{searchCommands: true, executeCapability: true}
	}
	allDocumentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: 5})
	if store.IsSharedConversation(ident) {
		if explicitlyNamesPrivateCapability(text, allDocumentation) || asksForOwnData(text) && containsPrivateCapability(allDocumentation) {
			return groundingPolicy{privateUnavailable: true}
		}
	}
	documentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{
		SharedConversation: store.IsSharedConversation(ident),
		Limit:              5,
	})
	if len(documentation) == 0 {
		return groundingPolicy{}
	}
	if asksForCommandUsage(text) {
		return groundingPolicy{searchCommands: true}
	}
	for _, match := range documentation {
		if match.DataScope == commands.DataScopeUserPrivate {
			return groundingPolicy{searchCommands: true, executeCapability: true}
		}
	}
	// Public summaries are intentionally searchable, but a fuzzy summary match
	// alone must not hijack casual conversation. Require a concrete capability
	// name, title, ID, or documented example argument in the user's request.
	if explicitlyNamesCapability(text, documentation) {
		return groundingPolicy{searchCommands: true, executeCapability: true}
	}
	return groundingPolicy{}
}

func asksForYoungEventLookup(text string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	if strings.Contains(compact, "第二课堂") {
		return true
	}
	if !strings.Contains(compact, "二课") {
		return false
	}
	for _, marker := range []string{"活动", "平台", "报名", "查询", "搜索", "查", "搜", "支持", "能"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func explicitlyNamesPrivateCapability(text string, documentation []commands.CapabilityDocumentation) bool {
	for _, item := range documentation {
		if item.DataScope == commands.DataScopeUserPrivate && explicitlyNamesCapability(text, []commands.CapabilityDocumentation{item}) {
			return true
		}
	}
	return false
}

func containsPrivateCapability(documentation []commands.CapabilityDocumentation) bool {
	for _, item := range documentation {
		if item.DataScope == commands.DataScopeUserPrivate {
			return true
		}
	}
	return false
}

func asksForOwnData(text string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	for _, marker := range []string{"我的", "我这", "我本", "我选", "我订阅", "我关注", "我有", "本人", "自己的"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func explicitlyNamesCapability(text string, documentation []commands.CapabilityDocumentation) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	for _, item := range documentation {
		markers := append(append([]string(nil), item.Forms...), item.Title, string(item.ID))
		for _, example := range append(append([]commands.CapabilityUsageExample(nil), item.Examples...), item.Shortcuts...) {
			markers = append(markers, strings.Join(example.Arguments, ""))
		}
		for _, marker := range markers {
			marker = strings.ToLower(strings.Join(strings.Fields(marker), ""))
			if marker != "" && strings.Contains(compact, marker) {
				return true
			}
		}
	}
	return false
}

func asksForCommandUsage(text string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	for _, marker := range []string{
		"怎么用", "如何用", "怎么发", "如何发", "用法", "帮助", "命令",
		"支持哪些", "能做什么", "有什么功能", "怎么查询", "如何查询",
	} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func asksToVerifyPreviousAnswer(text string) bool {
	compact := strings.ToLower(strings.Trim(strings.Join(strings.Fields(text), ""), "，,。！？!?"))
	for _, marker := range []string{
		"你确定吗", "你确定", "确定吗", "确定", "这对吗", "对吗", "对不对",
		"真的吗", "真实吗", "准确吗", "正确吗", "正确", "靠谱吗", "靠谱",
		"有错吗", "错了吗", "核实一下", "确认一下",
	} {
		if compact == marker {
			return true
		}
	}
	referencesPrevious := false
	for _, marker := range []string{"这个", "结果", "数据", "刚才", "上面", "之前", "回答", "回复"} {
		if strings.Contains(compact, marker) {
			referencesPrevious = true
			break
		}
	}
	if !referencesPrevious {
		return false
	}
	for _, marker := range []string{"确定", "核实", "确认", "查证", "验证", "准确", "真实", "正确", "靠谱", "对吗", "对不对", "错吗", "有误", "有没有错", "再查"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

// groundingModel keeps the provider transcript honest while enforcing the
// search-then-invoke contract through the provider's native tool_choice. It
// never fabricates an assistant tool call or rewrites a provider response.
type groundingModel struct {
	inner  model.BaseChatModel
	policy groundingPolicy

	mu                       sync.Mutex
	offeredCommandSearch     bool
	offeredCapability        bool
	offeredCampusSearch      bool
	offeredCampusCall        bool
	observedCommandSearch    bool
	observedCapabilityResult bool
	observedCapabilityReturn bool
	observedCampusSearch     bool
	observedCampusResult     bool
	observedCampusReturn     bool
	lastCapabilityResult     string
	lastCampusResult         string
	allowedCapabilities      map[string]struct{}
	allowedCampusTools       map[string]struct{}
}

func newGroundingModel(inner model.BaseChatModel, policy groundingPolicy) model.BaseChatModel {
	if inner == nil || !policy.searchCommands {
		return inner
	}
	return &groundingModel{inner: inner, policy: policy}
}

func (m *groundingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	toolName, grounded := m.withGroundingTools(ctx, input, opts)
	response, err := m.inner.Generate(ctx, input, grounded...)
	if err != nil || toolName == "" || callsRequiredTool(response, toolName) {
		return response, err
	}
	return m.inner.Generate(ctx, groundingRetryInput(input, toolName), grounded...)
}

func (m *groundingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	toolName, grounded := m.withGroundingTools(ctx, input, opts)
	stream, err := m.inner.Stream(ctx, input, grounded...)
	if err != nil {
		return nil, err
	}
	response, err := schema.ConcatMessageStream(stream)
	if err != nil || toolName == "" || callsRequiredTool(response, toolName) {
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]*schema.Message{response}), nil
	}
	stream, err = m.inner.Stream(ctx, groundingRetryInput(input, toolName), grounded...)
	if err != nil {
		return nil, err
	}
	response, err = schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}

func (m *groundingModel) withGroundingTools(ctx context.Context, input []*schema.Message, opts []model.Option) (string, []model.Option) {
	toolName := m.nextGroundingTool(ctx, input)
	if toolName == "" {
		return "", opts
	}
	options := model.GetCommonOptions(nil, opts...)
	tools := make([]*schema.ToolInfo, 0, 1)
	for _, candidate := range options.Tools {
		if candidate != nil && candidate.Name == toolName {
			tools = append(tools, candidate)
			break
		}
	}
	grounded := make([]model.Option, len(opts), len(opts)+2)
	copy(grounded, opts)
	// Kimi K3 always thinks and rejects a specified/required tool choice. Give
	// the model only the required next tool and keep tool_choice=auto; the host
	// separately refuses to deliver a factual answer without the real result.
	return toolName, append(grounded, model.WithTools(tools), model.WithToolChoice(schema.ToolChoiceAllowed))
}

func callsRequiredTool(message *schema.Message, name string) bool {
	if message == nil {
		return false
	}
	for _, call := range message.ToolCalls {
		if call.Function.Name == name {
			return true
		}
	}
	return false
}

func groundingRetryInput(input []*schema.Message, toolName string) []*schema.Message {
	retry := make([]*schema.Message, 0, len(input)+1)
	insertAt := len(input)
	for index := len(input) - 1; index >= 0; index-- {
		if input[index] != nil && input[index].Role == schema.User {
			insertAt = index
			break
		}
	}
	retry = append(retry, input[:insertAt]...)
	retry = append(retry, schema.SystemMessage("Your entire response must be a call to "+toolName+". Do not answer with text."))
	retry = append(retry, input[insertAt:]...)
	return retry
}

func (m *groundingModel) nextGroundingTool(ctx context.Context, input []*schema.Message) string {
	if len(input) == 0 {
		return ""
	}
	last := input[len(input)-1]
	if last == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if last.Role == schema.User && !m.offeredCommandSearch {
		m.offeredCommandSearch = true
		return commandSearchToolName
	}
	if last.Role == schema.Tool && toolNameForResult(input, last) == commandSearchToolName {
		if toolOutcomesFromContext(ctx).isError(last.ToolCallID) {
			return ""
		}
		m.allowedCapabilities, m.observedCommandSearch = commandSearchCapabilityIDs(last.Content)
		if m.policy.executeCapability && !m.offeredCapability && len(m.allowedCapabilities) > 0 {
			m.offeredCapability = true
			return capabilityToolName
		}
		if m.policy.requireCampusTool && m.observedCommandSearch && len(m.allowedCapabilities) == 0 && !m.offeredCampusSearch {
			m.offeredCampusSearch = true
			return campusSearchToolName
		}
	}
	if last.Role == schema.Tool && toolNameForResult(input, last) == capabilityToolName {
		capability := capabilityIDForResult(input, last)
		if !m.observedCommandSearch {
			m.allowedCapabilities, m.observedCommandSearch = recoverCommandSearchCapabilityIDs(input)
		}
		allowed := false
		if _, found := m.allowedCapabilities[capability]; found {
			allowed = true
		}
		if allowed {
			m.observedCapabilityReturn = true
			m.lastCapabilityResult = strings.TrimSpace(last.Content)
			if !toolOutcomesFromContext(ctx).isError(last.ToolCallID) {
				m.observedCapabilityResult = true
			}
		}
	}
	if last.Role == schema.Tool && toolNameForResult(input, last) == campusSearchToolName {
		if toolOutcomesFromContext(ctx).isError(last.ToolCallID) {
			return ""
		}
		m.allowedCampusTools, m.observedCampusSearch = campusSearchToolNames(last.Content)
		if m.policy.requireCampusTool && m.observedCampusSearch && len(m.allowedCampusTools) > 0 && !m.offeredCampusCall {
			m.offeredCampusCall = true
			return campusCallToolName
		}
	}
	if last.Role == schema.Tool && toolNameForResult(input, last) == campusCallToolName {
		name := campusCallNameForResult(input, last)
		if _, allowed := m.allowedCampusTools[name]; allowed {
			m.observedCampusReturn = true
			m.lastCampusResult = strings.TrimSpace(last.Content)
			if !toolOutcomesFromContext(ctx).isError(last.ToolCallID) {
				m.observedCampusResult = true
			}
		}
	}
	return ""
}

func (m *groundingModel) groundedToolResult() (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observedCampusReturn {
		return m.lastCampusResult, true
	}
	return m.lastCapabilityResult, m.observedCapabilityReturn
}

func (m *groundingModel) hasRequiredEvidence() bool {
	if m == nil || !m.policy.searchCommands {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observedCapabilityResult {
		// A real result counts only after the invoked capability was recovered
		// from the host-produced command-search transcript.
		return true
	}
	if m.policy.requireCampusTool {
		if !m.observedCommandSearch || len(m.allowedCapabilities) > 0 {
			return false
		}
		if m.observedCampusResult {
			return true
		}
		return m.observedCampusSearch && len(m.allowedCampusTools) == 0
	}
	return m.observedCommandSearch && (!m.policy.executeCapability || len(m.allowedCapabilities) == 0)
}

func campusSearchToolNames(result string) (map[string]struct{}, bool) {
	var documentation []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(result)), &documentation) != nil || documentation == nil {
		return nil, false
	}
	names := make(map[string]struct{}, len(documentation))
	for _, item := range documentation {
		if name := strings.TrimSpace(item.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	return names, true
}

func campusCallNameForResult(input []*schema.Message, result *schema.Message) string {
	if result == nil || result.ToolCallID == "" {
		return ""
	}
	for index := len(input) - 2; index >= 0; index-- {
		candidate := input[index]
		if candidate == nil || candidate.Role != schema.Assistant {
			continue
		}
		for _, call := range candidate.ToolCalls {
			if call.ID != result.ToolCallID || call.Function.Name != campusCallToolName {
				continue
			}
			var invocation campusToolCallInput
			if json.Unmarshal([]byte(call.Function.Arguments), &invocation) == nil {
				return strings.TrimSpace(invocation.Name)
			}
			return ""
		}
	}
	return ""
}

func recoverCommandSearchCapabilityIDs(input []*schema.Message) (map[string]struct{}, bool) {
	for index := len(input) - 2; index >= 0; index-- {
		candidate := input[index]
		if candidate == nil || candidate.Role != schema.Tool || toolNameForResult(input, candidate) != commandSearchToolName {
			continue
		}
		return commandSearchCapabilityIDs(candidate.Content)
	}
	return nil, false
}

func toolNameForResult(input []*schema.Message, result *schema.Message) string {
	if result.ToolName != "" {
		return result.ToolName
	}
	if result.ToolCallID == "" {
		return ""
	}
	for index := len(input) - 2; index >= 0; index-- {
		candidate := input[index]
		if candidate == nil || candidate.Role != schema.Assistant {
			continue
		}
		for _, call := range candidate.ToolCalls {
			if call.ID == result.ToolCallID {
				return call.Function.Name
			}
		}
	}
	return ""
}

func commandSearchCapabilityIDs(result string) (map[string]struct{}, bool) {
	var documentation []struct {
		ID       string `json:"id"`
		Examples []struct {
			Capability string `json:"capability"`
		} `json:"examples"`
		Shortcuts []struct {
			Capability string `json:"capability"`
		} `json:"shortcuts"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(result)), &documentation) != nil || documentation == nil {
		return nil, false
	}
	capabilities := make(map[string]struct{}, len(documentation))
	for _, item := range documentation {
		if id := strings.TrimSpace(item.ID); id != "" {
			capabilities[id] = struct{}{}
		}
		for _, example := range item.Examples {
			if id := strings.TrimSpace(example.Capability); id != "" {
				capabilities[id] = struct{}{}
			}
		}
		for _, example := range item.Shortcuts {
			if id := strings.TrimSpace(example.Capability); id != "" {
				capabilities[id] = struct{}{}
			}
		}
	}
	return capabilities, true
}

func capabilityIDForResult(input []*schema.Message, result *schema.Message) string {
	if result == nil || result.ToolCallID == "" {
		return ""
	}
	for index := len(input) - 2; index >= 0; index-- {
		candidate := input[index]
		if candidate == nil || candidate.Role != schema.Assistant {
			continue
		}
		for _, call := range candidate.ToolCalls {
			if call.ID != result.ToolCallID || call.Function.Name != capabilityToolName {
				continue
			}
			var invocation hostCapabilityInput
			if json.Unmarshal([]byte(call.Function.Arguments), &invocation) == nil {
				return strings.TrimSpace(invocation.Capability)
			}
			return ""
		}
	}
	return ""
}
