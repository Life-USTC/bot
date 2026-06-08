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

	"github.com/Life-USTC/Bot/internal/lifedata"
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

func (h Handler) bus(ctx context.Context, args []string) string {
	return h.busAt(ctx, args, time.Now())
}

func (h Handler) busAt(ctx context.Context, args []string, now time.Time) string {
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return commandError("校车查不到：", err)
	}
	items := nextBusByRoute(data, args, now)
	if len(items) == 0 {
		return "今天后面没查到校车。"
	}
	return strings.Join(formatBusItemsByDepartureCampus(items, 0), "\n")
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

func nextBusItems(data map[string]any, args []string, now time.Time) []busItem {
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
		if departure < nowMinutes {
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
	items := nextBusItems(data, args, now)
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

func formatBusItemsByDepartureCampus(items []busItem, limit int) []string {
	lines := make([]string, 0, len(items)+4)
	lastCampus := ""
	for i, item := range items {
		if limit > 0 && i >= limit {
			break
		}
		campus := item.DepartureCampus
		if campus == "" {
			campus = "其他"
		}
		if campus != lastCampus {
			if lastCampus != "" {
				lines = append(lines, "")
			}
			lastCampus = campus
		}
		lines = append(lines, formatBusItem(item))
	}
	return lines
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
