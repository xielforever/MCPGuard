# 设计记录与原文保留清单

日期：2026-10-24（Asia/Singapore）。范围：本轮设计与开发计划更新。

## 1 用户决定与执行范围

本聊天先确认多资源和原生身份问题，继而确认资源自助申请与密文下载，最后明确“由 MCPGuard 服务端建立连接，终端只收到查询结果”。用户随后要求按该方案完善设计和开发计划、补全完整生命周期、保留原设计，先不写代码。

本轮产物是本目录七份 Markdown，以及原入口和相关设计末尾的日期化补充。不是新实现、运行部署或新业务验收。接口、状态和算法工程候选以拟议设计理解；企业 IdP、秘密管理产品和生产参数没有由本轮自行选型。

## 2 保留规则和设计优先级

原文按字节前缀保留，只在末尾追加有日期的适用范围和链接。原有 requirements、总体设计、领域契约、交付计划、详细设计、决策和 specs 说明均未删改原段落。

原 v0.1 机器契约、SQL 模型、运行迁移、演示工具和测试源文件未修改。旧验收、图片、失败现场及现有本地运行说明整体保留。本轮开始时已有未提交实现，不做清理、提交、合并或回退。

只有 [增量入口](README.md)列明的多资源、账号申请、密文、客户端和撤权目标由本增量补充；其余 Action、审批和原生执行不变量继续适用。旧文档中的“尚未实现”或历史客户端状态保持原日期，不改写成今天的事实。

## 3 变更前原文指纹

基线来自本轮修改前的完整工作区可见文件，共 206 个；不是以最后提交替代未提交内容。下表是只追加日期化补充的 24 份原文，字节长度和 SHA256 用于核验变更后仍包含完全相同的原始前缀。无需复制或移动原文件即可保留可核验的历史。

| 文件 | 原始字节数 | 原文 SHA256 |
|---|---|---|
| `README.md` | 3182 | `45a9e32b773b8263f924411c1e1a27ad75fab5029aac2d6ec9f921d60a59150a` |
| `docs/README.md` | 2989 | `f859c1a41f8151b9dc145d5869b696905f2781e7aacaeb6aaa9cea7a9ae95a0c` |
| `docs/contracts.md` | 17531 | `7d51dfb83286bfa0fbccbdbd40089f177ffa54cb7e3320638bee7dbc6b3c42fa` |
| `docs/delivery-plan.md` | 12692 | `4b7888b3dd96dfb53d4307b82770ccada2b91edd98a4ba877aa2a931f22615aa` |
| `docs/detailed/README.md` | 3766 | `d0fd7c2b1f03aca60288678e8f24cfae7f54b81cd55763c29c7189765068eebd` |
| `docs/detailed/api-contract.md` | 9233 | `c7e7c00f1fd40d0db7af646a13c8b8d1d6e6d0b6f2059f30bf31c4309d0c0129` |
| `docs/detailed/chatgpt-access.md` | 13291 | `14d22f1dce838e1b988e862bbde511ddb64b120b6adcd62dbe7770a25f53e61e` |
| `docs/detailed/console-workflows.md` | 15032 | `2daf1225a70cedc5fab5a15f294b0f25519a76c5226070ad14f52f5103bb3c7c` |
| `docs/detailed/decisions.md` | 6312 | `fe2ec3b34cca719cd7ca2b1a6e29451b2058f64879072accf70bae07b5e1fde9` |
| `docs/detailed/failure-runbooks.md` | 16382 | `390a49f1cd00870da651b579293a128c9501eb4133dbf2c15750dcb84eb7147a` |
| `docs/detailed/native-execution.md` | 20961 | `c4096ec42350a2b0e229e5188b4a8d1f70fd2b987a563fc461c26925950cf79a` |
| `docs/detailed/security-policy.md` | 19758 | `d09be9084eeb3b25cc2a2ed5ee6698ac88b9bc18528805418dff3c9e40459c60` |
| `docs/detailed/service-operations.md` | 15330 | `d59bf6f29aeedd3593ce3be4941f1f692b68a85b5612d88c9209fe7b7e81024b` |
| `docs/detailed/state-storage.md` | 17544 | `15d1290c7f9793341f1debf5ca461489b5072e8d404525dab8b52e86ed70353e` |
| `docs/detailed/storage-invariants.md` | 12333 | `e9288c26eb61b1e0a6f6afef19cdf6821c409b69f31df6ead747f47408e922cb` |
| `docs/detailed/tool-catalog.md` | 10366 | `29ebb8c07a4b5d93c1580a25b2b80bf729bff6decf17d6b032c87d63d6367095` |
| `docs/detailed/transport-connector.md` | 20798 | `a7220359446aa230864cfe42202f9aae1243d19311774dd9248b4fb7f63e0d6f` |
| `docs/detailed/validation-plan.md` | 19249 | `cf5af727d7f685983e5a966068931319510652ff03aaabe432a7b69bd7c23b83` |
| `docs/ide-access-compatibility.md` | 11806 | `f68b1e737fc0b9c43fc0045f186299852d8a112bc278fe39a05ebcd7f77425cc` |
| `docs/mcp-protocol-design.md` | 10303 | `718c3cd6105c2f465f052a1b35ac4e30c047219ae65d15ff9450b468d005594d` |
| `docs/mvp-design-discussion.md` | 15983 | `8fa1c122e472a69eee6a5d85579e72df6e0e3677edbdd51d7f9dc2c6c38846f0` |
| `docs/native-adapter-design.md` | 9287 | `39dfa2ab390a03f580c67743a9a449a623d9ff07f6163c1df6748d6d6ff1321c` |
| `docs/requirements.md` | 11232 | `59d4761aa37d3e460a6455a33df372aaccc2a2d0eb13b1d62675a3daf297953d` |
| `docs/specs/README.md` | 7415 | `a62182b96d0ce32ff4e83c0aa528acd9ccf3ff1a09a296a6946c7be83616b61d` |

