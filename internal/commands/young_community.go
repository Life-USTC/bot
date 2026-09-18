package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const youngWorkspacePageSize = 100

func formatCalendarInstant(value string) string {
	if parsed, ok := lifedata.ParseAPITime(value); ok {
		return parsed.In(lifedata.ChinaLocation()).Format("2006-01-02 15:04")
	}
	return strings.TrimSpace(value)
}

type youngOrganizerQuery struct {
	Action      string
	Page        int
	Search      string
	OrganizerID string
}

func youngOrganizerArgsAcceptable(args []string) bool {
	return !hasArgs(args) || firstArgIsHelp(args) || func() bool {
		_, err := parseYoungOrganizerQuery(args)
		return err == nil
	}()
}

func parseYoungOrganizerQuery(args []string) (youngOrganizerQuery, error) {
	query := youngOrganizerQuery{Action: "list", Page: 1}
	if len(args) == 0 {
		return query, nil
	}
	action := normToken(args[0])
	args = args[1:]
	switch action {
	case "list", "列表", "搜索", "search":
		if action == "搜索" || action == "search" {
			query.Search = strings.TrimSpace(joinedArgs(args))
			if query.Search == "" {
				return youngOrganizerQuery{}, errors.New("想搜索哪个主办方？")
			}
			return query, nil
		}
		if len(args) > 0 {
			if page, err := strconv.Atoi(textutil.PlainDigits(args[0])); err == nil && page > 0 {
				query.Page = page
			} else {
				query.Search = strings.TrimSpace(joinedArgs(args))
			}
		}
		return query, nil
	case "get", "查看", "详情", "detail":
		if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
			return youngOrganizerQuery{}, errors.New("需要提供主办方 organizerId")
		}
		query.Action, query.OrganizerID = "detail", strings.TrimSpace(args[0])
		return query, nil
	default:
		if len(args) == 0 && strings.TrimSpace(action) != "" {
			query.Action, query.OrganizerID = "detail", strings.TrimSpace(action)
			return query, nil
		}
		return youngOrganizerQuery{}, errors.New("主办方用法：第二课堂 主办方、第二课堂 主办方 列表、第二课堂 主办方 查看 <organizerId>")
	}
}

