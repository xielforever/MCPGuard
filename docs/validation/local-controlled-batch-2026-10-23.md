# 本机受控功能批次验收记录

本批已实现、通过本地测试并部署；当前聊天新增工具的原生验收等待客户端目录刷新。实施和部署期间没有重启 Codex，没有使用旧云端插件、隧道或 app-server 验证脚本代替原生调用。

## 交付内容

六个固定业务工具中剩余五项已实现，另新增 `action.status/resume/cancel`、`history.list/get_result`。持久他人审批、版本化规则/资源/用户与组授权、真实控制台、Telegram Outbox 和受控写入恢复已接入当前网关。企业 SSO 按用户要求待定；真实 Telegram Bot 和接收目标待配置，没有发送真实通知。

本机入口仍为 `http://127.0.0.1:18080/mcp`。控制台为 `/web/`；Redis 为环回 18081，演示 HTTP 后端为环回 18082，原 PG 固定端口 18432。当前客户端身份不能自批，写入必须先由另一主体在控制台批准，再由申请者显式恢复。

## 本地测试及运行证据

80 个 Go 测试通过，包含 race、真实 PG/Redis/HTTP、完整 MCP SDK 路径、并发唯一执行、撤权、过期、拒绝、取消、崩溃恢复和未知结果禁止重放；56 项独立 HTTP/stdio 检查通过；20 项服务/续期单元测试通过，另 5 项旧的本机身份变更测试未启用；19 项隔离控制台交互检查通过，浏览器无 JavaScript 异常。`go vet` 通过。

控制台实际交互验证包括匿名访问、独立登录、申请者无自批按钮、另一主体批准后仍无执行资格、配置持久化、用户组、本人审计、通知待配置状态和退出登录。[审批页面](local-controlled-console-review.png)、[通知状态页面](local-controlled-console-settings.png) 来自隔离测试环境。

2026-10-23 15:08:19 UTC（23:08:19 新加坡时间）部署完成，二进制 SHA256 为 `d7795af3314834e2ad189b26ab553c1f4f496a8d40a3c99f07607d6ce7d33ecb`。原有 92 条 Action/Attempt/Execution 和 209 条审计记录在安装前后完全一致；原演示项没有重新播种。迁移前备份、旧二进制、首次部署失败现场和旧容器均保留。

首次 Redis 部署出现镜像对空卷目录 copy-up 导致所有者改变的问题，持久化目录不可写；修复为显式服务 UID 和 `volume-nocopy`。随后验证四项服务整体重启、停止 Redis/HTTP 后由 supervisor 自动拉起，以及 Redis 值重启后保留。Alice 自动续期仍有效：30 天有效期、7 天提前续期，身份/授权变化不自动恢复。

## 当前聊天原生证据

本轮通过模型可见的 `mcp__mcpguard_local__pg_get_demo_item` 直接读取 `item_demo_1`：`SUCCEEDED`，文本 `Alice scope A`、版本字符串 `1`，Action ID `23500301-9361-4dc3-9cd0-093a45f8e9c2`。随后直接调用原生 `audit_list` 取得关联事件 210 `EXECUTION_INTENT`、211 `SUCCEEDED`，下一游标为字符串 `211`。

本轮原生目录仍仅显示 PG 和审计两个工具；新增健康、业务、操作控制和历史工具没有模型可调用的入口，因此明确保持待验收。已请求只刷新本机连接目录，不要求重启 Codex；不会把开发 SDK/HTTP/界面测试写成当前聊天原生通过。

完整结果和 48 项源码摘要见 [机器记录](local-controlled-batch-2026-10-23.json)，直接原生结果见 [原生记录](local-controlled-batch-native-2026-10-23.json)。本地身份、适用范围和配置边界见 [本机受控配置](../controlled-local.md)。
