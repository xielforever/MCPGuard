# ChatGPT 首批接入与验证设计 v0.1

状态：官方资料核对与拟议接入方案，未完成真实 ChatGPT 验收。抓取日期：2026-10-22（UTC）。
首批产品候选已确定为 ChatGPT；网页/桌面、账号套餐、workspace 及管理员角色尚未明确，IdP 和通知平台未定。
已确认采用受控工具目录：获授权读取自动放行、受控写由他人审批、高危默认禁止。
本文只细化 ChatGPT 接入，不将 Codex CLI 或 Responses API 偷换为已选客户端。
系统边界仍为 ChatGPT → MCPGuard MCP Server → 原生 PG/RESP/HTTP，详见[传输设计](transport-connector.md)。

## 1 先确认产品面与账户资格

官方资料存在新 Plugins 体验、旧 Apps/Developer Mode 流程及本地 Codex host 配置，不能混用菜单和能力。[S1][S2][S5]

| 产品面 | 官方资料范围与本项目结论 |
|---|---|
| ChatGPT web 自定义 MCP | 当前优先验证候选；使用远程 MCP endpoint，不要求安装本地连接器 |
| Business、Enterprise/Edu workspace | Help 明确列出 web full MCP 与写/修改工具，创建及发布仍受 workspace 权限控制 |
| Pro 或其他个人账户 | Help 将 Pro 自定义连接描述为 read/fetch；其他套餐及当前开放情况须核对，不保证企业写流程可用 |
| ChatGPT Desktop | 官方另列 local MCP apps；特定 Codex host 文档列出 stdio/HTTP，但不能据此宣布本项目程序已兼容 |
| Deep research / company knowledge | Help 对这些模式列出 read/fetch 或 search/fetch 限制，不作为受控写入口 |
| Codex CLI / IDE extension | 另有本地配置及 stdio/HTTP 能力；不是本轮已选 ChatGPT 接入面的替代验收 |
| OpenAI Responses API | 应用开发者配置 MCP tool 与 API 授权流程，和 ChatGPT 账号/workspace 不是同一接入产品 |

Business 的自定义 workspace 连接创建/发布由管理员或 owner 负责；Enterprise/Edu 可用 RBAC 授权指定成员创建/测试，发布仍由管理员/owner 控制。[S1]
连接可用、workspace 已发布、用户已安装/授权和某个工具允许执行，是不同检查，不能仅截图工具列表就算通过。
账号或 workspace 不具备写工具资格时，应先说明产品限制；不能把写工具假标成只读规避。

## 2 推荐接入路径与现成连通前提

优先使用 ChatGPT 的自定义远程 MCP 连接指向企业 HTTPS `/mcp`，网关本身无需提供 ChatGPT 自定义 UI。[S1][S2]
ChatGPT 实际请求侧需能访问该 HTTPS 入口及适用 OAuth 端点；员工浏览器可达不自动证明平台请求侧可达。
用户已确认物理互通，本轮只核验既有路径的 TLS、请求与授权证据，不新增 VPN、隧道或网络建设任务。
stdio 接入仍是整体产品目标；ChatGPT web 的自定义远程连接不能直接配置本地 `command` 来启动 mcpguard-connect。
Desktop 是否采用本地接入程序，须先确定具体产品面、配置方式及账号权限后另验；不能将 CLI 的配置原样交付给 web。

建议按当前账户实际界面操作，而非冻结一套旧菜单：

1. 核对 ChatGPT 套餐、所处 workspace、成员/管理员角色及创建 MCP 连接的权限。
2. 当前 Plugins 体验从 Add custom MCP server 添加名称、HTTPS `/mcp` 与认证方式，并确认每连接风险提示。
3. 官方当前流程不要求先开启 Developer Mode；旧 Apps → Create 等步骤另列为 legacy，遇旧界面再按对应说明操作。
4. 完成员工自己的 OAuth 连接，检查工具扫描所得名称、Schema、认证描述及 annotations。
5. 管理员按适用 workspace 流程审查、发布和配置可访问群体及允许工具；用户安装/连接后在新对话测试。
6. 从授权读取开始，再测试待批写入、他人审批与显式 resume；不要用真实生产副作用验证初次接入。

公开目录投稿与企业内部自定义连接是不同交付；本项目不以 App Store 上架作为首条企业闭环前提。

## 3 员工 OAuth 与平台确认的边界

