package commands

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/textutil"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

// ResponseTextOrigin identifies who authored visible response text. The zero
// value is deliberately treated as host-authored by presentation code, so a
// synthetic prompt or error cannot accidentally inherit the text permission
// of a model response when responses are combined.
type ResponseTextOrigin string

const (
	ResponseTextOriginSynthetic ResponseTextOrigin = "synthetic"
	ResponseTextOriginLLM       ResponseTextOrigin = "llm"
)

type Response struct {
	Text       string
	TextOrigin ResponseTextOrigin
	// Data is the machine-readable result of the command. It deliberately
	// lives beside Text and Image: Text is presentation for a user, while Data
	// is the already-fetched domain value sent to a model or another host.
	Images []toolresult.ImageReference
	Data   any
	Image  *responses.Image
	Kind   string
	Parts  []Response
}

// ModelResult returns the stable model-facing envelope for this response.
// Callers that already have a shared result encoder may use Response.Data
// directly; this convenience keeps command-only integrations from having to
// reconstruct the envelope from rendered text.
func (r Response) ModelResult(operation, status string, observedAt time.Time) string {
	var err error
	if status != "success" && status != "succeeded" {
		err = errors.New(r.Text)
	}
	return toolresult.Encode("bot", operation, status, observedAt, r.Data, err, r.Images...)
}

const ResponseKindAuthWait = "auth_wait"

func textResponse(text string) Response {
	return Response{Text: text}
}

// helpImageTopic returns the help topic to render as an image for this command, if any.
func helpImageTopic(cmd Invocation) (string, bool) {
	if firstArgIsHelp(cmd.Args) {
		if topic := helpTopicCommand([]string{cmd.Name}); topic != "" {
			return topic, true
		}
		return cmd.Name, true
	}
	// Root commands whose empty-arg reply is the topic help card.
	switch cmd.Name {
	case "account", "feedback", "subscription":
		if !hasArgs(cmd.Args) {
			if topic := helpTopicCommand([]string{cmd.Name}); topic != "" {
				return topic, true
			}
		}
	}
	return "", false
}

func (h Handler) imageResponseFor(cmd Invocation, text string) *responses.Image {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !successfulImageText(text) {
		return nil
	}
	return h.imageResponseForText(cmd, text)
}

// imageResponseForOutcome trusts the explicit capability outcome rather than
// trying to infer command state from the domain text.
func (h Handler) imageResponseForOutcome(cmd Invocation, outcome CapabilityOutcome) *responses.Image {
	if outcome.Status != CapabilityOutcomeSuccess {
		return nil
	}
	if strings.TrimSpace(outcome.Response.Text) == "" {
		return nil
	}
	if !successfulImageText(outcome.Response.Text) {
		return nil
	}
	return h.imageResponseForText(cmd, outcome.Response.Text)
}

