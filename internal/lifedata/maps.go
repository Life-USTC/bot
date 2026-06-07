package lifedata

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/textutil"
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

func HomeworkCompleted(homework map[string]any) bool {
	if completed, ok := homework["isCompleted"].(bool); ok {
		return completed
	}
	return homework["completion"] != nil
}

func HomeworkCourseLabel(homework map[string]any) string {
	name := NestedPathString(homework, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if name != "" {
		return name
	}
	return NestedPathString(homework, []string{"section", "course"}, "code")
}

func ScheduleCourseLabel(schedule map[string]any) string {
	name := NestedPathString(schedule, []string{"section", "course"}, "namePrimary", "nameCn", "name")
	if name != "" {
		return name
	}
	return NestedString(schedule, "section", "code")
}

func SchedulePlaceLabel(schedule map[string]any) string {
	place := FirstString(schedule, "customPlace")
	if place != "" {
		return place
	}
	return NestedString(schedule, "room", "namePrimary", "nameCn", "name", "code")
}

func ScheduleTimeRange(schedule map[string]any) string {
	start := FirstString(schedule, "startTime")
	end := FirstString(schedule, "endTime")
	if start == "" && end == "" {
		return ""
	}
	return strings.TrimSpace(start + "-" + end)
}

func SortHomeworksByDue(homeworks []map[string]any) {
	sort.SliceStable(homeworks, func(i, j int) bool {
		return apiTimeLess(FirstString(homeworks[i], "submissionDueAt"), FirstString(homeworks[j], "submissionDueAt"))
	})
}

func SortSchedulesByStart(schedules []map[string]any) {
	sort.SliceStable(schedules, func(i, j int) bool {
		return FirstString(schedules[i], "startTime") < FirstString(schedules[j], "startTime")
	})
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
	if start == "" {
		return time.Time{}
	}
	if loc == nil {
		loc = day.Location()
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+start, loc)
	if err != nil {
		return time.Time{}
	}
	return parsed
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
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
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
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	raw := AnySlice(value)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
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
