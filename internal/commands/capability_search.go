package commands

import (
	"sort"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// CapabilitySearchOptions controls the registry-backed documentation search.
// SharedConversation removes private capabilities and private examples from
// the result before ranking, so a caller cannot accidentally advertise a
// personal-data command in a shared chat.
type CapabilitySearchOptions struct {
	SharedConversation bool
	Limit              int
}

// CapabilityDocumentation is the complete structured documentation for one
// registered capability. Metadata is derived from the same invocation policy
// used by execution; Examples retain exact normalized arguments.
type CapabilityDocumentation struct {
	ID           CapabilityID             `json:"id"`
	Forms        []string                 `json:"forms"`
	Group        string                   `json:"group"`
	Topic        string                   `json:"topic"`
	Title        string                   `json:"title"`
	Summary      string                   `json:"summary"`
	Effect       CapabilityEffect         `json:"effect"`
	Confirmation ConfirmationPolicy       `json:"confirmation"`
	DataScope    DataScope                `json:"dataScope"`
	Exposure     ResultExposure           `json:"exposure"`
	Examples     []CapabilityUsageExample `json:"examples"`
	Shortcuts    []CapabilityUsageExample `json:"shortcuts"`
	searchFields []string
}

// CapabilitySearchResult is a documentation match with a deterministic score.
// CapabilityDocumentation is embedded so callers can inspect result.ID,
// result.Examples, and policy metadata directly.
type CapabilitySearchResult struct {
	CapabilityDocumentation
	Score int `json:"score"`
}

// SearchCapabilities searches the command registry using normalized command
// IDs, forms, titles, summaries, descriptions and examples. Equal scores are
// ordered by stable capability ID, making the output deterministic across
// runs. Pass true to hide private capabilities in a shared conversation.
func SearchCapabilities(query string, sharedConversation bool) []CapabilitySearchResult {
	return SearchCapabilitiesWithOptions(query, CapabilitySearchOptions{SharedConversation: sharedConversation})
}

// SearchCapabilitiesWithOptions is the configurable form of
// SearchCapabilities.
func SearchCapabilitiesWithOptions(query string, options CapabilitySearchOptions) []CapabilitySearchResult {
	query = normalizeCapabilitySearchText(query)
	queryTokens := strings.Fields(query)
	results := make([]CapabilitySearchResult, 0, len(capabilityDescriptors))
	for _, descriptor := range capabilityDescriptors {
		documentation := capabilityDocumentationFor(descriptor, options.SharedConversation)
		if documentation == nil {
			continue
		}
		score, ok := capabilityDocumentationScore(query, queryTokens, *documentation)
		if !ok {
			continue
		}
		results = append(results, CapabilitySearchResult{CapabilityDocumentation: *documentation, Score: score})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})
	if options.Limit > 0 && len(results) > options.Limit {
		results = results[:options.Limit]
	}
	return copyCapabilitySearchResults(results)
}

// SearchCapabilityDocumentation returns the same search matches without the
// ranking wrapper. It is useful for help/tool-description consumers that only
// need structured documentation.
func SearchCapabilityDocumentation(query string, options CapabilitySearchOptions) []CapabilityDocumentation {
	results := SearchCapabilitiesWithOptions(query, options)
	documentation := make([]CapabilityDocumentation, 0, len(results))
	for _, result := range results {
		documentation = append(documentation, copyCapabilityDocumentation(result.CapabilityDocumentation))
	}
	return documentation
}

// SearchCapabilityDocs is a concise alias for documentation consumers.
func SearchCapabilityDocs(query string, sharedConversation bool) []CapabilityDocumentation {
	return SearchCapabilityDocumentation(query, CapabilitySearchOptions{SharedConversation: sharedConversation})
}

// SearchCapabilitiesForIdentity applies the shared-conversation policy from a
// concrete host identity.
func SearchCapabilitiesForIdentity(query string, identity store.Identity) []CapabilitySearchResult {
	return SearchCapabilities(query, store.IsSharedConversation(identity))
}

// SearchCapabilities is also available as a Handler method for hosts that
// already carry a command handler alongside their conversation identity.
func (h Handler) SearchCapabilities(query string, sharedConversation bool) []CapabilitySearchResult {
	return SearchCapabilities(query, sharedConversation)
}

// SearchCapabilityDocumentation is the Handler-method counterpart to the
// package-level documentation search API.
func (h Handler) SearchCapabilityDocumentation(query string, options CapabilitySearchOptions) []CapabilityDocumentation {
	return SearchCapabilityDocumentation(query, options)
}

