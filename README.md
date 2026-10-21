<div align="center">
  <img src="assets/branding/traverse-board-mark.png" alt="Universal-Code Logo" width="140">
  <h1>Universal-Code</h1>
  <p><strong>面向本地项目的开源 AI 编程工作台</strong></p>
  <p>连接云端与本地大模型，在同一工作流中读写代码、运行终端命令、查阅资料并实时审阅 Diff 改动。</p>

  <p>
    <a href="README.md">简体中文</a> |
    <a href="README.en.md">English</a>
  </p>

  <p>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/CWNU-Open-Source-Community/Universal-Code/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white"></a>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/releases"><img alt="Release" src="https://img.shields.io/github/v/release/CWNU-Open-Source-Community/Universal-Code?style=flat-square&color=blue&logo=github"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/CWNU-Open-Source-Community/Universal-Code?style=flat-square&color=green"></a>
    <img alt="Platform" src="https://img.shields.io/badge/Platform-Windows%20%7C%20macOS-blueviolet?style=flat-square">
    <img alt="Go" src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white">
    <img alt="Node" src="https://img.shields.io/badge/Node.js-24+-339933?style=flat-square&logo=node.js&logoColor=white">
    <img alt="React" src="https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react&logoColor=black">
    <a href="CONTRIBUTING.md"><img alt="PRs Welcome" src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square"></a>
  </p>
  <p>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/stargazers"><img alt="Stars" src="https://img.shields.io/github/stars/CWNU-Open-Source-Community/Universal-Code?style=flat-square&logo=github&color=gold"></a>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/network/members"><img alt="Forks" src="https://img.shields.io/github/forks/CWNU-Open-Source-Community/Universal-Code?style=flat-square&logo=github"></a>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/issues"><img alt="Issues" src="https://img.shields.io/github/issues/CWNU-Open-Source-Community/Universal-Code?style=flat-square&color=orange"></a>
  </p>
</div>

---

## 核心特性

- **深度聚焦本地项目**：直接阅读、分析与修改本地代码库，内置 Diff 逐行审阅与可信撤销（Rewind / Checkpoints）能力。
- **透明可控的安全策略**：支持只读分析、交互审批（Approval Mode）与全自动（Full Access）权限分级，高危操作（文件删除、非白名单命令）严格阻断并要求人工确认。
- **主流模型即开即用**：原生接入 OpenAI、Anthropic Claude、DeepSeek、Kimi、Ollama 本地模型与任何兼容 OpenAI 的第三方 API，支持单会话内即时热切，并支持通过系统浏览器快速完成 ChatGPT 账户授权登录。
- **真实来源的网络检索**：优先调用供应商原生搜索，支持直接抓取 GitHub PR/Issue、Hacker News 与 RSS/Atom 资讯，自动附带快照存证与引用出处。
- **现代苹果液态玻璃美学**：采用纯 CSS 物理折射模型（macOS / VisionOS 亚克力液态玻璃风格），提供深色、浅色与雾透磨砂质感，兼具 120fps 极速渲染。

---

## 快速开始

### 方式一：下载预编译客户端

<!-- public-downloads:start -->
当前公开预览版：[**v1.0.0-preview.20261020**](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v1.0.0-preview.20261020)（2026-10-20 发布）。

| 平台 | 下载与运行说明 |
| :--- | :--- |
| **Windows x64** | 下载 [TraverseBoard.exe](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/download/v1.0.0-preview.20261020/TraverseBoard.exe)，双击运行；需要 Edge WebView2 Runtime。详见 [Windows 下载说明](docs/windows-release.md)。 |
| **macOS Apple Silicon / Intel** | 在 [Release Assets](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v1.0.0-preview.20261020) 中选择名称含 `macos-arm64`（M 系列）或 `macos-amd64`（Intel）的 `.dmg`，将 `TraverseBoard.app` 拖入 Applications。详见 [macOS 下载说明](docs/macos-release.md)。 |

公开 Release 下载无需登录 GitHub，不受 Actions artifact 保留期限限制；可用同页的 [SHA256SUMS](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/download/v1.0.0-preview.20261020/SHA256SUMS) 核对文件。Windows EXE 未签名，Mac 包仅 ad-hoc 签名、未经 Apple 公证，首次启动可能被系统阻止。这是预览版，验证范围与已知问题以 Release 说明为准。
<!-- public-downloads:end -->

需要比公开预览更新的测试候选时，查看 [Desktop release 打包记录](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)，核对版本与源码提交。Actions 下载需要登录 GitHub，且会过期。
文件保留历史名称，窗口中的产品名为 Universal-Code。

