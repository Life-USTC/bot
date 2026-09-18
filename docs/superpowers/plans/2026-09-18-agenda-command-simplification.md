# 日程命令简化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 日程家族只保留 `日程 [日期]` 与 `日程 链接` 两种用法，以 1/16 概率在日程回复尾部追加 iCalendar 订阅链接，并删除"已删除功能"的防御代码与测试。

**Architecture:** 新文件 `internal/commands/agenda.go` 实现单日聚合日程（一次 `ListAllPersonalCalendarEvents(date, date)`，按五类事件分组渲染）；`calendar` capability descriptor 改指向新实现并删除 `ddl` 别名；`overview`/`upcoming_deadlines` 两个 capability 全链路删除，持久化旧 job 通过 `RestoreInvocation` 旧 ID 映射恢复为 `calendar`。

**Tech Stack:** Go（Bot 子仓库，工作目录 `/home/tiankaima/Source/Life-USTC/Bot`），httptest 假服务端 + `testAuthedHandler` 测试模式。

**Spec:** `docs/superpowers/specs/2026-09-18-agenda-command-simplification-design.md`（已获用户确认：概率 1/16，删除 ddl 别名）。

## Global Constraints

- 所有改动在 `Bot/` 子仓库内；服务端端点（`/api/workspace/overview` 等）不动。
- 日程家族全部 `DataScopeUserPrivate`，群内永远不响应；群内公共命令精确匹配规则不动。
- 群内未 @ 的 `/life xxx` 删除防御代码后落到既有忽略路径，群规则不受影响。
- 自然语言路由标识 `calendar_link` 保持不变。
- 每个任务结束 `go build ./... && go test ./internal/...` 必须全绿（在 `Bot/` 目录下运行），并单独 commit。
- 不要改动 `agent/lazy_mcp.go:366` 的 `add("截止", "截止时间", "ddl")`——那是 agent 工具搜索关键词，与命令别名无关。

---

### Task 1: 删除 /life 前缀防御代码与"已删除功能"测试

**Files:**
- Modify: `internal/commands/commands.go`（删 `HasRemovedCommandPrefix` 及其调用点 :301；修 feedback 帮助文本 :1195）
- Modify: `internal/routing/router.go:37-40`（删调用点）
- Test: `internal/commands/commands_test.go`（删 `TestLifePrefixIsRejected` :3708、`TestRemovedLifePrefixIsNotHandledOrLogged` :3925）
- Test: `internal/routing/router_test.go`（删 `TestRemovedLifePrefixNeverFallsThroughToAgentOrNaturalRoutes` :126-143）
- Test: `internal/commands/capability_registry_test.go`（删 `TestRemovedCommandFormsAreNotAccepted` :117-123）

**Interfaces:**
- Consumes: 无
- Produces: 无（纯删除 + 一行文案修复）

- [ ] **Step 1: 删除四个 removal 测试**

删除以下测试函数（整个函数体）：

- `internal/routing/router_test.go:126-143` `TestRemovedLifePrefixNeverFallsThroughToAgentOrNaturalRoutes`
- `internal/commands/commands_test.go:3708-3718` `TestLifePrefixIsRejected`
- `internal/commands/commands_test.go:3925` 起 `TestRemovedLifePrefixIsNotHandledOrLogged`（函数到下一个 `func Test` 前结束）
- `internal/commands/capability_registry_test.go:117-123` `TestRemovedCommandFormsAreNotAccepted`（注意：这是该文件最后一个测试，删除后检查文件尾部没有残留）

- [ ] **Step 2: 跑测试确认剩余测试仍通过**

Run: `cd Bot && go test ./internal/routing/ ./internal/commands/ 2>&1 | tail -5`
Expected: PASS（这些测试是自包含的删除，不影响其他测试）

- [ ] **Step 3: 删除防御代码**

`internal/commands/commands.go`：
- 删 `parseResultSingle` 中的调用点（:301-303）：

```go
	if HasRemovedCommandPrefix(raw) {
		return ParseResult{Status: ParseStatusUnknown}
	}
```

- 删整个函数（:364-371）：

