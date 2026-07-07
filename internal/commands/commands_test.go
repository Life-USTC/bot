package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

func TestHandleCourseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "/life course calculus"})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷      \tCalculus") {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestHandleCasualCourseSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "数学分析" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1006","namePrimary":"数学分析"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程 数学分析"})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟼      \t数学分析") {
		t.Fatalf("unexpected reply %q", reply)
	}
}

func TestSearchCoursesTrimsKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "数学分析" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1006","namePrimary":"数学分析"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply := handler.searchCourses(context.Background(), "  数学分析  ")
	if !strings.Contains(reply, "数学分析") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestFormatCourseOmitsTrailingTabWhenNameMissing(t *testing.T) {
	line := formatCourse(map[string]any{"code": "MATH1001"})
	if line != "- 𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatSectionOmitsTrailingTabWhenLabelMissing(t *testing.T) {
	line := formatSection(map[string]any{"code": "MATH1001.01"})
	if line != "- 𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷" {
		t.Fatalf("line = %q", line)
	}
}

func TestSearchSectionsTrimsKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "高等数学" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001.01","course":{"namePrimary":"高等数学"}}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply := handler.searchSections(context.Background(), "  高等数学  ")
	if !strings.Contains(reply, "高等数学") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleTeacherSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/teachers" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("search") != "张" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":12,"code":"T001","namePrimary":"张三","department":{"namePrimary":"数学科学学院"},"teacherTitle":{"namePrimary":"教授"}}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "老师 张"})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{"老师：", "𝚃𝟶𝟶𝟷", "张三 数学科学学院 教授"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestSearchTeachersTrimsKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "张" {
			t.Fatalf("search = %q", r.URL.Query().Get("search"))
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"T001","namePrimary":"张三"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply := handler.searchTeachers(context.Background(), "  张  ")
	if !strings.Contains(reply, "张三") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"/help", "/?", "帮助", "菜单", "/life -h", "/life 菜单"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "待办 / td") {
			t.Fatalf("unexpected reply for %q: %q", text, reply)
		}
	}
}

func TestHelpReplyIsCompact(t *testing.T) {
	reply := Handler{}.help()
	want := "可以直接发：待办 / td；td 写报告；td done 1；作业 / hw；作业 done 1；今日 / ddl；校车 / xc；xc 东区 西区；今天课表 / 明天课表；下一节课；订阅；通知；AI 工具；状态 / status；我 / me；反馈 你的建议；课程 数学分析；教学班 高等数学；老师 张；考试 / ks；登录 / 登录 状态"
	if reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
	if strings.ContainsAny(reply, "\n\r\x1b") {
		t.Fatalf("reply contains control separator: %q", reply)
	}
}

func TestIsHelpToken(t *testing.T) {
	for _, token := range []string{"/help", "/?", "-h", "--help", " help ", "?", "？", "帮助", "菜单"} {
		if !isHelpToken(token) {
			t.Fatalf("%q was not recognized as help", token)
		}
	}
	if isHelpToken("status") {
		t.Fatal("status was recognized as help")
	}
}

func TestFriendlyError(t *testing.T) {
	for _, err := range []error{
		life.HTTPError{StatusCode: http.StatusUnauthorized},
		errors.New("request failed: Unauthorized"),
	} {
		if got := friendlyError(err); got != "登录已过期。发送：登录" {
			t.Fatalf("friendlyError(%v) = %q", err, got)
		}
	}
	if got := friendlyError(errors.New("upstream timeout waiting for response")); got != "网络超时，等会儿再试" {
		t.Fatalf("timeout friendlyError = %q", got)
	}
	if got := friendlyError(context.DeadlineExceeded); got != "网络超时，等会儿再试" {
		t.Fatalf("deadline friendlyError = %q", got)
	}
	if got := friendlyError(errors.New("server exploded")); got != "server exploded" {
		t.Fatalf("passthrough friendlyError = %q", got)
	}
	if got := commandError("课表查不到：", errors.New("server exploded")); got != "课表查不到：server exploded" {
		t.Fatalf("commandError = %q", got)
	}
}

func TestCommandSpecsAreUsable(t *testing.T) {
	seen := map[string]bool{}
	aliases := map[string]string{}
	agentTools := map[string]string{}
	handler := Handler{Prefix: "/life"}
	lifeCommands := map[string]bool{
		"me":           true,
		"todo":         true,
		"homework":     true,
		"overview":     true,
		"subscription": true,
		"ping":         true,
		"status":       true,
		"semester":     true,
		"course":       true,
		"section":      true,
		"teacher":      true,
		"bus":          true,
		"schedule":     true,
		"nextclass":    true,
		"exam":         true,
		"list_semesters":               true,
		"course_search":                true,
		"section_search":               true,
		"teacher_search":               true,
		"course_by_jw_id":              true,
		"section_by_jw_id":             true,
		"teacher_by_id":                true,
		"bus_routes":                   true,
		"unsubscribe_section_by_jw_id": true,
		"my_subscribed_sections":       true,
		"section_schedules":            true,
		"section_exams":                true,
		"section_homeworks":            true,
		"dashboard":                    true,
		"upcoming_deadlines":           true,
	}
	storeCommands := map[string]bool{
		"notify": true,
		"agent":  true,
	}
	authCommands := map[string]bool{
		"login":        true,
		"logout":       true,
		"me":           true,
		"todo":         true,
		"homework":     true,
		"overview":     true,
		"subscription": true,
		"schedule":     true,
		"nextclass":    true,
		"exam":         true,
		"unsubscribe_section_by_jw_id": true,
		"my_subscribed_sections":       true,
		"section_schedules":            true,
		"section_exams":                true,
		"section_homeworks":            true,
		"dashboard":                    true,
		"upcoming_deadlines":           true,
	}
	helpCommands := map[string]bool{
		"login":        true,
		"todo":         true,
		"homework":     true,
		"subscription": true,
		"notify":       true,
		"agent":        true,
		"bus":          true,
		"schedule":     true,
		"feedback":     true,
	}
	for _, spec := range CommandSpecs() {
		if spec.Name == "" {
			t.Fatal("command spec has empty name")
		}
		if spec.Run == nil {
			t.Fatalf("command %q has nil Run", spec.Name)
		}
		if len(spec.Aliases) == 0 {
			t.Fatalf("command %q has no aliases", spec.Name)
		}
		if seen[spec.Name] {
			t.Fatalf("duplicate command spec %q", spec.Name)
		}
		if spec.NeedsLife != lifeCommands[spec.Name] {
			t.Fatalf("command %q NeedsLife = %v", spec.Name, spec.NeedsLife)
		}
		if spec.NeedsStore != storeCommands[spec.Name] {
			t.Fatalf("command %q NeedsStore = %v", spec.Name, spec.NeedsStore)
		}
		if spec.NeedsAuth != authCommands[spec.Name] {
			t.Fatalf("command %q NeedsAuth = %v", spec.Name, spec.NeedsAuth)
		}
		if spec.HasHelp != helpCommands[spec.Name] {
			t.Fatalf("command %q HasHelp = %v", spec.Name, spec.HasHelp)
		}
		seen[spec.Name] = true
		for _, alias := range spec.Aliases {
			key := normToken(alias)
			if owner, ok := aliases[key]; ok {
				t.Fatalf("alias %q for %q already belongs to %q", alias, spec.Name, owner)
			}
			aliases[key] = spec.Name
			name, _ := normalizeCommand(alias, nil)
			if name != spec.Name {
				t.Fatalf("alias %q normalized to %q, want %q", alias, name, spec.Name)
			}
		}
		for _, tool := range spec.AgentTools {
			if strings.TrimSpace(tool.Name) == "" {
				t.Fatalf("command %q has agent tool with empty name", spec.Name)
			}
			if strings.TrimSpace(tool.Description) == "" {
				t.Fatalf("agent tool %q for %q has empty description", tool.Name, spec.Name)
			}
			if strings.TrimSpace(tool.CommandText) == "" {
				t.Fatalf("agent tool %q for %q has empty command text", tool.Name, spec.Name)
			}
			if owner, ok := agentTools[tool.Name]; ok {
				t.Fatalf("agent tool %q for %q already belongs to %q", tool.Name, spec.Name, owner)
			}
			parsed, ok := handler.parse(tool.CommandText)
			if !ok {
				t.Fatalf("agent tool %q for %q command text %q was not parsed", tool.Name, spec.Name, tool.CommandText)
			}
			if parsed.Name != spec.Name {
				t.Fatalf("agent tool %q command text parsed as %q, want %q", tool.Name, parsed.Name, spec.Name)
			}
			agentTools[tool.Name] = spec.Name
		}
		if spec.Name == "schedule" {
			for _, alias := range spec.Aliases {
				parsed, ok := handler.parse(alias + "今天")
				if !ok || parsed.Name != "schedule" || strings.Join(parsed.Args, " ") != "today" {
					t.Fatalf("schedule alias %q attached day parsed as %#v, ok = %v", alias, parsed, ok)
				}
			}
		}
	}
	for _, name := range []string{"todo", "homework", "schedule", "notify", "bus", "teacher", "exam"} {
		if !seen[name] {
			t.Fatalf("missing command spec %q", name)
		}
	}
}

