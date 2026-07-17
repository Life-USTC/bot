package commands

import "strings"

type helpRow struct {
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
			title: "基础与账户",
			rows: []helpRow{
				{commandName: "login", command: "登录", description: "登录 Life@USTC"},
				{commandName: "logout", command: "退出", description: "退出并清除登录状态"},
				{commandName: "me", command: "我（me）", description: "查看当前登录用户"},
				{commandName: "status", command: "状态（status）", description: "查看服务与登录状态"},
				{commandName: "ping", command: "ping（p）", description: "检查 Life@USTC API"},
				{commandName: "semester", command: "学期", description: "查看当前学期"},
				{commandName: "list_semesters", command: "学期列表", description: "查看学期列表"},
			},
		},
		{
			title: "课程与日程",
			rows: []helpRow{
				{commandName: "overview", command: "今日（ddl）", description: "查看今日汇总"},
				{commandName: "schedule", command: "课表", description: "查看周课表或单日课表"},
				{commandName: "nextclass", command: "下一节课", description: "查看最近一节课"},
				{commandName: "exam", command: "考试（ks）", description: "查看考试"},
				{commandName: "course", command: "课程", description: "搜索课程"},
				{commandName: "section", command: "教学班", description: "搜索教学班"},
				{commandName: "teacher", command: "老师", description: "搜索老师"},
				{commandName: "dashboard", command: "概览", description: "汇总待办、作业和考试"},
				{commandName: "upcoming_deadlines", command: "近期截止", description: "查看近期截止事项"},
			},
		},
		{
			title: "任务",
			rows: []helpRow{
				{commandName: "todo", command: "待办（td）", description: "查看和管理待办"},
				{commandName: "homework", command: "作业（hw）", description: "查看和管理作业"},
			},
		},
		{
			title: "订阅与通知",
			rows: []helpRow{
				{commandName: "subscription", command: "订阅", description: "管理教学班订阅"},
				{commandName: "my_subscribed_sections", command: "我的订阅", description: "查看已订阅教学班"},
				{commandName: "unsubscribe_section_by_jw_id", command: "退订教学班", description: "按 JW ID 退订"},
				{commandName: "notify", command: "通知", description: "管理课表和作业提醒"},
			},
		},
		{
			title: "校车",
			rows: []helpRow{
				{commandName: "bus", command: "校车（xc）", description: "查询班次与设置偏好"},
				{commandName: "bus_routes", command: "校车路线", description: "查询校车路线"},
			},
		},
		{
			title: "高级查询",
			rows: []helpRow{
				{commandName: "course_search", command: "课程搜索", description: "按字段搜索课程"},
				{commandName: "section_search", command: "教学班搜索", description: "按字段搜索教学班"},
				{commandName: "teacher_search", command: "老师搜索", description: "按字段搜索老师"},
				{commandName: "course_by_jw_id", command: "课程编号", description: "按 JW ID 查看课程"},
				{commandName: "section_by_jw_id", command: "教学班编号", description: "按 JW ID 查看教学班"},
				{commandName: "teacher_by_id", command: "老师编号", description: "按 ID 查看老师"},
				{commandName: "section_schedules", command: "教学班课表", description: "查看指定教学班课表"},
				{commandName: "section_exams", command: "教学班考试", description: "查看指定教学班考试"},
				{commandName: "section_homeworks", command: "教学班作业", description: "查看指定教学班作业"},
			},
		},
		{
			title: "AI 与反馈",
			rows: []helpRow{
				{commandName: "agent", command: "AI 工具", description: "设置工具调用展示"},
				{commandName: "feedback", command: "反馈", description: "向管理员提交反馈"},
			},
		},
	}
}

