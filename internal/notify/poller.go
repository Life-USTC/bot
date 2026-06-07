package notify

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

const (
	classKind    = "class"
	homeworkKind = "homework"
)

type Sender interface {
	SendMessage(ctx context.Context, ident store.Identity, message string) error
}

type Poller struct {
	Life     *life.Client
	Auth     *auth.Manager
	Store    *store.Store
	Sender   Sender
	Interval time.Duration
	Now      func() time.Time
	Logger   *log.Logger
}

func (p *Poller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	p.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	if p.Life == nil || p.Auth == nil || p.Store == nil || p.Sender == nil {
		return
	}
	settings, err := p.Store.EnabledNotificationSettings(ctx)
	if err != nil {
		p.logf("list notification settings failed: %v", err)
		return
	}
	for _, setting := range settings {
		if setting.Identity.ConversationType != "private" {
			continue
		}
		p.notifyUser(ctx, setting)
	}
}

func (p *Poller) notifyUser(ctx context.Context, settings store.NotificationSettings) {
	token, err := p.Auth.AccessToken(ctx, settings.Identity)
	if err != nil {
		p.logf("notification token unavailable for %s: %v", settings.Identity.UserID, err)
		return
	}
	now := p.now().In(chinaLocation())
	if settings.ClassesEnabled {
		p.notifyClasses(ctx, settings.Identity, token, now)
	}
	if settings.HomeworkEnabled {
		p.notifyHomeworks(ctx, settings.Identity, token, now)
	}
}

func (p *Poller) notifyClasses(ctx context.Context, ident store.Identity, token string, now time.Time) {
	schedules, err := p.schedulesForDay(ctx, ident, token, now)
	if err != nil {
		p.logf("load schedules for notification failed: %v", err)
		return
	}
	for _, schedule := range schedules {
		start := scheduleStartTime(schedule, now)
		if start.IsZero() || start.Before(now) || start.After(now.Add(30*time.Minute)) {
			continue
		}
		key := notificationKey(classKind, scheduleKey(schedule, start))
		delivered, err := p.Store.NotificationDelivered(ctx, ident, classKind, key)
		if err != nil {
			p.logf("check class notification delivery failed: %v", err)
			continue
		}
		if delivered {
			continue
		}
		if err := p.Sender.SendMessage(ctx, ident, "课前提醒：\n"+formatSchedule(schedule)); err != nil {
			p.logf("send class notification failed: %v", err)
			continue
		}
		if _, err := p.Store.TryRecordNotificationDelivery(ctx, ident, classKind, key); err != nil {
			p.logf("record class notification failed: %v", err)
		}
	}
}

func (p *Poller) notifyHomeworks(ctx context.Context, ident store.Identity, token string, now time.Time) {
	homeworks, err := p.Life.SubscribedHomeworks(ctx, token)
	if life.IsUnauthorized(err) {
		if refreshed, refreshErr := p.Auth.Refresh(ctx, ident); refreshErr == nil {
			token = refreshed
			homeworks, err = p.Life.SubscribedHomeworks(ctx, token)
		}
	}
	if err != nil {
		p.logf("load homework notifications failed: %v", err)
		return
	}
	sort.Slice(homeworks, func(i, j int) bool {
		return firstString(homeworks[i], "submissionDueAt") < firstString(homeworks[j], "submissionDueAt")
	})
	for _, homework := range homeworks {
		if homeworkCompleted(homework) {
			continue
		}
		due, ok := parseAPITime(firstString(homework, "submissionDueAt"))
		if !ok || due.Before(now) || due.After(now.Add(24*time.Hour)) {
			continue
		}
		key := notificationKey(homeworkKind, firstNonEmpty(firstString(homework, "id"), firstString(homework, "title"), due.Format(time.RFC3339)))
		delivered, err := p.Store.NotificationDelivered(ctx, ident, homeworkKind, key)
		if err != nil {
			p.logf("check homework notification delivery failed: %v", err)
			continue
		}
		if delivered {
			continue
		}
		if err := p.Sender.SendMessage(ctx, ident, "作业提醒：\n"+formatHomework(homework)); err != nil {
			p.logf("send homework notification failed: %v", err)
			continue
		}
		if _, err := p.Store.TryRecordNotificationDelivery(ctx, ident, homeworkKind, key); err != nil {
			p.logf("record homework notification failed: %v", err)
		}
	}
}

func (p *Poller) schedulesForDay(ctx context.Context, ident store.Identity, token string, day time.Time) ([]map[string]any, error) {
	sub, err := p.Life.CurrentSubscription(ctx, token)
	if life.IsUnauthorized(err) {
		if refreshed, refreshErr := p.Auth.Refresh(ctx, ident); refreshErr == nil {
			token = refreshed
			sub, err = p.Life.CurrentSubscription(ctx, token)
		}
	}
	if err != nil {
		return nil, err
	}
	sectionIDs := subscriptionSectionIDsForDay(sub, day)
	if len(sectionIDs) == 0 {
		return nil, nil
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())
	all := make([]map[string]any, 0)
	for _, sectionID := range sectionIDs {
		values := url.Values{}
		values.Set("sectionId", sectionID)
		values.Set("dateFrom", start.UTC().Format(time.RFC3339))
		values.Set("dateTo", end.UTC().Format(time.RFC3339))
		values.Set("limit", "100")
		schedules, err := p.Life.Schedules(ctx, token, values)
		if err != nil {
			return nil, err
		}
		all = append(all, schedules...)
	}
	all = filterSchedulesForDay(all, day)
	sort.Slice(all, func(i, j int) bool {
		return firstString(all[i], "startTime") < firstString(all[j], "startTime")
	})
	return all, nil
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}

