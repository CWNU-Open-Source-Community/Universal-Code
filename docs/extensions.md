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

网页支持安装版本化 Skill ZIP，并上传原生 `SKILL.md` 目录；原生目录暂存后，
在扩展页审查与启用。
Plugin 来源导入支持 URL/Git，由对应后端 capability 控制；高级设置提供旧 Skill Catalog
发布者信任管理。历史 Skill 登记列表保留旧安装和移除操作。具体步骤见
[使用指南](usage.md)。

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
| `post_tool_failure` | 工具调用失败后额外触发（与 post_tool 互补）；deny 不可用 |
| `run_started` | Run 从 created 进入首次运行 |
| `run_completed` | completed/failed/cancelled 或 Supervisor finalize 写入前 |
| `session_opened` | 新 Session 与 Run 原子创建前 |
| `session_closed` | HTTP/Desktop Session archive 写入前 |
| `user_prompt_submit` | 操作者 prompt 进入提交面（Thread turn 端点——含经其路由的定时任务、PR 跟进与 Plan 修正，CLI `thread send`，Session 聊天，Session 消息提交）之后、任何持久化或排队之前；deny 干净拒绝提交。经 peer 投递直接入队的消息在其原始提交面触发 |
| `approval_requested` | Supervisor turn settle 进审批门时触发（durable 审批门场景下 run 落为 waiting_approval；`AgentRunner.Execute` 重入 waiting run 亦触发）；仅记录型 hook，审批决定权永远在操作者 |
| `compaction` | `MaybeCompact` 确认真正需要压缩后、摘要写入前 |
| `subagent` | 经操作者授权的 Specialist schedule 获取执行租约前 |
| `checkpoint` | 连续性或手工 Workspace Checkpoint 写入前 |

同一事件按 event、Plugin ID、Hook ID 稳定排序。每条声明有最大 2 秒超时，输入 256 KiB、输出 16 KiB、注释 16 条的上限；`failure_policy=deny` 失败关闭，`continue` 只记录失败并继续。每个结果写 metadata-only audit，不保存 Hook 输入、注释正文或工具 payload。

工具事件的过滤器二选一：`tool_names`（精确列表）或 `tool_pattern`（一条 Go 正则，非锚定匹配工具名，长度上限 256 字节；Go regexp 为线性时间引擎，不存在灾难性回溯）。两条路径的 deny/annotate/narrow 语义完全一致，声明改变 pattern 会推进注册表 generation。

声明可带 `async: true` 成为后台观测：只有 record 与 script 动作可以异步，且必须使用 `continue` 失败策略——deny/annotate/narrow 必须同步，fail-closed 策略在后台无从执行（声明会被拒绝，绝不静默降级为 fail-open）。后台 hook 在引擎 8 并发上限内独立运行，边界永不等待；脚本收到的 stdin payload 与同步路径相同，但其结果——包括自行输出的 denied——整体丢弃，审计只记录运行结果（completed/failed_continue，恒为非拒绝）。过载时在 64 槽位的并发缓冲池内以 `failed_continue` 审计行跳过本次触发（写在独立上下文上）；若审计池亦达到饱和（如极端并发叠加审计写入阻塞），为保障调用方延迟与避免无界并发，超出上限的审计将被丢弃并记录于引擎 `DroppedAudits` 计数器中，不再保证病态过载下每次触发均有记录。超时照常击杀子进程。进程退出时未完成的后台 hook 会丢失（记录在案，不做持久化）。

`user_prompt_submit` 的 payload 由 Go 编码：Thread 面携带 `thread_id`，Session 面携带 `session_id` 与 `run_id`，加上 `content`（超长 prompt 在 UTF-8 边界截断并带 `truncated` 标记；非法 UTF-8 字节一次性剔除），不支持工具过滤。deny 在消息意图预留与排队发生之前拒绝提交；annotate 只进入结果与审计，不注入模型上下文。已知盲区：image-only 提交（`content` 为空）不触发边界，MCP-only 提交以合成文案触发、真实 MCP 参数不在 payload 中；同一 operation key 的每次重试都会再次触发（attempt-scoped）。

## Operator 脚本 Hook

脚本 Hook（`action: "script"`）是对操作者开放的有界例外：一个哈希固定的外部程序在事件边界运行，只能通过一个有界 JSON 输出 **deny 或 annotate**。它不能修改工具参数、授予权限、访问网络凭据或递归触发 Hook；仓库内容、项目指令、Skill、Plugin 和工具输出都不能安装或放宽脚本 Hook——脚本 Hook 只来自操作者自己的主目录。

