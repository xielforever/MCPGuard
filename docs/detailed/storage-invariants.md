# 存储约束与事务落地说明

版本：v0.1补充设计。日期：2026-10-22。对应[状态设计](state-storage.md)与[SQL模型](../specs/metadata-model.sql)。

SQL是空隔离库的模型提案，不是现有程序的migration，不由应用或文档检查器自动执行。
使用PostgreSQL16+基础能力；UUID、HMAC摘要和加密原文由应用提供，不安装扩展、建立全局角色或连接原生服务。
文件创建独立mcpguard_model schema，已存在同名schema时失败，避免把重复执行误当成升级。

## 1 领域与表的映射

| 领域 | 表与关键关系 |
|---|---|
| 本地授权版本 | authorization_heads；一个组织一条auth_epoch门禁 |
| 主体 | principals；issuer与subject在组织内唯一，组/角色为可信映射后的本地快照 |
| 资源/工具/策略/凭据绑定 | catalog_revisions保存不可变候选/发布版本，catalog_heads.version是活动指针，row_version是独立管理revision |
| 受保护原文 | protected_payloads；计划、结果、证据、审计详情和命令响应按purpose区分；审批详情由PLAN受权展示 |
| 业务意图 | actions；一个Action固定一个plan_ref、plan_hash、主体和交互模式 |
| MCP尝试 | attempts；RPC ID保留字符串/整数类型，仅在client_scope中关联 |
| 人类决定 | decisions；绑定Action及plan_hash，approver_id引用另一可信主体 |
| 执行资格与事实 | executions；Action唯一领取记录，创建不证明发送 |
| 审计 | audit_events；对象间组合外键防止把别的Action的Attempt/Execution拼进事件 |
| 通知 | outbox；只允许审批请求、状态变化和受控导出通知 |
| 管理命令去重 | idempotency_records；按组织、主体、命令、目标及key_hash唯一 |

principals中的组与角色不是客户端自报字段，也不是完整企业目录或对象级授权表。
IdP映射与具体资源/数据权限尚需结合企业配置落实；SQL约束不能证明某个组或token可信。
模型未开启RLS，组合外键防止跨组织错误引用，不自动隔离任意SQL查询；所有对象操作仍须使用可信组织上下文鉴权。
JSON定义只保存非秘密配置及凭据引用；连接密码、token、原始敏感参数与响应体进入受保护载荷，不能藏进definition或safe_payload。

Tool/Policy尚未发布时活动version可空且enabled必须为false；资源与凭据绑定始终有有效配置版本。
API管理ETag使用row_version，不与publishedVersion/活动version混用。追加候选版本只推进管理revision，不切活动指针、不推进auth_epoch或失效执行中使用的发布快照。
真正发布、启停或活动配置变更才在授权头事务内推进auth_epoch；回退到某个既有版本也是一次新的管理变更，不能复活旧批准。

## 2 数据库强制的不变量

| 编号 | 模型强制约束 | 应用仍需完成 |
|---|---|---|
| S-01 | 组织范围的组合主键/外键 | 从验证后主体取得org，不信任请求自报组织 |
| S-02 | Action计划、主体、期限与交互模式不可修改 | 规范化、HMAC验证、前置条件和计划内容正确 |
| S-03 | catalog revision、Decision、审计不可原地更新/删除 | 发布检查、决策人资格、日志脱敏与变更审查 |
| S-04 | Execution对(org,action)唯一，禁止删除资格记录 | 只有合法领取者能进adapter；不能由租约接管 |
| S-05 | Action合法前后态与row_version推进 | 权限、期限、模式、取消和策略检查 |
| S-06 | 人类决定不能由原操作人自己写入 | 实际审批人来自认证上下文，具备当前审批资格 |
| S-07 | 已知派发事实不倒退，未知凭新增证据收敛且不能恢复RUNNING | 判断发送/提交证据可信度，禁用驱动隐式重送 |
| S-08 | Action/Execution状态在事务提交时一致 | 领取和完成使用同一本地事务 |
| S-09 | 载荷只能单向清理为保留引用的墓碑 | 活跃计划不能提前清理，正确加密与密钥管理 |
| S-10 | 管理去重身份不可改，未完成/未过期记录不可删除 | 同键同体比较、重放重新鉴权、TTL契约 |
| S-11 | Outbox去重身份与payload不可改 | 发送后CAS确认、退避及接收方幂等 |
| S-12 | 各种状态枚举与关键值域 | 业务风险分类、数据范围和返回Schema |

