# MCPGuard 领域与调用契约草案 v0.1

状态：拟议设计，供评审；不是已实现接口、数据库 DDL 或上线承诺。日期：2026-10-22。
本文全部字段、枚举、路径及示例均为拟议，具体命名可在实现前调整；已确认架构边界不因此改变。

## 1 已确认边界与未决项

- 企业中央多人、多资源网关；服务器与员工本地服务混合，网络已互通。
- IDE 通过远程 HTTP，或本地 stdio 接入程序访问同一中央网关；stdio 程序不另设策略与审批权威。
- MCP 层与网关是同一服务。网关提供 `tools/list`、`tools/call`，内部直接调用 PG、Redis、HTTP 原生客户端。
- 后端不是 MCP Server，不参与 MCP 握手、目录发现或版本协商。
- 已确认风险分级、受控写他人审批与受控工具目录；首发ChatGPT。具体IdP、通知、审批组与工具名单未定；v0.1不开放通用SQL/任意Redis命令/URL。
- MCP profile、登录及人类交互细节见[协议设计](mcp-protocol-design.md)与[IDE 兼容方案](ide-access-compatibility.md)。

## 2 领域实体

| 实体 | 拟议核心字段 | 契约边界 |
|---|---|---|
| Principal | `principalId, orgId, subject, issuer, groups, roles, authContext, authTime` | 从验证后的网关身份构造；请求参数不能自报角色 |
| Resource | `resourceId, orgId, kind, ownerScope, endpointConfigRef, credentialRef, credentialVersion, backendIdentity, configVersion, enabled` | `kind=POSTGRES/REDIS/HTTP`；本地与服务器统一注册 |
| ToolDefinition | `toolId, name, version, description, inputSchema, outputSchema, resourceBinding, adapterOperation, effectClass, limits, policyRefs, enabled` | 工具属于网关；资源绑定为固定资源或受控允许集 |
| Policy | `policyId, version, subjectSelector, resourceSelector, operationSelector, conditions, outcome, approvalRequirement, enabled` | `outcome=ALLOW/DENY/REQUIRE_APPROVAL`；组合作用规则须确定且可追溯 |
| OperationPlan | `planId, actionId, version, principalId, resourceId, resourceVersion, toolVersion, backendIdentity, credentialVersion, operation, parametersRef, policySnapshot, preconditions, limits, expiresAt, planHash` | 冻结将执行的完整业务操作，而非只冻结展示摘要 |
| ApprovalDecision | `decisionId, actionId, planHash, principalId, approverId, decision, decidedAt, expiresAt, reason, approvalContext` | `decision=APPROVE/REJECT`；批准者资格由企业策略验证 |
| Action | `actionId, orgId, principalId, toolId, state, authorizationSource, interactionMode, activePlanId, createdAt, expiresAt` | 一个业务意图固定一个计划；跨 MCP 续传保持同一 Action |
| Attempt | `attemptId, actionId, principalId, clientId, transport, mcpProfile, rpcId, startedAt, endedAt, outcome` | 一次 MCP 请求；RPC ID 仅在相应连接/请求上下文中有意义 |
| Execution | `executionId, actionId, planHash, dispatchState, requestStatus, sideEffectStatus, startedAt, endedAt, nativeEvidenceRef` | 一次执行领取及其派发跟踪；记录存在不证明已发送，不将重试隐藏为同一次成功 |
| AuditEvent | `eventId, orgId, actionId, attemptId, executionId, actorId, eventType, at, planHash, policyVersion, outcome, detailsRef` | 追加记录关键事实；具体存储及保留周期待定 |

Principal 是员工访问网关的身份；backendIdentity 是网关访问后端的实际身份，两者分别记录。
多个员工可经授权使用同一后端服务账号，但不能因此共享员工权限、Action 或结果访问权。
`parametersRef` 指向受保护的完整参数；`planHash` 覆盖其规范化业务内容、目标、版本及约束，不包含明文凭据。详细存储采用带版本的HMAC摘要，避免低熵敏感参数被普通摘要枚举。
凭据值不进入 MCP 参数、审批页面或普通审计；其引用、版本及实际身份仍参与执行校验。
资源地址可指向员工电脑；资源归属和访问规则不从“网络可达”自动推导。

## 3 工具定义与无害调用示例

以下定义仅说明字段关系，不构成开放通用命令或连接生产服务的授权。
示例返回结构采用第 6 节ToolResult，含完成和延后分支；`outputSchemaRef` 为文档内简写，实际注册时解析为 JSON Schema。