func (h Handler) imageResponseForText(cmd Invocation, text string) *responses.Image {
	imageText := imageRenderText(text)
	if cmd.Name == "help" {
		return responses.NewRichTextImage("help", helpRichText(cmd.Args...), imageText)
	}
	if topic, ok := helpImageTopic(cmd); ok {
		rich := helpRichText(topic)
		if strings.TrimSpace(rich) != "" {
			return responses.NewRichTextImage("help", rich, imageText)
		}
	}
	switch cmd.Name {
	case "schedule":
		plainText := textutil.PlainMonospace(text)
		title := imageTitle(plainText, "课表")
		if grid := weeklyScheduleGrid(plainText); grid != nil {
			if grid.DateRange != "" {
				title = "周课表"
			}
			return responses.NewScheduleGridImage("schedule", title, grid, imageText)
		}
		if firstArgIn(cmd.Args, "today", "tomorrow") {
			if grid := dailyScheduleGrid(plainText); grid != nil {
				return responses.NewScheduleGridImage("schedule", title, grid, imageText)
			}
		}
		return responses.NewRichTextImage("schedule", scheduleRichText(title, plainText), imageText)
	case "todo":
		if !todoImageArgs(cmd.Args) {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		return richTextImage("todo", imageTitle(plainText, "待办"), plainText)
	case "homework":
		if !homeworkImageArgs(cmd.Args) {
			return nil
		}
		plainText := textutil.PlainMonospace(text)
		return richTextImage("homework", imageTitle(plainText, "作业"), plainText)
	case "section_homeworks":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("homework", imageTitle(plainText, "作业"), plainText)
	case "exam", "section_exams":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("exam", imageTitle(plainText, "考试"), plainText)
	case "nextclass":
		plainText := textutil.PlainMonospace(text)
		title := imageTitle(plainText, "下一节课")
		return responses.NewRichTextImage("nextclass", scheduleRichText(title, plainText), imageText)
	case "calendar":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("calendar", imageTitle(plainText, "日程"), plainText)
	case "overview":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("overview", imageTitle(plainText, "我的概览"), plainText)
	case "upcoming_deadlines":
		plainText := textutil.PlainMonospace(text)
		return richTextImage("deadlines", imageTitle(plainText, "近期截止"), plainText)
	default:
		return nil
	}
}

func scheduleRichText(title, text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(lines[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			lines = lines[1:]
		}
	}

	out := []string{"# " + strings.TrimSpace(title), ""}
	atSectionStart := true
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			inTable = false
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			heading := strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(heading))
			atSectionStart = false
			inTable = false
			continue
		}
		if cells, ok := scheduleRichTableCells(line); ok {
			if !inTable {
				out = append(out,
					markdownRichTableRow([]string{"节次", "时间", "安排", "备注"}),
					markdownRichTableRow([]string{"---", "---", "---", "---"}),
				)
			}
			out = append(out, markdownRichTableRow(cells))
			inTable = true
		} else {
			out = append(out, imageRenderText(line))
			inTable = false
		}
		atSectionStart = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func scheduleRichTableCells(line string) ([]string, bool) {
	columns := strings.Split(line, "\t")
	if len(columns) < 3 {
		return nil, false
	}
	place := strings.TrimSpace(columns[0])
	timeRange := strings.TrimSpace(columns[1])
	course := strings.TrimSpace(strings.Join(columns[2:], " "))
	if timeRange == "" || course == "" {
		return nil, false
	}
	campus, room := scheduleCampusAndRoom(place)
	return []string{schedulePeriodLabel(timeRange), timeRange, course, scheduleLocationNote(campus, room)}, true
}

type lessonPeriod struct {
	start int
	end   int
}

var ustcLessonPeriods = [...]lessonPeriod{
	{start: 7*60 + 50, end: 8*60 + 35},
	{start: 8*60 + 40, end: 9*60 + 25},
	{start: 9*60 + 45, end: 10*60 + 30},
	{start: 10*60 + 35, end: 11*60 + 20},
	{start: 11*60 + 25, end: 12*60 + 10},
	{start: 14 * 60, end: 14*60 + 45},
	{start: 14*60 + 50, end: 15*60 + 35},
	{start: 15*60 + 55, end: 16*60 + 40},
	{start: 16*60 + 45, end: 17*60 + 30},
	{start: 17*60 + 35, end: 18*60 + 20},
	{start: 19*60 + 30, end: 20*60 + 15},
	{start: 20*60 + 20, end: 21*60 + 5},
	{start: 21*60 + 10, end: 21*60 + 55},
}

func schedulePeriodLabel(timeRange string) string {
	start, end, ok := schedulePeriodRange(timeRange)
	if !ok {
		return "—"
	}
	if start == end {
		return "第 " + strconv.Itoa(start) + " 小节"
	}
	return "第 " + strconv.Itoa(start) + "–" + strconv.Itoa(end) + " 小节"
}

