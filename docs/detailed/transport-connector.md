# MCP 传输与本地接入程序详细设计 v0.1

状态：拟议实现与验收契约，尚未实现或通过真实 IDE 验证。日期：2026-10-22。
依据：[协议边界](../mcp-protocol-design.md)、[IDE 接入](../ide-access-compatibility.md)与[领域契约](../contracts.md)。
HTTP 直连和本地 stdio 转 HTTP 都属于产品目标，进入同一个 MCPGuard 服务；PG、Redis、API 只由网关原生适配器访问。
首发ChatGPT、风险分级与受控写他人审批已确认；账号/工作区、身份源、通知渠道和等待预算仍需落实。ChatGPT按HTTP接入设计，stdio另选实际支持客户端验收。

## 1 模块边界与调用上下文

以下模块名与接口名均为拟议，不表示当前仓库已有对应实现。

| 模块 | 输入与职责 | 不承担的职责 |
|---|---|---|
| HTTPEntry | `/mcp`、TLS/Origin/大小限制、HTTP 状态及标准资源发现 | 原生业务执行 |
| IdentityVerifier | 验证访问令牌，构造 Principal 与权限 epoch | 根据客户端用户名授予身份或认定批准 |
| ProfileAdapter | SDK Server、2025/2026 请求与结果契约、能力限制 | 任意跨代语义转换 |
| CatalogService | 按主体生成工具目录、Schema、分页、缓存及变更通知 | 从原生后端发现 MCP 工具 |
| InvocationHandler | 归一请求、建立 Attempt、进入统一 ActionService | 在协议回调里直接连接数据库 |
| AttemptRouter | ID、进度、订阅、期限、取消及单次响应关联 | 将传输 ID 当作业务幂等键 |
| ActionService | 计划、策略、人类授权、单次领取及结果查询 | 依赖 IDE 私有管理 REST 调用 |
| Connector | 本地 stdio Server 与远程 HTTP Client，同 profile 转接 | 保存生产资源凭据或自证批准 |

统一上下文包含 Principal、profile、frontChannelId、attemptId、原始 RPC ID、deadline、cancel handle 与 trace ID。
RPC ID 保留字符串/整数类型及精度；路由键还包括主体和前端上下文，两个员工的 `id:1` 不能冲突。
Action 表示业务意图；Attempt 表示一次 MCP 请求；Execution 表示执行领取及派发跟踪，未领取时为空。
普通新调用创建 Action；经验证的 MRTR 或显式续传复用原 Action，仅新增 Attempt。
相同参数或 IDE 新 RPC ID 不能证明是旧请求；没有可靠续传上下文的新调用属于新意图，可能再次产生业务效果。

## 2 HTTP 直连调用序列

1. IDE 访问企业 HTTPS `/mcp`；缺少有效访问令牌时返回规范认证挑战，不创建业务 Action。
2. 客户端发现 Protected Resource Metadata 与授权服务器，完成适用注册/预注册及 OAuth 授权码、PKCE 流程。
3. HTTPEntry 验证 Origin、请求格式及限制；IdentityVerifier 验证 token 与目标 resource，取得可信 Principal。
4. ProfileAdapter 处理旧版握手或新版每请求元数据，形成真实可用的能力集合。
5. `tools/list` 调用 CatalogService；`tools/call` 校验工具、Schema、资源权限后进入 InvocationHandler。
6. Handler 创建或验证续传 Action，建立 Attempt；ActionService 固定计划并决定允许、拒绝或人类授权。
7. 仅在交互方式有效、权限和计划仍有效时原子领取执行资格；原生适配器执行，协议层不接触后端密码。
8. Handler 将业务结果编码为相应 profile 的 MCP 结果，结束本次 Attempt，并记录结果与响应交付事实。

响应交付失败不改变已知原生副作用；查状态与重发写操作是两种不同动作。
HTTP 认证拒绝不触发匿名、共享 token 或其他协议的回退；版本探测仅使用发现/握手，不使用业务写操作。

## 3 两个 profile 与能力基线

采用已核实的官方 Go SDK v1.8.0，要求 Go >=1.25；依赖升级属于实施工作，不是本文件已完成的修改。
两端按同 profile 转接；SDK 支持列表不等于已发布客户端兼容名单。

