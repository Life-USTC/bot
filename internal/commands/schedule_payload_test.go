package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/textutil"
)

var updateScheduleGolden = flag.Bool("update-schedule-golden", false, "rewrite testdata/schedule_payload/*.txt from the current replies")

// bulkScheduleMarker only ever appears in fields of the raw schedule rows that
// a schedule reply never prints: section scheduling scalars, room metadata,
// teacher records and schedule groups. A model-facing payload that contains it
// has forwarded the bulk rows instead of a projection of them.
const bulkScheduleMarker = "bulk-schedule-row-marker"

const (
	scheduleFixtureSectionCount = 12
	scheduleFixtureWeeks        = 16
)

// scheduleFixtureSemester mirrors semesterSchema in api/openapi.json and is the
// semester every subscriptionFixtureSection belongs to.
func scheduleFixtureSemester() map[string]any {
	return map[string]any{
		"id": 1, "jwId": 202601, "nameCn": "2026年秋季学期", "code": "2026F",
		"startDate": "2026-09-01T00:00:00+08:00", "endDate": "2027-01-15T23:59:59+08:00",
	}
}

// scheduleFixtureSection is the subscribed section i. Two kinds other than
// regular are mixed in so the membership label path is exercised.
func scheduleFixtureSection(i int) map[string]any {
	section := subscriptionFixtureSection(i)
	section["remark"] = "限本专业选修 " + bulkScheduleMarker
	section["dateTimePlaceText"] = "1-16周 " + bulkScheduleMarker
	switch i % 5 {
	case 1:
		section["kind"] = "auditor"
	case 3:
		section["kind"] = "teaching_assistant"
	}
	return section
}

func scheduleFixtureSubscription() map[string]any {
	sections := make([]any, 0, scheduleFixtureSectionCount)
	for i := range scheduleFixtureSectionCount {
		sections = append(sections, scheduleFixtureSection(i))
	}
	return map[string]any{"subscription": map[string]any{
		"userId": "u-1001", "sections": sections, "note": "",
		"calendarPath": "/api/workspace/calendar/ics/abcdef0123456789",
		"calendarUrl":  subscriptionFixtureCalendarURL,
	}}
}

func scheduleFixtureTeacher(i int) map[string]any {
	return map[string]any{
		"id": 700 + i, "jwId": 7000 + i, "personId": 70000 + i, "code": fmt.Sprintf("T%05d", i),
		"nameCn": "张教授", "nameEn": "Prof. Zhang", "namePrimary": "张教授", "nameSecondary": "Prof. Zhang",
		"department": map[string]any{
			"id": 11, "code": "MATH", "nameCn": "数学科学学院 " + bulkScheduleMarker, "nameEn": "School of Mathematical Sciences",
			"isCollege": true, "namePrimary": "数学科学学院", "nameSecondary": "School of Mathematical Sciences",
		},
		"teacherTitle": map[string]any{
			"id": 3, "jwId": 30, "nameCn": "教授 " + bulkScheduleMarker, "nameEn": "Professor", "code": "PROF",
			"enabled": true, "namePrimary": "教授", "nameSecondary": "Professor",
		},
		"_count": map[string]any{"sections": 4},
	}
}

func scheduleFixtureRoom(i int) map[string]any {
	return map[string]any{
		"id": 300 + i, "jwId": 3000 + i, "code": fmt.Sprintf("5%03d", 100+i),
		"nameCn": fmt.Sprintf("五教%d", 5100+i), "nameEn": fmt.Sprintf("5th Teaching Building %d", 5100+i),
		"namePrimary": fmt.Sprintf("五教%d", 5100+i), "nameSecondary": fmt.Sprintf("5th Teaching Building %d", 5100+i),
		"floor": 1, "virtual": false, "seatsForSection": 150, "seats": 160,
		"remark":     "多媒体教室 " + bulkScheduleMarker,
		"buildingId": 50, "roomTypeId": 4,
		"building": map[string]any{
			"id": 50, "jwId": 500, "code": "5", "campusId": 2,
			"nameCn": "第五教学楼", "nameEn": "5th Teaching Building", "namePrimary": "第五教学楼", "nameSecondary": "5th Teaching Building",
			"campus": map[string]any{
				"id": 2, "jwId": 20, "code": "EAST",
				"nameCn": "东区", "nameEn": "East Campus", "namePrimary": "东区", "nameSecondary": "East Campus",
			},
		},
		"roomType": map[string]any{
			"id": 4, "jwId": 40, "code": "MM", "nameCn": "多媒体教室 " + bulkScheduleMarker,
			"nameEn": "Multimedia Classroom", "namePrimary": "多媒体教室", "nameSecondary": "Multimedia Classroom",
		},
	}
}

