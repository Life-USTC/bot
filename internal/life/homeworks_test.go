package life

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubscribedHomeworksReadsEveryWorkspacePage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/workspace/homeworks" || r.URL.Query().Get("pageSize") != "50" || r.URL.Query().Get("page") != fmt.Sprint(requests) {
			t.Errorf("request = %s", r.URL.String())
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"hw-%d","completion":null}],"pagination":{"page":%d,"pageSize":50,"total":51,"totalPages":2}}`, requests, requests)
	}))
	defer server.Close()
	homeworks, err := NewClient(server.URL, server.Client()).SubscribedHomeworks(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(homeworks) != 2 || homeworks[1]["id"] != "hw-2" {
		t.Fatalf("requests=%d homeworks=%#v", requests, homeworks)
	}
}

func TestSubscribedHomeworksRejectsIncompleteOrObsoleteEnvelope(t *testing.T) {
	for _, body := range []string{`{"homeworks":[]}`, `{"data":[]}`, `{"pagination":{"page":1,"totalPages":1}}`, `{"data":[],"pagination":{"page":2,"totalPages":2}}`, `{"data":[],"pagination":{"page":1,"totalPages":101}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
			defer server.Close()
			if _, err := NewClient(server.URL, server.Client()).SubscribedHomeworks(context.Background(), "token"); err == nil {
				t.Fatal("invalid envelope silently treated as an empty collection")
			}
		})
	}
}

func TestSubscribedHomeworksDoesNotReturnPartialCollectionOnPageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"first"}],"pagination":{"page":1,"totalPages":2}}`)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	items, err := NewClient(server.URL, server.Client()).SubscribedHomeworks(context.Background(), "token")
	if err == nil || items != nil {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}
