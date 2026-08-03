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
	return []helpSection{
		{
			title: "常用",
			rows: []helpRow{
				{topic: "agenda", command: "日程", description: "今日安排、综合概览与近期截止"},
				{topic: "schedule", command: "课表", description: "周课表、单日课表与下一节课"},
				{topic: "exam", command: "考试（ks）", description: "查看已订阅课程的考试"},
				{topic: "todo", command: "待办（td）", description: "查看和管理待办"},
				{topic: "homework", command: "作业（hw）", description: "查看和管理作业"},
				{topic: "bus", command: "校车（xc）", description: "查询班次、路线与设置偏好"},
			},
		},
		{
			title: "账户与系统",
			rows: []helpRow{
				{topic: "account", command: "账户", description: "登录、退出与查看账户信息"},
				{topic: "settings", command: "设置", description: "管理通知等偏好"},
				{topic: "advanced", command: "AI", description: "管理 AI 工具调用展示"},
				{topic: "system", command: "系统", description: "查看服务状态与检查连通性"},
				{topic: "feedback", command: "反馈", description: "向管理员提交反馈"},
			},
		},
	}
}

func helpDetailSections() []helpSection {
	return []helpSection{
		{
			title: "快捷入口",
			rows: []helpRow{
				{topic: "shortcuts", commandName: "bus", command: "校车（xc）", description: "直接查询接下来的校车"},
				{topic: "shortcuts", commandName: "schedule", command: "今日课表（单日课表）", description: "相当于“课表 单日 今天”"},
				{topic: "shortcuts", commandName: "login", command: "登录", description: "相当于“账户 登录”"},
				{topic: "shortcuts", commandName: "todo", command: "待办（td）", description: "直接查看未完成待办"},
				{topic: "shortcuts", commandName: "homework", command: "作业（hw）", description: "直接查看未完成作业"},
				{topic: "shortcuts", commandName: "calendar", command: "今日（ddl）", description: "相当于“日程 今日”"},
				{topic: "shortcuts", commandName: "overview", command: "概览", description: "相当于“日程 概览”"},
				{topic: "shortcuts", commandName: "upcoming_deadlines", command: "近期截止 14", description: "相当于“日程 截止 14”"},
				{topic: "shortcuts", commandName: "schedule", command: "明日课表", description: "相当于“课表 单日 明天”"},
				{topic: "shortcuts", commandName: "nextclass", command: "下一节课", description: "相当于“课表 下一节”"},
				{topic: "shortcuts", commandName: "account", command: "我的", description: "相当于“账户 信息”"},
				{topic: "shortcuts", commandName: "logout", command: "退出", description: "相当于“账户 退出”"},
				{topic: "shortcuts", commandName: "status", command: "状态（status）", description: "相当于“系统 状态”"},
				{topic: "shortcuts", commandName: "notify", command: "通知", description: "相当于“设置 通知”"},
				{topic: "shortcuts", commandName: "agent", command: "AI 工具", description: "相当于“设置 工具调用”"},
			},
		},
		{
			title: "日程",
			rows: []helpRow{
				{topic: "agenda", commandName: "calendar", command: "日程 今日", description: "汇总今日课程、待办和作业"},
				{topic: "agenda", commandName: "overview", command: "日程 概览", description: "汇总待办、作业和考试"},
				{topic: "agenda", commandName: "upcoming_deadlines", command: "日程 截止", description: "查看未来 7 天的截止事项"},
				{topic: "agenda", commandName: "upcoming_deadlines", command: "日程 截止 14", description: "指定未来天数"},
			},
		},
		{
			title: "课表",
			rows: []helpRow{
				{topic: "schedule", commandName: "schedule", command: "课表", description: "查看本周课表"},
				{topic: "schedule", commandName: "schedule", command: "课表 本周", description: "查看本周课表"},
				{topic: "schedule", commandName: "schedule", command: "课表 下周", description: "查看下周课表"},
				{topic: "schedule", commandName: "schedule", command: "课表 第3周", description: "查看指定教学周"},
				{topic: "schedule", commandName: "schedule", command: "课表 05.06", description: "查看该日期所在周"},
				{topic: "schedule", commandName: "schedule", command: "课表 7.20周", description: "用月日查看所在周"},
				{topic: "schedule", commandName: "schedule", command: "课表 2026-05-06", description: "用完整日期查看所在周"},
				{topic: "schedule", commandName: "schedule", command: "课表 单日 今天", description: "只查看今天的课表"},
				{topic: "schedule", commandName: "schedule", command: "课表 单日 明天", description: "只查看明天的课表"},
				{topic: "schedule", commandName: "nextclass", command: "课表 下一节", description: "查看最近一节课"},
			},
		},
		{
			title: "待办",
			rows: []helpRow{
				{topic: "todo", commandName: "todo", command: "待办", description: "查看未完成待办，每页 30 条"},
				{topic: "todo", commandName: "todo", command: "待办 列表 第2页", description: "查看未完成待办的第 2 页"},
				{topic: "todo", commandName: "todo", command: "待办 列表 全部", description: "查看全部待办，每页 30 条"},
				{topic: "todo", commandName: "todo", command: "待办 列表 未完成", description: "只查看未完成待办"},
				{topic: "todo", commandName: "todo", command: "待办 列表 已完成", description: "只查看已完成待办"},
				{topic: "todo", commandName: "todo", command: "待办 列表 优先级 高", description: "按优先级筛选"},
				{topic: "todo", commandName: "todo", command: "待办 列表 截止前 2026-06-10", description: "筛选该日期前截止的待办"},
				{topic: "todo", commandName: "todo", command: "待办 列表 截止后 2026-06-10", description: "筛选该日期后截止的待办"},
				{topic: "todo", commandName: "todo", command: "待办 添加 写报告", description: "新增待办"},
				{topic: "todo", commandName: "todo", command: "待办 添加 写报告 内容 初稿 截止 2026-06-10", description: "新增带详细信息的待办"},
				{topic: "todo", commandName: "todo", command: "待办 完成 1", description: "完成第 1 条待办"},
				{topic: "todo", commandName: "todo", command: "待办 完成 1,2,3", description: "批量完成待办"},
				{topic: "todo", commandName: "todo", command: "待办 恢复 1", description: "恢复已完成待办"},
				{topic: "todo", commandName: "todo", command: "待办 恢复 1,2,3", description: "批量恢复已完成待办"},
				{topic: "todo", commandName: "todo", command: "待办 删除 1", description: "删除第 1 条待办"},
				{topic: "todo", commandName: "todo", command: "待办 删除 1,2,3", description: "批量删除待办"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 标题 新标题", description: "修改待办标题"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 内容 新备注", description: "修改待办内容"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 截止 2026-06-12", description: "修改截止日期"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 优先级 中", description: "修改优先级"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 完成", description: "标为已完成"},
				{topic: "todo", commandName: "todo", command: "待办 更新 1 未完成", description: "恢复为未完成"},
			},
		},
		{
			title: "作业",
			rows: []helpRow{
				{topic: "homework", commandName: "homework", command: "作业", description: "查看未完成作业，每页 30 条"},
				{topic: "homework", commandName: "homework", command: "作业 列表 第2页", description: "查看未完成作业的第 2 页"},
				{topic: "homework", commandName: "homework", command: "作业 列表 未完成", description: "只查看未完成作业"},
				{topic: "homework", commandName: "homework", command: "作业 列表 全部", description: "查看全部作业，每页 30 条"},
				{topic: "homework", commandName: "homework", command: "作业 列表 学期ID <ID>", description: "按学期 ID 筛选"},
				{topic: "homework", commandName: "homework", command: "作业 列表 学期JWID <JW ID>", description: "按学期 JW ID 筛选"},
				{topic: "homework", commandName: "homework", command: "作业 完成 1", description: "完成第 1 条作业"},
				{topic: "homework", commandName: "homework", command: "作业 完成 1,2,3", description: "批量完成作业"},
				{topic: "homework", commandName: "homework", command: "作业 恢复 1", description: "取消第 1 条作业的完成状态"},
				{topic: "homework", commandName: "homework", command: "作业 恢复 1,2,3", description: "批量取消完成状态"},
			},
		},
		{
			title: "考试",
			rows: []helpRow{
				{topic: "exam", commandName: "exam", command: "考试", description: "查看已订阅课程的考试，每页 30 条"},
				{topic: "exam", commandName: "exam", command: "考试 第2页", description: "查看考试的第 2 页"},
			},
		},
		{
			title: "课程",
			rows: []helpRow{
				{topic: "course", commandName: "course", command: "课程 数学分析", description: "按关键词快速搜索课程"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 数学分析", description: "搜索课程"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 关键词 数学分析", description: "显式指定关键词"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 培养层次ID <ID>", description: "按培养层次筛选"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 类别ID <ID>", description: "按课程类别筛选"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 课堂类型ID <ID>", description: "按课堂类型筛选"},
				{topic: "course", commandName: "course_search", command: "课程 搜索 数学分析 数量 <数量>", description: "限制返回数量"},
				{topic: "course", commandName: "course_by_jw_id", command: "课程 查看 <JW ID>", description: "按 JW ID 查看详情"},
			},
		},
		{
			title: "教学班",
			rows: []helpRow{
				{topic: "section", commandName: "section", command: "教学班 高等数学", description: "按关键词快速搜索教学班"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 高等数学", description: "搜索教学班"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 关键词 高等数学", description: "显式指定关键词"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 课程ID <ID>", description: "按课程 ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 课程JWID <JW ID>", description: "按课程 JW ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 学期ID <ID>", description: "按学期 ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 学期JWID <JW ID>", description: "按学期 JW ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 校区ID <ID>", description: "按校区 ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 院系ID <ID>", description: "按院系 ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 老师ID <ID>", description: "按老师 ID 筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 老师代码 <代码>", description: "按老师代码筛选"},
				{topic: "section", commandName: "section_search", command: "教学班 搜索 高等数学 数量 <数量>", description: "限制返回数量"},
				{topic: "section", commandName: "section_by_jw_id", command: "教学班 查看 <JW ID>", description: "按 JW ID 查看详情"},
				{topic: "section", commandName: "section_schedules", command: "教学班 课表 <JW ID> <开始日期> <结束日期>", description: "查看日期范围内的课表"},
				{topic: "section", commandName: "section_exams", command: "教学班 考试 <JW ID>", description: "查看指定教学班考试，每页 30 条"},
				{topic: "section", commandName: "section_exams", command: "教学班 考试 <JW ID> 第2页", description: "查看指定教学班考试的第 2 页"},
				{topic: "section", commandName: "section_homeworks", command: "教学班 作业 <JW ID>", description: "查看指定教学班作业，每页 30 条"},
				{topic: "section", commandName: "section_homeworks", command: "教学班 作业 <JW ID> 第2页", description: "查看指定教学班作业的第 2 页"},
			},
		},
		{
			title: "老师",
			rows: []helpRow{
				{topic: "teacher", commandName: "teacher", command: "老师 张", description: "按关键词快速搜索老师"},
				{topic: "teacher", commandName: "teacher_search", command: "老师 搜索 张", description: "搜索老师"},
				{topic: "teacher", commandName: "teacher_search", command: "老师 搜索 关键词 张", description: "显式指定关键词"},
				{topic: "teacher", commandName: "teacher_search", command: "老师 搜索 院系ID <ID>", description: "按院系 ID 筛选"},
				{topic: "teacher", commandName: "teacher_search", command: "老师 搜索 张 数量 <数量>", description: "限制返回数量"},
				{topic: "teacher", commandName: "teacher_by_id", command: "老师 查看 <ID>", description: "按 ID 查看详情"},
			},
		},
		{
			title: "学期",
			rows: []helpRow{
				{topic: "semester", commandName: "semester", command: "学期", description: "查看当前学期"},
				{topic: "semester", commandName: "semester", command: "学期 当前", description: "查看当前学期"},
				{topic: "semester", commandName: "list_semesters", command: "学期 列表", description: "列出最近 20 个学期"},
				{topic: "semester", commandName: "list_semesters", command: "学期 列表 10", description: "指定返回数量"},
			},
		},
		{
			title: "订阅",
			rows: []helpRow{
				{topic: "subscription", commandName: "subscription", command: "订阅", description: "查看已订阅教学班"},
				{topic: "subscription", commandName: "my_subscribed_sections", command: "订阅 列表", description: "查看已订阅教学班"},
				{topic: "subscription", commandName: "subscription", command: "订阅 添加 CONT5103P.01 CONT6104P.01", description: "批量订阅教学班"},
				{topic: "subscription", commandName: "unsubscribe_section_by_jw_id", command: "订阅 删除 <JW ID>", description: "按 JW ID 退订教学班"},
				{topic: "subscription", commandName: "subscription", command: "订阅 链接", description: "查看私有日历订阅链接"},
			},
		},
		{
			title: "校车",
			rows: []helpRow{
				{topic: "bus", commandName: "bus", command: "校车", description: "查看接下来各路线的校车"},
				{topic: "bus", commandName: "bus", command: "校车 查询 全部", description: "查看今天全部班次"},
				{topic: "bus", commandName: "bus", command: "校车 查询 我的路线", description: "查看偏好路线"},
				{topic: "bus", commandName: "bus", command: "校车 查询 东区 西区", description: "查询指定路线"},
				{topic: "bus", commandName: "bus", command: "校车 查询 之后 14:00", description: "查询指定时间后的全部路线"},
				{topic: "bus", commandName: "bus", command: "校车 查询 东区 西区 之后 14:00", description: "查询指定时间后的班次"},
				{topic: "bus", commandName: "bus", command: "校车 查询 东区 西区 已发车", description: "查询并包含已发车班次"},
				{topic: "bus", commandName: "bus_routes", command: "校车 路线", description: "列出全部校车路线"},
				{topic: "bus", commandName: "bus_routes", command: "校车 路线 从 东区 到 西区", description: "筛选起点和终点"},
				{topic: "bus", commandName: "bus", command: "校车 偏好", description: "查看校车偏好"},
				{topic: "bus", commandName: "bus", command: "校车 偏好 路线 东区 西区", description: "设置偏好路线"},
				{topic: "bus", commandName: "bus", command: "校车 偏好 已发车 开", description: "默认显示已发车班次"},
				{topic: "bus", commandName: "bus", command: "校车 偏好 已发车 关", description: "默认隐藏已发车班次"},
				{topic: "bus", commandName: "bus", command: "校车 偏好 南区 开", description: "显示南区校车"},
				{topic: "bus", commandName: "bus", command: "校车 偏好 南区 关", description: "隐藏南区校车"},
			},
		},
		{
			title: "账户",
			rows: []helpRow{
				{topic: "account", commandName: "login", command: "账户 登录", description: "开始 Life@USTC 登录"},
				{topic: "account", commandName: "login", command: "账户 登录状态", description: "查询当前登录流程"},
				{topic: "account", commandName: "account", command: "账户 信息", description: "查看当前登录用户"},
				{topic: "account", commandName: "status", command: "账户 状态", description: "查看服务与登录状态"},
				{topic: "account", commandName: "logout", command: "账户 退出", description: "退出并清除登录状态"},
			},
		},
		{
			title: "设置",
			rows: []helpRow{
				{topic: "settings", commandName: "settings", command: "设置", description: "查看设置命令"},
				{topic: "settings", commandName: "notify", command: "设置 通知", description: "查看通知设置"},
				{topic: "settings", commandName: "notify", command: "设置 通知 课表 开", description: "开启课前提醒"},
				{topic: "settings", commandName: "notify", command: "设置 通知 课表 关", description: "关闭课前提醒"},
				{topic: "settings", commandName: "notify", command: "设置 通知 作业 开", description: "开启作业提醒"},
				{topic: "settings", commandName: "notify", command: "设置 通知 作业 关", description: "关闭作业提醒"},
			},
		},
		{
			title: "AI",
			rows: []helpRow{
				{topic: "advanced", commandName: "agent", command: "AI 工具", description: "查看当前工具调用提示设置"},
				{topic: "advanced", commandName: "agent", command: "AI 工具 开", description: "回答时显示 LLM 工具调用提示"},
				{topic: "advanced", commandName: "agent", command: "AI 工具 关", description: "回答时隐藏 LLM 工具调用提示"},
				{topic: "advanced", commandName: "agent", command: "设置 工具调用", description: "等同于「AI 工具」"},
				{topic: "advanced", commandName: "agent", command: "设置 工具调用 开", description: "等同于「AI 工具 开」"},
				{topic: "advanced", commandName: "agent", command: "设置 工具调用 关", description: "等同于「AI 工具 关」"},
			},
		},
		{
			title: "系统",
			rows: []helpRow{
				{topic: "system", commandName: "status", command: "系统 状态", description: "查看服务与登录状态"},
				{topic: "system", commandName: "ping", command: "系统 检查", description: "检查 Life@USTC API 是否可用"},
			},
		},
		{
			title: "反馈",
			rows: []helpRow{
				{topic: "feedback", commandName: "feedback", command: "反馈 <你的建议>", description: "向管理员提交反馈"},
			},
		},
	}
}

func (h Handler) help(args ...string) string {
	if len(args) == 0 {
		return helpOverviewText()
	}
	topic := helpTopicCommand(args)
	text := formatHelpTopic(topic)
	if text == "" {
		return "没有找到一级命令“" + strings.TrimSpace(strings.Join(args, " ")) + "”。发送“帮助”查看命令总览。"
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
	"AI":           "advanced",
	"ai":           "advanced",
	"进阶":           "advanced",
	"高级":           "advanced",
	"帮助AI":         "advanced",
	"帮助ai":         "advanced",
	"快捷入口":         "shortcuts",
	"快捷":           "shortcuts",
	"shortcuts":    "shortcuts",
	"shortcut":     "shortcuts",
	"日程":           "agenda",
	"agenda":       "agenda",
	"课表":           "schedule",
	"schedule":     "schedule",
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

var internalHelpTopics = map[string]string{
	"calendar":                     "agenda",
	"overview":                     "agenda",
	"upcoming_deadlines":           "agenda",
	"schedule":                     "schedule",
	"nextclass":                    "schedule",
	"todo":                         "todo",
	"homework":                     "homework",
	"exam":                         "exam",
	"course":                       "course",
	"course_search":                "course",
	"course_by_jw_id":              "course",
	"section":                      "section",
	"section_search":               "section",
	"section_by_jw_id":             "section",
	"section_schedules":            "section",
	"section_exams":                "section",
	"section_homeworks":            "section",
	"teacher":                      "teacher",
	"teacher_search":               "teacher",
	"teacher_by_id":                "teacher",
	"semester":                     "semester",
	"list_semesters":               "semester",
	"subscription":                 "subscription",
	"my_subscribed_sections":       "subscription",
	"unsubscribe_section_by_jw_id": "subscription",
	"bus":                          "bus",
	"bus_routes":                   "bus",
	"login":                        "account",
	"logout":                       "account",
	"account":                      "account",
	"settings":                     "settings",
	"notify":                       "settings",
	"agent":                        "advanced",
	"status":                       "system",
	"ping":                         "system",
	"feedback":                     "feedback",
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
		return internalHelpTopics[name]
	}
	name, _ := normalizeCommand(args[0], args[1:])
	return internalHelpTopics[name]
}

var helpTopicTitles = map[string]string{
	"advanced":     "AI",
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

func helpOverviewRow(topic string) (helpRow, bool) {
	for _, section := range helpOverviewSections() {
		for _, row := range section.rows {
			if row.topic == topic {
				return row, true
			}
		}
	}
	return helpRow{}, false
}

func helpDetailRows(topic string) []helpRow {
	rows := []helpRow{}
	for _, section := range helpDetailSections() {
		for _, row := range section.rows {
			if row.topic == topic {
				rows = append(rows, row)
			}
		}
	}
	return rows
}