```go
// HasRemovedCommandPrefix identifies command paths that no longer exist. The
// router uses the same boundary as the parser so rejected slash commands can
// never fall through to natural-language handling or the Agent.
func HasRemovedCommandPrefix(text string) bool {
	raw := strings.TrimSpace(stripCQCodes(text))
	fields := strings.Fields(raw)
	return len(fields) > 0 && strings.HasPrefix(strings.ToLower(fields[0]), "/life")
}
```

`internal/routing/router.go`：删 `Decide` 开头（:38-40）：

```go
	if commands.HasRemovedCommandPrefix(inbound.Text) {
		return Decision{Action: ActionIgnore}
	}
```

- [ ] **Step 4: 修 feedback 帮助文本**

`internal/commands/commands.go:1195`：`fb` 不是注册命令 form，帮助里误导用户。把：

```go
			"fb 这里写你的建议",
```

改为：

```go
			"反馈 这里写你的建议",
```

- [ ] **Step 5: 构建并跑受影响包的测试**

Run: `cd Bot && go build ./... && go test ./internal/routing/ ./internal/commands/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd Bot && git add -A && git commit -m "commands: drop removed-command prefix guard and its tests"
```

---

### Task 2: 新日程命令 agenda.go + calendar descriptor 改造

**Files:**
- Create: `internal/commands/agenda.go`
- Create: `internal/commands/agenda_test.go`
- Modify: `internal/commands/capability_registry.go:459-461`（calendar descriptor：forms 删 `ddl`、executor 改 `h.agenda`、helpMeta 重写）
- Modify: `internal/commands/commands.go:480-490`（`case "日程"` 层级解析重写）
- Modify: `internal/commands/young_community.go:21-67`（删 `personalCalendar`；`formatCalendarInstant` :69-74 保留在原地或随 agenda 挪动——保留原地，agenda.go 直接调用）
- Modify: `internal/commands/response.go:153-155`（calendar 图片标题 "今日安排"→"日程"）
- Modify: `internal/responses/output.go:40-41`（`calendar` 的文本卡标题从 "课表" 拆出为 "日程"）
- Test: `internal/commands/commands_test.go:264` 与 `:969`（帮助文案断言）、`:3618-3621`（层级解析断言）
- Test: `internal/commands/young_community_test.go:108-126`（删 `TestPersonalCalendarUsesCompleteRESTAndKeepsYoungType`，由 agenda_test.go 新测试覆盖）

**Interfaces:**
- Consumes: `parseScheduleDateToken(value string, base time.Time) (time.Time, bool)`（commands.go:834）；`formatCalendarInstant`（young_community.go:69）；`h.accessToken`/`h.loginRequired`/`h.invalidInput`/`h.commandError`/`h.markData`；`auth.WithRefresh`；`h.Life.ListAllPersonalCalendarEvents(ctx, token, dateFrom, dateTo)` 返回 `[]life.PersonalCalendarEvent`（字段：ID/Type/At/EndsAt/Title/Location/URL/YoungID，life/young_community.go:399）；`h.Life.CurrentSubscription(ctx, token)` 返回 `map[string]any`；`lifedata.NestedString(data, "subscription", "calendarUrl")`；`formatNumberedLine(i int, text string) string`；`textutil.FirstNonEmpty`、`textutil.MonospaceDigits`、`lifedata.ChinaLocation()`、`chinaNow()`。
- Produces: `func (h Handler) agenda(ctx context.Context, ident store.Identity, args []string) string`（calendar descriptor 的 executor）；`func agendaDate(args []string, now time.Time) (time.Time, bool)`；`func formatAgenda(date time.Time, events []life.PersonalCalendarEvent) string`；`var agendaRandFloat = rand.Float64`（测试注入点）；`const agendaCalendarLinkProbability = 1.0 / 16`。

- [ ] **Step 1: 写失败测试 agenda_test.go**

创建 `internal/commands/agenda_test.go`：

