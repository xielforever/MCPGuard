# RA-04 资源申请与审批闭环

日期：2026-10-24。当前契约修订为 0.2.1。RA-04 已实现申请、受保护的人工决定、待开通授权、取消、失效收敛及幂等；本地结果见 [验收记录](validation/local-ra04-2026-10-24.md)。原 [RA-01/02/03 说明](resource-access-local.md)及历史记录保留。

## 当前状态与边界

申请接收时，服务端按当前身份校验资源可见性、申请权、资源启用、预设套餐和显式审批策略，直接原子落为 `PENDING_APPROVAL`。申请冻结资源/套餐版本、资源发布 revision、申请资格 revision、主体禁用 revision、策略版本/revision、期限、理由和摘要。修改申请范围或期限需新申请。

| 对象 | 当前可发生的状态变化 |
|---|---|
| AccessRequest | PENDING_APPROVAL → APPROVED / REJECTED / CANCELLED / EXPIRED / STALE |
| AccessDecision | 合格他人作出 HUMAN 决定，一份申请只保留一个不可变决定 |
| AccessGrant | 批准原子创建 PROVISIONING；待开通期间过期进入 EXPIRED，资格或配置失效进入 REVOKED；终态不复活 |

`APPROVED` 与 `ACTIVE` 继续分开。本阶段没有创建原生账号、核验原生权限或激活授权；待开通授权不伪造 accountId/bindingId。当前数据库约束拒绝直接写入 ACTIVE。RA-05/06 负责真实开通和资格核对，RA-07 之后才进入密文包流程，正式业务读取仍按 RA-11 实施。

期限及资格失效在查询、目录状态汇总、申请或决定事务中按当前数据库时间收敛，并持久记录终态。没有后台原生开通 worker；任何后续执行仍需在同一领取事务重新验证当前资格，不能把曾返回的 PROVISIONING 当作执行权。

## 当前入口

| 入口 | 用途 |
|---|---|
| MCP access.request | 选择 resourceId/profileId、requestedExpiry、reason、requestKey；主体由认证上下文取得 |
| MCP access.status | 读取本人或当前有指定资源审批资格的申请，关联决定和授权 |
| MCP access.cancel | 本人取消待批申请，提交 expectedRevision 和 requestKey |
| MCP access.grants | 本人授权分页列表，可按 resourceId 筛选 |
| POST /api/v2/access-requests | Bearer 客户端提交申请 |
| GET /api/v2/access-requests/{id} | 同 access.status |
| POST /api/v2/access-requests/{id}/cancel | 同 access.cancel；路径 ID 与请求体 requestId 必须一致 |
| GET /api/v2/access-grants、GET /api/v2/access-grants/{id} | 列表及具体授权状态 |
| GET /console/api/v2/access-requests | 经独立浏览器登录查看本人的资源审批范围内待批队列，不包含自己提交的申请 |
| GET /console/api/v2/access-requests/{id} | 受保护的审批详情及状态 |
| POST /console/api/v2/access-requests/{id}/decisions | 独立浏览器会话、Origin、CSRF、If-Match、Idempotency-Key 和当前资源审批权共同校验 |

MCP 不注册批准工具，Bearer API 没有审批别名。即使申请人有该资源的审批资格，也不能批准自己的申请。现有控制台认证继续复用；RA-04 提供受保护 API，完整申请/审批页面仍在 RA-16。

批准只能保留原套餐并缩短期限，不能扩大请求期限。拒绝不提交 approvedExpiry。决定匹配原 requestHash 和 revision，权限、资源、策略或主体资格已变化时拒绝旧决定。当前只支持显式人工决定，不从旧演示管理员、组授权或字符串策略引用自动推导预授权。

变更的幂等键按组织、主体、命令和目标隔离。相同键同载荷重新鉴权后返回原提交快照；同键异载荷返回 IDEMPOTENCY_CONFLICT。快照不是当前状态，过期或资格变化后使用 status/grants 查看当前终态，重放旧成功响应不会重新建立授权。分页游标有签名，并绑定主体、列表种类、资源筛选和授权 epoch。

## 显式启用和管理配置

新网关同时设置：

```text
MCPGUARD_RESOURCE_CATALOG=enabled
MCPGUARD_RESOURCE_ACCESS=enabled
```

缺省不开启申请流程。第二个开关依赖资源目录；启用前需显式执行目录迁移及运行权限配置：

