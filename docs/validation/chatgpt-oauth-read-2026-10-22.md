# ChatGPT HTTPS / OAuth 真实读取联调记录

日期：2026-10-22。结论：**OAuth 与 HTTPS 已接入，ChatGPT 确实触发一次原生 PG 读取并持久记录成功；ChatGPT 答复展示尚未验收通过。**

本轮按用户要求新增独立 `oauth-fixture`，保留原有 `dev-tokens` HTTP / stdio 测试。未替换企业 IdP，未部署生产服务，未宣称 SSE 或完整 P1 交付完成。验收基线为 `c4e56d98852e48148afc5a1519e373a0354b0118`，验证对象是当时未提交的 OAuth 工作区；验收后的仓库提交状态以 Git 为准。

## 已取得的证据

| 阶段 | 结果 | 证据 |
|---|---|---|
| OAuth 安全、运行配置和 RPC ID 保护 | 通过；11 个顶层测试、24 个包含子测试的 pass 事件，0 失败/跳过 | [安全测试](oauth-security-tests-2026-10-22.json) |
| 独立 HTTPS → OAuth → Streamable HTTP → PG → 审计 | 41 项通过；Alice / Bob / Visitor、隔离、续期和撤销 | [HTTPS 联调](oauth-http-https-2026-10-22.json) |
| 旧本地模式回归 | Go race 全套与独立 Python 48 项通过，HTTP 与 stdio 保留 | [回归输出](oauth-dev-mode-regression-2026-10-22.txt) |
| ChatGPT 发现与 OAuth 浏览器登录 | 实际网页创建并安装测试插件，发现 canonical resource/issuer，完成 Alice 授权，回到 ChatGPT | [真实客户端记录](chatgpt-oauth-read-2026-10-22.json) |
| ChatGPT 实际工具调用 | 平台展示工具与精确参数，经 Allow once 执行，PG 记录成功 | 同上，含执行结果与两条关联审计 |
| ChatGPT 可读最终答复 | **未通过**；原对话、刷新后和一次不重新调用工具的追问均显示 `This response couldn’t load` | [读取现场](chatgpt-read-ui-2026-10-22.png) |
| 不使用插件/工具的对照 | 同样显示 `This response couldn’t load`；不能把该症状归因于 MCPGuard | [对照现场](chatgpt-no-tools-ui-2026-10-22.png) |

测试源、工作区基线、二进制、SDK、Go、PG 镜像和 cloudflared 校验值见[来源清单](oauth-source-manifest-2026-10-22.json)。原有启动竞态修复及其证据文件保持历史内容，不重新标记为本轮证明。

## 实际客户端与连接