// scheduleFixtureSlots places section i into weekly slots: every section meets
// twice a week and section 0 meets a third time, so a teaching week holds 25
// lessons, the size of a full undergraduate week.
func scheduleFixtureSlots(i int) [][2]int {
	slots := [][2]int{{1 + i%5, i % 5}, {1 + (i+2)%5, (i + 3) % 5}}
	if i == 0 {
		slots = append(slots, [2]int{6, 2})
	}
	return slots
}

var scheduleFixtureUnits = [...][2]string{{"07:50", "09:25"}, {"09:45", "11:20"}, {"14:00", "15:35"}, {"15:55", "17:30"}, {"19:30", "21:05"}}

// scheduleFixtureRows mirrors sectionSchedulesResponseSchema: every lesson of
// section i over the semester, without the section record.
func scheduleFixtureRows(i int) []map[string]any {
	loc := lifedata.ChinaLocation()
	weekOneStart := time.Date(2026, 8, 30, 0, 0, 0, 0, loc)
	rows := make([]map[string]any, 0)
	for week := 1; week <= scheduleFixtureWeeks; week++ {
		for slotIndex, slot := range scheduleFixtureSlots(i) {
			weekday, unit := slot[0], slot[1]
			date := weekOneStart.AddDate(0, 0, (week-1)*7+weekday)
			teacher := scheduleFixtureTeacher(i)
			rows = append(rows, map[string]any{
				"id": 100000 + i*1000 + week*10 + slotIndex, "periods": 2.0,
				"date": date.Format(time.RFC3339), "weekday": weekday,
				"startTime": scheduleFixtureUnits[unit][0], "endTime": scheduleFixtureUnits[unit][1],
				"experiment": "无 " + bulkScheduleMarker, "customPlace": nil,
				"lessonType": "理论 " + bulkScheduleMarker, "weekIndex": week, "exerciseClass": false,
				"startUnit": 1 + unit*2, "endUnit": 2 + unit*2,
				"roomId": 300 + i, "sectionId": 89000 + i, "scheduleGroupId": 400 + i,
				"room":     scheduleFixtureRoom(i),
				"teachers": []any{teacher},
				"teacherParticipations": []any{map[string]any{
					"teacher": teacher, "periods": 2.0, "exerciseClass": false,
				}},
				"scheduleGroup": map[string]any{
					"id": 400 + i, "jwId": 4000 + i, "no": 1, "limitCount": 150, "stdCount": 118,
					"actualPeriods": 54.0, "isDefault": true, "sectionId": 89000 + i,
				},
			})
		}
	}
	return rows
}

// scheduleFixtureSectionDetail mirrors sectionDetailSchema: the section record
// plus every one of its lessons, schedule groups and teacher assignments.
func scheduleFixtureSectionDetail(i int) map[string]any {
	detail := scheduleFixtureSection(i)
	lessons := make([]any, 0)
	for _, row := range scheduleFixtureRows(i) {
		lesson := maps.Clone(row)
		for _, key := range []string{"room", "teachers", "teacherParticipations", "scheduleGroup"} {
			delete(lesson, key)
		}
		lessons = append(lessons, lesson)
	}
	detail["schedules"] = lessons
	detail["scheduleGroups"] = []any{map[string]any{
		"id": 400 + i, "jwId": 4000 + i, "no": 1, "limitCount": 150, "stdCount": 118,
		"actualPeriods": 54.0, "isDefault": true, "sectionId": 89000 + i,
	}}
	detail["teacherAssignments"] = []any{map[string]any{
		"id": 800 + i, "sectionId": 89000 + i, "teacherId": 700 + i, "period": 54.0,
		"role": "主讲 " + bulkScheduleMarker, "weekIndices": []int{1, 2, 3}, "weekIndicesMsg": "1-16周",
	}}
	detail["adminClasses"] = []any{}
	return detail
}

