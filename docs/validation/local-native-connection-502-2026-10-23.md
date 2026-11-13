# 当前聊天的本机 MCP 原生连接排查 — 2026-10-23

状态：`CURRENT_CHAT_NATIVE_READ_PASS_AFTER_RESTART`。用户报告重启后，当前聊天已经加载本机原生工具，并直接调用 `pg.get_demo_item` 成功。此前 HTTP 502 与未就绪客户端的证据保留在结构化记录的 `initialFailure` 中。

## 初始故障排查

- `mcpguard_local` 仍指向 `http://127.0.0.1:18080/mcp`，使用既有本机认证头辅助命令，服务与自动续期配置保留。
- Windows 直接访问及经当前系统代理访问 `/healthz` 均为 HTTP 200。这不是 MCP 工具调用验收，也不足以证明初始化 POST 路径正常。
- 当前聊天再次走原生 MCP 资源发现入口时，初始化握手仍返回 HTTP 502；工具目录没有本机 `mcpguard_local` 工具。
- 当前聊天的客户端日志多次记录 `omitting MCP server without an exact ready client server_name=mcpguard_local`。桌面客户端的新连接日志可以握手到 `MCPGuard / 2025-11-25`，不将其他连接的就绪状态当作本聊天验收。
- 没有充分证据更改服务器、凭据、鉴权或系统代理。当前聊天的失败客户端状态需要重载后再判断，根因尚未最终确认。

## 重启后的直接原生验收

官方 `config/mcpServer/reload` 可以重载配置并为已加载聊天排队刷新。本聊天提供的应用工具没有暴露该管理接口；本机 app-server daemon 控制 socket 也不可用。没有终止正在运行的 Codex 或其他聊天。

用户报告重启后，于 2026-10-23 14:07:02（新加坡时间），当前聊天暴露 `mcp__mcpguard_local__pg_get_demo_item`。直接传入 `{"itemId":"item_demo_1"}`，返回 `found=true`、`Alice scope A`、字符串版本 `1`，状态为 `SUCCEEDED / DISPATCHED / NONE`。

本次 execution 为 `3c83392e-d73a-46ec-8b35-283c9bdd45ad`，action 为 `39db1b98-2dbf-4f3a-9fc0-18654f66bf14`。这是当前聊天的原生工具实际返回，未运行验证脚本，未使用独立 app-server 客户端或云端插件替代。[直接调用完整结果](local-native-tool-read-2026-10-23.json)

本次修复阶段没有更改服务器、凭据或系统代理；观察到连接重启后恢复，但不据此声称最初 HTTP 502 的网络成因已被独立证明。

原生调用验收已通过。[故障与恢复结构化证据](local-native-connection-502-2026-10-23.json)

官方依据：[MCP 重启入口](https://learn.chatgpt.com/docs/extend/mcp)、[app-server 重载接口](https://learn.chatgpt.com/docs/app-server)。
