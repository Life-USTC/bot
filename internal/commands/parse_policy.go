package commands

import (
	"strings"
	"time"
)

func notifyArgsAcceptable(args []string) bool {
	if !hasArgs(args) {
		return true
	}
	if firstArgIn(args, "status", "help") {
		return len(args) == 1
	}
	if args[0] == "classes" || args[0] == "homework" || args[0] == "young" {
		return len(args) == 1 || len(args) == 2 && (args[1] == "on" || args[1] == "off")
	}
	return false
}

func homeworkArgsAcceptable(args []string) bool {
	if !hasArgs(args) {
		return true
	}
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	switch normToken(args[0]) {
	case "done", "undo":
		return len(args) >= 2 && strings.TrimSpace(joinedArgs(args[1:])) != ""
	}
	_, err := parseHomeworkListArgs(args)
	return err == nil
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
	if !hasArgs(args) {
		return true
	}
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	switch args[0] {
	case "link", "链接", "日历":
		return len(args) == 1
	case "kind":
		return subscriptionKindArgsAcceptable(args[1:])
	case "import", "导入", "添加", "新增", "remove", "取消", "删除", "移除", "退订":
		targetArgs, _, ok := splitSubscriptionMutationArgs(args[1:])
		return ok && sectionCodeListAcceptable(joinedArgs(targetArgs))
	default:
		return false
	}
}

func sectionCodeListAcceptable(raw string) bool {
	raw = strings.NewReplacer(",", " ", "，", " ", ";", " ", "；", " ").Replace(raw)
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		matches := sectionCodePattern.FindAllString(part, -1)
		if len(matches) != 1 || !strings.EqualFold(matches[0], part) {
			return false
		}
	}
	return true
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
	if !hasArgs(args) {
		return true
	}
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	remaining, _, err := extractListPage(args)
	return err == nil && len(remaining) == 0
}

func courseSearchArgsAcceptable(args []string) bool {
	return keywordSearchArgsAcceptable(args, map[string]bool{
		"education_level_id": true,
		"category_id":        true,
		"class_type_id":      true,
		"limit":              true,
	}, nil)
}

func sectionSearchArgsAcceptable(args []string) bool {
	return keywordSearchArgsAcceptable(args, map[string]bool{
		"course_id":      true,
		"course_jw_id":   true,
		"semester_id":    true,
		"semester_jw_id": true,
		"campus_id":      true,
		"department_id":  true,
		"teacher_id":     true,
		"limit":          true,
	}, map[string]bool{"teacher_code": true})
}

func teacherSearchArgsAcceptable(args []string) bool {
	return keywordSearchArgsAcceptable(args, map[string]bool{
		"department_id": true,
		"limit":         true,
	}, nil)
}

func keywordSearchArgsAcceptable(args []string, numericFilters, textFilters map[string]bool) bool {
	filter := func(token string) bool { return numericFilters[token] || textFilters[token] }
	seen := make(map[string]bool, len(numericFilters)+len(textFilters))
	for index := 0; index < len(args); index++ {
		key := normToken(args[index])
		if key == "keyword" {
			start := index + 1
			index = start
			for index < len(args) && !filter(normToken(args[index])) {
				index++
			}
			if strings.TrimSpace(joinedArgs(args[start:index])) == "" {
				return false
			}
			index--
			continue
		}
		if !filter(key) {
			continue
		}
		if seen[key] || index+1 >= len(args) || filter(normToken(args[index+1])) {
			return false
		}
		seen[key] = true
		value := strings.TrimSpace(args[index+1])
		if value == "" {
			return false
		}
		if numericFilters[key] {
			if _, ok := parseIntArg(value); !ok {
				return false
			}
		}
		index++
	}
	return true
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
