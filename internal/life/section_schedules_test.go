package life

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListSchedulesBySectionUsesTypedLegacyArray(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantLen int
		wantErr bool
	}{
		{"schedule", `[{"id":42,"date":"2026-11-04T00:00:00Z","startTime":"08:00","endTime":"09:00","room":{"namePrimary":"Room A"}}]`, 1, false},
		{"empty", `[]`, 0, false},
		{"paginated-object", `{"data":[],"pagination":{"page":1}}`, 0, true},
		{"wrong-id-type", `[{"id":"invalid"}]`, 0, true},
		{"invalid-date", `[{"id":42,"date":"not-a-date"}]`, 0, true},
		{"null", `null`, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/catalog/sections/123/schedules" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				query := r.URL.Query()
				if query.Get("limit") != "100" || query.Get("dateFrom") != "2026-11-04" || query.Get("dateTo") != "2026-11-05" || query.Has("page") || query.Has("pageSize") {
					t.Errorf("unexpected legacy query: %v", query)
				}
				if r.Header.Get("Authorization") != "Bearer owner" {
					t.Error("request lost owner authorization")
				}
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			rows, err := NewClient(server.URL, server.Client()).ListSchedulesBySection(t.Context(), "owner", 123, "2026-11-04", "2026-11-05")
			if (err != nil) != test.wantErr || len(rows) != test.wantLen {
				t.Fatalf("rows=%#v err=%v", rows, err)
			}
			if test.wantLen == 1 {
				room, ok := rows[0]["room"].(map[string]any)
				if rows[0]["id"] != float64(42) || rows[0]["startTime"] != "08:00" || !ok || room["namePrimary"] != "Room A" {
					t.Fatalf("schedule fields lost: %#v", rows[0])
				}
			}
		})
	}
}
