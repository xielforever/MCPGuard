# 本机连接凭据自动续期 — 2026-10-23

结论：`LOCAL_CREDENTIAL_AUTORENEW_PASS`。已为现有本机 `mcpguard_local` 连接使用的 Alice 测试身份开启自动续期。当前有效期为 **2026-11-22 13:40:45（新加坡时间）**，以后由后台提前延长，不需要每天手动续期或重新接入。

入口仍为 `http://127.0.0.1:18080/mcp`，使用 Streamable HTTP。未引入域名、隧道或云端插件。

[结构化结果](local-credential-renewal-2026-10-23.json) · [操作说明](../local-service.md)

## 续期规则

用户本轮明确要求处理凭据自动续期。在先前服务修复阶段保留的 24 小时身份有效期基础上，本次为这一项本机凭据登记独立运维策略：

- 每次续期后有效 30 天，剩余 7 天时进入续期窗口；后台每分钟检查一次。
- 建立 MCP 连接时同步检查，即使网关已经健康也检查身份。长时间停机导致凭据已过期时，按需启动服务并补续期。
- 原 token 保持不变；只更新这一个身份的 `expires_at`，因此已连接客户端不需要每天替换认证头。
- 登记绑定固定组织、Alice 身份、原 token 哈希和 `scope_a`。停用、撤销读取权限、改变范围、替换凭据均导致拒绝续期；不会自动恢复授权，也不会替换或创建身份。
- 普通服务安装默认不替其他身份登记续期；本机通过一次性 `EnableAutoRenew` 完成登记。Bob、Visitor、独立 OAuth 夹具及网关生产鉴权规则保持原样。

实现：[续期控制](../../scripts/local_credentials.py)、[服务接入](../../scripts/local_service.py)、[连接前检查](../../scripts/local-service.ps1)。登记、状态和追加事件日志均在被 Git 忽略的 `.dev/local-service/`；登记不存明文 token。续期使用宿主服务控制器的权限，网关运行容器没有增加数据库管理权限。

数据库续期事务先锁授权版本，再锁目标身份；条件更新保留现有授权版本触发器。未进入续期窗口时，不执行身份更新，也不提升授权版本。原网关的到期、禁用、权限与 scope 校验继续执行。

## 实际调用验证

测试只改测试身份的到期时间以压缩等待，不修改系统时钟。通过官方 Codex app-server 的 `mcpServer/tool/call` 调用实际配置的 `mcpguard_local`，读取 `pg.get_demo_item({"itemId":"item_demo_1"})`；四次均返回 `Alice scope A` 和 `SUCCEEDED / DISPATCHED / NONE`。

| 场景 | 结果 | 证据 |
| --- | --- | --- |
| 网关健康、凭据已过期 | 首先真实收到 401；建立 MCP 连接后同步续期并读取成功 | [恢复过程](local-credential-expired-recovery-2026-10-23.json)、[客户端](local-credential-native-expired-2026-10-23.json) |
| 持续连接跨过原到期时间 | 后台 13:38:33 续期，早于模拟到期时间 13:39:14；同一个客户端在 13:39:31 再次读取成功，网关和监督进程未重启 | [后台过程](local-credential-continuous-2026-10-23.json)、[两次客户端结果](local-credential-native-continuous-2026-10-23.json) |
| 模拟已过期 45 天且服务停止 | 仅发起 MCP 连接，自动拉起服务、完成续期并读取成功；没有另行执行手动启动或续期 | [恢复过程](local-credential-offline-recovery-2026-10-23.json)、[客户端](local-credential-native-offline-2026-10-23.json) |

表内时间均为 2026-10-23 新加坡时间。模拟了过期后的状态，没有声称实际等待了 45 天，也没有重新启动 Windows。

四次读取逐条与执行密文、action/attempt/execution 标识和持久审计核对，审计序号为 192–199。历史 191 条审计全部保留，action/attempt/execution 从 83 增至 87。[审计证据](local-credential-audit-2026-10-23.json)

## 授权保护验证

25 项针对性测试全部通过：20 项本机服务/续期单元保护测试，5 项实际 PostgreSQL 保护测试。实际数据库测试分别验证：

- 身份停用时不重新启用、不延长到期时间。
- 读取权限撤销时不恢复权限、不延长到期时间。
- scope 改变时不恢复原范围、不延长到期时间。
- token 哈希被替换时不恢复旧凭据、不延长到期时间。
- 凭据仍充足有效时，检查不改变身份或授权版本。

故障注入只短暂改变 Alice 的授权字段，测试清理后逐字段恢复；最终核对 Alice 除有效期外所有授权字段及 token 哈希与修复前一致，其他身份连同到期时间完全一致。

```powershell
wsl -d Ubuntu-24.04 --cd /mnt/c/dev/projects/MCPGuard -- env MCPGUARD_TEST_LOCAL_RENEWAL=1 python3 -m unittest discover -s tests -p 'test_local_*.py' -v
```

客户端验证使用临时内存上下文，没有模型回合或持久聊天；同一个客户端跨到期验证使用 [验证器](../../tests/local_mcp_verify.cjs) 的 `--repeat-after-ms 90000`。服务继续使用已配置的当前用户登录任务及按需启动路径。Windows 整机冷启动仍未实测。

原配置、历史审计和先前失败记录保留。先前服务恢复报告中“身份有效期未改动”对应当时阶段，现由本次明确授权的续期策略接续。
