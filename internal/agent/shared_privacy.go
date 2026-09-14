package agent

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/store"
)

// sharedConversationNeedsPrivateReply keeps personal-data requests out of shared chats.
func sharedConversationNeedsPrivateReply(text string, ident store.Identity) bool {
	if !store.IsSharedConversation(ident) || strings.TrimSpace(text) == "" {
		return false
	}
	for _, marker := range []string{"怎么用", "用法", "命令", "帮助"} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	documentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: 5})
	return asksForOwnData(text) && containsPrivateCapability(documentation)
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
