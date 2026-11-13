# v0.2 版本化契约与 RA-02/03 范围

日期：2026-10-24。规范源为 [source.json](../../../internal/contracts/v02/source.json)，Go DTO、公开 Schema、OpenAPI、MCP 清单和样例均由 [生成器](../../../scripts/generate_v02_contracts.py)生成。禁止手工维护副本。

## 契约状态

| 产物 | 作用 |
|---|---|
| contract.schema.json | Draft 2020-12 的领域、输入、输出、状态和界限 |
| openapi.json | /api/v2 路由、输入输出与逐接口实现阶段；PLANNED 路由不代表运行可用 |
| mcp-tools.json | 公开名称及输入输出契约，包含未来阶段，不用于自动注册所有工具 |
| examples.json | 正反样例；身份自报、额外 SQL、缺凭据、非法状态及大整数精度反例 |
| migration-draft.sql | 未来申请/账号/授权/密文生命周期的 SQL 草案，仅离线语法验证 |
| validate.py | 新契约、样例、OpenAPI 和 SQL 的离线检查，不访问数据库 |

实际 RA-02 迁移为 [独立 catalog 迁移](../../../internal/resources/migrations/001_catalog.sql)，与原 mcpguard 的 1..4 迁移分开并显式执行。目录和原生绑定使用 PostgreSQL 权威状态，私有秘密仅由服务端提供方解析。

RA-03 是先于账号申请及密文阶段的读取基础。预先配置的个人原生账号只在隔离集成测试探针 fixture.pg_read_item 中使用；探针定义仅存在于 *_test.go。生产入口仅可选发布 resource.list/get，不注册正式 pg.get_item、账号/凭据工具或无包读取接口。RA-11 接通授权与密文链后才能发布正式业务读取。

## 生成和校验

在具有仓库所需 Go 与 Python 的隔离环境中：

```text
python3 scripts/generate_v02_contracts.py
python3 scripts/generate_v02_contracts.py --check
go test ./internal/contracts/v02
python3 docs/specs/validate.py
python3 docs/specs/v0.2/validate.py
```

离线 Python 依赖沿用原 docs/specs/requirements.txt，在独立依赖目录安装，不改变应用依赖。schema 中的结构与格式校验不证明当前授权、期限顺序、禁止自批、状态迁移或数据库权限；这些由相应阶段领域事务和真实集成测试证明。

字段版本保持十进制字符串；期限使用 UTC ISO 8601 文本。生产期限由资源策略配置，不由样例日期定义。包格式候选已固定为版本 1 的服务端 AEAD 引用封套，尚未实现实际签发或加解密。

客户端上下文令牌仅出现在标记为敏感的私有接口契约，不出现在 MCP 工具输出。旧 v0.1 Schema、OpenAPI、固定工具和历史证据原文保留。

## 已固定的界限与当前验证

单次 JSON 上限 65536 字节、嵌套深度 16；目录每页 1–100 项；版本用最多 18 位的正十进制字符串；时间使用有效 UTC 日历日期，最多纳秒精度。包格式候选上限为 32768 字节、ciphertext base64url 文本上限 16384 字符，格式校验不代表已实现 AEAD。

RA-03 本地执行上限为 3 秒、16 个池、每池最多 2 个连接；套餐还限定超时、行数及结果字节数。授权终止时间、租约/下载 ticket/Context 的期限字段已经版本化；具体 TTL、留存和资源额度由后续服务策略配置，签发时必须满足授权和策略上限。本切片不提供这些未来服务的隐式生产默认值。

Go 运行校验与离线检查共用 29 个正反样例，并额外实施重复键、深度和大小检查；运行验证显式补足 jsonschema-go 将 `format` 作为注释处理的 UTC 日期校验。运行工具只携带其传递引用的定义，保持根 schema 为 object 以满足 MCP SDK，完整规范仍保留全部 58 个定义。

当前新旧校验及真实 PG/HTTP/stdio 的本地结果见[验收记录](../../validation/local-ra01-03-2026-10-24.md)，复现入口见[本地说明](../../resource-access-local.md)。



## RA-04 后续契约修订 0.2.1

前文的 58 定义/29 样例保留 RA-01 时点。当前为 66 个定义、38 个样例、23 个 HTTP 操作和 11 个 MCP 契约，仍由唯一源生成；原 0.2.0 候选保留于[历史快照](history/0.2.0/README.md)。后续生命周期迁移草案原字节不变，实际目录迁移新增 002。

当前可选注册 access.request/status/cancel/grants，不注册批准或未来凭据工具。PROVISIONING 不要求尚未产生的账号/绑定，ACTIVE 仍要求二者，但当前不可达；POLICY 等未来状态出现于契约不表示已实现。当前入口见[实现说明](../../resource-access-workflow-local.md)，结果见[RA-04 验收](../../validation/local-ra04-2026-10-24.md)。


