package notify

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const (
	classKind    = "class"
	homeworkKind = "homework"
)

type Sender interface {
	SendRichMessage(ctx context.Context, ident store.Identity, message string, image *responses.Image) error
}

type Poller struct {
	Life                 *life.Client
	Auth                 *auth.Manager
	Store                *store.Store
	Sender               Sender
	Interval             time.Duration
	Now                  func() time.Time
	Logger               *log.Logger
	EnableImageResponses bool
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
	if !settings.ClassesEnabled && !settings.HomeworkEnabled {
		return
	}
	token, err := p.Auth.AccessToken(ctx, settings.Identity)
	if err != nil {
		p.logf("notification token unavailable for %s: %v", settings.Identity.UserID, err)
		return
	}
	now := p.now().In(lifedata.ChinaLocation())
	if settings.ClassesEnabled {
		if settings.HomeworkEnabled {
			overview, err := auth.WithRefresh(ctx, p.Auth, settings.Identity, token, func(token string) (map[string]any, error) {
				return p.Life.GetUpcomingDeadlinesAt(ctx, token, 1, now)
			})
			if err != nil {
				p.logf("load notification overview failed: %v", err)
				return
			}
			p.notifyClasses(ctx, settings.Identity, overviewItems(overview, "schedules"), now)
			p.notifyHomeworks(ctx, settings.Identity, overviewItems(overview, "homeworks"), now)
			return
		}
		dateFrom, dateTo := lifedata.DayRFC3339Range(now)
		schedules, err := auth.WithRefresh(ctx, p.Auth, settings.Identity, token, func(token string) ([]map[string]any, error) {
			return p.Life.SubscribedSchedules(ctx, token, life.SubscribedScheduleQuery(dateFrom, dateTo))
		})
		if err != nil {
			p.logf("load schedules for notification failed: %v", err)
			return
		}
		p.notifyClasses(ctx, settings.Identity, schedules, now)
		return
	}
	homeworks, err := auth.WithRefresh(ctx, p.Auth, settings.Identity, token, func(token string) ([]map[string]any, error) {
		return p.Life.SubscribedHomeworks(ctx, token)
	})
	if err != nil {
		p.logf("load homework notifications failed: %v", err)
		return
	}
	p.notifyHomeworks(ctx, settings.Identity, homeworks, now)
}

func (p *Poller) notifyClasses(ctx context.Context, ident store.Identity, schedules []map[string]any, now time.Time) {
	schedules = lifedata.FilterSchedulesForDay(schedules, now)
	lifedata.SortSchedulesByStart(schedules)
	for _, schedule := range schedules {
		start := lifedata.ScheduleStartTime(schedule, now, nil)
		if start.IsZero() || start.Before(now) || start.After(now.Add(30*time.Minute)) {
			continue
		}
		key := notificationKey(classKind, scheduleKey(schedule, start))
		message := "课前提醒：\n" + formatSchedule(schedule)
		var image *responses.Image
		if p.EnableImageResponses {
			image = classReminderImage(schedule, message)
		}
		p.sendNotificationOnce(ctx, ident, classKind, key, message, image)
	}
}

func (p *Poller) notifyHomeworks(ctx context.Context, ident store.Identity, homeworks []map[string]any, now time.Time) {
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
		message := "作业提醒：\n" + formatHomework(homework)
		var image *responses.Image
		if p.EnableImageResponses {
			image = homeworkReminderImage(homework, message)
		}
		p.sendNotificationOnce(ctx, ident, homeworkKind, key, message, image)
	}
}

func overviewItems(overview map[string]any, key string) []map[string]any {
	group, _ := overview[key].(map[string]any)
	return lifedata.MapSlice(group["items"])
}

func (p *Poller) sendNotificationOnce(ctx context.Context, ident store.Identity, kind, key, message string, image *responses.Image) {
	delivered, err := p.Store.NotificationDelivered(ctx, ident, kind, key)
	if err != nil {
		p.logf("check %s notification delivery failed: %v", kind, err)
		return
	}
	if delivered {
		return
	}
	if err := p.Sender.SendRichMessage(ctx, ident, message, image); err != nil {
		p.logf("send %s notification failed: %v", kind, err)
		return
	}
	if _, err := p.Store.TryRecordNotificationDelivery(ctx, ident, kind, key); err != nil {
		p.logf("record %s notification failed: %v", kind, err)
	}
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
	return textutil.JoinNonEmpty("|", sectionID, start.Format("2006-01-02T15:04"), lifedata.FirstString(schedule, "startTime"), lifedata.FirstString(schedule, "endTime"))
}

func formatSchedule(schedule map[string]any) string {
	timeRange := lifedata.ScheduleTimeRange(schedule)
	course := lifedata.ScheduleCourseLabel(schedule)
	place := lifedata.SchedulePlaceLabel(schedule)
	parts := textutil.NonEmpty(place, timeRange, course)
	if len(parts) == 0 {
		return textutil.MonospaceDigits(lifedata.ScheduleFallbackLabel(schedule))
	}
	return textutil.MonospaceDigits(strings.Join(parts, "\t"))
}

func formatHomework(homework map[string]any) string {
	return textutil.MonospaceDigits(lifedata.HomeworkLabel(homework))
}

func classReminderImage(schedule map[string]any, altText string) *responses.Image {
	return reminderTableImage("class_reminder", "课前提醒",
		[]string{"地点", "时间", "课程"},
		[]string{
			lifedata.SchedulePlaceLabel(schedule),
			lifedata.ScheduleTimeRange(schedule),
			lifedata.ScheduleCourseLabel(schedule),
		}, altText)
}

func homeworkReminderImage(homework map[string]any, altText string) *responses.Image {
	title := lifedata.FirstString(homework, "title")
	if title == "" {
		title = lifedata.FirstString(homework, "id")
	}
	return reminderTableImage("homework_reminder", "作业提醒",
		[]string{"截止", "课程", "作业"},
		[]string{
			lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt")),
			lifedata.HomeworkCourseLabel(homework),
			title,
		}, altText)
}

func reminderTableImage(kind, title string, headers, cells []string, altText string) *responses.Image {
	separators := make([]string, len(headers))
	for i := range separators {
		separators[i] = "---"
	}
	richText := strings.Join([]string{
		"# " + title,
		"",
		reminderTableRow(headers),
		reminderTableRow(separators),
		reminderTableRow(cells),
	}, "\n")
	return responses.NewRichTextImage(kind, richText, altText)
}

func reminderTableRow(cells []string) string {
	clean := make([]string, len(cells))
	for i, cell := range cells {
		cell = strings.ReplaceAll(cell, "|", "｜")
		clean[i] = strings.Join(strings.Fields(cell), " ")
	}
	return "| " + strings.Join(clean, " | ") + " |"
}
