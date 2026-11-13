# MCPGuard Local Read 插件故障排查与服务端恢复

日期：2026-10-23，时区 Asia/Singapore。

当前状态：`RETIRED_CLOUD_PLUGINS_LOCAL_ONLY`。2026-10-23 用户明确要求移除两个旧云端插件，只保留本机 `mcpguard_local`。卸载接口确认 `MCPGuard Local Read` 原本已未安装，`MCPGuard Local Read Restored` 已卸载；卸载后直接通过本机原生工具再次读取成功。[清理与原生复验记录](local-only-plugin-cleanup-2026-10-23.json)。下文保留此前云端恢复历史，不作为当前使用入口。

历史恢复状态：`RECOVERED_VIA_REPLACEMENT_PLUGIN_NATIVE_CALL_AND_AUDIT_PASS`。隔离 OAuth 服务已恢复；替代云端插件 **MCPGuard Local Read Restored** 完成连接，并在当时的 Codex 会话通过实际已注册插件工具完成真实 PG 读取，持久结果及审计关联核对通过。旧插件及其失败记录保留。

## 故障证据

- 插件管理页面中的原地址为 `https://maiden-assuming-arrived-admitted.trycloudflare.com/mcp`，与历史记录一致。
- `mcpguard-oauth-https-v2` 在 2026-10-23 06:39:17 起记录 `Unauthorized: Tunnel not found`，原临时隧道已失效。
- OAuth PostgreSQL、网关和隧道容器均于 09:29:07 退出，退出码为 255；没有将这一状态归因为未经验证的具体宿主故障。
- 原本地 `mcpguard_local` 使用独立的 18080 入口，仍能正常读取；云端插件使用 HTTPS/OAuth 入口，不能以本地调用成功替代云端插件验收。

## 已完成的恢复

1. 在忽略提交的 `.dev/oauth/recovery-20261023T022435Z/` 保留原私有配置、运行状态和隧道日志；未删除数据库、业务记录或审计。
2. 启动原 OAuth PostgreSQL 并等待 TCP readiness。宿主映射端口从 32772 变为 32769，仅更新这套隔离夹具的私有 DSN，凭据保持原值。
3. 归档原停止的网关容器，使用相同镜像、二进制、只读挂载和测试身份重建 OAuth 网关，监听 `127.0.0.1:18081`。
4. 保留旧隧道，新建 `mcpguard-oauth-https-v3`；继续使用固定回环 Host，不关闭 DNS 重绑定保护。
5. 当前恢复入口为 `https://devoted-coins-widescreen-substitute.trycloudflare.com/mcp`；OAuth issuer/resource 已按同一 origin 更新。

## 验证结果与插件替代

独立命令行 HTTPS 检查通过 41 项，覆盖 OAuth 发现、Alice/Bob/Visitor 授权与权限、真实 PG 查询、数据范围、文本/结构化结果一致性、持久审计、刷新旋转和撤销。证据见 [HTTPS 检查](mcpguard-plugin-recovery-https-2026-10-23.json)。

修复前复测：原本地 MCP 原生工具成功读取 `item_demo_1` 得到 `Alice scope A`；旧云端 `MCPGuard Local Read` 仍返回 `-32603`。这组对照已保留在 [结构化记录](mcpguard-plugin-recovery-2026-10-23.json)。

官方[插件更新说明](https://developers.openai.com/plugins/deploy/submission)明确当前更新流程不支持更改既有 MCP server URL；当前账户的管理页面也未提供该控件。没有通过本地配置覆盖冒充远端已修复，也未删除旧插件。经用户确认继续完成网页连接步骤后，创建 [MCPGuard Local Read Restored](https://chatgpt.com/plugins/plugin_asdk_app_6adac7752f08819191228eb7dbbf9c88) 指向新地址，继续使用原测试凭据、精确稳定回调、PKCE S256 和 `mcp:read`，本次 UI 选择 `client_secret_post`。

2026-10-23 10:36:33，通过当前会话实际已注册的 `mcp__codex_apps__mcpguard_local_read_restored_pg_get_demo_item` 调用 `{"itemId":"item_demo_1"}`，得到 `found=true`、`Alice scope A`、字符串版本 `1`，以及 `SUCCEEDED` / `DISPATCHED` / `NONE`。不是独立 HTTP harness 的模拟结果。[原生工具结果](mcpguard-plugin-recovery-native-read-2026-10-23.json)

| 字段 | 结果 |
| --- | --- |
| actionId | `1e87e0c4-6d0c-40f3-83e5-78173e5486ed` |
| attemptId | `a843c9cb-390c-4a4e-8df6-c54fa49e171e` |
| executionId | `e059e213-e3d3-4519-9965-590bc00d9e9f` |
| 审计 | `31: EXECUTION_INTENT` → `32: SUCCEEDED`，均晚于读取前基线 30 |
| 持久执行结果 | 唯一执行，状态成功，加密结果非空 |

独立数据库查询核对两个审计事件的 Alice 主体及三个关联 ID 与工具返回一致。[审计证据](mcpguard-plugin-recovery-audit-2026-10-23.json)。当前 Codex 会话的云端插件工具调用已经通过；未重新验证 ChatGPT 网页聊天答复展示，不改写 2026-10-22 的 UI 历史阻塞结论。[连接状态截图](mcpguard-plugin-recovery-connected-2026-10-23.png)

Quick Tunnel 是临时入口，重建后域名会变；OAuth 测试授权仅存于内存，重启后需重新连接。长期稳定运行需要单独配置固定 HTTPS 域名/命名隧道及持久身份服务，本次未扩展到生产部署。

官方依据：[OAuth 连接契约](https://developers.openai.com/plugins/build/auth)、[连接与更新测试](https://developers.openai.com/plugins/deploy/connect-chatgpt)。旧验证文档保留历史状态，本记录不改写之前的验收结论。
