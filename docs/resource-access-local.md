# RA-01/02/03 本地实现与复现

日期：2026-10-24。当前切片完成版本化契约、可选资源目录，以及两套 PG、两用户独立原生身份的隔离读取验证。验证结果见 [本地验收记录](validation/local-ra01-03-2026-10-24.md)。原设计和后续阶段见 [开发计划](revisions/2026-10-24-resource-access/development-plan.md)。

## 已交付入口

| 入口 | 当前行为 |
|---|---|
| `resource.list` | 按当前认证主体列出可发现资源，支持 kind、limit 和有签名的分页游标 |
| `resource.get` | 按稳定 resourceId 返回安全目录视图和当前版本绑定的权限套餐摘要 |
| `GET /api/v2/resources`、`GET /api/v2/resources/{id}` | 与上述工具共用目录和当前身份校验 |
| `mcpguard-catalog migrate` | 管理员显式建立独立的 `mcpguard_ra` schema，校验迁移版本和 SHA256 |
| `mcpguard-catalog grant-runtime <role>` | 为指定运行角色授予目录读取及隔离读账本所需权限 |
| `mcpguard-catalog import <catalog.json>` | 管理员事务化导入版本、head、套餐、工具绑定、可见性和个人原生绑定 |

目录开关为 `MCPGUARD_RESOURCE_CATALOG=enabled`，缺省关闭，拼写错误会拒绝启动。启用前需以独立管理环境执行迁移、权限授予及明确的目录导入。普通启动不会自动建表、创建账号、导入权限或改动既有本地服务。已启用但迁移缺失、版本或校验和不符时，启动失败。

管理命令仅从其私有环境读取 `MCPGUARD_MIGRATION_DSN`；该值不进入 MCP 参数。资源导入文件不包含原生密码，运行网关没有目录管理 HTTP/MCP 接口。ResourceVersion、PermissionProfile、ToolResourceBinding 不原地改写；head 发布需递增 revision，不能退回旧资源版本。同一内容重复导入不推进授权 epoch。

## 读取边界

RA-03 的实际读取由集成测试二进制中的 `fixture.pg_read_item` 探针触发。该工具只在 `*_test.go` 注册，生产二进制不注册探针、不增加新资源的无包业务读取入口。正式 `pg.get_item` 按 RA-11 接入 AccessGrant、CredentialPackage 和 Context 校验后再开放。

测试预先创建四份个人绑定：orders/Alice、orders/Bob、customers/Alice、customers/Bob。两套 PG 使用同一个数据库名，两人分别在两套 PG 使用相同账号名，但秘密各不相同。服务端按主体、资源及版本、绑定及版本、原生身份、秘密版本、套餐版本和端点指纹选择连接池；不会以别名、单一全局业务 DSN 或客户端自报 owner/scope 选择账号。

原生账号只有 CONNECT、schema USAGE 和固定表 SELECT；没有管理员、BYPASSRLS、角色成员或写权限。PG 的 RLS 使用 `owner_name = CURRENT_USER` 隔离数据。查询模板不接收 SQL、权限 SQL、任意目标或 owner 条件，服务端每次复用连接均核验真实 session_user/current_user、对象权限和 RLS。释放连接时清理预编译语句及会话状态，失败则关闭连接。

本阶段 `SecretProvider` 的本地文件实现仅用于隔离环境，限制 loopback PG、常规文件和私有文件权限。密码留在服务端私有目录，目录返回值不包含地址、账号或秘密引用。它不代表企业秘密管理、账号开通或加密文件下载已经交付。目录中的 `NOT_REQUESTED` 反映尚无 RA-04 授权记录；`canRequest` 只表示可申请目录资格。

读取资格和结果写入独立的 `fixture_reads` / `fixture_audit`；资格关联主体、资源/绑定/秘密版本、epoch 和计划摘要，结果复用现有 Vault 加密。该探针账本尚未合并进旧 `audit.list`，完整生命周期审计和恢复处置随后续阶段实施。

## 本地复现

在仓库根目录执行；当前 Windows 环境使用 WSL Docker 中缓存的 Go 工具链：

