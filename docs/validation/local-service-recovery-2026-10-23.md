# 本机 MCPGuard 启动与恢复验证 — 2026-10-23

结论：`LOCAL_MCP_NATIVE_READ_AND_RESTART_RECOVERY_PASS`。本机 Codex 使用 Streamable HTTP 直连 `http://127.0.0.1:18080/mcp`；五次官方客户端工具调用均成功，且逐条与数据库中的执行记录及审计事件对应。当前服务保持运行。未配置域名、DNS 或隧道，未使用云端插件。

结构化结论见 [recovery.json](local-service-recovery-2026-10-23.json)，操作说明见 [本机服务运行](../local-service.md)。

## 原因与修复

检查时 WSL `Ubuntu-24.04` 和本项目容器均已停止；网关和数据库没有自动重启策略，数据库宿主端口使用动态分配，也没有 Windows 登录启动或持续监督进程。临时终端退出后，服务缺乏可靠的持续运行与恢复入口。

本次接管已有的本地 `dev-tokens` 测试夹具：

- 数据库固定为 `127.0.0.1:18432 -> 5432`，网关保持 `127.0.0.1:18080`。Windows 实际监听均为回环地址。
- 两个容器设置 `unless-stopped`；WSL 监督进程检查就绪状态并恢复意外停止的容器。数据库采用 TCP `pg_isready` 后再启动网关。
- 当前用户计划任务 `MCPGuard Local Service` 运行隐藏的普通权限 PowerShell 监督进程，持有前台 WSL 会话，并在本项目 WSL 监督进程退出后重试。Windows/Linux 锁防止重复监督及停止恢复竞争。
- Codex 的既有 `mcpguard_local` 配置仍使用上述本机 URL；认证头辅助命令先确认服务就绪，再读取私有测试凭据，因此服务停止后建立新连接也能按需启动。
- 网关使用只读文件系统和二进制挂载，移除整个仓库的可写挂载。运行环境只传入监听、认证模式、运行时数据库连接与加密/计划密钥，不传管理员连接或员工明文 token。

代码入口：[Windows 控制器](../../scripts/local-service.ps1)、[WSL 控制器](../../scripts/local_service.py)、[认证头辅助命令](../../scripts/mcp-local-headers.cjs)。

## 实际调用与恢复结果

验证使用官方 Codex app-server 的 `mcpServer/tool/call`，调用实际配置的 `mcpguard_local` 服务和 `pg.get_demo_item({"itemId":"item_demo_1"})`。每次均返回 `found=true`、`note="Alice scope A"`、`version="1"`，状态为 `SUCCEEDED / DISPATCHED / NONE`。

| 场景 | 实测结果 | 审计序号 | 客户端结果 |
| --- | --- | --- | --- |
| 基线读取 | 成功 | 182–183 | [JSON](local-service-native-before-restart-2026-10-23.json) |
| 强制结束网关进程后恢复 | 网关自动恢复，读取成功 | 184–185 | [JSON](local-service-native-after-crash-2026-10-23.json) |
| 控制器重启网关、数据库及监督进程 | 原数据继续可读 | 186–187 | [JSON](local-service-native-after-restart-2026-10-23.json) |
| 停止服务后仅发起 MCP 连接 | 认证头辅助命令按需启动，约 5.90 秒完成调用 | 188–189 | [JSON](local-service-native-on-demand-2026-10-23.json) |
| 结束本项目 WSL 监督进程后恢复 | 外层进程约 3.62 秒恢复监督，读取成功 | 190–191 | [JSON](local-service-native-after-supervisor-recovery-2026-10-23.json) |

异常恢复证据：[网关](local-service-crash-recovery-2026-10-23.json)、[按需启动](local-service-on-demand-2026-10-23.json)、[WSL 监督进程](local-service-supervisor-recovery-2026-10-23.json)。网关强制结束测试中 Docker 的 RestartCount 未增长，自动拉起由监督机制完成；不把该测试归因于 Docker 重启策略。

[数据库核对结果](local-service-audit-2026-10-23.json) 确认五个 action、attempt、execution 标识与客户端结果一致，每次都有 `EXECUTION_INTENT` 和 `SUCCEEDED`，执行结果密文存在。最后一次 execution 为 `bd553928-1393-44eb-b782-17f64e3e7932`。

计划任务已实际运行并参与重启和按需启动测试，状态为 `Running`，权限为 `Limited`，配置当前用户登录触发。任务返回值 `267009`（`0x41301`）表示正在运行。[启动证据](local-service-startup-2026-10-23.json)

## 数据、凭据与历史保留

沿用原 PostgreSQL 数据卷，没有重建数据、重新播种、执行迁移或生成身份。修复前 78 条 action/attempt/execution 和 181 条审计保留；五次新调用后分别为 83 条和 191 条审计，历史 181 条审计仍存在。加密密钥、员工权限、scope 和身份到期时间保持原值。

旧网关和数据库已改名为 `*-before-20261023t043447611352z` 并保持停止，私有配置备份于 `.dev/local-service/before-20261023t043447611352z/`。Codex 原配置另有私有备份。运行配置及认证凭据位于 Git 忽略的 `.dev/`，不写入本记录或共享客户端配置。

原固定 HTTPS 方案仅作为历史记录保留，状态已标记为 `NOT_PURSUED_LOCAL_DIRECT_SELECTED`。

## 验证边界

- 这是实际注册的本机 MCP 连接及工具调用验证。使用临时内存客户端上下文，未发起模型回合、未创建持久聊天，也未以 HTTP 模拟调用替代客户端调用。
- 尝试复用当前运行聊天时，官方客户端返回 `already has an active writer`；改用临时客户端上下文完成验证，没有中断当前聊天。当前聊天中已有的工具目录是否刷新未作成功声明。
- Windows 登录触发已配置，计划任务运行路径已实测；没有重启整台 Windows，因此整机冷启动和真实注销后登录尚未验收。没有全局关闭 WSL 或终止其他项目工作负载。
- Alice 仍为原来的限时测试身份，本次核验时有效、可读且属于 `scope_a`；到期时间仍为 **2026-10-23 22:48:35（新加坡时间）**。服务恢复不会自动延长凭据寿命或重新启用失效身份。

## 复验

在仓库根目录运行：

```powershell
powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Restart
node tests/local_mcp_verify.cjs --output .dev/local-service/read-result.json
```

九项针对性保护测试通过，覆盖容器归属、回环范围、固定端口、DSN 凭据与选项保留、运行环境秘密隔离、拒绝不合法配置及显式停止语义；两个 Node 脚本语法检查与补丁空白检查通过。

```powershell
wsl -d Ubuntu-24.04 --cd /mnt/c/dev/projects/MCPGuard -- python3 -m unittest discover -s tests -p test_local_service.py -v
```

接口及配置依据：[OpenAI MCP 配置](https://learn.chatgpt.com/docs/extend/mcp)、[官方 app-server 接口](https://learn.chatgpt.com/docs/app-server)、[Docker 重启策略](https://docs.docker.com/engine/containers/start-containers-automatically/)。
