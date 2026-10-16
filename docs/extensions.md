# MCP Client、Plugin 与受限生命周期 Hooks

本文说明 schema v120-v121 引入的低信任扩展运行时及后续 Universal Code 接线。英文说明见下半部分。

Windows 桌面版内置的应用操作服务见 [Computer Use 使用与授权](computer-use.md)。它复用下述 MCP 运行时，由宿主提供固定执行器和工具清单。

## 安全模型

MCP Server、Plugin 包、Skill 正文、Hook 注释和远程返回值都不是新的控制平面。Go 仍拥有状态机、凭证、工具注册表、JSON Schema 校验、Policy、预算、Run/Workspace Scope 和最终调用权。

- 扩展只在 Code Surface 生效；Cyber Surface 不公开 MCP 工具。
- MCP 工具仍受精确的 `Code/Deliver/root`、当前 Run/Agent attempt、运行时激活与租约约束；公开的新权限写入口是 `ask`、`auto`、`full`。连接、发现和工具调用使用公共操作授权。MCP 声明不能证明外部副作用安全：Ask/Auto 需要对精确的 Server 启动/发现与工具调用审批，Full 需要当前激活，既有待审或拒绝决定仍有效。每次调用都会重新发现能力并核对已批准的 capability fingerprint；漂移立即隔离。远程 input schema 只允许同一文档内的 `#...` 引用，外部/动态引用在 discovery 时被拒绝，编译器也被禁止加载文件或 URL。
- 远程 endpoint 必须是无 userinfo、query、fragment 的固定 HTTPS URL，redirect 被拒绝。Bearer 由系统凭证存储按引用注入，模型、Plugin、SQLite、HTTP 和 Desktop 都拿不到明文。
- stdio 启动使用宿主解析并固定的可执行文件、cwd 和显式环境，不自动继承任意父进程环境。标准 Agent Plugins 的 cwd/env 由其 LaunchSpec 与宿主安装/数据根解析；旧 descriptor 适配保留自身的临时目录和最小环境，以及在持久化前拒绝 secret-shaped argv 的规则。stdio 仍是宿主进程，三档操作审批不构成 Docker/OS 网络沙箱。
- MCP 结果按不可信证据处理，经过 UTF-8 修复、控制字符清理、脱敏和大小上限后才返回模型。专用 MCP 调用账本只保存参数摘要、能力指纹、状态、大小与时间；为保证 Supervisor 崩溃恢复，通用工具账本会保存同样经过 schema 校验、脱敏和大小限制的规范化调用/结果，但两者都不保存 bearer 或 transport 原始字节。
- 旧 `plugin.v1` 包的安装／staging 不执行安装脚本、仓库 Hook、二进制或包管理器生命周期。ZIP staging 只解析严格 JSON/UTF-8/PNG/WebP/Skill 资源，并拒绝 zip-slip、symlink、重复路径、额外文件、超限和摘要不一致。
- Hook 是 Go 解释的声明，不是脚本。它只能 `deny`、`annotate`、`record`，或在 `pre_tool` 删除顶层参数字段；不能增加参数、权限、网络、预算或重入。

## MCP Client 两阶段审查

一个 descriptor 固定 `transport`、`target`、来源、声明能力、凭证引用、Scope、超时和返回上限。状态流如下：

```text
staged
  -> approve_discovery
  -> capabilities_pending  (真实 initialize + tools/resources/prompts discovery)
  -> enable_capabilities   (固定 capability fingerprint)
  -> enabled
  -> disabled | quarantined | revoked
```

先创建 descriptor JSON，再执行：

```powershell
cyberagent mcp client stage --file .\server.json
cyberagent mcp client review <server-id> --action approve_discovery `
  --descriptor-fingerprint <sha256> --by <actor>
cyberagent mcp client refresh <server-id>
cyberagent mcp client review <server-id> --action enable_capabilities `
  --descriptor-fingerprint <sha256> --capability-fingerprint <sha256> --by <actor>
cyberagent mcp client list --workspace <workspace-id> --run <run-id>
cyberagent mcp client calls --run <run-id>
```

