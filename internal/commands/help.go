package commands

import "strings"

type helpRow struct {
	topic       string
	commandName string
	command     string
	description string
}

type helpSection struct {
	title string
	rows  []helpRow
}

func helpOverviewSections() []helpSection {
	return generatedHelpOverviewSections(false)
}

func helpDetailSections() []helpSection {
	return generatedHelpDetailSections()
}

func generatedHelpOverviewSections(shared bool) []helpSection {
	sections := []helpSection{{title: "校园查询"}, {title: "个人事务"}, {title: "账户与通知"}}
	for _, descriptor := range capabilityDescriptors {
		if !overviewCapability(descriptor.ID) {
			continue
		}
		if shared && descriptor.Requirements.DataScope != DataScopePublic {
			continue
		}
		command := descriptor.Help.Title
		if len(descriptor.Help.Examples) > 0 {
			command = strings.Fields(descriptor.Help.Examples[0].Command)[0]
		}
		switch descriptor.ID {
		case CapabilityTodo:
			command = "待办（td）"
		case CapabilityHomework:
			command = "作业（hw）"
		case CapabilityExam:
			command = "考试（ks）"
		case CapabilityBus:
			command = "校车（xc）"
		}
		row := helpRow{
			topic: descriptor.Help.Topic, commandName: string(descriptor.ID),
			command: command, description: descriptor.Help.Summary,
		}
		sections[overviewSectionIndex(descriptor.ID)].rows = append(sections[overviewSectionIndex(descriptor.ID)].rows, row)
	}
	return sections
}

func overviewCapability(id CapabilityID) bool {
	switch id {
	case CapabilitySemester, CapabilityWeather, CapabilityRoomMap, CapabilityYoungEvent,
		CapabilityCourse, CapabilitySection, CapabilityTeacher, CapabilityBus,
		CapabilityCalendar, CapabilityTodo, CapabilityHomework, CapabilitySchedule,
		CapabilityExam, CapabilitySubscription, CapabilityLogin, CapabilityNotify,
		CapabilityFeedback:
		return true
	default:
		return false
	}
}

func overviewSectionIndex(id CapabilityID) int {
	switch id {
	case CapabilitySemester, CapabilityWeather, CapabilityRoomMap, CapabilityYoungEvent,
		CapabilityCourse, CapabilitySection, CapabilityTeacher, CapabilityBus:
		return 0
	case CapabilityLogin, CapabilityNotify, CapabilityFeedback:
		return 2
	default:
		return 1
	}
}

func generatedHelpDetailSections() []helpSection {
	sections := []helpSection{{title: "快捷入口", rows: nil}}
	seenTopics := map[string]int{}
	seenCommands := map[string]map[string]bool{}
	for _, descriptor := range capabilityDescriptors {
		for _, shortcut := range descriptor.Help.Shortcuts {
			sections[0].rows = append(sections[0].rows, helpRow{
				topic: "shortcuts", commandName: string(descriptor.ID),
				command: shortcut.Command, description: shortcut.Description,
			})
		}
		if descriptor.Help.Topic == "" || len(descriptor.Help.Examples) == 0 {
			continue
		}
		index, ok := seenTopics[descriptor.Help.Topic]
		if !ok {
			sections = append(sections, helpSection{title: descriptor.Help.Title})
			index = len(sections) - 1
			seenTopics[descriptor.Help.Topic] = index
			seenCommands[descriptor.Help.Topic] = map[string]bool{}
		}
		for _, example := range descriptor.Help.Examples {
			command := strings.TrimSpace(example.Command)
			if seenCommands[descriptor.Help.Topic][command] {
				continue
			}
			seenCommands[descriptor.Help.Topic][command] = true
			sections[index].rows = append(sections[index].rows, helpRow{
				topic: descriptor.Help.Topic, commandName: string(descriptor.ID),
				command: example.Command, description: example.Description,
			})
		}
	}
	return sections
}

func (h Handler) help(args ...string) string {
	return h.helpFor(false, args...)
}

