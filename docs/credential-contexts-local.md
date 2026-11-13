# RA-09 本地缓存、CredentialContext 与多资源激活

日期：2026-10-26。本文记录 RA-09 的本地实现。原 [RA-08 私有交付](private-downloads-local.md)、[RA-07 引用包](credential-packages-local.md)、设计和历史验收保留。验证结果单独追加到 [RA-09 验收](validation/local-ra09-2026-10-26.md)。

## 1 身份和失效边界

一个 Context 固定绑定 `orgId + ownerId + clientInstanceId + 当前员工凭证摘要`，另外绑定该客户端的私有交付授权。客户端 ID 由受信服务端登记生成，自报设备名、`clientLabel`、MCP session 或同名缓存目录都不能创建这个身份。Context 令牌为随机 256 bit，服务端仅保存带域隔离的 HMAC 校验值；缓存保存私有令牌，HTTP 请求通过 `X-MCPGuard-Credential-Context: <contextId>.<token>` 携带，同时提供当前员工认证。这个头不能出现在模型可见的工具参数、结果或资源内容中。

| 独立对象 | 失效后行为 | 恢复条件 |
|---|---|---|
| 员工 token | 每次请求重新认证；换人、过期、停用和凭证替换拒绝。旧 Context 仍绑定旧凭证摘要 | 新的有效认证和私有客户端登记；新 profile 不继承激活 |
| Context | 短期限或绝对期限到达后为 EXPIRED；显式关闭/替换为 REVOKED，终态不能续活 | 显式创建新 Context，再提交仍有效的本地文件 |
| 凭据文件/租约 | 复制文件不能越过主体、资源、版本、授权或租约状态；REVOKED 包不能重领/续期/重新激活 | 自然到期且当前授权及所有版本仍符合时，显式签发新包和新租约 |
| 本地文件 | 缺失或损坏时该本地选项不可用；组件关闭该 Context 的对应激活，不撤销全局授权 | 缺失文件可通过新 ticket 重领同一有效包；损坏文件不覆盖，恢复后仍须服务端重新校验 |

Context 续期保持同一个 ID/令牌/主体/客户端，只在绝对期限内延长短会话期限。凭据续期追加新包、新租约和父子关联，不修改旧租约期限，不创建原生账号、开通任务或授权。每个父包最多一个后继包；同一父包的并发续期复用这个后继。后继包终结后继续通过后继 ID 续期，不能从旧父包制造另一条分支。

每个登记客户端最多一个 ACTIVE Context。显式替换会关闭旧 Context；丢失创建响应时不回放私有令牌，使用新的请求键重新创建。重建 Context 可继续领取同一登记客户端的待办，但没有任何自动继承的激活。

## 2 待领取任务和激活

`credential.prepare_download` 仍只返回安全 ID、期限和状态。携带有效 Context 头时，任务绑定受信的客户端 ID，交付方式为 `ENROLLED_CLIENT`。提供空白、重复、非法或失效 Context 头时拒绝，不能回退为手动领取；没有 Context 头的旧 RA-08 手动流程继续可用，保留原响应字段。

私有组件查询 `GET /api/v2/credential-downloads`。服务端按组织、主体和登记客户端筛选任务；签名游标还绑定当前 Context 和有效期。单页最多 20 项，可配置更小上限。ACTIVATED、REPLACED、DISCARDED 收据不可改写，完成任务不再占据待办队头。同一个用户的另一个登记客户端也看不到这些任务。

一次同步只扫描至多 128 个本地包，读取至多 5 页/100 个任务，使用总请求期限和单请求期限，没有无限重试或隐式后台任务。每次从未完成队头开始，收据防止重复，未完成工作留给下一次显式同步。任务在一次同步的最后一页中新产生替代 ticket 时，可以留待下一次同步。

私有 claim 仍需当前员工认证、Context 和对应的独立交付授权。先持久消费，再写网络响应；收到完整响应且通过结构、长度和摘要检查才原子发布本地文件。服务器已 CLAIMED 而客户端没有文件时，通过替代任务恢复同一个有效包，原生账号不参与这个恢复流程。

