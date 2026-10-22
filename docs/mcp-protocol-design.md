# MCPGuard MCP 接入与执行边界设计

状态：按用户明确的原生后端架构修订，细则待验证。日期：2026-10-22。

**MCP 仅用于 IDE 或 Agent 到 MCPGuard。网关对外是 MCP Server，对内通过原生驱动访问 PostgreSQL、Redis 和 HTTP API。**

下游服务不做 MCP 握手、能力协商或工具发现，不存在上下游 MCP 版本转换。本稿保留客户端侧 MCP 约束；实际原生执行见[适配设计](native-adapter-design.md)。

具体的远程入口、本地 stdio 接入、企业登录流程与客户端兼容矩阵见[IDE 接入兼容方案](ide-access-compatibility.md)。

## 1 工具由网关提供

网关注册工具名称、输入输出 Schema、允许目标、适配器操作及策略。例如 pg.query 对应数据库查询，redis.get 对应 key 读取，crm.get_customer 对应已登记 HTTP operation。

tools/list 从网关工具注册表生成，并按员工权限过滤；分页、目录变更和缓存都由网关负责，不拼接下游 MCP 工具目录。数据源 ID 是受控资源标识，不是连接串或认证凭据。

tools/call 经输入校验、权限、操作计划和策略后调用适配器。实际驱动接收结构化操作与执行上下文，不能把 MCP JSON 原文发送给 PG 或 Redis。

第一版可以仅声明 tools 及必需基础能力。resources、prompts、elicitation、Tasks、Apps 等仅在网关确实实现后声明。原生服务不需要具备这些 MCP 能力。

## 2 IDE 接入与版本

企业中央入口使用 HTTP MCP，并对受保护请求进行标准远程授权。产品同时提供 stdio-only IDE 的本地接入程序，转 HTTP 连接同一中央服务；两条路径均需验收。建议远程传输采用 Streamable HTTP，stdio 只属于 IDE 接入侧。

2025-11-25 与 2026-07-28 均有正式发布，行为不能混写：

| 行为 | 2025-11-25 | 2026-07-28 |
|---|---|---|
| 建连 | initialize、notifications/initialized | 无初始化握手；每请求版本与能力元数据，server/discover |
| HTTP 状态 | 可选协议 session、GET SSE | 无协议 session、无 GET 通知流 |
| 索取输入 | 服务端请求，如 elicitation/create | MRTR input_required，客户端补输入后用新 RPC ID 重试 |
| 通知 | 协商对应能力后发送 | subscriptions/listen、确认订阅与类型过滤 |
| 请求 SSE 断连 | 不应仅因断连视作取消 | 关闭该请求流视作取消，不支持 Last-Event-ID 恢复 |
| 结果与缓存 | 对应该版契约 | resultType；相关列表结果要求 ttlMs、cacheScope |
| Tasks | 实验能力 | 独立可选扩展，与旧实验版本方法不同 |

建议按实际 IDE 验证明确 profile；后端 PG、Redis 和 API 的版本或功能兼容由各自适配器处理，不能把它们列入 MCP 协商矩阵。

首发ChatGPT以实际协商profile为准。若其版本不在上述候选中，先核对固定SDK支持范围，补齐对应契约与测试后再启用，不能强迫其使用2026或暗中删字段转换。两代候选的测试覆盖不等于已承诺ChatGPT支持两代。

## 3 企业认证

企业网页登录和 MCP 客户端 OAuth 授权分别设计。网关作为受保护 resource server 提供标准发现信息，接入企业授权服务，验证 access token 的有效性、受众和权限。网页 cookie 或 OIDC ID token 不能直接代替 MCP access token。

后端按自己的认证方式处理：PG 数据库角色与认证、Redis ACL 用户与认证、HTTP API 的 token、签名或其他已配置机制。员工访问网关的 MCP token 不原样变成后端凭据。

session、action、订阅 ID 与 MRTR requestState 不替代身份。不同员工即便使用相同 RPC ID，也不能共享结果、目录缓存或授权状态。

## 4 消息与执行上下文

RPC ID 对应一次 MCP 尝试；action ID 对应业务意图；execution ID 对应一次执行领取及派发跟踪，记录存在不证明已发送。未领取执行权时不创建 execution。MRTR 重试必须使用新 RPC ID，并通过受验证状态关联原 action。

通知不等待响应，客户端读写不能被人工授权阻塞。进度与取消关联到正确调用；进度只表达真实状态，不保证 IDE 延长超时。目录缓存按权限隔离，工具调用时再次检查权限。

取消映射到适配器上下文，不向数据库发送 MCP notifications/cancelled。PG 尽力取消查询并处理事务，Redis 和 HTTP 停止等待不代表后端动作未执行。上游 MCP 的取消规则仍按客户端 profile 解释。

## 5 人类授权交互

