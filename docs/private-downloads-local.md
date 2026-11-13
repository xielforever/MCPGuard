# RA-08：下载准备、一次 claim 与私有交付

本文记录 2026-10-26 的 RA-08 实现。此前[引用包和租约](credential-packages-local.md)、[原生账号开通](native-provisioning-local.md)及[开通恢复](provision-recovery-local.md)继续保留。分层成绩见[本轮验收](validation/local-ra08-2026-10-26.md)，原计划仅追加[当前状态](revisions/2026-10-24-resource-access/development-plan.md)。

## 1 实现范围

模型通过 `credential.prepare_download` 准备本人 ACTIVE 授权的文件，只收到 credentialId、leaseId、downloadId、ticket 期限、包期限、revision、状态和交付模式。`downloadId` 是非秘密索引，不能单独兑换文件；没有免认证下载 URL。

本阶段交付 `MANUAL_OWNER` 模式：本人当前认证加独立私有下载授权，由单次受控组件领取。组件通过独立 HTTP 通道收取密文，校验并保存，MCP 返回和组件输出均不含密文或私有令牌。没有可信客户端 Context 时，不接受模型自报的 clientInstanceId，不假称已经支持自动同步、缓存激活或设备身份。

RA-09 的缓存/同步/Context、RA-10 的原生客户端适配、RA-11 正式资源读取及后续完整撤权/在途停止继续保留原退出条件。私有令牌登记使用明确的本地测试身份及服务器运维入口，企业 IdP/生产 scope 产品集成没有在本阶段验收。

## 2 准备和重领

`PrepareDownloadInput` 仍只有 grantId、requestKey。服务端重新认证发起人并核对 ACTIVE 个人授权、原生绑定、当前资源/套餐/资格、issuer/策略版本及期限；资源审批不会被重放成新账号开通。

初次准备复用 RA-07 的加密前 nonce 持久预留和锁外加密流程，在最终事务内原子写入包、租约、签发幂等、ticket、准备幂等及关键审计。准备失败或审计失败不会留下可用包/孤立租约；已预留 nonce 不回收。

每个主体/授权使用服务端派生的稳定签发键。不同 requestKey 可以生成新 ticket，但仍指向同一包和租约，不新增授权、不延长租约、不重新创建原生账号。同一 requestKey 返回原 ticket 的当前状态；已 CLAIMED/EXPIRED/CANCELLED 的原 ticket 不会被重置。相同 requestKey 换 grant 会冲突。准备幂等存储只保留调用者 requestKey 的 HMAC、输入摘要及 ticket 引用，避免持久化原始幂等键或秘密响应快照。

票据期限由启动时显式配置，取“数据库时间 + ticket TTL”和“包租约到期”的较早值，TTL 为 1 至 300 秒。每主体同时有效的 READY ticket 数量也需显式配置，范围 1 至 50；初查与最终提交均核对上限。过期行不占有效队列配额，历史行继续保留。

已终结的包不因新 requestKey 复活。包续期/替换需要后续明确流程，RA-08 不隐式创建新的租约或原生账号。

## 3 私有认证

准备接口使用当前员工认证。私有状态和 claim 额外要求 `X-MCPGuard-Delivery-Token`：独立 256 位随机授权，audience 固定 `mcpguard-delivery`、scope 固定 `credential:claim`，绑定当前组织、主体及员工凭据指纹，有明确期限（1 至 3600 秒，且不超过员工身份期限）。只保存带域分隔的 verifier HMAC，不保存原始令牌。

私有授权只通过服务器运维登记；没有模型可调用的登记或换取令牌接口。普通网关身份不能签发包或读取 blob；独立下载运行角色可处理包/ticket，不能读取原生秘密、创建开通任务或登记新私有授权。私有授权登记另需显式的 enrollment 数据库权限。

私有路由逐次重验员工及独立授权，拒绝跨主体/组织、替换或过期员工凭据、过期/撤销私有授权、重复认证头、Cookie、Origin 和 URL 查询参数。浏览器会话不自动转换为私有下载权限。MCP session ID、UUID、设备名或持有 ticket 均不构成领取授权。

客户端只把私有授权头发给私有下载路由，不交给 MCP 连接器的头配置。HTTPS 是正常入口；本地测试需显式允许 IP loopback HTTP。组件拒绝重定向，避免私有头转发到另一地址。

## 4 一次消费与响应未知