func (h Handler) youngOrganizers(ctx context.Context, args []string) string {
	query, err := parseYoungOrganizerQuery(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	if query.Action == "detail" {
		organizer, err := h.Life.GetYoungOrganizer(ctx, query.OrganizerID)
		if err != nil {
			if youngEventIsNotFound(err) {
				return h.notFound("没找到第二课堂主办方：" + query.OrganizerID)
			}
			return h.commandError("第二课堂主办方查不到：", err)
		}
		organizerID := textutil.FirstNonEmpty(organizer.ID, query.OrganizerID)
		collection, err := h.Life.ListAllYoungEventsWithQueryMetadata(ctx, "", life.YoungEventQuery{OrganizerID: organizerID, PageSize: youngWorkspacePageSize})
		if err != nil {
			return h.commandError("主办方活动查不到：", err)
		}
		events := collection.Data
		h.markData(map[string]any{"operation": "organizer_detail", "organizer": organizer, "events": events, "unknownDateCount": collection.UnknownDateCount, "source": collection.Source})
		lines := []string{fmt.Sprintf("主办方：%s", textutil.FirstNonEmpty(organizer.Name, organizerID)), "organizerId：" + organizerID,
			fmt.Sprintf("活动数：进行中 %d · 即将开始 %d · 历史 %d", organizer.ActiveCount, organizer.UpcomingCount, organizer.HistoryCount)}
		for i, event := range events {
			lines = append(lines, youngEventLines(event, h.Life.YoungEventURL(event.YoungID), strconv.Itoa(i+1))...)
		}
		if len(events) == 0 {
			lines = append(lines, "暂无活动。")
		}
		return strings.Join(lines, "\n")
	}
	page, err := h.Life.ListYoungOrganizers(ctx, query.Page, youngEventPageSize, query.Search)
	if err != nil {
		return h.commandError("第二课堂主办方查不到：", err)
	}
	h.markData(map[string]any{"operation": "organizer_list", "page": query.Page, "search": query.Search, "result": page})
	if len(page.Data) == 0 {
		return h.notFound("没有找到第二课堂主办方。")
	}
	lines := []string{"第二课堂主办方："}
	for i, organizer := range page.Data {
		lines = append(lines, fmt.Sprintf("%d. %s · organizerId：%s · 进行中 %d · 即将开始 %d · 历史 %d", i+1,
			textutil.FirstNonEmpty(organizer.Name, organizer.ID), organizer.ID, organizer.ActiveCount, organizer.UpcomingCount, organizer.HistoryCount))
	}
	if totalPages := page.Pagination.TotalPages; totalPages > 1 {
		pageNumber := page.Pagination.Page
		if pageNumber < 1 {
			pageNumber = query.Page
		}
		navigation := []string{fmt.Sprintf("第 %d/%d 页", pageNumber, totalPages)}
		if pageNumber > 1 {
			navigation = append(navigation, "上一页：发送「第二课堂 主办方 列表 "+strconv.Itoa(pageNumber-1)+"」")
		}
		if pageNumber < totalPages {
			navigation = append(navigation, "下一页：发送「第二课堂 主办方 列表 "+strconv.Itoa(pageNumber+1)+"」")
		}
		lines = append(lines, textutil.MonospaceDigits(strings.Join(navigation, " · ")))
	}
	return strings.Join(lines, "\n")
}

type youngCalendarQuery struct {
	Basis  string
	Period string
	Date   time.Time
}

func youngCalendarArgsAcceptable(args []string) bool {
	return !hasArgs(args) || firstArgIsHelp(args) || func() bool {
		_, err := parseYoungCalendarQuery(args, time.Now().In(lifedata.ChinaLocation()))
		return err == nil
	}()
}

func parseYoungCalendarQuery(args []string, now time.Time) (youngCalendarQuery, error) {
	query := youngCalendarQuery{Period: "day", Basis: "activity", Date: now.In(lifedata.ChinaLocation())}
	for _, raw := range args {
		value := normToken(raw)
		switch value {
		case "day", "日", "今天":
			query.Period = "day"
		case "week", "周", "本周", "星期":
			query.Period = "week"
		case "month", "月", "本月":
			query.Period = "month"
		case "activity", "活动", "活动时间":
			query.Basis = "activity"
		case "registration", "报名", "报名时间":
			query.Basis = "registration"
		default:
			parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(raw), lifedata.ChinaLocation())
			if err != nil {
				return youngCalendarQuery{}, errors.New("日历用法：第二课堂 日历 日|周|月 [YYYY-MM-DD] [活动|报名]")
			}
			query.Date = parsed
		}
	}
	return query, nil
}

func youngCalendarBounds(query youngCalendarQuery) (string, string) {
	date := query.Date.In(lifedata.ChinaLocation())
	switch query.Period {
	case "week":
		mondayOffset := (int(date.Weekday()) + 6) % 7
		start := date.AddDate(0, 0, -mondayOffset)
		end := start.AddDate(0, 0, 6)
		return start.Format("2006-01-02"), end.Format("2006-01-02")
	case "month":
		start := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, lifedata.ChinaLocation())
		end := start.AddDate(0, 1, -1)
		return start.Format("2006-01-02"), end.Format("2006-01-02")
	default:
		value := date.Format("2006-01-02")
		return value, value
	}
}

func (h Handler) youngCalendar(ctx context.Context, args []string) string {
	query, err := parseYoungCalendarQuery(args, chinaNow())
	if err != nil {
		return h.invalidInput(err.Error())
	}
	dateFrom, dateTo := youngCalendarBounds(query)
	collection, err := h.Life.ListAllYoungEventsWithQueryMetadata(ctx, "", life.YoungEventQuery{PageSize: youngWorkspacePageSize, DateFrom: dateFrom, DateTo: dateTo, TimeBasis: query.Basis})
	if err != nil {
		return h.commandError("第二课堂日历查不到：", err)
	}
	h.markData(map[string]any{"operation": "calendar", "period": query.Period, "date": query.Date.Format("2006-01-02"), "dateFrom": dateFrom, "dateTo": dateTo, "timeBasis": query.Basis, "events": collection.Data, "unknownDateCount": collection.UnknownDateCount, "source": collection.Source})
	return formatYoungCalendar(collection, dateFrom, dateTo, query.Basis, h.Life.YoungEventURL)
}

