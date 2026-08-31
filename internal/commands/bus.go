package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func parseGroupBus(text string) (Invocation, bool) {
	raw := stripCQCodes(text)
	if raw == "" || !containsBusKeyword(raw) {
		return Invocation{}, false
	}
	return commandResult(raw, "bus", busArgsFromText(raw)), true
}

var naturalBusAmbiguousMarkers = []string{
	"为什么", "怎么", "如何", "解释", "比较", "推荐", "设置", "偏好", "提醒", "添加", "修改", "删除", "问题", "bug",
	"查不到", "没查到", "不对", "错误",
}

func parseNaturalBusIntent(raw string) (Invocation, bool) {
	text := stripCQCodes(raw)
	if text == "" || strings.HasPrefix(text, "/") || !containsBusKeyword(text) || containsAny(strings.ToLower(text), naturalBusAmbiguousMarkers) {
		return Invocation{}, false
	}
	compact := strings.ToLower(strings.Join(strings.Fields(text), ""))
	query := false
	for _, marker := range []string{"查", "看", "几点", "什么时候", "哪班", "班次", "还有", "最早", "最晚", "下一班"} {
		if strings.Contains(compact, marker) {
			query = true
			break
		}
	}
	args := busArgsFromText(text)
	if !query && len(args) == 0 {
		return Invocation{}, false
	}
	invocation := commandResult(raw, "bus", args)
	invocation.NaturalRoute = "bus"
	return invocation, true
}

func containsBusKeyword(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "校车") || strings.Contains(lower, "班车") {
		return true
	}
	return textutil.IndexASCIIToken(lower, "xc") >= 0 || textutil.IndexASCIIToken(lower, "bus") >= 0
}

var cqCodeRE = regexp.MustCompile(`(?i)\[CQ:[^\]]+\]`)

var (
	busNumericDateRE      = regexp.MustCompile(`^([0-9]{4})[-/.]([0-9]{1,2})[-/.]([0-9]{1,2})$`)
	busChineseDateRE      = regexp.MustCompile(`^(?:([0-9]{4})年)?([0-9]{1,2})月([0-9]{1,2})[日号]$`)
	busScheduleSelectorRE = regexp.MustCompile(`(?i)(?:[0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}|(?:[0-9]{4}年)?[0-9]{1,2}月[0-9]{1,2}[日号]|周一(?:到|至|[-－—–~～])周五|星期一(?:到|至|[-－—–~～])星期五|礼拜一(?:到|至|[-－—–~～])礼拜五|周[一二三四五六日天]|星期[一二三四五六日天]|礼拜[一二三四五六日天]|工作日|周中|平日|周末|weekday|weekend|saturday|sunday|今天|明天|后天|today|tomorrow)`)
)

func stripCQCodes(text string) string {
	return strings.TrimSpace(cqCodeRE.ReplaceAllString(text, " "))
}

func busArgsFromText(text string) []string {
	type match struct {
		index int
		value string
	}
	matches := make([]match, 0, 3)
	seen := map[string]bool{}
	lookupText := strings.ToLower(text)
	for _, alias := range campusAliases() {
		for _, index := range campusAliasIndices(lookupText, alias) {
			campus := campusName(alias)
			seenKey := campus + "\x00" + strconv.Itoa(index)
			if campus != "" && !seen[seenKey] {
				matches = append(matches, match{index: index, value: campus})
				seen[seenKey] = true
			}
		}
	}
	for _, indices := range busScheduleSelectorRE.FindAllStringIndex(lookupText, -1) {
		value := text[indices[0]:indices[1]]
		seenKey := value + "\x00" + strconv.Itoa(indices[0])
		if !seen[seenKey] {
			matches = append(matches, match{index: indices[0], value: value})
			seen[seenKey] = true
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].index < matches[j].index
	})
	args := make([]string, 0, 3)
	for _, match := range matches {
		args = append(args, match.value)
	}
	campuses := busCampusesFromArgs(args)
	if len(campuses) == 1 {
		for _, match := range matches {
			if knownCampusName(match.value) && hasDestinationMarkerBefore(lookupText, match.index) {
				args = append([]string{"到"}, args...)
				break
			}
		}
	}
	return args
}

func campusAliasIndices(text, alias string) []int {
	indices := []int{}
	offset := 0
	for {
		index := campusAliasIndex(text[offset:], alias)
		if index < 0 {
			return indices
		}
		absolute := offset + index
		indices = append(indices, absolute)
		offset = absolute + len(alias)
		if offset >= len(text) {
			return indices
		}
	}
}

func hasDestinationMarkerBefore(text string, index int) bool {
	if index <= 0 || index > len(text) {
		return false
	}
	before := strings.TrimSpace(text[:index])
	if strings.HasSuffix(before, "从") || textutil.IndexASCIIToken(before, "from") >= 0 {
		return false
	}
	if strings.HasSuffix(before, "到") {
		return true
	}
	fields := strings.Fields(before)
	return len(fields) > 0 && fields[len(fields)-1] == "to"
}

func campusAliasIndex(text, alias string) int {
	alias = strings.ToLower(alias)
	for i := 0; i < len(alias); i++ {
		if alias[i] > 127 {
			if utf8.RuneCountInString(alias) == 1 {
				return singleChineseCampusAliasIndex(text, alias)
			}
			return strings.Index(text, alias)
		}
	}
	return textutil.IndexASCIIToken(text, alias)
}

func singleChineseCampusAliasIndex(text, alias string) int {
	index := strings.Index(text, alias)
	for index >= 0 {
		if singleChineseCampusAliasAt(text, index, len(alias)) {
			return index
		}
		next := strings.Index(text[index+len(alias):], alias)
		if next < 0 {
			return -1
		}
		index += len(alias) + next
	}
	return -1
}

