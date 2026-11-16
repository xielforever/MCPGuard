# RA-13 吊销本地实现说明

## 入口

运营方用独立的特权迁移连接执行撤销，不经 MCP 工具、不暴露给业务运行时角色：

```bash
MCPGUARD_METADATA_DSN=postgres://... \
MCPGUARD_ENCRYPTION_KEY=... MCPGUARD_PLAN_KEY=... \
mcpguard-catalog revoke <KIND> <targetId> <orgId> <actorId> [reason]
```

`KIND` 为 `CREDENTIAL | GRANT | PRINCIPAL | RESOURCE`。命令输出一段 JSON 报告本次实际影响范围：

```json
{"kind":"GRANT","targetId":"...","subjectId":"...","resourceId":"...",
 "grants":1,"credentials":1,"tickets":0,"contexts":0,"epoch":"42"}
```

`reason` 以 AEAD 密文写入审计，不落明文；`revocation_audit` 记录 actor、目标、subject/resource 与各项计数。

## 四级语义

| 级别 | 目标 | 级联 |
|---|---|---|
| `CREDENTIAL`（文件/包） | 单个凭据租约 | 租约置 REVOKED、未领取 ticket 置 CANCELLED；旧文件/旧 ticket 无法再换取新下载 |
| `GRANT` | 一次资源授权 | 该 grant 下全部租约/ticket 撤销，grant 头置 REVOKED |
| `PRINCIPAL` | 一个主体 | 该主体的 ACTIVE context、租约/ticket、全部 grant 撤销，主体 `enabled=false` |
| `RESOURCE` | 一个资源 | 该资源下全部租约/ticket、全部 grant 撤销，绑定禁用，资源头 `enabled=false` |

每次撤销在同一事务内先 `lock_auth_epoch_exclusive`，再 `advance_access_epoch`，因此：

- 撤销提交后，任何副本的下一次申请/签发/领取都会因主体/资源/绑定/授权状态或 epoch 复查失败；
- 撤销先于新领取提交时，新领取必定失败（后提交者看到 epoch 与终态）；
- 在途执行的实际停止、连接池淘汰与原生清理状态属于 RA-14，不在本阶段宣称完成。

## 原生清理重试

撤销只登记原生账号清理（PENDING），不宣称后端已清理。运营方用同一特权连接重试：

```bash
mcpguard-catalog revocation-cleanup status <orgId>
mcpguard-catalog revocation-cleanup retry <orgId> <provider-private.json> [limit]
```

`retry` 通过作废前的 provision plan（若存在）调用提供方 Cleanup；缺失/不可读计划或提供方失败都会记 FAILED/attempts+1，保持可重试；只有 `CLEANED/ABSENT_SEALED/DISABLED` 回执才置 CLEANED。重复撤销不会把已 CLEANED 重置回 PENDING。

## 边界

- 本地客户端缓存/旧 context 文件的服务端拒绝已验证；客户端侧彻底清除与真实客户端体验按 RA-18 验收。
- 控制台撤销页面按 RA-16 推进；本阶段入口为受控 CLI 与 `RevocationService`。
- 原生账号清理失败的处置沿用 RA-06 的清理重试路径，不在撤销事务内阻塞。