// scheduleFixtureServer serves the schedule endpoints in the shapes described
// by api/openapi.json. served accumulates the bytes of the schedule-bearing
// responses (subscribed schedules, section schedules, section detail), which
// is what the projection is measured against. Section schedules are fetched
// concurrently, so the count is atomic.
func scheduleFixtureServer(t *testing.T, served *atomic.Int64) *httptest.Server {
	t.Helper()
	subscription := scheduleFixtureSubscription()
	semester := scheduleFixtureSemester()
	rowsBySection := make(map[int][]map[string]any, scheduleFixtureSectionCount)
	for i := range scheduleFixtureSectionCount {
		rowsBySection[i] = scheduleFixtureRows(i)
	}
	inRange := func(r *http.Request, row map[string]any) bool {
		date, _ := time.Parse(time.RFC3339, row["date"].(string))
		if from, err := time.Parse(time.RFC3339, r.URL.Query().Get("dateFrom")); err == nil && date.Before(from) {
			return false
		}
		if to, err := time.Parse(time.RFC3339, r.URL.Query().Get("dateTo")); err == nil && date.After(to) {
			return false
		}
		return true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response any
		counted := true
		switch path := r.URL.Path; {
		case path == "/api/workspace/subscriptions/current":
			response, counted = subscription, false
		case path == "/api/catalog/semesters/current":
			response, counted = semester, false
		case path == "/api/catalog/semesters":
			response, counted = map[string]any{
				"data":       []any{semester},
				"pagination": map[string]any{"page": 1, "pageSize": 100, "total": 1, "totalPages": 1},
			}, false
		case path == "/api/workspace/schedules":
			// The subscribed-schedule endpoint embeds the section record in
			// every row.
			rows := make([]map[string]any, 0)
			for i := range scheduleFixtureSectionCount {
				for _, row := range rowsBySection[i] {
					if inRange(r, row) {
						withSection := maps.Clone(row)
						withSection["section"] = scheduleFixtureSection(i)
						rows = append(rows, withSection)
					}
				}
			}
			response = map[string]any{"schedules": rows}
		case strings.HasPrefix(path, "/api/catalog/sections/"):
			jwID, suffix, _ := strings.Cut(strings.TrimPrefix(path, "/api/catalog/sections/"), "/")
			id, err := strconv.Atoi(jwID)
			index := id - 200000
			if err != nil || index < 0 || index >= scheduleFixtureSectionCount {
				http.NotFound(w, r)
				return
			}
			switch suffix {
			case "":
				response = scheduleFixtureSectionDetail(index)
			case "schedules":
				rows := make([]map[string]any, 0)
				for _, row := range rowsBySection[index] {
					if inRange(r, row) {
						rows = append(rows, row)
					}
				}
				response = rows
			default:
				http.NotFound(w, r)
				return
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		if counted {
			served.Add(int64(len(body)))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

type scheduleScenario struct {
	name string
	run  func(Handler) string
	// lessons is the number of lessons the answer covers.
	lessons int
	// budget is the share of the schedule bytes the API answered with that
	// the model may receive.
	budget float64
}

func scheduleScenarios() []scheduleScenario {
	ident := testIdentity()
	// Monday of teaching week 3.
	monday := time.Date(2026, 9, 14, 10, 0, 0, 0, lifedata.ChinaLocation())
	return []scheduleScenario{
		{name: "day", lessons: 5, budget: 0.10, run: func(h Handler) string {
			return h.curriculumAt(context.Background(), ident, []string{"date:2026-09-14"}, monday)
		}},
		{name: "week", lessons: 25, budget: 0.10, run: func(h Handler) string {
			return h.curriculumAt(context.Background(), ident, []string{"week-number:3"}, monday)
		}},
		{name: "semester", lessons: scheduleFixtureLessonsFrom(-1, "2026-09-01"), budget: 0.02, run: func(h Handler) string {
			return h.curriculumAt(context.Background(), ident, []string{"semester:2026-秋"}, monday)
		}},
		{name: "next_class", lessons: 1, budget: 0.05, run: func(h Handler) string {
			return h.nextClassAt(context.Background(), ident, monday)
		}},
		{name: "section_schedules", lessons: scheduleFixtureLessonsFrom(0, "2026-09-01"), budget: 0.15, run: func(h Handler) string {
			return h.sectionSchedules(context.Background(), ident, []string{"200000", "2026-09-01", "2027-01-15"})
		}},
	}
}

// scheduleFixtureLessonsFrom counts the fixture lessons of section (or of
// every section when section < 0) on or after date. Teaching week 1 starts on
// Sunday 08-30, so its Monday lessons precede the 09-01 semester start.
func scheduleFixtureLessonsFrom(section int, date string) int {
	count := 0
	for i := range scheduleFixtureSectionCount {
		if section >= 0 && i != section {
			continue
		}
		for _, row := range scheduleFixtureRows(i) {
			if row["date"].(string)[:10] >= date {
				count++
			}
		}
	}
	return count
}

func scheduleGoldenPath(name string) string {
	return filepath.Join("testdata", "schedule_payload", name+".txt")
}

// runScheduleScenario executes one scenario against a fresh fixture server
// and returns its reply, the model-facing data and the schedule bytes served.
func runScheduleScenario(t *testing.T, scenario scheduleScenario) (string, []byte, int) {
	t.Helper()
	var served atomic.Int64
	server := scheduleFixtureServer(t, &served)
	handler := testAuthedHandler(t, server, testIdentity())
	handler.execution = &capabilityExecutionState{}
	reply := scenario.run(handler)
	if status := handler.execution.status; status != "" && status != CapabilityOutcomeSuccess {
		t.Fatalf("status = %s reply = %q", status, reply)
	}
	encoded, err := json.Marshal(handler.execution.data)
	if err != nil {
		t.Fatal(err)
	}
	return reply, encoded, int(served.Load())
}

// TestScheduleRepliesAreUnchangedByProjection pins every schedule reply to
// the text produced before the model-facing data was projected. The golden
// files were generated on origin/main (the bulk-forwarding code) with
// -update-schedule-golden, so any byte of drift in the reply fails here.
func TestScheduleRepliesAreUnchangedByProjection(t *testing.T) {
	for _, scenario := range scheduleScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			reply, _, _ := runScheduleScenario(t, scenario)
			path := scheduleGoldenPath(scenario.name)
			if *updateScheduleGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(reply), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if reply != string(want) {
				t.Fatalf("reply changed:\n--- want\n%s\n--- got\n%s", want, reply)
			}
		})
	}
}

// TestScheduleCapabilitiesProjectTheBulkRows pins the model-facing size and
// shape of every schedule capability. Each one used to forward the raw rows —
// a full section record, room and teachers per lesson — while printing a time,
// a place and a course name per lesson.
func TestScheduleCapabilitiesProjectTheBulkRows(t *testing.T) {
	for _, scenario := range scheduleScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			_, encoded, served := runScheduleScenario(t, scenario)
			t.Logf("%s: %d lessons, schedule API bytes %d, model-facing data %d chars",
				scenario.name, scenario.lessons, served, len(encoded))
			if strings.Contains(string(encoded), bulkScheduleMarker) || strings.Contains(string(encoded), bulkSubscriptionMarker) {
				t.Fatalf("model-facing data forwarded the bulk schedule rows (%d chars)", len(encoded))
			}
			if budget := int(float64(served) * scenario.budget); len(encoded) > budget {
				t.Fatalf("model-facing data = %d chars, budget = %d chars (schedule API bytes %d)",
					len(encoded), budget, served)
			}
		})
	}
}

