# 领域、状态与接口契约

版本：v0.2 设计增量。日期：2026-10-24。状态：拟议接口与逻辑存储约束，尚无对应 Schema、迁移或运行接口。本文件与 [凭据生命周期](credential-lifecycle.md) 是后续生成机器契约的输入；旧 [机器契约](../../specs/README.md) 继续只证明其原版本。

## 1 核心实体

所有资源归属、主体和对象访问由服务端校验。UUID、别名、RPC ID 或对象 ID 不能作为认证依据。版本用十进制字符串，时间由服务端生成和判断；模型不能设置状态、审批人、原生身份或授权来源。

| 实体 | 必要字段或关联 | 约束 |
|---|---|---|
| Resource / ResourceVersion | resourceId、orgId、kind、alias、owner、endpointConfigRef、configVersion、enabled、catalogVisibility | endpoint、TLS 和数据库由已登记不可变版本解析；别名不唯一代表目标 |
| PermissionProfile | profileId、version、resourceId、allowedOperations、nativePermissionPlanRef、limits、approvalPolicyRef | 预设最小权限套餐；原生可强制数据范围须明确，不接收用户 GRANT SQL |
| ToolResourceBinding | toolVersion、operationId/version、resourceId/version、profileId/version | 一套工具实现可绑定多资源；允许的资源不能由类型或名称隐式推导 |
| AccessRequest | requestId、ownerId、resource/version、profile/version、requestedExpiry、reason、requestHash、state、revision | 冻结申请快照；修改范围或期限创建新申请，不迁移旧决定 |
| AccessDecision | decisionId、requestId、requestHash、requestRevision、deciderId、source、decision、approvedExpiry、createdAt | 默认合格他人；预授权自动决定必须引用已审核 policy/version |
| ProvisioningJob | jobId、grantId、requestHash、nativeTarget、accountPlanHash、state、providerReceiptRef | 一条冻结开通任务；原生身份和权限可核对，状态未知不能盲重试 |
| NativeAccount | accountId、resourceId、subjectId、profileId/version、nativeIdentity、managementMode、observedPermissionRef | PROVISIONED 或明确登记的 PREEXISTING；默认按主体/资源/套餐独立，不自动接管外部账号 |
| AccessGrant | grantId、subjectId、resource/version、profile/version、accountId、bindingId、state、grantVersion、expiresAt、authEpoch | 只有开通核验后 ACTIVE 才允许签发或执行；批准本身不代表 ACTIVE |
| CredentialBinding | bindingId、subjectId、resourceId、accountId、nativeIdentity、bindingVersion、secretRef、secretVersion、enabled | 身份绑定版本与秘密轮换版本分开；秘密值不进入领域公开视图 |
| CredentialLease | leaseId、grantId/version、credentialId、expiresAt、state、revision | 记录包的服务端使用期限与撤销状态；有效期不超过授权及资源策略上限 |
| CredentialPackage | credentialId、leaseId、issuer/issuerEpoch、subjectId、resourceId、bindingId/version、formatVersion、keyId、blobRef、digest、state | .mcpguard 密文文件；只封装引用，digest 用于传输完整性，不是授权证明 |
| DownloadTicket | downloadId、credentialId、ownerId、clientInstanceId?、expiresAt、state、requestKey | 短期一次领取状态；有可信上下文时绑定该客户端，无时仅支持本人认证后手动领取；不会返回免认证下载 URL |
| CredentialContext | contextId、ownerId、clientInstanceId、expiresAt、verifierHash、state | 独立于 MCP session；随机上下文令牌仅对已认证用户有效，不返回模型 |
| ContextActivation | contextId、credentialId、resourceId、activatedAt、state | 激活证明该客户端递交过有效密文；每次执行仍查当前授权和包状态 |
| Revocation | revocationId、targetType/id、actorId、reason、authEpoch、committedAt、inFlightStop、nativeCleanup | 撤销状态、在途停止和原生清理分开；保留墓碑与审计 |

CallerIdentity 由已验证 issuer、subject 与组织绑定产生，不接受模型提交 ownerId。原生用户名不必与员工名相同；NativeAccount 与 CredentialBinding 保留可核验的对应关系。