func formatYoungCalendar(collection life.YoungEventCollection, dateFrom, dateTo, basis string, eventURL func(string) string) string {
	lines := []string{fmt.Sprintf("第二课堂日历（%s 至 %s，按%s）：", dateFrom, dateTo, youngBasisText(basis))}
	if source := formatYoungSource(collection.Source); source != "" {
		lines = append(lines, source)
	}
	groups := make(map[string][]life.YoungEvent)
	unknown := make([]life.YoungEvent, 0)
	for _, event := range collection.Data {
		date := youngEventCalendarDate(event, basis)
		if date == "" {
			unknown = append(unknown, event)
			continue
		}
		groups[date] = append(groups[date], event)
	}
	keys := make([]string, 0, len(groups))
	for date := range groups {
		keys = append(keys, date)
	}
	sort.Strings(keys)
	for _, date := range keys {
		lines = append(lines, "", date+"：")
		for i, event := range groups[date] {
			link := ""
			if eventURL != nil {
				link = eventURL(event.YoungID)
			}
			lines = append(lines, youngEventLines(event, link, strconv.Itoa(i+1))...)
		}
	}
	unknownCount := collection.UnknownDateCount
	if len(unknown) > unknownCount {
		unknownCount = len(unknown)
	}
	if unknownCount > 0 {
		lines = append(lines, "", fmt.Sprintf("日期待核实（%d）：发送「第二课堂 列表 日期未知」查看。", unknownCount))
		for i, event := range unknown {
			link := ""
			if eventURL != nil {
				link = eventURL(event.YoungID)
			}
			lines = append(lines, youngEventLines(event, link, strconv.Itoa(i+1))...)
		}
	}
	if len(collection.Data) == 0 && unknownCount == 0 {
		lines = append(lines, "暂无活动。")
	}
	return strings.Join(lines, "\n")
}

func youngEventCalendarDate(event life.YoungEvent, basis string) string {
	if event.DateUnknown {
		return ""
	}
	value := event.StartAt
	if basis == "registration" {
		value = event.ApplyStartAt
	}
	if value == nil || value.IsZero() {
		return ""
	}
	return value.In(lifedata.ChinaLocation()).Format("2006-01-02")
}

func formatYoungSource(source map[string]any) string {
	if len(source) == 0 {
		return ""
	}
	status := strings.TrimSpace(lifedata.FirstString(source, "status"))
	lastSyncedAt := strings.TrimSpace(lifedata.FirstString(source, "lastSyncedAt"))
	parts := make([]string, 0, 2)
	if status != "" {
		parts = append(parts, "状态 "+status)
	}
	if lastSyncedAt != "" {
		parts = append(parts, "更新于 "+formatCalendarInstant(lastSyncedAt))
	}
	if len(parts) == 0 {
		return ""
	}
	return "数据源：" + strings.Join(parts, "，")
}

func youngBasisText(value string) string {
	if value == "registration" {
		return "报名时间"
	}
	return "活动时间"
}

