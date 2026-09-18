package commands

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const agendaCalendarLinkProbability = 1.0 / 16

var agendaRandFloat = rand.Float64

func (h Handler) agenda(ctx context.Context, ident store.Identity, args []string) string {
	date, ok := agendaDate(args, chinaNow())
	if !ok {
		return h.invalidInput("日期格式看不懂，试试：日程 / 日程 明天 / 日程 9-20 / 日程 2026-09-20")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	dateString := date.In(lifedata.ChinaLocation()).Format("2006-01-02")
	events, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]life.PersonalCalendarEvent, error) {
		return h.Life.ListAllPersonalCalendarEvents(ctx, token, dateString, dateString)
	})
	if err != nil {
		return h.commandError("日程查不到：", err)
	}
	h.markData(map[string]any{"operation": "personal_calendar", "date": dateString, "events": events})
	reply := formatAgenda(date, events)
	if len(events) > 0 && agendaRandFloat() < agendaCalendarLinkProbability {
		if url := h.agendaCalendarURL(ctx, ident); url != "" {
			reply += "\n\n日历订阅链接：" + url
		}
	}
	return reply
}

// agendaCalendarURL 静默取当前用户的 iCalendar 链接；任何失败都不影响主回复。
func (h Handler) agendaCalendarURL(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return ""
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return ""
	}
	return lifedata.NestedString(data, "subscription", "calendarUrl")
}

func agendaDate(args []string, now time.Time) (time.Time, bool) {
	loc := lifedata.ChinaLocation()
	now = now.In(loc)
	if len(args) == 0 {
		return now, true
	}
	if len(args) != 1 {
		return time.Time{}, false
	}
	switch commandToken(args[0]) {
	case "今天", "今日", "today":
		return now, true
	case "明天", "明日", "tomorrow":
		return now.AddDate(0, 0, 1), true
	case "后天":
		return now.AddDate(0, 0, 2), true
	}
	return parseScheduleDateToken(args[0], now)
}

var agendaGroupOrder = []struct {
	Type  string
	Label string
}{
	{"schedule", "课程"},
	{"exam", "考试"},
	{"homework_due", "作业截止"},
	{"todo_due", "待办截止"},
	{"young_event", "第二课堂"},
}

func formatAgenda(date time.Time, events []life.PersonalCalendarEvent) string {
	title := textutil.MonospaceDigits(date.In(lifedata.ChinaLocation()).Format("01-02")) + " 日程："
	if len(events) == 0 {
		return title + "\n暂无安排。"
	}
	grouped := make(map[string][]life.PersonalCalendarEvent, len(agendaGroupOrder))
	var extra []life.PersonalCalendarEvent
	for _, event := range events {
		known := false
		for _, group := range agendaGroupOrder {
			if event.Type == group.Type {
				known = true
				break
			}
		}
		if known {
			grouped[event.Type] = append(grouped[event.Type], event)
		} else {
			extra = append(extra, event)
		}
	}
	lines := []string{title}
	appendGroup := func(label string, items []life.PersonalCalendarEvent) {
		if len(items) == 0 {
			return
		}
		lines = append(lines, "", fmt.Sprintf("%s (%d)：", label, len(items)))
		for i, event := range items {
			lines = append(lines, formatNumberedLine(i+1, formatAgendaEvent(event)))
		}
	}
	for _, group := range agendaGroupOrder {
		appendGroup(group.Label, grouped[group.Type])
	}
	appendGroup("其他", extra)
	return strings.Join(lines, "\n")
}

func formatAgendaEvent(event life.PersonalCalendarEvent) string {
	title := textutil.FirstNonEmpty(event.Title, "未命名安排")
	line := title
	if event.At != "" {
		line = formatAgendaInstant(event.At) + " " + line
	}
	if event.EndsAt != "" {
		line += " ~ " + formatAgendaInstant(event.EndsAt)
	}
	if event.Location != "" {
		line += " · " + event.Location
	}
	return line
}

func formatAgendaInstant(value string) string {
	if parsed, ok := lifedata.ParseAPITime(value); ok {
		return parsed.In(lifedata.ChinaLocation()).Format("15:04")
	}
	return strings.TrimSpace(value)
}
