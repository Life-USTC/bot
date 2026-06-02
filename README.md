# Life @ USTC Bot

Go bot framework for Life @ USTC.

It exposes a OneBot 12 HTTP implementation with
`github.com/botuniverse/go-libonebot` and includes a NapCat-compatible
OneBot 11 reverse WebSocket bridge.

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
OPENAI_API_KEY=
OPENAI_BASE_URL=

# NapCat reverse WebSocket. The local container is configured for /ws on 2280.
NAPCAT_REVERSE_ADDR=0.0.0.0:2280
NAPCAT_REVERSE_PATH=/ws
```

If a NapCat WebSocket server is configured instead, set `NAPCAT_WS_URL` and the
bot will dial it. Otherwise it listens for NapCat reverse WebSocket connections.

Set `BOT_ENABLE_AGENT=true` with `OPENAI_API_KEY` to enable the optional
LLM-backed private-chat assistant. The agent uses existing bot commands as
tools for curriculum, next class, homework, and shuttle bus queries. Group chats
still use deterministic command handling only.

## Test

```bash
go test ./...
```
