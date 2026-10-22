# MCPGuard 身份、权限与策略详细设计 v0.1

状态：设计建议，待评审与实现。日期：2026-10-22。本文字段、规则语法和接口均为拟议。
工程基线：单企业、模块化 Go 网关，PostgreSQL 保存权威授权及业务状态，本地 stdio connector 转入同一网关。
远程 HTTP 与 stdio 两入口执行同一套权限；网关原生访问 PG、Redis RESP、HTTP，不设置后端 MCP 身份层。
已确认风险分级：已授权的普通读取自动执行，受控写入由其他合格人员审批，高危操作默认禁止。
已确认 v0.1 采用受控工具目录：PG 模板、限定命令与 key 的 Redis 工具、固定 HTTP operation，不开放通用执行入口。
首发客户端已选 ChatGPT；其账号/workspace 类型及接入能力验证仍待定。具体 DSL、敏感读例外、审批组、IdP 和通知平台继续细化。
领域与状态引用[调用契约](../contracts.md)，协议行为引用[协议设计](../mcp-protocol-design.md)，不在本文新增 MCP profile 事实。

## 1 身份入口与令牌边界

| 入口 | 建议身份方式 | 网关接受的凭证与边界 |
|---|---|---|
| ChatGPT（首发目标）→ `/mcp` | 面向网关资源的 OAuth 授权 | 验证 access token；OIDC ID token、网页 cookie 不代替它 |
| 浏览器 → 控制台 `/api/v1` | 企业 OIDC 登录与网关服务端会话 | 使用浏览器会话；写请求另做 CSRF 与 Origin 检查 |
| stdio connector → `/mcp` | OAuth public native client，授权码与 PKCE | 使用面向网关的 access token；不因本机 stdio 放宽鉴权 |
| 网关 → 原生服务 | 已登记后端身份 | PG 角色、Redis ACL 或 HTTP 服务凭据；员工 token 不直接透传 |

网关是受保护 MCP 资源服务；企业 IdP 与 OAuth 授权服务的具体产品及是否由同一系统承担，仍待选择。
若企业 IdP 不能直接提供兼容授权流程或目标 access token，需另评估授权服务集成，不能用 ID token 临时代替。
JWT 验证约束包括可信 issuer、明确 audience、签名算法允许集、可信密钥、有效期及必要的时间声明。
密钥发现只访问管理员批准的 issuer 配置，不能信任 token 自带的任意密钥 URL；轮换与未知 key 行为需测试。
若选择不透明 access token，使用授权服务支持的验证机制；接口统一为 `IdentityVerifier.Verify`，不假设都可本地解析。
scope 是入口能力上限，不直接证明对某个 Resource、表、key、业务对象或审批单有权限。
网关认证失败按所选 MCP 授权规则返回；下游 PG 认证、Redis NOAUTH、HTTP 401/403 仍是工具执行失败。

## 2 浏览器与 connector 登录

建议浏览器采用服务端登录回调：校验 state、适用的 nonce、固定回调地址及授权响应，服务端保存令牌和会话。
会话 cookie 使用 Secure、HttpOnly 及适当 SameSite 属性；不把 access token 存在普通网页脚本可读取的持久存储。
浏览器会话不被 `/mcp` 当作 access token，管理接口也不因请求来自同一域就免除对象级授权。
建议 connector 使用系统浏览器登录、PKCE S256、一次性 state，不内置共享 client secret，不采集企业登录密码。
本地回调监听 loopback，关联原始登录事务；具体回调注册与端口策略须以选定授权服务实际支持为准。
授权码、code verifier、refresh token 不写 stdout、进程参数、URL 日志或 IDE 配置；stdout 仅承载 MCP 消息。
令牌优先存系统凭据库，按 issuer、资源 audience、client 与用户隔离；不可用时应明确选择受控回退，不能静默明文落盘。
续期、退出、用户切换分别验证；refresh token 撤销能力取决于授权服务，不承诺清除本地文件就撤销外部令牌。
本机受控进程能代表员工发起调用，connector 不能证明每个请求都由人手工输入；权限与审批仍由网关判定。

