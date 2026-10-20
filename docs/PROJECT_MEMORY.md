# Prayu Project Memory

> 2026-10-20 macOS browser CI follow-up: native test startup budgets alone do
> not cover the production tool dispatcher. macOS `browser_navigate` now has
> a three-minute execution budget for first-launch Chrome signature verification;
> shorter parent deadlines, cancellation and existing authority checks still
> apply. Other browser tools and platforms retain their thirty-second budgets.
> The native UI journey allows time for subsequent actions and reports failed
> browser state immediately. Vite is pinned to 8.3.4 for the current audit fix.
> Linux tests cannot certify the native macOS path; both Mac CI lanes must pass.

> 2026-10-11 sandbox integration: Local remains the default; users can explicitly
> select their own Docker Engine or Windows sbx 0.47.0. The SBX production path
> now binds a zero-tool helper, owns its fixed namespace, checks guest isolation
> and deletes the exact VM through the local daemon's UUID condition. Schema
> v189 repairs SBX Job admission without rewriting historical permissions.
> Real success/failure/cancellation, checkpoint and replay passed; all owned VMs
> were removed. See [acceptance](acceptance/2026-10-10-sandbox-selection.md) and
> [ADR 0172](adr/0172-sbx-owned-namespace-execution.md) for the Windows version
> contract and separate remaining frontend/platform acceptance work.

> 2026-10-10 neutral glass follow-up: after #294 merged, the user requested removal
> of purple, yellow/orange and other independent decorative palettes. Shared
> light/dark/glass tokens now use neutral high-opacity surfaces, white filled
> primary controls and dark foregrounds. V2 and legacy components share these
> roles; danger, code diffs and terminal ANSI output retain their content meaning.
> See [palette inventory and conventions](branding/neutral-glass-palette.md).

> 2026-10-10 product guidance revision: the frontend rebuild includes user-facing
> language and information hierarchy. The connection hub groups task preparation,
> tools/collaboration and runtime settings; MCP/Plugin/LSP show actual setup stages.
> Task, approval, recovery and delivery copy explains the current state and next
> available action. Technical records expand on demand, while decision effects
> stay visible. See [product language](PRODUCT_LANGUAGE.md) and the
> [integrated verification](FRONTEND_REBUILD_STATUS.md): 186 frontend files / 1,834
> tests, production build and an isolated real-browser pass. Remaining P4/P5 work
> continues under the same rebuild roadmap.

> 2026-10-10 frontend rebuild checkpoint: V2 exposes task navigation, exact-Run
> recovery/observation, connection management and PR discussion actions. New task
> drafts carry bounded budgets; Go resolves narrowing-only project configuration
> and pins the result through schema v186, creation replay and successor Runs.
> Supported manual HTTPS MCP registrations can manage bearer credentials through
> the shared Go API and OS store. Approval, scope and runtime capability gates
> remain independent. See [implementation status and evidence boundaries](FRONTEND_REBUILD_STATUS.md)
> and [task configuration ADR](adr/0170-task-budget-and-project-configuration.md).
> This checkpoint does not complete P4 environment/extension workflows or native
> cross-platform acceptance. Project exclusion paths and suggested skills/actions
> remain recorded metadata, without automatic filtering, installation or execution.

