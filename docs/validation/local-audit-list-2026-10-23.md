# 本机 audit.list 接入与验证

日期：2026-10-23。状态：**已实现并部署，用户重启后当前聊天原生 audit.list 调用及分页验收通过**。

## 实现

- 当前网关注册 `audit.list`，使用既有 `Store.Audit`，按受信任的 `org_id` 与 `principal_id` 共同过滤，每次查询重新检查身份有效性。参数不接受用户、组织、scope 或其他选择器。
- `after` 为非负 int64 的十进制字符串，默认 `"0"`；`limit` 为整数 1–100，默认 100。未知字段、重复字段、null、浮点数、越界数字及续传输入被拒绝。
- 成功结果为 `{events, nextAfter}`。MCP 的 `seq` 与 `nextAfter` 均使用十进制字符串，下一页直接传回 `nextAfter`。空页返回空数组并保留游标。既有 HTTP 接口继续使用原数字格式。
- HTTP 与 MCP 复用身份隔离查询和游标推进逻辑；MCP 也使用现有并发上限、取消链和两秒查询期限。错误只返回安全消息，不返回数据库细节或记录。
- 结果字段仅有序号、事件 ID、关联 Action/Attempt/Execution ID、事件类型和时间；不包含令牌、原始参数、SQL 或历史业务载荷。
- 审计权限沿用既有 HTTP 接口：每个有效身份均可查询本人记录。Alice、Bob 可见 PG 与审计两个工具；Visitor 只可见本人审计工具，PG 直接调用仍拒绝。

调用示例：

```json
{"after":"0","limit":2}
```

主要代码：[工具与参数处理](../../internal/gateway/audit.go)、[注册与目录权限](../../internal/gateway/gateway.go)、[既有身份隔离存储](../../internal/metadata/store.go)。

## 开发验证

- Go 参数边界、输出 Schema、原 PG 契约测试通过。
- 独立 PostgreSQL 容器 `mcpguard-audit-list-test-20261023` 上的 `TestReadSliceIntegration` 及 11 个子测试通过，使用 race 检查。新子测试验证不同用户、不同组织复用同一主体 ID 的隔离、每页默认上限、连续翻页、空页、身份选择器/超限/重复参数拒绝、已有连接撤权，以及超过 JavaScript 安全整数范围的真实审计事件精确游标。
- metadata、oauthfixture、runtimeconfig、wireids 的 Go race 测试通过；connect 包无独立测试。
- 既有 HTTP/stdio/PG 回归更新为两个工具目录，并增加审计翻页、超限拒绝和 stdio 审计调用，52 项检查通过。
- 上述为开发测试，**不计为当前聊天的原生 audit.list 验收**。测试使用独立身份和数据库；没有用本机 Alice 的权限或历史记录做故障注入。

首次数据库测试发现通用 MCP 客户端会将 int64 JSON 数字解析为 float64，使最大游标舍入。已改为 MCP 十进制字符串，并增加真实 bigint 事件回归；首次失败与后续通过日志保存在私有测试目录。另一次复验遇到临时容器重启后随机映射端口变化，更新独立测试 DSN 后通过；本机固定端口与配置未改。

## 本机部署

2026-10-23 06:36:57 UTC 完成网关更新，继续使用 `http://127.0.0.1:18080/mcp`、`dev-tokens` 和原凭据自动续期流程。只重启本项目网关，数据库保持运行。

- 原二进制 SHA256：`c9f1fdc63367bcfbc26ac7cba37d020e0703662e97332f58f65ce4bff07ed8a0`。
- 新二进制 SHA256：`72a29ed847de1316c3711f606e446a1da3eb97c879e1e08047957b3856c31ae7`，容器内文件与构建产物相符。
- 原二进制保存在 `.dev/local-service/before-audit-list-20261023T063651Z/`；数据、密钥和身份配置保留。
- 更新前后均为 91 Actions、91 Attempts、91 Executions、207 审计事件，最大审计序号 207。
- 更新后当前聊天直接调用原生 `mcp__mcpguard_local__pg_get_demo_item` 成功：`item_demo_1` 返回 `Alice scope A`、版本 `"1"`；Action `7a835bff-8017-4972-a7ad-f958c8ad2676`、Execution `df15eca9-7485-409c-bbeb-0a775923f50a`，观察时间 `2026-10-23T06:37:03.443275055Z`。

## 刷新前的原生工具目录状态

部署及原 PG 复验后，当时聊天工具目录仍只有 `mcp__mcpguard_local__pg_get_demo_item`，`audit.list` 尚未成为本会话可调用的原生工具。没有通过验证脚本、独立 app-server 工具调用或云端插件替代这一验收。

当时请用户刷新本机 `mcpguard_local` 连接，或重启 Codex，并保留待验收状态。用户随后回复“已重启”；刷新后的实际原生调用结果见下文，之前状态另存为 [刷新前机器记录](local-audit-list-before-native-2026-10-23.json)。

官方 [app-server 文档](https://learn.chatgpt.com/docs/app-server) 提供 `config/mcpServer/reload` 来重新加载配置并排队刷新已加载聊天。本次可用工具没有该管理入口，运行中的桌面 app-server 使用 stdio；另起一个 app-server 进程不能证明当前聊天的工具目录已更新，因此未采用此方式冒充刷新成功。

机器记录：[本轮状态、检查与源码摘要](local-audit-list-2026-10-23.json)。旧功能盘点是接入前的快照，见 [接入前检查](local-tool-capability-audit-2026-10-23.md)。

## 重启后当前聊天原生验收

2026-10-23 21:55:48（Asia/Singapore，13:55:48 UTC）完成记录。当前原生目录已包含 `mcp__mcpguard_local__audit_list` 与 `mcp__mcpguard_local__pg_get_demo_item`。

两次均直接调用本会话的 `mcp__mcpguard_local__audit_list`，未通过脚本或云端插件执行 MCP 请求：

| 调用参数 | 实际事件序号 | 实际事件类型 | nextAfter |
|---|---|---|---|
| `{"after":"0","limit":2}` | `"1"`、`"2"` | EXECUTION_INTENT、SUCCEEDED | `"2"` |
| `{"after":"2","limit":2}` | `"5"`、`"6"` | EXECUTION_INTENT、SUCCEEDED | `"6"` |

两次调用均成功，每页两条，事件 ID 没有重复；下一页序号均大于前一页游标，返回游标等于最后一个事件的序号。`seq` 与 `nextAfter` 均保持字符串格式。全局审计序号不要求在某个身份的筛选结果中连续。

原始返回值、事件及关联 ID、时间和分页断言完整保存于 [原生调用证据](local-audit-list-native-2026-10-23.json)。开发测试与当前聊天原生验收已分别通过。