claim 是严格空 JSON 对象的 POST。服务端在短事务内核对当前主体、独立授权、ticket 归属/READY 状态/期限、当前包租约和所有授权资格，并检查持久 blob 摘要。只有 ticket 状态改成 CLAIMED、revision 加一且关键 `CLAIM_COMMITTED` 审计原子提交后，才开始写响应。并发请求不能获得第二次交付资格。

事务外发送 `application/octet-stream`，附服务端固定 UUID 文件名、长度、SHA256、`Cache-Control: no-store`、`Pragma: no-cache`、`Referrer-Policy: no-referrer` 和 `nosniff`。不支持 Range、续传或用同一 ticket 重放部分文件。

| 阶段/结果 | 持久状态 | 恢复方式 |
|---|---|---|
| 请求未到服务端或消费事务失败 | READY，尚未过期且资格有效 | 私有查询确认后可明确重试原 ticket |
| 消费已提交，尚无写结果 | CLAIMED / UNKNOWN | 原 ticket 不重放；用新 requestKey 准备新 ticket，领取同一仍有效包 |
| 服务端 writer 接收完整响应 | CLAIMED / WRITE_COMPLETED | 仅证明服务器写入连接成功，不证明用户端保存、收到或激活 |
| 写响应失败/请求取消 | CLAIMED / WRITE_INTERRUPTED | 不回滚消费；查状态，准备新 ticket |
| 响应后结果审计失败 | CLAIMED / UNKNOWN | 不补发旧 ticket，不猜测客户端已收到 |
| 期限到达或资格失效先提交 | EXPIRED 或 CANCELLED | 不交付；新 ticket 仍必须重验原包与当前资格 |

