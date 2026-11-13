# RA-10 原生 HTTP helper、stdio 私有适配与能力声明

日期：2026-10-26。本文记录 RA-10 的本地交付；[RA-09](credential-contexts-local.md)、原设计与原验收记录保留。实际结果见 [RA-10 验收](validation/local-ra10-2026-10-26.md)。

## 1 两种入口及宿主边界

| 入口 | 私有交付与激活 | Context 头更新 | 能力边界 |
|---|---|---|---|
| 原生 HTTP + helper | 宿主私有头回调建立 Context；独立 worker 有界同步、保存文件、激活资源及续期 | 同一个 Context 续期保持令牌；自然到期、绝对期限或令牌更换需要宿主重连并重新读取 helper | 必须具备宿主私有回调，不能将回调输出交给模型 |
| stdio 私有模式 | 连接器在 tools/list、tools/call 前同步；prepare 成功后再执行有界私有同步 | 每次远程 HTTP 请求读取当前内部头快照；下次调用可重建自然 EXPIRED Context | stdout 只有 MCP 帧，私有 API 响应不转发 |
| URL-only 无私有钩子 | 原目录、申请、状态及既有工具按原规则工作 | 无新增私有能力 | 不声明自动文件交付或多资源私有激活 |

两个入口共用 `internal/clientadapter` 和 RA-09 私有组件，不解密包、不直连 PostgreSQL、不取得审批或原生账号管理权限。一个 profile 支持多资源，不为每套 PG 配置一个客户端连接。

HTTP helper 的 `headers` 命令是**宿主私有通道**：stdout 返回认证头 JSON，仅供 MCP 宿主内部 HTTP transport 使用。它不能注册成 MCP 工具、模型可读资源，也不能通过通用终端工具让模型代为执行后读取输出。stdio 入口没有这个输出路径，两个可执行文件承担不同的通道职责。

`mcpguard-http-helper worker` 是独立受控本地进程，须由用户或可信进程管理器启动。一次头回调不等于持续后台同步。worker 每次工作量沿用 RA-09 限额，总运行期最多一小时、间隔 100 ms–60 s，连续三次私有同步失败后退出；Context 终态直接停止。进程管理器应观察安全退出状态，不因失败重放 MCP 业务请求。

文件权限只隔离其他系统身份和误用，不隔离同系统用户的任意文件读取/进程执行。模型不可见的证明覆盖实际 MCP 帧、组件安全输出、stderr、网关日志和审计；真实客户端如何隔离宿主私有通道须在对应产品中验收。

## 2 私有生命周期与恢复

受信服务端使用既有 `mcpguard-credentials enroll-client` 交付私有 bootstrap。组件从环境读取当前员工 token；bootstrap、缓存和所有包保持在仓库外的私有目录。固定网关 origin、组织、主体、登记客户端和员工凭证摘要，后续更换账号、bootstrap 或 origin 时不能沿用其他人的 profile。

每次私有同步先检查当前身份/Context，再处理任务和文件；stdio 在远程业务派发前完成这个检查。过期恢复仅针对自然 **EXPIRED** Context，创建新 Context 后重新提交并验证仍有效的选中本地文件。**REVOKED** Context 不自动创建新实例，需要本人使用私有命令显式修复。已撤销包和失配授权不会因为重连、Context 重建或保留文件而恢复。

| 故障 | 原生 HTTP helper | stdio 私有适配 |
|---|---|---|
| 活跃 Context 临近到期 | worker 延长期限，令牌保持，宿主缓存头继续可用 | 调用前有界同步延长 |
| Context 自然到期/绝对期限到达 | worker 不在宿主背后换头；重连回调新建 Context、重验文件 | 下次调用前新建 Context、重验文件，远程请求使用新头 |
| Context 被主动关闭 | helper/worker 拒绝自动重建 | 调用前拒绝，业务未转发 |
| 员工 token 失效或变人 | 当前认证失败；不接受错配的旧 profile | 初始化或调用前拒绝，没有匿名/旧模式回退 |
| 缓存包文件丢失 | worker 重领同一仍有效包 | 下一次有界同步恢复 |
| 凭据包被撤销 | 保留文件仍不可激活；其他资源独立使用 | 同左，不通过重准备或重连复活 |
| 私有 claim 后响应丢失 | 后续私有同步核对 CLAIMED，领取替代 ticket | 已完成的 prepare 结果保留，后续私有同步恢复 |
| 普通业务调用结果未知 | helper 不代发业务 | 返回安全的结果未知错误，不自动重放业务 |

stdio 的 prepare 结果保持原业务对象，额外 `_meta["io.mcpguard/private-client"]` 为安全的 `AdapterDeliveryNotice`：`ACTIVE` 表示当次同步观察到对应资源/包激活；`PENDING_PRIVATE_SYNC` 表示需要后续私有同步。`businessRequestReplayed` 固定为 false。它不表示一次验证产生了持续有效的执行票据。

后置同步失败不把已完成的准备说成“未发生”，也不再次发起申请、审批、开通或 prepare。下一次调用前可处理已消费任务及替代 ticket。新 ticket 和 Context 恢复均不调用原生账号提供方。

## 3 能力声明与模型输出保护

