package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	listReply, ok := handler.Handle(context.Background(), Input{Text: "第二课堂", Identity: identity})
	if !ok {
		t.Fatal("young event list was not handled")
	}
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

	detailReply, ok := handler.Handle(context.Background(), Input{Text: "二课 查看 event-1", Identity: identity})
	if !ok {
		t.Fatal("young event detail was not handled")
	}
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
	if !handled || outcome.Status != CapabilityOutcomeNotFound || outcome.Response.Text != "没有第二课堂活动。" {
		t.Fatalf("empty list outcome = %#v, handled=%v", outcome, handled)
	}
}