func singleChineseCampusAliasAt(text string, index, length int) bool {
	before := text[:index]
	after := text[index+length:]
	if strings.HasSuffix(before, "从") || strings.HasSuffix(before, "到") || strings.HasSuffix(before, "去") || strings.HasSuffix(before, "往") {
		return true
	}
	if strings.HasPrefix(after, "到") || strings.HasPrefix(after, "去") || strings.HasPrefix(after, "往") {
		return true
	}
	return chineseCampusAliasBoundaryBefore(before) && chineseCampusAliasBoundaryAfter(after)
}

func chineseCampusAliasBoundaryBefore(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text)
	return isChineseCampusAliasBoundaryRune(r)
}

func chineseCampusAliasBoundaryAfter(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text)
	return isChineseCampusAliasBoundaryRune(r)
}

func isChineseCampusAliasBoundaryRune(r rune) bool {
	return !unicode.Is(unicode.Han, r) && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func (h Handler) bus(ctx context.Context, ident store.Identity, args []string) string {
	return h.busAt(ctx, ident, args, time.Now())
}

func (h Handler) busAt(ctx context.Context, ident store.Identity, args []string, now time.Time) string {
	if firstArgIs(args, "help") {
		return busHelp()
	}
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return commandError("校车查不到：", err)
	}
	if busPreferenceArgs(args) {
		return h.busPreferences(ctx, ident, data, args)
	}
	routeArgs, queryOptions := busQueryArgs(args, now)
	if queryOptions.QueryError != "" {
		return queryOptions.QueryError
	}
	options := queryOptions
	if !store.IsGroupConversation(ident) {
		preferences, ok := h.currentBusPreferences(ctx, ident)
		if ok {
			if options.ExplicitRoute && !options.ExplicitSchedule {
				options.ShowDeparted = preferences.ShowDepartedTrips
			}
			if options.UsePreferredRoute && len(routeArgs) == 0 && preferences.PreferredOriginCampusID != nil && preferences.PreferredDestinationCampusID != nil {
				options.ShowDeparted = preferences.ShowDepartedTrips
				if from, ok := campusNameByID(data, *preferences.PreferredOriginCampusID); ok {
					if to, ok := campusNameByID(data, *preferences.PreferredDestinationCampusID); ok {
						routeArgs = []string{from, to}
					}
				}
			}
		}
	}
	if h.EnableImageResponses {
		options.UsePreferredRoute = false
		options.ShowAll = true
		options.ShowDeparted = true
		options.After = false
		if options.ExplicitRoute {
			options.BidirectionalRoute = true
		} else {
			routeArgs = nil
		}
	}
	if len(options.AdditionalSchedules) == 0 {
		return h.busReplyForOptions(ctx, ident, data, routeArgs, now, options)
	}
	selections := append([]busScheduleSelection{{ServiceDay: options.ServiceDay, Date: options.Now, Label: options.DateLabel}}, options.AdditionalSchedules...)
	replies := make([]string, 0, len(selections))
	for _, selection := range selections {
		selectionOptions := options
		selectionOptions.ServiceDay = selection.ServiceDay
		selectionOptions.Now = selection.Date
		selectionOptions.DateLabel = selection.Label
		selectionOptions.AdditionalSchedules = nil
		replies = append(replies, h.busReplyForOptions(ctx, ident, data, routeArgs, now, selectionOptions))
	}
	return strings.Join(replies, "\n\n")
}

func (h Handler) busReplyForOptions(ctx context.Context, ident store.Identity, data map[string]any, routeArgs []string, now time.Time, options busQueryOptions) string {
	var items []busItem
	if options.ExplicitRoute {
		items = nextBusItemsWithOptions(data, routeArgs, now, options)
	} else {
		limit := busOverviewTripsPerRoute
		if options.ShowAll {
			limit = 0
		}
		items = nextBusItemsByRouteLimitWithOptions(data, routeArgs, now, options, limit)
		if len(routeArgs) == 0 && !h.EnableImageResponses && !h.showSouthCampusBus(ctx, ident) {
			items = filterSouthCampusBusItems(items)
		}
	}
	if len(items) == 0 {
		if options.ExplicitSchedule {
			if options.DateLabel != "" {
				return "查询日期：" + options.DateLabel + "\n没有查到校车。"
			}
			return "没查到符合指定服务日的校车。"
		}
		return "今天后面没查到校车。"
	}
	if options.ShowDeparted && !options.After {
		items = markNextBusItem(items, effectiveBusNow(now, options))
	}
	if options.ExplicitRoute {
		if campuses := busCampusesFromArgs(routeArgs); len(campuses) >= 2 {
			items = projectBusItemsToCampuses(items, campuses[:2])
		}
	}
	reply := strings.Join(formatBusItemsByRouteGroup(items, 0), "\n")
	if options.DateLabel != "" {
		reply = "查询日期：" + options.DateLabel + "\n" + reply
	}
	return reply
}

func busHelp() string {
	return strings.Join([]string{
		"可以直接发：",
		"校车",
		"校车 全部",
		"校车 周六 周日",
		"校车 周六 东区 太湖路园区",
		"校车 周日 太湖路园区 东区",
		"校车 工作日 东区 西区",
		"校车 2026-09-05 东区 西区",
		"校车 我的路线",
		"xc 东区 西区",
		"校车 偏好",
		"校车 设置 东区 西区",
		"校车 已发车 开",
		"校车 南区 开",
		"校车 已发车 关",
	}, "\n")
}

