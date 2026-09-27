package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/textutil"
)

// examLineText applies the typography an exam line goes through, so an
// expectation can be written as the plain text a reader sees.
func examLineText(value string) string {
	return textutil.MonospaceASCII(textutil.MonospaceDigits(value))
}

// bulkSubscriptionMarker only ever appears in fields that belong to the raw jw
// section record. A model-facing payload that contains it has forwarded the
// bulk current-subscription response instead of a projection of it.
const bulkSubscriptionMarker = "bulk-section-record-marker"

// subscriptionFixtureSection mirrors one entry of
// GET /api/workspace/subscriptions/current as described by api/openapi.json:
// the complete jw teaching-section record, not the handful of fields a reply
// prints. Every capability that reads this endpoint must project it down.
func subscriptionFixtureSection(i int) map[string]any {
	named := func(id int, cn, en string) map[string]any {
		return map[string]any{"id": id, "nameCn": cn, "nameEn": en, "namePrimary": cn, "nameSecondary": en}
	}
	section := map[string]any{
		"id": 89000 + i, "jwId": 200000 + i, "retiredAt": nil,
		"code":                    fmt.Sprintf("MATH%04d.%02d", 1000+i, (i%4)+1),
		"bizTypeId":               1,
		"credits":                 3.5,
		"period":                  54,
		"periodsPerWeek":          3.0,
		"timesPerWeek":            2,
		"stdCount":                120,
		"limitCount":              150,
		"graduateAndPostgraduate": false,
		"dateTimePlaceText":       "1-16周 星期一第1,2节 五教5102 " + bulkSubscriptionMarker,
		"dateTimePlacePersonText": nil,
		"actualPeriods":           54.0,
		"theoryPeriods":           54.0,
		"practicePeriods":         0.0,
		"experimentPeriods":       0.0,
		"machinePeriods":          0.0,
		"designPeriods":           0.0,
		"testPeriods":             0.0,
		"scheduleState":           "SCHEDULED",
		"suggestScheduleWeeks":    nil,
		"suggestScheduleWeekInfo": "1-16周",
		"scheduleJsonParams":      nil,
		"selectedStdCount":        118,
		"remark":                  "限本专业选修 " + bulkSubscriptionMarker,
		"scheduleRemark":          "",
		"courseId":                5000 + i,
		"semesterId":              1,
		"campusId":                2,
		"examModeId":              3,
		"openDepartmentId":        11,
		"teachLanguageId":         1,
		"roomTypeId":              4,
		"course": map[string]any{
			"id": 5000 + i, "jwId": 6000 + i, "code": fmt.Sprintf("MATH%04d", 1000+i),
			"nameCn":      fmt.Sprintf("数学分析（B%d）", (i%3)+1),
			"nameEn":      fmt.Sprintf("Mathematical Analysis B%d", (i%3)+1),
			"namePrimary": fmt.Sprintf("数学分析（B%d）", (i%3)+1),
			"categoryId":  1, "classTypeId": 2, "classifyId": 3, "educationLevelId": 4,
			"gradationId": 5, "typeId": 6,
			"nameSecondary":  fmt.Sprintf("Mathematical Analysis B%d", (i%3)+1),
			"category":       named(1, "公共基础课 "+bulkSubscriptionMarker, "General Foundation Course"),
			"classType":      named(2, "理论课 "+bulkSubscriptionMarker, "Theory Course"),
			"classify":       named(3, "必修课 "+bulkSubscriptionMarker, "Compulsory Course"),
			"educationLevel": named(4, "本科生 "+bulkSubscriptionMarker, "Undergraduate"),
			"gradation":      named(5, "一年级 "+bulkSubscriptionMarker, "First Year"),
			"type":           named(6, "校统一开设课程 "+bulkSubscriptionMarker, "University Wide Course"),
		},
		"semester": map[string]any{
			"id": 1, "jwId": 202601, "nameCn": "2026年秋季学期", "code": "2026F",
			"startDate": "2026-09-01", "endDate": "2027-01-15",
		},
		"campus": map[string]any{
			"id": 2, "jwId": 20, "nameCn": "东校区 " + bulkSubscriptionMarker, "nameEn": "East Campus",
			"code": "EAST", "namePrimary": "东校区", "nameSecondary": "East Campus",
		},
		"openDepartment": map[string]any{
			"id": 11, "jwId": 110, "code": "MATH", "nameCn": "数学科学学院 " + bulkSubscriptionMarker,
			"nameEn": "School of Mathematical Sciences", "isCollege": true,
			"namePrimary": "数学科学学院", "nameSecondary": "School of Mathematical Sciences",
		},
		"teachers": []any{
			map[string]any{
				"id": 700 + i, "jwId": 7000 + i, "personId": 70000 + i,
				"code": fmt.Sprintf("T%05d", i), "nameCn": "张教授", "nameEn": "Prof. Zhang",
				"namePrimary": "张教授", "nameSecondary": "Prof. Zhang",
			},
		},
		"kind": "regular",
	}
	if i%3 == 0 {
		section["exams"] = []any{map[string]any{
			"id": 9000 + i, "jwId": 99000 + i,
			"examDate":  fmt.Sprintf("2027-01-%02dT00:00:00+08:00", (i%20)+1),
			"startTime": "0830", "endTime": "1030",
			"examMode": "闭卷", "examModeId": 3,
			"examRooms": []any{
				map[string]any{"id": 1, "room": "东区 五教 5102", "namePrimary": "东区 五教 5102"},
				map[string]any{"id": 2, "room": "东区 五教 5103", "namePrimary": "东区 五教 5103"},
			},
		}}
	} else {
		section["exams"] = []any{}
	}
	return section
}