func TestCommandSpecsReturnsIsolatedSlices(t *testing.T) {
	specs := CommandSpecs()
	if len(specs) == 0 {
		t.Fatal("missing command specs")
	}
	aliasIndex := -1
	toolIndex := -1
	for i, spec := range specs {
		if len(spec.Aliases) > 0 && aliasIndex == -1 {
			aliasIndex = i
		}
		if len(spec.AgentTools) > 0 && toolIndex == -1 {
			toolIndex = i
		}
	}
	if aliasIndex == -1 || toolIndex == -1 {
		t.Fatalf("aliasIndex = %d, toolIndex = %d", aliasIndex, toolIndex)
	}

	aliasName := specs[aliasIndex].Name
	toolName := specs[toolIndex].Name
	specs[aliasIndex].Aliases[0] = "mutated"
	specs[toolIndex].AgentTools[0].CommandText = "mutated"

	handler := Handler{Prefix: "/life"}
	fresh := CommandSpecs()
	parsed, ok := handler.parse(fresh[aliasIndex].Aliases[0])
	if !ok || parsed.Name != aliasName {
		t.Fatalf("alias command parsed as %q, ok = %v, want %q", parsed.Name, ok, aliasName)
	}
	parsed, ok = handler.parse(fresh[toolIndex].AgentTools[0].CommandText)
	if !ok || parsed.Name != toolName {
		t.Fatalf("agent tool command parsed as %q, ok = %v, want %q", parsed.Name, ok, toolName)
	}
}

func TestHandleLifeCommandWithoutClientDoesNotPanic(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程 数学分析", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "Life @ USTC API unavailable") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "订阅 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "订阅 导入") {
		t.Fatalf("help reply = %q, ok = %v", reply, ok)
	}

	for _, text := range []string{"课程 help", "教学班 help", "校车 help", "状态 help"} {
		reply, ok = handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !ok || !strings.Contains(reply, "可以直接发：") {
			t.Fatalf("%q help reply = %q, ok = %v", text, reply, ok)
		}
	}
}

func TestHandleStoreCommandWithoutStoreKeepsHelp(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "通知", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "存储未配置") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "通知 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "通知 课表 开") {
		t.Fatalf("help reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleAuthCommandWithoutAuthStoreDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{},
		Prefix: "/life",
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "我", Identity: testIdentity()})
	if !ok || reply != "登录未配置。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "登录 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "登录用法：") {
		t.Fatalf("help reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleAuthCommandWithoutAuthManagerDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Prefix: "/life",
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "我", Identity: testIdentity()})
	if !ok || reply != "登录未配置。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "登录 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "登录用法：") {
		t.Fatalf("help reply = %q, ok = %v", reply, ok)
	}
}

func TestStatusWithAuthWithoutStoreDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	handler := Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{},
		Prefix: "/life",
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "状态", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "登录：未登录") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleTodoHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"待办 -h", "td help", "/life todo --help"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "待办 add 写报告") {
			t.Fatalf("unexpected reply for %q: %q", text, reply)
		}
	}
}

func TestNormalizeArgsTrimsAndDoesNotMutate(t *testing.T) {
	args := []string{" HELP ", "kept"}
	normalized := normalizeTodoArgs(args)
	if strings.Join(normalized, " ") != "help kept" {
		t.Fatalf("normalized = %#v", normalized)
	}
	if args[0] != " HELP " {
		t.Fatalf("args mutated = %#v", args)
	}

	args = []string{" KB ", "ON"}
	normalized = normalizeNotifyArgs(args)
	if strings.Join(normalized, " ") != "classes on" {
		t.Fatalf("notify normalized = %#v", normalized)
	}
	if args[0] != " KB " || args[1] != "ON" {
		t.Fatalf("notify args mutated = %#v", args)
	}

	args = []string{"作业呃开"}
	normalized = normalizeNotifyArgs(args)
	if strings.Join(normalized, " ") != "homework on" {
		t.Fatalf("compact notify normalized = %#v", normalized)
	}
	if args[0] != "作业呃开" {
		t.Fatalf("compact notify args mutated = %#v", args)
	}

	args = []string{" 开 "}
	normalized = normalizeAgentArgs(args)
	if strings.Join(normalized, " ") != "on" {
		t.Fatalf("agent normalized = %#v", normalized)
	}
	if args[0] != " 开 " {
		t.Fatalf("agent args mutated = %#v", args)
	}
}

func TestNormalizeNotificationKind(t *testing.T) {
	tests := map[string]string{
		" classes ":  "classes",
		"kb":         "classes",
		"课程":         "classes",
		"上课":         "classes",
		" homework ": "homework",
		"HW":         "homework",
		"作业":         "homework",
	}
	for input, want := range tests {
		got, ok := NormalizeNotificationKind(input)
		if !ok || got != want {
			t.Fatalf("%q = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if got, ok := NormalizeNotificationKind("校车"); ok || got != "" {
		t.Fatalf("unsupported kind = %q, %v", got, ok)
	}
}

func TestJoinedArgsTrimsJoinedText(t *testing.T) {
	if got := joinedArgs([]string{" 写", "报告 "}); got != "写 报告" {
		t.Fatalf("joinedArgs = %q", got)
	}
	if got := joinedArgs([]string{"写", " ", "报告"}); got != "写 报告" {
		t.Fatalf("joinedArgs with blank token = %q", got)
	}
	if got := joinedArgs(nil); got != "" {
		t.Fatalf("joinedArgs(nil) = %q", got)
	}
}

func TestHandleTodoAddCasual(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td 写报告", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if gotBody["title"] != "写报告" || !strings.Contains(reply, "已加待办：写报告") {
		t.Fatalf("body = %#v, reply = %q", gotBody, reply)
	}
}

func TestHandleOKConfirmsPendingCommand(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	_, err := handler.Store.SavePendingConfirmation(ctx, ident, "td 写报告", "agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := handler.Handle(ctx, Input{Text: "OK", Identity: ident})
	if !ok {
		t.Fatal("ok confirmation was not handled")
	}
	if gotBody["title"] != "写报告" || !strings.Contains(reply, "已加待办：写报告") {
		t.Fatalf("body = %#v, reply = %q", gotBody, reply)
	}
	pending, err := handler.Store.ActivePendingConfirmation(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatalf("pending confirmation still active = %#v", pending)
	}
}

func TestHandleTodoAddUsesRefreshedToken(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	refreshRequests := 0
	todoRequests := 0
	var gotBody map[string]any
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case handleOAuthRefreshMetadata(w, r, serverURL):
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			refreshRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/todos":
			todoRequests++
			if todoRequests == 1 {
				if got := r.Header.Get("Authorization"); got != "Bearer access" {
					t.Fatalf("initial authorization = %q", got)
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("refreshed authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	handler := testAuthedHandlerWithRefresh(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td 写报告", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if refreshRequests != 1 || todoRequests != 2 {
		t.Fatalf("refreshRequests = %d, todoRequests = %d", refreshRequests, todoRequests)
	}
	if gotBody["title"] != "写报告" || !strings.Contains(reply, "已加待办：写报告") {
		t.Fatalf("body = %#v, reply = %q", gotBody, reply)
	}
}

func TestLoginMentionsAutomaticPoll(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": serverURL + "/device",
			"token_endpoint":                serverURL + "/token",
			"registration_endpoint":         serverURL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device",
			"user_code":                 "USER-CODE",
			"verification_uri":          serverURL + "/verify",
			"verification_uri_complete": serverURL + "/verify?user_code=USER-CODE",
			"expires_in":                300,
			"interval":                  10,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	handler := Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Store:  s,
		Prefix: "/life",
	}
	reply, ok := handler.Handle(ctx, Input{Text: "登录", Identity: ident})
	if !ok {
		t.Fatal("login was not handled")
	}
	if !strings.Contains(reply, "系统将自动检查登录状态") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestLoginStatusAliasesPollExistingSession(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	handler := Handler{Auth: &auth.Manager{Store: s}, Prefix: "/life"}
	for _, text := range []string{"登录 状态", "登录 ok", "登录 好了", "登录 完成"} {
		reply, ok := handler.Handle(ctx, Input{Text: text, Identity: ident})
		if !ok || reply != "暂无进行中的登录。发送：登录" {
			t.Fatalf("%q reply = %q, ok = %v", text, reply, ok)
		}
	}
}

func TestHandleLoginHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"登录 help", "登录 -h", "/life login --help"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "登录用法：") || !strings.Contains(reply, "登录 状态") {
			t.Fatalf("%q reply = %q", text, reply)
		}
	}
}

func TestHandleScheduleHelpAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"课表 help", "课表 帮助", "schedule -h"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		if !strings.Contains(reply, "课表用法：") || strings.Contains(reply, "需要先登录") {
			t.Fatalf("%q reply = %q", text, reply)
		}
	}
}

