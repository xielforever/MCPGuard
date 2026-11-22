# 控制台连接器

管理员在星舰控制台的「资源 → 连接器」登记 PostgreSQL、Redis、Jenkins 或 Kubernetes。保存时选择获准调用的主体。草稿通过真实连通测试后即可启用，停用后新的 MCP 调用会被拒绝。客户端可在已有会话调用 `connector.list` 取得当前可用连接器的 ID。

## 启用管理功能

1. 对元数据运行迁移 006。沿用控制台配置和管理员身份。运行角色需要对 `mcpguard.connectors`、`mcpguard.connector_grants`、`mcpguard.connector_audit` 的 `SELECT`、`INSERT`，以及 `mcpguard.connectors` 的 `revision`、`enabled`、`test_status`、`tested_at` 列的 `UPDATE`。审计表保留追加权限。
2. 由部署者设置 `MCPGUARD_CONNECTOR_ALLOWED_CIDRS`，填允许网关访问的目标网段，使用逗号分隔。隔离本机验收可使用 `127.0.0.1/32`。未填写时保持现有启动方式。
3. 在已启用管理功能的网关中，连接器的登记、连通测试和启停从控制台完成。目的地址在每次新建连接时按允许网段校验。非回环连接使用验证证书的 TLS，可为有私有 CA 的连接填写 CA 证书。凭据随连接配置封存，控制台详情和 MCP 连接器目录仅返回安全摘要。

新版网关仍可在元数据版本 004 或 005 上运行既有功能。应用 006 后，回退到只接受 004 或 005 的旧网关时，需使用兼容版本的元数据备份。

## 支持的连接与读取

| 类型 | 需要填写的范围 | MCP 读取工具 |
| --- | --- | --- |
| PostgreSQL | 主机、端口、数据库、Schema 和只读账号 | `connector.pg.list_tables` 返回指定 Schema 中该账号可读的表。 |
| Redis | 主机、端口、允许的键前缀、前缀内可用于探测的键和 ACL 账号 | `connector.redis.get` 读取前缀内的单个键。 |
| Jenkins | 控制器地址、固定文件夹和 API token | `connector.jenkins.list_jobs` 列出任务，`connector.jenkins.describe_job` 返回当前参数。 |
| Kubernetes | API 地址、单一命名空间、服务账号 token 和所需的 CA 证书 | `connector.k8s.list_deployments` 读取该命名空间的 Deployment 摘要。为账号分配对应命名空间下的 `apps/deployments` 读取权限。 |

Jenkins 连接器默认只读。新增 Jenkins App 时可开启「允许提交构建审批」，由同一个 App 提供任务查询、参数发现和受控构建。构建还需要独立的提交和审批权限。已有文件配置继续走原入口。[Jenkins App 和受控构建](jenkins-integration.md)

## 跨项目复用

`github.com/xielforever/MCPGuard/pkg/connectors` 提供可导入的类型化适配器、连通测试、限定范围的读取和出站策略。调用方提供已获授权的后端定义。MCPGuard 继续负责连接器登记、凭据封存、身份核验、主体授权和审计，并在同一进程内调用该模块，从统一入口提供 `connector.list` 和各类读取工具。

给不同项目配置独立的 MCP 访问身份。在控制台为共享连接器授予多个主体，为私有连接器只授予所属主体。实际授权以网关核验的身份为准。连接器停用提交后，原 MCP 会话中的新调用也会被拒绝。

## 复验

`scripts/connectors_fixture.py` 准备独立 PostgreSQL、Redis、Jenkins 和 K3s 测试后端。在 Windows 使用 `scripts/connectors_console_browser.mjs` 驱动 Chrome 完成页面操作，随后核对实际 MCP 读取、坏凭据和停用结果。两段脚本通过本机私有测试文件交换短期凭据。

`scripts/connectors_multi_project_fixture.py` 从两个现有项目工作目录启动独立 DSH 客户端，使用不同测试身份接入同一个隔离网关。复验核对共享目录、私有授权、停用后的原会话调用以及实际 PostgreSQL 连接次数。[双项目验收](validation/local-connector-multi-project-2026-11-21.md)
