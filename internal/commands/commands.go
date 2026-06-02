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
	"github.com/Life-USTC/Bot/internal/store"
)

type Handler struct {
	Life   *life.Client
	Auth   *auth.Manager
	Store  *store.Store
	Prefix string
}

type Input struct {
	Text     string
	Identity store.Identity
}

func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	cmd, ok := h.parse(input.Text)
	if !ok {
		return "", false
	}
	h.recordState(ctx, input.Identity, cmd)
	var reply string
	if cmd.Name == "help" {
		reply = h.help()
		h.recordInteraction(ctx, input.Identity, cmd, reply)
		return reply, true
	}

	switch cmd.Name {
	case "login":
		reply = h.login(ctx, input.Identity, cmd.Args)
	case "logout":
		reply = h.logout(ctx, input.Identity)
	case "me":
		reply = h.me(ctx, input.Identity)
	case "todo":
		reply = h.todo(ctx, input.Identity, cmd.Args)
	case "sub", "subs", "subscription":
		reply = h.subscription(ctx, input.Identity)
	case "ping":
		if err := h.Life.Health(ctx); err != nil {
			reply = "Life @ USTC API unavailable: " + err.Error()
			break
		}
		reply = "Life @ USTC API is reachable."
	case "status":
		reply = h.status(ctx, input.Identity)
	case "semester":
		reply = h.currentSemester(ctx)
	case "course":
		reply = h.searchCourses(ctx, strings.Join(cmd.Args, " "))
	case "section":
		reply = h.searchSections(ctx, strings.Join(cmd.Args, " "))
	case "bus":
		reply = h.bus(ctx, cmd.Args)
	case "schedule":
		reply = h.subscription(ctx, input.Identity)
	default:
		reply = h.help()
	}
	h.recordInteraction(ctx, input.Identity, cmd, reply)
	return reply, true
}

type parsedCommand struct {
	Name string
	Args []string
	Raw  string
}

func (h Handler) parse(text string) (parsedCommand, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return parsedCommand{}, false
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return parsedCommand{}, false
	}
	prefix := h.Prefix
	if prefix == "" {
		prefix = "/life"
	}

	if fields[0] == prefix {
		if len(fields) == 1 {
			return parsedCommand{Name: "help", Raw: raw}, true
		}
		name, args := normalizeCommand(fields[1], fields[2:])
		return parsedCommand{Name: name, Args: args, Raw: raw}, true
	}

	if fields[0] == "/help" || fields[0] == "/?" || fields[0] == "help" || fields[0] == "帮助" || fields[0] == "？" {
		return parsedCommand{Name: "help", Raw: raw}, true
	}

	name, args := normalizeCommand(fields[0], fields[1:])
	if name == "" {
		return parsedCommand{}, false
	}
	return parsedCommand{Name: name, Args: args, Raw: raw}, true
}

func normalizeCommand(name string, args []string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "-h", "--help", "help", "?", "？", "帮助", "菜单":
		return "help", args
	case "p", "ping":
		return "ping", args
	case "status", "zt", "状态":
		return "status", args
	case "login", "登录", "dl":
		return "login", normalizeLoginArgs(args)
	case "logout", "退出", "登出":
		return "logout", args
	case "me", "我", "我的", "profile", "个人":
		return "me", args
	case "todo", "td", "待办", "代办", "todo待办":
		return "todo", normalizeTodoArgs(args)
	case "bus", "xc", "校车", "车":
		return "bus", args
	case "schedule", "sched", "rc", "日程", "课表", "订阅", "sub", "subs", "subscription":
		return "schedule", args
	case "semester", "term", "学期", "xq":
		return "semester", args
	case "course", "kc", "课程":
		return "course", args
	case "section", "class", "bj", "教学班", "班级":
		return "section", args
	}
	return "", args
}

func normalizeLoginArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(args[0]) {
	case "status", "check", "完成", "状态", "ok", "好了":
		next := append([]string(nil), args...)
		next[0] = "status"
		return next
	}
	return args
}

func normalizeTodoArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(args[0]) {
	case "-h", "--help", "help", "?", "？", "帮助":
		next := append([]string(nil), args...)
		next[0] = "help"
		return next
	case "add", "new", "create", "+", "添加", "新增", "加":
		next := append([]string(nil), args...)
		next[0] = "add"
		return next
	case "done", "finish", "complete", "ok", "x", "完成", "好了":
		next := append([]string(nil), args...)
		next[0] = "done"
		return next
	}
	return args
}

func (h Handler) help() string {
	return strings.Join([]string{
		"可以直接发：",
		"待办 / td",
		"td 写报告",
		"td done 1",
		"校车 / xc",
		"xc 东区 西区",
		"日程 / rc",
		"状态 / status",
		"我 / me",
		"课程 数学分析",
		"教学班 高等数学",
		"登录 / 登录 状态",
	}, "\n")
}

func (h Handler) login(ctx context.Context, ident store.Identity, args []string) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if len(args) > 0 && args[0] == "status" {
		result, err := h.Auth.PollDeviceLogin(ctx, ident)
		if err != nil {
			return "登录状态查不到：" + friendlyError(err)
		}
		return result.Message
	}
	session, err := h.Auth.BeginDeviceLogin(ctx, ident)
	if err != nil {
		return "登录开始失败：" + friendlyError(err)
	}
	link := session.VerificationURIComplete
	if link == "" {
		link = session.VerificationURI
	}
	return strings.Join([]string{
		"打开链接登录 Life @ USTC：",
		link,
		"验证码：" + session.UserCode,
		"登录完发：登录 状态",
	}, "\n")
}

func (h Handler) logout(ctx context.Context, ident store.Identity) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if err := h.Auth.Logout(ctx, ident); err != nil {
		return "退出失败：" + friendlyError(err)
	}
	return "已退出登录。"
}

func (h Handler) me(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	me, err := h.Life.Me(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			me, err = h.Life.Me(ctx, token)
		}
	}
	if err != nil {
		return "个人信息查不到：" + friendlyError(err)
	}
	name := firstString(me, "name", "username", "preferred_username", "email")
	if name == "" {
		name = firstString(me, "id", "sub")
	}
	return "已登录：" + name
}

func (h Handler) todo(ctx context.Context, ident store.Identity, args []string) string {
	if len(args) > 0 && args[0] == "help" {
		return strings.Join([]string{
			"待办用法：",
			"待办",
			"td",
			"待办 add 写报告",
			"td 写报告",
			"td + 买咖啡",
			"td done 1",
		}, "\n")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if len(args) > 0 && args[0] == "add" {
		title := strings.TrimSpace(strings.Join(args[1:], " "))
		if title == "" {
			return "想加什么？例如：待办 add 写报告"
		}
		return h.createTodo(ctx, ident, token, title)
	}
	if len(args) > 0 && args[0] == "done" {
		target := strings.TrimSpace(strings.Join(args[1:], " "))
		if target == "" {
			return "想完成哪条？例如：td done 1"
		}
		todos, err := h.pendingTodos(ctx, ident, token)
		if err != nil {
			return "待办查不到：" + friendlyError(err)
		}
		todo, ok := resolveTodo(todos, target)
		if !ok {
			return "没找到这条待办。发 td 看编号，再试：td done 1"
		}
		id := firstString(todo, "id")
		if id == "" {
			id = fmt.Sprint(todo["id"])
		}
		if id == "" || id == "<nil>" {
			return "这条待办没有可用 ID，暂时完成不了。"
		}
		err = h.Life.CompleteTodo(ctx, token, id)
		if err != nil && strings.Contains(err.Error(), " returned 401:") {
			token, refreshErr := h.Auth.Refresh(ctx, ident)
			if refreshErr == nil {
				err = h.Life.CompleteTodo(ctx, token, id)
			}
		}
		if err != nil {
			return "待办完成失败：" + friendlyError(err)
		}
		title := firstString(todo, "title")
		if title == "" {
			return "已完成。"
		}
		return "已完成：" + title
	}
	if len(args) > 0 {
		return h.createTodo(ctx, ident, token, strings.Join(args, " "))
	}
	todos, err := h.pendingTodos(ctx, ident, token)
	if err != nil {
		return "待办查不到：" + friendlyError(err)
	}
	if len(todos) == 0 {
		return "没有待办。"
	}
	lines := []string{"待办："}
	for i, todo := range todos {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(todos)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, firstString(todo, "title")))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) createTodo(ctx context.Context, ident store.Identity, token, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "想加什么？例如：td 写报告"
	}
	created, err := h.Life.CreateTodo(ctx, token, title)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			created, err = h.Life.CreateTodo(ctx, token, title)
		}
	}
	if err != nil {
		return "待办添加失败：" + friendlyError(err)
	}
	id := firstString(created, "id")
	if id == "" {
		id = fmt.Sprint(created["id"])
	}
	if id != "" {
		return "已加待办：" + title
	}
	return "已加待办"
}

