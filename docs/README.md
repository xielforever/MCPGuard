# MCPGuard 设计文档 v0.1

状态：设计初稿，待评审。日期：2026-10-22。文档描述目标方案，不表示功能已实现。

实现入口：[最小读取闭环](read-slice.md)。该增量包含本地测试身份、两种 MCP 入口、真实 PG 读取和持久审计；下列完整企业方案仍按各文件的设计状态理解。

**MCPGuard 本身就是企业 MCP 网关。**支持 IDE 通过 HTTP MCP 直连，或通过本地 stdio 转 HTTP 程序连接同一网关；网关再通过 PG、RESP、HTTP/HTTPS 等原生协议访问后端。企业服务器与员工本地资源已可互通。

当前评审入口：[详细设计](detailed/README.md)，覆盖模块与API、身份策略、存储DDL、两入口、原生执行和验证。用户决定与剩余平台信息集中在[决策记录](detailed/decisions.md)。

可校验的[契约与配置](specs/README.md)已补充JSON Schema、OpenAPI、六工具包、SQL模型与复现命令；界面与处置流程见[控制台设计](detailed/console-workflows.md)和[故障手册](detailed/failure-runbooks.md)。

## 阅读顺序

| 文档 | 回答的问题 |
|---|---|
| [产品需求与验收要求](requirements.md) | 为谁提供哪些能力？哪些已确认、建议或待定？ |
| [系统总体设计](mvp-design-discussion.md) | 网关由什么组成？一次调用、授权和执行如何流转？ |
| [领域与调用契约](contracts.md) | 资源、工具、计划、决定和执行有哪些字段？输入输出如何表达？ |
| [MCP 接入与执行边界](mcp-protocol-design.md) | IDE 到网关的版本、身份、调用、取消和结果如何处理？ |
| [IDE 接入兼容方案](ide-access-compatibility.md) | HTTP 直连与 stdio 程序如何实现和分别验收？ |
| [原生服务适配](native-adapter-design.md) | PG、Redis、API 的连接、事务、命令和副作用如何约束？ |
| [交付计划与验收追踪](delivery-plan.md) | 按什么依赖交付？如何证明从 IDE 到原生执行的闭环？ |

## 状态约定

“已确认”表示已确定的需求或架构边界，不表示开发完成。“建议”表示本稿提出的实现选择。“待定”表示尚未决定，后续讨论应同时更新相关契约与验收。

旧 Go/Vue 代理脚手架仍保留，但新运行入口已经切换到读取闭环。企业身份、受控写、他人审批及其他原生适配须继续按新方案实现及验证。SDK 或沙箱协议测试不能代替真实 IDE 验收。

## 下一轮讨论

风险分级、受控目录和ChatGPT首发已确认。继续细化具体工具与数据范围、合格审批组、ChatGPT账号/工作区、企业身份和通知平台；元数据工程基线选用PostgreSQL，留存和容量参数在验收前校准。详见交付计划D-01至D-06。