func (h Handler) youngSubscriptions(ctx context.Context, ident store.Identity, args []string) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	query, err := parseYoungSubscriptionArgs(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	if query.Kind == "event" {
		if query.Action == "list" {
			states, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]life.YoungEventSubscription, error) {
				return h.Life.ListAllYoungEventSubscriptions(ctx, token)
			})
			if err != nil {
				return h.commandError("活动订阅查不到：", err)
			}
			h.markData(map[string]any{"operation": "event_subscription_list", "data": states})
			return formatYoungEventSubscriptions(states)
		}
		if query.Action == "set" {
			state, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.YoungEventSubscription, error) {
				return h.Life.SetYoungEventSubscriptionWithOptions(ctx, token, query.ID, query.Enabled, query.RemindSignup, query.RemindDeadline, query.RemindStart)
			})
			if err != nil {
				return h.commandError("活动订阅更新失败：", err)
			}
			h.markData(map[string]any{"operation": "event_subscription", "state": state})
			return formatYoungEventSubscription(state)
		}
		state, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.YoungEventSubscription, error) {
			return h.Life.GetYoungEventSubscription(ctx, token, query.ID)
		})
		if err != nil {
			return h.commandError("活动订阅查不到：", err)
		}
		h.markData(map[string]any{"operation": "event_subscription", "state": state})
		return formatYoungEventSubscription(state)
	}
	if query.Action == "list" {
		states, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]life.YoungOrganizerSubscription, error) {
			return h.Life.ListAllYoungOrganizerSubscriptions(ctx, token)
		})
		if err != nil {
			return h.commandError("主办方关注查不到：", err)
		}
		h.markData(map[string]any{"operation": "organizer_subscription_list", "data": states})
		return formatYoungOrganizerSubscriptions(states)
	}
	if query.Action == "set" {
		state, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.YoungOrganizerSubscription, error) {
			return h.Life.SetYoungOrganizerSubscription(ctx, token, query.ID, query.Enabled)
		})
		if err != nil {
			return h.commandError("主办方关注更新失败：", err)
		}
		h.markData(map[string]any{"operation": "organizer_subscription", "state": state})
		return fmt.Sprintf("主办方 %s：%s", state.OrganizerID, onOffText(state.Subscribed))
	}
	state, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.YoungOrganizerSubscription, error) {
		return h.Life.GetYoungOrganizerSubscription(ctx, token, query.ID)
	})
	if err != nil {
		return h.commandError("主办方关注查不到：", err)
	}
	h.markData(map[string]any{"operation": "organizer_subscription", "state": state})
	return fmt.Sprintf("主办方 %s：%s", state.OrganizerID, onOffText(state.Subscribed))
}

type youngSubscriptionQuery struct {
	Kind           string
	Action         string
	ID             string
	Enabled        bool
	RemindSignup   *bool
	RemindDeadline *bool
	RemindStart    *bool
}

func parseYoungSubscriptionArgs(args []string) (youngSubscriptionQuery, error) {
	if len(args) == 0 || (len(args) == 1 && normToken(args[0]) == "列表") {
		return youngSubscriptionQuery{Kind: "event", Action: "list"}, nil
	}
	query := youngSubscriptionQuery{Kind: "event"}
	if value := normToken(args[0]); value == "主办方" || value == "organizer" || value == "组织" || value == "关注" {
		query.Kind = "organizer"
		args = args[1:]
	} else if value == "活动" || value == "event" || value == "报名" {
		args = args[1:]
	}
	if len(args) == 0 || normToken(args[0]) == "列表" || normToken(args[0]) == "list" {
		return queryWithAction(query, "list"), nil
	}
	query.ID = strings.TrimSpace(args[0])
	if query.ID == "" {
		return youngSubscriptionQuery{}, errors.New("需要提供活动 youngId 或主办方 organizerId")
	}
	if len(args) == 1 {
		query.Action = "get"
		return query, nil
	}
	if query.Kind == "organizer" && len(args) != 2 {
		return youngSubscriptionQuery{}, errors.New("主办方关注用法：第二课堂 订阅 主办方 <organizerId> 开|关")
	}
	enabled, ok := parseYoungSubscriptionBool(args[1])
	if !ok {
		return youngSubscriptionQuery{}, errors.New("状态请使用开或关")
	}
	query.Action, query.Enabled = "set", enabled
	if query.Kind == "event" && len(args) > 2 {
		var parseErr error
		query.RemindSignup, query.RemindDeadline, query.RemindStart, parseErr = parseYoungReminderOptions(args[2:])
		if parseErr != nil {
			return youngSubscriptionQuery{}, parseErr
		}
	}
	return query, nil
}

func youngSubscriptionArgsAcceptable(args []string) bool {
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	_, err := parseYoungSubscriptionArgs(args)
	return err == nil
}

func queryWithAction(query youngSubscriptionQuery, action string) youngSubscriptionQuery {
	query.Action = action
	return query
}

