# v0.2 增量迁移与配置回退

适用于已经运行 v0.1 旧接口、准备启用 v0.2 多资源自助访问的环境。网关启动时校验已安装的架构，迁移和原生提供方安装由管理员显式执行。RA-17 的可复现验证使用本机隔离 PostgreSQL。

## 升级前检查

1. 固定当前二进制、版本与配置。私下备份元数据和业务数据，核对备份可读并保存恢复所需的密钥版本和秘密引用。使用独立迁移账号，避免临时提高运行账号权限。
2. 核对旧直读、受控写和演示 HTTP 后端的实际 PostgreSQL 库、关系和数据范围。保留旧接口时，它们只能指向独立的 demo 库。带有 `resource_demo` 或 `mcpguard_provision` 架构的数据库会被旧 PG 适配拒绝，即使端口、别名或 DSN 文本不同，或者网关已经回退到旧接口配置。单独运行的 `mcpguard-demo-http` 与网关一并升级。自定义的旧 HTTP 后端也须核对数据范围。
3. 通过私有配置设置 `MCPGUARD_MIGRATION_DSN`。原生提供方使用独立的 `MCPGUARD_PROVIDER_ADMIN_DSN` 和 `MCPGUARD_PROVIDER_MANAGER_ROLE`，以及与所连数据库对应的提供方配置文件。依次检查元数据、资源目录，以及每个原生库的提供方版本。

```bash
mcpguard migrate --dry-run
mcpguard-catalog migrate --dry-run
mcpguard-provision install-provider provider-target.json --dry-run
```

每条命令返回 `schema`、`currentVersion` 和按顺序排列的 `pendingVersions`。预检使用 PostgreSQL 只读事务，只查询版本历史和校验和。版本缺段、校验和变化、版本超过当前二进制可识别范围时，预检和实际迁移均失败。旧版本可能按 LF 或 CRLF 计算校验和，两种已存在的记录都能核验，新记录按 LF 计算。预检应与隔离副本上的实际升级一起使用，以核对 DDL 和运行权限。

## 增量应用与只读业务演练

1. 在隔离副本上用相同配置执行下面的命令。`mcpguard` 维护 `mcpguard` 版本 1–4，资源目录独立维护 `mcpguard_ra` 版本 1–12，原生提供方独立维护 `mcpguard_provision` 版本 1–4。每组迁移按序在同一事务中应用，能够重跑。保留旧 Action、Attempt、Execution、审计和加密结果。

   ```bash
   mcpguard migrate
   mcpguard-catalog migrate
   mcpguard-provision install-provider provider-target.json
   ```

2. 按[资源目录](resource-access-local.md)、[受限账号开通](native-provisioning-local.md)和[凭据上下文](credential-contexts-local.md)登记明确的 v0.2 资源与审批人，并授予各运行账号所需权限。已有 demo 用户、旧账号、旧审批和历史计划不转换为 v0.2 资源授权。
3. 先用 `MCPGUARD_LEGACY_DEMO=disabled` 禁用旧业务工具。该模式可忽略旧 PG 和原生适配配置，保留审计、历史、状态查询及控制台。依次配置 `MCPGUARD_RESOURCE_CATALOG`、`MCPGUARD_RESOURCE_ACCESS`、`MCPGUARD_RESOURCE_CREDENTIALS`、`MCPGUARD_RESOURCE_DOWNLOADS`、`MCPGUARD_RESOURCE_CONTEXTS` 和 `MCPGUARD_RESOURCE_READS`，各项设为 `enabled`，并提供各自的现有私有配置。读取运行账号按 `grant-read-runtime` 授权。`MCPGUARD_READ_SECRETS` 为读取配置的私有文件，其中的端点引用仍用于账户绑定；已开通账号使用服务端加密存储的凭据。
4. 用隔离身份在两套库依次验证发现、申请、他人审批、原生账号开通、私有领取、激活和 `pg.get_item`。核对不同资源的结果、实际数据库身份、计划与审计关联，同步确认旧工具和受控写未挂载，其他主体持同一凭据会被拒绝。
5. 只有旧 demo 库和 v0.2 目标已隔离，才可将 `MCPGUARD_LEGACY_DEMO` 留空或设置为 `enabled`，同时保留旧工具供原场景使用。

## 回退演练

1. 停止供流及独立的 v0.2 签发、开通进程，排空或核对在途操作。对结果未知的操作保留审计并核实实际副作用。
2. 在当前二进制上设置 `MCPGUARD_RESOURCE_CATALOG=disabled` 并重启对应网关。该开关优先于原有子开关，在残留 v0.2 配置的情况下仍关闭资源目录、申请、凭据、私有交付及受保护工具。需要恢复 v0.1 服务时，只为已隔离的旧 demo 库启用旧接口。无法确认隔离时继续关闭旧业务工具。
3. 在新会话核对 `/api/v2/resources` 与私有签发入口返回 `404`，工具目录只恢复原来的兼容工具。验证老输入仍可读取原 demo 数据，`history.get_result` 仍可解密原有结果。再次核对 v0.2 版本账本、授权、吊销及凭据记录与回退前一致。
4. 配置回退保留所有新旧架构和恢复保护。若另行恢复旧元数据备份，维持停发、停执行状态，先按[恢复保护验收记录](validation/local-ra15-2026-11-16.md)核对业务库代次、吊销记录和未决副作用，再决定是否接受恢复点。

## 复演

从仓库根目录在 Linux/WSL 使用项目本地 Go 和 PostgreSQL 镜像。脚本为每次运行创建新的隔离夹具，备份和明细存放于该轮私有目录。

```bash
python3 -B scripts/migration_fixture.py
python3 -B scripts/migration_fixture.py --regression
```