激活请求提交实际加密文件和安全 ID。服务端在初次资格事务、事务外 AEAD 验证、最终当前状态事务三个位置检查边界，再原子追加激活版本、当前指针、任务完成收据、命令摘要和审计。撤权或关闭 Context 在解密期间发生，也不能产生新激活。每个 Context 同时保存多个资源的激活，历史版本只追加。

`CheckActivation` 仅是后续执行入口使用的服务端资格基础。RA-11 仍须在实际派发时重新核对资格；缓存、状态响应和一次验证都不能作为持续有效的执行票据。本轮没有注册正式 `pg.get_item` 或私有 activate/claim/decrypt MCP 工具。

## 3 私有缓存实现

新组件为 `cmd/mcpguard-session`，核心为 `internal/deliveryclient/session.go`。一个 profile 固定网关 origin、组织、主体、登记客户端和员工 token 摘要。员工 bearer 从环境读取，不写入缓存；私有交付授权文件单独保存。Context 令牌、索引和加密包仅保存在这个私有 profile 中。

```text
<private-profile>/
  .mcpguard-cache.lock       内核进程锁，崩溃后自动释放
  session.json              私有 Context、身份固定信息、限额、索引和命令请求键
  files/mcpguard-<id>.mgcred 只有服务端可解密的引用包
```

默认新 profile 限额为 128 个文件、16 MiB、每页 20 项、每次最多 5 页，Context 剩余 30 秒时可在仍 ACTIVE 且未达绝对期限的前提下续期。CLI 可在初始化时选择更小边界；已存在 profile 使用其持久策略。受保护的旧包文件保留，`selected` 固定每个资源的当前选择，Context 重建不会随机激活历史包。限额达到时拒绝，不通过删除历史文件腾出空间。

Linux 检查当前 UID、目录 0700/文件 0600，并使用 `flock`。Windows 使用当前用户和 SYSTEM 的受保护 DACL，拒绝其他允许 ACE、继承宽泛授权、UNC 和 reparse/junction；使用 `LockFileEx` 和 write-through 原子替换。路径拒绝 Git 工作树、符号链接、目录跳转和非普通文件；凭据文件不覆盖。私有内容写入前先收紧新临时文件，完整写入/同步后发布；持锁时有界清理崩溃遗留的私有未发布临时文件。

