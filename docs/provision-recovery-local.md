# RA-06 开通恢复、未知结果核对与清理重试

日期：2026-10-26。范围为独立开通进程的崩溃恢复、同一原生账号的未知结果核对、受限清理重试及迟到结果拒绝。当前证据见[验收记录](validation/local-ra06-2026-10-26.md)。[RA-05 说明](native-provisioning-local.md)和全部历史验收保留。

## 恢复资格与状态

正常建号仍只有一次管理派发资格。原 EXECUTING、OUTCOME_UNKNOWN、CLEANUP_PENDING 不会因进程重启而重新调用 Create；新增租约只允许核对或清理。恢复使用原冻结计划、账号 UUID/名称、目标指纹、目标/套餐版本、秘密版本和 HMAC，不能换账号、换目标或延长期限。

元数据目录迁移 004 新增 provision_recovery 和不可改写的 provision_attempt_events，保留原任务计划、首次结果和审计。新任务首次派发取得 30 秒租约；恢复只接管租约已过期且到达 nextAttemptAt 的任务。每次接管递增 generation，使用私有 owner nonce；完成、失败和激活均在数据库中检查当前 nonce、generation 和数据库时间。普通查询身份没有这些写权限。

| 发现的事实 | 结果 |
|---|---|
| 原租约仍有效，或尚未到重试时间 | 返回当前状态，不派发原生操作、不增加尝试次数 |
| 原生记录、计划、角色 OID 和实际权限一致，授权仍有效 | 重查当前资格/期限/绑定版本后原子追加授权 v2 / ACTIVE |
| 原生账号不存在 | 在原生侧封存原计划，任务 FAILED、授权开通失败；旧进程不能迟到建号 |
| 新建账号存在，但资格已撤销、到期、配置改变或权限不合格 | 不激活，进入 CLEANUP_PENDING；已终止授权不恢复 |
| 清理成功，且确认 LOGIN 与本模板权限已移除 | 任务 CLEANED、cleanupState=SUCCEEDED，原失败/撤权事实保留 |
| 原生服务不可用或清理回执丢失 | 保留 UNKNOWN/PENDING，记录下一次重试时间，不认定成功 |
| 账号所有权、角色 OID、计划完整性或目标不匹配 | MANUAL_REQUIRED；不自动接管、修改或删除外部账号 |
| 达到尝试上限 | MANUAL_REQUIRED，保留原生未知事实及人工处理要求，不继续原生调用 |

正常 VERIFIED 与失败 FAILED、CLEANED、MANUAL_REQUIRED 均不被重复领取。旧 v3 任务迁移时仅补入 generation=0 的恢复元信息，不虚构历史尝试；首次真实接管后才在公开状态中出现恢复摘要。

## 两侧的并发保护

元数据继续按 authorization head → 任务/租约 → 审计的顺序加锁。批量扫描只选候选，不持有队列行锁去等待组织锁；实际领取在组织锁内用行锁和代次判断完成，原生调用在事务外执行。旧进程的过期回写和补偿会被拒绝，不能停用已经由新进程核验激活的账号。