```yaml
name: redis.get_demo_note
version: "1"
description: 读取演示命名空间中的一条文本记录
resourceBinding: demo.redis
adapterOperation: redis.get
effectClass: READ
inputSchema:
  type: object
  properties:
    noteId: {type: string, enum: ["welcome", "help"]}
  required: [noteId]
  additionalProperties: false
outputSchemaRef: ToolResult
limits: {timeoutMs: 3000, maxResultBytes: 8192}
policyRefs: [demo-read]
```

MCP 调用参数：`{"name":"redis.get_demo_note","arguments":{"noteId":"welcome"}}`。
网关解析 `demo.redis` 与凭据，从可信授权范围注入scope并构造 `demo:{scope}:notes:{noteId}`，适配器以参数数组执行原生 GET；IDE 不传 Redis URL、密码或命令串。

```yaml
name: directory.get_demo_office
version: "1"
description: 从演示目录服务读取办公地点资料
resourceBinding: demo.directory
adapterOperation: http.operation
operation:
  operationId: getDemoOffice
  method: GET
  pathTemplate: /demo/offices/{officeId}
effectClass: READ
inputSchema:
  type: object
  properties:
    officeId: {type: string, enum: ["office-demo"]}
  required: [officeId]
  additionalProperties: false
outputSchemaRef: ToolResult
limits: {timeoutMs: 5000, maxResultBytes: 16384}
policyRefs: [demo-read]
```

MCP 调用参数：`{"name":"directory.get_demo_office","arguments":{"officeId":"office-demo"}}`。
网关从 Resource 解析基址与认证，只将 `officeId` 填入固定路径；客户端不能替换主机、认证头或 method。
READ 来自已审核的 operation 语义，不能仅凭 GET、工具名称或 MCP annotation 判定无副作用。
PG采用固定查询模板和绑定参数；受约束通用SQL不进入v0.1，未来需要时另行设计授权与验收。

## 4 端到端调用与审批绑定

1. 验证网关身份，构造 Principal；本地 stdio 接入也须关联同一企业主体。
2. `tools/list` 按主体权限生成目录；分页和缓存按权限隔离，不从原生后端获取 MCP 工具。
3. `tools/call` 校验工具版本、输入 Schema、资源绑定及当次访问权；目录可见不代替调用鉴权。
4. 创建 Action 与 Attempt，解析原生操作，形成不可变 OperationPlan，执行策略评估。
5. DENY 则终止且不派发；REQUIRE_APPROVAL 进入等待；ALLOW 按企业策略进入可执行状态。
6. 审批服务验证真实决定及资格，将 ApprovalDecision 绑定 `actionId + planHash + principalId`。
7. 依据已选交互模式继续调用；审批回调不默认取得原生执行权，MRTR 等续传规则见协议设计。
8. 重新检查撤权、资源启停、计划期限、必要前置条件及版本；通过后，在同一本地事务中原子领取执行权并登记 Execution 派发意图。
9. 原生 adapter 执行冻结计划，更新执行证据与结果，网关生成 MCP 工具响应并记录 AuditEvent。

等待审批不持有数据库事务、锁或专属连接；预览仅反映指定时点，不保证执行时目标仍相同。
批准后不能让模型重新生成 SQL、命令或请求体；业务参数、目标、身份或有效约束变化需终止旧Action，创建新Action和计划并按策略重新审批，不迁移旧决定。
模型传入 `approved=true`、IM 消息文本或浏览器跳转成功均不能替代已验证 ApprovalDecision。
ApprovalDecision 的记录保留与执行权的单次领取分开：重复回调或续传不得自动产生第二次派发。
本地领取及意图记录与远端副作用不构成跨系统事务，journal 已提交不证明后端收到操作。
恢复时不能证明未发出的执行进入派发未知；已领取资格不因超时或重启自动重领。仅在原生可靠幂等能力成立时另行显式设计恢复协议。

## 5 原生 adapter 输入契约

拟议入口：`Execute(AdapterRequest) -> AdapterResult`；适配器不接收 MCP JSON 原文，不自行解释审批交互。
`AdapterRequest` 包含：`executionId, actionId, planHash, principalContext, resourceSnapshot, backendIdentity, credentialHandle, operation, parameters, preconditions, limits, deadline, cancellation`。
其中 `credentialHandle` 仅在网关内部解析；adapter 不接受调用者提供的任意连接串、密码或认证头。
PG operation 固定模板版本及绑定值；Redis operation 固定命令、选项及全部 key；HTTP operation 固定业务 operation、路径、query 和 body。
连接池按资源、实际身份、凭据版本和连接配置隔离；每次调用仍独立鉴权，连接复用不传递员工权限。
网关在首次派发前持久化意图；adapter 根据可观察的原生证据报告派发事实，不能将意图记录当作发送或接收确认。
进程崩溃时即使尚未记录发送成功也可能已经发出；恢复不得将这类记录还原为可自动派发状态。

