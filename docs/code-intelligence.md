# Code Intelligence / LSP 语义代码智能

`code-intel-lsp.v1` 是由 Go 控制的只读 Language Server Protocol 运行时。它只启动操作者明确审查并写入进程配置的本地 Server，不扫描仓库配置、不自动安装扩展，也不接受模型提供的 executable、argv、环境变量或 Workspace 根目录。

The Go-owned `code-intel-lsp.v1` runtime starts only explicitly reviewed local language servers. It never discovers or installs a server from repository content, and a model cannot supply or widen its process configuration.

## 模式和工具 / Mode and tools

语义工具与 `agent-code-tools.v1` 的只读 Workspace authority 使用同一边界：仅 Code Surface、root、Plan 或 Deliver 可用；Cyber 和 Specialist 始终拒绝。Profile 不会把只读语义证据变成写入、Shell、Git、网络或审批能力。

| Tool | LSP operation | Required input |
|---|---|---|
| `code_workspace_symbols` | `workspace/symbol` | bounded query |
| `code_document_symbols` | `textDocument/documentSymbol` | Workspace-relative file |
| `code_definition` | `textDocument/definition` | file and zero-based position |
| `code_references` | `textDocument/references` | file, position, declaration choice |
| `code_implementation` | `textDocument/implementation` | file and position |
| `code_hover` | `textDocument/hover` | file and position |
| `code_signature_help` | `textDocument/signatureHelp` | file and position |
| `code_diagnostics` | pull diagnostics, with bounded publish fallback | file |
| `code_call_hierarchy` | prepare plus incoming/outgoing calls | file, position, direction |
| `code_type_hierarchy` | prepare plus super/subtypes | file, position, direction |

只有 Server 在 `initialize` 中真实协商成功的 capability 才进入模型工具定义。缺少某项 capability 会让对应工具保持不可见；它不会用猜测或静态语言表补齐。

### 修改后的诊断反馈

`workspace_apply` 成功结果附带 `diagnostics`：文档相对路径、已应用的
`document_sha256`、`coverage: single_document_single_server`、Server ID 和状态。
已有就绪且匹配文件类型的 Server 按 ID 排序选一个，沿用 `code_diagnostics`
的策略、Run 权限和项目路径检查；写前基线和写后查询各有一秒预算。
不会为了反馈启动尚未初始化的 Server。移动文件查询目标路径。

`current` 或 `partial` 包含既有 LSP Result、文档版本与内容哈希；只覆盖这一个
文档和 Server。查询前后的文件必须仍匹配已应用哈希；push 通知必须明确携带
相同文档版本。未收到可信版本返回 `pending`，
无后端或策略拒绝返回 `unavailable`，请求超时返回 `timeout`。这些状态均保留
已成功写入的收据，不代表“没有错误”。摘要最多返回五项诊断，超过内联大小
上限时提示另行调用 `code_diagnostics`。反馈不运行测试，也不改变执行权限。

对于 replace，写入前即时取得绑定 `original_sha256` 的完整诊断，并与本次
`proposed_sha256` 的结果比较。`diagnostics.delta.status: current` 附带
`baseline_provenance`；`added` 是新出现的诊断，`resolved` 是已消失的诊断，
`persisting` 是两版都有的历史诊断，空类别省略。匹配包含 source、code、
severity、message 和 range；位置变化也记为旧位置消失、新位置出现。它记录
两次观察间的诊断差异，同时修改的其他文件也可能影响这些诊断。

比较要求两版诊断均为 `current`、完整且未截断，Workspace、文档 URI、commit、branch、Server
描述指纹及协商能力相同。Git 内容变化引起的同一受审 Server 重启允许产生新的
generation；两版 provenance 分别保留实际 generation。历史缓存不作为基线。
无可信版本、摘要截断、配置或能力更换、create/move、操作重放等情况下，
`delta.status: unavailable` 和 `reason` 说明原因，省略分类结果，仍保留当前
文档反馈和成功写入收据。

## 操作者配置 / Operator configuration

先取得注册 Workspace 的稳定 ID，再计算 Server executable 的 SHA-256：

```powershell
cyberagent workspace show demo
Get-FileHash -Algorithm SHA256 C:\Tools\gopls.exe
```

```bash
cyberagent workspace show demo
sha256sum /opt/prayu-tools/gopls
```

配置文件必须是显式选择的绝对、规范、非链接普通文件。`source` 不能由配置提供；Go 根据实际配置文件名和内容摘要生成该字段。以下是 gopls 示例，所有占位值都必须替换：