func (h Handler) busPreferences(ctx context.Context, ident store.Identity, data map[string]any, args []string) string {
	if store.IsGroupConversation(ident) {
		return "群聊只能查校车；偏好请私聊设置。"
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	preferences, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.BusPreferences, error) {
		return h.Life.BusPreferences(ctx, token)
	})
	if err != nil {
		return commandError("校车偏好查不到：", err)
	}
	busSettings := h.currentBusSettings(ctx, ident)
	if showSouth, ok := parseBusShowSouth(args); ok {
		busSettings.ShowSouthCampus = showSouth
		args = removeBusShowSouthArgs(args)
		args = removeBusPreferenceCommandArgs(args)
	}
	update, shouldSave, message := parseBusPreferenceUpdate(data, args, preferences)
	if message != "" {
		return message
	}
	shouldSaveBusSettings := busSettings.ShowSouthCampus != h.currentBusSettings(ctx, ident).ShowSouthCampus
	if !shouldSave && !shouldSaveBusSettings {
		return formatBusPreferences(data, preferences, busSettings, "校车偏好：")
	}
	if shouldSaveBusSettings && h.Store != nil {
		if err := h.Store.SaveBusSettings(ctx, busSettings); err != nil {
			return commandError("校车偏好保存失败：", err)
		}
	}
	if shouldSave {
		preferences, err = auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.BusPreferences, error) {
			return h.Life.SetBusPreferences(ctx, token, update)
		})
		if err != nil {
			return commandError("校车偏好保存失败：", err)
		}
	}
	return formatBusPreferences(data, preferences, busSettings, "已更新校车偏好：")
}

func (h Handler) currentBusPreferences(ctx context.Context, ident store.Identity) (life.BusPreferences, bool) {
	if h.Auth == nil || h.Auth.Store == nil {
		return life.BusPreferences{}, false
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return life.BusPreferences{}, false
	}
	preferences, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.BusPreferences, error) {
		return h.Life.BusPreferences(ctx, token)
	})
	return preferences, err == nil
}

func (h Handler) currentBusSettings(ctx context.Context, ident store.Identity) store.BusSettings {
	if h.Store == nil {
		return store.BusSettings{Identity: ident}
	}
	settings, err := h.Store.BusSettings(ctx, ident)
	if err != nil {
		h.logf("load bus settings failed: %v", err)
		return store.BusSettings{Identity: ident}
	}
	return settings
}

func (h Handler) showSouthCampusBus(ctx context.Context, ident store.Identity) bool {
	return h.currentBusSettings(ctx, ident).ShowSouthCampus
}

func busPreferenceArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch normToken(args[0]) {
	case "preference", "preferences", "pref", "prefs", "偏好", "默认", "设置", "set", "已发车", "已出发", "show-departed", "departed", "南区校车", "show-south", "south-campus":
		return true
	default:
		return normToken(args[0]) == "南区" && len(args) > 1 && busBoolArg(args[1])
	}
}

func busQueryArgs(args []string, now time.Time) ([]string, busQueryOptions) {
	queryArgs := make([]string, 0, len(args))
	options := busQueryOptions{}
	selections := make([]busScheduleSelection, 0, 2)
	for _, arg := range args {
		parsed, recognized, err := parseBusScheduleSelectors(arg, now)
		if !recognized {
			queryArgs = append(queryArgs, arg)
			continue
		}
		if err != "" {
			options.QueryError = err
			continue
		}
		selections = append(selections, parsed...)
	}
	selections = deduplicateBusScheduleSelections(selections)
	if len(selections) > 1 {
		if !isSaturdaySundaySelections(selections) {
			options.QueryError = "一次只能查询一个日期；周六和周日可以一起查询。"
		} else if aligned, ok := alignRelativeSaturdaySundaySelections(selections, now); ok {
			selections = aligned
		} else {
			options.QueryError = "一起查询的周六和周日必须属于同一个周末。"
		}
	}
	if len(selections) > 0 {
		selection := selections[0]
		options.ServiceDay = selection.ServiceDay
		options.Now = selection.Date
		options.DateLabel = selection.Label
		options.AdditionalSchedules = append([]busScheduleSelection(nil), selections[1:]...)
		options.ExplicitSchedule = true
		options.ShowDeparted = true
	}

	out := make([]string, 0, len(queryArgs))
	for i := 0; i < len(queryArgs); i++ {
		switch normToken(queryArgs[i]) {
		case "all", "al", "全部", "所有":
			options.ShowAll = true
			continue
		case "preferred", "preference-route", "pref-route", "我的路线", "偏好路线", "默认路线":
			options.UsePreferredRoute = true
			continue
		case "after", "之后", "以后":
			baseTime := now
			if !options.Now.IsZero() {
				baseTime = options.Now
			}
			if i+1 < len(queryArgs) {
				if after, ok := parseBusAfterTime(queryArgs[i+1], baseTime); ok {
					options.Now = after
					options.After = true
					i++
					continue
				}
				if i+2 < len(queryArgs) {
					if after, ok := parseBusAfterTime(queryArgs[i+1]+" "+queryArgs[i+2], baseTime); ok {
						options.Now = after
						options.After = true
						i += 2
						continue
					}
				}
			}
		case "show-departed", "departed", "已发车", "已出发":
			if i+1 < len(queryArgs) {
				if value, ok := parseBusBool(queryArgs[i+1]); ok {
					options.ShowDeparted = value
					i++
					continue
				}
			}
			options.ShowDeparted = true
			continue
		}
		out = append(out, queryArgs[i])
	}
	if options.After && len(options.AdditionalSchedules) > 0 {
		options.QueryError = "一次只能为一个日期指定“之后”时间。"
	}
	options.ExplicitRoute = len(busCampusesFromArgs(out)) >= 2
	return out, options
}

type busScheduleSelection struct {
	ServiceDay      string
	Date            time.Time
	Label           string
	RelativeWeekday bool
}

func parseBusScheduleSelectors(value string, now time.Time) ([]busScheduleSelection, bool, string) {
	switch normToken(value) {
	case "周末", "weekend":
		saturday := nextBusWeekdayDate(now, time.Saturday)
		return []busScheduleSelection{
			newBusScheduleSelection(saturday),
			newBusScheduleSelection(saturday.AddDate(0, 0, 1)),
		}, true, ""
	}
	selection, recognized, err := parseBusScheduleSelector(value, now)
	if !recognized || err != "" {
		return nil, recognized, err
	}
	return []busScheduleSelection{selection}, true, ""
}

