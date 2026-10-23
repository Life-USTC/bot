package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

type recurringReminderQuery struct {
	Action                  string
	ID                      int64
	Spec                    store.RecurringReminderSpec
	TimezoneSet, EndDateSet bool
}

func normalizeRecurringReminderArgs(args []string) []string {
	args = append([]string(nil), args...)
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(args[0]) {
	case "添加", "新建", "create", "add":
		args[0] = "add"
	case "修改", "编辑", "update", "edit":
		args[0] = "edit"
	case "暂停", "pause":
		args[0] = "pause"
	case "恢复", "resume":
		args[0] = "resume"
	case "删除", "delete":
		args[0] = "delete"
	case "列表", "list":
		args[0] = "list"
	case "帮助", "help":
		args[0] = "help"
	}
	return args
}

func parseRecurringReminderArgs(args []string) (recurringReminderQuery, error) {
	args = normalizeRecurringReminderArgs(args)
	query := recurringReminderQuery{Action: "list"}
	if len(args) == 0 {
		return query, nil
	}
	query.Action = args[0]
	args = args[1:]
	if query.Action == "list" || query.Action == "help" {
		if len(args) != 0 {
			return query, errors.New("查看周期提醒时不需要其他参数")
		}
		return query, nil
	}
	if query.Action != "add" {
		if len(args) == 0 {
			return query, errors.New("请填写周期提醒编号")
		}
		var err error
		query.ID, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil || query.ID <= 0 {
			return query, errors.New("周期提醒编号必须是正整数")
		}
		args = args[1:]
	}
	switch query.Action {
	case "pause", "resume", "delete":
		if len(args) != 0 {
			return query, errors.New("此操作只需要周期提醒编号")
		}
		return query, nil
	case "add", "edit":
	default:
		return query, errors.New("支持添加、列表、修改、暂停、恢复和删除")
	}
	if len(args) < 3 {
		return query, errors.New("请填写频率、时间和内容，例如：周期提醒 添加 每天 09:00 喝水")
	}
	switch strings.ToLower(args[0]) {
	case "每天", "每日", "daily":
		query.Spec.Frequency = "daily"
	case "每周", "weekly":
		query.Spec.Frequency = "weekly"
		if len(args) < 4 {
			return query, errors.New("每周提醒还需要星期几，例如：每周 周一 09:00 写周报")
		}
		weekdays := map[string]time.Weekday{"周日": time.Sunday, "周天": time.Sunday, "星期日": time.Sunday, "sun": time.Sunday, "0": time.Sunday, "7": time.Sunday,
			"周一": time.Monday, "星期一": time.Monday, "mon": time.Monday, "1": time.Monday,
			"周二": time.Tuesday, "星期二": time.Tuesday, "tue": time.Tuesday, "2": time.Tuesday,
			"周三": time.Wednesday, "星期三": time.Wednesday, "wed": time.Wednesday, "3": time.Wednesday,
			"周四": time.Thursday, "星期四": time.Thursday, "thu": time.Thursday, "4": time.Thursday,
			"周五": time.Friday, "星期五": time.Friday, "fri": time.Friday, "5": time.Friday,
			"周六": time.Saturday, "星期六": time.Saturday, "sat": time.Saturday, "6": time.Saturday}
		var ok bool
		query.Spec.Weekday, ok = weekdays[strings.ToLower(args[1])]
		if !ok {
			return query, errors.New("星期几请使用周一到周日")
		}
		args = args[1:]
	default:
		return query, errors.New("频率请使用每天或每周")
	}
	query.Spec.TimeOfDay = args[1]
	var content []string
	for _, token := range args[2:] {
		key, value, hasValue := strings.Cut(token, "=")
		switch {
		case hasValue && (key == "时区" || key == "timezone"):
			if query.TimezoneSet || value == "" {
				return query, errors.New("时区只能填写一次且不能为空")
			}
			query.Spec.Timezone, query.TimezoneSet = value, true
		case hasValue && (key == "截止" || key == "until"):
			if query.EndDateSet || value == "" {
				return query, errors.New("截止日期只能填写一次；使用截止=无清除")
			}
			if value == "无" || value == "none" {
				value = ""
			}
			query.Spec.EndDate, query.EndDateSet = value, true
		default:
			content = append(content, token)
		}
	}
	query.Spec.Text = strings.Join(content, " ")
	return query, query.Spec.Validate()
}