## 2 状态与允许迁移

| 对象 | 正常迁移 | 异常、终止与恢复 |
|---|---|---|
| AccessRequest | SUBMITTED → PENDING_APPROVAL → APPROVED | REJECTED、CANCELLED、EXPIRED、STALE；自动规则决定也必须留 Decision |
| AccessGrant | PROVISIONING → ACTIVE | PROVISIONING_UNKNOWN、PROVISIONING_FAILED、REVOKED、EXPIRED；UNKNOWN 只读核对或清理，不能签发；终止后不原地重新激活 |
| ProvisioningJob | READY → EXECUTING → VERIFIED | OUTCOME_UNKNOWN、FAILED、CLEANUP_PENDING；恢复只核对同一原生标识，不获得新执行资格 |
| CredentialLease / Package | ACTIVE → REVOKED 或 EXPIRED | 新授权或重新签发创建新 credentialId；旧包不复活 |
| DownloadTicket | READY → CLAIMED | EXPIRED、CANCELLED；消费后不凭同 ID 返回第二份文件 |
| CredentialContext | ACTIVE → REVOKED 或 EXPIRED | 员工续期可在仍有效的当前身份下新建 context，不恢复撤销包或激活映射 |
| ContextActivation | ACTIVE → REVOKED 或 EXPIRED | 包、授权、资源或上下文任一失效即不可执行 |

APPROVED 的申请可关联处于 PROVISIONING 的授权。开通结果、决定和激活是不同事实；申请审批员不自动获得业务数据读取权。

批准的期限与范围只能等于或收窄申请，不得扩大；原申请快照不修改，决定和最终开通计划保留实际批准内容。开通完成时若原期限已过或申请条件已失效，授权终止并进入清理。PROVISIONING_UNKNOWN 仅在只读核对同一原生目标且确认权限合格后，经当前资格复查才可转 ACTIVE；不能靠重新领取管理任务跳过核对。

业务 Action、Attempt、Execution、DEFERRED、action.resume、派发和副作用状态继续沿用原版本。新增计划字段：resourceId/configVersion、grantId/grantVersion、credentialId/leaseId、bindingId/bindingVersion、nativeIdentity、secretVersion、operationVersion 和 authEpoch。秘密值及密文包本身不进入计划正文或摘要。

## 3 公开 MCP 工具

以下是拟议名称，不代表当前网关已注册。新工具输入使用 additionalProperties=false，标识、期限和列表大小有显式上限。资源目录是业务目录，不等同于 MCP 协议的 resources/list。

| 工具 | 输入 | 输出和限制 |
|---|---|---|
| resource.list | kind?、after?、limit? | 安全资源描述、canRequest、当前授权/开通状态；过滤后分页，无敏感端点和账号列表 |
| resource.get | resourceId | 本人可见资源及可申请套餐；不可见对象使用统一拒绝语义 |
| access.request | resourceId、profileId、requestedExpiry、reason、requestKey | requestId、state、revision；主体取认证上下文，不允许自报权限 SQL |
| access.status | requestId | 本人或受权审批范围的申请、决定摘要与开通状态，无秘密 |
| access.cancel | requestId、expectedRevision、requestKey | 只取消本人未终止申请；已批准授权走单独撤权，不修改历史决定 |
| access.grants | resourceId?、after?、limit? | 本人当前及历史授权摘要，明确 ACTIVE/PROVISIONING/REVOKED 等状态 |
| credential.prepare_download | grantId、requestKey | credentialId、leaseId、downloadId、expiresAt、state；不返回密文字节、上下文令牌或免认证链接 |
| credential.status | credentialId | 本人包和租约状态、期限、替换关系；不包含密文和原生秘密 |
| credential.revoke | credentialId、expectedRevision、reason、requestKey | 仅吊销本人文件；管理员跨人或授权级撤销使用独立管理权限 |

所有变更工具的 requestKey 是业务幂等键，范围为组织、已认证主体、工具和目标。客户端生成并保留原键；相同键同载荷在重新鉴权后返回原快照，同键异载荷返回 IDEMPOTENCY_CONFLICT；RPC ID 不作为幂等键。

