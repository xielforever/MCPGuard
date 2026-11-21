# 控制台四类连接器本地验收

2026-11-21（新加坡时间）在独立网关与测试后端完成验收。工作项为 [W4-RA01（#29）](https://github.com/xielforever/MCPGuard/issues/29)，源码以 Jenkins PR #28 的 `1fce288ee4b985fd1c9e7948c1abfb0dc0c06556` 为基线，包含本地连接器管理变更。

完整运行 `966f9720ff83` 由 Windows Chrome 实际提交四类连接器。四个页面表单均完成新增、连通测试与启用，页面审计逐一出现对应事件。随后用另一个获准的身份通过 MCP Streamable HTTP 读取后端。

| 类型 | 原生后端实际读取 |
| --- | --- |
| PostgreSQL 16.14 | 在 `commerce` Schema 查询表目录，得到 `orders`。 |
| Redis | 在允许前缀内读取测试键。越界键被 MCPGuard 拒绝。 |
| Jenkins 2.580.2 | 返回 `inventory-verification` 和 `orders-validation`，读取后者的 `GIT_REF` 和 `RUN_TESTS` 参数定义。 |
| K3s | 在 `orders-verification` 命名空间读取 `orders-validation` Deployment。额外传入其他命名空间被拒绝。 |

四类坏凭据测试均返回失败，启用请求返回 HTTP 409。未获连接器授权的主体查不到已启用条目，直接使用另一个主体的连接器 ID 也被拒绝。依次停用四条成功连接后，在同一个 MCP 会话中再次查询及调用，目录返回空列表，四类原有调用均被拒绝。验收后重新启用四条通过的连接器用于短时隔离预览。

定向 PostgreSQL 测试 `registry-80ab0ce960f3` 覆盖跨组织与管理权限、请求键重放、并发测试结果过期、凭据脱敏和审计。独立 K3s 验证 `82c5a4c5c80a` 经 TLS 验证后读到指定命名空间。原生 RBAC 对跨命名空间读取返回 HTTP 403，并且拒绝 `patch`。Go 全包 race `7da5b83494` 退出码为 0，333 项通过、0 项失败，22 项按夹具条件跳过。

较早验收保留了独立日志。镜像就绪检查已改用 K3s 内置的 `kubectl` 入口，Redis 明文回环夹具显式设置 `tls: false`。Windows 到 WSL 候选端口的系统转发曾超时，最终浏览器验收使用受限的本机回环转发。MCP 验收断言按 SDK 的语义处理成功响应中省略的 `isError`。

[脱敏完整运行结果](local-console-connectors-2026-11-21.json)
