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
	botfeedback "github.com/Life-USTC/Bot/internal/feedback"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/responses"
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

func TestHandlePublicCommandSharesCacheAcrossUsersAndAliases(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = w.Write([]byte(`{"data":[{"code":"MATH1001","namePrimary":"Calculus"}]}`))
	}))
	defer server.Close()

	stateStore, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateStore.Close() }()
	handler := Handler{
		Life:        life.NewClient(server.URL, server.Client()),
		Prefix:      "/life",
		PublicCache: NewPublicCommandCache(stateStore, "version-a", time.Minute, nil),
	}

	first, ok := handler.Handle(context.Background(), Input{
		Text: "/life course calculus",
		Identity: store.Identity{
			Platform: "napcat", UserID: "1", ConversationType: "private", ConversationID: "1",
		},
		SuppressLog: true,
	})
	if !ok {
		t.Fatal("first command was not handled")
	}
	second, ok := handler.Handle(context.Background(), Input{
		Text: "课程 calculus",
		Identity: store.Identity{
			Platform: "qqbot", UserID: "2", ConversationType: "private", ConversationID: "2",
		},
		SuppressLog: true,
	})
	if !ok {
		t.Fatal("second command was not handled")
	}
	if first != second {
		t.Fatalf("cached reply changed: first = %q, second = %q", first, second)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
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
		if r.URL.Path != "/api/catalog/teachers" {
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
		if !strings.Contains(reply, "发送「帮助 课表」可以查看「课表」命令的具体用法。") ||
			!strings.Contains(reply, "待办（td）\t查看和管理待办") {
			t.Fatalf("unexpected reply for %q: %q", text, reply)
		}
	}
}

func TestHelpReplyOnlyShowsPrimaryCommands(t *testing.T) {
	reply := Handler{}.help()
	for _, want := range []string{
		"Bot 帮助：",
		"发送「帮助 课表」可以查看「课表」命令的具体用法。",
		"常用：",
		"账户与系统：",
		"命令\t说明",
		"日程\t今日安排、综合概览与近期截止",
		"课表\t周课表、单日课表与下一节课",
		"待办（td）\t查看和管理待办",
		"作业（hw）\t查看和管理作业",
		"校车（xc）\t查询班次、路线与设置偏好",
		"设置\t管理通知等偏好",
		"AI\t管理 AI 工具调用展示",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	for _, unwanted := range []string{
		"• ",
		"├─",
		"└─",
		"课表 下周",
		"待办 完成 1",
		"作业 列表 学期ID",
		"校车 东区 西区",
		"教学班 搜索 老师代码",
		"教学资源：",
		"课程\t搜索或查看课程",
		"进阶：",
		"帮助 AI",
	} {
		if strings.Contains(reply, unwanted) {
			t.Fatalf("overview contains detailed usage %q: %q", unwanted, reply)
		}
	}
	if !strings.Contains(reply, "\n") || strings.ContainsAny(reply, "\r\x1b") {
		t.Fatalf("reply separators = %q", reply)
	}
}

func TestAdvancedAIHelpShowsToolTraceControls(t *testing.T) {
	reply, ok := (Handler{}).Handle(context.Background(), Input{Text: "帮助 AI", Identity: testIdentity()})
	if !ok {
		t.Fatal("advanced AI help was not handled")
	}
	for _, want := range []string{
		"AI 帮助：",
		"AI 工具\t查看当前工具调用提示设置",
		"AI 工具 开\t回答时显示 LLM 工具调用提示",
		"AI 工具 关\t回答时隐藏 LLM 工具调用提示",
		"设置 工具调用\t等同于「AI 工具」",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	for _, unwanted := range []string{
		"进阶 AI 帮助：",
		"设置\t查看通知",
		"设置 通知",
	} {
		if strings.Contains(reply, unwanted) {
			t.Fatalf("AI help unexpectedly contains %q: %q", unwanted, reply)
		}
	}
}

func TestSettingsAndAIHelpRenderAsImages(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	cases := []struct {
		text  string
		title string
		want  []string
		avoid []string
	}{
		{
			text:  "设置",
			title: "设置 帮助",
			want:  []string{"设置 通知", "开启课前提醒"},
			avoid: []string{"AI 工具", "工具调用"},
		},
		{
			text:  "设置 帮助",
			title: "设置 帮助",
			want:  []string{"设置 通知 作业 开"},
			avoid: []string{"AI 工具"},
		},
		{
			text:  "帮助 设置",
			title: "设置 帮助",
			want:  []string{"设置 通知"},
			avoid: []string{"AI 工具"},
		},
		{
			text:  "帮助 AI",
			title: "AI 帮助",
			want:  []string{"AI 工具 开", "设置 工具调用"},
			avoid: []string{"设置 通知"},
		},
		{
			text:  "AI 帮助",
			title: "AI 帮助",
			want:  []string{"AI 工具"},
			avoid: []string{"设置 通知"},
		},
		{
			text:  "AI 工具 帮助",
			title: "AI 帮助",
			want:  []string{"AI 工具 关"},
			avoid: []string{"设置 通知"},
		},
	}
	for _, tc := range cases {
		resp, ok := handler.HandleResponse(context.Background(), Input{Text: tc.text, Identity: testIdentity()})
		if !ok {
			t.Fatalf("%q not handled", tc.text)
		}
		if resp.Image == nil {
			t.Fatalf("%q expected help image, got text only: %q", tc.text, resp.Text)
		}
		if resp.Image.Kind != "help" {
			t.Fatalf("%q image kind = %q, want help", tc.text, resp.Image.Kind)
		}
		if resp.Image.Title != tc.title {
			t.Fatalf("%q image title = %q, want %q (rich=%q)", tc.text, resp.Image.Title, tc.title, resp.Image.RichText)
		}
		body := resp.Text + "\n" + resp.Image.RichText
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%q missing %q in text=%q rich=%q", tc.text, want, resp.Text, resp.Image.RichText)
			}
		}
		for _, avoid := range tc.avoid {
			if strings.Contains(body, avoid) {
				t.Fatalf("%q unexpectedly contains %q in text=%q rich=%q", tc.text, avoid, resp.Text, resp.Image.RichText)
			}
		}
		assertResponseImageRenders(t, resp.Image)
	}
}

