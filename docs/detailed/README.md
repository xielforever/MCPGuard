# MCPGuard 详细设计 v0.1

日期：2026-10-22。范围：在已确认总体架构下形成可实施的模块、数据、协议、执行与验证设计。当前交付为文档，未实现业务代码。

工程基线为模块化 Go 网关、独立 stdio 连接器、PostgreSQL 权威状态库和 Vue 控制台。企业网关自身提供 MCP；PG、Redis 和业务 API 仍使用原生协议。

## 评审顺序

| 文档 | 核心内容 |
|---|---|
| [设计决策与待确认事项](decisions.md) | 本轮技术选择、三项企业业务问题、影响与推进方式 |
| [身份、权限与策略](security-policy.md) | OAuth/会话、资源/数据授权、确定策略组合、审批资格与撤权 |
| [状态与存储](state-storage.md) | 核心DDL、不可变计划、行锁/CAS、单次领取、崩溃未知与outbox |
| [协议与连接器](transport-connector.md) | 两入口、profile、登录、长驻stdio映射、等待/控制工具/MRTR |
| [ChatGPT首发接入](chatgpt-access.md) | 官方产品能力、账户与发布限制、企业审批接入和实际验收范围 |
| [原生执行](native-execution.md) | 驱动契约、具体工具候选、PG事务/Redis命令/HTTP副作用与重试 |
| [服务、管理API与运行](service-operations.md) | Go模块、事务归属、接口版本与幂等、部署/恢复/可观测性 |
| [验证计划](validation-plan.md) | 夹具、桌面推演、并发故障点、两入口和真实IDE验收 |

## 可实施补充

| 文档 | 内容 |
|---|---|
| [可校验设计契约](../specs/README.md) | JSON Schema、OpenAPI、六工具配置、SQL模型与离线检查命令 |
| [受控工具目录与发布](tool-catalog.md) | 输入和成功data约束、可信scope、版本与ChatGPT快照 |
| [管理API契约](api-contract.md) | 审批完整详情、ETag、幂等顺序、取消及只读核对 |
| [存储约束落地](storage-invariants.md) | 数据库/应用责任、写前UNKNOWN、永久资格与事实收敛 |
| [控制台流程](console-workflows.md) | 角色页面、状态文案、审批与续传、冲突处理和结果未知 |
| [故障处置手册](failure-runbooks.md) | 数据库/通知/凭据/旧worker/恢复冻结/ChatGPT故障分流 |

这些是Markdown维护的源文档，流程图使用Mermaid；字段表和步骤提供图的文字说明。嵌入图用于开发评审，不能作为已经实现或通过验收的证据。

## 统一约束

- 同一个Action固定一个计划，Attempt表示本次MCP请求，Execution在领取时创建，记录存在不证明已发送。
- 持久Action状态统一为PENDING_APPROVAL、READY、EXECUTING及终态；策略允许和人类批准分别记录授权来源。
- 交互模式在创建时确定：BOUNDED_WAIT、CONTROL_FLOW或MRTR，不在调用结束后暗改模式。
- 需要延后时，CONTROL_FLOW返回独立DeferredActionResult；普通RPC已经完成，业务Action仍待批，后续显式resume使用新RPC。
- 审批回调只写决定；派发资格由有效活跃调用或受权续传原子领取，不能由通知、模型accept或到期租约产生。
- 不确定的写入保持结果未知，不自动重放；状态库备份恢复也不证明远端效果回滚。
- 业务写开始前先持久化副作用UNKNOWN；正常推进中的INTENT不等于确定未发送，失败结果data必须为null。

## 讨论入口

已确认风险分级、受控写他人审批、受控目录与ChatGPT首发；Q-01至Q-03的决定和剩余平台信息集中在[决策记录](decisions.md)。工程方案可并行推进，真实平台接通和客户端兼容范围以实际证据为准。

返回[总文档目录](../README.md)；相关高层契约见[领域与调用契约](../contracts.md)。
