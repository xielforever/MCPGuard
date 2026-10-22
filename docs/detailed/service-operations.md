# 服务、管理 API 与运行设计

版本：v0.1 详细设计。日期：2026-10-22。工程方案用于后续实现，当前未改业务代码。

采用一个模块化 Go 网关、一个客户端连接器和 PostgreSQL 权威元数据存储。控制台继续使用 Vue。模块内部使用类型化接口，不增加服务间 RPC、外部消息队列或作为状态库的 Redis 依赖。

## 1 代码组织与依赖

以下为拟议目录，现有仓库尚未按此迁移。

```text
cmd/mcpguard/                 网关装配、启动与关闭
cmd/mcpguard-connect/         本地 stdio 到 HTTP 连接器
internal/domain/             ID、不可变计划、状态与结果类型
internal/application/        Discover、Invoke、Decide、Resume、Cancel、Reconcile
internal/transport/mcp/       SDK 协议与工具结果映射
internal/transport/httpapi/   管理 API、浏览器会话与错误映射
internal/identity/           token、会话、主体与本地访问状态
internal/policy/             确定性权限与策略评估
internal/adapters/           PG、Redis、HTTP 原生执行
internal/persistence/        PostgreSQL repository 与事务管理
internal/secrets/            凭据引用解析与版本
internal/notify/             outbox 通知发送
internal/platform/           配置、结构化日志、指标与生命周期
migrations/                 元数据数据库版本迁移
web/                        Vue 管理与授权控制台
tests/integration/          拟议集成场景与测试夹具
```

入口依赖应用用例，应用依赖领域类型与端口，数据库和原生驱动实现端口。领域不导入 Gin、MCP SDK、SQL 驱动或 HTTP handler。接口在使用方定义，优先小接口，不为每个 struct 建立一套抽象。

MCP 与管理 API 共享领域服务；handler 不直接拼接多表操作，原生 adapter 不读取审批表或自行改变授权结论。整个用例通过 UnitOfWork 传递同一数据库事务，repository 不私自开启并提交子事务。

| 用例 | 本地事务内 | 本地事务外 |
|---|---|---|
| 修改/发布配置 | 条件版本写入、当前版本切换、授权 epoch、审计 | 必要的无副作用格式/连接检查 |
| 创建调用计划 | Action、Attempt、不可变计划、策略结论、状态、审计、通知意图 | 只读准备和经过限制的预览 |
| 人类决定 | 核验资格/计划/期限、唯一决定、状态 CAS、审计、outbox | 通知平台调用 |
| 领取执行 | 当前权限与版本检查、资格消费、Execution 意图、审计 | 凭据解析与原生驱动操作 |
| 完成执行 | 结果/副作用事实、Action 状态、审计、结果通知意图 | 大对象结果加密存放等外部 I/O |
| 核对未知结果 | 追加查询证据与核对记录，保留历史事实 | 只读原生状态查询；不派发原写操作 |

领取前校验凭据引用、版本及元信息；领取后在事务外解析该版本的执行handle。必要的预览使用明确只读身份与无副作用操作，不预先取得写执行权。领取后解析失败据实记录为未派发，不能归还资格让另一 worker 重发。锁顺序、隔离级别及撤权的线性化边界见[状态与存储](state-storage.md)。

## 2 管理 API 约定

前缀为 /api/v1，JSON 字段使用 camelCase；UUID 等 ID 是不透明字符串，不编码可授信权限。资源和工具等可变对象有单调 revision；工具、策略和操作计划的发布版本不可原地编辑。

| 端点 | 行为与结果 |
|---|---|
| GET /me、GET /capabilities | 当前主体及其可用产品能力，不返回凭据 |
| GET/POST /resources | 权限过滤列表；创建资源与受控凭据绑定，201 返回 Location |
| GET/PATCH /resources/{id} | 读取及修改允许字段；停启作为有版本的显式变更 |
| POST /resources/{id}/test-connection | 使用已注册配置做有限连接/认证检查，返回受保护诊断；不接受任意地址或 SQL |
| POST /tools、POST /tools/{id}/versions | 创建工具、提交不可变候选版本及 Schema |
| POST /tools/{id}/publish | 校验 operation/权限/资源绑定，原子切换发布指针及审计 |
| GET/POST/PATCH /policies | 权限与策略管理；发布/启停遵守版本和审计规则 |
| POST /credential-bindings/{id}/rotations | 绑定新凭据版本，淘汰旧池；响应只返回引用与版本 |
| GET /actions、GET /actions/{id} | 我的请求或明确受权范围；包含当前状态与可用操作 |
| POST /actions/{id}/cancel | 请求取消；未领取时原子终止，已领取时202表示已受理取消而非已回滚 |
| GET /approvals、POST /approvals/{id}/decisions | 待处理授权与可信决定；审批回调不直接派发 |
| GET /executions/{id} | 受控执行结果、派发证据和副作用状态 |
| POST /executions/{id}/reconciliation-requests | 请求只读核对；结果未知时不作为“重试”入口 |
| GET /audit-events、POST /audit-exports | 有界检索、受权异步导出，结果文件另行鉴权 |