新业务入口拟为 pg.get_item、pg.update_note，分别复用受控读取和固定更新模板，增加 resourceId 与 credentialId 两个非秘密选择字段；实际资源集合不枚举进公开 Schema。原 pg.get_demo_item 等 13 个演示工具保留原版本和输入，不能静默增加必填参数或据此扩大生产能力。

新路径的业务调用必须有 CredentialContext 中与 resourceId、credentialId 匹配的有效激活。仅知道这些 ID 或仅拥有员工 token，不能跳过本增量要求的密文领取/激活步骤。

## 4 拟议 REST 接口

本增量使用 /api/v2，避免改变现有 /api/v1 行为；旧 /api/v1 不能成为绕开新授权的别名。两个版本的可用范围分别登记并显式配置。

| 方法与相对路径 | 用途 | 访问边界 |
|---|---|---|
| GET /resources、GET /resources/{id} | 资源与套餐安全视图 | 当前主体的可见范围 |
| POST /resources、POST /resources/{id}/versions、POST /resources/{id}/publish | 登记、版本化和启用 | resource.manage；强 ETag、幂等和受控配置导入 |
| POST /access-requests、GET /access-requests/{id}、POST /access-requests/{id}/cancel | 申请、状态和取消 | request.self 或明确的查看范围 |
| POST /access-requests/{id}/decisions | 提交冻结申请的决定 | 受保护控制台、access.approve、禁止自批、requestHash 与 revision |
| GET /access-grants、GET /access-grants/{id} | 授权和开通状态 | 本人或管理范围 |
| POST /access-grants/{id}/revocations | 授权级撤销 | grant.revoke，列明受影响文件、激活和在途操作 |
| POST /credential-packages | 准备签发和下载 | 同 credential.prepare_download；当前 ACTIVE 授权 |
| GET /credential-downloads | 当前可信客户端待领取任务与非秘密状态 | 当前主体与已认证 clientInstanceId 范围，供组件有界同步；不返回密文或 bearer 链接 |
| POST /credential-downloads/{downloadId}/claim | 一次领取密文文件 | 员工本人及获准客户端、独立认证；响应 application/octet-stream |
| GET /credentials/{id}、POST /credentials/{id}/revocations | 文件状态、文件级吊销 | 本人或 credential.manage；不提供 decrypt |
| POST /credential-contexts | 建立当前客户端上下文 | 验证员工身份和客户端；随机令牌由私有通道交给组件 |
| POST /credential-contexts/{id}/renewals | 到期前续期当前客户端上下文 | 当前员工身份及上下文令牌有效、未撤销且未超绝对期限；不重新激活失效包 |
| POST /credential-contexts/{id}/activations | 递交密文并激活资源 | 需要当前身份和上下文令牌；请求体不进入日志和模型 |
| DELETE /credential-contexts/{id} | 退出及清除当前激活 | 本人或明确管理范围；本地删除不代替服务端关闭 |
| GET /revocations/{id} | 查看网关、在途与原生清理状态 | 对应主体或管理范围，字段脱敏 |

浏览器与客户端 API 使用分别声明的认证方案和受众，不默认互认 cookie、MCP Bearer、OIDC ID token。客户端私有接口的 access token 需有相应 audience/scope；采用同一网关资源受众时，也须逐路由验证 scope。跨服务调用不透传入站 MCP token。

浏览器变更继续验证 Origin、CSRF、If-Match 和 Idempotency-Key。资源更新、决定、取消、吊销提交 expectedRevision；MCP 适配层映射至同一业务命令，不能形成较宽松的实现。

下载与上下文令牌不是普通管理幂等响应，不能把秘密保存在去重快照后任意重放。只保存非秘密提交状态和 blob 引用；claim 消费后响应丢失需先查领取状态，通过新 ticket 重新交付同一仍有效包，不创建新的原生账号或授权。

## 5 错误与恢复边界

