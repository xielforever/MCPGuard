# 本机 gateway.health 接入与验证

日期：2026-10-23（Asia/Singapore）。状态：**已实现并部署；当前聊天原生工具入口尚未更新，原生调用待验收**。

## 工具契约

`gateway.health` 只接受 `{}` 或省略参数。未知字段、地址、SQL、身份选择器、超时覆盖、非对象、重复/续接内容及续传输入均拒绝。工具标记为只读、幂等、非破坏性。

| 字段 | 含义 |
|---|---|
| `live` | 本次请求由仍在运行的网关处理；依赖失败不会把它改成 false |
| `ready` | 元数据和业务 PG 两项运行依赖都在本次预算内通过检查 |
| `dependencies.metadata` | 元数据依赖的 `ready` 与安全 `status` |
| `dependencies.businessPostgres` | 业务 PG 依赖的 `ready` 与安全 `status` |
| `checkedAt` | 本次检查完成时间，UTC |
| `timeoutMs` | 总检查预算 2000 毫秒；更早的请求取消/期限优先 |

每项依赖状态仅为 `READY`、`UNAVAILABLE`、`TIMEOUT` 或 `CANCELLED`。`live=true、ready=false` 是成功完成健康检查后的有效状态，MCP `isError` 不因此为 true。参数错误、身份无效/无法验证、并发槽已满则返回工具错误，不提供依赖记录或内部错误。

## 检查内容与权限

- 元数据：schema 版本 2、非管理员且可写的运行角色、必要表/列可访问、审计与 Action/Attempt/Execution 所需 SELECT/INSERT/列 UPDATE 权限、授权锁函数及审计序列权限。
- 业务 PG：只读会话、SELECT-only 运行角色、固定业务表所需列可访问。
- 两项检查并行执行，使用网关现有连接池与运行账号，并复用请求取消和并发上限。查询不接收调用者指定目标。
- 表检查使用 `LIMIT 0`，不读取业务行；不插入审计、执行或身份记录。输出只有布尔值、状态与时间，不含 SQL、DSN、凭据、数据库原始错误或个人业务数据。
- 健康工具面向每个有效身份，调用时重新检查主体有效性；Visitor 可以检查健康，但仍不能直接调用 PG 业务读取。
- `ready=true` 不代表该员工拥有业务操作权限，也不代表写入、企业身份源、通知平台或所有后续流程均已验收。

`GET /healthz` 继续只表示进程存活，与后台监督循环兼容，不会因某个业务依赖异常而伪装成进程退出。MCP 身份验证仍依赖元数据：若该库完全不可访问，认证可能先失败，此时用 `/healthz` 独立判断进程存活，不能为了查询健康绕过认证。

源码：[MCP 工具与汇总](../../internal/gateway/health.go)、[元数据探测](../../internal/metadata/health.go)、[业务 PG 探测](../../internal/nativepg/health.go)、[目录权限](../../internal/gateway/gateway.go)。

## 已完成的开发验证

- Go race 检查通过：gateway、metadata、oauthfixture、runtimeconfig。nativepg 无独立测试文件，由 gateway 的真实 PG 测试覆盖新探测方法。
- 网关六个单元测试和真实 PG 读取集成的 12 个子测试通过。
- 新健康测试覆盖无参数契约、安全输出、Schema、探测并发、有界超时，以及不依赖数据库的 `/healthz`。
- 独立 PostgreSQL 夹具上撤销业务表 SELECT 权限：返回进程存活、业务依赖不可用、整体未就绪，元数据仍就绪；`/healthz` 返回 200。
- 撤销审计 INSERT 权限：元数据依赖不可用、业务依赖仍就绪、整体未就绪；`/healthz` 返回 200。
- 业务表持有排他锁：健康检查在预算内返回未就绪；释放锁、恢复权限后恢复就绪。
- 停用身份后，已存在的独立客户端会话无法继续查询健康。
- HTTP/stdio/PG 回归 56 项通过，包含三工具目录、健康调用、本人审计、业务范围、取消与重启恢复。

这些测试使用 `mcpguard-gateway-health-test-20261023` 独立夹具，没有在本机 Alice 的真实连接上撤权或锁表。首轮测试发现停用身份后 SDK 会关闭被调用的客户端，影响后续共享 Visitor 测试；已改用独立会话，并复验全部通过。首次日志保存在 `.dev/gateway-health-isolated-test-2026-10-23/go-integration-first.log`，未覆盖失败记录。

## 本机部署

2026-10-23 22:15:54（Asia/Singapore，14:15:54 UTC）完成部署。网关仍为 `http://127.0.0.1:18080/mcp`，使用原本机身份、固定端口、启动恢复与自动续期机制。

- 旧二进制 SHA256：`72a29ed847de1316c3711f606e446a1da3eb97c879e1e08047957b3856c31ae7`。
- 新二进制 SHA256：`d7d8e7fe567844d3f17816eccc6f43c6bb675765fcb36c4b13592d9f940c9835`，与容器内实际文件一致。
- 旧二进制保存在 `.dev/local-service/before-gateway-health-20261023T141546Z/`。
- 只重启本项目网关；更新前后均为 92 Actions、92 Attempts、92 Executions、209 审计事件，最大审计序号 209。数据、权限和身份配置未因部署改写。

## 当前聊天原生验收状态

部署后模型可见的原生目录仍只有 `mcp__mcpguard_local__audit_list` 和 `mcp__mcpguard_local__pg_get_demo_item`。用户已确认“已刷新本机连接”，但本轮执行期间再次检查，`gateway.health` 的原生入口仍未出现，函数也不可调用。

**尚未执行当前聊天原生 gateway.health 调用。** 上述开发测试和部署存活检查没有替代该验收，未用脚本、另起 app-server 或云端插件作为原生调用结果。

用户随后要求先完成功能，再统一验证，避免逐项重启 Codex 干扰其他任务。依此将本工具纳入批次结束后的统一原生验收，不再逐项要求刷新或重启。

集中验收时重新读取工具目录；入口出现后，直接调用本机 `gateway.health`，参数 `{}`，保存实际 `live`、`ready`、两项依赖状态和时间，再将本记录更新为通过。不需要再次实现或部署已完成的代码。

机器记录：[检查结果、部署来源与待验收状态](local-gateway-health-2026-10-23.json)。
