# 日程命令简化设计

日期：2026-09-18
状态：已获用户确认（概率 1/16，删除 ddl 别名）

## 背景

Bot 的日程/日历区域有 4 个 capability（calendar、overview、upcoming_deadlines、subscription link）加 1 条自然语言路由（calendar_link），同一概念有 3~4 个入口：

- `calendar`（别名 calendar/日程/今日/ddl）→ `personalCalendar`，实现住在 `internal/commands/young_community.go`，复用了第二课堂的解析器，导致个人日程接受无意义的"活动/报名"基准参数。
- `overview`（概览）与 `upcoming_deadlines`（近期截止）共用 `formatDashboard`，几乎是同一个东西。
- "日程"裸发只显示帮助，本身什么都不做。
- 自然语言路由名 `calendar_link` 与 `calendar` 命令无关，命名易混。

另外仓库里残留针对已删除功能的防御代码与测试：`HasRemovedCommandPrefix`（/life 前缀静默忽略）及 4 个"已删除功能确实被删了"的测试。

关键事实：服务端 `/api/workspace/calendar/events` 已聚合全部个人日程类型——`schedule`（课程）、`exam`（考试）、`homework_due`（作业截止）、`todo_due`（待办截止）、`young_event`（第二课堂）（`server/src/features/calendar/server/personal-calendar-page.ts`）。Bot 端一次调用即可覆盖"用户需要关心的所有日程"。

## 目标

1. 日程家族只保留两条用法：`日程 [日期]` 和 `日程 链接`。
2. `日程` 回复以 1/16 概率在尾部追加用户的 iCalendar 订阅链接。
3. 删除已删除功能的测试与防御代码。

## 命令面（私聊，全部 user_private）

- `日程` / `日程 今天` / `日程 明天` / `日程 后天` / `日程 9-20` / `日程 2026-09-20` / `日程 9月20日`
  → 一次 `ListAllPersonalCalendarEvents(date, date)`，按类型分组渲染：课程 / 考试 / 作业截止 / 待办截止 / 第二课堂，各带时间与地点；空组不显示；全空回"暂无安排"。
  - 日期解析复用现有 `parseScheduleDateToken`；空参数 = 今天。
  - Forms 保留：`日程`、`calendar`、`今日`；删除 `ddl`。
  - 砍掉借自二课解析器的"日/周/月/活动/报名"参数。
- `日程 链接` → 现有 iCalendar 订阅链接（`订阅 链接` 与自然语言路由"给我日历链接"等原样保留）。

## 随机追加日历链接

- 触发条件：`日程` 回复非空（即当天有至少一项安排）且为私聊（该命令本来就只在私聊可用）。
- 概率：1/16。实现为常量 `agendaCalendarLinkProbability = 1.0 / 16`，随机源可注入，测试用确定性随机源覆盖两种分支。
- 内容：尾部追加"日历订阅链接：<当前用户的 calendarUrl>"，取数复用 `subscriptionCalendarLink` 的现有逻辑（`CurrentSubscription` → `subscription.calendarUrl`）；取不到链接或登录失效时不追加、不影响主回复。

## 删除清单

命令与实现：

- capability `overview`、`upcoming_deadlines` 及其 Forms、帮助项；层级命令中的 `日程 概览`、`日程 截止`。
- 死代码：`myDashboard`、`upcomingDeadlines`、`formatDashboard`（仅这两处使用）；`personalCalendar` 以新实现替代并从 `young_community.go` 挪入 `commands.go`（或新文件 `agenda.go`）；Bot 侧不再调用 `GetMyDashboard`、`GetUpcomingDeadlines`（服务端端点不动，其他客户端可能仍在用）。
- `今日`/`ddl` 中原先指向 calendar 的 `ddl` 别名。

已删功能的防御与测试：

- `HasRemovedCommandPrefix`（`internal/commands/commands.go`、`internal/routing/router.go` 两处调用一并移除）。删除后 `/life xxx` 在私聊落到 agent，群内未 @ 仍静默忽略（群规则不受影响）。
- 测试：`internal/routing/router_test.go` `TestRemovedLifePrefixNeverFallsThroughToAgentOrNaturalRoutes`、`internal/commands/commands_test.go` `TestLifePrefixIsRejected` 与 `TestRemovedLifePrefixIsNotHandledOrLogged`、`internal/commands/capability_registry_test.go` `TestRemovedCommandFormsAreNotAccepted`。

## 兼容性

- `RestoreInvocation` 增加旧 ID 映射：`overview`、`upcoming_deadlines` → `calendar`（参数丢弃，按今日日程执行），队列中未过期的旧 command job 可正常恢复。
- agent 的命令手册与 `search_bot_commands` 从注册表生成，随 descriptor 变更自动更新，无需额外改动。
- `Invocation.NaturalRoute` 的 `calendar_link` 标识保持不变（内部名，对外不可见）。

## 不变约束（群内规则）

- 日程家族全部 user_private，群内永远不响应。
- 群内公共命令仍需精确匹配才回复；自然语言路由必须 @ 或回复 Bot 才生效。现有 router 测试继续覆盖。

## 测试

- 新增/改写 `日程` 命令测试：日期解析各形式、分组渲染（五类事件齐全/部分缺失/全空）、1/16 追加链接两个分支（注入随机源）、`日程 链接`。
- 删除上述 4 个 removal 测试。
- router 现有群聊激活规则测试不动。