func parseBusScheduleSelector(value string, now time.Time) (busScheduleSelection, bool, string) {
	value = normToken(value)
	loc := lifedata.ChinaLocation()
	now = now.In(loc)
	switch value {
	case "今天", "today":
		date := dateAtMidnight(now)
		return newBusScheduleSelection(date), true, ""
	case "明天", "tomorrow":
		date := dateAtMidnight(now).AddDate(0, 0, 1)
		return newBusScheduleSelection(date), true, ""
	case "后天":
		date := dateAtMidnight(now).AddDate(0, 0, 2)
		return newBusScheduleSelection(date), true, ""
	case "周六", "星期六", "礼拜六", "saturday":
		selection := newBusScheduleSelection(nextBusWeekdayDate(now, time.Saturday))
		selection.RelativeWeekday = true
		return selection, true, ""
	case "周日", "周天", "星期日", "星期天", "礼拜日", "礼拜天", "sunday":
		selection := newBusScheduleSelection(nextBusWeekdayDate(now, time.Sunday))
		selection.RelativeWeekday = true
		return selection, true, ""
	case "周一", "周二", "周三", "周四", "周五",
		"星期一", "星期二", "星期三", "星期四", "星期五",
		"礼拜一", "礼拜二", "礼拜三", "礼拜四", "礼拜五",
		"周一-周五", "周一－周五", "周一—周五", "周一–周五", "周一~周五", "周一～周五", "周一到周五", "周一至周五",
		"星期一-星期五", "星期一－星期五", "星期一—星期五", "星期一–星期五", "星期一~星期五", "星期一～星期五", "星期一到星期五", "星期一至星期五",
		"礼拜一-礼拜五", "礼拜一－礼拜五", "礼拜一—礼拜五", "礼拜一–礼拜五", "礼拜一~礼拜五", "礼拜一～礼拜五", "礼拜一到礼拜五", "礼拜一至礼拜五",
		"工作日", "周中", "平日", "weekday":
		return busScheduleSelection{ServiceDay: "weekday"}, true, ""
	}

	if match := busNumericDateRE.FindStringSubmatch(value); match != nil {
		return parsedBusDate(match[1], match[2], match[3], now)
	}
	if match := busChineseDateRE.FindStringSubmatch(value); match != nil {
		year := match[1]
		if year == "" {
			year = strconv.Itoa(now.Year())
		}
		return parsedBusDate(year, match[2], match[3], now)
	}
	return busScheduleSelection{}, false, ""
}

func parsedBusDate(yearText, monthText, dayText string, now time.Time) (busScheduleSelection, bool, string) {
	year, yearErr := strconv.Atoi(yearText)
	month, monthErr := strconv.Atoi(monthText)
	day, dayErr := strconv.Atoi(dayText)
	if yearErr != nil || monthErr != nil || dayErr != nil {
		return busScheduleSelection{}, true, "校车查询日期格式不正确。"
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, lifedata.ChinaLocation())
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return busScheduleSelection{}, true, "校车查询日期不存在。"
	}
	return newBusScheduleSelection(date), true, ""
}

func newBusScheduleSelection(date time.Time) busScheduleSelection {
	date = dateAtMidnight(date)
	weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return busScheduleSelection{
		ServiceDay: busServiceDay(date),
		Date:       date,
		Label:      date.Format("2006-01-02") + "（" + weekdays[date.Weekday()] + "）",
	}
}

func nextBusWeekdayDate(now time.Time, weekday time.Weekday) time.Time {
	today := dateAtMidnight(now)
	days := (int(weekday) - int(today.Weekday()) + 7) % 7
	return today.AddDate(0, 0, days)
}

func deduplicateBusScheduleSelections(selections []busScheduleSelection) []busScheduleSelection {
	out := make([]busScheduleSelection, 0, len(selections))
	seen := make(map[string]bool, len(selections))
	for _, selection := range selections {
		key := selection.ServiceDay + "\x00" + selection.Date.Format("2006-01-02")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, selection)
	}
	return out
}

func isSaturdaySundaySelections(selections []busScheduleSelection) bool {
	if len(selections) != 2 {
		return false
	}
	return (selections[0].ServiceDay == "saturday" && selections[1].ServiceDay == "sunday") ||
		(selections[0].ServiceDay == "sunday" && selections[1].ServiceDay == "saturday")
}

func alignRelativeSaturdaySundaySelections(selections []busScheduleSelection, now time.Time) ([]busScheduleSelection, bool) {
	var saturday, sunday busScheduleSelection
	for _, selection := range selections {
		if selection.ServiceDay == "saturday" {
			saturday = selection
		} else {
			sunday = selection
		}
	}
	if !saturday.Date.IsZero() && !sunday.Date.IsZero() && sameCalendarDate(saturday.Date.AddDate(0, 0, 1), sunday.Date) {
		return selections, true
	}
	if saturday.RelativeWeekday && sunday.RelativeWeekday {
		saturday = newBusScheduleSelection(nextBusWeekdayDate(now, time.Saturday))
		sunday = newBusScheduleSelection(saturday.Date.AddDate(0, 0, 1))
		saturday.RelativeWeekday = true
		sunday.RelativeWeekday = true
	} else {
		return nil, false
	}
	if selections[0].ServiceDay == "sunday" {
		return []busScheduleSelection{sunday, saturday}, true
	}
	return []busScheduleSelection{saturday, sunday}, true
}

func busServiceDay(date time.Time) string {
	switch date.In(lifedata.ChinaLocation()).Weekday() {
	case time.Saturday:
		return "saturday"
	case time.Sunday:
		return "sunday"
	default:
		return "weekday"
	}
}