func schedulePeriodRange(timeRange string) (int, int, bool) {
	normalized := strings.NewReplacer("～", "-", "~", "-", "–", "-", "—", "-").Replace(strings.TrimSpace(timeRange))
	parts := strings.SplitN(normalized, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, startOK := closestLessonPeriod(parts[0], true)
	end, endOK := closestLessonPeriod(parts[1], false)
	if !startOK || !endOK || end < start {
		return 0, 0, false
	}
	return start, end, true
}

func closestLessonPeriod(value string, useStart bool) (int, bool) {
	minutes, ok := clockMinutes(value)
	if !ok {
		return 0, false
	}
	bestPeriod := 0
	bestDifference := 11
	for i, period := range ustcLessonPeriods {
		target := period.end
		if useStart {
			target = period.start
		}
		difference := minutes - target
		if difference < 0 {
			difference = -difference
		}
		if difference < bestDifference {
			bestPeriod = i + 1
			bestDifference = difference
		}
	}
	return bestPeriod, bestPeriod > 0 && bestDifference <= 10
}

func clockMinutes(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func scheduleLocationNote(campus, room string) string {
	switch {
	case campus != "" && room != "":
		return campus + " · " + room
	case campus != "":
		return campus
	case room != "":
		return room
	default:
		return "—"
	}
}

var weeklyScheduleDayLabels = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

var scheduleGridSemesterPattern = regexp.MustCompile(`^(\d{4})年?(春|秋)(季)?学期?$`)

type scheduleGridMetadata struct {
	semester  string
	week      string
	dateRange string
}

const scheduleGridMetadataPrefix = "课表信息："

func formatScheduleGridMetadata(metadata scheduleGridMetadata) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(metadata.semester); value != "" {
		parts = append(parts, "学期："+value)
	}
	if value := strings.TrimSpace(metadata.week); value != "" {
		parts = append(parts, "教学周："+value)
	}
	if value := strings.TrimSpace(metadata.dateRange); value != "" {
		parts = append(parts, "日期："+value)
	}
	if len(parts) == 0 {
		return ""
	}
	return scheduleGridMetadataPrefix + strings.Join(parts, " · ")
}

func parseScheduleGridMetadata(line string) (scheduleGridMetadata, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, scheduleGridMetadataPrefix) {
		return scheduleGridMetadata{}, false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, scheduleGridMetadataPrefix))
	if line == "" {
		return scheduleGridMetadata{}, true
	}
	metadata := scheduleGridMetadata{}
	for _, part := range strings.Split(line, " · ") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "学期："):
			metadata.semester = strings.TrimSpace(strings.TrimPrefix(part, "学期："))
		case strings.HasPrefix(part, "教学周："):
			metadata.week = strings.TrimSpace(strings.TrimPrefix(part, "教学周："))
		case strings.HasPrefix(part, "日期："):
			metadata.dateRange = strings.TrimSpace(strings.TrimPrefix(part, "日期："))
		}
	}
	return metadata, true
}

func scheduleGridDateRange(days []responses.ScheduleGridDay) string {
	first, last := "", ""
	for _, day := range days {
		date := strings.TrimSpace(day.Date)
		if date == "" {
			continue
		}
		if first == "" {
			first = date
		}
		last = date
	}
	if first == "" || last == "" {
		return ""
	}
	return strings.ReplaceAll(first, "-", "/") + "-" + strings.ReplaceAll(last, "-", "/")
}

func scheduleGridSemesterLabel(value string) string {
	value = strings.TrimSpace(value)
	match := scheduleGridSemesterPattern.FindStringSubmatch(strings.Join(strings.Fields(value), ""))
	if len(match) != 4 {
		return value
	}
	return match[1] + " " + match[2] + "季学期"
}

func weeklyScheduleGrid(text string) *responses.ScheduleGrid {
	days := make([]responses.ScheduleGridDay, 0, len(weeklyScheduleDayLabels))
	items := []responses.ScheduleGridItem{}
	metadata := scheduleGridMetadata{}
	currentDay := -1
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == calendarSubscriptionHint {
			continue
		}
		if parsed, ok := parseScheduleGridMetadata(trimmed); ok {
			metadata = parsed
			continue
		}
		if day, ok := weeklyScheduleGridDay(trimmed, len(days)); ok {
			days = append(days, day)
			currentDay = len(days) - 1
			continue
		}
		if currentDay < 0 || trimmed == "没有课。" {
			continue
		}
		item, ok := weeklyScheduleGridItem(line, currentDay)
		if !ok {
			return nil
		}
		items = mergeScheduleGridItem(items, item)
	}
	if len(days) != len(weeklyScheduleDayLabels) {
		return nil
	}
	if metadata.dateRange == "" {
		metadata.dateRange = scheduleGridDateRange(days)
	}
	return &responses.ScheduleGrid{
		Semester:  metadata.semester,
		Week:      metadata.week,
		DateRange: metadata.dateRange,
		Days:      days,
		Periods:   weeklyScheduleGridPeriods(),
		Items:     items,
	}
}

func dailyScheduleGrid(text string) *responses.ScheduleGrid {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 2 {
		return nil
	}
	now := chinaNow()
	day := now
	date := now.Format("01-02")
	for _, field := range strings.Fields(lines[0]) {
		candidate := strings.Trim(strings.TrimSpace(field), "：:")
		if !scheduleGridDate(candidate) {
			continue
		}
		parsed, err := time.ParseInLocation("2006-01-02", strconv.Itoa(now.Year())+"-"+candidate, lifedata.ChinaLocation())
		if err == nil {
			day = parsed
			date = candidate
		}
		break
	}

	items := []responses.ScheduleGridItem{}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "没有课。" {
			continue
		}
		if _, ok := parseScheduleGridMetadata(trimmed); ok {
			continue
		}
		item, ok := weeklyScheduleGridItem(line, 0)
		if !ok {
			return nil
		}
		items = mergeScheduleGridItem(items, item)
	}
	return &responses.ScheduleGrid{
		Days: []responses.ScheduleGridDay{{
			Label: weeklyScheduleDayLabels[day.Weekday()],
			Date:  date,
		}},
		Periods: weeklyScheduleGridPeriods(),
		Items:   items,
	}
}