| 方式 | 适用条件与约束 |
|---|---|
| 普通工具调用内有界等待 | 按目标 IDE、网关和服务端超时设置窗口；最终仅返回一次结果，取消后未派发的 action 终止 |
| 2026 MRTR | 标准 input_required、requestState 与新 ID 续传；网关负责完整状态，不依赖原生后端实现 MRTR |
| 标准 Tasks | IDE 与网关具备并声明相应能力；网关持久任务，获批后执行普通原生操作 |
| 显式业务续传 | 不支持所需能力时，明确返回未执行，用户按约定查询或继续；需要客户端理解流程 |

普通最终响应后不能向同一 RPC 补发结果。MRTR 是标准多轮交互，不属于普通错误后继续发送；它结束当前 RPC 尝试，业务 action 可继续等待。

requestState 若影响执行，需校验完整性、主体、目标、工具、完整业务参数摘要和有效期，并由服务端保证单次消费。网关的人类授权状态机核验决策人身份及资格后认定持久决定，不能信任模型的 approved 字段或客户端 accept。URL elicitation 可引导进入授权页，不能证明外部人类决定已经完成。身份源负责可信身份，不直接决定具体操作是否获批。

MRTR模式的授权回调只更新状态，后续有效续传请求再领取原生执行权；不能只因回调批准就自动执行。首版采用风险分级和受控写他人审批；模型或ChatGPT界面的确认不替代网关可信决定。ChatGPT是否支持该MRTR组合需独立验证。

## 6 MCP 结果与原生错误映射

适配器返回结构化执行结果，网关映射为工具定义的 structuredContent 与可读摘要，保留列类型、二进制编码、截断和副作用状态等必要信息。输出 Schema 由网关制定，不假设所有结果都是简单文本。

| 情况 | IDE 侧响应原则 |
|---|---|
| 员工访问网关的 token 无效或权限不足 | 按 MCP 远程授权规则处理 401、403 等 |
| JSON、方法、版本或调用结构无效 | 对应 MCP 或 JSON-RPC 协议错误 |
| PG 认证或 SQL 错误、Redis 错误、业务 API 失败 | 脱敏后的工具执行结果，通常 isError 为 true |
| 业务 API 返回 401 或 403 | 原生操作失败，不直接冒充员工 MCP 登录失效或发出后端认证挑战 |
| 人类否决或业务政策阻断 | 建议按工具契约返回拒绝及未派发状态；这是产品映射，不是规范唯一错误码 |
| 写操作已发送但结果不明 | OUTCOME_UNKNOWN，不能标记未执行或自动重试 |

SQL 错误、Redis 错误和 HTTP 错误不一定证明全部副作用已回滚；请求状态与副作用状态分别表达。结果校验失败也不能据此自动重做写操作。

## 7 SDK 与验收

建议评估官方 Go SDK 的 Server、transport、JSON-RPC 与版本处理。此架构不需要为了 PG 或 Redis 引入下游 MCP Client。PG、Redis 和 HTTP 各自使用成熟原生客户端库。

已核对 Go SDK v1.8.0 支持多个 MCP profile，要求 Go 1.25.0；项目当前为 Go 1.22，接入该版本需升级。SDK 不替代企业权限、审批和原生执行语义；Tasks 是否可用需单独核对扩展实现。

| 验收组 | 目标 |
|---|---|
| MCP 接入 | 声明支持的profile、工具发现、Schema、通知、进度、取消与错误正确；ChatGPT以实测协商为准 |
| 企业身份 | 工具目录、调用、结果与 MRTR 状态逐次鉴权 |
| 原生调用 | 正确驱动、地址、身份与命令，不要求后端理解 MCP |
| 数据隔离 | 并发同号 RPC、多个资源与连接池不串用户上下文 |
| 人类授权 | 冻结原生操作计划，拒绝篡改与重复派发 |
| 故障 | PG 事务、Redis 命令、HTTP 副作用分别验证，未知结果不盲重试 |
| 互操作 | 独立语言 MCP 客户端和真实 IDE；真实测试 PG、Redis 与无害业务 API |

模拟 IDE、模拟身份和 IM 不需要模型 key。真实数据库和 Redis 可在测试环境运行，以覆盖原生协议、驱动和连接行为；仅替换成模拟 MCP 服务不能验证这套目标架构。

## 8 MCP 核对来源

以下协议资料仅适用于 IDE 到网关这一侧，固定到官方仓库 80041ab0a139a8f4fb3adbc99f7ae9113ea59671。

- [2026-07-28 发布](https://github.com/modelcontextprotocol/modelcontextprotocol/releases/tag/2026-07-28)与[版本规则](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2026-07-28/basic/versioning.mdx)
- [2025 生命周期](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2025-11-25/basic/lifecycle.mdx)与[2026 Streamable HTTP](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)
- [工具](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2026-07-28/server/tools.mdx)、[MRTR](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2026-07-28/basic/patterns/mrtr.mdx)与[远程授权](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/80041ab0a139a8f4fb3adbc99f7ae9113ea59671/docs/specification/2026-07-28/basic/authorization/index.mdx)
- [Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/README.md)与[Go 版本要求](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/go.mod)
