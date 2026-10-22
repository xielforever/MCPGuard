# 最小可运行读取闭环

日期：2026-10-22。当前实现是集中网关的第一条纵向链路：可信测试员工 → MCP 工具目录 → 权限与参数校验 → 持久资格 → 原生 PostgreSQL 读取 → 加密结果与持久审计。两种接入进入同一个网关，不连接下游 MCP Server。

## 启动和复现

需要 Go 工具链（模块最低 1.25，toolchain 固定 1.26.8）、Python 3、Docker 本地 daemon。首次运行需要下载 Go 依赖与 PostgreSQL 镜像；不需要大模型 key、企业 IdP、IM、主机 psql、Node 或 Redis。`GO=/absolute/path/to/go` 可选择实际 Go 编译器。Go 全仓库 race 检查还需要 C 编译器，用于原有 SQLite 包和 race runtime。

```bash
make dev-up
make dev-test
make dev-run
```

`dev-up` 在本机创建带项目标签的隔离 PostgreSQL 16.14 容器，镜像按 digest 固定；数据库端口随机映射到 `127.0.0.1`。创建独立元数据库和业务库、不同运行账号、假数据和三名测试员工，编译两个二进制，应用并重复核对 migrations 1–2。重复运行保留数据和加密密钥，将测试身份有效期刷新为 24 小时。

网关默认监听 `127.0.0.1:8080`，`GET /healthz` 表示进程存活；数据库失效时业务调用仍须通过持久资格事务，不能因 healthz 成功绕过校验。另一个终端可使用本地 stdio 接入：

```bash
python3 scripts/dev.py connect --employee alice
```

该进程的 stdin/stdout 专用于 MCP；使用 MCP 客户端启动它。可选测试员工为 `alice`、`bob`、`visitor`。例如支持 stdio 的客户端配置：

```json
{
  "mcpServers": {
    "mcpguard": {
      "command": "python3",
      "args": ["/absolute/path/to/MCPGuard/scripts/dev.py", "connect", "--employee", "alice"]
    }
  }
}
```

测试配置和随机秘密保存在 `.dev/env.json`，目录 0700、文件 0600，已被 Git 和 Docker build context 排除。启动脚本只把运行必需的两条 DSN 和两把密钥交给网关；迁移管理员密码与员工明文 token 不进入网关环境。连接器只取得当前员工 token，不需要原生服务凭据。不要把该文件提交、发布或复制到共享客户端配置中。

停止网关用 Ctrl-C；`make dev-down` 删除脚本创建、带正确项目标签的 PostgreSQL 容器、其匿名测试卷及 `.dev`。这是清除本地测试数据，先停止仍使用这些数据的网关。该命令不会操作其他名称或标签的容器。

## 当前工具和身份

| 测试员工 | 当前读取范围 | 工具目录 |
|---|---|---|
| Alice | scope_a | pg.get_demo_item |
| Bob | scope_b | pg.get_demo_item |
| Visitor | 无工具调用权 | 空 |

工具只接受 `{"itemId":"item_demo_1"}`。同一个 ID 在 A、B 范围返回不同假数据；Alice 读取 B 独有 ID 得到未找到。额外 scope、SQL、资源地址、身份字段及重复 JSON 键全部拒绝。版本值按十进制字符串返回，包含超过 JavaScript 安全整数范围的测试值。

固定 SQL 为 `SELECT item_id,note,version::text FROM demo.items WHERE scope_id=$1 AND item_id=$2 LIMIT 1`。scope 来自领取事务重新查询的可信员工记录，两个参数使用原生绑定。业务账号只有目标表 SELECT，连接默认只读，执行再使用 READ ONLY 事务；最多一行、note 最多 500 字符、结果最多 16 KiB、原生路径预算 3 秒、池最大 4 连接。元数据池最大 8 连接、业务处理最多并发 16。所有值均为测试基线，不能当作已核准的企业容量配置。

普通授权读取的 `authorization_source=POLICY`，不创建虚假人类批准。目录每次按权限过滤，直接调用再次鉴权；本地员工变更通过数据库触发器推进 auth_epoch，领取持有授权头共享锁，撤权与领取具有明确的提交边界。已完成领取的在途读取按原有三秒预算结束；不承诺外部身份源尚未同步的撤权。

## 持久状态和审计

运行迁移是 [001_read_slice.sql](../internal/metadata/migrations/001_read_slice.sql) 和 [002_authorization_lock.sql](../internal/metadata/migrations/002_authorization_lock.sql)，通过 `mcpguard migrate` 显式执行，管理员凭据只用于迁移。它是读取闭环的六张业务表与版本表，不是直接执行完整设计模型 [metadata-model.sql](specs/metadata-model.sql)。002 用固定 search_path 的受限 SECURITY DEFINER 函数取得授权头共享锁，运行账号不获得授权表 UPDATE 权限。尚未引入写入决策、完整版本目录、Outbox、管理命令幂等与通用受保护 payload 生命周期。

Action 固定完整计划；一个 MCP 业务请求建立独立 Attempt；`UNIQUE(org_id,action_id)` 永久限制一个 Execution。Action、Attempt、Execution 意图和审计在同一个短事务提交；提交失败或结果不明都不进入原生适配器。数据库触发器禁止计划、审计、终态和资格删除/改写，并在提交时检查三者状态一致。