## 3 Principal、组与基础访问权

`Principal` 建议包含 `principalId, issuer, subject, status, groupIds, roles, authSessionId, authzEpoch, authTime`。
稳定身份以经验证的 issuer 与 subject 映射；邮箱、显示名、IDE clientInfo 及模型提供的 userId 不作为授权主键。
外部组声明只作为可信身份源输入，需映射到网关组与版本；禁止客户端自报组或把未知组自动视为管理员。
本地授权关系、账号禁用、资源启停及策略版本由 PostgreSQL 保存，任何进程内缓存都不是权威状态。

| 建议角色 | 能力边界 |
|---|---|
| employee | 在明确授权范围内发现工具、调用、查看自己的受权状态 |
| resource-manager | 管理指定资源的地址、归属及版本；不自动获得业务数据读取权 |
| tool-publisher | 发布指定工具与版本；不能借发布工具扩大自己的执行权限 |
| policy-admin | 管理策略与权限映射；相关变更审计，职责分离规则待评审 |
| approver | 在指定资源、操作及风险范围内审批；不是通用数据访问角色 |
| auditor | 受范围约束地查询审计；不默认读取完整业务结果或凭据 |

基础准入需要同时满足主体有效、入口 scope、角色能力、Resource 授权、ToolDefinition 绑定和数据范围。
RBAC 决定可做哪类行为；资源属性及数据范围继续缩小权限，不能通过另一个角色将显式拒绝相加抵消。
资源属性建议包括归属组、项目、环境、敏感等级、允许工具集和可用状态，全部来自管理员维护的版本化配置。
所有 `/api/v1` 对象查询、审批、结果、审计导出同样检查对象归属；知道 action ID 不构成访问授权。

## 4 数据范围与原生约束

| 后端 | 建议范围表达 | 必须落实的位置 |
|---|---|---|
| PG | 数据库、schema、表/视图、列、模板、租户/项目行条件 | 审核模板、绑定参数、最小权限角色；必要时数据库行权限 |
| Redis | 允许命令、全部 key、命名空间及枚举范围 | 参数解析和 key 检查、原生 ACL、受限枚举工具 |
| HTTP | service、operation、业务账号及对象/字段范围 | 固定 operation 与认证身份、请求对象授权和输出映射 |

行条件必须来自可信主体和授权映射；调用者传入 projectId 只能作为待验证参数，不能自行决定租户范围。
若 adapter 不能可靠强制某项范围，则该工具与该授权组合不可发布，不能执行后再过滤结果冒充访问控制。
读取也受列/字段敏感性、输出字节、扫描工作量与并发预算约束；只做脱敏并不等于已经授权查询。
Redis SCAN 可见性须单独验证，不能假设 ACL 自动过滤枚举；HTTP GET 与 PG SELECT 前缀不证明业务上无副作用。
v0.1 不开放通用 SQL、通用 Redis 命令入口或任意 URL；仅执行目录内已审核模板、限定命令/key 和固定 operation。
未来通用执行能力须作为单独需求评审，不通过批准、客户端确认或增加参数绕过当前目录边界。

## 5 确定的策略组合

建议将基础准入与风险策略分两层：基础准入失败立即 DENY，审批不能为无资源或数据权限者增加权限。
基础准入通过后，对同一规范化计划评估完整策略集合，组合顺序固定为 `DENY > REQUIRE_APPROVAL > ALLOW`。
没有匹配到允许或审批路径时默认 DENY；未知工具、未登记操作、未知副作用分类不能走自动放行。
所有匹配 DENY 均有效，不能被后面的 ALLOW、管理员界面按钮或普通批准覆盖。
多个审批规则的约束取交集、要求全部满足；一个宽松审批规则不能替代另一个更严格规则，冲突时拒绝。
同一计划的限制取更严格值，例如允许 key 集合取交集、结果与影响上限取最小值；合并后无合法操作则拒绝。
策略与完整版本快照进入计划和审计；同一输入、同一快照应给出同一结果及匹配规则 ID。
字段缺失、类型不符、未知操作符、求值超限或策略引擎不可用时阻止派发，不能跳过未能求值的拒绝规则。
策略更新需静态校验、正反例测试和版本发布；禁止在执行时临时加载未审核的策略文本。

