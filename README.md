# Life @ USTC Bot

在 QQ 里使用 Life@USTC 的聊天入口。连到
[Life@USTC server](https://github.com/Life-USTC/server)，能力命名与 Web / CLI /
MCP 对齐（见
[interface hierarchy](https://github.com/Life-USTC/server/blob/main/docs/interface-hierarchy.md)）。

## 面向谁

- 想在群或私聊里查课表、作业、校车、待办的科大用户
- 需要 NapCat / QQ 官方 Bot 接入校园工作区的部署者

## 用户能做什么

直接发送中文命令或别名，无需命令前缀：

| 域 | 示例 |
|----|------|
| 校园信息 | `学期` · `课程` · `教学班` · `老师` · `校车` |
| 工作区 | `日程` · `课表` · `下一节课` · `待办` · `作业` · `考试` · `订阅` |
| 社区 | `反馈 …`（可路由给管理员） |
| 账户 | `登录` / `登录状态` · `退出` · `账户` · `设置` |
| 系统 | `状态` · `帮助`（含 `帮助 课表` 等专题） |

别名如 `td` / `hw` / `xc` / `kb` / `ddl` / `status` 只是捷径。`日程` 是课表+作业+考试+待办的聚合；`课表` 只看上课安排，`课表 2026秋` 或 `课表 26春` 可生成带教学周范围的整学期课表。

**登录**：设备码 OAuth；浏览器确认后用 `登录状态` 落库。Token 自动刷新，失败则清凭证并提示重登。

**通知（仅私聊）**：`设置 通知` 可分别开关课表 / 作业提醒；课前约 30 分钟、作业截止约 24 小时内推送（可配图）。群聊默认不推个人通知。

**图卡（可选）**：开启后，课表、待办、作业、考试、概览、校车、提醒等可先发短时 PNG，再回退文字。

**AI 助手（可选，仅私聊）**：接 OpenAI 兼容模型 + server MCP；群聊仍只走确定性命令。配置 Kimi 模型后，所有已登录的私聊用户均可使用文本、工具和图片理解。用量记在本地 SQLite。

**群聊**：个人只读命令默认关闭；开启后仍禁止改待办 / 作业完成 / 登录 / 通知等写操作。

## 接入方式

- **NapCat**：OneBot 11 反向 WebSocket（或出站 WS）
- **QQ 官方 Bot**：Webhook（推荐）和/或 Gateway

公开只读命令可按 TTL 缓存在本地 SQLite，部署版本参与缓存键，避免旧版本脏读。

## 给贡献者

环境变量、Compose 端口与反代路径见源码旁配置与 `compose.yaml`；开发检查用 `go test ./...`。
进程在 `BOT_HEALTH_ADDR`（默认 `127.0.0.1:2282`）提供 `/live`，只检查进程初始化和本地 SQLite，外部消息渠道断线不会触发容器重启。
编码约定以本仓库与 server 契约为准，不在此重复运维手册。

`make build` 会先校验 `api/openapi.provenance`，再从仓库内固定的
`api/openapi.json` 重新生成客户端，因此构建不依赖网络且可复现。更新契约时需提供
server 的完整提交 SHA，例如：

```sh
make sync-openapi generate \
  OPENAPI_SOURCE=../server/public/openapi.generated.json \
  OPENAPI_SERVER_SHA=<40-character-server-commit>
```

### 本地完整 E2E

仓库旁存在 `../server` checkout 时，可以启动隔离的 PostgreSQL、真实本地
Life@USTC Worker、Bot 进程和 NapCat 协议测试端，完整验证设备登录、原请求自动恢复、
iCalendar 私有链接投递及 `.ics` feed：

```sh
make dev-e2e
```

脚本使用独立的 Compose project、数据库和临时目录，不读取 Bot 的 `.env`，也不会连接
QQ 或生产服务。其他目录或端口可通过 `LIFE_USTC_SERVER_DIR`、
`DEV_E2E_SERVER_PORT`、`DEV_E2E_POSTGRES_PORT` 和 `DEV_E2E_INSPECTOR_PORT`
覆盖。
