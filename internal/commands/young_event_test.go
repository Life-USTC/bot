package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestYoungEventCommandsParseAsPublic(t *testing.T) {
	tests := []struct {
		text string
		args string
	}{
		{text: "第二课堂", args: ""},
		{text: "二课 列表 2", args: "列表 2"},
		{text: "第二课堂 搜索 志愿 服务", args: "搜索 志愿 服务"},
		{text: "第二课堂 查看 event-1", args: "查看 event-1"},
	}
	for _, test := range tests {
		result := ParseCommand(test.text)
		if !result.Valid() || result.Invocation.ID() != CapabilityYoungEvent || strings.Join(result.Invocation.Args, " ") != test.args {
			t.Errorf("ParseCommand(%q) = %#v, want young event args %q", test.text, result, test.args)
			continue
		}
		if got := result.Invocation.Policy().DataScope; got != DataScopePublic {
			t.Errorf("ParseCommand(%q) scope = %q, want public", test.text, got)
		}
	}
	for _, text := range []string{"第二课堂 搜索", "第二课堂 查看", "第二课堂 列表 0", "第二课堂 未知"} {
		if result := ParseCommand(text); result.Valid() {
			t.Errorf("invalid young event command %q parsed as valid: %#v", text, result)
		}
	}
}

func TestYoungEventListFiltersParseIntoCatalogQuery(t *testing.T) {
	query, err := parseYoungEventQuery([]string{"列表", "organizerId=org-1", "dateFrom=2026-09-14", "dateTo", "2026-09-20", "timeBasis=registration"})
	if err != nil {
		t.Fatal(err)
	}
	if query.organizerID != "org-1" || query.dateFrom != "2026-09-14" || query.dateTo != "2026-09-20" || query.timeBasis != "registration" || query.dateUnknown != nil {
		t.Fatalf("query = %#v", query)
	}
	query, err = parseYoungEventQuery([]string{"列表", "日期未知"})
	if err != nil || query.dateUnknown == nil || !*query.dateUnknown {
		t.Fatalf("unknown-date query = %#v, err=%v", query, err)
	}
}

func TestYoungEventHelpListsPublicForms(t *testing.T) {
	reply, ok := (Handler{}).Handle(context.Background(), Input{Text: "帮助 第二课堂", Identity: store.Identity{UserID: "help"}})
	if !ok {
		t.Fatal("young event help was not handled")
	}
	for _, want := range []string{
		"第二课堂 帮助：",
		"第二课堂\t查看第二课堂活动",
		"第二课堂 列表 2\t查看第二课堂活动第 2 页",
		"第二课堂 搜索 志愿\t按名称搜索第二课堂活动",
		"第二课堂 列表 日期未知 搜索 志愿 服务\t筛选后在末尾追加搜索关键词",
		"第二课堂 查看 <youngId>\t查看指定第二课堂活动详情",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("help missing %q: %q", want, reply)
		}
	}
}