## 6 受限表达式与策略规则示例

拟议 DSL 只提供 `all/any/not`、`eq/in/contains`、集合交并与有界数值比较；字段路径和类型采用允许集。
不执行任意 Go、JavaScript、shell、SQL 或网络请求，不允许模板插值形成表达式，也不向模型开放策略编辑入口。
字符串模式采用受限匹配语义与长度预算；首版可仅用枚举和已审核前缀，避免依赖自由正则表达式。
风险分级产品行为已确认；以下 YAML 的 DSL 语法、敏感读例外、审批组标识和阈值仍为拟议，不能直接作为上线配置。
所有审批规则均建立在基础访问权已满足之后，普通读自动执行不包含尚待细化的敏感读取例外。

```yaml
version: proposed-1
combine: deny-overrides-approval-overrides-allow
default: DENY
rules:
  - id: authorized-read
    match:
      all:
        - {field: plan.effectClass, op: eq, value: READ}
        - {field: access.resourceGranted, op: eq, value: true}
        - {field: access.dataScopeEnforceable, op: eq, value: true}
    outcome: ALLOW
    limits: {maxResultBytes: 262144} # 示例值，待容量与业务评审
  - id: sensitive-read-exception # 可选候选，敏感等级与审批组尚待细化
    match:
      all:
        - {field: plan.effectClass, op: eq, value: READ}
        - {field: resource.sensitivity, op: in, value: [CONFIDENTIAL, RESTRICTED]}
    outcome: REQUIRE_APPROVAL
    approval: {mode: OTHER_PERSON, eligibility: resource-data-approvers}
  - id: controlled-write
    match:
      all:
        - {field: plan.effectClass, op: eq, value: WRITE}
        - {field: plan.operationTier, op: eq, value: CONTROLLED}
    outcome: REQUIRE_APPROVAL
    approval: {mode: OTHER_PERSON, eligibility: resource-change-approvers}
  - id: prohibited-high-risk
    match:
      any:
        - {field: plan.operationTier, op: eq, value: PROHIBITED}
        - {field: tool.definitionState, op: eq, value: UNREVIEWED}
    outcome: DENY
```

`access.*` 是服务端完成基础授权后计算的只读事实；`effectClass/operationTier` 来自审核后的工具与原生操作语义。
敏感读取同时匹配 ALLOW 与 REQUIRE_APPROVAL 时，结果为需审批；若再匹配 DENY 则直接拒绝。
高危例子包括越界管理命令、未登记目标、无界破坏性操作；具体判定细则继续细化，v0.1 不设置通用批准放行例外。
风险等级不是模型置信度；不能由模型写 lowRisk 或通过工具 description/annotation 降低等级。

## 7 审批资格与冻结计划