func weeklyScheduleGridDay(line string, expectedIndex int) (responses.ScheduleGridDay, bool) {
	if expectedIndex < 0 || expectedIndex >= len(weeklyScheduleDayLabels) {
		return responses.ScheduleGridDay{}, false
	}
	line = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(line), "："), ":")
	fields := strings.Fields(line)
	if len(fields) == 1 && fields[0] == weeklyScheduleDayLabels[expectedIndex] {
		return responses.ScheduleGridDay{Label: fields[0]}, true
	}
	if len(fields) != 2 || fields[0] != weeklyScheduleDayLabels[expectedIndex] || !scheduleGridDate(fields[1]) {
		return responses.ScheduleGridDay{}, false
	}
	return responses.ScheduleGridDay{Label: fields[0], Date: fields[1]}, true
}

func scheduleGridDate(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return false
	}
	month, monthErr := strconv.Atoi(parts[0])
	day, dayErr := strconv.Atoi(parts[1])
	return monthErr == nil && dayErr == nil && month >= 1 && month <= 12 && day >= 1 && day <= 31
}

func weeklyScheduleGridItem(line string, day int) (responses.ScheduleGridItem, bool) {
	columns := strings.Split(line, "\t")
	if len(columns) < 3 {
		return responses.ScheduleGridItem{}, false
	}
	place := strings.TrimSpace(columns[0])
	timeRange := strings.TrimSpace(columns[1])
	course, kind := scheduleGridCourseAndKind(columns[2])
	weeks := strings.TrimSpace(strings.Join(columns[3:], " "))
	start, end, ok := schedulePeriodRange(timeRange)
	if !ok || course == "" {
		return responses.ScheduleGridItem{}, false
	}
	campus, room := scheduleCampusAndRoom(place)
	return responses.ScheduleGridItem{
		Day:         day,
		StartPeriod: start,
		EndPeriod:   end,
		Kind:        kind,
		Course:      course,
		Location:    scheduleLocationNote(campus, room),
		Weeks:       weeks,
	}, true
}

// scheduleGridCourseAndKind separates the personal role suffix used by the
// plain-text response from the semantic data sent to both image renderers.
// Keeping the suffix in the text response preserves the existing Bot output;
// the grid card can now render the role as a small badge without duplicating
// it in the course title.
func scheduleGridCourseAndKind(value string) (course, kind string) {
	course = strings.TrimSpace(value)
	for _, role := range []struct {
		label string
		kind  string
	}{
		{label: "助教", kind: lifedata.SubscriptionKindTeachingAssistant},
		{label: "旁听", kind: lifedata.SubscriptionKindAuditor},
	} {
		for _, pair := range []string{"（" + role.label + "）", "(" + role.label + ")"} {
			if !strings.HasSuffix(course, pair) {
				continue
			}
			course = strings.TrimSpace(strings.TrimSuffix(course, pair))
			return course, role.kind
		}
	}
	return course, ""
}

func mergeScheduleGridItem(items []responses.ScheduleGridItem, next responses.ScheduleGridItem) []responses.ScheduleGridItem {
	for i := range items {
		if items[i].Day != next.Day || items[i].StartPeriod != next.StartPeriod || items[i].EndPeriod != next.EndPeriod {
			continue
		}
		items[i].Course = joinScheduleGridText(items[i].Course, next.Course)
		items[i].Location = joinScheduleGridText(items[i].Location, next.Location)
		items[i].Weeks = joinScheduleGridText(items[i].Weeks, next.Weeks)
		mergeScheduleGridKinds(&items[i], next)
		return items
	}
	return append(items, next)
}