func TestYoungEventListAndDetailUsePublicLifeEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("young event request unexpectedly sent authorization %q", got)
		}
		switch r.URL.Path {
		case "/api/catalog/young-events":
			query := r.URL.Query()
			if query.Get("page") != "1" || query.Get("pageSize") != "10" || query.Get("search") != "" {
				t.Fatalf("list query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{
				"data":[{"youngId":"event-1","name":"校园分享","location":"东区图书馆","startAt":"2026-09-14T12:00:00Z","endAt":"2026-09-14T14:00:00Z","applyStartAt":"2026-09-01T00:00:00Z","applyEndAt":"2026-09-13T15:59:00Z","isActive":true}],
				"pagination":{"page":1,"pageSize":10,"total":1,"totalPages":1}
			}`))
		case "/api/catalog/young-events/event-1":
			_, _ = w.Write([]byte(`{
				"youngId":"event-1","name":"校园分享","location":"东区图书馆","startAt":"2026-09-14T12:00:00Z","endAt":"2026-09-14T14:00:00Z","applyStartAt":"2026-09-01T00:00:00Z","applyEndAt":"2026-09-13T15:59:00Z","isActive":true
			}`))
		default:
			t.Fatalf("unexpected young event path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	identity := store.Identity{Platform: "napcat", UserID: "7", ConversationType: "group", ConversationID: "42"}
	listResponse, ok := handler.HandleResponse(context.Background(), Input{Text: "第二课堂", Identity: identity})
	if !ok {
		t.Fatal("young event list was not handled")
	}
	listReply := listResponse.Text
	for _, want := range []string{
		"第二课堂：",
		"1. 校园分享",
		"youngId：event-1",
		"地点：东区图书馆",
		"活动时间：2026-09-14 20:00 ~ 2026-09-14 22:00",
		"报名时间：2026-09-01 08:00 ~ 2026-09-13 23:59",
		"链接：" + server.URL + "/catalog/young-events/event-1",
	} {
		if !strings.Contains(listReply, want) {
			t.Fatalf("list reply missing %q: %q", want, listReply)
		}
	}
	listData, ok := listResponse.Data.(map[string]any)
	if !ok || listData["operation"] != "list" {
		t.Fatalf("young event list Data = %#v", listResponse.Data)
	}
	if page, ok := listData["result"].(life.YoungEventPage); !ok || len(page.Data) != 1 || page.Data[0].YoungID != "event-1" {
		t.Fatalf("young event page Data = %#v", listData["result"])
	}

	detailResponse, ok := handler.HandleResponse(context.Background(), Input{Text: "二课 查看 event-1", Identity: identity})
	if !ok {
		t.Fatal("young event detail was not handled")
	}
	detailReply := detailResponse.Text
	for _, want := range []string{
		"第二课堂活动：校园分享",
		"youngId：event-1",
		"活动时间：2026-09-14 20:00 ~ 2026-09-14 22:00",
		"报名时间：2026-09-01 08:00 ~ 2026-09-13 23:59",
		"链接：" + server.URL + "/catalog/young-events/event-1",
	} {
		if !strings.Contains(detailReply, want) {
			t.Fatalf("detail reply missing %q: %q", want, detailReply)
		}
	}
	detailData, ok := detailResponse.Data.(map[string]any)
	if !ok || detailData["operation"] != "detail" {
		t.Fatalf("young event detail Data = %#v", detailResponse.Data)
	}
	if event, ok := detailData["event"].(life.YoungEvent); !ok || event.YoungID != "event-1" {
		t.Fatalf("young event detail domain Data = %#v", detailData["event"])
	}
}

func TestYoungEventEmptyListUsesNotFoundOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"page":1,"pageSize":10,"total":0,"totalPages":0}}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	outcome, handled := handler.HandleOutcome(context.Background(), Input{
		Text:     "第二课堂",
		Identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "group", ConversationID: "42"},
	})
	if !handled || outcome.Status != CapabilityOutcomeNotFound || outcome.Response.Text != "没有第二课堂活动。" || outcome.Response.Data == nil {
		t.Fatalf("empty list outcome = %#v, handled=%v", outcome, handled)
	}
}

func TestYoungEventMissingDetailUsesNotFoundOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not found"}`))
	}))
	defer server.Close()
	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	outcome, handled := handler.HandleOutcome(context.Background(), Input{
		Text:     "第二课堂 查看 missing",
		Identity: store.Identity{Platform: "napcat", UserID: "7", ConversationType: "group", ConversationID: "42"},
	})
	if !handled || outcome.Status != CapabilityOutcomeNotFound || !strings.Contains(outcome.Response.Text, "没找到第二课堂活动") {
		t.Fatalf("missing detail outcome = %#v, handled=%v", outcome, handled)
	}
}

func TestYoungEventSearchPaginationKeepsSearch(t *testing.T) {
	query, err := parseYoungEventQuery([]string{"搜索", "志愿", "服务", "第2页"})
	if err != nil || query.page != 2 || query.search != "志愿 服务" {
		t.Fatalf("query = %#v, err=%v", query, err)
	}
	reply := formatYoungEventPage(life.YoungEventPage{
		Data:       []life.YoungEvent{{YoungID: "event-2", Name: "志愿服务"}},
		Pagination: life.YoungEventPagination{Page: 2, PageSize: 10, Total: 30, TotalPages: 3},
	}, query, nil)
	if !strings.Contains(reply, "第二课堂 搜索 志愿 服务") || !strings.Contains(reply, "上一页") || !strings.Contains(reply, "下一页") {
		t.Fatalf("pagination lost search or navigation: %q", reply)
	}
}