远程凭证只通过命名引用管理：

```powershell
$env:PRAYU_MCP_TOKEN = '<access-token>'
cyberagent mcp credential set github-mcp --from-env PRAYU_MCP_TOKEN --confirm
cyberagent mcp credential status github-mcp
Remove-Item Env:PRAYU_MCP_TOKEN
```

CLI 只打印 `configured` 和凭证存储类型，不提供 readback。Server 在 discovery 中断、重启时处于 `connecting`，过期的持久 lease 会被确定性转为 `quarantined/unavailable`；调用阶段的连接、协商或工具失败会立即标记为 `unavailable` 并从模型工具投影移除，直到显式 refresh 再次真实发现。系统不会凭旧数据库记录自动恢复执行权。

## `plugin.v1`

Plugin ZIP 根目录必须含 `plugin.json`，签名包另含 `SIGNATURE.json`。manifest 的 `files` 是完整白名单，能力只能是 `skills`、`mcp_servers`、`ui_metadata`、`hooks`，且声明与实际 contribution 必须完全一致。Ed25519 签名绑定规范化 package fingerprint；签名有效不等于 publisher 已受信。

安装流程为：

```text
staged -> approved -> enabled -> disabled/quarantined/revoked
                              \-> rolled_back (由原子 rollback 替换)
```

典型命令：

```powershell
cyberagent plugin stage --file .\plugin.zip --by <actor>
cyberagent plugin import-url --url https://plugins.example/plugin.zip `
  --sha256 <archive-sha256> --by <actor>
cyberagent plugin import-git --repo https://github.com/example/plugin.git `
  --commit <full-commit-sha> --archive dist/plugin.zip --by <actor>
cyberagent plugin trust-publisher <installation-id> --by <actor>  # 同一动作已在桌面/Web 的“发布者信任”区提供，需绑定当前指纹与代次并显式确认
cyberagent plugin review <installation-id> --action approve `
  --fingerprint <sha256> --generation <n> --by <actor>
cyberagent plugin review <installation-id> --action enable `
  --fingerprint <sha256> --generation <n> `
  --capabilities skills,mcp_servers,ui_metadata,hooks --by <actor>
cyberagent plugin stage-mcp <installation-id> --scope workspace `
  --workspace <workspace-id>
```

HTTPS 导入要求预先给出 archive SHA-256 并拒绝所有 redirect。Git 导入只接受无凭证的固定 HTTPS repository、完整 commit SHA 和安全的仓库相对 ZIP 路径；它在临时 bare repository 中 fetch 精确 commit，禁用系统/全局配置、URL rewrite、credential helper、Hook 和 HTTP redirect，默认拒绝其他 transport，不 checkout、不加载子模块，只读取普通 blob。未签名或 publisher 未受信的包需要 `--confirm-untrusted` 才能批准或启用；明确 revoked 的 publisher 不能用该开关绕过，必须单独重新建立 publisher trust。升级启用会在同一事务中把精确 predecessor 转为 `rolled_back`，数据库保证每个 Plugin ID 最多一个 enabled 版本。Publisher revoke 会原子撤销其信任并撤销关联安装；rollback 同时固定当前/目标 package fingerprint、generation 和重新启用的能力。Plugin 声明的 MCP Server 仍只进入独立 MCP staging，不能借 Plugin 审查跳过 Server discovery/capability 审查。

`skills` capability 只审查并启用包内声明式 Skill 资源，不会因 Plugin 启用而自动选择、注入或授权任何 Run；后续消费仍必须经过既有的显式 Skill 安装/选择与模式兼容流程。UI metadata 同样只是惰性展示数据，不加载包内脚本。

## 原生 Skill 目录导入、发现与版本切换

原生 Agent Skill 或标准 Agent Plugins 目录使用原始 `SKILL.md` 和资源文件；导入保留所选目录的不可变快照。确认只启用 Skill 读取，不执行脚本或建立 MCP 连接：

```powershell
cyberagent skill import-dir .\my-skill --surface code `
  --operation-key my-skill-v1 --confirm-untrusted-skill
cyberagent plugin list
cyberagent plugin show <installation-id>
```

