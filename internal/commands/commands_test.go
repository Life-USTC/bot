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

func TestHandleGroupOnlyAllowsBusKeywords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bus" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"routes":[
				{"id":1,"stops":[
					{"campus":{"nameCn":"东区"}},
					{"campus":{"nameCn":"北区"}},
					{"campus":{"nameCn":"西区"}}
				]}
			],
			"trips":[
				{"routeId":1,"dayType":"weekday","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]},
				{"routeId":1,"dayType":"weekend","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[
					{"campusName":"东区","time":"23:59"},
					{"campusName":"北区"},
					{"campusName":"西区","time":"23:59"}
				]}
			]
		}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client()), Prefix: "/life"}
	groupInput := Input{
		Text: "东区到西区校车还有吗",
		Identity: store.Identity{
			Platform:         "napcat",
			UserID:           "42",
			ConversationType: "group",
			ConversationID:   "100",
		},
	}
	reply, ok := handler.Handle(context.Background(), groupInput)
	if !ok {
		t.Fatal("group bus message was not handled")
	}
	if !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿  →  北区\u3000 ———  →  西区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("reply = %q", reply)
	}

	groupInput.Identity.ConversationType = " GROUP "
	reply, ok = handler.Handle(context.Background(), groupInput)
	if !ok {
		t.Fatal("padded/cased group bus message was not handled")
	}

	groupInput.Text = "/life td"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group personal command reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,summary=&#91;动画表情&#93;,file=1.png,sub_type=1,url=https://example.invalid/download?rkey=CAQSMJSxCxAi3h4QEhInHuJOdWi5QXU7]"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if ok || reply != "" {
		t.Fatalf("group image reply = %q, ok = %v", reply, ok)
	}

	groupInput.Text = "[CQ:image,file=1.png] 校车"
	reply, ok = handler.Handle(context.Background(), groupInput)
	if !ok || !strings.Contains(reply, "东区\u3000 𝟸𝟹:𝟻𝟿") {
		t.Fatalf("group image caption reply = %q, ok = %v", reply, ok)
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
		"subscription": true,
		"ping":         true,
		"status":       true,
		"semester":     true,
		"course":       true,
		"section":      true,
		"bus":          true,
		"schedule":     true,
		"nextclass":    true,
	}
	storeCommands := map[string]bool{
		"notify": true,
	}
	authCommands := map[string]bool{
		"login":        true,
		"logout":       true,
		"me":           true,
		"todo":         true,
		"homework":     true,
		"subscription": true,
		"schedule":     true,
		"nextclass":    true,
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
	}
	for _, name := range []string{"todo", "homework", "schedule", "notify", "bus"} {
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
}

