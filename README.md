# Life @ USTC Bot

Go bot framework for Life @ USTC.

It exposes a OneBot 12 HTTP implementation with
`github.com/botuniverse/go-libonebot` and includes a NapCat-compatible
OneBot 11 reverse WebSocket bridge. It can also connect directly to the QQ
official bot platform gateway.

## Commands

Message commands use `/life` by default. Their primary Chinese names follow the
same domain groups as the other Life @ USTC interfaces:

```text
校园信息
  /life 学期
  /life 课程 <关键词>
  /life 教学班 <关键词>
  /life 老师 <关键词>
  /life 校车

我的工作区
  /life 日程
  /life 课表
  /life 下一节课
  /life 待办
  /life 待办 添加 <标题>
  /life 作业
  /life 考试
  /life 订阅

社区
  /life 反馈 <建议>

账户与设置
  /life 账户
  /life 登录
  /life 登录 状态
  /life 退出
  /life 设置
  /life 设置 通知
  /life 设置 通知 课表 开
  /life 设置 通知 作业 开
  /life 设置 AI 工具

系统
  /life 状态
  /life 帮助
```

Short forms such as `td`, `hw`, `xc`, `kb`, `ddl`, and `status` are aliases,
not separate command domains. `日程` is the aggregate view of classes,
homework, exams, and todos; `课表` only shows class schedules.

OneBot 12 extension actions:

```text
life_ustc.catalog_semester_current
life_ustc.catalog_course_search       {"search":"calculus"}
life_ustc.catalog_section_search      {"search":"calculus"}
life_ustc.catalog_bus_timetable_get
life_ustc.catalog_link_list
life_ustc.account_login_begin         {"platform":"napcat","user_id":"123"}
life_ustc.account_login_poll          {"platform":"napcat","user_id":"123"}
life_ustc.account_profile_get         {"platform":"napcat","user_id":"123"}
life_ustc.workspace_todo_list         {"platform":"napcat","user_id":"123"}
life_ustc.workspace_link_pin_list     {"platform":"napcat","user_id":"123"}
life_ustc.workspace_link_pin_set      {"platform":"napcat","user_id":"123","slug":"jw","action":"pin"}
```

`/life login` starts an OAuth device-code login and replies with the
verification link and user code. After approving in the browser, send
`/life login status`; the bot persists the token for that chat user in SQLite.
Authenticated commands refresh tokens automatically when possible. Concurrent
requests for the same user share one refresh exchange so rotating refresh tokens
cannot be mistaken for replay. If the authorization server rejects a refresh
grant, the stale local credential is removed and the next command asks the user
to log in again.

`/life 设置 通知` manages private-chat active pushes. The shorter
`/life 通知` form remains an alias. Class reminders and homework reminders can
be enabled independently, and sent reminders are recorded in SQLite so the same
item is not pushed repeatedly.

## Run

```bash
go run ./cmd/life-ustc-bot
```

Useful environment variables:

```text
LIFE_USTC_SERVER=http://localhost:3000
BOT_ONEBOT_HTTP_HOST=127.0.0.1
BOT_ONEBOT_HTTP_PORT=6700
BOT_ONEBOT_ACCESS_TOKEN=
BOT_COMMAND_PREFIX=/life
BOT_DB_PATH=.run/life-ustc-bot.db
BOT_BUILD_VERSION=dev
BOT_PUBLIC_COMMAND_CACHE_TTL_SECONDS=300
BOT_ENABLE_AGENT=false
BOT_LLM_MODEL=gpt-4o-mini
BOT_LLM_TIMEOUT_SECONDS=60
OPENAI_API_KEY=
OPENAI_BASE_URL=
BOT_ENABLE_IMAGE_RESPONSES=false
BOT_PUBLIC_BASE_URL=
BOT_MEDIA_ADDR=127.0.0.1:2281
BOT_IMAGE_FONT_PATH=
BOT_MEDIA_TTL_SECONDS=300
BOT_ALLOW_GROUP_PERSONAL_INFO=false
BOT_FEEDBACK_ADMIN_PLATFORM=
BOT_FEEDBACK_ADMIN_USERS=
BOT_FEEDBACK_ADMIN_GROUPS=

# NapCat reverse WebSocket. The local container is configured for /ws on 2280.
NAPCAT_REVERSE_ADDR=0.0.0.0:2280
NAPCAT_REVERSE_PATH=/ws

# QQ official bot platform. Enabled automatically when QQ_BOT_APPID and either
# QQ_BOT_APPSECRET or QQ_BOT_TOKEN are present, or explicitly with
# BOT_ENABLE_QQ_BOT=true.
BOT_ENABLE_QQ_BOT=
QQ_BOT_APPID=
QQ_BOT_APPSECRET=
QQ_BOT_TOKEN=
QQ_BOT_ID=
QQ_BOT_INTENTS=1174409216
QQ_BOT_API_BASE_URL=https://api.sgroup.qq.com
QQ_BOT_TOKEN_URL=https://bots.qq.com/app/getAppAccessToken
QQ_BOT_GATEWAY_URL=
BOT_ENABLE_QQ_BOT_GATEWAY=true
BOT_ENABLE_QQ_BOT_WEBHOOK=
QQ_BOT_WEBHOOK_ADDR=0.0.0.0:2290
QQ_BOT_WEBHOOK_PATH=/qqbot
```

