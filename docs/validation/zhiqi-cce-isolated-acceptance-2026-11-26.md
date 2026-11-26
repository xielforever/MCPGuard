# 执契 CCE 隔离验收（2026-11-26）

> 后续状态：2026-11-26 已在同一实例上原地拆分控制台前端与 Go 网关，Helm revision 3。本文保留拆分前 revision 2 的单 Deployment 验收记录；当前拓扑、端口转发和健康结果见[控制台/网关拆分记录](zhiqi-cce-console-split-2026-11-26.md)。

## 结论

在华为云 xyc CCE 的 `zhiqi` 命名空间完成一套隔离验收实例。执契启动时从 Nacos 读取指定 PostgreSQL 元数据库配置，以单副本 `Recreate` 运行；管理员从 `/console/` 创建、测试、启用并授权 Jenkins App，开发者在已安装的 DSH Desktop 中以独立申请人身份操作同一 `connectorId`，完成两次受控构建、独立审批、申请人恢复和 Jenkins 队列/构建回执。权限、停用、参数漂移、重复提交、Pod 替换和缩容恢复均通过；回执丢失只完成部分验证，详见边界。

- 源码提交：`d1f18f723ff7b9a8467a9b4339cb1f05b4a9de21`（`codex/zhiqi-brand`，基于 `5f5db0fe1c9a0f0a1b16ea0ccbaeeb397d026edd`）。
- 镜像：`swr.cn-east-2.myhuaweicloud.com/dx_x2era/zhiqi:zhiqi-d1f18f7@sha256:2a7ea20b037f80d0572e8235c203d98ef264bc75062663767159c6f0f23be0a4`。
- 运行实例：Helm release `zhiqi` revision 2，`deployed`；Deployment `zhiqi` 1 副本、`Recreate`；Pod 镜像摘要与 values 完全一致。
- 结论：隔离环境验收通过；生产发布未执行，[#24](https://github.com/xielforever/MCPGuard/issues/24) 与 [#25](https://github.com/xielforever/MCPGuard/issues/25) 仍待批准/现场验收。

结构化脱敏证据见 [zhiqi-cce-isolated-acceptance-2026-11-26.json](zhiqi-cce-isolated-acceptance-2026-11-26.json)。

## 环境与发布身份

| 项目 | 实测值 |
| --- | --- |
| 集群 / 区域 / 命名空间 | xyc / cn-east-2 / `zhiqi` |
| Helm | `zhiqi` revision 2，单副本，`Recreate` |
| 镜像 | `.../dx_x2era/zhiqi@sha256:2a7e...3e0a4`，运行 `imageID` 与之一致 |
| Nacos | `http://infra-core-nacos.infra.svc.cluster.local:8848/nacos`；Namespace `zhiqi`、Group `ZHIQI`、Data ID `metadata-pg.json` |
| Nacos 身份 | `zhiqi-reader`，仅该 Namespace/Group 的配置读权限；启动日志 `metadata connection loaded from Nacos` |
| PostgreSQL | 服务 `infra-core-postgres`，库 `zhiqi`；`zhiqi_migrator` / `zhiqi_runtime` 均非超级用户、不可建库、不可建角色；`sslmode=verify-ca` + 实例 CA |
| 迁移账本 | `mcpguard.schema_versions` 共 7 条，最新版本 7 |
| 探针 | 启动/存活 `mcpguard probe live`；就绪 `mcpguard probe ready` |

发布前实际读取了 kubeconfig 发布身份、SWR 仓库、Nacos 配置标识、执契专库与 Jenkins 测试范围。运行 Pod 为 `zhiqi-7bdfb459cf-hgznm`，`2/2` Ready（`zhiqi` + `jenkins-proxy`），应用监听 `127.0.0.1:18082`，Jenkins 回环代理监听 `127.0.0.1:18083`。

### 迁移与错误配置拦截

- 预检 Job `zhiqi-migrate-plan-plan20261126b` 完成，输出 `{"schema":"mcpguard","currentVersion":7,"pendingVersions":[]}`。
- 应用 Job `zhiqi-migrate-apply-apply20261126b` 完成，日志 `metadata migrations applied/verified`；账本读回 `7|7`。
- 迁移前备份：`zhiqi-pre-migration.dump`，1134 字节，SHA-256 `f29b1d859baf284236ae5851e364fe0b52894e0b1c08383573c5bd85195f37a3`；保留在本地忽略目录。
- 使用缺失 Data ID 的独立 Job 验证失败即停：Job `zhiqi-bad-config` 以非零退出，日志仅 `ERROR Nacos metadata configuration could not be read`，未泄露 Nacos 登录或数据库密码。

## 页面到 DSH

管理员页面为 `http://127.0.0.1:18082/console/`，HTTP 200；[管理员控制台截图](zhiqi-cce-2026-11-26-admin-console.png) 可见「执契」品牌、Jenkins App、测试通过/已启用状态及授权用户。

- Jenkins App：`26d53122-f733-4fd5-9c6a-9bb638d649c5`，名称「执契 Jenkins 验收」，revision 5，`PASSED`，`enabled=true`。
- Jenkins 服务为 `infra-jenkins`；`cloudbees-folder` 插件不存在，因此批准的测试范围是 Jenkins 根目录 `/`。
- 两个任务：`zhiqi-orders`（`GIT_REF`、`RUN_TESTS`）与 `zhiqi-inventory`（`PROFILE`、`RELEASE_TAG`）。
- DSH Desktop 实际客户端版本 `0.2.2-rc.5`，发现 17 个工具；MCP 端点 `http://127.0.0.1:18082/mcp`，申请人 token 对应 principal `7a000000-0000-4000-8000-000000000103`。

| 场景 | actionId | Jenkins queue | 构建号 | 参数 | 结果 |
| --- | --- | ---: | ---: | --- | --- |
| 订单受控构建 | `0e43ba6e-d9b7-4bb2-9222-95aaf4a78d6e` | 6 | 1 | `GIT_REF=release/orders`、`RUN_TESTS=false` | `SUCCEEDED` / `DISPATCHED` |
| 库存受控构建 | `ff944d12-85dc-4235-b14a-1c07695b4fb8` | 7 | 1 | `PROFILE=full`、`RELEASE_TAG=release-2026` | `SUCCEEDED` / `DISPATCHED` |
| 幂等重复提交 | `8c70c42e-65e7-4939-92be-4eafb039c959` | 8 | 2 | `GIT_REF=release/idempotent`、`RUN_TESTS=true` | `SUCCEEDED` / `DISPATCHED` |

待审批期间两个动作均为 `PENDING_APPROVAL` / `NOT_DISPATCHED`，Jenkins 构建数不变；由独立 reviewer 批准后为 `READY` / `NOT_DISPATCHED`；申请人用原 `actionId` 显式 `action.resume` 后才出现 queue 6/7/8 和构建 1/1/2。审计能对上提交、审批、派发和实际构建。

### 权限、停用与恢复

- 申请人自批：HTTP 403；该动作后续被独立 reviewer 拒绝（`DENIED`），未派发。
- 无关身份 outsider：`connector.jenkins.list_jobs` 返回 `ACCESS_DENIED`。
- 参数定义漂移：批准后的动作 `bcf58979-a291-4d09-ba88-6f387f6a3f98` 在 Jenkins 选择项变化后以 `FAILED` / `NOT_DISPATCHED` 结束；库存构建数保持 1，配置随后恢复。
- 停用/启用：应用停用后下一次 `list_jobs` 被拒；重新启用使 revision 从 4 推进到 5，旧审批动作 `cb95b27d-a49f-4cef-8230-9d085824230c` 以 `CANCELLED` / `NOT_DISPATCHED` 结束，未触发新构建。
- 重复提交/重复恢复：相同 `requestKey` 与参数返回原 `actionId`；参数变化返回 `CONFLICT`（`STATE_CONFLICT`）；完成后再次提交/恢复不产生第二次构建。
- Pod 替换：删除 Pod 后新 Pod `zhiqi-7bdfb459cf-zmtc9` 2/2 Ready；重新建立 `kubectl port-forward` 后原 DSH 配置重连，App、任务、动作历史与审计均可读。
- 缩容恢复：Deployment 缩到 0 后再恢复为 1，新 Pod `zhiqi-7bdfb459cf-hgznm` 2/2 Ready；元数据账本仍为 7，审计和迁移前备份均保留；DSH 以原配置重连并读回 App/动作/历史。

评审页面截图见 [reviewer 审批队列](zhiqi-cce-2026-11-26-reviewer-actions.png)；截图中的待审批动作在截图后由 reviewer 拒绝，未派发。

### 审计摘要

- 受控动作：`ACTION_CREATED`、`APPROVAL_APPROVE`、`REVIEW_APPROVE`、`EXECUTION_INTENT`、`SUCCEEDED`；执行记录 `SUCCEEDED` / `DISPATCHED`。
- 参数漂移：`EXECUTION_INTENT` 后为 `FAILED` / `NOT_DISPATCHED`，库存构建数不变。
- 停用：`PLAN_INVALIDATED`，动作 `CANCELLED` / `NOT_DISPATCHED`。
- 连接器审计：`CREATED`(1)、`TEST_PASSED`(2)、`ENABLED`(3)、`DISABLED`(4)、`ENABLED`(5)。

## 回退与保留

- 首次发布失败处置：独立坏配置 Job 非零退出并保留日志，未改动常驻 Deployment；随后验证了工作负载缩容到 0 的停用路径。
- 工作负载停用：`kubectl scale deployment/zhiqi --replicas=0` 后无 Ready 工作负载；元数据库账本、审计和迁移前备份均保留；恢复为 1 后 DSH 重连成功。
- 镜像回退：本次唯一的镜像变更（`/console/` 路由修复）架构兼容；上一镜像摘要 `sha256:402c130844b47c29ac78111d5e41d6db616b838d9a9ff7d007bbc57ab18efee1` 已记录，可按 Helm revision 回退。该回退未在本轮执行。
- 数据库恢复：迁移前备份已保留；未在隔离库执行恢复演练，因此不在本轮宣称数据库恢复通过。需要恢复时先在隔离库核对账号、授权和审计，再决定切换。
- 正式发布仍走 Wave 3 [#24](https://github.com/xielforever/MCPGuard/issues/24) 与 [#25](https://github.com/xielforever/MCPGuard/issues/25)；[#21](https://github.com/xielforever/MCPGuard/issues/21) 保持 Review。

## 边界与未完成项

1. Jenkins 未安装 `cloudbees-folder` 插件，测试范围是根目录而非文件夹；不影响两个任务和参数定义的验收。
2. Jenkins 在集群内是 HTTP；连接器只接受 HTTPS 或回环 HTTP，因此使用 Pod 内 `127.0.0.1:18083` 的 TCP 代理。该代理无 Service、不可从 Pod 外访问，仅用于隔离验收，不是生产入口。
3. 回执丢失只完成部分验证：三次客户端在读取响应前中止，服务端未在重试前派发，重试各产生恰好一次构建；未复现“已派发但响应丢失”的注入窗口。重复提交/恢复路径已独立验证无第二次构建。
4. 执行期间共享 `infra` 发布从 `infra-test` 替换为 `infra-core`；`zhiqi` 元数据库与 Nacos 数据保留，Nacos host、TLS 和服务名已更新并复验。最终证据只对应替换后的 `infra-core`。
5. 本记录是隔离验收，不是 Wave 3 生产批准；没有生产流量、真实企业 SSO 或正式 Jenkins HTTPS 入口。

## 证据文件

- [结构化证据 JSON](zhiqi-cce-isolated-acceptance-2026-11-26.json)
- [管理员控制台截图](zhiqi-cce-2026-11-26-admin-console.png)
- [reviewer 审批队列截图](zhiqi-cce-2026-11-26-reviewer-actions.png)
- 本地忽略目录中的迁移前备份、迁移 Job 日志、坏配置日志、DSH 运行日志和完整状态；这些文件不进入提交。

凭据只保存在本机 Git 忽略的 `.dev/zhiqi-cce-xyc/work/state.json` 及 Kubernetes Secret 中；本记录和提交不包含密码、token 或 Nacos 配置正文。