// scheduleRowAllowedKeys documents the exact fields a projected schedule row
// may carry, nested object by nested object.
var scheduleRowAllowedKeys = map[string]map[string]struct{}{
	"row":                  keySet("id", "date", "weekday", "weekIndex", "startTime", "endTime", "customPlace", "kind", "room", "section"),
	"room":                 keySet(append(scheduleEntityKeys(), "building")...),
	"room.building":        keySet("campus"),
	"section":              keySet(append(scheduleEntityKeys(), "course", "campus")...),
	"section.course":       keySet(scheduleEntityKeys()...),
	"section.campus":       keySet(scheduleEntityKeys()...),
	"room.building.campus": keySet(scheduleEntityKeys()...),
}

func scheduleEntityKeys() []string {
	return []string{"id", "jwId", "code", "name", "nameCn", "namePrimary", "kind"}
}

func keySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func assertScheduleShape(t *testing.T, path string, value map[string]any) {
	t.Helper()
	allowed, ok := scheduleRowAllowedKeys[path]
	if !ok {
		t.Fatalf("schedule row carried undocumented object %q: %#v", path, value)
	}
	for key, nested := range value {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("%s carried undocumented field %q: %#v", path, key, value)
		}
		if object, ok := nested.(map[string]any); ok {
			child := key
			if path != "row" && path != "" {
				child = path + "." + key
			}
			assertScheduleShape(t, child, object)
		}
	}
}

