package commands

import (
	"strings"
)

// commandArgsAcceptable enforces closed command schemas so free-form
// natural language falls through to the agent.
func commandArgsAcceptable(name string, args []string) bool {
	descriptor, ok := descriptorForID(name)
	if !ok || descriptor.Input == nil {
		return false
	}
	return descriptor.Input(args)
}

func notifyArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIn(args, "status", "help") {
		return true
	}
	if args[0] == "classes" || args[0] == "homework" {
		return len(args) == 1 || args[1] == "on" || args[1] == "off"
	}
	return false
}

func agentArgsAcceptable(args []string) bool {
	return !hasArgs(args) || firstArgIn(args, "status", "help", "on", "off")
}

func settingsArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	switch settingsTopic(args[0]) {
	case "notify":
		return notifyArgsAcceptable(normalizeNotifyArgs(args[1:]))
	case "agent":
		rest := args[1:]
		if len(rest) > 0 && firstArgIn(rest, "tool", "tools", "工具") {
			rest = rest[1:]
		}
		return agentArgsAcceptable(normalizeAgentArgs(rest))
	default:
		return false
	}
}

func homeworkArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	switch normToken(args[0]) {
	case "done", "undo", "pending", "all", "list", "ls", "查看", "列表", "未完成", "全部":
		return true
	case "semester_id", "semester_jw_id", "学期id", "学期jwid":
		return true
	}
	return isListPageToken(args[0])
}

func scheduleArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	if len(args) > 1 {
		return false
	}
	arg := args[0]
	if _, ok := normalizeScheduleDay(normToken(arg)); ok {
		return true
	}
	switch arg {
	case "this-week", "next-week":
		return true
	}
	if strings.HasPrefix(arg, "week-date:") || strings.HasPrefix(arg, "week-number:") || strings.HasPrefix(arg, "date:") || strings.HasPrefix(arg, "semester:") {
		return true
	}
	if _, ok := normalizeScheduleWeekTarget(arg); ok {
		return true
	}
	if _, ok := normalizeScheduleDateToken(arg); ok {
		return true
	}
	return false
}

func busCommandArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	if firstArgIn(args, "偏好", "设置", "已发车", "南区", "全部", "我的路线", "help", "查询") {
		return true
	}
	if len(busCampusesFromArgs(args)) > 0 {
		return true
	}
	for _, arg := range args {
		switch normToken(arg) {
		case "after", "之后", "已发车", "全部", "all", "from", "to", "到", "去", "往":
			return true
		}
	}
	return false
}

func subscriptionArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	switch args[0] {
	case "help", "link", "import", "链接", "日历", "导入", "添加", "新增":
		return true
	default:
		return len(extractSectionCodes(joinedArgs(args))) > 0
	}
}

func examArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	return isListPageToken(args[0])
}

func isListPageToken(value string) bool {
	remaining, _, err := extractListPage([]string{value})
	if err == nil && len(remaining) == 0 && strings.TrimSpace(value) != "" {
		return true
	}
	n, ok := parseIntArg(strings.TrimSpace(value))
	return ok && n > 0
}

func settingsTopic(token string) string {
	switch normToken(token) {
	case "notify", "notice", "提醒", "通知", "推送":
		return "notify"
	case "agent", "ai", "llm", "tool", "tools", "工具", "调试":
		return "agent"
	default:
		return ""
	}
}

func acceptedCommand(raw, name string, args []string) (Invocation, bool) {
	if name == "" {
		return Invocation{}, false
	}
	descriptor, ok := descriptorForID(name)
	if !ok {
		return Invocation{}, false
	}
	args = copyArgs(args)
	if descriptor.Normalize != nil {
		args = descriptor.Normalize(args)
	}
	if !descriptor.Accepts(args) {
		return Invocation{}, false
	}
	return Invocation{Capability: descriptor, Name: string(descriptor.ID), Args: args, Raw: raw}, true
}