func mergeScheduleGridKinds(item *responses.ScheduleGridItem, next responses.ScheduleGridItem) {
	kinds := append([]string(nil), item.AdditionalKinds...)
	if item.Kind != "" {
		kinds = append([]string{item.Kind}, kinds...)
	}
	if next.Kind != "" {
		kinds = append(kinds, next.Kind)
	}
	unique := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if kind == "" || containsString(unique, kind) {
			continue
		}
		unique = append(unique, kind)
	}
	if len(unique) == 0 {
		item.Kind = ""
		item.AdditionalKinds = nil
		return
	}
	item.Kind = unique[0]
	item.AdditionalKinds = append([]string(nil), unique[1:]...)
}

func joinScheduleGridText(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "" || left == right:
		return left
	default:
		return left + " / " + right
	}
}

func weeklyScheduleGridPeriods() []responses.ScheduleGridPeriod {
	periods := make([]responses.ScheduleGridPeriod, len(ustcLessonPeriods))
	for i, period := range ustcLessonPeriods {
		periods[i] = responses.ScheduleGridPeriod{
			Label: "第 " + strconv.Itoa(i+1) + " 节",
			Time:  fmt.Sprintf("%02d:%02d–%02d:%02d", period.start/60, period.start%60, period.end/60, period.end%60),
		}
	}
	return periods
}

func scheduleCampusAndRoom(place string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(place))
	if len(parts) == 0 {
		return "", ""
	}
	campus := campusName(parts[0])
	switch campus {
	case "东区", "西区", "中区", "北区", "南区", "高新区", "先研院":
		return campus, strings.TrimSpace(strings.TrimPrefix(place, parts[0]))
	default:
		return "", strings.TrimSpace(place)
	}
}

func markdownRichTableRow(cells []string) string {
	trimmed := make([]string, len(cells))
	for i, cell := range cells {
		trimmed[i] = strings.TrimSpace(cell)
	}
	return "| " + strings.Join(trimmed, " | ") + " |"
}

func richTextImage(kind, title, text string) *responses.Image {
	text = textutil.PlainMonospace(text)
	body := strings.Split(strings.TrimSpace(text), "\n")
	if len(body) > 0 {
		first := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(body[0]), "："), ":")
		if first == strings.TrimSpace(title) {
			body = body[1:]
		}
	}
	body = richTextSectionLines(body)
	body = richTextTableSections(kind, body)
	richText := strings.TrimSpace("# " + strings.TrimSpace(title) + "\n\n" + strings.Join(body, "\n"))
	return responses.NewRichTextImage(kind, richText, imageRenderText(text))
}

func richTextTableSections(kind string, lines []string) []string {
	out := []string{}
	section := ""
	if kind == "todo" {
		section = "待办"
	}
	block := []string{}
	flush := func() {
		if table, ok := richImageTable(kind, section, block); ok {
			out = append(out, table...)
		} else {
			out = append(out, block...)
		}
		block = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, trimmed)
			section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			continue
		}
		block = append(block, line)
	}
	flush()
	return out
}

func richImageTable(kind, section string, lines []string) ([]string, bool) {
	data := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			data = append(data, line)
		}
	}
	if len(data) == 0 {
		return nil, false
	}

	var headers []string
	var cellsFor func(string) ([]string, bool)
	sectionKind := richImageSectionKind(section)
	if sectionKind == "" && (kind == "homework" || kind == "exam") {
		sectionKind = kind
	}
	switch sectionKind {
	case "schedule":
		headers = []string{"节次", "时间", "安排", "备注"}
		cellsFor = overviewScheduleRichTableCells
	case "todo":
		headers = []string{"#", "截止", "待办"}
		cellsFor = todoRichTableCells
	case "homework":
		headers = []string{"#", "截止", "课程", "作业"}
		cellsFor = homeworkRichTableCells
	case "exam":
		headers = []string{"#", "日期", "时间", "课程", "教学班", "方式", "教室"}
		cellsFor = examRichTableCells
	default:
		return nil, false
	}
	rows := make([][]string, 0, len(data))
	for _, line := range data {
		cells, ok := cellsFor(line)
		if !ok {
			cells, ok = richImageMoreRow(line, len(headers))
		}
		if !ok {
			return nil, false
		}
		rows = append(rows, cells)
	}

	out := []string{markdownRichTableRow(headers)}
	separators := make([]string, len(headers))
	for i := range separators {
		separators[i] = "---"
	}
	out = append(out, markdownRichTableRow(separators))
	for _, row := range rows {
		out = append(out, markdownRichTableRow(row))
	}
	return out, true
}

