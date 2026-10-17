package notify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

// Notification source kinds. Exported so the 通知 command can mark sources the
// stored OAuth grant cannot poll.
const (
	KindClass    = "class"
	KindHomework = "homework"
	KindTodo     = "todo"
	KindYoung    = "young"
)

const (
	pollFailureBaseDelay = 5 * time.Minute
	pollFailureMaxDelay  = time.Hour
)

// sourceScopes maps each notification source to the OAuth scope its poll reads
// with. A grant issued before a scope existed stays valid for every other
// source, so the poller skips just the missing source instead of pausing the
// user's notifications or burning requests on guaranteed 401s.
var sourceScopes = map[string]string{
	KindClass:    "workspace.schedule:read",
	KindHomework: "workspace.homework:read",
	KindTodo:     "workspace.todo:read",
	KindYoung:    "workspace.young-notification:read",
}

var sourceLabels = map[string]string{
	KindClass:    "课前提醒",
	KindHomework: "作业提醒",
	KindTodo:     "待办提醒",
	KindYoung:    "第二课堂提醒",
}

// grantIncludesScope reports whether the space-separated OAuth grant includes
// scope. An empty grant is treated as unknown and passes: legacy credential
// rows predate the scope column, and failing open keeps their reminders
// running — a real gap then surfaces as a 401 under the usual backoff.
func grantIncludesScope(grant, scope string) bool {
	if strings.TrimSpace(grant) == "" {
		return true
	}
	for _, granted := range strings.Fields(grant) {
		if granted == scope {
			return true
		}
	}
	return false
}