`ClientAdapterCapabilities` 来自单一源 0.2.7，可通过两种程序的安全诊断命令读取。私有 stdio 模式在 MCP initialize 的 `capabilities.experimental["io.mcpguard/private-client"]` 声明实际能力；旧 stdio 模式不增加这个声明。声明不提供认证或授权，不能根据客户端自报能力放宽主体、包或资源检查。

stdio 显式支持 profile 2025-11-25 的 tools/list、tools/call，不宣告 sampling、elicitation、Tasks、目录变更订阅或任意版本转换。协议对初始化、能力协商和按已协商能力操作的要求见 [MCP 生命周期规范](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)。

MCP stdio 通道仍由既有 SDK 和 wireids 处理并发、取消、帧及 ID 类型/精度，stdout 只承载 MCP 消息；私有头通过 HTTP transport 注入，不进入 params、Meta 或工具正文。stdio 的通道要求见 [MCP transport 规范](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)。HTTP 主路径仍使用同一个远程 `/mcp`。

私有头类型没有可导出的秘密字段或隐式 JSON 输出。宿主回调只返回员工 Authorization 与 Context 头；独立交付授权只发给私有生命周期 API，不附加到普通 MCP 请求。stdio 还在结果边界检查当前员工 token、交付 token、Context token 和实际已缓存包的 nonce/ciphertext/tag；拒绝私有字段及 JSON-in-text 中的私有封套，不通过替换文本改变业务数据。远端错误只返回固定安全说明。

原生 HTTP 客户端直接接收网关的模型安全 MCP 结果，其私有生命周期响应在 helper/worker 内消费。本地验证记录宿主私有头输出与模型可见协议的不同证据层次，不把包含私有头的宿主通道计入公开输出。

## 4 安装与调用

已有目录、申请、引用包、下载和 Context 服务开关沿用 RA-09；本轮不新增迁移或生产默认开关。构建 `cmd/mcpguard-http-helper` 与 `cmd/mcpguard-connect`。所有例子中的路径/ID 为安全配置项，实际 token 只由进程私有环境 `MCPGUARD_TOKEN` 提供。

原生 HTTP 宿主将以下命令配置为**私有请求头回调**，stdout 消费成 HTTP 请求头。不同宿主的配置格式需要按实际版本填写；不使用虚构的通用配置格式：

```text
mcpguard-http-helper headers --gateway <https-origin>/mcp --auth-file <private-bootstrap> --directory <private-profile>
```

同时由可信进程管理器运行同一个 profile 的 worker：

```text
mcpguard-http-helper worker --gateway <https-origin>/mcp --auth-file <private-bootstrap> --directory <private-profile> --interval-ms 1000 --max-runtime-seconds 3600
```

`--once` 仅处理一轮有界同步；`--page-size` 和 `--max-pages` 仅设置新 profile 的更小边界。协议最大页数时让服务端选取其实际配置的更小单页上限；显式更小本地 page-size 仍按本地值请求。bootstrap 与 profile 使用 RA-09 的跨进程锁和 Windows/Linux 安全文件检查。

stdio 客户端只需启动一个连接器，保留原 gateway 参数，再增加两项私有配置：

```text
mcpguard-connect --gateway <https-origin>/mcp --private-auth-file <private-bootstrap> --private-directory <private-profile>
```

两项私有参数必须同时提供，缺少其中一项时拒绝，不回退到旧模式。两项都不提供时，旧 stdio 工作方式保持。`--allow-loopback-http` 只用于显式本地隔离验证，正常远程端点要求 HTTPS，拒绝重定向、带凭据 URL、query 和 fragment。

安全诊断命令不包含当前凭证或 Context 令牌：

```text
mcpguard-http-helper capabilities --entry NATIVE_HTTP_HELPER
mcpguard-http-helper capabilities --entry URL_ONLY
mcpguard-connect --capabilities
```

自然过期时按上表重连/下次调用恢复；主动关闭需要通过 `mcpguard-session init --replace-context ...` 显式修复，再让原生宿主重连。更换员工凭证须按当前身份重新登记及使用正确 profile，不手改缓存身份。包自然到期需要 RA-09 私有续期命令，仍受当前授权与版本检查约束。

## 5 本地验证与后续阶段

`scripts/adapter_fixture.py prepare` 创建全新场景和私有状态；先 `test` 跑完整 Go，再顺序执行 `wire`、`access-smoke`、`provision-smoke`、`credential-smoke`、`download-smoke`、`context-smoke`、`smoke`，最后 `recovery-smoke`。禁止让会改变权限/原生角色的流程并发使用同一场景。旧场景和失败原始日志继续保留。

RA-10 的 HTTP 证明使用独立宿主测试器调用真实 helper 私有输出、真实 HTTP MCP 和真实 worker；stdio 证明启动实际私有连接器并检查原始协议帧。Windows 原生执行分别覆盖安全缓存、头边界与控制器；这些证据与具体命名 coding 产品的实际私有钩子验收单列。

当前新增单一源 0.2.7：89 定义、70 样例，HTTP 28 契约和 MCP 11 契约保持；[0.2.6 六份原字节快照](specs/v0.2/history/0.2.6/README.md)与历史版本保留。下一项为 RA-11 的正式新资源读取、计划绑定、派发时服务端检查和结果控制。完整吊销/在途停止、轮换/恢复 fence、真实客户端和人工体验分别沿用 RA-13/14、RA-15、RA-18 的退出条件。
