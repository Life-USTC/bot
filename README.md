# Life @ USTC Bot

在 QQ 里使用 Life@USTC 的聊天入口。连到
[Life@USTC server](https://github.com/Life-USTC/server)，能力命名与 Web / CLI /
MCP 对齐（见
[interface hierarchy](https://github.com/Life-USTC/server/blob/main/docs/interface-hierarchy.md)）。

## 面向谁

- 想在私聊管理个人工作区，或在群聊查询公开校园信息的科大用户
- 需要 NapCat / QQ 官方 Bot 接入校园工作区的部署者

## 用户能做什么

直接发送中文命令或别名，无需命令前缀：

| 域 | 示例 |
|----|------|
| 校园信息 | `学期` · `课程` · `教学班` · `老师` · `校车` · `天气` · `教室` · `第二课堂` |
| 工作区 | `日程` · `课表` · `下一节课` · `待办` · `作业` · `考试` · `订阅` |
| 社区 | `反馈 …`（可路由给管理员） |
| 账户 | `登录` / `登录状态` · `退出` · `账户` · `设置` |
| 系统 | `状态` · `帮助`（含 `帮助 课表` 等专题） |

别名如 `td` / `hw` / `xc` / `kb` / `ddl` / `status` 只是捷径。`日程` 是课表+作业+考试+待办的聚合；`课表` 只看上课安排，`课表 2026秋` 或 `课表 26春` 可生成带教学周范围的整学期课表。

校车可按路线和日期查询，例如 `校车 周六 太湖路园区 东区`、`校车 周日 东区 太湖路园区`、`校车 工作日 东区 西区` 或 `校车 2026-09-06 东区 太湖路园区`。`周一-周五`、`周中`、`工作日` 使用工作日时刻；周六和周日时刻不同，需分别查询。

第二课堂查询无需登录：`第二课堂` 查看活动列表，`第二课堂 搜索 讲座` 搜索名称，`第二课堂 查看 <youngId>` 查看报名时间、地点等详情；报名仍在校方第二课堂网站完成。明确的第二课堂命令也可在群聊使用。

私聊发送 `订阅 身份 <JW ID> <普通|助教|旁听>` 可修改已有订阅的身份，例如 `订阅 身份 12345 助教`。教学班 JW ID 显示在订阅列表中；修改身份不会创建订阅，也不授予校方教学权限。

**登录**：设备码 OAuth；浏览器确认后自动恢复登录前未完成的请求。Token 自动刷新，失败则清凭证并提示重登。

**通知（仅私聊）**：`设置 通知` 可分别开关课表 / 作业提醒；课前约 30 分钟、作业截止约 24 小时内推送（可配图）。群聊默认不推个人通知。

**图卡（可选）**：开启后，课表、待办、作业、考试、概览、校车、提醒等可先发短时 PNG，再回退文字。

**AI 助手（可选）**：接 OpenAI 兼容模型 + server MCP。私聊通过 Bot 命令注册表使用已实现的个人能力；MCP 仅作为补充只读数据源，使用 MCP 专用 OAuth token，在需要时通过 `search_campus_tools` / `call_campus_tool` 发现和调用 `catalog_young_event_list`、`catalog_young_event_get`、`catalog_rooms_map` 三个工具。server 的其他 MCP 工具不会自动开放给 Bot，MCP 写操作、评论、描述和上传也不在当前白名单中。群聊只有在 @、回复 Presto 或平台交互明确激活后才进入 Agent，可调用公开 Bot 命令，但不开放 MCP。用量记在本地 SQLite。

**群聊**：`校车`、`校车 西区 高新区`、`课程 数学分析` 等明确公开查询无需 @；普通聊天中仅仅出现“校车”等关键词不会触发。课表、成绩、待办、订阅链接、账户和设置等个人能力始终只在私聊执行。回复 Presto 的公开查询时，可用“周日呢”这类短追问继承上一条路线。QQ 官方 Bot 通常只收到 @ 消息；NapCat 可以应用完整的无 @ 匹配规则。

## 接入方式

- **NapCat**：OneBot 11 反向 WebSocket（或出站 WS）；自动接受所有好友申请和邀请机器人入群的请求。其他用户申请加入已有群聊仍由群管理员处理。
- **QQ 官方 Bot**：Webhook（推荐）和/或 Gateway

公开只读命令可按 TTL 缓存在本地 SQLite，部署版本参与缓存键，避免旧版本脏读。

## 给贡献者

环境变量、Compose 端口与反代路径见源码旁配置与 `compose.yaml`；开发检查用 `go test ./...`。
进程在 `BOT_HEALTH_ADDR`（默认 `127.0.0.1:2282`）提供 `/live`，只检查进程初始化和本地 SQLite，外部消息渠道断线不会触发容器重启。
编码约定以本仓库与 server 契约为准，不在此重复运维手册。

### macOS 原生部署

生产 Bot 与渲染服务运行在 `tkm-mac-mini`，由系统级 launchd 自动启动。更新入口是 `scripts/deploy-mac.sh`。脚本默认通过
`tiankaima@tkm-mac-mini` 更新 `/Users/tiankaima/Services/life-ustc-bot`，只接受干净的已提交
版本：它用该提交创建临时源码归档，在本地临时副本中生成 Go vendor 目录，把归档传到远端，
再在 Darwin arm64 上使用 CGO 编译 Bot 和 Rust `renderd`。远端工具查找顺序是
`ROOT/toolchain/go/bin`（与 `go.mod` 对齐的 Go）、`ROOT/toolchain/bin`、`/opt/homebrew/bin`、`/usr/local/bin` 和 `/usr/bin`；Rust 构建使用
锁定的 `Cargo.lock` 和离线缓存，因此更新时需要预先准备好 Go vendor 所需的本地模块缓存以及
远端 Cargo registry 缓存。

远端的 `config.json` 是私有的 JSON 对象，键是 Bot 环境变量名。脚本只在远端用
`/opt/homebrew/bin/python3` 读取它并写入 Bot 的 launchd plist，不会 shell-source 或打印值。
`BOT_DB_PATH`、`BOT_RENDER_ENDPOINT`、`BOT_BUILD_VERSION` 以及本地健康地址由部署固定。部署使用
`runtime-fonts/` 下的扁平字体目录（思源黑体 Regular/Bold TTC 和 Fira Code TTF），不要把凭据或
完整字体树放入源码归档。

部署前远端应已有 `bin/`、`data/`、`logs/`、`build/`、`runtime-fonts/`、`config.json` 和现有
SQLite 数据库；初次生产迁移由迁移工作另行完成。脚本会在 `build/<deployment-id>/` 保留源码、
构建产物、旧二进制、数据库备份和日志。它先停 Bot，再备份并迁移 SQLite，随后原子替换二进制和
`/Library/LaunchDaemons/dev.life-ustc.{bot,renderd}.plist`；plist 为 root 所有、权限 600，两个
服务均设置 `UserName` 为部署用户（默认 `tiankaima`）、`RunAtLoad`、`KeepAlive`、工作目录和标准输出/错误日志。它
先启动并检查 `dev.life-ustc.renderd`，再启动并检查 Bot；失败时恢复二进制、数据库和 plist，并
重新加载部署前已加载的服务。部署锁和 launchd label 可避免同一 Bot 出现重复实例。

NapCat 与 Bot 同机运行，OneBot 反向 WebSocket 连接 `ws://127.0.0.1:2280/ws`，
`NAPCAT_REVERSE_ADDR` 使用 `127.0.0.1:2280`。图片在 Mac 下载或渲染后，通过 OneBot Base64
或官方 QQ 分片上传发送，不再启动公共图片 HTTP 服务。

Bot 的 `HTTPS_PROXY`、`HTTP_PROXY` 和 `NO_PROXY` 放在远端 `config.json` 中；生产出口应使用
Mac 本地代理。当前 `http://127.0.0.1:17890` 是 Clash Verge 的专用 mixed listener，
通过 `DIRECT` 出站，持久配置在 Clash 的 merge profile 中；它不依赖 cn 的 SSH 隧道。
切换出口前，必须在 QQ 开放平台的接口 IP 白名单中加入 Mac 的公网出口 IP，否则 QQ API 返回 `11298`。
官方 QQ 的公网入站反代通过 Tailscale 转发到 Mac 的 Webhook 监听地址；
是否启动 Gateway 和 Webhook，分别由 `BOT_ENABLE_QQ_BOT_GATEWAY`、`BOT_ENABLE_QQ_BOT_WEBHOOK` 控制。

```sh
./scripts/deploy-mac.sh
```

只检查当前提交和目标信息而不执行 SSH、构建或远端改动时，设置 `DEPLOY_DRY_RUN=1`。若使用不同
主机或路径，可覆盖 `REMOTE_HOST`、`REMOTE_USER`、`REMOTE_ROOT`；健康检查等待时间可用
`DEPLOY_HEALTH_TIMEOUT`（1–3600 秒）覆盖。

图卡由 Typst `renderd` 服务渲染，延续迁移前的简洁排版：近白画布、细横线、
18pt 标题、13pt 正文、9pt 页脚，数字与代码使用 Fira Code，中文及课程名称使用思源黑体 / Noto CJK。
校车查询只筛选线路，选中的线路保留全部站点和时刻表，并突出查询站点；未公布的时刻显示「—」。
校车表格每列宽 300pt，多条线路按实际渲染高度紧凑分配到两列，两列独立排列，避免短表旁的大块空白。文字图卡正文宽 480pt；表格行高及段落换行交由 Typst 自动处理，不插入额外断行字符。
下一班信息放在标题右侧；待办表格填满正文宽度，帮助菜单的命令列与说明列按 1:2 分配宽度。
课表保留完整节次和日期列，今天使用淡青色，课程使用原有浅色；长课程名自然换行。
天气画布宽 630pt，两个地点左右并列，湿度与风速在各列内纵向排列，保留温度曲线、降水概率和每日温度范围条。高度随内容变化，默认以 3× 输出 PNG。

启动 `renderd` 后可重新生成旧版原图和 Typst 对照页（旧版代码仅在临时目录内执行）：

```sh
./scripts/render-reference.sh
go run ./cmd/render-examples -endpoint http://127.0.0.1:9123/render -out examples
```

打开 `examples/index.html` 查看七组固定数据、固定时间的对照图；点击图片查看完整 PNG。
仅生成 Typst 图卡时可省略第一步。该命令及 CI 校验图片尺寸范围，渲染或文件写入失败时返回非零退出码。

`make build` 会先校验 `api/openapi.provenance`，再从仓库内固定的
`api/openapi.json` 重新生成客户端，因此构建不依赖网络且可复现。更新契约时需提供
server 的完整提交 SHA，例如：

```sh
make sync-openapi generate \
  OPENAPI_SOURCE=../server/public/openapi.generated.json \
  OPENAPI_SERVER_SHA=<40-character-server-commit>
```

### 教室地图查询

在 Bot 中直接发送教室编号（例如 `5201`、`3A204`、`GT-B110`），即可自动收到对应的位置图片；群聊中无需 @ Bot。支持小写、全角编号，也可以发送 `教室 3A204` 或 `3A204 在哪里？`。
有单间标注时会返回高亮楼层图；只有楼层概览时返回概览图。图片回复不附加位置文字。
群聊允许这类公开查询，课表和日程不会自动附加地图。

私聊中的自然语言查询也可以通过 MCP 的 `catalog_rooms_map` 工具完成。工具先返回
同一份教室地图数据，Bot 再投递高亮 PNG；没有可用图片时会保留公开地图 URL。

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
