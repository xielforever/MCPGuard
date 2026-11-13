# 本地 ChatGPT Streamable HTTP 接入记录（2026-10-23）

状态：`HTTP_READ_AND_AUDIT_PASS_DESKTOP_TOOL_UNAVAILABLE`。本地 Streamable HTTP 诊断客户端已完成真实 PostgreSQL 读取和审计核对；当前 ChatGPT 旧会话仍未获得原生 MCP 工具，桌面原生调用验收尚未通过。

最初准备阶段记录为 `PENDING_NEXT_DESKTOP_TURN`，用户随后确认重载并发起新一轮读取请求。新一轮仍返回 `unknown MCP server 'mcpguard_local'`，因此先前“下一轮即可调用”的判断被本轮复验否定。

## 本轮已完成

- 恢复 WSL `Ubuntu-24.04`、原隔离 PostgreSQL 16.14、运行容器和网关。Windows `GET http://127.0.0.1:18080/healthz` 返回 `200`，内容为 `{"auth":"dev-tokens","status":"up"}`。
- 数据库重启后的宿主映射端口由 `32770` 变为 `32768`，仅修正私有配置中的数据库连接地址。原配置已备份，历史业务数据、执行与审计记录保留，测试身份及权限未更改。
- 备份用户配置后，在 `C:\Users\dzz\.codex\config.toml` 追加 `mcpguard_local`。已有配置逐字节保留；`codex mcp get mcpguard_local --json` 确认启用，传输类型为 `streamable_http`，URL 为 `http://127.0.0.1:18080/mcp`。
- `http_headers_helper` 从忽略提交且受 NTFS ACL 保护的 `.dev/env.json` 读取 Alice 测试令牌，向客户端提供认证头。令牌未写入共享配置、仓库或本记录。该辅助命令仅提供认证头，MCP 请求通过 HTTP 直连网关。
- 使用官方本地 app-server 的 `mcpServerStatus/list` 预检，成功发现 `MCPGuard 0.1.0-read-slice` 和只读工具 `pg.get_demo_item`。匿名 MCP 请求返回 `401`。
- 用户确认已在桌面客户端重载。此后本轮工具目录仍只含历史远端插件；查询本地服务时返回 `unknown MCP server 'mcpguard_local'`。未调用历史 OAuth 插件替代本地验证。

## 真实 HTTP 读取与审计核对

2026-10-23 10:14:29（Asia/Singapore），由助手发起 Windows Node HTTP 诊断，使用已配置的 URL 和同一个私有认证头辅助命令，依次完成 MCP initialize、initialized、tools/list、tools/call。实际传输为 Streamable HTTP，MCP profile 为 `2025-11-25`。客户端明确标识为 `mcpguard-local-http-diagnostic`，未冒充桌面客户端。

工具 `pg.get_demo_item` 的参数为 `{"itemId":"item_demo_1"}`，返回 `found=true`、`note="Alice scope A"`、`version="1"`；状态为 `SUCCEEDED` / `DISPATCHED` / `NONE`。文本结果与 `structuredContent` 一致。

| 标识 | 值 |
| --- | --- |
| actionId | `e3468f69-068a-4979-8c57-191cfb0ee581` |
| attemptId | `2285e662-ea8c-4a33-85ae-71f97f1f07c1` |
| executionId | `e8ed9c98-52a2-4f85-8e2c-044df0e564d2` |

PostgreSQL 独立查询核对通过：

- 审计事件 `176: EXECUTION_INTENT`、`177: SUCCEEDED`，均晚于读取前基线 `maxAuditSeq=175`。
- 两条事件的组织、Alice 员工身份、action、attempt、execution 标识全部与工具结果一致。
- 对应执行记录唯一，状态为 `SUCCEEDED` / `DISPATCHED`，`result_ciphertext` 非空。

结构化调用和审计证据见 [真实读取 JSON](local-chatgpt-streamable-http-read-2026-10-23.json)，准备阶段与客户端状态见 [接入 JSON](local-chatgpt-streamable-http-2026-10-23.json)。私有配置备份、完整握手结果和运行辅助文件位于忽略提交的 `.dev/`。

## 尚待桌面原生工具调用

当前会话的工具目录没有 `mcpguard_local`，`resources/list` 返回 `unknown MCP server`。桌面客户端日志中，新建 MCP 运行连接已报告该服务器 `ready`，与当前旧会话的不可用状态不同；证据指向此会话保留旧 MCP 连接状态，尚未证明具体客户端缺陷。

本轮 HTTP 诊断证明确有数据读取和审计落库，不能代替 ChatGPT 已注册 MCP 工具的原生调用验收。后续需在重新初始化 MCP 连接的桌面会话中调用同一工具，再记录新的执行标识、工具结果与审计，并确认答复展示。其他桌面任务仍在运行，本轮没有重启整个 ChatGPT 应用。

官方配置依据：[Model Context Protocol](https://learn.chatgpt.com/docs/extend/mcp)。本地 ChatGPT、CLI、IDE 共用同一主机上的配置；支持 URL 形式的 Streamable HTTP 和本地认证头辅助命令。[app-server 文档](https://learn.chatgpt.com/docs/app-server)说明 MCP 配置重载会为已加载会话排队刷新。

原 HTTPS/OAuth 网页尝试的历史证据继续保留在 [2026-10-22 记录](chatgpt-oauth-read-2026-10-22.md)中。
