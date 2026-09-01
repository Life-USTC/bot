package commands

import (
	"strings"
	"time"
)

func notifyArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIn(args, "status", "help") {
		return true
	}
	if args[0] == "classes" || args[0] == "homework" {
		return len(args) == 1 || args[1] == "on" || args[1] == "off"
	}
	return false
}

func settingsArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	switch settingsTopic(args[0]) {
	case "notify":
		return notifyArgsAcceptable(normalizeNotifyArgs(args[1:]))
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
	if busPreferenceArgs(args) {
		return busPreferenceArgsAcceptable(args)
	}
	routeArgs, options := busQueryArgs(args, time.Now())
	if options.QueryError != "" {
		return false
	}
	return busRouteArgsAcceptable(routeArgs)
}

func busRouteArgsAcceptable(args []string) bool {
	for _, arg := range args {
		if knownCampusName(arg) {
			continue
		}
		switch normToken(arg) {
		case "from", "to", "从", "到", "去", "往":
			continue
		default:
			return false
		}
	}
	return true
}

func busPreferenceArgsAcceptable(args []string) bool {
	args = copyArgs(args)
	if firstArgIn(args, "preference", "preferences", "pref", "prefs", "偏好", "默认") {
		args = args[1:]
	}
	if len(args) == 0 {
		return true
	}
	switch normToken(args[0]) {
	case "set", "设置":
		args = args[1:]
		if _, ok := parseBusShowSouth(args); ok && len(removeBusShowSouthArgs(args)) == 0 {
			return true
		}
		if _, ok := parseBusShowDeparted(args); ok && len(removeBusShowDepartedArgs(args)) == 0 {
			return true
		}
		args = removeBusShowDepartedArgs(args)
		if len(busCampusesFromArgs(args)) != 2 {
			return false
		}
		for _, arg := range args {
			if !knownCampusName(arg) {
				return false
			}
		}
		return true
	case "show-departed", "departed", "已发车", "已出发":
		return len(args) == 1 || len(args) == 2 && busBoolArg(args[1])
	case "南区", "南区校车", "show-south", "south-campus":
		return len(args) == 2 && busBoolArg(args[1])
	default:
		return false
	}
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

func positiveIDArgs(args []string) bool {
	if len(args) != 1 {
		return false
	}
	_, ok := parseIntArg(args[0])
	return ok
}

func sectionScheduleArgsAcceptable(args []string) bool {
	if len(args) != 3 || !positiveIDArgs(args[:1]) {
		return false
	}
	from, fromOK := parseScheduleDateToken(args[1], time.Now())
	to, toOK := parseScheduleDateToken(args[2], time.Now())
	return fromOK && toOK && !to.Before(from)
}

func sectionPagedIDArgsAcceptable(args []string) bool {
	remaining, _, err := extractListPage(args)
	return err == nil && positiveIDArgs(remaining)
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
	default:
		return ""
	}
}

func acceptedCommandResult(raw, name string, args []string) ParseResult {
	if name == "" {
		return ParseResult{Status: ParseStatusUnknown}
	}
	descriptor, ok := descriptorForID(name)
	if !ok {
		return ParseResult{Status: ParseStatusUnknown}
	}
	args = copyArgs(args)
	if descriptor.Normalize != nil {
		args = descriptor.Normalize(args)
	}
	result := ParseResult{
		Status: ParseStatusValid,
		Invocation: Invocation{
			Capability: descriptor,
			Name:       string(descriptor.ID),
			Args:       args,
			Raw:        raw,
		},
	}
	if !descriptor.Accepts(args) {
		result.Status = ParseStatusInvalid
	}
	return result
}

func acceptedCommand(raw, name string, args []string) (Invocation, bool) {
	result := acceptedCommandResult(raw, name, args)
	if !result.Valid() {
		return Invocation{}, false
	}
	return result.Invocation, true
}
