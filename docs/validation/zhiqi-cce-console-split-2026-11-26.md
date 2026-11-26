# 执契 CCE 控制台/网关拆分（2026-11-26）

## 结论

在现有 xyc CCE `zhiqi` 实例上完成原地拆分：控制台前端与 Go 网关现在是两个独立 Deployment，网关仍只监听 Pod 回环，新增的 `gateway-ingress` sidecar 通过集群内 Service 暴露网关，NetworkPolicy 只允许前端 Pod 访问。按本次确认的范围，完成了构建、部署和健康/冒烟检查，未重新执行 Jenkins/DSH 完整业务验收。

- 源码提交：`88512b2e8c9b7da44bc02ba27b3c6d66e61c9ce7`（`zhiqi-console-split`，基于 `cad45fd`）。
- 镜像：`swr.cn-east-2.myhuaweicloud.com/dx_x2era/zhiqi:zhiqi-88512b2@sha256:58de459148250b89d07f575bf5b6ee7381f71f8c63c96274ba74e743f08d8c0f`。
- Helm：`zhiqi` revision 3，`deployed`；chart `zhiqi-0.2.0`。
- 当前拓扑：`zhiqi-console` 1 副本 + `zhiqi` 网关 1 副本（3 个容器），2 个 ClusterIP Service，1 个 Namespace 内 NetworkPolicy。

## 拆分后对象

| 对象 | 运行内容 |
| --- | --- |
| Deployment `zhiqi-console` | 1 个 `console` 容器；静态页面 + 同源反向代理；`1/1` Ready |
| Deployment `zhiqi` | `zhiqi` + `jenkins-proxy` + `gateway-ingress`；`3/3` Ready |
| Service `zhiqi-console` | ClusterIP `8080`；本机 port-forward 入口 |
| Service `zhiqi-gateway` | ClusterIP `18082`，targetPort 为 `gateway-ingress` 的 `18080` |
| NetworkPolicy `zhiqi-gateway-ingress` | 只允许 `zhiqi-console` Pod 访问网关 `18080` |
| 迁移 Job | 既有 plan/apply Job 仍为 Complete，未改动元数据库 |

网关自身仍是 `127.0.0.1:18082`，没有非回环主监听；`gateway-ingress` 只把网关回环端口转成集群内 `0.0.0.0:18080`，且只能被前端 Pod 访问。前端 `console` 进程监听 `0.0.0.0:8080`，保留浏览器 `Host`/`Origin` 后代理到 `zhiqi-gateway:18082`，因此网关的登录、CSRF、同源和身份校验不变。

## 健康与冒烟结果

| 检查 | 结果 |
| --- | --- |
| `zhiqi` Pod | `3/3 Running` |
| `zhiqi-console` Pod | `1/1 Running` |
| 前端 `/livez` | HTTP 200 |
| 前端 `/console/` | HTTP 200，`<title>执契控制台</title>` |
| 前端 `/web/console.js` | HTTP 200 |
| 网关经前端 `/readyz` | HTTP 200 |
| 网关容器内 `mcpguard probe ready` | 退出码 0 |
| Service Endpoints | `zhiqi-gateway` 一个 `18080` endpoint；`zhiqi-console` 一个 `8080` endpoint |
| 管理员登录（经前端） | 200；`admin=true`；连接器仍为 `26d53122-f733-4fd5-9c6a-9bb638d649c5`，revision 5，`PASSED`，enabled |
| MCP `initialize`（经前端） | 200；`serverInfo.title=执契`，协议 `2025-11-25` |

按本次确认的范围，没有重新跑 Jenkins 双任务、独立审批、队列回执和完整 DSH 验收；这些仍引用拆分前 revision 2 的历史记录。拆分后元数据、连接器、授权和审计均保留，管理员读取的连接器状态未变化。

## 访问方式

```powershell
kubectl --kubeconfig C:\dev\projects\xyc-ops\xyc-k8s-kubeconfig.yaml `
  --context externalTLSVerify -n zhiqi port-forward `
  --address 127.0.0.1 deployment/zhiqi-console 18082:8080
```

- 控制台：`http://127.0.0.1:18082/console/`
- MCP：`http://127.0.0.1:18082/mcp`
- 网关直接入口仍是 Pod 回环，不通过 Service 暴露；不要再用 `deployment/zhiqi` 做前端转发。

## 边界与未完成项

- 这是控制台前端/网关部署拆分，不是把审批、执行 worker 或连接器适配器拆成独立服务；业务仍由同一个 Go 网关进程处理。
- 网关的 `gateway-ingress` 显式使用 `--allow-non-loopback`；安全边界依赖 Namespace 内 NetworkPolicy 和集群网络；没有 Ingress/公网入口。
- 控制台启动时曾出现一次 `dial tcp ...:18082: connection refused` 的启动竞态日志，随后健康、登录和 MCP 冒烟均通过，未再重复；没有据此修改探针或启动顺序。
- 两个 Deployment 都是单副本 `Recreate`；控制台无状态，网关仍持有审批/密钥/审计状态，数据库和 Nacos 外置。
- 未执行镜像回退、数据库恢复、完整 Jenkins/DSH 业务验收或 Wave 3 生产发布；#21 仍按 Review，#24/#25 未完成。

管理员控制台截图：[zhiqi-cce-2026-11-26-console-split.png](zhiqi-cce-2026-11-26-console-split.png)。