func parseYoungSubscriptionBool(value string) (bool, bool) {
	switch normToken(value) {
	case "开", "开启", "订阅", "关注", "on", "true":
		return true, true
	case "关", "关闭", "取消", "退订", "取关", "off", "false":
		return false, true
	default:
		return false, false
	}
}

func parseYoungReminderOptions(args []string) (signup, deadline, start *bool, err error) {
	for i := 0; i < len(args); {
		field, value, consumed := splitYoungReminderToken(args[i])
		if !consumed {
			if i+1 >= len(args) {
				return nil, nil, nil, errors.New("提醒设置用法：报名|截止|开始提醒 开|关")
			}
			field, value = normToken(args[i]), args[i+1]
			i += 2
		} else {
			i++
		}
		parsed, ok := parseYoungSubscriptionBool(value)
		if !ok {
			return nil, nil, nil, errors.New("提醒状态请使用开或关")
		}
		valuePtr := &parsed
		switch field {
		case "报名", "报名提醒", "signup", "remindsignup", "remind-signup":
			if signup != nil {
				return nil, nil, nil, errors.New("报名提醒不能重复设置")
			}
			signup = valuePtr
		case "截止", "截止提醒", "deadline", "reminddeadline", "remind-deadline":
			if deadline != nil {
				return nil, nil, nil, errors.New("截止提醒不能重复设置")
			}
			deadline = valuePtr
		case "开始", "开始提醒", "start", "remindstart", "remind-start":
			if start != nil {
				return nil, nil, nil, errors.New("开始提醒不能重复设置")
			}
			start = valuePtr
		default:
			return nil, nil, nil, errors.New("提醒项请使用报名、截止或开始")
		}
	}
	return signup, deadline, start, nil
}

func splitYoungReminderToken(token string) (field, value string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(token), "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return normToken(parts[0]), parts[1], true
}

func formatYoungEventSubscription(state life.YoungEventSubscription) string {
	return fmt.Sprintf("活动订阅 %s：%s\n报名提醒：%s · 截止提醒：%s · 开始提醒：%s", state.YoungID, onOffText(state.Subscribed), onOffText(state.RemindSignup), onOffText(state.RemindDeadline), onOffText(state.RemindStart))
}

func formatYoungEventSubscriptions(states []life.YoungEventSubscription) string {
	if len(states) == 0 {
		return "还没有第二课堂活动订阅。"
	}
	lines := []string{"第二课堂活动订阅："}
	for i, state := range states {
		name := state.YoungID
		if state.Event != nil && state.Event.Name != "" {
			name = state.Event.Name + "（" + name + "）"
		}
		lines = append(lines, fmt.Sprintf("%d. %s · %s", i+1, name, onOffText(state.Subscribed)))
	}
	return strings.Join(lines, "\n")
}

