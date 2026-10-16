# 为 Universal Code 贡献 / Contributing to Universal Code

感谢你参与 Universal Code。这个仓库把“可恢复、可审计、默认关闭高权限能力”视为产品合同，而不只是实现细节。请优先提交范围清晰、可以独立验证的纵向切片；涉及执行权、凭证、网络、持久化或信任边界的改动，应先把威胁模型和失败语义写清楚。

Thank you for contributing to Universal Code. Resumability, auditability, and fail-closed authority are product contracts here. Prefer small, independently verifiable vertical slices. Changes involving execution, credentials, networking, persistence, or trust boundaries need an explicit threat model and failure semantics.

## 开始之前 / Before you start

1. 阅读 [架构说明](docs/architecture.md)、[项目状态](docs/PROJECT_STATUS.md) 和与改动最接近的 [ADR](docs/adr/)。长期任务还应核对 [任务书](docs/TASK_BOOK.md) 与 [项目记忆](docs/PROJECT_MEMORY.md)，避免重复已完成的切片或把历史计划当成当前能力。
2. 对行为变化先开 Issue 或 Draft PR，说明用户目标、信任边界、最小验收条件和明确不在范围内的事项。新增控制平面、授权路径、数据库状态机或跨语言所有权时，先新增 ADR；沿用既有决策的小修复可以在 PR 中引用相关 ADR。
3. 不要把公开百分比、历史测试记录或 `release_ready` 文案当成验证结果。PR 只声明你在当前提交上实际运行过的检查。

## 控制器装配契约 / Controller assembly contracts

HTTP 控制器窄接口增加方法时，同步更新 `httpapi.New` 的能力开关校验和真实生产控制器的装配、路由或能力投影测试。启用的已发布能力缺接口时应在构造期返回明确错误，不能通过运行时类型断言静默变成 404/503 或 false。关闭的可选能力仍可使用最小控制器；凭据等后端是否可用由显式能力方法表达。Toolgateway 的可选 Script Process 存储通过 `ScriptProcessCapabilities` 返回缺失契约原因，生产 SQLite 装配测试必须保证其能力仍完整。

When extending a narrow HTTP controller interface, update the capability-gated checks in `httpapi.New` and tests using concrete production controllers. An enabled published capability must reject a missing contract during construction rather than silently projecting 404/503 or false. Keep minimal controllers valid for disabled optional capabilities and express backend availability explicitly. Toolgateway reports optional Script Process storage contracts through `ScriptProcessCapabilities`; its production SQLite assembly test must retain complete support.

## 开发环境 / Development environment

仓库和 CI 当前固定以下主版本：

| 工具 | 版本 | 来源与用途 |
|---|---:|---|
| Go | 1.26.9 | [`go.mod`](go.mod)；唯一控制平面、CLI、TUI、HTTP API 与持久化 |
| Node.js | 24 | [CI](.github/workflows/ci.yml)；`web/` 下的 React/Vite 控制台 |
| Rust | 1.97.1 | [`analyzers/rust-toolchain.toml`](analyzers/rust-toolchain.toml)；确定性 Analyzer 夹具 |

Git 需要 2.41+；修改 Analyzer 时另需 rustup。普通 Go/Web 开发不要求 Docker、真实 Provider 或 API key；未配置外部模型时使用 Mock Provider。`configs/models.yaml` 是无秘密的示例，不是运行时配置源。

Go 构建使用 `go.mod` 中的精确补丁版本；本机安装其他版本时，设置
`GOTOOLCHAIN=go1.26.9`。较新的 Go 发布分支也可能尚未修复同一漏洞，不能只按版本大小判断安全性。

首次检出后，按你要修改的表面准备依赖：

```bash
# Go control plane
go mod verify

# React/Vite console
cd web
npm ci
cd ..

# Rust analyzer fixtures
rustup toolchain install 1.97.1 --profile minimal --component rustfmt --component clippy
rustup target add wasm32-wasip1 --toolchain 1.97.1
```

### Windows Desktop

桌面开发和便携构建必须在 Windows 10/11 上进行，并安装 Microsoft Edge WebView2 Evergreen Runtime `94.0.992.31` 或更新版本。还需要上表中的 Go 1.26.9、Node.js 24 和 PowerShell；Rust 只在修改或完整验证 Analyzer 路径时需要。应用会在打开 SQLite 前检查 WebView2，缺失或过旧时失败关闭，不会自行下载或安装。

