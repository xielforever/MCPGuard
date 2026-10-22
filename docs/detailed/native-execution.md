# 原生执行详细设计 v0.1

状态：设计提案，待评审，尚未实现。日期：2026-10-22。风险分级与受控目录已确认，六个具体示例工具仍为候选，未冻结。

依赖 [调用契约](../contracts.md)、[原生适配总览](../native-adapter-design.md)和[需求验收](../requirements.md)。IDE 经 HTTP 或 stdio 转 HTTP 进入同一 MCP 网关；下游仅使用 PG、RESP、HTTP，不要求服务实现 MCP。

首发客户端已确定为 ChatGPT；具体账号类型、接入能力和协议 profile 仍需核实，不据产品名称预设能力已经兼容。

## 1 技术边界与组件

建议 Go 使用 `pgx/v5` 与 `pgxpool`、`go-redis/v9`、标准库 `net/http`。具体补丁版本、Go 版本、支持的服务版本和配置必须在实施前锁定；主版本兼容不代表重试或取消行为已经验证。

网关元数据 PG 负责 Action、批准、Execution 领取和审计；业务 PG 是被纳管原生资源。两者使用不同逻辑连接、角色、凭据和池，不借元数据账号操作业务库，也不把跨库提交当成同一事务。

执行协调器负责派发资格和持久状态；Adapter 负责确定原生计划、技术校验、原生连接及证据。Adapter 不认定人类是否批准，不接受 MCP JSON 原文，不从用户参数取连接串或认证头。

服务位于企业服务器或员工本地不改变其类型；地址已可达。此处仅约束目标准入、认证和执行安全，不安排网络工程或部署员工侧执行代理。

## 2 Go 伪接口与职责

以下为拟议边界，类型细节留待代码实现；不是现有 SDK 或已落地 Go 包。

```go
type Adapter interface {
    Plan(context.Context, PlanInput) (NativePlan, error)
    Validate(context.Context, NativePlan, ResourceSnapshot) (Validation, error)
    Execute(context.Context, ExecuteInput) NativeResult
}
type ReadOnlyReconciler interface { // 可选能力，只查询，不重发操作
    Reconcile(context.Context, ReconcileInput) (NativeObservation, error)
}
type PlanInput struct {
    ActionID string
    Resource ResourceSnapshot
    Operation OperationSpec
    Arguments json.RawMessage
    Scope DataScope // 服务端根据可信主体推导，不取模型自报范围
}
type NativePlan struct {
    ActionID, ResourceID, ResourceVersion string
    OperationID, OperationVersion, AdapterVersion string
    BackendIdentity, CredentialVersion string
    PayloadRef, PayloadHash, PreconditionsRef string
    Limits Limits
}
type ExecuteInput struct {
    ExecutionID string
    Plan NativePlan
    Credential CredentialHandle // 内部短时引用，不返回调用者
    Deadline time.Time
    Journal DispatchJournal // 仅记录事实，不授予执行资格
}
type NativeResult struct {
    RequestStatus RequestStatus
    DispatchState DispatchState
    SideEffectStatus SideEffectStatus
    Data any
    Evidence []NativeEvidence
    Error *SafeError
}
```

`Plan` 只解析已注册 operation、模板及可信数据范围；不执行写入，也不预占事务。需要原生元数据查询时，仅使用已审核的只读查询并记录版本及观察时点。

`Validate` 检查计划摘要、资源/凭据版本、模板兼容性及技术前置条件，不能代替中央鉴权。对变化数据的执行前置条件必须在短执行窗口内重检，不能靠先前 Validate 保证不变。

`Execute` 仅接受协调器已领取的 Execution；不自行重新 Plan、不循环重试、不扩大 key/行/URL 范围。Deadline 与取消经 context 传递，清理使用单独的受限上下文，避免取消后直接跳过回滚。

可选 `Reconcile` 只读原目标的执行回执或业务证据，同样受只读模板、凭据与范围限制，由协调器追加观察和决定状态；不能隐式调用 Execute、补写、删除或重新提交。只看到当前值相同，不足以证明某次写入已经执行。

## 3 计划冻结、范围注入与凭据

先核验工具、资源与主体准入，再生成完整计划并评估策略。NativePlan 纳入 OperationPlan 的哈希，绑定原生目标、工具/模板/适配器版本、完整参数、实际身份、凭据版本、范围、期限、限制及前置条件。

