# 执契 xyc CCE 隔离验收

执契在 `zhiqi` 命名空间运行一个副本。控制台和本机 DSH Desktop 使用经 Kubernetes 鉴权的回环端口转发。网关每次启动从 Nacos 3.x 读取指定配置，连接既有 PostgreSQL 专库。浏览器中的 Jenkins App 配置与操作通过这个实例完成。

## 配置占位文件

将 [Helm 参数](examples/values-xyc.yaml)、[Nacos 正文](examples/metadata-pg.json) 和 [Secrets](examples/secrets-xyc.yaml) 复制到 Git 忽略的 `.dev/` 后编辑。示例中的 `REPLACE` 字段和 `192.0.2.0/24` 地址用于标示待填项。

| 位置 | 填写内容 |
| --- | --- |
| Helm 参数 | 私有 SWR 的 `zhiqi` 仓库地址和 `sha256:` 镜像摘要。确认同命名空间内的镜像拉取 Secret。填写 Nacos 地址、Namespace ID、Group、Data ID，以及 PostgreSQL 实际库名和端口。将 `connectorAllowedCIDRs` 收窄到获准的 Jenkins 地址。 |
| Nacos 配置 | 将 JSON 发布到 Helm 所指的配置，填写 PostgreSQL 私网 IP、专用运行用户名及密码。Nacos 登录账号需要该配置的读取权限。 |
| Kubernetes Secrets | 填写 Nacos 只读登录、PG 实例 CA、独立迁移 DSN、两份不同的 Base64 编码 32 字节密钥，以及控制台登录对应的主体 UUID 和 SHA-256 密码哈希。已有 Secret 时按实际名称填写 Helm 参数。 |
| Jenkins App | 在验收开始时从控制台填写 HTTPS 地址、获准的文件夹、专用账号和 API token。为连接测试准备实际可读取的任务。 |

数据库管理员在同一个目标库核验两套账号。迁移账号用于版本预检和应用迁移，运行账号需要 schema 使用权及受限的业务读写权限，并且审计表对运行账号只允许追加和读取。可按目标角色逐项核对 [隔离测试采用的授权](../../../scripts/fixtures/grants.sql)。首次准备隔离身份时，登记独立的管理员、DSH 申请人和审批人。组织中的 `jenkins` 受控资源、申请人的 `jenkins.submit_build` 以及审批人的审批权限都必须存在。 [Jenkins App 配置及权限](../../../docs/jenkins-integration.md)

`ZHIQI_METADATA_SOURCE=nacos` 使用 `ZHIQI_NACOS_URL`、`ZHIQI_NACOS_NAMESPACE_ID`、`ZHIQI_NACOS_GROUP`、`ZHIQI_NACOS_DATA_ID` 和 Nacos 登录环境变量读取配置。数据库名、端口和 CA 由 `ZHIQI_METADATA_DATABASE`、`ZHIQI_METADATA_PORT`、`ZHIQI_METADATA_CA_FILE` 装配。PG 使用 `verify-ca` 验证实例证书。应用配置变更后重启 Pod 生效。默认环境变量模式从 `MCPGUARD_METADATA_DSN` 装配本地实例。 [Nacos 3.x 客户端 API](https://nacos.io/docs/latest/manual/user/open-api/)

隔离环境的 Jenkins 仍是集群内 HTTP 服务，而连接器只接受 HTTPS 或回环 HTTP。Chart 因此在同一 Pod 内启动仅监听 `127.0.0.1:18083` 的 TCP 代理，转发到获准的 `infra-jenkins` 服务；页面登记 Jenkins App 时地址填 `http://127.0.0.1:18083`。该代理没有 Service，不能从 Pod 外访问，只用于本次隔离验收，不是生产入口方案。

## 发布顺序

1. 固定包含执契品牌的候选提交并完成回归。按目标节点架构构建镜像，推送到获准的私有 SWR。核对仓库摘要并填入 values。
2. 确认 xyc 的显式 kubeconfig、TLS 校验、`zhiqi` 发布权限和目标依赖。核对目标库身份、迁移版本及 Secret 引用。从目标 Pod 所在网络验证到 Nacos、PG 和 Jenkins 的实际路径。
3. 由数据库管理员完成专库备份和恢复预检，保护配套密钥。用相同镜像分别渲染 `migration.mode=plan` 和 `migration.mode=apply`。前者执行只读预检，检查 `schema`、`currentVersion`、`pendingVersions`。确认版本和备份后执行后者，再用新运行账号核对权限。每次运行使用唯一的 `migration.runId`。
4. 对独立 Job 和常驻 Deployment 分别做服务端 dry-run。迁移 Job 通过渲染后的清单提交，常驻 Deployment 用 Helm 发布。确认 Pod 镜像摘要、`/healthz` 和 `/readyz` 后开通本机转发。

以下示例从仓库根目录运行。预先设置 `KUBECONFIG`、`XYC_CONTEXT` 和本机私有 `VALUES` 路径。`RUN_ID` 使用 1 至 20 位小写字母或数字。

```bash
helm lint --strict deploy/helm/zhiqi -f "$VALUES"
helm template zhiqi deploy/helm/zhiqi -n zhiqi -f "$VALUES" \
  --set-string migration.mode=plan --set-string migration.runId="$RUN_ID" |
  kubectl --context "$XYC_CONTEXT" -n zhiqi create --dry-run=server -f -
helm template zhiqi deploy/helm/zhiqi -n zhiqi -f "$VALUES" |
  kubectl --context "$XYC_CONTEXT" -n zhiqi apply --dry-run=server -f -
```

迁移 Job 完成并核对备份后，再发布网关并开启转发。

```bash
helm upgrade --install zhiqi deploy/helm/zhiqi --kube-context "$XYC_CONTEXT" \
  -n zhiqi -f "$VALUES" --wait --timeout 3m
kubectl --context "$XYC_CONTEXT" -n zhiqi port-forward --address 127.0.0.1 deployment/zhiqi 18082:18082
```

## 验收与回退

- 管理员从 `http://127.0.0.1:18082/console/` 新建、测试、授权并启用 Jenkins App。在 DSH Desktop 登记 MCP 服务 `zhiqi`，地址为 `http://127.0.0.1:18082/mcp`，执契通过握手提供展示名。以申请人身份查询同一 `connectorId` 下两个参数定义不同的任务。
- 核对待审批时零派发。由独立审批人在控制台批准后，申请人继续原 `actionId`，核对 Jenkins 队列、实际构建和审计。补验越权、参数定义变化、重复请求、丢失队列回执以及停用后原会话调用。
- 记录 Pod 的运行镜像摘要和元库迁移版本。替换 Pod 后重新建立端口转发，使用同一 DSH 配置读回 App、授权和审计。存活失败与元库未就绪分别通过 `mcpguard probe live` 和 `mcpguard probe ready` 诊断。
- 回退时暂停接入并核对在途动作。首次发布失败时将 Deployment 缩为零副本，保留迁移回执与备份。后续升级回退到已核对架构兼容的上一版镜像及配置。数据库恢复先在隔离库演练并检查身份、授权和历史审计，再决定切换。

证据归档包含源提交、镜像 digest、迁移版本、独立角色、页面及 DSH 回执和 Jenkins 队列计数。发布条件和生产验收分别由 [#24](https://github.com/xielforever/MCPGuard/issues/24) 和 [#25](https://github.com/xielforever/MCPGuard/issues/25) 管理。