受控写采用已确认的他人审批：以稳定 principalId 校验审批人与操作者不同，并检查当前审批组、资源范围及操作资格。
ChatGPT 自身的确认、操作者本人点击或模型确认不替代企业其他合格人员的批准；审批组、代理/会签及敏感读细则继续细化。
合格审批者可以没有执行该操作的权限，但必须具备对应审批权；其决定不能补足请求者缺失的基础权限。
计划绑定操作者、资源/工具/策略版本、原生身份、凭据版本、完整业务参数、限制、前置条件和期限。
ApprovalDecision 绑定 `actionId + planHash + principalId` 与审批人事实；展示摘要不代替对完整参数的绑定。
人类页面通过认证管理 API 写决定，通知只负责路由；模型 approved 字段、通知送达与跳转成功均不构成批准。
Action 固定 `interactionMode=BOUNDED_WAIT/CONTROL_FLOW/MRTR`；审批回调仅记录决定，不直接领取或原生派发。
仅原活跃等待、受权 resume 或有效 MRTR 新 RPC 按该模式继续；`action.status/resume/cancel` 逐对象鉴权，不能代为批准。
派发前重查双方资格、撤权、资源停用、计划期限和有效策略；改变批准语义时终止旧Action、创建新Action和计划并重新授权，不在原Action替换计划或迁移旧决定。
审批期间不保留业务事务或锁；预览时点与实际条件不同，必须依赖计划前置条件或重新确认。
单次领取与派发意图在本地 PostgreSQL 事务中提交；审批不能绕过取消、到期或已消费资格。
本地 journal 不证明后端收到；不确定发送窗口进入 DISPATCH_UNKNOWN，不自动恢复执行资格，副作用状态独立记录。

## 8 本地撤权、JWT 与外部传播窗口

建议以组织 `auth_epoch` 行作为单企业统一本地授权门禁；主体、组授权、资源和策略另有各自配置版本。
执行领取事务对该行取共享锁，读取当前权限与版本并检查计划；撤权或授权配置修改事务取排他锁，更新状态并递增 epoch。
领取与意图提交是授权线性化点；本地撤权先提交则阻止后续领取，领取先提交则后续撤权针对在途执行，不能消除跨系统窗口。
每次受保护调用、审批决定和执行领取读取当前权威状态；缓存键包含相关 epoch，不能仅凭未过期 JWT 继续使用旧权限。
外部 JWT 不一定含网关自定义 epoch；不得伪造该 claim 或假定 IdP 已签入本地版本。
`authzEpoch` 表示网关当次解析的授权快照，续传须对照当前状态重查，旧组 claim 不能覆盖本地明确撤权。
需要撤销某个已接受 JWT 时，可登记可信 jti 或 token 的受控摘要；主体级拒绝旧 token 可结合已验证 iat 截止规则。
iat 缺失、粒度和时钟边界需确定验证行为；不能因为本地 epoch 增加，就宣称所有外部已签 JWT 的密码学有效性消失。
浏览器会话和 connector 授权上下文分别撤销；禁用主体可阻断两入口，普通单次退出的影响范围须在产品中明确。
外部 IdP 的离职、退组或 token 撤销，需通过事件、同步、验证接口或 token 到期被获知，具体机制与时延待选型。
本地 epoch 只保证已被网关记录的变化进入授权检查；不能消除尚未传播的外部变化，也不承诺瞬时外部撤权。
需明确组同步新鲜度预算、失联策略和短期 token 策略；超过预算的高风险派发建议阻止，预算值待业务评审。
审批决定与继续调用仍检查当前身份及权限；不得靠旧 ApprovalDecision 绕过组织门禁或新版本检查。
领取已提交后的撤权仅能尽力取消或阻止尚可阻止的派发，不能保证召回在途操作、后端提交或已交付结果。

## 9 服务账号与凭据托管接口

网关元数据 PG 账号与被管业务 PG 账号分离；业务账号按资源和操作最小化，不使用超级账号作为通用执行身份。
Redis 使用受限 ACL 用户和命令/key 范围；HTTP 服务凭据限定具体业务账号、权限与目标，不复用员工 MCP token。
连接池按资源、后端身份、凭据版本与连接配置隔离；会话角色、事务、未读响应及认证状态不得泄漏到下次调用。
建议 `SecretProvider.Describe(ref)` 只返回版本/状态，`Resolve(ref, executionContext)` 返回短生命周期内部 handle。
`Rotate/Revoke` 属于受控管理操作，审计绑定变化；普通工具和管理读取 API 不提供“读出密码”能力。
执行上下文必须与计划、资源和后端身份一致；凭据轮换改变身份或批准语义时使原计划失效或触发明确重新校验。
托管产品仍待定；建议外部密钥服务或等效受控机制，状态库保存引用，解密根密钥不得与密文无保护同存。
本地开发使用专用测试凭据，不将环境变量读取、配置导入或连通测试变成向 IDE 返回秘密的入口。