func TestHelpTopicShowsCompleteCommandDetails(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	for _, text := range []string{"帮助 课表", "/help 课表", "/life help 课表", "课表 帮助", "帮助 kb"} {
		reply, ok := handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !ok {
			t.Fatalf("%q was not handled", text)
		}
		for _, want := range []string{
			"课表 帮助：",
			"命令\t说明",
			"课表 下周\t查看下周课表",
			"课表 第3周\t查看指定教学周",
			"课表 单日 今天\t只查看今天的课表",
			"发送“帮助”返回命令总览。",
		} {
			if !strings.Contains(reply, want) {
				t.Fatalf("%q reply missing %q: %q", text, want, reply)
			}
		}
		if strings.Contains(reply, "待办 完成 1") {
			t.Fatalf("%q reply contains another command's details: %q", text, reply)
		}
	}

	reply, ok := handler.Handle(context.Background(), Input{Text: "帮助 不存在", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "没有找到一级命令“不存在”") {
		t.Fatalf("unknown topic reply = %q, ok = %v", reply, ok)
	}
}

func TestShortcutAndLegacyHelpResolveToCanonicalTopics(t *testing.T) {
	handler := Handler{Prefix: "/life", EnableImageResponses: true}
	response, ok := handler.HandleResponse(context.Background(), Input{Text: "帮助 快捷入口", Identity: testIdentity()})
	if !ok || response.Image == nil {
		t.Fatalf("shortcut help response = %#v, ok = %v", response, ok)
	}
	for _, want := range []string{
		"快捷入口 帮助：",
		"今日课表（单日课表）\t相当于“课表 单日 今天”",
		"登录\t相当于“账户 登录”",
		"下一节课\t相当于“课表 下一节”",
	} {
		if !strings.Contains(response.Text, want) {
			t.Fatalf("shortcut help missing %q: %q", want, response.Text)
		}
	}
	if response.Image.Title != "快捷入口 帮助" {
		t.Fatalf("shortcut help image title = %q", response.Image.Title)
	}
	assertResponseImageRenders(t, response.Image)

	for _, text := range []string{"帮助 课程搜索", "帮助 course_search", "课程 帮助"} {
		reply, handled := handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !handled || !strings.Contains(reply, "课程 帮助：") ||
			!strings.Contains(reply, "课程 搜索 培养层次ID <ID>") ||
			strings.Contains(reply, "education_level_id") {
			t.Fatalf("%q canonical help reply = %q, handled = %v", text, reply, handled)
		}
	}
}

func TestHelpOverviewAndDetailsCoverEveryCommandSpec(t *testing.T) {
	overviewCount := map[string]int{}
	for _, section := range helpOverviewSections() {
		if strings.TrimSpace(section.title) == "" || len(section.rows) == 0 {
			t.Fatalf("invalid overview section: %#v", section)
		}
		for _, row := range section.rows {
			if strings.TrimSpace(row.command) == "" || strings.TrimSpace(row.description) == "" {
				t.Fatalf("invalid overview row in %q: %#v", section.title, row)
			}
			if row.topic == "" {
				t.Fatalf("overview row has no topic: %#v", row)
			}
			overviewCount[row.topic]++
		}
	}

	detailCovered := map[string]bool{}
	topicDetails := map[string]bool{}
	for _, section := range helpDetailSections() {
		if strings.TrimSpace(section.title) == "" || len(section.rows) == 0 {
			t.Fatalf("invalid detail section: %#v", section)
		}
		for _, row := range section.rows {
			if strings.TrimSpace(row.command) == "" || strings.TrimSpace(row.description) == "" {
				t.Fatalf("invalid detail row in %q: %#v", section.title, row)
			}
			if strings.ContainsAny(row.command, "\t\r\n├└•") {
				t.Fatalf("help command is not a flat table cell: %q", row.command)
			}
			if row.commandName == "" {
				t.Fatalf("detail row has no command name: %#v", row)
			}
			detailCovered[row.commandName] = true
			topicDetails[row.topic] = true
			if strings.Contains(row.command, "<") {
				continue
			}
			example := row.command
			if start := strings.Index(example, "（"); start >= 0 && strings.HasSuffix(example, "）") {
				example = strings.TrimSpace(example[:start])
			}
			cmd, ok := (Handler{}).parse(example)
			if !ok || cmd.Name != row.commandName {
				t.Errorf("help example %q parsed as %#v, want %q", row.command, cmd, row.commandName)
			}
		}
	}
	visibleTopics := map[string]bool{
		"agenda": true, "schedule": true, "exam": true, "todo": true, "homework": true,
		"bus": true, "account": true, "settings": true, "system": true, "feedback": true, "advanced": true,
	}
	if len(overviewCount) != len(visibleTopics) {
		t.Errorf("overview has %d topics, want %d", len(overviewCount), len(visibleTopics))
	}
	for topic, count := range overviewCount {
		if !visibleTopics[topic] {
			t.Errorf("hidden topic %q appears in overview", topic)
		}
		if count != 1 {
			t.Errorf("topic %q appears %d times in overview, want once", topic, count)
		}
		if !topicDetails[topic] {
			t.Errorf("topic %q is missing detail help", topic)
		}
	}
	if !topicDetails["shortcuts"] {
		t.Error("shortcut detail help is missing")
	}
	for topic := range helpTopicTitles {
		if topic != "shortcuts" && !topicDetails[topic] {
			t.Errorf("topic %q is missing detail help", topic)
		}
	}
	for _, spec := range CommandSpecs() {
		topic, ok := internalHelpTopics[spec.Name]
		if !ok {
			t.Errorf("command %q is not assigned to a canonical topic", spec.Name)
		} else if _, ok := helpTopicTitles[topic]; !ok {
			t.Errorf("command %q maps to untitled topic %q", spec.Name, topic)
		}
		if !detailCovered[spec.Name] {
			t.Errorf("command %q is missing detail help", spec.Name)
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
	handler := Handler{Prefix: "/life"}
	lifeCommands := map[string]bool{
		"account":                      true,
		"todo":                         true,
		"homework":                     true,
		"calendar":                     true,
		"subscription":                 true,
		"ping":                         true,
		"status":                       true,
		"semester":                     true,
		"course":                       true,
		"section":                      true,
		"teacher":                      true,
		"bus":                          true,
		"schedule":                     true,
		"nextclass":                    true,
		"exam":                         true,
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
		"overview":                     true,
		"upcoming_deadlines":           true,
	}
	storeCommands := map[string]bool{
		"notify": true,
		"agent":  true,
	}
	authCommands := map[string]bool{
		"login":                        true,
		"logout":                       true,
		"account":                      true,
		"todo":                         true,
		"homework":                     true,
		"calendar":                     true,
		"subscription":                 true,
		"schedule":                     true,
		"nextclass":                    true,
		"exam":                         true,
		"unsubscribe_section_by_jw_id": true,
		"my_subscribed_sections":       true,
		"section_schedules":            true,
		"section_exams":                true,
		"section_homeworks":            true,
		"overview":                     true,
		"upcoming_deadlines":           true,
	}
	helpCommands := map[string]bool{
		"login":        true,
		"todo":         true,
		"homework":     true,
		"subscription": true,
		"notify":       true,
		"settings":     true,
		"agent":        true,
		"bus":          true,
		"schedule":     true,
		"feedback":     true,
	}
	publicCacheCommands := map[string]bool{
		"semester":         true,
		"course":           true,
		"section":          true,
		"teacher":          true,
		"list_semesters":   true,
		"course_search":    true,
		"section_search":   true,
		"teacher_search":   true,
		"course_by_jw_id":  true,
		"section_by_jw_id": true,
		"teacher_by_id":    true,
		"bus_routes":       true,
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
		if spec.PublicCache != publicCacheCommands[spec.Name] {
			t.Fatalf("command %q PublicCache = %v", spec.Name, spec.PublicCache)
		}
		seen[spec.Name] = true
		for _, alias := range spec.Aliases {
			key := normToken(alias)
			if owner, ok := aliases[key]; ok {
				t.Fatalf("alias %q for %q already belongs to %q", alias, spec.Name, owner)
			}
			aliases[key] = spec.Name
			name, _ := normalizeCommand(alias, nil)
			if key == "日程" || key == "账户" || key == "课程" || key == "教学班" || key == "班级" || key == "老师" || key == "教师" {
				if name != "help" {
					t.Fatalf("canonical root %q normalized to %q, want help", alias, name)
				}
				continue
			}
			if name != spec.Name {
				t.Fatalf("alias %q normalized to %q, want %q", alias, name, spec.Name)
			}
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
	for i, spec := range specs {
		if len(spec.Aliases) > 0 && aliasIndex == -1 {
			aliasIndex = i
		}
	}
	if aliasIndex == -1 {
		t.Fatal("missing command spec aliases")
	}

	aliasName := specs[aliasIndex].Name
	specs[aliasIndex].Aliases[0] = "mutated"

	handler := Handler{Prefix: "/life"}
	fresh := CommandSpecs()
	parsed, ok := handler.parse(fresh[aliasIndex].Aliases[0])
	if !ok || parsed.Name != aliasName {
		t.Fatalf("alias command parsed as %q, ok = %v, want %q", parsed.Name, ok, aliasName)
	}
}

func TestHandleLifeCommandWithoutClientDoesNotPanic(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "课程 数学分析", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "Life @ USTC API unavailable") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "订阅 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "订阅 添加") {
		t.Fatalf("help reply = %q, ok = %v", reply, ok)
	}

	for _, text := range []string{"课程 help", "教学班 help", "状态 help"} {
		reply, ok = handler.Handle(context.Background(), Input{Text: text, Identity: testIdentity()})
		if !ok || !strings.Contains(reply, " 帮助：") {
			t.Fatalf("%q help reply = %q, ok = %v", text, reply, ok)
		}
	}
	reply, ok = handler.Handle(context.Background(), Input{Text: "校车 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "校车 偏好") {
		t.Fatalf("bus help reply = %q, ok = %v", reply, ok)
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
	reply, ok := handler.Handle(context.Background(), Input{Text: "我的", Identity: testIdentity()})
	if !ok || reply != "登录未配置。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "登录 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "账户 帮助：") {
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
	reply, ok := handler.Handle(context.Background(), Input{Text: "我的", Identity: testIdentity()})
	if !ok || reply != "登录未配置。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}

	reply, ok = handler.Handle(context.Background(), Input{Text: "登录 help", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "账户 帮助：") {
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
		if !strings.Contains(reply, "待办 帮助：") ||
			!strings.Contains(reply, "待办 添加 写报告") ||
			!strings.Contains(reply, "待办 完成 1") {
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
		if r.URL.Path != "/api/workspace/todos" || r.Method != http.MethodPost {
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
		if r.URL.Path != "/api/workspace/todos" || r.Method != http.MethodPost {
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
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspace/todos":
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
		if !strings.Contains(reply, "账户 帮助：") || !strings.Contains(reply, "账户 登录状态") {
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
		if !strings.Contains(reply, "课表 帮助：") || strings.Contains(reply, "需要先登录") {
			t.Fatalf("%q reply = %q", text, reply)
		}
	}
}

func TestHandleResponseKeepsHandleTextCompatibility(t *testing.T) {
	ctx := context.Background()
	handler := Handler{Prefix: "/life", EnableImageResponses: true}

	response, ok := handler.HandleResponse(ctx, Input{Text: "/help", Identity: testIdentity()})
	if !ok {
		t.Fatal("command was not handled")
	}
	text, ok := handler.Handle(ctx, Input{Text: "/help", Identity: testIdentity()})
	if !ok {
		t.Fatal("Handle did not handle the command")
	}
	if response.Text != text {
		t.Fatalf("HandleResponse text = %q, Handle text = %q", response.Text, text)
	}
	if response.Image == nil || response.Image.Kind != "help" {
		t.Fatalf("help response image = %#v", response.Image)
	}
	for _, want := range []string{
		"发送「帮助 课表」可以查看「课表」命令的具体用法。",
		"## 常用",
		"| 命令 | 说明 |",
		"| 日程 | 今日安排、综合概览与近期截止 |",
		"| 课表 | 周课表、单日课表与下一节课 |",
		"| 待办（td） | 查看和管理待办 |",
		"| 校车（xc） | 查询班次、路线与设置偏好 |",
		"| 设置 | 管理通知等偏好 |",
		"| AI | 管理 AI 工具调用展示 |",
	} {
		if !strings.Contains(response.Image.RichText, want) {
			t.Fatalf("help rich text missing %q: %q", want, response.Image.RichText)
		}
	}
	if got := strings.Count(response.Image.RichText, "| 命令 | 说明 |"); got != len(helpOverviewSections()) {
		t.Fatalf("help table count = %d, want %d", got, len(helpOverviewSections()))
	}
	for _, unwanted := range []string{"├", "└", "•", "课表 下周", "待办 完成 1", "## 教学资源", "| 教学班 |", "## 进阶", "帮助 AI"} {
		if strings.Contains(response.Image.RichText, unwanted) {
			t.Fatalf("help overview still contains %q: %q", unwanted, response.Image.RichText)
		}
	}
	assertResponseImageRenders(t, response.Image)
}

func TestHandleImageDirectiveAllowsRenderableReadOnlyCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"routes":[{"id":1,"stops":[{"campus":{"nameCn":"东区"}},{"campus":{"nameCn":"西区"}}]}],
			"trips":[
				{"routeId":1,"dayType":"weekday","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[{"campusName":"东区","time":"23:59"},{"campusName":"西区","time":"23:59"}]},
				{"routeId":1,"dayType":"weekend","departureTime":"23:59","departureMinutes":1439,"arrivalTime":"23:59","stopTimes":[{"campusName":"东区","time":"23:59"},{"campusName":"西区","time":"23:59"}]}
			]
		}`))
	}))
	defer server.Close()
	handler := Handler{
		Life:                 life.NewClient(server.URL, server.Client()),
		EnableImageResponses: true,
	}
	response, ok := handler.HandleImageDirective(context.Background(), Input{
		Text:     "校车 东区 西区",
		Identity: testIdentity(),
	})
	if !ok || response.Image == nil || response.Kind != "bus" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
}

func TestHandleImageDirectiveRejectsMutatingCommand(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	response, ok := handler.HandleImageDirective(context.Background(), Input{
		Text:     "待办 添加 写报告",
		Identity: testIdentity(),
	})
	if ok || response.Text != "" || response.Image != nil || response.Kind != "" || len(response.Parts) != 0 {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
}

func TestDocumentedImageDirectiveFormsAreAllowed(t *testing.T) {
	handler := Handler{}
	examples := []string{
		"校车",
		"校车 查询 全部",
		"校车 查询 我的路线",
		"校车 查询 东区 西区",
		"校车 查询 东区 西区 之后 14:00 已发车",
		"课表",
		"课表 本周",
		"课表 下周",
		"课表 第3周",
		"课表 05.06",
		"课表 7.20周",
		"课表 2026-05-06",
		"课表 2026/5/6",
		"课表 2026.05.06",
		"课表 2026年5月6日",
		"课表 单日 今天",
		"课表 单日 明天",
		"今日课表",
		"明日课表",
		"今天课表",
		"明天课表",
		"课表 下一节",
		"下一节课",
		"待办",
		"待办 列表 全部",
		"待办 列表 未完成",
		"待办 列表 已完成",
		"待办 列表 未完成 优先级 高 截止前 2026-06-10 截止后 2026-06-01 第2页",
		"作业",
		"作业 列表 未完成",
		"作业 列表 全部 学期ID 12 学期JWID 123 第2页",
		"考试",
		"考试 第2页",
		"概览",
		"日程 概览",
		"近期截止",
		"近期截止 14",
		"日程 截止",
		"日程 截止 14",
		"教学班 作业 654",
		"教学班 作业 654 第2页",
		"教学班 考试 321",
		"教学班 考试 321 第2页",
		"教学班作业 654",
		"教学班考试 321 第2页",
	}
	for _, example := range examples {
		cmd, ok := handler.parse(example)
		if !ok {
			t.Errorf("%q was not parsed", example)
			continue
		}
		if !imageDirectiveCommandAllowed(cmd) {
			t.Errorf("%q parsed as %#v but is not image-directive safe", example, cmd)
		}
	}
}

func TestSubcommandHelpUsesImage(t *testing.T) {
	handler := Handler{Prefix: "/life", EnableImageResponses: true}
	response, ok := handler.HandleResponse(context.Background(), Input{Text: "课表 help", Identity: testIdentity()})
	if !ok || response.Image == nil || response.Image.Kind != "help" {
		t.Fatalf("response = %#v, ok = %v", response, ok)
	}
	if response.Image.Title != "课表 帮助" ||
		!strings.Contains(response.Image.RichText, "| 课表 第3周 | 查看指定教学周 |") ||
		!strings.Contains(response.Image.AltText, "课表 第3周") {
		t.Fatalf("image = %#v", response.Image)
	}
	assertResponseImageRenders(t, response.Image)
}

func TestHandleResponseAddsImageForEnabledSchedule(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/api/workspace/schedules" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"schedules":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	handler.EnableImageResponses = true
	response, ok := handler.HandleResponse(ctx, Input{Text: "今天课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "数据库系统") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image == nil {
		t.Fatal("image = nil, want schedule image")
	}
	if response.Image.Kind != "schedule" || !strings.HasPrefix(response.Image.Title, "今天 ") || !strings.HasSuffix(response.Image.Title, " 课表") {
		t.Fatalf("image = %#v", response.Image)
	}
	if !strings.Contains(response.Image.AltText, "数据库系统") {
		t.Fatalf("alt text = %q", response.Image.AltText)
	}
}

func TestImageResponseUsesPlainFontText(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := "𝟶𝟽-𝟶𝟾 课表：\n𝟷. \t𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷 𝟶𝟿:𝟻𝟶"

	img := handler.imageResponseFor(parsedCommand{Name: "schedule"}, text)
	if img == nil {
		t.Fatal("image = nil")
	}
	if img.Title != "07-08 课表" {
		t.Fatalf("title = %q", img.Title)
	}
	if !strings.Contains(img.AltText, "1.   MATH1001 09:50") {
		t.Fatalf("alt text = %q", img.AltText)
	}
	if strings.Contains(img.AltText, "\t") {
		t.Fatalf("alt text still uses tabs: %q", img.AltText)
	}
	if strings.Contains(img.AltText, "𝟷") || strings.Contains(img.AltText, "𝙼") {
		t.Fatalf("alt text still uses mathematical monospace characters: %q", img.AltText)
	}
}

func TestDailyScheduleImageUsesSingleDayGrid(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := strings.Join([]string{
		"今天 07-17 课表：",
		"西区 3A204\t09:50-11:25\t数据库系统",
		"高新区 GT-B112\t14:00-15:35\tComputer Networks",
	}, "\n")

	img := handler.imageResponseFor(parsedCommand{Name: "schedule", Args: []string{"today"}}, text)
	if img == nil || img.Grid == nil {
		t.Fatalf("image = %#v", img)
	}
	if len(img.Grid.Days) != 1 || img.Grid.Days[0].Date != "07-17" {
		t.Fatalf("days = %#v", img.Grid.Days)
	}
	if len(img.Grid.Items) != 2 {
		t.Fatalf("items = %#v", img.Grid.Items)
	}
	if first := img.Grid.Items[0]; first.Day != 0 || first.StartPeriod != 3 || first.EndPeriod != 4 ||
		first.Course != "数据库系统" || first.Location != "西区 · 3A204" {
		t.Fatalf("first item = %#v", first)
	}
	assertResponseImageRenders(t, img)
}

func TestWeeklyScheduleImageUsesSundayToSaturdayGrid(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := strings.Join([]string{
		"07-12 至 07-18 课表：",
		"周日 07-12：",
		"高新区 GT-B112\t09:50-11:25\t数据库系统",
		"",
		"周一 07-13：",
		"西区 3A204\t14:00-15:35\tComputer Networks",
		"",
		"周二 07-14：",
		"没有课。",
		"",
		"周三 07-15：",
		"没有课。",
		"",
		"周四 07-16：",
		"没有课。",
		"",
		"周五 07-17：",
		"没有课。",
		"",
		"周六 07-18：",
		"先研院 1A201\t19:30-21:05\tMachine Learning",
	}, "\n")

	img := handler.imageResponseFor(parsedCommand{Name: "schedule"}, text)
	if img == nil || img.Grid == nil {
		t.Fatalf("image = %#v", img)
	}
	if len(img.Grid.Days) != 7 || img.Grid.Days[0].Label != "周日" || img.Grid.Days[6].Label != "周六" {
		t.Fatalf("days = %#v", img.Grid.Days)
	}
	if len(img.Grid.Periods) != 13 {
		t.Fatalf("periods = %#v", img.Grid.Periods)
	}
	if len(img.Grid.Items) != 3 {
		t.Fatalf("items = %#v", img.Grid.Items)
	}
	first := img.Grid.Items[0]
	if first.Day != 0 || first.StartPeriod != 3 || first.EndPeriod != 4 || first.Course != "数据库系统" || first.Location != "高新区 · GT-B112" {
		t.Fatalf("first item = %#v", first)
	}
	assertResponseImageRenders(t, img)
}

func TestSchedulePeriodLabelUsesUSTCLessonTimes(t *testing.T) {
	tests := map[string]string{
		"07:50-08:35": "第 1 小节",
		"07:50-09:25": "第 1–2 小节",
		"09:50-11:25": "第 3–4 小节",
		"14:00-15:35": "第 6–7 小节",
		"19:30-21:05": "第 11–12 小节",
		"12:30-13:00": "—",
	}
	for timeRange, want := range tests {
		if got := schedulePeriodLabel(timeRange); got != want {
			t.Fatalf("schedulePeriodLabel(%q) = %q, want %q", timeRange, got, want)
		}
	}
}

func TestRichTextImageMarksSectionHeadings(t *testing.T) {
	img := richTextImage("overview", "07-15 安排", strings.Join([]string{
		"07-15 安排：",
		"今日课表 (1)：",
		"1.\t西区 3A204\t09:50-11:25\t数据库系统",
		"",
		"待办 (1)：",
		"1.\t截止 07-15 18:00\t写报告",
		"",
		"近期作业 (1)：",
		"1.\t截止 07-16 23:59\t数据库系统\tProblem Set 4",
		"",
		"考试 (1)：",
		"1.\t07-20\t14:30-16:30\t数学分析\tMATH1001.01\t闭卷\t3A101",
	}, "\n"))
	if img == nil {
		t.Fatal("image = nil")
	}
	for _, want := range []string{
		"## 今日课表 (1)\n| 节次 | 时间 | 安排 | 备注 |",
		"| 第 3–4 小节 | 09:50-11:25 | 数据库系统 | 西区 · 3A204 |",
		"## 待办 (1)\n| # | 截止 | 待办 |",
		"| 1 | 07-15 18:00 | 写报告 |",
		"## 近期作业 (1)\n| # | 截止 | 课程 | 作业 |",
		"| 1 | 07-16 23:59 | 数据库系统 | Problem Set 4 |",
		"## 考试 (1)\n| # | 日期 | 时间 | 课程 | 教学班 | 方式 | 教室 |",
		"| 1 | 07-20 | 14:30-16:30 | 数学分析 | MATH1001.01 | 闭卷 | 3A101 |",
	} {
		if !strings.Contains(img.RichText, want) {
			t.Fatalf("rich text missing %q: %q", want, img.RichText)
		}
	}
	assertResponseImageRenders(t, img)
}

func TestTodoImageUsesTable(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	img := handler.imageResponseFor(parsedCommand{Name: "todo"}, "待办：\n1.\t截止 07-16 18:00\t写报告\n2.\t\t买咖啡")
	if img == nil || !strings.Contains(img.RichText, "| # | 截止 | 待办 |") ||
		!strings.Contains(img.RichText, "| 1 | 07-16 18:00 | 写报告 |") ||
		!strings.Contains(img.RichText, "| 2 |  | 买咖啡 |") {
		t.Fatalf("rich text = %q", img.RichText)
	}
}

func TestTodoImageKeepsOverflowNoticeInTable(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	img := handler.imageResponseFor(parsedCommand{Name: "todo"}, "待办：\n1.\t\t写报告\n...and 4 more")
	if img == nil || !strings.Contains(img.RichText, "|  |  | ...and 4 more |") {
		t.Fatalf("rich text = %q", img.RichText)
	}
}

func TestTodoImageKeepsPaginationNoticeInTable(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	img := handler.imageResponseFor(
		parsedCommand{Name: "todo", Args: []string{"list", "第2页"}},
		"待办：\n31.\t\t写报告\n第 2/3 页 · 上一页：待办 列表 第1页 · 下一页：待办 列表 第3页",
	)
	if img == nil || !strings.Contains(img.RichText, "| 31 |  | 写报告 |") ||
		!strings.Contains(img.RichText, "|  |  | 第 2/3 页") {
		t.Fatalf("rich text = %q", img.RichText)
	}
	assertResponseImageRenders(t, img)
}

func TestHomeworkImageUsesGroupedTablesAndSkipsNonListReplies(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := strings.Join([]string{
		"作业：",
		"已逾期：",
		"1.\t截止 07-15 23:59\t数据库系统\tProblem Set 1",
		"",
		"近期：",
		"2.\t截止 07-18 23:59\t数学分析\t习题课作业",
	}, "\n")

	img := handler.imageResponseFor(parsedCommand{Name: "homework"}, text)
	if img == nil || img.Kind != "homework" || img.Title != "作业" {
		t.Fatalf("image = %#v", img)
	}
	for _, want := range []string{
		"## 已逾期\n| # | 截止 | 课程 | 作业 |",
		"| 1 | 07-15 23:59 | 数据库系统 | Problem Set 1 |",
		"## 近期\n| # | 截止 | 课程 | 作业 |",
	} {
		if !strings.Contains(img.RichText, want) {
			t.Fatalf("rich text missing %q: %q", want, img.RichText)
		}
	}

	for _, cmd := range []parsedCommand{
		{Name: "homework", Args: []string{"done", "1"}},
		{Name: "homework", Args: []string{"undo", "1"}},
	} {
		if got := handler.imageResponseFor(cmd, text); got != nil {
			t.Fatalf("non-list image = %#v, want nil", got)
		}
	}
	helpImage := handler.imageResponseFor(parsedCommand{Name: "homework", Args: []string{"help"}}, "作业用法：\n作业：查看作业")
	if helpImage == nil || helpImage.Kind != "help" {
		t.Fatalf("help image = %#v", helpImage)
	}
	for _, reply := range []string{"没有未完成作业。", "作业查不到：server exploded"} {
		if got := handler.imageResponseFor(parsedCommand{Name: "homework"}, reply); got != nil {
			t.Fatalf("empty/error image = %#v, want nil", got)
		}
	}
	sectionImage := handler.imageResponseFor(parsedCommand{Name: "section_homeworks"}, "作业：\n1.\t截止 07-18 23:59\t\tProblem Set 1")
	if sectionImage == nil || !strings.Contains(sectionImage.RichText, "| 1 | 07-18 23:59 |  | Problem Set 1 |") {
		t.Fatalf("section homework image = %#v", sectionImage)
	}
	for _, reply := range []string{"需要提供教学班 JW ID。", "该教学班没有作业。"} {
		if got := handler.imageResponseFor(parsedCommand{Name: "section_homeworks"}, reply); got != nil {
			t.Fatalf("section empty/error image = %#v, want nil", got)
		}
	}
}

func TestExamImageUsesTableForSubscriptionAndSectionQueries(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := "考试：\n1.\t07-20\t14:30-16:30\t数学分析\tMATH1001.01\t闭卷\t3A101"

	for _, name := range []string{"exam", "section_exams"} {
		img := handler.imageResponseFor(parsedCommand{Name: name}, text)
		if img == nil || img.Kind != "exam" || img.Title != "考试" {
			t.Fatalf("%s image = %#v", name, img)
		}
		if !strings.Contains(img.RichText, "| # | 日期 | 时间 | 课程 | 教学班 | 方式 | 教室 |") ||
			!strings.Contains(img.RichText, "| 1 | 07-20 | 14:30-16:30 | 数学分析 | MATH1001.01 | 闭卷 | 3A101 |") {
			t.Fatalf("%s rich text = %q", name, img.RichText)
		}
		assertResponseImageRenders(t, img)
	}

	for _, reply := range []string{"没有订阅课程考试。", "该教学班没有考试。", "考试查不到：server exploded"} {
		if got := handler.imageResponseFor(parsedCommand{Name: "exam"}, reply); got != nil {
			t.Fatalf("empty/error image = %#v, want nil", got)
		}
	}
	for _, reply := range []string{"需要提供教学班 JW ID。", "教学班查不到：server exploded"} {
		if got := handler.imageResponseFor(parsedCommand{Name: "section_exams"}, reply); got != nil {
			t.Fatalf("section error image = %#v, want nil", got)
		}
	}
}

func TestNextClassImageUsesScheduleTableAndSkipsEmptyReply(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := "下一节课：\n西区 3A204\t09:50-11:25\t数据库系统"

	img := handler.imageResponseFor(parsedCommand{Name: "nextclass"}, text)
	if img == nil || img.Kind != "nextclass" || img.Title != "下一节课" {
		t.Fatalf("image = %#v", img)
	}
	if !strings.Contains(img.RichText, "| 节次 | 时间 | 安排 | 备注 |") ||
		!strings.Contains(img.RichText, "| 第 3–4 小节 | 09:50-11:25 | 数据库系统 | 西区 · 3A204 |") {
		t.Fatalf("rich text = %q", img.RichText)
	}
	assertResponseImageRenders(t, img)

	for _, reply := range []string{"接下来一周没查到课。", "下一节课查不到：server exploded"} {
		if got := handler.imageResponseFor(parsedCommand{Name: "nextclass"}, reply); got != nil {
			t.Fatalf("empty/error image = %#v, want nil", got)
		}
	}
}

func assertResponseImageRenders(t *testing.T, image *responses.Image) {
	t.Helper()
	data, width, height, err := (responses.Renderer{}).RenderPNG(image)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || width <= 0 || height <= 0 {
		t.Fatalf("rendered image len=%d size=%dx%d", len(data), width, height)
	}
}

func TestImageResponseAddsBusImageAndSkipsBusNonResultReplies(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := strings.Join([]string{
		"东区   西区",
		"09:10  09:25",
		"",
		"西区   东区",
		"09:20  09:35",
	}, "\n")

	img := handler.imageResponseFor(parsedCommand{Name: "bus", Args: []string{"东区", "西区"}}, text)
	if img == nil {
		t.Fatal("image = nil, want bus image")
	}
	if img.Kind != "bus" || img.Title != "校车" {
		t.Fatalf("image = %#v", img)
	}
	if !strings.HasPrefix(img.RichText, "# 校车\n\n") || strings.Contains(img.RichText, "Life @ USTC") {
		t.Fatalf("rich text = %q", img.RichText)
	}
	if !strings.Contains(img.RichText, "| **东区** | **西区** |") {
		t.Fatalf("queried endpoints are not emphasized: %q", img.RichText)
	}
	if !strings.Contains(img.AltText, "09:10") || !strings.Contains(img.AltText, "西区") {
		t.Fatalf("alt text = %q", img.AltText)
	}

	for name, tc := range map[string]struct {
		cmd  parsedCommand
		text string
	}{
		"preference": {cmd: parsedCommand{Name: "bus", Args: []string{"偏好"}}, text: "校车偏好：\n路线：东区 → 西区"},
		"no service": {cmd: parsedCommand{Name: "bus"}, text: "今天后面没查到校车。"},
		"error":      {cmd: parsedCommand{Name: "bus"}, text: "校车查不到：server exploded"},
	} {
		if got := handler.imageResponseFor(tc.cmd, tc.text); got != nil {
			t.Fatalf("%s image = %#v, want nil", name, got)
		}
	}
	helpImage := handler.imageResponseFor(parsedCommand{Name: "bus", Args: []string{"help"}}, busHelp())
	if helpImage == nil || helpImage.Kind != "help" {
		t.Fatalf("help image = %#v", helpImage)
	}
}

func TestBusImagePreservesEmptyIntermediateStopCells(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"06:50\t07:00\t　　\t07:40",
	}, "\n")

	img := handler.imageResponseFor(parsedCommand{Name: "bus"}, text)
	if img == nil || !strings.Contains(img.RichText, "| 06:50 | 07:00 |  | 07:40 |") {
		t.Fatalf("rich text = %q", img.RichText)
	}
	if strings.Contains(img.RichText, "**") {
		t.Fatalf("all-routes headers should not be emphasized: %q", img.RichText)
	}
}

func TestBusImageEmphasizesOnlyBothQueriedEndpoints(t *testing.T) {
	handler := Handler{EnableImageResponses: true}
	text := "东区\t西区\t先研院\t高新区\n06:50\t07:00\t07:20\t07:40"

	queried := handler.imageResponseFor(parsedCommand{Name: "bus", Args: []string{"东区", "西区"}}, text)
	if queried == nil || !strings.Contains(queried.RichText, "| **东区** | **西区** | 先研院 | 高新区 |") {
		t.Fatalf("queried rich text = %q", queried.RichText)
	}

	oneEndpoint := handler.imageResponseFor(parsedCommand{Name: "bus", Args: []string{"东区"}}, text)
	if oneEndpoint == nil || strings.Contains(oneEndpoint.RichText, "**") {
		t.Fatalf("one-endpoint rich text = %q", oneEndpoint.RichText)
	}
}

func TestHandleResponseDoesNotAddImageWhenDisabled(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/todos" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","priority":"high"}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	response, ok := handler.HandleResponse(ctx, Input{Text: "td", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "写报告") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image != nil {
		t.Fatalf("image = %#v, want nil", response.Image)
	}
}

func TestHandleResponseLeavesTodoMutationTextOnly(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"todo-1","title":"写报告"}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	handler.EnableImageResponses = true
	response, ok := handler.HandleResponse(ctx, Input{Text: "td 写报告", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "已加待办：写报告") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image != nil {
		t.Fatalf("todo mutation image = %#v, want nil", response.Image)
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			if query := r.URL.Query().Encode(); query != "" {
				t.Fatalf("query = %q", query)
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","dueAt":"2026-05-14T23:55:00+08:00"},{"id":"todo-2","title":"买咖啡"}]}`))
		case r.URL.Path == "/api/workspace/todos/todo-1" && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":123,"title":"写报告"}]}`))
		case r.URL.Path == "/api/workspace/todos/123" && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			if query := r.URL.Query().Encode(); query != "" {
				t.Fatalf("query = %q", query)
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"回工位收拾"},{"id":"todo-2","title":"test"},{"id":"todo-3","title":"创建 2"},{"id":"todo-4","title":"创建 1"}]}`))
		case r.URL.Path == "/api/workspace/todos/batch" && r.Method == http.MethodPatch:
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

type feedbackRecorderFunc func(context.Context, store.Identity, botfeedback.Submission) (botfeedback.Result, error)

func (f feedbackRecorderFunc) Record(ctx context.Context, ident store.Identity, submission botfeedback.Submission) (botfeedback.Result, error) {
	return f(ctx, ident, submission)
}

func TestHandleFeedbackRecordsThroughService(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	ident.Platform = "qqbot"
	db, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	recorder, err := botfeedback.New(db, botfeedback.Config{Targets: []botfeedback.Target{
		{Platform: "napcat", ConversationType: "private", ConversationID: "1001"},
		{Platform: "napcat", ConversationType: "group", ConversationID: "2001"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Store:    db,
		Prefix:   "/life",
		Feedback: recorder,
	}
	reply, ok := handler.Handle(ctx, Input{Text: "反馈 校车显示有点乱", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if reply != "已收到反馈，会转给维护者。" {
		t.Fatalf("reply = %q", reply)
	}
	count, err := db.FeedbackCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("feedback count = %d", count)
	}
	due, err := db.ClaimDue(ctx, time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].Message.Target.Platform != "napcat" || due[1].Message.Target.Platform != "napcat" {
		t.Fatalf("admin intents = %#v", due)
	}
	if !strings.Contains(due[0].Message.Content.Text, "用户反馈") || !strings.Contains(due[0].Message.Content.Text, "用户：42") {
		t.Fatalf("message = %q", due[0].Message.Content.Text)
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
		RawText: "登录 student@example.com",
		Command: "login",
		Handled: true,
		Reply:   "登录成功，student@example.com",
		Status:  store.InteractionStatusHandled,
	}); err != nil {
		t.Fatal(err)
	}
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
	var submission botfeedback.Submission
	handler := Handler{
		Store:  db,
		Prefix: "/life",
		Feedback: feedbackRecorderFunc(func(ctx context.Context, ident store.Identity, got botfeedback.Submission) (botfeedback.Result, error) {
			submission = got
			return botfeedback.Result{ID: 1, AdminIntents: 1}, nil
		}),
	}
	reply, ok := handler.Handle(ctx, Input{Text: "反馈 一下", Identity: ident})
	if !ok || reply != "已收到反馈，会转给维护者。" {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
	if submission.Content != "一下" {
		t.Fatalf("content = %q", submission.Content)
	}
	for _, want := range []string{"用户：xc 东区 高新区", "Bot：东区"} {
		if !strings.Contains(submission.Context, want) {
			t.Fatalf("submission missing %q: %#v", want, submission)
		}
	}
	if strings.Contains(submission.Context, "旧反馈") || strings.Contains(submission.Context, "反馈发送失败") {
		t.Fatalf("submission includes previous feedback: %#v", submission)
	}
	if strings.Contains(submission.Context, "登录") || strings.Contains(submission.Context, "student@example.com") {
		t.Fatalf("submission includes unrelated private context: %#v", submission)
	}
}

func TestFormatFeedbackContextSkipsPrivateCommandsAndRedactsEmail(t *testing.T) {
	contextText := formatFeedbackContext([]store.Interaction{
		{Command: "login", RawText: "登录 student@example.com", Reply: "登录成功"},
	})
	if contextText != "" {
		t.Fatalf("login context = %q", contextText)
	}

	contextText = formatFeedbackContext([]store.Interaction{
		{Command: "agent", RawText: "联系 student@example.com", Reply: "已记录"},
	})
	if strings.Contains(contextText, "student@example.com") || !strings.Contains(contextText, "[已隐藏邮箱]") {
		t.Fatalf("email was not redacted: %q", contextText)
	}
}

func TestFormatFeedbackContextKeepsThreeMostRecentRelevantInteractions(t *testing.T) {
	contextText := formatFeedbackContext([]store.Interaction{
		{Command: "agent", RawText: "最旧问题", Reply: "最旧回复"},
		{Command: "login", RawText: "登录 student@example.com", Reply: "登录成功"},
		{Command: "bus", RawText: "校车", Reply: "校车回复"},
		{Command: "schedule", RawText: "课表", Reply: "课表回复"},
		{Command: "help", RawText: "帮助", Reply: "帮助回复"},
	})
	for _, want := range []string{"校车回复", "课表回复", "帮助回复"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context missing %q: %q", want, contextText)
		}
	}
	if strings.Contains(contextText, "最旧") || strings.Contains(contextText, "登录") || strings.Contains(contextText, "student@example.com") {
		t.Fatalf("context includes excluded history: %q", contextText)
	}
	if strings.Index(contextText, "校车回复") > strings.Index(contextText, "课表回复") ||
		strings.Index(contextText, "课表回复") > strings.Index(contextText, "帮助回复") {
		t.Fatalf("context is not chronological: %q", contextText)
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
		Prefix: "/life",
		Feedback: feedbackRecorderFunc(func(ctx context.Context, target store.Identity, submission botfeedback.Submission) (botfeedback.Result, error) {
			called = true
			if target.ConversationType != "group" || target.ConversationID != "3001" {
				t.Fatalf("target = %#v", target)
			}
			return botfeedback.Result{ID: 1, AdminIntents: 1}, nil
		}),
	}
	reply, ok := handler.Handle(ctx, Input{Text: "fb 群里也可以反馈", Identity: ident})
	if !ok || reply != "已收到反馈，会转给维护者。" || !called {
		t.Fatalf("reply = %q, ok = %v, called = %v", reply, ok, called)
	}
}

func TestHandleFeedbackReportsStoreFailure(t *testing.T) {
	handler := Handler{
		Prefix: "/life",
		Feedback: feedbackRecorderFunc(func(context.Context, store.Identity, botfeedback.Submission) (botfeedback.Result, error) {
			return botfeedback.Result{}, errors.New("sqlite unavailable")
		}),
	}
	reply, ok := handler.Handle(context.Background(), Input{Text: "反馈 校车显示有点乱", Identity: testIdentity()})
	if !ok || !strings.Contains(reply, "反馈保存失败") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleFeedbackWithoutServiceReportsUnavailable(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	reply, ok := handler.Handle(context.Background(), Input{Text: "反馈 校车显示有点乱", Identity: testIdentity()})
	if !ok || reply != "反馈功能暂不可用。" {
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
	if !ok || !strings.Contains(reply, "课表 帮助") {
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
	if !ok || reply != "反馈功能暂不可用。" {
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

	recorder, err := botfeedback.New(db, botfeedback.Config{})
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := Handler{Prefix: "/life", Store: db, Feedback: recorder}.Handle(ctx, Input{
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
		if r.URL.Path != "/api/workspace/todos" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if query := r.URL.Query().Encode(); query != "" {
			t.Fatalf("query = %s", query)
		}
		_, _ = w.Write([]byte(`{"todos":[
			{"id":"todo-1","title":"写报告","priority":"high","dueAt":"2026-06-09T18:00:00+08:00"},
			{"id":"todo-2","title":"太晚","priority":"high","dueAt":"2026-06-11T18:00:00+08:00"},
			{"id":"todo-3","title":"低优先级","priority":"low","dueAt":"2026-06-09T18:00:00+08:00"}
		]}`))
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
	if strings.Contains(reply, "太晚") || strings.Contains(reply, "低优先级") {
		t.Fatalf("reply contains unfiltered todos: %q", reply)
	}
}

func TestFilteredTodoIndexesMatchActions(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	patched := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			if r.URL.Query().Get("priority") == "high" {
				_, _ = w.Write([]byte(`{"todos":[{"id":"high","title":"高优先级","priority":"high","completed":false}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"todos":[
				{"id":"low","title":"低优先级","priority":"low","completed":false},
				{"id":"high","title":"高优先级","priority":"high","completed":false}
			]}`))
		case strings.HasPrefix(r.URL.Path, "/api/workspace/todos/") && r.Method == http.MethodPatch:
			patched = strings.TrimPrefix(r.URL.Path, "/api/workspace/todos/")
			_, _ = w.Write([]byte(`{"completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "待办 列表 优先级 高", Identity: ident})
	plain := textutil.PlainMonospace(reply)
	if !ok || !strings.Contains(plain, "2. \t\t高优先级") || strings.Contains(plain, "低优先级") {
		t.Fatalf("filtered reply = %q, ok = %v", plain, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "待办 完成 2", Identity: ident})
	if !ok || patched != "high" || !strings.Contains(reply, "高优先级") {
		t.Fatalf("patched=%q, reply=%q, ok=%v", patched, reply, ok)
	}
}

func TestFilterNumberedTodosMatchesAPIDateOnlyUTC(t *testing.T) {
	todos := []map[string]any{
		{"id": "early", "dueAt": "2026-06-10T04:00:00+08:00"},
		{"id": "late", "dueAt": "2026-06-10T09:00:00+08:00"},
	}
	filtered, err := filterNumberedTodos(todos, life.TodoListOptions{DueBefore: "2026-06-10"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || lifedata.FirstString(filtered[0].todo, "id") != "early" {
		t.Fatalf("filtered = %#v", filtered)
	}
}

func TestHandleTodoListPaginatesThirtyItemsWithGlobalIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	todos := make([]map[string]any, 65)
	due := time.Date(2026, 7, 1, 8, 0, 0, 0, lifedata.ChinaLocation())
	for i := range todos {
		todos[i] = map[string]any{
			"id":    fmt.Sprintf("todo-%02d", i+1),
			"title": fmt.Sprintf("Todo %02d", i+1),
			"dueAt": due.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		}
	}
	patched31 := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			if query := r.URL.Query().Encode(); query != "" {
				t.Fatalf("query = %q", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"todos": todos})
		case r.URL.Path == "/api/workspace/todos/todo-31" && r.Method == http.MethodPatch:
			patched31 = true
			_, _ = w.Write([]byte(`{"id":"todo-31","completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "待办 列表 第2页", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	plain := textutil.PlainMonospace(reply)
	for _, want := range []string{"31.\t", "Todo 31", "60.\t", "Todo 60", "第 2/3 页", "待办 列表 未完成 第1页", "待办 列表 未完成 第3页"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reply missing %q: %q", want, plain)
		}
	}
	for _, unwanted := range []string{"30.\t", "Todo 30", "61.\t", "Todo 61"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("reply unexpectedly contains %q: %q", unwanted, plain)
		}
	}
	reply, ok = handler.Handle(ctx, Input{Text: "待办 完成 31", Identity: ident})
	if !ok || !patched31 || !strings.Contains(textutil.PlainMonospace(reply), "Todo 31") {
		t.Fatalf("page 2 action mismatch: patched31=%v, reply=%q, ok=%v", patched31, reply, ok)
	}
}

func TestHandleTodoListRejectsPagePastEnd(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"Todo 1"}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "待办 列表 第2页", Identity: ident})
	if !ok || !strings.Contains(reply, "待办只有 1 页") || !strings.Contains(reply, "待办 列表 未完成 第1页") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestHandleTodoAddWithOptionalFields(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/todos" || r.Method != http.MethodPost {
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			if query := r.URL.Query().Encode(); query != "" {
				t.Fatalf("query = %q", query)
			}
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","completed":true}]}`))
		case r.URL.Path == "/api/workspace/todos/todo-1" && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"旧标题"}]}`))
		case r.URL.Path == "/api/workspace/todos/todo-1" && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/api/workspace/todos" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告"}]}`))
		case r.URL.Path == "/api/workspace/todos/todo-1" && r.Method == http.MethodDelete:
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
	if line != "截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻\t写报告" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatNumberedLinePadsBeforeTab(t *testing.T) {
	line := formatNumberedLine(1, "截止 05-14 23:55 写报告")
	if line != "𝟷. \t截止 𝟶𝟻-𝟷𝟺 𝟸𝟹:𝟻𝟻 写报告" {
		t.Fatalf("line = %q", line)
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
		case r.URL.Path == "/api/workspace/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"}},"completion":null},{"id":"hw-2","title":"Old PS","submissionDueAt":"2026-05-01T12:00:00+08:00","section":{"course":{"namePrimary":"组合数学"}},"completion":null}]}`))
		case r.URL.Path == "/api/workspace/homeworks/hw-1/completion" && r.Method == http.MethodPut:
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
	if !strings.Contains(reply, "已逾期：") || !strings.Contains(reply, "截止 𝟶𝟼-𝟶𝟹 𝟷𝟸:𝟶𝟶\t数据库系统\tProblem Set 𝟷") {
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

func TestHomeworkDisplayIndexesMatchActionsForUndatedItems(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	future := chinaNow().AddDate(0, 0, 30).Format(time.RFC3339)
	completedID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspace/homeworks" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"homeworks": []any{
				map[string]any{"id": "future", "title": "未来作业", "submissionDueAt": future, "completion": nil},
				map[string]any{"id": "undated", "title": "未定日期作业", "completion": nil},
			}})
		case strings.HasPrefix(r.URL.Path, "/api/workspace/homeworks/") && r.Method == http.MethodPut:
			completedID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/workspace/homeworks/"), "/completion")
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
	plain := textutil.PlainMonospace(reply)
	if !strings.Contains(plain, "1. \t\t\t未定日期作业") || !strings.Contains(plain, "2. \t截止") {
		t.Fatalf("unexpected display order: %q", plain)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "作业 完成 1", Identity: ident})
	if !ok || completedID != "undated" || !strings.Contains(reply, "未定日期作业") {
		t.Fatalf("completedID=%q, reply=%q, ok=%v", completedID, reply, ok)
	}
}

func TestHandleHomeworkListFiltersBySemesterID(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	completedID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspace/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[
				{"id":"hw-spring","title":"Spring HW","submissionDueAt":"2026-05-01T12:00:00+08:00","section":{"course":{"namePrimary":"组合数学"},"semester":{"id":2,"jwId":202501,"namePrimary":"2026春季"}},"completion":null},
				{"id":"hw-summer","title":"Summer HW","submissionDueAt":"2026-07-10T12:00:00+08:00","section":{"course":{"namePrimary":"数据库系统"},"semester":{"id":3,"jwId":202502,"namePrimary":"2026夏季"}},"completion":null}
			]}`))
		case strings.HasPrefix(r.URL.Path, "/api/workspace/homeworks/") && r.Method == http.MethodPut:
			completedID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/workspace/homeworks/"), "/completion")
			_, _ = w.Write([]byte(`{"completed":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "作业 semester_id 2", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "组合数学") || !strings.Contains(reply, "Spring HW") {
		t.Fatalf("reply missing spring homework: %q", reply)
	}
	if strings.Contains(reply, "数据库系统") || strings.Contains(reply, "Summer HW") {
		t.Fatalf("reply included summer homework: %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "作业 all semester_jw_id 202501", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "组合数学") || !strings.Contains(reply, "Spring HW") {
		t.Fatalf("reply missing spring homework for jw_id: %q", reply)
	}
	if strings.Contains(reply, "数据库系统") || strings.Contains(reply, "Summer HW") {
		t.Fatalf("reply included summer homework for jw_id: %q", reply)
	}

	reply, ok = handler.Handle(ctx, Input{Text: "作业 semester_id 3", Identity: ident})
	plain := textutil.PlainMonospace(reply)
	if !ok || !strings.Contains(plain, "2. \t") || !strings.Contains(plain, "Summer HW") || strings.Contains(plain, "Spring HW") {
		t.Fatalf("filtered summer reply = %q, ok = %v", plain, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "作业 完成 2", Identity: ident})
	if !ok || completedID != "hw-summer" || !strings.Contains(reply, "Summer HW") {
		t.Fatalf("completedID=%q, reply=%q, ok=%v", completedID, reply, ok)
	}
}

func TestHandleHomeworkDoneBatchByCommaSeparatedIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspace/homeworks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"homeworks":[{"id":"hw-1","title":"Problem Set 1","submissionDueAt":"2026-06-03T12:00:00+08:00","completion":null},{"id":"hw-2","title":"Problem Set 2","submissionDueAt":"2026-06-04T12:00:00+08:00","completion":null},{"id":"hw-3","title":"Problem Set 3","submissionDueAt":"2026-06-05T12:00:00+08:00","completion":null}]}`))
		case r.URL.Path == "/api/workspace/homeworks/completions" && r.Method == http.MethodPut:
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

