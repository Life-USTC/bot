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
)

type groundingPolicy struct {
	searchCommands     bool
	executeCapability  bool
	privateUnavailable bool
}

func groundingInstruction(base string, policy groundingPolicy) string {
	base = strings.TrimSpace(base)
	if !policy.searchCommands {
		return base
	}
	requirement := "CURRENT TURN REQUIREMENT: Call search_bot_commands before giving the user an answer. Do not answer from chat history or general knowledge."
	if policy.executeCapability {
		requirement += " If the search returns documentation, call invoke_bot_capability with one exact documented capability and use only its result. A plain-text factual answer before that capability result is invalid."
	}
	return base + "\n" + requirement
}

func groundingPolicyFor(text string, ident store.Identity) groundingPolicy {
	text = strings.TrimSpace(text)
	if text == "" {
		return groundingPolicy{}
	}
	verification := asksToVerifyPreviousAnswer(text)
	if verification {
		return groundingPolicy{searchCommands: true, executeCapability: true}
	}
	allDocumentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: 5})
	if store.IsSharedConversation(ident) {
		for _, match := range allDocumentation {
			if match.DataScope == commands.DataScopeUserPrivate {
				return groundingPolicy{privateUnavailable: true}
			}
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
	// The hard delivery gate protects fresh user-specific state. Public
	// information and general conversation still rely on the prompt's normal
	// tool policy so a fuzzy documentation match cannot hijack casual chat.
	for _, match := range documentation {
		if match.DataScope == commands.DataScopeUserPrivate {
			return groundingPolicy{searchCommands: true, executeCapability: true}
		}
	}
	return groundingPolicy{}
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
	observedCommandSearch    bool
	observedCapabilityResult bool
	observedCapabilityReturn bool
	lastCapabilityResult     string
	allowedCapabilities      map[string]struct{}
}

func newGroundingModel(inner model.BaseChatModel, policy groundingPolicy) model.BaseChatModel {
	if inner == nil || !policy.searchCommands {
		return inner
	}
	return &groundingModel{inner: inner, policy: policy}
}

func (m *groundingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, m.withGroundingTools(ctx, input, opts)...)
}

func (m *groundingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, input, m.withGroundingTools(ctx, input, opts)...)
}

func (m *groundingModel) withGroundingTools(ctx context.Context, input []*schema.Message, opts []model.Option) []model.Option {
	toolName := m.nextGroundingTool(ctx, input)
	if toolName == "" {
		return opts
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
	return append(grounded, model.WithTools(tools), model.WithToolChoice(schema.ToolChoiceAllowed))
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
		m.observedCommandSearch = true
		m.allowedCapabilities = commandSearchCapabilityIDs(last.Content)
		if m.policy.executeCapability && !m.offeredCapability && len(m.allowedCapabilities) > 0 {
			m.offeredCapability = true
			return capabilityToolName
		}
	}
	if last.Role == schema.Tool && toolNameForResult(input, last) == capabilityToolName {
		capability := capabilityIDForResult(input, last)
		if !m.observedCommandSearch {
			m.allowedCapabilities = recoverCommandSearchCapabilityIDs(input)
			m.observedCommandSearch = len(m.allowedCapabilities) > 0
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
	return ""
}

func (m *groundingModel) capabilityResult() (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
	return m.observedCommandSearch && !m.policy.executeCapability
}

func recoverCommandSearchCapabilityIDs(input []*schema.Message) map[string]struct{} {
	for index := len(input) - 2; index >= 0; index-- {
		candidate := input[index]
		if candidate == nil || candidate.Role != schema.Tool || toolNameForResult(input, candidate) != commandSearchToolName {
			continue
		}
		if capabilities := commandSearchCapabilityIDs(candidate.Content); len(capabilities) > 0 {
			return capabilities
		}
		return nil
	}
	return nil
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

func commandSearchCapabilityIDs(result string) map[string]struct{} {
	var documentation []struct {
		ID       string `json:"id"`
		Examples []struct {
			Capability string `json:"capability"`
		} `json:"examples"`
		Shortcuts []struct {
			Capability string `json:"capability"`
		} `json:"shortcuts"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(result)), &documentation) != nil {
		return nil
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
	return capabilities
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