## RA-05 后续契约修订 0.2.2（2026-10-26）

新增受限开通目标/既有账号登记/计划/原生权限观察契约，NativeAccount 关联真实观察，账号型绑定关联 accountId，access.status 可返回脱敏开通摘要。批准 v1 保留；实际原生核验与资格复查成功后追加 ACTIVE v2。当前 71 个定义、42 个样例、23 个 HTTP 契约、11 个 MCP 契约通过检查；5 份 SQL（草案、目录 001/002/003 和固定原生提供方）通过离线解析，真实权限及事务行为另由集成测试证明。

原 0.2.1 六份源/生成文件见[历史快照](history/0.2.1/README.md)，旧 0.2.0 继续保留。新正式业务读工具、客户端加密文件及 Context 不因本修订自动上线。实现入口见[RA-05 说明](../../native-provisioning-local.md)，结果见[验收](../../validation/local-ra05-2026-10-26.md)。


## RA-06 后续契约修订 0.2.3（2026-10-26）

新增 ProvisioningRecovery 及任务 CLEANED/MANUAL_REQUIRED，显式区分派发、核对、清理和仅结束元数据的 FINALIZE；界限为 30 秒租约、核对/清理各最多 6 次、5 至 60 秒退避及每轮最多 10 项。公开状态不含 owner nonce、原生秘密或连接。72 定义、46 样例、23 HTTP 契约、11 MCP 契约与 7 份 SQL 通过；未来契约仍不等于运行工具注册。

旧 0.2.2 源/DTO/生成物见[历史快照](history/0.2.2/README.md)，0.2.0/0.2.1 继续保留。实际恢复权限、崩溃和原生效果以[本轮验收](../../validation/local-ra06-2026-10-26.md)为准，使用方式见[恢复说明](../../provision-recovery-local.md)。


## RA-07 后续契约修订 0.2.4（2026-10-26）

新增显式 issuer/租约策略配置与服务器内部签发输入，CredentialStatus 增加 revision；固定规范 JSON、AES-256-GCM、AAD、12 字节 nonce、16 字节 tag、8192 字节引用与每密钥最多 2^32 次预留。76 定义、52 样例、23 HTTP 契约、11 MCP 契约及 8 份 SQL 校验通过；本阶段实际只可选新增 credential.status 与本人 GET 状态，未公开签发、解密、下载、Context 或正式读取。

0.2.3 六份原字节文件及 SHA256 见[历史快照](history/0.2.3/README.md)，0.2.0 至 0.2.2 继续保留。配置与复现见[引用包说明](../../credential-packages-local.md)，完整证据见[本轮验收](../../validation/local-ra07-2026-10-26.md)。


## RA-08 后续契约修订 0.2.5（2026-10-26）

79 个定义、58 个样例、24 个 HTTP 契约、11 个 MCP 契约与 9 份 SQL 通过。DownloadTicket 与安全准备结果增加双期限、revision、手动交付模式及明确的 writer 结果，严格空 claim 输入拒绝 owner/设备伪造；私有路由的 employee + delivery 双认证在 OpenAPI 中显式声明。

只新增可选 credential.prepare_download、POST 准备及私有 ticket 状态/claim，其他队列/Context/激活/正式读取仍未注册。0.2.4 源/DTO/生成物六份原字节及摘要见[历史快照](history/0.2.4/README.md)，0.2.0 至 0.2.3 继续保留。见[实现说明](../../private-downloads-local.md)和[分层验收](../../validation/local-ra08-2026-10-26.md)。


## 2026-10-26 后续修订：0.2.6 / RA-09

本规范现为 0.2.6，单一源生成 87 定义、66 样例、28 HTTP 契约和 11 MCP 契约。Context 绑定服务端登记客户端及当前员工凭证，私有状态/队列/激活/续期按 RA-09 落地；原生 HTTP/stdio 私有适配及正式新资源读取继续按 RA-10/11 推进。旧 0.2.5 六文件原字节[快照](history/0.2.5/README.md)与前文历史不改写。见[实现](../../credential-contexts-local.md)与[验收](../../validation/local-ra09-2026-10-26.md)。


## 2026-10-26 后续修订：0.2.7 / RA-10

当前为 89 定义、70 正反样例、28 HTTP 契约与 11 MCP 契约。增加安全客户端能力和私有同步结果提示，没有新增公开私有头/claim/activate 工具。stdio 私有模式通过 experimental 声明实现范围，URL-only 明确无自动私有生命周期能力。0.2.6 六文件[快照](history/0.2.6/README.md)和前文各时点保留；见[RA-10 说明](../../private-client-adapters-local.md)。
