# Life @ USTC Bot

Go bot framework for Life @ USTC.

It exposes a OneBot 12 HTTP implementation with
`github.com/botuniverse/go-libonebot` and includes a NapCat-compatible
OneBot 11 reverse WebSocket bridge.

## Commands

Message commands use `/life` by default:

```text
/life ping
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
```

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

# NapCat reverse WebSocket. The local container is configured for /ws on 2280.
NAPCAT_REVERSE_ADDR=0.0.0.0:2280
NAPCAT_REVERSE_PATH=/ws
```

If a NapCat WebSocket server is configured instead, set `NAPCAT_WS_URL` and the
bot will dial it. Otherwise it listens for NapCat reverse WebSocket connections.

## Test

```bash
go test ./...
```