| 项目 | 2025-11-25 | 2026-07-28 |
|---|---|---|
| 版本与能力 | `initialize`、`notifications/initialized`，按连接/会话协商 | 无握手；每请求 `_meta`，实现 `server/discover` |
| HTTP | POST；按功能支持 GET SSE 与可选 MCP-Session-Id | POST 请求流；无协议 session、无 GET 通知流 |
| 服务器索取输入 | 协商能力后的独立反向请求 | MRTR `input_required` 与新 RPC ID 续传 |
| 目录变更 | 声明 listChanged 后路由通知 | `subscriptions/listen`、先确认订阅、再按过滤类型发送 |
| 完成与缓存 | 使用该版工具结果及列表结构 | `resultType`；相关结果包含 `ttlMs`、`cacheScope` |
| HTTP 断流 | 不直接视作取消；显式取消与恢复另有约束 | 未完成请求的 SSE 断流视作取消，不支持 Last-Event-ID 恢复 |

首版标准基线为 `tools/list`、`tools/call` 及所选 profile 的必需发现、元数据、错误、期限与取消行为。
resources、prompts、sampling、roots、elicitation、Tasks、Apps 等未实现能力不宣告，也不伪造成功响应。
listChanged/订阅仅在目录失效与通知路由实现后声明；不支持动态通知时仍响应显式目录重查。
2026 校验 MCP-Protocol-Version、Mcp-Method、Mcp-Name 与 body 一致；工具别名变化时同步更新头和正文。
暂不发布需要自定义参数镜像头的工具定义；后续开放时必须实现对应校验，不能将 HTTP 头直接当可信策略输入。

## 4 HTTP 身份与令牌生命周期

受保护资源元数据按最终 resource URL 的规范位置提供；受众校验针对规范化网关 resource URI（含必要路径），不能只匹配同域名。
身份源可采用兼容企业授权服务，具体 IdP、客户端注册方式、组来源与撤权传播契约待定。
MCP access token 与管理页面会话、OIDC ID token 分开；不能假设 access token 必定为 JWT。
每个受保护请求均验证签发方、受众、有效性、scope 和当前资源权限；session、actionId、requestState 不能代替认证。
授权码流程使用 PKCE S256、可信 issuer、state/回调关联及登记的 redirect URI；发现地址与重定向采用受控访问策略。
401 表示网关身份无效或缺失；403 表示网关访问权限不足。PG/Redis/API 的认证失败不转换为员工重新登录挑战。
客户端或连接器在令牌过期前按授权服务支持的方式续期；刷新失败进入明确的重新登录状态，不无限重放业务请求。
会话/目录缓存不能延长已撤销权限；领取执行资格前仍重查主体与计划资格。
长时等待按显式授权有效期及权限 epoch 重检；不因连接曾认证就永久允许后续执行。
审计记录验证后主体和认证上下文，日志不保存访问令牌、刷新令牌或授权码。

## 5 工具目录、分页与缓存

CatalogService 将网关注册工具与当前员工权限求交，读取权限不直接授予调用权限。
内部目录缓存键至少包含 `orgId + principalId + permissionEpoch + catalogVersion + policyVersion + profile`。
permissionEpoch来源于组织权威auth_epoch，不是另一套撤权状态。组/角色与资源权限变化推进该epoch；工具或 Schema 发布推进 catalogVersion；相关策略发布推进 policyVersion。
缓存命中仍受 TTL 与有效主体约束；2026 权限相关结果使用 private cacheScope，不把敏感目录放入共享 HTTP 缓存。
分页 cursor 为服务端签发的不透明引用，绑定上述视图、排序位置与期限，不承载明文权限或凭据。
同一视图保持稳定排序；版本已变化的 cursor 返回明确无效/过期错误，由客户端重新列举，不能混合两版结果。
不得仅过滤第一页；即便某页权限过滤后为空，也必须正确表达后续 cursor，不能漏掉仍有权访问的工具。
每次工具调用重新鉴权和检查定义版本；目录可见、旧缓存或只读 annotation 均不作为放行依据。
目录变化仅通知有权接收的客户端；已提交的本地撤权影响后续调用与领取判定，不依赖客户端收到通知。领取提交后的在途操作遵守线性化边界，外部IdP尚未传播的变化不能承诺已生效。

