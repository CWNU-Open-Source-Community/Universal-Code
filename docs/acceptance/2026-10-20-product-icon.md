# 产品图标替换：真实页面前后对比

日期：2026-10-20。使用实际生产前端构建，在 Chromium 中打开连接页面；未替换 DOM 或模拟页面。浏览器视口为 1280 × 900，以下图片直接截取同一个连接面板。

- 修改前：`f1dbb597d3570bc84587523bd3fbc87f380f6023`（项目侧栏改动合入后的旧图标）。
- 修改后：`0047c3305a5ebd8cf443f9ea00e112781c51de69`（新产品图标）。
- 两个构建分别通过本地预览服务运行；确认实际加载对应的 512 像素品牌资源后截图。

| 主题 | 修改前 | 修改后 |
| --- | --- | --- |
| 浅色 | ![浅色连接页修改前](assets/2026-10-20-product-icon/before-light.png) | ![浅色连接页修改后](assets/2026-10-20-product-icon/after-light.png) |
| 深色 | ![深色连接页修改前](assets/2026-10-20-product-icon/before-dark.png) | ![深色连接页修改后](assets/2026-10-20-product-icon/after-dark.png) |

前端构建及品牌、ICO、ICNS、MSIX 和 Windows 内嵌资源检查已通过。PR 原提交的 CI 与 Windows/macOS 候选包构建全部通过。这些图片记录 Web 连接页的实际显示；macOS 原生应用交互尚未在本机验证。
