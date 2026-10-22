# 受控工具目录与发布设计

版本：v0.1 详细设计。日期：2026-10-22。

首版已确认采用受控工具目录与风险分级。本页将六个假数据候选整理为可校验配置，服务于设计评审和沙箱夹具；具体生产工具、数据范围与审批组仍需登记和授权。

配置见[演示工具包](../specs/demo-tools.yaml)，形状契约见[工具包Schema](../specs/tool-bundle.schema.json)。原生执行语义见[适配设计](native-execution.md)，公共结果见[调用契约](../contracts.md)。

## 1 工具包与运行边界

包格式固定 schemaVersion="1"，bundleVersion 标识不可变候选版本，fixtureOnly=true明确其演示用途。注册资源、原生模板、凭据及策略由网关维护，工具包不包含生产地址或秘密。

单工具Schema稳定引用为 tool-bundle.schema.json#/$defs/toolDefinition，可供管理API复用。配置形状限制支持的适配器和operation类别，不构成通用命令、SQL或URL导入入口。

公开工具名统一为1至64个ASCII字母/数字/点/下划线/连字符，首字符为字母；同一名称边界用于工具包、ActionView和API，资源别名与operation ID另有独立范围。

包内scopeBinding从网关已验证的授权映射读取scopeId；不是直接相信模型参数或外部token中的任意组字符串。模型输入不得包含scopeId、资源连接配置、凭据、审批状态或执行身份。

## 2 六个工具候选

| 工具 | 已固定原生操作 | 输入 | 交互及影响 |
|---|---|---|---|
| pg.get_demo_item | 固定读取模板，最多一条 | itemId | READ；授权后BOUNDED_WAIT |
| pg.update_demo_note | 固定更新模板，匹配旧版本，最多一行 | itemId、note、expectedVersion | WRITE；CONTROL_FLOW，合格他人审批 |
| redis.get_demo_note | GET；scope与note组成完整key | noteId=welcome/help | READ；不接受原生key、命令 |
| redis.set_demo_note | SET key value EX ttlSeconds | noteId、value、ttlSeconds | WRITE；显式TTL，不接受其他选项 |
| directory.get_demo_office | 固定GET /demo/offices/{officeId} | officeId=office-demo | READ；仍核验办公地点可见范围 |
| inventory.reserve_demo | 固定POST /demo/reservations与请求模板 | itemId、quantity | WRITE；修改演示库存预留状态 |

所有条目的riskTier为CONTROLLED，表示目录内且受资源范围约束，不能解释为任意读取都低风险。未知operation、未评审模板和高危操作不因工具包形状合法而获得执行资格。

## 3 输入与可信范围合并

每个inputSchema均以object为根、明确required并设置additionalProperties:false。枚举、长度、数量和TTL范围先做类型校验，再做资源及数据授权；Schema合格不证明有权。

PG模板的绑定顺序与允许列由模板注册表定义。演示读取条件为scope_id与item_id；更新再绑定expectedVersion并校验实际影响行数，不能让模型替换条件或模板。

expectedVersion与成功newVersion使用十进制字符串，演示范围小于10^18，能精确绑定PostgreSQL bigint且避免JavaScript整数精度损失。更新输入排除范围最大值，为version+1保留空间；不接受JSON number、负值或带符号/小数的文本。

Redis key模板为demo:{scopeId}:notes:{noteId}。scopeId先符合注册映射的允许字符，noteId只取welcome/help，再编码并构造完整key；ACL与adapter检查同一完整key。

固定SET EX示例将TTL限定为60至3600秒、文本最多1024字符，并限制实际UTF-8字节数。返回ttlSeconds表示确认写入时使用的过期参数，不是稍后观察到的剩余TTL。

HTTP origin与认证来自resourceBinding解析的注册资源，operation只声明管理员审核的method、pathTemplate及请求模板引用。模型没有url、method、headers或scopeId输入字段。

HTTP路径参数分别编码；预留请求的scope由可信映射加入已登记body模板。若业务API不能约束该对象/租户范围，则该工具与授权组合不可发布。

template://与request://是受控注册表引用，不是允许从网络或文件系统任意加载内容的地址。模板绑定失败、字段冲突或范围不可强制时拒绝发布/执行。

## 4 成功数据与公共结果

每个工具通过outputSchema引用同目录tool-result.schema.json，表示公共ToolResult契约；resultDataSchema再约束成功分支的业务data。

发布器离线解析受信本地引用，生成客户端能消费的完整Schema；不把本地文件路径直接交给ChatGPT下载。不能将引用解析扩大为任意远程URL抓取。

成功数据必须同时通过公共结果Schema与resultDataSchema。仅在requestStatus=SUCCEEDED的完成分支施加业务data约束，不能把待批DeferredActionResult当作缺失成功字段。

| 工具 | 成功data字段 |
|---|---|
| PG读取 | found、item；未找到时item=null，找到时含itemId/note/version |
| PG更新 | itemId、newVersion、affectedRows=1 |
| Redis读取 | found、value、encoding；空字符串与不存在分别表达 |
| Redis写入 | noteId、valueStored=true、已使用ttlSeconds |
| 办公地点读取 | officeId、displayName、timeZone |
| 库存预留 | reservationId、itemId、quantity、state=RESERVED |