func (h Handler) pendingTodos(ctx context.Context, ident store.Identity, token string) ([]map[string]any, error) {
	todos, err := h.Life.Todos(ctx, token, "false")
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			todos, err = h.Life.Todos(ctx, token, "false")
		}
	}
	return todos, err
}

func resolveTodo(todos []map[string]any, target string) (map[string]any, bool) {
	target = strings.TrimSpace(target)
	if index, err := strconv.Atoi(target); err == nil && index >= 1 && index <= len(todos) {
		return todos[index-1], true
	}
	needle := strings.ToLower(target)
	for _, todo := range todos {
		id := strings.ToLower(firstString(todo, "id"))
		title := strings.ToLower(firstString(todo, "title"))
		if needle == id || needle == title || strings.Contains(title, needle) {
			return todo, true
		}
	}
	return nil, false
}

func (h Handler) subscription(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := h.Life.CurrentSubscription(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			data, err = h.Life.CurrentSubscription(ctx, token)
		}
	}
	if err != nil {
		return "日程查不到：" + friendlyError(err)
	}
	sub, _ := data["subscription"].(map[string]any)
	sections, _ := sub["sections"].([]any)
	if len(sections) == 0 {
		return "还没有订阅课程。"
	}
	lines := []string{"日程订阅："}
	for i, item := range sections {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(sections)-i))
			break
		}
		section, _ := item.(map[string]any)
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) accessToken(ctx context.Context, ident store.Identity) (string, bool) {
	if h.Auth == nil {
		return "", false
	}
	token, err := h.Auth.AccessToken(ctx, ident)
	if err == nil {
		return token, true
	}
	return "", !errors.Is(err, auth.ErrNotLoggedIn) && false
}

func (h Handler) loginRequired() string {
	return "这个需要先登录。发：登录"
}

func (h Handler) recordState(ctx context.Context, ident store.Identity, cmd parsedCommand) {
	if h.Store == nil || ident.Platform == "" || ident.UserID == "" {
		return
	}
	_ = h.Store.RecordConversationState(ctx, ident, cmd.Name, strconv.Quote(cmd.Raw))
}

func (h Handler) recordInteraction(ctx context.Context, ident store.Identity, cmd parsedCommand, reply string) {
	if h.Store == nil || ident.Platform == "" || ident.UserID == "" {
		return
	}
	_ = h.Store.RecordInteraction(ctx, ident, store.Interaction{
		RawText: cmd.Raw,
		Command: cmd.Name,
		Args:    strings.Join(cmd.Args, " "),
		Handled: true,
		Reply:   reply,
	})
}

func (h Handler) currentSemester(ctx context.Context) string {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return "学期查不到：" + friendlyError(err)
	}
	name := firstString(semester, "name", "nameCn", "namePrimary")
	if name == "" {
		name = fmt.Sprint(semester["id"])
	}
	return "当前学期：" + name
}