// MissingSourceScopes lists the notification sources whose required OAuth
// scope is absent from grant, in display order.
func MissingSourceScopes(grant string) []string {
	var kinds []string
	for _, kind := range []string{KindClass, KindHomework, KindYoung, KindTodo} {
		if !grantIncludesScope(grant, sourceScopes[kind]) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

type Publisher interface {
	Enqueue(context.Context, message.Outbound) (delivery.Record, bool, error)
}

type pollFailure struct {
	count  int
	nextAt time.Time
}

type notificationPollResult uint8

const (
	pollSucceeded notificationPollResult = iota
	pollTransientFailure
	pollReauthRequired
)

type Poller struct {
	Life      *life.Client
	Auth      *auth.Manager
	Store     *store.Store
	Publisher Publisher
	Interval  time.Duration
	Now       func() time.Time
	Logger    *log.Logger

	failureMu sync.Mutex
	failures  map[string]pollFailure
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
	if p.Life == nil || p.Auth == nil || p.Auth.Store == nil || p.Store == nil || p.Publisher == nil {
		return
	}
	settings, err := p.Store.EnabledNotificationSettings(ctx)
	if err != nil {
		p.logf("list notification settings failed: %v", err)
		return
	}
	for _, setting := range settings {
		if !store.IsDirectConversation(setting.Identity) {
			continue
		}
		if !p.shouldPoll(setting.Identity) {
			continue
		}
		switch p.notifyUser(ctx, setting) {
		case pollSucceeded:
			p.clearPollFailure(setting.Identity)
		case pollReauthRequired:
			if err := p.Store.PauseNotificationsForReauth(ctx, setting.Identity); err != nil {
				p.logf("pause notification reminders failed: %v", err)
				p.notePollFailure(setting.Identity)
				continue
			}
			p.clearPollFailure(setting.Identity)
			for _, kind := range []string{KindClass, KindHomework, KindTodo, KindYoung} {
				p.clearPollFailureKey(notificationIdentityKey(setting.Identity) + "|" + kind)
			}
		case pollTransientFailure:
			p.notePollFailure(setting.Identity)
		}
	}
}

func (p *Poller) notifyUser(ctx context.Context, settings store.NotificationSettings) notificationPollResult {
	if !settings.ClassesEnabled && !settings.HomeworkEnabled && !settings.TodosEnabled && !settings.YoungEnabled {
		return pollSucceeded
	}
	token, err := p.Auth.AccessToken(ctx, settings.Identity)
	if err != nil {
		return p.resultForAuthError(ctx, settings.Identity, err)
	}
	now := p.now().In(lifedata.ChinaLocation())
	polls := []struct {
		kind    string
		enabled bool
		run     func(string) error
	}{
		{KindClass, settings.ClassesEnabled, func(token string) error {
			dateFrom, _ := lifedata.DayRFC3339Range(now)
			_, dateTo := lifedata.DayRFC3339Range(now.Add(30 * time.Minute))
			schedules, err := p.Life.SubscribedSchedules(ctx, token, life.SubscribedScheduleQuery(dateFrom, dateTo))
			if err != nil {
				return err
			}
			return p.notifyClasses(ctx, settings.Identity, schedules, now)
		}},
		{KindHomework, settings.HomeworkEnabled, func(token string) error {
			homeworks, err := p.Life.SubscribedHomeworks(ctx, token)
			if err != nil {
				return err
			}
			return p.notifyHomeworks(ctx, settings.Identity, homeworks, now)
		}},
		{KindTodo, settings.TodosEnabled, func(token string) error {
			todos, err := p.Life.TodosWithOptions(ctx, token, life.TodoListOptions{
				Completed: "false", DueAfter: now.Format(time.RFC3339), DueBefore: now.Add(24 * time.Hour).Format(time.RFC3339),
			})
			if err != nil {
				return err
			}
			return p.notifyTodos(ctx, settings.Identity, todos, now)
		}},
		{KindYoung, settings.YoungEnabled, func(token string) error {
			unread := true
			notifications, err := p.Life.ListAllYoungNotifications(ctx, token, &unread)
			if err != nil {
				return err
			}
			return p.notifyYoungNotifications(ctx, settings.Identity, token, notifications, now)
		}},
	}
	grant := p.grantedScopes(ctx, settings.Identity)
	for _, poll := range polls {
		if !poll.enabled {
			continue
		}
		// A grant issued before this source's scope existed can never poll it;
		// skip the request and tell the user once instead of 401-ing forever.
		// The other sources keep running on the same credential.
		if !grantIncludesScope(grant, sourceScopes[poll.kind]) {
			p.noteMissingScope(ctx, settings.Identity, poll.kind)
			continue
		}
		// Back off each source independently: an unavailable activity API must
		// not suppress an imminent class or deadline reminder.
		key := notificationIdentityKey(settings.Identity) + "|" + poll.kind
		if !p.shouldPollKey(key) {
			continue
		}
		err := auth.WithRefreshVoid(ctx, p.Auth, settings.Identity, token, func(current string) error {
			token = current
			return poll.run(current)
		})
		if err == nil {
			p.clearPollFailureKey(key)
			continue
		}
		p.logf("poll %s notifications failed: %v", poll.kind, err)
		if result := p.resultForAuthError(ctx, settings.Identity, err); result == pollReauthRequired {
			return result
		}
		p.notePollFailureKey(key)
	}
	return pollSucceeded
}

func (p *Poller) resultForAuthError(ctx context.Context, ident store.Identity, err error) notificationPollResult {
	if errors.Is(err, auth.ErrNotLoggedIn) || errors.Is(err, auth.ErrReauthorizationRequired) {
		return pollReauthRequired
	}
	credential, credentialErr := p.Store.Credential(ctx, ident)
	if credentialErr != nil {
		p.logf("check notification credential state failed: %v", credentialErr)
		return pollTransientFailure
	}
	if credential == nil {
		return pollReauthRequired
	}
	return pollTransientFailure
}

func (p *Poller) shouldPoll(ident store.Identity) bool {
	return p.shouldPollKey(notificationIdentityKey(ident))
}

// grantedScopes returns the stored grant's scope list, or "" (unknown) when
// the credential cannot be read — AccessToken already succeeded, so failing
// open here only risks a 401 that the per-source backoff absorbs.
func (p *Poller) grantedScopes(ctx context.Context, ident store.Identity) string {
	credential, err := p.Store.Credential(ctx, ident)
	if err != nil {
		p.logf("load notification credential scopes failed: %v", err)
		return ""
	}
	if credential == nil {
		return ""
	}
	return credential.Scope
}

// noteMissingScope tells the user, once, that one reminder source is paused
// until they reauthorize. The outbox dedupe key is durable, so later ticks and
// restarts never repeat the card. The toggle stays on and the source resumes
// as soon as a re-login stores a grant that includes the scope.
func (p *Poller) noteMissingScope(ctx context.Context, ident store.Identity, kind string) {
	text := sourceLabels[kind] + "已暂停：当前登录缺少新版本需要的权限。发送“登录”重新授权后自动恢复，其他提醒不受影响。"
	_, _ = p.enqueueNotification(ctx, ident, kind, "reauth-scope|"+sourceScopes[kind], text, nil, p.now().Add(7*24*time.Hour))
}

func (p *Poller) shouldPollKey(key string) bool {
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	failure, ok := p.failures[key]
	return !ok || !p.now().Before(failure.nextAt)
}

func (p *Poller) notePollFailure(ident store.Identity) {
	p.notePollFailureKey(notificationIdentityKey(ident))
}

func (p *Poller) notePollFailureKey(key string) {
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	if p.failures == nil {
		p.failures = make(map[string]pollFailure)
	}
	failure := p.failures[key]
	failure.count++
	delay := pollFailureBaseDelay << min(failure.count-1, 4)
	if delay > pollFailureMaxDelay {
		delay = pollFailureMaxDelay
	}
	failure.nextAt = p.now().Add(delay)
	p.failures[key] = failure
}

func (p *Poller) clearPollFailure(ident store.Identity) {
	p.clearPollFailureKey(notificationIdentityKey(ident))
}

func (p *Poller) clearPollFailureKey(key string) {
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	delete(p.failures, key)
}

func notificationIdentityKey(ident store.Identity) string {
	return ident.Platform + "|" + ident.UserID
}

func (p *Poller) notifyClasses(ctx context.Context, ident store.Identity, schedules []map[string]any, now time.Time) error {
	var errs []error
	lifedata.SortSchedulesByStart(schedules)
	for _, schedule := range schedules {
		day, ok := lifedata.ParseAPITime(lifedata.FirstString(schedule, "date"))
		if !ok {
			continue
		}
		start := lifedata.ScheduleStartTime(schedule, day.In(lifedata.ChinaLocation()), nil)
		if start.IsZero() || start.Before(now) || start.After(now.Add(30*time.Minute)) {
			continue
		}
		key := notificationKey(KindClass, scheduleKey(schedule, start))
		message := "课前提醒：\n" + formatSchedule(schedule)
		image := classReminderImage(schedule, message)
		if _, err := p.enqueueNotification(ctx, ident, KindClass, key, message, image, start.Add(15*time.Minute)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Poller) notifyHomeworks(ctx context.Context, ident store.Identity, homeworks []map[string]any, now time.Time) error {
	var errs []error
	lifedata.SortHomeworksByDue(homeworks)
	for _, homework := range homeworks {
		if lifedata.HomeworkCompleted(homework) {
			continue
		}
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(homework, "submissionDueAt"))
		if !ok || !due.After(now) || due.After(now.Add(24*time.Hour)) {
			continue
		}
		key := notificationKey(KindHomework, textutil.JoinNonEmpty("|", textutil.FirstNonEmpty(lifedata.FirstString(homework, "id"), lifedata.FirstString(homework, "title")), due.UTC().Format(time.RFC3339)))
		message := "作业提醒：\n" + formatHomework(homework)
		image := homeworkReminderImage(homework, message)
		if _, err := p.enqueueNotification(ctx, ident, KindHomework, key, message, image, due); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Poller) notifyYoungNotifications(ctx context.Context, ident store.Identity, token string, notifications []life.YoungNotification, now time.Time) error {
	for _, notification := range notifications {
		if strings.TrimSpace(notification.ID) == "" {
			continue
		}
		expiresAt := now.Add(24 * time.Hour)
		if parsed, ok := lifedata.ParseAPITime(notification.ExpiresAt); ok {
			expiresAt = parsed
		}
		// Expired server notices are no longer useful. Leave them unread so a
		// later server-side cleanup can decide their retention; never mark one
		// read before an outbox record exists.
		if !expiresAt.After(now) {
			continue
		}
		parts := []string{strings.TrimSpace(notification.Title)}
		if body := strings.TrimSpace(notification.Body); body != "" {
			parts = append(parts, body)
		}
		if notification.YoungID != "" {
			if link := p.Life.YoungEventURL(notification.YoungID); link != "" {
				parts = append(parts, "活动链接："+link)
			}
		}
		if notification.OrganizerID != "" {
			if link := p.Life.YoungOrganizerURL(notification.OrganizerID); link != "" {
				parts = append(parts, "主办方链接："+link)
			}
		}
		text := "第二课堂提醒：\n" + strings.Join(textutil.NonEmpty(parts...), "\n")
		_, err := p.enqueueNotification(ctx, ident, KindYoung, notification.ID, text, nil, expiresAt)
		if err != nil {
			return err
		}
		// The read transition happens only after durable enqueue succeeds. A
		// read failure leaves the unread record for the next poll; outbox
		// dedupe keeps that retry from sending a duplicate message.
		if err := auth.WithRefreshVoid(ctx, p.Auth, ident, token, func(token string) error {
			return p.Life.MarkYoungNotificationRead(ctx, token, notification.ID)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Poller) enqueueNotification(ctx context.Context, ident store.Identity, kind, key, text string, image *responses.Image, expiresAt time.Time) (bool, error) {
	content := message.Content{}
	if image != nil {
		payload, err := responses.EncodeImageIntent(image)
		if err != nil {
			return false, fmt.Errorf("encode %s notification image: %w", kind, err)
		}
		content.Parts = []message.ContentPart{{Attachment: &message.Attachment{
			MIMEType: "image/png", AltText: image.AltText, RenderPayload: payload,
		}}}
	} else {
		content.Parts = []message.ContentPart{{Text: text}}
	}
	target := message.Conversation{
		Platform: ident.Platform,
		Type:     ident.ConversationType,
		ID:       ident.ConversationID,
	}
	_, _, err := p.Publisher.Enqueue(ctx, message.Outbound{
		Kind: "notification." + kind, TextPolicy: message.TextPolicyImageOnly,
		Target: target, Content: content,
		DedupeKey: strings.Join([]string{"notification", target.Platform, target.Type, target.ID, key}, ":"),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		p.logf("enqueue %s notification failed: %v", kind, err)
	}
	return err == nil, err
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
	return responses.NewReminderCardImage("class_reminder", "课前提醒",
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
	return responses.NewReminderCardImage("homework_reminder", "作业提醒",
		[]string{"截止", "课程", "作业"},
		[]string{
			lifedata.FormatAPITime(lifedata.FirstString(homework, "submissionDueAt")),
			lifedata.HomeworkCourseLabel(homework),
			title,
		}, altText)
}
