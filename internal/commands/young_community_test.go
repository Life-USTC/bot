package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestYoungCommandHierarchyAndPrivacy(t *testing.T) {
	tests := []struct {
		input  string
		id     CapabilityID
		args   []string
		public bool
	}{
		{input: "第二课堂 主办方 查看 org-1", id: CapabilityYoungOrganizer, args: []string{"查看", "org-1"}, public: true},
		{input: "第二课堂 日历 周 2026-09-14 报名", id: CapabilityYoungCalendar, args: []string{"周", "2026-09-14", "报名"}, public: true},
		{input: "第二课堂 订阅 活动 event-1 开", id: CapabilityYoungSubscription, args: []string{"活动", "event-1", "开"}, public: false},
		{input: "第二课堂 通知 未读", id: CapabilityYoungNotification, args: []string{"未读"}, public: false},
		{input: "第二课堂 评论 event-1 回复 comment-1 好", id: CapabilityYoungComment, args: []string{"event-1", "回复", "comment-1", "好"}, public: false},
	}
	for _, test := range tests {
		inv, ok := ParseInvocation(test.input)
		if !ok || inv.ID() != test.id || strings.Join(inv.Args, " ") != strings.Join(test.args, " ") {
			t.Fatalf("ParseInvocation(%q) = %#v, ok=%v", test.input, inv, ok)
		}
		if (inv.Policy().DataScope == DataScopePublic) != test.public {
			t.Fatalf("policy %q = %#v", test.input, inv.Policy())
		}
	}

	shared := store.Identity{Platform: "test", UserID: "u", ConversationType: "group", ConversationID: "g"}
	private, ok := ParseInvocation("第二课堂 订阅 活动 event-1 开")
	if !ok || sharedCommandAllowed(private) {
		t.Fatalf("private Young command was allowed in group: %#v", private)
	}
	public, ok := ParseInvocation("第二课堂 日历 周")
	if !ok || !sharedCommandAllowed(public) {
		t.Fatalf("public Young calendar was blocked in group: %#v", public)
	}
	_ = shared
}

func TestYoungCalendarBoundsUseShanghaiMondayAndMonth(t *testing.T) {
	loc := lifedata.ChinaLocation()
	sunday := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	query, err := parseYoungCalendarQuery([]string{"周"}, sunday)
	if err != nil {
		t.Fatal(err)
	}
	from, to := youngCalendarBounds(query)
	if from != "2026-09-14" || to != "2026-09-20" {
		t.Fatalf("week bounds = %s..%s", from, to)
	}
	query, err = parseYoungCalendarQuery([]string{"月", "2026-02-11"}, sunday)
	if err != nil {
		t.Fatal(err)
	}
	from, to = youngCalendarBounds(query)
	if from != "2026-02-01" || to != "2026-02-28" {
		t.Fatalf("month bounds = %s..%s", from, to)
	}
}

func TestYoungSubscriptionReminderOptionsDoNotForceOtherReminders(t *testing.T) {
	query, err := parseYoungSubscriptionArgs([]string{"活动", "event-1", "开", "报名提醒", "关", "开始提醒=开"})
	if err != nil {
		t.Fatal(err)
	}
	if query.Action != "set" || query.RemindSignup == nil || *query.RemindSignup || query.RemindDeadline != nil || query.RemindStart == nil || !*query.RemindStart {
		t.Fatalf("query = %#v", query)
	}
	query, err = parseYoungSubscriptionArgs([]string{"活动", "event-1", "关"})
	if err != nil {
		t.Fatal(err)
	}
	if query.RemindSignup != nil || query.RemindDeadline != nil || query.RemindStart != nil {
		t.Fatalf("unsubscribe unexpectedly changed reminders: %#v", query)
	}
}

func TestYoungCalendarGroupsShanghaiDatesAndShowsSourceAndUnknowns(t *testing.T) {
	start := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	collection := life.YoungEventCollection{
		Data: []life.YoungEvent{
			{YoungID: "event-1", Name: "上海日期活动", StartAt: &start},
			{YoungID: "event-2", Name: "未知日期活动", DateUnknown: true},
		},
		UnknownDateCount: 2,
		Source:           map[string]any{"status": "stale", "lastSyncedAt": "2026-09-14T10:00:00+08:00"},
	}
	reply := formatYoungCalendar(collection, "2026-09-14", "2026-09-20", "activity", func(id string) string { return "https://life.test/" + id })
	for _, want := range []string{"数据源：状态 stale，更新于 2026-09-14 10:00", "2026-09-15：", "上海日期活动", "日期待核实（2）", "第二课堂 列表 日期未知", "未知日期活动"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("calendar missing %q: %s", want, reply)
		}
	}
}

