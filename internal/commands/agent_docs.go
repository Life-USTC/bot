package commands

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ImageCommandResolution is the host-side answer for messy NL → image directive.
type ImageCommandResolution struct {
	Directive  string   `json:"directive,omitempty"`
	Command    string   `json:"command,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Help       string   `json:"help,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// LookupBotHelp returns the same help text users get from 帮助 [topic].
func LookupBotHelp(topic string) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return Handler{}.help()
	}
	return Handler{}.help(strings.Fields(topic)...)
}

// ResolveImageCommand maps messy user text to a host-validated image directive when possible.
func ResolveImageCommand(text string) ImageCommandResolution {
	text = strings.TrimSpace(text)
	if text == "" {
		return ImageCommandResolution{Note: "empty query"}
	}

	handler := Handler{}
	tried := make([]string, 0, 8)
	seen := map[string]struct{}{}
	try := func(candidate string) (ImageCommandResolution, bool) {
		candidate = strings.Join(strings.Fields(strings.TrimSpace(candidate)), " ")
		if candidate == "" {
			return ImageCommandResolution{}, false
		}
		if _, ok := seen[candidate]; ok {
			return ImageCommandResolution{}, false
		}
		seen[candidate] = struct{}{}
		tried = append(tried, candidate)
		cmd, ok := handler.parse(candidate)
		if !ok || !imageDirectiveCommandAllowed(cmd) {
			return ImageCommandResolution{}, false
		}
		command := strings.TrimSpace(cmd.Raw)
		if command == "" {
			command = candidate
		}
		return ImageCommandResolution{
			Directive: "![](" + command + ")",
			Command:   command,
			Note:      "validated against the bot command parser; prefer an image-only reply with this directive",
		}, true
	}

	for _, candidate := range imageCommandCandidates(text) {
		if resolved, ok := try(candidate); ok {
			return resolved
		}
	}

	topic := inferHelpTopic(text)
	help := LookupBotHelp(topic)
	candidates := imageHelpExamples(topic)
	if len(candidates) == 0 {
		candidates = tried
	}
	return ImageCommandResolution{
		Candidates: candidates,
		Help:       help,
		Note:       "no validated image command; pick a candidate or ask lookup_bot_help, then emit ![](command)",
	}
}

func imageCommandCandidates(text string) []string {
	normalized := normalizeBotCommandText(text)
	spaced := insertCommandRootSpace(normalized)
	out := make([]string, 0, 8)
	add := func(candidate string) {
		candidate = strings.Join(strings.Fields(strings.TrimSpace(candidate)), " ")
		if candidate == "" {
			return
		}
		for _, existing := range out {
			if existing == candidate {
				return
			}
		}
		out = append(out, candidate)
	}
	// Prefer canonical bus query form before looser parses.
	add(preferBusQueryForm(spaced))
	add(preferBusQueryForm(normalized))
	add(spaced)
	add(normalized)
	add(text)
	return out
}

func normalizeBotCommandText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = expandGluedCampusNames(text)
	text = insertCommandRootSpace(text)
	return strings.Join(strings.Fields(text), " ")
}

func insertCommandRootSpace(text string) string {
	roots := []string{
		"近期截止", "下一节课", "今日课表", "明日课表", "今天课表", "明天课表",
		"校车", "课表", "待办", "作业", "考试", "概览", "日程",
	}
	for _, root := range roots {
		if !strings.HasPrefix(text, root) || len(text) == len(root) {
			continue
		}
		rest := text[len(root):]
		if strings.HasPrefix(rest, " ") {
			return text
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if unicode.IsLetter(r) || unicode.Is(unicode.Han, r) || unicode.IsDigit(r) {
			return root + " " + strings.TrimSpace(rest)
		}
	}
	return text
}

func expandGluedCampusNames(text string) string {
	names := []string{"高新区", "先研院", "东区", "西区", "中区", "北区", "南区"}
	abbrevs := []struct {
		short string
		full  string
	}{
		{"东", "东区"},
		{"西", "西区"},
		{"南", "南区"},
		{"北", "北区"},
		{"中", "中区"},
	}
	var parts []string
	var buf strings.Builder
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			parts = append(parts, s)
		}
		buf.Reset()
	}
	for i := 0; i < len(text); {
		matched := ""
		for _, name := range names {
			if strings.HasPrefix(text[i:], name) {
				matched = name
				break
			}
		}
		if matched != "" {
			flush()
			parts = append(parts, matched)
			i += len(matched)
			continue
		}
		expanded := ""
		advance := 0
		for _, abbrev := range abbrevs {
			if !strings.HasPrefix(text[i:], abbrev.short) {
				continue
			}
			rest := text[i+len(abbrev.short):]
			if strings.HasPrefix(rest, "区") {
				expanded = abbrev.full
				advance = len(abbrev.short) + len("区")
				break
			}
			for _, name := range names {
				if strings.HasPrefix(rest, name) {
					expanded = abbrev.full
					advance = len(abbrev.short)
					break
				}
			}
			if expanded != "" {
				break
			}
		}
		if expanded != "" {
			flush()
			parts = append(parts, expanded)
			i += advance
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			buf.WriteByte(text[i])
			i++
			continue
		}
		buf.WriteString(text[i : i+size])
		i += size
	}
	flush()
	return strings.Join(parts, " ")
}

func preferBusQueryForm(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	root := fields[0]
	if root != "校车" && root != "xc" && root != "bus" {
		return ""
	}
	rest := fields[1:]
	if len(rest) == 0 {
		return "校车"
	}
	if rest[0] == "查询" || rest[0] == "query" || rest[0] == "q" {
		return strings.Join(append([]string{"校车"}, rest...), " ")
	}
	if rest[0] == "设置" || rest[0] == "偏好" || rest[0] == "路线" {
		return ""
	}
	campuses := make([]string, 0, 2)
	for _, field := range rest {
		if name := campusName(field); name != "" {
			campuses = append(campuses, name)
		}
	}
	if len(campuses) >= 2 {
		return "校车 查询 " + campuses[0] + " " + campuses[1]
	}
	if len(campuses) == 1 {
		return "校车 查询 " + campuses[0]
	}
	return "校车 查询 " + strings.Join(rest, " ")
}

func inferHelpTopic(text string) string {
	normalized := normalizeBotCommandText(text)
	switch {
	case strings.Contains(normalized, "校车") || strings.Contains(normalized, "xc"):
		return "校车"
	case strings.Contains(normalized, "课表") || strings.Contains(normalized, "下一节"):
		return "课表"
	case strings.Contains(normalized, "待办"):
		return "待办"
	case strings.Contains(normalized, "作业"):
		return "作业"
	case strings.Contains(normalized, "考试"):
		return "考试"
	case strings.Contains(normalized, "概览") || strings.Contains(normalized, "日程"):
		return "日程"
	case strings.Contains(normalized, "截止"):
		return "日程"
	default:
		return ""
	}
}

func imageHelpExamples(topic string) []string {
	topic = helpTopicCommand([]string{topic})
	if topic == "" {
		return nil
	}
	var out []string
	for _, row := range helpDetailRows(topic) {
		cmd, ok := Handler{}.parse(row.command)
		if !ok || !imageDirectiveCommandAllowed(cmd) {
			continue
		}
		out = append(out, row.command)
		if len(out) >= 8 {
			break
		}
	}
	return out
}
