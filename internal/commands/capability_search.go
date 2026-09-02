package commands

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Life-USTC/Bot/internal/textutil"
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
	DataScope    DataScope                `json:"dataScope"`
	Exposure     ResultExposure           `json:"exposure"`
	Examples     []CapabilityUsageExample `json:"examples"`
	Shortcuts    []CapabilityUsageExample `json:"shortcuts"`
	searchFields []string
}

type capabilitySearchResult struct {
	CapabilityDocumentation
	Score int
}

// SearchCapabilityDocumentation searches the registry using IDs, forms,
// titles, summaries, and executable examples. Equal scores use stable ID
// order. Shared conversations are filtered before ranking.
func SearchCapabilityDocumentation(query string, options CapabilitySearchOptions) []CapabilityDocumentation {
	query = normalizeCapabilitySearchText(query)
	queryTokens := strings.Fields(query)
	results := make([]capabilitySearchResult, 0, len(capabilityDescriptors))
	for _, descriptor := range capabilityDescriptors {
		documentation := capabilityDocumentationFor(descriptor, options.SharedConversation)
		if documentation == nil {
			continue
		}
		score, ok := capabilityDocumentationScore(query, queryTokens, *documentation)
		if !ok {
			continue
		}
		results = append(results, capabilitySearchResult{CapabilityDocumentation: *documentation, Score: score})
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
	documentation := make([]CapabilityDocumentation, 0, len(results))
	for _, result := range results {
		documentation = append(documentation, copyCapabilityDocumentation(result.CapabilityDocumentation))
	}
	return documentation
}

func capabilityDocumentationFor(descriptor CapabilityDescriptor, shared bool) *CapabilityDocumentation {
	usage := descriptor.Usage()
	documentation := &CapabilityDocumentation{
		ID:        usage.ID,
		Forms:     append([]string(nil), usage.Forms...),
		Group:     usage.Group,
		Topic:     usage.Group,
		Title:     descriptor.Help.Title,
		Summary:   usage.Summary,
		Effect:    usage.Effect,
		DataScope: usage.DataScope,
		Exposure:  usage.Exposure,
		Examples:  copyUsageExamples(usage.Examples),
		Shortcuts: copyUsageExamples(descriptor.Help.Shortcuts),
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
	markerFields := append([]string{strings.ToLower(string(documentation.ID)), strings.ToLower(documentation.Title)}, documentation.Forms...)
	markers := strings.ToLower(strings.Join(markerFields, "\x00"))
	score := 0
	strongMatch := false
	if query == strings.ToLower(string(documentation.ID)) {
		score += 10000
		strongMatch = true
	}
	for _, form := range documentation.Forms {
		form = normalizeCapabilitySearchText(form)
		switch {
		case query == form:
			score += 9000
			strongMatch = true
		case strings.HasPrefix(form, query):
			score += 5000
			strongMatch = true
		case strings.Contains(form, query):
			score += 3000
			strongMatch = true
		case containsHan(form) && strings.Contains(query, form):
			score += 2500
			strongMatch = true
		}
	}
	matchedToken := strongMatch
	hanMatches := 0
	markerHanMatches := 0
	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		if containsHan(token) {
			hanMatches += textutil.MeaningfulSearchTokenMatches(token, joined)
			markerHanMatches += textutil.MeaningfulSearchTokenMatches(token, markers)
			// Natural Chinese searches often have no word boundaries and include
			// polite or action words. Rank on meaningful overlapping terms.
			continue
		}
		if textutil.MeaningfulSearchTokenMatches(token, joined) > 0 {
			score += 1000
			matchedToken = true
			if strings.Contains(strings.ToLower(string(documentation.ID)), token) {
				score += 300
			}
			continue
		}
		// Search queries commonly contain several synonyms in both languages.
		// Treat them as ranking hints; requiring every hint would discard the
		// correct command whenever one synonym is absent from its documentation.
		continue
	}
	// One generic grammatical overlap is too weak to select a capability.
	// Exact IDs/forms remain sufficient; fuzzy Han matching needs two pieces
	// of domain evidence across the complete query.
	if hanMatches >= 2 || markerHanMatches > 0 {
		score += hanMatches * 500
		matchedToken = true
	}
	if score == 0 || (!matchedToken && query != strings.ToLower(string(documentation.ID))) {
		return 0, false
	}
	return score, true
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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
