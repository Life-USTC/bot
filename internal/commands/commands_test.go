package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestHandleCourseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "/life course calculus"})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷      \tCalculus") {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestHandleCasualCourseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "数学分析" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1006","namePrimary":"数学分析"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程 数学分析"})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟼      \t数学分析") {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestHandleGroupOnlyAllowsBusKeywords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bus" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"routes":[
				{"id":1,"stops":[
					{"campus":{"nameCn":"东区"}},
					{"campus":{"nameCn":"北区"}},
					{"campus":{"nameCn":"西区"}}
				]}
			],
			"trips":[
				{"routeId":1,"dayType":"weekday","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]},
				{"routeId":1,"dayType":"weekend","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]}
			]
		}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	groupInput := Input{
		Text: "东区到西区校车还有吗",
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "group",
			ConversationID:   "100",
		},
	}
	reply, ok := handler.Handle(context.Background(), groupInput)
	if !ok {
		t.Fatal("group bus message was not handled")
	}
	if !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿  →  北区\u3000 ———  →  西区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("reply = %q", reply)
	}

	groupInput.Text = "/life td"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group personal command reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,summary=&#91;动画表情&#93;,file=1.png,sub_type=1,url=https://example.invalid/download?rkey=CAQSMJSxCxAi3h4QEhInHuJOdWi5QXU7]"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group image reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,file=1.png] 校车"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if !ok || !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("group image caption reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"/help", "/?", "帮助", "/life -h"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "待办 / td") {
			t.Fatalf("unexpected reply for %q: %q", text, reply)
		}
	}
}

func TestCommandSpecsAreUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range CommandSpecs() {
		if spec.Name == "" {
			t.Fatal("command spec has empty name")
		}
		if spec.Run == nil {
			t.Fatalf("command %q has nil Run", spec.Name)
		}
		if len(spec.Aliases) == 0 {
			t.Fatalf("command %q has no aliases", spec.Name)
		}
		if seen[spec.Name] {
			t.Fatalf("duplicate command spec %q", spec.Name)
		}
		seen[spec.Name] = true
		name, _ := normalizeCommand(spec.Aliases[0], nil)
		if name != spec.Name {
			t.Fatalf("alias %q normalized to %q, want %q", spec.Aliases[0], name, spec.Name)
		}
	}
	for _, name := range []string{"todo", "homework", "schedule", "notify", "bus"} {
		if !seen[name] {
			t.Fatalf("missing command spec %q", name)
		}
	}
}

func TestHandleTodoHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"待办 -h", "td help", "/life todo --help"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "待办 add 写报告") {
			t.Fatalf("unexpected reply for %q: %q", text, reply)
		}
	}
}

func TestHandleTodoAddCasual(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"todo-1"}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td 写报告", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if gotBody["title"] != "写报告" || !strings.Contains(reply, "已加待办：写报告") {
		t.Fatalf("body = %#v, reply = %q", gotBody, reply)
	}
}

