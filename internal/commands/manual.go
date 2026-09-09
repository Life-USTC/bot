package commands

import "strings"

// CommandManual renders the complete command reference the model is given up
// front. It is the same material the help capability shows a user, so the model
// can both call a command correctly and tell a user which command to type.
//
// It is deliberately the whole reference rather than a search result: the
// manual is static, so it sits in the cached prompt prefix, while a per-turn
// search costs an uncached block and a round trip and still leaves the model
// guessing at any form the search did not return.
func CommandManual(sharedConversation bool) string {
	documentation := SearchCapabilityDocumentation("", CapabilitySearchOptions{
		SharedConversation: sharedConversation,
	})
	var b strings.Builder
	for _, item := range documentation {
		b.WriteString(strings.Join(item.Forms, " / "))
		if summary := strings.TrimSpace(item.Summary); summary != "" {
			b.WriteString(" — ")
			b.WriteString(summary)
		}
		b.WriteString("\n")
		for _, example := range append(append([]CapabilityUsageExample(nil), item.Shortcuts...), item.Examples...) {
			command := strings.TrimSpace(example.Command)
			if command == "" {
				continue
			}
			b.WriteString("    ")
			b.WriteString(command)
			if description := strings.TrimSpace(example.Description); description != "" {
				b.WriteString("    # ")
				b.WriteString(description)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
