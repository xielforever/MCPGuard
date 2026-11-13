# D 主题 · 当前控制台六页迁移

日期：2026-10-29。范围：`internal/controlled` 中由网关提供的 `/web/` 真实控制台。沿用 [v1.0.0 UI/UX 规范](MASTER.md)和原有受保护管理 API。

本次先接入主题及共用组件，再迁移已有六个页面。历史 `web/` Vue 原型保持原样；新资源账号申请、开通、密文下载、Context 与吊销管理页面继续按原 RA 计划实施。本次没有把这些未来页面或 RA-16 标为完成。

用户随后明确“不要考虑移动端”。本轮最终交付与后续复核以桌面控制台为准，不再增加移动端适配或验收目标；此前已产生的窄屏记录保留其历史范围，不把它当作本轮移动端交付承诺。

## 1 主题与组件的唯一来源

| 层次 | 实现 |
| --- | --- |
| 设计数值 | [tokens.json](tokens.json)，保持 D 的颜色、字号、间距、圆角、尺寸与动效数值 |
| 状态文案 | [state-labels.json](state-labels.json)，与原有契约枚举绑定；未知代码明确显示“状态未确认” |
| 生成器 | [generate_ui_theme.py](../../scripts/generate_ui_theme.py)，生成 CSS 变量和冻结文案对象，支持 `--check` |
| 共用组件 | [components.js](../../internal/uitheme/components.js) / [components.css](../../internal/uitheme/components.css)：按钮、表单、表格、状态、提示、确认框、页签、轨道与空态 |
| Go 内嵌资源 | [assets.go](../../internal/uitheme/assets.go)，只允许四个公共 UI 资源；控制台通过固定路径加载 |
| 当前页面 | [console.html](../../internal/controlled/console.html)、[console.css](../../internal/controlled/console.css)、[console.js](../../internal/controlled/console.js) |

页面与组件颜色全部引用语义 token。输入、焦点、表格滚动区、弹窗和小屏触摸目标使用同一组件规则。轨道仅展示身份与资源关系；不把装饰动效解释成真实派发、健康状态或风险分数。支持暂停动效与系统减少动态效果偏好。

静态资源仍受固定 loopback Host 限制。CSP 改为仅加载同源脚本与样式，不再允许内联样式；资源设置 `no-store`、`nosniff` 和 `no-referrer`，不开放目录或任意源码读取。

## 2 六页保留的功能

| 页面 | 迁移后的实际功能 |
| --- | --- |
| 操作与审批 | 真实可见队列、筛选、冻结参数、申请人/资源/范围/期限核对、独立状态/派发/副作用、批准/拒绝、深链接与手动刷新 |
| 资源 | 已绑定资源、配置存在与启停状态、管理员启用/停用；“已配置”不表示已完成健康探测 |
| 规则 | 已有规则、启用/停用、新增规则，保留 DENY/REQUIRE_APPROVAL/ALLOW 原语义与服务端表达式校验 |
| 用户与权限 | 现有身份/范围/业务权限、工具授权、审批授权、管理成员、用户组创建/启停、组成员/工具/审批授权 |
| 本人审计 | 当前身份事件的有界分页，使用字符串游标；首次读取前明确显示尚未读取 |
| 通知与设置 | 真实本机身份/SSO 待配置状态、Telegram 配置状态与管理员投递记录；普通身份不显示伪造的空管理数据 |

概况只计算当前受权快照中的可见列表，明确最多 100 条，不使用样例人数、全局总量或固定时间。身份从服务端会话获取，正式页面没有演示身份切换器。所有业务字符串通过 `textContent` 等安全 DOM API 呈现。

## 3 审批、并发与恢复

1. 请求队列与冻结计划、决定区域分开。本人申请、无资格、已到期或未知状态不会出现批准/拒绝控制。
2. 审批确认绑定申请人、资源、范围、操作 ID、版本与期限。真正提交前重新 GET 当前计划并核对资格、到期、版本及计划摘要；POST 仍只包含旧契约的 `vote/planHash`。
3. 批准只记录决定。`READY` 与执行成功分开，浏览器不自动调用恢复或原生业务执行。未创建执行编号、未派发和副作用状态分别展示。
4. 配置确认绑定实际 payload 和当前配置版本。提交期间按钮防重复；`If-Match` 与 `Idempotency-Key` 保留。服务器明确拒绝或版本冲突时保留草稿，提示用户核对与刷新，不自动覆盖新版本。
5. 网络中断、响应不完整或服务端暂不可用时，管理提交进入“响应未确认”。原命令仅保留在当前页面的会话内存中；可手动核对状态，再显式复用原 body/版本/幂等编号重试。不会创建新命令或恢复业务执行。
6. 退出、401 或页面从浏览器缓存恢复时清空身份、管理内容、表单草稿、审计游标、详情和待确认命令。会话序号、快照序号与详情序号阻止迟到响应恢复旧身份/旧申请。
7. 确认框使用原生模态焦点约束；Escape 返回核对并恢复触发按钮焦点。调整窗口大小不重建弹窗。表格在自身命名滚动区横向滚动，小屏页面不横向溢出。

旧受控写决定接口尚不能保存理由，页面明确说明这一能力边界；没有伪造已保存理由、变更前快照、审批历史或执行结果正文。新资源权限决定的理由/期限规则仍按其独立契约实施。

## 4 验证与历史记录

本轮验证证据入口：[控制台 D 本地验证](validation/2026-10-29-console-d/README.md)。Go race、协议回归、真实隔离浏览器/API、故障注入及源码保留检查分别记录。模拟网络故障和显示状态的用例会明确标注，不等同于真实原生故障或实际 coding 客户端证明。

当前结果：Go race 294 项通过且无跳过、HTTP/stdio 56 项、浏览器/API 85 项、最终桌面专项 60 项、源码/主题 311 项；Go vet 与生成一致性通过。最终桌面复核覆盖 1920/1600/1440/1280px 六页和导航展开/收起。126 个原有方案与验证文件保持原字节，原四个本机服务的镜像/启动指纹未变。详见上述分项证据，不将各类数量合并为一个验收成绩。

迁移前 [源码快照](history/2026-10-29-before-console-d/README.md)保留旧 HTML/JS/Go 及原 token JSON。A/B/C/D/E 候选、先前验证报告与失败记录保持原字节。本轮修改清单和最终来源摘要另存，不把过去的失败或待选报告改写为通过。

复现入口：

```powershell
python scripts/generate_ui_theme.py --check
python scripts/validate_console_d.py --output .dev/console-d-ui-2026-10-29/source-checks.json
wsl.exe --exec python3 /mnt/c/dev/projects/MCPGuard/scripts/console_d_fixture.py prepare
wsl.exe --exec python3 /mnt/c/dev/projects/MCPGuard/scripts/console_d_fixture.py test
wsl.exe --exec python3 /mnt/c/dev/projects/MCPGuard/scripts/console_d_fixture.py wire
wsl.exe --exec python3 /mnt/c/dev/projects/MCPGuard/scripts/console_d_fixture.py ui-up
```

`prepare` 每次创建新的隔离场景；其余命令只处理本次场景。浏览器用例为 [console_d_ui.cjs](../../tests/console_d_ui.cjs)，依赖 Playwright 与 Chrome 的本地路径环境变量。测试登录材料限于新夹具私有文件，不进入报告、截图、模型可见文本或版本库。

本地技术验证与用户最终视觉/交互验收分开；原本运行的本机服务保持其原镜像与启动指纹，本次不部署到原服务、不提交或推送 Git。
