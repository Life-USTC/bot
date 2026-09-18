package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func withAgendaRand(t *testing.T, value float64) {
	t.Helper()
	old := agendaRandFloat
	agendaRandFloat = func() float64 { return value }
	t.Cleanup(func() { agendaRandFloat = old })
}

func TestAgendaDateParsesSupportedForms(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, lifedata.ChinaLocation())
	tests := []struct {
		args []string
		want string
		ok   bool
	}{
		{nil, "2026-09-18", true},
		{[]string{"今天"}, "2026-09-18", true},
		{[]string{"今日"}, "2026-09-18", true},
		{[]string{"明天"}, "2026-09-19", true},
		{[]string{"后天"}, "2026-09-20", true},
		{[]string{"9-20"}, "2026-09-20", true},
		{[]string{"9月20日"}, "2026-09-20", true},
		{[]string{"2026-09-20"}, "2026-09-20", true},
		{[]string{"概览"}, "", false},
		{[]string{"9-20", "多余"}, "", false},
	}
	for _, test := range tests {
		got, ok := agendaDate(test.args, base)
		if ok != test.ok {
			t.Fatalf("agendaDate(%v) ok = %v, want %v", test.args, ok, test.ok)
		}
		if ok && got.Format("2006-01-02") != test.want {
			t.Fatalf("agendaDate(%v) = %s, want %s", test.args, got.Format("2006-01-02"), test.want)
		}
	}
}

func TestAgendaGroupsAllEventTypes(t *testing.T) {
	withAgendaRand(t, 0.99) // 不触发链接追加
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("dateFrom") == "" || q.Get("dateFrom") != q.Get("dateTo") || q.Get("pageSize") != "100" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"s1","type":"schedule","at":"2026-09-18T09:50:00+08:00","endsAt":"2026-09-18T11:25:00+08:00","title":"计算机导论","location":"3A101"},
			{"id":"e1","type":"exam","at":"2026-09-18T14:30:00+08:00","endsAt":"2026-09-18T16:30:00+08:00","title":"数学分析","location":"GT-B112"},
			{"id":"h1","type":"homework_due","at":"2026-09-18T23:59:00+08:00","title":"作业一"},
			{"id":"t1","type":"todo_due","at":"2026-09-18T18:00:00+08:00","title":"写报告"},
			{"id":"y1","type":"young_event","at":"2026-09-18T10:00:00+08:00","title":"讲座","location":"东区"}
		],"pagination":{"page":1,"pageSize":100,"total":5,"totalPages":1}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程 今天", Identity: ident})
	if !ok {
		t.Fatalf("not handled: %q", reply)
	}
	reply = textutil.PlainMonospace(reply)
	for _, want := range []string{"课程 (1)：", "计算机导论", "09:50", "3A101", "考试 (1)：", "数学分析", "作业截止 (1)：", "作业一", "待办截止 (1)：", "写报告", "第二课堂 (1)：", "讲座", "东区"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(reply, "youngId") || strings.Contains(reply, "日历订阅链接") {
		t.Fatalf("unexpected content: %q", reply)
	}
	// 分组顺序：课程在考试前，考试在作业截止前
	if strings.Index(reply, "课程 (1)：") > strings.Index(reply, "考试 (1)：") ||
		strings.Index(reply, "考试 (1)：") > strings.Index(reply, "作业截止 (1)：") {
		t.Fatalf("group order wrong: %q", reply)
	}
}

func TestAgendaEmptyDay(t *testing.T) {
	withAgendaRand(t, 0) // 即使掷中也不应追加链接
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"page":1,"pageSize":100,"total":0,"totalPages":0}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程", Identity: ident})
	if !ok || !strings.Contains(reply, "暂无安排") || strings.Contains(reply, "日历订阅链接") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgendaAppendsCalendarLinkWhenDiceHits(t *testing.T) {
	withAgendaRand(t, 0) // 必中（0 < 1/16）
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/calendar/events":
			_, _ = w.Write([]byte(`{"data":[{"id":"t1","type":"todo_due","at":"2026-09-18T18:00:00+08:00","title":"写报告"}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
		case "/api/workspace/subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"https://life.example/ical/test.ics"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程", Identity: ident})
	if !ok || !strings.Contains(reply, "写报告") || !strings.Contains(reply, "日历订阅链接：https://life.example/ical/test.ics") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgendaLinkCommandShowsSubscriptionLink(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"https://life.example/ical/test.ics"}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程 链接", Identity: ident})
	if !ok || !strings.Contains(reply, "https://life.example/ical/test.ics") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