参数存受保护快照；摘要覆盖未脱敏业务内容，不覆盖明文密码。审批展示可脱敏，但执行仍来自同一完整快照；不能批准后重新拼接另一份业务请求。

数据范围由可信主体和资源配置注入，例如 `team_demo_a`。工具输入即使含相同字段也不能覆盖；资源、schema、表、key 前缀、路径和租户标识均需分别验证，不能只检查 resource ID。

PG 标识符来自受控模板；值使用绑定参数。Redis 使用完整 argv 与 key 字节范围。HTTP 固定 origin、路径模板与方法，再对 path/query/body 分别编码，不能将用户参数解释为新 URL。

派发前重新检查主体资格、计划期限、最新禁止策略、资源启停及相关版本。凭据轮换或后端身份变化使旧计划失效，不在后台悄悄换身份执行旧批准。

已确认按风险分级：获授权的普通读取自动放行，受控写必须由他人审批，高危操作禁止。此层只消费已验证的策略授权或合格他人的决定；自批不满足受控写要求，普通批准不能覆盖禁止或越权。

## 4 受控工具目录提案

已确认 v0.1 采用受控目录：PG 固定模板、Redis 限定命令及 key、HTTP 固定 operation，不开放任意 SQL、Redis 命令或 URL；未来版本如需扩展另议。以下六个具体工具全部使用假数据、假资源与假业务名称，仍是候选，未冻结清单。

| 工具候选 | 固定资源与 operation | 模型输入 | 服务端注入与约束 |
|---|---|---|---|
| `pg.get_demo_item` | `demo.pg` / `demo.item.read.v1` | `itemId` | `scopeId`、固定列、最多一条记录 |
| `pg.update_demo_note` | `demo.pg` / `demo.item.note.update.v1` | `itemId,note,expectedVersion` | `scopeId`、允许列、期望版本、最多影响一行 |
| `redis.get_demo_note` | `demo.redis` / `demo.note.get.v1` | `noteId` | `demo:{scope}:notes:{noteId}`，验证每段编码 |
| `redis.set_demo_note` | `demo.redis` / `demo.note.set.v1` | `noteId,value,ttlSeconds` | 同一 key 模板、值大小及 TTL 范围；不接受任意选项 |
| `directory.get_demo_office` | `demo.directory` / `getDemoOffice.v1` | `officeId` | 固定 GET 与 `/demo/offices/{officeId}`，可见办公室范围 |
| `inventory.reserve_demo` | `demo.inventory` / `reserveDemo.v1` | `itemId,quantity` | 固定 POST `/demo/reservations`、项目范围和请求体 Schema |

`additionalProperties:false` 仅限制未知字段，不替代权限、值域与范围检查。示例 ID 如 `item_demo_1`、`note_demo_1` 不映射生产数据；示例数量与期限由测试 fixture 指定，不据此冻结产品默认值。

## 5 PostgreSQL 读取与最小权限

使用审查后的固定查询模板和扩展查询参数绑定；不启用接受用户 SQL 的 simple-query 入口。动态标识符只从授权模板元数据选择，不用参数占位符冒充标识符转义。

读取角色仅授予必要数据库、schema、表/列及函数权限，不使用超级用户、任意角色切换或 BYPASSRLS。需要行级隔离时核对 RLS、表所有者行为与真实执行身份；共享服务角色不自动获得员工范围。

只读工具使用 `READ ONLY` 短事务、受控 search_path、statement/lock timeout 及结果行数/字节限制。会话配置由 Adapter 管理，不能接受用户 SET、COMMIT、COPY PROGRAM 或任意管理语句。

SELECT 首词、正则及 MCP readOnlyHint 都不能证明无副作用；写 CTE、多语句、SECURITY DEFINER、触发器、扩展与外部函数需要单独约束。数据库 READ ONLY 不是外部副作用沙箱，也不保证敏感数据或资源消耗安全。

只读函数和视图必须来自可信定义及固定版本；未知函数、外部访问与可替换 schema 不进入默认模板范围。若无法证明模板的只读边界，不按 READ 自动放行。

假数据读取模板：`SELECT item_id,note,version FROM demo.items WHERE scope_id=$1 AND item_id=$2 LIMIT 1`。`$1` 来自可信范围，`$2` 来自已校验 itemId；列名不作为对象 key 覆盖重复列。