网关提供 Protected Resource Metadata、可信授权服务器发现与面向网关 resource 的授权契约。[S3]
ChatGPT 的用户授权采用兼容 OAuth 2.1/PKCE；按具体连接支持选择 CIMD、DCR 或预注册，不假定任意 IdP 开箱兼容。
回调 URI 与客户端信息使用连接管理页实际给出的值；官方存在稳定与按连接区分的回调形式，不能仅写死一个示例 URL。
续期需核对授权服务的 refresh token 及适用 offline_access 配置；首次登录成功不代表过期后仍能调用。[S1][S3]
各受保护工具按官方 auth/reference 声明 securitySchemes；如需要工具级重新授权，使用适用的 mcp/www_authenticate challenge。
OpenAI所需的工具元数据由接入profile映射，保持领域策略与协议扩展分离，并以固定SDK实际序列化结果验收；不默认其他MCP客户端理解这些扩展。企业工具统一采用OAuth保护，不以匿名方式绕过员工身份要求。
认证声明不执行鉴权；服务端每次验证员工 token、issuer、audience、有效性、scope 与当前资源/数据权限。
不将员工 MCP token 透传成 PG、Redis 或业务 API 凭据；业务 API 401/403 也不能触发错误的 ChatGPT 登录流程。
平台对 MCP 客户端来源的验证不代替员工身份；OAuth 用户令牌与机器间共享管理员密钥不能混用。
具体 IdP、组同步和撤权传播仍待确定，本稿未声称真实企业身份已经接通。

ChatGPT 可按 app 权限、上下文和风险请求确认，也可能阻断部分高风险动作。[S1]
企业策略的“授权读取自动放行”只约束网关，不承诺 ChatGPT 免确认、免平台阻断或必然选择工具。
ChatGPT 操作人的确认点击不能证明企业另一位合格审批者已批准；两道边界分别执行，不能相互替代。

## 4 工具快照与员工目录隔离

官方 Help 描述自定义 workspace 连接会使用已批准工具及输入的冻结快照；工具变化需管理员 refresh/审查后更新。[S1]
新 Plugins 文档也要求改工具、Schema、认证或元数据后刷新连接，再在新对话复验；不同发布渠道更新方式须按实际产品核对。[S2]
不能假定一次共享 app 的工具快照会对每位员工重新 tools/list 并实时隐藏元信息。
建议发布最小、稳定且无敏感资源名称的业务工具集合；Schema、description、enum 中也不嵌入秘密或越权资源清单。
模型提交的 resourceId、key、业务对象仍在每次调用逐项鉴权，目录显示某工具不授予其全部资源访问权。
网关自身目录缓存继续按主体权限 epoch、catalog/policy versions 隔离，但该缓存不能控制 ChatGPT 的 workspace 快照。
严格隐藏工具元信息时，按权限组独立发布/配置 app 并实测平台访问控制；不能仅依赖服务端 tools/list 过滤承诺隐藏。
list_changed、订阅或 ttlMs 不替代管理员 refresh；未刷新造成的旧 Schema 调用应清晰失败，不擅自扩大输入兼容范围。
工具更新需记录发布版本、平台刷新状态与回归结果；平台旧快照不能使服务端旧权限或失效批准继续有效。

## 5 annotations 与受控工具目录

官方 reference 要求显式填写 readOnlyHint、destructiveHint、openWorldHint，idempotentHint 为可选；按实际可达副作用分类。[S4]

| 工具行为 | 注解与实现要求 |
|---|---|
| 纯业务读取 | 确实只读取/计算时 readOnlyHint=true；仍执行资源和数据准入 |
| 登记待批写工作流 | 会创建业务 Action/排队状态，不能因尚未写原生资源而标成纯只读 |
| action.status | 仅查询受权状态时可标只读，不隐式续期、批准、重新计划或触发派发 |
| action.resume | 可触发原生写，readOnlyHint=false；destructiveHint 按全部允许操作的实际影响填写 |
| action.cancel | 改变业务状态或取消工作，不能一概标只读；破坏性提示按真实效果确定 |
| 任意工具的幂等性 | 相同参数创建新 Action 可能有额外效果，不能因同 Action 只领取一次就填 idempotentHint=true |
| 封闭企业资源 | 外部托管不自动等于 openWorldHint=true；按有界账户/资源与开放外部交互的实际边界填写 |

