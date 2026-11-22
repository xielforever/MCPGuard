# MySQL App 隔离验收

2026-11-22（新加坡时间）。候选以 `dc44973f7b0980c5d0ae2313675c7965f61cfdda` 为基线，沿用 [Jenkins App](https://github.com/xielforever/MCPGuard/pull/31) 的控制台登记、测试、主体授权和启停流程。安装版 Chrome、DeepSeek Harness `0.2.2-rc.5`、原生 MySQL `8.4.11` 与隔离 PostgreSQL `16.14` 完成实测。DSH 使用 Streamable HTTP。

## 登记与获准读取

运行 `deeed8542860` 退出码为 `0`。网关在隔离元数据中应用迁移 007 并启动。Chrome 创建 `orders-mysql`，绑定单一数据库 `commerce_catalog`，只给一个测试 MCP 身份授权，测试通过后启用。获准身份通过 `connector.list` 发现此 App。

| MCP 工具 | 实际返回 |
| --- | --- |
| `connector.mysql.list_tables` | `commerce_catalog` 的基表 `orders`、`products`。 |
| `connector.mysql.list_columns` | `orders.id` 为 `bigint`、不可空、位置 1。`orders.order_status` 为 `varchar`、可空、位置 2。 |

两个结果都未截断。工具目录中 MySQL 只注册以上两个元数据工具。网关工件 SHA-256 为 `0ff125b499b4aaca30fc21e96b87d570bebc7cc29de71d0f5cc09671f360ceb6`。[脱敏验收 JSON](local-mysql-app-dsh-2026-11-22.json)

## 范围与授权负例

| 场景 | 实际结果 |
| --- | --- |
| 原生账号有更广权限 | 直接用夹具账号核对，可见 `finance_private` 和大小写不同的 `commerce_Catalog` 中的表与字段。DSH 只收到已登记库里的基表与字段，其他库的专有表与同名表的专有字段均不可读取。 |
| 改写目标 | 大小写别名、视图字段、跨库限定表名和额外 `database` 参数均被拒绝。 |
| 未获准主体 | 另一个 DSH 身份未发现此 App，直接使用其 ID 返回 `ACCESS_DENIED`。 |
| 错误凭据 | 连通测试失败，启用被拒。 |
| 停用 | 已连接的获准 DSH 会话再次调用返回 `ACCESS_DENIED`。审计读回 `CREATED`、`TEST_PASSED`、`ENABLED`、`DISABLED`。 |

首轮 `0187880da568` 在 Chrome 登记阶段发现后端白名单遗漏 MySQL。修复后由现有流程完整复跑，保留首轮诊断记录。脱敏报告只包含测试 App 标识、元数据、结果状态及审计类型。测试凭据留在隔离环境。

## 回归

本地 Go 定向测试覆盖 MySQL 配置规范化、非法表名和固定操作选择，并验证两种元数据操作沿用出站校验。隔离夹具 `ca30a688c1` 的 `go test -race -p 1 -count=1 -json ./...` 退出码为 `0`，347 项通过、0 项失败。22 项受控写、撤权和高危故障专项依夹具条件跳过。生成契约核对 5 项通过。

## 复跑

在 WSL 中使用 Docker、已安装的 Windows Chrome 和 DSH。Go MySQL Driver 固定为 `v1.10.2`。夹具使用固定镜像和本机离线 Go 缓存。[驱动版本](https://github.com/go-sql-driver/mysql/releases/tag/v1.10.2)

```bash
python3 -B scripts/mysql_app_dsh_fixture.py --report .dev/mysql-app-dsh.json
python3 -B scripts/read_fixture.py prepare
python3 -B scripts/read_fixture.py test
```