各成功对象及嵌套对象均拒绝额外字段，避免把原生响应整体透传成模型数据。输出映射只取已声明字段，认证头、内部连接信息与未授权业务字段不进入data。

schema校验失败若发生于原生成功之后，记录FAILED/APPLIED及受控证据，不自动再写一次；输出校验不能撤销已发生效果。截断需按公共metadata说明，不能静默生成Schema不一致的成功对象。
所有非成功FINAL的data必须为null，诊断与部分效果详情转入safeError和受控evidenceRef；不能因变为FAILED就整体透传原生响应。

## 5 语义、注解与时间限制

纯业务读取标readOnlyHint=true、destructiveHint=false、idempotentHint=true；必要审计记录不改变其业务只读分类。模板函数、视图或GET接口须先证明其实际边界。

创建待批写Action属于业务工作流变更，标readOnlyHint=false。三个写工具的idempotentHint=false：重复新调用会创建新Action，可能再次改值、刷新TTL或预留库存。

本包写操作会覆盖记录、替换值或改变可用库存，destructiveHint按此填写true；封闭演示资源的openWorldHint=false。注解只是客户端提示，不能代替网关权限、风险分类和他人决定。

timeoutMs限制一次原生执行，不是等待人类审批的期限。read采用BOUNDED_WAIT，write采用CONTROL_FLOW；Action业务有效期、执行hard deadline和HTTP流预算分别管理。

本包3000/5000毫秒、16KiB结果、TTL及数量上限全部是夹具值，不是生产默认值或容量承诺。工具包Schema的上界只是该候选配置profile，生产调整须版本化并回归。

## 6 静态校验与发布流水线

1. 解析严格JSON/YAML并验证工具包Schema，拒绝重复键和重复工具名/版本；不自动忽略未知配置字段。
2. 对inputSchema、resultDataSchema执行离线Draft202012Validator.check_schema，拒绝不受信外部引用。
3. 校验输入required均有定义；可信字段不能成为模型输入，模板占位符必须来自明确输入或scopeBinding。
4. 解析资源、operation、模板与策略版本，验证adapter匹配、可强制数据范围、读写语义和最小权限后端身份。
5. 编译成功结果Schema；检查注解、交互模式、超时与限制一致，不能将WRITE改为READ绕过审批。
6. 跑正反输入及成功数据样例，再用真实测试PG/Redis和可控HTTP验证实际语义、效果次数和未知窗口。
7. 生成不可变候选版本及摘要，经受权发布流程原子切换，并记录审计及受影响Action处理。
8. 更新ChatGPT应用快照并在新对话回归，保留平台刷新版本与结果；未刷新不放宽服务端校验。

工具包形状验证不证明SQL只读、HTTP幂等、Redis ACL范围或审批有效，这些由operation评审、原生权限和真实服务验收共同确定。

## 7 正反样例契约

examples只供离线验证，不进入MCP公开工具定义。四个数组约定如下：

- validInputs：必须全部通过本工具inputSchema。
- invalidInputs：必须全部被拒绝，包括额外scope/approved/原生命令字段及越界值。
- successDataSamples：必须全部通过本工具resultDataSchema。
- invalidSuccessDataSamples：必须全部被拒绝，包括额外敏感字段与错误数值类型。

每个工具均含至少一个正例与反例；不能把“所有样例被解析”当成正反断言通过。示例不携带真实身份，权限与跨用户拒绝仍需集成夹具覆盖。

输入示例（无resourceId、scopeId、原生key或密码）：

```json
{"name":"redis.get_demo_note","arguments":{"noteId":"welcome"}}
```

```json
{"name":"pg.update_demo_note","arguments":{"itemId":"item_demo_1","note":"新演示文本","expectedVersion":"1"}}
```

## 8 版本变更与ChatGPT快照

输入字段、operation模板、scope绑定、执行身份、结果字段或安全限制变化，必须新版本并评估旧计划；影响批准语义时终止旧Action，创建新计划重新授权，不迁移旧决定。

ChatGPT共享应用采用已发布工具快照，不能假设按员工实时隐藏所有元信息。发布最小稳定名称与Schema，不嵌入敏感资源枚举；严格隐藏需求按权限组配置应用并实测。

网关tools/list过滤与调用逐次鉴权保持有效，但list_changed、ttl或订阅不能替代ChatGPT管理员refresh。旧Schema请求明确失败，不以扩大输入兼容性维持表面成功。

首版读取与他人批准写闭环分别验收。ChatGPT界面自身确认不构成企业另一位审批人的决定；action.status/resume/cancel的作用与权限见[传输设计](transport-connector.md)。

验收关联AC-01/04/05/06/08/12/13及[验证计划](validation-plan.md)；本轮仅提供配置提案与离线契约，不声明真实平台、驱动或生产工具已经接通。