const subscriptionFixtureCalendarURL = "https://life.ustc.edu.cn/api/workspace/calendar/ics/abcdef0123456789"

func subscriptionFixtureBody(sectionCount int) map[string]any {
	sections := make([]any, 0, sectionCount)
	for i := range sectionCount {
		sections = append(sections, subscriptionFixtureSection(i))
	}
	return map[string]any{"subscription": map[string]any{
		"userId":       "u-1001",
		"sections":     sections,
		"note":         "",
		"calendarPath": "/api/workspace/calendar/ics/abcdef0123456789",
		"calendarUrl":  subscriptionFixtureCalendarURL,
	}}
}

func subscriptionFixtureServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestSubscriptionReadCapabilitiesProjectTheBulkRecord pins the model-facing
// size and shape of every read capability backed by the current-subscription
// endpoint. Each one used to forward the whole response — hundreds of KB of jw
// scheduling metadata — while printing a few fields of it.
func TestSubscriptionReadCapabilitiesProjectTheBulkRecord(t *testing.T) {
	const sectionCount = 75
	body := subscriptionFixtureBody(sectionCount)
	rawAPIBody, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	server := subscriptionFixtureServer(t, body)
	ident := testIdentity()

	for _, test := range []struct {
		name string
		id   CapabilityID
		args []string
		// budget is the share of the raw API response the model may receive.
		budget float64
		// replyContains keeps the projection from silently changing the answer.
		replyContains []string
	}{
		{
			name: "subscription list", id: CapabilitySubscription, budget: 0.25,
			replyContains: []string{textutil.MonospaceASCII("MATH1000.01"), "数学分析（B1）", "2026年秋季学期", "JW ID 200000"},
		},
		{
			name: "my_subscribed_sections", id: CapabilityMySubscribedSections, budget: 0.25,
			replyContains: []string{textutil.MonospaceASCII("MATH1000.01"), "数学分析（B1）", "2026年秋季学期", "JW ID 200000"},
		},
		{
			name: "exam", id: CapabilityExam, budget: 0.10,
			replyContains: []string{examLineText("01-01"), examLineText("08:30-10:30"), "闭卷", examLineText("五教 5102")},
		},
		{
			name: "subscription link", id: CapabilitySubscription, args: []string{"link"}, budget: 0.005,
			replyContains: []string{subscriptionFixtureCalendarURL},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := testAuthedHandler(t, server, ident)
			outcome, err := handler.ExecuteCapability(t.Context(), Input{Identity: ident}, test.id, test.args)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != CapabilityOutcomeSuccess {
				t.Fatalf("status = %s response = %q", outcome.Status, outcome.Response.Text)
			}
			for _, want := range test.replyContains {
				if !strings.Contains(outcome.Response.Text, want) {
					t.Fatalf("reply lost %q:\n%s", want, outcome.Response.Text)
				}
			}
			encoded, err := json.Marshal(outcome.Response.Data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), bulkSubscriptionMarker) {
				t.Fatalf("model-facing data forwarded the bulk section record (%d chars)", len(encoded))
			}
			if budget := int(float64(len(rawAPIBody)) * test.budget); len(encoded) > budget {
				t.Fatalf("model-facing data = %d chars, budget = %d chars (API body %d chars)",
					len(encoded), budget, len(rawAPIBody))
			}
		})
	}
}

// TestSubscriptionCalendarLinkShipsOnlyTheLink pins the most extreme case: the
// capability answers with a single URL, so the private calendar credential is
// the only subscription field it may carry.
func TestSubscriptionCalendarLinkShipsOnlyTheLink(t *testing.T) {
	server := subscriptionFixtureServer(t, subscriptionFixtureBody(12))
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)

	outcome, err := handler.ExecuteCapability(t.Context(), Input{Identity: ident}, CapabilitySubscription, []string{"link"})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := outcome.Response.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", outcome.Response.Data)
	}
	if len(data) != 2 || data["operation"] != "calendar_link" || data["calendar_url"] != subscriptionFixtureCalendarURL {
		t.Fatalf("calendar link data = %#v", data)
	}
}

