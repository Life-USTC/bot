package commands

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Response struct {
	Text  string
	Image *responses.Image
	Kind  string
}

func textResponse(text string) Response {
	return Response{Text: text}
}

func (h Handler) imageResponseFor(cmd parsedCommand, text string) *responses.Image {
	if !h.EnableImageResponses || strings.TrimSpace(text) == "" {
		return nil
	}
	if !successfulImageText(text) {
		return nil
	}
	imageText := imageRenderText(text)
	switch cmd.Name {
	case "schedule":
		if firstArgIs(cmd.Args, "help") {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		title := imageTitle(plainText, "课表")
		return responses.NewRichTextImage("schedule", scheduleRichText(title, plainText), imageText)
	case "todo":
		if !todoImageArgs(cmd.Args) {
			return nil
		}
		return richTextImage("todo", imageTitle(imageText, "待办"), imageText)
	case "overview":
		return richTextImage("overview", imageTitle(imageText, "今日安排"), imageText)
	case "dashboard":
		return richTextImage("dashboard", imageTitle(imageText, "我的概览"), imageText)
	case "upcoming_deadlines":
		return richTextImage("deadlines", imageTitle(imageText, "近期截止"), imageText)
	case "bus":
		if firstArgIs(cmd.Args, "help") || busPreferenceArgs(cmd.Args) {
			return nil
		}
		title := "校车"
		body := busImageRenderText(text)
		return responses.NewRichTextImage("bus", busRichText(title, body), body)
	default:
		return nil
	}
}

func scheduleRichText(title, text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(lines[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			lines = lines[1:]
		}
	}

	out := []string{"# " + strings.TrimSpace(title), ""}
	atSectionStart := true
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			inTable = false
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			heading := strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(heading))
			atSectionStart = false
			inTable = false
			continue
		}
		if cells, ok := scheduleRichTableCells(line); ok {
			if !inTable {
				out = append(out,
					markdownRichTableRow([]string{"校区", "教室", "时间", "课程"}),
					markdownRichTableRow([]string{"---", "---", "---", "---"}),
				)
			}
			out = append(out, markdownRichTableRow(cells))
			inTable = true
		} else {
			out = append(out, imageRenderText(line))
			inTable = false
		}
		atSectionStart = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func scheduleRichTableCells(line string) ([]string, bool) {
	columns := strings.Split(line, "\t")
	if len(columns) < 3 {
		return nil, false
	}
	place := strings.TrimSpace(columns[0])
	timeRange := strings.TrimSpace(columns[1])
	course := strings.TrimSpace(strings.Join(columns[2:], " "))
	if timeRange == "" || course == "" {
		return nil, false
	}
	campus, room := scheduleCampusAndRoom(place)
	return []string{campus, room, timeRange, course}, true
}

func scheduleCampusAndRoom(place string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(place))
	if len(parts) == 0 {
		return "", ""
	}
	campus := campusName(parts[0])
	switch campus {
	case "东区", "西区", "中区", "北区", "南区", "高新区", "先研院":
		return campus, strings.TrimSpace(strings.TrimPrefix(place, parts[0]))
	default:
		return "", strings.TrimSpace(place)
	}
}

func markdownRichTableRow(cells []string) string {
	trimmed := make([]string, len(cells))
	for i, cell := range cells {
		trimmed[i] = strings.TrimSpace(cell)
	}
	return "| " + strings.Join(trimmed, " | ") + " |"
}

func richTextImage(kind, title, text string) *responses.Image {
	body := strings.Split(strings.TrimSpace(text), "\n")
	if len(body) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(body[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			body = body[1:]
		}
	}
	body = richTextSectionLines(body)
	richText := strings.TrimSpace("# " + strings.TrimSpace(title) + "\n\n" + strings.Join(body, "\n"))
	return responses.NewRichTextImage(kind, richText, text)
}

func richTextSectionLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	atSectionStart := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			trimmed = strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(trimmed))
		} else {
			out = append(out, line)
		}
		atSectionStart = false
	}
	return out
}

func busRichText(title, text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := []string{"# " + strings.TrimSpace(title), ""}
	atHeader := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			atHeader = true
			continue
		}
		cells := richTextTableCells(line)
		if len(cells) == 0 {
			continue
		}
		out = append(out, markdownRichTableRow(cells))
		if atHeader {
			separators := make([]string, len(cells))
			for i := range separators {
				separators[i] = "---"
			}
			out = append(out, "| "+strings.Join(separators, " | ")+" |")
			atHeader = false
		}
	}
	return strings.Join(out, "\n")
}

func richTextTableCells(line string) []string {
	if !strings.Contains(line, "\t") {
		return strings.Fields(line)
	}
	cells := strings.Split(line, "\t")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func busImageRenderText(text string) string {
	return textutil.PlainMonospace(text)
}

func imageRenderText(text string) string {
	text = textutil.PlainMonospace(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\t':
			out.WriteString("  ")
		case '\n':
			out.WriteRune(r)
		case '\r':
			continue
		default:
			if r < ' ' {
				out.WriteRune(' ')
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func imageTitle(text, fallback string) string {
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(text), "\n")[0])
	first = strings.TrimSuffix(first, "：")
	first = strings.TrimSuffix(first, ":")
	if first == "" {
		return fallback
	}
	return first
}

func successfulImageText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	rejectPrefixes := []string{
		"需要先登录",
		"登录未配置",
		"Life @ USTC API unavailable",
		"课表查不到：",
		"待办查不到：",
		"今日安排查不到：",
		"概览查不到：",
		"近期截止查不到：",
		"校车查不到：",
		"今天后面没查到校车。",
	}
	for _, prefix := range rejectPrefixes {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	return true
}

func todoImageArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "list", "all", "pending", "completed":
		return true
	default:
		_, ok := normalizeTodoPriority(args[0])
		return ok
	}
}