func (h Handler) searchCourses(ctx context.Context, keyword string) string {
	if keyword == "" {
		return "想查哪门课？例如：课程 数学分析"
	}
	courses, err := h.Life.SearchCourses(ctx, keyword, 5)
	if err != nil {
		return "课程查不到：" + friendlyError(err)
	}
	if len(courses) == 0 {
		return "没找到课程。"
	}
	lines := []string{"课程："}
	for _, course := range courses {
		lines = append(lines, formatCourse(course))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchSections(ctx context.Context, keyword string) string {
	if keyword == "" {
		return "想查哪个教学班？例如：教学班 高等数学"
	}
	sections, err := h.Life.SearchSections(ctx, keyword, 5)
	if err != nil {
		return "教学班查不到：" + friendlyError(err)
	}
	if len(sections) == 0 {
		return "没找到教学班。"
	}
	lines := []string{"教学班："}
	for _, section := range sections {
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) status(ctx context.Context, ident store.Identity) string {
	api := "OK"
	if err := h.Life.Health(ctx); err != nil {
		api = friendlyError(err)
	}
	login := "未登录"
	if h.Auth != nil {
		if _, err := h.Auth.AccessToken(ctx, ident); err == nil {
			login = "已登录"
		}
	}
	return strings.Join([]string{
		"状态：",
		"Life API：" + api,
		"登录：" + login,
	}, "\n")
}

func (h Handler) bus(ctx context.Context, args []string) string {
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return "校车查不到：" + friendlyError(err)
	}
	items := nextBusByRoute(data, args, time.Now())
	if len(items) == 0 {
		return "今天后面没查到校车。"
	}
	title := "各路线下一班校车："
	from, to := busFilter(args)
	if from != "" && to != "" {
		title = from + " → " + to + " 各路线下一班："
	} else if from != "" {
		title = from + " 相关路线下一班："
	}
	lines := append([]string{title}, formatBusItemsByDepartureCampus(items, 8)...)
	return strings.Join(lines, "\n")
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
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		now = now.In(loc)
	}
	from, to := busFilter(args)
	dayType := "weekday"
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		dayType = "weekend"
	}
	nowMinutes := now.Hour()*60 + now.Minute()
	routes := busRouteMap(data["routes"])
	trips, _ := data["trips"].([]any)
	items := make([]busItem, 0, len(trips))
	for _, raw := range trips {
		trip, _ := raw.(map[string]any)
		if trip == nil || firstString(trip, "dayType") != dayType {
			continue
		}
		departure := intNumber(trip["departureMinutes"])
		if departure < nowMinutes {
			continue
		}
		route := routes[firstString(trip, "routeId")]
		routeStops := route.StopNames
		if len(routeStops) == 0 {
			routeStops = tripStopNames(trip)
		}
		if !routeMatches(routeStops, from, to) {
			continue
		}
		routeID := firstString(trip, "routeId")
		stops := busStops(trip, routeStops, busTime(firstString(trip, "departureTime"), departure), firstString(trip, "arrivalTime"))
		items = append(items, busItem{
			RouteID:          routeID,
			DepartureCampus:  firstStop(stops),
			ArrivalCampus:    lastStop(stops),
			Stops:            stops,
			DepartureMinutes: departure,
			DepartureTime:    busTime(firstString(trip, "departureTime"), departure),
			Arrival:          firstString(trip, "arrivalTime"),
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
		if i >= limit {
			break
		}
		campus := item.DepartureCampus
		if campus == "" {
			campus = "其他"
		}
		if campus != lastCampus {
			lines = append(lines, campus+"：")
			lastCampus = campus
		}
		lines = append(lines, "  "+formatBusItem(item))
	}
	return lines
}

func formatBusItem(item busItem) string {
	if len(item.Stops) > 0 {
		parts := make([]string, 0, len(item.Stops))
		for _, stop := range item.Stops {
			part := stop.Name
			if stop.Time != "" {
				part += " " + stop.Time
			}
			parts = append(parts, fullwidthDigits(part))
		}
		return strings.Join(parts, " → ")
	}
	line := strings.ReplaceAll(item.Route, " -> ", " → ") + "：" + item.DepartureTime
	if item.Arrival != "" {
		line += "（到 " + item.Arrival + "）"
	}
	return fullwidthDigits(line)
}

type busRoute struct {
	Name      string
	StopNames []string
}

func busRouteMap(raw any) map[string]busRoute {
	out := map[string]busRoute{}
	routes, _ := raw.([]any)
	for _, item := range routes {
		route, _ := item.(map[string]any)
		if route == nil {
			continue
		}
		stops := make([]string, 0)
		for _, rawStop := range anySlice(route["stops"]) {
			stop, _ := rawStop.(map[string]any)
			if stop == nil {
				continue
			}
			name := campusName(firstString(stop, "nameCn", "name", "namePrimary"))
			if name == "" {
				name = campusName(nestedString(stop, "campus", "nameCn", "namePrimary", "name"))
			}
			if name != "" {
				stops = append(stops, name)
			}
		}
		out[firstString(route, "id")] = busRoute{
			Name:      firstString(route, "nameCn", "namePrimary", "name"),
			StopNames: stops,
		}
	}
	return out
}

func tripStopNames(trip map[string]any) []string {
	stops := make([]string, 0)
	for _, rawStop := range anySlice(trip["stopTimes"]) {
		stop, _ := rawStop.(map[string]any)
		if stop == nil {
			continue
		}
		name := campusName(firstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, name)
		}
	}
	return stops
}

func busStops(trip map[string]any, routeStops []string, departureTime, arrivalTime string) []busStop {
	stops := make([]busStop, 0)
	for _, rawStop := range anySlice(trip["stopTimes"]) {
		stop, _ := rawStop.(map[string]any)
		if stop == nil {
			continue
		}
		name := campusName(firstString(stop, "campusName", "stopName", "nameCn", "name"))
		if name != "" {
			stops = append(stops, busStop{Name: name, Time: firstString(stop, "time")})
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
	return campusName(args[0]), campusName(args[1])
}

func routeMatches(stops []string, from, to string) bool {
	if from == "" || to == "" {
		if from == "" {
			return true
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

func fullwidthDigits(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '０' + (r - '0')
		}
		return r
	}, text)
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

func campusName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
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
	}
	return strings.TrimSpace(value)
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func intNumber(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		parsed, _ := strconv.Atoi(n)
		return parsed
	}
	return 0
}

func busTime(value string, minutes int) string {
	if value != "" {
		return value
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func formatCourse(course map[string]any) string {
	code := firstString(course, "code")
	name := firstString(course, "namePrimary", "nameCn", "name")
	if code == "" {
		return "- " + name
	}
	return "- " + code + " " + name
}

func formatSection(section map[string]any) string {
	code := firstString(section, "code")
	course := nestedString(section, "course", "namePrimary", "nameCn", "name")
	semester := nestedString(section, "semester", "name")
	parts := []string{code, course, semester}
	return "- " + strings.Join(nonEmpty(parts), " ")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := m[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case float64:
			return strconv.FormatInt(int64(value), 10)
		case int:
			return strconv.Itoa(value)
		}
	}
	return ""
}

func nestedString(m map[string]any, key string, nestedKeys ...string) string {
	child, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	return firstString(child, nestedKeys...)
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func friendlyError(err error) string {
	text := err.Error()
	if strings.Contains(text, " returned 401:") || strings.Contains(strings.ToLower(text), "unauthorized") {
		return "登录过期了，发：登录"
	}
	if strings.Contains(strings.ToLower(text), "timeout") {
		return "网络超时，等会儿再试"
	}
	return text
}