大整数、Decimal、时间和 bytea 按输出契约编码；限制实际读取工作量与输出，注明截断和观察时点。结果量上限不是查询成本上限，仍需数据库超时和并发配额。

## 6 PostgreSQL 受控写与提交边界

假数据更新模板：`UPDATE demo.items SET note=$3,version=version+1 WHERE scope_id=$1 AND item_id=$2 AND version=$4 RETURNING item_id,version`。

`$1` 为服务端范围，`$2/$3/$4` 分别为已批准的 ID、值与版本。上线前验证匹配键的唯一约束；不得只因期望影响一行便假定实际最多一行。

批准等待期间不持有事务。执行领取后获取业务连接并开启短事务，在同一事务内检查条件、执行模板、核对实际影响行数和返回值；零行按冲突处理，超限在提交前回滚，不仅截断返回结果。

触发器、级联和函数副作用不由主语句行数完整表示，需纳入模板评审。原生事务只能覆盖同一业务 PG 中可回滚操作，不覆盖外部函数、Redis、HTTP 或网关元数据 PG。

确认 PG 回滚仅证明本事务的可回滚效果已撤销；存在外部副作用而证据不足时不能报告整体 ROLLED_BACK，仍需保留 UNKNOWN 或已知部分效果。

只在取得 COMMIT 成功证据后报告写成功；执行阶段已取得 RETURNING 数据并不证明事务已提交。COMMIT 已调用而响应丢失时保留未知，不因 context 取消、socket 关闭或驱动返回 error 就宣称回滚。

SQLSTATE、约束错误或序列化失败不是自动重跑整个业务操作的许可；即使能确定本地事务回滚，也要遵循 Action 的执行资格和显式恢复契约，不能隐藏成一次正常派发。

不使用 EXPLAIN ANALYZE 作为无副作用预览。外部可见数据变化导致前置条件不成立时终止或重新计划，不重新生成更宽 UPDATE 去满足旧批准。

## 7 Redis 命令、TTL 与连接状态

使用 `go-redis/v9` 的类型化命令或受控 argv，配置专用 ACL 用户、最小命令集合及 key 范围。读取角色和写入角色分开；不能从 AUTH 成功推导员工拥有全部 key 权限。

key 由模板与可信 scope 构造，对 noteId 限定字符或明确编码，防止分隔符、通配符和编码差异扩大范围。多 key 工具必须枚举并检查全部 key；SCAN 枚举不假定由 key ACL 自动过滤。

SET 提案固定 `SET key value EX ttlSeconds` 语义；TTL 必须在资源策略允许范围内，零值不能隐式变为永久保存。是否 NX、XX 等条件写需另行注册并绑定计划，不能接收任意选项。

相同 SET 重发可能刷新 TTL、覆盖并发数据，因此不能因值相同就视为可安全重试。GET 不存在返回 `found:false`，与空字符串、空字节或原生错误分别表达。

普通工具不允许 AUTH、SELECT、CLIENT、CONFIG、ACL 或模块管理改变共享连接状态。Lua、FCALL、MULTI/EXEC 与 pipeline 不进入默认通用入口；确需使用时审批完整命令/脚本和全部 key，并核验服务版本。

Redis 原子执行不提供错误后的通用回滚；事务或脚本可能部分生效。命令发出后取消等待不能撤销写入，未读响应或未知协议状态的连接不得直接归还池。

显式设置 `MaxRetries:-1` 禁止 go-redis 的普通自动重试；`0` 会使用默认重试，并不表示关闭。Cluster/Sentinel 的重定向、故障切换和拓扑重试须另行审查，不能因普通客户端关闭重试而认定全部路径安全。

## 8 HTTP 目标、编码与传输

每个 HTTP operation 固定 scheme、host、port、允许 path 模板、method、输入输出 Schema 和认证来源。不能仅按 GET/POST 判断读写；语义由已审 operation 定义。

path 段单独编码并验证 `/`、反斜杠、点段及双重解码行为；query 用结构化编码，不允许业务参数替换 origin、端口、路径前缀或另一租户路由。绝对 URL、userinfo 和任意 fragment 不作为用户输入能力。