func (h Handler) helpFor(shared bool, args ...string) string {
	// Help is a local capability, so expose its command document directly
	// instead of asking a model to recover it from the rendered table below.
	h.markData(structuredHelpDataFor(shared, args...))
	if len(args) == 0 {
		return HelpOverview(shared)
	}
	if len(args) == 1 && isHelpToken(args[0]) {
		return HelpOverview(shared)
	}
	topic := helpTopicCommand(args)
	text := formatHelpTopicFor(topic, shared)
	if text == "" {
		return h.notFound("没有找到一级命令“" + strings.TrimSpace(strings.Join(args, " ")) + "”。发送“帮助”查看命令总览。")
	}
	return text
}

func structuredHelpData(args ...string) map[string]any {
	return structuredHelpDataFor(false, args...)
}

func structuredHelpDataFor(shared bool, args ...string) map[string]any {
	documentation := SearchCapabilityDocumentation("", CapabilitySearchOptions{SharedConversation: shared})
	topic := ""
	if len(args) > 0 && !isHelpToken(args[0]) {
		topic = helpTopicCommand(args)
	}
	if topic != "" {
		filtered := make([]CapabilityDocumentation, 0, len(documentation))
		for _, item := range documentation {
			if item.Topic == topic {
				filtered = append(filtered, item)
			}
		}
		documentation = filtered
	}
	return map[string]any{
		"type":     "command_help",
		"topic":    topic,
		"commands": documentation,
		"shared":   shared,
		"query":    strings.TrimSpace(strings.Join(args, " ")),
	}
}

func formatHelpTopic(topic string) string {
	return formatHelpTopicFor(topic, false)
}

func formatHelpTopicFor(topic string, shared bool) string {
	title, ok := helpTopicTitles[topic]
	if !ok {
		return ""
	}
	lines := []string{title + " 帮助：", "命令\t说明"}
	for _, row := range helpDetailRowsFor(topic, shared) {
		lines = append(lines, row.command+"\t"+row.description)
	}
	lines = append(lines, "", "发送“帮助”返回命令总览。")
	return strings.Join(lines, "\n")
}

func invalidCapabilityUsageResponse(id CapabilityID) Response {
	descriptor, ok := CapabilityDescriptorFor(id)
	if !ok {
		return Response{
			Text: "没有找到这个能力。请先发送“帮助”查看可用命令。",
			Data: map[string]any{"type": "unknown_capability", "id": id},
			Kind: string(id),
		}
	}
	data := structuredHelpDataFor(false, string(id))
	lines := []string{descriptor.Help.Title + "的参数无法识别。"}
	examples := descriptor.Help.Examples
	if len(examples) == 0 {
		if help := formatHelpTopic(descriptor.Help.Topic); help != "" {
			lines = append(lines, "", help)
		}
		return Response{Text: strings.Join(lines, "\n"), Data: data, Kind: string(id)}
	}
	lines = append(lines, "", "可以这样发送：")
	for _, example := range examples {
		command := strings.TrimSpace(example.Command)
		if command == "" {
			continue
		}
		line := command
		if description := strings.TrimSpace(example.Description); description != "" {
			line += "：" + description
		}
		lines = append(lines, line)
	}
	return Response{Text: strings.Join(lines, "\n"), Data: data, Kind: string(id)}
}

func helpOverviewText() string {
	return HelpOverview(false)
}

// HelpOverview renders the user-facing command menu. Shared conversations
// receive only public capabilities, while private conversations include the
// personal Life and notification operations.
func HelpOverview(shared bool) string {
	lines := []string{
		"Bot 帮助：",
		"发送「帮助 课表」可以查看「课表」命令的具体用法。",
	}
	for _, section := range generatedHelpOverviewSections(shared) {
		if len(section.rows) == 0 {
			continue
		}
		lines = append(lines, "", section.title+"：", "命令\t说明")
		for _, row := range section.rows {
			lines = append(lines, row.command+"\t"+row.description)
		}
	}
	return strings.Join(lines, "\n")
}

func helpRichText(args ...string) string {
	if len(args) == 0 {
		return helpOverviewRichText()
	}
	topic := helpTopicCommand(args)
	title, ok := helpTopicTitle(topic)
	if !ok {
		return ""
	}
	lines := []string{
		"# " + title + " 帮助",
		"",
		markdownRichTableRow([]string{"命令", "说明"}),
		markdownRichTableRow([]string{"---", "---"}),
	}
	for _, row := range helpDetailRows(topic) {
		lines = append(lines, markdownRichTableRow([]string{row.command, row.description}))
	}
	lines = append(lines, "", "发送“帮助”返回命令总览。")
	return strings.Join(lines, "\n")
}