func TestJoinedArgsTrimsJoinedText(t *testing.T) {
	if got := joinedArgs([]string{" 写", "报告 "}); got != "写 报告" {
		t.Fatalf("joinedArgs = %q", got)
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

func TestHandleTodayCurriculum(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.URL.Path == "/api/schedules":
			if r.URL.Query().Get("sectionId") != "101" {
				t.Fatalf("sectionId = %q", r.URL.Query().Get("sectionId"))
			}
			if !strings.HasSuffix(r.URL.Query().Get("dateFrom"), "Z") || !strings.HasSuffix(r.URL.Query().Get("dateTo"), "Z") {
				t.Fatalf("date range = %q %q", r.URL.Query().Get("dateFrom"), r.URL.Query().Get("dateTo"))
			}
			_, _ = w.Write([]byte(`{"data":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
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

func TestCurriculumUsesRefreshedTokenForSchedules(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	currentCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-authorization-server":
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, serverURL, serverURL+"/token")
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/calendar-subscriptions/current":
			currentCalls++
			if currentCalls == 1 {
				if got := r.Header.Get("Authorization"); got != "Bearer access" {
					t.Fatalf("initial current authorization = %q", got)
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("refreshed current authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/schedules":
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("schedules authorization = %q", got)
			}
			if r.URL.Query().Get("sectionId") != "101" {
				t.Fatalf("sectionId = %q", r.URL.Query().Get("sectionId"))
			}
			_, _ = w.Write([]byte(`{"data":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}}}]}`))
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
	currentOldTokenCalls := 0
	scheduleCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-authorization-server":
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, serverURL, serverURL+"/token")
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			refreshRequests++
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/calendar-subscriptions/current":
			switch r.Header.Get("Authorization") {
			case "Bearer access":
				currentOldTokenCalls++
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			case "Bearer refreshed":
				_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
			default:
				t.Fatalf("current authorization = %q", r.Header.Get("Authorization"))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/schedules":
			scheduleCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("schedules authorization = %q", got)
			}
			if scheduleCalls == 1 {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}}}]}`, today)))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}}}]}`, tomorrow)))
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
	if refreshRequests != 1 || currentOldTokenCalls != 1 || scheduleCalls != 2 {
		t.Fatalf("refreshRequests = %d, currentOldTokenCalls = %d, scheduleCalls = %d", refreshRequests, currentOldTokenCalls, scheduleCalls)
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
		case r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.URL.Path == "/api/schedules":
			scheduleCalls++
			if scheduleCalls == 1 {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`, today)))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}},"room":{"namePrimary":"GT-B112"}}]}`, tomorrow)))
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
		case r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101}]}}`))
		case r.URL.Path == "/api/schedules":
			_, _ = w.Write([]byte(`{"data":[{"startTime":"09:00","endTime":"09:45","section":{"course":{"namePrimary":"已过去"}}},{"startTime":"11:00","endTime":"11:45","section":{"course":{"namePrimary":"下一节"}}}]}`))
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
	reply, ok = handler.Handle(ctx, Input{Text: "通知 课表 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
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

func TestBulkSubscribeSectionsAddsMatchedSections(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var replacedIDs []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/calendar-subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101,"code":"CONT5103P.01"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/sections/match-codes":
			var req struct {
				Codes []string `json:"codes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if strings.Join(req.Codes, ",") != "CONT5103P.01,CONT6104P.01,BAD000.01" {
				t.Fatalf("codes = %#v", req.Codes)
			}
			_, _ = w.Write([]byte(`{
				"semester":{"nameCn":"2026年春季学期"},
				"sections":[
					{"id":101,"code":"CONT5103P.01","course":{"namePrimary":"随机过程理论"}},
					{"id":202,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"}}
				],
				"unmatchedCodes":["BAD000.01"]
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/calendar-subscriptions":
			var req struct {
				SectionIDs []int `json:"sectionIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			replacedIDs = req.SectionIDs
			_, _ = w.Write([]byte(`{"subscription":{"sections":[]}}`))
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
	if strings.Join(intStrings(replacedIDs), ",") != "101,202" {
		t.Fatalf("sectionIds = %#v", replacedIDs)
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
	var replacedIDs []int
	currentCalls := 0
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/oauth-authorization-server":
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, serverURL, serverURL+"/token")
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/calendar-subscriptions/current":
			currentCalls++
			if currentCalls == 1 {
				if got := r.Header.Get("Authorization"); got != "Bearer access" {
					t.Fatalf("initial current authorization = %q", got)
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("refreshed current authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101,"code":"CONT5103P.01"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/sections/match-codes":
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("match authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"sections":[{"id":202,"code":"CONT6104P.01","course":{"namePrimary":"组合数学"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/calendar-subscriptions":
			if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
				t.Fatalf("replace authorization = %q", got)
			}
			var req struct {
				SectionIDs []int `json:"sectionIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			replacedIDs = req.SectionIDs
			_, _ = w.Write([]byte(`{"subscription":{"sections":[]}}`))
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
	if strings.Join(intStrings(replacedIDs), ",") != "101,202" {
		t.Fatalf("sectionIds = %#v; reply = %q", replacedIDs, reply)
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

func TestBusAtReturnsNoServiceAfterLastTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bus" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"西区"}}]}],
			"trips":[{"routeId":1,"dayType":"weekday","departureTime":"09:00","departureMinutes":540,"arrivalTime":"09:15"}]
		}`))
	}))
	defer server.Close()

	handler := Handler{Life: life.NewClient(server.URL, server.Client())}
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, lifedata.ChinaLocation())
	if reply := handler.busAt(context.Background(), nil, now); reply != "今天后面没查到校车。" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNextBusItemsFiltersRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "北区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:00", "departureMinutes": float64(540), "arrivalTime": "09:15"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:20" || items[0].Route != "东区 → 北区 → 西区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsUsesShanghaiTime(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "08:30", "departureMinutes": float64(510), "arrivalTime": "08:45"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
		},
	}
	now := time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC)
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:20" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsSupportsDestinationOnlyFilter(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:20", "departureMinutes": float64(560), "arrivalTime": "09:35"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"到", "东区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "西区 → 东区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsSkipsInvalidDepartureMinutes(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureMinutes": float64(560.5), "arrivalTime": "09:35"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureTime != "09:30" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusItemsIgnoresBlankRouteIDs(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": "   ",
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "南区"}},
					map[string]any{"campus": map[string]any{"nameCn": "高新区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{
				"routeId":          "missing-route",
				"dayType":          "weekday",
				"departureTime":    "09:20",
				"departureMinutes": float64(560),
				"arrivalTime":      "09:35",
				"stopTimes": []any{
					map[string]any{"campusName": "东区", "time": "09:20"},
					map[string]any{"campusName": "西区", "time": "09:35"},
				},
			},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusItems(data, []string{"东区", "西区"}, now)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "东区 → 西区" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestNextBusByRouteReturnsOneTripPerRoute(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "09:10", "departureMinutes": float64(550), "arrivalTime": "09:25"},
			map[string]any{"routeId": float64(1), "dayType": "weekday", "departureTime": "10:10", "departureMinutes": float64(610), "arrivalTime": "10:25"},
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:30", "departureMinutes": float64(570), "arrivalTime": "09:45"},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Route != "东区 → 西区" || items[0].DepartureTime != "09:10" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].Route != "西区 → 东区" || items[1].DepartureTime != "09:30" {
		t.Fatalf("second item = %#v", items[1])
	}
}

func TestNextBusByRouteSortsByDepartureCampus(t *testing.T) {
	data := map[string]any{
		"routes": []any{
			map[string]any{
				"id": float64(1),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
				},
			},
			map[string]any{
				"id": float64(2),
				"stops": []any{
					map[string]any{"campus": map[string]any{"nameCn": "西区"}},
					map[string]any{"campus": map[string]any{"nameCn": "东区"}},
				},
			},
		},
		"trips": []any{
			map[string]any{"routeId": float64(2), "dayType": "weekday", "departureTime": "09:05", "departureMinutes": float64(545), "arrivalTime": "09:20"},
			map[string]any{
				"routeId":          float64(1),
				"dayType":          "weekday",
				"departureTime":    "09:30",
				"departureMinutes": float64(570),
				"arrivalTime":      "09:45",
				"stopTimes": []any{
					map[string]any{"campusName": "东区", "time": "09:30"},
					map[string]any{"campusName": "北区"},
					map[string]any{"campusName": "西区", "time": "09:45"},
				},
			},
		},
	}
	now := time.Date(2026, 6, 2, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	items := nextBusByRoute(data, nil, now)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].DepartureCampus != "东区" || items[1].DepartureCampus != "西区" {
		t.Fatalf("items = %#v", items)
	}
	lines := formatBusItemsByDepartureCampus(items, 8)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区\u3000 𝟶𝟿:𝟹𝟶  →  北区\u3000 ———  →  西区\u3000 𝟶𝟿:𝟺𝟻\n\n西区\u3000 𝟶𝟿:𝟶𝟻") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestFormatBusItemsNoLimitShowsAllRoutes(t *testing.T) {
	items := []busItem{
		{DepartureCampus: "东区", Stops: []busStop{{Name: "东区", Time: "09:00"}}},
		{DepartureCampus: "西区", Stops: []busStop{{Name: "西区", Time: "09:05"}}},
	}

	lines := formatBusItemsByDepartureCampus(items, 0)
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "东区") || !strings.Contains(got, "西区") {
		t.Fatalf("formatted lines = %q", got)
	}
}

func TestBusArgsFromTextAcceptsEnglishCampusAliases(t *testing.T) {
	tests := map[string][]string{
		"Any BUS from EAST campus to west campus?": {"东区", "西区"},
		"bus from gx to north":                     {"高新区", "北区"},
		"bus to west campus":                       {"到", "西区"},
		"校车到西区":                                    {"到", "西区"},
		"bus to northeast tomorrow":                {},
		"bus from northeast to north":              {"北区"},
	}
	for text, want := range tests {
		got := busArgsFromText(text)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, got, want)
		}
	}
}

func TestBusArgsFromTextAvoidsOneCharCampusInsideWords(t *testing.T) {
	tests := map[string][]string{
		"校车中午到西区": {"到", "西区"},
		"校车东到西":   {"东区", "西区"},
		"xc 东 西":  {"东区", "西区"},
	}
	for text, want := range tests {
		got := busArgsFromText(text)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%q args = %#v, want %#v", text, got, want)
		}
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
		"状态": "status",
		"zt": "status",
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