客户端只能提交 Schema 中的业务字段。Host、Authorization、Cookie、Proxy-*、转发身份、Connection 等受控头由网关或认证提供方生成；Content-Length 等由客户端库构造，不接受模型透传。

访问网关的 MCP token 不用作业务认证。凭据只在目标完成验证后按注册方式附加；资源配置的授权目标、TLS 与代理设置显式管理，不无条件继承进程环境代理或关闭证书校验。

默认 `CheckRedirect` 返回 `http.ErrUseLastResponse`，将 3xx 留给操作契约处理，不自动跟随。确需跳转时逐跳验证目标与方法变化，不跨 origin 携带认证；3xx 本身也不证明没有副作用。

为业务写显式控制重送：标准 Transport 对复用连接和可重放请求可能自动重发，`Idempotency-Key` 也影响判断；关闭业务层重试和 CheckRedirect 不等于关闭这些行为。

首版候选单次发送路径使用独立 HTTP/1.1 Transport、非复用连接并禁用自动跳转；读路径可单独复用。该取舍须按锁定 Go 版本以故障计数实测；需要 HTTP/2 或复用写连接时，先证明其重试边界，不虚构标准库 retry=false 开关。

HTTP 幂等键只有在业务服务确实持久校验相同键与请求摘要、返回既有结果并定义保留期时才可用于恢复。mock 服务可实现该契约；真实 endpoint 是否具备仍待验证，不能仅加 header 就声称可靠幂等。

按 deadline、响应头/体积和解析上限限制资源；响应体完整消费或关闭，结果脱敏。200 后 Schema 校验失败可能已经写入；401、403、429、5xx 均按原生语义记录，不自动触发网关员工重新登录或重放。

## 9 连接池、取消与凭据轮换

池键至少包含 resource、实际后端身份、凭据版本、目标/TLS/连接配置；存在用户特有会话状态时进一步隔离。每次复用仍重新鉴权，不能用共享服务账号继承前一员工权限。

PG 使用受控会话初始化和事务局部设置，释放前确认 rows 已关闭、事务已收口、连接协议空闲；无法确认则销毁连接。search_path、角色、临时对象和未提交事务不能交给下一调用。

Redis 必须读尽本次响应并确认模式正常；不允许 PUBSUB、MULTI 或阻塞命令状态跨调用残留。HTTP 用户 cookie、可变认证头和响应缓存不跨主体复用，连接复用本身不承载员工授权。

凭据轮换停止向旧池分配新执行，失效旧计划并排空/关闭旧连接；对已派发操作按事实收尾，不因轮换重发。凭据解析失败在发送前可报告未派发，不打印 DSN、密码、token 或完整认证头。

## 10 执行领取、journal 与恢复

执行协调器在元数据事务中检查当前资格，最多消费一次 Action 的派发资格，创建 Execution 并记录 `INTENT_RECORDED`。它表示领取与跟踪开始，不表示原生请求已经发送或被服务接受。

在可能开始原生业务写之前，先持久化该Execution副作用为UNKNOWN，成功提交后才调用驱动；记录失败就停止发送。这个阶段可以是RUNNING/INTENT_RECORDED/UNKNOWN，避免后端可能已生效而发送证据尚未落库时仍对外断言NONE。

元数据事务与业务请求不构成分布式事务。记录意图后崩溃，即使没有“发送完成”记录也可能已经发出；无法证明未发送时转 `DISPATCH_UNKNOWN`，不能由 lease 到期、重启或另一 worker 重新领取发送。

Adapter 依据驱动、协议与业务回执记录 `DISPATCHED` 或保持未知。TCP 写成功不证明服务处理或提交，TCP 写报错也可能已部分传输；不能仅凭本地错误构造 NONE 或 ROLLED_BACK。

执行者尚在正常推进时可保留 `RUNNING/INTENT_RECORDED`；拥有者丢失后不把它当待发送队列。取消与领取原子排序，领取后若仍能证明未发送可终止，但资格不自动重置供再次派发。

Reconcile 仅查询可信的 operation receipt、版本证据或原生事务结果。PG 当前行、Redis 当前值和 HTTP 当前资源状态若不能唯一关联 execution，仍保持 UNKNOWN；没有证据不“猜成功”。

