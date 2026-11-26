# RA-05 受限原生账号开通与核验

日期：2026-10-26。实现范围为受限 PostgreSQL ProvisioningProvider、新建个人账号、明确登记的既有账号核验，以及核验后的 AccessGrant 激活。本地验证结果见[本轮验收](validation/local-ra05-2026-10-26.md)。原[申请审批说明](resource-access-workflow-local.md)、[RA-01/02/03](resource-access-local.md)和全部历史记录保留。

## 入口与身份

开通使用独立的 `mcpguard-provision` 进程。网关继续处理目录、申请、人工审批和状态查询，不加载原生账号管理凭据，也不发布开通或批准 MCP 工具。当前正式新资源业务读工具仍待 RA-11；本轮原生读取通过内部集成测试探针验证。

| 身份 | 能力 |
|---|---|
| 元数据迁移管理员 | 显式迁移、登记资源/开通配置、授予运行角色必要权限 |
| 原生 PG 安装管理员 | 显式安装固定管理函数及目标配置，不作为普通开通运行身份 |
| 元数据开通运行角色 | 读取批准快照、写开通意图/核验/秘密密文/激活版本及审计；不能写旧业务 Action 表，不能改配置或历史 |
| 原生 PG 管理运行角色 | 仅调用固定管理函数；无 SUPERUSER、CREATEROLE、CREATEDB、BYPASSRLS、成员角色或任意 DDL/GRANT 权限 |
| 个人原生查询账号 | 真实 LOGIN、固定表 SELECT 和 owner_name/current_user RLS；无账号管理、角色切换或表/列写权限 |