Supervisor 首次最多展示 32 条已安装 Skill 摘要。更多摘要通过 `skill_read` 的 `{"catalog":true}` 请求及返回的 `next_request` 分页取得，不会因第 33 项让 Run 失败。分页只返回元数据，不占用激活名额；读取正文仍核对当前启用状态、surface、revision 和 generation。单次正文或资源读取仍有 64 KiB 上限，暂不支持内容分页。

要为当前任务明确指定 Skill，在新任务的任务设置中选择已安装技能，再确认使用其不可信上下文。
候选同时包含旧登记、已启用的版本化 Skill 包和原生 `SKILL.md` 组件，并按当前工作区、
工作面、阶段与任务类型核对资格。无需编辑 `.prayu/config.yaml`。原生 Skill 保存真实安装、
组件与内容版本的绑定；停用、撤销或版本改变后，旧选择不能恢复权限。手动选择沿用每任务
最多 4 项、合计最多 4096 Token 的边界，超出预算会拒绝而不会截断正文。

网页的“Skill 包”入口目前上传版本化 ZIP，新导入内容在扩展页审查与启用；历史 Skill
登记列表用于兼容旧安装和移除。原生目录可通过上述 `skill import-dir` 导入后，在任务
设置中手动选择。网页直接导入原生目录、URL/Git 导入和旧 Skill Catalog 发布者信任的
GUI 入口仍由 #334 跟踪。

同一来源的新 revision 使用新的 operation key 导入。如果旧 revision 仍启用，命令会报告 `another plugin version is enabled`；此时新 revision 已保留为 `approved`，旧 revision 继续启用。通过 `plugin list`、`plugin show` 核对两者的来源和 revision，并从各自最新输出取得 ID、`package_fingerprint` 与 `generation`，然后显式切换：

```powershell
cyberagent plugin review <old-installation-id> --action disable `
  --fingerprint <old-package-fingerprint> --generation <old-generation> --by operator
cyberagent plugin review <new-installation-id> --action enable `
  --fingerprint <new-package-fingerprint> --generation <new-generation> `
  --capabilities skills --confirm-untrusted --by operator
