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
	documentation := commands.SearchCapabilityDocumentation(text, commands.CapabilitySearchOptions{Limit: 5})
	return explicitlyNamesPrivateCapability(text, documentation) || asksForOwnData(text) && containsPrivateCapability(documentation)
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