func recurringReminderPolicy(inv Invocation) CapabilityPolicy {
	if firstArgIs(inv.Args, "delete") {
		return privateWritePolicy(inv, EffectDestructive)
	}
	if firstArgIn(inv.Args, "add", "edit", "pause", "resume") {
		return privateWritePolicy(inv, EffectWrite)
	}
	return readPolicy(inv, DataScopeUserPrivate)
}

func (h Handler) recurringReminder(ctx context.Context, ident store.Identity, args []string) string {
	query, err := parseRecurringReminderArgs(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	if query.Action == "help" {
		return "周期提醒：\n添加 每天 HH:MM 内容\n添加 每周 周一 HH:MM 内容\n列表\n修改 编号 每天 HH:MM 内容\n暂停/恢复/删除 编号\n可选：时区=Asia/Shanghai 截止=YYYY-MM-DD（包含当天）；截止=无清除。\n修改时省略时区或截止日期会保留原值。通知 关会暂停全部周期提醒；通知 开不会恢复，请显式恢复。"
	}
	if query.Action == "list" {
		rows, err := h.Store.RecurringReminders(ctx, ident)
		if err != nil {
			return h.commandError("周期提醒查不到：", err)
		}
		h.markData(map[string]any{"operation": "recurring_reminder_list", "reminders": rows})
		if len(rows) == 0 {
			return "暂无周期提醒。可以发送：周期提醒 添加 每天 09:00 喝水"
		}
		parts := []string{fmt.Sprintf("周期提醒（%d 条）", len(rows))}
		for _, row := range rows {
			parts = append(parts, formatRecurringReminder(row))
		}
		return strings.Join(parts, "\n\n")
	}
	var rule store.RecurringReminder
	if query.ID != 0 {
		rule, err = h.Store.RecurringReminder(ctx, ident, query.ID)
		if err != nil {
			return h.recurringReminderError(err)
		}
	}
	switch query.Action {
	case "add", "edit":
		if query.Action == "edit" {
			if !query.TimezoneSet {
				query.Spec.Timezone = rule.Timezone
			}
			if !query.EndDateSet {
				query.Spec.EndDate = rule.EndDate
			}
		}
		rule, err = h.Store.SaveRecurringReminder(ctx, ident, query.ID, query.Spec, time.Now())
	case "pause", "resume":
		rule, err = h.Store.PauseRecurringReminder(ctx, ident, query.ID, query.Action == "pause", time.Now())
	case "delete":
		err = h.Store.DeleteRecurringReminder(ctx, ident, query.ID)
	}
	if err != nil {
		return h.recurringReminderError(err)
	}
	h.markData(map[string]any{"operation": "recurring_reminder_" + query.Action, "reminder": rule})
	if query.Action == "delete" {
		return fmt.Sprintf("已删除周期提醒 #%d：%s", rule.ID, rule.Text)
	}
	return formatRecurringReminder(rule)
}

func (h Handler) recurringReminderError(err error) string {
	if errors.Is(err, store.ErrRecurringReminderNotFound) {
		return h.notFound(err.Error())
	}
	if errors.Is(err, store.ErrRecurringReminderEnded) {
		return h.invalidInput(err.Error())
	}
	return h.commandError("周期提醒操作失败：", err)
}

func formatRecurringReminder(rule store.RecurringReminder) string {
	frequency := "每天"
	if rule.Frequency == "weekly" {
		frequency = "每周" + []string{"日", "一", "二", "三", "四", "五", "六"}[rule.Weekday]
	}
	status := "已结束"
	if rule.Paused {
		status = "已暂停"
	} else if rule.NextAt != nil {
		location, _ := time.LoadLocation(rule.Timezone)
		status = "下次 " + rule.NextAt.In(location).Format("2006-01-02 15:04")
	}
	text := fmt.Sprintf("#%d %s\n%s %s · %s\n%s", rule.ID, rule.Text, frequency, rule.TimeOfDay, rule.Timezone, status)
	if rule.EndDate != "" {
		text += "\n截止 " + rule.EndDate + "（含当天）"
	}
	return text
}