```powershell
wsl.exe --exec python3 scripts/resource_fixture.py prepare
wsl.exe --exec python3 scripts/resource_fixture.py test
wsl.exe --exec python3 scripts/resource_fixture.py wire
wsl.exe --exec python3 scripts/resource_fixture.py smoke
wsl.exe --exec python3 scripts/resource_fixture.py contracts
```

`prepare` 创建新的随机命名场景，包含独立元数据/旧演示 PG、两套新业务 PG 和用于旧功能回归的 Redis；不会清空原场景。私有配置与原始输出位于 Linux `/tmp/mcpguard-ra01-03-<runId>`，外层目录为 0700；仓库 `.dev/ra01-03-latest` 只记录当前场景位置。构建文件进入独立 `.dev/ra01-03-build`，不会覆盖原运行二进制。

`test` 执行 `go test -race -p 1 -count=1 -json ./...`，会在专用夹具中测试停用、版本变化和绑定失效，因此重做完整场景应先重新 `prepare`。失败日志与已有场景保留，不把旧失败改写为通过。`wire` 使用独立进程验证旧 HTTP/stdio 路径、两用户、取消隔离和重启审计保留。MCP SDK 的新资源调用与真实桌面客户端验收分开记录。

`smoke` 以实际网关二进制在隔离端口启用目录开关，验证三种身份的安全目录、MCP 初始化和 `resource.list`，并确认测试探针及正式 `pg.get_item` 没有注册。验收结束后只停止本任务创建的容器，保留数据、私有配置和所有日志；重新验证仍从 `prepare` 创建新场景。

契约生成和离线校验参见 [v0.2 契约说明](specs/v0.2/README.md)。正式员工 SSO、申请/审批/原生开通、密文包签发下载、私有激活、生产读取/受控写及完整吊销仍按 RA-04 至 RA-18 推进。


## 后续 RA-04 状态（2026-10-24）

前文保留 RA-01/02/03 状态。申请、人工决定、待开通授权及失效终态随后已实现并本地验证，见[RA-04 说明](resource-access-workflow-local.md)和[验收](validation/local-ra04-2026-10-24.md)。RA-04 使用独立开关、新夹具脚本和持久私有目录，不改写原验证记录；真实开通、密文和正式执行继续按后续阶段推进。


## 后续 RA-05 状态（2026-10-26）

RA-03 原预配账号基础保留。RA-05 已补充受限开通身份、真实新建个人账号、明确登记的既有账号核验和核验后激活；本地加密秘密只在服务端解密，不能据此认定客户端密文包阶段完成。详见[本地说明](native-provisioning-local.md)和[验收](validation/local-ra05-2026-10-26.md)。正式新资源读工具依然等待 RA-11。


## 后续 RA-06 状态（2026-10-26）

RA-06 已补齐开通崩溃恢复、未知结果核对和托管模板清理重试，原两资源/两用户读取基础及历史证据保留。恢复不会获得新业务执行资格，也不提前发布正式 pg.get_item。详见[恢复说明](provision-recovery-local.md)及[验收](validation/local-ra06-2026-10-26.md)。


## 后续 RA-07 状态（2026-10-26）

RA-07 已补齐服务端 AEAD 引用包、终态租约和在线复查；包与原生秘密解析分离。测试中继续验证两 PG、两用户独立账号及服务端原生读取基础，正式 pg.get_item 仍未注册。见[说明](credential-packages-local.md)及[验收](validation/local-ra07-2026-10-26.md)。


## 后续 RA-08 状态（2026-10-26）

本人 ACTIVE 授权现可准备安全下载任务，经独立私有认证一次领取密文。两套 PG 的实际账号、授权、包和租约数量在网络重试及重领前后保持，原读取基础继续回归；正式 pg.get_item 尚未注册。见[私有交付说明](private-downloads-local.md)与[验收](validation/local-ra08-2026-10-26.md)。


## 后续 RA-09 状态（2026-10-26）

两套 PG、两用户读取基础继续回归；Context 现可并行维护多个资源的个人激活，缓存、重领、Context 重建和续期均复查当前身份与包/授权。正式新资源读取及实际派发仍由 RA-11 接通。见[RA-09](credential-contexts-local.md)。
