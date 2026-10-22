# MCPGuard 可校验设计契约

版本：v0.1设计补充。日期：2026-10-22。本目录提供评审与后续实现使用的契约、假数据配置和离线检查，不接入企业资源。

## 1 文件与用途

| 文件 | 用途 |
|---|---|
| [openapi.yaml](openapi.yaml) | 管理API核心子集：14个路径、16项操作，审批详情、版本前提、幂等与取消 |
| [tool-result.schema.json](tool-result.schema.json) | 资源业务工具的FINAL/DEFERRED结果，区分请求状态、发送证据和副作用 |
| [action-control-result.schema.json](action-control-result.schema.json) | action.status/resume/cancel的独立输入与结果；RUNNING不伪装成FINAL |
| [action-view.schema.json](action-view.schema.json) | 受权请求视图、状态/模式与可用操作的必要约束 |
| [error.schema.json](error.schema.json) | 管理HTTP错误；不与原生服务认证错误混用 |
| [tool-bundle.schema.json](tool-bundle.schema.json) | 受控工具包和单工具定义，禁止未知配置形状 |
| [demo-tools.yaml](demo-tools.yaml) | 六个演示工具、可信范围绑定及正反输入/成功数据 |
| [metadata-model.sql](metadata-model.sql) | 独立空库schema模型、组合FK、永久执行资格、不可变与状态约束 |
| [contract-examples.json](contract-examples.json) | 公共结果、控制结果与ActionView的正反断言 |
| [validate.py](validate.py) | 离线Schema、例子、引用、OpenAPI和SQL/PLpgSQL语法检查 |
| [requirements.txt](requirements.txt) | 文档校验工具的已验证直接依赖版本，不改变应用依赖 |

OpenAPI是核心管理接口子集，不包含完整身份提供方、密钥托管或通知平台API。完整服务边界仍见[服务设计](../detailed/service-operations.md)。

## 2 结果与字段约定

资源业务工具的structuredContent是ToolResult：kind=DEFERRED表示原RPC已完成、业务尚待批；kind=FINAL表示该次业务处理已经得到终态结果，不等于原生成功。

FINAL不接受RUNNING或INTENT_RECORDED；状态查询可以表达这两者。未领取时executionId为null；受理前业务失败可无Action/Attempt，不能为了输出造一条执行记录。

所有非成功FINAL的data固定为null。失败或部分效果的诊断放safeError与受控evidenceRef，避免用FAILED绕过字段允许集。每个成功data还须通过工具自己的resultDataSchema。

控制工具输出与ToolResult分开：

- action.status返回ActionView，即使所查原生操作失败，状态查询本身也可成功。
- action.resume未批准返回APPROVAL_PENDING；重复请求遇原执行RUNNING时返回EXECUTION_IN_PROGRESS，不抢占或补发旧RPC。
- 已有终态可附受权的当前业务FINAL视图，保留原业务来源；已完成但结果内容不可取时返回STATE_RETURNED与result=null，未知结果可查询和核对，不再次派发。
- 受理前对象权限失败返回不含Action详情的CONTROL_ERROR，不能泄漏他人状态。

控制结果里的Action、附带FINAL和当前主体必须由服务端校验关联及权限；JSON Schema不能比较两个字段的UUID是否相等，也不能核实人类身份、nonce或外部事实。
控制请求按外层error决定isError；成功查询到FAILED/UNKNOWN仍是查询成功，正文须明确业务结果。原RPC响应历史不改写，当前视图只依可信证据收敛，不能用当前业务值伪造丢失的原结果。

公开工具名统一1至64字符、首字符为ASCII字母，其他字符允许字母/数字/点/下划线/连字符。配置的资源别名通过注册表解析为真实资源UUID，不把别名当作连接串或授权证明。

计划HMAC-SHA256以43字符无填充base64url显示。UUID与摘要示例只满足格式，没有对应真实主体、计划或密码学授权。

## 3 客户端Schema的生成边界

工具包中的outputSchema是仓库本地引用，不能直接发布相对文件路径让ChatGPT下载。发布器应只解析受信注册表/本地引用，按固定版本生成自包含Schema或正确重定位的本地defs。

公共结果封套和工具data约束需同时保留。校验器在内存构造type=object的合并输出Schema并检查正反样例；这只是编译契约演示，不是已实现网关发布器。

inputSchema和resultDataSchema不得触发任意网络或文件读取。当前样例不含远程引用；将来需要共享defs时只能从受控注册表导入并固定版本，不能因Schema验证引入任意URL抓取。

ChatGPT产品面的工具快照、Schema子集及刷新流程仍需真实账户验收；离线可解析不等于平台已支持所有组合。

## 4 复现检查

在仓库根目录运行，推荐隔离Python环境；依赖安装可能需要访问软件包源，校验过程本身只读取本目录文件。

```bash
python -m venv /tmp/mcpguard-spec-check
/tmp/mcpguard-spec-check/bin/python -m pip install -r docs/specs/requirements.txt
/tmp/mcpguard-spec-check/bin/python docs/specs/validate.py
```

校验器执行以下检查：

1. 严格JSON/YAML加载、重复键拒绝、五份JSON Schema的meta-schema及受信本地引用。
2. 公共结果/ActionView/控制结果的正例必须通过、反例必须拒绝，不能只统计文件被解析。
3. 六个工具的正反输入、正反成功data，以及公共封套与工具成功数据约束的合并输出。
4. OpenAPI3.1完整规范验证、唯一operationId及引用解析；对本地无环引用图展开后验证，不访问外部schema地址。
5. PostgreSQL SQL声明和九个PL/pgSQL函数的语法解析。

检查不会执行SQL、创建数据库、运行原生服务、发送通知或调用模型。不需要大模型key或生产凭据。

## 5 能证明与不能证明的事项

通过检查说明这些设计文件形状一致、声明的样例正反断言成立、OpenAPI符合规范、SQL可被解析。它不证明数据库内的触发器/锁行为、审批身份、权限或原生副作用真实有效。

metadata-model.sql会在明确执行时创建独立mcpguard_model schema，不可直接当作线上migration或对现有数据库重复应用。模型的部署权限、升级与回滚必须由后续实施处理。

应用验收继续按[验证计划](../detailed/validation-plan.md)执行，所有真实网关/数据库/ChatGPT场景仍需记录实际证据；本轮不将契约检查计入AC或A编号的端到端通过率。

## 6 维护规则

字段、枚举和必要状态组合以本目录机器契约为准，权限、生命周期及原生事实的解释由领域设计约束；出现矛盾时先修契约，不能由实现选择更宽松的一方。

输入、输出、操作模板、范围或安全限制改变时，更新对应工具/Schema版本、正反样例、相关文档和ChatGPT快照回归。影响旧批准语义时终止旧Action并新建计划，不迁移决定。

候选工具版本只改变管理head revision，发布才切换活动版本及授权epoch。数据Schema通过、元数据写入成功、ChatGPT平台确认和企业他人审批是不同事实，任何一个都不能替代其余检查。

本目录使用工具依赖只服务设计验证，具体业务工具仍是六个假数据候选；生产资源、数据权限、企业身份和通知平台不从样例自动生成或开放。
