# Windows / WSL 本机执契服务

本机 Codex 通过 Streamable HTTP 直接连接 `http://127.0.0.1:18080/mcp`。服务运行在 WSL `Ubuntu-24.04` 的 Docker 中，不需要域名、HTTPS 隧道或云端插件。

本机执契服务沿用 `mcpguard_local` 接入配置。两个旧云端插件 `MCPGuard Local Read`、`MCPGuard Local Read Restored` 均已不再安装；移除后本机原生工具直接读取复验通过。[记录](validation/local-only-plugin-cleanup-2026-10-23.json)

本运行器接管已有的本地验证夹具：`.dev/env.json` 的容器名为 `mcpguard-local-verified-postgres`、认证为 `dev-tokens`、网关监听为 `127.0.0.1:18080`。不会重新初始化测试数据、执行迁移、生成新身份或覆盖其他项目容器。本机 Alice 凭据已按用户要求单独开启自动续期。

## 控制服务

在仓库根目录的 PowerShell 中运行：

```powershell
# 首次安装：保留原数据卷和凭据，归档旧容器，固定数据库端口并注册启动项。
powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Install

powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Start
powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Restart
powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Status
powershell.exe -NoProfile -File scripts/local-service.ps1 -Action Stop
```

当前机器已完成一次性自动续期登记。其他已有夹具若要启用，可执行 `-Action EnableAutoRenew`；普通安装不会替其他身份默认开启续期。

数据库固定映射为 `127.0.0.1:18432 -> 5432`，网关保持 `127.0.0.1:18080`。端口被其他服务占用时安装会失败，不会结束占用者。原容器改名归档，数据卷、加密密钥、员工凭据、权限和历史审计保持原值；私有配置备份在 `.dev/local-service/before-*/`。

`Stop` 停止本项目的网关、数据库和监督循环，保留容器与数据。以后主动 `Start` 或重新建立本地 MCP 连接会按需启动。不要用会清除测试数据的 `dev.py down` 代替服务停止。

## 启动与恢复机制

- 两个服务容器配置 `unless-stopped`；后台监督循环也会修复意外停止的本项目容器。
- Windows 当前用户登录时，计划任务 `MCPGuard Local Service` 以普通用户权限运行隐藏的 PowerShell 监督进程。无权注册任务时，使用当前用户 Startup 快捷方式，不提升权限。
- PowerShell 持有前台 WSL 命令，使 WSL 服务不依赖某个临时终端；本项目 WSL 监督进程退出后，外层进程会重试。Windows 与 Linux 分别加锁，重复启动不会产生多个有效监督实例；停止与恢复操作串行执行，避免停止时又被拉起。
- 网关只接受回环监听。网关容器只挂载网关二进制并接收必要的运行环境，不挂载整个仓库、Docker socket、管理员 DSN 或员工明文 token。

计划任务负责当前用户登录后的启动，不宣称在无人登录的 Windows 启动阶段可用；整机冷启动尚需真实重启 Windows 验收。不会为了验证本项目而结束整个 WSL 或其他工作负载。

## Codex 本地连接

现有 `[mcp_servers.mcpguard_local]` 的 URL 保持不变；认证头辅助命令切换到 [mcp-local-headers.cjs](../scripts/mcp-local-headers.cjs)，启动超时为 120 秒。

辅助命令先确认本机服务就绪，再从受权限保护且被 Git 忽略的 `.dev/env.json` 读取 Alice 测试 token。stdout 只输出认证头 JSON；启动日志和秘密不进入 Codex 共享配置。不在每次工具调用时启动进程，辅助命令由 Codex 在建立连接或按其规则刷新认证头时调用。

```toml
[mcp_servers.mcpguard_local]
url = "http://127.0.0.1:18080/mcp"
http_headers_helper = '"C:/Program Files/nodejs/node.exe" "C:/dev/projects/MCPGuard/scripts/mcp-local-headers.cjs"'
startup_timeout_sec = 120
tool_timeout_sec = 30
enabled = true
```