```go
package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
)

func withAgendaRand(t *testing.T, value float64) {
	t.Helper()
	old := agendaRandFloat
	agendaRandFloat = func() float64 { return value }
	t.Cleanup(func() { agendaRandFloat = old })
}

func TestAgendaDateParsesSupportedForms(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, lifedata.ChinaLocation())
	tests := []struct {
		args []string
		want string
		ok   bool
	}{
		{nil, "2026-09-18", true},
		{[]string{"今天"}, "2026-09-18", true},
		{[]string{"今日"}, "2026-09-18", true},
		{[]string{"明天"}, "2026-09-19", true},
		{[]string{"后天"}, "2026-09-20", true},
		{[]string{"9-20"}, "2026-09-20", true},
		{[]string{"9月20日"}, "2026-09-20", true},
		{[]string{"2026-09-20"}, "2026-09-20", true},
		{[]string{"概览"}, "", false},
		{[]string{"9-20", "多余"}, "", false},
	}
	for _, test := range tests {
		got, ok := agendaDate(test.args, base)
		if ok != test.ok {
			t.Fatalf("agendaDate(%v) ok = %v, want %v", test.args, ok, test.ok)
		}
		if ok && got.Format("2006-01-02") != test.want {
			t.Fatalf("agendaDate(%v) = %s, want %s", test.args, got.Format("2006-01-02"), test.want)
		}
	}
}

func TestAgendaGroupsAllEventTypes(t *testing.T) {
	withAgendaRand(t, 0.99) // 不触发链接追加
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("dateFrom") == "" || q.Get("dateFrom") != q.Get("dateTo") || q.Get("pageSize") != "100" {
			t.Fatalf("query = %s", q.Encode())
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"s1","type":"schedule","at":"2026-09-18T09:50:00+08:00","endsAt":"2026-09-18T11:25:00+08:00","title":"计算机导论","location":"3A101"},
			{"id":"e1","type":"exam","at":"2026-09-18T14:30:00+08:00","endsAt":"2026-09-18T16:30:00+08:00","title":"数学分析","location":"GT-B112"},
			{"id":"h1","type":"homework_due","at":"2026-09-18T23:59:00+08:00","title":"作业一"},
			{"id":"t1","type":"todo_due","at":"2026-09-18T18:00:00+08:00","title":"写报告"},
			{"id":"y1","type":"young_event","at":"2026-09-18T10:00:00+08:00","title":"讲座","location":"东区"}
		],"pagination":{"page":1,"pageSize":100,"total":5,"totalPages":1}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程 今天", Identity: ident})
	if !ok {
		t.Fatalf("not handled: %q", reply)
	}
	for _, want := range []string{"课程 (1)：", "计算机导论", "09:50", "3A101", "考试 (1)：", "数学分析", "作业截止 (1)：", "作业一", "待办截止 (1)：", "写报告", "第二课堂 (1)：", "讲座", "东区"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(reply, "youngId") || strings.Contains(reply, "日历订阅链接") {
		t.Fatalf("unexpected content: %q", reply)
	}
	// 分组顺序：课程在考试前，考试在作业截止前
	if strings.Index(reply, "课程 (1)：") > strings.Index(reply, "考试 (1)：") ||
		strings.Index(reply, "考试 (1)：") > strings.Index(reply, "作业截止 (1)：") {
		t.Fatalf("group order wrong: %q", reply)
	}
}

func TestAgendaEmptyDay(t *testing.T) {
	withAgendaRand(t, 0) // 即使掷中也不应追加链接
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/calendar/events" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"page":1,"pageSize":100,"total":0,"totalPages":0}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程", Identity: ident})
	if !ok || !strings.Contains(reply, "暂无安排") || strings.Contains(reply, "日历订阅链接") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgendaAppendsCalendarLinkWhenDiceHits(t *testing.T) {
	withAgendaRand(t, 0) // 必中（0 < 1/16）
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspace/calendar/events":
			_, _ = w.Write([]byte(`{"data":[{"id":"t1","type":"todo_due","at":"2026-09-18T18:00:00+08:00","title":"写报告"}],"pagination":{"page":1,"pageSize":100,"total":1,"totalPages":1}}`))
		case "/api/workspace/subscriptions/current":
			_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"https://life.example/ical/test.ics"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程", Identity: ident})
	if !ok || !strings.Contains(reply, "写报告") || !strings.Contains(reply, "日历订阅链接：https://life.example/ical/test.ics") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}

func TestAgendaLinkCommandShowsSubscriptionLink(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspace/subscriptions/current" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"subscription":{"calendarUrl":"https://life.example/ical/test.ics"}}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	reply, ok := handler.Handle(ctx, Input{Text: "日程 链接", Identity: ident})
	if !ok || !strings.Contains(reply, "https://life.example/ical/test.ics") {
		t.Fatalf("reply = %q, ok = %v", reply, ok)
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `cd Bot && go test ./internal/commands/ -run TestAgenda -v 2>&1 | tail -5`
Expected: FAIL 编译错误（`agendaRandFloat`/`agendaDate` 未定义）

- [ ] **Step 3: 实现 agenda.go**

创建 `internal/commands/agenda.go`：

```go
package commands

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const agendaCalendarLinkProbability = 1.0 / 16

