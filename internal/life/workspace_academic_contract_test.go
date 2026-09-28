package life

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/Life-USTC/Bot/internal/specification"
)

func TestSpecWorkspaceExamsComplete(t *testing.T) {
	t.Run("bot.workspace-exam-completeness", func(t *testing.T) {
		contract := specification.Begin(t)
		wire := contract.Wire("workspace_exam_list")
		trace := collectionTrace{}
		var fixtures []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wire.Request(r)
			trace.record(r)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if r.URL.Query().Get("includeDateUnknown") != "true" {
				t.Error("undated inclusion missing")
			}
			body, rows := academicFixture(t, wire, "exams.json", page, 5001, 50)
			fixtures = append(fixtures, rows...)
			wire.Fixture(body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		}))
		defer server.Close()
		rows, err := NewClient(server.URL, server.Client()).SubscribedExams(context.Background(), "owner")
		undated := 0
		for _, row := range rows {
			if row["examDate"] == nil {
				undated++
			}
		}
		contract.Check("complete", trace.observation(rows, err, wire.Projection(fixtures, rows), undated))
	})
}
func TestSpecWorkspaceExamsFailure(t *testing.T) {
	t.Run("bot.workspace-exam-page-failure", func(t *testing.T) {
		contract := specification.Begin(t)
		wire := contract.Wire("workspace_exam_list")
		for _, failure := range []string{"server", "malformed", "wrong-page", "empty-page"} {
			trace := collectionTrace{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire.Request(r)
				trace.record(r)
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				body, _ := academicFixture(t, wire, "exams.json", page, 101, 50)
				if page == 2 {
					switch failure {
					case "server":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					case "malformed":
						body["data"].([]any)[0].(map[string]any)["id"] = "invalid"
					case "wrong-page":
						body["pagination"].(map[string]any)["page"] = 1
					case "empty-page":
						body["data"] = []any{}
					}
				}
				if page == 2 && failure == "malformed" {
					wire.InvalidFixture(body)
				} else {
					wire.Fixture(body)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			rows, err := NewClient(server.URL, server.Client()).SubscribedExams(context.Background(), "owner")
			server.Close()
			contract.Check(failure, trace.observation(rows, err, []string{}, 0))
		}
	})
}
func TestSpecSectionHomeworkCollection(t *testing.T) {
	t.Run("bot.section-homework-collection", func(t *testing.T) {
		contract := specification.Begin(t)
		wire := contract.Wire("community_section_homework_list")
		for _, fail := range []bool{false, true} {
			trace := collectionTrace{}
			var fixtures []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire.Request(r)
				trace.record(r)
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if fail && page == 2 {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				body, rows := academicFixture(t, wire, "section-homeworks.json", page, 51, 50)
				fixtures = append(fixtures, rows...)
				wire.Fixture(body)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			rows, err := NewClient(server.URL, server.Client()).ListHomeworksBySection(context.Background(), "owner", 654)
			server.Close()
			name := "complete"
			mismatches := []string{}
			if fail {
				name = "later-failure"
			} else {
				mismatches = wire.Projection(fixtures, rows)
			}
			contract.Check(name, trace.observation(rows, err, mismatches, 0))
		}
	})
}

// Fixtures use real required fields and coherent page/size/total arithmetic.
// The data are explicit synthetic records; every served success is validated
// against the pinned operation before the production client receives it.
func academicFixture(t *testing.T, wire *specification.Wire, file string, page, total, pageSize int) (map[string]any, []map[string]any) {
	t.Helper()
	body := wire.FixtureFile(file)
	prototype := body["data"].([]any)[0]
	encoded, err := json.Marshal(prototype)
	if err != nil {
		t.Fatal(err)
	}
	data := []any{}
	rows := []map[string]any{}
	for index := (page - 1) * pageSize; index < min(page*pageSize, total); index++ {
		var row map[string]any
		if err := json.Unmarshal(encoded, &row); err != nil {
			t.Fatal(err)
		}
		if file == "exams.json" {
			row["id"] = float64(index + 1)
			if index == total-1 {
				row["examDate"] = nil
			}
			section := row["section"].(map[string]any)
			section["semester"].(map[string]any)["id"] = float64(index + 1)
			section["course"].(map[string]any)["namePrimary"] = "Course " + strconv.Itoa(index+1)
			row["monitors"].([]any)[0].(map[string]any)["jwId"] = float64(index + 1001)
		} else {
			row["id"] = "hw-" + strconv.Itoa(index+1)
			row["completionRequired"] = index%2 == 0
		}
		data = append(data, row)
		rows = append(rows, row)
	}
	body["data"] = data
	body["pagination"] = map[string]any{"page": page, "pageSize": pageSize, "total": total, "totalPages": (total + pageSize - 1) / pageSize}
	return body, rows
}

type collectionTrace struct {
	pages      []int
	pageSizes  []int
	bearers    []string
	sectionIDs []string
}

func (trace *collectionTrace) record(r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	trace.pages = append(trace.pages, page)
	if !slices.Contains(trace.pageSizes, pageSize) {
		trace.pageSizes = append(trace.pageSizes, pageSize)
	}
	bearer := r.Header.Get("Authorization")
	if !slices.Contains(trace.bearers, bearer) {
		trace.bearers = append(trace.bearers, bearer)
	}
	section := r.URL.Query().Get("sectionJwId")
	if section != "" && !slices.Contains(trace.sectionIDs, section) {
		trace.sectionIDs = append(trace.sectionIDs, section)
	}
}
func (trace collectionTrace) observation(rows []map[string]any, err error, mismatches []string, undated int) specification.Collection {
	sort.Ints(trace.pageSizes)
	sort.Strings(trace.bearers)
	sort.Strings(trace.sectionIDs)
	if trace.sectionIDs == nil {
		trace.sectionIDs = []string{}
	}
	return specification.Collection{Requests: len(trace.pages), Pages: trace.pages, PageSizes: trace.pageSizes, Rows: len(rows), UndatedRows: undated, ProjectionMismatches: mismatches, Error: err != nil, PartialRows: err != nil && rows != nil, Bearers: trace.bearers, SectionJwIDs: trace.sectionIDs}
}
