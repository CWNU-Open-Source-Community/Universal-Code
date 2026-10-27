# 消息时间：真实页面对照

前后图片均来自 Windows 上的 Chromium，使用生产构建和同一个本地 Go 服务、同一条已保存聊天。测试服务通过实际文件工具读取 README.md，返回离线验证回复；没有修改页面 DOM 来模拟消息或时间。

- 基线：`10311148d5f581d68e4603e9a435c257015cc81d`。
- 桌面视口：1280 × 900，见 [before.png](before.png) / [after.png](after.png)。
- 窄视口：600 × 900，见 [before-narrow.png](before-narrow.png) / [after-narrow.png](after-narrow.png)。
- 用户消息原始时间为 `2026-10-21T17:47:40.211Z`；助手消息为 `2026-10-21T17:47:40.402Z`。本机 UTC+8，界面均显示 `01:47`。
- 实际页面的 `title` 与读屏标签包含 `2026年10月22日 GMT+8 01:47:40`，`time` 元素保留原始 ISO 时间。
- 已检查两个视口中时间与复制、引用、分支等操作的排列。600 px 的正文横向滚动条在基线中也存在。

15 项 narrative / narrative-streaming 聚焦测试及生产构建通过；测试覆盖缺失、无效时间和正在生成的回复。此次没有原生桌面打包验证。