> 2026-10-09 recovery and connection workflow checkpoint (#292): Continuity
> Fork/Resume and checkpoint Fork open the returned Run in V2 without inheriting
> a source Thread binding. Late completion refreshes record and Thread lists
> without replacing a later navigation. GitHub connection editing uses the selected
> generation; confirmed disconnect deletes the connection's current local credential,
> preserving settings and historical evidence. Approval navigation retains the exact
> PR, snapshot and connection binding. Verification is recorded in the issue/PR; this slice
> does not add execution authority or certify the remaining frontend rebuild.

> 2026-10-09 Go security baseline (#289): Go 1.26.9 is the minimum compiler;
> CI and release builders read the exact version from go.mod. Local desktop
> builders require that same patched compiler. The x/net
> dependency includes the October HTTP/2 fixes. Native macOS packaging now
> declares and verifies macOS 12, matching the compiler's actual support floor.
> The nightly full matrix and blocking vulnerability audit remain enabled;
> see [CI security baseline](ci.md#go-security-baseline) for scan evidence limits.

> 2026-10-07 permission/capability alignment (#287): the frontend consumes the
> current Ask / Auto / Full readiness contract, with a Go HTTP fixture checked by
> both Go and the real APIClient. Task review exposes existing Run-bound delivery
> tools and explains independent startup gates. Historical permission readers and
> all Go authority checks remain in place. See
> [implementation and verification scope](FRONTEND_CAPABILITY_ALIGNMENT_287.md).

> 2026-10-07 workbench journey follow-up (#257): explicit extension actions return
> to the task composer, new-draft image eligibility matches the selectable creation
> route, unavailable historical Run directories fail closed, and Desktop bootstrap
> preserves the GitHub review capability. See the [acceptance scope and remaining
> candidates](acceptance/2026-10-07-workbench-journeys.md); this does not complete the
> broader model-to-delivery acceptance matrix.

> 2026-10-07 extension onboarding checkpoint (#276): shared Web/Desktop settings
> stage manual MCP descriptors, import pinned Plugin ZIPs and stage/review LSP
> configuration through Go-owned services. Backend capability flags, source/scope,
> explicit review and actual call evidence remain distinct. LSP uses a stable
> manager and the existing reviewed configuration format; MCP calls retain the
> normal task and approval path. See [ADR 0168](adr/0168-extension-first-onboarding.md).

> 2026-10-07 task-list checkpoint (#272): title search filters the complete matching
> task set before stable creation-keyset pagination. A bounded Go-owned execution
> observation travels with each list item; uncertain ownership/read failures stay
> unknown. Search/clear preserve the active task and draft. See
> [ADR 0167](adr/0167-task-title-search-and-execution-observation.md).

> 2026-10-06 application preview checkpoint (#274): ordinary Thread submissions remain
> the startup path. A Thread-scoped projection connects existing Command Runtime Jobs,
> exact source messages, bounded sanitized output and unverified loopback candidates.
> Browser close and exact process-owned Job cleanup remain separate; no new launch
> endpoint or process manager is introduced. See [ADR 0166](adr/0166-thread-application-service-preview.md).

> 2026-10-06 frontend checkpoint (#260): latest Thread transcript synchronization is
> separate from manual history paging; stable narrative rows are reused, and settings,
> Inspector and review modules load on first use. Source identities, Go-owned state,
> reconnect/final persistence synchronization and the official complete fonts are retained.
> See [performance measurements and verification scope](FRONTEND_PERFORMANCE_260.md).

> Scope checkpoint (2026-08-13): continue only the general-purpose Agent Harness and Code workflow. CTF-specific solving/offensive automation is an optional add-on with no active slices; retain generic extension seams only. Historical Cyber percentages are not current planning metrics. See [PRODUCT_SCOPE.md](PRODUCT_SCOPE.md).

Last updated: 2026-08-26

## Current Single-Slice Checkpoint: Durable Risk Escalation / Issue #138 / Schema v136

`risk_escalation.v1` 将 Workspace Access 之外的网络目标/用途、凭据种类、宿主路径、Policy
拒绝、非白名单工具及其他高风险请求冻结为完整宿主命令、资源预算、Run/Workspace、
Supervisor turn/call/invocation、执行快照/revision、root fingerprint、capability generation
和规范化 scope fingerprint。模型只可提案；操作者可拒绝、批准一次，或给当前 Run 的精确
scope 选择 1-900 秒、1-8 次上限。Grant 没有 bearer，每次 use/过期/撤销/失效均可审计。

提案和既有 Approval 原子提交，Run durable waiting 并释放 lease；决策只恢复原未执行 call，
拒绝是普通工具结果。host start 前持久化 intent；缺少终态 result 时永久 uncertain 且不自动
重试。重启不恢复进程 authority；权限/Profile/Workspace/root/executable/capability 漂移使
proposal 与匹配 grant 失效。继续复用 Approval/Grant、host proposal 和 lease 账本，不建立
第二套决策源。见 ADR 0140 与 `docs/risk-escalation.md`。

## Previous Single-Slice Checkpoint: Atomic Standard Code Preset / Issue #135 / Schema v133

`standard_code_preset.v1` 由 Go Application 单点拥有，将 Code/Plan、ready Local 或显式 Docker、
controlled、`workspace_access`、restricted CDP 与 exact trusted/ready Drydock 作为一个幂等元组
提交。`auto` 只选择 Local；不可用时只投影明确 Docker/Approval 下一步，不能回退 host 或
`full_access`。响应固定禁网、无凭证、`capability_grant=false`，不携带 bearer/private path。

created/paused Run 必须无 active lease；running 先写 `waiting_for_pause` 意图，精确 key 重试，
等 lease 与 Supervisor quiescent 后将 pause 与所有快照/事件同事务提交。Surface 不兼容创建新
Code Run。Drydock create 复用现有 WAL/ownership recovery；最终 fault injection 证明无半配置。
CLI、HTTP/OpenAPI、Desktop/React 共用合同，模型/Skill/MCP/Plugin/Hook/仓库配置不具备入口。
Drydock/worktree 不是安全沙箱；Local OS 或固定 Docker 提供隔离。见 ADR 0136。

## Previous Single-Slice Checkpoint: Unified Thread Transcript / Issue #153 / Schema v130

`thread_transcript.v1` 复用 schema-v129 Thread succession 与 schema-v130 item-stream ledger，按
Run ordinal、durable event sequence 和 item position 投影用户消息、公开模型文本、Harness 事实、
结构化工具阶段、审批、验证、检查点、交付与 Run 边界。opaque keyset cursor 与 exact route/filter
绑定，追加事件、successor 和进程重启不会移动旧窗口；provisional 模型/工具项只在对应 durable
identity 到达前出现，确认历史不重排。

Thread 页面成为普通工作流入口，同屏提供 Composer、暂停/恢复、审批、继续与交付；专业审计页
继续保留。Transcript 使用可变高度虚拟化和原生 disclosure；Composer 为独立 sticky sibling，
覆盖窄屏、200% 等效高度、中文 IME、虚拟键盘与 reduced motion。投影不含参数、raw output、
provider bytes、凭据或 private reasoning；React 不拥有 Supervisor authority。边界见 ADR 0134。

## Previous Single-Slice Checkpoint: Item-Level Model/Tool Streaming / Issue #152 / Schema v130

`llm.item_stream.v1` 将 OpenAI interleaved tool-call delta、Anthropic content block、Ollama/Mock
complete-item 与 legacy `ChatChunk` 归一为严格有序 response/item/call 生命周期。Application
以 Run/attempt 事实替换 upstream ID；argument delta 只在 256 KiB 有界内存中拼接，必须与完整、
合法 JSON call 一致。Provider execution event 被拒绝，只有 Go Supervisor 在 durable call 建立和
全部既有 Policy/budget/authority/idempotency 检查后记录 execution start/completion。

`model_public_stream.v3` 只显示稳定 ID、状态、脱敏工具名和参数字节数；`model.delta` 只保存
content-free boundary。二者都不能表示参数、raw wire、凭据或 private thinking。schema v130
不可变绑定 provisional response/item/call 与本地 call ID；旧 Session message 确定性投影为 durable
completed message item。取消、EOF、缺失/畸形 usage、模型漂移、重复/矛盾终态不补造成功完成。
边界见 ADR 0133。

## Previous Single-Slice Checkpoint: Stable Thread Identity / Issue #151 / Schema v129

`thread.v1` 为用户任务和 URL 提供稳定身份，并将历史 Run/Session 无损绑定到
Thread。终态 Run 后继续输入会原子创建新 Run/Session，不继承审批、lease、进程、
网络或凭据 authority。生命周期、导出与恢复边界见 ADR 0132。

## Previous Single-Slice Checkpoint: Standard Code Docker Fallback / Issue #133 / Schema v128

`standard-code-command.v1` 固定上层 toolchain/argv/relative-cwd/timeout/purpose schema，不暴露
backend/image/endpoint/mount/network/env/credential/Docker flags。Docker adapter 只认一个 Go 编译
profile：精确当前 Drydock -> `/workspace`，network none，固定 65532、只读 rootfs/toolchains、空
credential 输入与资源上限。固定镜像只 inspect 不 pull；daemon/image 缺失映射稳定 blocker/remediation，
禁止回退 unsandboxed host。

Drydock generation/Checkpoint/binding、Docker profile、workspace_access revision、process gates、
exact approval、Policy、budget 和 readiness 在 start 前重验；启动后的预期写入由 terminal
Drydock Checkpoint 归属。cancel/crash/restart/permission drift 复用已有 WAL/lease/exact labels/config/
absence cleanup；未知文件、目录、container 保留。Drydock/Worktree 是 ownership/recovery boundary，
不是 isolation boundary。边界与操作见 ADR 0131 / `docs/standard-code-docker.md`。

Schema v128 只重建既有 Docker product admission parent table，把 `workspace_access` 加入闭集 CHECK，
原样复制历史 admission，并恢复其 insert/binding/cancellation/start/launch/receipt/immutable triggers；
迁移提交前继续执行 foreign-key check。

## Previous Single-Slice Checkpoint: Run-Owned Drydock / Issue #131 / Schema v127

`drydock-workspace.v1` 为一个 Code Run 持久绑定 source Workspace/root/repository/common-dir/
branch/base、独立产品管理 worktree/branch、managed registry、ownership generation 和 current
binding。首次 create 只展示精确来源与 dirty/raw-index 状态，第二次必须确认同一 Workspace Trust
digest；Trust 永远不授予 process/network/credential/Git remote authority，source dirty 内容不复制。

Create 使用 WAL；Use/Checkpoint/Rewind/Undo/Fork/Deliver/Cleanup 逐次重验完整身份。Checkpoint
覆盖 tracked/untracked/raw index；Fork 建立 authority-reset Run；Delivery 只给 review patch，禁止
merge/push/force/source overwrite。崩溃、漂移或用户改动保留为 `recovery_required`；Cleanup/GC
只非 force 删除 exact clean owner，不枚举或删除未知目录。边界见 ADR 0129 与 `docs/drydock.md`。

## Previous Single-Slice Checkpoint: Unified Runtime Readiness / Issue #130 / Schema v126

Issue #130 已由 PR #144 合并。PR #143 先行合并并关闭 #129，因此 Epic #128 中 #130 对 #129 的
唯一前置依赖已经解除；该协议继续为 #135 Standard Code 预设和 #136 安全模式 UX 提供前置。
SQLite 不迁移，仍为 v126。

`run_capability_readiness.v1` 由 Go Application 单一计算 Permission、Profile、Interaction、
browser-CDP 与 Standard Code 的 `selected`、`selectable`、`runtime_available`、稳定
`blocked_by[]`、对应 `remediation[]` 和 `restart_required`。CLI 直接调用同一服务，HTTP 与
Desktop 复用认证 read route；React 只严格校验和呈现，不再从 Run/lease/client gates 重建授权。
投影固定 `capability_grant=false`，不含路径、endpoint、凭证、owner/lease 或 raw backend error。

Local/Docker 在 Run 状态与控制路径允许时可以记录意图，但没有 sandbox proof、Docker 或 backend
readiness 时继续 runtime unavailable。Standard Code 在本切片只投影
`capability_not_implemented`，不开放 mutation 或运行权。协议、稳定排序、版本拒绝和隐私边界见
ADR 0128。

完成证据：`go test -p 2 -count=1 -timeout 30m ./...` 全绿（Application 628.645s、HTTP
184.223s、Store 885.493s、Desktop 35.269s），readiness Application/HTTP 聚焦 race、Go vet、
module verify、Desktop secure tags、66 文件 299 项 Web、TypeScript、Vite production build 与
OpenAPI/TypeScript 双重确定性生成通过。OpenAPI/TypeScript SHA-256 为
`fde7d8af96ad0fcd719eab5d2b7b7701d1519996310253c777f174ed0267694e` /
`84c1cb48fa14466c2308f8a3d909eacd08466b2eaf7b8a95cd7a221c8d94d7d2`。

## Previous Single-Slice Checkpoint: Workspace Access Permission / Issue #129 / Schema v126

`workspace_access · 工作区执行` 是 Standard Code 的未来安全权限上限：Workspace 内读写可由
策略表达，但命令只能交给通过独立 readiness 的 Workspace Sandbox adapter。宿主无沙箱执行、
网络、凭证、用户主目录、持久用户/Agent 终端、Agent 输入和完整 CDP 均为 false。#129 不实现
adapter，也没有运行期开关；生产 bootstrap 固定 readiness=false，所有界面必须灰显且不得回退
到现有 host Command Runtime。选择需要精确确认，模型/Agent/Skill/仓库/恢复路径不能切档。

Schema v126 保留所有旧快照与 operation replay，通过 create-copy-drop-rename 扩展 SQLite CHECK，
并临时移除/按 canonical 定义恢复跨表 trigger；提交前验证 foreign keys。权限 revision 变化会在
同一事务释放活动 execution lease，旧 Job owner 和工具 authority 随 snapshot/revision/generation
漂移失败关闭。完整决策见 ADR 0127。

完成证据：串行无缓存全仓 Go 全绿（Store 843.955s），受影响 domain/executionauth/application/
store/httpapi 聚焦 race 全绿，Go vet/module、Windows Desktop secure tags、OpenAPI/TypeScript 生成、
66 文件 290 项 Web、production build 与 npm audit 零漏洞通过；CI 已加入同一聚焦 race 门。

## Previous Single-Slice Checkpoint: Legacy v97 Upgrade Compatibility / Schema v125

`v0.1.0-rc.2` 在共享用户数据目录遇到一枚正式固化前的 Windows 预览 v97 历史时返回
`FAILED_PRECONDITION`。发布 EXE 与 metadata hash 正确，数据库 `PRAGMA integrity_check` 为 `ok`；
旧 checksum 固定为 `844427411efc98247ebf521badb98a8c96e3a17b8aa5c7d429b7192d4dcad83b`，
正式值为 `e279b320761a7ae9ff7af17dbb9df0eceb80702d434f9dca30eca6825892559d`。
两份 v97 schema 的 24 个对象中只有 cleanup receipt trigger 不同：旧 trigger 多两项更严格的
final-transition lease identity 条件。

ADR 0126 与 schema v125 固化唯一兼容路线：仅接受 exact version/name/legacy checksum，绝不改写
旧 `schema_migrations` row；随后在事务中重建正式 trigger，再继续正常升级。未知 checksum、
错名、gap 和 newer schema 继续失败关闭。回归必须证明正式/旧 checksum 固定、真实旧结构可升级、
Workspace 数据保留、trigger 与正式 v97 字节语义一致、integrity 为 `ok`。不得让用户删除数据库，
不得替换已发布 rc.2 资产；完成后发布新 prerelease。便携包默认仍使用共享
`%USERPROFILE%\.cyberagent-workbench`，重新解压不会隔离数据。

本地验证已完成：专项与完整 Store 回归、289 项前端测试、Desktop Go/Wails 测试、生产标签
EXE 构建，以及真实旧 v97 数据库副本的 EXE 启动升级均通过。原数据库 hash 未改变，临时副本
已删除。正式发布仍需新 prerelease 的 CI 与 Windows 手工兼容矩阵，不能把本地构建当成已发布资产。

## Previous Single-Slice Checkpoint: GitHub Review Provider / Issue #118

2026-08-21 的 issue #118 推进到 `github-review-provider.v1`（ADR 0123，schema v124）。
GitHub App Device Flow 是首选生产路径：公开 client ID 可持久化，device code 仅在进程内，
access/refresh token 只进入 OS credential store。qualification 精确验证 account、App
installation 内的目标 repository、细粒度权限、SSO、rate limit、固定 API 版本与网络范围；
REST/GraphQL 只连接 `github.com`/`api.github.com`，Actions 日志跳转还需逐 host allowlist。

PR metadata、完整有界 changed files、reviews、threads/comments、checks、jobs、失败日志摘录与
Artifact metadata 以不可变、已清洗、不可信快照保存，再绑定本地 exact merge-base/HEAD、完整
diff、stable hunk、index/worktree/status/file hashes、冲突与可选 LSP 证据。相同内容的 evidence
仍按 Run/Workspace 单独登记，避免跨 Run authority 混淆；Code/root 模型只可读取当前 Run 已落库
的 evidence，不能自行联网。reply、resolve/unresolve、submit review、request reviewer 各自需要
精确 preview、一次性 Approval、当前 Code/Deliver 网络权限与执行前 capability/PR drift 复核；
重启恢复只观察 marker/state，绝不重放不确定写入。CLI、认证 OpenAPI、Desktop Repository 面板
与 Skill `review@1.5.0` / `focused-checks@1.2.0` 共用该 Application 服务。操作边界见
`docs/github-review.md`，不得在后续压缩中把 GitHub token、通用 HTTP 或远端 mutation 暴露给模型。

## Previous Single-Slice Checkpoint: Advanced Git Recovery and Managed Worktrees / Issue #117

2026-08-20 的 issue #117 落地 `git-advanced.v1`（ADR 0122，schema v123）。严格 union 只允许
hunk stage/unstage/revert、stash、rebase/cherry-pick/bisect 和受管 worktree 的 Go-owned
模板，不接受 raw Git argv、Shell、host path、任意 ref/pathspec 或 bisect 命令。preview 固定
repository/common-dir、HEAD/branch、index/worktree/status/stash/sequencer/upstream；hunk 再固定
base/index blob、whole-file worktree hash、context 与 patch。execution 重新渲染全部证据并绑定
当前 permission revision、process-random capability generation、private lease 和一次性 Approval，
每次 mutation 均开闭 Workspace Checkpoint。

operation 写前以 CAS 获取唯一执行权，terminal receipt/事件不可变；rebase/cherry/bisect 另有
original ref、target、current HEAD、sequencer digest、conflict 与 generation 的 durable sequence。
重启只观察并 terminalize，不重放；active/conflicted sequence 保持显式 continue/skip/abort。
worktree 目标由 managed root/common-dir fingerprint/safe name 派生，永久登记 repo/common-dir/
branch/commit/Run/Workspace/path hash；symlink/junction、复用、locked/dirty/head drift、外部或
未登记 prune 默认拒绝。create 后登记前崩溃只能在全身份和 clean 状态精确匹配时纳入 registry，
旧 operation 仍是 interrupted，不能伪装 success。

Git 环境关闭 system/global config、hook、credential helper、prompt、editor/pager、external diff、
fsmonitor、LFS smudge 与 executable local filter/diff/merge driver；所有 caller path 固定 literal
pathspec。保护/共享/detached history、force push/reset-hard/clean-force 不可表达。bisect run 只有
Go/npm 固定 offline recipe、step/timeout budget 与 whole-process-tree reap。CLI、OpenAPI、Desktop
和 startup recovery 共用 Application；公开投影不含 private lease、host path、raw JSON/argv。
`review` 1.4.0、`focused-checks` 1.1.0 与 exact archives 已同步。恢复入口见
`docs/git-advanced.md`，禁止在后续 compaction 中退回通用 Git/Shell 实现或重放 running row。

## Upstream Checkpoint: Deliverable Batch Agents / Issue #103

2026-08-20 的 issue #103 落地 `batch-delivery.v1`（ADR 0119，schema v118）。该合同只消费
已审批/admission 的 core child proposal，精确复核 Agent、DAG、预算与 expected artifacts，
再为最多两个 child 创建独立 branch/worktree、generation lease、一次性 owner token 与关闭
工具 profile。旧 SpecialistRunner 仍是 no-tool；可交付工具只允许 owned Scope 内的有界
list/read/search、人工提案式 create/replace 与固定 Git status/diff/commit，不允许 delete/
rename、Shell、任意 process、network、credential、Debug、审批或派生 child。

plan/workspace/mailbox/receipt/review/merge queue/step 和 operation facts 全部持久化。commit 先写
intent，崩溃恢复只接受 prior HEAD 的一个直属非 merge 提交；Submit 与 Review 各自重算完整
merge-base diff、call-chain digest、changed paths 和验证，并在验证后重证 exact clean state。
dirty/state drift、作者摘要、stale generation 或越界路径不能变成交付。独立 integration
worktree 按 DAG 顺序逐项合并并累积重跑已合并 task 的全部验证，再重证 source、确定性 merge
commit、integration 与所有 child receipt；base drift 需第二次确认，overlap、state drift、
文本/语义冲突和测试失败 block。只有 exact state 下的失败才回滚当前 step，不污染 source/
child；崩溃恢复不接受任意 descendant。最终只产生本地 integration branch/head，不 push 或开 PR。

默认验证只有 `git_diff_check`。`go_test`/`npm_test` 会运行 child-authored code，必须显式开启
permission-control + danger-full-access + `--enable-batch-validation-execution`，且 Run 每次仍是
running + current full_access（或显式更高的 debug）；Desktop batch mutation 另有
`--enable-batch-delivery-control`。
验证不使用 Go test cache，以 Windows Job / Unix process-group 边界回收 inherited 后代，只
持久化完整观察流 output digest；offline/去凭证环境不是 OS sandbox，POSIX 主动 daemonize
脱组仍是残余风险。owner token 明文不持久化，遗失后需 CAS 轮换 generation。取消只
清理 exact clean identity，dirty/committed/drifted 成果保留；启动 reconciliation 在非 running
Run 上不产生文件/Git 副作用，也不恢复 token、进程或旧 authority。完整边界见
`docs/batch-delivery.md` 与 ADR 0119。
## Current Single-Slice Checkpoint: Source-Bound Real-Browser UI Evidence / Issue #102

2026-08-20 的 issue #102 落地 `ui-evidence.v1`（ADR 0120，schema v119）。不可变
manifest 把 Run/Mission/Session/Workspace 与 fresh source checkpoint、可选 build/必需 start
`command-runtime.v2` recipe、readiness、固定浏览器 executable/version/SHA-256、literal
loopback URL/route、viewport/DPR、locale/theme/reduced-motion、deterministic fixture/seed/page
state、有序交互/断言、capture/mask 和 fail-closed diagnostics 绑定。Application 在 build
前、readiness 后、浏览器断言后和 owned process cleanup 后重捕获 source；commit/branch/index/tracked/untracked/root
漂移失败关闭。已占用 readiness 端口返回 `launch/preexisting_service`，不会收养或停止外部
服务。

应用由 v116 Run-owned command runtime 持有，浏览器沿用 Safe Web 的固定安装验证、创建时
Job Object/WFP 绑定与 attempt-private Profile，但使用独立
`restricted-cdp-ui-evidence.v1` 方法白名单。允许真实 navigate/click/type/selector、PNG、
DOM、accessibility、performance 和 bounded console/page/network/HTTP diagnostics；拒绝
`Runtime.evaluate`、cookie、response body、request mutation/replay、Full CDP 和个人 Profile。
取消、超时与 Desktop shutdown 等待 browser/application tree、Profile、network guard 和端口
清理；启动/读取仍要求 live lease，清理只能凭本 Attempt 的 durable Job/operation/Run/lease
绑定回收当前 manager 精确持有的 Job。重启只把残留 `running` 收敛为
`interrupted/cleanup`，不恢复启动 authority。

状态闭集为 `not_run|running|passed|failed|cancelled|timed_out|interrupted`，只有 `passed`
可通过；失败阶段固定 build/launch/readiness/navigation/selector/assertion/console/network/
capture/cleanup。SQLite v119 封存 manifest、append-only steps 与内容寻址 artifacts，限制
32 MiB/artifact、128 MiB/attempt、2 GiB/store。每个 artifact 绑定 source commit、Run/
Attempt/step、MIME、bytes、SHA-256、viewport/dimensions、capture time、redaction 和
`untrusted=true`；文本、URL 和动态 screenshot 区域按统一规则脱敏。

Desktop 默认保留历史只读面，只有 Run execution + permission control + danger-full-access +
restricted browser CDP + `--enable-ui-evidence` 同时启用时允许启动/取消。认证 OpenAPI 与
Desktop 共用异步 Application service；CLI 仅 list/show/hash-verified exclusive export。
`run-verify` 升级为 1.1.0，并要求与 `focused-checks`/PR receipt 对齐。Windows CI 用真实
Edge、临时 Profile、创建时 Job 和 deterministic loopback fixture 覆盖 desktop/mobile、
light/dark、en-US/zh-CN、full/reduced motion；`receipt.json` 固定 clean commit、浏览器
version/hash、matrix 和 artifact hash/尺寸，并由缺失 click handler 的回归页证明真实交互
断言能发现源码/build 检查无法单独证明的错误。完整操作边界见 `docs/ui-evidence.md`。

最终本地门：完整 Go 套件通过（Application 611.312 秒、HTTP 229.102 秒、Store v1→v119
974.372 秒），受影响路径 race/vet/staticcheck、模块完整性、OpenAPI/TypeScript 确定性生成、
62 个前端文件/266 项测试、build/npm audit、Rust fmt/test/clippy、CI YAML 和 Windows Desktop
可复现双构建均通过。真实 Edge 151.0.4129.93 的最终 source-bound receipt 由 PR/CI 报告；
regression caught 与 browser tree/port/Profile/fixture cleanup 全为 true。本次真实运行还修复了 Windows Profile
sharing-lock 的 exact-owner 5 秒有界清理重试，并把 screenshot pixel surface/dimensions 在
domain/SQLite/React 三层绑定到 viewport×DPR、在完整 PNG 解码前限制 header dimensions。
diagnostic URL 会重新通过 exact TargetScope，越界 scheme 只保留 `[blocked-url]`；React 还
复核 step/artifact chronology。连续真实运行的三张 PNG 逐字节一致，Desktop 可复现 EXE
的最终 source-bound SHA-256 由 PR/CI 报告。
最终本地与 CI receipt 均在 clean checkout 强制 source identity。Go 1.26.5 的 govulncheck
5 项可达结果均来自标准库且修复于
1.26.6，没有新增模块依赖问题。

## Previous Single-Slice Checkpoint: Transactional Workspace Checkpoints / Issue #101

2026-08-19 的 issue #101 落地 `workspace-checkpoint.v1`（ADR 0118，schema v117）。每个
检查点不可变绑定 Run/Mission/Session/Workspace、attempt/capability generation、触发收据、
parent/cursor、规范根指纹、base commit/branch、原始 Git index 与稳定 tracked/untracked
manifest；普通文件/index 以 SHA-256 内容寻址去重。固定上限为 20,000 entries、4 MiB/
file、32 MiB/index、64 MiB/checkpoint、2,000 preview changes、256 conflicts、2 GiB
全局 blob store。ignored/generated/large/sensitive/link/reparse/special/unreadable/external
都显式标记；没有静态加密声明，疑似敏感文件不会写入 blob。SQLite sealing/refcount/
immutability/GC/quota trigger 与 schema v1-v116 downgrade chain 均已覆盖测试。

FileEdit 与 `agent-code-tools.v1` 写入、Run-owned command foreground/background、typed Git
写入以及未来 agent merge writer 共用 operation-keyed before/after boundary。Shell 没有
portable watcher，因此即便 in-root bytes 可存储也明确为 partial；根外、registry/service/
network side effects 不在恢复承诺中。Preview 比较 reviewed cursor、target 与 freshly
captured live state；external edit、dirty index、root/branch/commit/case/link drift 都冲突。
Undo/Redo/Rewind 是 append-only 新写，要求 paused Code/Deliver、active Session、无 live
execution lease、当前权限/进程 capability、显式 operator 与 exact CAS cursor，不使用
`git reset --hard` 或 blanket untracked deletion。

Fork 创建并验证独立 Git worktree/branch，再原子注册 Workspace/Mission/Run/Session/events/
continuity node；只复制目标 bytes/index，不继承 permission/profile authority、approval、
credential、capability、lease、terminal/process/network grant，source cursor 不变。启动时
reconciliation 关闭普通 interrupted boundary、只在 identity/authority/cursor 仍匹配时恢复
prepared restore，并完成已注册但 checkpoint 尚未落盘的 Fork 而不复制 worktree。CLI、
OpenAPI 与 Desktop Checkpoints tab 共用 Application 服务。不要把 conversation continuity
checkpoint 当成 Workspace bytes，也不要把 Workspace restore 当成历史 authority 恢复。

## Previous Single-Slice Checkpoint: Structured Dependency Waiting / Issue #50

2026-08-15 的 issue #50 落地结构化依赖等待（ADR 0102，schema v101）。`agent_dependency_edges`
持久化版本化 wait edge（source/target/reason/deadline/generation/failure_policy，
五态 wait|satisfied|failed|cancelled|expired）；写入事务内用 `waitgraph.DAG` 在持久化 open
边上拒绝自环/两节点环/多节点环/下层运行时→Agent 反向等待/跨 Run 端点/超 64 深度链。
`agent_dependency_wakes` 每条边至多一张唯一唤醒收据：结算、父取消扇出、deadline 过期与
崩溃恢复共用同一 settle 事务，回放/并发绝不二次唤醒；等待中的 agent 源按既有 wake 语义转
ready，fail 策略下转 failed。RunService 终态钩子自动扇出取消。无进展 deadline 发出
`dependency.deadlock_detected`；同一源 wake→rewait 超 64 次拒绝为稳定 `LIVELOCK` 错误并发
`dependency.livelock_detected`（新 apperror 码 DEADLOCK/LIVELOCK）。Run Activity 新增
`dependency` kind 折叠项（七种事件，只显示 source→target 与 reason，不透传 raw child
output）。模型只能提出意图；#51（模型驱动 child 调度）消费本合同的 open 边、唤醒收据与
诊断投影。

## Previous Single-Slice Checkpoint: Monetary Budget, Price Snapshots, Qualification / Issue #49

2026-08-15 的 issue #49 落地金额预算、价格快照与 Provider 资格诊断（ADR 0101，
schema v100）。账本全整数 micro-USD：`pricing` 包提供 `price_snapshot.v1` wire 格式（
64 KiB/512 entries/指纹重算/未知字段拒绝/有效期覆盖导入时刻且≤1年）与天花板除法估算；
`run_monetary_usage`/`run_monetary_reservations` 按 reserve→call→settle-or-release 记账，
CAS 推进、open=reserved−settled−released、remaining=cap−open，跨 root/specialist/
readonly fanout 共享 run 聚合；终态 `model.*` 事件在下次 reserve/usage 前对账自愈，run
终态强制释放 open 预留。价格只由 Go 控制面导入（HTTP `POST /api/v1/models/prices`、
`provider price-import`），模型/README/Skill/仓库文件无法触达；同内容导入幂等回放，新导入
原子轮换 active。`MaxCostUSD>0` 才启用闸门，缺价格条目 fail-closed，结算缺条目按整笔
预留保守计费。资格状态 8 值 taxonomy 持久化于 `qualification_status.<provider>.<model>`，
诊断/qualification/可用性视图均返回，未诊断留空。Mission 级金额上限与 vendor 价格订阅
留待后续。

## Previous Single-Slice Checkpoint: Ollama Loopback Provider / Issue #48

2026-08-15 的 issue #48 新增无凭证 loopback-only Ollama Provider（ADR 0100）。启用
条件只有显式 `CYBERAGENT_OLLAMA_BASE_URL`（仅 loopback `http`）+ `CYBERAGENT_OLLAMA_MODEL`；
非 loopback、HTTPS、URL 凭证/query/fragment、路径前缀、redirect 与代理 transport 拒绝。
原生 `/api/tags` 模型列表、`/api/chat`（同步 + NDJSON 流式，含取消/截断/trailing-event
拒绝）、`/api/show` 能力探测（tools/vision/JSON/context，未知即不支持）、usage 估算与
稳定错误映射（model_not_found/capacity/rate_limit/network + 服务未启动可解释诊断）。
no-tool 模型在 Harness（ToolStrategy=none）与 Provider（拒绝透传 Tool schema）两层都走
安全路径。Registry kind `ollama`/transport `ollama_chat` 接入 CLI/HTTP/Desktop/Web；
credential 枚举保持四位。不自动安装/pull/扫描局域网；本地模型仍过 Policy/redaction/
budget/Tool Gateway。本机无 Ollama，真实 smoke 为文档化可选步骤；fake-server 离线测试
覆盖全部路径。

## Previous Single-Slice Checkpoint: Docker Sandbox Product Admission / Schema v99

2026-08-14 的 issue #40 将 network-none Docker Sandbox 接入产品。新的
`DockerSandboxService` 是 readiness、admission、status、start、cancellation 和 startup
recovery 的唯一业务服务；CLI 直接调用，HTTP/OpenAPI 与 Desktop 只做投影，模型
`sandbox_docker_run_propose` 经 fenced Tool Gateway adapter 只能调用 Admit。模型不能
提交 daemon endpoint、Docker flag、host bind、environment/proxy、image override、
network relaxation，也不能调用 Start/Cancel/transport。

真实执行默认关闭。每次 service construction 生成 fresh random runtime epoch，只把摘要
写入 admission；SQLite、旧 capability snapshot 或重启都不能恢复 start authority。准入和
start final fence 都重新校验当前 Run/Mission/Workspace、v54 exact plan/Manifest、当前
`docker` Profile、最新 `approval|full_access|debug` permission、精确 approved per-call
`sandbox.manifest` approval、Policy、剩余 wall/tool budget 与 30 秒
`sandbox.readiness.v1`。Full Access 不替代精确审批。Docker 不可用时稳定失败关闭，不回退
LocalRunner/host process。

产品 Manifest 目前必须 environment-free、secret-free、
`network.mode=disabled`、零 target。Create 使用 Docker network none，并在 inspect 时
拒绝 port/DNS/extra-host/link/endpoint/proxy/address/gateway/alias/DNS-name 状态。Generic
Manifest/v54 中的 allowlist 不代表 enforcement；exact host/port/protocol 仍需独立
Go-owned egress guard。因此 `ManagedEgressEnabled` 与 allowlist 均返回
`managed_egress_unavailable`，remediation 是使用 network disabled/none。不要在后续压缩
上下文中把 scoped egress 记成已完成。

Schema v99 新增 immutable product admission、独立 Start WAL/launch binding、append-only
cancellation、terminal receipt，并持久化 v98 output-staging entries 与每 attempt 唯一约束。
Migration 不回填或扩权 v97/v98。CPU/memory/PIDs/disk/wall/log/tool budgets 都被 admission
冻结并在 Go/SQLite/Docker/I/O 边界复核。Post-exit 在 exact exited checkpoint 与
lease/identity fence 后先捕获 bounded logs；只有 natural exit 0 且当前 Artifact authority
仍匹配才 stage/re-read/re-hash/atomically commit outputs，之后 cleanup。Timeout/cancel/
non-zero/I/O failure/authority change 不提交输出，cleanup 仍必须完成。

Cancellation 先持久化 sticky fact，再 signal active context 或 takeover released/expired
exact lifecycle。Startup recovery 只处理已有 launch binding 的 records：可以 reconcile/
terminate/clean exact owned resource，但不得用旧 epoch start；admission-only 不会自动启动。
Run Activity 只投影 metadata whitelist 与
`preparing|starting|running|cancelling|cleaning|cleaned`/bounded terminal state。Focused
domain/Sandbox/Store/Application/race tests 已通过；最终仍需完成 cross-surface、OpenAPI/
Web/docs 与 optional real-daemon integration gate，再由 root 记录最终结果。见 ADR 0099。

### Previous Single-Slice Checkpoint: Bounded Docker Container I/O Contract / Schema v98

On 2026-08-14 schema v98 added the bounded Docker container I/O contract for
issue #39, still without any product entry or authority grant. A sealed
read-only input projection (sandbox_docker_input_projection.v1) binds
canonical relative paths, SHA-256, size, and media type to the lifecycle
attempt/generation, plan, observation, Run, Mission, Workspace, and spec; the
input mount stays fixed at /run/cyberagent/inputs read-only and a mount
isolation verifier checks real inspection mounts (exactly one writable mount,
owned by the dedicated output target; input and workspace trees read-only).
Log capture (sandbox_docker_log_capture.v1) goes only through POST
containers/{id}/attach with logs=1&stderr=1&stdout=1&stream=0; a demuxer
bounds bytes (256 KiB) and lines (4096) per stream, enforces a deadline,
rejects malformed/oversized frames, counts and replaces invalid UTF-8,
redacts secrets, and persists only digest-only receipts. Output staging
(sandbox_docker_output_staging.v1) exports only GET containers/{id}/archive
of the dedicated output mount and walks the tar under one Windows/Linux path
rule set: no absolute/traversal/backslash/drive-letter paths, no
symlink/hardlink/device entries, no duplicates, caps of 64 files / 4 MiB per
file / 16 MiB total, media-type detection, and text redaction into a
process-local staging directory; rejected archives carry no trusted manifest.
Atomic output commit (sandbox_docker_output_commit.v1) requires an accepted
manifest that exactly matches completed staging entries, re-reads and
re-hashes the staged files, and inserts the receipt plus every entry in one
replay-safe SQLite transaction; a failure leaves no partial rows. The HTTP
transport allowlist admits exactly the two endpoints, the application service
stays unwired from CLI/HTTP/Desktop, and 15 new event kinds record the
prepared/acquired/taken-over/failed/completed transitions. Focused domain,
golden-vector, adversarial-path, transport, store, and application tests pass
on macOS; Windows/Linux CI will exercise the same path matrix. See ADR 0098
and issue #39. Do not repeat this slice or the v97 lifecycle review after
compaction. At that historical checkpoint product admission was the next
Docker slice; schema v99 above has now delivered its exact network-none subset.

### Previous Checkpoint: macOS Desktop Portable Build / Schema v97 Unchanged

On 2026-08-14 a new single-slice delivery added a macOS portable Desktop build
without changing SQLite schema or any authority boundary. cmd/cyberagent-desktop
is now split by build tag: shared shell code under "desktop", Windows-only
WebView2/Acrylic/registry-launcher code under "windows && desktop &&
wv2runtime.error", and a new "darwin && desktop" port. macOS uses bundled
WKWebView (no runtime prerequisite probe), reports startup failures through a
bounded, strictly escaped best-effort osascript dialog, and discovers workspace
launchers from a fixed .app set launched only through /usr/bin/open; Finder
opens the registered directory itself. Windows-only seams (Credential Manager,
ConPTY terminal, Safe Web/WFP, controlled host execution) keep failing closed
on macOS. The in-process renderer origin gate pins the platform authority
(wails.localhost on Windows, the custom wails:// scheme host on macOS) and the
build pins the cgo compile/link deployment target to the Go 11.0 minimum.
scripts/build-desktop-darwin.sh produces an ad-hoc-signed, un-notarized
build/desktop/Prayu.app with the same consecutive-build SHA-256 reproducibility
check, plus scripts/check-macos-compat.sh Mach-O/codesign/deployment-target
checks; the CI gains a desktop-macos job and the frontend checks fail fast on
a non-Node-24 baseline. Desktop-tagged Go tests pass on macOS, App/
desktop-bridge React tests pass, and release_ready stays false until signing,
notarization, and the manual macOS matrix. See ADR 0097 and
docs/DESKTOP_PLAN.md. Do not repeat this port or the Windows-file review after
compaction.

### Previous Checkpoint: Durable Docker Lifecycle Ownership / Schema v97

On 2026-08-14 schema v97 added a separate, package-private, non-authorizing
Docker lifecycle aggregate. It atomically persists the immutable launch intent
and initial lease before create, records an immutable prepared action before
each Docker mutation, appends hash-chained lifecycle transitions, and permits
one immutable cleanup receipt. The lifecycle Attempt is distinct from the v56
never-started rehearsal Attempt and binds its exact v54 plan, Run, Mission,
Workspace, request/spec/authority/name/endpoint fingerprints, immutable
resource generation, and versioned ownership-label fingerprint.

Every action, transition, cleanup receipt, and daemon mutation is fenced by
the complete active lease identity: lease id, owner id, lease generation,
resource generation, and expiry. Renewal retains the identity; only release or
expiry permits generation-plus-one takeover. Recovery is database-led and
inspects only the deterministic name. The complete nine-label ownership set
and exact container configuration must match before mutation; partial, legacy,
foreign, or inconsistent containers fail closed and remain untouched.

The Supervisor reconciles pre-create, created, started, exited,
timeout/cancellation, cleaning, cleaned, and failed states without duplicate
daemon effects or cleanup receipts. Focused Sandbox, Store, and Application
tests cover write-ahead order, stale fencing, takeover, replay, ambiguous
responses, exact recovery, foreign-container safety, and private event payloads.
See ADR 0096. Product entry, execution authority, output/log capture, Artifact
commit, and production admission remain false. ADR 0095 retains the real Docker
Desktop transport acceptance; do not rerun it after compaction unless the
transport changes.

### Previous Single-Slice Checkpoint: P13-G1 Workspace Import / Schema v96

On 2026-08-13 the user assigned the `P13-G1` label to a new single-slice
Workspace-import delivery. This label is intentionally separate from the
archived P13-G1/G2/G3 Live Activity batch below; do not repeat either body of
work after compaction.

The Windows Desktop New Task flow now performs `select directory -> register
Workspace -> create Run`. Wails opens the native directory picker; the selected
absolute path remains inside Go, where an existing directory is normalized and
registered idempotently. React receives only the Workspace ID, display name,
and timestamp, then uses the existing idempotent Run-create endpoint. Selecting
the same directory reuses its Workspace, same-name directories receive stable
distinct names, cancellation creates nothing, and import does not write a
`.prayu` file or otherwise modify the selected directory. The ordinary Web UI
retains the registered-Workspace selector as a compatibility fallback.

Focused Go Workspace/Desktop tests, a real SQLite control-plane import test,
24 focused React tests, strict TypeScript, Vite production assets, Desktop/WebUI
package tests, and Windows Desktop packaging pass. The current EXE is
`build/desktop/cyberagent-desktop.exe`, SHA-256
`42e3019559dbeafef83a0868ab6700a424718da5c207f720a836c3505b0811be`.
No schema, model, WFP, Docker, paid Provider, Agent authority, or file-access
policy changed. This turn is complete at P13-G1; do not infer or start P13-G2.

## Maintenance Checkpoint: README and Dependency PR Review

The 2026-08-13 maintenance pass reviewed every open GitHub PR. It merged
`#18`, `#20`, `#21`, `#22`, `#23`, and `#26`; it closed `#17` and `#19`
because both must be rebased and regenerate the shared Rust/WASI fixture once,
and closed `#25` because jsdom 30 requires a newer Node baseline and conflicted
after the grouped Web update. Combined `main` commit `0a6a4d5` passed all four
GitHub CI jobs: Go, TypeScript, Rust fixtures, and Windows Desktop.

The root README is now a concise Chinese product page with an English mirror in
`README.en.md`. Legacy percentages and slice phases live under the README's
historical-development section and in the existing ledgers. `PRODUCT_SCOPE.md`
is authoritative: CTF-specific solving and offensive automation are optional
add-ons with no active slices; only generic extension seams remain. Do not
repeat this PR review or reinterpret historical Cyber percentages as queued
work after compaction.

## Previous UI Checkpoint: P13-H1 through P13-H3 / Schema v96

P13-H1 reorganizes the existing right-side Workbench tools into explicit
Workspace, Run, and Coming Soon zones. Review and Files remain workspace
surfaces; Terminal and Side Tasks remain Run surfaces; Browser is a disabled
reserved item and gains no CDP, network, process, or navigation authority. The
zoning takes product-structure cues from `agegr/pi-web` while retaining Prayu's
existing React routes and Go-owned control plane.

P13-H2 replaces the beige composer/public-stream/popover surfaces with an
independently implemented liquid-glass material: translucent fills,
backdrop blur, saturation, inset highlights, restrained shadows, and distinct
light/dark/glass variables. It takes general material cues from
`greyd097/yzrt` and `u7663394/LGGC-liquid-glass`; no reference source was
copied. Reduced-transparency and forced-colors fallbacks remain readable.

P13-H3 self-hosts JetBrains Mono Variable, forces Vite font assets to remain
same-origin under the strict `font-src 'self'` CSP, adds a bundled favicon, and
makes the right sidecar an overlay at narrow widths so it cannot squeeze the
composer into vertical text. Production Playwright QA verified a 790x118
desktop composer and a 381x118 narrow composer with zero horizontal overflow
or console errors. Strict TypeScript, 17 focused React tests, a zero-vulnerability
production npm audit, production Vite assets, and the Windows Desktop package
pass. The current EXE SHA-256 is
`d4dd4156574835683fdd8882348577832a674efc645499a9870085327596df42`.
The latest upstream pagination, keyboard-navigation, and focus-trap changes
also pass the expanded 21-test focused gate. Automated Windows compatibility
checks pass; release readiness remains false
only because signing, an installer, and the manual Windows matrix are not part
of this slice. Schema and authority are unchanged. Do not repeat P13-C through
P13-H, WFP, Docker, or paid Provider probes after compaction.

### Previous Checkpoint: P13-G1 through P13-G3 / Schema v96

P13-G1 repairs the Desktop disclosure geometry that compressed Harness facts
into vertical, overlapping columns and tightens assistant Markdown and timeline
spacing. P13-G2 upgrades the process-local stream to
`model_public_stream.v2` with explicit `root_message` and `tool_commentary`
content kinds. Ordinary final answers can no longer fall back into Live
Activity; only bounded, plain-text, Policy-checked text accompanying Tool Calls
can become provisional or durable commentary. Verified Tool lifecycle facts
are grouped as `Ran N operations` using the actual operation names and terminal
status, never model claims.

P13-G3 removes stale provisional rows after bounded finalization misses and
refreshes exact Run activity/event queries immediately and after short
projection delays. A durable commentary item replaces its provisional identity
without duplication, and completed Tool facts override stale running requests.
Targeted React tests pass 87/87, affected Go application/runactivity/httpapi
packages pass, strict TypeScript and Vite production build pass, and the rebuilt
Windows Desktop was visually checked against the reported overlap. The current
artifact is `build/desktop/cyberagent-desktop.exe`, SHA-256
`e556e38dc47cf21feb61fcce1a6f14ed2f248be029430ae3101f3dce0f386312`.
No WFP, Docker, or paid Provider probe was repeated.

### Previous Checkpoint: P13-E1 through P13-F3 / Schema v96

P13-E1 adds allowlisted GFM rendering for public assistant text. Raw HTML,
images, and non-HTTP(S) links remain inert. P13-E2 groups consecutive Harness
facts into disclosure blocks without collapsing operator or model messages.
P13-E3 adds confirmed, idempotent Session archival that hides a conversation
while retaining messages, its Run, and audit history. Foreground cancellation,
fresh execution handoff keys, and a bounded structured-Tool timeout prevent a
failed URL or Tool turn from permanently owning the input queue.

P13-F1 adds a compact changed-file index and right-side, line-numbered unified
diff review drawer. P13-F2 adds the display-only
`model_public_commentary.v1` contract and immutable
`model.public_commentary` event before Tool execution. Commentary is Policy
checked, redacted, UTF-8 valid, bounded, non-verifiable, and excluded from
Session history and trusted context. Provider thinking, prompts, raw deltas,
Tool arguments, and raw Tool output remain private. P13-F3 moves the existing
process-local public stream into Activity and converges provisional text into
the exact durable attempt/model/tool-round item without duplication.

The ordinary all-package Go suite and `go vet ./...` pass. The six-slice gate
passes full `go test -race ./...` in 513.4 seconds (Store 488.3 seconds),
`staticcheck`, zero reachable `govulncheck` findings, Rust fmt/test/clippy,
55 frontend files / 208 tests, stable OpenAPI generation, zero production npm
vulnerabilities, and the Vite production build. No unresolved high/medium
finding or deadlock remains. Do not repeat P13-C through P13-G, WFP, Docker, or
paid Provider probes after compaction.

### Previous Checkpoint: P13-D1 through P13-D3 / Schema v96

P13-D fixed interactive second-turn convergence, made composer Plan mode the
single product entry, and reduced default Run navigation to Activity,
Approvals, Diffs, Repository, and Files. Its strict protocol, stale-revision,
and UI-only authority boundaries remain unchanged.

### Previous Checkpoint: P13-C1 through P13-C3 / Schema v96

P13-C1 makes the previously domain-only `host_command_proposal.v1` contract
durable for `approval` permission Runs. The Root Supervisor can propose one
exact absolute executable plus SHA-256, separated argv, Workspace-contained
cwd, sanitized environment key set and digest, explicit host-network intent,
timeout, and purpose. It cannot submit Shell text, environment values,
persistent/background execution, or common interpreter inline-code switches.
Schema v96 immutably binds the proposal and digest-only operation to the exact
Run/Mission/Session/Workspace/root Agent, active tool invocation, interaction,
execution profile, and permission revision. Direct update and deletion fail.

P13-C2 adds an independent operator review and exactly-once execution chain.
Approval requires a separate operation key, explicit execution confirmation,
the process-local operator-approval gate, and a current trusted
Code/Local/Controlled `approval` snapshot. Go reopens and rehashes the
executable, reconstructs the sanitized environment, and rechecks every durable
binding before recording a write-ahead execution intent. A prepared intent
without a durable result is uncertain and can never retry automatically.
Result bytes are bounded, control-cleaned, redacted, and returned to the exact
Session as `UNTRUSTED HOST COMMAND RESULT` with no instruction authority; only
metadata receipts and digests persist.

P13-C3 exposes proposal creation as the Root-only `host_command_propose` Tool
and review through read-token GET plus control-token POST HTTP routes and the
Desktop approval center. The UI displays the executable identity, separated
argv, cwd, environment names/digest, timeout, host-network intent, and a
prominent `non_sandboxed=true` warning. Approval requires an explicit checkbox
and typed confirmation action. Environment values, raw output, Shell strings,
and capability grants are absent from OpenAPI and renderer contracts. Ordinary
API and Desktop processes require both permission control and the separately
default-off host-proposal startup flag; operator preview enables this
approval-only path but does not enable danger-full-access or Debug maximum.

The cumulative six-slice gate is complete on the final code. The uncached
ordinary Go suite passed in 505.4 seconds and the full race suite passed in 614
seconds with zero races. Vet, zero-warning staticcheck, govulncheck (zero
reachable vulnerabilities; two module advisories are not called), module
verification/tidy, 52 Web files/192 tests, strict TypeScript, deterministic
OpenAPI/TypeScript generation, Vite, zero-vulnerability npm audit, Rust
format/7+2 tests/clippy/RustSec/WASI release, secure Desktop tags, and the
reproducible Windows build all pass. OpenAPI is 88 paths / 96 operations / 212
schemas. The unsigned portable executable is 47,230,464 bytes with SHA-256
`38029b13fb65d3fea4bc807d17e77736757acb71a2151564d353dca8d5f8c8af`;
`windows_automated_checks_passed=true` and `release_ready=false` are correct.

The combined audit found no unresolved high- or medium-severity issue. It
confirmed separate read/control authentication, exact URL Run/proposal
binding, immutable transactional review/intent/result records, write-ahead
exactly-once fencing, process-local operator capability rechecks, and the
absence of environment values, raw output, Shell text, or renderer-created
authority. During the gate it fixed missing ordinary-API capability
observability, a Desktop tagged-test hierarchy assumption, the README v96
continuity row, and one Go error-string style issue. GitHub CI run
`31310048813` then showed that Linux `internal/store` was still actively
migrating when Go's default per-package ten-minute timeout expired; every
completed package and all TypeScript, Windows Desktop, and Rust jobs were
green, and no assertion or deadlock failed. CI now uses the same explicit
20-minute full-suite budget as the local release gate without changing package
scope, assertions, or fail-fast behavior. Do not repeat P13-B, P13-C, this full
gate, WFP, Docker, or paid Provider probes after context compaction.

### Previous Checkpoint: P13-B1 through P13-B3 / Schema v95

P13-B1 adds `model_public_stream.v1`, a process-local replaceable snapshot of
only the safe public `root_lifecycle.v1.message` prefix. The Go parser accepts
top-level string fields only and withholds content until version/action are
valid. Unknown, duplicate, nested, fenced, invalid UTF-8/escape, Policy-denied,
or secret-bearing content fails closed. Raw Provider JSON remains in worker
memory; provisional text never enters SQLite, Run events, or later model
context. The final validated Session message remains canonical.

P13-B2 exposes that snapshot through authenticated exact-Run
`GET /api/v1/runs/{run_id}/active-call`. Desktop polls only during an active
execution, replaces complete snapshots by monotonic revision, ignores stale
responses, retains the last safe text while the durable result finalizes, and
uses the returned attempt identifiers for idempotent cancellation. A restart
does not restore provisional text. This is public assistant prose, not private
chain-of-thought, provider thinking, hidden Prompt, or Tool payload. ADR 0092
is authoritative.

P13-B3 exercised the configured real `deepseek/deepseek-v4-flash` route. An
isolated Run received five streaming events, performed one strict root repair,
used 771 tokens, invoked no tools, completed, and persisted the final assistant
message to its exact Session. No credential or raw provider payload was copied
into documentation. Focused Go application/HTTP tests, vet, 51 Web files with
189 assertions, strict TypeScript, deterministic OpenAPI (85 paths / 93
operations / 207 schemas), Vite production build, and Desktop packaging pass.
This is the first three slices of a new six-slice cycle: do not repeat the full
repository/race/WFP gate until the next three slices are complete.

The P13-B slice review corrected three implementation details: the Web client
now treats `stream_chunks` as a bounded provider-chunk count rather than the
32-event durable delta ceiling, completed polling delays remove their abort
listeners, and Go reparses cumulative root JSON at a 64-byte cadence with a
mandatory final scan. The final-scan regression is covered without another
paid Provider request.

### Previous Checkpoint: P10-L1 through P10-M3 / Schema v95

P10-L1 executes only the build-embedded, provenance-pinned Rust
`wasm32-wasip1` fixture through a fresh `wazero v1.12.0` Interpreter runtime.
It uses bounded in-memory stdin/stdout, deterministic random bytes, a synthetic
argv, an empty environment, context deadline/cancellation close, strict result
validation, and no filesystem, network, subprocess, native process, host path,
or caller-supplied module.

P10-L2/schema v94 issues a maximum-five-minute one-shot capability exact-bound
to Run, Workspace, request, candidate, and embedded module. Only the bearer
digest is durable; SQLite atomically consumes it once and rejects expiry,
replay, wrong bindings, mutation, and deletion. P10-L3/schema v95 then commits
the redacted execution record, metadata-only Artifact descriptor, capability
consumption, and Run events in one transaction. The Artifact body is only the
validated metadata-summary JSON; raw request/input, bearer, module bytes, file
contents, and guest stderr never enter the public receipt.

P10-M1 exposes only that fixed service through control-token HTTP/OpenAPI,
Desktop capability projection, CLI, and a bilingual React panel. P10-M2 adds a
default-Chinese `zh-CN|en-US` UI switch and verifies real model chat end to end:
Windows Credential Manager-backed provider reload, exact Harness qualification,
model route, durable Run/Session user message, one bounded Supervisor step, and
SQLite assistant-message readback through an Anthropic-compatible SSE server.

P10-M3 is complete. The portable package includes a hash-bound
`Start-Prayu-Operator-Preview.cmd` and bilingual `LOCAL-TEST-GUIDE.txt`; the
launcher enables only the safe operator bundle and contains no danger-full,
Debug maximum, Full CDP, user-terminal, or wake-worker flag. An isolated
`CYBERAGENT_HOME` smoke created and migrated its store, kept the Desktop alive,
and closed only the exact process it launched.

The cumulative six-slice release gate moved from L3 to after M3 and is complete:
ordinary Go passed in 610.6 seconds under the explicit 20-minute local release
timeout; race passed in 503.5 seconds with zero races; vet, staticcheck,
govulncheck, modules, 50 Web files/182 tests, strict TypeScript/OpenAPI/Vite/npm,
Rust fmt/7+2 tests/clippy/RustSec/WASI release, reproducible Desktop packaging,
and isolated smoke all passed. Real Anthropic-compatible Provider chat passed
three consecutive production-path tests against deterministic local SSE. The
audit fixed a concurrent Store migration-ledger race and workspace-file read
TOCTOU. Do not repeat L1-M3, P10-J/K, their full gate, or WFP after compaction.

The 2026-08-08 manual Desktop acceptance follow-up is also complete. A clean
first launch now registers the Go-owned `default` Workspace, both composers use
Enter to send / Shift+Enter for a newline / IME-safe suppression without a blue
textarea outline, and Provider status distinguishes unconfigured from failed.
DeepSeek passed real diagnostic, two-call Harness qualification, and isolated
Session chat after disabling its default thinking mode and placing Anthropic
`tool_result` blocks before sibling text. MiMo was not configured locally, so no
MiMo network result was fabricated. Hosted CI subsequently disclosed new high
advisories in transitive `js-yaml 4.3.0` and `nanoid 3.3.16`; the lock now uses
`4.3.1` and `3.3.17`, and `npm audit --audit-level=high` reports zero findings.

### Earlier Browser Checkpoint: P11-C5/C6/C7 / Schema v91

P11-C5 adds a product-inert Safe Web Windows process adapter. A short-lived
`BrowserStartAuthorization` exact-binds the schema-v85 acceptance/review,
schema-v91 restricted permission, executable identity, Run/Workspace/Session,
Profile generation, target scope, budget, attempt, lease, and process-local
gates. Windows starts a fixed argument set without a Shell, suspended and
atomically Job-bound before resume; kill-on-close, process, memory, and runtime
limits cover the complete process tree.

P11-C6 materializes only the exact disposable Profile generation. Canonical
owner markers, process-local leases, Profile-local environment directories,
generation ancestry, quiescent release, quarantine/recheck, and exact-owned
cleanup reject personal profiles, foreign markers, indirect paths, active
generations, and replayed cleanup.

P11-C7 adds a closed literal-loopback CDP transport for one owned browser
context and target. It permits only exact-scope navigation, bounded DOM
metadata, and bounded PNG screenshots. Page text, `Runtime.evaluate`, Cookies,
bodies, request mutation/replay, arbitrary methods, and Full Debug CDP have no
path. Evidence is always untrusted. No CLI, HTTP, Desktop, Tool, Skill, or model
route exists, and tests use fake processes plus a local scripted WebSocket.
Because CDP Fetch interception is not an OS network sandbox, verified
OS/container network containment and recoverable runtime orchestration block
product activation. ADR 0083 is authoritative.

### Previous Checkpoint: P11-C4 / Schema v91 + Browser CDP Permission Ceilings

P11-C4A adds immutable `run_browser_cdp_permission.v1` snapshots and operation
replay. Every new or migrated Run starts at `restricted`, whose policy ceiling
contains exact-scope navigation, bounded DOM snapshots, and screenshots.
`full_debug` additionally includes request capture/mutation/replay, Cookie
access, and arbitrary CDP methods. SQLite and Go permanently keep transport,
browser-start, runtime, and capability authority false in both modes.

P11-C4B routes the same Go service through CLI, HTTP/OpenAPI, Desktop bootstrap,
and Run detail. Full mode requires current durable `debug` execution permission,
the ordinary CDP-control gate, a dedicated full-CDP process gate, and exact
operator confirmation. A persisted selection never restores process-local
authority. Models, Agents, Tools, Skills, repository/document/browser content
cannot choose either mode.

P11-C4C adds the two controls to `Settings > 权限`. Full mode is labelled
`完整 CDP（调试）` and visibly carries `高度敏感权限`; unavailable prerequisites
are explained in place. The UI also states that selection does not start a
browser. The generated contract is 83 paths / 91 operations / 203 schemas.
No browser process, Profile write, network connection, CDP transport, Cookie
read, request mutation, or CDP method was enabled. ADR 0082 is authoritative.

### Earlier Checkpoint: P12-E / Schema v90 + Gated Host Execution

P12-E1 keeps arbitrary one-shot commands separate from the schema-v89 fixed
diagnostics. `host_command.v1` freezes exact executable SHA-256, argv, cwd,
sanitized environment names/digest, explicit host-network intent, timeout, and
Run/Workspace/interaction/Profile/permission revisions. The Root Supervisor
may form only a non-authorizing `approval` proposal, and an independent
operator review is exact and single-use. This is currently a Go domain and
threat-model contract only: it has no persistence, model Tool, HTTP, Desktop,
or execution route.

P12-E2 advances SQLite to v90 and adds operator CLI `run host-execute` for an
exact trusted Code/Local/Controlled Run with durable `full_access`, current
permission-control/danger-full-access process gates, and two explicit
confirmations. Windows uses exact `CreateProcess`, PE/SHA pinning, sanitized
environment, closed stdin, creation-time Job Object, 32-process/2-GiB bounds,
bounded output, timeout/cancellation, and tree reap. Intent is durable before
start; receipt is metadata-only; environment values and raw output remain
transient; an uncertain intent is never retried automatically. This is
non-sandboxed current-user host filesystem and network access, and has no
HTTP, Desktop, or model invocation path.

P12-E3 adds a Go-only Debug Agent-input controller over an already-running
user ConPTY. A 15-second to 15-minute memory bearer binds the exact Workspace,
Run, terminal, interaction, Profile, and permission revisions. Every write is
one complete UTF-8 command line, runs permanent Policy, and writes only
metadata/digest audit events. Tokens and raw input never enter SQLite;
uncertain writes are not retried. Host lifecycle, Run/binding drift, terminal
replacement, expiry, and shutdown revoke the grant. The controller lives only
inside the Go Desktop `ControlPlane`; renderer, HTTP, Skills, repository
content, and models cannot grant or invoke it. ADR 0081 is authoritative.

P13-A remains the user-facing reasoning boundary. `run_activity.v1` separates
public model updates, operator input, and verifiable Go Harness events. It
never maps provider thinking, model deltas, raw payloads, prompts, Tool
arguments, or Tool output, and fixes `private_reasoning_included=false`.
`root_lifecycle.v1.message` is public progress/result prose, not private
chain-of-thought. ADR 0080 is authoritative.

Schema v89 fixed-command proposals remain the conservative model execution
path. The Root can request only `git-status`, `git-diff-check`, `go-version`,
or `powershell-workspace-list`; an independent operator review revalidates all
bindings before the v87 restricted runner executes once, and the bounded
result returns as `instruction_authorized=false` evidence. ADR 0079 is
authoritative.

Desktop D1-UX10 gives those controls one dedicated `权限` Settings page. Host
permission, CDP permission, interaction shape, and execution environment remain
separate Run-scoped values and continue through Go HTTP/OpenAPI services. With
no selected Run, the page is inert. UI state never substitutes for a current
Go authorization decision.

The Settings sidebar now reuses the accessible workbench resizer with the same
232/286/420 px minimum/default/maximum bounds. Windows Wails uses a native
Acrylic backdrop and transparent WebView instead of the old full-window ink
backgrounds. D1-UX11 adds a CSS/React Prayu app mark and persisted
light/dark/transparent-glass tokens. Idle controls are transparent, hover is
translucent, and selected navigation and segmented controls are opaque white;
the legacy orange-brush gradients, clipping paths, pseudo-elements, and image
wordmark are deleted. Windows remains free to use a more opaque fallback when
transparency is disabled or the window is maximized. Real-window verification
covered Acrylic wallpaper sampling, and headless desktop/mobile verification
covered all three themes, selected states, no pseudo-element brush, and no
390 px horizontal overflow. ADR 0078 remains authoritative.

## Resume First

Prayu is a local-first, resumable, auditable AI Agent workbench. Go is the only control plane. TypeScript consumes Go-owned HTTP/OpenAPI contracts, while the current Rust fixture implements deterministic digest and in-memory ZIP central-directory functions behind Go-owned JSON protocols without product invocation. The `cyberagent` CLI, Go module, data directory, environment, HTTP, credential, and selected Windows identifiers remain stable compatibility contracts. CTF-specific solving remains deferred until the generic runtime, Skills, Sandbox, and analyzer boundaries are stable.

Read in this order after a long context break:

1. `README.md`
2. This file
3. `docs/PROJECT_STATUS.md`
4. `docs/PROGRESS_BOOK.md`
5. `docs/TASK_BOOK.md`
6. `docs/adr/0001-go-control-plane.md`
7. `docs/adr/0002-run-centric-runtime.md`
8. `docs/adr/0003-run-execution-modes.md`
9. `docs/adr/0004-plan-delivery-workflow.md`
10. `docs/adr/0005-operator-steering-queue.md`
11. `docs/adr/0006-operator-steering-controls.md`
12. `docs/adr/0007-specialist-skill-context.md`
13. `docs/adr/0008-sandbox-manifest-boundary.md`
14. `docs/adr/0009-sandbox-approval-candidate.md`
15. `docs/adr/0010-disabled-sandbox-lifecycle.md`
16. `docs/adr/0011-disabled-sandbox-preflight.md`
17. `docs/adr/0012-simulation-only-sandbox-evidence.md`
18. `docs/adr/0013-read-only-docker-observation.md`
19. `docs/adr/0014-deterministic-docker-container-plan.md`
20. `docs/adr/0015-bounded-docker-write-rehearsal.md`
21. `docs/adr/0016-recoverable-docker-rehearsal-attempt.md`
22. `docs/adr/0017-descriptor-sealed-host-input-staging.md`
23. `docs/adr/0018-durable-pre-stage-host-input-requirement.md`
24. `docs/adr/0019-daemon-owned-host-input-handoff.md`
25. `docs/adr/0020-deterministic-runtime-input-projection.md`
26. `docs/adr/0021-recoverable-runtime-input-application.md`
27. `docs/adr/0022-retained-runtime-input-resource-lifecycle.md`
28. `docs/adr/0023-blocked-docker-start-gate-review.md`
29. `docs/adr/0024-strict-inert-skill-package.md`
30. `docs/adr/0025-protected-delete-command-guard.md`
31. `docs/adr/0026-run-execution-profile-selection.md`
32. `docs/adr/0027-non-authorizing-docker-production-evidence-ledger.md`
33. `docs/adr/0028-recoverable-docker-production-evidence-attempts.md`
34. `docs/adr/0029-bounded-linux-read-only-docker-evidence-harness.md`
35. `docs/adr/0030-immutable-docker-production-evidence-review.md`
36. `docs/adr/0031-content-addressed-inert-skill-registry.md`
37. `docs/adr/0032-external-skill-run-context.md`
38. `docs/adr/0033-pathless-desktop-skill-preview.md`
39. `docs/adr/0034-embedded-read-first-wails-shell.md`
40. `docs/adr/0035-desktop-lifecycle-and-event-resumption.md`
41. `docs/adr/0036-idempotent-controlled-run-creation.md`
42. `docs/adr/0037-controlled-session-message-submission.md`
43. `docs/adr/0038-idempotent-run-control-and-bounded-handoff.md`
44. `docs/adr/0039-model-plan-and-approval-controls.md`
45. `docs/adr/0040-provider-diff-wake-controls.md`
46. `docs/adr/0041-explicit-wake-file-apply-and-inert-skill-install.md`
47. `docs/adr/0042-receipts-explorer-portable-build.md`
48. `docs/adr/0043-workspace-search-evidence-attachment-receipt-history.md`
49. `docs/adr/0044-operator-action-center-evidence-inventory-command-palette.md`
50. `docs/adr/0045-go-issued-editor-system-credentials-bounded-wake-worker.md`
51. `docs/adr/0046-safe-editor-recovery-provider-generation-worker-health.md`
52. `docs/adr/0047-read-only-repository-change-set-code-journey.md`
53. `docs/adr/0048-bounded-diff-verification-code-handoff.md`
54. `docs/adr/0049-deadlock-livelock-runtime-guards.md`
55. `docs/adr/0050-repository-history-verification-plan-handoff-export.md`
56. `docs/adr/0051-exact-commit-verification-association-runner-lifecycle.md`
57. `docs/adr/0052-conservative-model-context-cumulative-handoff-memory.md`
58. `docs/adr/0053-commit-preview-handoff-coverage-process-conformance.md`
59. `docs/adr/0054-file-history-verification-drilldown-runner-exit-evidence.md`
60. `docs/adr/0055-history-navigation-verification-pagination-runner-runtime-evidence.md`
61. `docs/adr/0056-exact-commit-comparison-keyset-verification-runner-control-evidence.md`
62. `docs/adr/0057-comparison-preview-verification-snapshot-runner-timeline-evidence.md`
63. `docs/adr/0058-paired-comparison-snapshot-receipts-runner-evidence-set.md`
64. `docs/adr/0059-paired-navigation-receipt-review-runner-golden-vectors.md`
65. `docs/adr/0060-keyboard-paired-preview-handoff-reviews-receipt-compatibility.md`
66. `docs/adr/0061-exact-receipt-review-navigation-audit-facts-envelope-golden.md`
67. `docs/adr/0062-go-owned-analyzer-protocol-rust-fixture-shared-vectors.md`
68. `docs/adr/0063-inert-analyzer-registry-zip-inventory-shared-vectors.md`
69. `docs/adr/0064-prayu-brand-and-dual-surface-desktop-shell.md`
70. `docs/adr/0065-non-starting-analyzer-invocation-bridge.md`
71. `docs/adr/0066-test-only-analyzer-subprocess-conformance.md`
72. `docs/adr/0067-inert-analyzer-result-staging-and-product-adapter-threat-model.md`
73. `docs/adr/0068-real-wails-startup-and-migration-compatibility.md`
74. `docs/adr/0069-go-owned-browser-profiles-target-scope-and-session-plan.md`
75. `docs/adr/0070-frameless-workbench-resizable-sidebar-agent-composer.md`
76. `docs/adr/0071-inert-browser-executable-profile-lifecycle-and-sealed-cdp.md`
77. `docs/adr/0072-workbench-docks-and-operator-confirmed-workspace-opening.md`
78. `docs/adr/0073-browser-publisher-launch-lease-and-review-gates.md`
79. `docs/adr/0074-model-harness-protocol-profiles-and-qualification.md`
80. `docs/adr/0075-execution-interaction-trust-model.md`
81. `docs/adr/0076-controlled-windows-execution-and-user-terminal.md`
82. `docs/adr/0077-four-level-run-execution-permissions.md`
83. `docs/adr/0078-desktop-permission-center-and-native-acrylic.md`
84. `docs/adr/0079-review-gated-fixed-command-proposals.md`
85. `docs/adr/0080-public-model-updates-and-harness-activity.md`
86. `docs/adr/0081-gated-host-execution-and-debug-terminal-input.md`
87. `docs/adr/0082-browser-cdp-permission-ceilings.md`
88. `docs/adr/0083-restricted-loopback-browser-runtime-core.md`
89. `docs/adr/0084-windows-wfp-browser-containment-and-runtime-lifecycle.md`
90. `docs/adr/0085-analyzer-format-release-and-launch-plan-candidates.md`
91. `docs/adr/0086-analyzer-signed-provenance-scope-approval-and-test-sandbox-conformance.md`
92. `docs/adr/0087-analyzer-immutable-handoff-low-privilege-and-filesystem-conformance.md`
93. `docs/adr/0088-analyzer-product-admission-authenticated-capability-and-recovery-acceptance.md`
94. `docs/adr/0089-analyzer-durable-request-intent-and-recovery-ledger.md`
95. `docs/adr/0090-embedded-wasi-analyzer-isolation-candidate.md`
96. `docs/adr/0091-embedded-analyzer-product-route-bilingual-desktop-preview.md`
97. `docs/adr/0092-safe-public-model-streaming-and-desktop-convergence.md`
98. `docs/adr/0093-review-gated-approval-mode-host-command-proposals.md`
99. `docs/adr/0094-liquid-glass-workbench-zoning.md`
100. `docs/adr/0095-non-authorizing-real-docker-lifecycle-probe.md`
101. `docs/adr/0096-durable-docker-lifecycle-ownership-and-recovery.md`
102. `docs/DESKTOP_PLAN.md`
103. `docs/SKILL_PACKAGE_PLAN.md`
104. `docs/adr/0097-macos-desktop-portable-build.md`
105. `docs/adr/0098-bounded-docker-container-io-contract.md`

## Current Baseline

- Architecture completion: about 99%; the V2 run-centric control plane is about 99% complete.
- Product usability: about 98% for the complete Code + Cyber product.
- Generic coding-agent workflow usability: about 98%.
- Cyber autonomous-workflow usability: about 20%.
- These are engineering estimates based on tested roadmap slices, not performance benchmarks. Do not reuse the retired single-axis "overall product vision" percentage.
- Database schema: v118.
- The canonical bilingual schema timeline moved from `README.md` to [`development-history.md`](development-history.md) on 2026-09-21. `TestSchemaHistoryListsEveryVersionInOrder` in `internal/store/readme_history_test.go` still binds its row count and ordering to `LatestSchemaVersion`, so a future migration cannot silently leave the public history missing or out of sequence.
- Main languages: Go control plane, TypeScript React/Vite local console, and deterministic Rust 1.97.1 digest/ZIP protocol functions. Rust has no Agent, LLM, config, key, persistence, network, filesystem, subprocess, or product-lifecycle ownership.
- Analyzer status: P10-A through P10-K define and validate the Go/Rust protocol and embedded-WASI boundary. P10-L/schema v94-v95 adds real fixed-module execution, one-shot exact-bound authorization, atomic consumption, redacted execution, metadata-only Artifact content, and Run events. P10-M exposes only this embedded module through CLI/control-token HTTP/Desktop/React; callers cannot provide WebAssembly, imports, mount, network, command, argv, environment, or native process. See ADR 0062, ADR 0063, ADR 0090, ADR 0091, and `analyzers/README.md`.
- Model Harness status: non-schema A1/A2/A3 adds `model_harness.v1` exact transport/tool/JSON/streaming profiles, Go preflight for Root/Specialist/read-only Fan-out, and `model_harness_qualification.v1` at-most-two-call synthetic qualification. Mock is trusted offline; Anthropic-compatible models require explicit qualification. Qualification stores only exact binding digest, capability booleans, and seven-day expiry in existing Provider settings; the synthetic Tool is never executed, availability remains no-probe, and qualification grants no Tool/Shell/file/browser/Docker authority. P13-A adds the durable public-activity read projection; P13-B adds a separate process-local safe public assistant stream with exact cancellation and no raw Provider persistence; P13-C adds the Root-only, independently reviewed `approval` host-command proposal Tool without granting the model execution authority. P10-M2 proves the production Anthropic-compatible route and durable chat path against deterministic local SSE, while P13-B3 verifies one configured real DeepSeek path. Current OpenAPI is 88 paths / 96 operations / 212 schemas. See ADR 0074, ADR 0080, ADR 0091, ADR 0092, and ADR 0093.
- Browser status: P11-A1 through P11-C3 fix three Profiles, exact target scope, inert plans, fixed-location discovery, disposable-profile recovery plans, sealed Disabled/Fake CDP, same-handle Authenticode acceptance, immutable launch attempts/leases, and independent review. P11-C4A-C4C adds independent `restricted|full_debug` CDP policy ceilings and operator controls. P11-C5-C7 adds a concrete Safe Web Windows process adapter, exact disposable-Profile lifecycle, and closed literal-loopback restricted CDP transport, but keeps them product-inert. There is no CLI/HTTP/Desktop/Tool/Skill/model route or interactive browser, and Full Debug CDP remains unavailable. Verified OS/container network containment blocks activation. See ADR 0069, ADR 0071, ADR 0073, ADR 0082, and ADR 0083.
- Desktop status: the Wails v2.13.0 shell retains its embedded React/in-process Go API, Run/editor/repository/verification/Handoff/model/credential/wake workflows and composable docks. It now defaults to Chinese with a persistent Chinese/English switch under Settings > Personal > General. The safe `--operator-preview` launcher enables model credentials, Harness qualification, routes, Run/Session chat, safe provisional assistant streaming with exact cancellation, approvals, file proposals, and the fixed Analyzer without enabling danger-full-access, maximum Debug, Full CDP, Agent terminal input, or Wake Worker. D0-Mac adds a macOS portable build: the darwin desktop tag compiles the same control plane over bundled WKWebView, workspace launchers run only through fixed /usr/bin/open, and build-desktop-darwin.sh emits an ad-hoc-signed un-notarized Prayu.app with reproducible metadata; Windows-only credential/terminal/browser seams fail closed on macOS. Windows 10/WebView2/scaling coverage, code signing, and installer remain pending, so `release_ready=false`; the local operator-preview chat path is enabled. See ADR 0091, ADR 0092, ADR 0097, and `docs/DESKTOP_PLAN.md`.
- Prayu UX status: D1-UX1 through D1-UX11 introduce the Prayu identity, frameless titlebar, bounded resizable workbench and Settings sidebars, Go-backed composer, four-control toolbar, dedicated permission center, native Acrylic, a CSS/React app mark, and three persisted appearance choices. Full-window ink backgrounds, screenshot-based selected overlays, the image wordmark, and orange-brush CSS are no longer used; selected navigation and segmented controls are opaque-white CSS rounded surfaces. Summary/Review/Files/Side Tasks use existing bounded Go surfaces. Browser remains inert; Terminal becomes real only when the Desktop is explicitly started with `--enable-user-terminal`, and remains user-owned. Open Workspace stays operator-confirmed and pathless at the renderer boundary. Stable CyberAgent compatibility identifiers remain unchanged. See ADR 0064, ADR 0070, ADR 0072, ADR 0076, and ADR 0078.
- Execution status: P12-A through P12-E define interaction intent, controlled Windows commands, user terminal, four host-permission levels, review-gated fixed commands, a dual-confirmed operator host executor, and a Go-only Debug terminal-input controller through schema v90. P11-C4 advances to v91 for CDP policy ceilings while C5-C7 remains a separate product-inert browser core. P10-L advances to v95 only for the fixed embedded Analyzer. P13-C/schema v96 adds a distinct Root-proposed, independently approved, exactly-once non-shell host-command path only for `approval`; it remains non-sandboxed, default-off, one-shot, and without automatic retry or persistent terminal authority. See ADR 0075 through ADR 0083, ADR 0091, and ADR 0093.
- P12-E release gate: completed. The uncached serial Go suite passed in 576.3 seconds and the full race suite passed in 717.6 seconds with no races. Vet, zero-warning staticcheck, govulncheck (zero reachable vulnerabilities), module verify/tidy, secure Desktop tags, 48-file/165-test React, strict TypeScript, deterministic OpenAPI, Vite, npm audit, Rust format/7+2 tests/clippy, privacy/authority scans, and reproducible Windows Desktop build all passed. The portable executable is 42,757,120 bytes with SHA-256 `801bda9b5343b72999827beeb3bfecd6fdd907b9795736f540637b77a26cb771`; `release_ready=false` remains correct. A separate opt-in adapter test launched only the current Go test binary by exact path/SHA and passed ordinary plus race execution. No arbitrary user program or product `host-execute` command was run during verification.
- Custom Skill status: the five embedded `skill.v1` guides and explicitly selected external packages are Run-loadable through separate protocols. Schema v69 adds persistent content-addressed import/history; schema v70 adds a second explicitly confirmed exact Run selection and redacted user-role root/Specialist context; schema v71 adds bounded read-only provenance across HTTP/TUI/Web. D1-A adds a pathless, one-time-handle preview boundary; D1-B1 adds explicit HTTP/Desktop registration through the same inert Registry. External packages remain untrusted and grant no declared tools. Installation executes no content and still does not select a package for a Run. See ADR 0024, ADR 0031 through ADR 0033, ADR 0041, and `docs/SKILL_PACKAGE_PLAN.md`.
- Protected-delete status: explicit recursive, absolute/traversing/wildcard, environment-derived, command-substituted, current-home, PowerShell/`cmd`, and common interpreter deletion intents are permanently denied before approval across Shell, ScriptProcess, and Sandbox Policy. This is defense in depth; Local/container process execution remains disabled and a future executor still requires OS/container isolation. See ADR 0025.
- Canonical branch: `main`; do not create a branch or PR unless the user asks.
- Canonical remote: `Qiyuanqiii/CTF-CyberAgent-Workbench`.

Implemented foundations include resumable RunSupervisor turns, SQLite checkpoints and execution leases, model streaming/retry/cancellation, one Go Provider Registry with persisted routes, redacted availability, and environment/system-credential sources, WorkItems/Notes/context compaction, Tool Gateway and durable approvals, source-bound Artifacts, a stable root Agent, review-gated two-child Specialist scheduling, parent-selected minimal Specialist Skill context, separate 1/2/4/6 read-only Fan-out, immutable Finding/Evidence/Report lifecycles, SARIF/CI output, Go-owned Code/Cyber plus Plan/Deliver modes, strict three-direction Plan proposals with explicit operator selection and separate Deliver transition, safe-boundary operator steering with pending-only cancellation and explicit drain, controlled HTTP/Desktop submission into that existing queue, constrained metadata-only approval decisions, embedded Skills, strict external-package validation, a content-addressed inert user Skill Registry, explicitly confirmed external-Skill Run pinning with redacted root/Specialist delivery, pathless Desktop Skill preview plus confirmed inert registration, Go-issued Monaco FileEdit proposals followed by independently authorized review/apply, read-only Git repository state and bounded multi-file summaries, a Code-only Journey over existing capabilities, explicit foreground wake consumption plus a default-off 1 x 1-step worker, a bounded operator action center, metadata-only attached-evidence inventory, navigation/refresh-only command palette, a Windows Wails shell with embedded React, an in-process Go API, same-database lifecycle recovery, shared SSE/poll high-water cursors, fail-closed WebView2/origin handling, and idempotent closed Run creation, plus the existing non-authorizing Sandbox/Docker evidence architecture, loopback API/SSE/OpenAPI, Headless NDJSON, Run-first Bubble Tea TUI, and React/Vite console.

Schema v73 and non-schema D1-S2 additionally complete independent pending-only HTTP/Desktop steering cancellation, digest-idempotent Run start/pause/resume, and an at-most-eight-item frozen execution handoff through the existing RunSupervisor. These controls add no Desktop-native worker and no Local/Docker/Shell process authority; ADR 0038 records the boundary.

Non-schema D1-M1/P1/A1 adds one no-probe redacted Provider/model Registry projection, separate Plan-selection and Deliver controls, and a bounded metadata-only durable approval queue. Approval rechecks Policy and remains dry-run/process-disabled; it cannot create a Grant, write a file, or start Shell/Local/Docker. OpenAPI is 36 paths/84 schemas/27 GET/11 control POST. ADR 0039 records the boundary.

Non-schema D1-M2/D1-D1 and schema-v74 D1-Q1 add explicit content-free Provider diagnostics plus persist-before-memory route selection, exact body-free Diff review without apply authority, and durable bounded wake/retry intent with one generation-fenced owner. The wake API manages intent only and starts no background worker, Run, model, tool, or process. ADR 0040 records the boundary.

Schema-v75 D1-Q2, schema-v76 D1-D2, and non-schema D1-B1 add one explicit foreground wake consumer, a separately authorized current-hash/Policy-checked FileEdit apply, and confirmed HTTP/Desktop import into the inert Skill Registry. D1-U1/E1/W1 then add durable receipts, bounded Workspace exploration, and reproducible portable diagnostics. Schema-v77 D1-E2/C1/U2 adds bounded search, separately gated non-authorizing evidence attachment, and refreshable terminal receipt history. Non-schema D1-O1/C2/K1 adds a bounded operator action center, metadata-only attached-evidence inventory, and navigation/refresh-only command palette. These add no hidden worker, renderer host-path/body input, document authority, install-time execution, or general host/container process authority. ADR 0041 through ADR 0044 record the boundaries.

Non-schema D1-I1/M3/J1 adds a locally bundled Monaco proposal/Diff editor over a five-minute Go source handle, Windows Credential Manager-backed Provider setup with status-only UI responses, and a default-off process-lifetime wake worker capped at one due intent and one Supervisor step per tick. Proposal/review/apply and model/credential/wake capabilities remain independent. No renderer host path, credential readback, Tool Runner, Shell, LocalRunner, or Docker authority is added. ADR 0045 records the boundaries.

Non-schema D1-I2/M4/J2 adds hash-bound source-handle rotation and read-only durable
proposal recovery, complete generation-safe Provider Registry reload, and authenticated
metadata-only capability plus worker-health projection. Stale editor content is never
rebased, candidate failure preserves the serving Registry, active calls retain their
captured Provider, and worker health cannot enable a process or service. ADR 0046
records the boundaries.

Non-schema D1-U1/E1/W1 adds `operation_receipt.v1` for apply/wake/install,
hash-and-age constrained FileEdit staging recovery, and read-bearer
`workspace_explorer.v1` with canonical Go path resolution, bounded redacted UTF-8, and
non-authorizing provenance. React can navigate only Go-issued exact child paths and
renders file bodies as plain text. `cyberagent doctor portable` plus the Windows build
scripts provide deterministic release metadata, PE/COFF/module checks, and consecutive
binary hash verification. The tested double-build SHA-256 is
`33fb9ca3064df98191ac50b2a3ef9431e1b5c81abe8c610d4be15db113cdf1ef`.
Automated checks pass, while the manual Windows 10/WebView2 matrix correctly keeps
`release_ready=false`. ADR 0042 records the boundary.

## Security Invariants

- Go owns Policy, scope, budgets, state transitions, Docker/process control, API-key access, and file permissions.
- `model_availability.v2` is a deterministic no-probe projection. It adds only bounded Harness profiles/readiness; API keys, Base URLs, environment-variable names, clients, binding digests, prompts, model output, Tool arguments, and raw errors never enter it. Secret-like or malformed model/route identifiers fail closed or are redacted.
- `provider_diagnostic.v1` is explicit and content-free. Each invocation may make one bounded model request, but model text, secrets, endpoints, environment-variable names, clients, and raw errors never enter the result. Route persistence succeeds before the in-memory Router changes.
- `model_harness_qualification.v1` is a separate explicitly confirmed operation. It may make at most two bounded external model requests, dispatches no Tool, and returns no prompt/response/arguments/raw error. A persisted qualification is exact-binding, seven-day, and capability-only; changing Provider/model/Base URL/strategy or reaching expiry fails closed. Qualification does not prove model quality or grant execution authority.
- `provider_credential.v1` is status-only after mutation. Windows stores exact supported Provider keys in Credential Manager with a 2,560-byte ceiling; plaintext never enters SQLite, events, logs, model context, frontend persistence, or any response. Non-Windows platforms have no plaintext fallback and environment variables keep priority. Go atomically advances a generation only after the complete candidate Registry, persisted routes, and required credential reads succeed; otherwise the old generation remains active.
- File-edit review exact-binds Run/Mission/Session/Workspace/proposal/approval and returns metadata plus bounded redacted Diff only. `approve_intent` never writes a file. Schema v76 apply is a separate capability that rechecks Policy, Workspace resolution, original/current hash, target hash, and idempotent result; browsers submit neither path nor body.
- `file_edit_proposal.v1` accepts only a five-minute opaque handle and replacement text after Go has issued complete, untruncated, unredacted UTF-8 for the exact running Run/active Session/Workspace. The handle is one-intent, current hash and Policy are rechecked, and the result remains pending without a file write.
- `file_edit_proposal_recovery.v1` is read-only. Handle rotation requires the previous SHA-256 to match the current Workspace file and carries no renderer draft; durable recovery rechecks pending status, bindings, bodies, and hashes, then returns no source handle and fixes `editable=false`.
- `operation_receipt.v1` is a content-free projection of an already durable apply/wake/install result. Go owns the outcome/replay/retry/cleanup tuple; TypeScript cross-checks it against the enclosing response and cannot invent recovery authority. Staging cleanup is restricted by exact directory, reserved prefix, age, ordinary-file identity, size, and approved content hash.
- `workspace_explorer.v1` treats repository content as evidence rather than authority. Go alone resolves the registered root and canonical relative path, refuses links/redirects/traversal/ambiguous names, redacts and bounds UTF-8, and emits `instruction_authorized=false`. The DTO never includes the root; React cannot submit an arbitrary host path or execute Markdown/HTML from file content.
- `workspace_search.v1` searches only bounded redacted Explorer projections. It has fixed directory/entry/file/result/read ceilings, no indexer or watcher, and returns canonical references plus false-authority provenance only.
- `session_evidence_attachment.v1` exact-binds a reprojected Workspace file to the running/paused Run and active Session. Go and SQLite require a tool-role message with `instruction_authorized=false`; document text cannot become an operator instruction, approval, Scope change, or capability grant.
- `operation_receipt_history.v1` is a bounded terminal projection with opaque IDs. It exposes no operation key/digest, path/content hash, requester, archive metadata, or private lease; FileEdit staging inspection is read-only and uncertainty remains `pending_review`.
- Schema v74 wake intent is not authority by itself. Schema v75 can consume one due intent through the existing bounded RunSupervisor handoff. D1-J1 optionally automates that same consumer only after an explicit process startup flag and control token; one serial owner may consume at most one intent and one step per tick, with no Tool Runner or Shell/Local/Docker dependency. D1-J2 health is metadata-only, omits private identity/error state, serializes public `RunOnce`, and cannot enable the worker or install a service.
- Plan direction selection and Deliver transition are separate operator operations. Neither can start/resume execution, call a model/tool, acquire a lease, or grant capability.
- Desktop/Web approval can only deny or approve-once under a fresh Policy check. Shell is dry-run, ScriptProcess is process-disabled, replace-file is deny-only, permanent denial is non-overridable, and no Session Grant, file write, or real process can result.
- Core Specialist delegation is capped at two children and requires explicit operator review, application, and scheduling.
- A Specialist receives at most one parent-selected built-in Skill guide. Assignment text, model output, and external content cannot choose or widen that subset.
- `skill_package.v1` is accepted only as bounded untrusted input to a pure in-memory validator. Schema v69 may persist an explicitly confirmed validated archive and metadata, but import never selects, executes, injects context, calls a Provider/network/tool, or grants declared dependencies. Object reads revalidate archive and semantic identity; every authority bit remains false.
- Schema v69 stores external archives by SHA-256 behind non-executing write/verify and read-only loader interfaces plus immutable installation/result/removal ledgers. Code and Cyber catalogs are separate, Cyber accepts only `script`, built-in names are reserved, and removal retains bytes. See ADR 0031.
- Schema v70 requires a second explicit confirmation to pin one to four exact active packages to a created Run. At most one item is operator-designated for Specialist delivery. Every load revalidates the exact object and Manifest, redacts secrets, obeys separate root/child budgets, and appears only in a user-role untrusted-guidance envelope. SQLite/events store metadata only; first-call commits are atomic; Policy/tool/Shell/network/secret/scope/delegation authority remains false. Pinned installations cannot be removed in Go or SQL. See ADR 0032.
- Desktop exposes exactly four Wails-bound methods: connection bootstrap, native Skill selection, pathless preview, and handle-only inert installation. All other controls, including Provider credentials, FileEdit proposal/recovery, capability discovery, and wake automation/health, remain in the in-process Go HTTP Handler. It opens no TCP listener; embeds one validated production renderer; keeps tokens, retry keys, confirmation/source handles, transient password input, and its bounded 16-Run/500-frame event cache only in memory; defaults to read-only; and accepts no renderer host path or archive bytes. Independent flags gate every control class. The optional worker is process-lifetime and 1 x 1-step only; no capability creates a Grant, install hook, or general host/container process authority. See ADR 0034 through ADR 0046.
- The 1/2/4/6 Fan-out pool is separate, read-only, tool-free, network-free, write-free, and creates no Agent.
- Dangerous cyber commands remain permanently denied; approval cannot override permanent Policy denial.
- Protected or unresolved deletion through executable Shell/ScriptProcess/Sandbox intent is a critical permanent denial. Non-executable evidence is not reclassified as a command, and passing the classifier can never authorize host execution.
- External files, repository text, logs, web/mail, tool output, and memory are untrusted evidence with `instruction_authorized=false`; they never become system/assistant authority through persistence or compaction.
- Shell and ScriptProcess approval paths are dry-run only. Real Local and container-process command execution is disabled.
- `run_execution_profile.v1` records only operator intent. Every profile fixes `process_enabled=false`, `execution_authorized=false`, and `capability_grant=false`; Docker still requires its production start gate and Local still requires an unimplemented OS-sandbox gate. Selection is allowed only for `created` or quiescent `paused` Runs and cannot be widened by TypeScript, a model, a child Agent, or approval.
- `sandbox_docker_production_evidence.v1` accepts no caller-supplied conclusion, report, endpoint, socket, path, image, resource, container identity, or raw daemon response. Windows and Linux without explicit opt-in never contact a daemon. The v67 Linux harness may produce `capture_complete` only through its durable read-only protocol; all sixteen items remain insufficient and every start/process/export/Artifact authority bit remains false.
- Schema v66 commits an immutable production-evidence attempt, digest-only operation, and generation lease before collector invocation, then requires a current-generation quiescent reconciliation checkpoint. Failure and completion are fenced to the full private lease identity; released/expired attempts resume only at generation N+1. The checkpoint records zero daemon reads/resources and is not production resource verification. SQL rejects new v65 evidence operations without an attempt result. Legacy evidence gets no fabricated attempt, CLI omits lease IDs/owners, and all daemon/start/process/export/Artifact authority remains false. See ADR 0028.
- Schema v67 requires a second immutable harness intent and a daemon-aware empty-scope reconciliation before four capture GETs. The fixed Linux local transport performs exactly one labeled container-list GET plus `_ping`, `version`, `info`, and exact pre-existing digest inspect, each with a four-second bound and no pull/mutation method. Its result fixes all sixteen checks to `observed_failed`, `production_verified_count=0`, and zero start/process/output/Artifact authority. A persisted intent cannot downgrade to the v66 inert result, restart must reconcile under the current generation, and CLI/events persist no resource ID, socket, payload, path, or private lease identity. See ADR 0029.
- Schema v68 records one explicit `accepted|rejected` decision for an exact completed v67 receipt. Acceptance uses only `metadata_scope_accepted`; rejection uses one of five bounded reason codes, with no free-form body. The operation/review pair is atomic and immutable, migration fabricates no decisions, and both Go and SQL preserve zero production verification, sixteen blockers, and false start/process/output/Artifact authority. The review path has no Docker or process dependency. See ADR 0030.
- `sandbox_manifest.v1`, `sandbox_execution_candidate.v1`, `sandbox_execution.v1`, `sandbox_preflight.v1`, `sandbox_backend_evidence.v1`, `sandbox_output_simulation.v1`, `sandbox_docker_observation.v1`, `sandbox_docker_container_plan.v1`, `sandbox_docker_container_rehearsal.v1`, `sandbox_docker_container_rehearsal_attempt.v1`, `sandbox_docker_host_input_staging.v1`, `sandbox_docker_host_input_requirement.v1`, the v59 handoff, v60 projection plan, v61 runtime-input application, v62 retained-resource lifecycle, and v63 start-gate review are evidence, preparation, cleanup, or design facts, never process-execution permits. Schemas v48-v63 fix execution, start, export, and production Artifact-commit capabilities to false even after exact operator approval.
- Sandbox execution ownership uses a separate generation-fenced lease. The initial lease can only prepare a disabled record; cleanup can recover after Run termination, but stale generations cannot commit.
- Input Artifacts are reverified by exact Run/Session/Workspace, digest, size, MIME, source, stream, order, and a 16 MiB aggregate cap. v50 stores no Artifact body or raw output path.
- The v51 backend handshake is disabled, container identity is unbound, and all 16 threat-model checks remain required/unverified/not-probed. Output slots store only opaque locator fingerprints and cannot export or commit Artifacts.
- The v52 fake client never contacts Docker. Its 16 `simulated_pass` items remain unverified and production-untrusted; the output harness commits only to an in-memory fake and must leave `run_artifacts` unchanged.
- The v53 Docker observer exposes only fixed-endpoint GET operations. It has no create/start/run/exec/pull/remove method, ignores `DOCKER_HOST`, stores no raw daemon/socket/repository identity, and cannot turn metadata observation into production verification or execution authority. Private-mount support remains explicitly unobservable through this read-only protocol.
- The v54 compiler emits a full container specification only in memory and persists metadata-only controls and fake steps. Its fake writer has no daemon transport; success, failure, crash, and cancellation all leave real containers and production Artifacts untouched. `compiled_not_applied` is not production verification.
- The v55 writer is a separate default-disabled transport fixed to the Linux local Unix socket and Docker API `1.40`. Its closed allowlist permits exact image/container inspection, create, and non-forced delete with fixed anonymous-volume cleanup. The image RepoDigest must match and declare no `VOLUME` before create. Its first profile is network-, environment-, and secret-free; it never starts a container, pulls an image, exports output, or grants backend/execution/Artifact authority. Raw container IDs and host paths remain transient, semantic replay does not contact Docker, and cancellation/uncertain-create cleanup re-inspects under an independent bounded context before deleting only an exact authority match.
- The v56 attempt is durable before daemon mutation and fenced by an expiring monotonically generated SQLite lease. Stage can create once or adopt only an exact stopped authority match, then freezes 19 configuration controls with `execution_evidence=false`. Cleanup deletes only the exact request/configuration/authority/container-ID-fingerprint match or accepts absence. Stale generations fail closed, failure codes are bounded and append-only, attempt-ID resume requires full Manifest resubmission and fresh confirmation, and the raw operation key is not required or exposed. Image and container environments must both be empty.
- The v57 host-input intent is recorded after the v56 stopped-container stage and before cleanup. Linux uses `openat2` no-symlink/no-magic-link/beneath/no-cross-device resolution and `O_PATH` special-file preflight, supports directory and single-file mounts, rechecks descriptor identity and metadata, writes a deterministic sanitized tar to `memfd`, applies write/grow/shrink/seal kernel seals, and rereads the bundle for digest verification. SQLite blocks completion while an intent is pending and retains metadata only. The bundle is not passed to Docker, so `daemon_consumed=false` and `execution_evidence=false`; v57 closes descriptor-capture replacement but does not yet prove daemon consumption or process isolation.
- The v59 handoff is default-disabled and requires daemon-write, capture, and handoff confirmation. It uses only fixed API `1.40` archive/volume/container operations, a deterministic local-volume carrier, fixed `/cyberagent-input/bundle.tar`, exact daemon readback, a final read-only never-started target check, and complete resource deletion. User mounts cannot overlap the reserved destination. Retry removes only exact owned residue, while foreign collisions fail closed. Durable evidence grants no start, exec, output, backend, execution, or Artifact authority.
- The v60 projection plan requires a separately persisted operator confirmation and a completed v59 handoff. It recaptures the exact sealed input, accepts only byte-for-byte canonical v57 PAX tar, maps directory-root read-only mounts and fixed Artifact input in memory, and binds deterministic future volume identity to the handoff fingerprint. Tables/events/CLI retain no raw target, path, file name/content, volume name, or archive bytes. `compiled_not_applied` grants no daemon, start, exec, output, backend, execution, or Artifact authority.
- The v61 application requires separate operator and daemon-write confirmations plus a durable intent and independent generation lease before Docker mutation. It revalidates v48-v60 and recaptures the exact sealed input, then uses a fixed local-Unix allowlist to create deterministic volumes/carriers, upload only to `/cyberagent-input`, verify daemon readback, and attach every input read-only/`NoCopy` to one fully inspected never-started target. Retry and bounded cleanup touch only full authority matches; foreign collisions fail closed, stale generations cannot commit, and operations stop early enough to reserve cleanup before takeover. Durable output contains no paths, targets, file/resource names, raw IDs, archives, sockets, raw keys, or private lease identities. `volumes_applied_target_never_started` grants no start, exec, output, backend, execution, or Artifact authority.
- The v62 resource lifecycle requires an explicit read-only inspection before a separately dual-confirmed cleanup. Descriptor reconstruction revalidates v48-v61 but never recaptures the input bundle. Complete never-started/read-only/`NoCopy` evidence is true only when the exact target and every volume are present. Cleanup intent and generation lease commit before Docker access; all resources are preflighted before any DELETE, a foreign collision causes zero DELETE, the target is removed by inspected ID before exact volumes, and final inspection requires total absence. Failure release, takeover, stale-worker fencing, and semantic replay are durable. No names, IDs, paths, sockets, raw keys, or private leases persist, and `exact_owned_resources_absent` grants no start, exec, output, backend, execution, or Artifact authority.
- The v63 review requires completed v62 cleanup, a resupplied Manifest, a stable digest-only operation identity, and explicit design-review confirmation. It maps all sixteen v51 checks to fixed evidence classes, sources, blockers, and future gates, while every check remains unverified and insufficient. Its eleven-transition process blueprint requires generation-fenced single ownership, write-ahead state, fixed endpoint, bounded logs, wait, TERM/KILL escalation, cancellation fan-out, and orphan reconciliation, but every transition remains unimplemented and unauthorized. The only outcome is `blocked/deny_start`; the path has no daemon transport, input capture, process, output-export, or Artifact authority.
- The Web/Desktop UI is read-mostly. Its read and optional distinct control bearers remain in memory and never belong in URLs or browser storage. The control bearer may select a non-authorizing Run execution profile, create a closed schema-v72 Run, or, under a separately enabled capability, enqueue one bounded message for an exact Run-bound Session. It cannot start/resume/drain a Run, acquire a lease, call a model/tool, approve an action, cancel/reorder steering, start a process, or read API resources.
- Provider keys are read from process environment only and must never enter Git, SQLite, events, or logs.

## Completed Sandbox Slice History (Latest: v68)

Schema v57 adds a default-disabled host-input capture gate to the recoverable v56 never-started rehearsal. It requires separate operator confirmation, binds an immutable intent to the exact attempt, stopped-container fingerprint, plan, input digest, requester, and current lease generation, and makes SQLite completion depend on a matching result.

The Linux stager opens the absolute workspace root and every read-only mount with `openat2` no-symlink/no-magic-link/beneath/no-cross-device resolution. It preflights entries through `O_PATH`, reopens only matching ordinary files/directories, and therefore rejects FIFOs and other special files before a potentially blocking read. Directory and single-file mounts are both valid; hard links, traversal, excessive depth, entry limits, and byte limits fail closed. Once the whole tree is descriptor-pinned it rechecks device, inode, mode, link count, size, mtime, and ctime, then builds a deterministic sanitized tar with exact revalidated input Artifacts. Directory inode size is excluded from the content digest. The tar exists only in a sealable `memfd`; write/grow/shrink/seal bits are applied and the bundle is reread to verify its digest. Windows reports `staging_unsupported` before a container can be created.

Application verifies returned Artifact bytes and payload digest, stores only bounded counts/digests/security flags, and performs best-effort stopped-container cleanup before releasing the lease on staging failure. A later generation resumes a pending intent without another create, including when cleanup was already checkpointed. CLI adds the opt-in flags plus metadata-only list/show commands. Raw paths, file content, descriptors, raw container IDs, commands, environment values, secrets, sockets, operation keys, and private lease identities stay out of v57 tables and events.

Focused tests cover separate confirmation, default-disabled and unsupported behavior, deterministic replay, rename/replace/delete detection after pin, symlink/hard-link/FIFO rejection, single-file mounts, bounded directory enumeration, cancellation, report mismatch, cleanup-first failure, restart/takeover without a second create, stale-generation fencing, SQL completion gating, immutability, privacy, and v56-to-v57 migration. Final ordinary/race suites pass in 155.0s/168.1s; vet/staticcheck/module/govulncheck, 17 frontend tests, OpenAPI/build/npm audit, repository scans, isolated schema-v57 binary smoke, focused repetition, and Linux test-binary cross-compilation pass. GitHub Actions supplies the Linux runtime proof. The audit tightened root-parent symlink rejection, public report construction, Artifact byte/digest revalidation, stage-to-intent chronology, resource-limit errors, ambiguous confirmation, file-mount SQL/report constraints, filesystem-independent directory digests, special-file preflight, bounded/cancellable reads, independent-ID semantic convergence, and pre-acquire rejection of missing resume confirmation. No high/medium issue is currently known. The bundle is deliberately not passed to Docker, so this slice adds no execution usability and does not satisfy a future start gate.

GitHub Actions run `29396264276` passed commit `8719dff` with Go/Linux in 3m55s and TypeScript in 23s, providing the Linux runtime proof. The preceding run `29395980413` failed only because the single-file test fixture no longer covered its directory working path; the corrected mixed directory/file fixture now exercises the intended report constraint.

Schema v58 closes the v57 post-stage/pre-intent downgrade window for all new attempts. `sandbox_docker_host_input_requirement.v1` is created atomically with the v56 attempt, initial lease, and audit events before daemon stage. It binds the required/confirmed choice to attempt, plan, Run, Mission, Workspace, requester, digest-only operation identity, complete authority fingerprints, and bounded input counts. Generated row IDs and timestamps are excluded from its semantic fingerprint.

Recovery treats that durable choice as authoritative. Required attempts automatically resume v57 capture without repeating staging flags and cannot complete without matching evidence; false requirements cannot be widened. Go and SQLite independently enforce binding, immutability, false-to-staging rejection, and completion gating. Migration intentionally leaves legacy v57 attempts without a requirement because historical operator intent cannot be invented, but copies their IDs into an immutable compatibility set before new marker inserts are disabled. Every later stage/staging/completion must have a requirement or that migration marker. Tables, events, and CLI projections remain metadata-only. Focused tests cover migration, SQL mutation/deletion, privacy, completion gating, false widening, two-Store candidate convergence, completed and pending operation replay, generation-two crash recovery without flags, and CLI output.

The v58 audit rejected direct archive upload into the read-only target: Docker rejects archive writes to read-only rootfs/volumes, and weakening the target is outside authority. No archive, volume, start, exec, pull, build, export, or Artifact surface was added. The v57 bundle remains daemon-unconsumed and every production flag remains false. ADR 0018 reserves schema v59 for a separately audited daemon-owned carrier, exact upload/readback verification, carrier removal, and read-only final attachment.

Final local gates pass: full ordinary/race suites took 158.1s/168.4s; vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 17 tests in 8 frontend files, OpenAPI generation, production build, zero-vulnerability npm audit, repository privacy/artifact/process/encoding/Markdown scans, Linux sandbox test-binary cross-compilation, diff checks, and isolated schema-v58 real-binary workspace smoke are green. Domain requirement tests passed 50 repetitions, Store convergence/missing-requirement tests 30, Application pending-recovery/no-widen tests 20, and Store/Application race repetitions 10 each. The audit fixed pending operation-key recovery selecting a new candidate, unmatched explicit flags beside a durable requirement, direct-SQL post-migration attempts without requirements, and false-requirement zero-input compatibility. No unresolved high/medium issue is known. Linux real-daemon handoff evidence remains pending because this Windows host has no Docker.

GitHub Actions run `29400696276` passed feature commit `4b570f7` with Go/Linux in 2m39s and TypeScript in 23s.

Schema v59 completes the daemon-owned immutable host-input handoff gate. Every new attempt receives an immutable handoff requirement with its v58 capture requirement before daemon stage. A write-ahead intent binds the exact v57 bundle report/digest, attempt/plan, stopped-container fingerprint, active generation, requester, and full authority before archive or volume mutation. Required cleanup and completion are blocked until a matching immutable result exists; migrated v58 attempts keep explicit legacy compatibility without invented intent.

The fixed Linux transport wraps the sealed archive as `bundle.tar`, creates one deterministic daemon-owned local volume and never-started writable carrier, uploads only to `/cyberagent-input`, reads the file back through Docker, and verifies exact bytes and SHA-256. It removes the carrier and original stopped target, recreates and inspects the target with the volume read-only, then removes target and volume. Exact carrier/volume/final-target crash residue converges; foreign resources are protected. Early bundle/image failures clean only fingerprint-matched owned resources under an independent context. The reserved destination tree cannot overlap Manifest mounts. No start, exec, attach, pull, build, network mutation, export, forced volume delete, arbitrary endpoint/path, or Artifact writer exists.

Focused sandbox, Store, Application, and CLI tests cover fixed endpoint/path allowlists, successful cleanup, exact crash-residue convergence, foreign-volume protection, invalid-bundle early cleanup, destination overlap, four confirmations, sealed-handle closure, metadata privacy, write-ahead intent, generation-two retry, immutability, migration, and cleanup/completion gates. The Linux sandbox test binary cross-compiles with the new opt-in real-daemon handoff harness. This Windows host cannot execute that harness, so no Linux real-daemon runtime claim is made locally. Final ordinary/race suites passed in 183.1s/185.1s, and GitHub Actions run `29406403201` passed feature commit `fb1daca` with Go/Linux in 2m37s and TypeScript in 28s.

Schema v60 adds the separately confirmed deterministic runtime-input projection plan. Application accepts only a completed v59 handoff and completed attempt, revalidates the complete v48-v59 authority, recompiles the exact Manifest/container specification, and recaptures the v57 sealed bundle. A frozen report view prevents mutable provider metadata from changing during parsing. Report fingerprint, bundle digest/length, source and Artifact counts, and Artifact payload identity must match durable evidence.

The compiler permits only byte-for-byte canonical v57 PAX tar and rejects links, devices, traversal, duplicates, missing parents, unexpected roots, empty Artifacts, trailing bytes, and non-canonical headers. The first profile requires directory-root read-only mounts; each root becomes a separate relative tar projection, while Artifacts map to fixed `/cyberagent-input/artifacts`. Transient future volume names include the v59 handoff fingerprint, so restart retries are deterministic and identical input across different Runs remains isolated.

SQLite schema v60 atomically commits one operator-confirmed plan, ordered digest-only items, an aggregate completion marker, operation binding, and metadata event under the Run write lock. Go and SQL enforce contiguous item sets, aggregate sums, immutable records, exact handoff/attempt/plan binding, and false daemon/start/exec/export/backend/execution/Artifact authority. CLI adds `docker-runtime-input-plan`, `docker-runtime-input-plans`, and `docker-runtime-input-plan-show`; output contains no raw target, host path, file name/content, volume name, or archive bytes. Migration from v59 creates no projection facts. The audit fixed missing durable confirmation, cross-Run future volume-name collision, an incorrect global item-fingerprint uniqueness constraint, non-canonical trailing tar acceptance, deprecated tar xattr inspection, duplicate/out-of-range mount ordinals, incomplete plan chronology, and canonical long-PAX-path compatibility. No unresolved high/medium issue is known.

The final local gate passed full ordinary/race suites in 198.9s/194.0s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 17 tests across 8 frontend files, OpenAPI drift, production build, zero-vulnerability npm audit, repository privacy/artifact/encoding/Markdown scans, Linux sandbox cross-compilation, diff checks, and an isolated real-binary schema-v60 Workspace smoke. Compiler/Store/Application/CLI repetitions passed at 50/30/20/10, and the critical Sandbox/Store/Application race set passed 10 rounds. GitHub Actions run `29428011306` passed feature commit `cc92421` with Go/Linux in 2m48s and TypeScript in 24s. This Windows host cannot run the v59 handoff or now-implemented v61 application real-daemon harness; no start authority follows from v60.

Schema v61 adds recoverable runtime-input application without process execution. `ApplyDockerRuntimeInputs` requires two explicit confirmations and commits an immutable intent plus an independent generation lease before recapture or daemon access. It then revalidates the complete v48-v60 chain, recompiles the same container specification, re-resolves the writable output bind, recaptures v57 bytes, and requires the v60 projection facts to reproduce exactly. Failed generations append only typed metadata, release atomically, and can be resumed; completion and failure are fenced to the current lease. A completed operation replays without provider or daemon effects.

The Unix transport is fixed to Docker API `1.40` on the local socket. For every projection it creates an authority-labelled local volume and never-started carrier, writes the canonical archive only at `/cyberagent-input`, reads it back, verifies exact expected tar entries, modes, and content, and removes the carrier. It then creates and exactly inspects one retained target whose input volumes are all read-only/`NoCopy`; the reviewed output bind is the sole writable mount. Existing exact residue is reconciled, foreign collisions are preserved and rejected, failure cleanup runs independently, and daemon work stops before lease expiry with cleanup time reserved. The transport exposes no start, exec, attach, logs, export, pull, build, network mutation, forced volume delete, or arbitrary endpoint.

SQLite v61 stores intent (including its unique digest-keyed operation binding), lease, bounded failure, immutable result, and events as metadata only; no separate operation table is needed. CLI adds `docker-runtime-input-apply`, `docker-runtime-input-apply-resume`, `docker-runtime-input-applications`, and `docker-runtime-input-application-show`; neither persistence nor output contains raw target/path/file/resource names, IDs, archives, sockets, operation keys, or private lease identities. Focused Sandbox, Store, Application, CLI, migration, SQL immutability, privacy, stale-worker, takeover, replay, collision, readback, cleanup, allowlist, and Windows unsupported tests pass. `volumes_applied_target_never_started` remains false for start, process, export, backend, execution, and Artifact authority. ADR 0021 is the recovery boundary.

The v61 final local gate passed full ordinary/race suites in 197.5s/316.8s, vet, zero-warning staticcheck, module verification, zero-finding govulncheck, strict TypeScript, 17 frontend tests, OpenAPI drift, production build, zero-vulnerability npm audit, repository privacy/process/endpoint/encoding/Markdown scans, diff checks, Linux sandbox test-binary cross-compilation, and isolated schema-v61 real-binary smoke; Sandbox race tests passed 20 repetitions. The audit tightened per-volume readback limits, lease cleanup reserve and time validity, Application-level resume confirmations, cancellation-safe typed failure persistence, narrow v55/v59/v61 transport interfaces, daemon-returned `RW`/`NoCopy` evidence, and operation-digest syntax. No unresolved high/medium issue is known. The Windows host cannot execute the opt-in Linux v59/v61 real-daemon harnesses, so start remains blocked. GitHub Actions run `29437941378` passed feature commit `f4aaf7a` with Go/Linux in 2m37s and TypeScript in 27s.

Schema v62 adds immutable metadata-only inspection for the v61 retained target and volumes, plus a separate recoverable exact-owned cleanup. Inspection requires explicit read confirmation, reconstructs the exact resource descriptor from current durable authority without input recapture, and records complete, partial/absent, or unsafe foreign-collision state. Only a complete exact target and all exact volumes establish never-started/read-only/`NoCopy` evidence; unsafe evidence is persisted and returned as a failed precondition.

Cleanup requires its own operator and daemon-write confirmations and a cleanup-eligible inspection. An immutable intent and active generation lease commit before transport use. The fixed local-Unix implementation preflights every target/volume before any DELETE, performs zero DELETE after a foreign collision, removes the target by inspected ID before exact volumes, and rechecks total absence. Bounded failure codes release the lease; later generations recover while stale workers are fenced. Completed operation and resume replay are metadata-only. Windows exposes distinct narrow unsupported inspector/cleanup capabilities and never falls back to host execution.

Focused Sandbox, Store, Application, CLI, migration, SQL immutability, privacy, replay, failure/takeover, and platform tests pass. The audit corrected read-only/`NoCopy` overclaiming for partial or unsafe inspections, made resource-cleanup event names unambiguous, exposed foreign-collision failure truthfully in CLI output, rejected future/out-of-window terminal timestamps, made v61/v62 lease rows undeletable, and extended the Linux opt-in v57/v59/v60/v61 harness through v62 cleanup. No high/medium issue is currently known. ADR 0022 records the boundary.

The v62 final local gate passed full ordinary/race suites in 313.6s/329.6s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 17 frontend tests across 8 files, OpenAPI/build/npm audit, repository privacy/capability/encoding/Markdown scans, diff checks, Linux sandbox test-binary cross-compilation, isolated schema-v62 real-binary smoke, and Sandbox/Store/Application/CLI stress repetitions of 20/15/10/10. GitHub Actions run `29444398815` passed feature commit `d250d32` with Go/Linux in 2m35s and TypeScript in 20s. The Windows host still cannot execute the opt-in Linux v59/v61/v62 real-daemon chain, so no start or process-isolation claim follows.

Schema v63 adds the immutable design-only Docker start-gate review. It requires a completed v62 cleanup, exact Manifest resubmission, and explicit operator confirmation, then revalidates the v48-v62 authority chain without input recapture or daemon contact. All sixteen v51 checks are stored with fixed evidence class/source, blocker code, and future gate; all remain `production_verified=false` and `sufficient_for_start=false`. The same transaction freezes an eleven-transition process lifecycle blueprint with per-Run generation-fenced single ownership, write-ahead intent, fixed endpoint, cancellation fan-out, bounded logs, wait, graceful/forced termination, and orphan reconciliation. Every transition is unimplemented and unauthorized, and every process/output/Artifact authority bit is false. Store migration creates no historical reviews; operation replay, cross-Store convergence, SQL immutability, fingerprint tamper detection, CLI privacy, and no-provider/no-daemon behavior have focused coverage. ADR 0023 records this boundary.

The v63 final local gate passed the final code's full ordinary/race suites in 196.9s/212.3s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 17 frontend tests across 8 files, OpenAPI/build/npm audit, repository credential/secret-file/process-entry/encoding/diff scans, Linux sandbox test-binary cross-compilation, isolated schema-v63 real-binary Workspace/Skill smoke, and Sandbox/Store/Application/CLI stress repetitions of 20/15/10/10. The audit added immediate `Rows.Err` handling for v63 child tables and lower-case internal error strings for static rules. No unresolved high/medium issue is known. Desktop and custom-Skill package changes are planning-only and add no binary dependency, import surface, installer, registry mutation, or authority. The Linux real-daemon chain remains unexecuted on this Windows host, so every v63 blocker remains unresolved. GitHub Actions run `29503856229` passed commit `e25a2ab` with Go/Linux in 2m32s and TypeScript in 24s.

The docs-only run `29444664401` exposed an npm-registry advisory-endpoint outage after `npm ci` had already reported zero vulnerabilities; GitHub then left the completed runner marked in progress. CI now retries `npm audit --audit-level=high` at most three times with bounded delay. A real high-severity finding still fails every attempt and blocks the workflow.

The first non-schema `skill_package.v1` slice adds a strict pure-memory ZIP parser, immutable metadata preview, canonical semantic fingerprint, raw archive digest, adversarial tests, and fuzzing. The only product entry is `skill package validate`: it performs a bounded regular-file read with symlink and identity-change rejection, prints no body/source path, creates no database, and keeps install, command, network, Provider, tool, and capability authority false. The accepted profile is exactly two ordered Deflate entries (`manifest.json`, `SKILL.md`) with fixed ZIP 2.0 data descriptors, zero metadata, no prefix/gaps/tail, a 64 KiB archive cap, bounded decompression/ratio, CRC/header agreement, and the existing strict `skill.v1` semantic checks. ADR 0024 records the boundary. This slice adds no migration, user Registry, import/install command, Run selection, or Desktop/HTTP upload.

The final package-validation gate passed full ordinary/race suites in 239.4s/226.8s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, 20 seconds and about 26.45 million parser fuzz executions, 78.5% `internal/skills` statement coverage, 100 parser and 20 CLI repetitions, strict TypeScript, 17 frontend tests across 8 files, OpenAPI/build/npm audit, and credential/runtime-artifact/replacement-character/Markdown-link/diff scans. The audit pinned the central-directory creator version and exact Deflate-stream exhaustion to close hidden post-stream payloads, replaced a deprecated test fixture API, and wrapped filesystem causes behind stable path-free CLI errors. Synthetic redaction fixtures are the only key-pattern scan matches. No unresolved high/medium issue is known. GitHub Actions run `29512332025` passed commit `55b3fae` with Go/Linux in 3m4s and TypeScript in 20s.

The non-schema protected-delete slice adds a path-free critical permanent Policy decision before approval for explicit recursive, absolute/traversing/wildcard, environment-derived, command-substituted, current-home, PowerShell/`cmd`, and common interpreter deletion forms. Raw Shell and decoded ScriptProcess/Sandbox intents share the guard; argument-map ordering is deterministic, ordinary evidence remains non-executable, and denied proposals cannot acquire a dry-run result through operator approval. The focused audit fixed Node `require('fs').rmSync(...)`, leading `../`, and PowerShell `-Force` classification edge cases. ADR 0025 records that this classifier is only defense in depth: opaque scripts/build tools require OS/container isolation, and Local/container process execution remains disabled. That slice added no migration or authority and left schema at v63; schema v64 became the execution-profile control plane, and the Skill Registry was subsequently completed at schema v69 after the v68 evidence-review slice.

The final local gate passed the full ordinary suite in 197.0s and the full race suite in 222.6s, plus 20 repeated race runs across the Policy/Gateway/Application protected-delete paths, about 406,000 fuzz executions, 100/50/50 focused repetitions, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 17 frontend tests, OpenAPI/build/npm audit, and credential/runtime-artifact/replacement-character/Markdown-link/diff scans. The credential pattern scan found only six existing synthetic redaction fixtures. No unresolved high/medium issue is known. Local Linux cross-compilation was not repeated because the host command policy rejected the temporary-artifact cleanup command; GitHub Linux CI remains the platform proof after push.

## Completed Execution-Profile Slice (v64)

Schema v64 adds immutable `run_execution_profile.v1` snapshots and digest-only idempotency operations. New and migrated Runs default to `preview`; operators may select `preview`, `docker`, or `local` only while a Run is `created` or `paused` with no active execution lease. Domain validation, Store revalidation, SQLite checks/triggers, CLI output, HTTP DTOs, OpenAPI, generated TypeScript, and React all preserve the same fixed mapping and false authority bits. Transitions append `run.execution_profile_selected`; initial compatibility snapshots deliberately do not rewrite historical event sequences.

The local Web console accepts an optional distinct control bearer in page memory. That credential is never put in a URL, body, storage, or read request. The only newly exposed mutation is an idempotent profile selection; TypeScript submits only the profile enum and cannot submit backend, scope, network, gate, approval, process, execution, or capability fields. ADR 0026 records the boundary.

The v64 final local gate passed final-code full ordinary tests in 225.9s; the complete race suite passed in 196.9s, followed by targeted profile/HTTP race after the final DTO privacy reduction. Vet/staticcheck/module/govulncheck, strict TypeScript, 21 frontend tests, production build, npm audit, deterministic generated-contract hashes, chronology/link/privacy/artifact/encoding/diff checks, and isolated CLI smoke are green. GitHub Actions run `29523634340` passed implementation commit `8378419` with Go/Linux in 3m0s and TypeScript in 26s. Audit fixes were limited to generic control-request wording, six static error-style findings, and omission of requester/reason from browser DTOs. No unresolved high/medium issue is known. Linux real-daemon evidence was not run on this Windows host, so Docker start remains blocked.

A later real production-bundle smoke exposed one low-risk Web availability defect: Vite 8 emitted `index-D0TcvGy-.css`, whose trailing URL-safe hyphen defeated the old last-separator filename heuristic. `assetNameHasDigest` now searches backward for a bounded URL-safe digest; the primary bundle fixture uses the observed name and short/invalid suffixes remain denied. The exact built bundle then loaded under Go, served CSP-protected HTML, rendered on desktop and a 390x844 mobile viewport without horizontal overflow, and completed Docker-to-Preview UI selection while both execution authority bits remained false. The follow-up full suite passed in 201.1s, with 20 targeted race repetitions plus clean vet/staticcheck.

## Completed Docker Production-Evidence Slice (v65)

Schema v65 adds immutable `sandbox_docker_production_evidence.v1` aggregates, sixteen ordered probe items, and digest-only idempotency operations. Captures bind the exact v63 blocked review, Run/Mission/Workspace, authority and threat-model fingerprints, and the same operator. The CLI accepts only the review ID, bounded operation key, and explicit confirmation. It cannot accept evidence conclusions, JSON reports, sockets, paths, images, resources, container IDs, or raw daemon responses. One transaction stores the aggregate, all items, operation binding, and a metadata-only event; immutable SQL triggers, semantic replay, and a 32-capture-per-Run cap close mutation and unbounded-growth paths. Migration creates no historical receipts.

At the v65 delivery point, the local collector was deliberately inert. Windows returned `unsupported_platform`; Linux without `CYBERAGENT_DOCKER_PRODUCTION_EVIDENCE=1` returned `opt_in_required`; Linux with opt-in returned `harness_pending`. All three paths made zero daemon, network, Docker CLI, and process calls. The v65 Application hard-rejected `capture_complete` and `real_daemon_contacted=true` before persistence. Schema v66 later supplied the ownership boundary, and schema v67 now supplies the separately constrained read-only harness. Every probe remains `sufficient_for_start=false`, and all start/process/output/Artifact authority remains false. ADR 0027 records the ledger boundary.

Focused Domain, Store, Application, CLI, migration, SQL, idempotency, privacy, and malicious-collector tests pass. The final local ordinary suite passed in 212.3s and the full race suite in 213.9s. Vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 21 frontend tests, OpenAPI drift, production build, zero-vulnerability npm audit, credential/runtime-artifact/process-capability/diff scans, the canonical README history check, and an isolated real-CLI schema-v65 Workspace smoke are green. The audit fixed eleven static error-style findings, made multi-field identity validation deterministic, bound the SQL operation key directly to its evidence root, and rejected a future collector that could otherwise claim real-daemon contact before durable harness ownership. No unresolved high/medium issue is known. This Windows host did not run a Linux daemon harness because none exists in v65.

GitHub Actions run `29532551701` passed implementation commit `e97daf0`; Go/Linux completed in 2m47s and TypeScript in 20s.

## Completed Recoverable Production-Evidence Attempt Slice (v66)

Schema v66 adds immutable `sandbox_docker_production_evidence_attempt.v1` intents, digest-only operations, expiring generation-fenced leases, per-generation quiescent reconciliation checkpoints, bounded typed failures, and one immutable evidence result. The Application persists attempt ownership and the current checkpoint before collector invocation, derives a deadline from both the fixed 30-second capture bound and lease expiry, and releases the lease atomically with either failure or evidence completion. Released retries and expired takeovers advance generation; stale workers cannot commit. New SQL enforcement prevents the legacy Store path from creating a new v65 evidence operation without an attempt result, while legacy v65 receipts remain readable and receive no invented attempts.

CLI capture now reports the associated attempt and adds metadata-only `docker-production-evidence-attempts`, `docker-production-evidence-attempt-show`, and explicitly confirmed `docker-production-evidence-attempt-resume`. Lease IDs and owners, raw errors, sockets, paths, resource/container identities, and daemon payloads are not exposed. Focused tests cover collector-visible write-ahead ordering, active conflicts, released recovery, expired takeover, stale fencing, unsafe-contact failure, generation-two completion, SQL bypass rejection, migration compatibility, immutability, privacy, and replay without recollection. ADR 0028 records the boundary.

At the v66 delivery point, the Windows/Linux collector remained inert and no real daemon harness was executed. Its reconciliation checkpoint records only zero daemon reads and zero known resources; it is an ownership/order fact, not Docker production-resource verification. Schema v67 later adds a separate daemon-aware checkpoint while leaving every start, process, output, Artifact, backend, execution, and capability authority bit false. Architecture completion remains about 98% and product usability about 45-50%, because these slices improve evidence and recoverability rather than adding an end-user execution capability.

The final local gate passed the full ordinary/race suites in 206.9s/230.3s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, strict TypeScript, 21 frontend tests, OpenAPI/build/npm audit, 50-file Markdown-link validation, repository credential/runtime-artifact/encoding/process-entry/diff scans, and focused Domain/Store/Application/CLI repetitions of 50/10/5/5 plus critical race repetitions of 5/3/3. An isolated real binary loaded only mock, migrated a separate runtime to schema v66, and initialized/listed a Workspace. Host protected-delete policy rejected recursive cleanup, so that directory remains under the OS temporary root for normal cleanup; no user action is required.

The audit fixed the v66 lease's missing immutable-delete trigger, constrained direct-SQL release to pre-expiry time and generation acquisition to the prior release/expiry chronology, and corrected trailing `--limit` parsing for the v65 capture list. No unresolved high/medium issue is known.

GitHub Actions run `29538732903` passed implementation commit `3e52b7d`; Go/Linux completed in 3m33s and TypeScript in 25s.

## Completed Linux Read-Only Production-Evidence Harness Slice (v67)

Schema v67 adds immutable harness intent, daemon-aware reconciliation, and result records behind the v66 attempt boundary. An explicitly opted-in Linux collector first proves that the exact attempt-labeled container scope is empty, then performs `_ping`, `version`, `info`, and inspect for the exact already-present image digest. The transport is fixed to the local Unix endpoint, ignores `DOCKER_HOST`, exposes no mutation method, never pulls, and permits at most five GETs with a four-second per-call bound inside the existing 30-second attempt deadline.

The v67 result is deliberately non-authorizing: all sixteen probes are `observed_failed`, `production_verified_count` is exactly zero, and every start/process/output/Artifact authority bit remains false. Go and SQLite bind intent, control reconciliation, daemon reconciliation, lease generation, evidence, and operation; a persisted v67 intent cannot fall back to the v66 inert result. Released/expired recovery uses generation N+1 and repeats daemon-aware empty-scope reconciliation. Public output keeps only bounded metadata and contact confidence, never raw daemon errors, payloads, sockets, image repository names, resource identities, paths, or private lease identity. ADR 0029 records the boundary.

Focused Domain, Store, Application, HTTP transport, migration, and CLI tests cover exact call ordering, label filtering, collision rejection, durable ordering, generation binding, zero verification, immutability, v66 fallback rejection, in-flight migration compatibility, privacy, and replay without new daemon calls. The final local gate passed full ordinary/race suites in 215.2s/233.1s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, 21 frontend tests, generated-contract drift checks, production build, zero-vulnerability npm audit, 51-file Markdown validation, repository privacy/artifact/encoding/Docker-mutation/diff scans, isolated real-binary Workspace smoke, Linux test-binary cross-compilation, and focused Sandbox/Store/Application/CLI repetitions of 50/10/5/5 plus race repetitions of 10/3/3/3.

The audit forced production verification to exactly zero, recomputed the exact label selector, cross-bound the v66 control reconciliation, required evidence before lease expiry, closed a direct-SQL timestamp mismatch that could leave an immutable half-terminal record, and made daemon-contact reporting evidence-based. No unresolved high/medium issue is known. The local Windows host did not run the optional real Linux daemon path; no real Docker, container start, Shell, or host process was executed. Protected host deletion policy rejected recursive cleanup of the isolated smoke roots, so two directories remain under the OS temporary root for normal cleanup and need no user action. GitHub Actions run `29543385038` passed implementation commit `8bc0929`; Go/Linux completed in 2m50s and TypeScript in 24s.

## Completed Immutable Production-Evidence Review Slice (v68)

Schema v68 adds immutable `sandbox_docker_production_evidence_review.v1` and digest-only operation records over one exact completed v67 harness receipt. The operator explicitly confirms one accepted or rejected decision. Acceptance can only classify the bounded metadata scope; rejection uses one of five fixed codes. There is no free-form reason, uploaded report, daemon payload, resource identity, path, socket, raw operation key, or private lease identity in the request or public projection.

The Store writes operation first and review second in one transaction. A deferred foreign key and reciprocal triggers make both halves mandatory at commit, while source triggers rebind the blocked v63 review, v65 receipt/items, v66 attempt, and v67 harness result. Each evidence/attempt receives at most one immutable decision, and migration creates no historical review. Same-key/same-semantic replay returns the existing record without another event or daemon call; changed semantics conflict.

Even an accepted receipt retains `production_verified_count=0`, `sufficient_check_count=0`, `blocker_count=16`, and false start-gate/container/process/output/Artifact authority. v68 performs no Docker request, model call, Shell, or host-process start. The final ordinary/race suites passed in 247.9s/276.3s; vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, 21 frontend tests, OpenAPI/build/npm audit, 57-file/74-link Markdown validation, credential/encoding/forbidden-entry/diff scans, Linux cross-compilation, and isolated real-CLI schema-v68 smoke are green. Focused Domain/Store/Application/CLI repetitions passed at 50/10/5/5 and race repetitions at 10/3/3/3.

The independent audit closed stored operation/review request-fingerprint drift at both SQL and Store read/replay boundaries, added review-only and operation immutability/source-tamper negative tests, proved two-Store convergence, and extended rejected decisions through Store/event/Application/CLI/list/show/replay. No unresolved high/medium issue is known. Protected deletion left one cross-compiled binary and one smoke root under the OS temporary directory for normal cleanup; no manual action is required. GitHub Actions run `29552080990` passed implementation commit `41583ac`; Go/Linux completed in 2m57s and TypeScript in 24s. ADR 0030 records the boundary.

## Completed Inert User Skill Registry Slice (v69)

Schema v69 adds five immutable tables for installation operations/intents/results and removal operations/tombstones. A stable raw operation key is stored only as a domain-separated digest. Deferred foreign keys and reciprocal triggers require operation/root pairs to commit together; all rows are append-only. Installation intent commits before object publication, so a same-key retry recovers an interrupted import. Same-key changed intent conflicts, same name/version under another operation conflicts, and two SQLite connections converge on one intent/result.

`LocalPackageObjectStore` stores only strictly validated deterministic ZIP bytes below `$CYBERAGENT_HOME/skill-registry/objects/sha256/<prefix>/<digest>.zip`. It exposes `Put` and `Verify` only, publishes through an exclusive same-directory temporary file plus file sync and atomic hard link, and revalidates size, SHA-256, ZIP structure, semantic package fingerprint, and file identity on every completed replay/list/show. Symlinks, replacement, corruption, forged receipts, and cancellation fail closed. The package body and source path never enter SQLite, Run events, or CLI output.

CLI now supports explicitly confirmed `skill import`, metadata-only `skill installed` and `installed show`, and explicitly confirmed `skill remove`. External packages are always `operator_installed_untrusted`; all command, hook, network, Provider, tool-grant, Run-selection, and context-injection authority is false. Built-in names cannot be shadowed. Code/Cyber catalogs are separate and Cyber accepts exactly the `script` Profile. Removal appends a tombstone, retains the content object, blocks an exact Run-pinned version in Go and SQL, and has no implicit reinstall/restore behavior. ADR 0031 records the boundary.

The final local gate passed ordinary/race suites in 259.7s/275.3s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, OpenAPI drift checks, 21 frontend tests, production build, and zero-vulnerability npm audit. Focused v69 race tests passed three repetitions, and real two-Service import/removal convergence with independently generated IDs/timestamps passed 20 ordinary and 10 race repetitions. Initial Linux CI run `29556933994` exposed concurrent nested-directory preparation through `os.Root.MkdirAll`; the object store now creates each component independently, accepts an existing component only after `Lstat` proves it is a real directory, and rejects symlink redirection. Twelve independent Stores passed 100 ordinary and 20 race repetitions, and the Linux test binary cross-compiled. GitHub Actions run `29557803407` passed fix commit `d28b100` with Go/Linux in 3m21s and TypeScript in 23s. The broader audit fixed migration downgrade ordering, static error text, redundant temporary cleanup state, object-receipt interface binding, cancellation immediately before publication, semantic replay comparison of generated identities, and credential redaction for the free-text Manifest description before SQLite persistence. No unresolved high/medium issue is known. No model, network request, Shell, Docker operation, installation hook, or host process was run by this slice.

## Completed External Skill Run Context Slice (v70)

Schema v70 adds immutable external selection/item/operation ledgers and separate root/Specialist context preparation/commit ledgers. Selection requires a created Run, current mode, exact active v69 installation/result/object identity, stable digest-only operation, and a second explicit untrusted-context confirmation. It allows one to four packages under a 4096 aggregate budget and at most one operator-designated Specialist package. Code/Cyber/Profile constraints remain fixed, same-intent replay converges, and Go plus SQL prevent removal of a pinned installation.

`PackageObjectLoader` is separate from object publication. It opens only the expected content-addressed path and rechecks ordinary-file identity, size, archive SHA-256, deterministic ZIP structure, semantic package fingerprint, and Manifest/content binding before returning a defensive in-memory copy. Root and Specialist assembly redact secrets and apply separate budgets; the child defaults to 1024 and is capped at 2048. Content is serialized only as a user-role `external_skill_guidance.v1` envelope with Policy/tool/Shell/network/secret/scope/delegation authority fixed false. System control text explicitly treats external content as optional workflow guidance and document claims as evidence, not instructions.

Root context preparation commits with the corresponding first `model.started`; Specialist preparation commits with its first Specialist model-call start. SQLite and events retain counts, budgets, redaction totals, selection/context fingerprints, and closed authority facts, never package content, source paths, raw keys, secrets, or model text. CLI supports `skill select-external` and `skill external-selection`; HTTP/TUI/Web mutation and upload remain absent. Focused tests cover confirmation, replay, exact binding, cross-surface/Profile rejection, secret redaction, object mismatch, root/child user-role provenance, no-tool child delivery, immutable SQL, direct-SQL removal protection, and v69-to-v70 migration without fabricated state. ADR 0032 records the boundary.

The final local gate passed the full ordinary/race suites in 197.6s/264.4s, vet, zero-warning staticcheck, module verification/tidy diff, zero-finding govulncheck, 21 frontend tests across nine files, OpenAPI drift checks, production build, zero-vulnerability npm audit, repository credential/runtime-artifact/encoding/43-file Markdown-link/diff scans, and an isolated real-CLI schema-v70 smoke. The audit fixed one medium-severity schema defect that had made `installation_id` globally unique, incorrectly preventing one verified installation from being independently selected by later Runs; uniqueness is now scoped to a selection and covered by a second-Run regression. Further hardening binds Specialist provenance to the latest mode in Go and SQL, rejects Specialist packages above 2048 before persistence, dynamically accepts the valid 1024-2048 range, preserves same-operation replay across Plan-to-Delivery drift, pairs selection and operation with a deferred reciprocal foreign key, closes cancellation windows around object parsing, and emits every non-guidance authority field explicitly false. No unresolved high/medium issue is known. No real model, network, Shell, Docker, installer hook, or host process ran. Protected cleanup left the isolated smoke root under the OS temporary directory for normal cleanup; no user action is required. GitHub Actions run `29566538449` passed implementation commit `edc4073` with Go/Linux in 3m42s and TypeScript in 21s.

## Completed External Skill Read Projection Slice (v71)

Schema v71 adds two SQLite read-only views and a separate `external_skill_projection.v1` Go contract. Existing v70 selections become visible without backfill or new events. Runs without an external selection remain absent. The root projection contains Run/mode/surface/Profile, bounded token and item totals, closed authority facts, and root/Specialist prepared/committed counts. Item rows contain only ordinal, name, version, token upper bound, trust class, declared-tool count, and Specialist eligibility.

The safe type cannot represent package bodies, paths, byte sizes, digests/fingerprints, selection/installation/mode-snapshot IDs, requester/operation identities, or attempt/Agent identities. Store tests inspect the view columns, reject writes, verify v70 in-place upgrade without fabricated facts, and scan serialized values for private identities. The same DTO is optional in Run detail and available from read-only `GET /api/v1/runs/{run_id}/external-skills`; the OpenAPI contract now has 26 paths and 61 schemas. TUI adds a read-only `Skills` activity and stable count projection. React adds a button-free Run-overview panel. No control capability, model call, package load, Shell, network, Docker, installer hook, or Agent-controlled host process was added or run.

Final local gates: full ordinary/race suites passed in 227.1s/301.1s; vet, zero-warning staticcheck, module verify/tidy diff, zero-finding govulncheck, deterministic OpenAPI/TypeScript generation, strict TypeScript, 9 files/22 frontend tests, production build, zero-vulnerability npm audit, credential/runtime-artifact/production-process-entry/encoding/54-file and 78-relative-link Markdown/diff scans, and an isolated real-binary schema-v71 Workspace smoke are green. The audit added explicit Run matching to all four preparation/commit count subqueries and a separate ordinary/race HTTP regression for a valid Run without a selection. No unresolved high/medium issue is known. No real Provider, Agent-controlled Shell/host process, Docker, installer hook, or external network call ran. The smoke root remains below the OS temporary directory for normal cleanup and requires no user action. GitHub Actions run `29574167659` passed implementation commit `3947bea`; Go/Linux completed in 2m56s and TypeScript in 25s.

## Completed Pathless Desktop Skill Preview Boundary (non-schema D1-A)

The CLI package validation/import paths now share `skills.ReadPackageFile` and `skills.ValidatePackageFile`. The reader accepts only one bounded non-symlink ordinary file, rejects leading/trailing whitespace rewrites, compares pre-open/opened/post-read identity, size, and modification time, honors cancellation, and returns path-free errors. The existing strict deterministic ZIP parser remains the sole semantic validator.

`desktop.NewSkillPackagePreviewBoundary` returns a native-only Go selector function and a separately bindable renderer bridge. The selector validates immediately and stores only a constrained projection; it never stores the path or body. The renderer receives a cryptographically random 256-bit URL-safe handle with a five-minute TTL, sixteen-entry process cap, and atomic single-consumption rule. The DTO excludes file/path/body, Manifest description/content path/content digest, and fixes command/hook/network/Provider/tool/install authority false. At D1-A delivery there was no Wails registration; ADR 0034 now binds only this pathless flow in the read-first shell. The preview still creates no database/event/model/network/process/Docker/installation mutation.

The final full ordinary/race suites passed in 255.8s/314.0s. Desktop passed 100 ordinary and 25 race repetitions, the Skill file boundary passed 100 repetitions, and the CLI package chain passed 10. Vet, zero-warning staticcheck, module verify/tidy diff, zero-finding govulncheck, OpenAPI drift checks, 22 frontend tests, production build, zero-vulnerability npm audit, 60-file/79-relative-link Markdown validation, and repository privacy scans are green. Tests cover path-free JSON allowlists, source replacement after selection, missing/directory/symlink/empty/oversized/malformed input, cancellation without consumption, expiry/capacity, entropy failure, replay, and 32 concurrent consumers with exactly one success. No unresolved high/medium issue is known. GitHub Actions run `29578985787` passed implementation commit `45c047c` with Go/Linux in 3m13s and TypeScript in 27s. ADR 0033 records the boundary. Schema remains v71 and product-usability estimates remain unchanged because no end-user Desktop UI exists.

## Completed Embedded Read-First Windows Desktop Shell (non-schema D0-A)

Wails v2.13.0 is now a pinned direct Go dependency. `cmd/cyberagent-desktop` builds only on Windows with the `desktop` tag and embeds the Vite production bundle through `web/assets_desktop.go`. `webui.LoadEmbeddedFS` snapshots only a bounded UTF-8 index and content-hashed allowlisted assets. The existing `httpapi.API` Handler runs directly behind the Wails AssetServer, with no TCP listener or second business API. Its narrow adapter pins loopback identity and handles the two exact Wails v2.13 request-shape differences observed in real startup.

The complete Wails renderer binding has exactly `Bootstrap`, `SelectSkillPackage`, and `PreviewSkillPackage`. Bootstrap returns same-origin `/api/v1`, bounded app/UI metadata, and ephemeral memory-only tokens with every process/Shell/Docker/Skill-install/path-input authority bit false. The default launch has no control token. Explicit `--enable-profile-control` creates a distinct control token only for the existing v64 non-authorizing profile selection. The native `.zip` dialog is serialized and path-free at the renderer boundary; valid selections reuse ADR 0033's one-time handle, while cancellation and native errors reveal no path.

React auto-connects without showing or persisting tokens. Ordinary Web retains SSE, while Windows Desktop uses bounded opaque-cursor event polling because Wails v2 does not stream AssetServer responses on Windows. The top bar exposes only package preview and refresh in the default shell; it contains no ineffective disconnect, install, terminal, process, approval, or queue mutation control. Single-instance restore, file/drop denial, context-menu denial, CSP, renderer code integrity, and a path-free typed startup failure dialog are active. No installer, registry, auto-start, updater, protocol, service, signed release, LocalRunner, Docker start, Shell, model call, network Scope mutation, or new SQLite fact exists.

Focused Go and TypeScript tests cover the exact binding/JSON allowlists, closed authority, token distinction, dialog serialization, path privacy, one-time handles, embedded asset bounds, Wails request compatibility, auto-connection, polling, cancellation, and no-install preview. A production-tag binary built successfully and was launched against an isolated schema-v71 home. Visual checks covered the 1440x900 shell, automatic API connection, empty Run/Session states, the Skill modal, and native `.zip` dialog without overlap or blank rendering.

The final local gate passed the full ordinary/race suites in 205.1s/293.9s, ordinary and desktop-tag vet/staticcheck, ordinary and desktop-tag zero-finding govulncheck, module verification/tidy, deterministic OpenAPI generation, strict TypeScript, 31 tests across 12 frontend files, the production build, zero-vulnerability npm audit, 61-file/82-relative-link Markdown validation, and credential/runtime-artifact/encoding/forbidden-execution/diff scans. Desktop-focused packages passed 50 ordinary and 10 race repetitions. The final unsigned Windows GUI is 21,022,208 bytes with SHA-256 `6b355cfa72b41d225e62ed58ac24cb9493bbf2a71f4d45120e6f0dbf5308ad0c`; it is ignored by Git. The audit fixed the real Wails empty-root/no-body-GET compatibility cases, hid the ineffective Desktop disconnect action, rejected equal read/control tokens, added a path-free startup error, and finally made nil-URL/missing-RequestURI adaptation fail closed. No unresolved high/medium issue is known. GitHub Actions run `29602281365` passed implementation commit `2c0b81c`; Go/Linux completed in 4m57s, TypeScript in 26s, and the new Windows Desktop job in 4m27s. ADR 0034 records the boundary.

Metrics after D0-A: architecture remains about 98% (V2 about 99%); complete-product usability is about 52-56%, generic coding-agent workflow usability about 46%, and Cyber autonomous workflow usability about 20%. The gain is a real read-first Windows client and native package risk preview, not execution authority.

## Completed Desktop Lifecycle And Event Resumption Hardening (non-schema D0-B)

`desktop.OpenControlPlane` now owns one Desktop SQLite connection and the existing in-process `httpapi.API` without opening a socket. Same-database tests prove an independent CLI connection can append events while Desktop is open, Desktop can close and reopen without losing its high-water cursor, six control planes can open the initialized database concurrently, and close is idempotent. `desktop.Lifecycle` coalesces early second-instance signals, discards their arguments and working directory, restores only the existing window, serializes native restore with shutdown, cancels its context, and cannot restart after stop.

The read-only `run-event-poll.v1` endpoint returns real `run-events.v1` frames and the same Run-bound opaque cursor as SSE. It rejects unknown/duplicate query fields, invalid limits, cross-Run cursors, and SSE-only headers; Go validates a contiguous batch before returning it. React no longer synthesizes Desktop cursors from ordinary event pages. It keeps at most 16 Runs and 500 frames per Run in module memory, resumes across component remount, performs one stale-cursor reset per mount, honors abort after every response, and never uses Local or Session Storage.

Secure Windows builds require `desktop,production,wv2runtime.error`. WebView2 `94.0.992.31` or newer is checked read-only before bundle/database initialization; missing, old, or failed probes return bounded path-free guidance with no URL, download, or installer. The in-process adapter accepts exact `http://wails.localhost`, canonicalizes `RequestURI`, pins loopback identity, and rejects alternate origins. A Desktop-only renderer guard blocks external links, forms, and popup calls while ordinary browser navigation remains unchanged. Wails start-origin validation remains the native binding authority.

Final local validation passed the 256.6-second full ordinary suite, 273.5-second full race suite, ordinary and secure-Desktop vet/staticcheck/govulncheck, deterministic OpenAPI generation, strict TypeScript, 37 frontend tests across 13 files, production build, and zero-vulnerability npm audit. A focused 20-round lifecycle race also passed after the audit serialized native restore and shutdown. The first Desktop-tag vulnerability scan found five newly reachable `x/net/html@v0.54.0` advisories; `x/net` was upgraded to fixed v0.55.0, `x/sys` resolved to v0.45.0, and both final scans report no vulnerabilities. Windows 11 Pro x64 10.0.26200 real-process smoke of the final 19,572,224-byte binary (SHA-256 `f26ea87f42701a7eba8efa789900ea6953ef3c1533ff95106ec4b8e6b02b1160`) proved second-instance handoff, forced termination, same-database reopen, and zero residual Desktop processes. GitHub Actions run `29609621468` passed implementation commit `c9b1c66` with Go/Linux in 5m00s, Windows Desktop in 4m21s, and TypeScript in 23s. The unsigned binary remains ignored and non-distributable; Windows 10 real-machine coverage is pending. No unresolved high/medium issue is known, and no Agent-controlled Shell/Local/Docker, Provider, Skill installation, or external network operation ran. ADR 0035 records the boundary.

Metrics after D0-B: architecture remains about 98% (V2 about 99%); complete-product usability is about 53-57%, generic coding-agent workflow usability about 47%, and Cyber autonomous workflow usability about 20%. The increase reflects a recoverable read-first Desktop client, not new execution authority.

## Completed Idempotent Controlled Run Creation (schema v72 / D1-R1)

Schema v72 adds immutable digest-only `run_creation.v1` operations. The
Application service requires a registered Workspace and creates one redacted
Mission, interactive created Run, active Session, initial mode, preview/noop
execution profile, root Agent, and exact initial events in one immediate SQLite
transaction. Network is disabled, targets are empty, the default budget is
fixed, model route equals Profile, and the goal is bounded to 4096 UTF-8 bytes.
Same-key/same-intent replay returns the original graph across restart or
independent connections; changed intent conflicts. SQL triggers independently
rebind the entire closed initial graph and make operations immutable.

HTTP adds a strict control `POST /api/v1/runs` and read-only paginated
`GET /api/v1/workspaces`; the latter omits root paths. OpenAPI has 28 paths,
65 schemas, 25 GET operations, and four control POST operations. Desktop adds
an independent `--enable-run-creation` capability while keeping the native
bridge at exactly three methods. A creation-only token cannot reach existing
cancellation/profile controls. React adds a responsive New Run dialog with
Workspace, Profile, surface, and phase selection, in-memory uncertain-failure
key reuse, UTF-8 byte preflight, closed response validation, query refresh, and
new Run selection. Neither token nor operation key enters browser storage.

The implementation and audit cover immutable SQL, direct-SQL default-budget
rejection, cross-connection convergence, strict HTTP JSON/header/body handling,
Workspace path privacy, Desktop capability separation, forged Goal/Workspace/
mode/authority response rejection, strict UTF-8 JSON, and multibyte limits.
Historical downgrade fixtures now remove v72 triggers before deleting older
profile tables; SQL binds exact initial timestamps, root state, and event count;
and the creation service uses a narrow Store contract instead of inheriting
unrelated Run-transition authority. No model, tool, Shell, host process, Docker, network
request, Skill installer hook, or execution lease is invoked. ADR 0036 records
the boundary.

The final local gate passed full ordinary/race Go suites in 271.5s/257.9s,
ordinary and secure-Desktop tests plus vet/staticcheck/govulncheck, module
verification/tidy, deterministic OpenAPI/TypeScript generation, 45 frontend
tests across 14 files, strict TypeScript, the production builds, zero-finding
dependency audits, 63-file/86-relative-link Markdown validation, privacy and
forbidden-entry scans, and an isolated schema-v72 CLI smoke. No unresolved
high/medium issue is known.

Metrics after D1-R1: architecture remains about 98% (V2 about 99%);
complete-product usability is about 55-59%, generic coding-agent workflow
usability about 49%, and Cyber autonomous workflow usability about 20%.

## Completed Controlled Session Message Submission (non-schema D1-S1)

D1-S1 is one three-slice product batch over the existing schema-v45/v46
operator-steering queue; database schema remains v72. Slice one adds the narrow
`SessionMessageSubmissionService` and strict
`POST /api/v1/sessions/{session_id}/messages`. Go reloads the Session and exact
bound Run, redacts and bounds content, and reuses the existing digest-idempotent
enqueue operation and event semantics. The response is metadata-only and fixes
execution/model/tool/capability facts false.

Slice two adds independent Desktop `--enable-session-messages` bootstrap and
in-process Handler wiring. It neither expands the exact three-method Wails
bridge nor couples profile, creation, cancellation, or Session capabilities.
Slice three adds the React Session composer for running or paused bound Runs,
a 16 KiB UTF-8 preflight, memory-only same-intent uncertain-failure replay, and
queue-status feedback without echoing submitted content. Browser storage stays
empty and Go remains authoritative.

The integrated batch functional gate passed a direct 255.6-second full ordinary
Go suite on final code, the 80.5-second Desktop-tag focused suite, final focused Application/HTTP/
Desktop regression, 52 frontend tests across 15 files, strict TypeScript, Vite
production build, and Windows production Desktop build. The path invokes no
Provider, execution lease, tool, Shell, Docker, external network, or process.
The agreed six-slice robustness gate deliberately moves full race, vet,
staticcheck, govulncheck, dependency, and extended privacy analysis to the end
of the next three-slice batch. GitHub Actions run `29633205163` passed
implementation commit `3ecb22a`: Go/Linux 3m06s, Windows Desktop 1m38s, and
TypeScript 25s, including remote vet, govulncheck, and dependency audit. ADR
0037 records the boundary.

Metrics after D1-S1: architecture remains about 98% (V2 about 99%);
complete-product usability is about 57-61%, generic coding-agent workflow
usability about 52%, and Cyber autonomous workflow usability about 20%.

## Completed Run Control And Bounded Handoff (schema v73 / D1-S2/D1-L1/D1-X1)

D1-S2 adds non-schema `session_steering_cancellation.v1` and strict HTTP/Desktop/React control over the existing v46 ledger. Cancellation is exact Session -> Run -> message bound and pending-only. Public metadata now derives whether a pending message already has a prepared delivery, and React hides cancellation in that state; prepared, committed, or cancelled facts remain immutable.

Schema v73 adds `run_lifecycle_control.v1` and `run_execution_handoff.v1`. Lifecycle start/pause/resume uses an immutable digest-idempotency operation and exact quiescence/lease/Agent/Supervisor gates. A delayed replay returns the original operation plus the current Run state, so later legal transitions do not invalidate retry. The execution operation freezes at most eight pending identities before work, then uses the existing RunSupervisor, Policy, cumulative budgets, model/tool ledgers, private execution lease, checkpoints, and events. Later appends cannot enter the batch; cancellation before delivery is skipped; an empty selection completes without a lease or model call. Terminal completion is fenced to the exact lease generation and exact result intent.

HTTP adds independent strict control routes for message cancellation, Run lifecycle, and bounded execution. Desktop adds `--enable-session-steering-control`, `--enable-run-lifecycle`, and `--enable-run-execution`; the Wails bridge remains three methods. React adds pending cancellation, Start/Pause/Resume, and an at-most-eight-step Run Queue control with memory-only intent-bound retry keys. Responses contain counts/status only and omit message/model/tool content, raw keys, and private lease identities.

The cumulative six-slice gate passed the final ordinary suite in 268.2 seconds and race suite in 295.3 seconds, ordinary and secure-Desktop vet/staticcheck, both zero-finding govulncheck paths, module verify/tidy diff, deterministic contract generation, strict TypeScript, 66 frontend tests across 16 files, Vite and Windows production builds, zero-vulnerability npm audit, and repository privacy/artifact/forbidden-entry scans. The unsigned GUI is 20,849,664 bytes with SHA-256 `ce3ff2b4609068de996b6362e3a5008c4d2348eae73c48ad0661c4e22739eba5`.

The combined audit fixed delayed lifecycle replay, misleading cancellation for prepared items, stale-lease/changed-intent terminal handoff replay, execution retry-key reuse after `max_steps` changes, a false-positive frontend assertion, and two static-analysis findings. The first ordinary timing wrapper was also found not to propagate a child exit code; the suite was rerun directly and passed. This was a test-orchestration issue, not a product defect. No unresolved high/medium issue is known. ADR 0038 records the authority boundary.

Metrics after D1-X1: architecture remains about 98% (V2 about 99%); complete-product usability is about 61-65%, generic coding-agent workflow usability about 56%, and Cyber autonomous workflow usability about 20%. The gain is an explicit resumable mock/Provider Agent loop, not host/container process execution or an automatic background scheduler.

## Completed Model, Plan, And Approval Controls (non-schema D1-M1/P1/A1)

D1-M1 replaces separate CLI/Desktop router construction with one Go-owned `modelregistry.Registry`. It registers mock plus valid environment-backed Mimo, DeepSeek, and Anthropic-compatible Providers, then loads the five persisted routes. `GET /api/v1/models` returns only bounded names, models, status, credential-source class, network-required/configuration-error booleans, and route availability. It contains no key, Base URL, environment-variable name, client, or raw error and performs no Provider request. Secret-like or malformed model/route identifiers are rejected or projected as unavailable/redacted.

D1-P1 exposes `plan_delivery_control.v1` as two distinct digest-idempotent POST operations. Direction selection reloads the exact proposal/Run and reuses the existing atomic selection, WorkItem, Note, and event path without changing phase. Deliver transition requires that immutable selection and reuses the Run-mode ledger. Neither operation starts/resumes the Run, obtains a lease, calls a model/tool, or grants authority.

D1-A1 exposes `approval_queue.v1` and `approval_control.v1`. The read queue contains at most 100 pending metadata items and excludes command, arguments, paths, file content, fingerprints, reasons, operations, and private authority. Decision reloads the exact Run, approval, and source; only pending nonterminal requests may change. Approve-once is limited to current-Policy-approved dry-run Shell and process-disabled ScriptProcess sources; replace-file can only be denied, permanent Policy denial cannot be overridden, and all process/Shell/Docker/write/Grant/capability outputs stay false. A committed same-key decision remains replayable after the Run becomes terminal, while a new terminal decision remains rejected.

Desktop adds independent `--enable-plan-delivery` and `--enable-approvals`; model availability remains read-only. The native Wails bridge stays exactly three methods. React adds a model dialog, explicit direction/Deliver controls, and an Approval tab with memory-only intent-bound retry keys and strict response authority validation. OpenAPI now has 36 paths, 84 schemas, 27 GET operations, and 11 control POST operations.

The ordinary integrated gate passes the final full Go suite in 310.1 seconds, focused Windows Desktop tags, all 73 frontend tests across 18 files, strict TypeScript, Vite and Windows production builds, and zero-vulnerability npm audit. The combined audit fixed secret-like model identifier projection, terminal replay of an already committed approval, missing frontend Session/Workspace approval binding, and one encoding defect. No Provider network call, real Shell/Local/Docker process, file write, Session Grant, installer, or external Skill execution occurred. No unresolved high/medium issue is known. ADR 0039 records the boundary.

Metrics after D1-A1: architecture remains about 98% (V2 about 99%); complete-product usability is about 64-68%, generic coding-agent workflow usability about 60%, and Cyber autonomous workflow usability about 20%.

## Completed Provider, Diff, And Wake Controls (schema v74 / D1-M2/D1-D1/D1-Q1)

D1-M2 adds explicit `provider_diagnostic.v1` plus persisted `model_route_control.v1`.
Route changes commit to SQLite before the concurrent-safe Router changes. Diagnostics run
only after an operator action and make at most one 15-second content-free/tool-disabled
request. Their DTO contains status metadata only and no model text, key, endpoint,
environment-variable name, client, or raw error.

D1-D1 adds exact Run/Mission/Session/Workspace FileEdit metadata, bounded redacted Diff,
and review-only `approve_intent|deny`. It never selects file bodies into HTTP and never
writes the workspace. The audit found the approval/edit two-transaction crash window;
same-outcome retry now repairs edit state after a committed approval, while opposite or
cross-bound decisions fail closed.

Schema v74 D1-Q1 adds digest-idempotent wake schedule/cancel, bounded attempts/backoff/
deadline, and one generation-fenced owner. Public state omits owner/lease identity and
fixes background/model/tool/execution authority false. No goroutine, service, automatic
Run transition, or Run execution lease exists. OpenAPI is 43 paths, 96 schemas, 30 GET,
and 16 control POST operations. ADR 0040 records the boundary.

The final six-slice robustness gate passes on the audited code. Full ordinary/race suites
took 278.6s/296.1s. Ordinary and secure-Desktop vet/staticcheck/govulncheck, module
verification/tidy, deterministic OpenAPI, 80 React tests across 20 files, strict
TypeScript, Vite/Windows production builds, zero-vulnerability npm audit, isolated CLI
smoke, and UTF-8/local-link/changed-credential/runtime-artifact/new-process-entry scans
are green. Focused route and wake race repetitions also pass. Audit fixes cover the
approval/edit crash window, body-free exact Diff SQL projection, expired-final-lease
event ordering, invalid delay/deadline combinations, and concurrent durable/in-memory
route ordering. No live key, network Provider, Shell, LocalRunner, Docker, or file apply
was used. No unresolved high/medium issue is known.

GitHub Actions run `29649564643` passed for implementation commit `37fbfbf`:
TypeScript console 30s, Windows Desktop shell 1m58s, and Go control plane 3m44s.

Metrics after implementation: architecture remains about 98% (V2 about 99%);
complete-product usability is about 67-71%, generic coding-agent workflow usability
about 63%, and Cyber autonomous workflow usability about 20%.

## Completed Foreground Wake, File Apply, And Inert Skill Installation (schema v75-v76 / D1-Q2/D1-D2/D1-B1)

Schema v75 adds `run_wake_consumer.v1`. An operator action claims one due intent and
routes at most eight steps through the existing durable handoff and RunSupervisor. The
claim, exact handoff binding, completion/failure, and events are restart-safe. A
crash-uncertain handoff without a result remains prepared, cannot be reclaimed after
lease expiry, and cannot be cancelled or failed as though no model call occurred.
There is no background goroutine, service, startup task, or hidden polling loop.

Schema v76 adds `file_edit_apply.v1`. Go reloads the exact Run/Mission/Session/
Workspace/proposal/approval, rechecks the running Run, active Session, current Policy,
and original/current SHA-256 at the final write boundary, then uses same-directory
staging and atomic replacement before verifying the proposed digest. One Edit admits
one apply operation and persists one idempotent result. Run-bound edits must use
`review-approve` followed by `apply`; the
legacy approve command cannot bypass the separate capability. HTTP and React receive no
path or file body.

Non-schema D1-B1 adds a fourth narrow native method, `InstallSkillPackage`, and one
independent HTTP control. Desktop consumes a short-lived confirmation handle; HTTP
accepts strict bounded canonical base64. Both call the existing content-addressed inert
Registry and require explicit untrusted-package confirmation. No content, script, hook,
command, tool, Provider, network request, Run selection, or context delivery runs during
installation. ADR 0041 records all three boundaries.

The final ordinary Go suite passes in 333.1s, along with focused race, Windows Desktop
tags, 85 React tests, strict TypeScript, deterministic OpenAPI/TypeScript, Vite/Windows
production builds, vet, module verification/tidy, npm audit, isolated CLI smoke, and
privacy/UTF-8/link/artifact/entry scans. The audit fixed prepared-wake reclaim/cancel,
failed-call fact binding, stale FileEdit recovery authority, duplicate apply operations,
and direct-truncation writes. No high/medium issue remains known. A forced kill before
atomic replacement may leave one redacted hidden staging file; D1-U1 owns this low-risk
recovery receipt and cleanup design. Current
metrics are architecture about 98% (V2 about 99%), complete-product usability about
70-74%, generic coding-agent workflow usability about 66%, and Cyber autonomous
workflow usability about 20%.

GitHub Actions run `29655417908` passed implementation commit `79f07fb`: the
TypeScript console completed in 28s, the Windows Desktop shell in 2m5s, and the Go
control plane in 3m57s. Remote checks included API drift, frontend tests/build/audit,
Desktop build/boundaries, module verification, the full Go suite, vet, and govulncheck.

## Completed Receipts, Workspace Explorer, And Portable Diagnostics (D1-U1/E1/W1)

D1-U1 adds one strict `operation_receipt.v1` projection to FileEdit apply, foreground
wake consumption, and inert Skill installation. It contains no operation key/digest,
path/body, model content, requester, or private lease. Go owns the closed outcome,
replay, retry, recovery, and cleanup tuple. FileEdit replays attempt conservative
cleanup only for an old ordinary reserved staging file in the exact target directory
whose full bytes match the approved proposal SHA-256; uncertainty is reported without
changing the durable apply result.

D1-E1 adds read-only `workspace_explorer.v1` and the Run Files tab. Go resolves the
registered root, accepts only canonical slash-separated relative paths, follows no
link or redirected component, scans at most 400 entries, returns at most 200, reads at
most 64 KiB of UTF-8, and caps redacted output at 128 KiB. The root and internal staging
names stay out of the DTO. Each result has non-authorizing `context_provenance.v1`;
React accepts only the exact child path derived from the current parent and name and
renders content as plain text.

D1-W1 adds `cyberagent doctor portable`, reproducible linker metadata, a repository-
contained/no-child-reparse-point build output boundary, consecutive SHA-256 builds,
and PE architecture/executable/zero-COFF-timestamp/hash/trimpath/module/non-installing
checks. Automated success and release approval are separate; the manual Windows 10,
WebView2, display, launch, and recovery matrix keeps `release_ready=false`.

The cumulative six-slice gate is green. Full ordinary/race suites passed in
294.0s/338.3s; ordinary and secure-Desktop test/vet, zero-warning staticcheck,
zero-finding govulncheck, module verification/tidy, 88 React tests across 22 files,
strict TypeScript, deterministic OpenAPI/TypeScript, Vite build, zero-vulnerability
npm audit, isolated mock-only CLI smoke, privacy/artifact scans, and the real Windows
double build passed. OpenAPI is 47 paths/106 schemas/31 GET/19 control POST. The double
build SHA-256 is
`33fb9ca3064df98191ac50b2a3ef9431e1b5c81abe8c610d4be15db113cdf1ef`.
No unresolved high/medium issue is known. No real Provider, LocalRunner, Shell, Docker,
network attack, installer, registry mutation, startup task, or updater was used.
GitHub Actions run `29658783000` passed implementation commit `5f0f397`: Go control
plane 5m49s, TypeScript console 32s, and Windows Desktop shell 2m11s.
Current estimates are architecture about 98% (V2 about 99%), complete-product usability
about 74-78%, generic Coding Agent workflow about 70%, and Cyber autonomous workflow
about 20%.

## Completed Workspace Search, Evidence Attachment, And Receipt History (schema v77 / D1-E2/C1/U2)

D1-E2 adds deterministic `workspace_search.v1` over redacted Explorer projections.
Queries are normalized and bounded to 128 Unicode code points; one request scans at
most 128 directories, 1,000 entries, 64 regular files, 50 results, and the declared
Explorer byte ceiling including UTF-8 look-ahead. Paths are canonical relative
references, links and internal staging are skipped, snippets are plain text, and all
provenance remains `instruction_authorized=false`. There is no index, watcher, daemon,
host root, raw-byte search, model call, or renderer filesystem authority.

Schema v77 D1-C1 adds immutable `session_evidence_attachment.v1`. The independently
gated HTTP/Desktop route accepts only an exact Run, Workspace reference, projected
SHA-256, protocol version, and memory-held idempotency key. Go reloads and exact-binds
Run/Mission/active Session/registered Workspace, reprojects the current file, then one
transaction stores the tool-role evidence message, event, and attachment. Go and the
SQLite trigger both require matching `context_provenance.v1` and false instruction
authority. On model projection the text is untrusted user evidence, so README content
addressed to automated assistants cannot become an operator instruction, approval, or
capability. Same-operation replay returns the durable snapshot; a new stale operation
conflicts.

D1-U2 adds `operation_receipt_history.v1`, a newest-first at-most-100 terminal view of
FileEdit apply, foreground wake, and inert Skill installation. It supports an exact Run
filter and emits opaque domain-separated IDs. Keys/digests, paths/content hashes,
requester/owner identity, archive metadata, and private leases stay internal. FileEdit
staging inspection is read-only; uncertainty stays `pending_review` and listing never
deletes a file.

The ordinary gate passed the uncached Go suite in 297.9s, Windows Desktop tag tests,
`go vet`, module verification/tidy, 92 React tests across 23 files, strict TypeScript,
deterministic OpenAPI/TypeScript, Vite build, zero-vulnerability npm audit, and a
isolated mock-only CLI smoke plus a reproducible Windows double build. OpenAPI is 50
paths/53 operations/112 schemas. The
unsigned binary SHA-256 is
`d187601e9e9d8cb0d4ee644e3c9aa1c7617905580b001ef7955dbc35b8c47af3`;
automated compatibility passed and release readiness remains false. The audit fixed
Unicode case-mapping offsets, the true UTF-8 look-ahead read ceiling, and schema-level
canonical source-reference enforcement. No unresolved high/medium issue is known and
no real Provider, Shell, LocalRunner, Docker, network request, API key, installer,
registry mutation, startup task, or updater was used. Current estimates: architecture
about 98% (V2 about 99%), complete-product usability about 78-82%, generic Coding Agent
about 74%, and Cyber automation about 20%. ADR 0043 is authoritative.

GitHub Actions run `29661764283` passed implementation commit `ffbdc72`: TypeScript
console 34s, Windows Desktop shell 2m21s, and Go control plane including govulncheck
3m48s.

## Completed Operator Actions, Evidence Inventory, And Command Palette (D1-O1/C2/K1)

D1-O1 adds read-only `operator_action_center.v1` for one exact Run. Go joins bounded
indexed pending steering, approval, FileEdit review/apply readiness, and due wake facts,
then revalidates Run/Mission/Session/Workspace and due time. At most 100 closed metadata
items use domain-separated opaque public IDs. Source rows, operations, requesters,
messages, commands, paths, Diff/content, leases, and authority fields remain private;
listing performs no decision or execution.

D1-C2 adds `session_evidence_inventory.v1` for immutable evidence already attached to
the exact Run-bound active Session and Workspace. It returns only closed source kind,
canonical reference, SHA-256, attachment time, and fixed false instruction authority.
It omits message identity/body, attaching operator, event sequence, private operations,
and capability state. React may send a Go-issued source reference only to the existing
Explorer, which rechecks its own Workspace/path boundary.

D1-K1 adds a static `Ctrl+K` command palette. Commands navigate existing Run tabs or
refresh current Run queries only. They submit no path, content, approval, operation,
capability, process, network, or secret value and persist no browser state.

The cumulative six-slice gate is green. Full ordinary/race suites passed in
319.6s/299.8s; ordinary/secure-Desktop tests, vet, zero-warning staticcheck,
zero-finding govulncheck, module verification/tidy, deterministic OpenAPI/TypeScript,
97 React tests across 26 files, strict TypeScript, Vite build, zero-vulnerability npm
audit, isolated mock-only CLI smoke, repository hygiene scans, and the real Windows
reproducible double build passed. OpenAPI is 51 paths/55 operations/116 schemas with
SHA-256 `B9CD79254D9AE09A2DB4BCC6268F04CA8F4ADD6C638E6BAA4DA42FC223A10181`.
The unsigned Desktop binary SHA-256 is
`a89b2357a5f1e7376ea8a533356028ccd5ea5eaec388b14d7623343fd041f520`.

Real-browser desktop/mobile auditing verified action navigation, evidence empty/source
states, palette filtering/Enter/Escape behavior, responsive containment, live SSE, and
stable connection count. It discovered and fixed a canonical event-version mismatch
(`event.v1` versus Go's `v1`) plus response-body leakage on failed reconnect. OpenAPI
now imports the Go version constant and generates a literal TypeScript type; every
parse/transport failure cancels the reader before reconnect. No unresolved high/medium
issue is known. No real Provider, API key, Shell, LocalRunner, Docker, external network,
installer, registry mutation, startup task, or updater was used. Current estimates:
architecture about 98% (V2 about 99%), complete-product usability about 80-84%, generic
Coding Agent workflow about 77%, and Cyber automation about 20%. ADR 0044 is
authoritative.

GitHub Actions run `29665187925` passed implementation commit `1151aaf`: TypeScript
console 36s, Windows Desktop shell 2m23s, and Go control plane 3m35s.

## Completed Go-Issued Editor, System Credentials, And Bounded Wake (D1-I1/M3/J1)

D1-I1 adds `file_edit_proposal.v1`. Go issues complete, untruncated, unredacted UTF-8
for an exact running Run/active Session/registered Workspace behind a 256-bit,
five-minute, one-intent source handle. The locally bundled lazy Monaco editor receives
no host path and submits only the handle plus proposed text. Go reloads bindings,
current hash, secret redaction, and Policy before creating a pending FileEdit. Proposal,
review, and apply remain independent; the editor cannot write a file.

D1-M3 adds `provider_credential.v1`. Windows stores exact `mimo`, `deepseek`, and
`anthropic` secrets in Credential Manager with the documented 2,560-byte generic
credential ceiling. TypeScript receives only configured/store/restart status with
`plaintext_returned=false`. Keys do not enter SQLite, events, logs, diagnostics, model
context, or browser persistence. Non-Windows builds fail closed without a plaintext
fallback; environment variables remain higher priority. A restart is intentionally
required before the Provider Registry uses a changed system credential.

D1-J1 adds `run_wake_worker.v1`. The worker starts only with
`--enable-wake-worker` and a distinct control token, owns one serial process-lifetime
loop, and consumes at most one due intent with `max_steps=1` through the existing
Foreground Wake Consumer/RunSupervisor. Durable wake ownership, budgets, Policy,
leases, checkpoints, and cancellation are unchanged. It owns no Tool Runner and has no
Shell, LocalRunner, Docker, or service/startup authority.

The ordinary integrated gate passed the final uncached full Go suite in 327.6s,
`go vet`, secure Desktop-tag tests, deterministic OpenAPI/TypeScript, strict
TypeScript, 102 React tests across 28 files, the Vite production build, and a
zero-vulnerability npm audit. A reproducible Windows double build produced SHA-256
`a0e6aa0a3d15ccc39712f8a0a64d7de06e4a6af426e060b6378b1011c93a1cf6` and correctly
kept `release_ready=false`.

The combined review fixed the Windows credential-size limit, exact Provider-name
normalization, one bad credential read disabling the whole application, worker restart
after Desktop close, FileEdit ID/intent drift after an uncertain save, internal errors
on post-review replay, a `models:null` contract failure for unconfigured Providers, a
secret-clearing frontend test race, and Monaco CDN/dependency risk. Monaco 0.53.0 and
all five workers now ship locally and lazily; desktop and 390x844 mobile UI smoke pass,
and npm reports zero known vulnerabilities. No unresolved high/medium issue is known.
No real key, Provider
request, Shell, LocalRunner, or Docker operation was used. Current estimates are
architecture about 99% (V2 about 99%), complete-product usability about 84-87%, generic
Coding Agent workflow about 82%, and Cyber automation about 20%. ADR 0045 is
authoritative.

GitHub Actions run `29671519260` passed implementation commit `ee36405`: TypeScript
42s, Windows Desktop 2m31s, and Go control plane 3m54s.

## Completed Safe Editor Recovery, Provider Generations, And Worker Health (D1-I2/M4/J2)

D1-I2 adds two distinct recovery paths. `ReissueSource` rotates an expired source
handle only when Go reloads the exact Run/active Session/Workspace and the current
file still matches the previously issued SHA-256; renderer draft text is absent from
that request. `file_edit_proposal_recovery.v1` revalidates a durable pending proposal's
binding, status, stored bodies, hashes, and current target, then returns a handle-free,
`editable=false` Diff. Stale or missing targets are reported, never rebased. The
closed `missing` original-hash sentinel is valid only for an empty original body.

D1-M4 makes the Provider Registry generation-safe. Credential changes build a complete
candidate Registry, load persisted routes, and finish every required credential read
before one atomic swap. Any candidate failure leaves the old generation active. A
Router call resolves its route and captures the immutable Provider under one read
lock, so an in-flight call completes on its old Provider while later calls use the new
generation. Status remains plaintext-free and reports reload/generation metadata;
successful reload needs no restart.

D1-J2 adds authenticated read-only `runtime_capabilities.v1` and
`run_wake_worker_health.v1`. The closed worker states are `disabled`, `ready`,
`running`, `draining`, and `stopped`, with concurrency/max-steps fixed at one. The
projection omits token, owner, lease, Run, operation, and private error, fixes process/
Shell/Docker authority false, and cannot enable a worker or install a service. All
public `RunOnce` calls serialize, reject nil context, and drain before stop. Enabling
the process-start worker now also requires a distinct control token at API construction.

The cumulative six-slice gate is green. Final uncached ordinary/race suites passed in
322.9s/352.8s, together with `go vet`, zero-warning staticcheck, zero-finding
govulncheck, module verification/tidy, secure Desktop tags, strict TypeScript, 108
React tests across 29 files, deterministic OpenAPI/TypeScript, Vite build,
zero-vulnerability npm audit, and a reproducible Windows double build. OpenAPI has 57
paths, 61 operations, and 125 schemas. The unsigned Desktop binary SHA-256 is
`30a3d9d19e02f32f8ea976fc071bc6942ed06fba3e7cad937310a78e46e74dfc` and correctly
remains `release_ready=false`.

The combined review fixed mixed route/Provider generations, candidate credential-read
failure replacing the active Registry, false concurrent-reload failure, inconsistent
list generations, worker construction without a control token, concurrent public
`RunOnce`, nil worker context, missing-file proposal recovery, and recovery-dialog
close/error behavior. Real-browser desktop/mobile smoke found no horizontal overflow
or console error. No unresolved high/medium issue is known. No real API key, Provider
request, Shell, LocalRunner, Docker, attack traffic, or external network was used.
Current estimates: architecture about 99% (V2 about 99%), complete-product usability
about 87-90%, generic Coding Agent workflow about 84%, and Cyber automation about 20%.
ADR 0046 is authoritative.

GitHub Actions run `29674460349` passed implementation commit `7d5736e`: TypeScript
console 38s, Windows Desktop shell 2m49s, and Go control plane 3m43s.

## Completed Repository State, Change Sets, And Code Journey (D1-G1/I3/F1)

D1-G1 adds pure-Go `repository_state.v1` for the exact registered Workspace root.
It uses `go-git/v5` without parent discovery, subprocesses, network, remotes, or hooks.
A redirected `.git` file and every symbolic link below real `.git` metadata fail
closed. The cancellable metadata walk is capped at 50,000 entries, status processing
at 10,000 entries, and output at 200 canonical relative paths. Secret-looking path or
branch data is omitted; host roots, bodies, remote configuration, and command output
never enter the projection.

D1-I3 adds exact-bound `file_edit_change_set.v1` over at most 100 existing FileEdit
previews. It returns metadata and Diff byte counts, not Diff content. Review and apply
remain independent per-file operations, atomic/batch mutation is explicitly false,
and mixed completion remains visible as partial state. D1-F1 adds a Code-only Journey
for Scope, Plan, Queue and execute, Review, and Verify and report. Its buttons navigate
existing Go-owned views; the component has no API client or composite mutation and is
not injected into Cyber mode.

The cumulative six-slice gate is green. The ordinary full suite passed in 321.7s
before audit hardening; the final-code full race suite passed in 490.4s, followed by
ten normal repository repetitions. Vet, zero-warning staticcheck, module verification,
zero reachable/imported-package govulncheck findings, secure Desktop tags, strict
TypeScript, 114 React tests across 31 files, deterministic OpenAPI/TypeScript, Vite,
zero-vulnerability npm audit, isolated mock-only CLI smoke, and a reproducible Windows
double build passed. OpenAPI has 59 paths, 63 operations, and 129 schemas. The unsigned
Desktop SHA-256 is `145757cb1a8bbafc9080fdc29f4ada69d34b850ca64f702310ea44578ca677a9`
and remains `release_ready=false`.

Govulncheck retains one module-only residual, `GO-2026-5932`, because the transitive
`golang.org/x/crypto/openpgp` package is unmaintained and has no fixed version. The
application neither imports nor calls it. The audit fixed nested Git-metadata link
reads and a strict-client false rejection when a terminal Run exposes a proposed
FileEdit with zero allowed actions. Browser desktop/mobile checks found no page-level
overflow or console error. No unresolved high/medium issue is known. No real API key,
Provider request, Shell, LocalRunner, Docker, Git hook, attack traffic, or external
network operation was used. Current estimates are architecture about 99% (V2 about
99%), complete-product usability about 89-92%, generic Coding Agent usability about
88%, and Cyber automation about 20%. ADR 0047 is authoritative.

GitHub Actions run `29678257802` passed implementation commit `d69a812`: TypeScript
console 43s, Go control plane 5m32s, and Windows Desktop shell 5m29s.

## Completed Repository Diff, Verification Evidence, And Code Handoff (D1-G2/V1/F2)

D1-G2 adds pure-Go `repository_diff.v1` over the exact registered Workspace root. It
uses the existing no-parent/no-link repository and Explorer boundaries, starts no
process, accesses no remote/network/hook, and returns no host root or raw unredacted
body. At most 50 changes are projected, with 64 KiB per patch and 512 KiB aggregate.
HEAD and Workspace text are secret-redacted before the unified patch is built;
binary, oversized, linked, and unavailable states remain closed metadata.

Schema v78 D1-V1 adds immutable `operator_verification_evidence.v1`. A distinct
control capability accepts one `pass|fail|unknown` observation and a memory-held
idempotency key, then Go redacts and exact-binds Run/Mission/active Session/Workspace.
The Store rechecks active Session status inside the same transaction that appends the
metadata event and immutable row. SQLite independently binds the event and fixes
command execution, model assertion, approval, and authority to false. Inventory is
bounded to 100; migration from v77 fabricates no evidence.

D1-F2 adds Code-only `code_handoff.v1`. It regenerates bounded Plan/WorkItem, steering
queue, FileEdit change-set, verification, pending action, and Finding-report reference
summaries from durable sources. Private messages, verification summaries, Diffs,
content, operations, requesters, and leases stay internal. An event high-water mark is
checked before and after assembly with four bounded retries, so a changing Run returns
conflict instead of a torn handoff. The response cannot mutate, resume, or execute.

The ordinary integrated gate passed the final uncached Go suite in 308.1s, including
Store in 299.8s. Desktop-tag tests, vet, targeted zero-warning staticcheck, module
verification/tidy, deterministic OpenAPI/TypeScript, strict TypeScript, 120 React
tests across 35 files, Vite production build, zero-vulnerability npm audit, repository
privacy/UTF-8/artifact/process-network scans, and a reproducible Windows double build
are green. OpenAPI has 62 paths, 67 operations, and 143 schemas with SHA-256
`652707A6D9CA72EBBD86B6FD407A382DFBE85B094927C82AFC3765D2648332B3`.
The unsigned Desktop SHA-256 is
`2ab74a47794287bac71877172136f02631b5cc9a44febd930e8ee7b1913ba93f` and remains
`release_ready=false`.

Production-bundle browser checks recorded one isolated pass observation, confirmed
the Handoff refresh and non-Git Repository empty state, found no page-level horizontal
overflow at desktop/mobile widths, and produced no console error. The combined audit
fixed an active-Session transaction race, a torn multi-source Handoff snapshot,
top-level Diff truncation under-reporting, strict-client reference/action/truncation
gaps, CR normalization drift, and missing React verification-capability propagation.
No unresolved high/medium issue is known. No real key, Provider, Shell, LocalRunner,
Docker, hook, attack traffic, installer, registry mutation, startup task, or external
network was used. Current estimates are architecture about 99% (V2 about 99%),
complete-product usability about 92-94%, generic Coding Agent usability about 92%,
and Cyber automation about 20%. ADR 0048 is authoritative.

GitHub Actions run `29682547524` passed implementation commit `cff7489`: TypeScript
console 42s, Windows Desktop shell 2m34s, and Go control plane including vet and
govulncheck 3m33s.

## Completed Deadlock And Livelock Runtime Guards (H1/H2/H3, Schema v79)

H1 wraps every enabled in-process Tool invocation in a default 15-second hard
deadline, capped at five minutes, with stable timeout/cancellation exits and panic
recovery. Built-in reads now check context in read/walk loops, constrain `max_bytes`
to the configured and platform limits, and open only regular files. Unix uses
nonblocking/no-follow descriptors; other platforms compare pre-open and opened-file
identity. FIFO, device, and socket reads fail instead of blocking.

H2 adds the bounded process-wide `waitgraph`: 4,096 active nodes, 8,192 active edges,
reference-counted idempotent release, and pre-insertion direct/indirect cycle checks.
Tool/Retriever/Store/Runner nodes cannot synchronously wait on an Agent. The root
Supervisor injects Agent identity, Specialist rounds record parent-to-child waits,
and Tool Gateway invocation propagates a unique Tool identity. Future RAG, Store
callback, Model, and Runner adapters must register their synchronous boundaries too.

H3/schema v79 adds `run_progress_guard.v1`. Three identical `continue` actions detect
`repeated_action`; six varying `continue` actions without selected structured-state
change detect `no_observable_progress`. Detection atomically commits metadata events,
Session messages, checkpoint, and `running -> paused`; original-action replay remains
exactly once. Only a later durable `paused -> running` event can reset a detected
guard. v78 upgrade creates no synthetic guard. Rows and events retain fingerprints,
counters, thresholds, and reasons, not model text.

The cumulative six-slice robustness gate is green. Final uncached Go passed in 312s
(Store 304.6s), final-code full race in 358s, Tool/wait-graph repetitions x20, and v79
Store repetitions x10. Ordinary/secure-Desktop tests and vet, zero-warning
staticcheck, module verification/tidy, zero reachable govulncheck findings, 120 React
tests in 35 files, strict TypeScript, deterministic API generation, Vite, zero npm
vulnerabilities, mock-only CLI smoke, credential/artifact scans, reproducible Windows
build, and desktop/mobile browser checks passed. The unsigned GUI SHA-256 is
`31e0df63d3fbbccac6728ad2322196bee55d57e775a15cc34f752c0632bdc699` and remains
`release_ready=false`.

The audit closed platform-integer/OOM read limits, observing-at-threshold corruption,
detected-to-observing reset without resume proof, corrupt-row fail-open behavior, and
counter overflow. The retained module-only `GO-2026-5932` package is neither imported
nor called. No enabled path has a known unresolved high/medium issue. No real key,
Provider, Shell, LocalRunner, Docker, attack traffic, or external network was used.
A timeout cannot forcibly kill an arbitrary third-party goroutine that ignores its
context, and real process-tree deadlocks remain for the independent Runner lifecycle
gate because process execution is still disabled. ADR 0049 is authoritative.

GitHub Actions run `29688544340` passed implementation commit `2012bfa`: TypeScript
console 42s, Windows Desktop shell 3m13s, and Go control plane including vet and
govulncheck 3m54s.

中文交接：本批把工具永久阻塞、同步依赖成环和“模型每轮都返回但一直无进展”分成三层
处理。内置 Tool 已可取消且不读特殊文件；root、child 与 Tool 的同步等待可在阻塞前拒绝
环路；v79 会把重复三轮或停滞六轮的 Run 原子暂停并要求操作者显式恢复。没有开放任何
新执行权限，双指标保持架构约 99%、产品可用度约 92-94%。

## Completed Repository History, Verification Plans, And Handoff Export (D1-G3/V2/F3, Schema v80)

D1-G3 adds pure-Go `repository_history.v1` over the exact registered Workspace root.
It follows at most 50 first-parent commits, scans at most 1,024 local branch references,
and returns at most 64 branches. Subjects are normalized, secret-redacted, and bounded.
Author identities, email, bodies, remotes, host roots, subprocesses, network, and hooks
are absent. Redirected or linked Git metadata fails closed, and hostile parent/omission
counters saturate at protocol bounds.

D1-V2/schema v80 adds immutable `operator_verification_plan.v1` with 1-32 ordered
operator-authored checks. It is exact-bound to one Code Run, active Session, Mission
Workspace, metadata event, operation/request digest, and content digests. Go and SQLite
fix it as guidance only with command execution, model assertion, result inference,
approval, and authority false. It remains separate from v78 verification outcomes.

D1-F3 adds bounded `code_handoff_export.v1` Markdown/JSON export. A stable
`code_handoff.v1` source supplies the event high-water mark; output is capped at
256 KiB and carries MIME, filename, UTF-8 byte count, and SHA-256. TypeScript verifies
all metadata, content digest, and Run/high-water binding before download. Export cannot
resume, mutate, accept a report, apply an edit, or execute.

The ordinary integrated gate passed uncached full Go in 334.6s, then post-audit
Repository/Application/Store/HTTP regressions and vet. All 124 React tests across 37
files, strict TypeScript, deterministic OpenAPI/TypeScript, and Vite production build
passed. OpenAPI is 65 paths/71 operations/155 schemas with SHA-256
`99887F651B563C56C87D19C5624EDD776AFC29AA6095EAB8C685E6767C165E7F`.
Chrome-extension production-bundle checks found no root/email disclosure, inferred
verification result, page overflow, console warning, or console error.

The audit fixed exact-limit plan inventory rejection, hostile Git metadata count
overflow, stale idempotency-key reuse after a failed plan intent was edited, and
premature download object-URL revocation. No unresolved high/medium issue
is known. No real key, Provider, Shell, LocalRunner, Docker, hook, attack traffic, or
external network was used. Current estimates: architecture about 99% (V2 about 99%),
complete-product usability about 93-95%, generic Coding Agent about 93%, and Cyber
automation about 20%. ADR 0050 is authoritative.

GitHub Actions run `29695882120` passed implementation commit `d70d96c`: TypeScript
console 43s, Windows Desktop shell 2m39s, and Go control plane including vet and
govulncheck 3m56s.

中文交接：Repository 现在能安全显示本地 first-parent 历史；Verify 把“准备检查什么”和
“实际观察到什么结果”分成不可变的两套事实；Handoff 可导出带事件高水位和 SHA-256 的
Markdown/JSON。三者都没有增加 Git mutation、自动验证、恢复、文件 apply 或进程执行权。

## Completed Exact Commit, Verification Association, And Runner Lifecycle (D1-G4/V3/R1, Schema v81)

D1-G4 adds pure-Go `repository_commit_detail.v1` for one exact lowercase SHA-1 object
at the registered Workspace root. It compares the commit tree with its first parent
and returns at most 200 canonical changed-path metadata entries. Author/email/body,
blob content, remote/root, checkout/ref mutation, subprocess, network, and hooks remain
absent. Redirected metadata, links, malformed trees, and missing objects fail closed.

D1-V3/schema v81 adds immutable
`operator_verification_plan_evidence_association.v1`. One evidence observation may
answer exactly one earlier plan item; one item may retain multiple contradictory
observations. Go, transactional Store checks, and SQLite triggers exact-bind the Code
Run, active Session, Workspace, plan/item/evidence, event, operation, and digest. The
coverage projection reports per-item pass/fail/unknown counts and unobserved state only;
it never infers an overall plan result.

R1 adds `runner_lifecycle_contract.v1` around a simulation-only backend: start/wait,
pre-cancellation, timeout, TERM/KILL grace, final inspection/reap, shared wait-graph
entry, partial-start cleanup, and orphan cleanup. It has no CLI, HTTP, Desktop, Agent,
LocalRunner, Docker, `os/exec`, or product capability wiring.

The cumulative six-slice gate passed final uncached Go in 509s and full race in 341s,
ordinary/secure-Desktop tests and vet, zero-warning staticcheck, module verify/tidy,
zero reachable govulncheck findings, 127 React tests in 37 files, strict TypeScript,
deterministic API generation, Vite, zero npm vulnerabilities, isolated mock-only CLI,
privacy/artifact checks, reproducible Windows build, and desktop/mobile Chrome checks.
OpenAPI is 68 paths, 74 operations, and 163 schemas with SHA-256
`CFAD160A85306B2602F95A62298828DB86BDFAAF6D55F47BA468860079C42E8D`. The generated
TypeScript schema SHA-256 is
`CCA5EF8B86E7F0D494E7B2BAF4FCA92FBE3FCB9C3A54E58D4A3C3B77028D5B73`. The unsigned
GUI SHA-256 is `77fb4d6fede1c1e3a0c3f3e9d39581e28f7a6880e0e25b222dcf0d3c701d1213` and remains
`release_ready=false`.

The browser gate recorded a plan, pass evidence, and explicit association, then
recovered `1/1 observed` and `1 linked` after reload with no page-level overflow or
console warning/error. The audit replaced a Git walker that silently skipped missing
subtree objects, fixed v81 downgrade-trigger cleanup ordering, saturated redaction
counters, constrained the OpenAPI control whitelist, and cleaned partial/invalid
Runner starts. No unresolved high/medium issue is known on an enabled path. The module
graph retains only unimported/uncalled transitive `GO-2026-5932`. No real key,
Provider, Shell, LocalRunner, Docker, hook, attack traffic, or external network was
used. Architecture remains about 99%, complete-product usability about 94-96%, generic
Coding Agent usability about 94%, and Cyber automation about 20%. ADR 0051 is
authoritative.

中文交接：Repository 可在不读取正文的前提下解释一个精确提交改了哪些路径；Verify 只在
操作者明确动作后把计划项与人工观察关联，矛盾证据也不会被抹平或推导成整体验证通过；
Runner 目前仅有 simulation-only 生命周期契约，真实宿主机与 Docker 进程仍完全关闭。

## Completed Conservative Context And Cumulative Handoff Memory (C1/C2/C3, Schema v82)

C1 replaces the rune/4 estimator with saturating conservative estimation: ASCII uses
the larger word or four-character estimate, while each non-ASCII UTF-8 byte counts as
one token. CJK, emoji, mixed text, tool calls/results, tool descriptions, and JSON
schemas now enter complete-request accounting.

C2 adds `model_context_window.v1`. The fallback is 32,768 total, 1,024 safety,
1,024 default output, and 4,096 maximum output tokens. Exact Provider/Model overrides
are part of atomic Router generations. Root and Specialist calls apply the gate before
every real Provider request. Only oldest ordinary Session history may be removed;
control, current input, memory, Skills, and tools remain mandatory and fail before the
Provider when oversized. The default is a conservative local policy, not a Provider
capability claim, and there is no user-facing override command yet.

C3/schema v82 changes one-shot context summaries into bounded cumulative
`handoff_memory.v1`. More than eight active messages still preserves the newest four;
the handoff stores at most 4,000 characters and 12 prioritized records. Exact previous
ID, row and record SHA-256, cumulative compacted/omitted counts, monotonic ordinals,
and a Session-message ID high-water survive repeated compaction. SQLite rejects update/delete/stale forks; Go validates on
read. Legacy v0 rows remain readable and fold into v1 as non-authoritative evidence.
Only provenance-confirmed operator messages or Go control may retain instruction
authority. Arbitrary `AGENTS.md`, README, repository, tool, and model text is not
automatically reloaded as control.

The uncached full Go suite passed in 348.5s, changed-package `go vet` passed, and
strict TypeScript plus 127 Vitest tests in 37 files passed. The audit fixed the original
loss of earlier summaries, separated handoff retention into a dedicated 12-record cap,
initialized zero-value Router maps, corrected v81 downgrade-fixture ordering, bounded/redacted source references, clamped clock rollback, and added message-high-water crash recovery. No unresolved high/medium issue is
known. No real key, Provider, Shell, LocalRunner, Docker, hook, attack traffic, or
external network was used. ADR 0052 is authoritative.

## Completed Exact Preview, Handoff Coverage, And Process Conformance (D1-G5/V4/R2)

D1-G5 adds `repository_commit_file_preview.v1`: exact registered root, exact lowercase
commit ID, exact canonical path, regular/executable UTF-8 only, 64 KiB input, 128 KiB
redacted projection, projected-content SHA-256, and non-authorizing provenance. It
returns no raw blob or root and performs no checkout, ref mutation, process, network,
remote, or hook action.

D1-V4 adds bounded verification coverage to Code Handoff and Markdown/JSON export. At
most 100 flat references expose only plan/item digests, explicit outcome counts, and
latest association sequence. Private bodies and aggregate result inference remain
absent; contradictions stay visible. Go and TypeScript both validate all totals,
digests, bindings, and false-authority fields.

R2 renames the lifecycle marker to `NonProductOnly` and adds Windows Job Object plus
Unix process-group conformance in `_test.go` only. Tests start the current Go test
binary and prove cooperative termination, forced kill, and child cleanup after parent
exit. Product CLI/HTTP/Desktop/Agent/Sandbox/Local/Docker paths cannot construct these
adapters.

The full six-slice gate passed ordinary Go in 380 seconds, race in 411.2 seconds,
post-audit focused ordinary/race regressions, ordinary/secure-Desktop checks,
vet/staticcheck/module/govulncheck, all 127 Web tests, strict TypeScript, deterministic
API generation, Vite, zero npm vulnerabilities, isolated mock-only CLI, privacy and
process-entry scans, Linux cross-compilation, and a reproducible Windows build. The
final unsigned GUI SHA-256 is
`44d54bf9d50b7cd99b89f5089833823ce0337bb0e0158ec16ef6aa9a5b415614` and remains
`release_ready=false`. The audit fixed platform-width count addition, negative/empty
aggregate event facts, and duplicate plan/count acceptance. No enabled path has a
known unresolved high/medium issue. ADR 0053 is authoritative.

## Completed Exact File History, Verification Drilldown, And Exit Evidence (D1-G6/V5/R3)

D1-G6 adds `repository_file_history.v1`: one exact canonical path at one registered
Workspace root, current HEAD, first-parent history, at most 512 scanned commits and 50
returned changes. It returns only object/time/status/mode metadata and a bounded,
redacted subject. Raw blobs, patches, author/body/remote/root data, rename inference,
checkout/ref mutation, subprocesses, network, and hooks remain absent.

D1-V5 adds `operator_verification_plan_item_coverage.v1`. One exact Run, plan, and
ordinal returns bounded immutable association metadata with explicit pass/fail/unknown
outcomes. Guidance/evidence bodies, operator identity, aggregate verdicts, model or
command execution, approval, and authority remain absent. Go, SQLite, HTTP, and strict
TypeScript independently validate ownership, digests, counts, ordering, uniqueness,
truncation, and false-authority fields.

R3 adds internal `runner_exit_evidence.v1` to `NonProductOnly`. Only after a process
tree is proven reaped may the test boundary report exit code plus per-stream observed
bytes, a 64 KiB captured-prefix count/SHA-256, and truncation metadata. Raw output is
never returned. No product starter, CLI/HTTP/Desktop route, profile, approval, Local,
Docker, or Agent capability can construct it.

The ordinary three-slice gate passed uncached `go test ./...` in 373.3 seconds,
focused race tests, `go vet`, affected-package staticcheck, module verification/tidy,
37 Web files with 127 tests, strict TypeScript, deterministic OpenAPI/TypeScript,
Vite, zero npm vulnerabilities, Desktop-tag tests, Linux runner-test cross-compilation,
and reproducible Windows builds. OpenAPI is 71 paths/77 operations/170 schemas with
SHA-256 `C78A701600F8535A9C2398C12B3AAA7A695A93AD58913010D8904ADEED121625`;
the TypeScript schema SHA-256 is
`977B8EEE7E9A268040453E0ADFB6FFB4C58489D4B90B94177473DC4B882E4740`.
The final unsigned GUI SHA-256 is
`c96047d7f3ea0afbe3b2f54f1c4ded197a861b29d644cb2edb449c8b3e46b031` and remains
`release_ready=false`.

The combined audit fixed non-monotonic Git commit-clock rejection and two verification
coverage validation gaps: duplicate count rows on non-selected items, and inconsistent
or duplicate truncated associations. No enabled path has a known unresolved high/medium
authority issue. No real key, Provider, Shell, LocalRunner, Docker, hook, attack traffic,
or external network was used. ADR 0054 is authoritative.

## Completed History Navigation, Verification Pagination, And Runtime Evidence (D1-G7/V6/R4)

D1-G7 lets each exact-file-history row reuse the existing exact-commit detail route and,
for regular/executable files only, the existing redacted exact-commit preview route.
React submits only Go-projected Workspace/object/path identities. Deleted, symlink, and
submodule rows cannot request previews; raw Git, mutation, process, network, and hooks
remain absent.

D1-V6 adds shared strict `limit` plus route-scoped opaque `cursor` pagination to one
exact verification item. SQLite uses immutable event/ID ordering with one-row lookahead;
Go revalidates ownership, digests, aggregate counts, exact page size, ordering,
uniqueness, and outcomes. The 100,000-row offset window is bounded. React explicitly
loads 25 older references and rejects cross-page aggregate/high-water drift, duplicate
IDs, and non-descending events. This remains a live projection with no private bodies,
operator identity, aggregate verdict, mutation, approval, command/model, or authority.

R4 adds `runner_runtime_evidence.v1` to internal `NonProductOnly`. After tree reaping it
can describe bounded stdin count/digest with closed/non-inherited/no-raw facts, captured
stdio with zero extra/inherited descriptors and no names/paths, and bounded wall/parent
CPU/optional peak-resident metadata with no raw/network telemetry. Exit and runtime
evidence use independent bounded contexts and commit atomically only after both validate.
Malformed or changing evidence returns `StopEvidenceFailed`. OS adapters remain
`_test.go` only and no product process starter or wiring exists.

The cumulative six-slice gate passed final ordinary/race Go in 377.3/409.8 seconds,
ordinary/secure Desktop test and vet, full vet/staticcheck, both govulncheck paths,
module verify/tidy, 37 files/127 Web tests, strict TypeScript, deterministic OpenAPI/
TypeScript, Vite, zero npm vulnerabilities, isolated mock-only CLI, credential/artifact/
process-entry scans, Linux runner-test cross-compilation, and reproducible Windows dual
build. OpenAPI remains 71/77/170; hashes are
`7418F7CAEED0BA6A5E69E574215F22CC8AA47458A75FB70FD0679FDEDD332BA1` and
`A3EE3B6E7E1020924B6AB1140F3EC4176A9550407187B7DDB3AA1E6FA15697CD`.
The unsigned GUI SHA-256 is
`1d51529b1a6d7d90e121e770faa54c9f4d77b4a96d3c0d920fe091178a299da2` and remains
`release_ready=false`. The audit found no unresolved high/medium issue on an enabled
path. No real Provider/key, Shell, LocalRunner, Docker, hook, attack traffic, external
network, installer, registry mutation, or product process start was used. ADR 0055 is
authoritative.

## Completed Exact Commit Comparison, Snapshot Pagination, And Runner Control Evidence (D1-G8/V7/R5)

D1-G8 adds `repository_commit_comparison.v1` for two exact lowercase local commit
objects in one registered Workspace. The pure-Go bounded tree projection does not
require ancestry and returns only redacted subject/time plus canonical change/kind/
content-change/mode-change metadata. Author/body/blob/patch/remote/root/rename
inference, checkout/ref mutation, process, network, hooks, and authority are absent.
React uses a Workspace-bound two-step base/head selection and strict TypeScript checks.

D1-V7 replaces the exact verification item's offset/live pagination with a frozen
association-event high-water and descending `(event_sequence, association_id)` keyset.
The route-scoped opaque cursor also carries the consumed count; Store and application
logic recompute the anchor rank inside the snapshot before serving the next page. Later
appends do not shift loaded pages. The 100,000-row window remains hard-bounded and ends
with `page.truncated=true` when more snapshot rows exist. Private bodies, identity,
aggregate verdict, mutation, execution, approval, and authority remain absent.

R5 adds internal `runner_resource_limit_evidence.v1` and
`runner_termination_cause_evidence.v1`. They distinguish configured wall timeout and
grace values from unconfigured/unverified CPU, memory, and OS quotas, then bind one Go
control trigger to wait/terminate/kill without inferring an OS signal or cause. All four
post-reap evidence records validate and commit atomically; repeated drift fails closed.
Concrete OS adapters remain test-only and no product process starter exists.

The integrated batch gate passed uncached Go in 391.1 seconds, focused race, full vet
and zero-warning staticcheck, module verification, ordinary/secure Desktop, 37 files/
128 Web tests, strict TypeScript, deterministic OpenAPI/TypeScript, Vite, zero high npm
vulnerabilities, and reproducible Windows dual build. OpenAPI is 72/78/171; hashes are
`839C731B4D96B9F60A1EB26A0178D0C6212282C0E1287CFC233E7C6AA9520373` and
`741D882B9213C7AE8244FA90648D08FC351A547334E44CCCA3319C439D4E4D9F`.
The unsigned GUI SHA-256 is
`748411c3b3dfd56768c814fd06b6da7e5e81dcd636ad69b658d862afca313e01` and remains
`release_ready=false`. No known unresolved high/medium issue exists on an enabled path.
No real Provider/key, Shell, LocalRunner, Docker, hook, attack traffic, external network,
installer, registry mutation, or product process start was used. ADR 0056 is
authoritative.

## Completed Comparison Preview, Verification Snapshot, And Runner Timeline Evidence (D1-G9/V8/R6)

D1-G9 lets a comparison row reuse the existing exact redacted preview independently for
its base and head regular/executable entries. Added and deleted paths expose only the
side that exists; links, submodules, and absent entries stay unavailable. Selection is
Workspace/object/path-bound and the rendered header repeats the exact returned hash and
path. No new Go Repository route or authority was added.

D1-V8 adds deterministic `operator_verification_plan_item_snapshot_export.v1`
Markdown/JSON downloads. The service freezes the current exact-item association-event
high-water through the existing detail boundary, includes at most 100 descending
metadata references, explicit outcome counts/truncation, exact Run/Session/Workspace/
plan/item digest bindings, content SHA-256/bytes, safe filename/MIME, and a 256 KiB cap.
It includes no private bodies or operator identity, infers no result, persists no
acceptance, grants no approval/authority, and starts no execution. TypeScript validates
both envelope and content before download.

R6 adds internal `runner_lifecycle_timeline_evidence.v1` and
`runner_deadline_budget_evidence.v1`. The former records only logical control ordinals;
the latter records independent Go context ceilings and actual-path applied flags. They
claim no wall-clock/backend duration, process identity, cumulative deadline, CPU/memory
limit, or OS enforcement. Six post-reap records validate and assign atomically;
concrete adapters remain test-only and no product starter exists.

The cumulative six-slice robustness gate passed ordinary/race Go in 387.3/395.8
seconds; ordinary/secure Desktop test/vet/staticcheck/govulncheck; full vet/staticcheck;
module verify/tidy; 37 files/129 Web tests; strict TypeScript; deterministic OpenAPI/
TypeScript; Vite; zero high npm vulnerabilities; isolated mock-only CLI; privacy and
product-entry scans; Linux Runner test-binary cross-compilation; and reproducible
Windows build. OpenAPI is 73/79/172; hashes are
`8FF7E6A39132ED46DA828009D6D7A603D05B862AEA734E4D8C3E13838DD8A8AE` and
`FE852EEC8B561D14BD5C3FD1411B2F1930C8A80F15551D071DD3356236BA3503`.
The unsigned GUI SHA-256 is
`7aa5c3bf67a0af12e51e396977632e5dcc21c74dc04411d3fec7b6f09719aeef` and remains
`release_ready=false`.

Audit found and fixed one low-risk query-normalization regression: export formats are
now exactly one byte-exact value, and missing/duplicate/whitespace forms fail closed for
both snapshot and existing Handoff exports. No known unresolved high/medium issue exists
on an enabled path. No real Provider/key, Shell, LocalRunner, Docker, hook, attack
traffic, external network, installer, registry mutation, or product process start was
used. ADR 0057 is authoritative.

## Completed Paired Comparison, Snapshot Receipts, And Runner Evidence-Set Digest (D1-G10/V9/R7)

D1-G10 adds one explicit paired redacted preview workspace over the existing exact-file
endpoint. Selection is bound to Workspace/base/head/path; both panes repeat the Go-
returned hash/path, while added/deleted files render one explicit absent side. No Git
route, raw blob/patch, checkout, process, network, hook, or authority was added.

D1-V9/schema v83 adds immutable
`operator_verification_plan_item_snapshot_receipt.v1` history. Application rebuilds the
current deterministic export; Store takes a Run writer lock and rechecks active Code
Session, Workspace, plan/item digests, association high-water/counts, and truncation
before atomically inserting the metadata-only event and receipt. The table retains no
snapshot body, rejects update/delete, and upgrades v82 without fabricated rows. Public
history is bounded to 100, omits private recorder identity, and fixes snapshot/result
acceptance, inference, rewrite, approval, authority, and execution to false. Download
and receipt remain separate UI actions labelled `record only`.

R7 adds `runner_evidence_set_receipt.v1` over the fixed six-record post-reap tuple. A
map-free bounded canonical JSON body is hashed and discarded; Result retains only the
protocol list, SHA-256, bytes, and explicit false claims for wall-clock ordering, raw
output, process identity, OS enforcement, and product execution. All records plus the
receipt validate before atomic assignment; product wiring remains absent.

The three-slice functional gate passed uncached Go in 394.1 seconds, full vet, focused
tamper/migration/HTTP/UI/Runner regressions, Desktop boundary tests, 37 files/129 Web
tests, strict TypeScript, Vite, deterministic OpenAPI/TypeScript, and reproducible
Windows build. OpenAPI is 74/81/176 with hashes
`7E50A343391F167989E871828B1494F45E3A02581198D5B880C3FC3E795B521D` and
`A693C4E62D65B7D39A5E5668EA319F57E97613AA61234B0658ABC9CBF80F9334`.
The unsigned GUI SHA-256 is
`d5e37e193223a41939598edceb77a92637430b0c87c52233cdafb9c2fda10bb5` and remains
`release_ready=false`.

Audit fixed three low-risk contract/verification issues: an embedded control DTO was
made explicit for the reflection generator, the inventory protocol gained its exact v1
enum, and the authenticated live-route fixture now builds an exact valid receipt
request. The first remote Go run exposed the fixture omission; the corrected local full
suite explicitly propagated `go test` exit code 0 and the focused route passed five
times. No known unresolved high/medium issue remains on an enabled path. No real
Provider/key, Shell, LocalRunner, Docker, hook, attack traffic, installer, registry
mutation, or product process start was used. ADR 0058 is authoritative.

## Completed Paired Navigation, Receipt Reviews, And Runner Golden Vectors (D1-G11/V10/R8)

D1-G11 adds previous/next navigation over only the bounded exact comparison changes
that already have an available regular or executable side. It rebuilds the same exact
Workspace/base/head/path selection and reuses the existing two redacted preview calls.
Absent sides issue no request. No Git route, raw content class, mutation, subprocess,
network, hook, host-root projection, or authority was added.

D1-V10/schema v84 adds immutable
`operator_verification_plan_item_snapshot_receipt_review.v1`. One exact receipt may
receive one `metadata_confirmed|metadata_disputed` decision after explicit confirmation
that the review is non-authorizing. Application and Store bind the exact receipt ID,
content digest, receipt event sequence, Run, active Code Session, Workspace, latest
mode, operation digest, request fingerprint, review event, and chronology. One
transaction appends event plus row; SQLite rejects update/delete and v83 upgrade
fabricates no history. Exact intent replays, while changed intent, a second review, or
stale/cross-Run metadata fails closed.

Private reviewer identity remains Store-only. Public Go/OpenAPI/TypeScript views fix
private body/identity inclusion, snapshot/result acceptance, result inference, rewrite,
approval, authority, and execution to false. React locks the receipt from the successful
mutation response even if a later inventory refresh fails. A confirmed metadata review
is not a test pass, approval, release decision, or execution grant.

R8 stores two cross-platform golden descriptors for the existing non-product
`runner_evidence_set_receipt.v1`: normal empty exit and forced timeout with bounded
truncated metadata. Windows and Linux tests reconstruct the six typed records and pin
canonical byte count, SHA-256, protocol order, and false semantic claims. No raw output,
environment, process identity, canonical runtime body, or product starter is added.

The cumulative six-slice robustness gate passed final ordinary/race Go in 357.6/383.4
seconds; ten focused race repetitions; ordinary and secure-Desktop tests/vet;
zero-warning staticcheck; two zero-reachable-finding govulncheck paths; module verify/
tidy; 37 files/130 Web tests; strict TypeScript/Vite; zero npm vulnerabilities;
deterministic OpenAPI/TypeScript; and reproducible Windows dual build. OpenAPI is
75/83/180 with SHA-256
`F7978C6BBBA216C20A082520BA2B4885D1B16B3D4F42934E3FE328DE1367B075`; generated
TypeScript is
`E888605EB32D47CCDA045646D84F94AF1BACEC19EBCB976071F1AA2443225112`. The unsigned
GUI SHA-256 is
`3bbf545b5ee07597d32345a8dce4f49f063475d881b164a18abf00fd5ff9bc6f` and remains
`release_ready=false` pending the manual Windows 10/WebView2 matrix.

Audit fixed four low-risk issues: event-identifier formatting churn, stale review UI
after a successful mutation plus failed refetch, signed-sequence pre-overflow rejection,
and v84 trigger removal ordering in historical downgrade fixtures. No known unresolved
high/medium issue exists on an enabled path. No real Provider/key, Shell, LocalRunner,
Docker, hook, attack traffic, external network, installer, registry mutation, or
product process start was used. ADR 0059 is authoritative.

## Completed Keyboard Preview, Handoff Review Projection, And Receipt Compatibility (D1-G12/V11/R9)

D1-G12 makes the existing paired redacted preview region focusable. Opening a preview
moves focus into it; plain left/right arrows navigate only the bounded comparison
candidates, Escape or the close button dismisses it, and focus returns to the exact
trigger. Modified shortcuts and out-of-range movement are ignored. No Repository API,
Git authority, process, network, hook, or raw-content class was added.

D1-V11 projects existing schema-v84 receipt reviews into the regenerable Code Handoff
and its Markdown/JSON exports. The projection carries confirmed/disputed totals and at
most twenty exact review/receipt references with digest, event sequences, decision, and
time. Go independently verifies exact Run/Session/Workspace binding, uniqueness, and
descending chronology. Reviewer identity, operation keys, private bodies, acceptance,
inference, rewrite, approval, authority, and execution remain absent or false.

R9 adds an internal side-effect-free 8 KiB compatibility decoder for
`runner_evidence_set_receipt.v1`. Exact fields, protocols, types, UTF-8, one complete
object, canonical record matching, digest, and false authority claims are required.
One valid baseline and eleven malformed/future/widening rejection vectors run in
ordinary and Windows CI; no product Runner starter or import path exists.

The three-slice functional gate passed uncached Go in 403.0 seconds, repeated focused
HTTP/Runner checks, vet, 37 files/130 Web tests, strict TypeScript, Vite, zero high npm
vulnerabilities, deterministic 75-path/83-operation/182-schema generation, secure
Desktop boundary tests, and a reproducible Windows build. OpenAPI SHA-256 is
`e637623b6931c88466fd8d04412da31091af36c9e57161dc7d6c3784f64f56a3`; the unsigned GUI
SHA-256 is `a02843a00fc050d9eee51426fc460a0b40eb3413a256c6fb855a838b562c9a72`, with
`release_ready=false` pending the manual Windows 10/WebView2 matrix.

Audit found and fixed four low-risk issues: missing exact receipt digests in Markdown,
missing independent projection uniqueness/order checks, incomplete false-authority
validation in the embedded JSON parser, and a missing OpenAPI digest pattern. No known
unresolved high/medium issue exists on an enabled path. No real Provider/key, Shell,
LocalRunner, Docker, hook, attack traffic, external network, installer, registry
mutation, or product process start was used. ADR 0060 is authoritative.

## Completed Exact Review Navigation, Journey Audit Facts, And Envelope Golden Vectors (D1-G13/V12/R10)

D1-G13 adds an icon-only Handoff-to-Verify navigation target containing the existing
seven immutable review-reference fields. Verify independently matches the strict review
inventory, exact receipt ID/digest/event, and current verification plan/item digests
before expanding and focusing the receipt. Missing, truncated, stale, or drifting data
shows a bounded unavailable state and never falls back to a nearby record. The target is
component-memory-only and is cleared after leaving Verify; it grants no mutation,
acceptance, approval, resume, or execution authority.

D1-V12 adds at most three receipt-review facts to Code Journey from the existing strict
`code_handoff.v1` projection. Confirmed/disputed totals, event/time metadata, metadata-
only/non-authorizing labels, and source truncation remain visible. Clicking a fact uses
the same exact Verify matching path. No reviewer identity, body, operation key, model
assertion, browser persistence, new API, or capability is added.

R10 adds two accepted encoded-envelope vectors for the internal non-product
`runner_evidence_set_receipt.v1`: normal empty exit and forced timeout. Both are exactly
660 bytes and have pinned SHA-256 values. Strict decode, typed-record compatibility, and
byte-identical re-encoding are required. The test selector is shared by Linux and Windows
CI and has no product import, network, subprocess, Docker, or Runner starter connection.

The cumulative six-slice robustness gate passed ordinary/race Go in 421.0/509.5 seconds;
full vet, zero-warning staticcheck, module verify/tidy, and two govulncheck paths with zero
reachable findings; 37 files/134 Web tests, strict TypeScript, deterministic API
generation, Vite, zero npm vulnerabilities; secure Desktop tests/vet; and a reproducible
Windows dual build. OpenAPI remains 75/83/182 with SHA-256
`e637623b6931c88466fd8d04412da31091af36c9e57161dc7d6c3784f64f56a3`; generated
TypeScript SHA-256 is
`339498e9d72c1fc70b44bf5e799996e255368d89a45b80989e4a31d6015f8578`. The unsigned
GUI SHA-256 is
`7ae75f36c2291fbf9e7d9e72071ae8d8534f4e27dd56c6d34bd04dc064f47a19` and remains
`release_ready=false` pending the manual Windows 10/WebView2 matrix.

Review found and fixed five low-risk state/diagnostic issues: a theoretically ambiguous
focus key, missing current plan/item digest cross-binding, stale target reuse on ordinary
Verify navigation, hidden source truncation at exactly three items, and a visual focus
marker surviving later projection drift. No known unresolved high/medium issue exists on
an enabled path. No real Provider/key, Shell, LocalRunner, Docker, hook, attack traffic,
external network, installer, registry mutation, or product process start was used. ADR
0061 is authoritative.

中文交接：Handoff/Journey 现在只能把有界复核元数据当作“待核对引用”送进 Verify；Verify
独立核对 review、receipt、plan/item digest 后才聚焦，任何漂移都失败关闭。R10 只固定内部
纯函数信封的 660-byte/SHA，不是产品导入或执行入口。schema/OpenAPI 未变，真实执行继续关闭。

## Completed Go-Owned Analyzer Protocol And Rust Fixture (P10-A1/A2/A3)

P10-A1 adds `internal/analyzer` as a pure Go protocol owner. It fixes strict
`analyzer_protocol.v1`, `analyzer_result.v1`, and `analyzer_error.v1` envelopes; 96 KiB
request, 64 KiB decoded-input, 16 KiB result, 100-30,000 ms declared-timeout, safe-ID,
media-type, canonical-Base64, and eight-level JSON limits; four explicitly false
capabilities; and fourteen stable error codes. Duplicate, unknown, missing, trailing,
future, oversized, malformed, or authority-widening input fails closed without reflecting
parser details. The package has no process starter or product imports.

P10-A2 adds the first Rust workspace and `cyberagent-analyzer-fixture`. Rust 1.97.1,
Cargo 1.97.1, and the locked `clap`/`serde`/`serde_json`/`base64`/`sha2` dependency graph
implement bounded stdin JSON -> stdout JSON only. Success emits media type, byte count,
SHA-256, UTF-8, and line count without raw content. No path, URL, command, environment,
model, Run, Session, persistence, or capability input exists.

P10-A3 makes Go and Rust independently validate one shared five-vector contract. The
file pins every version, limit, exit code, error code, expected JSON, stdout byte count,
and stdout SHA-256. CI adds a separate Rust fmt/locked-test/zero-warning-clippy job, while
the Go job explicitly validates the same vectors before the full suite.

The first three-slice functional gate passed full uncached Go in 394.6 seconds, vet, more
than one million Go protocol fuzz evaluations, five Rust unit tests plus one shared-vector
integration test, fmt, clippy, success/denial fixture CLI smokes, 37 files/134 Web tests,
deterministic OpenAPI, Vite, npm audit, RustSec, secure Desktop tests/vet, and a reproducible
Windows dual build. The unsigned GUI SHA-256 is
`69ed40aede0cfc23e075df824fecf6c1ef7b4b0586a8f4b685b7d8aa95dde3b4` and remains
`release_ready=false`. RustSec loaded 1,166 official advisories and found zero known
vulnerabilities among 41 locked crate dependencies. Review fixed shared-
constant coverage, explicit-false parity, result line-count validation, oversized error
classification, and recursive JSON-depth bounds. No known unresolved high/medium issue
exists on an enabled path. ADR 0062 is authoritative.

中文交接（P10-A 时点）：Rust 已经开始，但当时只有开发期确定性摘要夹具。Go 仍是唯一主控，
Rust 只处理内联 Base64 并输出有界元数据；当时没有 Registry、产品桥接、文件读取、Run/Event/
SQLite/Artifact 写入或任何执行授权。schema 仍为 v84，OpenAPI 仍为 75/83/182。

## Completed Inert Analyzer Registry And ZIP Inventory (P10-B1/B2/B3)

P10-B1 adds the fixed Go-owned `analyzer_descriptor.v1` Registry. It lists only
`archive.zip.inventory.v1` and `fixture.digest.v1` in deterministic ID order, returns
clone-isolated descriptors, and exposes no registration API. Exact request/result
protocols, media types, and limits are fixed. Filesystem/network/subprocess/environment
and product-invocation/process-start/file-input/Artifact-commit values are all false.
There is no executable, command, path, URL, or starter field.

P10-B2 adds the strict Go `archive.inventory.v1` reference and validator. It reads only a
maximum-64-KiB inline ZIP central directory and never calls `File.Open`. Hard limits are
32 entries, 128 bytes per name, 2,048 total name bytes, and 16 KiB result JSON. Declared
8 MiB per-entry size, 32 MiB aggregate size, and 100:1 compression ratio become bounded
metadata risks without allocating or decompressing declared content. Eight sorted risk
codes cover path, duplicate, directory-data, size, and ratio hazards. The decoder
recomputes all counts, saturated totals, integer ratios, risks, and false semantic claims.

P10-B3 pins MIT-licensed `rawzip 0.5.1`, which has no transitive dependency and forbids
unsafe code. Rust uses only `ZipArchive::from_slice` and central-record iteration. Go and
Rust independently verify five exact benign/traversal/duplicate/oversized/ratio ZIP
vectors, output semantics, bytes, and SHA-256; neither implementation invokes the other.

The cumulative six-slice gate passed full ordinary/race Go in 380.2/401.5 seconds,
twenty additional analyzer race repetitions, about 12.99 million fuzz evaluations,
vet/staticcheck/govulncheck, module
verify/tidy, seven Rust unit and two shared-vector tests, fmt/clippy, 37 files/134 Web
tests, strict TypeScript, deterministic 75/83/182 OpenAPI, Vite/npm, secure Desktop, and
a reproducible Windows dual build. The unsigned GUI SHA-256 is
`871c6270de44f3d6aecd31064127cdbfb400c5d6e6936e44698bcc30b0c611db`; release readiness
remains false. RustSec loaded 1,166 official advisories and scanned 42 locked crate
dependencies with zero known vulnerabilities. Review fixed strict empty-array round-trip,
hostile-size saturation/invalid-name classification, Rust warnings, a deprecated test
helper, and declared-CRC evidence naming. No known unresolved high/medium issue exists on
an enabled path.

中文交接：Go 现在有一个不可变、无启动器的两项 analyzer 目录；Rust 可以确定性列出内存 ZIP
中央目录并标注风险，但产品仍无法调用它。没有路径输入、解压、文件写入、Go `os/exec`、Run/Event/
SQLite/Artifact 接线或新授权。schema/OpenAPI 仍为 v84 与 75/83/182，边界见 ADR 0063。

## Completed Prayu Brand And Dual-Surface Desktop Shell (D1-UX1/UX2/UX3)

D1-UX1 makes Prayu the user-visible identity through one Go-owned product constant and
the React shell. Established `cyberagent` CLI/module/data/environment/HTTP/credential/
Windows compatibility identifiers remain unchanged, so no schema, database, credential,
or script migration occurs.

D1-UX2 installs the exact supplied workspace background and wordmark plus a distinct
approved Settings background. Selected task, Run, and Settings rows use the same warm dark
base, CSS-generated right-side orange brush, orange icon, and cream text; no screenshot crop
is used as a selected-state asset. The main workspace is a cream 90%-opaque surface rather
than a second nested card.

D1-UX3 adds working workspace/Settings switching, Settings navigation, bounded API/schema/
version/surface/capability facts, a display-only comfortable/compact preference, and
responsive desktop/mobile geometry. TypeScript receives no new credential, Policy, model,
tool, filesystem, Shell, Docker, subprocess, network, approval, or execution authority.

The three-slice gate passed 400.5 seconds of uncached full-repository Go tests, full vet,
38 files/137 React tests, strict TypeScript, deterministic OpenAPI regeneration, Vite,
zero-vulnerability npm audit, 7+2 locked Rust tests, fmt/clippy, secure Desktop test/vet,
and a reproducible Windows dual build. The unsigned Desktop SHA-256 is
`0b294a9759e216c918775f05710148da6d45cde0e4e443e773894ecad6801a9b` and
`release_ready=false`. The dependency audit initially failed closed on `js-yaml 4.2.0`;
the narrowly scoped Redocly transitive override to fixed 4.3.0 passed API generation,
all Web tests, build, and a zero-vulnerability re-audit.

Visual QA used only an in-memory read-only fixture at 1440x900 and 390x844. It confirmed
the CSS selected-state brush, distinct backgrounds, translucent work surface, fully
visible mobile selection, and no horizontal overflow. Review found no unresolved high or
medium risk on an enabled path; one low-risk denied-`localStorage` startup failure was
fixed with a fail-soft fallback and regression test. No Provider/key, Shell, LocalRunner, Docker, product Rust
process, attack traffic, installer, registry mutation, or new product execution was used.
Schema/OpenAPI remain v84 and 75/83/182. ADR 0064 is authoritative.

## Completed Non-Starting Analyzer Invocation Bridge (P10-C1/C2/C3)

P10-C1 adds the Go-owned `analyzer_invocation.v1` candidate. It embeds the exact fixed
descriptor and binds descriptor, canonical request, and decoded inline-input SHA-256 plus
input bytes, media type, and request limits. It retains no body, Base64, path, command,
environment, or executable. Product invocation, process start, file input, result
persistence, Artifact commit, and all capabilities remain false. Strict decoding rejects
unknown, duplicate, missing, future, digest-drifted, or widened fields; pure validation
reconstructs the expected value from the fixed Registry and original request.

P10-C2 adds a package-sealed Transport implemented only by DisabledTransport and the
constructor-validated deterministic FakeTransport. The bridge revalidates the candidate,
canonicalizes request stdin, enforces the declared deadline and three stdout bounds,
classifies 0/2/3/other exits, selects the exact descriptor result decoder, and requires
deterministic success/rejection bytes to match recomputation from the current request. Its
`analyzer_invocation_outcome.v1` contains only bounded metadata and explicit false raw-output
and product-invocation claims. It retains no stdout body, stderr, or process identity.

P10-C3 fixes eight versioned failure vectors: crash, in-flight cancellation, timeout,
malformed JSON, future JSON, wrong analyzer, oversized stdout, and unknown exit. Every case
survives candidate encode/decode reconstruction, a new bridge, two executions, strict outcome
decode, and byte-identical replay. Candidate/outcome fuzz targets require idempotent accepted
envelopes.

The cumulative six-slice gate passed 418.5/459.2-second final uncached ordinary/race Go, twenty
additional final-code analyzer race repetitions, about 4.65 million batch fuzz executions,
vet/staticcheck, two zero-reachable-finding govulncheck paths, module verify/tidy, 7+2 locked
Rust tests, fmt/clippy/RustSec, 38 files/137 Web tests, strict TypeScript, deterministic
OpenAPI, Vite/npm, secure Desktop, and a reproducible Windows dual build. The unsigned GUI
SHA-256 is `82a5f7b4f012c0bc39da13d3b00cc98831e8002653a4a59f54d58f63e7126b50` and
`release_ready=false`.

Review fixed four validation/integrity issues before the final gate, including cross-input
archive success/error replay under one request ID. No known
unresolved high/medium issue exists on an enabled path. The analyzer package imports only
the Go standard library; exact key-prefix and forbidden product-import/process-entry scans
found nothing. No real Provider/key, Shell, LocalRunner, Docker, hook, attack traffic,
product analyzer process, SQLite/Event/Artifact write, installer, registry mutation, or new
authority was used. Schema/OpenAPI remain v84 and 75/83/182. ADR 0065 is authoritative.

中文交接：当前 Go 已经能生成并重启复核 analyzer 调用候选，也能用 Disabled/Fake bridge 固定
超时、取消、崩溃、输出上限、退出码和严格结果语义；但它仍不会启动 Rust 二进制，也没有接入
Run/Event/SQLite/Artifact 或任何用户入口。架构约 99%、产品可用度约 95-97%、通用 Coding Agent
约 95-96%、Cyber 自动化约 20%。

## Completed Test-Only Analyzer Subprocess Conformance (P10-D1/D2/D3)

P10-D1 adds `analyzer_executable_identity.v1` and `analyzer_invocation_preflight.v1`.
They strictly bind the fixed analyzer descriptor/protocols, target GOOS/GOARCH, exact
executable byte count, and SHA-256 without retaining bytes, path, command, arguments,
environment, or working directory. Descriptor determinism is not executable-semantic proof;
format/semantic verification, process start, product invocation,
persistence, and all authority remain false. Empty or over-32-MiB binaries fail closed.

P10-D2 adds a real Rust subprocess adapter only to analyzer test compilation units. The
public bridge still rejects it. CI explicitly supplies the fixture path; the adapter re-reads
and byte-compares it, reconstructs D1 records, uses an isolated temporary working directory,
empty environment, no Rust arguments, canonical closed stdin, and bounded stdout/stderr.
Production analyzer files are regression-scanned against process imports. Public product
outcome validation/encoding/decoding reject the test-only transport label.

P10-D3 covers real Rust success, runtime rejection, blocked-stdin timeout, cancellation after
start, and forced termination. Test-only Go helpers cover graceful/hard descendant reap,
parent-exit orphan detection and cleanup, malformed/future/wrong stdout, and stderr privacy.
Linux uses a process group and TERM/KILL; Windows uses a kill-on-close Job Object and a 200ms
grace before hard termination. GitHub CI runs the fixture gate on Linux and Windows.

The functional gate passed final 321.1-second full Go, five focused race rounds, about 1.89
million new-envelope fuzz executions, vet/staticcheck/module, zero-finding Analyzer govulncheck,
7+2 Rust tests plus fmt/clippy,
38 files/137 Web tests, strict TypeScript/OpenAPI, Vite/npm, secure Desktop, and a reproducible
Windows dual build. GUI SHA-256 is
`649c7107fdc6e8bad3b718e705d7ce9a5003ea7891c649606695286adf61bf93` and
`release_ready=false`. Review separated descriptor determinism from unverified executable
semantics and fixed accidental parent-environment inheritance, race helper startup/deadline
confusion, wait ownership, and the first draft's product-codec acceptance of a test-only
transport label.
No enabled product path gained process authority. Schema/OpenAPI remain v84 and 75/83/182;
ADR 0066 is authoritative.

中文交接：当前 Go 已能在测试门中以精确二进制摘要启动 Rust fixture，并验证成功、拒绝、超时、
取消、强杀、整树回收、孤儿清理和 stderr 隐私；但公开 Bridge 仍拒绝真实 adapter，CLI/HTTP/Desktop/
Run/Event/SQLite/Artifact 都没有启动或持久化入口。pathname 复读仍不等于 TOCTOU-safe handle，格式、
架构、签名、来源和 OS 资源限制也尚未验证。架构约 99%、完整产品可用度约 95-97%、通用 Coding
Agent 约 95-96%、Cyber 自动化约 20%。

## Completed Inert Analyzer Result Staging And Adapter Blockers (P10-E1/E2/E3)

P10-E1 adds strict `analyzer_validated_result_candidate.v1` and
`analyzer_artifact_candidate.v1` records. Go rebuilds and validates the invocation candidate,
executable identity, preflight, successful product-codec outcome, and exact deterministic
result before emitting body-free metadata. The nested Artifact candidate has no path,
Run/Session/Workspace binding, persistence, event, publication, or commit authority.

P10-E2 adds atomic no-replace staging only in `_test.go`. A private test directory receives a
mode-0600 pending envelope, file sync, and same-volume hard-link publication. Recovery covers
replay, cancellation, explicit rollback, crash before/after publish, truncation, and foreign
collisions. Rollback removes only the exact expected envelope or a valid interrupted prefix;
unrelated same-name pending/final files are preserved. There is no directory-fsync proof,
durable intent, generation lease, Store/SQLite/Run/Event integration, or product durability.

P10-E3 fixes 20 mandatory controls in
`analyzer_product_adapter_threat_model.v1`. All remain required, unimplemented, unverified,
start-blocking, and non-overridable. They cover immutable executable-handle identity,
PE/ELF and architecture checks, provenance/version allowlists, least privilege, filesystem/
network/environment isolation, CPU/memory/process/deadline limits, complete tree reap,
bounded redacted stdio, operator-scoped approval, atomic handoff, durable recovery,
append-only audit, and orphan reconciliation.

The final six-slice gate passed uncached full ordinary/race Go in 397.6/462.5 seconds, twenty
final staging race repetitions, a ten-round real Rust/process-tree/staging race gate during
review, and about 2.17 million new fuzz executions. Vet/staticcheck/module checks, 7+2 locked
Rust tests, fmt/clippy/RustSec, 38 files/137 Web tests, strict TypeScript/OpenAPI, Vite/npm,
secure Desktop, Linux Analyzer cross-compilation, and a reproducible Windows dual build all
passed. `govulncheck` found no reachable or imported-package issue; it reports only
GO-2026-5932 for the unimported `golang.org/x/crypto/openpgp` package in a dependency module,
with no fixed version. The unsigned GUI SHA-256 is
`10effa0de5f5fc159e43f99aa97f45fc7579e4413b4ec0f3c7051dd4e217dabf` and
`release_ready=false`.

Review fixed one low-risk test-harness rollback ownership issue. One initial full Go command
also hit a transient Windows GCC/ld failure with no linker diagnostic; the affected package
and both complete final ordinary/race gates subsequently passed. No enabled product path has
a known unresolved high/medium issue. No real Provider/key, Shell, LocalRunner, Docker, hook,
attack traffic, product analyzer process, Store/Event/Artifact write, installer, registry
mutation, or new authority was used. Schema/OpenAPI remain v84 and 75/83/182. ADR 0067 is
authoritative.

中文交接：Go 现在可以把一份确定性成功结果封印成无正文、无提交权的候选，并在测试目录验证原子
暂存与崩溃恢复；20 项产品 adapter 条件仍全部阻塞启动。它不是可供用户调用的 Rust 执行入口，也
没有接入 Run、事件、SQLite、Artifact writer、CLI、HTTP 或 Desktop。架构约 99%、完整产品可用度约
95-97%、通用 Coding Agent 约 95-96%、Cyber 自动化约 20%。

## Completed Browser Control Foundation (P11-A1/A2/A3)

P11-A1 fixes three Go-owned browser descriptors: `safe-web`, `ctf-lab`, and
`ctf-instrumented`. Code mode keeps normal browser security and excludes private targets;
CTF Lab can describe exact private targets plus interception/mutation/replay/cookie tooling;
Instrumented can additionally request three explicit security relaxations, requires risk
acknowledgement and a future container, and marks evidence as instrumented. Every descriptor
requires disposable state, forbids personal profiles/extensions/password stores/host files,
and grants zero runtime authority.

P11-A2 adds `browser_target_scope.v1` with at most eight canonical exact HTTP(S) origins,
IDNA/default-port/IP normalization, redirect and DNS-result revalidation, literal-IP pinning,
and a stable fingerprint. Safe Web rejects private/loopback rebinding. Every profile rejects
cloud metadata, link-local, multicast, unspecified/reserved addresses, non-HTTP schemes, URL
credentials/fragments/backslashes, and off-scope origins.

P11-A3 adds `browser_session_plan.v1`. It binds Session/Run/Workspace, profile/scope digests,
proxy endpoint and system credential name, requested tooling/relaxations, disposable isolation,
limits, evidence class, backend, and sorted launch blockers. It stores no proxy secret or userinfo;
the model cannot clean profiles. `start_blocked` is true and all process/network/profile-write/
mutation/replay/Artifact plus proxy credential/network authority is false.

The gate passed a final-dependency 364.5-second full Go run, full vet, targeted race/staticcheck, about 8.2
million URL/proxy fuzz executions, 38 files/137 Web tests, strict TypeScript/Vite, and zero-
vulnerability npm audit. A first targeted scan found reachable GO-2026-5970 through IDNA in
`x/text v0.37.0`; v0.39.0 fixes it and the repeated scan has zero reachable findings. No real browser/network/process/
profile directory/credential/Docker/Shell/Provider/Store/Event/Artifact path ran. Schema/OpenAPI
remain v84 and 75/83/182. ADR 0069 is authoritative.

中文交接：当前只有 Go 浏览器控制合同，没有可操作窗口、Chromium 启动、导航或抓包。Wails
WebView2 继续只渲染 Prayu；不能把“Browser Session Plan 已完成”误写成“内置浏览器已可用”。
架构仍约 99%，完整产品可用度约 95-97%，Coding 约 95-96%，Cyber 自动化约 20%。

## Completed Frameless Workbench And Agent Composer (D1-UX4/UX5/UX6)

D1-UX4 switches the Windows Wails development shell to a frameless surface and implements
the visible application titlebar in React. Minimize, maximize/restore, and close call the
existing Wails runtime only when it is present; browser tests remain fail-soft. This does
not add an installer, registry writes, startup behavior, updater, or a second desktop host.

D1-UX5 replaces the fixed sidebar with a 286 px default, 232-420 px bounded splitter. Pointer,
keyboard, and double-click reset paths share the same clamp; the width is a presentation-only
local preference. Its drag target is intentionally transparent, so resizing no longer draws
the orange line that was mistaken for content. Active rows use a CSS-generated orange brush
and never paste the supplied concept screenshot or an extracted active-row bitmap.

D1-UX6 adds one shared Agent composer for new Runs, Run delivery, and Session chat. Its add
menu exposes only already-supported workspace attachments, target mode, Plan mode, and
installed Skills. Model choices are loaded lazily from the Go model-availability API and
changes use the existing controlled route mutation. The context ring reports conservative
Go-owned limits. The current Provider contract has no `reasoning_effort`, so Standard is the
only active choice and High/Max remain visibly disabled rather than claiming a false setting.
New-Run text prefills the existing controlled Run dialog; it does not bypass Go authorization.

The focused Web gate passes strict TypeScript, production Vite build, and 41 files/143 tests.
Desktop automated checks and the reproducible Windows development build pass; the latest
unsigned GUI SHA-256 is
`28ae5b21efa7746f0bd3c6646351daca6234aeeb2e85c082982e4e915b95400b` and
`release_ready=false`. Visual QA at 1440x900 confirmed the frameless titlebar, bounded cream
work surface, add/model/reasoning/context popovers, fully visible model labels, and pure-CSS
active state. A Settings-background opacity issue found in QA was corrected and rebuilt;
the final post-rebuild click-through was intentionally skipped when the desktop-control tool
detected concurrent physical user input. No Codex window was controlled or closed.

No credential/key, Policy, Tool, Shell, LocalRunner, Docker, browser, filesystem, process,
network, approval, persistence, or installer authority was added. Schema/OpenAPI remain v84
and 75/83/182. Architecture remains about 99%, full product usability about 95-97%, general
Coding Agent capability about 95-96%, and Cyber automation about 20%. ADR 0070 is
authoritative.

中文交接：本批完成无边框 Prayu 窗口、透明热区的有界可调侧栏，以及复用既有 Go API 的 Agent
输入区。模型、附件、Plan 和 Skill 都没有绕过 Go；高/最高推理强度在协议未支持前保持禁用。选中态
已改为纯 CSS 橙色笔刷，不再把概念图贴进功能框。设置页独立背景已按视觉复核结果调亮并重建。

## Completed Inert Browser Runtime Adapters (P11-B1/B2/B3)

P11-B1 discovers Edge, Chrome, and Chromium only below Windows Known Folder roots and fixed
Go-owned suffixes. It rejects filesystem indirection and non-regular or malformed PE files,
then binds product/channel/path, host and target architecture, exact byte count, SHA-256, and
read-only Windows file-version metadata. It never searches PATH or executes a candidate.
Publisher signature, launch trust, path persistence, process start, and all runtime authority
remain false. A post-version same-file check closes ordinary replacement drift; this still is
not a TOCTOU-safe launch handle.

P11-B2 derives one exact disposable directory from the Session profile token under a dedicated
`browser-profiles` root. Pure metadata classifies absent, exact active/stale/released, foreign,
and corrupt states. Exact stale recovery advances generation N to N+1 and changes owner/marker
digests; an old generation becomes foreign. Only an exact released current owner can produce a
delete-blocked, no-wildcard cleanup candidate. No directory or marker is created, read, renamed,
written, or deleted.

P11-B3 adds a package-sealed CDP bridge admitting only Disabled and deterministic Fake
transports. Exact-scope navigation, DOM JSON, PNG/JPEG screenshot, and request-capture JSON
contracts enforce action-specific byte/count limits, cancellation, deadlines, strict canonical
outcome JSON, and metadata-only results. Fake successes are explicitly synthetic. Raw DOM,
pixels, headers, cookies, bodies, and request entries are absent; process/network/profile-write/
mutation/replay/Artifact/product-execution facts and authority remain false.

The combined audit found and fixed three future-wiring risks before delivery: path identity is
rechecked after version lookup, PE identity explicitly does not claim publisher/launch trust,
and Fake screenshot/capture successes validate minimum format and count consistency. Focused
normal/race/vet/staticcheck, source-capability and mutation tests pass with 77.8% package statement
coverage. The integrated gate passes final-graph full Go in 378.0 seconds, full vet, 41 files/143
React tests, strict TypeScript, Vite build, zero-vulnerability npm audit, and patch hygiene.

No real browser, process, network, profile directory, credential, Docker, Shell, Provider,
Store/Event, or Artifact path ran. Schema/OpenAPI remain v84 and 75/83/182. Overall architecture
remains about 99%, full product usability about 95-97%, Coding about 95-96%, and Cyber automation
about 20%. Browser-control architecture is materially stronger, but end-user browser usability
is still 0% because no real launch or navigation exists. ADR 0071 is authoritative.

中文交接：P11-B1/B2/B3 只完成浏览器发现、目录生命周期和 Fake/Disabled CDP 的不可执行合同。
不要把 synthetic success、PE 摘要或 cleanup candidate 写成真实浏览器、可信签名或删除授权；Wails
WebView2 仍只渲染 Prayu，内置浏览器对用户仍不可用。

## Completed Workbench Docks And Native Workspace Opening (D1-UX7/UX8/UX9)

D1-UX7 replaces the former single Settings action with the requested compact four-control
workbench toolbar. Summary, Bottom Panel, and Right Sidecar are independent layout states and can
remain visible together. Keyboard routes match the visible menu for Review, Browser, Files, Side
Tasks, and the bottom panel. The sidecar is a real React component tree, not a pasted screenshot.

D1-UX8 reuses existing bounded Go surfaces: Summary reads repository plus selected Run/Session
metadata, Review embeds the redacted Repository Diff, Files embeds Workspace Explorer, and Side
Tasks pages current WorkItems. Browser and terminal surfaces intentionally report that they are
not started. The bottom panel contains no PTY, xterm input, Shell, process, or browser bridge.

D1-UX9 adds a separate Desktop-native manual convenience contract. TypeScript submits only an
opaque registered Workspace ID and one fixed launcher ID. Go resolves the root from SQLite and
returns no path. Windows discovers a bounded recognized catalog, validates the exact directory
and executable, presents both in a native confirmation, revalidates after confirmation, and starts
at most one selected external app. Explorer/editors receive only the registered root; an external
Terminal receives no command arguments and only inherits that root as its working directory.

This action is not Agent execution. It grants no model, child, HTTP, Tool Gateway, Runner, Shell,
browser, arbitrary argument, or environment authority. The external app runs under its own Windows
permissions after operator confirmation and Prayu does not supervise its later behavior or claim
publisher/signature trust. `process_execution_enabled`, embedded terminal, browser start, Local,
and Docker gates remain false.

The six-slice robustness gate passes serial ordinary/race full Go in 512/603 seconds, full vet and
staticcheck, focused Desktop race, secure Desktop-tag test/vet, zero-reachable-finding ordinary and
Desktop govulncheck, module verification/tidy, 42 files/148 Web tests, strict TypeScript,
deterministic OpenAPI, Vite, and zero-vulnerability npm audits. Rust fmt, 7+2 locked tests, Clippy,
and a cached RustSec scan over 1,166 advisories/42 crates pass; the attempted online RustSec refresh
failed on a GitHub Git-transport I/O error and is not represented as fresh. The reproducible Windows
dual build passes with SHA-256
`8aaf3365e3c4d2e41b6f6b6dbf75f1b580a48d24419ba288d4235a41b5549cb8` and
`release_ready=false`.

Parallel full Go testing on this Windows host can time out because many packages concurrently run
the 84-step SQLite migration suite; `-p 1` is the authoritative local full gate and passes. This is
a test-infrastructure performance residual, not evidence of an Agent/Runner deadlock. Review fixed
one low-risk future-resolver gap by rejecting non-canonical absolute Workspace roots independently
at the bridge and native launcher. Schema and
OpenAPI remain v84 and 75/83/182. Metrics remain architecture about 99%, full product usability
about 95-97%, Coding about 95-96%, and Cyber automation about 20%. ADR 0072 is authoritative.

中文交接：右上角四控件、摘要、底栏和右侧栏已是真实 UI；审阅/文件/侧边任务复用既有 Go 能力，
终端和浏览器仍明确未启动。只有操作员可在原生确认后用固定应用打开已登记 Workspace；渲染层拿不到
路径，也不能提交命令或参数。这不代表 Agent 已获得宿主机进程权限。

## Completed Browser Publisher, Lease, And Review Gates

P11-C1 uses one read-only handle to bind candidate bytes, PE architecture, file identity, SHA-256,
and cache-only Windows Authenticode. Exact publisher policy accepts Chrome `Google LLC`/legacy
`Google Inc` and Edge `Microsoft Corporation` only. Chromium remains refused because no stable
publisher policy exists. The result is `accepted_for_review`, never complete launch trust.

P11-C2 adds schema v85 immutable launch attempts, generation leases, idempotent preparation
operations, and bounded events. Every attempt binds the exact Session, Run, Workspace, executable,
disposable-profile generation, scope, budgets, backend, and process-tree contract. Lifecycle,
restart observation, cancellation, termination, and cleanup remain package-sealed Disabled/Fake
contracts and start no process.

P11-C3 requires an independent reviewer while the exact lease is active and recomputes the full
attempt fingerprint. Owner/reviewer and operation identities are retained only as domain-separated
digests. Accepted review still leaves process, network, profile-write, termination, cleanup, CDP,
and Artifact authority false.

The six-slice robustness gate passes serial ordinary/race full Go in about 545/660 seconds, full
vet/staticcheck, module verify/tidy, ordinary and secure-Desktop govulncheck with zero reachable or
imported findings, secure Desktop race/tag checks, 42 files/148 Web tests, strict TypeScript/API,
Vite/npm, Rust fmt/7+2 tests/Clippy/audit/real-fixture conformance, and a reproducible Windows dual
build. The unsigned GUI SHA-256 is
`a7e482adfff18068c4d3fb588d8c4e25a79ac0aefff053df7a7ab57902b5b85b`;
`release_ready=false`.

An opt-in read-only local smoke inspected fixed Chrome and Edge installations. Both signatures and
fixed publishers passed into review-candidate state, two candidates were inspected, and no browser
process was started. No browser network, Profile write, CDP, credential, Shell, Docker, Provider,
or Artifact path ran. No unresolved high/medium issue was found on an enabled path. ADR 0073 is
authoritative.

中文交接：C1-C3 已把发布者/同句柄复核、持久化 attempt/代际 lease 和独立 reviewer 串成 schema
v85，但这仍是“启动前门禁”。本机只读烟测没有启动 Chrome/Edge。下一批才可逐项考虑 Safe Web
真实启动、一次性 Profile 落盘与最小 CDP；三者必须继续拆开审计。

## Completed Model Harness Protocol And Qualification (A1/A2/A3)

Model Harness A1 adds Go-owned `model_harness.v1`. A profile is bound to one exact Provider/model
and records transport (`mock`, `anthropic_messages`, or legacy `provider_contract`), Tool strategy,
JSON strategy, streaming behavior, qualification status, and a binding digest. Mock is a trusted
offline profile. The Anthropic-compatible Provider uses native Tool calls and prompt-directed JSON
but starts as `qualification_required`; an in-process legacy Provider receives an explicitly named
compatibility profile for source compatibility and should migrate to the describer interface.

A2 calls one shared Go preflight immediately before Root, Specialist, and read-only Fan-out model
requests. Root requires qualified ToolCall, ToolResult, strict-JSON, and streaming behavior before
it can send Tools. Specialist and Fan-out are always no-tool workloads and Go removes any supplied
Tool definitions. Native JSON is enabled only for a native profile; prompt JSON remains transport
specific. An expired, changed, or incomplete qualification fails before the Provider call.

A3 adds `model_harness_qualification.v1` to the existing Registry settings plus CLI, HTTP, Desktop,
and React. Explicit confirmation may perform at most two 30-second-bounded streamed calls for an
external model. The first must return exactly one in-memory nonce ToolCall; the second must consume
the synthetic ToolResult, make no further ToolCall, and return exact `model_harness_probe.v1` JSON.
No synthetic Tool is dispatched. Chunks, bytes, JSON fields, and persisted records are bounded.
Success stores only the exact binding digest, four capability booleans, and a seven-day expiry.
Startup restores only an exact unexpired binding. Read-only availability remains no-probe and now
uses `model_availability.v2`; connectivity diagnostic and Harness qualification remain separate.

The integrated gate passes full Go in about 398.8 seconds, vet, focused race, zero-warning
staticcheck, module verification, 42 files/149 React tests, strict TypeScript, Vite production
build, and deterministic 76/84/185 OpenAPI/TypeScript generation. Audit caught and fixed a
Fan-out range-value write-back bug plus missing final streamed
Provider/model identity rejection during qualification. Two-shard and wrong-model regression tests
pin both fixes. No real Tool, Shell, file, browser, Docker, target-network, analyzer, or new
authority path was used. The first remote CI then exposed a development-only `brace-expansion`
high-severity DoS advisory in the OpenAPI generator chain. An exact 5.0.8 override preserves
generation, all 149 Web tests, and the production build while returning `npm audit` to zero
vulnerabilities. SQLite remains v85. ADR 0074 is authoritative.

中文交接：模型接入现在不再只看“HTTP 能通”。外部模型必须通过一次合成 ToolCall 与一次精确 JSON
回执，且资格绑定精确 Provider/模型/地址/策略并会过期。普通连通性测试和资格校验仍是两个动作；
资格通过也不会开放 Shell、文件、浏览器或 Docker。

## P12-B Controlled Execution And User Terminal Handoff

P12-B1 advances SQLite to v87 and is the first product host-process slice. It is
not the general `LocalRunner`: `run command-execute` can start only the exact
`git-status`, hardened `git-diff-check`, `go-version`, or fixed
`powershell-workspace-list` template after operator confirmation. The intent is
persisted before start. Windows uses OS-derived installation roots, pins one
non-reparse regular PE, creates a restricted low-integrity token, assigns a
creation-time Job Object, permits one process/512 MiB, closes stdin and inherited
environment, bounds captured/observed output, and reaps on timeout/cancel.
Receipts contain metadata and hashes only; raw stdout/stderr is transient. There
is no independent network sandbox, arbitrary executable/argv, or automatic retry
after an uncertain start. The PowerShell relative path is canonical UTF-8 hex
data decoded only by the fixed script, so path text cannot become an expression.
Go and SQLite both validate exact output-count, truncation, digest, time, and
limit relationships.

P12-B2 adds `--enable-user-terminal`. It creates a current-user Windows
PowerShell `-NoLogo -NoProfile` ConPTY only for an exact trusted
Code/Local/Debug Run, with creation-time Job ownership, at most eight process-local
sessions, and a 4 MiB rolling output buffer. xterm is the user interface. The
user starts and types; no terminal bytes, environment, or process identity are
persisted.

P12-B3 wires only an internal Agent input consumer. It requires the exact
P12-A2 bearer and cannot start, replace, resize, or retarget a terminal. Native
lock/disconnect/logoff/suspend/resume, Run termination, profile or interaction
change, terminal replacement, and shutdown revoke leases. Run-to-Workspace
identity is immutable; a Workspace-scope revoker exists for a future independent
switch path but is not wired to the renderer. The Desktop renderer has no
lease-issuance route.

Final P12-B verification: `go test -p 1 -count=1 ./...` passed in 796.6 seconds;
vet, warning-free staticcheck, Runner/Terminal race, module verify/tidy,
zero-reachable-finding govulncheck, secure Desktop tags, real Windows
restricted-process/ConPTY/host-boundary opt-in smokes, 43 files/151 React,
strict TypeScript, Vite, zero-vulnerability npm audit, and the reproducible
Desktop double build passed. The executable SHA-256 is
`6f60f97096a06305e26d3c68ef26f93622c80a4784ad23ea72d2b28353fc2e77`;
`release_ready=false`.

The combined audit fixed PowerShell path-expression injection, forged/impossible
receipt relationships, stale renderer start and shutdown cleanup, and a
double-owned Win32 pipe read handle that could close a reused IOCP handle after
`os.NewFile` finalization. Pipe ownership now transfers to exactly one collector
and closes before result publication. Complete destructive Policy test examples
are assembled at runtime to avoid a Defender ML false positive; the exact
fixtures and permanent-denial behavior are unchanged. No enabled path has a
known unresolved high/medium issue.

## Completed Public Activity Projection (P13-A1/A2/A3)

P13-A1 adds pure Go `run_activity.v1` over an explicit event allowlist.
P13-A2 makes it the default Desktop Run view and keeps raw Events separate.
P13-A3 fixes root `message` semantics as public progress, not hidden reasoning.
The API is read-only, uses existing schema-v89 events, and grants no new
authority. Focused Go, 48-file/165-item React, strict TypeScript, OpenAPI
82/90/199, and Vite production checks pass. The uncached serialized full Go
suite passed in 483.9 seconds; repository-wide vet/staticcheck, focused
`runactivity/httpapi/application` race, zero-reachable govulncheck, module
verify/tidy, deterministic API regeneration, and npm zero-vulnerability audit
also pass. Anthropic `thinking`/`thinking_delta` exclusion and the fixed
activity-status enum have dedicated regressions, so private reasoning and
payload-controlled CSS tokens fail closed. See ADR 0080.

## Completed Gated Host Execution And Debug Input (P12-E1/E2/E3)

P12-E1 introduces the separate `host_command.v1` proposal/review domain. It
freezes executable SHA-256, argv, cwd, sanitized environment metadata, host
network intent, timeout, and all durable Run bindings without extending the
schema-v89 fixed diagnostic Tool. The contract is non-authorizing and has no
persistence, model, HTTP, Desktop, or execution route.

P12-E2 advances SQLite to v90. Operator CLI `run host-execute` requires an
exact Code/Local/Controlled/trusted Run, durable `full_access`, current
permission-control and danger-full-access gates, and two confirmations.
Windows uses exact non-Shell process creation, PE/SHA pinning, a sanitized
environment, closed stdin, creation-time Job ownership, 32-process/2-GiB
bounds, output/deadline/cancellation limits, and process-tree reap. A durable
intent precedes start; a metadata-only receipt follows; raw output and
environment values remain transient. This is current-user non-sandboxed host
filesystem and network access and is not exposed through HTTP, Desktop, or a
model Tool.

P12-E3 adds a Go-only controller over an existing user ConPTY. A short
process-local bearer exact-binds Workspace, Run, terminal, interaction,
Profile, and permission revisions. One complete UTF-8 command line passes
permanent Policy before write. Grant, prepared, completed, and revoked events
retain only metadata, digests, and byte counts. Uncertain writes are never
retried. Host lifecycle, Run/binding drift, terminal replacement, expiry, and
shutdown revoke the binding. The renderer, HTTP, Skills, repository content,
and models cannot issue or use it. See ADR 0081.

## Completed Browser CDP Permission Ceilings (P11-C4A/C4B/C4C)

P11-C4A advances SQLite to v91 with immutable
`run_browser_cdp_permission.v1` snapshots and digest-keyed operation replay.
`restricted` permits only navigation/DOM/screenshot in the policy ceiling;
`full_debug` additionally names capture/mutation/replay/Cookie/arbitrary-method
families. Both modes permanently fix transport, browser start, runtime, and
capability authority false in Go and SQLite.

P11-C4B adds one operator-only service across CLI, HTTP/OpenAPI, and Desktop.
Full mode requires current durable Debug execution permission, ordinary CDP
control, the dedicated full-CDP process gate, and exact confirmation. Stored
selection never carries a process-local grant across restart. Untrusted model,
Agent, Tool, Skill, browser, document, and repository requesters are rejected.

P11-C4C adds the two-mode control to the Permission page. `完整 CDP（调试）`
is visibly marked `高度敏感权限`, unavailable prerequisites fail closed, and
the page explicitly reports the transport as closed. Focused domain/store,
application/HTTP/OpenAPI, CLI/API/Desktop, strict TypeScript, and React tests
cover migration, immutable replay, gate hierarchy, confirmation, projection,
and UI behavior. The deterministic contract is 83/91/203. No browser or CDP
runtime ran. See ADR 0082.

The integrated gate passed `go test -count=1 ./...` in about 578 seconds,
focused application/HTTP race tests, ordinary and secure-tag Desktop tests and
vet, module verification, and a clean `go mod tidy`. The web gate passed 48
Vitest files / 178 tests, strict typechecking, production build, and a
high-severity npm audit with zero vulnerabilities. A source audit found no
authority=true assignment, browser/process launch, remote-debugging flag, or
CDP transport dependency in the v91 path.

## Completed Restricted Loopback Browser Runtime Core (P11-C5/C6/C7)

P11-C5 implements a Windows-only Safe Web process adapter behind exact,
short-lived runtime authorization. It revalidates the pinned executable,
schema-v85 acceptance/review, schema-v91 restricted permission, ownership,
scope, budget, attempt, generation lease, and deadlines. A fixed argument set
starts without a Shell and joins a kill-on-close, bounded Job Object at process
creation time before its primary thread resumes.

P11-C6 materializes and owns only one exact disposable Profile generation.
Canonical markers, process-local leases, exact ancestry, Profile-local
environment directories, quiescent release, and quarantine/recheck cleanup
prevent personal-profile reuse, indirect-path cleanup, foreign-marker
adoption, active-generation deletion, and replayed cleanup.

P11-C7 adds one closed restricted CDP transport. It dials only the exact
Profile's literal `127.0.0.1` endpoint without a proxy, owns one disposable
browser context/target, denies downloads, service workers, cache, and
out-of-scope requests, and exposes only navigation, bounded DOM metadata, and
bounded PNG screenshots. It cannot expose text, evaluate scripts, read
Cookies/bodies, mutate/replay requests, or dispatch arbitrary methods. Full
Debug CDP remains a separate unavailable implementation.

This batch and P11-C4A-C4C form the six-slice robustness gate. The complete
ordinary Go suite passed in 495.8 seconds. The repository-wide race pass was
green except that `internal/store` reached the default ten-minute package
timeout; the same Store race suite then passed in 503.453 seconds under an
explicit 30-minute ceiling with no race report. Vet, warning-free staticcheck,
zero reachable govulncheck findings, module verify/tidy, 48 Vitest files / 178
tests, strict TypeScript/API/Vite/npm, Rust fmt/7+2 tests/Clippy/conformance,
and the reproducible Windows build all passed. The GUI SHA-256 is
`a6ac44c0078e32577c7a90bce2a159f22b44400607862dfd6e83704faef1cbdb`.
Tests used fake process creation and a local scripted WebSocket and started no
installed browser.

The combined audit found no unresolved high/medium issue on an enabled route
because no product route exists. It did identify one release blocker: CDP
Fetch interception cannot prove that browser-internal networking has no bypass.
An OS/container network boundary with production evidence must pass before a
CLI, HTTP, Desktop, Tool, Skill, or model adapter is allowed. See ADR 0083.

## Completed WFP Containment And Recoverable Browser Lifecycle (P11-C8A/C8B)

P11-C8A implements a CDP-independent Windows WFP dynamic-session probe. It
atomically installs one exact executable/address/TCP-port permit above IPv4 and
IPv6 default-deny filters, starts the accepted browser in a creation-time Job,
and verifies wrong-port, alternate-loopback, local non-loopback, IPv6, process,
Filter ID, and disposable-Profile cleanup behavior. It accepts no proxy,
caller URL, public target, personal Profile, or security-disable flag.

The local non-elevated production probe failed closed with
`wfp_elevation_required` before browser creation. Therefore implementation is
complete but production acceptance is not. C8C remains unavailable. WFP
application-ID rules affect every process using the same executable path, so
the long-lived product adapter must also resolve that process-wide race.

P11-C8B advances SQLite to v92 with append-only
`browser_runtime_checkpoint.v1` and `browser_runtime_receipt.v1`. The exact
stage chain reconciles CDP close, process-tree quiescence, WFP teardown, Profile
release, cleanup, completion, failure, and recovery. Cleanup continues under a
bounded independent context after caller cancellation, CDP-close failure, or
checkpoint persistence failure. Profile release requires verified process and
network cleanup. Events and receipts are redacted and contain no raw output,
page material, screenshot, personal Profile, or Full CDP data.

Windows SDK ABI regression tests fix the manually bound WFP structures at the
native x64 offsets (`FWPM_ACTION0` 20 bytes and `FWPM_FILTER0` 200 bytes) and
verify IPv4 host-order values. See ADR 0084.

The final batch gate ran once: `go test -count=1 ./...` passed in about 435
seconds, followed by repository-wide vet and warning-free staticcheck, focused
ordinary/race browser-runtime tests, and a CGO-disabled Linux cross-compile.
The combined audit fixed native WFP ABI alignment, explicit verification after
disposable-Profile deletion, and an exclusive concurrent `Finalize` claim. Two
concurrent finalizers now yield exactly one successful cleanup and one receipt.
No installed browser or product route was started.

### 2026-08-02 WFP Probe Follow-up

The first elevated Edge probe crossed elevation and proved dynamic WFP-session
creation plus atomic filter installation, then failed closed with
`baseline_canaries_not_observed`. A focused no-WFP smoke test using the same
production Job Object, disposable Profile, and five local canaries reproduced
zero requests. The cause was the fixed Chromium resolver rule: `MAP *
~NOTFOUND` also suppressed IP literals. The probe now keeps DNS default-deny
while adding deterministic `EXCLUDE` entries only for its five Go-created
canary addresses; WFP continues to bind the exact address/port scope. The real
installed-Edge baseline then passed in about 0.6 seconds.

Probe runtime errors now distinguish early browser exit, canary timeout,
caller cancellation, and other runtime failure, and the completion race
rechecks the observation before failing. Focused probe tests, the full
`internal/browserruntime` package, package vet, and focused race detection pass.
Schema v92 was not rerun or changed. C8C remains disabled until a fresh
administrator WFP probe returns `passed=true` and independent review plus the
same-executable application-ID issue are resolved.

The next elevated rerun failed in about 182ms with
`baseline_browser_exited_before_canaries`. The same process path passes under a
normal token, and the Windows starter previously inherited the elevated parent
token. `windows_browser_job.v2` now uses the same user's UAC linked standard
token whenever the parent is elevated. Before `ResumeThread`, it verifies the
child has the same SID, is not elevated, and has Medium Integrity; absence or
ambiguity fails closed as `*_standard_user_token_unavailable`. The network
policy is now `browser_network_containment_policy.v2`, invalidating v1 evidence.
No Explorer token, Chromium sandbox-disable flag, schema migration, or product
browser route was added. Non-elevated real-Edge smoke, browserruntime package
tests, focused race, and package vet pass; elevated production rerun remains.

## Completed Analyzer Format, Release, And Launch Candidates (P10-F1/F2/F3)

P10-F1 adds strict `analyzer_executable_format.v1` evidence over caller-owned
bytes. It binds the invocation candidate, executable identity, preflight,
whole-image byte count, and SHA-256; validates bounded PE/ELF structures and
requires exact GOOS/GOARCH format and machine agreement. It does not read a
path, launch a command, or prove executable semantics, provenance, or an
immutable handle.

P10-F2 adds digest-only release manifest, operator allowlist, and release
candidate protocols. An exact allowlist match pins the executable, format
evidence, provenance statement, signer identity, and signature envelope, but
does not verify cryptography or grant release, process, network, filesystem,
or product authority.

P10-F3 adds a deterministic resource/sandbox launch-plan candidate and an
exact operator design review. Hard limits and enforcement are required but
remain unverified. The plan contains no path, command, argv, environment,
input body, or process starter; `start_blocked=true`, and the review cannot
authorize execution. See ADR 0085.

The three-slice functional gate passed: final analyzer ordinary tests, vet,
warning-free staticcheck, and focused race passed; the repository-wide
ordinary Go suite passed in about 562.9 seconds before the final semantic
tightening; Rust workspace 7+2 tests passed. After the audit renamed two
overclaiming fields and tightened truncated PE/ELF rejection, the affected Go
package passed again. Schema remains v92 and product metrics are unchanged.

## Completed Analyzer Provenance And Sandbox Candidate Batch (P10-G1/G2/G3)

P10-G1 adds canonical caller-byte `analyzer_provenance_statement.v1` and
`analyzer_provenance_verification.v1`. The release statement is signed with
Ed25519 over a fixed domain-separated payload and binds source, revision,
recipe, signer, executable, format evidence, release, GOOS, and GOARCH. Raw
statement, key, and signature bytes are not returned. Platform signature,
immutable handle, release approval, and every execution authority remain
false.

P10-G2 adds `analyzer_scope_limits_approval.v1`. The same operator who reviewed
the F3 design candidate must provide one exact confirmation for the exact
request, release, provenance verification, resource limits, and sandbox plan.
The receipt is not authenticated and is not a durable grant, capability grant,
override, process-start authorization, persistence route, or Artifact commit.

P10-G3 adds real Windows and Linux process observations only in `_test.go`.
Windows verifies configured Job Object memory/CPU/process/kill-on-close limits,
minimal environment, second-process rejection, and tree reaping. Linux CI
verifies rlimits, `no_new_privs`, an architecture-checked seccomp socket deny,
minimal environment, and process-group reaping. Read-only filesystem,
dedicated identity, immutable-handle handoff, and complete product sandbox
enforcement remain explicitly unverified. Production analyzer files still
contain no process starter or product surface. See ADR 0086.

The six-slice robustness gate passed the uncached repository Go suite in about
513.5 seconds, the full race suite in 547 seconds, repository vet/staticcheck,
zero-reachable-finding govulncheck, Go module verification/tidy, 5 additional
focused Analyzer race repetitions, 178 React tests across 48 files, strict
TypeScript/Vite, zero-vulnerability npm audit, Rust fmt/test/clippy/RustSec,
secure Desktop checks, and a reproducible Windows double build. The dependency
gate found and fixed newly reported `brace-expansion` and `postcss` advisories
by pinning 5.0.9 and 8.5.25, then reran Web tests and build. The final GUI SHA-256 is
`d9bf7dc005d513046777cf7ad6a8fcf49a64190de1bef76ca822cbaf53ca9e48`;
release readiness remains false. No unresolved high/medium issue is known on
an enabled path. Schema and product metrics remain unchanged.

## Completed Analyzer Isolation Boundary Conformance Batch (P10-H1/H2/H3)

P10-H1 opens the analyzer object before replacing its path, then passes only
the caller-owned read handle on Windows or file descriptor on Linux. The child
and caller observe the original digest after replacement, while the child
receives no path authority.

P10-H2 runs a test helper in a separate low-privilege context. Windows uses a
primary token with maximum privileges disabled, the effective Administrators
SID disabled, and Low Integrity; Linux uses a
new user namespace mapped to UID/GID 65534 with `no_new_privs` and zero
effective capabilities. These are dedicated execution contexts, not
provisioned OS accounts. Windows retains the caller SID and Linux maps back to
the caller, so `DedicatedAccountObserved` remains false.

P10-H3 gives the helper read-only access to the exact input and write access
only to private staging. Windows uses a protected DACL plus Low Integrity MIC;
Linux uses Landlock plus the user namespace. Both platforms verify create-only
no-replace handoff, conflict rejection, and residue cleanup.

All child processes remain in platform `_test.go`. Complete filesystem
sandboxing, a provisioned dedicated account, product start, execution,
persistence, and Artifact authority remain false. Windows ordinary/race tests,
vet, and warning-free staticcheck pass. Linux cross-compiles locally without
CGO and is pinned into the Ubuntu native CI gate. The final repository-wide
ordinary Go functional gate passed in 435.4 seconds; Store completed normally
in 417.6 seconds. Native Ubuntu H1/H2/H3 passed in Actions run `30867231130`.
That run exposed two follow-up gate failures: hosted Windows treated UAC
`TokenElevation` as authority, and npm reported the new `undici <7.29.0`
advisory. Windows now disables the effective Administrators SID and checks
membership directly; Web pins 7.29.0. Five focused Windows repetitions,
Analyzer race/vet/staticcheck, 178 Web tests, production build, and zero-finding
npm audit pass locally pending the follow-up CI run. See ADR 0087.

Actions run `30873766068` confirmed Web and native Ubuntu gates, then exposed
two independent races. Hosted Windows removed effective administrator
membership correctly but could not initialize the checkout-resident helper
(`0xc0000142`) because the runner workspace depended on Administrators ACLs.
The test now copies the exact helper into a caller-SID/SYSTEM protected Medium
Integrity directory and gives the filesystem fixture root the same explicit
ACL; administrator membership stays disabled. The Ubuntu full suite also
exposed a pre-existing Skill removal race where an operation became visible
after an installation snapshot was read. The application now refreshes the
installation after observing the atomically committed operation/tombstone.
Windows H tests pass 10 repetitions; Skill removal passes 20 ordinary and 5
race repetitions; affected-package race, vet, and staticcheck pass.

Actions run `30877113396` passed full Go, Web, and native Ubuntu H1/H2/H3, but
the GitHub-hosted Windows service session returned exact
`STATUS_DLL_INIT_FAILED` (`0xc0000142`) before the verified Low Integrity helper
initialized even from the private directory. This is now represented honestly:
the parent directly verifies the restricted primary token's same user SID,
disabled effective Administrators membership, Low Integrity, and privilege
ceiling; Windows CI uses verbose output and skips only for GitHub Windows plus
that exact status and empty child output. Other errors fail. Local Windows
still launches and verifies the real child for 10 repetitions. The hosted skip
is not production or child-process evidence, and no product starter was opened.

## Completed Analyzer Product Admission Contracts (P10-I1/I2/I3)

P10-I1 rebuilds the exact F/G/H evidence chain and classifies 20 product
controls. Eighteen have candidate observations, thirteen of those are only
test-conformance observations, none are production verified, and all twenty
remain start blockers. Durable intent and append-only audit are explicitly
missing. Admission, adapter readiness, process-start readiness, and every
authority field remain false.

P10-I2 defines domain-separated Ed25519 request and verification contracts
bound to the exact admission matrix, scope, launch plan, release, analyzer,
platform, executable, operator identity, nonzero 32-byte nonce, and bounded
validity interval. The request has no path, command, argv, environment, or input
body. Signature verification does not issue a bearer capability: clock
acceptance, durable replay protection, and atomic consumption remain absent,
and no raw key, signature, or nonce is retained in the contract.

P10-I3 binds ten ordered restart/failure scenarios to those exact contracts.
All scenarios require idempotent handling and remain open product blockers;
write-ahead intent, generation fencing, process identity/tree quiescence,
no-replace publication, and foreign-resource protection are required where
applicable. No lifecycle store, cleanup executor, recovery reconciler, apply
path, or process starter exists.

The cumulative six-slice robustness gate passed: full uncached Go in 554.5
seconds, full race in 617.8 seconds, vet, warning-free staticcheck,
govulncheck, module verification, 48 Web files/178 tests, API check, production
build, zero-finding npm audit, Rust fmt/test/clippy/RustSec, Desktop boundaries,
and Linux no-CGO Analyzer cross-compilation. Schema remains v92. See ADR 0088.

## Completed Analyzer Durable Start Control Batch (P10-J1/J2/J3)

P10-J1 advances SQLite to schema v93 with an append-only signed-request
projection. It binds the exact Run, Workspace, operator/evidence digests,
validity interval, adapter, and a globally unique nonce. Exact replay is
idempotent; nonce rebinding, terminal Run use, and Workspace mismatch fail.
No raw nonce, key, signature, path, command, argv, environment, input, or
process material is retained, and the record is not a bearer capability.

P10-J2 adds generation-fenced write-ahead intents. Disabled is terminal at
generation one. Fake is restricted to prepared -> consumed and then a closed
fake/recovery state, or pre-consumption expiry/cancellation. Go validation and
SQLite triggers independently enforce exact predecessor fingerprints, latest
generation, signed expiry, strict JSON shape, and zero runtime authority.

P10-J3 appends one redacted receipt and two bounded Run-event projections in
the same transaction as each intent generation. Restart reconciliation only
changes dangling consumed metadata to recovery_required and expired prepared
metadata to expired. It never starts, observes, kills, or cleans a process and
never commits an Artifact. There is no CLI, HTTP, Desktop, Tool, Skill, model,
or real Runner route. See ADR 0089.

The 2026-08-05 focused closeout fixed two audit findings: stored successor
states must be known even when reason is empty, and receipt predecessors must
match Run, Workspace, request fingerprint, and previous intent fingerprint.
Analyzer/Store focused ordinary and race tests, `go vet`, `staticcheck`, and
`git diff --check` pass. This was not another full six-slice repository gate.

## Next Slice

P11-C8C remains blocked and is tracked in GitHub Issue #1. Do not spend normal
mainline turns rerunning the WFP probe or earlier v92 audits until external
evidence or the product-safe same-executable design changes. A future
operator-only Restricted Safe Web adapter must still exclude model Tools,
personal Profiles, request mutation/replay, arbitrary remote debugging, CTF
security-disable flags, and Full Debug CDP.

P10-L1/L2/L3 and P10-M1/M2/M3 are complete at schema v95. Do not repeat the v93
ledger, Fake lifecycle, embedded-WASI architecture selection, fixed execution,
one-shot authorization, atomic evidence, bilingual product route, real-chat
integration, portable preview package, WFP probe, P10-I contracts, or this
cumulative six-slice gate after context compaction.

The first operator-acceptance defects are fixed. Do not repeat their diagnosis:
an empty registry now receives Go-owned Workspace `default`; both Desktop
composers use Enter to send, Shift+Enter to insert a newline, and suppress send
during IME composition without a blue textarea outline; Provider status labels
separate unconfigured from failed. DeepSeek requires thinking disabled and
Anthropic `tool_result` blocks before any same-message text. A live
`deepseek-v4-flash` diagnostic, two-call Harness qualification with
`root_eligible=true`, and isolated real Session chat all passed. MiMo is not
configured on this machine and has not been network-tested.

P13-C1/C2/C3 and their cumulative six-slice gate are complete. The current
portable operator-preview package has already been rebuilt reproducibly; do
not rebuild it again solely because context was compacted. The next action is
operator acceptance of `build\desktop\Start-Prayu-Operator-Preview.cmd`, with
special attention to the approval center's exact executable/SHA/argv/cwd,
non-sandboxed host-network warning, deny path, explicit approve confirmation,
single execution, and untrusted evidence display. Future implementation must
start from a newly reproduced acceptance result or a newly assigned slice.
Docker PTY, arbitrary model Shell, signed distribution, the Windows
10/WebView2/scaling matrix, and WFP/C8C remain separate gates.

## Local Machine Note

Rustup uses the repository-pinned current-user minimal
`1.97.1-x86_64-pc-windows-msvc` toolchain under `analyzers/rust-toolchain.toml`.
Visual Studio 2022 Build Tools 17.14.37 with the C++ workload supplies the MSVC linker.
Run Rust build/test commands from a Developer PowerShell or after loading `VsDevCmd.bat`;
the source, pinned toolchain, and `Cargo.lock` remain the reproducible authority. `cargo audit`
is installed; this version accepts the default lockfile scan rather than a `--locked` flag.

The default `~/.cyberagent-workbench/cyberagent.db` currently carries a historical schema-v30 checksum that differs from this repository's immutable migration definition, so CLI startup correctly fails closed with `migration 30 checksum or name mismatch` and Desktop shows a bounded `FAILED_PRECONDITION`/startup code instead of silently resetting it. The v75-v91 and D1-Q2 through P11-C7 slices did not rewrite migrations 1-74, and fresh/upgrade fixtures pass. Preserve that local database for backup/diagnosis; do not delete it or rewrite `schema_migrations` automatically. Desktop visual and recovery tests use separate `CYBERAGENT_HOME` directories under the repository's ignored build root or the OS temporary root.

## Delivery Loop

Work in batches of three focused slices. During implementation, run focused compile and regression checks; after the third slice, run one integrated functional gate, review the combined behavior/diff, update README/status/progress/task memory, commit on `main`, push, and verify CI. Every second batch, after six slices, additionally run the complete race/vet/staticcheck/govulncheck/dependency/privacy robustness gate. Keep real Sandbox execution and CTF automation closed until their dedicated audits pass.
