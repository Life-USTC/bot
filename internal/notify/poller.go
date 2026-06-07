package notify

import (
	"context"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
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
	now := p.now().In(lifedata.ChinaLocation())
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
	if token, ok := p.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		homeworks, err = p.Life.SubscribedHomeworks(ctx, token)
	}
	if err != nil {
		p.logf("load homework notifications failed: %v", err)
		return
	}
	sort.Slice(homeworks, func(i, j int) bool {
		return lifedata.FirstString(homeworks[i], "submissionDueAt") < lifedata.FirstString(homeworks[j], "submissionDueAt")
	})
	for _, homework := range homeworks {
		if lifedata.HomeworkCompleted(homework) {
			continue
		}
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
		if !ok || due.Before(now) || due.After(now.Add(24*time.Hour)) {
			continue
		}
		key := notificationKey(homeworkKind, firstNonEmpty(lifedata.FirstString(homework, "id"), lifedata.FirstString(homework, "title"), due.Format(time.RFC3339)))
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
	if token, ok := p.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		sub, err = p.Life.CurrentSubscription(ctx, token)
	}
	if err != nil {
		return nil, err
	}
	sectionIDs := lifedata.SubscriptionSectionIDsForDay(sub, day)
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
	all = lifedata.FilterSchedulesForDay(all, day)
	sort.Slice(all, func(i, j int) bool {
		return lifedata.FirstString(all[i], "startTime") < lifedata.FirstString(all[j], "startTime")
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
	sectionID := lifedata.NestedString(schedule, "section", "id")
	return strings.Join(nonEmpty([]string{sectionID, start.Format("2006-01-02T15:04"), lifedata.FirstString(schedule, "startTime"), lifedata.FirstString(schedule, "endTime")}), "|")
}

func scheduleStartTime(schedule map[string]any, day time.Time) time.Time {
	start := lifedata.FirstString(schedule, "startTime")
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
	timeRange := strings.TrimSpace(lifedata.FirstString(schedule, "startTime") + "-" + lifedata.FirstString(schedule, "endTime"))
	course := lifedata.NestedPathString(schedule, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if course == "" {
		course = lifedata.NestedString(schedule, "section", "code")
	}
	place := lifedata.FirstString(schedule, "customPlace")
	if place == "" {
		place = lifedata.NestedString(schedule, "room", "namePrimary", "nameCn", "name", "code")
	}
	parts := nonEmpty([]string{place, timeRange, course})
	return monospaceDigits(strings.Join(parts, "\t"))
}

func formatHomework(homework map[string]any) string {
	course := lifedata.NestedPathString(homework, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if course == "" {
		course = lifedata.NestedPathString(homework, []string{"section", "course"}, "code")
	}
	title := lifedata.FirstString(homework, "title")
	due := lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt"))
	return monospaceDigits(strings.Join(nonEmpty([]string{"截止 " + due, course, title}), " · "))
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
