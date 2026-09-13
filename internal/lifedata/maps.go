package lifedata

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/textutil"
)

const (
	SubscriptionKindRegular           = "regular"
	SubscriptionKindAuditor           = "auditor"
	SubscriptionKindTeachingAssistant = "teaching_assistant"
)

func FirstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := m[key].(type) {
		case string:
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		case float64:
			return strconv.FormatFloat(value, 'f', -1, 64)
		case int:
			return strconv.Itoa(value)
		case int64:
			return strconv.FormatInt(value, 10)
		}
	}
	return ""
}

func NestedString(m map[string]any, key string, keys ...string) string {
	child, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	return FirstString(child, keys...)
}

func NestedPathString(m map[string]any, path []string, keys ...string) string {
	current := m
	for _, key := range path {
		child, ok := current[key].(map[string]any)
		if !ok {
			return ""
		}
		current = child
	}
	return FirstString(current, keys...)
}

// SubscriptionKindLabel turns the API membership kind into the short label
// used beside a course name. Regular membership and unknown values stay
// unlabelled.
func SubscriptionKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case SubscriptionKindAuditor:
		return "旁听"
	case SubscriptionKindTeachingAssistant:
		return "助教"
	default:
		return ""
	}
}

// MembershipKind accepts either a section itself or an object containing a
// section, as used by subscription, schedule, and homework payloads.
func MembershipKind(data map[string]any) string {
	if data == nil {
		return ""
	}
	if kind := FirstString(data, "kind"); kind != "" {
		return kind
	}
	section, _ := data["section"].(map[string]any)
	return FirstString(section, "kind")
}

// MembershipLabel returns the display label for an object with a personal
// section membership.
func MembershipLabel(data map[string]any) string {
	return SubscriptionKindLabel(MembershipKind(data))
}

// CourseLabel adds a personal membership label to a course name. Text
// responses keep this suffix; structured timetable images split it into a
// separate role badge before rendering.
func CourseLabel(name string, data map[string]any) string {
	name = strings.TrimSpace(name)
	if label := MembershipLabel(data); label != "" {
		return strings.TrimSpace(name + "（" + label + "）")
	}
	return name
}

func HomeworkCompleted(homework map[string]any) bool {
	if completed, ok := homework["isCompleted"].(bool); ok {
		return completed
	}
	return homework["completion"] != nil
}

func HomeworkCompletionRequired(homework map[string]any) bool {
	required, ok := homework["completionRequired"].(bool)
	if !ok {
		return true
	}
	return required
}

// HomeworkPendingForDisplay keeps regular pending semantics and excludes an
// uncompleted homework without a completion requirement once its deadline has
// arrived. Missing deadlines remain pending.
func HomeworkPendingForDisplay(homework map[string]any, now time.Time) bool {
	if HomeworkCompleted(homework) {
		return false
	}
	if HomeworkCompletionRequired(homework) {
		return true
	}
	due, ok := ParseAPITime(FirstString(homework, "submissionDueAt"))
	return !ok || due.After(now)
}

const HomeworkNoCompletionLabel = "无需完成"

func HomeworkStatusLabel(homework map[string]any) string {
	if !HomeworkCompletionRequired(homework) {
		return HomeworkNoCompletionLabel
	}
	return ""
}