// TestSubscriptionListShipsProjectedSectionsOnly documents the exact fields a
// subscribed-section list may carry, and that the private calendar URL is not
// one of them: only the calendar-link capability answers with that credential.
func TestSubscriptionListShipsProjectedSectionsOnly(t *testing.T) {
	const sectionCount = 12
	server := subscriptionFixtureServer(t, subscriptionFixtureBody(sectionCount))
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)

	outcome, err := handler.ExecuteCapability(t.Context(), Input{Identity: ident}, CapabilityMySubscribedSections, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := outcome.Response.Data.(map[string]any)
	if !ok || data["operation"] != "list" {
		t.Fatalf("data = %#v", outcome.Response.Data)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), subscriptionFixtureCalendarURL) {
		t.Fatal("subscribed-section list leaked the private calendar URL")
	}
	subscription, ok := data["subscription"].(map[string]any)
	if !ok || len(subscription) != 1 {
		t.Fatalf("subscription = %#v", data["subscription"])
	}
	sections, ok := subscription["sections"].([]map[string]any)
	if !ok || len(sections) != sectionCount {
		t.Fatalf("sections = %#v", subscription["sections"])
	}
	allowed := map[string]struct{}{
		"id": {}, "jwId": {}, "code": {}, "kind": {},
		"name": {}, "nameCn": {}, "nameEn": {}, "namePrimary": {}, "nameSecondary": {},
		"course": {}, "semester": {}, "teacher": {}, "teachers": {},
	}
	for _, section := range sections {
		for key := range section {
			if _, ok := allowed[key]; !ok {
				t.Fatalf("section carried undocumented field %q: %#v", key, section)
			}
		}
	}
	if sections[0]["code"] != "MATH1000.01" || sections[0]["kind"] != "regular" {
		t.Fatalf("section identity = %#v", sections[0])
	}
	if course, ok := sections[0]["course"].(map[string]any); !ok || course["namePrimary"] != "数学分析（B1）" {
		t.Fatalf("section course = %#v", sections[0]["course"])
	}
}

// TestSubscriptionExamsShipExamsWithoutTheirSectionRecord pins that the exam
// capability answers with exams, not with the 75-section subscription that
// merely carried them.
func TestSubscriptionExamsShipExamsWithoutTheirSectionRecord(t *testing.T) {
	const sectionCount = 12
	server := subscriptionFixtureServer(t, subscriptionFixtureBody(sectionCount))
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)

	outcome, err := handler.ExecuteCapability(t.Context(), Input{Identity: ident}, CapabilityExam, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := outcome.Response.Data.(map[string]any)
	if !ok || data["operation"] != "exams" {
		t.Fatalf("data = %#v", outcome.Response.Data)
	}
	if _, ok := data["subscription"]; ok {
		t.Fatalf("exam data retained the whole subscription: %#v", data)
	}
	exams, ok := data["exams"].([]map[string]any)
	if !ok {
		t.Fatalf("exams = %#v", data["exams"])
	}
	// Every third fixture section carries one exam.
	if want := (sectionCount + 2) / 3; len(exams) != want {
		t.Fatalf("exams = %d, want %d", len(exams), want)
	}
	allowed := map[string]struct{}{
		"id": {}, "jwId": {}, "examDate": {}, "date": {},
		"startTime": {}, "endTime": {}, "examMode": {}, "examRooms": {}, "section": {},
	}
	for _, exam := range exams {
		for key := range exam {
			if _, ok := allowed[key]; !ok {
				t.Fatalf("exam carried undocumented field %q: %#v", key, exam)
			}
		}
	}
	first := exams[0]
	if first["examMode"] != "闭卷" || first["startTime"] != "0830" {
		t.Fatalf("exam detail = %#v", first)
	}
	rooms, ok := first["examRooms"].([]map[string]any)
	if !ok || len(rooms) != 2 || rooms[0]["room"] != "东区 五教 5102" {
		t.Fatalf("exam rooms = %#v", first["examRooms"])
	}
	section, ok := first["section"].(map[string]any)
	if !ok || section["code"] != "MATH1000.01" {
		t.Fatalf("exam section = %#v", first["section"])
	}
	if _, ok := section["teachers"]; ok {
		t.Fatalf("exam section carried teachers it never prints: %#v", section)
	}
	// Exams are answered in display order, so the recorded data matches the reply.
	if exams[0]["examDate"].(string) > exams[len(exams)-1]["examDate"].(string) {
		t.Fatalf("exam data is not in display order: %#v", exams)
	}
}

// TestSubscribedSectionProbeRecordsOnlyItsCount covers the internal probe used
// when a day has no schedules. It answers a yes/no question, so it must not
// record the subscription record it loaded to answer it.
func TestSubscribedSectionProbeRecordsOnlyItsCount(t *testing.T) {
	const sectionCount = 9
	server := subscriptionFixtureServer(t, subscriptionFixtureBody(sectionCount))
	ident := testIdentity()
	handler := testAuthedHandler(t, server, ident)
	handler.execution = &capabilityExecutionState{}

	has, err := handler.hasSubscribedSections(t.Context(), ident, "access")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("fixture subscription should report subscribed sections")
	}
	encoded, err := json.Marshal(handler.execution.data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), bulkSubscriptionMarker) {
		t.Fatalf("subscription probe recorded the bulk section record (%d chars)", len(encoded))
	}
	data, ok := handler.execution.data.(map[string]any)
	if !ok || data["subscribed_section_count"] != sectionCount {
		t.Fatalf("probe data = %#v", handler.execution.data)
	}
}
