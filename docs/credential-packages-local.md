# RA-07：服务端引用包、租约和在线校验

本文对应 2026-10-26 的 RA-07 实现。分层验证及源快照见[本轮验收](validation/local-ra07-2026-10-26.md)；既有[申请审批](resource-access-workflow-local.md)、[账号开通](native-provisioning-local.md)及[崩溃恢复](provision-recovery-local.md)继续保留。

## 1 交付边界

本阶段实现服务端签发、密文存储、在线完整性/资格校验、单文件租约关闭，以及可选的本人状态查询。原生密码仍由既有 `SecretProvider` / `VaultSecretProvider` 在服务端读取。引用包只包含绑定引用，没有数据库连接密码、DSN、secretRef、nativeIdentity、刷新令牌、密钥或脚本。

`CredentialService.Validate` 返回本次检查时刻的脱敏状态，不返回引用明文，也不产生可供后续请求复用的执行票据。未来 RA-11 的实际派发必须重新核验当前主体、授权、包、租约及 Context。已有 `ReadFixture` 只在测试中用于验证服务端原生读取基础，未发布正式 `pg.get_item`。

RA-08 的下载准备/一次 claim/私有交付、RA-09 的本地缓存/Context、RA-10 的原生客户端适配和 RA-13/14 的完整吊销/在途停止均未在本阶段开放。服务端 CLI 的私有测试文件不是客户端下载凭据的证明。

## 2 密文格式与密钥

格式版本固定为 `1`，算法固定为 AES-256-GCM，nonce 为 12 字节，认证标签为 16 字节。外层字段按生成 DTO 的顺序输出紧凑 JSON：`formatVersion`、`issuer`、`keyId`、`nonce`、`ciphertext`、`authenticationTag`。二进制字段使用无填充、严格规范的 base64url；拒绝重复/额外字段、尾随内容、非规范编码及未知版本。包最多 32768 字节，引用明文最多 8192 字节。

AAD 是以下紧凑 JSON 数组的 UTF-8 字节，其中 issuer/keyId 替换为服务端已登记值：

```json
["mcpguard-reference","1","issuer","keyId","mcpguard-execution"]
```

引用绑定 orgId、subjectId、resourceId/resourceVersion、grantId/grantVersion、bindingId/bindingVersion、profileId/profileVersion、credentialId、leaseId、issuer/issuerEpoch、audience、签发时间和到期时间。服务端验证完整引用与持久化记录一致，并校验整个规范包的 SHA256。

`PackageKeyProvider` 返回服务端不透明加解密句柄，和解析原生连接秘密的 `SecretProvider` 分离。本地实现从权限受限的普通文件读取 32 字节独立密钥，拒绝符号链接、组/其他用户可读写权限、重复配置、重复密钥材料、外部 key URL/算法字段，以及与当前元数据加密密钥或计划密钥相同的值。生产 KMS/HSM 尚未实现；本地文件权限证明不等同于生产密钥托管验收。

