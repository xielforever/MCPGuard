# 管理 API 与契约评审规范

版本：v0.1 设计补充。日期：2026-10-22。接口尚未实现，以下请求仅供评审和后续代码生成。

机器可读核心子集见 [OpenAPI](../specs/openapi.yaml)。ActionView、ToolResult和错误复用独立JSON Schema，避免前端、MCP与管理API各自定义状态。
本文件补充身份、版本、幂等和审批语义；Schema校验不替代对象授权、计划绑定或数据库事务。

## 1 接口范围与身份

统一管理前缀为 /api/v1；MCP客户端使用独立 /mcp，显式续传调用注册工具action.status、action.resume、action.cancel。
本子集不新增REST resume，也不向模型提供提交人类批准的MCP工具。

浏览器使用经企业登录建立的服务端会话，cookie候选名为mcpguard_session；写请求验证Origin与X-CSRF-Token。
OpenAPI不虚构企业IdP或授权端点，也不假定网页cookie、OIDC ID token与MCP access token可以互换。
服务身份管理访问如需开放，应另定受众、scope及撤权契约，不能直接复用全员共享管理员token。

Principal与审批人均由服务端验证产生；请求不能提交approverId、角色或组来授予自己资格。
知道UUID、URI、planHash或曾看过某个工具，不构成访问资源、审批或执行结果的授权。
API请求requestId仅用于诊断关联，不等于业务Action或MCP Attempt；受理前拒绝不虚造这些记录。

## 2 对象版本和修改前提

资源、工具head和Action读取返回强ETag，拟议格式为带引号的"rev-7"，对应正整数revision。
PATCH资源、创建工具版本、发布工具、取消Action和提交决定要求If-Match；首版只接受单个强ETag。
缺少条件返回428，格式错误400，旧版本412；这些路径不能提交目标变更。
应用服务以数据库条件更新保证“比较与写入”原子；不能在handler先GET再无条件UPDATE。

审批决定同时携带actionRevision与planHash，前者必须与If-Match指向同一Action版本，后者绑定完整不可变计划。
发布版本不可原地修改；变更操作、目标或批准语义时终止旧Action并重新计划，不迁移旧决定。
命令响应是事务提交时的快照；它可能在送达前已过时，需要当前状态时重新GET，不假定响应仍是最新版本。

## 3 管理命令幂等

创建、局部修改、版本追加、发布、决定、取消和只读核对申请使用Idempotency-Key。
唯一范围为组织、实际主体、命令类型和目标；创建命令的目标为对应集合，不以客户端自报主体划分。
请求摘要覆盖规范化业务体、路径目标与影响语义的版本前提；相同键不能换If-Match来执行另一条命令。

同键同请求在重新鉴权后重放原状态码、响应体及提交快照；同键异体返回409。
先做当次认证、对象授权和必要CSRF检查，再查去重并比较原请求；只有新命令才执行当前If-Match前提。合法重放不因原命令已推进revision而变为412；请求摘要不包含token、cookie或可轮换CSRF秘密。
同键仍在处理中时返回明确冲突/处理状态，不另取执行权；唯一约束裁决并发首用。
幂等记录、业务修改和关键审计同一本地事务；完成结果不包含秘密，敏感响应通过受控引用保存。

响应的Idempotency-Expires-At说明管理命令去重保证期限，不等于Action期限或数据留存期限。
在保证期内确认重试管理命令时沿用原键、原体和原条件；不能新建随机键来绕过不确定结果。
过期或记录清理后不能保证识别旧键，客户端先查当前对象，由人明确是否构成新意图，不能静默危险重放。
管理命令幂等不证明PG、Redis或业务HTTP写可重放；未知原生结果始终遵守单次领取与只读核对规则。

## 4 核心接口与读取权限

| 资源组 | 核心接口 | 约束 |
|---|---|---|
| 当前身份 | GET /me | 仅返回当前主体及受权能力 |
| 资源 | GET/POST /resources，GET/PATCH /resources/{id} | 注册时停用；端点与凭据为受控引用，无明文秘密 |
| 工具 | GET /tools/{id}，POST versions、publish | head ETag、不可变候选、独立审核及发布事务 |
| 请求 | GET /actions、GET /actions/{id}、POST cancel | 本人或明确受权范围；当前状态不等于原生成功 |
| 审批 | GET /approvals、GET /approvals/{id}、POST decisions | 单级合格他人、完整受权审阅、决定不派发 |
| 执行 | GET /executions/{id}、POST reconciliation-requests | 结果逐次鉴权；核对只读，不重做原操作 |