ChatGPT的共享应用工具快照是另一层目录，网关缓存/通知不能保证逐员工实时隐藏平台已发布元信息。采用最小稳定工具集；有严格工具隐藏要求时按权限组发布并实测，工具变更按平台流程refresh，详见[ChatGPT接入](chatgpt-access.md)。

## 6 stdio Connector 启动与登录

拟议程序名 `mcpguard-connect`；以下 run/login/logout 均为待实现接口，不是当前可执行命令。
建议先显式 `login --gateway <HTTPS入口>`，完成员工浏览器授权，再由 IDE 按生成配置启动 `run`。
run 不向 stdin 请求人类输入；stdin 已被 IDE 用于 MCP。无登录态或刷新失效时在 stderr 给出登录指引并明确退出。
交互式 login 可启动受限 loopback 回调与浏览器；回调地址、客户端注册及跨平台支持随 IdP 和目标 IDE 实测确定。
远程生产入口只使用 HTTPS；测试例外须显式限定到隔离环境，不能自动忽略证书校验。
配置仅保存网关 URL、选定 profile、账号选择与非秘密参数，不保存生产资源地址凭据或全员共享 token。
CredentialStore 优先使用系统凭据库，按网关 resource、授权 issuer、clientId 与员工账号隔离令牌。
刷新令牌轮换采用原子替换；issuer 或 resource 改变时重新验证/授权，不复用另一授权服务器签发的客户端凭据。
同账号多 IDE 进程使用单飞刷新或跨进程锁与凭据版本检查，避免并发轮换使另一进程覆盖新刷新令牌。
logout 清理对应账号秘密并按授权服务支持情况撤销令牌；不能承诺仅删除本地文件就立即吊销所有已签发 token。
token 不进入命令参数、配置 URL、stdout 或普通日志；stderr 也只输出脱敏诊断。

## 7 Connector 长驻读写与映射

run 持有一个持久 stdio reader、一个串行 stdout writer，以及受限并发的 HTTP 请求/结果流任务。
每个 HTTP 流持续读取至完成、取消或故障；2025 GET 通知流与 2026 订阅流按 profile 单独维护。
不能通过“读一条 IDE 消息→阻塞等一条 HTTP 消息”实现；等待审批时其他请求、通知和取消仍可处理。
stdout 仅写一行一条完整 JSON，换行恰好一次；设置显式帧上限并检查读错、EOF、队列饱和与部分写入。
HTTP transport 的 202/空 body 不伪造成 JSON-RPC result；stdio 通知不等待不存在的 RPC 回复。
映射表包含本地通道、方向、原始 ID、远程请求、进度 token、订阅标识及终止状态；映射完成后及时回收。
若 SDK 两端分别终止并重写 ID，同时映射取消 requestId、progressToken 和 2026 subscriptionId。
服务端能力不得超过远程网关与 Connector 的共同实现；不将本机目录擅自作为 roots，也不代员工完成 sampling。
网关业务处理器只相信验证后员工主体，不能相信 Connector 自报角色、已审批标志或生产凭据。
IDE EOF 后停止接收新请求，按 profile 发出适用取消并清理流和映射；诊断退出原因，不留下无期限后台写入。

## 8 取消、断连与故障映射

| 输入事件 | 处理方式 |
|---|---|
| 2025 stdio notifications/cancelled | 映射 requestId，向 HTTP 网关发送该 profile 的显式取消 |
| 2025 HTTP 流中断 | 不单独视作取消；保留逻辑期限，恢复能力仅在明确实现时启用 |
| 2026 stdio notifications/cancelled | 关闭对应远程请求 SSE 流；不关闭其他请求或全部订阅 |
| 2026 HTTP 未完成请求断流 | 终止对应 Attempt；派发前撤销该路径执行资格，派发后尽力取消原生上下文 |
| 正常完成 MRTR input_required 后关流 | 本次 Attempt 正常结束，不能当作业务 Action 已取消 |
| 后端写入已发出但无最终证据 | 持久表达 OUTCOME_UNKNOWN 或 DISPATCH_UNKNOWN，不能自动重放 |