var agendaRandFloat = rand.Float64

func (h Handler) agenda(ctx context.Context, ident store.Identity, args []string) string {
	date, ok := agendaDate(args, chinaNow())
	if !ok {
		return h.invalidInput("日期格式看不懂，试试：日程 / 日程 明天 / 日程 9-20 / 日程 2026-09-20")
	}
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	dateString := date.In(lifedata.ChinaLocation()).Format("2006-01-02")
	events, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]life.PersonalCalendarEvent, error) {
		return h.Life.ListAllPersonalCalendarEvents(ctx, token, dateString, dateString)
	})
	if err != nil {
		return h.commandError("日程查不到：", err)
	}
	h.markData(map[string]any{"operation": "personal_calendar", "date": dateString, "events": events})
	reply := formatAgenda(date, events)
	if len(events) > 0 && agendaRandFloat() < agendaCalendarLinkProbability {
		if url := h.agendaCalendarURL(ctx, ident); url != "" {
			reply += "\n\n日历订阅链接：" + url
		}
	}
	return reply
}

// agendaCalendarURL 静默取当前用户的 iCalendar 链接；任何失败都不影响主回复。
func (h Handler) agendaCalendarURL(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return ""
	}
	data, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) (map[string]any, error) {
		return h.Life.CurrentSubscription(ctx, token)
	})
	if err != nil {
		return ""
	}
	return lifedata.NestedString(data, "subscription", "calendarUrl")
}

func agendaDate(args []string, now time.Time) (time.Time, bool) {
	loc := lifedata.ChinaLocation()
	now = now.In(loc)
	if len(args) == 0 {
		return now, true
	}
	if len(args) != 1 {
		return time.Time{}, false
	}
	switch commandToken(args[0]) {
	case "今天", "今日", "today":
		return now, true
	case "明天", "明日", "tomorrow":
		return now.AddDate(0, 0, 1), true
	case "后天":
		return now.AddDate(0, 0, 2), true
	}
	return parseScheduleDateToken(args[0], now)
}

var agendaGroupOrder = []struct {
	Type  string
	Label string
}{
	{"schedule", "课程"},
	{"exam", "考试"},
	{"homework_due", "作业截止"},
	{"todo_due", "待办截止"},
	{"young_event", "第二课堂"},
}

func formatAgenda(date time.Time, events []life.PersonalCalendarEvent) string {
	title := textutil.MonospaceDigits(date.In(lifedata.ChinaLocation()).Format("01-02")) + " 日程："
	if len(events) == 0 {
		return title + "\n暂无安排。"
	}
	grouped := make(map[string][]life.PersonalCalendarEvent, len(agendaGroupOrder))
	var extra []life.PersonalCalendarEvent
	for _, event := range events {
		known := false
		for _, group := range agendaGroupOrder {
			if event.Type == group.Type {
				known = true
				break
			}
		}
		if known {
			grouped[event.Type] = append(grouped[event.Type], event)
		} else {
			extra = append(extra, event)
		}
	}
	lines := []string{title}
	appendGroup := func(label string, items []life.PersonalCalendarEvent) {
		if len(items) == 0 {
			return
		}
		lines = append(lines, "", fmt.Sprintf("%s (%d)：", label, len(items)))
		for i, event := range items {
			lines = append(lines, formatNumberedLine(i+1, formatAgendaEvent(event)))
		}
	}
	for _, group := range agendaGroupOrder {
		appendGroup(group.Label, grouped[group.Type])
	}
	appendGroup("其他", extra)
	return strings.Join(lines, "\n")
}