func dateAtMidnight(value time.Time) time.Time {
	value = value.In(lifedata.ChinaLocation())
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func sameCalendarDate(left, right time.Time) bool {
	left = left.In(lifedata.ChinaLocation())
	right = right.In(lifedata.ChinaLocation())
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}

func parseBusAfterTime(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	loc := lifedata.ChinaLocation()
	now = now.In(loc)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.In(loc), true
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "15:04"} {
		parsed, err := time.ParseInLocation(layout, value, loc)
		if err != nil {
			continue
		}
		if layout == "15:04" {
			return time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc), true
		}
		return parsed, true
	}
	return time.Time{}, false
}

func parseBusPreferenceUpdate(data map[string]any, args []string, current life.BusPreferences) (life.BusPreferences, bool, string) {
	args = copyArgs(args)
	if len(args) == 0 {
		return current, false, ""
	}
	switch normToken(args[0]) {
	case "preference", "preferences", "pref", "prefs", "偏好", "默认":
		args = args[1:]
	case "set", "设置":
		args = args[1:]
		if len(args) == 0 {
			return current, false, "想设置哪条路线？例如：校车 设置 东区 西区"
		}
	}
	if len(args) == 0 {
		return current, false, ""
	}
	next := current
	if showDeparted, ok := parseBusShowDeparted(args); ok {
		next.ShowDepartedTrips = showDeparted
		args = removeBusShowDepartedArgs(args)
	}
	campuses := busCampusesFromArgs(args)
	switch len(campuses) {
	case 0:
		return next, true, ""
	case 1:
		return current, false, "路线需要出发和到达校区。例如：校车 设置 东区 西区"
	default:
		fromID, ok := campusIDByName(data, campuses[0])
		if !ok {
			return current, false, "没找到出发校区：" + campuses[0]
		}
		toID, ok := campusIDByName(data, campuses[1])
		if !ok {
			return current, false, "没找到到达校区：" + campuses[1]
		}
		next.PreferredOriginCampusID = &fromID
		next.PreferredDestinationCampusID = &toID
		return next, true, ""
	}
}

func formatBusPreferences(data map[string]any, preferences life.BusPreferences, busSettings store.BusSettings, title string) string {
	route := "未设置"
	if preferences.PreferredOriginCampusID != nil && preferences.PreferredDestinationCampusID != nil {
		from, fromOK := campusNameByID(data, *preferences.PreferredOriginCampusID)
		to, toOK := campusNameByID(data, *preferences.PreferredDestinationCampusID)
		if fromOK && toOK {
			route = from + " → " + to
		} else {
			route = fmt.Sprintf("%d → %d", *preferences.PreferredOriginCampusID, *preferences.PreferredDestinationCampusID)
		}
	}
	showDeparted := "不显示"
	if preferences.ShowDepartedTrips {
		showDeparted = "显示"
	}
	showSouth := "不显示"
	if busSettings.ShowSouthCampus {
		showSouth = "显示"
	}
	return strings.Join([]string{
		title,
		"路线：" + route,
		"已发车：" + showDeparted,
		"南区：" + showSouth,
	}, "\n")
}

func parseBusShowDeparted(args []string) (bool, bool) {
	for i, arg := range args {
		switch normToken(arg) {
		case "show-departed", "departed", "已发车", "已出发":
			if i+1 < len(args) {
				if value, ok := parseBusBool(args[i+1]); ok {
					return value, true
				}
			}
			return true, true
		}
		if value, ok := parseBusBool(arg); ok && i > 0 {
			prev := normToken(args[i-1])
			if prev == "show-departed" || prev == "departed" || prev == "已发车" || prev == "已出发" {
				return value, true
			}
		}
	}
	return false, false
}

func parseBusShowSouth(args []string) (bool, bool) {
	for i, arg := range args {
		switch normToken(arg) {
		case "南区", "南区校车", "show-south", "south-campus":
			if i+1 < len(args) {
				if value, ok := parseBusBool(args[i+1]); ok {
					return value, true
				}
			}
		}
	}
	return false, false
}

func parseBusBool(value string) (bool, bool) {
	switch normToken(value) {
	case "on", "true", "1", "yes", "y", "open", "enable", "enabled", "show", "开", "开启", "显示":
		return true, true
	case "off", "false", "0", "no", "n", "close", "disable", "disabled", "hide", "关", "关闭", "不显示", "隐藏":
		return false, true
	default:
		return false, false
	}
}

func busBoolArg(value string) bool {
	_, ok := parseBusBool(value)
	return ok
}

func removeBusShowDepartedArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch normToken(args[i]) {
		case "show-departed", "departed", "已发车", "已出发":
			if i+1 < len(args) {
				if _, ok := parseBusBool(args[i+1]); ok {
					i++
				}
			}
			continue
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func removeBusShowSouthArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch normToken(args[i]) {
		case "南区", "南区校车", "show-south", "south-campus":
			if i+1 < len(args) {
				if _, ok := parseBusBool(args[i+1]); ok {
					i++
					continue
				}
			}
		}
		out = append(out, args[i])
	}
	return out
}

func removeBusPreferenceCommandArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch normToken(arg) {
		case "preference", "preferences", "pref", "prefs", "偏好", "默认", "set", "设置":
			continue
		default:
			out = append(out, arg)
		}
	}
	return out
}

func busCampusesFromArgs(args []string) []string {
	campuses := make([]string, 0, 2)
	for _, arg := range args {
		campus := campusName(arg)
		if campus == "" || campus == arg && !knownCampusName(campus) {
			continue
		}
		campuses = append(campuses, campus)
		if len(campuses) >= 2 {
			break
		}
	}
	return campuses
}

func knownCampusName(value string) bool {
	switch campusName(value) {
	case "东区", "西区", "中区", "北区", "南区", "高新区", "先研院", "太湖路园区":
		return true
	default:
		return false
	}
}

