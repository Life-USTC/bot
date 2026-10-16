package commands

// The schedule endpoints answer with rows that embed the complete jw section
// record — dozens of scheduling scalars plus nested course, room, campus and
// teacher objects — repeated once per lesson. A schedule line prints only the
// time range, the place, the course identity and the personal membership
// kind, so the day, week, semester and section-schedule capabilities record
// this projection instead of forwarding the bulk rows to the model.

import (
	"sort"

	"github.com/Life-USTC/Bot/internal/lifedata"
)

// compactSchedules is the model-facing view of a list of schedule rows.
func compactSchedules(schedules []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(schedules))
	for _, schedule := range schedules {
		result = append(result, compactSchedule(schedule))
	}
	return result
}

// compactSectionSchedules projects the lessons of a single section. The
// section is the same on every row, so it is left to the caller to record
// once; a campus the place label falls back to stays on the row.
func compactSectionSchedules(schedules []map[string]any) []map[string]any {
	result := compactSchedules(schedules)
	for _, row := range result {
		section, ok := row["section"].(map[string]any)
		if !ok {
			continue
		}
		delete(row, "section")
		if campus, ok := section["campus"]; ok {
			row["section"] = map[string]any{"campus": campus}
		}
	}
	return result
}

// compactSchedule keeps the fields a schedule line prints — time range, place,
// course identity and membership kind — plus the date and week coordinates the
// weekly and semester views aggregate by.
func compactSchedule(schedule map[string]any) map[string]any {
	compact := make(map[string]any)
	for _, key := range []string{
		"id", "date", "weekday", "weekIndex", "startTime", "endTime", "customPlace", "kind",
	} {
		if value, ok := schedule[key]; ok {
			compact[key] = value
		}
	}
	if room, ok := schedule["room"].(map[string]any); ok {
		compact["room"] = compactScheduleRoom(room)
	}
	if section, ok := schedule["section"].(map[string]any); ok {
		compactSection := compactScheduleSection(section)
		if campus, ok := section["campus"].(map[string]any); ok && !scheduleRoomHasCampus(schedule) {
			compactSection["campus"] = compactScheduleEntity(campus)
		}
		compact["section"] = compactSection
	}
	return compact
}

// compactScheduleEntity keeps the identity of a room, campus, section or
// course together with the names lifedata's schedule labels read
// (namePrimary, nameCn, name, code). Secondary and English names are never
// printed on a schedule line and are dropped; repeated on every lesson they
// were most of what remained of a projected row.
func compactScheduleEntity(entity map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"id", "jwId", "code", "name", "nameCn", "namePrimary", "kind"} {
		if value, ok := entity[key]; ok {
			result[key] = value
		}
	}
	return result
}

// compactScheduleRoom keeps the room identity and the campus the place label
// prefixes it with. The building itself is never printed; it is kept only as
// the path to that campus.
func compactScheduleRoom(room map[string]any) map[string]any {
	result := compactScheduleEntity(room)
	if building, ok := room["building"].(map[string]any); ok {
		if campus, ok := building["campus"].(map[string]any); ok {
			result["building"] = map[string]any{"campus": compactScheduleEntity(campus)}
		}
	}
	return result
}

// compactScheduleSection keeps the section identity a schedule line prints:
// code, course name and membership kind. Teachers and the semester are not
// part of a lesson line; the week and semester views carry the semester once
// at the top level instead of once per lesson.
func compactScheduleSection(section map[string]any) map[string]any {
	result := compactScheduleEntity(section)
	if course, ok := section["course"].(map[string]any); ok {
		result["course"] = compactScheduleEntity(course)
	}
	return result
}

// scheduleRoomHasCampus mirrors lifedata.SchedulePlaceLabel: the section
// campus is printed only when the room carries no campus of its own.
func scheduleRoomHasCampus(schedule map[string]any) bool {
	return lifedata.NestedPathString(schedule, []string{"room", "building", "campus"}, "namePrimary", "nameCn", "name", "code") != ""
}

// compactScheduleSemester keeps the semester identity and the date range the
// weekly metadata line is derived from.
func compactScheduleSemester(semester map[string]any) map[string]any {
	if semester == nil {
		return nil
	}
	compact := compactSubscriptionEntity(semester)
	for _, key := range []string{"startDate", "endDate"} {
		if value, ok := semester[key]; ok {
			compact[key] = value
		}
	}
	return compact
}

// compactSemesterScheduleEntries is the model-facing view of a semester
// timetable: the weekly slots the reply prints, each with its teaching weeks,
// in display order.
func compactSemesterScheduleEntries(entries []semesterScheduleEntry) []map[string]any {
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		weeks := make([]int, 0, len(entry.weeks))
		for week := range entry.weeks {
			weeks = append(weeks, week)
		}
		sort.Ints(weeks)
		compact := map[string]any{
			"weekday":   entry.day,
			"startTime": entry.startTime,
			"endTime":   entry.endTime,
			"course":    entry.course,
			"place":     entry.place,
			"weeks":     weeks,
		}
		if entry.section != nil {
			compact["section"] = entry.section
		}
		result = append(result, compact)
	}
	return result
}