安装函数使用固定 `search_path=pg_catalog,pg_temp` 的 SECURITY DEFINER，仅管理员安装事务内撤销 PUBLIC 执行权，再向管理角色明确授予指定函数执行权。函数固定模板、对象和 `mg_` 加账号 UUID 的命名；申请人不能提供 SQL、密码、原生账号名或管理 DSN。此处理遵循 [PostgreSQL 16 的函数安全说明](https://www.postgresql.org/docs/16/sql-createfunction.html)。

## 配置和版本

唯一机器契约源仍是 `internal/contracts/v02/source.json`，当前修订 0.2.2。新增 ProvisioningTarget、ExistingAccountRegistration、ProvisioningConfiguration、ProvisioningPlan、NativeObservation；NativeAccount 必须关联资源版本和实际核验证据，账号型 CredentialBinding 关联 accountId。原 0.2.1 按字节归档于[历史快照](specs/v0.2/history/0.2.1/README.md)。

ProvisioningTarget 精确绑定 org/resource/version/profile/version、endpointConfigRef、目标指纹、targetId、模板及配置版本。当前只支持 PostgreSQL、`PG_OWNER_RLS_V1` 权限套餐及 `PG_OWNER_SELECT_V1` 开通模板，固定 `mcpguard_business.resource_demo.items`。安装不会建立业务表、放宽 RLS 或接管同名角色。

同一配置重放不产生新记录；改变内容必须增加版本，所有生效版本追加到不可改写的配置历史。原生端 targetId 的定义不可原地替换；配置升级需先以新的 targetId 安装相应目标，再导入增加版本的元数据配置。申请冻结开通配置及既有账号登记版本，配置改变后旧批准不能被用于新计划。

已有账号必须由管理员逐主体、资源、套餐、版本登记，明确 accountId、nativeIdentity、secretRef/secretVersion 和登记版本；私有凭据只交给独立开通进程。不存在登记时才采用新建模式；禁用登记不会退回自动建号。同一物理目标与原生账号不能被登记给其他主体、组织或套餐使用。不存在基于用户名猜测、自动接管或修改既有账号权限的路径。

## 开通和激活

1. `access.request` 与受保护人工决定沿用 RA-04。批准原子地产生 AccessGrant v1 / PROVISIONING，账号和绑定仍为空。
2. 独立进程显式执行 `run <orgId> <grantId>`，或一次性 `run-once`（最多 10 项）。在组织授权锁内重查批准、当前主体/目录/套餐资格、配置版本、绑定版本和到期时间。
3. 冻结不可变计划及 HMAC，持久化唯一开通意图 EXECUTING、加密原生秘密及意图审计，再调用原生 PG。每授权最多一个开通任务；已有任务重放只返回原任务状态。
4. PROVISIONED 使用服务端随机密码及系统生成个人角色，通过固定管理函数建号。PREEXISTING 使用明确登记的个人凭据，只做核验，不修改密码、权限或 LOGIN 状态。
5. 使用真实个人凭据连接目标 PG，核对 session_user/current_user、数据库、角色 OID、有效期、角色成员/管理权限、表/列权限、其他业务对象权限、可执行 SECURITY DEFINER 函数及精确 RLS；实际执行只读固定表探针。
6. 再次取得授权锁，重查资格、期限、目标/登记版本及当前绑定版本。核验、NativeAccount、CredentialBinding、任务 VERIFIED、授权 v2 / ACTIVE 及关键审计在同一元数据事务中提交。批准 v1 原样保留，账户/绑定 ID 来自真实开通事实。
7. `access.status` 返回 request/decision/grant 及可选的脱敏 provisioning 任务摘要；HTTP、stdio 和重启后均读取相同持久事实。授权列表只返回 head 指向的当前版本。

密码在元数据中使用现有 Vault 的 AEAD 封装，绑定组织、账号及秘密版本。计划和审计只引用秘密版本，不包含密码。VaultSecretProvider 在服务端读取当前有效主体、ACTIVE grant 和绑定后解密；本阶段没有客户端下载、密文包或 Context。

本轮内部读探针使用真实新账号，并在复用连接时复核扩展权限及已观察的角色 OID，拒绝额外列写权限或账号被删除重建。正式入口仍不允许通过旧无包接口读取新资源。

## 失败和后续阶段

| 情形 | 持久状态与行为 |
|---|---|
| 原生创建返回值丢失或效果不确定 | 任务 OUTCOME_UNKNOWN、授权 PROVISIONING_UNKNOWN；不激活、不盲目重建 |
| 明确登记的既有账号无法核验 | 任务 FAILED、授权 PROVISIONING_FAILED；不改动该原生账号 |
| 新建成功后核验或激活失败 | 任务 CLEANUP_PENDING；最佳努力对本任务拥有且 OID/计划匹配的账号执行 NOLOGIN；无 ACTIVE 或部分激活提交 |
| 开通期间资格或目标版本变化 | 迟到结果不能激活，授权进入终态或在查询时收敛；保留原批准和开通事实 |
| 关键激活审计写入失败 | 激活事务整体回滚，进入失败/补偿流程 |
| 进程在意图后崩溃 | 已有 EXECUTING 保留，不自动派发第二次；RA-06 负责核对收敛 |

RA-05 提供一次开通的持久资格和失败时保守处理，不能据此宣布 RA-06 完成。跨进程租约、崩溃恢复、已创建但回执未知的核对、清理重试与最终完成状态仍属下一项。NOLOGIN 不证明已停止所有在途会话或撤销了原生残留权限；完整吊销/池淘汰/在途停止仍属 RA-13/14。

当前受限提供方只允许 loopback、明确的 `mcpguard_business` 数据库和隔离测试中的明文传输配置；不宣称生产 TLS、外部秘密管理/KMS、企业 IdP 或真实客户端验收完成。独立开通进程具备账号管理能力，部署时应以独立服务身份和私有配置注入管理凭据。

## 显式管理与本地复现

管理命令均需管理员明确运行，应用启动不执行迁移或安装。以下路径为配置位置示意，须使用自己的已审核文件和独立环境。

```text
mcpguard-catalog migrate
mcpguard-catalog import-provisioning /private/provisioning.json
mcpguard-catalog grant-provision-runtime <metadata_worker_role>
mcpguard-provision install-provider /private/one-pg-targets.json
mcpguard-provision run <orgId> <grantId>
mcpguard-provision run-once
```

`mcpguard-catalog` 使用 MCPGUARD_MIGRATION_DSN；原生安装使用 MCPGUARD_PROVIDER_ADMIN_DSN 和 MCPGUARD_PROVIDER_MANAGER_ROLE。普通开通进程只读取自己的 MCPGUARD_METADATA_DSN、MCPGUARD_ENCRYPTION_KEY、MCPGUARD_PLAN_KEY 和 MCPGUARD_PROVIDER_CONFIG，不需要普通 MCP Token 或全局 BusinessDSN。

私有 provider 文件包含 endpoints、managers（按 endpointConfigRef）、existingSecrets（按 ref@version）；要求普通文件、0600 权限、无重复/未知 JSON 字段，地址/端口/数据库必须与目录指纹一致。不得在日志、MCP 输出或公开证据中保存它。

隔离夹具在 WSL 中使用已缓存 Go 镜像及 PostgreSQL 16：

```bash
python3 scripts/provision_fixture.py prepare
python3 scripts/provision_fixture.py test
python3 scripts/provision_fixture.py wire
python3 scripts/provision_fixture.py access-smoke
python3 scripts/provision_fixture.py smoke
python3 scripts/provision_fixture.py contracts
```

prepare 每次创建新场景，不覆盖旧场景，私有状态保留在 `~/.local/state/mcpguard-ra05/`，指针和构建位于忽略提交的 `.dev/ra05-*`。test 先于 smoke 执行，避免实际激活改变夹具的初始绑定。此脚本不会加载或重启现有本地执契服务。


## 后续 RA-06 状态（2026-10-26）

前文保留 RA-05 完成时点。后续已新增目录迁移 004、原生提供方迁移 002、恢复租约/代次、永久封存、未知结果核对和有界清理重试。旧计划、首次结果审计和批准 v1 保留；只有同一原生结果核验且当前授权仍有效，才可从 UNKNOWN 追加 ACTIVE；终止授权不复活。详见[RA-06 说明](provision-recovery-local.md)与[验收](validation/local-ra06-2026-10-26.md)。本阶段 CLEANED 不表示已停止所有在途会话。


## 后续 RA-07 状态（2026-10-26）

服务端可基于已核验的 ACTIVE 授权签发 AEAD 引用包；包中没有原生账号密码、DSN 或 secretRef。签发失败和下载阶段的后续重试都不得重新取得建号资格。原生秘密仍由既有服务端提供方解析，账号创建/核验/恢复/清理逻辑保持。详见[RA-07 说明](credential-packages-local.md)。


## 后续 RA-08 状态（2026-10-26）

下载准备只消费已核验的个人授权，不调用原生账号提供方。相同包的新 ticket、消费后响应丢失、进程重启和并发领取前后，两套 PG 原生账号以及元数据账号/任务/授权/包/租约数量一致。原账号创建/核验/恢复行为继续回归；见[私有交付](private-downloads-local.md)及[验收](validation/local-ra08-2026-10-26.md)。