`keyId` 在同一 org/issuer 下永久绑定相同密钥指纹。按真实指纹累计的使用次数和 nonce 预留跨 keyId 别名、issuerEpoch 保留，每个密钥最多预留 2^32 次；换别名不能清零。AES-GCM 要求同一密钥下 nonce 唯一，见 [Go AEAD 文档](https://pkg.go.dev/crypto/cipher#AEAD)。独立密钥和认证加密的选择参考 [OWASP Cryptographic Storage](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html)。

nonce 在调用 Seal 前先以 `(key fingerprint, nonce)` 唯一键持久预留，同时提交计数和审计。后续密钥不可用、进程中断或最终提交失败只会烧掉预留，不会复用 nonce，也不会留下可用包。nonce 碰撞最多重新生成 3 次，仍碰撞则停止；加密尚未开始。预留不可删除或重置，引用包和 blob 不可改写。

## 3 显式配置及租约

通过单一契约源 `internal/contracts/v02/source.json` 的 `CredentialConfiguration` 导入公开配置。`CredentialIssuer` 指定 org、issuer、issuerEpoch、keyId、密钥指纹、递增版本和 enabled；配置不包含密钥材料。`CredentialLeasePolicy` 按 org/资源版本/套餐版本设置 enabled、递增版本和 `maxTtlSeconds`（1 至 86400）。没有默认租期或默认启用，缺少配置则不能签发。

租约到期时间是“签发时刻 + 策略上限”和“授权到期时间”的较早值，采用数据库时间。签发前及加密后提交前都复查当前主体 token、ACTIVE 授权、当前资源和套餐、已核验的个人账号绑定、申请资格、issuer/epoch/密钥身份、配置版本和期限。

包和租约在最终事务内原子写入；幂等记录和关键审计同事务提交。相同主体和 requestKey 重试返回原包状态，不创建第二份包；同 key 更换 grant 会冲突。并发签发可能多烧掉几次预留，最终只产生一个包。

租约为唯一可变权威状态：`ACTIVE → REVOKED` 或 `ACTIVE → EXPIRED`，revision 加一，禁止复活、延长期限或更换绑定。包状态由租约派生，不维护第二份可漂移状态。本轮不提供续期；续期需要后续阶段明确的新资格/新包流程。

## 4 在线检查与撤销

校验首先在短事务内重新认证当前主体、读取其本人包/租约并检查资格；释放元数据锁后执行加解密；最后再次进入事务核验同一主体、记录、资格、版本、状态和期限，并提交关键校验审计。解密期间撤权不能通过最终检查。包的 issuer/keyId 只用于匹配服务端登记的密钥句柄，不允许从输入加载 URL、算法或任意密钥。

当前授权、资源、绑定、申请资格、issuer/epoch 或策略版本失效时，状态查询/校验将仍为 ACTIVE 的租约收敛到 REVOKED；时间到期先收敛为 EXPIRED。主体被停用、token 被替换或登录凭据过期时，先拒绝本次调用，不借该失效身份读取状态。重新启用资源或策略不会恢复已经关闭的包。

服务端 `Revoke` 仅关闭当前主体持有的一个 credentialId，要求 revision 和 requestKey；关闭原因用元数据密钥加密，关闭事件和幂等记录原子提交。相同授权下其他包不受单文件撤销影响。旧密文可以继续保留在磁盘，但后续在线校验失败。关闭租约不依赖包解密或包密钥可用；关闭单文件不会停用原生账号。

本阶段当前 issuer/policy 版本变化会严格使旧包失效；多版本密钥的宽限、轮换和迁移策略留给后续 RA-15。不能只更新 issuerEpoch 就宣布旧备份恢复安全：若 nonce 账本回退后仍复用同一密钥，会破坏 GCM 的唯一性要求。恢复旧备份前应停止签发并使用全新密钥材料，完整外部 recovery fence 仍在后续阶段验收范围内。

## 5 服务端入口与数据库角色

增量资源迁移为 `005_credential_packages.sql`，001 至 004 以及原生提供方 001/002、元数据 1 至 4 不改写。新增 issuer/policy 及其追加历史、密钥登记/计数、nonce 预留、包/租约/blob、命令幂等和关键审计。v4 升级不制造凭据包，不改写旧授权、计划、账号、恢复租约和尝试审计。

独立服务器运维二进制 `mcpguard-credentials` 使用独立元数据角色、当前发起人 token、元数据密钥和服务端包密钥，提供以下操作；输出只有安全状态，失败诊断不包含包、引用明文或连接秘密：

```text
mcpguard-credentials issue <grantId> <requestKey>
mcpguard-credentials status <credentialId>
mcpguard-credentials check <resourceId> <credentialId> <server-private-file>
mcpguard-credentials revoke <credentialId> <expectedRevision> <requestKey> <reason>
```

这不是 coding 客户端连接器，不能把上述服务端环境或密钥复制到客户端。配置环境为 `MCPGUARD_METADATA_DSN`、`MCPGUARD_ENCRYPTION_KEY`、`MCPGUARD_PLAN_KEY`、`MCPGUARD_CREDENTIAL_KEYS`、`MCPGUARD_TOKEN`。不要把 token 或密钥直接写进终端命令、公开日志或仓库。

管理命令新增 `import-credentials`、`grant-credential-runtime`、`grant-credential-status-runtime`。独立凭据运行角色没有原生秘密读取、账号开通或公开配置修改权限。状态角色只能读取必要元数据、关闭本人失效租约及写关键审计，不能读取 blob、预留、计数和幂等存储。重跑旧的 grant-runtime/access/provision 命令也不会开放这些新增加密存储表。数据库管理账号仍在信任边界内。

网关默认关闭本阶段入口。只有同时启用 `MCPGUARD_RESOURCE_CATALOG=enabled`、`MCPGUARD_RESOURCE_ACCESS=enabled`、`MCPGUARD_RESOURCE_CREDENTIALS=enabled` 并配置 `MCPGUARD_CREDENTIAL_KEYS` 时才注册：

| 入口 | 返回 |
|---|---|
| MCP `credential.status({credentialId})` | 本人的 credentialId、leaseId、expiresAt、state、revision |
| GET `/api/v2/credentials/{id}` | 同一脱敏状态；要求当前认证，`Cache-Control: no-store` |

未注册签发、解密、下载、blob、激活、公开撤销或正式业务读取入口。规范中保留的后续契约不表示运行时已开放。

## 6 隔离本地复现

WSL/Docker 中运行以下顺序；prepare 始终新建元数据 PG、两套业务 PG、Redis 和独立密钥，旧场景及现有服务不会被重建。每次运行保留各自日志，失败场景不得改写成最终通过。

```text
python3 scripts/credential_fixture.py prepare
python3 scripts/credential_fixture.py test
python3 scripts/credential_fixture.py wire
python3 scripts/credential_fixture.py access-smoke
python3 scripts/credential_fixture.py provision-smoke
python3 scripts/credential_fixture.py smoke
python3 scripts/credential_fixture.py recovery-smoke
python3 scripts/credential_fixture.py contracts
```

新 credential smoke 在 recovery-smoke 前运行，因为既有 RA-06 最后一个撤权反例会关闭基础夹具的一项申请资格；不通过恢复该授权掩盖旧反例。私有状态位于 WSL 的 `~/.local/state/mcpguard-ra07/`，权限目录 0700、文件 0600；仓库 `.dev/ra07-build` 与指针为忽略项。结束时停止本轮自建容器并保留容器、数据库、私有文件和日志，不能将这个清理动作解释为真实资源撤权验收。


## 后续 RA-08 状态（2026-10-26）

前文保留 RA-07 完成时点。后续已实现下载准备和 MANUAL_OWNER 一次私有领取：包/租约/ticket/关键审计最终原子提交，nonce 预留保持先行持久化；不同请求键的新 ticket 复用同一包和租约，终结包不因重领复活。可选公开工具增加 credential.prepare_download，私有 claim 不进入 MCP，原生秘密仍留在服务器。详见[RA-08 说明](private-downloads-local.md)与[验收](validation/local-ra08-2026-10-26.md)。


## 后续 RA-09 状态（2026-10-26）

包仍只在服务端解密和校验。Context 激活须上传实际包并在最终事务复查当前状态；Context 续期保持身份及绝对期限，凭据续期追加后继包和租约，不修改旧期限、不创建账号。自然过期且当前授权仍符合时可续期，REVOKED 包始终拒绝。见[实现及边界](credential-contexts-local.md)。


## 后续 RA-10 状态（2026-10-26）

引用包由两种私有入口下载并提交服务端激活，仍不在终端解密或直连 PG。两入口的缺失文件、Context 到期、主动关闭、包撤销和跨用户错配均有本地证明；HTTP 私有头回调与模型协议是不同通道。见[实现](private-client-adapters-local.md)与[验收](validation/local-ra10-2026-10-26.md)。
