# 执契设计文档 v0.1

当前控制台 UI 实施入口：[D 主题与现有六页迁移](../design-system/mcpguard/implementation-console-d.md)。该记录说明统一主题、共用组件、现有真实 API 的保留及本地验证范围；下文原始设计状态保留其历史时点。

状态：设计初稿，待评审。日期：2026-10-22。文档描述目标方案，不表示功能已实现。

实现入口：[最小读取闭环](read-slice.md)。该增量包含本地测试身份、两种 MCP 入口、真实 PG 读取和持久审计；下列完整企业方案仍按各文件的设计状态理解。

后续本地联调入口：[OAuth + HTTPS + ChatGPT](oauth-local-chatgpt.md)。该测试身份服务与原有本地模式隔离，企业 SSO 仍待接入。

**执契为企业应用提供统一的 MCP 接入。**支持 IDE 通过 HTTP MCP 直连，或通过本地 stdio 转 HTTP 程序连接同一网关；网关再通过 PG、RESP、HTTP/HTTPS 等原生协议访问后端。企业服务器与员工本地资源已可互通。

当前评审入口：[详细设计](detailed/README.md)，覆盖模块与API、身份策略、存储DDL、两入口、原生执行和验证。用户决定与剩余平台信息集中在[决策记录](detailed/decisions.md)。

可校验的[契约与配置](specs/README.md)已补充JSON Schema、OpenAPI、六工具包、SQL模型与复现命令；界面与处置流程见[控制台设计](detailed/console-workflows.md)和[故障手册](detailed/failure-runbooks.md)。

## 阅读顺序

| 文档 | 回答的问题 |
|---|---|
| [产品需求与验收要求](requirements.md) | 为谁提供哪些能力？哪些已确认、建议或待定？ |
| [系统总体设计](mvp-design-discussion.md) | 网关由什么组成？一次调用、授权和执行如何流转？ |
| [领域与调用契约](contracts.md) | 资源、工具、计划、决定和执行有哪些字段？输入输出如何表达？ |
| [MCP 接入与执行边界](mcp-protocol-design.md) | IDE 到网关的版本、身份、调用、取消和结果如何处理？ |
| [IDE 接入兼容方案](ide-access-compatibility.md) | HTTP 直连与 stdio 程序如何实现和分别验收？ |
| [原生服务适配](native-adapter-design.md) | PG、Redis、API 的连接、事务、命令和副作用如何约束？ |
| [交付计划与验收追踪](delivery-plan.md) | 按什么依赖交付？如何证明从 IDE 到原生执行的闭环？ |

## 状态约定

“已确认”表示已确定的需求或架构边界，不表示开发完成。“建议”表示本稿提出的实现选择。“待定”表示尚未决定，后续讨论应同时更新相关契约与验收。

旧 Go/Vue 代理脚手架仍保留，但新运行入口已经切换到读取闭环。企业身份、受控写、他人审批及其他原生适配须继续按新方案实现及验证。SDK 或沙箱协议测试不能代替真实 IDE 验收。

## 下一轮讨论

风险分级、受控目录和ChatGPT首发已确认。继续细化具体工具与数据范围、合格审批组、ChatGPT账号/工作区、企业身份和通知平台；元数据工程基线选用PostgreSQL，留存和容量参数在验收前校准。详见交付计划D-01至D-06。


## 2026-10-24 当前增量入口：多资源自助访问 v0.2

后续开发采用[多资源自助访问设计](revisions/2026-10-24-resource-access/README.md)中明确的增量：统一网关配置、多资源目录、账号权限申请、仅服务端可解密的凭据文件、服务端原生执行和在线吊销。原文按 2026-10-22 的范围保留，历史实现和验收不回写。

| 当前入口 | 内容 |
|---|---|
| [架构与流程](revisions/2026-10-24-resource-access/architecture.md) | 登记、发现、申请、账号开通、下载、执行及撤权 |
| [领域与接口](revisions/2026-10-24-resource-access/contracts.md) | 当前授权、包、租约、上下文、API 和状态 |
| [凭据与客户端](revisions/2026-10-24-resource-access/credential-lifecycle.md) | 加解密、私有下载、单配置多资源及在线校验 |
| [开发计划](revisions/2026-10-24-resource-access/development-plan.md) | RA-P0 至 RA-P7、依赖、任务、迁移和退出条件 |
| [验收矩阵](revisions/2026-10-24-resource-access/acceptance.md) | RA-AC-01 至 RA-AC-24，业务场景全部 NOT_RUN |