func TestYoungUnknownDateHintsPreserveFilters(t *testing.T) {
	tests := []struct {
		name    string
		command string
		initial url.Values
		unknown url.Values
	}{
		{
			name:    "list organizer registration and date range",
			command: "第二课堂 列表 organizerId=org-1 dateFrom=2026-09-14 dateTo=2026-09-20 timeBasis=registration dateUnknown=false 第2页",
			initial: url.Values{"page": {"2"}, "pageSize": {"10"}, "organizerId": {"org-1"}, "dateFrom": {"2026-09-14"}, "dateTo": {"2026-09-20"}, "timeBasis": {"registration"}, "dateUnknown": {"false"}},
			unknown: url.Values{"page": {"1"}, "pageSize": {"10"}, "organizerId": {"org-1"}, "timeBasis": {"registration"}, "dateUnknown": {"true"}},
		},
		{
			name:    "multiword search",
			command: "第二课堂 搜索 志愿 服务 第2页",
			initial: url.Values{"page": {"2"}, "pageSize": {"10"}, "search": {"志愿 服务"}},
			unknown: url.Values{"page": {"1"}, "pageSize": {"10"}, "search": {"志愿 服务"}, "dateUnknown": {"true"}},
		},
		{
			name:    "search containing punctuation and filter-like keywords",
			command: "第二课堂 搜索 O'Reilly & 志愿+服务 organizerId=literal",
			initial: url.Values{"page": {"1"}, "pageSize": {"10"}, "search": {"O'Reilly & 志愿+服务 organizerId=literal"}},
			unknown: url.Values{"page": {"1"}, "pageSize": {"10"}, "search": {"O'Reilly & 志愿+服务 organizerId=literal"}, "dateUnknown": {"true"}},
		},
		{
			name:    "registration calendar",
			command: "第二课堂 日历 周 2026-09-14 报名",
			initial: url.Values{"page": {"1"}, "pageSize": {"100"}, "dateFrom": {"2026-09-14"}, "dateTo": {"2026-09-20"}, "timeBasis": {"registration"}},
			unknown: url.Values{"page": {"1"}, "pageSize": {"10"}, "timeBasis": {"registration"}, "dateUnknown": {"true"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan url.Values, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/catalog/young-events" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected public request: %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				query := r.URL.Query()
				requests <- query
				page, _ := strconv.Atoi(query.Get("page"))
				pageSize, _ := strconv.Atoi(query.Get("pageSize"))
				_ = json.NewEncoder(w).Encode(life.YoungEventPage{
					Data:       []life.YoungEvent{},
					Pagination: life.YoungEventPagination{Page: page, PageSize: pageSize},
					Meta:       life.YoungEventMetadata{UnknownDateCount: 3, Source: map[string]any{"status": "unknown", "lastSyncedAt": nil}},
				})
			}))
			defer server.Close()
			handler := Handler{Life: life.NewClient(server.URL, server.Client())}
			identity := store.Identity{Platform: "test", UserID: "u", ConversationType: "group", ConversationID: "g"}
			reply, ok := handler.Handle(context.Background(), Input{Text: test.command, Identity: identity})
			if !ok || len(requests) != 1 {
				t.Fatalf("initial command not executed: handled=%v reply=%q requests=%d", ok, reply, len(requests))
			}
			if got := <-requests; !reflect.DeepEqual(got, test.initial) {
				t.Fatalf("initial request = %v, want %v", got, test.initial)
			}
			_, hintAndSuffix, found := strings.Cut(reply, "发送「")
			hint, _, closed := strings.Cut(hintAndSuffix, "」")
			if !found || !closed {
				t.Fatalf("no executable unknown-date hint: %q", reply)
			}
			reply, ok = handler.Handle(context.Background(), Input{Text: hint, Identity: identity})
			if !ok || len(requests) != 1 {
				t.Fatalf("hint %q not executed: handled=%v reply=%q requests=%d", hint, ok, reply, len(requests))
			}
			if got := <-requests; !reflect.DeepEqual(got, test.unknown) {
				t.Fatalf("hint %q request = %v, want %v", hint, got, test.unknown)
			}
		})
	}
}

func TestYoungFilteredSearchPaginationPreservesQuery(t *testing.T) {
	query, err := parseYoungEventQuery([]string{"列表", "organizerId=org-1/一+&", "dateUnknown=false", "dateFrom=2026-09-14", "timeBasis=registration", "搜索", "志愿2026", "organizerId=literal", "第2页"})
	if err != nil {
		t.Fatal(err)
	}
	reply := formatYoungEventPage(life.YoungEventPage{
		Pagination: life.YoungEventPagination{Page: 2, PageSize: 10, Total: 30, TotalPages: 3},
	}, query, nil)
	for _, direction := range []struct {
		label string
		page  int
	}{{"上一页", 1}, {"下一页", 3}} {
		_, tail, found := strings.Cut(reply, direction.label+"：发送「")
		command, _, closed := strings.Cut(tail, "」")
		parsed := ParseCommand(command)
		if !found || !closed || !parsed.Valid() {
			t.Fatalf("invalid navigation command %q in %q", command, reply)
		}
		got, err := parseYoungEventQuery(parsed.Invocation.Args)
		want := query
		want.page = direction.page
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("navigation %q = %#v, err=%v, want %#v", command, got, err, want)
		}
	}
	for _, command := range []string{
		"第二课堂 列表 日期未知 搜索",
		"第二课堂 列表 dateFrom=2026-09-14 日期未知 搜索 志愿",
		"第二课堂 列表 dateFrom=2026-09-20 dateTo=2026-09-14 search 志愿",
	} {
		if parsed := ParseCommand(command); parsed.Valid() {
			t.Errorf("invalid filtered search accepted: %q", command)
		}
	}
}

func TestYoungEventDetailShowsParticipationAndPreservesUnknownOccupancy(t *testing.T) {
	yes := true
	capacity := 20
	event := life.YoungEvent{YoungID: "metadata", Name: "Metadata", RequiresSignup: &yes, RequiresSignupInfo: &yes, IsOnline: &yes, OnlineMeetingInfo: "800-414-186", AllowedAttachmentTypes: []string{"pdf", "docx"}, ExternalSponsor: "合作机构", Capacity: &capacity}
	reply := strings.Join(youngEventLines(event, "https://example.test/event", ""), "\n")
	for _, want := range []string{"需要报名", "报名时需填写补充信息", "800-414-186", "PDF, DOCX", "合作机构", "名额：20"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("missing %q: %s", want, reply)
		}
	}
	if strings.Contains(reply, "已报名：0") {
		t.Fatal("unknown occupancy rendered as zero")
	}
}