func helpOverviewRichText() string {
	lines := []string{
		"# Bot 帮助",
		"发送「帮助 课表」可以查看「课表」命令的具体用法。",
	}
	for _, section := range helpOverviewSections() {
		lines = append(lines,
			"",
			"## "+section.title,
			markdownRichTableRow([]string{"命令", "说明"}),
			markdownRichTableRow([]string{"---", "---"}),
		)
		for _, row := range section.rows {
			lines = append(lines, markdownRichTableRow([]string{row.command, row.description}))
		}
	}
	return strings.Join(lines, "\n")
}

var helpTopicAliases = map[string]string{
	"快捷入口":         "shortcuts",
	"快捷":           "shortcuts",
	"shortcuts":    "shortcuts",
	"shortcut":     "shortcuts",
	"日程":           "agenda",
	"agenda":       "agenda",
	"课表":           "schedule",
	"schedule":     "schedule",
	"kb":           "schedule",
	"待办":           "todo",
	"todo":         "todo",
	"作业":           "homework",
	"homework":     "homework",
	"考试":           "exam",
	"exam":         "exam",
	"课程":           "course",
	"course":       "course",
	"教学班":          "section",
	"section":      "section",
	"老师":           "teacher",
	"教师":           "teacher",
	"teacher":      "teacher",
	"学期":           "semester",
	"semester":     "semester",
	"订阅":           "subscription",
	"subscription": "subscription",
	"通知":           "notifications",
	"提醒":           "notifications",
	"notify":       "notifications",
	"校车":           "bus",
	"bus":          "bus",
	"天气":           "weather",
	"weather":      "weather",
	"教室":           "room",
	"地图":           "room",
	"room":         "room",
	"room_map":     "room",
	"第二课堂":         "young_event",
	"二课":           "young_event",
	"young_event":  "young_event",
	"账户":           "account",
	"账号":           "account",
	"account":      "account",
	"反馈":           "feedback",
	"feedback":     "feedback",
}

func helpTopicCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	key := commandToken(args[0])
	if topic, ok := helpTopicAliases[key]; ok {
		return topic
	}
	if _, ok := helpTopicTitles[key]; ok {
		return key
	}
	if name, _, ok := normalizeJoinedCommand(args[0], args[1:]); ok {
		return capabilityTopic(name)
	}
	name, _ := normalizeCommand(args[0], args[1:])
	return capabilityTopic(name)
}

func capabilityTopic(name string) string {
	descriptor, ok := descriptorForID(name)
	if !ok {
		return ""
	}
	return descriptor.Help.Topic
}

var helpTopicTitles = map[string]string{
	"shortcuts":     "快捷入口",
	"agenda":        "日程",
	"schedule":      "课表",
	"todo":          "待办",
	"homework":      "作业",
	"exam":          "考试",
	"course":        "课程",
	"section":       "教学班",
	"teacher":       "老师",
	"semester":      "学期",
	"subscription":  "订阅",
	"notifications": "通知",
	"bus":           "校车",
	"weather":       "天气",
	"room":          "教室",
	"young_event":   "第二课堂",
	"account":       "账户",
	"feedback":      "反馈",
}

func helpTopicTitle(topic string) (string, bool) {
	title, ok := helpTopicTitles[topic]
	return title, ok
}

func helpDetailRows(topic string) []helpRow {
	return helpDetailRowsFor(topic, false)
}

func helpDetailRowsFor(topic string, shared bool) []helpRow {
	rows := []helpRow{}
	for _, section := range helpDetailSections() {
		for _, row := range section.rows {
			if row.topic == topic {
				if shared {
					command := usageCommandWithoutShortcutLabel(row.command)
					invocation, parsed := ParseInvocation(command)
					if parsed && invocation.Policy().DataScope != DataScopePublic {
						continue
					}
				}
				if strings.Contains(row.description, "相当于“") && !strings.Contains(row.description, "”") {
					row.description += "”"
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}