func helpDetailSections() []helpSection {
	return []helpSection{
		{
			title: "基础与账户",
			rows: []helpRow{
				{commandName: "login", command: "登录", description: "开始 Life@USTC 登录"},
				{commandName: "login", command: "登录 状态", description: "查询当前登录流程"},
				{commandName: "logout", command: "退出", description: "退出并清除登录状态"},
				{commandName: "me", command: "我（me）", description: "查看当前登录用户"},
				{commandName: "status", command: "状态（status）", description: "查看服务与登录状态"},
				{commandName: "ping", command: "ping（p）", description: "检查 Life@USTC API 是否可用"},
				{commandName: "semester", command: "学期", description: "查看当前学期"},
				{commandName: "list_semesters", command: "学期列表", description: "列出最近 20 个学期"},
				{commandName: "list_semesters", command: "学期列表 10", description: "指定返回的学期数量"},
			},
		},
		{
			title: "课程与日程",
			rows: []helpRow{
				{commandName: "overview", command: "今日（ddl）", description: "汇总今日课程、待办和作业"},
				{commandName: "schedule", command: "课表", description: "查看本周课表"},
				{commandName: "schedule", command: "课表 本周", description: "查看本周课表"},
				{commandName: "schedule", command: "课表 下周", description: "查看下周课表"},
				{commandName: "schedule", command: "课表 第3周", description: "查看指定教学周"},
				{commandName: "schedule", command: "课表 05.06", description: "查看该日期所在周"},
				{commandName: "schedule", command: "课表 7.20周", description: "用月日查看所在周"},
				{commandName: "schedule", command: "课表 2026-05-06", description: "用完整日期查看所在周"},
				{commandName: "schedule", command: "今日课表（单日课表）", description: "只查看今天的课表"},
				{commandName: "schedule", command: "明日课表", description: "只查看明天的课表"},
				{commandName: "nextclass", command: "下一节课", description: "查看最近一节课"},
				{commandName: "exam", command: "考试（ks）", description: "查看已订阅课程的考试"},
				{commandName: "course", command: "课程 数学分析", description: "按关键词搜索课程"},
				{commandName: "section", command: "教学班 高等数学", description: "按关键词搜索教学班"},
				{commandName: "teacher", command: "老师 张", description: "按关键词搜索老师"},
				{commandName: "dashboard", command: "概览", description: "汇总待办、作业和考试"},
				{commandName: "upcoming_deadlines", command: "近期截止", description: "查看未来 7 天的截止事项"},
				{commandName: "upcoming_deadlines", command: "近期截止 14", description: "指定未来天数"},
			},
		},
		{
			title: "待办",
			rows: []helpRow{
				{commandName: "todo", command: "待办（td）", description: "查看未完成待办"},
				{commandName: "todo", command: "待办 全部", description: "查看全部待办"},
				{commandName: "todo", command: "待办 未完成", description: "只查看未完成待办"},
				{commandName: "todo", command: "待办 已完成", description: "只查看已完成待办"},
				{commandName: "todo", command: "待办 高", description: "按优先级筛选待办"},
				{commandName: "todo", command: "待办 列表 截止前 2026-06-10", description: "筛选指定日期前截止的待办"},
				{commandName: "todo", command: "待办 列表 截止后 2026-06-10", description: "筛选指定日期后截止的待办"},
				{commandName: "todo", command: "待办 列表 优先级 高", description: "在列表中按优先级筛选"},
				{commandName: "todo", command: "待办 写报告", description: "快速新增待办"},
				{commandName: "todo", command: "待办 新增 写报告", description: "新增待办"},
				{commandName: "todo", command: "待办 新增 写报告 内容 完成初稿 截止 2026-06-10 优先级 高", description: "新增带内容、截止日期和优先级的待办"},
				{commandName: "todo", command: "待办 完成 1", description: "完成第 1 条待办"},
				{commandName: "todo", command: "待办 完成 1,2,3", description: "批量完成待办"},
				{commandName: "todo", command: "待办 撤销 1", description: "恢复已完成待办"},
				{commandName: "todo", command: "待办 撤销 1,2,3", description: "批量恢复已完成待办"},
				{commandName: "todo", command: "待办 删除 1", description: "删除第 1 条待办"},
				{commandName: "todo", command: "待办 删除 1,2,3", description: "批量删除待办"},
				{commandName: "todo", command: "待办 更新 1 标题 新标题", description: "修改待办标题"},
				{commandName: "todo", command: "待办 更新 1 内容 新备注", description: "修改待办内容"},
				{commandName: "todo", command: "待办 更新 1 截止 2026-06-12", description: "修改待办截止日期"},
				{commandName: "todo", command: "待办 更新 1 优先级 中", description: "修改待办优先级"},
				{commandName: "todo", command: "待办 更新 1 完成", description: "把待办标为已完成"},
				{commandName: "todo", command: "待办 更新 1 未完成", description: "把待办恢复为未完成"},
			},
		},
		{
			title: "作业",
			rows: []helpRow{
				{commandName: "homework", command: "作业（hw）", description: "查看未完成作业"},
				{commandName: "homework", command: "作业 未完成", description: "只查看未完成作业"},
				{commandName: "homework", command: "作业 全部", description: "查看全部作业"},
				{commandName: "homework", command: "作业 semester_id <学期 ID>", description: "按学期 ID 筛选"},
				{commandName: "homework", command: "作业 semester_jw_id <学期 JW ID>", description: "按学期 JW ID 筛选"},
				{commandName: "homework", command: "作业 完成 1", description: "完成第 1 条作业"},
				{commandName: "homework", command: "作业 完成 1,2,3", description: "批量完成作业"},
				{commandName: "homework", command: "作业 撤销 1", description: "取消作业完成状态"},
				{commandName: "homework", command: "作业 撤销 1,2,3", description: "批量取消完成状态"},
			},
		},
		{
			title: "订阅与通知",
			rows: []helpRow{
				{commandName: "subscription", command: "订阅", description: "查看已订阅教学班"},
				{commandName: "subscription", command: "订阅 链接", description: "查看私有日历订阅链接"},
				{commandName: "subscription", command: "订阅 导入 CONT5103P.01 CONT6104P.01", description: "批量订阅教学班"},
				{commandName: "subscription", command: "订阅 CONT5103P.01", description: "直接订阅教学班代码"},
				{commandName: "my_subscribed_sections", command: "我的订阅", description: "查看已订阅教学班"},
				{commandName: "unsubscribe_section_by_jw_id", command: "退订教学班 <JW ID>", description: "按 JW ID 退订教学班"},
				{commandName: "notify", command: "通知", description: "查看通知设置"},
				{commandName: "notify", command: "通知 状态", description: "查看通知设置"},
				{commandName: "notify", command: "通知 课表 开", description: "开启课前提醒"},
				{commandName: "notify", command: "通知 课表 关", description: "关闭课前提醒"},
				{commandName: "notify", command: "通知 作业 开", description: "开启作业提醒"},
				{commandName: "notify", command: "通知 作业 关", description: "关闭作业提醒"},
			},
		},
		{
			title: "校车",
			rows: []helpRow{
				{commandName: "bus", command: "校车（xc）", description: "查看接下来各路线的校车"},
				{commandName: "bus", command: "校车 全部", description: "查看今天全部班次"},
				{commandName: "bus", command: "校车 我的路线", description: "查看偏好路线"},
				{commandName: "bus", command: "校车 东区 西区", description: "查询指定路线"},
				{commandName: "bus", command: "校车 之后 14:00", description: "查询指定时间后的全部路线"},
				{commandName: "bus", command: "校车 东区 西区 之后 14:00", description: "查询指定时间后的班次"},
				{commandName: "bus", command: "校车 东区 西区 已发车", description: "查询路线并包含已发车班次"},
				{commandName: "bus", command: "校车 偏好", description: "查看校车偏好"},
				{commandName: "bus", command: "校车 设置 东区 西区", description: "设置偏好路线"},
				{commandName: "bus", command: "校车 已发车 开", description: "默认显示已发车班次"},
				{commandName: "bus", command: "校车 已发车 关", description: "默认隐藏已发车班次"},
				{commandName: "bus", command: "校车 南区 开", description: "显示南区校车"},
				{commandName: "bus", command: "校车 南区 关", description: "隐藏南区校车"},
				{commandName: "bus_routes", command: "校车路线", description: "列出全部校车路线"},
				{commandName: "bus_routes", command: "校车路线 从 东区 到 西区", description: "筛选起点和终点"},
			},
		},
		{
			title: "高级查询",
			rows: []helpRow{
				{commandName: "course_search", command: "课程搜索 <关键词>", description: "高级搜索课程"},
				{commandName: "course_search", command: "课程搜索 keyword 数学分析", description: "用 keyword 指定课程关键词"},
				{commandName: "course_search", command: "课程搜索 education_level_id <ID>", description: "按培养层次筛选课程"},
				{commandName: "course_search", command: "课程搜索 category_id <ID>", description: "按课程类别筛选"},
				{commandName: "course_search", command: "课程搜索 class_type_id <ID>", description: "按课堂类型筛选"},
				{commandName: "course_search", command: "课程搜索 数学分析 limit <数量>", description: "限制课程结果数量"},
				{commandName: "section_search", command: "教学班搜索 <关键词>", description: "高级搜索教学班"},
				{commandName: "section_search", command: "教学班搜索 keyword 高等数学", description: "用 keyword 指定教学班关键词"},
				{commandName: "section_search", command: "教学班搜索 course_id <ID>", description: "按课程 ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 course_jw_id <JW ID>", description: "按课程 JW ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 semester_id <ID>", description: "按学期 ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 semester_jw_id <JW ID>", description: "按学期 JW ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 campus_id <ID>", description: "按校区 ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 department_id <ID>", description: "按院系 ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 teacher_id <ID>", description: "按老师 ID 筛选"},
				{commandName: "section_search", command: "教学班搜索 teacher_code <代码>", description: "按老师代码筛选"},
				{commandName: "section_search", command: "教学班搜索 高等数学 limit <数量>", description: "限制教学班结果数量"},
				{commandName: "teacher_search", command: "老师搜索 <关键词>", description: "高级搜索老师"},
				{commandName: "teacher_search", command: "老师搜索 keyword 张", description: "用 keyword 指定老师关键词"},
				{commandName: "teacher_search", command: "老师搜索 department_id <ID>", description: "按院系 ID 筛选老师"},
				{commandName: "teacher_search", command: "老师搜索 张 limit <数量>", description: "限制老师结果数量"},
				{commandName: "course_by_jw_id", command: "课程编号 <JW ID>", description: "按 JW ID 查看课程详情"},
				{commandName: "section_by_jw_id", command: "教学班编号 <JW ID>", description: "按 JW ID 查看教学班详情"},
				{commandName: "teacher_by_id", command: "老师编号 <ID>", description: "按 ID 查看老师详情"},
				{commandName: "section_schedules", command: "教学班课表 <JW ID> <开始日期> <结束日期>", description: "查看教学班在日期范围内的课表"},
				{commandName: "section_exams", command: "教学班考试 <JW ID>", description: "查看指定教学班考试"},
				{commandName: "section_homeworks", command: "教学班作业 <JW ID>", description: "查看指定教学班作业"},
			},
		},
		{
			title: "AI 与反馈",
			rows: []helpRow{
				{commandName: "agent", command: "AI 工具", description: "查看工具调用展示设置"},
				{commandName: "agent", command: "AI 工具 状态", description: "查看工具调用展示设置"},
				{commandName: "agent", command: "AI 工具 开", description: "显示 LLM 工具调用"},
				{commandName: "agent", command: "AI 工具 关", description: "隐藏 LLM 工具调用"},
				{commandName: "feedback", command: "反馈 <你的建议>", description: "向管理员提交反馈"},
			},
		},
	}
}

