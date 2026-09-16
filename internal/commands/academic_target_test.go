package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

func academicTestSection(id, semester int, name string) map[string]any {
	return map[string]any{
		"id": id, "jwId": id, "code": "001548." + strconv.Itoa(id),
		"semester": map[string]any{"id": semester, "nameCn": "2026秋"},
		"course":   map[string]any{"jwId": 1548, "code": "001548", "nameCn": name},
		"teachers": []any{map[string]any{"nameCn": "张老师"}},
		"campus":   map[string]any{"nameCn": "西区"},
		"exams":    []any{},
	}
}

func TestAcademicQueriesPreferUniqueCurrentSubscription(t *testing.T) {
	for _, command := range []string{
		"课堂 数学分析 课表",
		"课程 数学分析 课表",
		"课程 查看 001548.42",
		"教学班 课表 001548",
		"班级 001548.42 课表",
		"教学班 查看 数学分析",
		"教学班 考试 数学分析",
		"教学班 作业 001548",
		"课程 查看 001548",
		"课程 查看 code:1548",
		"课堂 课表 code:1548",
	} {
		t.Run(command, func(t *testing.T) {
			var publicSearch atomic.Int32
			section := academicTestSection(42, 2, "数学分析(B1)")
			if strings.Contains(command, "code:1548") {
				section["course"].(map[string]any)["code"] = "1548"
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/catalog/semesters/current":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 2})
				case "/api/workspace/subscriptions/current":
					if r.Header.Get("Authorization") != "Bearer access" {
						t.Error("missing personal authorization")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"subscription": map[string]any{"sections": []any{academicTestSection(41, 1, "数学分析(B1)"), section}}})
				case "/api/catalog/sections/42":
					_ = json.NewEncoder(w).Encode(section)
				case "/api/catalog/courses/1548":
					_ = json.NewEncoder(w).Encode(section["course"])
				case "/api/catalog/sections/42/schedules":
					if r.URL.Query().Get("dateFrom") == "" || r.URL.Query().Get("dateTo") == "" {
						t.Error("default date range missing")
					}
					_ = json.NewEncoder(w).Encode([]any{})
				case "/api/community/section-homeworks":
					_ = json.NewEncoder(w).Encode(map[string]any{"homeworks": []any{}})
				case "/api/catalog/sections", "/api/catalog/courses":
					publicSearch.Add(1)
					t.Error("unique personal match should not use public search")
					http.Error(w, "unexpected", 500)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			ident := testIdentity()
			h := testAuthedHandler(t, api, ident)
			response, ok := h.HandleResponse(t.Context(), Input{Text: command, Identity: ident})
			if !ok || strings.Contains(response.Text, "查不到") || strings.Contains(response.Text, "需要先登录") || strings.Contains(response.Text, "没找到") || strings.Contains(response.Text, "候选") {
				t.Fatalf("command=%s response=%#v ok=%v", command, response, ok)
			}
			if publicSearch.Load() != 0 {
				t.Fatal("personal match leaked into public resolver")
			}
		})
	}
}

func TestAcademicAmbiguityDoesNotExecuteQuery(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2})
		case "/api/workspace/subscriptions/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"subscription": map[string]any{"sections": []any{academicTestSection(42, 2, "数学分析(B1)"), academicTestSection(43, 2, "数学分析(B2)")}}})
		default:
			t.Errorf("ambiguous target executed %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	ident := testIdentity()
	h := testAuthedHandler(t, api, ident)
	response, _ := h.HandleResponse(t.Context(), Input{Text: "课堂 数学分析 课表", Identity: ident})
	for _, want := range []string{"2 个候选", "张老师", "西区", "JW ID：42", "JW ID：43"} {
		if !strings.Contains(response.Text, want) {
			t.Fatalf("missing %s in %s", want, response.Text)
		}
	}
}

func TestAcademicPublicMatchingConsumesAllPagesWithoutPersonalLookup(t *testing.T) {
	var pages atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2})
		case "/api/catalog/sections":
			pages.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if r.URL.Query().Get("semesterId") != "2" {
				t.Error("missing semester filter")
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("private credential sent to public search")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{academicTestSection(41+page, 2, "数学分析")}, "pagination": map[string]any{"page": page, "totalPages": 2}})
		default:
			t.Errorf("public query used unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	// A group identity must never resolve against the sender's subscriptions.
	privateIdentity := testIdentity()
	h := testAuthedHandler(t, api, privateIdentity)
	response, ok := h.HandleResponse(t.Context(), Input{Text: "课堂 查看 数学分析", Identity: store.Identity{Platform: privateIdentity.Platform, UserID: privateIdentity.UserID, ConversationType: "group", ConversationID: "group"}})
	if !ok || pages.Load() != 2 || !strings.Contains(response.Text, "2 个候选") {
		t.Fatalf("response=%#v pages=%d", response, pages.Load())
	}
}

func TestAcademicSemesterAndIdentifierBoundaries(t *testing.T) {
	for _, code := range []string{"001548", "001548.06", "MATH1006.01"} {
		if _, ok := academicJWID(code); ok {
			t.Fatalf("code treated as JW ID: %s", code)
		}
	}
	if id, ok := academicJWID("179671"); !ok || id != 179671 {
		t.Fatal("JW ID not recognized")
	}
	if academicSectionInSemester(map[string]any{"jwId": 42}, 2) {
		t.Fatal("unknown semester treated as current")
	}
	if academicSectionInSemester(academicTestSection(42, 1, "数学分析"), 2) {
		t.Fatal("old semester treated as current")
	}
	q, ok := parseAcademicQuery([]string{"数学分析", "2026-09-01", "2026-09-07", "学期", "2026秋"}, true)
	if !ok || q.Target != "数学分析" || q.Semester != "2026秋" || q.From == "" || q.To == "" {
		t.Fatalf("query=%#v ok=%v", q, ok)
	}
}

func TestAcademicExplicitSemesterRejectsWrongSection(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/catalog/sections/42" {
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(academicTestSection(42, 1, "数学分析"))
	}))
	defer api.Close()
	h := Handler{Life: life.NewClient(api.URL, api.Client())}
	response, _ := h.HandleResponse(t.Context(), Input{Text: "课堂 查看 42 学期 2"})
	if !strings.Contains(response.Text, "不属于指定学期") {
		t.Fatalf("response=%#v", response)
	}
}

func TestAcademicPublicFuzzyMatchRequiresChoice(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2})
		case "/api/catalog/sections":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{academicTestSection(42, 2, "数学分析(B1)")}, "pagination": map[string]any{"page": 1, "totalPages": 1}})
		default:
			t.Errorf("fuzzy public match executed %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	h := Handler{Life: life.NewClient(api.URL, api.Client())}
	response, _ := h.HandleResponse(t.Context(), Input{Text: "课堂 查看 数学分析"})
	if !strings.Contains(response.Text, "请选择") && !strings.Contains(response.Text, "请明确选择") {
		t.Fatalf("response=%#v", response)
	}
}