func campusIDByName(data map[string]any, name string) (int, bool) {
	name = campusName(name)
	for _, campus := range lifedata.MapSlice(data["campuses"]) {
		id, ok := lifedata.IntValue(campus["id"])
		if !ok {
			continue
		}
		for _, key := range []string{"nameCn", "namePrimary", "nameSecondary", "nameEn", "name"} {
			if campusName(lifedata.FirstString(campus, key)) == name {
				return id, true
			}
		}
	}
	return 0, false
}

func campusNameByID(data map[string]any, id int) (string, bool) {
	for _, campus := range lifedata.MapSlice(data["campuses"]) {
		campusID, ok := lifedata.IntValue(campus["id"])
		if !ok || campusID != id {
			continue
		}
		name := campusName(lifedata.FirstString(campus, "nameCn", "namePrimary", "nameSecondary", "nameEn", "name"))
		return name, name != ""
	}
	return "", false
}

type busItem struct {
	RouteID          string
	DepartureCampus  string
	ArrivalCampus    string
	Stops            []busStop
	DepartureMinutes int
	DepartureTime    string
	Arrival          string
	Route            string
	Highlight        bool
}

type busStop struct {
	Name string
	Time string
}

type busQueryOptions struct {
	ShowDeparted        bool
	Now                 time.Time
	After               bool
	UsePreferredRoute   bool
	ExplicitRoute       bool
	BidirectionalRoute  bool
	ShowAll             bool
	ServiceDay          string
	DateLabel           string
	AdditionalSchedules []busScheduleSelection
	ExplicitSchedule    bool
	QueryError          string
}

func nextBusItems(data map[string]any, args []string, now time.Time) []busItem {
	return nextBusItemsWithOptions(data, args, now, busQueryOptions{})
}

func nextBusItemsWithOptions(data map[string]any, args []string, now time.Time, options busQueryOptions) []busItem {
	now = effectiveBusNow(now, options)
	from, to := busFilter(args)
	dayType := options.ServiceDay
	if dayType == "" {
		dayType = busServiceDay(now)
	}
	nowMinutes := now.Hour()*60 + now.Minute()
	routes := busRouteMap(data["routes"])
	trips := lifedata.MapSlice(data["trips"])
	items := make([]busItem, 0, len(trips))
	for _, trip := range trips {
		if lifedata.FirstString(trip, "dayType") != dayType {
			continue
		}
		departure, ok := lifedata.IntValue(trip["departureMinutes"])
		if !ok {
			continue
		}
		if (!options.ShowDeparted || options.After) && departure < nowMinutes {
			continue
		}
		route := routes[lifedata.FirstString(trip, "routeId")]
		routeStops := route.StopNames
		if len(routeStops) == 0 {
			routeStops = tripStopNames(trip)
		}
		matchesRoute := routeMatches(routeStops, from, to)
		if options.BidirectionalRoute && from != "" && to != "" {
			matchesRoute = matchesRoute || routeMatches(routeStops, to, from)
		}
		if !matchesRoute {
			continue
		}
		routeID := lifedata.FirstString(trip, "routeId")
		stops := busStops(trip, routeStops, busTime(lifedata.FirstString(trip, "departureTime"), departure), lifedata.FirstString(trip, "arrivalTime"))
		items = append(items, busItem{
			RouteID:          routeID,
			DepartureCampus:  firstStop(stops),
			ArrivalCampus:    lastStop(stops),
			Stops:            stops,
			DepartureMinutes: departure,
			DepartureTime:    busTime(lifedata.FirstString(trip, "departureTime"), departure),
			Arrival:          lifedata.FirstString(trip, "arrivalTime"),
			Route:            busRouteLabel(route, routeStops),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].DepartureMinutes < items[j].DepartureMinutes
	})
	return items
}

func effectiveBusNow(now time.Time, options busQueryOptions) time.Time {
	if !options.Now.IsZero() {
		now = options.Now
	}
	return now.In(lifedata.ChinaLocation())
}

func markNextBusItem(items []busItem, now time.Time) []busItem {
	now = now.In(lifedata.ChinaLocation())
	nowMinutes := now.Hour()*60 + now.Minute()
	out := append([]busItem(nil), items...)
	nextIndex := -1
	for i, item := range out {
		if item.DepartureMinutes < nowMinutes {
			continue
		}
		if nextIndex < 0 || item.DepartureMinutes < out[nextIndex].DepartureMinutes {
			nextIndex = i
		}
	}
	if nextIndex >= 0 {
		out[nextIndex].Highlight = true
	}
	return out
}

func nextBusByRoute(data map[string]any, args []string, now time.Time) []busItem {
	return nextBusByRouteWithOptions(data, args, now, busQueryOptions{})
}

func nextBusByRouteWithOptions(data map[string]any, args []string, now time.Time, options busQueryOptions) []busItem {
	return nextBusItemsByRouteLimitWithOptions(data, args, now, options, 1)
}

func nextBusItemsByRouteLimitWithOptions(data map[string]any, args []string, now time.Time, options busQueryOptions, limit int) []busItem {
	items := nextBusItemsWithOptions(data, args, now, options)
	byRoute := make(map[string][]busItem)
	for _, item := range items {
		key := busRouteKey(item)
		if limit > 0 && len(byRoute[key]) >= limit {
			continue
		}
		byRoute[key] = append(byRoute[key], item)
	}
	out := make([]busItem, 0, len(byRoute)*limit)
	for _, routeItems := range byRoute {
		out = append(out, routeItems...)
	}
	sort.Slice(out, func(i, j int) bool {
		leftDeparture := busDepartureCampus(out[i])
		rightDeparture := busDepartureCampus(out[j])
		if campusRank(leftDeparture) != campusRank(rightDeparture) {
			return campusRank(leftDeparture) < campusRank(rightDeparture)
		}
		leftKey := busRouteKey(out[i])
		rightKey := busRouteKey(out[j])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		if out[i].DepartureMinutes == out[j].DepartureMinutes {
			return out[i].Route < out[j].Route
		}
		return out[i].DepartureMinutes < out[j].DepartureMinutes
	})
	return out
}