func (h Handler) help(args ...string) string {
	if len(args) == 0 {
		return helpOverviewText()
	}
	commandName := helpTopicCommand(args)
	overview, ok := helpOverviewRow(commandName)
	if !ok {
		return "没有找到一级命令“" + strings.TrimSpace(strings.Join(args, " ")) + "”。发送“帮助”查看命令总览。"
	}
	lines := []string{overview.command + " 帮助：", "命令\t说明"}
	for _, row := range helpDetailRows(commandName) {
		lines = append(lines, row.command+"\t"+row.description)
	}
	lines = append(lines, "", "发送“帮助”返回命令总览。")
	return strings.Join(lines, "\n")
}

func helpOverviewText() string {
	lines := []string{
		"Bot 帮助：",
		"发送“帮助 课表”等命令查看具体用法。",
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
	commandName := helpTopicCommand(args)
	overview, ok := helpOverviewRow(commandName)
	if !ok {
		return ""
	}
	lines := []string{
		"# " + overview.command + " 帮助",
		"",
		markdownRichTableRow([]string{"命令", "说明"}),
		markdownRichTableRow([]string{"---", "---"}),
	}
	for _, row := range helpDetailRows(commandName) {
		lines = append(lines, markdownRichTableRow([]string{row.command, row.description}))
	}
	lines = append(lines, "", "发送“帮助”返回命令总览。")
	return strings.Join(lines, "\n")
}

func helpOverviewRichText() string {
	lines := []string{
		"# Bot 帮助",
		"",
		"发送“帮助 课表”等命令查看具体用法。",
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

func helpTopicCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	name, _, ok := normalizeJoinedCommand(args[0], args[1:])
	if ok {
		return name
	}
	name, _ = normalizeCommand(args[0], args[1:])
	if name == "help" {
		return ""
	}
	return name
}

func helpOverviewRow(commandName string) (helpRow, bool) {
	for _, section := range helpOverviewSections() {
		for _, row := range section.rows {
			if row.commandName == commandName {
				return row, true
			}
		}
	}
	return helpRow{}, false
}

func helpDetailRows(commandName string) []helpRow {
	rows := []helpRow{}
	for _, section := range helpDetailSections() {
		for _, row := range section.rows {
			if row.commandName == commandName {
				rows = append(rows, row)
			}
		}
	}
	return rows
}
