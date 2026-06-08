package notify

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
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
	if p.Life == nil || p.Auth == nil || p.Auth.Store == nil || p.Store == nil || p.Sender == nil {
		return
	}
	settings, err := p.Store.EnabledNotificationSettings(ctx)
	if err != nil {
		p.logf("list notification settings failed: %v", err)
		return
	}
	for _, setting := range settings {
		if !store.IsPrivateConversation(setting.Identity) {
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
		token = p.notifyClasses(ctx, settings.Identity, token, now)
	}
	if settings.HomeworkEnabled {
		p.notifyHomeworks(ctx, settings.Identity, token, now)
	}
}

func (p *Poller) notifyClasses(ctx context.Context, ident store.Identity, token string, now time.Time) string {
	schedules, token, err := p.schedulesForDay(ctx, ident, token, now)
	if err != nil {
		p.logf("load schedules for notification failed: %v", err)
		return token
	}
	for _, schedule := range schedules {
		start := lifedata.ScheduleStartTime(schedule, now, nil)
		if start.IsZero() || start.Before(now) || start.After(now.Add(30*time.Minute)) {
			continue
		}
		key := notificationKey(classKind, scheduleKey(schedule, start))
		p.sendNotificationOnce(ctx, ident, classKind, key, "课前提醒：\n"+formatSchedule(schedule))
	}
	return token
}

func (p *Poller) notifyHomeworks(ctx context.Context, ident store.Identity, token string, now time.Time) {
	homeworks, err := auth.WithRefresh(ctx, p.Auth, ident, token, func(token string) ([]map[string]any, error) {
		return p.Life.SubscribedHomeworks(ctx, token)
	})
	if err != nil {
		p.logf("load homework notifications failed: %v", err)
		return
	}
	lifedata.SortHomeworksByDue(homeworks)
	for _, homework := range homeworks {
		if lifedata.HomeworkCompleted(homework) {
			continue
		}
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
		if !ok || due.Before(now) || due.After(now.Add(24*time.Hour)) {
			continue
		}
		key := notificationKey(homeworkKind, textutil.FirstNonEmpty(lifedata.FirstString(homework, "id"), lifedata.FirstString(homework, "title"), due.Format(time.RFC3339)))
		p.sendNotificationOnce(ctx, ident, homeworkKind, key, "作业提醒：\n"+formatHomework(homework))
	}
}

func (p *Poller) sendNotificationOnce(ctx context.Context, ident store.Identity, kind, key, message string) {
	delivered, err := p.Store.NotificationDelivered(ctx, ident, kind, key)
	if err != nil {
		p.logf("check %s notification delivery failed: %v", kind, err)
		return
	}
	if delivered {
		return
	}
	if err := p.Sender.SendMessage(ctx, ident, message); err != nil {
		p.logf("send %s notification failed: %v", kind, err)
		return
	}
	if _, err := p.Store.TryRecordNotificationDelivery(ctx, ident, kind, key); err != nil {
		p.logf("record %s notification failed: %v", kind, err)
	}
}

func (p *Poller) schedulesForDay(ctx context.Context, ident store.Identity, token string, day time.Time) ([]map[string]any, string, error) {
	sub, err := p.Life.CurrentSubscription(ctx, token)
	if refreshed, ok := p.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
		token = refreshed
		sub, err = p.Life.CurrentSubscription(ctx, token)
	}
	if err != nil {
		return nil, token, err
	}
	sectionIDs := lifedata.SubscriptionSectionIDsForDay(sub, day)
	if len(sectionIDs) == 0 {
		return nil, token, nil
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())
	all := make([]map[string]any, 0, len(sectionIDs))
	for _, sectionID := range sectionIDs {
		values := life.ScheduleQuery(sectionID, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
		schedules, err := p.Life.Schedules(ctx, token, values)
		if refreshed, ok := p.Auth.RefreshIfUnauthorized(ctx, ident, err); ok {
			token = refreshed
			schedules, err = p.Life.Schedules(ctx, token, values)
		}
		if err != nil {
			return nil, token, err
		}
		all = append(all, schedules...)
	}
	all = lifedata.FilterSchedulesForDay(all, day)
	lifedata.SortSchedulesByStart(all)
	return all, token, nil
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
	return strings.Join(textutil.NonEmpty(sectionID, start.Format("2006-01-02T15:04"), lifedata.FirstString(schedule, "startTime"), lifedata.FirstString(schedule, "endTime")), "|")
}

func formatSchedule(schedule map[string]any) string {
	timeRange := lifedata.ScheduleTimeRange(schedule)
	course := lifedata.ScheduleCourseLabel(schedule)
	place := lifedata.SchedulePlaceLabel(schedule)
	parts := textutil.NonEmpty(place, timeRange, course)
	return textutil.MonospaceDigits(strings.Join(parts, "\t"))
}

func formatHomework(homework map[string]any) string {
	course := lifedata.HomeworkCourseLabel(homework)
	title := lifedata.FirstString(homework, "title")
	due := lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt"))
	dueLabel := ""
	if due != "" {
		dueLabel = "截止 " + due
	}
	return textutil.MonospaceDigits(strings.Join(textutil.NonEmpty(dueLabel, course, title), " · "))
}