新能力尚未编码、部署或验收；v0.1 机器契约只保持原版本适用范围。

## 2026-10-24 后续状态：RA-01/02/03 已完成本地切片

上述表格保留设计交付时点。后续已按用户授权完成版本化契约、资源目录和个人原生身份读取基础，并通过本地新旧回归。当前结果见[验收记录](validation/local-ra01-03-2026-10-24.md)，使用与复现方式见[本地说明](resource-access-local.md)。

正式业务读取仍等待 RA-11 的授权/密文检查，当前新资源读取只在集成测试探针中验证；申请、开通、下载、激活、完整吊销及真实客户端验收继续按原依赖推进。原设计、历史证据和旧迁移保留，既有本地服务未部署本次代码。


## 2026-10-24 后续状态：RA-04 已完成本地切片

申请、保护审批决定及待开通 AccessGrant 现已完成本地验证，前文保留原阶段状态。当前入口见[流程说明](resource-access-workflow-local.md)，成绩及边界见[验收](validation/local-ra04-2026-10-24.md)。原生账号开通与 ACTIVE、密文和正式业务执行仍按后续阶段推进。


## 2026-10-26 后续状态：RA-05 已完成本地切片

受限 ProvisioningProvider、真实个人账号创建/既有账号核验和 AccessGrant 激活已完成本地验证，见[实现与复现](native-provisioning-local.md)及[验收](validation/local-ra05-2026-10-26.md)。原文保留各阶段快照；当前批准先产生 PROVISIONING，经真实权限核验和资格复查后才追加 ACTIVE 版本。下一项为 RA-06，完整恢复/清理及密文下载、正式执行和吊销不在本轮完成范围。


## 2026-10-26 后续状态：RA-06 已完成本地切片

当前已补齐开通任务崩溃恢复、未知结果核对和清理重试，见[实现及复现](provision-recovery-local.md)与[验收](validation/local-ra06-2026-10-26.md)。恢复只处理原冻结计划，不换名重建、不复活终止授权，无法确认的结果明确进入待重试或人工处理。前文按各阶段时点保留；下一项为 RA-07。


## 2026-10-26 后续状态：RA-07 已完成本地切片

服务端引用包、独立密钥、持久 nonce 预留、受授权/策略约束的租约、在线状态校验及单文件租约关闭已完成，详见[说明及复现](credential-packages-local.md)与[验收](validation/local-ra07-2026-10-26.md)。可选公开入口仅返回本人脱敏状态。客户端下载/Context 和实际派发复查继续依赖后续阶段；前文各时点原文保留。


## 2026-10-26 后续状态：RA-08 已完成本地切片

当前新增 credential.prepare_download、安全 ticket 状态、独立认证的一次私有 claim、服务器写响应结果审计和单次受控下载组件。117 项实际进程检查覆盖并发、请求未到达、消费后响应丢失和新 ticket 不增加原生账号；250 条 Go race 全通过。见[实现及复现](private-downloads-local.md)、[验收](validation/local-ra08-2026-10-26.md)。MCP/输出/日志脱敏与同系统账号任意文件读取隔离分别说明，自动 Context/缓存同步留给 RA-09。


## 2026-10-26 后续状态：RA-09 本地完成

当前增加经登记客户端绑定的 Context、有界私有队列、本地安全缓存、多资源激活、Context 续期和包/租约追加续期。未知领取结果通过同包新 ticket 恢复，原生账号不重建。实现、开关、命令和明确边界见[RA-09 说明](credential-contexts-local.md)，实际成绩及保留记录见[验收](validation/local-ra09-2026-10-26.md)。Windows 文件安全与真实 coding 客户端适配分别验收，下一项为 RA-10。


## 2026-10-26 后续状态：RA-10 本地完成