func TestHandleTodoDoneByIndex(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			if r.URL.Query().Get("completed") != "false" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","dueAt":"2026-05-14T23:55:00+08:00"},{"id":"todo-2","title":"买咖啡"}]}`))
		case r.URL.Path == "/api/todos/todo-1" && r.Method == http.MethodPatch:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"completed":true`) {
				t.Fatalf("patch body = %s", body)
			}
			patched = true
			_, _ = w.Write([]byte(`{"id":"todo-1","completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td done 𝟷", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !patched || !strings.Contains(reply, "已完成：写报告") {
		t.Fatalf("patched = %v, reply = %q", patched, reply)
	}
}

func TestHandleTodoDoneUsesNumericID(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":123,"title":"写报告"}]}`))
		case r.URL.Path == "/api/todos/123" && r.Method == http.MethodPatch:
			patched = true
			_, _ = w.Write([]byte(`{"id":123,"completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td done 1", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !patched || !strings.Contains(reply, "已完成：写报告") {
		t.Fatalf("patched = %v, reply = %q", patched, reply)
	}
}

func TestHandleTodoDoneBatchByCommaSeparatedIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			if r.URL.Query().Get("completed") != "false" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"回工位收拾"},{"id":"todo-2","title":"test"},{"id":"todo-3","title":"创建 2"},{"id":"todo-4","title":"创建 1"}]}`))
		case r.URL.Path == "/api/todos/batch" && r.Method == http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "代办 完成 1,2,3,4", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 4 {
		t.Fatalf("items = %#v", gotBody["items"])
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || m["completed"] != true {
			t.Fatalf("item = %#v", item)
		}
	}
	if !strings.Contains(reply, "已完成 4 条") || !strings.Contains(reply, "回工位收拾") || !strings.Contains(reply, "创建 1") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleFeedbackSendsToConfiguredTargets(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	sent := []store.Identity{}
	messages := []string{}
	handler := Handler{
		Store:          db,
		Prefix:         "/life",
		FeedbackUsers:  []string{"1001"},
		FeedbackGroups: []string{"2001"},
		FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
			sent = append(sent, target)
			messages = append(messages, message)
			return nil
		},
	}
	reply, ok := handler.Handle(ctx, Input{Text: "反馈 校车显示有点乱", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if reply != "已收到反馈，会转给维护者。" {
		t.Fatalf("reply = %q", reply)
	}
	if len(sent) != 2 {
		t.Fatalf("sent = %#v", sent)
	}
	if sent[0].ConversationType != "private" || sent[0].ConversationID != "1001" {
		t.Fatalf("private target = %#v", sent[0])
	}
	if sent[1].ConversationType != "group" || sent[1].ConversationID != "2001" {
		t.Fatalf("group target = %#v", sent[1])
	}
	if !strings.Contains(messages[0], "用户反馈") || !strings.Contains(messages[0], "用户：42") || !strings.Contains(messages[0], "校车显示有点乱") {
		t.Fatalf("message = %q", messages[0])
	}
	count, err := db.FeedbackCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
}

func TestHandleFeedbackIncludesRecentContext(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RecordInteraction(ctx, ident, store.Interaction{
		RawText: "xc 东区 高新区",
		Command: "bus",
		Handled: true,
		Reply:   "东区 𝟷𝟸:𝟻𝟶  →  高新区 𝟷𝟹:𝟺𝟶",
		Status:  store.InteractionStatusHandled,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordInteraction(ctx, ident, store.Interaction{
		RawText: "反馈 旧反馈",
		Command: "feedback",
		Handled: true,
		Reply:   "反馈发送失败",
		Status:  store.InteractionStatusHandled,
	}); err != nil {
		t.Fatal(err)
	}
	var message string
	handler := Handler{
		Store:         db,
		Prefix:        "/life",
		FeedbackUsers: []string{"1001"},
		FeedbackSend: func(ctx context.Context, target store.Identity, sent string) error {
			message = sent
			return nil
		},
	}
	reply, ok := handler.Handle(ctx, Input{Text: "反馈 一下", Identity: ident})
	if !ok || reply != "已收到反馈，会转给维护者。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	for _, want := range []string{"内容：一下", "最近对话：", "用户：xc 东区 高新区", "Bot：东区"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
	if strings.Contains(message, "旧反馈") || strings.Contains(message, "反馈发送失败") {
		t.Fatalf("message includes previous feedback: %q", message)
	}
}

func TestHandleFeedbackWorksInGroup(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	ident.ConversationType = "group"
	ident.ConversationID = "3001"
	ident.UserID = "42"
	called := false
	handler := Handler{
		Prefix:        "/life",
		FeedbackUsers: []string{"1001"},
		FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
			called = true
			if !strings.Contains(message, "来源：group:3001") {
				t.Fatalf("message = %q", message)
			}
			return nil
		},
	}
	reply, ok := handler.Handle(ctx, Input{Text: "fb 群里也可以反馈", Identity: ident})
	if !ok || reply != "已收到反馈，会转给维护者。" || !called {
		t.Fatalf("reply = %q, ok = %v, called = %v", reply, ok, called)
	}
}

func TestHandleFeedbackStoresWhenConfiguredTargetSendFails(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var logs bytes.Buffer
	handler := Handler{
		Store:         db,
		Prefix:        "/life",
		Logger:        log.New(&logs, "", 0),
		FeedbackUsers: []string{"bad-target"},
		FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
			return errors.New("qq bot invalid request")
		},
	}
	reply, ok := handler.Handle(ctx, Input{Text: "反馈 校车显示有点乱", Identity: ident})
	if !ok || reply != "已收到反馈。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := db.FeedbackCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
	if !strings.Contains(logs.String(), "send feedback failed") || !strings.Contains(logs.String(), "qq bot invalid request") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestHandleFeedbackWithoutStoreReportsSendFailure(t *testing.T) {
	handler := Handler{
		Prefix:        "/life",
		FeedbackUsers: []string{"bad-target"},
		FeedbackSend: func(ctx context.Context, target store.Identity, message string) error {
			return errors.New("qq bot invalid request")
		},
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "反馈 校车显示有点乱", Identity: testIdentity()})
	if !ok || reply != "反馈发送失败，请稍后再试。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleGroupPersonalInfoRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	ident.ConversationType = "group"
	ident.ConversationID = "3001"

	reply, ok := Handler{Prefix: "/life"}.Handle(ctx, Input{
		Text:     "课表 help",
		Identity: ident,
	})
	if ok || reply != "" {
		t.Fatalf("default group personal reply = %q, ok = %v", reply, ok)
	}

	reply, ok = Handler{Prefix: "/life", AllowGroupPersonalInfo: true}.Handle(ctx, Input{
		Text:     "课表 help",
		Identity: ident,
	})
	if !ok || !strings.Contains(reply, "课表用法") {
		t.Fatalf("enabled group schedule reply = %q, ok = %v", reply, ok)
	}

	reply, ok = Handler{Prefix: "/life", AllowGroupPersonalInfo: true}.Handle(ctx, Input{
		Text:     "td add 写报告",
		Identity: ident,
	})
	if ok || reply != "" {
		t.Fatalf("enabled group todo write reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleFeedbackRequiresConfiguredTarget(t *testing.T) {
	reply, ok := Handler{Prefix: "/life"}.Handle(context.Background(), Input{
		Text:     "反馈 hello",
		Identity: testIdentity(),
	})
	if !ok || reply != "反馈通道还没配置。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleFeedbackRecordsWithoutConfiguredTarget(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	reply, ok := Handler{Prefix: "/life", Store: db}.Handle(ctx, Input{
		Text:     "反馈 希望支持错别字",
		Identity: ident,
	})
	if !ok || reply != "已收到反馈。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := db.FeedbackCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
}

func TestHandleTodoListWithFilters(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		values := r.URL.Query()
		if values.Get("completed") != "" || values.Get("priority") != "high" || values.Get("dueBefore") != "2026-06-10" {
			t.Fatalf("query = %s", values.Encode())
		}
		_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","priority":"high"}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td all high before 2026-06-10", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "写报告") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleTodoAddWithOptionalFields(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td add 写报告 due 2026-06-10 priority high content 读第一章", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if gotBody["title"] != "写报告" || gotBody["dueAt"] != "2026-06-10" || gotBody["priority"] != "high" || gotBody["content"] != "读第一章" {
		t.Fatalf("body = %#v", gotBody)
	}
	if !strings.Contains(reply, "已加待办：写报告") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleTodoUndoByIndex(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			if r.URL.Query().Get("completed") != "true" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","completed":true}]}`))
		case r.URL.Path == "/api/todos/todo-1" && r.Method == http.MethodPatch:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"completed":false`) {
				t.Fatalf("patch body = %s", body)
			}
			patched = true
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td undo 1", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !patched || !strings.Contains(reply, "已恢复：写报告") {
		t.Fatalf("patched = %v, reply = %q", patched, reply)
	}
}

func TestHandleTodoUpdateByIndex(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"旧标题"}]}`))
		case r.URL.Path == "/api/todos/todo-1" && r.Method == http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td update 1 title 新标题 due 2026-06-10 priority low content 备注", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if gotBody["title"] != "新标题" || gotBody["dueAt"] != "2026-06-10" || gotBody["priority"] != "low" || gotBody["content"] != "备注" {
		t.Fatalf("body = %#v", gotBody)
	}
	if !strings.Contains(reply, "已修改待办：旧标题") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestParseTodoUpdateArgsIncludesContent(t *testing.T) {
	opts := parseTodoUpdateArgs([]string{"title", "新标题", "due", "2026-06-10", "priority", "low", "content", "备注"})
	if opts.Title != "新标题" || opts.DueAt != "2026-06-10" || opts.Priority != "low" || opts.Content != "备注" {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestHandleTodoDeleteByIndex(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告"}]}`))
		case r.URL.Path == "/api/todos/todo-1" && r.Method == http.MethodDelete:
			deleted = true
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "td delete 1", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !deleted || !strings.Contains(reply, "已删除：写报告") {
		t.Fatalf("deleted = %v, reply = %q", deleted, reply)
	}
}

func TestTodoCompletionReply(t *testing.T) {
	tests := map[string]string{
		"写报告":  "已完成：写报告",
		" \t ": "已完成。",
	}
	for title, want := range tests {
		if got := todoCompletionReply(title); got != want {
			t.Fatalf("todoCompletionReply(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestResolveTodoMatchesDisplayDigitsInTitle(t *testing.T) {
	todos := []map[string]any{
		{"id": "todo-1", "title": "写报告 1"},
		{"id": "todo-2", "title": "写报告 2"},
	}
	todo, ok := resolveTodo(todos, "报告 𝟸")
	if !ok || lifedata.FirstString(todo, "id") != "todo-2" {
		t.Fatalf("todo = %#v, ok = %v", todo, ok)
	}
}

func TestResolveTodoRejectsBlankTarget(t *testing.T) {
	todos := []map[string]any{{"id": "todo-1", "title": "写报告"}}
	if todo, ok := resolveTodo(todos, " \t "); ok || todo != nil {
		t.Fatalf("todo = %#v, ok = %v", todo, ok)
	}
}

func TestFormatTodoDueDateFirst(t *testing.T) {
	line := textutil.MonospaceDigits(formatTodo(map[string]any{
		"title": "写报告",
		"dueAt": "2026-05-14T23:55:00+08:00",
	}))
	if line != "截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻 写报告" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatNumberedLinePadsBeforeTab(t *testing.T) {
	line := formatNumberedLine(1, "截止 05-14 23:55 写报告")
	if line != "𝟷. \t截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻 写报告" {
		t.Fatalf("line = %q", line)
	}
}

func TestMoreLineFormatting(t *testing.T) {
	if got := moreLine(12, false); got != "...and 12 more" {
		t.Fatalf("plain moreLine = %q", got)
	}
	if got := moreLine(12, true); got != "...and 𝟷𝟸 more" {
		t.Fatalf("monospace moreLine = %q", got)
	}
}

func TestChinaNowUsesChinaLocation(t *testing.T) {
	now := chinaNow()
	name, offset := now.Zone()
	if now.Location().String() != lifedata.ChinaLocation().String() || name != "CST" || offset != 8*60*60 {
		t.Fatalf("chinaNow location = %v, zone = %s, offset = %d", now.Location(), name, offset)
	}
}

func TestHandleHomeworkListAndDone(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/me/subscriptions/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null},{"id":"hw-2","title":"Old PS","submissionDueAt":"2026-05-01T12:00:00+08:00","section":{"course":{"namePrimary":"组合数学"}},"completion":null}]}`))
		case r.URL.Path == "/api/homeworks/hw-1/completion" && r.Method == http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			completed = body["completed"] == true
			_, _ = w.Write([]byte(`{"completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "作业", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "已逾期：") || !strings.Contains(reply, "截止 𝟶𝟼-𝟶𝟹 𝟷𝟸:𝟶𝟶 · 数据库系统 · Problem Set 𝟷") {
		t.Fatalf("reply = %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "作业 done 𝟸", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !completed || !strings.Contains(reply, "已完成作业：Problem Set 1") {
		t.Fatalf("completed = %v, reply = %q", completed, reply)
	}
}

func TestHandleHomeworkDoneBatchByCommaSeparatedIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/me/subscriptions/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00","completion":null},{"id":"hw-2","title":"Problem Set 2","submissionDueAt":"2026-06-04T12:00:00+08:00","completion":null},{"id":"hw-3","title":"Problem Set 3","submissionDueAt":"2026-06-05T12:00:00+08:00","completion":null}]}`))
		case r.URL.Path == "/api/homeworks/completions" && r.Method == http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "作业 done 1,2,3", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items = %#v", gotBody["items"])
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || m["completed"] != true {
			t.Fatalf("item = %#v", item)
		}
	}
	if !strings.Contains(reply, "已完成 3 条作业") || !strings.Contains(reply, "Problem Set 1") || !strings.Contains(reply, "Problem Set 3") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHomeworkCompletionReply(t *testing.T) {
	tests := []struct {
		completed bool
		title     string
		want      string
	}{
		{completed: true, title: "Problem Set 1", want: "已完成作业：Problem Set 1"},
		{completed: true, title: " \t ", want: "已完成作业。"},
		{completed: false, title: "Problem Set 1", want: "已取消完成：Problem Set 1"},
		{completed: false, title: "", want: "已取消完成。"},
	}
	for _, tt := range tests {
		if got := homeworkCompletionReply(tt.completed, tt.title); got != tt.want {
			t.Fatalf("homeworkCompletionReply(%v, %q) = %q, want %q", tt.completed, tt.title, got, tt.want)
		}
	}
}

func TestResolveHomeworkMatchesDisplayDigitsInTitle(t *testing.T) {
	homeworks := []map[string]any{
		{"id": "hw-1", "title": "Problem Set 1"},
		{"id": "hw-2", "title": "Problem Set 2"},
	}
	homework, ok := resolveHomework(homeworks, "set 𝟸")
	if !ok || lifedata.FirstString(homework, "id") != "hw-2" {
		t.Fatalf("homework = %#v, ok = %v", homework, ok)
	}
	homework, ok = resolveHomework(homeworks, "２")
	if !ok || lifedata.FirstString(homework, "id") != "hw-2" {
		t.Fatalf("fullwidth homework = %#v, ok = %v", homework, ok)
	}
}

func TestResolveHomeworkRejectsBlankTarget(t *testing.T) {
	homeworks := []map[string]any{{"id": "hw-1", "title": "Problem Set 1"}}
	if homework, ok := resolveHomework(homeworks, " \t "); ok || homework != nil {
		t.Fatalf("homework = %#v, ok = %v", homework, ok)
	}
}

func TestFormatHomeworkListGroupsByDueTime(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, lifedata.ChinaLocation())
	reply := formatHomeworkListAt([]map[string]any{
		{"id": "overdue", "title": "Past", "submissionDueAt": "2026-06-07T11:00:00+08:00"},
		{"id": "nearby", "title": "Soon", "submissionDueAt": "2026-06-14T12:00:00+08:00"},
		{"id": "future", "title": "Later", "submissionDueAt": "2026-06-14T12:01:00+08:00"},
	}, now)
	for _, want := range []string{"已逾期：", "近期：", "未来："} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Index(reply, "Past") > strings.Index(reply, "Soon") || strings.Index(reply, "Soon") > strings.Index(reply, "Later") {
		t.Fatalf("reply order = %q", reply)
	}
}

func TestHandleOverviewCombinesPersonalData(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	now := chinaNow()
	today := now.Format("2006-01-02")
	wantExamDate := textutil.MonospaceDigits(now.Format("01-02"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/calendar-subscriptions/current":
			_, _ = fmt.Fprintf(w, `{"subscription":{"sections":[
				{"id":101,"code":"CS1001.01","course":{"namePrimary":"计算机导论"},"semester":{"startDate":"2026-02-01T00:00:00+08:00","endDate":"2026-07-01T00:00:00+08:00"},"exams":[{"id":1,"examDate":%q,"startTime":900,"endTime":1100,"examRooms":[{"room":"GT-B112"}]}]}
			]}}`, today+"T00:00:00+08:00")
		case "/api/me/subscriptions/schedules":
			_, _ = fmt.Fprintf(w, `{"schedules":[{"id":1,"date":%q,"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"计算机导论"}},"room":{"namePrimary":"3A101"}}]}`, today+"T00:00:00+08:00")
		case "/api/todos":
			if r.URL.Query().Get("completed") != "false" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = fmt.Fprintf(w, `{"todos":[{"id":"todo-1","title":"写报告","dueAt":%q}]}`, today+"T18:00:00+08:00")
		case "/api/me/subscriptions/homeworks":
			_, _ = fmt.Fprintf(w, `{"homeworks":[{"id":"hw-1","title":"作业一","submissionDueAt":%q,"section":{"course":{"namePrimary":"数学分析"}}}]}`, today+"T23:59:00+08:00")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "今日", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{"安排：", "今日课表：", "计算机导论", "待办：", "写报告", "近期作业：", "作业一", "考试：", wantExamDate} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestHandleExamListFromSubscriptionPayload(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/api/calendar-subscriptions/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"sections":[
			{"code":"MATH1001.01","course":{"namePrimary":"数学分析"},"exams":[{"id":2,"examDate":"2026-06-20T00:00:00+08:00","startTime":1430,"endTime":1630,"examMode":"闭卷","examRooms":[{"room":"3A101"}]}]},
			{"code":"CS1001.01","course":{"namePrimary":"计算机导论"},"exams":[{"id":1,"examDate":"2026-06-10T00:00:00+08:00","startTime":900,"endTime":1100,"examRooms":[{"room":"GT-B112"}]}]}
		]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "考试", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{
		"考试：",
		"𝟷. \t𝟶𝟼-𝟷𝟶 · 𝟶𝟿:𝟶𝟶-𝟷𝟷:𝟶𝟶 · 计算机导论 · 𝙲𝚂𝟷𝟶𝟶𝟷.𝟶𝟷 · 𝙶𝚃-𝙱𝟷𝟷𝟸",
		"𝟸. \t𝟶𝟼-𝟸𝟶 · 𝟷𝟺:𝟹𝟶-𝟷𝟼:𝟹𝟶 · 数学分析 · 𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷 · 闭卷 · 𝟹𝙰𝟷𝟶𝟷",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Index(reply, "计算机导论") > strings.Index(reply, "数学分析") {
		t.Fatalf("reply not sorted by date: %q", reply)
	}
}

func TestHandleExamListEmpty(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"subscription":{"sections":[{"code":"MATH1001.01","exams":[]}]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "ks", Identity: ident})
	if !ok || reply != "没有订阅课程考试。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestFormatExamDateUnknown(t *testing.T) {
	line := formatExam(subscriptionExam{
		exam:    map[string]any{"id": "exam-1"},
		section: map[string]any{"course": map[string]any{"namePrimary": "随机过程"}},
	})
	if line != "日期待定 · 随机过程" {
		t.Fatalf("line = %q", line)
	}
}

func TestHandleTodayCurriculum(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/me/subscriptions/schedules":
			if !strings.HasSuffix(r.URL.Query().Get("dateFrom"), "Z") || !strings.HasSuffix(r.URL.Query().Get("dateTo"), "Z") {
				t.Fatalf("date range = %q %q", r.URL.Query().Get("dateFrom"), r.URL.Query().Get("dateTo"))
			}
			_, _ = w.Write([]byte(`{"schedules":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "今天课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "西区 𝟹𝙰𝟸𝟶𝟺\t𝟶𝟿:𝟻𝟶-𝟷𝟷:𝟸𝟻\t数据库系统") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleCurriculumForSpecificDate(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, lifedata.ChinaLocation())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/api/me/subscriptions/schedules" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(r.URL.Query().Get("dateFrom"), "2026-06-22T16:00:00Z") || !strings.Contains(r.URL.Query().Get("dateTo"), "2026-06-23T15:59:59Z") {
			t.Fatalf("date range = %q %q", r.URL.Query().Get("dateFrom"), r.URL.Query().Get("dateTo"))
		}
		_, _ = w.Write([]byte(`{"schedules":[{"date":"2026-06-23T00:00:00+08:00","startTime":"07:50","endTime":"09:25","section":{"course":{"namePrimary":"随机过程理论"}},"room":{"namePrimary":"GT-A405"}}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, []string{"date:6.23"}, base)
	if !strings.Contains(reply, "𝟶𝟼-𝟸𝟹 课表：") || !strings.Contains(reply, "随机过程理论") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestCurriculumUsesRefreshedTokenForSchedules(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	scheduleCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case handleOAuthRefreshMetadata(w, r, serverURL):
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/me/subscriptions/schedules":
			scheduleCalls++
			if scheduleCalls == 1 {
				if got := r.Header.Get("Authorization"); got != "Bearer access" {
					t.Fatalf("initial schedules authorization = %q", got)
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("schedules authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"schedules":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	handler := testAuthedHandlerWithRefresh(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "今天课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "数据库系统") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestBareCurriculumReusesRefreshedTokenAcrossDays(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, lifedata.ChinaLocation())
	today := day.Format("2006-01-02")
	tomorrow := day.AddDate(0, 0, 1).Format("2006-01-02")
	refreshRequests := 0
	scheduleOldTokenCalls := 0
	scheduleCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case handleOAuthRefreshMetadata(w, r, serverURL):
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			refreshRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/me/subscriptions/schedules":
			switch r.Header.Get("Authorization") {
			case "Bearer access":
				scheduleOldTokenCalls++
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			case "Bearer refreshed":
				scheduleCalls++
				if scheduleCalls == 1 {
					_, _ = w.Write([]byte(fmt.Sprintf(`{"schedules":[{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}}}]}`, today)))
					return
				}
				_, _ = w.Write([]byte(fmt.Sprintf(`{"schedules":[{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}}}]}`, tomorrow)))
			default:
				t.Fatalf("schedules authorization = %q", r.Header.Get("Authorization"))
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	handler := testAuthedHandlerWithRefresh(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, nil, day)
	if !strings.Contains(reply, "数据库系统") || !strings.Contains(reply, "编译原理") {
		t.Fatalf("reply = %q", reply)
	}
	if refreshRequests != 1 || scheduleOldTokenCalls != 1 || scheduleCalls != 2 {
		t.Fatalf("refreshRequests = %d, scheduleOldTokenCalls = %d, scheduleCalls = %d", refreshRequests, scheduleOldTokenCalls, scheduleCalls)
	}
}

func TestBareCurriculumShowsTodayAndTomorrowAtFixedDate(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	scheduleCalls := 0
	day := time.Date(2026, 6, 7, 12, 0, 0, 0, lifedata.ChinaLocation())
	today := day.Format("2006-01-02")
	tomorrow := day.AddDate(0, 0, 1).Format("2006-01-02")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/me/subscriptions/schedules":
			scheduleCalls++
			if scheduleCalls == 1 {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"schedules":[{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`, today)))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"schedules":[{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}},"room":{"namePrimary":"GT-B112"}}]}`, tomorrow)))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, nil, day)
	if !strings.Contains(reply, "今明两日课表：") || !strings.Contains(reply, "今天：") || !strings.Contains(reply, "明天：") {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(reply, "数据库系统") || !strings.Contains(reply, "编译原理") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNextClassSkipsPastClassAtFixedTime(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/me/subscriptions/schedules":
			_, _ = w.Write([]byte(`{"schedules":[{"startTime":"09:00","endTime":"09:45","section":{"course":{"namePrimary":"已过去"}}},{"startTime":"11:00","endTime":"11:45","section":{"course":{"namePrimary":"下一节"}}}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	now := time.Date(2026, 6, 7, 10, 0, 0, 0, lifedata.ChinaLocation())
	reply := handler.nextClassAt(ctx, ident, now)
	if !strings.Contains(reply, "下一节课：") || !strings.Contains(reply, "下一节") || strings.Contains(reply, "已过去") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestFetchSchedulesForSectionsLimitsConcurrency(t *testing.T) {
	var current int32
	var maxSeen int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/schedules" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		now := atomic.AddInt32(&current, 1)
		for {
			previous := atomic.LoadInt32(&maxSeen)
			if now <= previous || atomic.CompareAndSwapInt32(&maxSeen, previous, now) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	sectionIDs := make([]string, 20)
	for i := range sectionIDs {
		sectionIDs[i] = strconv.Itoa(i + 1)
	}
	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	_, err := handler.fetchSchedulesForSections(context.Background(), "token", sectionIDs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&maxSeen); got > 8 {
		t.Fatalf("max concurrency = %d", got)
	}
}

func TestSubscriptionHelpDoesNotList(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "订阅 help", Identity: testIdentity()})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "订阅 导入") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNotificationSettingsCommand(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	handler := Handler{Store: s, Prefix: "/life"}

	reply, ok := handler.Handle(ctx, Input{Text: "通知", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：关") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "设置", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：关") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("settings alias reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 课表 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 作业呃开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：开") {
		t.Fatalf("compact typo reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 作业 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：开") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 上课 开启", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：开") {
		t.Fatalf("class alias reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 hw 关闭", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("homework alias reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 课表", Identity: ident})
	if !ok || !strings.Contains(reply, "想打开还是关闭") {
		t.Fatalf("missing state reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "通知 校车", Identity: ident})
	if !ok || !strings.Contains(reply, "支持：课表、作业") {
		t.Fatalf("invalid kind reply = %q, ok = %v", reply, ok)
	}

	paddedIdent := ident
	paddedIdent.ConversationType = " PRIVATE "
	reply, ok = handler.Handle(ctx, Input{Text: "通知", Identity: paddedIdent})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("padded private reply = %q, ok = %v", reply, ok)
	}
}

func TestAgentSettingsCommand(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	handler := Handler{Store: s, Prefix: "/life"}

	reply, ok := handler.Handle(ctx, Input{Text: "AI 工具", Identity: ident})
	if !ok || !strings.Contains(reply, "AI 工具调用展示：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "AI 工具 开", Identity: ident})
	if !ok || !strings.Contains(reply, "AI 工具调用展示：开") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	settings, err := s.AgentSettings(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.ExposeToolCalls {
		t.Fatalf("settings = %#v", settings)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "AI 工具 关", Identity: ident})
	if !ok || !strings.Contains(reply, "AI 工具调用展示：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "AI 工具 maybe", Identity: ident})
	if !ok || !strings.Contains(reply, "想打开还是关闭") {
		t.Fatalf("invalid reply = %q, ok = %v", reply, ok)
	}
	groupIdent := ident
	groupIdent.ConversationType = "group"
	reply, ok = handler.Handle(ctx, Input{Text: "AI 工具 开", Identity: groupIdent})
	if ok || reply != "" {
		t.Fatalf("group reply = %q, ok = %v", reply, ok)
	}
}

func TestSubscriptionListGroupsBySemester(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/calendar-subscriptions/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"sections":[
			{"id":101,"code":"CONT5103P.01","course":{"namePrimary":"随机过程理论"},"semester":{"nameCn":"2026年春季学期"}},
			{"id":102,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"},"semester":{"nameCn":"2026年春季学期"}},
			{"id":201,"code":"MATH1001.01","course":{"namePrimary":"数学分析"},"semester":{"nameCn":"2025年秋季学期"}}
		]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{"日程订阅：", "2026年春季学期：", "2025年秋季学期：", "- 𝙲𝙾𝙽𝚃𝟻𝟷𝟶𝟹𝙿.𝟶𝟷  \t随机过程理论", "- 𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷   \t数学分析"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Index(reply, "2026年春季学期：") > strings.Index(reply, "2025年秋季学期：") {
		t.Fatalf("semester order changed: %q", reply)
	}
	if strings.Contains(reply, "...and") {
		t.Fatalf("reply should not be folded: %q", reply)
	}
}

func TestSubscriptionSectionIDIntsSkipsNonPositiveIDs(t *testing.T) {
	data := map[string]any{
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{"id": float64(-1)},
				map[string]any{"id": float64(0)},
				map[string]any{"id": float64(101)},
			},
		},
	}
	ids := subscriptionSectionIDInts(data)
	if strings.Join(intStrings(ids), ",") != "101" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestBulkSubscribeSectionsAddsMatchedSections(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/calendar-subscriptions/batch":
			var req struct {
				Action string   `json:"action"`
				Codes  []string `json:"codes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Action != "add" {
				t.Fatalf("action = %q", req.Action)
			}
			if strings.Join(req.Codes, ",") != "CONT5103P.01,CONT6104P.01,BAD000.01" {
				t.Fatalf("codes = %#v", req.Codes)
			}
			_, _ = w.Write([]byte(`{
				"semester":{"nameCn":"2026年春季学期"},
				"matchedCodes":["CONT5103P.01","CONT6104P.01"],
				"unmatchedCodes":["BAD000.01"],
				"sections":[
					{"id":101,"code":"CONT5103P.01","course":{"namePrimary":"随机过程理论"}},
					{"id":202,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"}}
				],
				"addedCount":1,
				"unchangedCount":1
			}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅 导入 cont5103p.01, CONT6104P.01 BAD000.01", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	for _, want := range []string{"已订阅 𝟸 个教学班（新增 𝟷 个，已存在 𝟷 个）。", "2026年春季学期", "𝙲𝙾𝙽𝚃𝟼𝟷𝟶𝟺𝙿.𝟶𝟷  \t组合数学", "𝙱𝙰𝙳𝟶𝟶𝟶.𝟶𝟷"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestBulkSubscribeSectionsUsesRefreshedToken(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	importCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case handleOAuthRefreshMetadata(w, r, serverURL):
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/calendar-subscriptions/batch":
			importCalls++
			if importCalls == 1 {
				if got := r.Header.Get("Authorization"); got != "Bearer access" {
					t.Fatalf("initial import authorization = %q", got)
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("refreshed import authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{
				"semester":{"nameCn":"2026年春季学期"},
				"matchedCodes":["CONT6104P.01"],
				"unmatchedCodes":[],
				"sections":[{"id":202,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"}}],
				"addedCount":1,
				"unchangedCount":0
			}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	handler := testAuthedHandlerWithRefresh(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅 导入 CONT6104P.01", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if importCalls != 2 {
		t.Fatalf("importCalls = %d", importCalls)
	}
	if !strings.Contains(reply, "组合数学") {
		t.Fatalf("reply missing course: %q", reply)
	}
}

func TestFormatScheduleLocationFirstAndFixedWidth(t *testing.T) {
	line := formatSchedule(map[string]any{
		"startTime":   "07:50",
		"endTime":     "09:25",
		"customPlace": "GT-A405",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "随机过程理论"},
		},
	})
	if line != "𝙶𝚃-𝙰𝟺𝟶𝟻 \t𝟶𝟽:𝟻𝟶-𝟶𝟿:𝟸𝟻\t随机过程理论" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatScheduleOmitsMissingTimeColumn(t *testing.T) {
	line := formatSchedule(map[string]any{
		"customPlace": "GT-A405",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "随机过程理论"},
		},
	})
	if line != "𝙶𝚃-𝙰𝟺𝟶𝟻 \t随机过程理论" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatScheduleFallsBackToID(t *testing.T) {
	line := formatSchedule(map[string]any{
		"section": map[string]any{"id": "101"},
	})
	if line != "𝟷𝟶𝟷" {
		t.Fatalf("line = %q", line)
	}
}

func TestSubscriptionSectionIDsForDayFiltersSemester(t *testing.T) {
	data := map[string]any{
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{
					"id": "current",
					"semester": map[string]any{
						"startDate": "2026-03-01T08:00:00+08:00",
						"endDate":   "2026-07-03T08:00:00+08:00",
					},
				},
				map[string]any{
					"id": "old",
					"semester": map[string]any{
						"startDate": "2025-09-07T08:00:00+08:00",
						"endDate":   "2026-01-23T08:00:00+08:00",
					},
				},
			},
		},
	}
	day := time.Date(2026, 6, 2, 12, 0, 0, 0, lifedata.ChinaLocation())
	ids := lifedata.SubscriptionSectionIDsForDay(data, day)
	if len(ids) != 1 || ids[0] != "current" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestFilterSchedulesForDayDropsAdjacentDates(t *testing.T) {
	day := time.Date(2026, 6, 2, 12, 0, 0, 0, lifedata.ChinaLocation())
	schedules := []map[string]any{
		{"date": "2026-06-01T08:00:00+08:00", "startTime": "07:50"},
		{"date": "2026-06-02T08:00:00+08:00", "startTime": "09:45"},
	}
	filtered := lifedata.FilterSchedulesForDay(schedules, day)
	if len(filtered) != 1 || lifedata.FirstString(filtered[0], "startTime") != "09:45" {
		t.Fatalf("filtered = %#v", filtered)
	}
}

func TestNormalizeCommandAliases(t *testing.T) {
	tests := map[string]string{
		"待办": "todo",
		"代办": "todo",
		"td": "todo",
		"校车": "bus",
		"xc": "bus",
		"日程": "schedule",
		"rc": "schedule",
		"kb": "schedule",
		"老师": "teacher",
		"js": "teacher",
		"考试": "exam",
		"ks": "exam",
		"状态": "status",
		"zt": "status",
		"设置": "notify",
		"反馈": "feedback",
		"fb": "feedback",
	}
	handler := Handler{Prefix: "/life"}
	for text, want := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != want {
			t.Fatalf("%q parsed as %q, want %q", text, cmd.Name, want)
		}
	}
}

func TestParseAttachedFeedbackAndNotifyCommands(t *testing.T) {
	handler := Handler{Prefix: "/life"}

	cmd, ok := handler.parse("反馈上面的对话问题")
	if !ok || cmd.Name != "feedback" || strings.Join(cmd.Args, " ") != "上面的对话问题" {
		t.Fatalf("feedback parsed as %#v, ok=%v", cmd, ok)
	}

	cmd, ok = handler.parse("通知课表开")
	if !ok || cmd.Name != "notify" || strings.Join(cmd.Args, " ") != "classes on" {
		t.Fatalf("notify parsed as %#v, ok=%v", cmd, ok)
	}

	cmd, ok = handler.parse("设置作业呃开")
	if !ok || cmd.Name != "notify" || strings.Join(cmd.Args, " ") != "homework on" {
		t.Fatalf("settings parsed as %#v, ok=%v", cmd, ok)
	}
}

func TestPrefixedUnknownCommandParsesAsHelp(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	cmd, ok := handler.parse("/life nope")
	if !ok {
		t.Fatal("command was not parsed")
	}
	if cmd.Name != "help" {
		t.Fatalf("command name = %q, want help", cmd.Name)
	}
	if len(cmd.Args) != 0 {
		t.Fatalf("args = %#v", cmd.Args)
	}
}

func TestAttachedPrefixCommandParses(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	tests := map[string]struct {
		name string
		args []string
	}{
		"/life校车 东区 西区": {name: "bus", args: []string{"东区", "西区"}},
		"/lifekb今天":     {name: "schedule", args: []string{"today"}},
	}
	for text, want := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != want.name || strings.Join(cmd.Args, " ") != strings.Join(want.args, " ") {
			t.Fatalf("%q parsed as name=%q args=%#v, want name=%q args=%#v", text, cmd.Name, cmd.Args, want.name, want.args)
		}
	}
}

func TestAttachedPrefixUnknownCommandParsesAsHelp(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	cmd, ok := handler.parse("/lifenope")
	if !ok {
		t.Fatal("command was not parsed")
	}
	if cmd.Name != "help" {
		t.Fatalf("command name = %q, want help", cmd.Name)
	}
}

func TestNormalizeSubscriptionImportAliases(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{
		"订阅 导入 CONT5103P.01",
		"订阅 批量 CONT5103P.01",
		"订阅 添加 CONT5103P.01",
		"订阅 新增 CONT5103P.01",
		"订阅 + CONT5103P.01",
		"sub add CONT5103P.01",
	} {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "subscription" || strings.Join(cmd.Args, " ") != "import CONT5103P.01" {
			t.Fatalf("%q parsed as name=%q args=%#v", text, cmd.Name, cmd.Args)
		}
	}
}

func TestNormalizeTodoActionAliases(t *testing.T) {
	tests := map[string][]string{
		"td + 买咖啡":         {"add", "买咖啡"},
		"待办 添加 写报告":        {"add", "写报告"},
		"代办 新增 写报告":        {"add", "写报告"},
		"todo create task": {"add", "task"},
		"td 完成 1":          {"done", "1"},
		"待办 好了 1":          {"done", "1"},
		"todo finish 1":    {"done", "1"},
		"td x 1":           {"done", "1"},
		"td 撤销 1":          {"undo", "1"},
		"td 删除 1":          {"delete", "1"},
		"td 修改 1 title x":  {"update", "1", "title", "x"},
		"td 全部":            {"all"},
		"td 已完成":           {"completed"},
	}
	handler := Handler{Prefix: "/life"}
	for text, wantArgs := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "todo" || strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("%q parsed as name=%q args=%#v", text, cmd.Name, cmd.Args)
		}
	}
}

func TestParseSlashCommandAliases(t *testing.T) {
	tests := map[string]struct {
		name string
		args []string
	}{
		"/校车 东区 西区": {name: "bus", args: []string{"东区", "西区"}},
		"/待办":       {name: "todo"},
		"/作业":       {name: "homework"},
		"/课表":       {name: "schedule"},
	}
	handler := Handler{Prefix: "/life"}
	for text, want := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != want.name || strings.Join(cmd.Args, " ") != strings.Join(want.args, " ") {
			t.Fatalf("%q parsed as name=%q args=%#v", text, cmd.Name, cmd.Args)
		}
	}
}

func TestHandleRejectsPastedCommandLines(t *testing.T) {
	text := strings.Join([]string{
		"待办 add 组合数学 期末考试 due 2026-06-25 07:50",
		"",
		"待办 add 随机过程理论 期末考试 due 2026-06-25 09:45",
	}, "\n")
	reply, ok := Handler{Prefix: "/life"}.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
	if !ok || reply != "检测到多条命令。为避免误操作，一次只处理一条；请分开发送。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestNormalizeHomeworkActionAliases(t *testing.T) {
	tests := map[string][]string{
		"作业 取消 1":    {"undo", "1"},
		"作业 撤销 1":    {"undo", "1"},
		"hw reset 1": {"undo", "1"},
		"作业 全部":      {"all"},
		"hw all":     {"all"},
		"作业 未完成":     {"pending"},
		"hw pending": {"pending"},
	}
	handler := Handler{Prefix: "/life"}
	for text, wantArgs := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "homework" || strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("%q parsed as name=%q args=%#v", text, cmd.Name, cmd.Args)
		}
	}
}

func TestNormalizeScheduleTypos(t *testing.T) {
	tests := map[string][]string{
		"课标":                {},
		"today kb":          {"today"},
		"todaykb":           {"today"},
		"kb today":          {"today"},
		"今天 课表":             {"today"},
		"今天课标":              {"today"},
		"今日课标":              {"today"},
		"课表今天":              {"today"},
		"kb今天":              {"today"},
		"tomorrow schedule": {"tomorrow"},
		"tomorrowsched":     {"tomorrow"},
		"sched tomorrow":    {"tomorrow"},
		"明天 课表":             {"tomorrow"},
		"明天课标":              {"tomorrow"},
		"明日课标":              {"tomorrow"},
		"课表明天":              {"tomorrow"},
		"明日kb":              {"tomorrow"},
		"课表 6.23":           {"date:6.23"},
		"6.23 课表":           {"date:6.23"},
		"课表6月23日":           {"date:6月23日"},
	}
	handler := Handler{Prefix: "/life"}
	for text, wantArgs := range tests {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "schedule" {
			t.Fatalf("%q parsed as %q, want schedule", text, cmd.Name)
		}
		if strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, cmd.Args, wantArgs)
		}
	}
}

func TestNormalizeJoinedNextClass(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"下一节课", "下一 节课"} {
		cmd, ok := handler.parse(text)
		if !ok {
			t.Fatalf("%q was not parsed", text)
		}
		if cmd.Name != "nextclass" {
			t.Fatalf("%q parsed as %q, want nextclass", text, cmd.Name)
		}
	}
}

func intStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

func TestCurrentSemesterUsesNumericID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":202602}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply := handler.currentSemester(context.Background())
	if reply != "当前学期：202602" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestCurrentSemesterWithoutLabelIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	reply := handler.currentSemester(context.Background())
	if reply != "当前学期未知。" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleSuppressLog(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nameCn":"2026年春季学期"}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "学期", Identity: ident, SuppressLog: true})
	if !ok || reply == "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := handler.Store.InteractionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestHandleSkipsLogForIncompleteConversationIdentity(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	handler := Handler{Store: s, Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{
		Text: "/life help",
		Identity: store.Identity{
			Platform: "napcat",
			UserID:   "42",
		},
	})
	if !ok || reply == "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	count, err := s.InteractionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("interaction count = %d", count)
	}
}

func TestPrefixedUnknownCommandLogsAsHelp(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ident := testIdentity()
	handler := Handler{Store: s, Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "/life nope", Identity: ident})
	if !ok || !strings.Contains(reply, "待办 / td") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	recent, err := s.RecentHandledInteractions(context.Background(), ident, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent = %#v", recent)
	}
	if recent[0].Command != "help" {
		t.Fatalf("logged command = %q, want help", recent[0].Command)
	}
}

func TestRecordInteractionUsesJoinedArgs(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	handler := Handler{Store: s}
	handler.recordInteraction(ctx, ident, parsedCommand{
		Name: "todo",
		Args: []string{" done ", " 1 "},
		Raw:  "td done 1",
	}, "ok")

	recent, err := s.RecentHandledInteractions(ctx, ident, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Args != "done 1" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestHandleLogsRecordFailures(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	handler := Handler{
		Store:  s,
		Prefix: "/life",
		Logger: log.New(&logs, "", 0),
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "/life help", Identity: testIdentity()})
	if !ok || reply == "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	if !strings.Contains(logs.String(), "record conversation state failed") {
		t.Fatalf("missing state log: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "record command interaction failed") {
		t.Fatalf("missing interaction log: %q", logs.String())
	}
}

func TestAccessTokenReturnsFalseWhenUnavailable(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	handler := Handler{}
	token, ok := handler.accessToken(ctx, ident)
	if ok || token != "" {
		t.Fatalf("nil auth token = %q, ok = %v", token, ok)
	}

	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	handler.Auth = &auth.Manager{Store: s}
	token, ok = handler.accessToken(ctx, ident)
	if ok || token != "" {
		t.Fatalf("missing credential token = %q, ok = %v", token, ok)
	}
}

func TestParseSearchCoursesArgsWithFilters(t *testing.T) {
	opts := parseSearchCoursesArgs([]string{"keyword", "数学分析", "education_level_id", "1", "category_id", "2", "class_type_id", "3", "limit", "10"})
	if opts.Keyword != "数学分析" || opts.EducationLevelID != 1 || opts.CategoryID != 2 || opts.ClassTypeID != 3 || opts.Limit != 10 {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestParseSearchSectionsArgsWithFilters(t *testing.T) {
	opts := parseSearchSectionsArgs([]string{
		"keyword", "高等数学",
		"course_id", "11",
		"course_jw_id", "12",
		"semester_id", "13",
		"semester_jw_id", "14",
		"campus_id", "15",
		"department_id", "16",
		"teacher_id", "17",
		"teacher_code", "T001",
		"limit", "20",
	})
	if opts.Keyword != "高等数学" ||
		opts.CourseID != 11 || opts.CourseJwID != 12 ||
		opts.SemesterID != 13 || opts.SemesterJwID != 14 ||
		opts.CampusID != 15 || opts.DepartmentID != 16 ||
		opts.TeacherID != 17 || opts.TeacherCode != "T001" ||
		opts.Limit != 20 {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestParseSearchTeachersArgsWithFilters(t *testing.T) {
	opts := parseSearchTeachersArgs([]string{"keyword", "张", "department_id", "5", "limit", "8"})
	if opts.Keyword != "张" || opts.DepartmentID != 5 || opts.Limit != 8 {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestHandleCourseSearchWithFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/courses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("search") != "数学分析" || q.Get("educationLevelId") != "1" || q.Get("categoryId") != "2" || q.Get("classTypeId") != "3" || q.Get("limit") != "10" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1006","namePrimary":"数学分析"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程搜索 keyword 数学分析 education_level_id 1 category_id 2 class_type_id 3 limit 10"})
	if !ok || !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟼") || !strings.Contains(reply, "数学分析") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleSectionSearchWithFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("search") != "高等数学" || q.Get("courseId") != "11" || q.Get("teacherCode") != "T001" || q.Get("limit") != "20" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001.01","course":{"namePrimary":"高等数学"}}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "教学班搜索 keyword 高等数学 course_id 11 teacher_code T001 limit 20"})
	if !ok || !strings.Contains(reply, "𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷") || !strings.Contains(reply, "高等数学") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleTeacherSearchWithFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/teachers" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("search") != "张" || q.Get("departmentId") != "5" || q.Get("limit") != "8" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"data":[{"id":12,"code":"T001","namePrimary":"张三","department":{"namePrimary":"数学科学学院"},"teacherTitle":{"namePrimary":"教授"}}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "老师搜索 keyword 张 department_id 5 limit 8"})
	if !ok || !strings.Contains(reply, "张三") || !strings.Contains(reply, "数学科学学院") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleCourseByJwID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/courses/123" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"CS1001","namePrimary":"计算机导论"}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程编号 123"})
	if !ok || !strings.Contains(reply, "𝙲𝚂𝟷𝟶𝟶𝟷") || !strings.Contains(reply, "计算机导论") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleSectionByJwID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections/456" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"CS1001.01","course":{"namePrimary":"计算机导论"},"semester":{"name":"2026春季"}}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "教学班编号 456"})
	if !ok || !strings.Contains(reply, "𝙲𝚂𝟷𝟶𝟶𝟷.𝟶𝟷") || !strings.Contains(reply, "计算机导论") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleTeacherByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/teachers/12" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":12,"code":"T001","namePrimary":"张三","department":{"namePrimary":"数学科学学院"},"teacherTitle":{"namePrimary":"教授"}}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "老师编号 12"})
	if !ok || !strings.Contains(reply, "张三") || !strings.Contains(reply, "教授") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleListSemesters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/semesters" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "20" {
			t.Fatalf("limit = %q", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"data":[{"namePrimary":"2026春季","startDate":"2026-02-17T00:00:00+08:00","endDate":"2026-07-06T00:00:00+08:00"}]}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "学期列表"})
	if !ok || !strings.Contains(reply, "2026春季") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestFormatBusRoutes(t *testing.T) {
	reply := formatBusRoutes(map[string]any{
		"routes": []any{
			map[string]any{
				"nameCn":      "东高新线",
				"originCampus": map[string]any{"namePrimary": "东区"},
				"destinationCampus": map[string]any{"namePrimary": "高新区"},
				"stops": []any{
					map[string]any{"campus": map[string]any{"namePrimary": "东区"}},
					map[string]any{"campus": map[string]any{"namePrimary": "高新区"}},
				},
			},
		},
	})
	want := "东高新线（东区 → 高新区） 经停 东区、高新区"
	if !strings.Contains(reply, want) {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

func TestHandleBusRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/bus/routes":
			_, _ = w.Write([]byte(`{"routes":[{"nameCn":"东高新线","originCampus":{"namePrimary":"东区"},"destinationCampus":{"namePrimary":"高新区"},"stops":[]}],"campuses":[{"id":1,"namePrimary":"东区"},{"id":2,"namePrimary":"高新区"}]}`))
		case "/api/bus":
			_, _ = w.Write([]byte(`{"campuses":[{"id":1,"namePrimary":"东区"},{"id":2,"namePrimary":"高新区"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "校车路线 from 东区 to 高新区"})
	if !ok || !strings.Contains(reply, "东高新线") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleSectionSchedules(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections/789/schedules" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("dateFrom") != "2026-06-01" || q.Get("dateTo") != "2026-06-07" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "教学班课表 789 2026-06-01 2026-06-07", Identity: ident})
	if !ok || !strings.Contains(reply, "数据库系统") || !strings.Contains(reply, "西区 𝟹𝙰𝟸𝟶𝟺") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleSectionExams(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sections/321" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"MATH1001.01","course":{"namePrimary":"数学分析"},"exams":[{"id":1,"examDate":"2026-06-20T00:00:00+08:00","startTime":1430,"endTime":1630,"examMode":"闭卷","examRooms":[{"room":"3A101"}]}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "教学班考试 321", Identity: ident})
	if !ok || !strings.Contains(reply, "数学分析") || !strings.Contains(reply, "𝟶𝟼-𝟸𝟶") || !strings.Contains(reply, "闭卷") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleSectionHomeworks(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/homeworks" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("sectionJwId") != "654" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00"}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "教学班作业 654", Identity: ident})
	if !ok || !strings.Contains(reply, "Problem Set") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestFormatDashboard(t *testing.T) {
	data := map[string]any{
		"counts": map[string]any{
			"todaySchedules":   2,
			"pendingHomeworks": 3,
			"dueSoonHomeworks": 1,
			"upcomingExams":    4,
		},
		"dueTodos": map[string]any{
			"items": []any{map[string]any{"title": "写报告", "dueAt": "2026-06-10T18:00:00+08:00"}},
		},
		"homeworks": map[string]any{
			"items": []any{map[string]any{"title": "Problem Set 1", "submissionDueAt": "2026-06-03T12:00:00+08:00"}},
		},
		"exams": map[string]any{
			"items": []any{
				map[string]any{
					"section":  map[string]any{"course": map[string]any{"namePrimary": "数学分析"}},
					"examDate": "2026-06-20T00:00:00+08:00",
					"startTime": 900,
					"endTime":   1100,
					"examRooms": []any{map[string]any{"room": "3A101"}},
				},
			},
		},
	}
	reply := formatDashboard(data, "我的概览")
	for _, want := range []string{"我的概览", "今日课表 2", "待办：", "作业：", "考试：", "数学分析"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
}

func TestHandleDashboard(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me/overview" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"counts":{"todaySchedules":1,"pendingHomeworks":1},"dueTodos":{"items":[{"title":"写报告"}]},"homeworks":{"items":[{"title":"作业一"}]},"exams":{"items":[]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "概览", Identity: ident})
	if !ok || !strings.Contains(reply, "我的概览") || !strings.Contains(reply, "写报告") || !strings.Contains(reply, "作业一") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleUpcomingDeadlines(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me/overview" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("homeworkWindowDays") != "14" || q.Get("limit") != "50" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"counts":{"upcomingExams":1},"dueTodos":{"items":[]},"homeworks":{"items":[{"title":"作业一"}]},"exams":{"items":[]}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "近期截止 14", Identity: ident})
	if !ok || !strings.Contains(reply, "未来 14 天截止") || !strings.Contains(reply, "作业一") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleUnsubscribeSectionByJwID(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/calendar-subscriptions/current" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101,"jwId":999,"code":"CS1001.01","course":{"namePrimary":"计算机导论"}}]}}`))
		case r.URL.Path == "/api/calendar-subscriptions/batch" && r.Method == http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["action"] != "remove" {
				t.Fatalf("action = %q", body["action"])
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "退订教学班 999", Identity: ident})
	if !ok || reply != "已退订教学班。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func testAuthedHandler(t *testing.T, server *httptest.Server, ident store.Identity) Handler {
	t.Helper()
	return testAuthedHandlerWithCredential(t, server, ident, store.Credential{
		ClientID:    "client",
		AccessToken: "access",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
		Resource:    server.URL,
	})
}

func testAuthedHandlerWithRefresh(t *testing.T, server *httptest.Server, ident store.Identity) Handler {
	t.Helper()
	return testAuthedHandlerWithCredential(t, server, ident, store.Credential{
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		Resource:     server.URL,
	})
}

func testAuthedHandlerWithCredential(t *testing.T, server *httptest.Server, ident store.Identity, cred store.Credential) Handler {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SaveCredential(context.Background(), ident, cred); err != nil {
		t.Fatal(err)
	}
	return Handler{
		Life:   life.NewClient(server.URL, server.Client()),
		Auth:   &auth.Manager{Server: server.URL, HTTPClient: server.Client(), Store: s},
		Store:  s,
		Prefix: "/life",
	}
}

func handleOAuthRefreshMetadata(w http.ResponseWriter, r *http.Request, serverURL string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server/api/auth",
		"/api/auth/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration":
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, serverURL, serverURL+"/token")
		return true
	default:
		return false
	}
}

func testIdentity() store.Identity {
	return store.Identity{
		Platform:         "napcat",
		UserID:           "42",
		ConversationType: "private",
		ConversationID:   "42",
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "hello"})
	if ok || reply != "" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