func decodeScheduleData(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("data = %s: %v", encoded, err)
	}
	return data
}

// TestScheduleDataShipsProjectedRowsOnly pins the projected fields per row and
// that each row still carries what its reply line printed: time, place, course
// identity and the personal membership kind.
func TestScheduleDataShipsProjectedRowsOnly(t *testing.T) {
	for _, scenario := range scheduleScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			reply, encoded, _ := runScheduleScenario(t, scenario)
			data := decodeScheduleData(t, encoded)
			if scenario.name == "semester" {
				assertSemesterEntries(t, reply, data)
				return
			}
			var rows []any
			if scenario.name == "next_class" {
				row, ok := data["schedule"].(map[string]any)
				if !ok || data["operation"] != "next_class" {
					t.Fatalf("data = %s", encoded)
				}
				rows = []any{row}
			} else {
				rows, _ = data["schedules"].([]any)
			}
			if len(rows) != scenario.lessons {
				t.Fatalf("rows = %d, want %d", len(rows), scenario.lessons)
			}
			kinds := map[string]bool{}
			for _, raw := range rows {
				row := raw.(map[string]any)
				assertScheduleShape(t, "row", row)
				if scenario.name == "section_schedules" {
					// The section is identical for every lesson, so it is
					// recorded once beside the rows rather than in each.
					if _, ok := row["section"]; ok {
						t.Fatalf("section schedule row repeated the section: %#v", row)
					}
					section, ok := data["section"].(map[string]any)
					if !ok {
						t.Fatalf("section schedules lost the section: %s", encoded)
					}
					assertScheduleShape(t, "section", section)
					row = maps.Clone(row)
					row["section"] = section
				}
				if lifedata.ScheduleTimeRange(row) == "" || lifedata.SchedulePlaceLabel(row) == "" || lifedata.ScheduleCourseLabel(row) == "" {
					t.Fatalf("projected row lost a printed field: %#v", row)
				}
				if lifedata.FirstString(row, "date") == "" || lifedata.FirstInt(row, "weekIndex") <= 0 {
					t.Fatalf("projected row lost its date coordinates: %#v", row)
				}
				kinds[lifedata.MembershipKind(row)] = true
			}
			if scenario.name == "week" {
				for _, kind := range []string{lifedata.SubscriptionKindRegular, lifedata.SubscriptionKindAuditor, lifedata.SubscriptionKindTeachingAssistant} {
					if !kinds[kind] {
						t.Fatalf("projected rows lost membership kind %q: %v", kind, kinds)
					}
				}
			}
			if semester, ok := data["semester"].(map[string]any); ok {
				for key := range semester {
					if _, ok := keySet("id", "jwId", "code", "name", "nameCn", "nameEn", "namePrimary", "nameSecondary", "kind", "startDate", "endDate")[key]; !ok {
						t.Fatalf("semester carried undocumented field %q: %#v", key, semester)
					}
				}
			}
		})
	}
}

