# MCPGuard

企业集中 MCP 网关。客户端通过 HTTP 直连，或经本地 stdio 接入同一网关；网关使用 PostgreSQL 等后端的原生协议执行受控操作。

当前本机演示批次已实现六个固定业务工具、持久他人审批、操作控制/历史、规则、资源、用户组和受保护控制台；Telegram Outbox 已完成本地接收器测试，真实 Bot/收件人待配置，企业 SSO 由用户暂缓选型。当前聊天原生验收与服务端测试分别记录，见 [本批检查清单](docs/local-batch-checklist.md)、[受控本机配置](docs/controlled-local.md) 和 [本批证据](docs/validation/local-controlled-batch-2026-10-23.json)。

原有官方 Go MCP SDK、两种入口、真实 PG 读取和加密审计的历史基线见 [最小读取闭环](docs/read-slice.md) 与 [原验收记录](docs/validation/read-slice-2026-10-22.md)，总体规划见 [设计文档目录](docs/README.md)。

Windows / WSL 本地复验及启动竞态修复见 [本地闭环记录](docs/validation/local-read-slice-windows-wsl-2026-10-22.md)。

本机 Codex 直接使用 Streamable HTTP `http://127.0.0.1:18080/mcp`。现有 Windows/WSL 夹具的固定端口、后台运行、登录启动、按需拉起、凭据自动续期和重启命令见 [本机服务运行](docs/local-service.md)；不需要域名或公网隧道。

独立 OAuth 测试身份、临时 HTTPS 入口与 ChatGPT 联调步骤见 [HTTPS/OAuth 本地接入](docs/oauth-local-chatgpt.md)。原有本地测试模式继续保留。

ChatGPT 已完成 OAuth 并触发真实 PG 读取；当前网页答复加载失败，无工具对照也复现，用户展示尚未验收。[本轮记录与边界](docs/validation/chatgpt-oauth-read-2026-10-22.md)。

## 本地运行

需要 Go（最低 1.25，toolchain 1.26.8）、Python 3、Docker，race 测试另需 C 编译器；不需要大模型 key。

```bash
make dev-up    # 隔离 PG、数据库角色、假数据、测试身份、迁移和构建
make dev-test  # Go race + 独立 Python HTTP/stdio 联调
make dev-run   # 网关 http://127.0.0.1:8080/mcp
```

支持 stdio 的客户端可启动：

```bash
python3 /absolute/path/to/MCPGuard/scripts/dev.py connect --employee alice
```

测试工具 `pg.get_demo_item` 只接受 `{"itemId":"item_demo_1"}`；数据范围由网关根据员工注入。`audit.list` 接受 `{"after":"0","limit":2}`，仅查询当前身份的审计摘要，每页 1–100 条。`gateway.health` 接受 `{}`，分别检查进程存活及元数据/业务 PG 依赖就绪。Alice、Bob 访问各自业务范围，Visitor 只有本人审计及健康检查权。随机秘密位于忽略提交的 `.dev/env.json`（0600）；`make dev-down` 清除本项目的本地测试容器和数据。

当前支持 `dev-tokens` 与独立 `oauth-fixture` 测试身份，MCP profile 为 `2025-11-25`。受控工具使用显式启用的本机演示配置；普通 `dev-up` 不隐式扩大权限或连接新后端。企业 SSO 和真实 Telegram 投递尚未验收。原代理、确认码和 Vue 页面属于未挂载的旧脚手架；当前控制台由网关内嵌提供。

License: MIT


## 2026-10-24：多资源自助访问设计增量

已补充[设计入口](docs/revisions/2026-10-24-resource-access/README.md)、[开发计划](docs/revisions/2026-10-24-resource-access/development-plan.md)和[验收矩阵](docs/revisions/2026-10-24-resource-access/acceptance.md)。目标流程为多资源发现、账号及权限申请、原生开通、加密凭据下载、服务端执行和在线吊销；终端只持密文并取得业务结果。

本次为文档交付，新流程尚未实现。上文的本机演示实现和历史证据继续按原范围理解；既有设计及记录保留，新旧适用范围见[保留清单](docs/revisions/2026-10-24-resource-access/history.md)。

## 2026-10-24：RA-01/02/03 本地实现增量

