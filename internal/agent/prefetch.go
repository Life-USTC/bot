package agent

import (
	"encoding/json"
	"strings"

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

// prefetchLimit bounds how much documentation the host injects on its own.
// Over-triggering is deliberately cheap here: a wrong guess only spends a few
// hundred tokens the model ignores. It never blocks or replaces an answer.
const prefetchLimit = 3

// prefetchPolicy is a retrieval hint, not an enforcement rule: it only decides
// what documentation the host looks up on the model's behalf.
//
// privateUnavailable is the one remaining deterministic decision: a shared
// conversation asking for personal data is answered by the host without a
// provider call, because that is a privacy boundary rather than a guess about
// what the model should look up.
type prefetchPolicy struct {
	searchCommands     bool
	privateUnavailable bool

	documentation []commands.CapabilityDocumentation
}

func prefetchPolicyFor(text string, ident store.Identity) prefetchPolicy {
	text = strings.TrimSpace(text)
	if text == "" {
		return prefetchPolicy{}
	}
	shared := store.IsSharedConversation(ident)
	if shared {
		all := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: prefetchLimit})
		if explicitlyNamesPrivateCapability(text, all) || asksForOwnData(text) && containsPrivateCapability(all) {
			return prefetchPolicy{privateUnavailable: true}
		}
	}
	documentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{
		SharedConversation: shared,
		Limit:              prefetchLimit,
	})
	policy := prefetchPolicy{}
	if len(documentation) > 0 {
		policy.searchCommands = true
		policy.documentation = documentation
	}
	return policy
}

// seededToolCall hands the model the search the host already ran, as a real
// assistant tool call and its result rather than as prose in the system prompt.
// The transcript then reads exactly as if the model had searched first, so no
// instruction is needed to explain what the block is or how to treat it.
//
// It is appended after the current user turn and never persisted: the cached
// history prefix is untouched, and a later turn rebuilds its own search rather
// than inheriting this one.
func (p prefetchPolicy) seededToolCall(query string) []*schema.Message {
	if !p.searchCommands || len(p.documentation) == 0 {
		return nil
	}
	arguments, err := json.Marshal(commandSearchInput{Query: strings.TrimSpace(query)})
	if err != nil {
		return nil
	}
	documentation, err := json.Marshal(p.documentation)
	if err != nil {
		return nil
	}
	call := schema.ToolCall{
		ID:       prefetchToolCallID,
		Type:     "function",
		Function: schema.FunctionCall{Name: commandSearchToolName, Arguments: string(arguments)},
	}
	return []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{call}),
		schema.ToolMessage(string(documentation), prefetchToolCallID, schema.WithToolName(commandSearchToolName)),
	}
}

const prefetchToolCallID = "host_prefetch_search_bot_commands"

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

// explicitlyNamesCapability gates the one deterministic decision left: a
// shared conversation refusing personal data. Substring matching stays loose
// on purpose — a marker such as 课表 is only two characters and tightening the
// rule would lose it. The cost of a loose match here is now a private-chat
// redirect instead of a discarded answer.
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