func notificationKey(kind, item string) string {
	return kind + ":" + item
}

func scheduleKey(schedule map[string]any, start time.Time) string {
	sectionID := nestedString(schedule, "section", "id")
	return strings.Join(nonEmpty([]string{sectionID, start.Format("2006-01-02T15:04"), firstString(schedule, "startTime"), firstString(schedule, "endTime")}), "|")
}

func scheduleStartTime(schedule map[string]any, day time.Time) time.Time {
	start := firstString(schedule, "startTime")
	if start == "" {
		return time.Time{}
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+start, day.Location())
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func formatSchedule(schedule map[string]any) string {
	timeRange := strings.TrimSpace(firstString(schedule, "startTime") + "-" + firstString(schedule, "endTime"))
	course := nestedPathString(schedule, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if course == "" {
		course = nestedString(schedule, "section", "code")
	}
	place := firstString(schedule, "customPlace")
	if place == "" {
		place = nestedString(schedule, "room", "namePrimary", "nameCn", "name", "code")
	}
	parts := nonEmpty([]string{place, timeRange, course})
	return monospaceDigits(strings.Join(parts, "\t"))
}

func formatHomework(homework map[string]any) string {
	course := nestedPathString(homework, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if course == "" {
		course = nestedPathString(homework, []string{"section", "course"}, "code")
	}
	title := firstString(homework, "title")
	due := formatAPITime(firstString(homework, "submissionDueAt"))
	return monospaceDigits(strings.Join(nonEmpty([]string{"截止 " + due, course, title}), " · "))
}

func subscriptionSectionIDsForDay(data map[string]any, day time.Time) []string {
	sub, _ := data["subscription"].(map[string]any)
	sections, _ := sub["sections"].([]any)
	out := make([]string, 0, len(sections))
	fallback := make([]string, 0, len(sections))
	sawSemester := false
	for _, item := range sections {
		section, _ := item.(map[string]any)
		if section == nil {
			continue
		}
		id := firstString(section, "id")
		if id != "" {
			fallback = append(fallback, id)
		}
		semester, _ := section["semester"].(map[string]any)
		if semester == nil {
			continue
		}
		sawSemester = true
		if !day.IsZero() && !semesterContainsDay(semester, day) {
			continue
		}
		if id != "" {
			out = append(out, id)
		}
	}
	if !sawSemester || day.IsZero() {
		return fallback
	}
	return out
}

func semesterContainsDay(semester map[string]any, day time.Time) bool {
	loc := day.Location()
	start, okStart := parseAPITime(firstString(semester, "startDate"))
	end, okEnd := parseAPITime(firstString(semester, "endDate"))
	target := day.In(loc).Format("2006-01-02")
	if okStart && target < start.In(loc).Format("2006-01-02") {
		return false
	}
	if okEnd && target > end.In(loc).Format("2006-01-02") {
		return false
	}
	return okStart || okEnd
}

func filterSchedulesForDay(schedules []map[string]any, day time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(schedules))
	for _, schedule := range schedules {
		if scheduleMatchesDay(schedule, day) {
			out = append(out, schedule)
		}
	}
	return out
}

func scheduleMatchesDay(schedule map[string]any, day time.Time) bool {
	date := firstString(schedule, "date")
	if date == "" {
		return true
	}
	parsed, ok := parseAPITime(date)
	if !ok {
		return true
	}
	return parsed.In(day.Location()).Format("2006-01-02") == day.In(day.Location()).Format("2006-01-02")
}

func homeworkCompleted(homework map[string]any) bool {
	if completed, ok := homework["isCompleted"].(bool); ok {
		return completed
	}
	return homework["completion"] != nil
}

func formatAPITime(value string) string {
	if value == "" {
		return ""
	}
	parsed, ok := parseAPITime(value)
	if !ok {
		return value
	}
	return parsed.In(chinaLocation()).Format("01-02 15:04")
}

func parseAPITime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, chinaLocation()); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func chinaLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := m[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case float64:
			return fmt.Sprintf("%.0f", value)
		case int:
			return fmt.Sprint(value)
		}
	}
	return ""
}

func nestedString(m map[string]any, key string, names ...string) string {
	nested, _ := m[key].(map[string]any)
	if nested == nil {
		return ""
	}
	return firstString(nested, names...)
}

func nestedPathString(m map[string]any, path []string, names ...string) string {
	current := m
	for _, key := range path {
		next, _ := current[key].(map[string]any)
		if next == nil {
			return ""
		}
		current = next
	}
	return firstString(current, names...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" && value != "截止 " {
			out = append(out, value)
		}
	}
	return out
}

func monospaceDigits(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '𝟶' + (r - '0')
		}
		return r
	}, text)
}
