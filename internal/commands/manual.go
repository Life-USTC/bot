package commands

import "strings"

// CommandManual renders the complete command reference used by the model.
// It is generated from the registry so the executable forms, aliases and
// examples cannot drift from the parser. Shared conversations receive only
// public capabilities and examples.
func CommandManual(shared bool) string {
	documentation := SearchCapabilityDocumentation("", CapabilitySearchOptions{
		SharedConversation: shared,
	})
	var b strings.Builder
	for _, item := range documentation {
		forms := strings.TrimSpace(strings.Join(item.Forms, " / "))
		if forms == "" {
			continue
		}
		b.WriteString(forms)
		if summary := strings.TrimSpace(item.Summary); summary != "" {
			b.WriteString(" — ")
			b.WriteString(summary)
		}
		b.WriteByte('\n')
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
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