| 代码 | 含义与客户端动作 |
|---|---|
| ACCESS_DENIED | 对象不可见、归属不符或权限不足；统一安全消息，不帮助枚举 |
| RESOURCE_UNAVAILABLE | 资源停用、版本不可用或依赖故障；不能换目标继续 |
| ACCESS_PENDING / PROVISIONING_PENDING | 申请或原生开通未完成；查状态，不自动新建申请 |
| PROVISIONING_UNKNOWN | 原生开通效果未确定；只读核对同一任务，禁止盲重建 |
| CREDENTIAL_REQUIRED | 缺少本增量的有效包激活；通过可信组件领取或激活 |
| CREDENTIAL_INVALID / CREDENTIAL_EXPIRED / CREDENTIAL_REVOKED | 包无效、过期或已撤销；无效与归属不符对未授权方合并为安全拒绝 |
| CREDENTIAL_CONTEXT_EXPIRED | 客户端上下文已过期或需更新；由可信组件重建并按客户端实际能力刷新连接头，不触发业务写自动重放 |
| GRANT_INACTIVE / BINDING_CHANGED / PLAN_STALE | 授权无效、身份/资源语义变化或冻结计划过时；重新申请或新建计划，不迁移旧批准 |
| IDEMPOTENCY_CONFLICT / REVISION_CONFLICT | 键复用异载荷或版本冲突；刷新对象，不能随机换键危险重试 |
| DEPENDENCY_UNAVAILABLE | 当前身份、权威状态或必要凭据能力不可确认；停止新执行 |
| OUTCOME_UNKNOWN | 已领取或派发后结果不确定；查询原 Action，不重放写入 |

只有入口身份认证失败使用对应 401/403 认证挑战。包、业务授权和后端原生认证失败使用工具业务错误或 REST 对象错误，不伪装成员工登录失效，避免认证刷新自动重试业务写入。

## 6 逻辑存储和事务约束

后续迁移新增资源版本、套餐、申请/决定、开通任务、账号绑定、授权、包/租约、下载 ticket、context/activation 和撤销墓碑等表；此处不提供可执行 DDL。本轮不修改原 SQL 模型或运行迁移。

约束至少包括：所有外键含 orgId；ID 不跨组织引用；版本不可原地改写；决策唯一绑定快照；requestKey 并发唯一；grant 只能在已核验绑定下激活；同一开通任务只有一次管理派发资格；包的 credentialId 永不复用；下载 ticket 只能消费一次；context 令牌只存 verifierHash；业务 Execution 对 Action 的永久唯一约束继续有效。

申请决定、授权变更、包签发/撤销及执行领取均与关键审计同事务。统一锁顺序为 authorization head → Resource → AccessGrant → CredentialLease/Package → Action/资格 → 幂等及审计；不在持锁期间调用原生服务。ContextActivation、客户端缓存及进程内判断均不能替代当前权威状态。

撤权更新当前状态与 authEpoch，并使已有激活映射失效。恢复旧快照后按 recovery fence 核对，不能因数据库里重新出现 ACTIVE 就恢复访问。原生管理任务和业务执行分别记录，不混用 executionId，也不把开通任务的允许重核对推广成业务写重试。


## 7 RA-04 实现修订：0.2.1（2026-10-24）

原文保留为设计记录。当前唯一机器源仍为 internal/contracts/v02/source.json，修订号 0.2.1；原 0.2.0 候选见[原字节快照](../../specs/v0.2/history/0.2.0/README.md)。PROVISIONING 允许没有尚未产生的 accountId/bindingId，ACTIVE 的契约仍要求二者；当前运行数据库禁止直接 ACTIVE。Grant 增加 requestId、decisionId 及独立状态 revision。

access.status 和决定响应使用 AccessStatusResult 关联 request、decision?、grant?。拒绝不提交 approvedExpiry；批准必须携带且只可缩短申请期限。新增显式策略版本、期限和资源限定审批配置，不从旧演示管理员/组或字符串引用自动推导预授权。

实际人工决定入口为 /console/api/v2/access-requests/{id}/decisions，校验独立浏览器会话、Origin、CSRF、If-Match 和 Idempotency-Key。对应 OpenAPI 声明独立 server/cookie 认证；Bearer /api/v2 和 MCP 没有批准别名。当前终态和幂等快照规则见[流程说明](../../resource-access-workflow-local.md)。