### 方式二：从源码构建运行

安装 **Go（版本遵循 [go.mod](go.mod)，当前为 1.26.10）** 与 **Node.js 24**（CI 使用 24.16.0），然后克隆仓库：

```bash
git clone https://github.com/CWNU-Open-Source-Community/Universal-Code.git
cd Universal-Code
```

Windows 在仓库根目录的 PowerShell 中运行：

```powershell
.\scripts\build-desktop.ps1 -Version v1.0.0-dev
.\build\desktop\TraverseBoard.exe
```

macOS 需要 Xcode Command Line Tools。在仓库根目录运行：

```sh
CGO_ENABLED=1 bash scripts/build-desktop-darwin.sh -Version v1.0.0-dev
open build/desktop/TraverseBoard.app
```

脚本会安装前端锁定依赖、检查并构建内嵌界面，再使用各平台需要的 Go 标签和链接参数生成桌面应用。
应用包含后端服务，无需另外启动后端。完整要求见 [Windows](docs/windows-release.md) / [macOS](docs/macos-release.md) 说明。

---

## 模型配置与连接

首次打开客户端或新建对话时，点击输入区上方的模型选择器进行配置：

1. **云端 API（OpenAI / Claude / DeepSeek / Kimi 等）**：
   - 选择对应提供商，填入你的 API Key、模型标识符及服务 Base URL（如适用）。
   - 点击 **保存并检查**，系统将发起一次轻量调用以验证额度与连通性。
2. **ChatGPT 账户浏览器授权（无需 API Key）**：
   - 在模型配置中选择 **ChatGPT**，点击 **Continue with ChatGPT**。
   - 系统将唤起系统浏览器完成授权，随后可直接选择账户可用模型。
3. **本地 Ollama（零门槛离线运行）**：
   - 本地启动 Ollama 服务（默认 `http://127.0.0.1:11434`）。
   - 在提供商列表选择 **Ollama**，输入模型名称（如 `qwen2.5-coder:7b`、`deepseek-r1:8b`）即可直接调用。

---

## 界面预览

| 浅色与深色对比 | 雾透磨砂玻璃 (Mist) |
| :---: | :---: |
| ![浅色与深色界面对比](docs/images/appearance/light-dark.png) | ![雾透外观](docs/images/appearance/conversation-mist.png) |

可在客户端 **设置 → 外观** 中自由切换 **系统跟随、浅色、深色、雾透玻璃**。

---

## 详细文档导航

| 领域 | 文档链接 | 描述 |
| :--- | :--- | :--- |
| **使用指南** | [用户手册 (Usage Guide)](docs/usage.md) | 完整客户端使用、CLI 命令与常见工作流 |
| **聊天资源** | [链接、文件与剪贴板](docs/chat-resource-interactions.md) | 外链预览、资源右键菜单、文件与图片复制粘贴 |
| **搜索与取证** | [网络检索与快照 (Web Evidence)](docs/web-evidence.md) | 检索配置、快照存储、URL 溯源与失败诊断 |
| **改动审计** | [工作区检查点 (Checkpoints)](docs/workspace-checkpoints.md) | 逐行 Diff 审阅、快照保存与安全恢复 (Rewind) |
| **生态扩展** | [扩展与 MCP 协议 (Extensions)](docs/extensions.md) | 接入 Model Context Protocol (MCP) 与外部 Skill |
| **代码智能** | [LSP 语言服务器 (Code Intelligence)](docs/code-intelligence.md) | 符号跳转、类型推断与代码索引支持 |
| **系统架构** | [架构设计方案 (Architecture)](docs/architecture.md) | Go 控制面、执行状态机与安全策略实现 |
| **发布日志** | [版本发布说明 (Release Notes)](docs/releases/v1.0.0.md) | v1.0.0 验证范围、兼容性与更新记录 |

---

## 参与贡献

欢迎提交 Issue 与 Pull Request！在开始开发前，请先阅读我们的 [贡献指南 (CONTRIBUTING.md)](CONTRIBUTING.md)。

## 开源许可证与字体合规

- 本项目基于 [Apache License 2.0](LICENSE) 协议开源。
- **第三方字体声明**：中文界面包含华为技术有限公司授权的 HarmonyOS Sans Fonts，完整 [字体许可文本](web/public/licenses/HarmonyOS-Sans.txt) 随软件一并分发；字体源文件与校验哈希详见 [字体出处说明](web/src/assets/fonts/PROVENANCE.md)。