原生提供方增加独立迁移 002，原 001 保持原字节。创建、核对和清理都使用同一账号的事务级 advisory lock；核对不会与尚未提交的建号事务交错得出错误的“不存在”结论。锁的行为按 [PostgreSQL 16 显式锁说明](https://www.postgresql.org/docs/16/explicit-locking.html#ADVISORY-LOCKS)实现并通过真实事务竞争验证。

原生 account_fences 是永久记录。确认不存在或开始清理后，该账号计划不能再次创建；即使旧进程持有原密码和冻结参数，Create 也会拒绝。账号记录和清理同时核对 targetId、accountId、planHash、期限和角色 OID，不通过同名角色猜测所有权。

原生新增函数沿用固定 search_path、SECURITY DEFINER、同一安装事务中撤销 PUBLIC 执行权、只给独立管理角色授权的边界，依据 [PostgreSQL 函数安全要求](https://www.postgresql.org/docs/16/sql-createfunction.html)。普通查询账号不能执行核对、清理或任意 DDL/GRANT。

## 清理和重试边界

清理只针对本计划创建且原生账本/OID 匹配的 PROVISIONED 账号：设置 NOLOGIN，并撤回本模板授予的 SELECT、schema USAGE 和显式数据库 CONNECT；确认实际 LOGIN、有效 SELECT 和 schema USAGE 已消失后才记录 CLEANED。重复清理使用同一账号和永久封存记录，允许核对回执丢失后的既定清理结果，不创建新身份。

不 DROP 角色、数据库、表或业务数据，不处理 PREEXISTING 的原生权限。已有账号在恢复时仍只核验；失效或核验失败不会改密码、NOLOGIN 或撤权。无法确认所有权、同名账号被替换或有效权限仍未撤回时，不标清理成功。

核对和清理分别最多 6 次领取，退避为 5、10、20、40、60 秒，上限 60 秒；数据库时间决定可领取时点。最后一次领取后崩溃，也能通过只结束元数据的 FINALIZE 代次进入 MANUAL_REQUIRED，不会出现第七次原生调用或永久卡在待处理状态。

CLEANED 只证明本轮托管模板清理及禁止新登录。它不等于所有外部权限已删除、已有会话已终止或整个授权吊销流程完成；在途停止、主动池淘汰及完整吊销仍属 RA-13/14。恢复旧备份的全局 recovery fence、密钥与秘密轮换仍属 RA-15。

## 入口、部署与兼容

仍使用独立 mcpguard-provision 及管理凭据，网关不加载原生管理 DSN，也不发布恢复/清理 MCP 工具。显式管理升级顺序为：安装原生提供方增量，应用目录迁移 004，更新运行角色授权，再启动使用新二进制的开通/恢复进程。应用启动不自动迁移、安装或启动后台任务。

```text
mcpguard-provision install-provider /private/one-pg-targets.json
mcpguard-catalog migrate
mcpguard-catalog grant-provision-runtime <metadata_worker_role>
mcpguard-provision recover <orgId> <grantId>
mcpguard-provision recover-once
```

recover-once 每轮最多选择 10 个到期任务；它是显式、有界的运维入口。本阶段没有为用户创建定时任务或部署常驻进程。run/run-once 继续服务正常首次开通；对已有任务重放只读取当前记录，不重新获得建号资格。

recover 输出 VERIFIED、CLEANED 或已确认失败的 FAILED 时退出 0；仍需等待、未知、待清理或需人工处理时退出非零，并返回脱敏状态。清理成功不表示授权成功。诊断不打印秘密、密码、DSN、owner nonce 或请求原生参数。

access.status 的 provisioning 增加可选恢复摘要：代次、核对/清理次数、操作、cleanupState、固定结果代码，以及租约到期或下次重试时间。它不返回私有租约持有者、原生身份或连接信息。当前唯一机器契约修订为 0.2.3，旧 0.2.2 的六份文件已[按字节归档](specs/v0.2/history/0.2.2/README.md)。

## 本地复现

```bash
python3 scripts/recovery_fixture.py prepare
python3 scripts/recovery_fixture.py test
python3 scripts/recovery_fixture.py wire
python3 scripts/recovery_fixture.py access-smoke
python3 scripts/recovery_fixture.py provision-smoke
python3 scripts/recovery_fixture.py smoke
python3 scripts/recovery_fixture.py contracts
```

脚本每次创建新场景，私有状态保留在 ~/.local/state/mcpguard-ra06，构建和当前指针位于忽略提交的 .dev/ra06-*。按顺序运行，最后的故障进程检查会改变其自有测试资源的申请资格。原 RA-04/05 脚本、状态指针和场景不覆盖。

实际进程中断使用仅存在于 Go 测试二进制的检查点，生产 CLI 没有崩溃开关、任意 SQL 或检查点路径参数。验证区分真实 SIGKILL、真实 PG 效果，以及模拟的网络不可用/回执丢失；这些证据不代替真实桌面客户端、人工体验或生产部署验收。


## 后续 RA-07 状态（2026-10-26）

RA-07 已新增服务端引用包、租约及在线校验，原开通未知结果恢复与清理重试继续回归。只有恢复后当前仍 ACTIVE 的授权可以签发；未知/清理中或终止授权不能签发。nonce 在加密前持久预留，后续失败保留预留而不生成可用包。本阶段不等于旧备份全局 fence 完成；恢复旧 nonce 账本后必须避免复用同一密钥材料。见[引用包说明](credential-packages-local.md)和[本轮验收](validation/local-ra07-2026-10-26.md)。


## 后续 RA-08 状态（2026-10-26）

下载 ticket 的消费与响应结果现单独持久记录。网络中断不触发账号重建，CLAIMED/UNKNOWN 通过新 ticket 重取同一仍有效包；下载恢复不取得原开通任务的派发资格。原 RA-06 崩溃核对和清理重试继续通过，原生未知结果与下载响应未知分别记录。详见[RA-08 说明](private-downloads-local.md)。