```powershell
# 在仓库根目录构建未签名的本地测试包
./scripts/build-desktop.ps1

# 发布候选验证会连续构建并比较 SHA-256
./scripts/build-desktop.ps1 -VerifyReproducible
```

产物位于忽略的 `build/desktop/`。它不是已签名发行版；自动检查通过也不能替代待完成的 Windows 10/WebView2/显示缩放人工矩阵。更多边界见 [Desktop Plan](docs/DESKTOP_PLAN.md) 和包内使用的 [本地测试说明](packaging/windows/LOCAL-TEST-GUIDE.txt)。

### macOS Desktop

macOS 便携构建需要 macOS 12+（Monterey）与 Xcode 命令行工具（codesign），以及上表中的 Go 1.26.9、Node.js 24；WKWebView 随系统提供，不需要 WebView2 式预检。前端测试环境固定在 Node.js 24 基线：本地 Node 版本不符时脚本会在前端检查前明确报错（可用 `nvm use 24` 切换，或 `-SkipFrontend` 跳过已构建好的 web/dist）。

```bash
# 在仓库根目录构建未签名、ad-hoc 签名的本地测试包
./scripts/build-desktop-darwin.sh

# 发布候选验证会连续构建并比较 SHA-256
./scripts/build-desktop-darwin.sh -VerifyReproducible

open build/desktop/TraverseBoard.app
```

产物位于忽略的 `build/desktop/`。它不是已签名/已公证发行版；系统凭证库、ConPTY 用户终端、受限浏览器与完整 CDP 在 macOS 保持关闭或失败关闭，凭证使用环境变量。更多边界见 [ADR 0097](docs/adr/0097-macos-desktop-portable-build.md) 和包内使用的 [本地测试说明](packaging/macos/LOCAL-TEST-GUIDE.txt)。

## 从源码运行 / Run from source

```powershell
git clone https://github.com/CWNU-Open-Source-Community/Universal-Code.git
cd "Universal-Code"

go run ./cmd/cyberagent version
go run ./cmd/cyberagent provider list
go run ./cmd/cyberagent workspace init demo
go run ./cmd/cyberagent workspace list
go run ./cmd/cyberagent tui
```

