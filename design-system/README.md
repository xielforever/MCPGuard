# 执契 界面设计

执契采用[白文篆印](mcpguard/brand/zhiqi-seal-concept.png)。内嵌控制台、测试授权页和 Vue 原型共用同一枚[透明方印](../internal/uitheme/brand/zhiqi-seal-mark.png)。

**当前实现入口（2026-10-29）：**D「星舰 · 审批舰桥」已迁移网关内嵌控制台现有六页，使用统一 token 与共用组件。见[六页迁移记录](mcpguard/implementation-console-d.md)和[规范 MASTER](mcpguard/MASTER.md)。下文为各轮设计记录，保留其历史时点；`web/` Vue 原型继续保留历史定位。

当前阶段：**整体重设计的视觉方向待用户选择**。

最新材料为 [2026-10-29 三套视觉方向](mcpguard/proposals/2026-10-29/README.md)，包含可直接用浏览器打开的 [单文件离线预览](mcpguard/proposals/2026-10-29/preview-offline.html)。

本目录中的方案为独立设计草案；尚未选定项目权威视觉规范，也未迁移当前业务页面。选定方向后再建立 `mcpguard/MASTER.md` 和正式共用主题源，随后迁移实际控制台与历史原型的基础样式。

## 2026-10-29 第二轮更新：科幻审批（最新）

用户进一步提出“这个审批UI我希望设计的有科幻风格，不要太古板”。最新方向为 [D 星舰审批舰桥 / E 光舱轨道中枢](mcpguard/proposals/2026-10-29-sci-fi/README.md)，可直接打开 [科幻版离线预览](mcpguard/proposals/2026-10-29-sci-fi/preview-offline.html)。

第二轮重点调整审批布局、视觉语言、关系示意与动效。首轮 A/B/C 方案及其验证记录完整保留；两轮均尚未应用到业务页面，待用户确定方向后实施统一规范。


## 2026-10-29 第三轮更新：D 已选定，UI/UX 规范 v1.0（当前入口）

用户已明确“采用设计D，开始出UIUX设计规范”。当前规范入口为 [执契 UI/UX MASTER](mcpguard/MASTER.md)，数值单源为 [tokens.json](mcpguard/tokens.json)，另有 [共用组件](mcpguard/components.md)、[跨页流程](mcpguard/workflows.md)、[页面与验收](mcpguard/acceptance.md)及[变更记录](mcpguard/CHANGELOG.md)。

视觉方向已确认为 D「星舰 · 审批舰桥」。上文各轮“待选”及原候选报告保留其历史时点；A/B/C/E 不再作为当前正式方向。规范交付与数值/文案映射检查不等于主题已接入、RA-16 已完成或业务页面已验收，本次不改运行页面。
