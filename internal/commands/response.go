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
		return richTextImage("schedule", imageTitle(imageText, "课表"), imageText)
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
		cells := strings.Fields(line)
		if len(cells) == 0 {
			continue
		}
		out = append(out, "| "+strings.Join(cells, " | ")+" |")
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