Windows 权限创建和检查使用系统安全描述符 API，语义参考 [Microsoft 安全描述符示例](https://learn.microsoft.com/en-us/windows/win32/secauthz/creating-a-security-descriptor-for-a-new-object-in-c--) 和 [SetNamedSecurityInfoW](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setnamedsecurityinfow)。本轮分别验证 Linux/WSL 私有组件和 Windows 原生文件安全。它们不等于某个真实 coding 客户端已完成私有交付适配。

缓存防止误传、跨 profile 混用和其他系统身份读取。**同一系统用户拥有任意文件读取/进程执行权限时，目录权限不提供模型与用户进程之间的隔离。** 因此模型不可见的声明限定为 MCP 协议、CLI 安全输出、应用日志和审计；部署客户端仍需落实 RA-10 的独立私有组件边界。

## 4 私有接口和本地使用

新增私有接口如下，全部要求当前员工认证，除创建外还要求 Context 头。创建要求独立的已登记交付授权。claim 另外要求相同交付授权；携带交付头的其他私有请求也校验其与当前 Context 的绑定。浏览器 Cookie/Origin 不提供私有访问权限，敏感头不能放到 URL。

| 接口 | 用途 |
|---|---|
| POST /api/v2/credential-contexts | 一次交付私有令牌，登记客户端来源于交付授权 |
| GET /api/v2/credential-contexts/{id} | 安全状态和当前多资源激活 |
| POST /api/v2/credential-contexts/{id}/renewals | 版本检查后续期；保持绝对上限 |
| DELETE /api/v2/credential-contexts/{id} | 显式关闭 |
| POST /api/v2/credential-contexts/{id}/activations | 上传引用包，服务端校验并追加激活 |
| POST /api/v2/credential-contexts/{id}/deactivations | 关闭这个 Context 的某项激活 |
| GET /api/v2/credential-downloads?limit=&after= | 客户端隔离的有界待办队列 |
| POST /api/v2/credential-downloads/{id}/replacements | 不复用已消费响应，创建同包替代 ticket |
| POST /api/v2/credentials/{id}/renewals | 显式追加后继包、租约及待领取任务 |

在可信服务端，使用只具登记权限的身份运行 `mcpguard-credentials enroll-client <ttl-seconds> <new-private-bootstrap-file>`，通过用户私有渠道交付 bootstrap。安全输出不含交付令牌。Context 运行角色没有登记权限，也没有读取原生秘密或管理账号的权限。

本地组件从 `MCPGUARD_TOKEN` 环境读取当前员工凭证。不要把真实 token、Context 令牌或 `.mgcred` 内容贴到模型对话中。以下参数全部为安全路径/ID，bootstrap 和 profile 必须位于仓库外：

```text
mcpguard-session init --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session sync --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session status --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session renew-context --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session renew-credential --credential-id <id> --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session init --replace-context --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
mcpguard-session close --gateway <https-origin> --auth-file <private-bootstrap> --directory <private-profile>
```

显式私有 `prepare --grant-id <id>` 复用原 POST /api/v2/credential-packages 准备接口。它不申请或审批账号。组件不会向模型输出私有 Context 创建响应；所有成功命令只输出安全标识、状态和计数。

## 5 开关、迁移和复现

RA-09 默认关闭，需已启用目录、申请、包及下载功能，再显式配置：

```text
MCPGUARD_RESOURCE_CONTEXTS=enabled
MCPGUARD_CONTEXT_DSN=<context-runtime-dsn>
MCPGUARD_CONTEXT_TTL_SECONDS=<1..900>
MCPGUARD_CONTEXT_LIFETIME_SECONDS=<TTL..3600>
MCPGUARD_CONTEXT_MAX_ACTIVATIONS=<1..100>
MCPGUARD_SYNC_PAGE_LIMIT=<1..20>
```

新增 `007_credential_contexts.sql`，原资源 001..006、元数据 001..004 和原生 001..002 不改。使用 `mcpguard-catalog grant-context-runtime <role>` 配置独立 Context 运行角色；`grant-context-enrollment <role>` 只用于可信登记端。原下载角色在 schema7 下仅增加新 queue sequence 的使用权限，仍可领取旧手动 ticket。迁移不制造客户端、Context、激活或新原生账号。

在 WSL 中运行 `python3 scripts/context_fixture.py prepare` 创建新的隔离场景，再顺序执行 `test`、`wire`、`access-smoke`、`provision-smoke`、`credential-smoke`、`download-smoke`、`smoke`、最后 `recovery-smoke`。Go 读取基础会修改测试资源和原生角色，不能与上述流程同时作用于同一个场景。旧场景、失败日志、原报告和固定本地服务均保留。

当前规范为 0.2.6；前一版六份原字节产物保留在 [0.2.5 快照](specs/v0.2/history/0.2.5/README.md)。RA-10 原生 HTTP/stdio 私有适配、RA-11 正式读取派发、RA-13/14 完整吊销及在途停止、RA-15 轮换/恢复 fence 和 RA-18 真实客户端验收继续按原计划推进。


## 后续 RA-10 状态（2026-10-26）

前文的私有组件现由 HTTP helper/worker 和 stdio 私有模式驱动。两入口均有界同步、重验文件、激活多资源，不重放业务；自然 EXPIRED 与主动 REVOKED 分别处理。默认协议最大页数时由服务端选择其更小单页策略，显式小页保留本地界限。旧手动命令及 schema7 不变，见[RA-10](private-client-adapters-local.md)。