## 10 传输、日志与结果保护

中央接入建议 HTTPS；原生跨机连接配置经验证的 TLS 或企业等效保护，生产不以跳过证书校验作为联通方案。
只信任受控反向代理传入的身份相关头；不从任意 X-Forwarded-* 或用户请求头获取已认证身份。
工具不能替换 Resource 主机、认证头或任意 URL；HTTP 重定向、DNS 与新目标地址应遵守注册资源的访问约束。
普通日志保留关联 ID、规则、时长和脱敏错误，不写 token、密码、确认秘密、完整连接串及默认完整 SQL 参数/响应体。
审批页与审计摘要按字段分类显示；完整参数和原生证据以受控引用保存，另行鉴权、加密及限制保留周期。
结果在进入 MCP、日志、控制台与导出前分别应用字段规则，保留脱敏/截断标记；掩码不改变实际副作用状态。
授权禁止读取的字段应在操作范围中排除；仅可读取但不宜发送给模型的字段，按结果交付规则移除或转换。
结果缓存按主体、资源、数据范围与版本隔离，读取旧结果也检查当前权限；通知仅携带最少摘要和受控详情链接。

原生返回内容视为不可信业务数据，不从中导入工具定义、权限、审批人或可执行指令。结构化字段与摘要均不能自行触发下一次原生操作；即使模型随后按恶意内容请求新工具，仍完整通过准入、策略和他人审批。
审批与控制台将参数、工具描述和原生错误按文本/安全JSON展示，不直接渲染不可信HTML。对“结果中包含操作指令”和页面脚本输入分别验收，不把文本标签或提示词当作权限边界。

## 11 威胁与验证映射

以下为待实现验收，不是现有安全验证结论；均使用测试身份、无害数据及可控故障。

| 威胁/失败 | 设计约束 | 验证与现有需求关联 |
|---|---|---|
| ID token、cookie 或错 audience token 被当 MCP 身份 | 分离入口及凭证验证 | 正反 token 用例、两入口登录隔离；AC-02/03 |
| connector 授权劫持、跨用户 token 复用 | PKCE/state、回调绑定、系统凭据隔离 | 并发登录、回调替换、用户切换；AC-03/12 |
| 未授权者借审批取得资源权 | 基础准入先行，审批不扩权 | 无资源权限即使持有批准也拒绝；AC-01/05 |
| 宽松 ALLOW 覆盖敏感例外或 DENY | 确定组合与失败关闭 | 规则排列、交集、缺字段与求值失败；AC-05 |
| 模型替换参数、资源或审批人 | 冻结计划、真实资格校验 | 批准后篡改与伪造决定、过期测试；AC-06 |
| 已撤权主体靠旧 JWT/目录缓存执行 | 当前本地状态与 epoch 检查 | 本地撤权、延迟 IdP 事件、旧 token；AC-01/03 |
| 后端服务账号越界或连接串权限 | 原生最小权限与池状态清理 | 真 PG/Redis/HTTP 的拒绝和复用检查；AC-08/13 |
| HTTP 目标替换、日志或结果泄密 | 固定目标、字段交付和脱敏 | 重定向/目标替换、错误与导出扫描；AC-04/12 |
| 重复批准、崩溃、超时后重复写 | 单次领取、未知状态不自动重派 | 故障注入与实际副作用计数；AC-07/09/10 |

实现前仍需细化：具体工具与数据范围、DSL、敏感读例外、审批组、IdP/组同步、ChatGPT 账号/workspace 类型与能力验证、通知、凭据托管与保留策略。
后续将选定策略、实现提交及证据关联上述验收；不得把示例 YAML、设计审阅或单元测试替代真实身份和原生服务联调。