## 本人审计原生工具

本机 `mcpguard_local` 提供 `pg.get_demo_item`、`audit.list` 和 `gateway.health`。审计工具只读取当前连接身份的审计事件摘要；服务端按组织和主体过滤，并在每次查询时重新核对身份是否有效。调用参数不允许包含用户、组织或数据范围选择器。

`audit.list` 的参数示例为 `{"after":"0","limit":2}`。省略 `after` 从开头读取；省略 `limit` 默认 100，每页只允许 1–100 条。把返回的 `nextAfter` 原样作为下一页的 `after`；空页保留原游标。`after`、`nextAfter` 和事件 `seq` 使用十进制字符串，避免客户端丢失 bigint 精度。既有 HTTP 审计接口继续使用原数字格式。

审计权限沿用已有 HTTP 审计接口：有效身份可以读取自己的记录，即使没有 PG 业务读取权。Alice、Bob 可见三个工具；Visitor 可见 `audit.list` 和 `gateway.health`，直接调用 PG 工具仍然拒绝。身份停用、到期或凭据改变后，既有连接也不能继续查询审计。结果仅含事件 ID、类型、时间和关联 ID，不含令牌、SQL 或历史业务结果。

在聊天中可直接要求：“用本机 mcpguard_local 的原生 audit.list，after 为字符串 0，limit 为 2。”工具目录更新后应通过当前聊天的原生工具直接验证；客户端脚本结果不能替代该验收。

## 健康检查原生工具

`gateway.health` 只接受空参数 `{}`，区分两个事实：`live` 表示本次请求由仍在运行的网关处理；`ready` 表示当前元数据与业务 PG 两项依赖的检查都通过。依赖异常时可返回 `live=true、ready=false`，这是成功执行健康检查后的结果，不把工具调用标为错误。

每次调用并行检查两项依赖，总预算为两秒，返回 `checkedAt`、`timeoutMs`，以及 `dependencies.metadata`、`dependencies.businessPostgres` 各自的 `ready` 和安全状态 `READY`、`UNAVAILABLE`、`TIMEOUT` 或 `CANCELLED`。没有使用缓存或调用者指定的地址。

元数据检查包含 schema 版本 2、低权限运行角色、必要表/列、审计/资格所需权限与序列权限；业务 PG 检查包含只读会话、SELECT-only 角色和固定业务表的必要列。表检查使用零行查询，不读取业务行或写入审计、执行和身份记录。状态不返回连接串、凭据、SQL 或数据库原始错误。`ready=true` 不代表当前员工有业务读取权，也不代表全链路写入、企业身份源或通知服务已验收。

现有 `GET /healthz` 仍只表示进程存活，HTTP 200 不能证明依赖就绪。MCP 工具继续要求有效身份；元数据库完全不可用、导致身份验证无法完成时，MCP 请求会先认证失败，此时通过 `/healthz` 单独判断进程是否仍在运行。

在聊天中可直接要求：“用本机 mcpguard_local 原生调用 gateway.health。”工具目录刷新后，以当前聊天的实际原生返回作为最终验收。

## 功能接入与集中验收

按用户 2026-10-23 的要求，先完成本批已明确的功能、相关实现及本地回归，再统一进行原生工具验收。开发期间不按工具逐项要求重启 Codex；工具目录需要刷新时，在批次结束后集中安排，避免干扰其他任务。

尚未完成原生调用的功能保留“待统一验收”状态，开发测试、服务存活或独立客户端结果不能替代原生验收。已通过的实际原生结果和未完成的验证记录分别保留。

## 本机凭据自动续期

2026-10-23 已为此机器的 Alice 本地读取凭据开启自动续期，不再沿用首次播种的 24 小时到期时间：

