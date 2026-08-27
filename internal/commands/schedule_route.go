package commands

import "strings"

var naturalSchedulePrefixes = []string{
	"麻烦帮我查一下", "麻烦帮我看一下", "可以帮我查一下", "可以帮我看一下",
	"我想知道", "我想看看", "我想查查", "我想看", "我想查", "帮我查一下", "帮我看一下",
	"麻烦查一下", "麻烦看一下", "请帮我查", "请帮我看", "请查一下", "请看一下",
	"想知道", "想看看", "想查查", "查一下", "看一下", "帮我查", "帮我看",
	"麻烦查", "麻烦看", "请查", "请看", "查询", "查查", "看看", "想看", "想查", "查", "看",
}

var naturalScheduleSuffixes = []string{"可以吗", "好吗", "谢谢", "吧", "呢", "呀", "吗"}

var ambiguousScheduleMarkers = []string{
	"还有", "顺便", "并且", "同时", "以及", "然后", "提醒", "添加", "修改", "删除",
	"为什么", "怎么", "如何", "解释", "比较", "推荐",
}

// parseNaturalScheduleIntent only accepts a narrow read-only grammar, then
// delegates target normalization to the canonical schedule command parser.
func parseNaturalScheduleIntent(raw string) (parsedCommand, bool) {
	text := strings.Join(strings.Fields(strings.TrimSpace(raw)), "")
	text = strings.Trim(text, "，,。！？!?；;")
	if text == "" || containsAny(text, ambiguousScheduleMarkers) {
		return parsedCommand{}, false
	}
	text = trimFirstPrefix(text, naturalSchedulePrefixes)
	text = strings.TrimPrefix(text, "我")
	text = trimFirstSuffix(text, naturalScheduleSuffixes)
	text = strings.Trim(text, "，,。！？!?；;")

	for _, ending := range []string{"有什么课", "有哪些课", "上什么课", "上哪些课"} {
		if target, ok := strings.CutSuffix(text, ending); ok {
			return routedScheduleCommand(raw, target+"课表")
		}
	}

	if strings.Count(text, "课表") != 1 {
		return parsedCommand{}, false
	}
	text = strings.ReplaceAll(text, "的课表", "课表")
	return routedScheduleCommand(raw, text)
}

func routedScheduleCommand(raw, compact string) (parsedCommand, bool) {
	if compact == "课表" {
		return parsedCommand{Name: "schedule", Raw: raw, NaturalRoute: "schedule"}, true
	}
	name, args, ok := normalizeJoinedCommand(compact, nil)
	if !ok || name != "schedule" || !scheduleArgsAcceptable(args) {
		return parsedCommand{}, false
	}
	return parsedCommand{Name: name, Args: args, Raw: raw, NaturalRoute: "schedule"}, true
}

func trimFirstPrefix(value string, prefixes []string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func trimFirstSuffix(value string, suffixes []string) string {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return strings.TrimSuffix(value, suffix)
		}
	}
	return value
}

func containsAny(value string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
