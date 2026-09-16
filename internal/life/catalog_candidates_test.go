package life

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestCatalogCandidatesRequireCompletePagination(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(strconv.FormatBool(broken), func(t *testing.T) {
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pages++
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if broken && page == 2 {
					http.Error(w, "failed", http.StatusBadGateway)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"jwId": page}}, "pagination": map[string]any{"page": page, "totalPages": 2}})
			}))
			defer server.Close()
			items, err := NewClient(server.URL, server.Client()).SectionCandidates(context.Background(), "001548", 2)
			if pages != 2 {
				t.Fatalf("pages=%d", pages)
			}
			if broken {
				if err == nil || items != nil {
					t.Fatalf("partial candidates escaped: %#v %v", items, err)
				}
			} else if err != nil || len(items) != 2 {
				t.Fatalf("items=%#v err=%v", items, err)
			}
		})
	}
}
