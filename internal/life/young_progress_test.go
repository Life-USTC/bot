package life

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

func TestCompleteYoungReadRetainsAdapterPageDecisions(t *testing.T) {
	tests := []struct {
		name       string
		pages      []YoungEventPage
		wantIDs    []string
		wantTotal  int
		wantPages  int
		wantCount  int
		wantSource map[string]any
	}{
		{
			name: "short pages retain duplicates and maximum metadata",
			pages: []YoungEventPage{
				{Data: []YoungEvent{{YoungID: "same"}}, Pagination: YoungEventPagination{Total: 5, TotalPages: 3}, Meta: YoungEventMetadata{UnknownDateCount: 4}},
				{Data: []YoungEvent{{YoungID: "same"}}, Pagination: YoungEventPagination{Total: 3, TotalPages: 2}, Meta: YoungEventMetadata{UnknownDateCount: 2, Source: map[string]any{"status": "fresh"}}},
			},
			wantIDs: []string{"same", "same"}, wantTotal: 5, wantPages: 3, wantCount: 4, wantSource: map[string]any{"status": "fresh"},
		},
		{
			name: "total continues a short page without page count",
			pages: []YoungEventPage{
				{Data: []YoungEvent{{YoungID: "first"}}, Pagination: YoungEventPagination{Total: 3}},
				{Data: []YoungEvent{{YoungID: "second"}}, Pagination: YoungEventPagination{Total: 2}},
			},
			wantIDs: []string{"first", "second"}, wantTotal: 3, wantPages: 2,
		},
		{
			name: "missing totals use page length and accepted rows",
			pages: []YoungEventPage{
				{Data: []YoungEvent{{YoungID: "first"}, {YoungID: "second"}}},
				{Data: []YoungEvent{{YoungID: "third"}}},
			},
			wantIDs: []string{"first", "second", "third"}, wantTotal: 3, wantPages: 2,
		},
		{
			name: "empty page stops despite advertised continuation",
			pages: []YoungEventPage{
				{Data: []YoungEvent{{YoungID: "first"}, {YoungID: "second"}}, Pagination: YoungEventPagination{TotalPages: 3}, Meta: YoungEventMetadata{Source: map[string]any{"status": "stale"}}},
				{Data: []YoungEvent{}, Pagination: YoungEventPagination{Total: 100, TotalPages: 50}, Meta: YoungEventMetadata{UnknownDateCount: 7, Source: map[string]any{"status": "fresh"}}},
			},
			wantIDs: []string{"first", "second"}, wantTotal: 100, wantPages: 50, wantCount: 7, wantSource: map[string]any{"status": "stale"},
		},
		{
			name:      "terminal full page does not request another",
			pages:     []YoungEventPage{{Data: []YoungEvent{{YoungID: "first"}, {YoungID: "second"}}, Pagination: YoungEventPagination{TotalPages: 1}}},
			wantIDs:   []string{"first", "second"},
			wantTotal: 2, wantPages: 1,
		},
		{
			name:  "empty result does not infer a total",
			pages: []YoungEventPage{{Data: []YoungEvent{}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Query().Get("page") != strconv.Itoa(requests) || r.URL.Query().Get("pageSize") != "2" {
					t.Errorf("request %d = %s", requests, r.URL)
				}
				if requests > len(test.pages) {
					t.Errorf("unexpected request after terminal page: %s", r.URL)
					http.Error(w, "unexpected page", http.StatusInternalServerError)
					return
				}
				page := test.pages[requests-1]
				if page.Meta.Source == nil {
					page.Meta.Source = map[string]any{"status": "fresh", "lastSyncedAt": nil}
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			got, err := NewClient(server.URL, server.Client()).ListAllYoungEventsWithQueryMetadata(context.Background(), "", YoungEventQuery{Page: 7, PageSize: 2})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range got.Data {
				ids = append(ids, row.YoungID)
			}
			wantPagination := YoungEventPagination{Page: 1, PageSize: 2, Total: test.wantTotal, TotalPages: test.wantPages}
			wantSource := map[string]any{"status": "fresh", "lastSyncedAt": nil}
			for key, value := range test.wantSource {
				wantSource[key] = value
			}
			if !reflect.DeepEqual(ids, test.wantIDs) || got.Pagination != wantPagination || got.UnknownDateCount != test.wantCount || !reflect.DeepEqual(got.Source, wantSource) || requests != len(test.pages) {
				t.Fatalf("IDs = %v, pagination = %#v, unknown = %d, source = %v, requests = %d; want IDs %v, pagination %#v, unknown %d, source %v", ids, got.Pagination, got.UnknownDateCount, got.Source, requests, test.wantIDs, wantPagination, test.wantCount, wantSource)
			}
		})
	}
}

func TestCompleteYoungReadReturnsNoPartialResultAfterLaterFailure(t *testing.T) {
	for _, failure := range []string{"http", "decode"} {
		for _, metadata := range []bool{false, true} {
			t.Run(failure+"/metadata="+strconv.FormatBool(metadata), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.URL.Query().Get("page") != strconv.Itoa(requests) || requests > 2 {
						t.Errorf("unexpected request %d: %s", requests, r.URL)
					}
					if requests == 1 {
						_ = json.NewEncoder(w).Encode(YoungEventPage{
							Data: []YoungEvent{{YoungID: "first"}}, Pagination: YoungEventPagination{TotalPages: 2},
							Meta: YoungEventMetadata{UnknownDateCount: 4, Source: map[string]any{"status": "fresh"}},
						})
						return
					}
					if failure == "http" {
						http.Error(w, "later page unavailable", http.StatusServiceUnavailable)
					} else {
						_, _ = w.Write([]byte(`{"data":`))
					}
				}))
				defer server.Close()
				client := NewClient(server.URL, server.Client())
				if metadata {
					got, err := client.ListAllYoungEventsWithQueryMetadata(context.Background(), "", YoungEventQuery{})
					if err == nil || !reflect.DeepEqual(got, YoungEventCollection{}) {
						t.Fatalf("partial collection escaped: %#v, err = %v", got, err)
					}
				} else {
					got, err := client.ListAllYoungEventsWithQuery(context.Background(), "", YoungEventQuery{})
					if err == nil || got != nil {
						t.Fatalf("partial rows escaped: %#v, err = %v", got, err)
					}
				}
				if requests != 2 {
					t.Fatalf("requests = %d, want 2", requests)
				}
			})
		}
	}
}