func richImageMoreRow(line string, width int) ([]string, bool) {
	line = strings.TrimSpace(line)
	isMore := strings.HasPrefix(line, "...and ") && strings.HasSuffix(line, " more")
	isPagination := strings.HasPrefix(line, "第 ") && strings.Contains(line, " 页")
	isListHint := strings.HasPrefix(line, "另有 ") && strings.Contains(line, "查看完整列表")
	if !isMore && !isPagination && !isListHint {
		return nil, false
	}
	row := make([]string, width)
	row[len(row)-1] = line
	return row, true
}

func richImageSectionKind(section string) string {
	section = strings.TrimSpace(section)
	if start := strings.LastIndex(section, " ("); start >= 0 && strings.HasSuffix(section, ")") {
		section = strings.TrimSpace(section[:start])
	}
	switch {
	case strings.Contains(section, "课表"):
		return "schedule"
	case section == "待办":
		return "todo"
	case section == "作业" || section == "近期作业":
		return "homework"
	case section == "考试":
		return "exam"
	default:
		return ""
	}
}

func overviewScheduleRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 4)
	if !ok {
		return nil, false
	}
	campus, room := scheduleCampusAndRoom(cells[1])
	timeRange := cells[2]
	return []string{schedulePeriodLabel(timeRange), timeRange, strings.Join(cells[3:], " "), scheduleLocationNote(campus, room)}, true
}

func todoRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 3)
	if !ok {
		return nil, false
	}
	cells[1] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[1]), "截止 "))
	return cells, true
}

func homeworkRichTableCells(line string) ([]string, bool) {
	cells, ok := fixedNumberedRichTableCells(line, 4)
	if !ok {
		return nil, false
	}
	cells[1] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[1]), "截止 "))
	return cells, true
}

func examRichTableCells(line string) ([]string, bool) {
	return fixedNumberedRichTableCells(line, 7)
}

func fixedNumberedRichTableCells(line string, width int) ([]string, bool) {
	cells, ok := numberedRichTableCells(line)
	if !ok || len(cells) > width {
		return nil, false
	}
	row := make([]string, width)
	copy(row, cells)
	return row, true
}

func numberedRichTableCells(line string) ([]string, bool) {
	cells := richTextTableCells(line)
	if len(cells) < 2 {
		return nil, false
	}
	index := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cells[0]), "."))
	if index == "" {
		return nil, false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	cells[0] = index
	return cells, true
}

func richTextSectionLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	atSectionStart := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			atSectionStart = true
			continue
		}
		if atSectionStart && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
			trimmed = strings.TrimSuffix(strings.TrimSuffix(trimmed, "："), ":")
			out = append(out, "## "+strings.TrimSpace(trimmed))
		} else {
			out = append(out, line)
		}
		atSectionStart = false
	}
	return out
}

func richTextTableCells(line string) []string {
	if !strings.Contains(line, "\t") {
		return strings.Fields(line)
	}
	cells := strings.Split(line, "\t")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func imageRenderText(text string) string {
	text = textutil.PlainMonospace(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\t':
			out.WriteString("  ")
		case '\n':
			out.WriteRune(r)
		case '\r':
			continue
		default:
			if r < ' ' {
				out.WriteRune(' ')
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func imageTitle(text, fallback string) string {
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(text), "\n")[0])
	first = strings.TrimSuffix(first, "：")
	first = strings.TrimSuffix(first, ":")
	if first == "" {
		return fallback
	}
	return first
}

func successfulImageText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	firstLine := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	lowerFirstLine := strings.ToLower(firstLine)
	if strings.HasPrefix(firstLine, "需要") || strings.HasPrefix(firstLine, "请先") ||
		strings.HasPrefix(firstLine, "登录") || strings.HasPrefix(firstLine, "没有") ||
		strings.HasPrefix(firstLine, "还没有") || strings.Contains(lowerFirstLine, "unavailable") {
		return false
	}
	failureMarkers := []string{
		"查不到", "没查到", "没有", "未找到", "失败", "错误", "不可用", "未配置", "无法",
		"缺少", "无效", "不存在", "格式不太对", "暂时操作不了", "暂时修改不了",
		"暂时删除不了", "超时", "必须", "只能", "只有", "分页用法",
	}
	for _, marker := range failureMarkers {
		if strings.Contains(firstLine, marker) {
			return false
		}
	}
	return true
}

func todoImageArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "list", "all", "pending", "completed":
		return true
	default:
		_, ok := normalizeTodoPriority(args[0])
		return ok
	}
}

func homeworkImageArgs(args []string) bool {
	return !firstArgIn(args, "help", "done", "undo")
}