func TestPersonalCalendarUsesCompleteRESTAndKeepsYoungType(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" || r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"young-1","type":"young_event","at":"2026-09-15T10:00:00+08:00","endsAt":null,"title":"活动","location":"东区","url":"/catalog/young-events/young-1","youngId":"young-1"}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
	}))
	defer server.Close()
	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程 今日", Identity: ident})
	if !ok || !strings.Contains(reply, "第二课堂活动") || !strings.Contains(reply, "youngId：young-1") {
		t.Fatalf("reply=%q ok=%v", reply, ok)
	}
}

func TestYoungSubscriptionCommandControlsReminderFields(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/workspace/young-event-subscriptions/event-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["subscribed"] != true || body["remindSignup"] != false || body["remindStart"] != true {
			t.Fatalf("body = %#v", body)
		}
		if _, exists := body["remindDeadline"]; exists {
			t.Fatalf("unspecified deadline reminder was sent: %#v", body)
		}
		_, _ = w.Write([]byte(`{"youngId":"event-1","subscribed":true,"remindSignup":false,"remindDeadline":true,"remindStart":true}`))
	}))
	defer server.Close()
	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "第二课堂 订阅 活动 event-1 开 报名提醒 关 开始提醒 开", Identity: ident})
	if !ok || !strings.Contains(reply, "活动订阅 event-1：开") || !strings.Contains(reply, "报名提醒：关") || !strings.Contains(reply, "开始提醒：开") {
		t.Fatalf("reply=%q ok=%v", reply, ok)
	}
}

func TestYoungCalendarFetchesAllPagesAndDisplaysMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/young-events" || r.URL.Query().Get("dateFrom") != "2026-09-14" || r.URL.Query().Get("dateTo") != "2026-09-20" || r.URL.Query().Get("timeBasis") != "activity" || r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		page := r.URL.Query().Get("page")
		if page == "1" {
			_, _ = w.Write([]byte(`{"data":[{"youngId":"event-1","name":"周一活动","startAt":"2026-09-14T10:00:00+08:00","endAt":null,"applyStartAt":null,"applyEndAt":null,"dateUnknown":false}],"pagination":{"page":1,"pageSize":100,"total":2,"totalPages":2},"unknownDateCount":1,"source":{"status":"fresh","lastSyncedAt":"2026-09-14T08:00:00+08:00"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"youngId":"event-2","name":"周二活动","startAt":"2026-09-15T10:00:00+08:00","endAt":null,"applyStartAt":null,"applyEndAt":null,"dateUnknown":false}],"pagination":{"page":2,"pageSize":100,"total":2,"totalPages":2},"unknownDateCount":1,"source":{"status":"fresh","lastSyncedAt":"2026-09-14T08:00:00+08:00"}}`))
	}))
	defer server.Close()
	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply, ok := handler.Handle(context.Background(), Input{Text: "第二课堂 日历 周 2026-09-14", Identity: store.Identity{Platform: "test", UserID: "group", ConversationType: "group", ConversationID: "group"}})
	if !ok {
		t.Fatal("calendar command was not handled")
	}
	for _, want := range []string{"2026-09-14：", "周一活动", "2026-09-15：", "周二活动", "数据源：状态 fresh，更新于 2026-09-14 08:00", "日期待核实（1）"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %s", want, reply)
		}
	}
}

func TestYoungCommentListDisplaysActionableIDs(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/community/comments" || r.URL.Query().Get("targetType") != "young-event" || r.URL.Query().Get("youngId") != "event-1" {
			t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"comment-1","body":"可以一起参加"}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
	}))
	defer server.Close()
	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "第二课堂 评论 event-1", Identity: ident})
	if !ok || !strings.Contains(reply, "commentId：comment-1") || !strings.Contains(reply, "可以一起参加") {
		t.Fatalf("reply=%q ok=%v", reply, ok)
	}
}

func TestYoungCommentListDisplaysChildrenAndDeletedPlaceholders(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/community/comments" || r.URL.Query().Get("targetType") != "young-event" || r.URL.Query().Get("youngId") != "event-1" {
			t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"comment-1","body":"根评论","children":[{"id":"comment-2","parentId":"comment-1","body":"回复内容","children":[]},{"id":"comment-3","parentId":"comment-1","status":"deleted","deletedAt":"2026-09-15T10:00:00+08:00","body":"","children":[]}]}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
	}))
	defer server.Close()
	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "第二课堂 评论 event-1", Identity: ident})
	if !ok {
		t.Fatalf("comment list was not handled: %q", reply)
	}
	for _, want := range []string{
		"commentId：comment-1 根评论",
		"commentId：comment-2 parentId：comment-1 回复内容",
		"commentId：comment-3 parentId：comment-1 （评论已删除）",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %s", want, reply)
		}
	}
}