- 每次续期后有效 30 天，剩余 7 天时提前延长；后台每分钟检查一次。尚未进入续期窗口时，不更新数据库，也不提升授权版本。
- 建立本机 MCP 连接时也会同步检查。即使长时间关机导致身份已经过期，服务启动后会先续期，再交给客户端调用；网关已运行时同样会检查，不能只凭进程健康就判断连接可用。
- 沿用现有 token，因此已连接客户端不需要每天替换认证头、重新接入或重新授权。
- 只允许续期已登记的本机 Alice、原 token 哈希和 `scope_a`。身份被停用、读取权限被撤销、scope 改变或凭据被替换时拒绝续期，不会恢复这些授权。Bob、Visitor 和独立 OAuth 夹具不受影响。
- 私有登记在 `.dev/local-service/credential-policy.json`，检查状态在 `credential-status.json`，实际续期事件追加到 `credential-events.jsonl`。登记与状态不保存明文 token；网关继续使用原来的低权限数据库角色，续期由本机服务控制器执行。

这是本机 `dev-tokens` 测试夹具的运维策略，不改变生产身份、OAuth 或网关的到期鉴权规则。[实现](../scripts/local_credentials.py)；[续期验证记录](validation/local-credential-renewal-2026-10-23.md)。

## 验证

[验证脚本](../tests/local_mcp_verify.cjs) 通过官方 Codex app-server 的 `mcpServer/tool/call`，调用实际配置的 `mcpguard_local`，不启动模型回合或使用云端插件。默认只创建临时内存客户端上下文，不新增持久聊天，也不抢占正在运行的聊天。

这类客户端验证证明独立连接能执行读取，不等于当前聊天已经加载模型可直接调用的原生工具。当前聊天曾在原生初始化时返回 HTTP 502；重启后，已于 2026-10-23 14:07:02（新加坡时间）通过当前聊天的本机原生工具直接读取成功。故障与恢复过程见 [当前聊天原生连接记录](validation/local-native-connection-502-2026-10-23.md)，实际结果见 [原生调用证据](validation/local-native-tool-read-2026-10-23.json)。

```powershell
node tests/local_mcp_verify.cjs --output .dev/local-service/read-result.json
wsl -d Ubuntu-24.04 --cd /mnt/c/dev/projects/MCPGuard -- python3 -m unittest discover -s tests -p test_local_service.py -v
```

实际读取、异常退出恢复、数据库/网关重启、按需启动、监督进程恢复以及对应审计结果见 [2026-10-23 记录](validation/local-service-recovery-2026-10-23.md)。该历史记录中的“未改身份到期时间”描述先前服务修复阶段；其后的续期授权和验证见上述独立记录。`healthz` 只表示进程存活，验收以真实工具结果与持久审计关联为准。

续期相关保护测试（包含短暂更改 Alice 权限、并在清理阶段恢复的本机数据库测试）：

```powershell
wsl -d Ubuntu-24.04 --cd /mnt/c/dev/projects/MCPGuard -- env MCPGUARD_TEST_LOCAL_RENEWAL=1 python3 -m unittest discover -s tests -p 'test_local_*.py' -v
```

配置依据：[OpenAI 本地 MCP 配置](https://learn.chatgpt.com/docs/extend/mcp)、[app-server 接口](https://learn.chatgpt.com/docs/app-server)、[Docker 重启策略](https://docs.docker.com/engine/containers/start-containers-automatically/)。

## 受控演示批次

2026-10-23 后续批次新增 Redis 和演示 HTTP 后端，受控功能与控制台说明见 [本机受控配置](controlled-local.md)。supervisor 根据受保护的固定服务清单接管这两个后端；原有启动、重启和停止命令现在统一管理 PostgreSQL、Redis、演示 HTTP 及网关。当前 Chat 的原生工具目录验收进展见 [本批检查清单](local-batch-checklist.md)。

Redis 使用本项目持久卷、显式运行用户和 `volume-nocopy`，避免镜像初始化覆盖卷目录所有者。首次部署的权限失败现场、元数据备份和旧容器均保留；修复后已验证停止 Redis/HTTP 后端时 supervisor 自动拉起，Redis 值在重启后保留。未重启 Codex。
