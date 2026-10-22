# 状态与存储详细设计 v0.1

状态：可实施设计建议，待评审，尚未实现。日期：2026-10-22。
依据：[领域契约](../contracts.md)、[需求](../requirements.md)、[原生适配](../native-adapter-design.md)。
基线建议为模块化 Go 单服务与独立 IDE connector；PostgreSQL 为唯一权威元数据库。
使用 PostgreSQL 16+ 基础能力，部署选择仍受支持的 minor；业务 Redis 不承担状态依赖，不默认引入 Kafka 或外部队列。
已确认受控目录、风险分级、受控写他人审批与ChatGPT首发；工程上首版采用单级决策，具体工具条目、eligible group、IdP、留存期限与容量待定。

## 1 实体与状态边界

| 实体 | 持久职责 | 禁止混同 |
|---|---|---|
| Action | 一个主体对固定计划的业务意图、策略结论、授权及派发资格 | 不是一条 HTTP 连接或 JSON-RPC ID |
| Attempt | 一次 MCP 请求尝试，记录 profile、连接关联域、RPC ID 及结束原因 | MRTR 新 ID 可关联原 Action，不能因此重获执行资格 |
| Execution | 领取资格与记录意图时创建的一条原生派发跟踪 | 创建记录不证明请求已发送或后端已提交 |
| Decision | 可信人类对 action、计划摘要及策略要求的决定 | 客户端 accept、消息送达或跳转不构成批准 |
| AuditEvent / Outbox | 本地关键事实 / 可重复投递的通知事件 | Outbox 不是原生操作重试队列 |

建议 Action 状态为 PENDING_APPROVAL、READY、EXECUTING、SUCCEEDED、FAILED、DENIED、CANCELLED、EXPIRED、OUTCOME_UNKNOWN。
DENY 优先于 ALLOW 与人类批准，无匹配授权规则时默认拒绝；批准不增加资源准入权。
已获资源与操作权限的普通读取自动放行，受控写进入单级他人审批，高危操作默认禁用；不能靠普通批准覆盖高危禁令。
首版仅执行已注册的受控工具，不启用通用 SQL、任意 Redis 命令、任意 URL 或本人二次确认入口。
审批前先解析、校验并约束完整计划；计划固定主体、资源/工具/策略/凭据版本、真实操作、参数、前置条件及期限。
v0.1 建议一个 Action 固定一个计划；需要改变计划则终止旧 Action，创建新 Action 并重新评估，旧决定不迁移。
plan_hash 对规范化完整计划使用带版本的 HMAC 摘要，避免敏感低熵参数被普通摘要枚举；摘要不代替受保护原文。

完整计划采用带encodingVersion的确定性编码：校验后的类型化参数转换为规范JSON，大整数/Decimal按约定字符串、二进制按显式编码表示，拒绝非有限数值与含糊类型。使用RFC8785规范化允许的数据子集，HMAC-SHA256绑定组织、主体、目标、版本、原生操作、完整参数和全部约束；不以压缩SQL空白或关键词替换等方式猜测操作等价。
摘要密钥与数据加密密钥用途分离，记录key version；轮换后保留未结束计划所需验证能力，缺失密钥时禁止执行。所有续传重新校验服务端保存计划，不以客户端摘要代替原文。

## 2 元数据模型与关键约束

所有对象带 org_id；即使首版单企业，也用组合外键阻止跨组织错误引用。UUID 由应用生成，不要求额外数据库扩展。
完整空库模型见[metadata-model.sql](../specs/metadata-model.sql)，包括组合外键、状态约束、索引与不可变触发器；[存储约束说明](storage-invariants.md)逐项区分数据库保证与应用职责。该模型不是生产migration，本页不再复制另一份DDL。

| 表 | 核心约束 |
|---|---|
| authorization_heads / principals | 组织授权epoch、可信issuer/subject映射、本地角色组快照 |
| catalog_revisions / catalog_heads | 内容版本不可变；当前发布指针与管理row_version分离，未发布工具/策略可无活动版本 |
| protected_payloads | 参数/结果/证据密文、用途、密钥版本和单向清理墓碑 |
| actions / attempts | 一个固定计划与交互模式；每次MCP尝试保持独立关联 |
| decisions | 另一可信主体的不可变决定，绑定Action和完整计划摘要 |
| executions | 同组织Action永久唯一资格；deadline、恢复上界、租约和证据分开 |
| audit_events / outbox | 同事务关键审计；通知至少一次，禁止原生写重投 |
| idempotency_records | 组织/主体/命令/目标/键唯一，明确响应和保留生命周期 |

