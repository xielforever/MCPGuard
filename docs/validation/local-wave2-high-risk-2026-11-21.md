# Wave 2 高危操作隔离验收

本轮按 Wave 2 Must 草案在本地实现高危操作与渠道通知。原 [RA-18 #21](https://github.com/xielforever/MCPGuard/issues/21) 按既有范围保持 Review。

同一候选 MCPGuard 工件通过 HTTP 直连和 stdio 的三项正例，共六条动作。DeepSeek Harness 版本为 `0.2.2-rc.3`，捆绑的 MCP SDK 为 `2.2.0`。两份最终报告的十项共享工件 SHA-256 均一致。

## 候选行为

| 操作 | 待审计划 | 通过条件 |
| --- | --- | --- |
| `UPDATE_ORDER_AMOUNT` | `tenant_a/order_recalc_1`，版本 1，金额从 100 调至 120 | 影响 1 行，版本升至 2，其余行保持原值 |
| `CREATE_ORDERS_INDEX` | 在测试订单的 `shipping_code` 上新建普通索引 | PostgreSQL 目录中的索引定义、归属与有效状态相符，业务行保持原值 |
| `DELETE_CANCELLED_ORDER` | `tenant_a/order_cancelled_1`，状态 `CANCELLED`，版本 1 | 删除指定的 1 行，同主键的其他租户记录与其余行保持原值 |

高危工具通过 `MCPGUARD_HIGH_RISK_WRITES=enabled` 启用，要求本地回环、开发身份、控制台、指定资源与指定的独立审批人。两条入口在独立夹具中接同一候选网关，HTTP 使用企业微信本地接收器，stdio 使用飞书本地接收器。数据库执行账号只能通过夹具的三个固定函数操作测试订单。

审批人收到待办回执后可批准。授权通知携带动作编号、到期时间和登录后取码路径。申请人从受信任的 DeepSeek Harness 本地输入提交一次性授权码，用服务端时间校验有效期。运行时默认有效期为 5 分钟，可配置。复跑脚本用 180 秒，将自然到期用例和其他操作并行安排，以覆盖仍有效的跨动作提交和同码并发。旧 `action.resume` 的模型参数仍仅有 `actionId`，授权码通过已安装 Harness 所带的 MCP 客户端私下提交。网关将授权码消费和唯一派发意图提交到同一事务后才调用 PostgreSQL。结果未知时保留原动作与派发记录。

## 复跑

在 WSL 的仓库目录使用各入口独立夹具。脚本通过安装版 Harness 的原生 MCP 工具执行器提交计划，通过其中打包的 MCP 客户端复现本地私密提交。脚本使用合成账号、浏览器会话 API 和私密测试管道。

```bash
python3 -B scripts/wave2_high_risk_fixture.py http --report .dev/wave2-high-risk-http.json
python3 -B scripts/wave2_high_risk_fixture.py stdio --reuse-build --report .dev/wave2-high-risk-stdio.json
python3 -B scripts/migration_fixture.py
python3 -B scripts/migration_fixture.py --regression
```

## 验收证据

| 运行 | 结论 |
| --- | --- |
| HTTP 基础轮，夹具 `b0bdf67540` | 已保存安装版 Harness 的三条成功回执，112 项断言通过 |
| stdio 基础轮，夹具 `b0ae10ea3a` | 客户端通过了三项原生操作断言。末尾读取 Windows 日志时因混合编码退出。独立读回确认三项原生结果、各一次授权码消费与派发，以及两个通知回执 |
| PostgreSQL 16.14 独立权限与原生效果 | 三种操作均通过。错误目标和版本、无授权账号、直接 DDL 和 DELETE 均被拒绝 |
| 企业微信和飞书占位接收器 | 两种路径都复核了首次 HTTP 503 与原通知编号重试确认 |
| v14 增量迁移、回退与只读演练 | 夹具 `a6f4327b77`。迁移和回退 22 项、旧只读路径 116 项通过 |
| Go 竞态回归 | 常规隔离回归 309 项，受控写专项 10 项，撤权专项 10 项，合计 329 项。最终 Go 源码的离线全包竞态回归退出码为 0 |
| 同工件 50 秒补验 | HTTP `39c5f9f40b` 与 stdio `d3a3a7ae63` 均从网关读回待审批、自然到期、缺码、错码、跨动作及其他申请人提交的拒绝审计。多次启动 Windows 私密提交进程耗尽窗口，抵达网关的并发提交已过期。两套夹具均零派发，订单和索引保持初始状态 |
| HTTP 180 秒复跑，夹具 `5271fe0cba` | 退出码 0。三项原生操作和 145 项断言通过。并发提交返回一条成功和一条拒绝。待审批、自然到期、跨动作、错主体及重放均有零新增派发证据 |
| stdio 180 秒首次复跑，夹具 `6f599861ec` | 一笔有效授权码抵达网关，金额修改成功且仅派发一次。另一笔的 Windows 连接器停在传输启动阶段，流程在建索引和删除之前退出 |
| stdio 同会话补验，夹具 `79db11b152` | 退出码 0。三项原生操作和 118 项断言通过。一个已连接的 DSH SDK 会话发送两笔并发请求，一条成功、一条拒绝。原生变更与审计只出现一次 |

stdio 的自然到期使用同一网关工件在独立夹具 `d3a3a7ae63` 实测，动作 `19adea6c-6058-435b-8572-83412c0da22d` 记录了 `CODE_EXPIRED`，派发意图为零。成功轮与诊断轮均保留，网关、客户端及 PostgreSQL 的对应数据收录于 [脱敏证据](local-wave2-high-risk-2026-11-21.json)。

本地补验通过已安装的 Harness 宿主和私密输入。包括 DSH 图形界面内输入在内的人工体验、正式渠道接入及生产部署均有独立的验收范围。

## 故障专项补验

两项专项通过现有受控写服务、真实 PostgreSQL 16.14 和隔离 RA-12 账号执行。故障仅作用于临时夹具。初次专项使用夹具 `ac8ee792ab`，两个 Go 竞态测试退出码均为 0。

| 场景 | 注入位置 | 实际结果 |
| --- | --- | --- |
| 关键审计写入失败 | 仅拒绝当前动作的 `DISPATCH_INTENT` 入库，独立计数确认故障触发一次 | 动作保持 `APPROVED`，授权码未消费，派发意图和原生连接池均为零，订单快照保持原值。恢复审计后，恢复扫描仍为零派发。申请人显式再次提交后，更新一行，金额为 `120`、版本为 `2` |
| PostgreSQL 提交结果未知 | 服务端确认 `COMMIT` 后，测试连接在 pgx 收到回执前断开 | 网关持久化 `OUTCOME_UNKNOWN`。独立 PostgreSQL 连接读回目标金额 `120`、版本 `2`，另一租户仍为 `100`、版本 `1`。重建服务、两次恢复扫描、查询状态和显式重交同一码后，原生 SQL、COMMIT、授权码消费和派发意图的计数仍各为一。重交审计记录为 `ALREADY_CONSUMED` |

在 WSL 的候选工作区可通过现有隔离夹具复跑。

```bash
python3 -B scripts/write_fixture.py prepare
MCPGUARD_TEST_RUN='TestHighRiskAuditWriteFailure|TestHighRiskFaultCommitOutcomeUnknownNeverRetries' \
  python3 -B scripts/read_fixture.py test
```

专项的动作编号、计数及原生读回值保存在 [脱敏证据](local-wave2-high-risk-2026-11-21.json) 的 `faultAcceptance` 中。

基于 `3ba48b0` 的独立 PR 候选在夹具 `469cd90ed6` 复跑受控写与两项专项，共 12 项通过。常规竞态夹具 `7a3e556fc4` 通过 309 项，撤权夹具 `d26f9885ef` 通过 10 项。按包和测试名核对三组结果，共 **331 项**通过，常规组跳过的 22 项均在专项组执行通过。迁移夹具 `cb4d452aec` 通过迁移与回退 22 项、受保护只读 116 项，5 份生成契约检查通过。WSL 原生测试快照与 PR 工作树的 329 份源文件逐字节一致。