保留原 Execution 与追加核对记录；再次业务尝试需要新 Action、重新授权并展示旧结果未知的风险。仅在已验证原生幂等能力时另行设计恢复协议，不能由 Adapter 暗中重试。

## 11 详细故障与结果表

下表沿用契约枚举；副作用状态以实际证据为准，FAILED、isError 与网络断开都不等于未生效。

| 故障或观察 | 派发状态 | 请求/副作用 | 处置 |
|---|---|---|---|
| Schema、范围、凭据解析在发送前失败 | NOT_DISPATCHED | FAILED/NONE | 返回脱敏原因；未领取前 executionId 为空 |
| 意图已落库，正常执行者尚未发出 | INTENT_RECORDED | RUNNING/NONE | 继续当前领取，不授予其他 worker 新资格 |
| 意图落库后崩溃，无法证明是否发送 | DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 保留 journal，不因 lease 超时重发 |
| 已审核纯读在同一崩溃窗口 | DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/NONE | 仅在只读边界已证明时记录 NONE，仍不自动重领 |
| PG 条件冲突或超行数且确认事务回滚 | DISPATCHED | FAILED/ROLLED_BACK | 记录实际行数/条件证据，不扩张旧计划 |
| PG COMMIT 返回前连接中断 | DISPATCHED 或 DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 区分已知调用与传输证据，只读核对，不重跑 |
| PG 回滚确认丢失且副作用无法确定 | DISPATCHED 或 DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 不凭回滚调用本身宣称已回滚 |
| Redis SET 已发出但响应丢失 | DISPATCHED 或 DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 不重置 TTL；废弃未知状态连接 |
| Redis 多命令明确有成功也有错误 | DISPATCHED | FAILED/PARTIAL | 保留逐项结果，不报告整体回滚 |
| HTTP 业务写超时或断开 | DISPATCHED 或 DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 禁止盲重试；有可信回执才允许核对 |
| 原生写成功已确认，结果 Schema 校验失败 | DISPATCHED | FAILED/APPLIED | 审计保留成功证据，不能因编码失败再写一次 |
| 派发前取消且确定未发送 | NOT_DISPATCHED | CANCELLED/NONE | 关闭当前路径；已领取资格不自动恢复 |
| 派发后取消但原生结果仍未知 | DISPATCHED 或 DISPATCH_UNKNOWN | OUTCOME_UNKNOWN/UNKNOWN | 尽力取消与清理，不承诺撤销副作用 |

## 12 验收与实施前决定

| 验收映射 | 必测证据 |
|---|---|
| AC-01、AC-04、AC-06 | 两员工/两范围；resource、模板、scope、参数及身份替换被拒，审批页与执行摘要一致 |
| AC-05、AC-07 | 获准普通读自动放行、受控写他人审批且拒绝自批、高危禁止；重复决定、续传与并发 worker 只消费一次派发资格 |
| AC-08 | 真实测试 PG/Redis 与 mock HTTP，无模型 key；两条 MCP 入口调用相同原生计划 |
| AC-09、AC-10 | 提交/响应丢失、部分副作用、lease 过期和重启；记录结果未知并核对实际请求计数 |
| AC-12、AC-13 | 凭据不泄漏；跨用户池、轮换、取消、未读响应与事务清理不串上下文 |
| AC-11 | Action、Execution、原生证据和审计可关联；状态查询不触发第二次执行 |

故障测试须统计服务端实际收到/生效次数，覆盖驱动和 HTTP Transport 的隐藏重试与跳转；不能仅统计 Adapter 调用次数。测试数据均可销毁，不连接生产服务。

待确认：六个具体工具及细化数据范围、真实后端身份与凭据映射、企业 IdP 及组来源、IM 平台、ChatGPT 账号类型与接入能力、服务版本与部署配置。风险分级、受控目录和首发 ChatGPT 已确定，不再列为未决事项。

参考：[pgx](https://github.com/jackc/pgx)、[go-redis 重试选项](https://github.com/redis/go-redis/blob/v9.12.1/options.go)、[Go Transport 重试](https://github.com/golang/go/blob/go1.25.0/src/net/http/transport.go)、[PG 事务](https://www.postgresql.org/docs/current/sql-set-transaction.html)和[Redis 事务](https://redis.io/docs/latest/develop/using-commands/transactions/)。引用版本用于核对行为，最终采用版本仍需锁定验证。