func formatAgendaEvent(event life.PersonalCalendarEvent) string {
	title := textutil.FirstNonEmpty(event.Title, "未命名安排")
	line := title
	if event.At != "" {
		line = formatAgendaInstant(event.At) + " " + line
	}
	if event.EndsAt != "" {
		line += " ~ " + formatAgendaInstant(event.EndsAt)
	}
	if event.Location != "" {
		line += " · " + event.Location
	}
	return line
}

func formatAgendaInstant(value string) string {
	if parsed, ok := lifedata.ParseAPITime(value); ok {
		return parsed.In(lifedata.ChinaLocation()).Format("15:04")
	}
	return strings.TrimSpace(value)
}
```

- [ ] **Step 4: 接线——descriptor、层级解析、删旧实现**

`internal/commands/capability_registry.go:459-461`，calendar descriptor 替换为（删 `ddl` form、executor 改 `h.agenda`、帮助重写）：

```go
		descriptor(CapabilityCalendar, []string{"calendar", "日程", "今日"}, CapabilityRequirements{Life: true, OAuth: true, DataScope: DataScopeUserPrivate}, EffectRead, ExposureModel, allowArgs, nil, func(h Handler, ctx context.Context, ident store.Identity, args []string) string {
			return h.agenda(ctx, ident, args)
		}, func(inv Invocation) CapabilityPolicy { return readPolicy(inv, DataScopeUserPrivate) }, helpMeta("agenda", "日程", "查看某一天的课程、考试、作业、待办与第二课堂安排", true, []HelpExample{example("日程", "查看今天的全部安排"), example("日程 明天", "查看明天的安排"), example("日程 9-20", "查看指定日期的安排"), example("日程 链接", "获取 iCalendar 日历订阅链接")}, []HelpExample{example("今日", "相当于“日程”")})),
```

`internal/commands/commands.go:480-490`，`case "日程"` 替换为：

```go
	case "日程":
		switch action {
		case "链接", "日历":
			return "subscription", []string{"link"}, true
		}
		return "calendar", args, true
```

（裸 `日程` → `calendar` 无参数 = 今天；`日程 帮助` 由前面的 `isHelpToken` 分支处理到 `help("日程")`，`helpTopicAliases` 已有 `"日程": "agenda"`，无需改动。）

`internal/commands/young_community.go`：删 `personalCalendar` 函数（:21-67）。保留 `formatCalendarInstant`（:69-74，agenda.go 未用它，但它仍被 `youngCalendar` 使用——先 grep 确认 `formatCalendarInstant` 的其他调用点，若无其他调用则一并删除）。

`internal/commands/response.go:153-155`：`imageTitle(plainText, "今日安排")` → `imageTitle(plainText, "日程")`。

`internal/commands/../responses/output.go:40-41`：把

```go
	case "schedule", "nextclass", "calendar":
		return "课表"
```

改为：

```go
	case "schedule", "nextclass":
		return "课表"
	case "calendar":
		return "日程"
```

- [ ] **Step 5: 更新被破坏的旧断言**

`internal/commands/commands_test.go:264`：
`"日程\t今日安排、综合概览与近期截止"` → `"日程\t查看某一天的课程、考试、作业、待办与第二课堂安排"`

`internal/commands/commands_test.go:969`：
`"| 日程 | 今日安排、综合概览与近期截止 |"` → `"| 日程 | 查看某一天的课程、考试、作业、待办与第二课堂安排 |"`

`internal/commands/commands_test.go:3618-3621`（`TestCanonicalCommandHierarchy` 的四行）替换为：

```go
		{text: "日程", name: "calendar"},
		{text: "日程 今日", name: "calendar", args: "今日"},
		{text: "日程 明天", name: "calendar", args: "明天"},
		{text: "日程 链接", name: "subscription", args: "link"},
