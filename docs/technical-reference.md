# 技术能力参考

[返回首页](../README.md) | [English](technical-reference.en.md)

这里收录从首页移入的运行时、工具与扩展说明，供需要了解实现边界的开发者查阅。普通用户从[首页快速开始](../README.md#快速开始)进入；具体平台可用性和验收结果见 [v1.0.0 发布说明](releases/v1.0.0.md)。

文中的 Profile、Surface、Mission 等名称包含内部诊断与历史兼容概念，不是首次使用时需要选择的一组入口。`cyberagent` CLI、Go module、`CYBERAGENT_*` 环境变量及数据/安装标识继续兼容，详见[品牌迁移](branding/README.md)。

## 定位与运行时

针路簿是一个由 Go 主控的本地 AI Agent 工作台。它把模型路由、长任务恢复、工作区、工具调用、审批、预算、记忆和审计事件统一到一个 Thread/Run 运行时中，并通过 Windows/macOS Desktop、React Thread 工作台、CLI 和 loopback HTTP/OpenAPI 提供同一套核心能力。TUI、headless 与扩展入口按明确的 maintenance/extension 等级维护，而不是另一套产品。

面向用户的规范词汇是：`Thread（任务）` 表示稳定任务与历史身份，`Run` 表示一次有限执行尝试，`Step` 与 `Tool Item` 表示 Run 中的叙事步骤与结构化工具项，`Workspace` 表示操作者选择的源码范围，`Plan item（计划项）` 表示 Plan/Delivery 中的有界条目。`Mission`（不可变意图与 Scope）和 `Session`（Run 独占的上下文与授权边界）只在高级诊断或兼容语境出现。终态 Run 后继续输入会在同一 Thread 内原子创建全新 Run 及其 Session，且不继承旧审批、租约、进程、网络或凭证。模型可以规划和提出动作，但 Go 始终拥有状态机、凭证、权限、持久化与执行边界。仓库文件、网页、模型文字和工具输出都只是不可信证据，不能自行升级为指令或权限。完整词汇映射见[规范词汇表](convergence/vocabulary.md)，运行合同见 [ADR 0132](adr/0132-thread-identity-run-succession.md)。

当前产品重点是**通用 Code Agent 工作流**。CTF/专项网络安全求解已调整为可选附加能力，暂不进入活跃开发计划；仓库只保留通用的 Skill、Tool、Analyzer、Sandbox、Provider 和 Report 扩展接口，供未来独立插件接入。详见[产品范围](PRODUCT_SCOPE.md)与 [pre-1.0 收敛策略](convergence/README.md)。

## 为什么选择针路簿

通用 Agent 的难点不只是“让模型调用工具”，而是让长任务在失败、重启、审批和多人协作条件下仍然可恢复、可解释、可约束。

### 确定性工程与 Agent 协作

- **Go 硬约束：** Run 状态、预算、Scope、Policy、审批、幂等键、租约和审计记录由代码验证。
- **模型动态决策：** 模型负责理解目标、制定计划、选择已公开的工具和生成面向用户的说明。
- **事实分层：** 模型公开进度与 Harness 验证事实分开显示；模型声称“已完成”不会替代工具结果或验证收据。
- **默认最小权限：** 高权限能力需要独立、显式、可撤销的操作者授权，持久配置本身不携带运行 authority。
- **可恢复执行：** SQLite 是状态真源，Run、Session、事件、检查点和操作收据可以跨进程恢复。

### 单一控制平面

```text
CLI / React / Windows + macOS Desktop / loopback API
                    |
              Go control plane
       +------------+-------------+
       |            |             |
   LLM Router   Tool Gateway   Run Supervisor
       |            |             |
       +------ Policy / Approval --+
                    |
      SQLite / Workspace / Rust / Sandbox
```

允许的调用方向始终是 `TypeScript -> Go -> LLM/Rust/Docker`。TypeScript 不是安全边界；Rust 只做确定性分析，不管理 Agent、Session 或密钥。

## 核心能力

| 领域 | 当前能力 |
|---|---|
| Agent 运行时 | 稳定 Thread 身份、有限 Run 尝试、Step/Tool Item、无授权继承的 Run succession，以及高级诊断中的 Mission/Run-local Session 边界 |
| 模型与上下文 | Mock、Anthropic-compatible、OpenAI-compatible 与 loopback-only Ollama Provider、模型路由、资格校验、能力探测、流式响应、上下文压缩、层级项目指令、显式 user/project 长期记忆与 Session 恢复树 |
| 计划与协作 | Plan/Delivery、Plan item（兼容 identity 为 `WorkItem`/`work_item`）、备注、最多两个核心 child、`batch-delivery.v1` 独立 Worktree/分支/邮箱/交付复核/顺序合并，以及 1/2/4/6 档只读 Fan-out |
| 工具与权限 | Tool Gateway、Schema、Policy、Scope、Ask/Auto/Full 操作审批、统一命令与 Job 账本，以及需独立租约的 Debug 终端输入 |
| 代码工作流 | 系统目录选择与 Workspace 导入、工作区浏览、仓库状态、提交历史、Diff 审阅、文件编辑提案、只读 `code-intel-lsp.v1` 语义工具、事务化 Workspace Checkpoint、Run-owned Drydock、稳定 hunk、stash/rebase/cherry-pick/bisect、受管 worktree、GitHub App PR/CI 证据与审批回写、Undo/Redo/Rewind、独立 Fork、验证计划、`standard_code_delivery.v1` 交付真实性、Code Journey 与 Handoff |
| 可观测性 | 追加式 Run 事件、Live Activity、公开模型进度、Harness 事实、Artifact、Finding/Evidence/Report、SARIF、持久化有界计划任务与脱敏结构化诊断包 |
| 扩展 | 模式感知的惰性 Skill 包、生成候选人工审查、两阶段 MCP Client、签名 `plugin.v1`、受限生命周期 Hooks、Provider/Tool 接口、Go/Rust JSON 协议、内嵌 WASI Analyzer、Sandbox 合同与默认关闭的 network-none Docker 产品执行 |
| 客户端 | `cyberagent` CLI、Bubble Tea TUI、认证 HTTP/OpenAPI、React/Vite、Windows/macOS Desktop 便携预览 |

### 模型可调用的工作区工具

Schema v115 引入 `agent-code-tools.v1`，让 root Supervisor 能在真实 Workspace 中完成多轮“搜索 -> 阅读 -> 修改”闭环，同时不把文件系统权限交给模型。可用性由 Go 按 Run、Mission、Workspace、根目录指纹、Surface、Phase、Role、Profile、权限档及各自 revision 生成；模型只能提交符合 JSON Schema 的参数，不能伪造或扩大这份 authority。

| 模式 | 可用工具 |
|---|---|
| Code / Plan / root | `workspace_list`、`workspace_read`、`workspace_glob`、`workspace_grep`；存在当前 Run 的 GitHub 证据时还可用 `github_review_evidence_list/read` |
| Code / Deliver / root（Code 或 Script Profile） | 上述只读工具，加 `workspace_change`、`workspace_apply`、`workspace_delete` |
| Code / Deliver / root（Review 或 Learn Profile） | 仅上述只读工具 |
| Cyber Surface 或 Specialist | 不公开任何 `agent-code-tools.v1` 工具，并在 capability 快照中说明拒绝原因 |

只读结果稳定排序、分页且有界，并拒绝根目录逃逸、大小写别名、未列入 Go allowlist 的隐藏项（仅 `.github` 作为代码证据开放）、忽略项、链接或重解析点、二进制、非 UTF-8 与超限文件。`workspace_change` 只创建 replace/create/move 提案；`workspace_delete` 是独立、需精确确认的删除提案；`workspace_apply` 只能应用已经批准的精确版本，并重新检查原文件与目标文件哈希，避免审阅后内容漂移。每次调用、结果/拒绝、authority 快照、预算消耗与有界 Artifact 都进入可恢复 Supervisor 账本。`cyberagent run show <run-id>`、Run Detail API 和 Desktop Run 页面可查看当前 generation、逐工具可用性与拒绝原因。该协议不授予 Shell、Git、网络或 Sandbox 权限；完整设计见[使用手册](usage.md)和 [ADR 0116](adr/0116-model-callable-workspace-tools.md)。

### 操作审批与工作区隔离

当前可写权限只有 Ask、Auto、Full。Ask/Auto 根据真实操作效果和精确审批决定是否执行；Full 必须显式确认并在当前进程激活。Standard Code 预设选择 Ask；历史权限行只用于证据读取，不能恢复执行。

Windows x64 的 `--enable-workspace-sandbox` 只有通过真实 AppContainer/WFP/Job/ACL readiness 后才能安装 Local adapter。Local 与显式 Docker 共用 `sandboxed_workspace` Command Runtime；后端不可用时不回退宿主。单独安装的 `host_unsandboxed` adapter 要求 danger startup gate，Ask/Auto 仍须精确持久审批，Full 须当前激活。权限 revision 变化会使旧调用和 Job 的原授权失效。见 [Command Runtime adapter split](architecture/command-runtime-adapter-split.md) 与 [ADR 0165](adr/0165-retire-legacy-command-execution.md)。

#130 新增 Go-owned `run_capability_readiness.v1`，把当前已选、现在可切换和后端当前可运行拆成独立事实，并为 Permission、Profile、Interaction、CDP 与 Standard Code 返回稳定阻塞码、修复动作和重启要求。CLI、HTTP、Desktop 与 React 共用该投影；响应不含私有路径且始终 `capability_grant=false`。详见 [ADR 0128](adr/0128-go-owned-run-capability-readiness.md)。

固定 Docker Engine 的 `network=none` 是显式选择的 Standard Code 后端。Go 只投影精确 Drydock 到 `/workspace`，固定非 root 用户、只读根文件系统/工具链和资源上限，不继承凭据。命令不能提供镜像、endpoint、mount 或 Docker flags；daemon/镜像失败不触发拉取或宿主回退。Drydock 是所有权与恢复边界，隔离来自容器。见 [Standard Code Docker](standard-code-docker.md)。

`standard_code_preset.v1` 将 Code/Plan、已就绪 Local 或显式 Docker、controlled、新 Run 的 Ask、restricted CDP 与可信 Drydock 一次幂等提交。既有现代偏好保留，预设不激活 Full。运行中的 Run 须通过原 pause-and-configure 边界静止后再提交，失败不留下部分快照；自动选择不切换到 Docker、宿主或 Full。结果不含权限 bearer。见 [Standard Code 原子预设](standard-code-preset.md)。

Schema v135 与 #137 在同一 `RunSupervisor` 内加入 `standard_code_supervisor.v1` 有界完成协议。Standard Code root 必须先完成两个连续只读轮次，经操作者选择 Plan 并进入 Deliver，再以已审阅 apply 的 after-Checkpoint 建立 mutation epoch；真实 Command Runtime 失败进入 Diagnose，修复后只有当前 epoch 的结构化成功才能进入 Deliver/finish。命令、Job、修复、输出、无进展和重复失败均有固定上限，重启重复副作用、权限/上下文漂移和陈旧 Job cursor 失败关闭，所有决定进入 append-only v135 账本。详见 [Standard Code 编码闭环](standard-code-supervisor.md)与 [ADR 0137](adr/0137-bounded-standard-code-supervisor.md)。

旧 `risk_escalation.v1` 的创建、审阅、授权消费和执行入口已退役。只保留不可变提案、grant、intent/result 及其读取；恢复只能消费精确原调用的已保存结果、拒绝或未知事实，不能重发副作用。新命令使用统一操作审批。见 [历史风险升级证据](risk-escalation.md)。

Schema v137 与 #139 新增 `standard_code_delivery.v1` 交付真实性门。最终 Checkpoint、base/head、真实 committed/index/worktree/untracked/conflict Diff、Command Runtime 终态/exit/tree-reaped、输出摘要与 Artifact、重试、permission/backend generation 和未覆盖项进入同一不可变 receipt；只有当前 Workspace revision 的完整终态成功可为 `passed/verified`。修改、权限或后端漂移会在读取时把旧 receipt 投影为 `stale`，而不改写历史；失败、截断、取消、超时、审批拒绝与无测试均有明确状态。Desktop、CLI/HTTP、Code Handoff、GitHub Review 与最终回复消费同一投影，并链接文件、测试输出及 Checkpoint/Undo/Rewind/Fork。记录不会自动 commit、push、merge、覆盖来源或删除无法证明归属的文件。详见[交付真实性门](standard-code-delivery.md)与 [ADR 0142](adr/0142-standard-code-delivery-truth-gate.md)。

#181 增加独立 packaged Standard Code 安全矩阵 executor：portable ZIP 中逐字节一致的 `TraverseBoard.exe` 经 Tool Gateway 与 Go Application 对固定 40 项矩阵执行 75 个 Local/Docker 组合，并绑定 source/EXE/ZIP/matrix/backend generation、Job/Event/Artifact/Checkpoint、恢复与精确 cleanup 的不可变 hash chain。未执行、后端不可用、证据不全或权限/lease/root/backend 漂移全部失败关闭；Docker 不可用只产生 `approval_required` 事实，不会静默切换 Full Access。固定 recovery worker 会在 harness 自有目录内注入退出、强杀、重启等价、lease 过期及并发 Drydock 修改，再由新进程复核终态 tree-reaped Job 和用户改动保留。该报告只供 #140 owner 聚合，不能自行宣布发布门通过。详见[packaged E2E 与安全矩阵](standard-code-packaged-e2e.md)及 [ADR 0143](adr/0143-standard-code-packaged-security-matrix.md)。

### 模型与工具的 item 级流式事件

Schema v130 与 `llm.item_stream.v1` 将 OpenAI 的交错 tool-call delta、Anthropic content block、Ollama/Mock 的完整 item，以及旧 `ChatChunk` 统一为有序的 response/item/call 生命周期。参数增量只在有界内存中拼接；Provider 只能声明调用已准备，不能签发 authority 或执行工具。Go 在完整 JSON 通过大小、敏感数据、Policy、预算与幂等检查后才记录执行开始/完成，并以稳定 response/item/call ID 对齐临时 UI 卡片与持久工具账本。

公开 `model_public_stream.v3` 和 `model.delta` 只携带稳定 ID、状态、脱敏工具名、参数字节数及无正文边界，不保存参数、raw wire、凭据或私有推理。取消、断流、缺失 usage、模型漂移和畸形/重复终态会稳定失败，且不会补造成功完成事件；旧 Session 历史无需改写即可显示为 durable completed item。设计与失败语义见 [ADR 0133](adr/0133-item-level-model-tool-streaming.md)。

`thread_transcript.v1` 进一步把同一 Thread 内的用户消息、Assistant 公开文本、Harness 事实、结构化工具阶段、审批、验证、检查点、交付和多 Run 边界合为主页面叙事。持久顺序使用 `(Run ordinal, event sequence, item position)`；追加事件和 successor 不会移动旧 keyset cursor。临时模型/工具卡片只在相同稳定身份的 durable event 到达前存在，模型陈述与可验证 Harness 事实始终使用不同标签、图标和无障碍文本。

Thread 页面同屏提供发送、暂停/恢复、审批、继续和交付查看；Events、Artifacts、Run 与 Session 页面继续作为专业审计面。长历史由有界可变高度虚拟列表承担，Composer 是独立 sticky 根布局区域，并覆盖窄屏、200% 等效缩放、中文 IME、虚拟键盘、safe area 和 reduced motion。安全投影不包含工具参数、raw output、provider bytes、凭据或私有推理。设计与恢复合同见 [ADR 0134](adr/0134-unified-thread-transcript.md)。

### Run 级 Web 证据

v1.0.0 的自动策略优先使用当前供应商支持的 Responses 原生搜索；只向已配置的 SearXNG 回退，DDG 必须显式选择。GitHub/HN/RSS 来源连接器已接入同一工具、快照与引用路径，具体支持范围见[搜索与引用](web-evidence.md)。兼容 Responses API 不代表已经提供原生搜索能力。

Schema v134 的 `web_search`、`web_fetch` 与 `web_citation` 把公网资料发现、抓取和引用拆成三条 Go-owned 路径。SearXNG 等普通 Search 结果仍是不可引用的发现 stub；经过精确 Provider/model/credential binding 资格验证的托管搜索会在不可变 Search operation 中保存带指纹的 `provider_grounded_citation.v1`，允许普通回答直接引用来源 URL，同时明确标注“供应商佐证、非本地验证”。Fetch 在独立的网页抓取 authority、HTTPS/443、SSRF、DNS pinning、逐跳重定向复核、超时、大小、MIME 与受控解析器边界内生成不可变快照。Ask、Auto、Full 均执行 robots policy，并保留私网、loopback、云 metadata、DNS rebinding、HTTPS、重定向、大小与超时边界。`web_citation` 仍只绑定同一 Run 中已抓取或 partial 的本地快照。网页正文、搜索片段和 PDF 文本始终是不可信证据，不能授权工具或覆盖系统指令。

Provider-native 托管搜索的 API 出站使用当前模型路由派生的精确、私有 Provider authority，不要求把 Provider API host 写入网页抓取白名单，也不会因此扩大 `web_fetch`。SearXNG 仍需显式配置 `CYBERAGENT_WEB_SEARCH_ENDPOINT` 并处于相应 Run 网络边界内；缺少所选后端时不会暗中切换到浏览器、Shell 或其他收费 Provider。Thread、`cyberagent web-evidence list` 和认证 HTTP API 继续使用 Go-owned 安全投影。配置、故障处理、条款/版权说明见 [Web Evidence](web-evidence.md)，设计见 [ADR 0137](adr/0137-go-owned-web-evidence.md)。

### Run-owned Drydock 工作目录

Schema v127 的 `drydock-workspace.v1` 从精确的 source Workspace、repository/common-dir、branch、base commit、root 指纹和 dirty/index 状态创建产品管理的独立 worktree。首次 create 只返回 Workspace Trust digest；操作者必须把该 digest 原样带入第二次确认，任何来源漂移都会失败关闭。来源未提交内容只进入 Trust 回执，不会被暗中复制。Trust 永远固定 `grants_process_authority=false`；Drydock 本身不提供进程、网络、凭证或宿主文件系统隔离。

每个 Use/Checkpoint/Rewind/Undo/Fork/Deliver/Cleanup 都重新证明 Run、Workspace、root、Git registration、branch/base/current binding 和 ownership generation。Checkpoint 精确保存 tracked、untracked 与 raw index；崩溃、冲突、用户改动、未知目录或身份不确定时一律保留目录并进入人工恢复。Delivery 只生成审阅 patch，固定禁止 merge/push/force/覆盖来源；Cleanup/到期 GC 只以非 force 方式删除完整证明为产品所有且干净的 worktree，并保留分支与全部事件/收据。命令与恢复手册见 [Drydock 工作目录](drydock.md)，设计见 [ADR 0129](adr/0129-run-owned-drydock-workspaces.md)。

### 只读 LSP 语义代码智能

`code-intel-lsp.v1` 只启动操作者显式配置、审查并以 executable SHA-256 固定的本地 Language Server。Go 拥有 initialize、文档同步、请求取消/超时、崩溃重启和进程树清理；模型只在 Code Surface 的 root Plan/Deliver 回合看到 Server 实际协商成功的 workspace/document symbol、definition、references、implementation、hover、signature、diagnostics、call/type hierarchy 工具。Cyber、Specialist、rename、code action 和 format 均不开放。

每份语义结果绑定 Workspace/root、commit/branch/dirty digest、document URI/hash/version、Server generation/capability fingerprint 和 query/page。编辑、切换分支、换根目录或 Server 重启会让旧证据显式变为 stale；越界 URI、远程 link、secret/control 文本和超限结果会被拒绝、清洗或标记 partial。CLI 提供 `code-intel status|qualify`，认证 OpenAPI 和 Desktop 设置页只显示来源、语言、健康、capability、generation、有界错误与模型工具，永不返回 executable、argv、环境或凭证。最小环境和“不授予网络”不是 OS Sandbox；Server 本身必须受信任。配置、真实 gopls/TypeScript 兼容矩阵和残余风险见 [LSP 语义代码智能](code-intelligence.md)。

### 事务化 Workspace Checkpoint

Schema v117 的 `workspace-checkpoint.v1` 在文件工具、Run-owned 命令批次/后台 Job、typed Git 写入与 agent merge 边界前后记录不可变检查点。检查点固定 base commit、branch、原始 Git index、稳定排序的 tracked/untracked manifest、内容哈希、触发收据、attempt/capability generation 与恢复等级；普通文件和 index 以 SHA-256 内容寻址去重，ignored/generated/large/sensitive/link/external 状态会显式标记而不是静默承诺可恢复。Shell 没有可移植 watcher，因此其边界明确降级为 `partial`，只承诺观测到的 Workspace/Git 状态，不宣称回滚根目录外副作用。

Desktop 的 **工作区检查点 / Checkpoints**、CLI 的 `cyberagent workspace checkpoint ...` 和认证 OpenAPI 共用同一个 Application 服务。Rewind、Undo、Redo 先做 live/current/target 三方预览，再以精确 cursor CAS 和当前权限确认写入；恢复本身是一次新的追加式写操作，不改写旧历史、不调用 `git reset --hard`、不批量清理 untracked 文件。Fork 从历史检查点建立独立 Git worktree、Workspace、Mission/Run/Session，且不继承审批、凭据、capability、lease、进程或网络授权；HTTP/Desktop 不接受或返回绝对 worktree 路径，由 Go 从受信源 Workspace 确定性生成同级目标。完整操作说明见 [Workspace Checkpoints](workspace-checkpoints.md)，设计与失败语义见 [ADR 0118](adr/0118-transactional-workspace-checkpoints.md)。

### 审批式高级 Git

Schema v123 的 `git-advanced.v1` 以默认关闭的 Go-owned 控制面提供稳定内容指纹 hunk stage/unstage/revert、明确 base/index/worktree/untracked 角色的 stash、可持续恢复的 rebase/cherry-pick/bisect 状态机，以及只能位于产品管理根内的 worktree create/lock/unlock/remove/prune。每次写入都固定 repository/common-dir、HEAD/branch、index/worktree/stash/sequencer/upstream、permission、capability generation 与 Workspace lease，先展示完整 preview，再创建一次性 Approval 与 Checkpoint；执行前全部重算，任一漂移都会失败关闭。终态收据、冲突 base/ours/theirs、序列 generation 和 worktree 注册表均持久且不可变。

高级 Git 不接受 raw argv、Shell、host path、任意 ref/pathspec 或 bisect 命令；Git 运行时关闭 hook、credential helper、外部 diff、filter/merge driver 和交互入口。保护分支、配置 upstream 的共享 rebase、detached 历史改写、force push、reset-hard、clean-force、脏/外部 worktree 删除都不能表达。重启只观察并终结 `running` 操作，不重放；已精确创建但尚未登记的 worktree 可在全量身份匹配后保守纳入注册表，但原操作仍标记 `interrupted`。Desktop、`cyberagent git-advanced` 与认证 OpenAPI 共用同一 Application 服务；完整命令、限制和恢复流程见[高级 Git 工作流](git-advanced.md)，设计见 [ADR 0122](adr/0122-go-owned-advanced-git-lifecycle.md)。

### GitHub App 审阅集成

Schema v124 的 `github-review-provider.v1` 把 GitHub PR 元数据、完整有界 changed-file 分页、reviews/threads/comments、checks/jobs、失败日志摘录和 Artifact 元数据保存为不可变、已清洗的不可信快照，再与本地精确 merge-base/HEAD、完整 diff、稳定 hunk、文件哈希、冲突及可选 LSP 证据组成 `verified/partial/stale/unavailable/not_run` 证据图。Code/root 模型只获得绑定当前 Run 的本地只读 `github_review_evidence_list/read`；它不能自行联网或回写。

产品优先使用开启 Device Flow 的 GitHub App，device code 仅驻留内存，access/refresh token 只进入操作系统凭据库。连接默认只读；Reply、Resolve/Unresolve、Submit Review 与 Request Reviewer 还需显式连接级写回开关、精确预览和一次性 Approval，执行前重查 Code/Deliver、网络权限、App installation/SSO、能力 generation 与 PR/base/head/merge-base。OAuth/PAT 在 v1 只作为无法扩大到写回的只读迁移路径；重启恢复只观察幂等 marker，绝不猜测重放。API/Desktop 必须显式启用 `--enable-github-review`。配置、最小权限、CLI/OpenAPI、真实联调和 GitHub Developer Program 申请门见 [GitHub Review Provider](github-review.md)，设计见 [ADR 0123](adr/0123-go-owned-github-review-provider.md)。

### 可交付 child 与隔离合并

Schema v118 的 `batch-delivery.v1` 将一个已经审批并 admission 的核心 child DAG 物化为最多两个独立 Git worktree/branch。每个 child 只获得绑定 Agent、generation、过期时间与一次性 owner token 的关闭工具集：owned Scope 内的 list/read/glob/grep、人工提案式 change/apply，以及固定 status/diff/commit；delete/rename、Shell、任意 process、network、credential、Debug、审批和继续派生 child 始终为 false。旧 Specialist 运行时仍保持 no-tool；它不会因为存在此协议而隐式扩权。

child 只有在 worktree clean、HEAD 是 base 的后代、全部 changed path 属于声明 Scope、固定验证通过且交付包含 base/head、完整 diff/call-chain 摘要、测试收据、evidence 与已知限制时，才能进入 `ready_for_review`。Submit 与 Reviewer 都会在验证结束后重新证明 exact branch/HEAD/diff/clean 状态；Reviewer 还必须独立确认完整 merge-base diff、调用链与验证，Desktop 不把作者摘要当证据。顺序 merge queue 在独立 integration worktree 上从最新已确认 base 逐项应用，每步重跑截至当前步骤的全部累积验证并重新证明 source、integration 与所有 child receipt；重叠、base drift、状态漂移、文本/语义冲突或测试失败都会阻止队列，不会自动选一方覆盖，也不会 push、开 PR 或修改远端。

默认验证只执行不会运行仓库代码的 `git diff --check`。`go_test`/`npm_test` 会执行 child 提交的代码，因此只有操作者启用相应控制能力，且当前 Run 仍为 `running` 并持有 现代 Full 且当前进程激活有效时才可在宿主运行；Desktop 还需显式 `--enable-batch-delivery-control`，宿主校验另需 permission control、danger-full-access 与 `--enable-batch-validation-execution`。验证进程使用 Windows Job Object / Unix process-group 生命周期边界，Go 测试禁用缓存，持久层只记录完整输出流摘要；其离线/去凭证环境仍只是降险措施，不是 OS 网络或文件系统沙箱，POSIX 主动脱离 inherited process group 也仍是显式宿主权限的残余风险。完整操作与恢复说明见[可交付多代理](batch-delivery.md)，设计决策见 [ADR 0119](adr/0119-deliverable-batch-agents.md)。

### MCP Client、Plugin 与受限 Hooks

Schema v120-v121 增加 Go-owned MCP Client 和惰性 `plugin.v1` 包。MCP descriptor 先审查是否允许 discovery，再对真实协商得到的 tools/resources/prompts capability fingerprint 单独审查；精确 `Code/Deliver/root` 的 Ask/Auto 调用须通过逐操作审批，Full 调用须有当前进程激活；模式切换不能绕过 pending/denied 审批，每次调用都会重新发现并在漂移时隔离。远程 HTTPS bearer 仅按引用从系统凭证存储注入，stdio/HTTP 返回值始终作为不可信证据清洗；模型和普通 UI 永远看不到明文凭证，专用 MCP 审计只保存摘要。Supervisor 恢复账本只持久化经过 schema 校验、脱敏和大小限制的规范化调用/结果，不保存 bearer 或 transport 原始字节。

Plugin ZIP 只允许声明式 Skills、MCP descriptors、UI metadata 和 Hooks；严格文件白名单、摘要、大小、格式与可选 Ed25519 签名在 staging 时验证，默认禁用并逐能力人工启用。外部包可由固定 SHA-256 的无 redirect HTTPS 或固定 commit 的 bare Git 导入；升级/回滚原子切换唯一 enabled 版本，publisher revoke 不能由 `confirm-untrusted` 绕过。Hook 已接到 Tool、Run、Session、Compaction、Specialist 和 Checkpoint 的真实 Go 事务边界，只能拒绝、注释、记录或在 `pre_tool` 删除顶层字段。Desktop 设置页可按当前 Run/Workspace 查看健康、来源、审查和 metadata-only 调用审计，并用精确 fingerprint/generation 立即禁用。完整命令、状态机与残余宿主风险见 [MCP Client、Plugin 与受限 Hooks](extensions.md)。
### 源码绑定的真实浏览器 UI 证据

Schema v119 的 `ui-evidence.v1` 把真实页面验证绑定到 commit/dirty digest/index/worktree manifest、精确 build/start recipe、固定浏览器 version/可执行文件 SHA-256、literal loopback URL/route、viewport/DPR、locale/theme/reduced motion、deterministic fixture/seed/page state、步骤与 capture policy。Application 在 build 前、readiness 后、浏览器断言后以及 owned process cleanup 完成后重新核对源码；拒绝已占用端口，不收养既有服务或个人 Browser Profile。Windows Desktop 的执行入口默认关闭，只有 Run execution、现代 Full 与当前进程激活、danger-full-access、restricted CDP 与 `--enable-ui-evidence` 同时成立才开放。

Desktop、认证 OpenAPI 与只读/导出 CLI 共用同一份不可变 Attempt、step 和 artifact 语义。PNG、DOM、accessibility、console/page error、network/HTTP 与 performance 产物都保存 SHA-256/MIME/尺寸/viewport/source step/commit/Run/Attempt/redaction/retention policy，且 PNG 尺寸必须匹配 `viewport × DPR`；页面和产物始终不可信、不授权。`not_run` 明确保持中性，只有 exact `passed` 才算通过。Windows CI 用 creation-time Job Object、临时 Profile 和 deterministic loopback fixture 跑真实 Edge 的 desktop/mobile、theme/locale/reduced-motion 矩阵，并证明缺失 click handler 的回归只能被真实页面交互断言捕获。详见 [UI Evidence 操作手册](ui-evidence.md)与 [ADR 0120](adr/0120-source-bound-real-browser-ui-evidence.md)。

### 真实 Git、PowerShell 与 Bash

针路簿调用真实的 Git 和操作系统 Shell，不是命令模拟器；但它也不会给模型一个永久、无审阅的裸终端。当前 Code 工作流按风险拆成以下入口：

| 入口 | 实际执行 | 权限与限制 |
|---|---|---|
| 类型化 Git | 真实 `git` 进程；覆盖本地整文件与稳定 hunk 操作、stash、rebase/cherry-pick/bisect、受管 worktree，以及独立授权的 fetch、fast-forward pull、push branch、创建/更新 PR | 参数由 Go 合成，固定仓库/权限/lease 状态并先审批与 Checkpoint；禁用 hook/外部 diff/凭证/自定义 driver，raw argv、force push 与共享历史改写不开放 |
| Run-owned 命令运行时 | 同一 `command-runtime.v2` Job/cursor/Artifact 协议，运行于 ready Local、固定 Docker 或显式安装的宿主 adapter | Code/Deliver/root + 当前 Ask/Auto/Full；sandbox 须独立 readiness，host Ask/Auto 须精确审批，Full 须当前激活。adapter/generation、当前 lease、输入和 Policy 每次复核 |
| Debug 持久终端 | Windows PowerShell/ConPTY/Job 或 macOS Bash/PTY/进程组 | Debug 是 interaction；Agent 输入要求现代 Full、当前激活、用户启动的终端和可撤销的 15 秒至 15 分钟独立租约 |
| 操作者固定命令 | 显式确认的 CLI `host-execute` | 进入同一 Command Runtime/Job 账本；不保留另一套 Once/Host 执行器 |

`command_runtime` 与用户持久终端不共享 session 或所有权。Schema v116 先以当前 Supervisor generation lease 写入不可变启动意图；schema v131 再把 exact adapter kind/backend/identity/generation/isolation/network/credential policy 固定到广告、调用、Job 与回执。独立、可过期的进程所有者心跳维持后台 Job，另一进程不能凭数据库记录收养它；旧 v116 行只读投影为不可执行的 `legacy_unbound`。宿主、Windows Local 与固定 Docker adapter 都复用同一套有界 stdin 生命周期；Local 把 manager-owned pipe 复制到 AppContainer 的唯一 stdin handle，Docker 则以 runner v2 + schema v132 生命周期 WAL 把进程内输入绑定到精确 owned container。输入字节不持久化，重启也不会收养旧管道。崩溃时 backend 必须回收 owned 进程树，重启只把所有者已过期的记录收敛为 `interrupted`，绝不按持久 PID 重新执行。stdout/stderr 以单调 cursor 保留通道与时间，内联窗口溢出后仍可生成有 SHA-256 的有界 Artifact；所有返回模型的内容统一去除 ANSI/C1/Unicode 控制序列、修复 UTF-8 并脱敏。

`debug_terminal` 每次命令写入仍经过 Shell Policy；需要另行逐条审批的命令不会借 Debug 租约绕过审批。固定的 `interrupt` 动作只发送 Ctrl+C，沿用相同租约、宿主 Policy、审计、操作去重和原生授权。授权瞬间会固定输出水位，模型不能读取租约授予前的终端滚动内容。为支持 Run 恢复，模型提交的规范化调用和水位之后脱敏、有界的结果会进入 Supervisor 工具记录；schema v113 让该工具进入同一持久调用账本并保留既有记录。进程内 Workspace 根目录摘要和 mode revision 会阻止目录或阶段漂移后旧租约复活；用户键盘输入、原始 PTY 字节、根目录路径和租约 bearer 均不持久化。应用重启会终止会话并使租约失效。Cyber Surface 不公开这些宿主 Shell 路径。完整边界见[使用手册](usage.md)、[ADR 0114](adr/0114-real-shell-transports-and-supervised-debug-terminal.md)和 [ADR 0117](adr/0117-run-owned-command-runtime.md)。

### 安全边界

- 不公开 Provider 私有 thinking、原始 Prompt、raw delta、工具参数、工具原始输出或 API key。
- 项目指令、长期记忆和对话 Checkpoint 始终是不可信、非授权上下文；Workspace Checkpoint 只保存有界文件/index 状态。两类 Fork/Resume 都不恢复审批、capability、凭据、网络、进程、终端租约或执行档位。详见[双语上下文/威胁模型与删除说明](context-continuity.md)、[Workspace Checkpoints](workspace-checkpoints.md)、[ADR 0115](adr/0115-non-authorizing-durable-context-continuity.md)和 [ADR 0118](adr/0118-transactional-workspace-checkpoints.md)。
- 文件编辑、宿主命令、浏览器方法、终端输入和 Sandbox 分别重验实际操作授权；Full CDP 是现代 Full 下的独立子开关，仍要求当前激活与专属 capability。
- 可交付 child 的 owner token 只在创建或 generation 轮换响应中返回一次，SQLite 和普通 HTTP/Desktop 投影仅保留摘要；丢失后必须 CAS 轮换 generation，旧 token 立即失效。
- `host_unsandboxed` 对显式 `network=host` 命令意图逐操作检查授权与审批，并按运行时规则接入代理；`credentials=none` 表示不注入凭据，不等于宿主凭据不可访问。该 adapter 不是 OS 网络沙箱。`sandboxed_workspace` 只有在 Local/Docker 独立隔离 readiness 成立时才报告 `network=denied` 与 `credentials=none`，且只运行于 Drydock。
- 新命令与精确审批使用统一 Command Runtime；Debug interaction 的终端输入另需短期租约。模型、Skill 与仓库文档不能开启宿主执行或终端权限。
- Docker Sandbox 产品入口默认关闭。显式进程 capability、当前 `docker` Profile、匹配权限档、精确 per-call 审批、Policy、预算与 30 秒 readiness 必须同时成立；数据库记录不能在重启后恢复 start authority。
- 当前产品执行只接受 environment-free、secret-free 的 `network=disabled` Manifest，并在 Docker create/inspect 两侧固定 `network none`。allowlist/scoped egress 仍缺少 Go-owned host/port/protocol guard，因此一律以 `managed_egress_unavailable` 失败关闭；Docker 不可用时没有宿主 fallback。
- Windows Desktop 只在显式 `--enable-ui-evidence` 及其 Run execution/danger-full-access/restricted-CDP 前置条件同时成立时开放 loopback-only 真实浏览器证据；macOS 与普通 CLI 保持只读。Full CDP 是现代 Full 下的可选子权限：进入 Full 默认开启，可随时关闭，重新开启需要当前进程激活和显式高风险确认，离开 Full 时强制关闭。它只面向 Traverse 管理的隔离内置浏览器，不是 Wails WebView 或系统 Chrome；切换开关本身不启动浏览器。Windows Wails Desktop 已提供 Run-scoped Open/Status/Close 生产接口：后端发现可信浏览器、创建独立 Profile、持有 Job/CDP 生命周期，并在显式关闭、TTL、进程退出、撤权、Run 终态或 Desktop 退出时收敛清理。普通 CLI 与 Supervisor 仍不获得这一进程 authority。
- Windows/macOS Desktop 当前都是未签名的开发者/操作者便携预览，不是正式安装包；macOS 产物只有 ad-hoc 签名且未公证。

### Docker Sandbox 产品入口（默认关闭）

Schema v99 把 v97 的可恢复生命周期与 v98 的有界 I/O 组合到同一个 Go
`DockerSandboxService`。CLI、HTTP/OpenAPI、Desktop 和模型提案都复用该服务；模型工具
`sandbox_docker_run_propose` 只能请求准入，不能启动容器，也不能提交 Docker flags、
daemon endpoint、宿主 bind、环境变量或网络放宽。

```powershell
# 未带 capability 时只返回稳定的 disabled readiness，不接触 Docker 写接口。
cyberagent run sandbox docker-readiness <plan-id> --manifest-file <manifest.json>

# 真正准入/启动必须在同一进程显式开启 Docker 与权限 capability。
cyberagent run sandbox docker-admit <plan-id> --manifest-file <manifest.json> `
  --operation-key <stable-key> --enable-docker-execution --enable-permission-control
```

完整 CLI、HTTP、取消/恢复、reason/remediation 与预算说明见
[使用手册](usage.md)、
[HTTP API](http-api.md) 和
[ADR 0099](adr/0099-docker-sandbox-product-admission-and-recovery.md)。普通 Code 工作流
仍不依赖 Docker。

### 模式感知 Skill 与生成候选

当前 13 个内置 Skill 使用 `profiles × surfaces × phases × roles` 四维兼容矩阵，并把
`user_invocable`、`model_invocable` 与 `explicit_only` 作为独立调用策略。schema v111
让外部 Skill 安装账本也完整保存这些字段；legacy 包保持原指纹和“仅操作者显式调用”
策略。安装始终是惰性的，不等于选择、正文注入或能力授权。

`run-skill-generator` 只适用于 Code/Deliver/root，并且必须由操作者显式选择。模型的
`skill_candidate_propose` 只能创建绑定真实工具调用和内容指纹的不可信候选；schema
v112 以只追加记录推导 `proposed -> approved -> imported` 或 `proposed -> rejected`，
模型、Agent 和 Supervisor 身份不能充当人工 reviewer。批准与导入是两步独立操作，
导入还需再次确认不可信指令；导入后仍需原有独立流程才能选择。

```powershell
cyberagent skill candidates --run <run-id>
cyberagent skill candidate show <candidate-id> --show-content
cyberagent skill candidate approve <candidate-id> `
  --candidate-fingerprint <sha256> --operation-key <stable-review-key>
cyberagent skill candidate import <candidate-id> `
  --candidate-fingerprint <sha256> --operation-key <stable-import-key> `
  --confirm-untrusted-skill
```

完整模式矩阵、候选限制和失败恢复语义见[使用手册](usage.md)与
[ADR 0113](adr/0113-mode-aware-external-skill-ledger-and-generated-candidate-review.md)。

### 持久化有界监控与结构化诊断

Schema v122 的 `scheduled-job.v1` 可为一个显式 Run 建立单次或固定间隔监控，并持久化
timezone、next wake、deadline、停止条件、轮次/模型/耗时预算、重试退避、通知和 owner。
进程内 Worker 固定并发度 1，只能通过启动参数开启；租约 generation 与私有 fence 防止
重启、并发 worker 或迟到完成造成重复执行。默认 `read_only` 且模型预算为零，目标状态
没有变化时只记录 `unchanged`，不会调用模型或工具。

`doctor-snapshot.v1`、`debug-query.v1` 和 `diagnostic-bundle.v1` 提供 provider/model harness、
Run/Workspace/权限/网络/工具 readiness 与有界单调事件时间线。事件 payload、prompt、终端
输入、命令输入和 secret 始终 withheld/redacted。CLI、认证 HTTP/OpenAPI、React 与 Desktop
复用同一 Application 服务；Desktop 支持创建、暂停、恢复、取消、查看下次唤醒/最近结果/
通知及导出诊断包。完整说明见[计划任务与结构化诊断](scheduled-jobs-diagnostics.md)和
[ADR 0121](adr/0121-durable-scheduled-monitoring-and-structured-diagnostics.md)。

## 可选附加能力

CTF、自动化渗透、漏洞利用、横向移动和专项攻防工具链**不属于当前核心开发范围**。现有 `ctf` CLI 仅为早期兼容骨架，不代表已经具备自动解题或真实攻击能力。

未来如重新启动该方向，应以独立插件或 Profile 接入现有通用接口：

- `llm.Provider` 与模型 Harness；
- `tools.Tool`、Skill 包和 Policy/Scope；
- Go/Rust Analyzer JSON 协议；
- `sandbox.Runner` 与独立网络隔离证据；
- Finding/Evidence/Report 与 SARIF 导出。

任何附加包都不能绕过 Go 控制平面，也不能把“面向 CTF”解释为默认开放公网扫描、凭证读取或破坏性命令。详见[产品范围](PRODUCT_SCOPE.md)。