catalog_revisions 保存不可变资源、工具、策略和凭据绑定版本；其 definition 只含凭据引用与版本，不含秘密。
catalog_heads.version是活动/发布指针，row_version是管理ETag。追加未发布候选只推进管理revision，不切发布指针或auth_epoch；真正发布、停启及影响访问的配置变更才进入授权版本边界。
授权普通读转READY时记录authorization_source=POLICY，合格人类决定满足要求后转READY时记录HUMAN；READY本身不证明有人批准。
plan_ref 保存完整已解析计划，plan_summary 是脱敏展示；所有执行参数来自 plan_ref，不从通知或续传重新拼装。
通过列级 UPDATE 授权与迁移触发器禁止改写 Action 计划字段、已发布 revision 和 Decision；变更只能追加版本或事件。
转换函数验证Action合法前后态；Attempt 的 rpc_id 保留类型，不建跨连接唯一约束。
Execution 的 UNIQUE(org_id,action_id) 是永久单次资格约束；不能删除记录或换 execution ID 规避它。
Decision 去重还须校验已有事件内容相同；同 source_event_id 的不同内容作为冲突拒绝，不静默覆盖。
补建 Action 按主体/时间、Attempt 按 action/时间、Decision 按 action/时间的索引，并索引全部实际查询的外键列。
待审批索引建议为 (org_id,expires_at,id) WHERE state='PENDING_APPROVAL'；READY 工作集按组织/创建时间建部分索引。
Execution 对 RUNNING 的 lease_until 建部分索引；用 EXPLAIN 验证真实查询，不为全部 JSON 字段建立 GIN。

## 3 授权、拒绝及一次领取事务

所有本地身份/组权限、资源停启、当前 revision、凭据绑定和策略变更，必须在同事务锁定 authorization_heads 并递增 auth_epoch。
调用和批准事务对该行持 FOR SHARE；配置/撤权持 FOR UPDATE。共享锁允许不同 Action 并发，变更锁提供本地串行边界。
锁顺序统一为授权头 → Action → Execution/Decision；涉及多个对象时按 ID 排序，事务内不访问 IdP、IM 或原生服务。
解析与策略预计算可在事务外完成，但落库及领取时必须在锁下核对 epoch、版本和有效性；过时则拒绝并重新计划。
以下 ASSERT 表示应用校验失败即 ROLLBACK，不是 PostgreSQL 内置语法；所有 SQL 使用绑定参数。

```sql
BEGIN;
SELECT auth_epoch FROM authorization_heads WHERE org_id=$org FOR SHARE;
SELECT * FROM actions WHERE org_id=$org AND id=$action FOR UPDATE;
-- ASSERT 身份有效、actor有准入权、plan_hash正确、basis_epoch等于当前值；
-- ASSERT 当前资源/工具/策略/凭据版本允许执行，期限用clock_timestamp()在持锁后检查；
-- ASSERT state=READY、没有拒绝/取消/过期；受控写有一名合格且不同于发起人的审批者，当前仍有资格；
-- ASSERT 此交互模式允许当前Attempt/续传领取，不因审批回调到达自动派发。
UPDATE actions SET state='EXECUTING',row_version=row_version+1
 WHERE org_id=$org AND id=$action AND state='READY' AND row_version=$expected
   AND cancel_requested_at IS NULL AND expires_at>clock_timestamp()
 RETURNING id; -- 必须恰好1行，否则整体回滚
INSERT INTO executions(org_id,id,action_id,plan_hash,owner_instance,claim_token,lease_until,hard_deadline,recovery_after,
                       dispatch_state,request_status,side_effect_status)
 VALUES($org,$exec,$action,$hash,$instance,$claim,$lease,$hard_deadline,$recovery_after,'INTENT_RECORDED','RUNNING','NONE');
-- 同事务INSERT AuditEvent(EXECUTION_INTENT)；必要通知INSERT Outbox；任何持久化错误均回滚。
COMMIT;
-- 仅确认COMMIT成功的领取者可进入adapter；COMMIT结果不明时只查询核对，不猜测成功后发包。
```