// assertSemesterEntries pins the semester view: it records the weekly slots
// its reply prints, each with its teaching weeks, instead of every dated
// lesson of the semester.
func assertSemesterEntries(t *testing.T, reply string, data map[string]any) {
	t.Helper()
	if _, ok := data["schedules"]; ok {
		t.Fatalf("semester data still carries per-lesson rows: %#v", data)
	}
	entries, _ := data["entries"].([]any)
	// One reply line per slot: every line that ends with its teaching weeks.
	printed := 0
	for _, line := range strings.Split(reply, "\n") {
		if strings.Count(line, "\t") == 3 {
			printed++
		}
	}
	if printed == 0 || len(entries) != printed {
		t.Fatalf("entries = %d, reply slot lines = %d", len(entries), printed)
	}
	allowed := keySet("weekday", "startTime", "endTime", "course", "place", "weeks", "section")
	kinds := map[string]bool{}
	for _, raw := range entries {
		entry := raw.(map[string]any)
		for key := range entry {
			if _, ok := allowed[key]; !ok {
				t.Fatalf("semester entry carried undocumented field %q: %#v", key, entry)
			}
		}
		section, ok := entry["section"].(map[string]any)
		if !ok {
			t.Fatalf("semester entry lost its section: %#v", entry)
		}
		assertScheduleShape(t, "section", section)
		kinds[lifedata.FirstString(section, "kind")] = true
		weeks, _ := entry["weeks"].([]any)
		if len(weeks) == 0 || entry["course"] == "" || entry["place"] == "" {
			t.Fatalf("semester entry lost a printed field: %#v", entry)
		}
		line := strings.Join([]string{
			textutil.MonospaceASCII(entry["place"].(string)),
			textutil.MonospaceDigits(entry["startTime"].(string) + "-" + entry["endTime"].(string)),
			entry["course"].(string),
		}, "\t")
		if !strings.Contains(reply, line) {
			t.Fatalf("semester entry %q is not a reply line", line)
		}
	}
	for _, kind := range []string{lifedata.SubscriptionKindRegular, lifedata.SubscriptionKindAuditor, lifedata.SubscriptionKindTeachingAssistant} {
		if !kinds[kind] {
			t.Fatalf("semester entries lost membership kind %q: %v", kind, kinds)
		}
	}
}

// TestCompactScheduleKeepsEveryPrintedField covers the place-label branches
// the fixture week does not reach. A projected row must format to exactly the
// line its raw row formats to.
func TestCompactScheduleKeepsEveryPrintedField(t *testing.T) {
	campus := map[string]any{"id": 1, "nameCn": "西区", "namePrimary": "西区", "nameEn": "West " + bulkScheduleMarker}
	section := map[string]any{
		"id": 7, "jwId": 70, "code": "CS1001.01", "remark": bulkScheduleMarker,
		"campus": campus,
		"course": map[string]any{"id": 8, "nameCn": "数据结构", "namePrimary": "数据结构", "nameEn": bulkScheduleMarker},
	}
	for _, test := range []struct {
		name string
		row  map[string]any
	}{
		{name: "custom place", row: map[string]any{
			"startTime": "08:00", "endTime": "09:35", "customPlace": "线上 腾讯会议", "section": section,
			"room": map[string]any{"namePrimary": "3A204", "remark": bulkScheduleMarker},
		}},
		{name: "room without campus falls back to section campus", row: map[string]any{
			"startTime": "08:00", "endTime": "09:35", "section": section,
			"room": map[string]any{"id": 2, "namePrimary": "3A204", "seats": 120, "remark": bulkScheduleMarker},
		}},
		{name: "room already prefixed by campus", row: map[string]any{
			"startTime": "08:00", "endTime": "09:35", "section": section,
			"room": map[string]any{"nameCn": "西区 3A204", "building": map[string]any{"code": bulkScheduleMarker, "campus": campus}},
		}},
		{name: "room code only", row: map[string]any{
			"startTime": "08:00", "endTime": "09:35", "section": map[string]any{"code": "CS1001.01"},
			"room": map[string]any{"code": "3A204", "building": map[string]any{"campus": map[string]any{"code": "WEST"}}},
		}},
		{name: "no room uses section campus", row: map[string]any{"startTime": "08:00", "endTime": "09:35", "section": section}},
		{name: "row level kind", row: map[string]any{
			"startTime": "08:00", "endTime": "09:35", "kind": "auditor", "section": section,
		}},
		{name: "section kind", row: map[string]any{
			"startTime": "08:00", "section": map[string]any{"code": "CS1001.01", "kind": "teaching_assistant", "remark": bulkScheduleMarker},
		}},
		{name: "fallback label", row: map[string]any{"id": 42, "periods": 2, "lessonType": bulkScheduleMarker}},
		{name: "fallback label from section", row: map[string]any{"section": map[string]any{"id": 7, "remark": bulkScheduleMarker}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			compact := compactSchedule(test.row)
			if got, want := formatSchedule(compact), formatSchedule(test.row); got != want {
				t.Fatalf("projected line = %q, raw line = %q", got, want)
			}
			encoded, err := json.Marshal(compact)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), bulkScheduleMarker) {
				t.Fatalf("projection kept an unprinted field: %s", encoded)
			}
			assertScheduleShape(t, "row", compact)
		})
	}
}