## 6 结果、请求状态与副作用状态

Action持久状态统一为 `PENDING_APPROVAL/READY/EXECUTING/SUCCEEDED/FAILED/DENIED/CANCELLED/EXPIRED/OUTCOME_UNKNOWN`，策略允许和人类批准分别记录授权来源 `POLICY/HUMAN`；等待及拒绝时来源可空，人类拒绝通过决定与原因区分。
交互模式 `interactionMode=BOUNDED_WAIT/CONTROL_FLOW/MRTR` 在创建Action时固定；审批回调只写决定，原有效等待、显式resume或有效MRTR续传按模式领取。
业务工具输出Schema为完成结果 `ExecutionResult` 或已延后结果 `DeferredActionResult` 的明确分支；MRTR的input_required仍按所选协议profile编码，不冒充本产品普通工具结果。

`DeferredActionResult` 包含 `kind=DEFERRED, actionId, attemptId, state, executionId=null, dispatchState=NOT_DISPATCHED, sideEffectStatus=NONE, approvalUrl, nextActions, expiresAt`。原RPC已完成，业务Action仍可待批，不能用RUNNING暗示原RPC会继续。后续action.status/resume/cancel使用新的普通MCP工具调用，resume不接受新业务参数。
`approvalUrl`只定位受保护页面，不携带批准权；控制工具逐对象鉴权，不提供模型提交人类批准的能力。普通有界等待结束且未派发则终止，不能临时改为CONTROL_FLOW，详见[协议与连接器细化](detailed/transport-connector.md)。

Deferred表示有效待批状态，MCP isError=false且文字明确未执行；不能据此显示原生成功。action.resume只续传CONTROL_FLOW，MRTR仍使用有效协议状态。明确拒绝和实际执行失败采用失败结果，状态查询本身成功不等于原生操作成功。

控制工具有独立[ActionControlResult](specs/action-control-result.schema.json)：输出ActionView和控制outcome，可附受权的当前业务FINAL视图；RUNNING返回EXECUTION_IN_PROGRESS，已完成但结果内容不可取时STATE_RETURNED与result=null，不伪造数据或重做。对象权限失败不披露Action，MCP协议认证错误仍按所选profile处理。
控制工具外层error决定isError，查询成功但业务FAILED/UNKNOWN仍如实显示业务失败/未知；资源业务工具本身的失败映射不变。附带结果与Action的身份/事实对应由服务端验证，旧RPC响应历史不因后续核对改写。

完成结果使用 `kind=FINAL`，字段为 `actionId, attemptId, executionId, requestStatus, dispatchState, sideEffectStatus, data, error, metadata, evidenceRef`。由网关补充关联字段，适配器不构造MCP请求ID；错误和成功均按工具Schema返回，成功时error=null。
仅业务受理前的失败允许actionId/attemptId同时为null，并明确FAILED、NOT_DISPATCHED、NONE和无Execution，不为符合Schema虚造记录；requestId可关联入口诊断。RUNNING只用于状态查询，普通最终结果不接受RUNNING或INTENT_RECORDED。可校验定义见[ToolResult Schema](specs/tool-result.schema.json)和[示例集](specs/contract-examples.json)。
Execution 仅在领取执行权时创建；此前拒绝、校验失败或取消的结果中 `executionId=null`，不能为符合输出 Schema 虚造派发记录。已创建 Action 的业务结果携带其 `actionId` 和本次 `attemptId`，供受权查询关联；协议、认证等业务受理前错误可无 Action，并走相应协议响应。
`dispatchState=NOT_DISPATCHED/INTENT_RECORDED/DISPATCHED/DISPATCH_UNKNOWN`；`requestStatus=RUNNING/SUCCEEDED/FAILED/CANCELLED/OUTCOME_UNKNOWN`。
NOT_DISPATCHED 必须能确定未发送；INTENT_RECORDED 仅表示已持久化领取与派发意图，当前执行者尚在正常推进。
DISPATCHED 表示有证据确认派发发生，不等于后端已提交；DISPATCH_UNKNOWN 表示无法确定是否发出，不能从 journal 推断后端收到。
RUNNING 用于状态查询或有效进度，不表示普通最终 MCP 响应后还能补发同一 RPC 的结果。
`sideEffectStatus=NONE/APPLIED/ROLLED_BACK/PARTIAL/UNKNOWN`，与请求结果分开表达；不适用的事务术语不强加给 Redis/HTTP。

