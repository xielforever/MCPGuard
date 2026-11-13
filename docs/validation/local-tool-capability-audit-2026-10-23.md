# 本机 mcpguard_local 功能接入审计

后续更新：本报告保留接入前快照。`audit.list` 随后已实现并部署；当前聊天的原生调用验收进展见 [接入记录](local-audit-list-2026-10-23.md)。

检查日期：2026-10-23，Asia/Singapore。检查范围为本会话原生工具目录、本机连接配置、运行中的 HTTP 接口、网关构建来源以及仓库实现调用链。本轮只检查并记录，未修改服务、凭据、工具注册或插件配置。

## 结论

只有一个业务工具的直接原因是服务端 `gateway.New` 只注册了 `pg.get_demo_item`。本机连接没有配置 `enabled_tools` 或 `disabled_tools` 过滤器；不存在一批已注册、被本机配置隐藏的工具。

当前版本完成的是第一条真实 PostgreSQL 读取闭环。可以优先接入 MCP 的现成功能是“查询本人审计记录”；健康接口也可包装成工具，但只表示进程存活。规则、审批、通知和管理页面存在旧原型代码，尚未形成当前网关的完整能力。设计文件中的另外五个业务工具未实现。

## 当前运行事实

| 项目 | 本轮结果 |
|---|---|
| Git HEAD | `a9d2760e97768e2a5fb8b206242036d78cd8a8f1` |
| 本机连接 | `mcpguard_local`，启用，`http://127.0.0.1:18080/mcp` |
| 客户端工具白名单/黑名单 | 均未配置 |
| 当前会话原生工具目录 | 仅 `mcp__mcpguard_local__pg_get_demo_item`，参数为 `itemId` |
| 当前认证方式 | `dev-tokens` |
| `GET /healthz` | HTTP 200，`status=up`、`auth=dev-tokens` |
| 带当前本机身份的 `GET /api/v1/audit?after=0&limit=1` | HTTP 200，返回一条事件，`nextAfter=1` |
| `GET /api/v1/dashboard` | HTTP 404 |
| `GET /api/v1/rules` | HTTP 404 |
| `GET /api/v1/logs` | HTTP 404 |
| `GET /api/v1/settings` | HTTP 404 |
| `GET /web/` | HTTP 404 |

工具目录通过本会话工具元数据检查；上述 HTTP 请求用于验证独立管理接口，不是以脚本替代 MCP 业务工具调用。本轮未重新执行演示读取，之前已保存的原生读取结果见 [原生调用记录](local-native-tool-read-2026-10-23.json)。审计探测只记录事件数量及字段名，没有保存凭据或业务载荷。

构建来源核对：容器内 `/usr/local/bin/mcpguard` 和工作区 `bin/mcpguard` 的 SHA256 均为 `c9f1fdc63367bcfbc26ac7cba37d020e0703662e97332f58f65ce4bff07ed8a0`，与 [OAuth 增量源码清单](oauth-source-manifest-2026-10-22.json) 完全一致；该清单内 12 个源文件按约定归一化换行后的 SHA256 也全部匹配当前工作区。二进制内记录的是当时未提交 OAuth 增量的基线 `c4e56d9...` 和 `modified=true`，不能仅据此判为遗漏当前提交。运行事实与当前核心代码一致。

## 已实现，但未作为 MCP 工具开放

