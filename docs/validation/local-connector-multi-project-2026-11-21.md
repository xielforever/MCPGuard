# 连接器运行模块与双项目本地验收

2026-11-21（新加坡时间），在 [W4-RA01（#29）](https://github.com/xielforever/MCPGuard/issues/29) 工作树提取 `pkg/connectors`。MCPGuard 在同一进程调用该包，继续管理配置、凭据、身份授权、审计和 MCP 工具。

## 运行模块

使用另一个有独立 `go.mod` 的 Go 消费模块，在 Go 1.26.9 离线容器内导入 `github.com/xielforever/MCPGuard/pkg/connectors`。消费者取得限定命名空间的类型化 Deployment 结果。验证执行定义序列化省略凭据，调用未支持的操作被拒绝。

## 两个真实项目

运行 `2c543f39809a` 从两个实际 Git 工作目录启动已安装的 DeepSeek Harness `0.2.2-rc.3`。两个客户端进程使用各自的隔离配置和测试身份，经 `mcpguard-connect` stdio 调用同一个 Streamable HTTP 网关。网关连接真实的隔离 PostgreSQL 16.14，验收数据在专用测试 Schema 中。验收后再次读取两个项目的 Git HEAD，与启动前记录一致。

| 项目 | 授权范围 | 停用共享连接器后 |
| --- | --- | --- |
| DBHub，`ebc75d7c` | `connector.pg.list_tables` 列出共享 `shared_delivery` 中的 `jobs` 表和私有 `dbhub_private` 中的 `inventory` 表。另一方的私有连接器不出现在目录，直接指定其 ID 返回 `ACCESS_DENIED`。 | 同一个客户端的首个共享调用返回 `ACCESS_DENIED`，目录移除共享连接器，原私有目录读取仍成功。 |
| dsh-project-control，`0707b0aa` | `connector.pg.list_tables` 列出共享 `shared_delivery` 中的 `jobs` 表和私有 `dsh_project_control_private` 中的 `pipelines` 表。另一方的私有连接器不出现在目录，直接指定其 ID 返回 `ACCESS_DENIED`。 | 同一个客户端的首个共享调用返回 `ACCESS_DENIED`，目录移除共享连接器，原私有目录读取仍成功。 |

两次跨项目私有调用及两次停用后调用均未产生 PostgreSQL 连接。六次授权读取各产生一次实际后端连接。停用在网关审计中有独立的 `DISABLED` 事件。两项目一直连接同一网关，停用后沿用各自已建立的 DSH 客户端。

[脱敏完整记录](local-connector-multi-project-2026-11-21.json) · [复验脚本](../../scripts/connectors_multi_project_fixture.py)

## 兼容回归

模块抽取后的运行 `9c762d8cf5e7` 在真实 Chrome 中完成 PostgreSQL、Redis、Jenkins 与 K3s 连接器的新增、测试和启用，再经 MCP 核对四种读取、授权隔离、错误凭据与停用。测试进程退出码为 0。[四后端复验脱敏记录](local-connector-module-four-backends-2026-11-21.json)

模块、登记、Jenkins 和网关的定向 race 回归通过。全包首轮 `47a93b79f3` 中，连接器相关用例均通过，既有 `TestCredentialContextsIntegration/concurrent_package_renewal_one_successor_no_account` 触发并发续期汇总断言。只读对账确认存储记录为一笔续期、两个凭据包、一个原生账号，符合期望计数。六路续期中至少一次调用返回了错误。该用例的执行时间窗内，元数据 PostgreSQL 日志记录了一次锁等待超时。原始日志已保留。复跑沿用相同代码和一套新的隔离 PostgreSQL。

复跑 `c77038f0e8` 执行 `go test -race -p 1 -count=1 -json ./...`，退出码为 0。335 项通过、0 项失败、22 项按夹具条件跳过。两轮日志分别保留。

## PR 合并前复核

[PR #28](https://github.com/xielforever/MCPGuard/pull/28) 已合入 `main`，合并提交为 `5ec803645996a1f1f7312f704b98d87502003705`。已确认合并树与此前验收的 Jenkins 提交内容完全一致。#29 的变更以该合并提交为基线。

复核发现 Redis 测试键接受的长度大于 MCP 读取上限。两条路径已共用 1024 字节上限。隔离运行 `a864917ff779` 退出码为 0，真实 Redis 的 1024 字节键通过连通测试并由 MCP 读回，1025 字节请求被拒绝。四后端复验脚本已加入这个边界，原有验收记录保留。