interaction_mode 创建后不可改：BOUNDED_WAIT 仅原有效调用领取，等待结束且未派发则终止；CONTROL_FLOW 保持业务待批。
CONTROL_FLOW 的普通最终结果为 DeferredActionResult，action.status 查询、action.resume 显式续传、action.cancel 取消；resume不接受新业务参数。
MRTR 仅有效状态绑定的新RPC领取；CONTROL_FLOW仅经鉴权且满足条件的resume领取；Attempt结束不统一等于Action取消。
所有模式的审批回调只持久化决定与通知，不触发adapter；领取时须验证活跃调用或有效续传，Tasks非首版依赖。
批准/否决也锁授权头和 Action，验证决策身份、资格、模式、计划摘要与期限，先 INSERT Decision 再变更 Action。
首版管理API的审批资源是Action的受控视图，approvalId映射Action ID，Decision单独有唯一ID；不另维护一套与Action可能分叉的待批状态。
REJECT 可将尚未领取的 PENDING_APPROVAL/READY 变为 DENIED；READY 后并不禁止有效否决先于领取生效。
APPROVE 仅在无 deny/拒绝/取消且满足所选要求时转 READY；重复批准返回已有结果，不重置状态或期限。
受控写的approval_requirement采用 `mode=OTHER_PERSON, minApprovals=1`：一名eligible group内审批者同意，可信主体必须不同于发起人；多条规则组合仍需满足全部资格约束。
eligible group 的具体配置仍待确定；未配置或没有合格他人时不取得执行资格，不回退为自批或管理员自动批准。
普通读取由策略记录 ALLOW 后转 READY，不虚造人类 Decision；高危操作保持 DENIED，首版不启用 self-confirmation。
本地撤权事务：BEGIN → 授权头 FOR UPDATE → 修改权限/停用或当前版本 → auth_epoch+1 → AuditEvent → COMMIT。
粗粒度 epoch 变化使旧计划重新校验或失效，首版接受其保守性；未来细化版本向量须保留同等原子性。
JWT 签名有效不代表未被企业撤权；每次调用、批准、领取和结果查询都检查本地授权状态。
外部 IdP 撤权只能在推送、内省或同步被接收并提交后进入此边界；延迟与失联策略必须配置，不能声称即时感知。
领取提交是本地授权线性化点，不是与远端发包的原子事务；之后提交的撤权无法保证阻止已领取操作。
可在确认尚未发包时再次检查并终止；该检查仍不能消除检查与网络发送间的跨系统竞争，已派发只做尽力取消。

## 4 取消、派发证据与重启恢复

取消先验证调用者及 Action 归属，再按该 MCP profile/交互契约判断是取消 Attempt 还是终止业务 Action。
未领取时在 Action 行锁下转 CANCELLED，与批准、否决和领取串行；已有终态不被迟到决定覆盖。
已领取时记录 cancel_requested_at、审计和取消意图；不能仅凭HTTP连接关闭或持久状态缺项宣称原生写入未执行。

| dispatch_state | 记录含义 | request / side effect |
|---|---|---|
| NOT_DISPATCHED | 尚无 Execution，或原领取者证明根本未发送并封闭发送路径 | 未派发拒绝/取消时 NONE；不得重新消费同一Action |
| INTENT_RECORDED | 领取与意图已提交，原执行者正常推进，尚无派发证据 | RUNNING；确认未发出阶段可为 NONE |
| DISPATCHED | adapter 有明确派发证据，不等于提交成功 | 成功、失败或 OUTCOME_UNKNOWN；副作用据实记录 |
| DISPATCH_UNKNOWN | 崩溃/断联后无法确定是否发出 | OUTCOME_UNKNOWN；可能写入为 UNKNOWN，已审核只读可为 NONE |