func HomeworkCourseLabel(homework map[string]any) string {
	name := NestedPathString(homework, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if name != "" {
		return CourseLabel(name, homework)
	}
	return CourseLabel(NestedPathString(homework, []string{"section", "course"}, "code"), homework)
}

func HomeworkLabel(homework map[string]any) string {
	due := FormatAPITime(FirstString(homework, "submissionDueAt"))
	dueParts := make([]string, 0, 2)
	if status := HomeworkStatusLabel(homework); status != "" {
		dueParts = append(dueParts, status)
	}
	if due != "" {
		dueParts = append(dueParts, "截止 "+due)
	}
	parts := textutil.NonEmpty(
		strings.Join(dueParts, " · "),
		HomeworkCourseLabel(homework),
		FirstString(homework, "title"),
	)
	if len(parts) == 0 {
		return FirstString(homework, "id")
	}
	return strings.Join(parts, " · ")
}

func ScheduleCourseLabel(schedule map[string]any) string {
	name := NestedPathString(schedule, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if name != "" {
		return CourseLabel(name, schedule)
	}
	return CourseLabel(NestedString(schedule, "section", "code"), schedule)
}

func SchedulePlaceLabel(schedule map[string]any) string {
	place := FirstString(schedule, "customPlace")
	if place != "" {
		return place
	}
	room := NestedString(schedule, "room", "namePrimary", "nameCn", "name", "code")
	campus := NestedPathString(schedule, []string{"room", "building", "campus"}, "namePrimary", "nameCn", "name", "code")
	if campus == "" {
		campus = NestedPathString(schedule, []string{"section", "campus"}, "namePrimary", "nameCn", "name", "code")
	}
	if campus == "" || strings.HasPrefix(room, campus) {
		return room
	}
	return strings.TrimSpace(campus + " " + room)
}

func ScheduleTimeRange(schedule map[string]any) string {
	start := FirstString(schedule, "startTime")
	end := FirstString(schedule, "endTime")
	if start == "" && end == "" {
		return ""
	}
	return strings.TrimSpace(start + "-" + end)
}

func ScheduleFallbackLabel(schedule map[string]any) string {
	return textutil.FirstNonEmpty(FirstString(schedule, "id"), NestedString(schedule, "section", "id"))
}

func DayRFC3339Range(day time.Time) (string, string) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())
	return start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
}

func SortHomeworksByDue(homeworks []map[string]any) {
	sort.SliceStable(homeworks, func(i, j int) bool {
		return apiTimeLess(FirstString(homeworks[i], "submissionDueAt"), FirstString(homeworks[j], "submissionDueAt"))
	})
}

func SortSchedulesByStart(schedules []map[string]any) {
	sort.SliceStable(schedules, func(i, j int) bool {
		left := FirstString(schedules[i], "startTime")
		right := FirstString(schedules[j], "startTime")
		leftMinutes, leftOK := clockMinutes(left)
		rightMinutes, rightOK := clockMinutes(right)
		switch {
		case leftOK && rightOK:
			return leftMinutes < rightMinutes
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return left < right
		}
	})
}

func clockMinutes(value string) (int, bool) {
	hour, minute, _, ok := clockParts(value)
	if !ok {
		return 0, false
	}
	return hour*60 + minute, true
}

func clockParts(value string) (int, int, int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, 0, false
	}
	second := 0
	if len(parts) == 3 {
		parsed, err := strconv.Atoi(parts[2])
		if err != nil || parsed < 0 || parsed > 59 {
			return 0, 0, 0, false
		}
		second = parsed
	}
	return hour, minute, second, true
}

func SubscriptionSectionIDs(data map[string]any) []string {
	return SubscriptionSectionIDsForDay(data, time.Time{})
}

func SubscriptionSectionIDsForDay(data map[string]any, day time.Time) []string {
	sections := SubscriptionSections(data)
	out := make([]string, 0, len(sections))
	fallback := make([]string, 0, len(sections))
	sawSemester := false
	for _, section := range sections {
		id := FirstString(section, "id")
		if id != "" {
			fallback = append(fallback, id)
		}
		semester, _ := section["semester"].(map[string]any)
		if semester == nil {
			continue
		}
		sawSemester = true
		if !day.IsZero() && !SemesterContainsDay(semester, day) {
			continue
		}
		if id != "" {
			out = append(out, id)
		}
	}
	if !sawSemester || day.IsZero() {
		return fallback
	}
	return out
}

func SubscriptionSections(data map[string]any) []map[string]any {
	sub, _ := data["subscription"].(map[string]any)
	return MapSlice(sub["sections"])
}

// SubscriptionSectionKinds indexes the personal section memberships by the
// identifiers that schedule and homework payloads commonly carry.
func SubscriptionSectionKinds(data map[string]any) map[string]string {
	result := make(map[string]string)
	for _, section := range SubscriptionSections(data) {
		kind := strings.TrimSpace(FirstString(section, "kind"))
		if kind == "" {
			continue
		}
		for _, key := range subscriptionSectionKeys(section) {
			result[key] = kind
		}
	}
	return result
}