模型触发器使用调用者权限，不依赖SECURITY DEFINER获得额外授权；应用运行角色不得拥有schema、ALTER/TRUNCATE或禁用触发器权限。
“应用追加审计”不表示数据库管理员不能修改；外部防篡改保留机制属于独立部署选择。

## 3 状态与关联的一致性

Action初次只能是PENDING_APPROVAL、策略放行的READY或DENIED，不能插入一条声称已经执行完成的业务历史。
READY来源区分POLICY与HUMAN；人类路径进入READY必须记录HUMAN，普通读不虚造Decision。
Decision之外的审批资源是Action的受控视图，approvalId映射Action ID，不维护第二套待批状态。

未领取的DENIED、EXPIRED、READY、PENDING_APPROVAL不能有Execution；CANCELLED可表示派发前取消而没有Execution。
EXECUTING、SUCCEEDED、OUTCOME_UNKNOWN必须有关联Execution；有Execution时，终态与request_status保持一致。
数据库使用DEFERRABLE INITIALLY DEFERRED约束触发器，允许同一事务先更新Action、再插入Execution。
检查读取的是提交前该Action的最终行状态，不依赖某次中间UPDATE捕获的旧状态。

同一个Action不能借新execution ID、删旧记录、改plan_ref、延长expires_at或换interaction_mode恢复资格。
已知终态的请求结果不可改成另一种请求结果；副作用仍为UNKNOWN时可追加可信核对证据并收敛副作用状态。
OUTCOME_UNKNOWN可依据证据收敛为成功/失败/取消，但不能变回READY、EXECUTING或RUNNING。
派发、请求结果或副作用从未知收敛时，数据库统一要求新增非空且不同于旧值的evidence_ref；引用存在不证明其内容权威，仍由应用核验。
每次收敛在同一事务追加审计；数据库不判断“当前值恰好相同”是否能证明某次写入发生。

## 4 领取、批准、取消的事务顺序

锁顺序固定为授权头→Action→Execution/Decision；同层多对象按ID排序。
本地授权/组/资源/工具/策略/凭据绑定修改在同事务持授权头排他锁并推进auth_epoch。
调用、决定与领取持共享锁，在锁内检查当前主体、版本、期限、策略和审批者资格。
所有网络访问在短元数据事务之外，不能持锁等待IdP、通知、人类或原生结果。

领取事务先确认Action为READY，校验有效Attempt/续传和完整计划；再做带row_version的条件UPDATE。
只有恰好一行更新时，才插入INTENT_RECORDED/RUNNING/NONE的Execution、必要审计和通知意图。
确认本地COMMIT后才允许adapter开始；COMMIT结果不明时查询核对，不猜测成功后发包。
另一并发领取者受Action锁/CAS和Execution唯一约束限制，不能向原生服务发送第二次操作。

批准/否决必须核验plan_hash、操作人、另一审批人、当前资格、期限与所选OTHER_PERSON要求。
先写不可变Decision，再推进Action及审计；source_event_id重复但请求摘要不同应返回冲突。
取消与领取在同一Action行上串行；未领取时终止，已领取时仅记录取消请求并尽力处理。
本地撤权先提交则阻止后续领取；领取先提交后的撤权不能保证召回已在途的原生效果。

## 5 派发窗口与副作用证据

原生业务写可能开始之前，先在元数据事务把该Execution的side_effect_status改为UNKNOWN并提交，再调用原生驱动。
此持久化失败就不发送；不能在后端可能已经生效、发送日志尚未来得及写入时，仍让查询断言副作用为NONE。
因此INTENT_RECORDED可同时为RUNNING/UNKNOWN：它只有已消费意图，尚无明确发送证据。
创建Execution时的NONE仅表示准备阶段已知尚未开始，不能当成已完成无副作用的最终结果。