- **位置**：`<CYBERAGENT_HOME>/hooks.d/*.json` 声明 + 同目录树内的脚本文件。目录缺失等于未配置；声明与插件 Hook 共享引擎 64 条总上限（合并超限会以可诊断错误整体失败关闭），声明单文件 64 KiB，脚本单文件 1 MiB。任何异常（symlink/junction、超大、非法 JSON、路径逃逸、内容哈希漂移）都失败关闭而不是跳过。
- **声明**：`script` 动作必须带 `script` 规格：`interpreter`（`powershell`（仅 Windows）/`pwsh`/`sh`（仅 POSIX）/`python`/`node`）、相对 `path`（不得绝对、不得 `..`）、`args`（最多 8 个、限定安全字符集）、`sha256`（脚本内容 SHA-256）。脚本 Hook 超时上限 10 秒（声明式 Hook 仍为 2 秒），不允许 `post_tool`。
- **执行**：宿主每次执行前经 `os.Root` 重新验证脚本是普通文件并复算哈希，漂移立即失败；子进程以固定的解释器参数向量启动（无 shell），工作目录为 hooks.d，环境仅含 OS 运行必需变量（Windows：`SystemRoot` 等；POSIX：`PATH`/`TMPDIR`），不继承父进程环境的任何其他变量，凭据与 Provider 配置对脚本不可见。stdin 收到有界 JSON（`event`/`run_id`/`workspace_id`/`tool_name`/`payload`）；stdout 必须是单个有界 JSON（`protocol_version` + `denied`/`reason`/`annotations`），16 KiB 上限、拒绝未知字段和尾随 JSON；stderr 只以有界形式进入失败错误文本。已知的残留边界：校验与解释器打开文件之间存在固有 TOCTOU 窗口（写 hooks.d 即操作者级权限）；超时只终止直接子进程，脚本自行遗留的后台子进程会令本次执行失败关闭；固定 digest 只覆盖声明脚本本身，不覆盖其 import 的模块——模块解析限定在 hooks.d 目录内，helper 是操作者信任文件而非 digest 固定文件，改动 helper 不会触发哈希漂移。
- **审计**：脚本拒绝与声明式 deny 一样写 `plugin_hook_audits`（`plugin_id=operator`，fingerprint 为脚本哈希；schema v223 扩展了审计表的 action 约束与绑定触发器）；`denied` 输出映射为拒绝结果，但模型侧错误文本仍是固定的官方文案，脚本提供的 `reason` 不进入模型可见错误。插件路径三重强制：manifest 校验拒绝 script 声明，加载时过滤历史 staging 残留，引擎注册表拒绝非 `operator` 来源的 script hook。

设计依据见 [ADR 0197](adr/0197-operator-script-hooks.md)。

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

Actual hook boundaries are Tool pre/post (and additional `post_tool_failure` on errors), initial Run start, terminal Run finalization, Session creation/archive, the durable approval gate (`approval_requested`, record-only, on Supervisor settlement and Execute re-entry), operator prompt submission at its four surfaces (the Thread turn endpoint, CLI `thread send`, Session chat, and Session message submission) before any persistence or queueing, real compaction immediately before summary persistence, operator-authorized Specialist scheduling, and continuity/manual Workspace Checkpoint persistence. Tool events filter by either the exact `tool_names` list or a single `tool_pattern` Go regular expression (unanchored against the tool name, at most 256 bytes; Go regexp is a linear-time engine without catastrophic backtracking). Ordering, time, input/output, annotation, reentry, and failure-policy limits are enforced by Go and every execution writes a metadata-only audit.

A declaration with `async: true` becomes a background tap: only record and script actions may run asynchronously, and both require the `continue` failure policy — deny, annotate, and narrow must stay synchronous because a background hook can never block the boundary, and a fail-closed policy has nothing to close (declaring one is rejected, never silently degraded to fail-open). Background hooks run inside an engine-wide ceiling of eight concurrent executions; the boundary never waits, background scripts receive the same stdin payload as synchronous ones, but their entire result — including a script's own `denied` output — is discarded, and the audit ledger only records the run outcome (`completed`/`failed_continue`, always non-rejecting). An engine at its ceiling skips the fire with a `failed_continue` audit row dispatched through a bounded 64-slot audit pool on a detached context; under extreme overload where the audit pool is also saturated (e.g. flood fires paired with a stalled audit sink), excess audit writes are dropped and observable via the engine's `DroppedAudits` counter rather than guaranteeing persistence under pathological overload. Timeouts still kill the child. Unfinished background hooks are lost at process exit; this is documented, not persisted.