默认配置使用确定性的 Mock Provider，不需要 API key。接入外部模型前，请先阅读 [Provider 与模型配置](docs/usage.md#model-and-provider-commands)；凭证应进入系统凭证存储或进程环境，不能提交到仓库。

OpenAI-compatible 连接使用独立的 `CYBERAGENT_OPENAI_API_KEY`、
`CYBERAGENT_OPENAI_BASE_URL` 与 `CYBERAGENT_OPENAI_MODEL` 环境变量；后两项默认分别为
`https://api.openai.com` 和 `gpt-4.1-mini`。仓库内的 `configs/models.yaml`
只是无秘密示例，不会作为运行时配置源。

本地 Ollama 是唯一无凭证 Provider，只在显式设置 `CYBERAGENT_OLLAMA_BASE_URL`（仅
loopback `http`，默认 `http://127.0.0.1:11434`）与 `CYBERAGENT_OLLAMA_MODEL` 时启用；
非 loopback、HTTPS、redirect 与代理绕过一律拒绝。tools/vision/JSON/context 能力按
`/api/show` 探测结果失败关闭，不自动安装 Ollama、不 pull 模型、不扫描局域网。

The default configuration uses the deterministic Mock Provider and requires no API key. Read [Model and Provider Commands](docs/usage.md#model-and-provider-commands) before connecting an external model. Credentials belong in the OS credential store or process environment, never in the repository.

Local Ollama is the only keyless Provider and enables only when `CYBERAGENT_OLLAMA_BASE_URL` (loopback `http` only, default `http://127.0.0.1:11434`) and `CYBERAGENT_OLLAMA_MODEL` are set explicitly. Non-loopback hosts, HTTPS, redirects, and proxy bypasses are rejected; tools/vision/JSON/context capabilities fail closed from `/api/show` probing, and Traverse Board never installs Ollama, pulls a model, or scans the LAN.

## Repository layout / 项目结构

| Path | Purpose |
|---|---|
| `cmd/cyberagent` | CLI, TUI, and API entry point |
| `cmd/cyberagent-desktop` | Windows/macOS Desktop shell |
| `internal/` | Go domain, application, Policy, Store, Tool, Sandbox, and HTTP control plane |
| `web/` | React/Vite operator UI; owns no authority, secrets, or executor |
| `analyzers/` | Deterministic Rust Analyzer and shared vectors |
| `configs/` | Secret-free configuration templates |
| `docs/` | Architecture, usage, status ledgers, ADRs, and product scope |
| `packaging/` | Local portable preview and test guidance |

## 设计与实现原则 / Design and implementation principles

### 提交一个纵向切片

一个功能切片通常应同时覆盖：

1. Go 领域合同和状态转换；
2. SQLite 约束、迁移与恢复/重放语义（如有持久状态）；
3. Policy、Scope、Approval、Budget、Capability 和执行租约中适用的门禁；
4. CLI、HTTP/OpenAPI、Web 或 Desktop 中真正需要的最小产品入口；
5. 正常、拒绝、取消、超时、不确定提交和重启恢复测试；
6. 对应 ADR、使用说明和项目状态更新。

不要只在 React 中模拟 Go 状态，也不要让 Rust、浏览器或桌面原生绑定形成第二套控制平面。允许的方向始终是 `TypeScript -> Go -> Rust/Docker/LLM`；授权、凭证、持久化和工作区权限由 Go 拥有。

### 保持安全不变量

- 默认失败关闭。配置档位、界面选项或模型输出都不能自行授予执行、网络、文件写入或安装权限。
- 把仓库内容、模型文本、Tool 输出、外部 Skill、Analyzer 结果和网络内容视为不可信证据，而不是指令或验证事实。
- 高权限动作继续经过独立的 Scope、Policy、人工确认、预算、Capability、幂等/恰好一次与租约检查；不能通过新增旁路“临时”绕过。
- 用户可见状态必须来自持久事实。模型声称“完成”不能替代 Finding、Evidence、Verification、Receipt 或测试结果。
- 新的 mutation 必须定义重放键、冲突、取消、崩溃和恢复行为。对外 DTO 不暴露 lease/fencing identity、operation key/digest、原始 Tool 参数或私有生命周期。
- 变更执行权限、信任边界、跨语言所有权、对外协议或不可逆迁移时，提交新的顺序编号 ADR，并写清备选方案、拒绝路径和剩余未授权能力。

数据库迁移必须只向前、可在现有数据库上重放，并同时在 Go 与 SQLite 层保留关键不变量。不要手工改写用户数据库，也不要通过迁移猜测过去不存在的授权事实。

## OpenAPI 与生成文件 / OpenAPI and generated files

Go DTO 和显式路由目录是 API 的唯一来源。修改 HTTP 路由或 DTO 后，从仓库根目录重新生成并提交这两个受测快照：

```bash
go run ./cmd/cyberagent api openapi --output docs/openapi.json
cd web
npm run generate:api
cd ..
```

不要手工编辑 `docs/openapi.json` 或 `web/src/api/schema.d.ts`。`npm run check:api` 会重新生成 TypeScript 类型并在存在漂移时失败。生成的 DTO 仍须排除凭证、Workspace 绝对根路径、Artifact/Session 私有正文、模型原始输出、Tool 参数以及 lease/fencing 标识。

## 验证 / Validation

先运行与改动最接近的聚焦检查，再在提交前扩大范围。以下命令均来自当前仓库脚本或 CI；不要在 PR 中勾选未实际运行的项目。

### 聚焦检查

```bash
# Go：以下为 HTTP/OpenAPI 改动的示例；换成实际受影响包
go test -count=1 ./internal/httpapi
go test -count=1 ./internal/httpapi -run '^TestOpenAPIDocumentIsDeterministicCapabilitySeparatedAndSecretFree$'

# Web
cd web
npm test -- src/api/client.test.ts
npm run typecheck

# Rust analyzer
cd analyzers
cargo fmt --all -- --check
cargo test --locked
cargo clippy --locked --all-targets -- -D warnings
```

如改动影响并发、恢复、状态机或跨进程边界，应对受影响 Go 包增加 `go test -race -count=1 ...`。如改动影响 Desktop，至少运行 CI 使用的安全 tag 边界：

```powershell
# Windows
go test -tags "desktop,wv2runtime.error" -count=1 ./cmd/cyberagent-desktop ./internal/desktop ./internal/webui
```

```bash
# macOS
go test -tags "desktop" -count=1 ./cmd/cyberagent-desktop ./internal/desktop ./internal/webui
```

### 提交前检查

对跨层功能或准备合并的改动，运行适用的完整检查：

```bash
# Go
go mod verify
go test -timeout 20m -count=1 ./...
go vet ./...

# Web
cd web
npm ci
npm run check:api
npm test
npm run build

# Rust
cd analyzers
cargo fmt --all -- --check
cargo test --locked
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --bin cyberagent-analyzer-fixture
cargo +1.97.1 build --locked --target wasm32-wasip1 --package cyberagent-analyzer-fixture --release
```

完整 `go test -race -timeout 20m -count=1 ./...`、`staticcheck ./...`、`govulncheck ./...`、依赖审计和 Windows 可复现构建适合权限/持久化/并发边界、累计发布门或维护者要求的改动。它们可能较慢或需要额外工具；如果本机无法运行，请在 PR 中明确列为“未运行”并依赖相应 CI/维护者门禁，不要把缺失工具写成通过。

最后运行：

```bash
git diff --check
git status --short
```

确认生成文件已提交、没有意外构建产物，且所有测试结果都对应当前提交。

## 数据、凭证与测试隔离 / Data, secrets, and test isolation

- 绝不要提交 API key、Bearer/control token、Cookie、凭证截图、`.env`、Provider 原始响应或真实模型 Prompt/Tool payload。
- 不要提交 `CYBERAGENT_HOME` 中的数据、`.cyberagent-workbench/`、`cyberagent.db`、SQLite/WAL 文件、`build/`、`web/dist/`、`node_modules/` 或 Rust `target/`。
- 手工/集成测试应使用独立临时目录或专用 `CYBERAGENT_HOME`，并默认使用 Mock Provider。不要在测试中扫描公网、产生攻击流量、启动未授权 Docker/LocalRunner，或读取系统凭证。
- Fixture 应最小化、确定、去标识化。日志、错误、Artifact 和快照必须经过现有脱敏边界；不要为了测试方便降低上限或把私有正文写入公开投影。
- Store 迁移测试使用 `openHistoricalTestDatabase(t, path, version)` 准备所需历史版本，在独立副本中写入旧数据，再通过 `Open` 或被测迁移执行升级。该 helper 复用真实历史迁移生成的数据库，不共享连接或测试数据；准备失败由 helper 直接报告。
- 检验迁移引擎、事务回滚及历史 schema 的独立对照测试仍从空数据库逐步执行迁移。新增升级测试直接选择所需历史起点，避免先建到较新版本再维护一串反向删除语句；原有数据保留、约束、拒绝及恢复断言应随测试保留。

如果误提交了秘密，请立即停止推送并轮换凭证；仅从 Git 历史删除并不能撤销已经泄露的值。不要在公开 Issue 中粘贴可利用细节、可用凭证或敏感主机数据；如果尚无私密报告渠道，只提供最小、脱敏的影响说明，并请求维护者建立私密沟通。

## 提交与 PR / Commits and pull requests

- 从最新 `main` 创建主题分支，每个 PR 保持一个可审阅的目标；不要夹带格式化、依赖升级或历史状态重写。
- 使用简短、祈使语气的提交标题。正文解释行为、边界或迁移理由，而不是逐文件复述 diff。
- 完整填写 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md)：说明改了什么、为什么现在需要、用户/开发者影响、实际验证和未运行的检查。
- 行为或进度变化时更新相应使用文档、ADR 和当前状态，但不要重写历史 ADR/Progress 记录来伪造连续性。
- 明确列出安全审计结果：是否新增权限、网络、进程、文件写入、凭证、持久化或外部输入；如果答案是否，也应说明如何保持现有不变量。

小而完整的 PR 比跨越多个信任边界的大改写更容易验证和恢复。遇到不确定的授权设计时，请先提交 ADR 或 Draft PR 讨论。