| 功能 | 当前实现与接入状态 | 接入所需工作与边界 |
|---|---|---|
| 查询本人审计记录 | 当前网关已提供受认证保护的 `GET /api/v1/audit`，本轮 HTTP 200；`Store.Audit` 按可信组织及主体过滤 | 可优先增加 MCP 包装，复用身份重验及分页。拟议工具名 `audit.list`，当前不存在。只允许 `after`、`limit`；上限 100，不接收调用者指定的组织或用户。现有结果是事件类型、关联 ID、时间等摘要，不含完整历史业务结果 |
| 存活状态 | 当前网关已有 `GET /healthz`，本轮 HTTP 200 | 可选包装为拟议工具 `gateway.health`，当前不存在。它不检查数据库、授权存储或业务依赖，不能标为完整就绪检查 |
| OAuth 测试身份服务 | 已实现发现、PKCE S256、预注册客户端、测试登录同意、令牌交换、刷新轮换与撤销 | 属于另一种认证模式。当前本机使用 `dev-tokens`；不是漏掉的业务工具，也不要求恢复域名或云端插件。测试身份及授权状态保存在内存，重启清除，不等于企业 SSO |
| stdio 接入程序 | `cmd/mcpguard-connect` 与 `internal/connect` 已使用官方 SDK 转接至同一 HTTP 网关 | 属于另一种接入方式，转发同一工具目录，不会多出业务工具。本机已经直接使用 HTTP |
| 数据库迁移、服务恢复、凭据续期 | 迁移命令及本机维护流程已有实现；启动与续期接入证据见既有本机验证记录 | 属于运行维护能力。此次未重新故障注入或续期测试，不应把迁移管理员权限、密钥和令牌维护直接交给模型 |

主要源码：[网关注册及 HTTP 路由](../../internal/gateway/gateway.go)、[审计存储](../../internal/metadata/store.go)、[OAuth 测试服务](../../internal/oauthfixture/server.go)、[stdio 转接](../../internal/connect/connect.go)、[运行入口](../../cmd/mcpguard/main.go)。

## 已在现有工具内部生效的能力

这些能力服务于当前读取链路，不是缺失的独立工具：

- 身份校验、目录权限过滤、领取时重新校验权限及可信数据范围。`scope` 从服务端主体记录取得，调用者不能传入。
- 参数严格校验，只接受合规的 `itemId`，拒绝额外字段、重复字段和任意 SQL。
- 原生 PG 参数绑定、只读账号与只读事务，最多一行，原生执行预算三秒，结果及并发数量有界。
- 不可变计划、HMAC 摘要、AES-GCM 加密计划及结果，Action/Attempt/Execution 资格和审计持久化。
- 显式 MCP 取消传递至 PostgreSQL，独立有界清理与终态落库。
- 过期在途读取资格恢复：记录未知结果，不自动重放原生操作。该恢复仅适用于现有只读链路，不能宣称受控写入恢复或多实例执行隔离已经完成。

固定业务 SQL 是 `SELECT item_id,note,version::text FROM demo.items WHERE scope_id=$1 AND item_id=$2 LIMIT 1`。因此目前不是一个已实现通用数据库能力、只在客户端隐藏工具的版本。

主要源码：[PG 读取器](../../internal/nativepg/reader.go)、[网关](../../internal/gateway/gateway.go)、[元数据事务与加密](../../internal/metadata/)。

## 有实现代码，但尚未接成当前网关功能

当前入口不初始化旧 `api`、`proxy`、`rules`、`auth`、`notify` 或 SQLite `store`。`runtimeconfig` 也明确不加载旧代理配置。下面只确认组件代码存在，不把源码检查当作功能验收。

| 旧组件 | 已写出的部分 | 尚缺的连接或实现 |
|---|---|---|
| 正则规则引擎 | 规则增删列举、优先级排序、按参数文本匹配；内置危险 SQL、文件删除、Kubernetes 删除、关机等模式 | 未进入当前网关；匹配不使用 `ToolName` 字段，未形成当前架构的资源权限、版本化策略和禁止自批流程。内置字符串规则不代表数据库写入、文件或 Kubernetes 执行工具已经存在 |
| SQLite 规则和日志 | 规则 CRUD、日志写入/列举/统计组件存在 | 未接当前 PG 权威状态与审计；没有当前业务调用链写入旧日志。旧日志结构有原始参数和确认码字段，不能直接作为新 MCP 审计接口返回 |
| 旧 MCP 代理 | pass/confirm/block 分支和后端转发；旧子进程逐行 stdio 转发 | 当前入口未使用；与已实现的新版 SDK stdio 接入是不同代码，不能混为一谈 |
| 验证码确认 | 内存中的确认创建、超时与验证码验证 | 没有持久独立审批、受信任审批人和确认后的执行恢复。旧代理还把确认码放进返回调用方的错误消息，不满足所设计的人类授权边界 |
| 飞书、Telegram 通知 | HTTP 发送载荷及通知管理器 | 未被当前应用初始化或接入审批执行；没有本轮真实投递验证。未发现 Slack/Webhook 通用通知实现，页面选项不能当作已实现提供商 |
| 管理 HTTP 路由 | 旧路由文件中有规则/日志读取、统计与验证码验证处理器 | `SetupRouter` 没有被当前入口调用；规则新增/修改/删除处理器为空；设置读取占位、写入为空；没有用户管理路由，也没有该路由自己的认证中间件 |
| Vue 控制台 | 仪表盘、规则、审计、用户与设置页面以及 API 包装文件 | 页面使用硬编码数据和占位交互；规则保存明确为 mock；用户保存只关闭窗口；设置按钮没有保存处理。页面没有使用所定义的 API 包装，确认路径和设置更新方法还与旧后端不一致 |