func formatYoungOrganizerSubscriptions(states []life.YoungOrganizerSubscription) string {
	if len(states) == 0 {
		return "还没有关注第二课堂主办方。"
	}
	lines := []string{"第二课堂主办方关注："}
	for i, state := range states {
		name := state.OrganizerID
		if state.Organizer != nil && state.Organizer.Name != "" {
			name = state.Organizer.Name + "（" + name + "）"
		}
		lines = append(lines, fmt.Sprintf("%d. %s · %s", i+1, name, onOffText(state.Subscribed)))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) youngNotifications(ctx context.Context, ident store.Identity, args []string) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if len(args) > 0 && normToken(args[0]) == "已读" {
		args = append([]string{"read"}, args[1:]...)
	}
	if len(args) > 0 && normToken(args[0]) == "read" {
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return h.invalidInput("通知已读用法：第二课堂 通知 已读 <notificationId>")
		}
		if err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.MarkYoungNotificationRead(ctx, token, args[1])
		}); err != nil {
			return h.commandError("第二课堂通知标记失败：", err)
		}
		h.markData(map[string]any{"operation": "notification_read", "id": args[1], "success": true})
		return "第二课堂通知已标记为已读。"
	}
	var unread *bool
	if len(args) > 0 && (normToken(args[0]) == "未读" || normToken(args[0]) == "unread") {
		value := true
		unread = &value
	}
	notifications, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]life.YoungNotification, error) {
		return h.Life.ListAllYoungNotifications(ctx, token, unread)
	})
	if err != nil {
		return h.commandError("第二课堂通知查不到：", err)
	}
	h.markData(map[string]any{"operation": "notification_list", "data": notifications})
	if len(notifications) == 0 {
		return "暂无第二课堂通知。"
	}
	lines := []string{"第二课堂通知："}
	for i, item := range notifications {
		status := "已读"
		if item.ReadAt == "" {
			status = "未读"
		}
		lines = append(lines, fmt.Sprintf("%d. [%s] %s · %s", i+1, status, textutil.FirstNonEmpty(item.Title, item.Kind), item.CreatedAt))
		if item.Body != "" {
			lines = append(lines, "   "+item.Body)
		}
		if item.YoungID != "" {
			if link := h.Life.YoungEventURL(item.YoungID); link != "" {
				lines = append(lines, "   活动链接："+link)
			}
		}
		if item.OrganizerID != "" {
			if link := h.Life.YoungOrganizerURL(item.OrganizerID); link != "" {
				lines = append(lines, "   主办方链接："+link)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func youngNotificationArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return true
	}
	if len(args) == 1 {
		return firstArgIn(args, "未读", "unread")
	}
	return len(args) == 2 && firstArgIn(args, "已读", "read") && strings.TrimSpace(args[1]) != ""
}

func (h Handler) youngComments(ctx context.Context, ident store.Identity, args []string) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	query, err := parseYoungCommentArgs(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}
	switch query.Action {
	case "list":
		comments, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
			return h.Life.ListAllYoungComments(ctx, token, query.YoungID)
		})
		if err != nil {
			return h.commandError("活动评论查不到：", err)
		}
		h.markData(map[string]any{"operation": "comment_list", "youngId": query.YoungID, "data": comments})
		if len(comments) == 0 {
			return "暂无评论。"
		}
		lines := []string{"活动评论（" + query.YoungID + "）："}
		for i, item := range comments {
			appendYoungCommentLines(&lines, item, fmt.Sprintf("%d", i+1), 0, "")
		}
		return strings.Join(lines, "\n")
	case "create":
		result, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
			return h.Life.CreateYoungComment(ctx, token, query.YoungID, query.Body, query.CommentID, "", false)
		})
		if err != nil {
			return h.commandError("评论发送失败：", err)
		}
		h.markData(map[string]any{"operation": "comment_create", "youngId": query.YoungID, "comment": result})
		return "评论已发送。"
	case "update":
		result, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
			return h.Life.UpdateYoungComment(ctx, token, query.CommentID, query.Body)
		})
		if err != nil {
			return h.commandError("评论修改失败：", err)
		}
		h.markData(map[string]any{"operation": "comment_update", "comment": result})
		return "评论已修改。"
	case "delete":
		if err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.DeleteYoungComment(ctx, token, query.CommentID)
		}); err != nil {
			return h.commandError("评论删除失败：", err)
		}
		h.markData(map[string]any{"operation": "comment_delete", "commentId": query.CommentID})
		return "评论已删除。"
	case "react":
		if err := auth.WithRefreshVoid(ctx, h.Auth, ident, token, func(token string) error {
			return h.Life.ReactYoungComment(ctx, token, query.CommentID, query.Reaction, query.RemoveReaction)
		}); err != nil {
			return h.commandError("评论反应更新失败：", err)
		}
		h.markData(map[string]any{"operation": "comment_reaction", "commentId": query.CommentID, "reaction": query.Reaction, "removed": query.RemoveReaction})
		return "评论反应已更新。"
	default:
		return h.invalidInput("评论命令无法识别。")
	}
}