取消/超时/批准竞争由 ActionService 原子判断；协议层不能仅靠关闭连接替代业务资格终止。
2025 临时 HTTP 断连与显式取消不同；逻辑 Attempt 的有界期限仍有效，到期后未派发的普通等待路径终止。
派发后取消不证明回滚；PG、Redis、HTTP 分别报告已知事实，状态与副作用按统一领域契约表达。
不能确定原请求是否被接收时，不把认证刷新、连接恢复或 SDK 默认重试当作重新发送写工具的许可。

## 9 审批交互的三种明确模式

交互模式在创建 Action 时确定并持久化；不能在普通等待超时后暗中改成持久后台执行。
无论模式如何，审批页由网关核验本人或合格审批人身份和资格；URL、actionId、accept 均不携带批准权。

### 9.1 普通调用内有界等待

原 tools/call 保持有效，等待预算取 IDE、Connector、HTTP 路径、网关及业务期限的可用约束，不预设固定五分钟。
审批回调只写决定；仍有效的 Handler 重新校验资格并原子领取，只有领取成功才创建 Execution 和调用适配器。
拒绝、过期或派发前取消终止该路径；最终结果只返回一次，取消后的回复按相应协议要求处理。
普通等待最终结束且未派发时，迟到批准不得执行；若用户主动重新发起，应明确这是可能产生新效果的新业务意图。

### 9.2 2026 MRTR 续传

仅对已验收的现代客户端启用；返回标准 `resultType: input_required`，并按实际需要包含 requestState/inputRequests。
提供 requestState 时客户端原样带回；存在 inputRequests 时按所声明能力提供相应 inputResponses，新尝试使用新 RPC ID。
requestState 绑定原 Action、主体、工具/计划/资源版本、完整参数摘要、期限与交互阶段，签名或受控引用防止篡改。
初次 RPC 在 input_required 完成后结束；审批回调仅更新持久决定，有效续传重新鉴权后才能原子领取派发资格。
客户端输入不能自证人类批准；URL elicitation 的 accept 只说明交互响应，批准事实以服务端核验记录为准。
尚未获批时可返回新的合法 MRTR 阶段或明确待批状态；不把每次查询当成消费原生执行资格。
重复或并发续传只能取得既有状态/已存结果，不能产生第二次派发；未收到续传则不因批准回调自动执行。
MRTR 不保证客户端重试，不是 Tasks；过期状态需要清理，已取消或明确拒绝的 Action 不得借新 requestState 复活。

### 9.3 tools-only 的显式控制工具流程

这是网关业务工具契约，不是MCP自带自动重试机制。工程基线对需异步他人审批的工具优先采用该模式，首发ChatGPT能否顺利消费与续传仍需实测。
原调用选择 CONTROL_FLOW 后可完成为独立 DeferredActionResult，明确“尚未执行”，Action 在业务有效期内继续待批。
该结果包含 actionId、当前 state、executionId:null、dispatchState:NOT_DISPATCHED、审批页 URL 和下一步说明。
相关业务工具的 outputSchema 显式包含此分支；不能返回 ExecutionResult.RUNNING 暗示已结束的原 RPC 仍会补结果。
文本摘要与 structuredContent 一起说明需人工操作；客户端不理解自动流程时，用户可在门户查看并明确要求调用控制工具。

| 拟议工具 | 最小输入 | 结果与边界 |
|---|---|---|
| action.status | actionId | 逐对象鉴权后返回计划摘要、决定、执行/结果状态与可用后续动作 |
| action.resume | actionId | 仅续传CONTROL_FLOW，不接受新业务参数；核验主体、完整冻结计划及决定后领取原Action执行权 |
| action.cancel | actionId | 请求终止未派发路径或尽力取消已派发操作，结果不声称必然回滚 |