取得可信发送事实后记录DISPATCHED；TCP写成功不证明业务已提交，写报错也不证明未传输。
确定结果后分别记录request_status及NONE/APPLIED/ROLLED_BACK/PARTIAL/UNKNOWN，失败不自动等于回滚。
丢失拥有者且无法确认是否发送时进入DISPATCH_UNKNOWN/OUTCOME_UNKNOWN；已知发送事实不倒退。
若旧sender已隔离，且取得权威证据证明命令根本未发送，可将DISPATCH_UNKNOWN收敛为NOT_DISPATCHED/NONE，但request_status只能是FAILED或CANCELLED。
该终止收敛必须绑定新的受控证据引用，保留原Execution与资格墓碑；不能进入INTENT_RECORDED、READY或RUNNING，也不重置租约供重新发送。
普通人工备注、缺少发送日志、当前业务值相同均不等于未发送证明；已知DISPATCHED不能退回NOT_DISPATCHED/NONE。
原生错误的请求结果已经明确FAILED但副作用UNKNOWN时，后续核对只收敛副作用，不伪造一次成功请求。

owner_instance应包含实例启动代际；claim_token与row_version用于旧拥有者和当前状态CAS。
hard_deadline在领取时固定，recovery_after保存当次hard_deadline加grace的绝对上界；lease不能越过它。
心跳只延长健康观察窗口，不新增执行权；watchdog处置前重新核对当前lease、token、拥有者及状态。
租约过期不转移拥有者、不重发；隔离旧进程的能力来自部署控制，不能由数据库token替代。

## 6 敏感载荷、去重与保留

protected_payloads只存经过认证加密的密文与外置密钥引用/版本；purpose与解密后类型由应用校验。
同一计划不通过换密文引用或重新编码改变业务含义；需要改变计划时创建新Action。
清理仅把ciphertext置空、记录purged_at并保留引用行；未完成计划和有效幂等响应必须先检查引用生命周期。
Execution资格墓碑不得随着结果数据或审计详情TTL一起删除，旧Action命名空间不能重新获得发送资格。

随机管理幂等键的稳定key_hash用于唯一查找，请求摘要的算法/密钥版本随记录保存；重放按原记录版本比较。
不能仅因密钥轮换，使用新摘要绕过原去重记录；密钥不可用时明确失败，不假装是新命令。
未完成命令不可因TTL清理；完成键被清理后不再承诺识别旧键，客户端必须遵守去重有效期。
通知可以至少一次重投，safe_payload只携带最少摘要、event ID与受控详情链接；Outbox不是原生写重试表。

运行角色按用途授予具体表/列权限，迁移、日常运行、受控清理和审计读取角色分离。
清理Outbox仅处理已送达且超过保留期记录；数据库所有者的控制能力不作为普通API权限。
审计元数据归档/删减需按留存策略另行制定受控迁移，不由普通清理角色绕过追加约束；执行资格墓碑继续保留。
授权auth_epoch描述本地权限变化，不是灾难恢复代际；从旧备份恢复可能丢失已消费的执行资格。
恢复先关闭写派发，依据独立恢复代际与外部业务证据核对，不能只递增auth_epoch就把旧快照当成安全许可。

## 7 评审与验证入口

SQL包含所有表、组合外键、查询索引和触发器声明；没有运行用户、生产DSN、示例秘密或原生写语句。
执行模型前，应由实施环境建立独立空测试库并固定PostgreSQL minor；本轮不自动应用到任何数据库。
本稿提供模型约束，实际空库初始化、触发器行为和并发锁测试仍需记录数据库证据。
重点对应AC-06/07/09/10/11/12/13、A-04/05/09/10/11，具体场景见[验证计划](validation-plan.md)。

验收需覆盖：自批与伪造资格、跨组织FK、同Action双领取、意图窗口副作用UNKNOWN、终态回退、重复决定及命令、早期清理、旧租约恢复和备份回退。
数据库模型通过不能替代HTTP/stdio、ChatGPT、企业身份与真实原生服务的验收。
