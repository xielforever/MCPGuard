# ChatGPT HTTPS / OAuth 本地联调

这是隔离测试身份服务，用于验证 ChatGPT → HTTPS → Streamable HTTP → MCPGuard → 原生 PG。企业 IdP、组同步和生产 SSO 仍待接入。原有 `scripts/dev.py` / `make dev-*` 的 `dev-tokens` HTTP、stdio 测试方式继续保留。

本机实际运行、真实 ChatGPT 读取、审计与当前答复展示阻塞见[2026-10-22 联调记录](validation/chatgpt-oauth-read-2026-10-22.md)。

## 启动隔离夹具

在有 Go 1.26.8、Python 3、Docker 的 Linux / WSL 环境运行：

```bash
python3 scripts/oauth_dev.py up
```

创建独立 `mcpguard-oauth-postgres`，复用固定 PG 16.14 镜像和现有迁移；单独生成角色凭据、加密密钥和三个测试主体。默认 OAuth 网关只监听 `127.0.0.1:18081`，不修改旧夹具的监听地址或数据。`.dev/oauth` 保存秘密，必须保持仅当前用户、管理员和系统可读。Linux 原生权限为 0700/0600；Windows/WSL 的 NTFS ACL 需在宿主核实，不能只依赖 chmod。

## 临时 HTTPS 入口

从 [Cloudflare 官方发行页](https://github.com/cloudflare/cloudflared/releases)取得 cloudflared，核对发行资产 SHA-256。只转发 OAuth 网关的回环端口：

```bash
cloudflared tunnel --no-autoupdate --url http://127.0.0.1:18081 --http-host-header 127.0.0.1:18081 --protocol http2
```

取得临时 `https://….trycloudflare.com` 域名后，在另一个终端配置并启动：

```bash
python3 scripts/oauth_dev.py configure --origin https://实际临时域名.trycloudflare.com
python3 scripts/oauth_dev.py run
```

`--origin` 不能带尾斜杠。入口必须转到 18081，不得转发旧 dev-tokens 网关、数据库、Docker socket 或管理服务。HTTP 后端只监听显式回环 IP；HTTPS 在 Cloudflare 边缘终止并经隧道到本机。`--http-host-header` 固定后端 Host 为回环地址，兼容 SDK 的本地监听 Host 检查；不关闭 DNS 重绑定保护。OAuth issuer/resource/回调与浏览器 Origin 仍取显式外部 HTTPS origin，不信任转发头推导地址。

[Quick Tunnel 不支持 SSE](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)。此联调明确采用 MCP Streamable HTTP 的 JSON POST 响应；可选 GET SSE 返回 405。不能据此声称已验证 SSE、服务端通知或持续流。需要这些能力时，换成支持 SSE 的命名隧道/HTTPS 反向代理，再单独验证 transport 和流式 request-ID 映射。

## OAuth 契约

- Protected Resource Metadata：`/.well-known/oauth-protected-resource/mcp`，根路径别名亦可发现；固定 resource 为外部 HTTPS `/mcp`。
- Authorization Server Metadata：`/.well-known/oauth-authorization-server`；固定 issuer 为外部 HTTPS origin。
- 固定 confidential client；支持 `client_secret_basic`、`client_secret_post`；不启用开放注册或匿名映射。
- Authorization Code + 强制 PKCE S256；精确回调匹配；浏览器登录和显式同意；Secure / HttpOnly / SameSite cookie、CSRF 和 Origin 检查。
- 唯一 scope 为 `mcp:read`；授权和兑换绑定同一 resource。token 请求省略 resource 时继承授权绑定；指定其他 resource 则拒绝。
- 高熵不透明 access token 10 分钟、单次 code 2 分钟、refresh family 8 小时；续期旋转，refresh 重用及撤销使 family 失效。所有令牌仅保存摘要。
- OAuth 授权状态限单实例内存；重启会撤销全部授权，需重新连接。数据库里的业务 Action、Attempt、Execution、审计继续持久化。
- 用户名为 alice / bob / visitor；密码是本机随机生成的 32 字节测试秘密，不能换成人工弱口令或企业密码。服务端配置只存密码和 client secret 的 SHA-256。
- 每次 MCP 请求仍从元数据读取 enabled、期限、权限与数据范围；领取执行时继续在事务中复验。客户端不能通过 OAuth 参数或 MCP 参数选择 scope_id。
- 此公开 profile 不挂载 `/api/v1/audit` 管理读取入口；业务审计仍落库。

## ChatGPT 配置

在当前账号实际提供的 Plugins → Add → Add custom MCP server 中填名称、外部 HTTPS `/mcp` 和 OAuth。Advanced OAuth settings 选择预注册客户端；从 `.dev/oauth/credentials.json` 本地填入 client ID 和 client secret，默认 scope 设为 `mcp:read`。不要上传配置文件或把秘密发到对话。

当前 AS 声明并返回 `iss`，默认配置 [官方文档](https://developers.openai.com/plugins/build/auth)所列稳定回调 `https://chatgpt.com/connector_platform_oauth_redirect`。如果实际 UI 显示其他回调，用 `configure --redirect 完整实际回调` 精确更新并重启网关；不使用通配符或放宽回调规则。

用 alice 的本地生成密码登录测试授权页，再安装连接。新对话显式选择该插件，要求调用 `pg.get_demo_item`，参数 `{"itemId":"item_demo_1"}`。预期 note 为 `Alice scope A`；记录真实工具结果与 actionId / attemptId / executionId，再核对 PG 审计。创建连接、安装成功和真实工具读取是不同验证阶段。

## 本地验证与停止

```bash
go test -race -p 2 ./internal/oauthfixture ./internal/runtimeconfig
python3 tests/oauth_e2e.py
# 已启动 HTTPS 服务时：
python3 tests/oauth_e2e.py --url https://实际临时域名.trycloudflare.com --output .dev/oauth/https-result.json
python3 scripts/dev.py test
```

独立 Python 检查覆盖发现、三主体 PKCE 登录、目录权限、真实 PG、数据范围、原生 bigint 字符串、会话隔离、关联审计、续期和撤销。HTTPS harness 通过不等于 ChatGPT 通过，真实客户端证据另记。

结束时停止 cloudflared 和 OAuth 网关进程即可关闭公网入口并使内存授权失效；保留数据库、日志和证据。临时域名重建后需重新 configure、重启网关并更新 ChatGPT 连接。不要使用 `dev.py down` 作为关闭入口的方法，该命令会删除旧夹具及其数据。