```json
{
  "protocol_version": "code-intel-config.v1",
  "servers": [
    {
      "protocol_version": "code-intel-lsp.v1",
      "id": "gopls-main",
      "name": "gopls",
      "workspace_id": "workspace-replace-with-real-id",
      "languages": [
        { "id": "go", "extensions": [".go"] }
      ],
      "executable": "C:\\Tools\\gopls.exe",
      "arguments": ["serve"],
      "executable_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
      "request_timeout_ms": 15000,
      "reviewed_by": "operator-name",
      "reviewed_at": "2026-08-20T00:00:00Z"
    }
  ]
}
```

TypeScript Language Server 使用 Node 时，Node 是受 SHA-256 约束的 executable，CLI 和 `tsserver.path` 是描述符指纹的一部分：

```json
{
  "protocol_version": "code-intel-config.v1",
  "servers": [
    {
      "protocol_version": "code-intel-lsp.v1",
      "id": "typescript-main",
      "name": "TypeScript Language Server",
      "workspace_id": "workspace-replace-with-real-id",
      "languages": [
        { "id": "typescript", "extensions": [".ts", ".tsx"] }
      ],
      "executable": "C:\\Program Files\\nodejs\\node.exe",
      "arguments": [
        "C:\\PrayuTools\\typescript-language-server\\lib\\cli.mjs",
        "--stdio"
      ],
      "executable_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
      "initialization_options": {
        "tsserver": {
          "path": "C:\\PrayuTools\\typescript\\lib\\tsserver.js"
        }
      },
      "request_timeout_ms": 15000,
      "reviewed_by": "operator-name",
      "reviewed_at": "2026-08-20T00:00:00Z"
    }
  ]
}
```

对于解释器型 Server，executable hash 只证明 Node 本身；argv 和 initialization options 虽进入 descriptor fingerprint，但运行时不会为整个 JavaScript 依赖树建立内容证明。生产配置应把精确版本的 Server 和 TypeScript 包放在操作者管理的只读目录中，独立验证包完整性，并在任一文件变化后重新审查。需要完整依赖树隔离时应再叠加 OS Sandbox；本协议不宣称提供该隔离。

配置最多包含 32 个 Server。语言、扩展名、argv、初始化 JSON、审查身份、超时和重复 Workspace/Server identity 都经过严格有界校验。Server executable 必须是绝对路径下的非链接普通文件；资格检查和每次启动前都会重新计算摘要。

## CLI、API 与 Desktop

```powershell
# 只加载配置并显示 metadata；不启动 Server。
cyberagent code-intel status --config C:\TraverseBoard\code-intel.json

# 只做 Workspace、审查和 executable hash 资格检查。
cyberagent code-intel qualify --config C:\TraverseBoard\code-intel.json `
  --workspace demo --start=false --json

# 资格检查后真实 initialize，并显示协商后的 capability/generation。
cyberagent code-intel qualify --config C:\TraverseBoard\code-intel.json `
  --workspace demo --json

# 普通 CLI/API 进程也可预先选择同一配置。
$env:CYBERAGENT_CODE_INTEL_CONFIG = "C:\TraverseBoard\code-intel.json"
cyberagent api serve --code-intel-config C:\TraverseBoard\code-intel.json

# Desktop 使用独立启动参数。
cyberagent-desktop --code-intel-config C:\TraverseBoard\code-intel.json
```

认证只读 API 提供：

```text
GET /api/v1/code-intel
GET /api/v1/code-intel?workspace_id=<registered-workspace-id>
```

响应只投影 source label/hash、语言、health、Server version、capabilities、model-visible tools、generation、capability fingerprint、有界错误、资格布尔值与待审查/已审查配置元数据。它从不返回 executable、argv、环境变量、凭证、原始 Server 日志或 Workspace 内容。`GET /api/v1/capabilities` 的 `code_intel_enabled` 只说明当前进程提供 Go 运行时；空配置、已审查配置与实际查询成功仍是不同状态，也不授予模型调用权限。

Web/Desktop 设置页使用同一 API 显示 Server 和资格状态。未显式指定配置文件的进程还可在设置页完成首次配置：

```text
POST /api/v1/code-intel/configurations
POST /api/v1/code-intel/configurations/{server_id}/review
POST /api/v1/code-intel/configurations/{server_id}/test
```

这三个入口需要 Control Bearer 和现有扩展控制开关。先登记已安装程序的描述符和 SHA-256，Go 保存当前进程内的待审查草稿；按指纹明确审查后，才以现有 `code-intel-config.v1` 格式保存到应用 home 的 `code-intel.json`。默认 Web/Desktop 下次启动加载这个文件。保存不启动服务器，后续测试操作才初始化所选服务器并执行一次只读文件符号或工作区符号查询，返回有界结果、来源与截断状态。