func capabilityDocumentationFor(descriptor CapabilityDescriptor, shared bool) *CapabilityDocumentation {
	usage := descriptor.Usage()
	documentation := &CapabilityDocumentation{
		ID:           usage.ID,
		Forms:        append([]string(nil), usage.Forms...),
		Group:        usage.Group,
		Topic:        usage.Group,
		Title:        descriptor.Help.Title,
		Summary:      usage.Summary,
		Effect:       usage.Effect,
		Confirmation: usage.Confirmation,
		DataScope:    usage.DataScope,
		Exposure:     usage.Exposure,
		Examples:     copyUsageExamples(usage.Examples),
		Shortcuts:    copyUsageExamples(descriptor.Help.Shortcuts),
	}
	if !shared {
		documentation.searchFields = capabilityDocumentationSearchFields(*documentation, false)
		return documentation
	}
	publicExamples := documentation.Examples[:0]
	for _, example := range documentation.Examples {
		if example.DataScope == DataScopePublic {
			publicExamples = append(publicExamples, example)
		}
	}
	documentation.Examples = publicExamples
	publicShortcuts := documentation.Shortcuts[:0]
	for _, shortcut := range documentation.Shortcuts {
		if shortcut.DataScope == DataScopePublic {
			publicShortcuts = append(publicShortcuts, shortcut)
		}
	}
	documentation.Shortcuts = publicShortcuts
	if documentation.DataScope != DataScopePublic && len(documentation.Examples) == 0 && len(documentation.Shortcuts) == 0 {
		return nil
	}
	documentation.searchFields = capabilityDocumentationSearchFields(*documentation, true)
	return documentation
}

func capabilityDocumentationScore(query string, queryTokens []string, documentation CapabilityDocumentation) (int, bool) {
	if query == "" {
		return 0, true
	}
	fields := documentation.searchFields
	if len(fields) == 0 {
		fields = capabilityDocumentationSearchFields(documentation, false)
	}
	joined := strings.Join(fields, "\x00")
	score := 0
	if query == strings.ToLower(string(documentation.ID)) {
		score += 10000
	}
	for _, form := range documentation.Forms {
		form = normalizeCapabilitySearchText(form)
		switch {
		case query == form:
			score += 9000
		case strings.HasPrefix(form, query):
			score += 5000
		case strings.Contains(form, query):
			score += 3000
		}
	}
	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		if strings.Contains(joined, token) {
			score += 1000
			if strings.Contains(strings.ToLower(string(documentation.ID)), token) {
				score += 300
			}
			continue
		}
		return 0, false
	}
	if score == 0 {
		return 0, false
	}
	return score, true
}

func capabilityDocumentationSearchFields(documentation CapabilityDocumentation, shared bool) []string {
	fields := []string{
		strings.ToLower(string(documentation.ID)),
		strings.ToLower(documentation.Title),
	}
	privateExample := false
	for _, example := range documentation.Examples {
		if example.DataScope != DataScopePublic {
			privateExample = true
		}
	}
	if !shared || !privateExample {
		fields = append(fields, strings.ToLower(documentation.Summary))
	}
	for _, form := range documentation.Forms {
		fields = append(fields, strings.ToLower(form))
	}
	for _, example := range documentation.Examples {
		fields = append(fields, strings.ToLower(example.Command), strings.ToLower(example.Description), strings.ToLower(string(example.Capability)))
		fields = append(fields, strings.ToLower(strings.Join(example.Arguments, " ")))
	}
	for _, shortcut := range documentation.Shortcuts {
		fields = append(fields, strings.ToLower(shortcut.Command), strings.ToLower(shortcut.Description))
	}
	return fields
}

func normalizeCapabilitySearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(
		"/", " ",
		"-", " ",
		"_", " ",
		"（", " ",
		"）", " ",
		"(", " ",
		")", " ",
		"：", " ",
		":", " ",
		"，", " ",
		",", " ",
	).Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func copyCapabilityDocumentation(documentation CapabilityDocumentation) CapabilityDocumentation {
	documentation.Forms = append([]string(nil), documentation.Forms...)
	documentation.Examples = copyUsageExamples(documentation.Examples)
	documentation.Shortcuts = copyUsageExamples(documentation.Shortcuts)
	documentation.searchFields = append([]string(nil), documentation.searchFields...)
	return documentation
}

func copyCapabilitySearchResults(results []CapabilitySearchResult) []CapabilitySearchResult {
	copy := make([]CapabilitySearchResult, len(results))
	for i, result := range results {
		result.CapabilityDocumentation = copyCapabilityDocumentation(result.CapabilityDocumentation)
		copy[i] = result
	}
	return copy
}
