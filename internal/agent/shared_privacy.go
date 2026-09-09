package agent

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

const (
	botCommandToolName   = "run_bot_command"
	campusSearchToolName = "search_campus_tools"
	campusCallToolName   = "call_campus_tool"
)

// privateMatchLimit bounds how far the personal-data check looks for a private
// capability behind an ambient request.
const privateMatchLimit = 3

// sharedConversationPolicy is the one deterministic decision the host still
// makes for the model: a shared conversation asking for personal data is
// answered without a provider call. That is a privacy boundary rather than a
// guess about what the model should look up, and every other kind of steering
// was removed with the forced grounding contract.
type sharedConversationPolicy struct {
	privateUnavailable bool
}

func sharedConversationPolicyFor(text string, ident store.Identity) sharedConversationPolicy {
	text = strings.TrimSpace(text)
	if text == "" || !store.IsSharedConversation(ident) {
		return sharedConversationPolicy{}
	}
	// Both halves are required. Capability markers are short enough to appear
	// inside ordinary words — 作业 sits inside 作业本 — so a documentation match
	// alone redirected people who were not asking about their own data at all.
	if !asksForOwnData(text) {
		return sharedConversationPolicy{}
	}
	documentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: privateMatchLimit})
	if containsPrivateCapability(documentation) {
		return sharedConversationPolicy{privateUnavailable: true}
	}
	return sharedConversationPolicy{}
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
