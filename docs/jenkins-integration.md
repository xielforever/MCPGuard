# Jenkins 受控构建

DSH 从 MCPGuard 查询 Jenkins 任务及其当前参数定义，由独立审批人在 MCPGuard 控制台审批。申请人通过 `action.resume` 领取执行结果，通过 `jenkins.get_queue` 按自身动作编号查询 Jenkins 队列。HTTP 直连和 stdio 转发都使用网关的同一套 MCP 工具。

## 启用

1. 运行元数据迁移 005，新增可配置的 `jenkins` 资源类型。迁移保留已有资源、授权和数据。现有未启用 Jenkins 的实例可继续使用元数据版本 004。
2. 由元数据管理员在 `mcpguard.control_resources` 为目标组织登记并启用 `jenkins` 资源。给申请人授予 `jenkins.submit_build`，给独立审批人授予 `jenkins` 资源的审批权限。任务发现和队列查询也受申请人的资源授权约束。
3. 在运行用户可读的受保护配置文件中设置 `orgId`、`controllerId`、`baseUrl`、`username`、`apiToken` 和 `folders`。用 `MCPGUARD_JENKINS_CONFIG` 指向文件，并配置现有审批控制台。Jenkins 专用账号需要查看配置范围内的文件夹和任务、触发构建以及查看队列项的权限。正式 Jenkins 使用 HTTPS，HTTP 仅允许回环地址。Jenkins API token 留在网关配置文件中。
4. `folders` 使用 Jenkins 文件夹的完整名称，例如 `["operations/commerce"]`。显式配置 `[""]` 会从根目录查找任务。

## DSH 调用

| 工具 | 输入与返回 |
| --- | --- |
| `jenkins.list_jobs` | 用空对象调用，返回授权范围中的任务及其 `jobRef`。 |
| `jenkins.describe_job` | 提供 `jobRef`，返回参数名称、类型、可用选项和默认值，以及 `schemaRevision` 与可提交状态。 |
| `jenkins.submit_build` | 提供 `jobRef`、`schemaRevision`、`parameters` 和 `requestKey`，返回待审批动作及 `actionId`。`parameters` 为对象，无参数的任务提交空对象。 |
| `action.resume` | 审批通过后提交 `actionId`，返回 Jenkins 队列编号或明确的失败状态。 |
| `jenkins.get_queue` | 提供已有队列回执的 `actionId`，返回 Jenkins 队列状态及已分配的构建编号。 |

当前可提交普通字符串、多行文本、布尔值和固定选项。密码、文件、凭据及未支持的插件参数会使任务不可提交。网关按发现到的定义校验参数，并在审批后发送构建请求前重新检查任务和参数定义。定义发生变更时，动作以 `JENKINS_SCHEMA_CHANGED` 结束，申请人须重新查询参数后发起新申请。

参数版本覆盖 Jenkins API 返回的任务可构建状态、参数名称、类型、固定选项及安全默认值。校验和触发是两个独立的 Jenkins API 请求，配置恰好在两次请求之间改动时仍存在竞争窗口。

同一次申请的重试使用原 `requestKey` 和原参数。相同键与相同请求复用原动作，出现请求内容变更则返回冲突。一次动作最多派发一次。Jenkins 收到请求后回执丢失时，MCPGuard 记录 `OUTCOME_UNKNOWN`，保留原动作及审计记录，需在 Jenkins 核对结果。

## 复验

本地验收使用安装的 DeepSeek Harness、独立的 MCPGuard 元数据、官方 Jenkins 容器和两个原生任务。测试脚本 `scripts/jenkins_dsh_fixture.py` 可以写出脱敏报告，并核对 Jenkins 实际队列与构建记录。
