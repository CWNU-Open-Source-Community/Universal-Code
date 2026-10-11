<div align="center">
  <img src="assets/branding/traverse-board-mark.png" alt="Universal-Code 图标" width="180">
  <h1>Universal-Code</h1>
  <p><strong>在本地项目中，让 AI 帮你改代码、运行命令、查资料并审阅结果。</strong></p>
  <p>
    <a href="README.md">简体中文</a> |
    <a href="README.en.md">English</a>
  </p>
  <p>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/CWNU-Open-Source-Community/Universal-Code/ci.yml?branch=main&style=flat-square"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/CWNU-Open-Source-Community/Universal-Code?style=flat-square"></a>
  </p>
</div>

Universal Code 是一个桌面 AI 编程工作台。连接你选择的模型，在同一段对话中推进项目，查看文件改动、命令结果和资料来源。

**v1.0.0「启航」正在准备发布。** [发布说明](docs/releases/v1.0.0.md)记录已验证范围和待完成事项；目前公开下载仍有历史预览，Mac 新包为未公证预览。

## 为什么选择 Universal Code

- **围绕真实项目工作**：阅读和修改文件、运行构建、查看差异，把任务结果落到工作区。
- **少打断，保留控制**：选择当前任务的 Full Access 后，普通文件修改与联网命令可自动执行；文件删除仍需确认。
- **查资料时保留来源**：优先使用供应商支持的原生搜索，也能读取 GitHub、Hacker News 和 RSS/Atom，并保留网页快照与引用。
- **继续已有工作**：任务历史、工具记录和检查点保存在本机，可回看进度、检查改动和继续对话。

## 快速开始

### 1. 打开应用

| 平台 | 从这里开始 |
| --- | --- |
| Windows 10/11 | [下载已发布版本](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases)中的 `TraverseBoard.exe`，双击打开；需要 WebView2。[下载说明](docs/windows-release.md) |
| macOS | [获取 Apple Silicon / Intel 预览包](docs/macos-release.md)，解压并打开 `TraverseBoard.app`。当前未公证，模型凭据配置仍有平台限制。 |

下面是当前 v1.0.0 源码中的 Windows 上手流程；历史预览的界面可能不同。需要试用最新源码时，见[本地构建](CONTRIBUTING.md#开发环境--development-environment)。

### 2. 连接模型

新建对话并选择项目文件夹。首次对话按提示连接模型，选择供应商并填写 API Key、模型名称及必要的服务地址，点击 **保存并检查**。检查通过后会回到原草稿并选中该模型；已有模型可在输入区切换。

需要模型服务的可用凭据和额度，登录应用账号不等于已连接模型。检查会调用所选服务并可能计费；原生搜索也取决于该服务是否支持。

### 3. 发出第一条任务

在这段对话中，先试一个容易核对结果的请求：

> 阅读这个项目，说明启动方式和主要目录，再指出一处值得改进的地方。

需要执行修改时，在当前任务选择合适的权限档。完成后查看文件差异、命令输出和引用；有后续要求，直接在同一对话继续。

## 按需了解更多

| 想做什么 | 文档 |
| --- | --- |
| 配置模型或使用 CLI | [使用手册](docs/usage.md) |
| 配置搜索、查看来源与排查失败 | [搜索与引用](docs/web-evidence.md) |
| 检查或恢复文件改动 | [工作区检查点](docs/workspace-checkpoints.md) |
| 接入 MCP、插件或代码智能 | [扩展](docs/extensions.md) · [LSP](docs/code-intelligence.md) |
| 从源码开发，了解设计 | [贡献指南](CONTRIBUTING.md) · [架构](docs/architecture.md) |
| 查看全部文档和开发历史 | [文档导航](docs/README.md) · [历史索引](docs/development-history.md) |

## 反馈与贡献

通过 [Issues](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues)反馈问题或建议，参与开发请阅读[贡献指南](CONTRIBUTING.md)。当前核心方向是通用 AI 编程工作流，详见[产品范围](docs/PRODUCT_SCOPE.md)。

## 许可证

[Apache License 2.0](LICENSE)。

**第三方字体声明：** 中文界面使用 Huawei Device Co., Ltd. 的 HarmonyOS Sans Fonts，完整[字体许可](web/public/licenses/HarmonyOS-Sans.txt)随软件发布；[来源与哈希](web/src/assets/fonts/PROVENANCE.md)可供核对。