这些是管理应用 API，不是 MCP 方法。普通 IDE 使用 tools/list、tools/call；需要显式续传时调用注册工具 action.status、action.resume、action.cancel，不能被要求调用内部管理 REST。

浏览器审批详情应展示主体、真实目标、完整受权操作详情、限制、过期时间与当前版本。审批完成展示“已批准，等待有效调用继续”等真实状态，不把按钮成功显示为业务执行成功。

## 3 并发修改与管理命令幂等

GET 单个可变对象返回 ETag；修改、停启、发布和轮换要求 If-Match。缺失返回428，旧版本返回412；SQL 条件更新将版本比较和写入放在同一原子操作。失败时控制台提示重新加载比较，不能无提示覆盖他人修改。

创建、决定、取消和轮换等管理命令使用 Idempotency-Key。唯一作用域为组织、实际主体、命令类型及目标；同键同规范化请求返回原命令结果，同键不同请求409。重放仍重新鉴权，不向已撤权主体返回旧敏感响应。

幂等记录、业务状态与审计同一事务提交，避免“请求已成功但去重记录未写入”的窗口。并发首用靠唯一约束裁决，处理中返回明确命令状态；在生命周期内未完成命令不得被清理或复用同键。

顺序为认证/当次对象授权及必要CSRF检查→查找同作用域幂等记录→比较请求摘要并重放，只有新命令才检查当前If-Match并尝试变更。不能先因对象已变返回412而破坏合法重放；摘要包含原版本前提但不包含可轮换token、cookie或CSRF秘密。

幂等键有效期和命令/审计保留期分别配置，命令响应返回去重有效期。记录清理后服务端未必能识别旧键，过期后不再承诺去重；客户端不得在有效期后静默重放危险命令。审批决定还须验证 planHash、Action revision、可信审批主体与资格；“同一个人点了同一个按钮”不替代数据库约束。

管理命令去重不保证原生业务写幂等。MCP RPC ID 也不是跨会话业务键：没有有效 Action 关联的新 tools/call 属于新意图，可能产生新的业务效果。首版不依赖模型生成随机字符串证明跨新 Action 去重，受控写入通过人类授权及明确重做流程约束。

## 4 列表、错误与结果访问

列表使用稳定的 (createdAt, id) 游标排序，限制 pageSize；游标绑定主体范围、过滤条件、排序与目录版本，签名后校验。版本变化时返回失效并重新查询，不因旧 cursor 扩大权限；列表不是无限期一致性快照。

管理 API 错误采用以下拟议结构，原生工具结果继续遵守[统一契约](../contracts.md)。

```json
{
  "code": "VERSION_CONFLICT",
  "message": "对象已更新，请重新读取后操作",
  "requestId": "request_demo_1",
  "details": {"currentRevision": 8}
}
```

| HTTP 状态 | 管理 API 含义 |
|---|---|
| 400 / 422 | 消息格式或业务字段校验失败 |
| 401 / 403 | 管理主体无效或缺少所需权限 |
| 404 | 目标不存在，或对象访问策略选择隐藏其存在 |
| 409 | 状态冲突、幂等键异体复用、重复不兼容决定 |
| 412 / 428 | 版本条件不匹配 / 缺少版本条件 |
| 429 | 进入处理前容量限制，返回可用的重试提示 |
| 503 | 元数据、身份或必要凭据依赖不可用，不能派发 |

读取执行状态返回200和真实结果，包括 OUTCOME_UNKNOWN；HTTP 成功不代表原生业务成功。禁止原样输出 err.Error()、连接串或认证头。

结果查询与导出逐对象鉴权；拥有 Action ID 或曾经调用过工具不保证当前仍可读取敏感结果。业务结果、审批详情、普通审计摘要分别制定脱敏范围，结果过期只删受控数据，不篡改已发生的派发与决策事实。

## 5 启动配置与动态配置

启动配置包括监听、受信代理、数据库引用、身份提供方、凭据提供方、限制、日志和功能开关。资源、工具、策略与审批路由在版本化注册表维护，修改后更新 epoch 和审计，不要求重启。

下例仅表达配置形状，不是现有程序可读取的配置；地址使用示例域名，不包含生产秘密。

```yaml
server:
  listen: ":8080"
  externalUrl: "https://mcpguard.example.invalid"
metadata:
  driver: postgres
  dsnRef: "secret://mcpguard/metadata/current"
identity:
  issuer: "https://login.example.invalid"
  resource: "https://mcpguard.example.invalid/mcp"
mcp:
  profiles: ["2025-11-25", "2026-07-28"]
  enabledCapabilities: ["tools"]
secrets:
  provider: configured-provider
```

实际配置还必须填写并校验容量、deadline、结果和审批期限；上述片段没有隐含生产默认值。缺少必要配置、身份/密钥配置冲突、迁移不兼容或端口监听失败时退出并输出脱敏错误，不能静默降为无认证或默认放行。

