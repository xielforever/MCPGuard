# 本机受控工具配置

本批使用当前网关和 PostgreSQL 元数据，不挂载旧代理、SQLite 路由或验证码确认流程。原设计、旧原型与历史验收记录保留。

## 工具与身份

本机演示配置中，Alice 保持原有 `scope_a`。新增五个工具授权只允许申请固定的演示操作；不会授予管理或审批角色。

| 工具 | 行为 |
| --- | --- |
| `pg.get_demo_item` | 原有只读链路、原有永久执行记录 |
| `pg.update_demo_note` | 固定 UPDATE，字符串版本号、乐观锁、一行上限；他人审批后恢复 |
| `redis.get_demo_note` | 固定作用域命名空间，仅 `welcome`、`help` |
| `redis.set_demo_note` | 固定 SET EX，TTL 60–3600 秒；他人审批后恢复 |
| `directory.get_demo_office` | 固定 GET 路径，仅 `office-demo` |
| `inventory.reserve_demo` | 固定 POST，数量 1–10；他人审批后恢复 |
| `action.status/resume/cancel` | 查询、显式恢复或取消本人操作；没有模型可调用的批准工具 |
| `history.list/get_result` | 本人操作历史和存储结果，兼容原 PG 读取记录；不重复访问原生后端 |
| `audit.list` | 本人审计，十进制字符串游标，每页 1–100 条 |
| `gateway.health` | 区分进程存活与已配置依赖就绪；共享两秒预算 |

`history.list` 的 `after` 是服务端返回的不透明游标，每页 1–100 条，按创建时间及 ID 排序。`history.get_result` 只接受本人 `actionId`。历史只读操作支持 `action.status`，不通过恢复工具重新执行。

这些是演示资源的真实原生适配器。HTTP 演示后端的库存和预留记录保存在业务 PostgreSQL；Redis 使用独立持久卷。它们不是已配置的企业生产数据源。

## 审批与恢复

模型申请写入后取得 `DEFERRED`、操作 ID、到期时间及本机控制台链接。待审批时可查询或取消；另一位有资源审批权限的主体批准后，申请者调用 `action.resume`。审批回调只保存决定，不执行操作。

计划冻结主体、范围、工具/资源版本、完整参数、配置版本、凭据绑定和到期时间，用 HMAC 绑定并加密保存。编码使用本实现的确定性 JSON，不宣称通用跨语言 JCS。数据库约束保护计划、审批、审计和永久唯一执行资格。

规则 DENY 优先；ALLOW 不授予缺失的业务权限，也不能跳过写审批。组授权只在组启用且用户为成员时生效；主体上明确的 false 优先于组授权。读取、审批、恢复和目录过滤使用同一组授权函数。资源关闭、权限撤销或配置变化后，旧计划不能继续执行，需重新申请。

取消已派发操作只是取消请求。网络断开、提交回应丢失或过期执行意图会保守记为 `OUTCOME_UNKNOWN`，保留可能存在的副作用，不能自动重放。通知 worker 和恢复 worker 都不会派发业务写入。

## 本机控制台

入口是 [本机控制台](http://127.0.0.1:18080/web/)。当前受维护的页面随网关内嵌发布；`web/` 下的旧 Vue 原型保留为历史实现，不是当前控制台。页面读取真实状态，提供冻结计划审批、资源、规则、现有用户权限、用户组、本人审计和通知状态。

浏览器使用独立随机登录密钥、HttpOnly/SameSite Strict 会话、30 分钟期限、逐请求身份重验、固定 Host/Origin 及 CSRF 校验。MCP Bearer 不能用于审批 API。管理变更必须带 `If-Match` 和 `Idempotency-Key`；同键同载荷的已完成操作可在旧 revision 下重取，冲突载荷被拒绝。

本机演示部署生成独立 `operator` 身份：没有业务读取权限，具备演示资源审批和控制台管理权限；当前 Alice 不具有自批权限。私有登录信息存放于 `.dev/local-console-login.json`，不进入 Git、MCP 输出或通知。此为单机演示身份隔离；企业人员身份验证仍待用户选定 SSO 后接入。

本地模式依照用户要求使用固定环回 HTTP，浏览器会话不设置 Secure；不适用于公网。原设计 HTTPS schema 原样保留；运行时使用明确允许环回链接的本地派生 schema。

## Telegram

用户确认 SSO 待定、通知优先 Telegram。通知 Outbox 已持久化，限定一个明确的组织和接收目标，最多尝试五次，租约过期可重新投递。投递为至少一次，崩溃可能导致重复通知，不会导致重复业务执行。

通知只包含操作 ID、安全摘要和本机入口，不包含参数、业务结果、令牌或审批验证码。Bot API 回应必须为 HTTP 200 且 `ok=true` 才记为已送达。未配置时保持待配置状态，不发送真实消息。

配置文件内容见 [Telegram 模板](../deploy/local/telegram.example.json)。把填写后的文件保存在私有目录 `.dev/local-service/controlled-runtime/telegram.json`，再将 `.dev/env.json` 的 `MCPGUARD_NOTIFICATION_CONFIG` 设置为该文件的 WSL 绝对路径，并重新运行 `scripts/local_controlled.py setup` 更新网关运行配置；无需重启 Codex。此步骤应由本机操作员完成，密钥不要发在聊天中。

Telegram 手机端的 `127.0.0.1` 链接指向手机本身；审批需在运行 MCPGuard 的电脑上打开控制台。本批不引入公网入口。协议实现参考 [Redis RESP](https://redis.io/docs/latest/develop/reference/protocol-spec/) 和 [Telegram Bot API](https://core.telegram.org/bots/api#sendmessage)。本地接收器测试通过不等于真实 Telegram 已送达。

## 本地测试与运行

构建产物先写入 `.dev/controlled-build/`。`scripts/controlled_fixture.py prepare/test` 只使用名字明确的隔离容器和私有状态，不重置当前本机数据。测试覆盖真实 PG、Redis、HTTP、审批/重放边界、HTTP/stdio 和浏览器交互。开发脚本及 SDK 测试不作为本聊天原生工具验收。

通过本地测试后，在 WSL 中运行 `python3 scripts/local_controlled.py setup` 进行一次本机服务更新。迁移前保存元数据备份、原二进制、私有配置和旧容器。原 `demo.items` 不重置，原只读执行与审计记录不改写；新功能使用新的受控账本，并共享审计序列保持原游标可用。

本机 supervisor 接管 PostgreSQL、Redis、演示 HTTP 后端和网关，保留原有 Alice 凭据续期策略。未注册其他身份的自动续期，不会因重启恢复被撤销的授权。新增工具在客户端目录缓存未更新时先记录待原生验收，实施期间不要求重启 Codex。