// ApplySubscriptionKinds copies membership kinds from a current subscription
// response into schedule/homework objects that refer to those sections. It
// preserves a kind already supplied by the endpoint itself.
func ApplySubscriptionKinds(subscription map[string]any, items []map[string]any) {
	kinds := SubscriptionSectionKinds(subscription)
	if len(kinds) == 0 {
		return
	}
	for _, item := range items {
		if item == nil || MembershipKind(item) != "" {
			continue
		}
		section, _ := item["section"].(map[string]any)
		if section != nil {
			if kind := lookupSubscriptionKind(kinds, section); kind != "" {
				section["kind"] = kind
				continue
			}
		}
		if kind := lookupSubscriptionKind(kinds, item); kind != "" {
			item["kind"] = kind
		}
	}
}

func lookupSubscriptionKind(kinds map[string]string, data map[string]any) string {
	for _, key := range subscriptionSectionKeys(data) {
		if kind := kinds[key]; kind != "" {
			return kind
		}
	}
	return ""
}

func subscriptionSectionKeys(section map[string]any) []string {
	keys := make([]string, 0, 3)
	for _, field := range []string{"id", "jwId", "code"} {
		value := strings.TrimSpace(FirstString(section, field))
		if value == "" {
			continue
		}
		keys = append(keys, field+":"+strings.ToLower(value))
	}
	return keys
}

func SemesterContainsDay(semester map[string]any, day time.Time) bool {
	loc := day.Location()
	start, okStart := ParseAPITime(FirstString(semester, "startDate"))
	end, okEnd := ParseAPITime(FirstString(semester, "endDate"))
	target := day.In(loc).Format("2006-01-02")
	if okStart && target < start.In(loc).Format("2006-01-02") {
		return false
	}
	if okEnd && target > end.In(loc).Format("2006-01-02") {
		return false
	}
	return okStart || okEnd
}

func FilterSchedulesForDay(schedules []map[string]any, day time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(schedules))
	for _, schedule := range schedules {
		if ScheduleMatchesDay(schedule, day) {
			out = append(out, schedule)
		}
	}
	return out
}

func ScheduleMatchesDay(schedule map[string]any, day time.Time) bool {
	date := FirstString(schedule, "date")
	if date == "" {
		return true
	}
	parsed, ok := ParseAPITime(date)
	if !ok {
		return true
	}
	return parsed.In(day.Location()).Format("2006-01-02") == day.In(day.Location()).Format("2006-01-02")
}

func ScheduleStartTime(schedule map[string]any, day time.Time, loc *time.Location) time.Time {
	start := FirstString(schedule, "startTime")
	hour, minute, second, ok := clockParts(start)
	if !ok {
		return time.Time{}
	}
	if loc == nil {
		loc = day.Location()
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, second, 0, loc)
}

func FormatAPITime(value string) string {
	if value == "" {
		return ""
	}
	parsed, ok := ParseAPITime(value)
	if !ok {
		return strings.TrimSpace(value)
	}
	return parsed.In(ChinaLocation()).Format("01-02 15:04")
}

func ParseAPITime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, ChinaLocation()); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func apiTimeLess(left, right string) bool {
	leftTime, leftOK := ParseAPITime(left)
	rightTime, rightOK := ParseAPITime(right)
	switch {
	case leftOK && rightOK:
		return leftTime.Before(rightTime)
	case leftOK:
		return true
	case rightOK:
		return false
	default:
		return left < right
	}
}

func ChinaLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

func FirstInt(m map[string]any, keys ...string) int {
	if m == nil {
		return 0
	}
	for _, key := range keys {
		if value, ok := IntValue(m[key]); ok {
			return value
		}
	}
	return 0
}

func IntValue(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if math.Trunc(n) != n {
			return 0, false
		}
		return int(n), true
	case string:
		parsed, err := strconv.Atoi(textutil.PlainDigits(strings.TrimSpace(n)))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func StringSlice(value any) []string {
	if items, ok := value.([]string); ok {
		out := make([]string, 0, len(items))
		for _, text := range items {
			out = appendNonEmptyString(out, text)
		}
		return out
	}
	raw := AnySlice(value)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = appendNonEmptyString(out, text)
		}
	}
	return out
}

func appendNonEmptyString(out []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return out
	}
	return append(out, text)
}

func AnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func MapSlice(value any) []map[string]any {
	if items, ok := value.([]map[string]any); ok {
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if item != nil {
				out = append(out, item)
			}
		}
		return out
	}
	raw := AnySlice(value)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func SemesterLabel(data map[string]any) string {
	semester, _ := data["semester"].(map[string]any)
	return FirstString(semester, "namePrimary", "nameCn", "name")
}