func TestFormatHomeworkListSeparatesCompletedFromOverdue(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, lifedata.ChinaLocation())
	reply := formatHomeworkListAt([]map[string]any{
		{"id": "done", "title": "Finished", "submissionDueAt": "2026-06-07T10:00:00+08:00", "isCompleted": true},
		{"id": "open", "title": "Pending", "submissionDueAt": "2026-06-07T11:00:00+08:00"},
	}, now)
	completedAt := strings.Index(reply, "已完成：")
	overdueAt := strings.Index(reply, "已逾期：")
	finishedAt := strings.Index(reply, "Finished")
	pendingAt := strings.Index(reply, "Pending")
	if completedAt < 0 || overdueAt < 0 || finishedAt < completedAt || pendingAt < overdueAt {
		t.Fatalf("reply = %q", reply)
	}
	if finishedAt > overdueAt && finishedAt < pendingAt {
		t.Fatalf("completed homework appears in overdue group: %q", reply)
	}
}

func TestHandleHomeworkListPaginatesThirtyItemsWithGlobalIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	homeworks := make([]map[string]any, 65)
	due := time.Date(2026, 7, 1, 8, 0, 0, 0, lifedata.ChinaLocation())
	for i := range homeworks {
		homeworks[i] = map[string]any{
			"id":              fmt.Sprintf("homework-%02d", i+1),
			"title":           fmt.Sprintf("Homework %02d", i+1),
			"submissionDueAt": due.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			"completion":      nil,
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/homeworks" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"homeworks": homeworks})
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "作业 列表 第2页", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	plain := textutil.PlainMonospace(reply)
	for _, want := range []string{"31.\t", "Homework 31", "60.\t", "Homework 60", "第 2/3 页", "作业 列表 未完成 第1页", "作业 列表 未完成 第3页"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reply missing %q: %q", want, plain)
		}
	}
	for _, unwanted := range []string{"30.\t", "Homework 30", "61.\t", "Homework 61"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("reply unexpectedly contains %q: %q", unwanted, plain)
		}
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
		case "/api/workspace/subscriptions/current":
			_, _ = fmt.Fprintf(w, `{"subscription":{"sections":[
				{"id":101,"code":"CS1001.01","course":{"namePrimary":"计算机导论"},"semester":{"startDate":"2026-02-01T00:00:00+08:00","endDate":"2026-07-01T00:00:00+08:00"},"exams":[{"id":1,"examDate":%q,"startTime":900,"endTime":1100,"examRooms":[{"room":"GT-B112"}]}]}
			]}}`, today+"T00:00:00+08:00")
		case "/api/workspace/schedules":
			_, _ = fmt.Fprintf(w, `{"schedules":[{"id":1,"date":%q,"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"计算机导论"}},"room":{"namePrimary":"3A101"}}]}`, today+"T00:00:00+08:00")
		case "/api/workspace/todos":
			if r.URL.Query().Get("completed") != "false" {
				t.Fatalf("completed = %q", r.URL.Query().Get("completed"))
			}
			_, _ = fmt.Fprintf(w, `{"todos":[{"id":"todo-1","title":"写报告","dueAt":%q}]}`, today+"T18:00:00+08:00")
		case "/api/workspace/homeworks":
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
	for _, want := range []string{"安排：", "今日课表 (1)：", "计算机导论", "待办 (1)：", "写报告", "近期作业 (1)：", "作业一", "考试 (1)：", wantExamDate} {
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
		if r.URL.Path != "/api/workspace/subscriptions/current" {
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
		"𝟷. \t𝟶𝟼-𝟷𝟶\t𝟶𝟿:𝟶𝟶-𝟷𝟷:𝟶𝟶\t计算机导论\t𝙲𝚂𝟷𝟶𝟶𝟷.𝟶𝟷\t\t𝙶𝚃-𝙱𝟷𝟷𝟸",
		"𝟸. \t𝟶𝟼-𝟸𝟶\t𝟷𝟺:𝟹𝟶-𝟷𝟼:𝟹𝟶\t数学分析\t𝙼𝙰𝚃𝙷𝟷𝟶𝟶𝟷.𝟶𝟷\t闭卷\t𝟹𝙰𝟷𝟶𝟷",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Index(reply, "计算机导论") > strings.Index(reply, "数学分析") {
		t.Fatalf("reply not sorted by date: %q", reply)
	}
}

func TestHandleExamListPaginatesThirtyItemsWithGlobalIndexes(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	exams := make([]map[string]any, 65)
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, lifedata.ChinaLocation())
	for i := range exams {
		exams[i] = map[string]any{
			"id":        i + 1,
			"examDate":  date.AddDate(0, 0, i).Format(time.RFC3339),
			"startTime": 900,
			"endTime":   1100,
			"examMode":  fmt.Sprintf("Exam %02d", i+1),
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subscription": map[string]any{
				"sections": []any{map[string]any{
					"code":   "TEST1001.01",
					"course": map[string]any{"namePrimary": "测试课程"},
					"exams":  exams,
				}},
			},
		})
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "考试 第2页", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	plain := textutil.PlainMonospace(reply)
	for _, want := range []string{"31.\t", "Exam 31", "60.\t", "Exam 60", "第 2/3 页", "考试 第1页", "考试 第3页"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reply missing %q: %q", want, plain)
		}
	}
	for _, unwanted := range []string{"30.\t", "Exam 30", "61.\t", "Exam 61"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("reply unexpectedly contains %q: %q", unwanted, plain)
		}
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
	if line != "日期待定\t\t随机过程" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatExamRoomsDeduplicatesSharedBuildingPrefix(t *testing.T) {
	exam := func(rooms ...string) map[string]any {
		list := make([]any, len(rooms))
		for i, room := range rooms {
			list[i] = map[string]any{"name": room}
		}
		return map[string]any{"examRooms": list}
	}
	if got := formatExamRooms(exam("东区 五教 5102", "东区 五教 5103")); got != "东区 五教 5102、5103" {
		t.Fatalf("shared-prefix rooms = %q", got)
	}
	if got := formatExamRooms(exam("东区 五教 5102", "西区 三教 3A204")); got != "东区 五教 5102、西区 三教 3A204" {
		t.Fatalf("distinct rooms = %q", got)
	}
	if got := formatExamRooms(exam("东区 五教 5102")); got != "东区 五教 5102" {
		t.Fatalf("single room = %q", got)
	}
	if got := formatExamRooms(exam("GT-B112", "GT-B113")); got != "GT-B112、GT-B113" {
		t.Fatalf("prefix-less rooms = %q", got)
	}
}