关键依据：

- [旧路由](../../internal/api/router.go)：规则 POST/PUT/DELETE 空处理器、设置占位；当前 `/api/v1/...` 及 `/web/` 对应探测均为 404。
- [旧规则引擎](../../internal/rules/engine.go) 与 [内置规则](../../internal/rules/builtin.go)。
- [旧确认](../../internal/auth/confirm.go) 与 [返回确认码的旧代理](../../internal/proxy/handler.go)。
- [通知组件](../../internal/notify/)、[旧 SQLite 存储](../../internal/store/)。
- [规则页面](../../web/src/views/Rules.vue)、[用户页面](../../web/src/views/Users.vue)、[设置页面](../../web/src/views/Settings.vue)、[API 包装](../../web/src/api/index.js)。包装使用 `/actions/:id/confirm` 而旧后端为 `/confirm/:id`；设置包装用 PUT，旧后端仅有空 POST；包装中的 `/users` 没有后端路由。

## 设计中的业务工具，并未实现

[六工具设计提案](../specs/demo-tools.yaml) 中，只有第一项 `pg.get_demo_item` 已在当前网关实现。其余五项为：

| 设计工具 | 当前缺口 |
|---|---|
| `pg.update_demo_note` | 没有写适配器、受控写权限或完整人类审批执行链 |
| `redis.get_demo_note` | 没有 Redis 原生适配器或工具注册 |
| `redis.set_demo_note` | 同上，并缺写入审批和恢复链 |
| `directory.get_demo_office` | 没有目录服务 HTTP 适配器或工具注册 |
| `inventory.reserve_demo` | 没有库存服务 HTTP 适配器或工具注册 |

该 YAML、OpenAPI 与完整元数据模型是设计材料，不是运行时工具加载器。提案顶部“尚未实现”的总说明对第一项已过时，应以当前代码和运行检查区分；没有改写原设计文件。

企业 SSO、用户/组管理、通用资源目录、持久他人审批、通知 Outbox、完整受控写入与多适配器仍需开发。已有加密结果持久化也不等于已实现操作历史/结果查询接口；若新增此类 MCP 工具，仍需实现按主体鉴权的结果读取服务。

## 接入建议

1. 优先新增本人审计查询工具，复用 `Store.Audit`，严格保留可信身份与分页约束。需要新增工具 Schema、注册、受保护处理和原生调用验证。
2. 如确有使用需求，再增加健康工具；若要报告“可用”，需要补依赖检查，不能直接扩大 `/healthz` 的含义。
3. 下一批业务能力属于产品开发：按已确认需求实现真实受限查询或五个设计工具，分别接通原生适配器、权限、结果和验收。受控写入必须先完成持久审批及执行边界。
4. 旧规则、确认、通知和页面按当前集中网关架构整合并验收。直接挂载旧路由或暴露验证码流程不能补齐这些能力。

本轮核验为工具元数据、源码调用链、源文件/运行二进制哈希和只读 HTTP 探测；没有以设计文档、页面截图或旧测试成绩替代当前功能状态，也没有对旧通知或审批组件宣称端到端通过。
