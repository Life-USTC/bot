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
	return generatedHelpOverviewSections()
}

func helpDetailSections() []helpSection {
	return generatedHelpDetailSections()
}

func generatedHelpOverviewSections() []helpSection {
	sections := []helpSection{{title: "常用"}, {title: "账户与系统"}}
	for _, descriptor := range capabilityDescriptors {
		if !descriptor.Help.Overview || descriptor.Help.Topic == "teacher" {
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
		if descriptor.Help.Topic == "agenda" || descriptor.Help.Topic == "schedule" || descriptor.Help.Topic == "exam" || descriptor.Help.Topic == "todo" || descriptor.Help.Topic == "homework" || descriptor.Help.Topic == "bus" {
			sections[0].rows = append(sections[0].rows, row)
		} else {
			sections[1].rows = append(sections[1].rows, row)
		}
	}
	return sections
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
	if len(args) == 0 {
		return helpOverviewText()
	}
	topic := helpTopicCommand(args)
	text := formatHelpTopic(topic)
	if text == "" {
		return h.notFound("没有找到一级命令“" + strings.TrimSpace(strings.Join(args, " ")) + "”。发送“帮助”查看命令总览。")
	}
	return text
}

func formatHelpTopic(topic string) string {
	title, ok := helpTopicTitles[topic]
	if !ok {
		return ""
	}
	lines := []string{title + " 帮助：", "命令\t说明"}
	for _, row := range helpDetailRows(topic) {
		lines = append(lines, row.command+"\t"+row.description)
	}
	lines = append(lines, "", "发送“帮助”返回命令总览。")
	return strings.Join(lines, "\n")
}

func helpOverviewText() string {
	lines := []string{
		"Bot 帮助：",
		"发送「帮助 课表」可以查看「课表」命令的具体用法。",
	}
	for _, section := range helpOverviewSections() {
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
	"校车":           "bus",
	"bus":          "bus",
	"账户":           "account",
	"账号":           "account",
	"account":      "account",
	"设置":           "settings",
	"settings":     "settings",
	"系统":           "system",
	"system":       "system",
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
	"shortcuts":    "快捷入口",
	"agenda":       "日程",
	"schedule":     "课表",
	"todo":         "待办",
	"homework":     "作业",
	"exam":         "考试",
	"course":       "课程",
	"section":      "教学班",
	"teacher":      "老师",
	"semester":     "学期",
	"subscription": "订阅",
	"bus":          "校车",
	"account":      "账户",
	"settings":     "设置",
	"system":       "系统",
	"feedback":     "反馈",
}

func helpTopicTitle(topic string) (string, bool) {
	title, ok := helpTopicTitles[topic]
	return title, ok
}

func helpDetailRows(topic string) []helpRow {
	rows := []helpRow{}
	for _, section := range helpDetailSections() {
		for _, row := range section.rows {
			if row.topic == topic {
				if strings.Contains(row.description, "相当于“") && !strings.Contains(row.description, "”") {
					row.description += "”"
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}
