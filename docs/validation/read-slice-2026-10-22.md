# 最小读取闭环验证记录

日期：2026-10-22。目标：初始提交 `ad0a685cf39e48cf4abe72a6d8b6f0a8f0e89bac` 之上的读取增量。下列验证在提交前的工作区完成，源码与构建产物哈希见证据清单；未部署企业服务。运行方式见 [读取闭环](../read-slice.md)。

## 环境与执行结果

使用 Linux amd64 沙箱、Go 1.26.8、官方 Go MCP SDK 1.8.0、pgx 5.11.0、PostgreSQL 16.14。PG 镜像固定为 `postgres:16.14-alpine@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777`。独立员工、两个数据库、原生只读角色与随机本地秘密均由测试脚本创建，没有连接企业生产资源，没有调用模型。

| 检查 | 已验证结果 |
|---|---|
| Go 全仓库 race 测试 | `go test -race -p 2 -count=1 ./...` 通过；真实 PG 测试有显式 fixture 凭据，未跳过 |
| 真实 PG 集成 | 10 个场景通过，见下表 |
| 独立客户端 | Python 标准库客户端对实际两个二进制执行 48 项检查，HTTP 与 stdio 均通过 |
| 设计契约校验 | 5 个 Schema、48 正例与 64 反例断言、6 工具定义、16 OpenAPI 操作、231 引用通过 |
| 运行迁移 | migrations 1–2 在真实 PG 应用，重复执行核对 checksum 通过 |
| 全新启动复现 | 删除本轮自建测试容器及私有测试数据后，重新 dev-up 与 dev-test 全部通过 |
| 容器镜像 | Docker build 通过；distroless 非 root 用户 65532 运行真实两范围读取及无权目录检查通过（Linux host 网络，仅用于本地夹具） |
| 文档链接 | 本地 Markdown 链接检查通过，无失效目标 |

Go 真实集成场景：

| 场景 | 证据与结果 |
|---|---|
| 两员工、两个 scope | 同 item ID 返回各自数据；A 读 B 独有记录为未找到；大版本号保持字符串精度 |
| 无权目录和直接调用 | Visitor 目录为空；猜测调用拒绝；scope、SQL、重复参数拒绝，业务 ID 不虚构 |
| 现有会话权限变更 | 撤权后重新列目录与调用均拒绝；范围改变后不使用旧 scope |
| 原生角色最小权限 | 关闭 session 默认只读后 UPDATE 仍由 ACL 拒绝；启动拒绝管理员角色 |
| 资格与不变量 | 6 个并发重复 Execution INSERT 均被唯一约束拒绝；计划、终态、审计和永久资格受到保护；提交时检查状态一致 |
| 审计失败关闭派发 | 撤销审计 INSERT 后领取回滚；Action 数量及原生池获取次数不变 |
| 显式取消 | 用另一 PG 连接持表锁制造实际等待；MCP 取消取得 PG 确认，最终 CANCELLED/DISPATCHED/NONE；终态无业务数据 |
| 清理与超时 | 无遗留锁等待或 idle-in-transaction，连接回到池并可重用；原生预算到期终止 |
| 审计权限与分页 | 仅本人事件；拒绝 principalId 参数；关联 ID、分页与安全字段正确 |
| 崩溃遗留状态恢复 | 注入历史 RUNNING 夹具，由恢复器标记未知；不访问原生池，不创建第二个资格。这是状态恢复测试，不是实际 kill 的完整故障矩阵 |

独立 Python 验证握手、目录、查询与文本/结构化结果一致，跨员工同大整数 RPC ID、数字/字符串 ID 区分、超过 int64 的 ID、无效令牌、会话跨员工复用拒绝、请求大小和 Origin 限制。stdio 同时阻塞两个真实 PG 查询，取消其中一个后另一个仍可完成，等待期间目录请求正常；stdout 仅含 JSON-RPC 帧，退出和输出检查不暴露 token。网关正常停止、重新启动后，原有审计事件 ID、关联和内容保持一致。

SDK 数字 ID 精度问题由独立客户端发现，已在 [wireids](../../internal/wireids/ids.go) 修复适配并通过两种入口验证。底层仍使用官方 SDK；没有修改下载依赖或将 RPC ID 用作全局业务幂等键。

## 证据范围

复现命令为 `make dev-up && make dev-test`。Python 机器结果写入本地 `.dev/e2e-results.json`；其中只有检查名称、版本与结果，不包含令牌、DSN 或业务结果。测试账号、密钥和私有环境配置不放入本文或版本库。

本沙箱的 `/usr/bin/go` 不是 Go 编译器，本次实际执行通过环境变量选择 `GO=/tmp/mcpguard-review/toolchain/go/bin/go`，并使用 `GOTOOLCHAIN=go1.26.8`、独立临时 GOMODCACHE/GOCACHE。其他环境使用正常安装的 Go 即可；本沙箱重跑构建时也须选择实际编译器。已有二进制可直接 `make dev-run`。

已归档 [48 项独立检查结果](read-slice-e2e-2026-10-22.json)、[镜像运行结果](read-slice-image-2026-10-22.json)及 [源文件清单与构建产物 SHA256](read-slice-artifacts-2026-10-22.json)。镜像是本地 `mcpguard:read-slice`，没有推送镜像仓库。

设计验证器的 `databaseExecuted=false` 与 `serviceIntegrationRun=false` 仍如实表示它自身仅检查设计文件；运行 SQL 和服务联调由上面的独立测试执行。真实运行的是读取增量迁移，完整 `docs/specs/metadata-model.sql` 仍只通过语法检查。

当前不验证真实 ChatGPT/其他 IDE、企业 OAuth/SSO、TLS 企业部署、受控写及他人审批、Redis/API、企业通知、完整元数据模型迁移、KMS/轮换/留存、多实例与灾难恢复。当前通过不能替代这些验收。