新增 HTTP helper/worker 与 stdio 私有模式，能力声明区分宿主私有头、每次远程请求更新和无私有钩子的 URL-only 客户端。两入口分别证明交付、激活、失效恢复及输出隔离；原流程和旧记录保留。具体命令、限制及证明层次见[RA-10 说明](private-client-adapters-local.md)与[验收](validation/local-ra10-2026-10-26.md)。


## 2026-10-29 界面方向与 UI/UX 规范

用户已选择 D「星舰 · 审批舰桥」，项目级 [UI/UX 规范 v1.0](../design-system/mcpguard/MASTER.md)现已建立，覆盖数值 token、共用组件、审批、资源账号权限、私有交付、Context、多资源与吊销状态，以及管理页面和后续验收。

各轮候选与检查保持原样；当前选型见[变更记录](../design-system/mcpguard/CHANGELOG.md)。本次是规范交付，未修改页面、后端或版本化业务契约，也不改变原 RA 开发计划与验收状态。

## 2026-11-14 后续状态：RA-11 本地完成

新增 `pg.get_item` 计划读取：显式资源与凭据绑定、不可变计划、派发前后复查、服务端原生凭据与关联审计；仅在显式配置下注册。契约保持 0.2.7，未改字段。见[验收](validation/local-ra11-2026-11-14.md)。RA-12 及之后的依赖与退出条件不变。

## 2026-11-16 后续状态：RA-12 本地完成

新增受控写 `pg.update_note`：计划冻结、他人审批、显式派发、单次执行、乐观锁与派发前后复查；写身份为独立受限 PG 账号（仅行级 UPDATE + `owner_update` 行策略），工具默认不注册。契约 0.2.8，provider DDL v3。标准场景 299 Go race、隔离写场景 5 项端到端用例通过，见[验收](validation/local-ra12-2026-11-16.md)。控制台审批页、真实客户端与生产部署留待后续阶段。

## 2026-11-16 后续状态：RA-13 本地完成

新增文件/授权/主体/资源四级撤销与 epoch：特权 CLI `mcpguard-catalog revoke` 在单事务内锁定并推进授权 epoch，级联终态化租约、未领取 ticket、context 与 grant，禁用绑定/资源头，并写撤销审计（含影响计数）。撤销先提交则新领取失败；另一副本连接池可见同一 epoch 与终态。见[实现说明](revocations-local.md)与[验收](validation/local-ra13-2026-11-16.md)。在途停止/原生清理（RA-14）、客户端缓存与真实客户端（RA-18）、控制台撤销页（RA-16）未验收。

## 2026-11-16 后续状态：RA-14 本地完成

撤销时在途写/读计划转 `OUTCOME_UNKNOWN`（保留完成事实、不重放）、原生池按 `auth_epoch` 淘汰、历史结果按 epoch 拒绝载荷，并新增原生账号清理台账：撤销只登记 PENDING，失败可重试，成功证据才 CLEANED。见[验收](validation/local-ra14-2026-11-16.md)。后端在途语句终止、控制台与真实客户端未验收。

## 2026-11-16 后续状态：RA-15 本地完成

issuer 轮换使旧批准与旧包失效；恢复 fence 跨存储实现：原生 PG 保存 per-org 恢复代次，元数据记录恢复点，原生更高即拒绝签发/领取/派发，`restore-check`/`restore-accept` 供运营方核验。provider DDL v4、元数据迁移 012。见[验收](validation/local-ra15-2026-11-16.md)。真实客户端、生产备份恢复演练未验收。

## 2026-11-16 后续状态：RA-16 本地完成

控制台新增自助访问页：资源/用量、凭据状态、吊销记录与四级撤销、写审批队列与决定，全部走真实 API（会话+CSRF）。禁止自批（403）、重复决定（409）、响应无秘密。见[验收](validation/local-ra16-2026-11-16.md)。派发仍由申请人 resume；真实客户端与生产未验收。

## RA-17 增量迁移与配置回退

元数据、资源目录和原生提供方支持只读迁移预检。通过独立开关启用或关闭旧演示工具与 v0.2 业务入口，回退时保留两代账本。详细配置、验证步骤和回退顺序见[迁移与回退手册](migration-rollback-local.md)和[本地验收](validation/local-ra17-2026-11-18.md)。