```

`internal/commands/young_community_test.go`：删 `TestPersonalCalendarUsesCompleteRESTAndKeepsYoungType`（:108-126，已由 `TestAgendaGroupsAllEventTypes` 覆盖）。

- [ ] **Step 6: 跑 commands 包全部测试**

Run: `cd Bot && go test ./internal/commands/ ./internal/responses/`
Expected: PASS（若 `概览`/`近期截止` 相关测试此时失败，属预期——它们在 Task 3 删除，但本任务必须先保证除这些外的测试全绿；若失败名单超出预期范围，停下来修）

注意：`TestHandleDashboard`/`TestHandleUpcomingDeadlines`/`TestHandleOverviewCombinesPersonalData` 此时仍应通过（旧实现未删）。若它们挂了说明误伤了共享 helper。

- [ ] **Step 7: Commit**

```bash
cd Bot && git add -A && git commit -m "commands: replace personal calendar with grouped day agenda"
```

---

### Task 3: 删除 overview / upcoming_deadlines 全链路 + 旧 job 兼容

**Files:**
- Modify: `internal/commands/capability_registry.go:49-50`（删常量）、`:611-625`（删两个 descriptor）、`RestoreInvocation` :245-255（加旧 ID 映射）
- Modify: `internal/commands/commands.go`（删 `overview` :2098-2135、`formatOverview` :2137-2151、`appendOverviewSection` :2153-2166、`dueSoonHomeworks` :2168-2181、`myDashboard` :4337-4350、`upcomingDeadlines` :4352-4371、`formatDashboard` :4373-4407、`appendListOverflow` :4409-4415、`dashboardItemSlice`/`dashboardItemTotal` :4323-4335）
- Modify: `internal/commands/capability_invocation.go:441-444`（删 receipt 分支）
- Modify: `internal/commands/response.go:156-161`（删 `overview`/`upcoming_deadlines` 图片分支）
- Modify: `internal/responses/render.go:874-893`（删 `overview`/`dashboard`/`deadlines` 主题分支）
- Modify: `internal/responses/output.go:48-49`（删 `"overview"` 标题分支）
- Modify: `internal/life/client.go:321-347`（删 `GetMyDashboard`/`GetUpcomingDeadlines`/`GetUpcomingDeadlinesAt`）
- Modify: `internal/botapp/coordinator.go:493`（持久化命令校验改用 Name+args 重建）
- Test: `internal/commands/commands_test.go`（删 `TestHandleOverviewCombinesPersonalData` :2500、`TestFormatDashboard` :4285、`TestFormatDashboardUsesTotalsAndPointsToFullLists` :4322、`TestFormatOverviewPointsToCompleteList` :4343、`TestCalendarSubscriptionHintAppearsForScheduleResults` :4359、`TestHandleDashboard` :4373、`TestHandleUpcomingDeadlines` :4391；`TestRichTextImageMarksSectionHeadings` :1219 kind `"overview"` → `"calendar"`）
- Test: `internal/commands/capability_registry_test.go:50`（`len(seen) < 35` → `< 33`）并新增 `RestoreInvocation` 旧 ID 映射测试

**Interfaces:**
- Consumes: Task 2 的 `agenda`（旧 job 恢复后的实际执行入口）。
- Produces: `RestoreInvocation(id CapabilityID, args []string) (Invocation, bool)` 行为变更——`"overview"`/`"upcoming_deadlines"` 映射到 `calendar` descriptor，`Invocation.Name` 保留持久化原名，`Args` 置空。

- [ ] **Step 1: 写失败测试——旧 ID 恢复映射**

在 `internal/commands/capability_registry_test.go` 追加：

```go
func TestRestoreInvocationMapsRemovedAgendaCapabilities(t *testing.T) {
	for _, legacy := range []string{"overview", "upcoming_deadlines"} {
		invocation, ok := RestoreInvocation(CapabilityID(legacy), []string{"14"})
		if !ok {
			t.Fatalf("RestoreInvocation(%q) not restored", legacy)
		}
		if invocation.ID() != CapabilityCalendar {
			t.Fatalf("RestoreInvocation(%q) ID = %q, want %q", legacy, invocation.ID(), CapabilityCalendar)
		}
		if invocation.Name != legacy {
			t.Fatalf("RestoreInvocation(%q) Name = %q, want persisted name", legacy, invocation.Name)
		}
		if len(invocation.Args) != 0 {
			t.Fatalf("RestoreInvocation(%q) Args = %#v, want dropped", legacy, invocation.Args)
		}
	}
	if _, ok := RestoreInvocation("no_such_capability", nil); ok {
		t.Fatal("unknown capability unexpectedly restored")
	}
}
```

Run: `cd Bot && go test ./internal/commands/ -run TestRestoreInvocationMaps -v`
Expected: FAIL（`overview` 恢复后 ID 仍是 overview——映射尚未实现）

- [ ] **Step 2: 实现旧 ID 映射 + coordinator 校验修正**

`internal/commands/capability_registry.go`，`RestoreInvocation`（:245-255）替换为：

```go
// legacyCapabilityIDMap keeps persisted command jobs executable after their
// capability was folded into another one.
var legacyCapabilityIDMap = map[string]CapabilityID{
	"overview":           CapabilityCalendar,
	"upcoming_deadlines": CapabilityCalendar,
}