cyberagent plugin show <old-installation-id>
cyberagent plugin show <new-installation-id>
```

这两个步骤之间没有启用的 revision；这是显式停用/启用流程，不是原子替换。旧 revision 和对象仍保留可检查。用原 operation key 重试相同来源内容不会恢复已停用权限，也不会增加 generation；内容变化必须使用新的 operation key。

## Hook 真实边界

当前会触发以下固定事件：

| 事件 | Go-owned 边界 |
|---|---|
| `pre_tool` / `post_tool` | 统一 Tool Gateway 调用前/后 |
| `run_started` | Run 从 created 进入首次运行 |
| `run_completed` | completed/failed/cancelled 或 Supervisor finalize 写入前 |
| `session_opened` | 新 Session 与 Run 原子创建前 |
| `session_closed` | HTTP/Desktop Session archive 写入前 |
| `compaction` | `MaybeCompact` 确认真正需要压缩后、摘要写入前 |
| `subagent` | 经操作者授权的 Specialist schedule 获取执行租约前 |
| `checkpoint` | 连续性或手工 Workspace Checkpoint 写入前 |

同一事件按 event、Plugin ID、Hook ID 稳定排序。每条声明有最大 2 秒超时，输入 256 KiB、输出 16 KiB、注释 16 条的上限；`failure_policy=deny` 失败关闭，`continue` 只记录失败并继续。每个结果写 metadata-only audit，不保存 Hook 输入、注释正文或工具 payload。

## Desktop 与 HTTP

Desktop 设置页的“**MCP 与 Plugin**”按选定 Run/Workspace 展示来源、transport、健康、审查状态、能力、指纹和 metadata-only 调用收据，可用当前 fingerprint/generation 立即禁用一个 Server 或 Plugin。控制动作需要控制 bearer；普通 read bearer 只能读取脱敏投影。HTTP 永不返回 credential value、MCP 参数/结果、Plugin archive 或 publisher public key。

---

# MCP Client, Plugins, and restricted lifecycle hooks

Schemas v120-v121 introduced the low-trust extension runtime; Universal Code adds the current launch and operation-authority wiring. It creates no second control plane. Go still owns state, credentials, tool registration, schemas, Policy, budgets, scope, and final invocation authority.

## Trust and execution boundaries

- Extensions remain Code-surface only. MCP admission requires the exact `Code/Deliver/root` scope, current Run/Agent attempt, runtime activation, and lease. New public permission writers use `ask`, `auto`, and `full`; connect, discovery, and tool calls use the common operation authorizer. Server declarations do not verify external effects, so Ask/Auto require approval of the exact server startup/discovery and tool call. Full requires current activation and preserves existing pending or denied decisions.
- Discovery is reviewed in two stages. Every call rediscovers capabilities and compares the approved fingerprint; drift quarantines the server before the tool executes. Remote input schemas may use only document-local `#...` references; external/dynamic references are rejected during discovery and the compiler cannot load files or URLs.
- Remote transports require a fixed HTTPS URL and reject redirects. A bearer is fetched from the OS credential store by name and injected only into the outbound request.
- A stdio launch pins the host-resolved executable, cwd, and explicit environment without arbitrary parent-environment inheritance. Standard Agent Plugins resolve cwd/env from their LaunchSpec and host-managed install/data roots; legacy descriptors retain their temporary-directory and minimal-environment adapter and reject secret-shaped argv before persistence. Stdio remains host execution; three-mode operation approval does not provide Docker or OS network isolation.
- Remote output is untrusted, bounded, control-stripped, UTF-8 repaired, and redacted. Dedicated MCP audits contain hashes and metadata only. The generic Supervisor recovery ledger retains schema-validated, redacted, bounded canonical calls/results, but neither ledger stores bearer values or raw transport bytes.
- `plugin.v1` packages are inert. Staging validates a complete file allowlist, digests, bounds, strict contribution formats, and an optional Ed25519 signature; package code, lifecycle scripts, symlinks, and undeclared files are rejected.
- Declarative hooks may deny, annotate, record, or remove top-level fields at `pre_tool`. They cannot execute code, widen a call, grant authority, add network/budget, or recurse.

The MCP state flow is `staged -> approve_discovery -> capabilities_pending -> enable_capabilities -> enabled`. Disable and revoke are immediate; capability or restart ambiguity quarantines the server. A connection, discovery, or tool-call failure during invocation marks it unavailable and removes it from model-visible capabilities until an explicit refresh succeeds. Plugin installation is independently `staged -> approved -> enabled`, with capability-by-capability enablement, publisher trust/revocation, atomic upgrade/rollback, and a separate MCP review for contributed server descriptors. HTTPS import requires an exact archive SHA-256 and rejects every redirect. Git import accepts only a credential-free HTTPS repository, a full commit SHA, and a safe repository-relative ZIP path; it fetches into a temporary bare repository with system/global Git config, URL rewrites, credential helpers, hooks, redirects, and non-HTTPS protocols disabled, performs no checkout or submodule load, and reads only the exact ordinary blob. A revoked publisher cannot be bypassed with untrusted confirmation, and the database permits at most one enabled version of each Plugin ID. Enabling a Plugin's `skills` capability never auto-selects, injects, or authorizes those inert resources for a Run; the existing explicit Skill installation, selection, and mode-compatibility path remains authoritative.

Actual hook boundaries are Tool pre/post, initial Run start, terminal Run finalization, Session creation/archive, real compaction immediately before summary persistence, operator-authorized Specialist scheduling, and continuity/manual Workspace Checkpoint persistence. Ordering, time, input/output, annotation, reentry, and failure-policy limits are enforced by Go and every execution writes a metadata-only audit.

The Desktop **MCP & Plugins** settings view and authenticated HTTP projection show scoped source, health, review state, capabilities, fingerprints, and metadata-only receipts. A control bearer can immediately disable one exact server or plugin generation. Credential values, MCP arguments/results, plugin archives, and publisher public keys are never returned by these surfaces.