| 场景 | requestStatus | dispatchState | sideEffectStatus |
|---|---|---|---|
| 参数不合法、无权或审批否决 | FAILED | NOT_DISPATCHED | NONE |
| 已领取并记录意图，执行者尚未派发且正常运行 | RUNNING | INTENT_RECORDED | NONE |
| 已审核只读操作成功 | SUCCEEDED | DISPATCHED | NONE |
| 写入获明确成功确认 | SUCCEEDED | DISPATCHED | APPLIED |
| PG 事务失败且确认回滚 | FAILED | DISPATCHED | ROLLED_BACK |
| 写入成功但结果格式校验失败 | FAILED | DISPATCHED | APPLIED |
| 部分命令已生效，其余明确失败 | FAILED | DISPATCHED | PARTIAL |
| 已确认派发的写入未获最终结果 | OUTCOME_UNKNOWN | DISPATCHED | UNKNOWN |
| 写入意图已记录，进程崩溃且无法确定是否发出 | OUTCOME_UNKNOWN | DISPATCH_UNKNOWN | UNKNOWN |
| 已审核只读操作在相同崩溃窗口中无法确定是否发出 | OUTCOME_UNKNOWN | DISPATCH_UNKNOWN | NONE |
| 派发前已取消 | CANCELLED | NOT_DISPATCHED | NONE |

派发后的取消只有证据足够时才声明 CANCELLED；否则按已知执行事实或 OUTCOME_UNKNOWN 表达，取消不证明回滚。
`error` 拟含 `code, category, safeMessage, nativeCode, retryDisposition`；默认不自动重试副作用不确定的执行。
PG 原生认证失败、Redis NOAUTH、业务 API HTTP 401/403 均映射工具失败，不能冒充员工网关 MCP 401 或触发错误登录挑战。
协议结构错误走对应 MCP/JSON-RPC 错误；业务拒绝及原生执行错误通常走脱敏工具结果与 `isError=true`。
结果示例：`{"kind":"FINAL","actionId":"11111111-1111-4111-8111-111111111111","attemptId":"22222222-2222-4222-8222-222222222222","executionId":"33333333-3333-4333-8333-333333333333","requestStatus":"SUCCEEDED","dispatchState":"DISPATCHED","sideEffectStatus":"NONE","data":{"found":true,"value":"欢迎"},"error":null,"metadata":{"encoding":"utf-8","truncated":false}}`。
PG 保留列类型与重复列名；大整数和 Decimal 显式编码。Redis 区分不存在、空值和二进制；HTTP 状态码作为原生元数据。
非成功FINAL的data固定null；失败诊断只走safeError和受控evidenceRef，不用FAILED分支绕过成功data的字段允许集。每个成功data还须通过工具包resultDataSchema，公共封套Schema不授予任意业务字段交付权。
结果与错误均执行大小限制及脱敏；Schema 失败或截断不改变已经发生的原生副作用。

## 7 管理 API 资源分组与鉴权

以下路径拟以 `/api/v1` 为前缀，属于管理与业务控制 API，不是新增 MCP 标准方法。

| 资源组/拟议路径 | 能力 | 必要鉴权 |
|---|---|---|
| `GET /me`、`GET /capabilities` | 当前主体与可用产品能力 | 已认证主体；仅返回自身范围 |
| `/resources`、`/resources/{id}/test-connection` | 资源注册、版本、停启、受限连通测试 | `resource.read/manage/test`；逐资源归属检查 |
| `/tools`、`/policies` | 定义、版本、绑定及启停 | `catalog.read/manage`、`policy.read/manage`；变更审计 |
| `/credential-bindings` | 凭据引用及轮换绑定 | `credential.manage`；禁止读取明文凭据 |
| `/actions/{id}`、`/executions/{id}` | 状态、受控结果与取消请求 | 本人且有资源权，或明确运维权限；逐对象检查 |
| `/approvals/{id}/decisions` | 提交人类决定 | 实际审批者身份、资格、计划版本及防重复校验 |
| `/audit-events`、`/audit-exports` | 检索与受控导出 | 独立审计权限、组织/资源范围及字段限制 |

管理界面登录态与 MCP access token 的接入方式分别设计；任何一种认证都不能跳过对象级授权。
网页写请求需相应的跨站请求防护；服务间请求用服务身份。具体 IdP、会话与 token 方案留待身份设计确定。
AuditEvent 至少覆盖计划、策略、审批、执行权领取、原生派发、结果、取消及配置变更，敏感详情使用受控引用。
本稿待评审重点：审批主体及方式、Policy 组合规则、恢复与幂等边界、凭据映射、结果访问与审计保留。