// RestoreInvocation reconstructs the exact normalized invocation persisted by
// the router. It intentionally does not validate or renormalize arguments;
// Handler owns the final validity and policy checks at execution time.
func RestoreInvocation(id CapabilityID, args []string) (Invocation, bool) {
	descriptor, ok := descriptorForID(string(id))
	if !ok {
		mapped, legacy := legacyCapabilityIDMap[string(id)]
		if !legacy {
			return Invocation{}, false
		}
		descriptor, ok = descriptorForID(string(mapped))
		if !ok {
			return Invocation{}, false
		}
		args = nil
	}
	return Invocation{
		Capability: descriptor,
		Name:       string(id),
		Args:       append([]string(nil), args...),
	}, true
}
```

注意：`Name` 从原来的 `string(descriptor.ID)` 改为 `string(id)`（保留持久化原名）。grep 确认 `RestoreInvocation` 只有 `botapp/coordinator.go:488` 一个调用点，无其他依赖 Name==ID 的代码。

`internal/botapp/coordinator.go:493`，把：

```go
		if strings.TrimSpace(job.Invocation.Command) != invocation.CanonicalCommand() {
```

改为（持久化时 `Command` 本来就是 `Name + " " + args` 拼接，见 coordinator.go:333-337，用持久化字段重建可对旧 ID 兼容）：

```go
		persisted := strings.TrimSpace(strings.TrimSpace(job.Invocation.Name) + " " + strings.Join(job.Invocation.Args, " "))
		if strings.TrimSpace(job.Invocation.Command) != persisted {
```

- [ ] **Step 3: 删 capability 常量与 descriptor**

`internal/commands/capability_registry.go`：
- 删 :49-50 两行常量 `CapabilityOverview` / `CapabilityUpcomingDeadlines`
- 删 :611-625 的两个 descriptor（`CapabilityOverview` 与 `CapabilityUpcomingDeadlines` 条目，到 :626 的 `}` 前）

`internal/commands/capability_invocation.go:441-444`，把：

```go
	case CapabilityCalendar, CapabilityOverview:
		return query("日程")
	case CapabilityUpcomingDeadlines:
		return query("截止事项")
```

改为：

```go
	case CapabilityCalendar:
		return query("日程")
```

- [ ] **Step 4: 删 handler、formatter、life client、图片/主题分支**

`internal/commands/commands.go` 删（行号以当前文件为准，删除顺序建议从后往前避免行号漂移）：
- `formatDashboard`（:4373-4407）、`appendListOverflow`（:4409-4415——先 grep 确认无其他调用点，已知仅 formatOverview 系的 `appendOverviewSection` 和 formatDashboard 使用）
- `myDashboard`（:4337-4350）、`upcomingDeadlines`（:4352-4371）
- `dashboardItemSlice`/`dashboardItemTotal`（:4323-3335）
- `overview`（:2098-2135）、`formatOverview`（:2137-2151）、`appendOverviewSection`（:2153-2166）、`dueSoonHomeworks`（:2168-2181）

注意保留：`withCalendarSubscriptionHint`（课表/考试命令在用，commands.go:2857/2997/3194/3278/4232）、`schedulesForDay`/`pendingTodos`/`homeworks`/`subscriptionExams`/`upcomingSubscriptionExams`（exam/schedule 命令在用）。删完后 `go build ./...` 会报出任何残留引用，逐一处理。

`internal/commands/response.go`：删 :156-161 的 `case "overview":` 与 `case "upcoming_deadlines":` 两个分支。

`internal/responses/render.go`：删 `case "overview":`（:874-878）、`case "dashboard":`（:879-883）、`case "deadlines":`（:889-893）三个主题分支。`internal/responses/richtext_test.go:120` 的 `NewRichTextImage("dashboard", ...)` 只是普通 kind 字符串、不断言主题，不用动。

`internal/responses/output.go`：删 :48-49 `case "overview": return "概览"`。

`internal/life/client.go`：删 `GetMyDashboard`（:321-327）、`GetUpcomingDeadlines`（:329-331）、`GetUpcomingDeadlinesAt`（:333-347）。删后 grep `GetMyDashboard|GetUpcomingDeadlines` 确认无残留引用；`int64Ptr` 若无人使用一并删（grep 确认）。

- [ ] **Step 5: 删/改旧测试**

`internal/commands/commands_test.go` 删除以下测试函数：
- `TestHandleOverviewCombinesPersonalData`（:2500-2537）
- `TestFormatDashboard`（:4285-4320）
- `TestFormatDashboardUsesTotalsAndPointsToFullLists`（:4322-4341）
- `TestFormatOverviewPointsToCompleteList`（:4343-4357）
- `TestCalendarSubscriptionHintAppearsForScheduleResults`（:4359-4371——它测的是 `formatOverview` 的 hint；`withCalendarSubscriptionHint` 本身由课表/考试路径的既有测试覆盖）
- `TestHandleDashboard`（:4373-4389）
- `TestHandleUpcomingDeadlines`（:4391-4411）

修改 `TestRichTextImageMarksSectionHeadings`（:1219）：`richTextImage("overview", ...)` → `richTextImage("calendar", ...)`（该测试测的是通用小节标题 markdown 转换，与 overview 无关）。

`internal/commands/capability_registry_test.go:50`：`len(seen) < 35` → `len(seen) < 33`（删了 2 个 capability）。

- [ ] **Step 6: 全量构建与测试**

Run: `cd Bot && gofmt -l internal/ && go build ./... && go test ./...`
Expected: gofmt 无输出；build 与全部测试 PASS

- [ ] **Step 7: Commit**

```bash
cd Bot && git add -A && git commit -m "commands: remove overview and upcoming_deadlines capabilities"
```

---

### Task 4: 最终验证

**Files:** 无新增改动。

- [ ] **Step 1: 全量检查**

Run: `cd Bot && gofmt -l . && go vet ./... && go build ./... && go test ./...`
Expected: 全绿

- [ ] **Step 2: 人工确认删除清单无残留**

Run:

```bash
cd Bot && grep -rn "HasRemovedCommandPrefix\|myDashboard\|upcomingDeadlines\|formatDashboard\|formatOverview\|personalCalendar\|GetMyDashboard\|GetUpcomingDeadlines\|CapabilityOverview\|CapabilityUpcomingDeadlines" internal/ || echo "clean"
grep -rn '"ddl"' internal/commands/ || echo "no ddl form"
```

Expected: 两行都输出 clean / no ddl form（`agent/lazy_mcp.go:366` 的 `"ddl"` 字符串是工具关键词，保留，不在 `internal/commands/` 下）

- [ ] **Step 3: 对照 spec 验收**

逐项核对 `docs/superpowers/specs/2026-09-18-agenda-command-simplification-design.md`：
- `日程 [日期]` 五类分组渲染 ✓（Task 2）
- `日程 链接` ✓（Task 2）
- 1/16 追加链接，取不到不影响主回复 ✓（Task 2）
- forms 删 `ddl` ✓（Task 2）
- 4 个 removal 测试 + `HasRemovedCommandPrefix` 删除 ✓（Task 1）
- overview/upcoming_deadlines 全链路删除 ✓（Task 3）
- 旧 job `RestoreInvocation` 映射 ✓（Task 3）
- 群规则不变（router 其余测试原样通过）✓（Task 1/3 测试）

- [ ] **Step 4: 报告**

向用户汇总改动与测试结果，提示可用 `./scripts/deploy-mac.sh` 部署（要求 git 干净），由用户决定何时部署。