后续已按用户授权完成版本化契约、可选 `resource.list/get` 目录，以及两套 PG、Alice/Bob 独立原生账号的隔离读取基础。新旧功能本地验证通过，详见[实现及复现说明](docs/resource-access-local.md)和[本轮验收记录](docs/validation/local-ra01-03-2026-10-24.md)。

目录需显式启用；新资源读取当前仅由集成测试探针验证，正式 `pg.get_item` 待 RA-11 接通授权和密文校验后开放。上节“文档交付”保留为此前状态记录；账号申请、加密凭据下载及完整吊销仍按[后续计划](docs/revisions/2026-10-24-resource-access/development-plan.md)推进。本次未部署或重启既有本地服务。


## 2026-10-24：RA-04 申请、决定与授权状态

后续已完成可选的 access.request/status/cancel/grants、独立浏览器保护审批入口和 AccessGrant 状态闭环，并通过本地新旧回归。批准生成 PROVISIONING，真实原生开通与 ACTIVE 待 RA-05；不增加无凭据的新资源业务执行入口。当前入口和复现见[流程说明](docs/resource-access-workflow-local.md)，结果见[本轮验收](docs/validation/local-ra04-2026-10-24.md)。原设计、契约快照、验收和旧迁移保留，本轮代码尚未部署到现有本地服务。


## 2026-10-26：RA-05 受限账号开通与真实核验

后续已完成独立开通进程、受限 PG 管理函数、新建/明确登记的个人账号核验和核验后 ACTIVE 激活。当前入口见[实现说明](docs/native-provisioning-local.md)，136 条 Go race、34 项新流程实际进程检查及旧功能回归见[本轮验收](docs/validation/local-ra05-2026-10-26.md)。批准 v1 原样保留，真实核验和账号/绑定关联追加为授权 v2；普通查询身份没有账号管理权。

原 0.2.1 契约按字节[归档](docs/specs/v0.2/history/0.2.1/README.md)，当前契约为 0.2.2。原设计及验收继续按各自阶段理解。RA-06 的恢复核对/清理重试、客户端密文下载、正式新资源执行和完整吊销仍待后续阶段；未部署本轮代码到现有本地服务。


## 2026-10-26：RA-06 开通恢复与清理重试

后续已完成带租约代次的崩溃恢复、同一原生账号的未知结果核对、永久封存和有界清理重试。167 条 Go race 和 35 项新流程实际进程检查通过，原 RA-04/05 与旧协议回归保持通过。当前入口见[恢复说明](docs/provision-recovery-local.md)及[本轮验收](docs/validation/local-ra06-2026-10-26.md)。原设计、历史状态和旧契约保留。

恢复不重新获得建号资格，撤权/到期后的迟到结果只进入清理；CLEANED 仅证明禁止新登录和本轮模板权限已撤回，完整在途停止仍按 RA-13/14 推进。下一项是 RA-07 的服务端 AEAD 引用包、租约与状态校验；客户端文件下载和正式业务读取没有提前开放。本轮没有部署到现有本地服务。


## 2026-10-26：RA-07 服务端引用包与租约

RA-07 已实现固定 AES-256-GCM 引用包、独立服务端包密钥、加密前持久 nonce 预留、按当前身份和授权复查、终态租约及脱敏状态查询。包不含原生密码，旧密文在服务端撤销后无法通过在线检查。当前入口见[实现说明](docs/credential-packages-local.md)和[本轮验收](docs/validation/local-ra07-2026-10-26.md)。

本地通过 210 条 Go race、101 项新进程检查，原申请/开通/恢复及旧协议回归继续通过；原设计、计划、旧迁移和证据保留。下一项为 RA-08 的一次领取与私有交付，本阶段没有开放客户端下载、Context 或正式资源读取，也没有部署到现有服务。


## 2026-10-26：RA-08 私有下载与一次领取

RA-08 已补齐安全准备、一次 ticket 消费、独立认证的私有交付、脱敏审计及 Linux/WSL 单次下载组件。MCP 只返回标识和状态，密文字节只进入私有领取响应及私有文件；响应丢失通过新 ticket 重取同一仍有效包，不创建新原生账号。说明见[私有交付](docs/private-downloads-local.md)，成绩见[本轮验收](docs/validation/local-ra08-2026-10-26.md)。