- 客户端：已登录 Chrome 中的 ChatGPT 网页；实际 Plugins → Add custom MCP server → OAuth → User-Defined OAuth Client → Create as a plugin。
- 插件：[MCPGuard Local Read](https://chatgpt.com/plugins/plugin_asdk_app_6ada2c1ed9e48191af1e36dd6e0c45e8)。这是当前账号的个人测试插件；未检查或宣称具体订阅、企业 workspace 发布、管理员资格。
- HTTPS resource：`https://maiden-assuming-arrived-admitted.trycloudflare.com/mcp`；issuer 为同一 origin。
- 认证：预注册 confidential client，ChatGPT 实际选择 `client_secret_basic`，PKCE S256，scope `mcp:read`。
- 回调：实际 UI 显示 `https://chatgpt.com/connector_platform_oauth_redirect`，与精确白名单一致。服务端广告并返回 `iss`，浏览器完成登录及回调后插件的 Continue connecting 提示消失，工具连接进入可用状态。
- 协议：网关仅支持并选择 MCP `2025-11-25`；本轮未采集客户端原始 initialize 请求的版本值，不宣称 ChatGPT 的其他 profile 或高级能力。
- 临时传输：Streamable HTTP JSON POST 响应；可选 GET SSE 明确返回 405。Quick Tunnel 不支持 SSE，相关能力另验。

流程依据 [OpenAI OAuth 文档](https://developers.openai.com/plugins/build/auth)与[自定义 MCP 接入说明](https://developers.openai.com/api/docs/guides/custom-mcp-server)，不能用 Codex、API 或独立 harness 的调用冒充网页 ChatGPT 验收。

## 唯一真实读取与数据库关联

在[真实读取对话](https://chatgpt.com/c/6ada2e60-429c-83e8-84d9-2fe41a987e7a)中，仅提供工具名 `pg.get_demo_item` 和参数 `{"itemId":"item_demo_1"}`，要求报告实际数据及关联 ID，没有把预期 note 或任何关联 ID 放进提示词。

ChatGPT 请求一次工具调用。检查平台 See details 显示的 `pg.get_demo_item` 与 `itemId=item_demo_1` 后选择 **Allow once**，没有改为永久允许。平台确认与企业授权是独立边界。

PG 持久结果和审计显示：

| 字段 | 实际值 |
|---|---|
| 主体 | Alice，`scope_a`，来自隔离测试身份映射 |
| item.note / version | `Alice scope A` / 字符串 `1` |
| requestStatus | `SUCCEEDED` |
| dispatchState | `DISPATCHED` |
| sideEffectStatus | `NONE`，业务只读；审计照常写入 |
| actionId | `1121e990-ba09-4da0-b9ea-5c7783e3f951` |
| attemptId | `ed356916-9492-4ac8-a99f-851720672dab` |
| executionId | `25297d15-0469-4391-844e-a149ea1fe9c3` |
| 原生结果 observedAt | `2026-10-22T15:41:36.192988516Z` |
| 审计 | seq `22` EXECUTION_INTENT → seq `23` SUCCEEDED，同一组三个 ID |

结果证据来自对该次已存执行结果使用现有 Vault 认证解密，包含完整原始业务结果；**并非从 ChatGPT 成功展示的答复取得**。同时验证只有一条 Execution、结果密文存在、两条审计关联一致。追问要求不再次调用工具；之后查询未出现新的业务 Action。

## 失败场景与修正

1. 首个临时入口透传公网 Host，SDK 对回环监听返回 `403 Forbidden: invalid Host header`。保留该隧道容器后停止它，新建入口加 `--http-host-header 127.0.0.1:18081`，不关闭 SDK 的 DNS 重绑定检查；公网 41 项随后通过。
2. 第一次真实浏览器表单返回 `origin is not allowed`。审查发现授权页 `Referrer-Policy: no-referrer` 会影响浏览器非 CORS 表单 POST 的 Origin；改为 `same-origin`，继续严格检查外部 origin，拒绝 `null` 和任意来源。该次请求的原始 Origin 头未采集，原因判断依据浏览器行为规则与后续复验，未伪造报文证据。[MDN Origin 说明](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Origin)
3. 浏览器还会对表单后续回调执行 CSP `form-action` 限制。授权页增加已预注册回调 origin，服务端对完整 redirect_uri 的精确检查继续保留；补充安全头回归后，浏览器成功回到 ChatGPT。
4. 首轮工具执行后，网页显示答复加载失败。先保存原始截图，再刷新同一条对话；未重新生成该次读取。追加“使用已有结果，不再调用工具”的追问也失败。
5. 新建[无工具对照对话](https://chatgpt.com/c/6ada2fc6-ba04-83e8-9e25-ac06e2711c65)，仅请求回复 OK，同样失败。浏览器还存在 Statsig 网络错误，但未证实它与答复失败的因果关系。具体 ChatGPT 故障原因未确定，不能将端到端用户展示标记为通过。

## 当前运行与恢复入口

原 `mcpguard-local-gateway` 继续监听 `127.0.0.1:18080`；新 `mcpguard-oauth-gateway` 监听 `127.0.0.1:18081`，独立 `mcpguard-oauth-postgres` 在回环映射 32772。`mcpguard-oauth-https-v2` 仅将 HTTPS 转发到新网关，没有挂载 Docker socket、数据库秘密文件或整个仓库。新网关只挂载二进制与含摘要的 OAuth 配置，环境变量仅含运行角色 DSN 和本地加密密钥。

最后检查：旧本地 `/healthz`、新网关回环 `/healthz` 和 WSL curl 访问公网 HTTPS `/healthz` 均返回 up。Windows PowerShell 的一次公网 HTTPS 探测出现传输 EOF，随后独立 WSL 探测成功；临时入口不据此保证长期或所有客户端的网络稳定性，也未将该 EOF 直接认定为 ChatGPT 答复错误的原因。

临时入口与 OAuth 网关在本次交付时保留运行，便于恢复客户端验证。关闭入口、撤销内存授权而保留证据和数据库：

```powershell
wsl -d Ubuntu-24.04 -- docker stop mcpguard-oauth-https-v2 mcpguard-oauth-gateway
```

重新启动同一隧道容器可能分配新域名；按[运行说明](../oauth-local-chatgpt.md)重新 configure、重启网关并更新 ChatGPT 连接。网关重启撤销全部内存授权，需重新登录。秘密保存在忽略提交且受 NTFS ACL 保护的 `.dev/oauth`，没有写入验收 JSON、截图或 Git。

下一次客户端验收在 ChatGPT 能正常显示无工具答复后，回到已保留的测试对话重新执行一次读取并核对新的三个 ID。当前未取得的用户可读结果不能由 PG 证据或任何其他客户端替代。