写结果只追加固定事件码，使用独立、最长两秒的审计上下文；HTTP 请求取消不会撤回已提交的消费。网络发生中断时不能从客户端错误推断服务器未处理，组件不自动重试 claim。HTTP 对非幂等请求的重试边界见 [RFC 9110 §9.2.2](https://datatracker.ietf.org/doc/html/rfc9110#section-9.2.2)。

撤权先于 claim 提交时拒绝交付。消费已经提交后的在途停止仍归后续完整撤权阶段，不能把本轮 ticket 取消解释为已回收发送中的字节或已经保存的文件。

## 5 脱敏审计

新审计表只允许 orgId、subjectId、downloadId、credentialId、authorizationId、固定事件类型、服务器数据库身份及时间。事件为 AUTHORIZATION_ENROLLED、TICKET_READY/EXPIRED/CANCELLED、CLAIM_COMMITTED、DELIVERY_WRITE_COMPLETED/INTERRUPTED，没有任意 payload、错误文本、请求/响应体、完整 URL、Cookie、令牌或密文列。

私有路由拒绝带 Origin/Cookie/query 的请求时，不进入记录这些原始字段的旧入口诊断。组件和服务器 CLI 只输出安全标识、状态或固定错误；私有文件、令牌、数据包及真实网络响应不写标准输出。准备和消费的关键审计失败会阻断提交；响应后审计失败保留 UNKNOWN。依据见 [OWASP Logging：Data to exclude](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html#data-to-exclude)。

“模型不可见”的本轮证明范围是 MCP HTTP/stdio 返回、私有组件标准输出、应用日志和持久审计不包含文件或令牌。0700/0600 保护不同系统用户，不能单独隔离拥有同一系统身份且可任意执行命令/读取文件的模型进程；更强隔离需独立系统身份或受保护的凭据代理，不能从本轮协议脱敏推断已经完成。

## 6 运维与单次组件

新增资源迁移 `006_private_downloads.sql`，原 001 至 005、原生 001/002 和元数据 1 至 4 不改写。新表为 delivery_authorizations、download_tickets、download_commands、download_audit。真实 v5 包、blob、租约、nonce、计数、授权及恢复历史升级后保持；升级不会自动建立 ticket 或私有授权。

服务器管理入口新增：

```text
mcpguard-catalog grant-delivery-runtime <dedicated-delivery-role>
mcpguard-catalog grant-delivery-enrollment <trusted-server-enrollment-role>
mcpguard-credentials enroll-delivery <ttlSeconds> <new-server-private-authorization-file>
```

`enroll-delivery` 要求当前员工认证、独立服务器元数据身份及现有服务器密钥配置；文件通过受控私有渠道交给本人组件，不能贴入模型或代码库。服务器命令输出不含私有授权内容。不要把服务器运维二进制作为 coding MCP 连接器。

可选网关配置，均需显式提供：

```text
MCPGUARD_RESOURCE_CATALOG=enabled
MCPGUARD_RESOURCE_ACCESS=enabled
MCPGUARD_RESOURCE_CREDENTIALS=enabled
MCPGUARD_CREDENTIAL_KEYS=<server-private-key-file>
MCPGUARD_RESOURCE_DOWNLOADS=enabled
MCPGUARD_DELIVERY_DSN=<dedicated-server-delivery-runtime>
MCPGUARD_DOWNLOAD_TTL_SECONDS=<1..300>
MCPGUARD_DOWNLOAD_MAX_READY=<1..50>
```

| 运行入口 | 认证/返回 |
|---|---|
| MCP credential.prepare_download | 员工本人；安全准备结果，无文件字节 |
| POST /api/v2/credential-packages | 同上 |
| GET /api/v2/credential-downloads/{id} | 当前员工 + 私有下载授权；本人 ticket 状态 |
| POST /api/v2/credential-downloads/{id}/claim | 同上；一次二进制响应 |

单次组件 `mcpguard-download` 不需要服务器密钥、元数据 DSN 或原生密码，当前交付 Linux/WSL 文件权限实现：

```text
mcpguard-download status -gateway <origin> -download-id <id> -auth-file <private-file>
mcpguard-download claim -gateway <origin> -download-id <id> -auth-file <private-file> -directory <existing-private-directory>
```

当前员工 token 从 `MCPGUARD_TOKEN` 私有环境读取；本地测试另加 `-allow-loopback-http`。组件拒绝不安全的认证文件、符号链接及 Git 工作区内的目标目录，目录须为 0700；领取前已有同名文件时不消费 ticket、不覆盖。完整接收后核对固定格式/规范编码、文件名、长度和 SHA256，先写 0600 临时文件并同步，再用不可覆盖的原子链接发布。中断或校验失败不发布半文件。标准输出只有安全保存回执。

Windows 原生 ACL、本地缓存索引、自动任务同步、Context、激活和原生客户端配置仍按 RA-09/10 验收；当前 Windows 原生执行会拒绝该文件操作，不以 chmod 声称具备 Windows 隔离。

## 7 隔离复现及失败保留

WSL/Docker 中按顺序运行；每次 prepare 创建新数据库、两套业务 PG、Redis、独立下载角色和新密钥。不会更新已有本机服务或旧夹具。

```text
python3 scripts/download_fixture.py prepare
python3 scripts/download_fixture.py test
python3 scripts/download_fixture.py wire
python3 scripts/download_fixture.py access-smoke
python3 scripts/download_fixture.py provision-smoke
python3 scripts/download_fixture.py credential-smoke
python3 scripts/download_fixture.py smoke
python3 scripts/download_fixture.py recovery-smoke
python3 scripts/download_fixture.py contracts
```

新私有交付检查在 recovery-smoke 前完成，因为原 RA-06 最后的撤权反例会关闭基础夹具的一项申请资格；不恢复旧反例来凑通过。服务端重启后，测试会重新认证独立人工审批会话；持久下载授权、ticket 和消费状态则继续从数据库核对。

私有状态位于 `~/.local/state/mcpguard-ra08/`，保留失败日志、授权文件、密文文件、网络中断证据及数据库；仓库 `.dev/ra08-build` 与选择指针为忽略项。结束只停止本轮自建容器，保留容器及卷。原始成绩与最终新场景的通过分别记录，见[验收](validation/local-ra08-2026-10-26.md)。


## 后续 RA-09 状态（2026-10-26）

前文保留 RA-08 手动模式时点。后续新增 ENROLLED_CLIENT 交付：任务由当前 Context 绑定受信登记客户端，手动接口不能接管绑定任务；无 Context 头的旧手动响应和一次领取语义保留。新私有组件补齐安全缓存、有界待办、未知结果替代任务、多资源激活和续期，并分别验证 Windows 缓存安全。见[RA-09 说明](credential-contexts-local.md)及[验收](validation/local-ra09-2026-10-26.md)。


## 后续 RA-10 状态（2026-10-26）

HTTP worker 与 stdio 私有适配现分别消费受信客户端任务，下载响应只进入私有缓存。stdio 在已完成 prepare 后遇到私有响应丢失时保留原结果及安全 PENDING_PRIVATE_SYNC 提示，后续仅恢复私有任务。原手动交付继续回归，见[RA-10](private-client-adapters-local.md)。
