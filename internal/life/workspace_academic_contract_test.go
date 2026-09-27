package life

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSpecWorkspaceExamsComplete(t *testing.T) {
	t.Run("bot.workspace-exam-completeness", func(t *testing.T) {
		const totalPages = 101
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/api/workspace/exams" || r.URL.Query().Get("includeDateUnknown") != "true" || r.URL.Query().Get("pageSize") != "50" || r.Header.Get("Authorization") != "Bearer owner" {
				t.Errorf("unexpected request %s", r.URL)
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			date := any("2026-06-01T00:00:00Z")
			if page == 2 {
				date = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": page, "examDate": date, "monitors": []any{map[string]any{"jwId": page + 1000, "nameCn": "Public monitor"}}, "section": map[string]any{"jwId": page + 40, "semester": map[string]any{"id": page, "namePrimary": "Semester " + strconv.Itoa(page)}, "course": map[string]any{"namePrimary": "Course " + strconv.Itoa(page)}}}}, "pagination": map[string]any{"page": page, "totalPages": totalPages}})
		}))
		defer server.Close()
		rows, err := NewClient(server.URL, server.Client()).SubscribedExams(context.Background(), "owner")
		if err != nil || calls != totalPages || len(rows) != totalPages {
			t.Fatalf("rows=%#v calls=%d err=%v", rows, calls, err)
		}
		if rows[1]["examDate"] != nil {
			t.Fatal("undated exam was lost")
		}
		for i, row := range rows {
			monitors := row["monitors"].([]any)
			if len(monitors) != 1 || monitors[0].(map[string]any)["jwId"] != float64(i+1001) {
				t.Fatalf("lost public monitor campus identifier: %#v", row)
			}
			section := row["section"].(map[string]any)
			if section["semester"].(map[string]any)["id"] != float64(i+1) || section["course"].(map[string]any)["namePrimary"] != "Course "+strconv.Itoa(i+1) {
				t.Fatalf("lost semester/course context: %#v", row)
			}
		}
	})
}

func TestSpecWorkspaceExamsFailure(t *testing.T) {
	t.Run("bot.workspace-exam-page-failure", func(t *testing.T) {
		for _, failure := range []string{"server", "malformed", "wrong-page", "empty-page"} {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("page") == "1" {
					_, _ = io.WriteString(w, `{"data":[{"id":1}],"pagination":{"page":1,"totalPages":3}}`)
					return
				}
				switch failure {
				case "server":
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				case "malformed":
					_, _ = io.WriteString(w, `{"data":[{"id":"invalid"}],"pagination":{"page":2,"totalPages":3}}`)
				case "wrong-page":
					_, _ = io.WriteString(w, `{"data":[{"id":2}],"pagination":{"page":1,"totalPages":3}}`)
				case "empty-page":
					_, _ = io.WriteString(w, `{"data":[],"pagination":{"page":2,"totalPages":3}}`)
				}
			}))
			rows, err := NewClient(server.URL, server.Client()).SubscribedExams(context.Background(), "owner")
			server.Close()
			if err == nil || rows != nil || calls != 2 {
				t.Fatalf("%s returned partial success: rows=%#v err=%v calls=%d", failure, rows, err, calls)
			}
		}
	})
}

func TestSpecSectionHomeworkCollection(t *testing.T) {
	t.Run("bot.section-homework-collection", func(t *testing.T) {
		for _, fail := range []bool{false, true} {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/community/section-homeworks" || r.URL.Query().Get("sectionJwId") != "654" || r.URL.Query().Get("pageSize") != "50" || r.Header.Get("Authorization") != "Bearer owner" {
					t.Errorf("unexpected request %s", r.URL)
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if fail && page == 2 {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "hw-" + strconv.Itoa(page), "title": "Assignment", "completionRequired": page == 1}}, "pagination": map[string]any{"page": page, "totalPages": 2}})
			}))
			rows, err := NewClient(server.URL, server.Client()).ListHomeworksBySection(context.Background(), "owner", 654)
			server.Close()
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			if fail {
				if err == nil || rows != nil {
					t.Fatalf("partial success: %#v %v", rows, err)
				}
				continue
			}
			if err != nil || len(rows) != 2 || rows[0]["id"] != "hw-1" || rows[1]["id"] != "hw-2" || rows[0]["completionRequired"] != true || rows[1]["completionRequired"] != false {
				t.Fatalf("bad projection: %#v %v", rows, err)
			}
		}
	})
}