若通过参数或环境选择了显式配置，设置页保留状态读取但不提供配置写入；继续编辑所选文件并使用原 CLI 流程，或不指定该文件重启后使用设置管理。`extensions` inventory 的 `onboarding.lsp_configuration` 表示当前进程是否支持设置管理，旧后端缺少此字段时不开放操作。失败保留输入；可执行文件不可用或哈希变化需修正后重新登记、审查。待审查草稿不跨进程恢复。

模型工具仍由每轮 Supervisor 使用当前 Run 和 Workspace 重新生成，复用同一个 Go manager 和原有权限检查。操作者测试不会给模型新增能力或写入权限。

传入 `workspace_id` 时，Server inventory 与资格均只返回该精确 Workspace；未选择 Run 时 Desktop 才显示进程级全局 inventory。

## 证据和失效 / Evidence and invalidation

每个结果固定以下来源：

- Workspace ID、root fingerprint；
- Git repository availability、commit、branch、dirty 状态和 dirty digest；
- document URI、相对路径、SHA-256 和 LSP document version；
- Server ID、process generation 和 capability fingerprint；
- query fingerprint、稳定分页 cursor/offset、截断和 warning。

编辑文件、切换 branch/commit、改变 dirty 集合、换 Workspace root、重启或崩溃恢复 Server、改变协商 capability，都会使旧证据成为 `stale`。Server 不健康或运行时不存在时为 `unavailable`；Server 返回了被丢弃的越界项、fallback diagnostics、截断页或其他 warning 时为 `partial`。只有所有绑定仍相同才是 `current`。

`review@1.5.0` 和 `focused-checks@1.2.0` 会消费这些状态及 GitHub Review evidence graph，但语义 Server 结果始终只是 `semantic_language_server` 级证据，不是代码正确性证明、测试通过、审阅批准或写入授权。

## 生命周期与安全边界 / Lifecycle and trust boundary

- Go 负责 initialize、`didOpen`/`didChange`/`didClose`、shutdown/exit、请求取消、超时、崩溃重启和进程树清理。
- Windows 使用 kill-on-close Job Object；POSIX 使用独立 process group，并在 Linux 设置 parent-death signal。
- 环境只保留最小运行字段，覆盖临时 HOME/cache，关闭 Go/npm 网络解析，并不加载 Shell Profile 或传递凭证。
- 单帧、结果、日志、Markdown、link、diagnostic、hierarchy、页数和请求时间都有硬上限。
- Server 返回的每个 file URI 都重新通过 Workspace 路径策略；外部路径、链接/reparse point、大小写混淆、远程 URI 和非法转义被拒绝或降级为 partial。
- Markdown/link、控制字符和 secret-shaped 文本会被清洗；远程 link 不进入证据。

显式审查的 LSP Server 仍是本地用户权限下的真实进程。`network_access_granted=false`、去凭证环境和 `GOPROXY=off`/npm offline 是针路簿不授予网络意图的事实，不是 OS 防火墙或文件系统 Sandbox 的证明。不要把未知仓库提供的二进制、脚本或配置写入本配置；需要更强隔离时使用独立低权限账户、容器或其他 OS 级 Sandbox。

Git 来源绑定复用仓库的 exact-root 检查；带重定向 `.git` 文件的 linked worktree 当前会明确失败关闭，而不会悄悄省略 commit/branch 证明。脏状态摘要包含有界 Git index 与脏文件内容哈希；路径被清洗、状态被截断或内容超过绑定上限时，同样拒绝生成可能误标为 current 的语义证据。

## 验证 / Verification

普通测试使用协议级假 Server 覆盖崩溃、hang、cancel、oversize、非法 JSON-RPC、restart、进程回收、恶意 URI/link/secret 和稳定分页。CI 另在 Linux、macOS、Windows 安装固定版本的 `gopls v0.21.1`、`typescript-language-server 6.0.0` 和 `typescript 5.9.3`：gopls 必须协商并执行全部十项工具；TypeScript Server 必须执行其实际声明的九项工具，完整集合由两者共同覆盖。Linux 还运行 race/fault suite。

`TestRealGoplsAppliedDiagnosticDelta` 使用同一 gopls 版本，经操作者配置加载和
初始化，在普通目录及有初始 commit 的 Git 项目中执行
`workspace_change → workspace_apply`：新增类型错误、保留原错误，再修复新增
错误，并核对实际文件和前后诊断 SHA。包加载在自动反馈前预热，反馈沿用各一秒
预算。设置本地二进制路径后运行（PowerShell）：

```powershell
$env:CYBERAGENT_GOPLS_SMOKE_BINARY = 'C:\Tools\gopls.exe'
go test ./internal/application -run '^TestRealGoplsAppliedDiagnosticDelta$' -count=1 -v
```

未设置该环境变量时，此项真实后端测试会跳过。
