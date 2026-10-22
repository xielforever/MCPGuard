# MCPGuard

企业集中 MCP 网关。客户端通过 HTTP 直连，或经本地 stdio 接入同一网关；网关使用 PostgreSQL 等后端的原生协议执行受控操作。

当前已实现并通过沙箱联调的**最小读取闭环**：官方 Go MCP SDK、两种入口、独立员工测试身份、权限过滤、一条真实 PG 查询、加密持久状态和审计。完整运行说明与范围见 [最小读取闭环](docs/read-slice.md)，验证证据见 [验收记录](docs/validation/read-slice-2026-10-22.md)，总体规划见 [设计文档目录](docs/README.md)。

Windows / WSL 本地复验及启动竞态修复见 [本地闭环记录](docs/validation/local-read-slice-windows-wsl-2026-10-22.md)。

独立 OAuth 测试身份、临时 HTTPS 入口与 ChatGPT 联调步骤见 [HTTPS/OAuth 本地接入](docs/oauth-local-chatgpt.md)。原有本地测试模式继续保留。

ChatGPT 已完成 OAuth 并触发真实 PG 读取；当前网页答复加载失败，无工具对照也复现，用户展示尚未验收。[本轮记录与边界](docs/validation/chatgpt-oauth-read-2026-10-22.md)。

## 本地运行

需要 Go（最低 1.25，toolchain 1.26.8）、Python 3、Docker，race 测试另需 C 编译器；不需要大模型 key。

```bash
make dev-up    # 隔离 PG、数据库角色、假数据、测试身份、迁移和构建
make dev-test  # Go race + 独立 Python HTTP/stdio 联调
make dev-run   # 网关 http://127.0.0.1:8080/mcp
```

支持 stdio 的客户端可启动：

```bash
python3 /absolute/path/to/MCPGuard/scripts/dev.py connect --employee alice
```

测试工具 `pg.get_demo_item` 只接受 `{"itemId":"item_demo_1"}`；数据范围由网关根据员工注入。Alice、Bob 访问各自范围，Visitor 无调用权。随机秘密位于忽略提交的 `.dev/env.json`（0600）；`make dev-down` 清除本项目的本地测试容器和数据。

当前支持 `dev-tokens` 与独立 `oauth-fixture` 测试身份，MCP profile 为 `2025-11-25`。企业 SSO、写入审批、Redis/API 和新控制台仍待开发/验收；ChatGPT 真实客户端结果以独立验收记录为准。原代理、确认码和页面属于未挂载的旧脚手架。

License: MIT