func filterSouthCampusBusItems(items []busItem) []busItem {
	out := make([]busItem, 0, len(items))
	for _, item := range items {
		if hasBusStop(busItemStopNames(item), "南区") {
			continue
		}
		out = append(out, item)
	}
	return out
}

func formatBusItemsByRouteGroup(items []busItem, limit int) []string {
	items = sortedBusItemsByRouteGroup(items)
	lines := make([]string, 0, len(items)*3)
	routeItems := []busItem{}
	for i, item := range items {
		if limit > 0 && i >= limit {
			break
		}
		if len(routeItems) > 0 && busRouteKey(item) != busRouteKey(routeItems[0]) {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, formatBusItemsAsStopTimeTable(routeItems)...)
			routeItems = nil
		}
		routeItems = append(routeItems, item)
	}
	if len(routeItems) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, formatBusItemsAsStopTimeTable(routeItems)...)
	}
	return lines
}

func formatBusItemsAsStopTimeTable(items []busItem) []string {
	stops := busTableStops(items)
	if len(stops) == 0 {
		return nil
	}
	rows := make([]textutil.DisplayTableRow, 0, len(items))
	for _, item := range items {
		times := busStopTimes(item)
		cells := make([]string, 0, len(stops))
		for _, stop := range stops {
			timeText := ""
			if stopTime := times[stop]; stopTime != "" {
				timeText = textutil.MonospaceDigits(stopTime)
			}
			cells = append(cells, timeText)
		}
		row := textutil.DisplayTableRow{Cells: cells}
		if item.Highlight {
			row.Suffix = "✨"
		}
		rows = append(rows, row)
	}
	return textutil.FormatDisplayTable(stops, rows, textutil.DisplayTableOptions{
		EmptyWidthText: busMissingTimePlaceholder,
		EmptyCell:      busTableBlankCell,
	})
}

func projectBusItemsToCampuses(items []busItem, campuses []string) []busItem {
	if len(campuses) < 2 {
		return items
	}
	from := campusName(campuses[0])
	to := campusName(campuses[1])
	if from == "" || to == "" || from == to {
		return items
	}
	out := make([]busItem, 0, len(items))
	for _, item := range items {
		fromIdx, toIdx := busStopIndexes(item.Stops, from, to)
		if fromIdx < 0 || toIdx < 0 {
			continue
		}
		lo, hi := fromIdx, toIdx
		if lo > hi {
			lo, hi = hi, lo
		}
		segment := make([]busStop, 0, hi-lo+1)
		for _, stop := range item.Stops[lo : hi+1] {
			segment = append(segment, busStop{Name: campusName(stop.Name), Time: stop.Time})
		}
		next := item
		next.Stops = segment
		out = append(out, next)
	}
	if len(out) == 0 {
		return items
	}
	return out
}

func busStopIndexes(stops []busStop, from, to string) (int, int) {
	fromIdx := -1
	for i, stop := range stops {
		name := campusName(stop.Name)
		if name == from && fromIdx < 0 {
			fromIdx = i
		}
		if fromIdx >= 0 && name == to {
			return fromIdx, i
		}
	}
	fromIdx = -1
	for i, stop := range stops {
		name := campusName(stop.Name)
		if name == to && fromIdx < 0 {
			fromIdx = i
		}
		if fromIdx >= 0 && name == from {
			return fromIdx, i
		}
	}
	return -1, -1
}

func busTableBlankCell(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat("\u3000", width/2) + strings.Repeat(" ", width%2)
}

func busTableStops(items []busItem) []string {
	stops := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		for _, stop := range item.Stops {
			if stop.Name == "" || seen[stop.Name] {
				continue
			}
			stops = append(stops, stop.Name)
			seen[stop.Name] = true
		}
	}
	return stops
}

func busStopTimes(item busItem) map[string]string {
	times := make(map[string]string, len(item.Stops))
	for _, stop := range item.Stops {
		if stop.Name != "" {
			times[stop.Name] = stop.Time
		}
	}
	return times
}

func sortedBusItemsByRouteGroup(items []busItem) []busItem {
	out := append([]busItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		leftGroup := busRouteGroupRank(out[i])
		rightGroup := busRouteGroupRank(out[j])
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		leftDeparture := busDepartureCampus(out[i])
		rightDeparture := busDepartureCampus(out[j])
		if campusRank(leftDeparture) != campusRank(rightDeparture) {
			return campusRank(leftDeparture) < campusRank(rightDeparture)
		}
		leftKey := busRouteKey(out[i])
		rightKey := busRouteKey(out[j])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		if out[i].DepartureMinutes == out[j].DepartureMinutes {
			return out[i].Route < out[j].Route
		}
		return out[i].DepartureMinutes < out[j].DepartureMinutes
	})
	return out
}

func busRouteKey(item busItem) string {
	if item.RouteID != "" {
		return item.RouteID
	}
	if item.Route != "" {
		return item.Route
	}
	return strings.Join(busItemStopNames(item), " → ")
}

func busDepartureCampus(item busItem) string {
	if item.DepartureCampus != "" {
		return item.DepartureCampus
	}
	stops := busItemStopNames(item)
	if len(stops) > 0 {
		return stops[0]
	}
	return ""
}

func busRouteGroupRank(item busItem) int {
	stops := busItemStopNames(item)
	if hasBusStop(stops, "东区") && hasBusStop(stops, "高新区") {
		return 0
	}
	if hasBusStop(stops, "东区") && hasBusStop(stops, "西区") {
		return 1
	}
	if hasBusStop(stops, "高新区") && hasBusStop(stops, "先研院") {
		return 2
	}
	if hasBusStop(stops, "南区") {
		return 3
	}
	return 4
}

