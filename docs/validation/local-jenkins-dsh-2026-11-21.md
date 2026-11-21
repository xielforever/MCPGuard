# Jenkins 接入本地验收

验收日期为 2026-11-21（新加坡时间）。使用已安装的 DeepSeek Harness 0.2.2.0，通过 MCP Streamable HTTP 调用隔离的 MCPGuard。审批使用独立审批人账号和控制台接口。Jenkins 2.580.2 运行于固定摘要的官方镜像，PostgreSQL 元数据使用独立测试数据库。

完整运行 `43097831b7c0` 退出码为 0。Jenkins 收到三笔构建 POST，实际完成三次构建。

| 场景 | 验收结果 |
| --- | --- |
| `orders-validation` | 动态读取 `GIT_REF` 和布尔参数 `RUN_TESTS`。申请停在待审批状态，审批后由申请人恢复动作。回执为队列 `1`，Jenkins 构建 `#1` 的参数与冻结计划一致。 |
| `inventory-verification` | 动态读取固定选项 `PROFILE` 和字符串 `RELEASE_TAG`。回执为队列 `2`，Jenkins 构建 `#1` 的参数与冻结计划一致。 |
| 重复提交 | 正常场景在待审批和完成后用原键重提，均返回原动作。用原键更改参数时返回冲突。每个动作只发出一笔构建请求。 |
| 审批期间参数变化 | 在 Jenkins 修改待批任务的 `PROFILE` 选项，审批后恢复动作。记录为 `JENKINS_SCHEMA_CHANGED`、`NOT_DISPATCHED`，Jenkins 收到零笔构建请求。 |
| Jenkins 接收后回执丢失 | 故障代理在 Jenkins 返回原生队列 `3` 后丢弃该 HTTP 响应。网关记录为 `OUTCOME_UNKNOWN`、`DISPATCH_UNKNOWN`，继续重提和两次恢复后仍只有原来的一笔构建请求，对应 `orders-validation` 构建 `#2`。 |

验收脚本将 MCP 动作编号、队列编号、Jenkins 构建编号及网关审计事件逐笔关联。审计核对包含申请创建、独立审批、执行意图和最终状态，各动作只有一条执行记录。脱敏结果见 [验收记录](local-jenkins-dsh-2026-11-21.json)。

前几次运行保留了原始日志。首轮两个正常动作通过，随后发现测试脚本的 Jenkins Choice XML 路径假设需要修正。第二轮在发出首个 MCP 请求前遇到 Windows 启动超时。第三轮业务和故障场景断言通过，审计断言补入网关现有的 `REVIEW_APPROVE` 事件后再从头运行。

## Go 回归

隔离 PostgreSQL 16.14 上运行 `go test -race -p 1 -count=1 -json ./...`。最终运行 `a1329847d4` 退出码为 0，320 项通过、0 项失败。标准环境按夹具条件跳过 22 项，涉及受控写入、撤权和 Wave 2 故障专项。另独立执行的 Jenkins 包 race 测试全部通过。

首轮 Go 回归 `69cca5ca71` 的原日志已保留。新增回执用例的模拟路由漏掉了无参数构建端点末尾的 `/build`。修正测试夹具后重新执行全量回归，Jenkins 执行代码与已通过的 DSH 验收一致。
