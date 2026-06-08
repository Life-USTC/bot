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

func parseGroupBus(text string) (parsedCommand, bool) {
	raw := stripCQCodes(text)
	if raw == "" || !containsBusKeyword(raw) {
		return parsedCommand{}, false
	}
	return commandResult(raw, "bus", busArgsFromText(raw)), true
}

func containsBusKeyword(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "校车") || strings.Contains(lower, "班车") {
		return true
	}
	return textutil.IndexASCIIToken(lower, "xc") >= 0 || textutil.IndexASCIIToken(lower, "bus") >= 0
}

var cqCodeRE = regexp.MustCompile(`(?i)\[CQ:[^\]]+\]`)

func stripCQCodes(text string) string {
	return strings.TrimSpace(cqCodeRE.ReplaceAllString(text, " "))
}

func busArgsFromText(text string) []string {
	type match struct {
		index  int
		campus string
	}
	matches := make([]match, 0, 2)
	seen := map[string]bool{}
	lookupText := strings.ToLower(text)
	for _, alias := range campusAliases() {
		for _, index := range campusAliasIndices(lookupText, alias) {
			campus := campusName(alias)
			seenKey := campus + "\x00" + strconv.Itoa(index)
			if campus != "" && !seen[seenKey] {
				matches = append(matches, match{index: index, campus: campus})
				seen[seenKey] = true
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].index < matches[j].index
	})
	args := make([]string, 0, 2)
	for _, match := range matches {
		if len(args) >= 2 {
			break
		}
		args = append(args, match.campus)
	}
	if len(args) == 1 && hasDestinationMarkerBefore(lookupText, matches[0].index) {
		return []string{"到", args[0]}
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
	options := queryOptions
	if !store.IsGroupConversation(ident) {
		preferences, ok := h.currentBusPreferences(ctx, ident)
		if ok {
			if options.ExplicitRoute {
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
	var items []busItem
	if options.ExplicitRoute {
		items = nextBusItemsWithOptions(data, routeArgs, now, options)
	} else {
		items = nextBusByRouteWithOptions(data, routeArgs, now, options)
	}
	if len(items) == 0 {
		return "今天后面没查到校车。"
	}
	if options.ExplicitRoute {
		return strings.Join(formatBusItemsAsStopTimeTable(items), "\n")
	}
	return strings.Join(formatBusItemsByRouteGroup(items, 0), "\n")
}

func busHelp() string {
	return strings.Join([]string{
		"可以直接发：",
		"校车",
		"校车 全部",
		"校车 我的路线",
		"xc 东区 西区",
		"校车 偏好",
		"校车 设置 东区 西区",
		"校车 已发车 开",
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
	update, shouldSave, message := parseBusPreferenceUpdate(data, args, preferences)
	if message != "" {
		return message
	}
	if !shouldSave {
		return formatBusPreferences(data, preferences, "校车偏好：")
	}
	preferences, err = auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (life.BusPreferences, error) {
		return h.Life.SetBusPreferences(ctx, token, update)
	})
	if err != nil {
		return commandError("校车偏好保存失败：", err)
	}
	return formatBusPreferences(data, preferences, "已更新校车偏好：")
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

func busPreferenceArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch normToken(args[0]) {
	case "preference", "preferences", "pref", "prefs", "偏好", "默认", "设置", "set", "已发车", "已出发", "show-departed", "departed":
		return true
	default:
		return false
	}
}

func busQueryArgs(args []string, now time.Time) ([]string, busQueryOptions) {
	out := make([]string, 0, len(args))
	options := busQueryOptions{}
	for i := 0; i < len(args); i++ {
		switch normToken(args[i]) {
		case "all", "全部", "所有":
			continue
		case "preferred", "preference-route", "pref-route", "我的路线", "偏好路线", "默认路线":
			options.UsePreferredRoute = true
			continue
		case "after", "之后", "以后":
			if i+1 < len(args) {
				if after, ok := parseBusAfterTime(args[i+1], now); ok {
					options.Now = after
					options.After = true
					i++
					continue
				}
				if i+2 < len(args) {
					if after, ok := parseBusAfterTime(args[i+1]+" "+args[i+2], now); ok {
						options.Now = after
						options.After = true
						i += 2
						continue
					}
				}
			}
		}
		out = append(out, args[i])
	}
	options.ExplicitRoute = len(busCampusesFromArgs(out)) >= 2
	return out, options
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

func formatBusPreferences(data map[string]any, preferences life.BusPreferences, title string) string {
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
	return strings.Join([]string{
		title,
		"路线：" + route,
		"已发车：" + showDeparted,
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
	case "东区", "西区", "中区", "北区", "南区", "高新区", "先研院":
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
}

type busStop struct {
	Name string
	Time string
}

type busQueryOptions struct {
	ShowDeparted      bool
	Now               time.Time
	After             bool
	UsePreferredRoute bool
	ExplicitRoute     bool
}

func nextBusItems(data map[string]any, args []string, now time.Time) []busItem {
	return nextBusItemsWithOptions(data, args, now, busQueryOptions{})
}

func nextBusItemsWithOptions(data map[string]any, args []string, now time.Time, options busQueryOptions) []busItem {
	if !options.Now.IsZero() {
		now = options.Now
	}
	now = now.In(lifedata.ChinaLocation())
	from, to := busFilter(args)
	dayType := "weekday"
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		dayType = "weekend"
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
		if !routeMatches(routeStops, from, to) {
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

func nextBusByRoute(data map[string]any, args []string, now time.Time) []busItem {
	return nextBusByRouteWithOptions(data, args, now, busQueryOptions{})
}

func nextBusByRouteWithOptions(data map[string]any, args []string, now time.Time, options busQueryOptions) []busItem {
	items := nextBusItemsWithOptions(data, args, now, options)
	byRoute := make(map[string]busItem)
	for _, item := range items {
		key := item.RouteID
		if key == "" {
			key = item.Route
		}
		if _, exists := byRoute[key]; !exists {
			byRoute[key] = item
		}
	}
	out := make([]busItem, 0, len(byRoute))
	for _, item := range byRoute {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if campusRank(out[i].DepartureCampus) != campusRank(out[j].DepartureCampus) {
			return campusRank(out[i].DepartureCampus) < campusRank(out[j].DepartureCampus)
		}
		if out[i].DepartureMinutes == out[j].DepartureMinutes {
			return out[i].Route < out[j].Route
		}
		return out[i].DepartureMinutes < out[j].DepartureMinutes
	})
	return out
}

func formatBusItemsByRouteGroup(items []busItem, limit int) []string {
	items = sortedBusItemsByRouteGroup(items)
	lines := make([]string, 0, len(items)+4)
	lastGroup := -1
	for i, item := range items {
		if limit > 0 && i >= limit {
			break
		}
		group := busRouteGroupRank(item)
		if group != lastGroup {
			if lastGroup != -1 {
				lines = append(lines, "")
			}
			lastGroup = group
		}
		lines = append(lines, formatBusItem(item))
	}
	return lines
}

func formatBusItemsAsStopTimeTable(items []busItem) []string {
	stops := busTableStops(items)
	if len(stops) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, strings.Join(stops, "\t"))
	for _, item := range items {
		times := busStopTimes(item)
		row := make([]string, 0, len(stops))
		for _, stop := range stops {
			timeText := busMissingTimePlaceholder
			if stopTime := times[stop]; stopTime != "" {
				timeText = textutil.MonospaceDigits(stopTime)
			}
			row = append(row, timeText)
		}
		lines = append(lines, strings.Join(row, "\t"))
	}
	return lines
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
		if campusRank(out[i].DepartureCampus) != campusRank(out[j].DepartureCampus) {
			return campusRank(out[i].DepartureCampus) < campusRank(out[j].DepartureCampus)
		}
		if out[i].DepartureMinutes == out[j].DepartureMinutes {
			return out[i].Route < out[j].Route
		}
		return out[i].DepartureMinutes < out[j].DepartureMinutes
	})
	return out
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

func formatBusItem(item busItem) string {
	if len(item.Stops) > 0 {
		parts := make([]string, 0, len(item.Stops))
		for _, stop := range item.Stops {
			parts = append(parts, formatBusStop(stop))
		}
		return strings.Join(parts, "  →  ")
	}
	line := strings.ReplaceAll(item.Route, " -> ", " → ") + "：" + textutil.MonospaceDigits(item.DepartureTime)
	if item.Arrival != "" {
		line += "（到 " + textutil.MonospaceDigits(item.Arrival) + "）"
	}
	return line
}

func formatBusStop(stop busStop) string {
	name := textutil.PadRightDisplayWide(stop.Name, busStopNameColumnWidth)
	timeText := busMissingTimePlaceholder
	if stop.Time != "" {
		timeText = textutil.MonospaceDigits(stop.Time)
	}
	return name + " " + timeText
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
	case "":
		return 99
	default:
		return 50
	}
}

func campusAliases() []string {
	return []string{
		"east campus", "west campus", "central campus", "north campus", "south campus",
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

const busStopNameColumnWidth = 3
const busMissingTimePlaceholder = "———"