func appendYoungCommentLines(lines *[]string, comment map[string]any, number string, depth int, inheritedParentID string) {
	commentID := strings.TrimSpace(lifedata.FirstString(comment, "id", "commentId"))
	parentID := strings.TrimSpace(lifedata.FirstString(comment, "parentId"))
	if parentID == "" {
		parentID = strings.TrimSpace(inheritedParentID)
	}

	parts := make([]string, 0, 3)
	if commentID != "" {
		parts = append(parts, "commentId："+commentID)
	}
	if parentID != "" {
		parts = append(parts, "parentId："+parentID)
	}

	deleted := strings.EqualFold(strings.TrimSpace(lifedata.FirstString(comment, "status")), "deleted") || strings.TrimSpace(lifedata.FirstString(comment, "deletedAt")) != ""
	body := textutil.FirstNonEmpty(lifedata.FirstString(comment, "body"), lifedata.FirstString(comment, "renderedBody"))
	if deleted {
		body = "（评论已删除）"
	} else if body == "" {
		body = "（无内容）"
	}
	parts = append(parts, body)

	indent := strings.Repeat("  ", depth)
	*lines = append(*lines, fmt.Sprintf("%s%s. %s", indent, number, strings.Join(parts, " ")))

	for i, child := range lifedata.MapSlice(comment["replies"]) {
		appendYoungCommentLines(lines, child, fmt.Sprintf("%s.%d", number, i+1), depth+1, commentID)
	}
}

func youngCommentArgsAcceptable(args []string) bool {
	if firstArgIsHelp(args) {
		return len(args) == 1
	}
	_, err := parseYoungCommentArgs(args)
	return err == nil
}

type youngCommentQuery struct {
	Action         string
	YoungID        string
	CommentID      string
	Body           string
	Reaction       string
	RemoveReaction bool
}

func parseYoungCommentArgs(args []string) (youngCommentQuery, error) {
	if len(args) == 0 {
		return youngCommentQuery{}, errors.New("需要提供活动 youngId")
	}
	query := youngCommentQuery{Action: "list", YoungID: strings.TrimSpace(args[0])}
	if query.YoungID == "" {
		return youngCommentQuery{}, errors.New("需要提供活动 youngId")
	}
	if len(args) == 1 {
		return query, nil
	}
	action := normToken(args[1])
	args = args[2:]
	switch action {
	case "发", "发布", "评论", "post", "create":
		query.Action, query.Body = "create", strings.TrimSpace(joinedArgs(args))
	case "回复", "reply":
		if len(args) < 2 {
			return youngCommentQuery{}, errors.New("回复用法：第二课堂 评论 <youngId> 回复 <commentId> <内容>")
		}
		query.Action, query.CommentID, query.Body = "create", strings.TrimSpace(args[0]), strings.TrimSpace(joinedArgs(args[1:]))
	case "编辑", "修改", "edit", "update":
		if len(args) < 2 {
			return youngCommentQuery{}, errors.New("编辑用法：第二课堂 评论 <youngId> 编辑 <commentId> <内容>")
		}
		query.Action, query.CommentID, query.Body = "update", strings.TrimSpace(args[0]), strings.TrimSpace(joinedArgs(args[1:]))
	case "删除", "delete", "remove":
		if len(args) != 1 {
			return youngCommentQuery{}, errors.New("删除用法：第二课堂 评论 <youngId> 删除 <commentId>")
		}
		query.Action, query.CommentID = "delete", strings.TrimSpace(args[0])
	case "赞", "反应", "reaction", "react":
		if len(args) < 2 {
			return youngCommentQuery{}, errors.New("反应用法：第二课堂 评论 <youngId> 反应 <commentId> <type> [取消]")
		}
		query.Action, query.CommentID, query.Reaction = "react", strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
		if len(args) > 2 && (normToken(args[2]) == "取消" || normToken(args[2]) == "remove" || normToken(args[2]) == "off") {
			query.RemoveReaction = true
		}
	default:
		return youngCommentQuery{}, errors.New("评论用法：第二课堂 评论 <youngId>、发、回复、编辑、删除或反应")
	}
	if (query.Action == "create" || query.Action == "update") && query.Body == "" {
		return youngCommentQuery{}, errors.New("评论内容不能为空")
	}
	if query.CommentID == "" && query.Action != "list" && query.Action != "create" {
		return youngCommentQuery{}, errors.New("需要提供 commentId")
	}
	return query, nil
}