250 条 Go race、117 项新进程检查及原 RA-04/05/06/07、旧协议回归通过。原文与历史证据保留，下一项为 RA-09 的本地缓存、有界同步及 CredentialContext。原生客户端验收、Windows ACL、自动激活和正式资源读取继续按后续阶段验收；本轮没有部署到现有服务。


## 2026-10-26：RA-09 私有缓存、Context 与多资源激活

RA-09 已实现本地身份固定的安全缓存、待领取任务有界同步、CredentialContext、多资源激活和两类续期。员工 token、Context、文件/租约分别检查，Context 重建须重新提交有效文件，已撤销包不能重领、续期或重新激活。Linux/WSL 私有组件与 Windows 原生缓存安全分别验证，原功能与历史记录保留。见[说明](docs/credential-contexts-local.md)和[验收](docs/validation/local-ra09-2026-10-26.md)。下一项为 RA-10 的原生 HTTP helper 与 stdio 私有适配。


## 2026-10-26：RA-10 HTTP helper 与 stdio 私有适配

RA-10 已补齐原生 HTTP 私有头 helper、有界同步 worker、可选 stdio 私有模式和客户端能力声明。HTTP 头缓存与重连限制明确，stdio 调用前/准备后驱动私有交付及 Context 激活，私有响应不进入模型；未知结果只恢复私有任务，不重放业务。286 Go race、130 新进程和 11 Windows 原生检查通过。见[实现说明](docs/private-client-adapters-local.md)、[验收](docs/validation/local-ra10-2026-10-26.md)。下一项为 RA-11 正式新资源读取和派发复查。

## 2026-11-14：RA-11 计划读取与凭据绑定

已实现 `pg.get_item` 计划读取：显式 `resourceId`/`credentialId`、不可变计划绑定、派发前后资格复查、服务端解析原生凭据和关联审计。工具只在 `MCPGUARD_RESOURCE_READS=enabled` 且提供显式私密文件时注册，默认构建仍不开放正式业务读取；契约保持 0.2.7，未新增字段。299 Go race 通过，见[验收](docs/validation/local-ra11-2026-11-14.md)。下一项为 RA-12 的受控写与他人审批，真实客户端与生产部署仍未验收。

## 2026-11-16：RA-12 受控写与他人审批

已实现 `pg.update_note` 受控写：写计划冻结、他人审批（申请人不可自批）、显式恢复派发、单次派发、乐观锁版本冲突、派发前后凭据与绑定复查、关联审计；写身份为独立 `mg_` 受限账号，仅持有条级 UPDATE 与 `owner_update` 行策略，工具默认不注册。契约升级 0.2.8（开通模板新增 `PG_OWNER_UPDATE_V1`），provider DDL 为 v3。标准场景 299 Go race 通过；隔离写场景 5 项端到端用例通过，见[验收](docs/validation/local-ra12-2026-11-16.md)。控制台审批页面、真实客户端与生产部署仍未验收。

## 2026-11-16：RA-13 文件/授权/主体/资源级吊销与 epoch

已实现运营方特权的 `mcpguard-catalog revoke`：按文件（凭据）、授权、主体、资源四级撤销，单事务内锁定并推进授权 epoch、级联终态化租约/未领取 ticket/context/grant、禁用绑定与资源头，并写入带影响计数的撤销审计。撤销提交后新领取与新派发失败，另一副本连接池也能观察到 epoch 与终态。标准场景 299 Go race 通过；吊销场景 5 项端到端用例通过，见[实现说明](docs/revocations-local.md)与[验收](docs/validation/local-ra13-2026-11-16.md)。在途停止与原生清理（RA-14）、客户端缓存清理与真实客户端（RA-18）、控制台撤销页（RA-16）未验收。

## 2026-11-16：RA-14 在途停止、池淘汰与清理状态

已实现撤销时把在途写计划/读计划标记为 `OUTCOME_UNKNOWN`（不重放）、按 authorization epoch 淘汰原生连接池、撤销历史结果按 epoch 拒绝返回载荷，以及原生账号清理的 PENDING/FAILED/CLEANED 状态与可重试清理（失败不误报成功）。标准场景 299 Go race、写场景 6 项、吊销/清理场景 6 项用例通过，见[验收](docs/validation/local-ra14-2026-11-16.md)。真正终止原生后端语句、控制台与真实客户端未验收。