func TestLoginMentionsAutomaticPoll(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"token_endpoint":                serverURL + "/token",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device",
			"user_code":                 "USER-CODE",
			"verification_uri":          serverURL + "/verify",
			"verification_uri_complete": serverURL + "/verify?user_code=USER-CODE",
			"expires_in":                300,
			"interval":                  10,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	handler := Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Store:  s,
		Prefix: "/life",
	}
	reply, ok := handler.Handle(ctx, Input{Text: "登录", Identity: ident})
	if !ok {
		t.Fatal("login was not handled")
	}
	if !strings.Contains(reply, "系统将自动检查登录状态") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleTodoDoneByIndex(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			if r.URL.Query().Get("completed") != "false" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","dueAt":"2026-05-14T23:55:00+08:00"},{"id":"todo-2","title":"买咖啡"}]}`))
		case r.URL.Path == "/api/todos/todo-1" && r.Method == http.MethodPatch:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"completed":true`) {
				t.Fatalf("patch body = %s", body)
			}
			patched = true
			_, _ = w.Write([]byte(`{"id":"todo-1","completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td done 1", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !patched || !strings.Contains(reply, "已完成：写报告") {
		t.Fatalf("patched = %v, reply = %q", patched, reply)
	}
}

func TestFormatTodoDueDateFirst(t *testing.T) {
	line := monospaceDigits(formatTodo(map[string]any{
		"title": "写报告",
		"dueAt": "2026-05-14T23:55:00+08:00",
	}))
	if line != "截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻 写报告" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatNumberedLinePadsBeforeTab(t *testing.T) {
	line := formatNumberedLine(1, "截止 05-14 23:55 写报告")
	if line != "𝟷. \t截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻 写报告" {
		t.Fatalf("line = %q", line)
	}
}

func TestHandleHomeworkListAndDone(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/me/subscriptions/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null},{"id":"hw-2","title":"Old PS","submissionDueAt":"2026-05-01T12:00:00+08:00","section":{"course":{"namePrimary":"组合数学"}},"completion":null}]}`))
		case r.URL.Path == "/api/homeworks/hw-1/completion" && r.Method == http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			completed = body["completed"] == true
			_, _ = w.Write([]byte(`{"completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "作业", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "已逾期：") || !strings.Contains(reply, "截止 𝟶𝟼-𝟶𝟹 𝟷𝟸:𝟶𝟶 · 数据库系统 · Problem Set 𝟷") {
		t.Fatalf("reply = %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "作业 done 2", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !completed || !strings.Contains(reply, "已完成作业：Problem Set 1") {
		t.Fatalf("completed = %v, reply = %q", completed, reply)
	}
}