The `user_prompt_submit` payload is Go-encoded: Thread surfaces carry `thread_id`, Session surfaces carry `session_id` and `run_id`, plus `content` (oversized prompts clip on a UTF-8 boundary with a `truncated` flag; invalid UTF-8 bytes are dropped once); tool filters are not allowed. A deny rejects the submission before the message intent or steering queue entry exists; annotations stay in the result and audit and are never injected into model context. Known blind spots: image-only submissions (empty content) skip the boundary, MCP-only submissions fire with synthesized text while the real MCP arguments stay outside the payload, and every attempt of the same operation key re-fires the boundary (attempt-scoped).

## Operator script hooks

Script hooks (`action: "script"`) are the one bounded exception opened to the
operator: a hash-pinned external program runs at the declared event boundary and
can only **deny or annotate** through a single bounded JSON output. It cannot
modify tool arguments, grant authority, reach network credentials, or recurse,
and repository content, project instructions, skills, plugins, and tool output
can never install or widen script hooks — they come only from the operator's
own home directory.

- **Location**: `<CYBERAGENT_HOME>/hooks.d/*.json` declarations plus script files
  inside the same tree. A missing directory means none are configured;
  declarations share the engine's 64-entry ceiling with enabled plugin hooks
  (an over-cap merge fails closed with a diagnosable error), each declaration
  is at most 64 KiB and each script file at most 1 MiB. Every anomaly —
  symlink/junction, oversized or malformed declarations, path escapes,
  content-digest drift — fails closed instead of being skipped.
- **Declarations**: a `script` action requires a `script` spec: `interpreter`
  (`powershell` (Windows only) / `pwsh` / `sh` (POSIX only) / `python` /
  `node`), a relative `path` (never absolute, never `..`), up to 8 arguments in
  a fixed safe charset, and the script content `sha256`. Script hooks allow a
  10-second timeout ceiling (declarative hooks remain at 2 seconds) and are
  rejected on `post_tool`.
- **Execution**: before every run the host re-verifies through `os.Root` that
  the script is still a regular file and re-hashes it; drift fails closed. The
  child process starts from a fixed interpreter argument vector (no shell), with
  the hooks directory as cwd and an explicit minimal environment of
  OS-operational variables only (Windows: `SystemRoot` and friends; POSIX:
  `PATH`/`TMPDIR`); no other parent-environment value is inherited, and
  credentials and provider configuration stay invisible to scripts. Stdin
  receives bounded JSON (`event`/`run_id`/`workspace_id`/`tool_name`/
  `payload`); stdout must be one bounded JSON value (`protocol_version` plus
  `denied`/`reason`/`annotations`) within 16 KiB, rejecting unknown fields and
  trailing JSON; stderr reaches only the bounded failure error text. Known
  residual bounds: a TOCTOU window remains between verification and the
  interpreter's own open (writing hooks.d is already operator-level power), and
  the timeout kills the direct child only — background children left by a
  script fail the execution closed. The pinned digest covers only the declared
  script file, not the modules it imports: module resolution is confined to the
  hooks.d directory, but helpers are trusted operator files, not digest-pinned
  ones — editing a helper does not trigger digest drift.
- **Audit**: script denials record like declared denies in
  `plugin_hook_audits` (`plugin_id=operator`, fingerprint is the script hash;
  schema v223 widens the audit action constraint and its binding trigger);
  the model-facing denial text stays the fixed official wording, and the
  script-supplied `reason` never reaches model-visible errors. The plugin path
  is enforced three times: manifest validation rejects script declarations,
  loading filters any pre-existing staged manifest, and the engine registry
  rejects script hooks from any source other than `operator`.

See [ADR 0197](adr/0197-operator-script-hooks.md) for the design rationale.

The Desktop **MCP & Plugins** settings view and authenticated HTTP projection show scoped source, health, review state, capabilities, fingerprints, and metadata-only receipts. A control bearer can immediately disable one exact server or plugin generation. Credential values, MCP arguments/results, plugin archives, and publisher public keys are never returned by these surfaces.