```text
mcpguard-catalog migrate
mcpguard-catalog grant-runtime <runtime-role>
mcpguard-catalog grant-access-runtime <runtime-role>
mcpguard-catalog import <catalog.json>
mcpguard-catalog import-access <access.json>
```

管理命令的迁移 DSN 仅从私有环境读取。access.json 使用生成契约 `AccessConfiguration`，配置不可变策略版本、单调递增的策略 head 以及组织/资源/主体限定的审批资格；普通运行身份无策略管理权。相同内容重复导入不改变 epoch，同版本异内容拒绝。缺少有效套餐策略时不能申请。

每项策略明确 maxGrantTtlSeconds 与 reviewTtlSeconds，不提供隐式生产期限。resource.get 为可申请的套餐返回这两项界限；resource.list/get 在启用 RA-04 时显示最新申请/授权流程状态。该目录汇总不是执行资格，具体授权仍以 grants/status 为准。

申请、决定和幂等响应复用服务端 Vault 加密保存，审计只保留主体、对象关联和事件类型。状态、决定、授权及关键审计同事务提交。新增 `access_audit` 尚未并入旧 audit.list；完整跨阶段审计和管理页面随后续阶段交付。

## 隔离复现

在仓库根目录运行：

```powershell
wsl.exe --exec python3 scripts/access_fixture.py prepare
wsl.exe --exec python3 scripts/access_fixture.py test
wsl.exe --exec python3 scripts/access_fixture.py wire
wsl.exe --exec python3 scripts/access_fixture.py smoke
wsl.exe --exec python3 scripts/access_fixture.py contracts
```

每次 prepare 新建独立场景，不重置原服务或历史场景。新构建位于 `.dev/ra04-build`，指针为 `.dev/ra04-latest`。私有配置、日志位于 Linux 用户目录 `.local/state/mcpguard-ra04/`，权限 0700；不再依赖重启时可能被清理的 /tmp。测试会改变专用场景的权限和状态，重复完整验证应从新 prepare 开始。

test 执行全仓库 Go race 检查，包含两套 PG 原生读取和既有受控工具回归；wire 独立验证旧 HTTP/stdio；smoke 以实际二进制完成 MCP 申请、独立浏览器决定、stdio 查询/取消和进程重启恢复。测试结果与桌面原生客户端、人工体验或部署验收分别记录。


## 后续 RA-05 状态（2026-10-26）

前文保留 RA-04 时点，其“当前不可 ACTIVE”不覆盖后续迁移 003。RA-05 现已实现独立受限开通进程、个人原生账号创建/明确登记的既有账号核验，以及真实证据关联的授权 v2 / ACTIVE。批准 v1 不变，原生核验与当前资格/期限复查缺一不可。详见[RA-05 说明](native-provisioning-local.md)及[验收](validation/local-ra05-2026-10-26.md)。RA-06 的核对恢复和清理重试继续独立推进。


## 后续 RA-06 状态（2026-10-26）

开通任务现可在租约过期后按冻结计划核对真实结果；未知账号不换名重建，撤权后迟到账号只清理，重试和人工处理状态可通过 access.status 脱敏查看。原申请、决定、取消和幂等语义保持。当前说明见[恢复流程](provision-recovery-local.md)，分层结果见[验收](validation/local-ra06-2026-10-26.md)。


## 后续 RA-07 状态（2026-10-26）

只有个人原生账号已经核验、授权 ACTIVE 且当前资格仍有效，才能在服务器内部签发引用包。签发前后均核对主体、授权、绑定、资源/套餐、issuer 和租约策略；审批接口及原申请状态闭环不变。新增 credential.status 仅查看本人安全状态。详见[引用包说明](credential-packages-local.md)及[验收](validation/local-ra07-2026-10-26.md)。


## 后续 RA-08 状态（2026-10-26）

审批与原生核验后，本人可用 credential.prepare_download 取得安全标识和短期 ticket；不接受客户端自报 owner/clientInstanceId 决定资格。MCP bearer、浏览器 Cookie 或 ticket ID 单独都不能领取文件，私有接口另需独立下载授权。原申请、决定及终态不变，见[RA-08 说明](private-downloads-local.md)。


## 后续 RA-09 状态（2026-10-26）

申请、审批、原生开通之后，私有组件现在可以按登记客户端有界同步待办、保存包并激活多个资源。重领和续期不重复申请、批准或创建原生账号；员工凭证变更不能改用另一个人的缓存。见[缓存与 Context](credential-contexts-local.md)及[验收](validation/local-ra09-2026-10-26.md)。