计划通过 HMAC-SHA256 摘要，计划与结果通过 AES-256-GCM 加密，AAD 绑定组织、Action、用途及密钥版本。当前计划只包含固定 ASCII 字符串和整数文本，排序 JSON 编码符合所用 RFC8785 子集；没有将普通 Go JSON 编码器宣称为通用 JCS 实现。密钥用途分离，版本暂固定 `local-v1`；尚未实现企业 KMS、轮换与留存清理。保留 `.dev` 密钥才能读取既有密文。

原生返回证明 SELECT 已派发后才保存 `DISPATCHED`。进入驱动后发生没有服务端回复的连接故障，记录 `DISPATCH_UNKNOWN/OUTCOME_UNKNOWN`；只有只读操作，所以副作用始终 `NONE`。失败和取消不返回业务数据，也不返回 SQL、DSN、原生错误详情或凭据。非成功结果的 `data=null`，MCP `isError=true`，文本内容与 structuredContent 保持相同事实。

显式 MCP 取消传递给 PG CancelRequest；独立的有界清理负责回滚、归还连接和完成审计，不使用已经取消的请求上下文写终态。3 秒原生期限到达时同样取消。HTTP 临时断流不会被额外解释为 2025 协议的显式取消。进程丢失、结果未持久化的读取资格超过原生期限 15 秒后由恢复器标记未知，保留原资格，不重发原生操作。当前恢复逻辑仅适用于这条只读链路，不作为写操作恢复或多实例 fencing 实现。

`GET /api/v1/audit?after=0&limit=25` 使用与 MCP 相同的员工 Bearer token，只查询本人事件，最多 100 条并返回 `nextAfter`。接口拒绝调用方指定组织/员工；事件包含事件 ID、Action/Attempt/Execution 关联和时间，不含原始参数、结果或 token。业务接受前的权限/参数拒绝只记安全事件，三个业务 ID 均为空。未认证请求没有可信员工，不伪造业务对象。尚无管理者全组织审计 API 或新控制台。

## 入口和配置边界

官方 Go MCP SDK v1.8.0 处理 initialize/initialized、Streamable HTTP、stdio 并发和取消，PG 使用 pgx v5.11.0。当前发布的测试 profile 只有 `2025-11-25`；不声明 2026 MRTR、Tasks、sampling、elicitation、resources、prompts、动态目录通知或 OAuth 能力。连接器使用相同 profile，逐次转发目录和调用，不缓存权限；关闭自动恢复重试，不自动重放工具调用。

SDK v1.8.0 的 JSON-RPC 解码会先把数字 ID 转成 float64。独立客户端测试发现精度问题后，增加 [wireids](../internal/wireids/ids.go) 边界适配：外部字符串/整数 ID 编码为 SDK 内部字符串，响应再还原，取消目标使用相同编码；数字与同文本字符串不会混同，不修改工具参数或业务 ID。支持超过 int64 的整数，ID 规范化后最多 1024 字节，拒绝非整数、过量指数展开和重复 envelope 字段；HTTP 与 stdio 都做独立往返验证。该适配不替换 SDK 的协议握手、会话和并发逻辑。

| 环境变量 | 用途 |
|---|---|
| MCPGUARD_AUTH_MODE | 必须显式为 dev-tokens |
| MCPGUARD_LISTEN | 默认 127.0.0.1:8080；只接受 loopback IP |
| MCPGUARD_METADATA_DSN | 非管理员元数据账号 |
| MCPGUARD_BUSINESS_DSN | 原生 PG SELECT-only 账号 |
| MCPGUARD_ENCRYPTION_KEY | 独立随机 32 字节的 Base64 |
| MCPGUARD_PLAN_KEY | 另一把独立随机 32 字节的 Base64 |
| MCPGUARD_MIGRATION_DSN | 仅 migrate 子命令使用 |
| MCPGUARD_TOKEN | 仅连接器使用的员工测试 token |

原 configs/mcpguard.yaml、internal/proxy、确认码、Gin 管理接口和 Vue 页面仍是旧脚手架，新入口不挂载它们。原配置缺失不会触发匿名或旧代理回退。旧包暂保留在仓库，后续单独清理。

连接器独立使用时：`mcpguard-connect -gateway https://gateway.example/mcp`；token 通过环境提供。测试 HTTP 例外须显式 `-allow-loopback-http`，且主机必须是 loopback IP。禁止 URL 用户信息、查询参数和重定向，stdout 只输出协议帧。浏览器 Origin、HTTP 64 KiB 请求上限、会话员工绑定及逐请求认证均在网关处理。

## 验证和剩余工作

`make dev-test` 使用真实 PG 跑 Go race 测试，再启动实际网关和连接器二进制，由独立 Python 客户端进行 HTTP/stdio 协议测试。Go 数据库测试没有显式 fixture 凭据时会跳过，单独 `make test` 成功不能代替 `make dev-test`。

测试包含两员工同 RPC ID、两个范围的实际查询、目录过滤和直接拒绝、额外/重复参数拒绝、现有会话撤权和范围变更、原生角色禁止 UPDATE、原子资格与不可变约束、关键审计失败时不访问原生库、真实锁等待的取消与期限、池连接清理、stdio 单请求取消隔离、审计权限/分页及网关重启。详细已执行证据见 [验证记录](validation/read-slice-2026-10-22.md)。

企业 OAuth/SSO、ChatGPT 账号与工作区接入、HTTPS 部署、受控写与他人审批、Redis/API 原生适配和完整控制台仍待实现/验收。当前本地测试成功不能作为这些能力已交付的证据。