func TestHandleTodayCurriculum(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/workspace/schedules":
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

func TestHandleCurriculumDateShowsContainingWeek(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, lifedata.ChinaLocation())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/api/workspace/schedules" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("dateFrom"); got != "2026-06-20T16:00:00Z" {
			t.Fatalf("dateFrom = %q", got)
		}
		if got := r.URL.Query().Get("dateTo"); got != "2026-06-27T15:59:59Z" {
			t.Fatalf("date range = %q %q", r.URL.Query().Get("dateFrom"), r.URL.Query().Get("dateTo"))
		}
		_, _ = w.Write([]byte(`{"schedules":[{"date":"2026-06-23T00:00:00+08:00","startTime":"07:50","endTime":"09:25","section":{"course":{"namePrimary":"随机过程理论"}},"room":{"namePrimary":"GT-A405"}}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, []string{"week-date:6.23"}, base)
	if !strings.Contains(reply, "06-21 至 06-27 课表：") || !strings.Contains(reply, "随机过程理论") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleCurriculumDoesNotClaimNoClassWithoutSubscriptions(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, lifedata.ChinaLocation())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/schedules":
			_, _ = w.Write([]byte(`{"schedules":[]}`))
		case "/api/workspace/subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[]}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, []string{"date:5.3"}, base)
	if !strings.Contains(reply, "没有查到已关注的班级") || strings.Contains(reply, "没有课") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestHandleCurriculumConfirmsNoClassWhenSubscriptionsExist(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, lifedata.ChinaLocation())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/schedules":
			_, _ = w.Write([]byte(`{"schedules":[]}`))
		case "/api/workspace/subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":71}]}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, []string{"date:5.3"}, base)
	if !strings.Contains(reply, "没有课") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestParseScheduleDateTokenSupportsDottedFullDate(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, lifedata.ChinaLocation())
	parsed, ok := parseScheduleDateToken("date:2022.05.03", base)
	if !ok {
		t.Fatal("date was not parsed")
	}
	if got := parsed.Format("2006-01-02"); got != "2022-05-03" {
		t.Fatalf("parsed date = %q, want 2022-05-03", got)
	}
}

func TestCurriculumRejectsInvalidDateInsteadOfUsingToday(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, lifedata.ChinaLocation())
	reply := (Handler{}).curriculumAt(
		context.Background(),
		testIdentity(),
		[]string{"2022.05.99"},
		base,
	)
	if !strings.Contains(reply, "日期格式不太对") {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/schedules":
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

func TestBareCurriculumReusesRefreshedTokenForWeek(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspace/schedules":
			switch r.Header.Get("Authorization") {
			case "Bearer access":
				scheduleOldTokenCalls++
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			case "Bearer refreshed":
				scheduleCalls++
				_, _ = fmt.Fprintf(w, `{"schedules":[
					{"date":"%sT08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}}},
					{"date":"%sT08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}}}
				]}`, today, tomorrow)
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
	if refreshRequests != 1 || scheduleOldTokenCalls != 1 || scheduleCalls != 1 {
		t.Fatalf("refreshRequests = %d, scheduleOldTokenCalls = %d, scheduleCalls = %d", refreshRequests, scheduleOldTokenCalls, scheduleCalls)
	}
}

func TestBareCurriculumShowsSundayToSaturdayWeek(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	scheduleCalls := 0
	day := time.Date(2026, 7, 16, 12, 0, 0, 0, lifedata.ChinaLocation())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/schedules":
			scheduleCalls++
			if got := r.URL.Query().Get("dateFrom"); got != "2026-07-11T16:00:00Z" {
				t.Fatalf("dateFrom = %q", got)
			}
			if got := r.URL.Query().Get("dateTo"); got != "2026-07-18T15:59:59Z" {
				t.Fatalf("dateTo = %q", got)
			}
			_, _ = w.Write([]byte(`{"schedules":[
				{"date":"2026-07-12T08:00:00+08:00","startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}},
				{"date":"2026-07-18T08:00:00+08:00","startTime":"14:00","endTime":"15:35","section":{"course":{"namePrimary":"编译原理"}},"room":{"namePrimary":"GT-B112"}}
			]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, nil, day)
	for _, want := range []string{"07-12 至 07-18 课表：", "周日 07-12：", "周六 07-18：", "数据库系统", "编译原理"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if scheduleCalls != 1 {
		t.Fatalf("scheduleCalls = %d", scheduleCalls)
	}
}

func TestCurriculumSupportsAcademicWeekNumber(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/catalog/semesters/current":
			_, _ = w.Write([]byte(`{"startDate":"2026-02-23T00:00:00+08:00","endDate":"2026-07-05T23:59:59+08:00"}`))
		case "/api/workspace/schedules":
			if got := r.URL.Query().Get("dateFrom"); got != "2026-03-07T16:00:00Z" {
				t.Fatalf("dateFrom = %q", got)
			}
			if got := r.URL.Query().Get("dateTo"); got != "2026-03-14T15:59:59Z" {
				t.Fatalf("dateTo = %q", got)
			}
			_, _ = w.Write([]byte(`{"schedules":[]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply := handler.curriculumAt(ctx, ident, []string{"week-number:3"}, time.Date(2026, 7, 16, 12, 0, 0, 0, lifedata.ChinaLocation()))
	if !strings.Contains(reply, "03-08 至 03-14 课表：") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestNextClassSkipsPastClassAtFixedTime(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/schedules":
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
		if r.URL.Path != "/api/catalog/schedules" {
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
	if !strings.Contains(reply, "订阅 添加") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestSubscriptionCalendarLink(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"https://example.test/calendar/private-token.ics"}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "订阅链接", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(reply, "https://example.test/calendar/private-token.ics") || !strings.Contains(reply, "请勿公开") {
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
	if !ok || !strings.Contains(reply, "设置 帮助：") || !strings.Contains(reply, "设置 通知") || strings.Contains(reply, "AI 工具") {
		t.Fatalf("settings help reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "设置 通知", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：关") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("nested notification settings reply = %q, ok = %v", reply, ok)
	}
	reply, ok = handler.Handle(ctx, Input{Text: "设置 通知 课表 开", Identity: ident})
	if !ok || !strings.Contains(reply, "课前提醒：开") || !strings.Contains(reply, "作业提醒：关") {
		t.Fatalf("nested notification update reply = %q, ok = %v", reply, ok)
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
	if ok {
		t.Fatalf("unknown notify kind should fall through, reply = %q", reply)
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
	if ok {
		t.Fatalf("invalid agent args should fall through, reply = %q", reply)
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
		if r.URL.Path != "/api/workspace/subscriptions/current" {
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
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspace/subscriptions/batch":
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
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspace/subscriptions/batch":
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

func TestFormatScheduleUsesFixedColumns(t *testing.T) {
	line := formatSchedule(map[string]any{
		"startTime":   "07:50",
		"endTime":     "09:25",
		"customPlace": "GT-A405",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "随机过程理论"},
		},
	})
	if line != "𝙶𝚃-𝙰𝟺𝟶𝟻\t𝟶𝟽:𝟻𝟶-𝟶𝟿:𝟸𝟻\t随机过程理论" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatSchedulePreservesMissingTimeColumn(t *testing.T) {
	line := formatSchedule(map[string]any{
		"customPlace": "GT-A405",
		"section": map[string]any{
			"course": map[string]any{"namePrimary": "随机过程理论"},
		},
	})
	if line != "𝙶𝚃-𝙰𝟺𝟶𝟻\t\t随机过程理论" {
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
		"日程": "help",
		"rc": "calendar",
		"kb": "schedule",
		"老师": "help",
		"js": "teacher",
		"考试": "exam",
		"ks": "exam",
		"状态": "status",
		"zt": "status",
		"设置": "settings",
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

func TestCanonicalCommandHierarchy(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	tests := []struct {
		text string
		name string
		args string
	}{
		{text: "日程", name: "help", args: "日程"},
		{text: "日程 今日", name: "calendar"},
		{text: "日程 概览", name: "overview"},
		{text: "日程 截止 14", name: "upcoming_deadlines", args: "14"},
		{text: "课表 单日 今天", name: "schedule", args: "today"},
		{text: "课表 单日 明天", name: "schedule", args: "tomorrow"},
		{text: "课表 下一节", name: "nextclass"},
		{text: "待办 添加 写报告", name: "todo", args: "add 写报告"},
		{text: "待办 恢复 1", name: "todo", args: "undo 1"},
		{text: "待办 列表 第2页", name: "todo", args: "list 第2页"},
		{text: "作业 列表 学期ID 42", name: "homework", args: "semester_id 42"},
		{text: "作业 列表 第2页", name: "homework", args: "第2页"},
		{text: "作业 恢复 1", name: "homework", args: "undo 1"},
		{text: "考试 第2页", name: "exam", args: "第2页"},
		{text: "课程", name: "help", args: "课程"},
		{text: "课程 搜索 关键词 数学分析 培养层次ID 1 类别ID 2 课堂类型ID 3 数量 10", name: "course_search", args: "keyword 数学分析 education_level_id 1 category_id 2 class_type_id 3 limit 10"},
		{text: "课程 查看 123", name: "course_by_jw_id", args: "123"},
		{text: "教学班", name: "help", args: "教学班"},
		{text: "教学班 搜索 课程ID 11 老师代码 T001 数量 20", name: "section_search", args: "course_id 11 teacher_code T001 limit 20"},
		{text: "教学班 查看 123", name: "section_by_jw_id", args: "123"},
		{text: "教学班 课表 123 2026-07-01 2026-07-07", name: "section_schedules", args: "123 2026-07-01 2026-07-07"},
		{text: "教学班 考试 123", name: "section_exams", args: "123"},
		{text: "教学班 考试 123 第2页", name: "section_exams", args: "123 第2页"},
		{text: "教学班 作业 123", name: "section_homeworks", args: "123"},
		{text: "教学班 作业 123 第2页", name: "section_homeworks", args: "123 第2页"},
		{text: "老师", name: "help", args: "老师"},
		{text: "老师 搜索 院系ID 5 数量 8", name: "teacher_search", args: "department_id 5 limit 8"},
		{text: "老师 查看 12", name: "teacher_by_id", args: "12"},
		{text: "学期 当前", name: "semester"},
		{text: "学期 列表 10", name: "list_semesters", args: "10"},
		{text: "订阅 添加 CONT5103P.01 CONT6104P.01", name: "subscription", args: "import CONT5103P.01 CONT6104P.01"},
		{text: "订阅 删除 999", name: "unsubscribe_section_by_jw_id", args: "999"},
		{text: "订阅 列表", name: "my_subscribed_sections"},
		{text: "订阅 链接", name: "subscription", args: "link"},
		{text: "课程订阅", name: "subscription"},
		{text: "课程 订阅", name: "subscription"},
		{text: "校车 查询 东区 西区", name: "bus", args: "东区 西区"},
		{text: "校车 路线 从 东区 到 西区", name: "bus_routes", args: "从 东区 到 西区"},
		{text: "校车 偏好 路线 东区 西区", name: "bus", args: "设置 东区 西区"},
		{text: "校车 偏好 已发车 开", name: "bus", args: "已发车 开"},
		{text: "账户", name: "help", args: "账户"},
		{text: "账户 登录 状态", name: "login", args: "status"},
		{text: "账户 信息", name: "account"},
		{text: "账户 退出", name: "logout"},
		{text: "设置 通知 课表 开", name: "notify", args: "classes on"},
		{text: "设置 工具调用 开", name: "agent", args: "on"},
		{text: "设置 AI 工具 关", name: "agent", args: "off"},
		{text: "系统", name: "help", args: "系统"},
		{text: "系统 状态", name: "status"},
		{text: "系统 检查", name: "ping"},
	}
	for _, tt := range tests {
		cmd, ok := handler.parse(tt.text)
		if !ok || cmd.Name != tt.name || joinedArgs(cmd.Args) != tt.args {
			t.Errorf("%q parsed as %#v, ok = %v; want name=%q args=%q", tt.text, cmd, ok, tt.name, tt.args)
		}
	}
}

func TestLegacyCommandsRemainCompatibleWithCanonicalHierarchy(t *testing.T) {
	handler := Handler{Prefix: "/life"}
	tests := []struct {
		text string
		name string
	}{
		{text: "课程搜索 keyword 数学分析", name: "course_search"},
		{text: "课程编号 123", name: "course_by_jw_id"},
		{text: "教学班搜索 keyword 高等数学", name: "section_search"},
		{text: "教学班课表 123 2026-07-01 2026-07-07", name: "section_schedules"},
		{text: "老师搜索 keyword 张", name: "teacher_search"},
		{text: "学期列表 10", name: "list_semesters"},
		{text: "我的订阅", name: "my_subscribed_sections"},
		{text: "退订教学班 999", name: "unsubscribe_section_by_jw_id"},
		{text: "校车路线 从 东区 到 西区", name: "bus_routes"},
		{text: "通知 课表 开", name: "notify"},
		{text: "AI 工具 开", name: "agent"},
		{text: "待办 写报告", name: "todo"},
	}
	for _, tt := range tests {
		cmd, ok := handler.parse(tt.text)
		if !ok || cmd.Name != tt.name {
			t.Errorf("%q parsed as %#v, ok = %v; want %q", tt.text, cmd, ok, tt.name)
		}
	}
}

func TestParseAttachedFeedbackAndNotifyCommands(t *testing.T) {
	handler := Handler{Prefix: "/life"}

	if _, ok := handler.parse("反馈上面的对话问题"); ok {
		t.Fatal("attached feedback without space should not parse")
	}
	if _, ok := handler.parse("通知课表开"); ok {
		t.Fatal("attached notify without space should not parse")
	}
	if _, ok := handler.parse("提醒我这周三之前研究清楚"); ok {
		t.Fatal("natural-language reminder should not parse as notify")
	}
	if _, ok := handler.parse("建议你改进校车显示"); ok {
		t.Fatal("natural-language suggestion should not parse as feedback")
	}

	cmd, ok := handler.parse("反馈 上面的对话问题")
	if !ok || cmd.Name != "feedback" || strings.Join(cmd.Args, " ") != "上面的对话问题" {
		t.Fatalf("spaced feedback parsed as %#v, ok=%v", cmd, ok)
	}
	cmd, ok = handler.parse("通知 课表 开")
	if !ok || cmd.Name != "notify" || strings.Join(cmd.Args, " ") != "classes on" {
		t.Fatalf("spaced notify parsed as %#v, ok=%v", cmd, ok)
	}
	if _, ok = handler.parse("设置作业呃开"); ok {
		t.Fatal("compact settings text unexpectedly parsed as a notification command")
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

func TestNormalizeScheduleArgsSupportsWeekTargets(t *testing.T) {
	tests := map[string]string{
		"本周":    "this-week",
		"这周":    "this-week",
		"下周":    "next-week",
		"第3周":   "week-number:3",
		"7.20周": "week-date:7.20",
	}
	for input, want := range tests {
		got := normalizeScheduleArgs([]string{input})
		if len(got) != 1 || got[0] != want {
			t.Fatalf("normalizeScheduleArgs(%q) = %#v, want %q", input, got, want)
		}
	}

	handler := Handler{Prefix: "/life"}
	for input, want := range map[string]string{
		"课表本周":  "this-week",
		"课表第3周": "week-number:3",
	} {
		cmd, ok := handler.parse(input)
		if !ok || cmd.Name != "schedule" || len(cmd.Args) != 1 || cmd.Args[0] != want {
			t.Fatalf("%q parsed as %#v, ok=%v", input, cmd, ok)
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
		"今日课表":              {"today"},
		"今日课标":              {"today"},
		"单日课表":              {"today"},
		"单日 课表":             {"today"},
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
		"课表 6.23":           {"week-date:6.23"},
		"课表 05.06":          {"week-date:05.06"},
		"课表 2022.05.03":     {"week-date:2022.05.03"},
		"6.23 课表":           {"week-date:6.23"},
		"课表6月23日":           {"week-date:6月23日"},
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
	if !ok || !strings.Contains(reply, "待办（td）\t查看和管理待办") {
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
		if r.URL.Path != "/api/catalog/courses" {
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
		if r.URL.Path != "/api/catalog/sections" {
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
		if r.URL.Path != "/api/catalog/teachers" {
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
		if r.URL.Path != "/api/catalog/courses/123" {
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
		if r.URL.Path != "/api/catalog/sections/456" {
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
		if r.URL.Path != "/api/catalog/teachers/12" {
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
		if r.URL.Path != "/api/catalog/semesters" {
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
				"nameCn":            "东高新线",
				"originCampus":      map[string]any{"namePrimary": "东区"},
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
		case "/api/catalog/bus/routes":
			_, _ = w.Write([]byte(`{"routes":[{"nameCn":"东高新线","originCampus":{"namePrimary":"东区"},"destinationCampus":{"namePrimary":"高新区"},"stops":[]}],"campuses":[{"id":1,"namePrimary":"东区"},{"id":2,"namePrimary":"高新区"}]}`))
		case "/api/catalog/bus":
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
		if r.URL.Path != "/api/catalog/sections/789/schedules" {
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
		if r.URL.Path != "/api/catalog/sections/321" {
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
		if r.URL.Path != "/api/community/section-homeworks" {
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
					"section":   map[string]any{"course": map[string]any{"namePrimary": "数学分析"}},
					"examDate":  "2026-06-20T00:00:00+08:00",
					"startTime": 900,
					"endTime":   1100,
					"examRooms": []any{map[string]any{"room": "3A101"}},
				},
			},
		},
	}
	reply := formatDashboard(data, "我的概览")
	for _, want := range []string{"我的概览", "待办 (1)：", "作业 (1)：", "考试 (1)：", "数学分析"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(reply, "今日课表 2") || strings.Contains(reply, "待交作业 3") || strings.Contains(reply, " · ") {
		t.Fatalf("reply still contains the summary line: %q", reply)
	}
}

func TestFormatDashboardUsesTotalsAndPointsToFullLists(t *testing.T) {
	items := make([]any, summaryDisplayLimit)
	for i := range items {
		items[i] = map[string]any{"title": fmt.Sprintf("Todo %d", i+1)}
	}
	reply := formatDashboard(map[string]any{
		"dueTodos": map[string]any{"total": 20, "items": items},
	}, "我的概览")
	plain := textutil.PlainMonospace(reply)
	for _, want := range []string{"待办 (20)：", "另有 12 条", "发送「待办」查看完整列表"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reply missing %q: %q", want, plain)
		}
	}
	image := (Handler{EnableImageResponses: true}).imageResponseFor(parsedCommand{Name: "overview"}, reply)
	if image == nil || !strings.Contains(textutil.PlainMonospace(image.RichText), "|  |  | 另有 12 条") {
		t.Fatalf("overview image = %#v", image)
	}
	assertResponseImageRenders(t, image)
}

func TestFormatOverviewPointsToCompleteList(t *testing.T) {
	todos := make([]map[string]any, 4)
	for i := range todos {
		todos[i] = map[string]any{"title": fmt.Sprintf("Todo %d", i+1)}
	}
	reply := textutil.PlainMonospace(formatOverview(chinaNow(), nil, todos, nil, nil))
	for _, want := range []string{"待办 (4)：", "Todo 3", "另有 1 条", "发送「待办」查看完整列表"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(reply, "Todo 4") || strings.Contains(reply, "...and") {
		t.Fatalf("reply contains inaccessible preview content: %q", reply)
	}
}

func TestHandleDashboard(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/overview" {
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
		if r.URL.Path != "/api/workspace/overview" {
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
		case r.URL.Path == "/api/workspace/subscriptions/current" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"subscription":{"sections":[{"id":101,"jwId":999,"code":"CS1001.01","course":{"namePrimary":"计算机导论"}}]}}`))
		case r.URL.Path == "/api/workspace/subscriptions/batch" && r.Method == http.MethodPost:
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