原执行者更新时必须以 execution ID、claim_token 和预期状态 CAS；成功响应及证据尽量同本地事务落库。
DISPATCHED 是观测事实，不是可靠接收回执；内存“准备发包”或 journal 提交不能作为该证据。
业务写可能开始前，原执行者先持久化side_effect_status=UNKNOWN再调用驱动；失败则不发送。INTENT_RECORDED在正常推进时可为RUNNING/UNKNOWN，不能把尚无发送证据等同于副作用确定为NONE。
Action.expires_at是最晚领取/续传时间；Execution.hard_deadline在领取时固定，取有效调用、工具执行预算与计划执行约束的最严值，不能靠心跳延长。它与审批等待期限分别管理。
执行者独立心跳按execution ID、claim_token、owner和RUNNING状态CAS续租，使用数据库时间；lease窗口大于心跳间隔并留抖动预算，不能无限续过hard_deadline加配置版本固定的grace。续租失败时原执行者停止尚未发送路径并尽力取消在途操作，记录可取得的事实。
watchdog扫描只产生候选；处置前锁定或CAS重查当前lease、claim_token、owner和状态，不能依据陈旧扫描快照中断已正常续租的执行。租约只反映健康，不授予接管派发权。
确认过期后不更换owner、不重新调用adapter；按部署控制能力隔离/终止对应实例启动代际，再CAS转未知并告警。隔离未确认时仍保留不确定性，不能宣称旧执行者必然停止或可安全接管。
只允许有证据的派发状态迁移；不得将未知改回INTENT重跑，已确认派发的事实也不能被后到的超时覆盖。
原生数据前置条件在adapter自己的事务或服务契约边界核验，不能放在持元数据锁的领取事务中做网络检查。
隔离/终止旧进程用于防止其恢复后继续发送；仅靠元数据 fencing token 不能阻止不理解该token的PG/Redis/HTTP。
恢复遇 INTENT_RECORDED 且不能证明未发送则标 DISPATCH_UNKNOWN；有已发送证据则保留 DISPATCHED，结果仍未知。
进程曾看到成功但尚未落库也属于恢复未知窗口；不得由“数据库没有成功记录”推断失败。
核对只使用可信原生状态/业务幂等查询，不自动重发；后续证据可收敛未知结果，追加审计且不产生新派发。
只有隔离旧发送者并取得权威未发送证明，派发未知才可终止收敛为NOT_DISPATCHED/NONE与FAILED或CANCELLED；保留Execution资格墓碑。普通备注或查不到回执不足以证明未发送，已知DISPATCHED事实不能退回未发送。
需要重做必须单独创建新 Action、展示先前不确定性并重新授权；同一副作用不能伪装成安全自动重试。

## 5 审计、Outbox 与敏感信息生命周期

建议 audit_events 保存 event ID、org/action/attempt/execution、actor、类型、时间、计划摘要及受控详情引用，只允许业务角色追加。
同一次状态转换、必要审计及 outbox 在一个 PG 事务提交；关键审计无法写入时不派发，不依赖异步补记授权事实。
outbox 核心列为 id、org_id、dedupe_key、event_type、safe_payload、available_at、attempts、lease_until、claim_token、delivered_at。
UNIQUE(org_id,dedupe_key) 防止重复产生同一通知；对 delivered_at IS NULL 的 available_at 建部分索引。
worker 用短事务 SELECT ... FOR UPDATE SKIP LOCKED + UPDATE 租约，COMMIT 后发送IM；按 claim_token CAS 确认送达。
发送成功后崩溃会重复投递，因此通知为至少一次；消费者按 event ID 去重，重复点击仍走 Decision/Action 幂等。
仅投递审批通知、状态变化和受控审计出口；禁止outbox消费者重投PG写、Redis命令或业务HTTP操作。
通知失败按退避和期限重试，不能变成批准；通知平台HTTP成功还须核验业务状态，死信供人工处理。
管理命令另存idempotency_records：org_id、actor_id、command_type、target_id、key_hash、request_hash、command_id、state、response_ref、expires_at。以组织/主体/命令/目标/键的唯一约束裁决并发，同键异体409；记录与业务状态/审计同事务，响应引用受控存放。
未完成命令不得因TTL清理而重复执行；清理已完成键后不再承诺识别旧键，客户端必须遵守已公布有效期。该表只去重管理命令，不接管原生写重试。
敏感计划参数、结果和原生证据分别加密到 protected_payloads，密钥引用/版本外置；采用应用层加密，不要求pgcrypto。
结果按配置限量/截断，默认不持久化无限数据；查询详情重新鉴权，审计员不自动拥有全部业务数据读取权。
期限按计划/结果/证据/审计类别配置；未完成 Action 所需计划不能提前清理，过期或密钥不可用时禁止继续执行。
清理角色将ciphertext置空并记录purged_at，保留payload引用行、派发墓碑和摘要；清理不恢复资格，不级联删除Execution。
备份同样加密、限制访问并有保留周期；删除在线数据不等于备份立即消失，留存与密钥销毁契约需一并记录。
迁移角色独立于运行角色，禁止业务账号修改不可变记录或删除执行资格；运行池设置有界事务/锁等待并监测死锁与队列积压。
验收重点：并发批准/拒绝/取消/领取、本地撤权竞争、提交结果不明、派发前后kill、lease过期旧worker恢复、通知重复及秘密过期。