## 4 文档验证边界

本轮检查限定为文档内部链接、Markdown 基本结构、需求/阶段/工作项/验收追踪、原文前缀和非文档文件指纹。临时基线保存在工作区外的任务临时目录，不写入 Git 或记忆库。

旧 specs 验证器的依赖在所探测的 WSL Python 中不完整；本轮未安装依赖，也不以旧机器契约检查替代新设计验收。原机器契约保持原字节，后续 RA-01 建立新版本和相应校验。未运行 Go、数据库、浏览器或 MCP 业务验收。

业务测试矩阵保持 NOT_RUN；文档检查的结果不能升级为功能实现、原生客户端通过或人工体验验收。

## 5 技术依据

以下为解释信任边界和后续实现选择的主要资料；资料可支持工程设计，不替代本项目测试。

- [MCP 授权规范](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)：入口身份与下游独立凭据。
- [MCP 安全规范](https://modelcontextprotocol.io/docs/2025-11-25/tutorials/security/security_best_practices)：逐请求认证，不能以会话 ID 代替身份。
- [MCP 工具结果](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)：资源链接不等于客户端安装本地凭据。
- [OpenAI 官方 MCP 文档](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)：本地 HTTP helper 与缓存边界；具体客户端仍须实测。
- [OWASP 加密存储](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html)：成熟认证加密、密钥隔离。
- [Vault 数据库凭据机制](https://developer.hashicorp.com/vault/docs/secrets/databases)：账号提供方、动态身份与租约的可复用模式，未指定其为必选产品。
- [PostgreSQL 会话控制](https://www.postgresql.org/docs/16/functions-admin.html)：取消/终止信号与确认结果的区别。



## 6 本轮文档核验结果

2026-10-24：PASS_DOC_CHECK。新增 7 份设计文档，24 份原文只追加日期化补充；24 份原始字节前缀逐一核对一致，其余 182 个原有文件 SHA256 保持不变。全部 31 个新增/补充文档范围中，102 条本地链接可解析，代码围栏闭合、无新增行尾空白，git diff --check 通过。

追踪核对：14 项 RA-REQ 需求、19 个 RA-00 至 RA-18 工作项、24 项 RA-AC 场景无重复或未定义引用，每项需求均有验收映射。检查未发现本轮非文档修改或范围外新增文件。

本结果仅代表文档结构、关联和历史保留检查。业务实现仍为 NOT_STARTED，业务验收仍为 NOT_RUN；没有执行迁移、签发凭据、访问业务数据库或重启服务。Mermaid 围栏经过结构检查，未将其当作实际客户端或业务流程渲染验收。

## 7 后续实现与保留记录：RA-01/02/03（2026-10-24）

用户随后明确授权开始 RA-01，再实现 RA-02/03 并本地验证。前文的文档核验结果、原文指纹及当时未实现状态保留；新的当前状态见[开发计划第 8 节](development-plan.md#8-后续实现记录ra-0102032026-10-24)与[实现验收记录](../../validation/local-ra01-03-2026-10-24.md)。

开发前将当前工作树 213 个非忽略文件归档至工作区外，SHA256 为 `90ca9b137471dd9dc7af32257f11ce9414f68e8b058b5271e78fcf86f18308f5`。本次不从旧提交覆盖既有未提交代码，不改写旧迁移和历史验证；新目录独立迁移，文档追加当前记录。

实现过程的三组中间失败场景 `4ce06fa3f7`、`f665bffa19`、`16a05d72bd` 及日志保留为失败；最终在新场景 `ef4fde51ae` 通过本地验证。未重启或部署原本地服务，未签发真实凭据。来源、保留校验及分层证据见[机器记录](../../validation/local-ra01-03-2026-10-24.json)。


## 8 后续实现与保留记录：RA-04（2026-10-24）

用户继续授权后的交付见[本轮验收](../../validation/local-ra04-2026-10-24.md)。开发前固定 241 个非忽略文件，归档 SHA256 为 30f07463d77ba5c9cdbce1203956ffc971be0764d4863ba26494f2590bf48f6a。原验收和旧迁移原字节保留，设计/计划仅追加记录。原 0.2.0 规范源及生成物见[历史快照](../../specs/v0.2/history/0.2.0/README.md)，不参与当前编译。

启动阶段遇到 WSL 重启及 /tmp 配置失效，失效场景不计业务通过；随后改用持久私有目录并记录最终分层结果。本任务没有主动重启或部署原服务，不把环境恢复记作连续运行证明。中间场景、最终源指纹与保留校验见[机器记录](../../validation/local-ra04-2026-10-24.json)。


## 9 后续实现与保留记录：RA-05（2026-10-26）

本轮按计划实现受限提供方和实际账号核验，见[验收](../../validation/local-ra05-2026-10-26.md)。开发前工作树 262 个非忽略文件已归档到工作区外，SHA256 为 `3012ce49dd07b9285c812d195befffe11b7df9d25144a506e2a0176404f9d759`。原 RA-01/02/03、RA-04、其他历史验证及迁移 001/002 保留原字节；旧设计和计划仅追加本轮记录，0.2.1 规范源/生成物/DTO 按字节[归档](../../specs/v0.2/history/0.2.1/README.md)。

预备场景 9af61c3b82、失败场景 3a1f6b0dca、定向验证场景 87bb60c295 分别保留。首轮暴露激活约束别名歧义，修复后进一步修正测试行 ID 和升级夹具连接/运行权限配置；失败日志不改写为通过。最终独立新场景 0e73d8c4ef 完成全量及进程验证。只停止本轮拥有的夹具，不删除数据库/容器/卷；本轮未部署代码或主动重启现有本地服务，未把运行环境恢复当连续可用性证明。


## 10 后续实现与保留记录：RA-06（2026-10-26）

本轮保留 283 个开发前非忽略文件的工作区外归档，SHA256 为 `8e400e3a8fb4a896b2e28850ebd7c33753784b443dabdd20e214a903880f64ff`。原设计/计划仅追加记录，旧资源迁移 001/002/003、原生提供方 001 及全部旧验证原字节保留；0.2.2 的源/DTO/生成物另行[归档](../../specs/v0.2/history/0.2.2/README.md)。

失败场景 0b379b061b 保留了首轮新迁移的 CASE 表达式解析失败；c3dc997195 保留 UNKNOWN 结果未被旧激活更新语句覆盖的失败，修正后定向验证通过；cfc1d41e02 保留并发观察连接缓存统计快照导致的测试误判，改用独立观察连接后通过。dd9f14bed1 为进一步边界验证，最终 b45d2be58f 完成全量及进程检查。失败日志没有改写为通过，场景、数据库和容器保留。本轮未部署到现有服务，详见[验收记录](../../validation/local-ra06-2026-10-26.md)。


## 11 后续实现与保留记录：RA-07（2026-10-26）

开发前 303 个非忽略文件在工作区外归档，SHA256 为 `cfd581f69bf742d20a8ef74e1816aaeb1806cc0db8e7b4297dd93a3864da73d4`。原文只追加；既有 75 份验证文件、资源 001 至 004、原生 001/002、元数据 1 至 4 和 go.mod/go.sum 原字节保留。0.2.3 的源、DTO 和生成物六份另见[归档](../../specs/v0.2/history/0.2.3/README.md)。

da83600c9f 为首轮编译及服务端定向验证场景；71fedf2c50 保留全量中 v4 测试恢复 GENERATED ALWAYS 审计序号失败的原始日志，测试改为显式 OVERRIDING SYSTEM VALUE 后定向通过，该场景还完成 101 项进程验证。最终 d6c5efcf79 使用新私有密钥和新数据库完成全量回归与进程验证。首次失败未回写成通过，三个场景及日志均保留，结束时只停止本轮自建容器，现有服务没有部署变更。见[本轮验收](../../validation/local-ra07-2026-10-26.md)。


## 12 后续实现与保留记录：RA-08（2026-10-26）

开发前 327 个非忽略文件已归档，SHA256 为 `2802e66c0d5dd85bb7c8c1573152b703c984062ded0ebb4aa617937eb76c35c2`。原 78 份验证、资源 001..005、原生 001/002、元数据 1..4 共 11 迁移及 go.mod/go.sum 原字节保留，既有文档只追加；0.2.4 六文件另行[归档](../../specs/v0.2/history/0.2.4/README.md)。

首轮 9cc7b9fbdb 保留测试私有目录权限未显式设置、原生账号表无 org_id 导致库存查询失败、重复测试固定 token 摘要冲突，以及故意重启后旧浏览器会话被拒绝的原始失败记录。修正测试隔离和重新认证方式后定向及 117 项进程检查通过；该场景的首次全量 246 通过、两项失败（含父测试）仍原样保留。最终 b8f7bbc435 使用全新状态完成 250 Go 与全部进程验证，没有回写首轮失败。场景、密文、私有文件和数据库日志保留，结束仅停止本轮容器。见[验收](../../validation/local-ra08-2026-10-26.md)。


## 13 后续实现与保留记录：RA-09（2026-10-26）

开发前 351 个非忽略文件已归档，SHA256 为 `86881972f0188207ac021dc4e2f06d226b3772b67c5684e8b5b93abe55a1188b`。原 81 份验证、资源 001..006、原生 001/002、元数据 1..4 共 12 迁移及 go.sum 原字节保留；既有文档只追加。go.mod 仅将原有版本的 x/sys 标为直接依赖，用于 Windows 系统安全 API，没有升级版本。0.2.5 六文件另行[归档](../../specs/v0.2/history/0.2.5/README.md)。

cae7ab95e5 保留初始定向失败及修复复验；3a63fc19aa 保留旧读取回归与新开通进程误用同一场景导致状态互相干扰的全量/进程失败，以及手动响应新增指针字段导致的幂等相等断言回归。后续手动模式保持原响应，验证改为新场景顺序执行。02afc88ad9 保留进程夹具的方法参数遗漏、撤销包被映射为依赖故障导致同步中止的失败，并保留修正后 100 项进程通过。最终 cdc8adcaca 使用全新状态顺序完成验证，未回写先前失败。Windows 初次命令参数问题及原始日志也保留，最终原生 7 项检查单列。场景、包、私有缓存、日志与数据库均保留，结束仅停止本轮容器。详见[验收](../../validation/local-ra09-2026-10-26.md)。


## 14 后续实现与保留记录：RA-10（2026-10-26）

开发前 382 个非忽略文件归档 SHA256 为 `c86d5543fef075aef05d3b803110167f222705daccb40d5fa7df2fbfcf632463`。原 84 份验证、13 份迁移及 go.mod/go.sum 原字节保留，既有文档只追加；本轮不新增迁移。0.2.6 六文件原字节另行[归档](../../specs/v0.2/history/0.2.6/README.md)。

初始 1d9b15545d 保留网络夹具持续截断替代 claim 导致无法恢复的失败，以及短 TTL Context 在关闭断言前已自然 EXPIRED 的原始失败；夹具改为只中断一次，并显式确认被关闭对象仍 ACTIVE 后进入 REVOKED。修正后 122 项通过。最终 6b4cc1c147 使用全新状态顺序完成 286 Go、130 新流程及全部旧流程，新增实际 worker 保持缓存头续期的证明。原失败未覆盖；两个场景的缓存、包和日志保留，收尾只停止本轮容器。见[验收](../../validation/local-ra10-2026-10-26.md)。
