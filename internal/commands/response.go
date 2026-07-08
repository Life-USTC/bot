package commands

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/responses"
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
	switch cmd.Name {
	case "schedule":
		if firstArgIs(cmd.Args, "help") {
			return nil
		}
		return responses.NewTextImage("schedule", imageTitle(text, "课表"), text)
	case "todo":
		if !todoImageArgs(cmd.Args) {
			return nil
		}
		return responses.NewTextImage("todo", imageTitle(text, "待办"), text)
	case "overview":
		return responses.NewTextImage("overview", imageTitle(text, "今日安排"), text)
	case "dashboard":
		return responses.NewTextImage("dashboard", imageTitle(text, "我的概览"), text)
	case "upcoming_deadlines":
		return responses.NewTextImage("deadlines", imageTitle(text, "近期截止"), text)
	default:
		return nil
	}
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