func busItemStopNames(item busItem) []string {
	if len(item.Stops) > 0 {
		stops := make([]string, 0, len(item.Stops))
		for _, stop := range item.Stops {
			if stop.Name != "" {
				stops = append(stops, stop.Name)
			}
		}
		return stops
	}
	stops := []string{}
	for _, stop := range strings.Split(item.Route, "→") {
		stop = strings.TrimSpace(stop)
		if stop != "" {
			stops = append(stops, stop)
		}
	}
	if len(stops) == 0 {
		stops = append(stops, item.DepartureCampus, item.ArrivalCampus)
	}
	return stops
}

func hasBusStop(stops []string, target string) bool {
	for _, stop := range stops {
		if stop == target {
			return true
		}
	}
	return false
}

type busRoute struct {
	Name      string
	StopNames []string
}

func busRouteMap(raw any) map[string]busRoute {
	routes := lifedata.MapSlice(raw)
	out := make(map[string]busRoute, len(routes))
	for _, route := range routes {
		rawStops := lifedata.MapSlice(route["stops"])
		stops := make([]string, 0, len(rawStops))
		for _, stop := range rawStops {
			name := campusName(lifedata.FirstString(stop, "nameCn", "name", "namePrimary"))
			if name == "" {
				name = campusName(lifedata.NestedString(stop, "campus", "nameCn", "namePrimary", "name"))
			}
			if name != "" {
				stops = append(stops, name)
			}
		}
		id := lifedata.FirstString(route, "id")
		if id == "" {
			continue
		}
		out[id] = busRoute{
			Name:      lifedata.FirstString(route, "nameCn", "namePrimary", "name"),
			StopNames: stops,
		}
	}
	return out
}

func tripStopNames(trip map[string]any) []string {
	rawStops := lifedata.MapSlice(trip["stopTimes"])
	stops := make([]string, 0, len(rawStops))
	for _, stop := range rawStops {
		name := campusName(lifedata.FirstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, name)
		}
	}
	return stops
}

func busStops(trip map[string]any, routeStops []string, departureTime, arrivalTime string) []busStop {
	rawStops := lifedata.MapSlice(trip["stopTimes"])
	stops := make([]busStop, 0, len(rawStops))
	for _, stop := range rawStops {
		name := campusName(lifedata.FirstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, busStop{Name: name, Time: lifedata.FirstString(stop, "time")})
		}
	}
	if len(stops) > 0 {
		return stops
	}
	for i, name := range routeStops {
		stop := busStop{Name: name}
		if i == 0 {
			stop.Time = departureTime
		}
		if i == len(routeStops)-1 {
			stop.Time = arrivalTime
		}
		stops = append(stops, stop)
	}
	return stops
}

func busFilter(args []string) (string, string) {
	if len(args) == 1 {
		return campusName(args[0]), ""
	}
	if len(args) < 2 {
		return "", ""
	}
	switch normToken(args[0]) {
	case "to", "到":
		return "", campusName(args[1])
	}
	return campusName(args[0]), campusName(args[1])
}

func routeMatches(stops []string, from, to string) bool {
	if from == "" || to == "" {
		if from == "" {
			if to == "" {
				return true
			}
			for i, stop := range stops {
				if stop == to {
					return i > 0
				}
			}
			return false
		}
		for _, stop := range stops {
			if stop == from {
				return true
			}
		}
		return false
	}
	fromIndex := -1
	for i, stop := range stops {
		if stop == from && fromIndex == -1 {
			fromIndex = i
		}
		if stop == to && fromIndex >= 0 && i > fromIndex {
			return true
		}
	}
	return false
}

func busRouteLabel(route busRoute, stops []string) string {
	if len(stops) > 0 {
		return strings.Join(stops, " → ")
	}
	if route.Name != "" {
		return strings.ReplaceAll(route.Name, " -> ", " → ")
	}
	return "校车"
}

func firstStop(stops []busStop) string {
	if len(stops) == 0 {
		return ""
	}
	return stops[0].Name
}

func lastStop(stops []busStop) string {
	if len(stops) == 0 {
		return ""
	}
	return stops[len(stops)-1].Name
}

func campusRank(campus string) int {
	switch campus {
	case "东区":
		return 0
	case "西区":
		return 1
	case "中区":
		return 2
	case "北区":
		return 3
	case "南区":
		return 4
	case "高新区":
		return 5
	case "太湖路园区":
		return 6
	case "":
		return 99
	default:
		return 50
	}
}

func campusAliases() []string {
	return []string{
		"east campus", "west campus", "central campus", "north campus", "south campus",
		"taihu road campus", "taihu campus", "taihu",
		"太湖路园区", "太湖路校区", "太湖路", "太湖园区", "太湖校区", "太湖",
		"高新区", "高新园区", "高新",
		"先研院",
		"东区", "西区", "中区", "北区", "南区",
		"east", "west", "center", "central", "north", "south", "gx",
		"东", "西", "中", "北", "南",
	}
}

func campusName(value string) string {
	switch textutil.LowerTrim(value) {
	case "东", "东区", "east", "east campus":
		return "东区"
	case "西", "西区", "west", "west campus":
		return "西区"
	case "中", "中区", "center", "central", "central campus":
		return "中区"
	case "北", "北区", "north", "north campus":
		return "北区"
	case "南", "南区", "south", "south campus":
		return "南区"
	case "高新", "高新区", "高新园区", "gx":
		return "高新区"
	case "太湖", "太湖路", "太湖园区", "太湖校区", "太湖路园区", "太湖路校区", "taihu", "taihu campus", "taihu road campus":
		return "太湖路园区"
	case "先研院":
		return "先研院"
	}
	return strings.TrimSpace(value)
}

func busTime(value string, minutes int) string {
	if value != "" {
		return value
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

const busMissingTimePlaceholder = "———"
const busOverviewTripsPerRoute = 3
