# MCPGuard 固定 HTTPS 入口

状态：`NOT_PURSUED_LOCAL_DIRECT_SELECTED`。用户于 2026-10-23 明确改为本机直连，停止推进域名方案。未创建命名隧道、DNS 记录或固定地址插件。以下仅保留之前的准备方案，不作为待执行任务；当前运行入口见 [本机服务](local-service.md)。

## 固定入口的范围

固定入口使用长期保留的同一个 Tunnel UUID 和 DNS 主机名。重启连接进程时执行 `tunnel run`，不再执行 `tunnel create`，也不使用生成随机域名的 Quick Tunnel。客户端继续通过 HTTPS `/mcp` 使用 Streamable HTTP。

运行容器只挂载单隧道凭据与配置，不挂载具有账户管理权限的 `cert.pem`、OAuth 密码/客户端秘密、数据库秘密、Docker socket 或整个仓库。只路由明确的子域名到 OAuth 网关 `127.0.0.1:18081`，固定回环 Host，其他主机名返回 404。[配置模板](../deploy/cloudflared/config.yml.example)

Cloudflare 必须已能管理该域名的 DNS；在更改现有记录前检查是否已有其他服务占用，不通过强制覆盖解决冲突。本流程不自动迁移主域名 nameserver。

## 接入顺序

1. 确认完整子域名及所在 Cloudflare zone。核对 DNS 记录和现有隧道，避免覆盖其他服务。
2. 使用 `cloudflared tunnel login` 完成账户授权，或由用户提供已有隧道的本机私有凭据文件。凭据不经聊天传输。账户管理证书与运行凭据分开保存。
3. 创建或复用专用命名隧道 `mcpguard-local-read`，保留 Tunnel UUID 及对应的单隧道凭据。只有初次创建时执行 `tunnel create`。
4. 将新子域名 CNAME 路由到该 UUID。使用 [Cloudflare 官方 CLI 流程](https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/create-local-tunnel/)；`tunnel route dns` 不带覆盖现有 DNS 的选项。
5. 在忽略提交、权限受限的 `.dev/https-fixed/` 保存实际配置与 `tunnel.json`，替换模板中的示例域名和 UUID；使用 `cloudflared tunnel --config <config> ingress validate` 和 `ingress rule` 检查选路。
6. 运行专用 `mcpguard-https-fixed` 连接容器，使用 `--restart unless-stopped`、只读凭据和现有已核验的 cloudflared 二进制；不触碰其他项目的网关、数据库或服务。
7. 固定 HTTPS 健康检查正常后，备份 OAuth 配置，将 `public_origin` 一次性切换为固定 HTTPS origin，保留原测试客户端、主体、精确回调及权限。重启 OAuth 网关使该配置生效。
8. 完成 HTTPS/OAuth/PG/审计检查，再创建指向固定地址的插件并连接一次。现有平台不支持直接修改已注册 MCP server 的 URL，因此这次从临时域名迁移到固定域名需要一次替代连接；保留旧连接及其历史证据。[平台限制](https://developers.openai.com/plugins/deploy/submission)
9. 使用实际已注册的固定地址插件调用 `pg.get_demo_item`，核对真实 PG 结果及对应审计。随后只重启 `mcpguard-https-fixed`，再次使用同一个插件调用并核对审计，验证无需更换地址或重新授权。记录两次原生结果、Tunnel UUID、DNS/HTTPS 与网关进程状态。
10. 固定入口验收通过后，停止原 Quick Tunnel 容器，保留其配置、容器、失败记录和验证证据。

## 重启边界

- 只重启命名隧道：固定域名、Tunnel UUID 和 OAuth 网关进程保持不变；预期无需重建插件或重新登录，必须以同一个实际插件前后两次读取验证。
- 重启 OAuth 测试网关：当前授权仍存在内存中，全部 grant 会失效；固定域名不变、插件无须重建，但需要重新登录。这是现有测试身份实现的边界，不能通过关闭过期/撤销校验来规避。
- 重启整套 WSL/Docker：还须检查数据库映射端口、网关 DSN、Docker/WSL 启动和主体有效期。本次固定隧道入口不能代替整套服务的重启恢复验收。

单隧道凭据只允许运行指定隧道；账户证书能管理账户下的隧道，两者不可互换。[官方权限说明](https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/tunnel-permissions/)
