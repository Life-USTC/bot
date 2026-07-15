# Life @ USTC Bot

Go bot framework for Life @ USTC.

It exposes a OneBot 12 HTTP implementation with
`github.com/botuniverse/go-libonebot` and includes a NapCat-compatible
OneBot 11 reverse WebSocket bridge. It can also connect directly to the QQ
official bot platform gateway.

## Commands

Message commands use `/life` by default:

```text
/life ping
/life login
/life login status
/life logout
/life me
/life todo
/life todo add <title>
/life sub
/life notify
/life notify schedule on
/life notify homework on
/life semester
/life course <keyword>
/life section <keyword>
/life bus
```

OneBot 12 extension actions:

```text
life_ustc.get_current_semester
life_ustc.search_courses      {"search":"calculus"}
life_ustc.search_sections     {"search":"calculus"}
life_ustc.get_bus
life_ustc.begin_login         {"platform":"napcat","user_id":"123"}
life_ustc.poll_login          {"platform":"napcat","user_id":"123"}
life_ustc.get_me              {"platform":"napcat","user_id":"123"}
life_ustc.list_todos          {"platform":"napcat","user_id":"123"}
```

`/life login` starts an OAuth device-code login and replies with the
verification link and user code. After approving in the browser, send
`/life login status`; the bot persists the token for that chat user in SQLite.
Authenticated commands refresh tokens automatically when possible.

`/life notify` manages private-chat active pushes. Class reminders and homework
reminders can be enabled independently, and sent reminders are recorded in SQLite
so the same item is not pushed repeatedly.

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
to let schedule, todo list, overview, dashboard, and upcoming-deadline replies
send a short-lived PNG image before falling back to text. Route `/media/*` from
the public host to `BOT_MEDIA_ADDR`. The runtime image installs
`fonts-noto-cjk` and `fonts-firacode`; set `BOT_IMAGE_FONT_PATH` only when
overriding the default Chinese font.

Set `BOT_ENABLE_AGENT=true` with `OPENAI_API_KEY` to enable the optional
LLM-backed private-chat assistant. The agent uses existing bot commands as
tools for curriculum, next class, homework, and shuttle bus queries. Group chats
still use deterministic command handling only. Increase `BOT_LLM_TIMEOUT_SECONDS`
for slower reasoning models; failed agent runs store the provider error in
SQLite `agent_runs.error`.

Set `BOT_ALLOW_GROUP_PERSONAL_INFO=true` to allow read-only personal-info
commands such as curriculum, homework, todos, and exams in group chats. Mutating
commands such as todo add/done/delete, homework done/undo, login/logout, and
notification changes are still blocked in groups.

Set `BOT_FEEDBACK_ADMIN_USERS` and/or `BOT_FEEDBACK_ADMIN_GROUPS` to comma-,
semicolon-, or space-separated QQ IDs to enable `反馈 ...` / `fb ...`.

## Test

```bash
go test ./...
```