## 8 RA-05 实现修订：0.2.2（2026-10-26）

规范源仍为 internal/contracts/v02/source.json，新增 ProvisioningTarget、ExistingAccountRegistration、ProvisioningConfiguration、ProvisioningPlan、NativeObservation。NativeAccount 强制关联资源版本和实际权限观察，CredentialBinding 可关联 accountId；access.status 增加脱敏 provisioning 摘要。当前契约共 71 个定义、42 个正反样例、23 个 HTTP 契约、11 个 MCP 契约；列出未来接口不等于已经注册。

批准版本 1 不写账号/绑定；独立开通进程实际核验并复查当前资格后，追加含 accountId/bindingId/observation/job 关联的版本 2，再切 ACTIVE。原版本不覆盖。失败状态包含 PROVISIONING_UNKNOWN/PROVISIONING_FAILED，不能通过重放开通意图将其直接激活。原 0.2.1 见[字节归档](../../specs/v0.2/history/0.2.1/README.md)，实现及边界见[RA-05 说明](../../native-provisioning-local.md)。


## 9 RA-06 实现修订：0.2.3（2026-10-26）

规范源仍为 internal/contracts/v02/source.json。ProvisioningJob 增加 CLEANED/MANUAL_REQUIRED 及可选 ProvisioningRecovery：generation、核对和清理次数、DISPATCH/RECONCILE/CLEANUP/FINALIZE、cleanupState、固定结果码和时间界限。owner nonce、原生回执/连接及密码不返回客户端。旧 v3 generation=0 仅表示迁移元信息，首次真实接管前不伪造公开恢复代次。

当前 72 个定义、46 个正反样例、23 个 HTTP 契约、11 个 MCP 契约通过；没有新增公开恢复或管理 MCP 工具。只有核对同一原生结果、当前资格和期限后，PROVISIONING_UNKNOWN 才能追加 ACTIVE；REVOKED、EXPIRED、PROVISIONING_FAILED 不恢复。CLEANED 只证明本计划模板清理，不能解释为授权成功或在途已停止。旧 0.2.2 见[原字节快照](../../specs/v0.2/history/0.2.2/README.md)，当前见[恢复说明](../../provision-recovery-local.md)。


## 10 RA-07 实现修订：0.2.4（2026-10-26）

唯一规范源仍为 internal/contracts/v02/source.json。新增 CredentialIssuer、CredentialLeasePolicy、CredentialConfiguration 和服务器内部 IssueCredentialInput；CredentialStatus 显式增加 revision，约束 issuer/keyId、固定 AES-256-GCM/AAD、12 字节 nonce、16 字节 tag、8192 字节引用和每密钥最多 2^32 次持久预留。

76 定义、52 正反样例、23 HTTP 契约、11 MCP 契约及 8 份 SQL 均校验通过。只有 credential.status 及本人 GET 状态进入 RA-07 可选注册，规范中的其他后续接口继续 PLANNED。服务端签发/校验/关闭命令不是公开 MCP 接口，没有解密 API。旧 0.2.3 见[原字节归档](../../specs/v0.2/history/0.2.3/README.md)，五份生成物由同一源生成，未来契约不等于实际入口。


## 11 RA-08 实现修订：0.2.5（2026-10-26）

新增 DownloadIDInput、严格空 ClaimDownloadInput 和安全 DeliveryAuthorizationInfo。DownloadTicket/PrepareDownloadResult 显式区分 ticket expiresAt 与 credentialExpiresAt，增加 revision、MANUAL_OWNER 与服务器写结果；幂等重放可返回 READY/CLAIMED/EXPIRED/CANCELLED，不能重放文件。私有授权原值不是模型结果字段。

当前 79 定义、58 正反样例、24 HTTP 契约、11 MCP 契约、9 份 SQL 通过。credential.prepare_download 与 POST 准备进入 RA-08，新增私有 GET ticket 状态，claim 必须同时有 employee 和独立 delivery 认证。队列同步、Context、激活等仍 PLANNED，运行注册没有提前开放。0.2.4 六份原字节见[归档](../../specs/v0.2/history/0.2.4/README.md)，入口与语义见[说明](../../private-downloads-local.md)。