数据库使用当前受支持 PostgreSQL 版本及维护中的 minor，设计仅依赖16+基础能力。Go 构建基线至少满足 SDK v1.8.0 的 Go1.25.0要求，发布固定维护中的工具链与模块校验值，不选已知过期补丁。前端使用受支持的 Node LTS；具体补丁与镜像 digest 在实现时锁定。

## 6 部署与生命周期

首条闭环可单实例部署，数据库约束从第一版起支持多实例并发；生产副本数根据可用性要求确定。多副本不自动消除 MCP 传输状态：2025 profile 的会话、GET SSE 与请求所有者必须按 SDK 实现验证粘性路由或受控恢复，不能宣称任意副本可无缝续接所有流。

MCP 的流、进度和活跃取消属于请求所有者；持久 Action 与审批事实可跨实例查询。实例间可以轮询持久状态唤醒本地等待者，首版不需要共享内存假设或单独消息中间件。

TLS 可由可信反向代理终止；网关只采信明确配置的代理及转发头，禁止根据任意客户端传入的用户头构造 Principal。浏览器使用精确 CORS 来源、CSRF 防护；MCP 按规范校验相应请求边界。网关对 native TLS/证书的校验不能以网络已互通为由关闭。

SSE 路径关闭响应缓冲，代理与客户端 timeout 按 profile 和实测设置。限制 body、header、并发、排队与结果大小；ReadHeaderTimeout 等入口限制与长流生命周期分别处理，不能用一个很短的全局请求超时截断合法流。

SIGTERM 按以下顺序处理：readiness 关闭并停止新领取，停止 outbox 新领取，允许已接收调用在有界期限内结束，再发送取消、记录未确认执行为未知，关闭流、连接池与数据库。强制终止来不及记结果时由恢复器依据持久意图标记未知，不重新派发。

| 运行能力 | 设计要求 |
|---|---|
| liveness | 进程与事件循环可用；不因单个业务资源宕机反复重启 |
| readiness | 迁移兼容、元数据库和必要身份/凭据验证能力可用，允许接收新受控请求 |
| 凭据轮换 | 绑定版本变化、失效旧池、重验尚未领取计划；保留轮换审计 |
| 数据库迁移 | 独立部署步骤、单执行者锁、expand/contract；不能无条件自动降级 schema |
| 备份恢复 | 加密备份并演练恢复；回退元数据后不得把旧“未派发”误当远端未执行 |
| 容器 | 非 root、固定镜像与依赖、按需只读文件系统、受控临时目录与 CA |
| 容量控制 | 按主体/资源限制并发和等待；多实例本地限制乘副本数的上界需计算 |
| 通知故障 | outbox 退避和有限重投、人工可见积压；通知失败不产生批准或执行 |

元数据回退可能抹掉已消费资格。灾难恢复默认关闭写派发，核对恢复点后的不可逆执行记录/业务证据后再开放；仅从备份看到 NOT_DISPATCHED 不能保证可安全重做。

## 7 可观测性与故障处理

日志关联 requestId、Action、Attempt、Execution、资源及策略版本；用受控标识定位，不输出原始敏感参数或密钥。指标避免以 actionId、完整 URL 或 key 作为高基数标签。

至少观测接入拒绝、工具调用延迟、审批等待/过期、领取冲突、原生耗时与池等待、结果未知、审计持久失败、身份验证失败、通知积压、限流与取消。OUTCOME_UNKNOWN 或派发结果无法持久化应触发可见告警和受权核对流程。

追踪跨度跨协议入口、计划、授权、领取、原生调用、结果落库；长审批使用关联事件，不把 span 或数据库事务保持数小时。应用侧追加审计不等于数据库管理员无法修改，若企业有防篡改要求可对接外部保留机制。

死锁/序列化重试只覆盖确定尚未跨原生派发的本地事务。未知 COMMIT 需查询幂等/执行记录核实；数据库重试库不能包裹整个“领取→原生执行→落库”函数。

## 8 现有模块迁移

| 当前代码 | 迁移处理 |
|---|---|
| cmd/mcpguard、internal/config | 重写装配与显式校验；保留 Go/YAML 使用方式，移除缺配置默认降级 |
| internal/api/router.go | 保留 Gin，拆薄 handler，加可信身份、对象授权、版本、幂等与脱敏 |
| internal/proxy | 替换下游 MCP 转发；接入官方 MCP Server 和独立 connector |
| internal/auth | 拆身份与人类授权；替换内存验证码和向模型返回确认秘密的路径 |
| internal/rules | 复用三类结论概念，替换 regex-only 与默认放行为结构化策略 |
| internal/store | 替换 SQLite 聚合存储、加入迁移与应用事务；旧数据不能直接当有效批准导入 |
| internal/notify | 复用适配器思路，加入持久 outbox；通知内容不作为审批事实 |
| web | 保留 Vue 外壳，移除 mock 成功路径，按管理 API 与真实状态重接页面 |

实现验收见[验证计划](validation-plan.md)。本文件不新增运行环境要求到企业网络，也不把当前旧脚手架的构建结果当成新架构验证。