func TestHandleTodayCurriculum(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.URL.Path == "/api/schedules":
			if r.URL.Query().Get("sectionId") != "101" {
				t.Fatalf("sectionId = %q", r.URL.Query().Get("sectionId"))
			}
			if !strings.HasSuffix(r.URL.Query().Get("dateFrom"), "Z") || !strings.HasSuffix(r.URL.Query().Get("dateTo"), "Z") {
				t.Fatalf("date range = %q %q", r.URL.Query().Get("dateFrom"), r.URL.Query().Get("dateTo"))
			}
			_, _ = w.Write([]byte(`{"data":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "今天课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "西区 𝟹𝙰𝟸𝟶𝟺\t𝟶𝟿:𝟻𝟶-𝟷𝟷:𝟸𝟻\t数据库系统") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleBareCurriculumShowsTodayAndTomorrow(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	scheduleCalls := 0
	today := time.Now().In(chinaLocation()).Format("2006-01-02")
	tomorrow := time.Now().In(chinaLocation()).AddDate(0, 0, 1).Format("2006-01-02")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.URL.Path == "/api/schedules":
			scheduleCalls++
			if scheduleCalls == 1 {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`, today)))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}},"room":{"namePrimary":"GT-B112"}}]}`, tomorrow)))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "今明两日课表：") || !strings.Contains(reply, "今天：") || !strings.Contains(reply, "明天：") {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(reply, "数据库系统") || !strings.Contains(reply, "编译原理") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestSubscriptionHelpDoesNotList(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "订阅 help", Identity: testIdentity()})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "订阅 导入") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNotificationSettingsCommand(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	handler := Handler{Store: s, Prefix: "/life"}

	reply, ok := handler.Handle(ctx, Input{Text: "通知", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：关") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 课表 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 作业 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：开") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestSubscriptionListGroupsBySemester(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/calendar-subscriptions/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"sections":[
			{"id":101,"code":"CONT5103P.01","course":{"namePrimary":"随机过程理论"},"semester":{"nameCn":"2026年春季学期"}},
			{"id":102,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"},"semester":{"nameCn":"2026年春季学期"}},
			{"id":201,"code":"MATH1001.01","course":{"namePrimary":"数学分析"},"semester":{"nameCn":"2025年秋季学期"}}
		]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{"日程订阅：", "2026年春季学期：", "2025年秋季学期：", "- 𝙲𝙾𝙽𝚃𝟻𝟷𝟶𝟹𝙿.𝟶𝟷  \t随机过程理论", "- 𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷   \t数学分析"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Index(reply, "2026年春季学期：") > strings.Index(reply, "2025年秋季学期：") {
		t.Fatalf("semester order changed: %q", reply)
	}
	if strings.Contains(reply, "...and") {
		t.Fatalf("reply should not be folded: %q", reply)
	}
}

func TestBulkSubscribeSectionsAddsMatchedSections(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var replacedIDs []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101,"code":"CONT5103P.01"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/sections/match-codes":
			var req struct {
				Codes []string `json:"codes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if strings.Join(req.Codes, ",") != "CONT5103P.01,CONT6104P.01,BAD000.01" {
				t.Fatalf("codes = %#v", req.Codes)
			}
			_, _ = w.Write([]byte(`{
				"semester":{"nameCn":"2026年春季学期"},
				"sections":[
					{"id":101,"code":"CONT5103P.01","course":{"namePrimary":"随机过程理论"}},
					{"id":202,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"}}
				],
				"unmatchedCodes":["BAD000.01"]
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/calendar-subscriptions":
			var req struct {
				SectionIDs []int `json:"sectionIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			replacedIDs = req.SectionIDs
			_, _ = w.Write([]byte(`{"subscription":{"sections":[]}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅 导入 cont5103p.01, CONT6104P.01 BAD000.01", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if strings.Join(intStrings(replacedIDs), ",") != "101,202" {
		t.Fatalf("sectionIds = %#v", replacedIDs)
	}
	for _, want := range []string{"已订阅 𝟸 个教学班（新增 𝟷 个，已存在 𝟷 个）。", "2026年春季学期", "𝙲𝙾𝙽𝚃𝟼𝟷𝟶𝟺𝙿.𝟶𝟷  \t组合数学", "BAD000.01"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestFormatScheduleLocationFirstAndFixedWidth(t *testing.T) {
	line := formatSchedule(map[string]any{
		"startTime":   "07:50",
		"endTime":     "09:25",
		"customPlace": "GT-A405",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "随机过程理论"},
		},
	})
	if line != "𝙶𝚃-𝙰𝟺𝟶𝟻 \t𝟶𝟽:𝟻𝟶-𝟶𝟿:𝟸𝟻\t随机过程理论" {
		t.Fatalf("line = %q", line)
	}
}

func TestSubscriptionSectionIDsForDayFiltersSemester(t *testing.T) {
	data := map[string]any{
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{
					"id": "current",
					"semester": map[string]any{
						"startDate": "2026-03-01T08:00:00+08:00",
						"endDate":   "2026-07-03T08:00:00+08:00",
					},
				},
				map[string]any{
					"id": "old",
					"semester": map[string]any{
						"startDate": "2025-09-07T08:00:00+08:00",
						"endDate":   "2026-01-23T08:00:00+08:00",
					},
				},
			},
		},
	}
	day := time.Date(2026, 6, 2, 12, 0, 0, 0, chinaLocation())
	ids := subscriptionSectionIDsForDay(data, day)
	if len(ids) != 1 || ids[0] != "current" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestFilterSchedulesForDayDropsAdjacentDates(t *testing.T) {
	day := time.Date(2026, 6, 2, 12, 0, 0, 0, chinaLocation())
	schedules := []map[string]any{
		{"date": "2026-06-01T08:00:00+08:00", "startTime": "07:50"},
		{"date": "2026-06-02T08:00:00+08:00", "startTime": "09:45"},
	}
	filtered := filterSchedulesForDay(schedules, day)
	if len(filtered) != 1 || firstString(filtered[0], "startTime") != "09:45" {
		t.Fatalf("filtered = %#v", filtered)
	}
}

func TestNextBusItemsFiltersRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "北区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:00", "departureMinutes": float64(540), "arrivalTime": "09:15"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:20" || items[0].Route != "东区 → 北区 → 西区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusByRouteReturnsOneTripPerRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:10", "departureMinutes": float64(550), "arrivalTime": "09:25"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "10:10", "departureMinutes": float64(610), "arrivalTime": "10:25"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "东区 → 西区" || items[0].DepartureTime != "09:10" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].Route != "西区 → 东区" || items[1].DepartureTime != "09:30" {
		t.Fatalf("second item = %#v", items[1])
	}
}

func TestNextBusByRouteSortsByDepartureCampus(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:05", "departureMinutes": float64(545), "arrivalTime": "09:20"},
			map[string]any{
				"routeId":          float64(1),
				"dayType":          "weekday",
				"departureTime":    "09:30",
				"departureMinutes": float64(570),
				"arrivalTime":      "09:45",
				"stopTimes": []any{
					map[string]any{"campusName": "东区", "time": "09:30"},
					map[string]any{"campusName": "北区"},
					map[string]any{"campusName": "西区", "time": "09:45"},
				},
			},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureCampus != "东区" || items[1].DepartureCampus != "西区" {
		t.Fatalf("items = %#v", items)
	}
	lines := formatBusItemsByDepartureCampus(items, 8)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区\u3000 𝟶𝟿:𝟹𝟶  →  北区\u3000 ———  →  西区\u3000 𝟶𝟿:𝟺𝟻\n\n西区\u3000 𝟶𝟿:𝟶𝟻") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestFormatBusItemsNoLimitShowsAllRoutes(t *testing.T) {
	items := []busItem{
		{DepartureCampus: "东区", Stops: []busStop{{Name: "东区", Time: "09:00"}}},
		{DepartureCampus: "西区", Stops: []busStop{{Name: "西区", Time: "09:05"}}},
	}

	lines := formatBusItemsByDepartureCampus(items, 0)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区") || !strings.Contains(got, "西区") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestNormalizeCommandAliases(t *testing.T) {
	tests := map[string]string{
		"待办": "todo",
		"代办": "todo",
		"td": "todo",
		"校车": "bus",
		"xc": "bus",
		"日程": "schedule",
		"rc": "schedule",
		"kb": "schedule",
		"状态": "status",
		"zt": "status",
	}
	handler := Handler{Prefix: "/life"}
	for text, want := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != want {
			t.Fatalf("%q parsed as %q, want %q", text, cmd.Name, want)
		}
	}
}

func TestNormalizeScheduleTypos(t *testing.T) {
	tests := map[string][]string{
		"课标":   {},
		"今天课标": {"today"},
		"今日课标": {"today"},
		"明天课标": {"tomorrow"},
		"明日课标": {"tomorrow"},
	}
	handler := Handler{Prefix: "/life"}
	for text, wantArgs := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "schedule" {
			t.Fatalf("%q parsed as %q, want schedule", text, cmd.Name)
		}
		if strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, cmd.Args, wantArgs)
		}
	}
}

func intStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

func TestHandleSuppressLog(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nameCn":"2026年春季学期"}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "学期", Identity: ident, SuppressLog: true})
	if !ok || reply == "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := handler.Store.InteractionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("interaction count = %d", count)
	}
}

func testAuthedHandler(t *testing.T, server *httptest.Server, ident store.Identity) Handler {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	err = s.SaveCredential(context.Background(), ident, store.Credential{
		ClientID:    "client",
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
		Resource:    server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Store:  s,
		Prefix: "/life",
	}
}

func testIdentity() store.Identity {
	return store.Identity{
		Platform:         "napcat",
		UserID:           "42",
		ConversationType: "private",
		ConversationID:   "42",
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "hello"})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
