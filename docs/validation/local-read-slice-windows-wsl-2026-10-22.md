# Windows / WSL 本地最小读取闭环记录

日期：2026-10-22，时区 Asia/Singapore（UTC+08:00）。结果：**PASS**。实际链路为 Windows 客户端 → HTTP 或 WSL stdio 连接器 → 本地网关 → 原生 PostgreSQL → 持久结果与审计。

本地目录原为空，从 `xielforever/MCPGuard` 的 main 拉取提交 `2871a0018d6a4d22bc65dacdef7f24cd9a4032ae`。本轮另修复启动脚本的 PostgreSQL 就绪竞态，未修改网关、原生读取、授权或审计实现。

## 实际执行结果

| 检查 | 本轮结果 |
|---|---|
| 修复后的全新启动 | 新建独立空 PG 容器成功；创建元数据库、业务库、不同角色和三名测试员工 |
| 迁移 | 1–2 应用成功，第二次执行校验成功 |
| 全仓库 Go race | `go test -json -race -p 2 -count=1 ./...` 通过；15 条测试通过事件包含 10 个 PG 子场景及其父测试；测试用例跳过数为 0；无测试文件的包单列为 package skip |
| 真实 PG 场景 | 10/10：范围隔离、参数/直接调用拒绝、会话撤权、原生只读角色、资格与不可变约束、审计失败关闭派发、真实查询取消、有界超时、审计权限/分页、遗留资格恢复 |
| 独立 Python 联调 | 48/48：实际二进制的 HTTP、stdio、真实 PG、RPC ID 精度、单请求取消隔离、审计与网关重启 |
| Windows 本机客户端 | 19/19：Node 从 Windows localhost 完成 HTTP 握手、两范围读取、无权拒绝、bigint 精度与审计；通过 WSL/Docker stdin/stdout 完成 stdio 实际读取及 EOF 退出 |
| 当前进程与外部环境 | 网关 `/healthz` 返回 200；原 DBHub 的 8080、5173 两个 `/api/ready` 均在测试前后返回 200 |

Go/Python 联调执行时间为 22:48:45–22:50:49（UTC+08:00）。Windows 客户端在此之后针对保留运行的网关验证。

## 本地环境与保留服务

- Windows 11 企业版，WSL Ubuntu 24.04，Docker 29.9.1，Linux amd64。
- 测试容器复用本机已有的 `dbhub-local/golang:1.26.9-bookworm` 镜像，实际编译器及 race 测试为 **Go 1.26.8**；Python 标准库；Windows Node 24.19.0。
- PostgreSQL 实际查询版本为 **16.14 / 160014**，与项目固定镜像一致。未使用本机已有的其他 PG 实例。
- 固定镜像索引为 `sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777`；linux/amd64 manifest 为 `sha256:7a396fd264a2067788b6551122b50f162bf6136312c7fc9d74381cb92c648382`。
- 镜像下载较慢，改由 HTTPS 下载同一 OCI 内容并分段获取大文件，完整验证索引、manifest、config 和全部 layer 的 SHA256 后导入。未修改 Docker daemon 配置。
- 网关：`http://127.0.0.1:18080/mcp`；存活检查：`http://127.0.0.1:18080/healthz`。8080 原由 DBHub 使用。
- 保留运行：`mcpguard-local-gateway`、`mcpguard-local-runner`、`mcpguard-local-verified-postgres`。PG 本轮映射为 `127.0.0.1:32770`，其他服务未重启。
- 本轮配置仍在 `.dev/env.json`，测试身份有效期为 24 小时；在 Windows 上通过 `.dev` 的受保护 NTFS ACL 限制访问。未将日志里的 POSIX `0600` 提示当作 NTFS 权限证明。

当前使用开发容器运行已构建程序；本轮没有重新验收发布用 distroless 镜像。原沙箱的容器证据继续保留在 [原记录](read-slice-2026-10-22.md)。

## 发现并修复的启动竞态

首次全新启动在 `scripts/dev.py up` 的建库阶段失败，`psql` 返回 2。PG 日志证明：镜像的临时初始化进程只开放 Unix socket，脚本的 `SELECT 1` 提前成功；临时进程随即正常关闭，后续建库连接中断。

[启动脚本](../../scripts/dev.py) 现在先用容器内 `pg_isready -h 127.0.0.1 -p 5432` 等待正式 TCP 服务，再执行原有 SQL 就绪检查。数据库、权限和测试断言不变。修复后使用另一个全新容器完成初始化及全部测试。

首次失败容器 `mcpguard-dev-postgres` 已停止，容器和匿名卷未删除。失败日志见 [保留记录](local-read-slice-bootstrap-before-fix-2026-10-22.txt)；对应原配置备份在私有 `.dev/bootstrap-before-fix-env.json`。未覆写原沙箱验收记录。

## 在当前机器复现

以下在 PowerShell 执行，使用本轮已创建的 runner、缓存和私有配置。

```powershell
cd C:\dev\projects\MCPGuard
wsl -d Ubuntu-24.04 -- docker start mcpguard-local-runner
wsl -d Ubuntu-24.04 -- docker exec mcpguard-local-runner python3 scripts/dev.py up
wsl -d Ubuntu-24.04 -- docker exec mcpguard-local-runner python3 scripts/dev.py test
```

`up` 保留数据和密钥，刷新测试身份；上述命令不依赖 Windows 安装 Go。通过当前容器选择本轮实际配置，不使用默认 8080。

启动或停止保留的网关：

```powershell
wsl -d Ubuntu-24.04 -- docker start mcpguard-local-gateway
wsl -d Ubuntu-24.04 -- docker stop mcpguard-local-gateway
```

stdio 客户端启动命令（stdin/stdout 为 MCP 协议）：

```powershell
wsl -d Ubuntu-24.04 -- docker exec -i mcpguard-local-runner python3 scripts/dev.py connect --employee alice
```

本轮还启动了隐藏 WSL 保活进程，PID 记录于 `.dev/wsl-keepalive.pid`。测试库和首次失败现场均保留，未执行 `dev.py down`。

## 证据与范围

- [Go/Python 机器结果、源码与二进制 SHA256](local-read-slice-windows-wsl-2026-10-22.json)
- [19 项 Windows 客户端结果](local-read-slice-windows-client-2026-10-22.json)
- [实际 PG 版本、迁移 checksum、容器与镜像来源](local-read-slice-runtime-2026-10-22.json)

Windows Git checkout 使用 CRLF；机器结果同时记录原始工作区和归一化 LF 的源码摘要。机器结果中的 `commit` 是验证时的基线 HEAD，包含修复的实际源码由 `sourceSHA256` 和 `normalizedLFSourceSHA256` 绑定。本轮源码与原归档基线的差异仅为 `scripts/dev.py` 的就绪检查；替换回基线提交 `2871a00` 中该文件后，归一化摘要与原归档一致。测试数据库独立创建，迁移 checksum 对应本轮实际嵌入的 SQL。

本轮验证开发测试身份和读取闭环，不包含真实 ChatGPT/IDE 账号接入、企业 OAuth/SSO、写入审批、Redis/API、企业 TLS 部署或完整控制台；没有调用模型，也不需要模型 key。