Public, user-independent read commands are cached in SQLite for the configured
TTL. `BOT_BUILD_VERSION` is part of every cache key, and `scripts/deploy-cn.sh`
sets it to the deployed Git revision so a new deployment cannot reuse results
from an older version.

If a NapCat WebSocket server is configured instead, set `NAPCAT_WS_URL` and the
bot will dial it. Otherwise it listens for NapCat reverse WebSocket connections.

For QQ official bot platform, the default intent is `1 << 12 | 1 << 25 | 1 << 26 | 1 << 30`
(`DIRECT_MESSAGE`, `GROUP_AND_C2C_EVENT`, `INTERACTION`, plus `PUBLIC_GUILD_MESSAGES`), which receives
`DIRECT_MESSAGE_CREATE`, `C2C_MESSAGE_CREATE`, `GROUP_AT_MESSAGE_CREATE`, `INTERACTION_CREATE`, and `AT_MESSAGE_CREATE`.
Replies use `/v2/users/{openid}/messages` for single chat,
`/v2/groups/{group_openid}/messages` for groups, and `/channels/{channel_id}/messages`
for channel @ messages.

Tencent's official Go SDK recommends webhook callbacks for new QQ official bot
event delivery. Configure the QQ bot platform callback URL to
`http(s)://<public-host>/qqbot`, select the message events you need, and make
sure the callback can reach `QQ_BOT_WEBHOOK_ADDR`. The websocket gateway can
remain enabled for diagnostics, but it is not the primary receive path.

Set `BOT_ENABLE_IMAGE_RESPONSES=true` plus `BOT_PUBLIC_BASE_URL=https://<public-host>`
to let schedule, next-class, todo list, homework list, exam list, overview,
dashboard, upcoming-deadline, bus timetable, and reminder replies send a
short-lived PNG image before falling back to text. Route `/media/*` from the
public host to `BOT_MEDIA_ADDR`. The runtime image installs
`fonts-noto-cjk` and `fonts-firacode`; set `BOT_IMAGE_FONT_PATH` only when
overriding the default Chinese font.

Set `BOT_ENABLE_AGENT=true` with `OPENAI_API_KEY` to enable the optional
LLM-backed private-chat assistant. The agent uses existing bot commands as
tools for curriculum, next class, homework, and shuttle bus queries. Group chats
still use deterministic command handling only. Increase `BOT_LLM_TIMEOUT_SECONDS`
for slower reasoning models; failed agent runs store the provider error in
SQLite `agent_runs.error`.

Set `PREMIUM_MODEL_API_KEY` to enable the OpenAI-compatible premium model.
`PREMIUM_MODEL_BASE_URL` defaults to `https://api.moonshot.cn/v1` and
`PREMIUM_MODEL` defaults to `kimi-k3`. IDs already listed in
`BOT_FEEDBACK_ADMIN_USERS` automatically use this model; all other users remain
on the default model. Premium users can send up to four PNG, JPEG, WebP, or GIF
images in a message for visual understanding.

Each agent run records its provider, model, prompt/cache/output token counts,
and cost in nanoyuan in SQLite `agent_runs`. `ConversationSpending` and
`UserSpending` aggregate totals for a conversation or platform user.

Set `BOT_ALLOW_GROUP_PERSONAL_INFO=true` to allow read-only personal-info
commands such as curriculum, homework, todos, and exams in group chats. Mutating
commands such as todo add/done/delete, homework done/undo, login/logout, and
notification changes are still blocked in groups.

Set `BOT_FEEDBACK_ADMIN_USERS` and/or `BOT_FEEDBACK_ADMIN_GROUPS` to comma-,
semicolon-, or space-separated QQ IDs to enable `反馈 ...` / `fb ...`. Set
`BOT_FEEDBACK_ADMIN_PLATFORM` when all admin notifications should use one
adapter, such as `napcat`.

## Test

```bash
go test ./...
```

## Deploy

The versioned `compose.yaml` builds the archived source from `./src`, loads
runtime configuration from `.env`, and persists SQLite data in the existing
host `./data` directory. `scripts/deploy-cn.sh` uploads both `.env` and the
Compose definition before validating and starting the service.

Inside the container, listeners that are published through Compose must bind to
all interfaces:

```text
NAPCAT_REVERSE_ADDR=0.0.0.0:2280
BOT_MEDIA_ADDR=0.0.0.0:2281
QQ_BOT_WEBHOOK_ADDR=0.0.0.0:2290
```

The host ports bind to `127.0.0.1` by default. Point the host reverse proxy at:

```text
/media/*  -> http://127.0.0.1:2281/media/*
/qqbot    -> http://127.0.0.1:2290/qqbot
```

Set `BOT_BIND_IP` only when a listener must be reachable directly from another
host. Host port overrides are `NAPCAT_REVERSE_HOST_PORT`,
`BOT_MEDIA_HOST_PORT`, and `QQ_BOT_WEBHOOK_HOST_PORT`.
