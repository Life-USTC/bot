# Life @ USTC Bot

Go chat client for [Life@USTC](https://life-ustc.tiankaima.dev). Speaks OneBot 12
(HTTP + NapCat reverse WebSocket) and the QQ official bot gateway. All product
data comes from the **server** (`LIFE_USTC_SERVER`); domain nouns follow
[server interface hierarchy](https://github.com/Life-USTC/server/blob/main/docs/interface-hierarchy.md).

## Commands

Prefix defaults to `/life`. Chinese names match the shared domains:

```text
校园信息    学期 · 课程 · 教学班 · 老师 · 校车
我的工作区  日程 · 课表 · 下一节课 · 待办[+添加] · 作业 · 考试 · 订阅
社区        反馈 <建议>
账户与设置  账户 · 登录[/状态] · 退出 · 设置[通知…] · 设置 AI 工具
系统        状态 · 帮助
```

Aliases (`td`, `hw`, `xc`, `kb`, `ddl`, `status`, …) are shortcuts, not domains.
`日程` aggregates classes/homework/exams/todos; `课表` is schedules only.

OneBot 12 extension actions use `life_ustc.<capability_id>` (e.g.
`life_ustc.workspace_todo_list`). See `--help` / source for the full parameter set.

`/life login` starts OAuth device-code login; `/life login status` finishes and
stores the token in SQLite. Tokens refresh automatically; failed refresh clears
credentials. `/life 设置 通知` (alias `/life 通知`) toggles class/homework pushes.

## Run

```bash
go run ./cmd/life-ustc-bot
go test ./...
```

| Variable | Purpose |
|----------|---------|
| `LIFE_USTC_SERVER` | Server base URL (e.g. `http://localhost:3000`) |
| `BOT_COMMAND_PREFIX` | Default `/life` |
| `BOT_DB_PATH` | SQLite path (default `.run/life-ustc-bot.db`) |
| `BOT_ONEBOT_HTTP_HOST` / `PORT` | OneBot 12 HTTP listen |
| `BOT_ONEBOT_ACCESS_TOKEN` | Optional OneBot access token |
| `NAPCAT_REVERSE_ADDR` / `PATH` | NapCat reverse WS (Compose: `0.0.0.0:2280`, `/ws`) |
| `NAPCAT_WS_URL` | Optional outbound NapCat WS instead of reverse listen |
| `QQ_BOT_APPID` + `QQ_BOT_APPSECRET` or `QQ_BOT_TOKEN` | Enable QQ official bot |
| `QQ_BOT_WEBHOOK_ADDR` / `PATH` | Preferred QQ event callback (`/qqbot`) |
| `BOT_ENABLE_IMAGE_RESPONSES` + `BOT_PUBLIC_BASE_URL` | PNG cards via `/media/*` → `BOT_MEDIA_ADDR` |
| `BOT_ENABLE_AGENT` + `OPENAI_API_KEY` | Optional private-chat LLM agent |
| `PREMIUM_MODEL_API_KEY` | Optional premium model (admins in `BOT_FEEDBACK_ADMIN_USERS`) |
| `BOT_ALLOW_GROUP_PERSONAL_INFO` | Allow read-only personal commands in groups |
| `BOT_FEEDBACK_ADMIN_USERS` / `GROUPS` / `PLATFORM` | Feedback admin routing |
| `BOT_PUBLIC_COMMAND_CACHE_TTL_SECONDS` | Cache TTL for public reads (`BOT_BUILD_VERSION` in key) |

QQ default intents cover DM, group/@, and interaction events. Prefer webhook
callbacks to `http(s)://<public-host>/qqbot`; gateway may stay for diagnostics.

Image replies need `fonts-noto-cjk` (image sets this in Docker) or
`BOT_IMAGE_FONT_PATH`. Agent runs log tokens/cost in SQLite `agent_runs`.

## Deploy

`compose.yaml` + `scripts/deploy-cn.sh`. In-container binds:

```text
NAPCAT_REVERSE_ADDR=0.0.0.0:2280
BOT_MEDIA_ADDR=0.0.0.0:2281
QQ_BOT_WEBHOOK_ADDR=0.0.0.0:2290
```

Host ports default to `127.0.0.1`. Proxy `/media/*` → `:2281` and `/qqbot` → `:2290`.