输入 Schema 使用明确字段和 additionalProperties:false；输出是各工具公开 Schema 对应的 MCP tool result。
控制工具使用独立[ActionControlResult](../specs/action-control-result.schema.json)，输入复用其中actionIdInput；不复用资源业务ToolResult。正常状态输出包含ActionView和outcome，可选附受权的当前业务FINAL结果视图；重复resume遇RUNNING时返回EXECUTION_IN_PROGRESS，而不是伪造FINAL或DEFERRED。已完成但结果被清理/不可展示时返回STATE_RETURNED与result=null，不重新执行或伪造数据。
对象受理前的失败返回无Action详情的CONTROL_ERROR。当前控制请求以requestId关联，附带FINAL保留原业务Action/Attempt/Execution来源；当前事实可依可信核对证据收敛，但旧RPC响应历史不改写。跨字段ID/事实一致性和当次结果权限仍由服务端检查。
控制工具外层error非空或kind=CONTROL_ERROR时isError=true；外层error=null的状态返回为false，嵌套业务FAILED/OUTCOME_UNKNOWN不把成功查询变为查询失败。摘要必须明确展示业务失败或未知，不能写成执行成功；资源业务工具本身仍按其FINAL状态映射失败。
这些工具进入同一个领域服务和审计，不要求普通 IDE 调用内部管理 REST API，也不暴露审批提交工具给模型。
actionId 只是对象定位；状态/结果查询、resume 与 cancel 均校验当前主体、归属及权限，不能访问他人 Action。
resume 对未批准返回明确 APPROVAL_PENDING；对 DENIED、EXPIRED、CANCELLED 拒绝，不重新计划或扩大操作。人类拒绝持久为DENIED并记录可信拒绝决定与原因。
已领取/已完成/结果未知的 Action 返回既有状态或受控结果，不再派发；需要人工核对时明确下一步。
资源工具首次待批返回DeferredActionResult；控制工具未获批返回ActionControlResult的APPROVAL_PENDING，isError=false且明确尚未执行。否决、越权、过期及完成的执行失败按相应失败契约处理。status查询成功不表示原生操作成功；为查询配置速率限制，不要求模型无期限轮询或假定ChatGPT自动后台继续。
首版受控写由他人批准；模型可在合格他人批准后请求resume，但无法靠自己调用控制工具、ChatGPT自身确认或自批产生企业批准。
没有可依赖稳定业务幂等键的普通 IDE 重试，不自动关联旧 Action；用户手动重做新 Action 可能重复效果，必须明示。

## 10 错误、运行约束与发布证据

无效 JSON、未知 method、版本或结构错误由 SDK/profile 编码；工具业务失败使用脱敏 isError 结果并保留副作用状态。
未创建 Action 的协议前置错误无 Action/Execution；业务结果按领域契约提供关联 ID，未领取时不虚造 Execution。
后端业务 401/403 不冒充前端 OAuth 失败；拒绝/待批结果遵守各工具输出 Schema，不随意混入不兼容 structuredContent。
2025 若启用协议 session，其状态绑定认证主体；多副本需选择并验证会话归属/路由和流恢复，不可泛称全部 stateless。
2026 无协议 session 也不代表无业务持久状态；Action、决定、执行意图与审计不能仅保存在某台实例内存。
限制连接数、并发 Attempt、流、待批数量、帧与结果大小；实际阈值、超时和退避经测量确定，不虚构容量。
目录缓存、连接重建和进度不能绕过撤权；同 Action 单次领取不承诺跨新 Action 或原生系统端到端恰好一次。

| 验收组 | 必须收集的证据 |
|---|---|
| 两入口/两 profile | 发现或握手、同号并发、能力子集、标准错误；分别记录入口与 profile |
| 身份与缓存 | 登录/续期/退出、错误受众、撤权 epoch、跨主体 cursor 与目录隔离 |
| 流与取消 | stdio EOF、2025 断流/取消区别、2026 请求流取消、无 stdout 日志杂音 |
| 三种审批交互 | 有界超时、MRTR 新 ID 与状态重放、控制工具未批/否决/过期不得执行 |
| 故障与结果 | 批准/取消竞争、并发 resume、派发后断流、未知结果查询而不重放写入 |
| 真实产品 | HTTP 与 stdio 各至少一个真实 IDE 完整组合；等待窗口与所选 IdP 单列验证 |

沙箱使用无模型 key 的 SDK/独立语言客户端、模拟身份和审批、真实测试 PG/Redis 与无害 HTTP API。
真实 IDE 的 MRTR/Tasks、浏览器登录、停止按钮与结果消费须实测；当前没有任何客户端已通过的声明。
