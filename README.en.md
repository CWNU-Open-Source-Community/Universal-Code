<div align="center">
  <img src="assets/branding/traverse-board-mark.png" alt="Universal-Code icon" width="180">
  <h1>Universal-Code</h1>
  <p><strong>Work with AI in your local project: edit code, run commands, research, and review results.</strong></p>
  <p>
    <a href="README.md">简体中文</a> |
    <a href="README.en.md">English</a>
  </p>
  <p>
    <a href="https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/CWNU-Open-Source-Community/Universal-Code/ci.yml?branch=main&style=flat-square"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/CWNU-Open-Source-Community/Universal-Code?style=flat-square"></a>
  </p>
</div>

Universal Code is a desktop AI coding workbench. Connect your chosen model and move a project forward in one conversation, with file changes, command results, and sources available for review.

**v1.0.0 “First Voyage” is being prepared for release.** The [release notes](docs/releases/v1.0.0.md) record verified scope and remaining work. Public downloads still include older previews; new Mac packages are unnotarized previews.

## Why Universal Code?

- **Work on a real project**: read and edit files, run builds, and inspect the resulting changes.
- **Fewer interruptions, with control**: select Full Access for a task to allow ordinary file changes and network commands to proceed automatically; file deletion still requires confirmation.
- **Keep research traceable**: prefer native search when the provider supports it, read GitHub, Hacker News, and RSS/Atom sources, and retain page snapshots and citations.
- **Continue existing work**: task history, tool records, and checkpoints stay on your machine so you can inspect progress and continue the conversation.

## Quick start

### 1. Open the app

| Platform | Start here |
| --- | --- |
| Windows 10/11 | Download `TraverseBoard.exe` from a [published release](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases) and double-click it. WebView2 is required. [Download details](docs/windows-release.md) |
| macOS | Get an [Apple Silicon / Intel preview](docs/macos-release.md), extract it, and open `TraverseBoard.app`. Packages are unnotarized; model credential setup still has platform limitations. |

The Windows steps below describe the current v1.0.0 source. Older preview interfaces may differ. To try current source, see the [local build instructions](CONTRIBUTING.md#开发环境--development-environment).

### 2. Connect a model

Create a conversation and select a project folder. Follow the first-conversation prompt to select a provider and enter its API key, model name, and service URL where needed. Choose **保存并检查 / Save and check**. A successful check returns to your draft with that model selected; configured models can be switched in the composer.

You need valid model-service credentials and available quota; signing into the app does not connect a model. Checks call the selected service and may incur charges. Native search also requires provider support.

### 3. Start a task

In that conversation, try a request whose result is easy to review:

> Read this project, explain how to run it and its main directories, then suggest one improvement.

Choose the appropriate task permission before making changes. Review file diffs, command output, and citations, then send follow-up requests in the same conversation.

## Explore when needed

| I want to… | Documentation |
| --- | --- |
| Configure models or use the CLI | [Usage guide](docs/usage.md) |
| Configure search, inspect sources, or diagnose failures | [Search and citations](docs/web-evidence.md) |
| Inspect or restore file changes | [Workspace checkpoints](docs/workspace-checkpoints.md) |
| Add MCP, plugins, or code intelligence | [Extensions](docs/extensions.md) · [LSP](docs/code-intelligence.md) |
| Develop from source or understand the design | [Contributing](CONTRIBUTING.md) · [Architecture](docs/architecture.md) |
| Browse all documentation and development history | [Documentation index](docs/README.md) · [History](docs/development-history.md) |

## Feedback and contributions

Use [Issues](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues) for bugs and suggestions, and read the [contribution guide](CONTRIBUTING.md) before developing. The active focus is general AI coding workflows; see [Product scope](docs/PRODUCT_SCOPE.md).

## License

[Apache License 2.0](LICENSE).

**Third-party font notice:** the Chinese interface uses HarmonyOS Sans Fonts by Huawei Device Co., Ltd. The full [font license](web/public/licenses/HarmonyOS-Sans.txt) ships with the app; [provenance and hashes](web/src/assets/fonts/PROVENANCE.md) are available for inspection.
