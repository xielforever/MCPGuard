# Jenkins App 隔离验收

验收日期为 2026-11-22（新加坡时间）。候选以 `214b40e1b9787bcbb33552c84fd6f6362cb7bdd0` 为基线，承接 [#29](https://github.com/xielforever/MCPGuard/issues/29) 的连接器登记和 [PR #28](https://github.com/xielforever/MCPGuard/pull/28) 的受控构建。通过 Chrome、安装版 DeepSeek Harness `0.2.2-rc.5`、Jenkins `2.580.2` 和隔离 PostgreSQL `16.14` 实测。DSH 使用 Streamable HTTP。

## 同一个 App 完成查询和受控构建

运行 `4603907b8969` 退出码为 `0`。网关仅启用控制台连接器配置。Chrome 使用管理账号创建、测试并启用了 `delivery-ci` 和 `existing-read-only-ci`。前者显式开启构建审批，后者采用默认只读配置。安装版 DSH 从 `connector.list` 取得 `delivery-ci` 的 ID，用同一个 ID 发现任务、读取动态参数并提交构建审批。独立审批账号批准后，申请人在 DSH 领取执行结果，并用原 `actionId` 查询 Jenkins 队列。

| 任务 | DSH 查到的动态参数 | 原生结果 |
| --- | --- | --- |
| `orders-validation` | `GIT_REF`、`RUN_TESTS` | Jenkins 队列 `1`，构建 `#1`，实际触发一次 |
| `inventory-verification` | `PROFILE`、`RELEASE_TAG` | Jenkins 队列 `2`，构建 `#1`，实际触发一次 |

两笔正常构建都核对了实际 Jenkins 参数与批准计划，并关联了申请、独立审批、执行意图和最终结果审计。网关工件 SHA-256 为 `22a30760b1fb0d5b9f9c2954743362f6b0a439f9aecacb209f87dc4aaba66534`。[App 完整脱敏回执](local-jenkins-app-dsh-2026-11-22.json)

## 限制、故障和兼容

| 场景 | 观察结果 |
| --- | --- |
| 只读 App 与独立身份 | 只读 App 仍可查询，构建被拒。第二个 DSH 身份可读共享 App，直接读取私有 App 被拒。共享 App 的读取授权未暴露构建提交工具。 |
| 审批等待及参数变化 | 等待审批时保持 `NOT_DISPATCHED`。待批期间修改任务参数定义后，原动作返回 `JENKINS_SCHEMA_CHANGED`，Jenkins 收到零笔构建请求。 |
| 停用和旧审批 | 已连接的 DSH 会话在 App 停用后收到拒绝。重新启用后版本从 `3` 推进至 `5`，已有批准动作返回 `NOT_RESUMABLE`。审计有 `PLAN_INVALIDATED`，执行意图和后端构建请求均为零。 |
| 重复提交和回执丢失 | 同键同内容复用原动作，同键不同参数发生冲突。故障代理只丢弃一次 Jenkins 已接受的回执。网关保留 `OUTCOME_UNKNOWN`，重复查询和恢复后后端仍只有一笔请求，对应队列 `3`。 |
| 既有文件配置 | 默认模式运行 `45f842fe7b78` 退出码为 `0`。原 `jenkins.list_jobs`、`jenkins.describe_job`、无需 `connectorId` 的提交和队列查询经过安装版 DSH 复验，覆盖两笔正常构建、参数变化与回执丢失。[旧路径脱敏回执](local-jenkins-app-legacy-dsh-2026-11-22.json) |

## Go 竞态回归

隔离 PostgreSQL 夹具 `931eac055f` 执行 `go test -race -p 1 -count=1 -json ./...`。退出码为 `0`，336 项通过，0 项失败。22 项受控写、撤权和高危故障专项按本夹具条件跳过。`pkg/connectors` 和 `internal/connectors` 的定向测试也通过，覆盖旧配置的只读默认值、构建开关的类型约束，以及查询与受控构建共享同一 Jenkins 定义。

首次回归准备在生成契约检查阶段发现 Windows 工作树中的 CRLF 与仓库的 LF 工件不同。五份工件恢复为仓库行尾后，生成契约检查通过，仓库中的工件内容没有变化。失败场景和最终竞态日志分别保留。

## 复跑

在 WSL 中使用隔离 Docker 和当前 Windows 安装版 Chrome、DSH。

```bash
python3 -B scripts/jenkins_dsh_fixture.py --app --report .dev/jenkins-app-dsh.json
python3 -B scripts/jenkins_dsh_fixture.py --report .dev/jenkins-app-legacy-dsh.json
python3 -B scripts/read_fixture.py prepare
python3 -B scripts/read_fixture.py test
```

首次 App 运行在浏览器准备阶段等待 Windows 子进程超时，尚未发出业务请求。该轮原始日志保留。复跑通过本次夹具的私有临时文件传递浏览器输入，用完即清除。归档 JSON 只保留连接器、动作、队列和审计的脱敏证据。