高危默认禁止须由网关策略执行；不靠 destructiveHint 代替阻断，也不靠改工具名绕过分类。
只记录必要调用审计不会使纯业务读取变为业务写；登记待批计划则是显式工作流变更。action.resume在同Action重复调用不重派经过验证后可声明相应幂等性，不能把该结论推广到每次创建新Action的写工具。
不发布任意 SQL、任意 Redis 命令、任意 URL 或模型批准工具；只发布已审核 Schema 与原生 operation。
返回标准可读摘要和公开 outputSchema 对应的结构化内容；无需为了普通工具调用引入 Apps UI/widget。

## 6 受控写推荐 CONTROL_FLOW

在 ChatGPT 真实等待/MRTR 能力未核实前，推荐将受控写设计为显式工具流程，避免持续占住原 RPC 等别人审批。
此为本项目业务契约，不能声称 ChatGPT 天然懂 action 工具或会自动轮询；需明确描述并实测多轮操作。

1. 员工请求受控写，网关验证准入并冻结完整计划，创建 CONTROL_FLOW Action；高危或无权请求直接拒绝。
2. 原工具返回已完成的 DeferredActionResult：actionId、待审批状态、NOT_DISPATCHED、executionId:null 和审批页链接。
3. 文本明确“尚未执行”；合格的另一人在门户经企业身份验证，查看完整受权计划并作决定。
4. 回调只持久化决定，不派发。操作人后续可显式要求 ChatGPT 调用 action.status 检查状态。
5. 已获批后，由有效 action.resume 新调用重新鉴权、校验计划/期限/审批资格并原子领取原 Action 执行权。
6. resume 不接受新业务参数；未批、否决、过期或取消均不派发，已有执行返回既有状态/受控结果，不重复写入。

门户链接和 actionId 不含批准权；ChatGPT、模型、操作人自身都不能冒充另一审批者。
普通 tools/call 新 ID 或重复参数不保证关联旧 Action；手动重做是新意图，可能再次生效，须说明后再授权。
原工具最终响应后绝不向旧 RPC 补发结果；后续状态与实际结果由新的控制工具调用取得。

## 7 协议版本与高级能力的证据边界

本次核对的 ChatGPT 产品页没有为该用户的具体入口承诺精确 MCP profile、MRTR 或 Tasks 行为。
auth 文档链接某一版 MCP 规范、官方 SDK 支持新版或示例能初始化，均不能证明 ChatGPT 已按 2026-07-28 执行。
网关保留两 profile 设计；接入时记录 initialize.protocolVersion 或每请求版本头/_meta、实际方法和结果契约后再声明支持。
MRTR、elicitation、Tasks、取消界面、等待窗口、后台通知均列为该账号/产品面的实测项，不以“full MCP”概括全部通过。
CONTROL_FLOW 不需要普通 ChatGPT 调用管理 REST，也不依赖 MRTR；若平台不能调用所需写工具，应报告账户/策略限制而非绕过。

## 8 首条验收与来源

首条读取：获准员工登录 → 选择自定义连接 → 调用无害目录工具 → 原生测试服务读取 → 返回摘要/数据 → 关联审计。
首条写入：待批结果 → 门户另一人批准 → action.status/resume → 单次原生写 → 查实际状态与审计。
反例覆盖无权限资源、自批、模型 approved、旧批准换参数、重复 resume、过期、取消及写后响应丢失。
平台确认/阻断但网关没有收到请求时，应记录“尚无网关 Attempt”，不能伪称网关审批或原生执行已发生。
证据注明套餐、workspace/角色、web/desktop、应用发布方式、工具快照版本、profile、OAuth 与真实结果。

来源均为 OpenAI 官方页面，核对日期 2026-10-22；动态套餐、界面和权限应在实施时再次核验。

- [S1 自定义 MCP 连接、套餐、权限与快照](https://help.openai.com/en/articles/12584461-connecting-custom-mcp-servers-in-chatgpt)
- [S2 连接、测试与刷新元数据](https://developers.openai.com/plugins/deploy/connect-chatgpt)
- [S3 OAuth、客户端注册、令牌与工具级认证](https://developers.openai.com/plugins/build/auth)
- [S4 工具 annotations、securitySchemes 与结果参考](https://developers.openai.com/plugins/reference)
- [S5 Codex host/Desktop/CLI 的 MCP 边界](https://developers.openai.com/codex/mcp)
- [S6 Plugins 产品面与 Desktop-only/local apps](https://help.openai.com/en/articles/20001256-plugins-in-chatgpt)
- [S7 Responses API MCP，非 ChatGPT workspace 配置](https://developers.openai.com/api/docs/guides/tools-connectors-mcp)