endpointConfigRef必须先经受控导入或端点配置管理登记，credentialRef指向配置的托管机制；本API子集不涵盖全部托管接口。
ResourceCreate的enabled固定false，后续有版本的启用另行鉴权；注册成功不等于连接已验证或工具已发布。
列表按主体过滤，使用有界pageSize和不透明cursor；cursor绑定过滤、排序、范围及视图版本，不能传给另一员工复用。
审批资源首版是Action视图，approvalId映射Action ID，Decision有独立ID；不维护另一套可能分叉的待批状态。

## 5 审批详情与提交示例

GET /actions/{id}的ActionView不包含计划明文；GET /approvals/{id}在独立审批展示权限下返回reviewPlan。
reviewPlan来自服务端冻结计划，包含原操作者、工具/资源/凭据版本、后端身份、真实operation、参数、可信范围绑定、限制和前置条件。
原生operation可包含SQL模板与绑定、Redis argv或HTTP方法/路径/查询/请求体；始终不含凭据值、nonce或认证秘密。
parameters按注册工具inputSchema与审批字段权限校验，服务端scopeBindings单独存放；审批客户端不能提交替代plan、拼接新SQL或覆盖可信范围。
缺少查看审批相关敏感字段的权限时，不授予只看摘要的盲批；服务端返回拒绝或隐藏对象，决定接口也重查该资格。

以下ID和摘要仅是格式示例，不指向真实员工或有效批准：

```http
POST /api/v1/approvals/40000000-0000-4000-8000-000000000001/decisions HTTP/1.1
If-Match: "rev-7"
Idempotency-Key: 90000000-0000-4000-8000-000000000001
X-CSRF-Token: <由当前管理会话签发>
Content-Type: application/json

{
  "planHash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "actionRevision": 7,
  "decision": "APPROVE",
  "reason": "已核对固定目标、影响范围与版本前置条件"
}
```

服务端验证当前真实审批主体与操作人不同，处于合格组，且Action、计划、期限、版本和基础准入仍有效。
同一人点确认、ChatGPT自身确认、模型approved字段或通知送达均不能代替这一决定。
合格批准只使待批路径满足READY条件；回调不能领取执行权。CONTROL_FLOW需操作人后续以新RPC调用action.resume。
否决、到期、取消或高危DENY不能被普通批准覆盖；已消费资格不能因重复决定再次领取。

## 6 取消、结果与只读核对

未领取的取消以行锁/CAS终止路径并返回200；已领取的取消返回202，表示取消意图持久化。
202既不保证原生已停止，也不保证已回滚；真实后端效果可能为APPLIED、PARTIAL或UNKNOWN。
Execution记录仅证明执行资格与意图已登记；INTENT_RECORDED不等于DISPATCHED，更不等于提交成功。

执行视图可返回RUNNING；只有形成终态且调用者有结果权时才携带FINAL ToolResult。
FINAL的actionId、attemptId与executionId必须真实关联；数据被清理或无展示权限时不伪造结果，不改变副作用事实。
HTTP 200读取OUTCOME_UNKNOWN是正常查询成功；不能把它转成“已写成功”或“完全未执行”。

reconciliation-requests仅请求读取可信业务回执并追加证据，不执行原写操作。
当前值相同不必然证明某次Execution成功；没有可靠核对能力时明确保持未知，不让核对接口成为隐藏重试入口。
结果权限、审批展示权和审计权分别校验，审批人不自动获得全部业务结果。

## 7 错误与后续验证

400/422区分结构与业务语义；401/403属于管理主体；404可按对象策略隐藏存在。
409表示状态或幂等冲突，412/428表示版本前提，429返回有界等待提示，503表示必要依赖或命令结果无法确认。
错误统一复用ErrorEnvelope并脱敏，禁止直接输出驱动err.Error()、密码、连接串或内部认证头。
业务API的401/403在MCP工具结果中表达，不能冒充员工管理登录失效。

本轮只校验文档和机器契约；实现后需以真实元数据PG验证同键并发、If-Match、审批/取消竞争、恢复和对象越权。
OpenAPI Schema验证只覆盖结构；基础访问权、OTHER_PERSON、完整计划、状态迁移、数据库事务与真实副作用另验。
具体用例关联[验证计划](validation-plan.md)的V08–V12、V14–V19及[交付追踪](../delivery-plan.md)的A-02–A-05、A-09–A-11。
