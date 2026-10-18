# 开发历史与迁移索引 / Development history

[首页](../README.md) | [English home](../README.en.md) | [文档导航](README.md)

## Historical overview

This section archives the slice and percentage vocabulary that previously occupied the top of the README. These values explain the project's evolution; they are not benchmarks, release promises, or proof of current behavior. Code, tests, [PROJECT_STATUS](PROJECT_STATUS.md), and the relevant ADR remain authoritative.

### Legacy dual-metric snapshot

At **2026-08-13 / schema v96 / P13-H1 through P13-H3**, the old task book estimated:

| Historical metric | Legacy estimate | Meaning |
|---|---:|---|
| Architecture completion | about 99% | Coverage of the Go control plane, Run/Session, events, approvals, budgets, Skills, reporting, and language boundaries |
| Product usability | about 98% | General end-to-end workflows in the developer/operator preview, not production-release readiness |
| General Coding Agent | about 98% | Code workspace, chat, planning, review, proposals, verification, and handoff |
| Cyber automation | about 20% | Retired roadmap estimate; this is no longer an active product metric and is now optional add-on scope |

### Phase and slice index

| Phase | Historical delivery theme |
|---|---|
| v0.1 / P0-P2 | CLI scaffold, Workspace, SQLite, Providers, Sessions, and resumable Run-centric Supervisor |
| P3-P5 | Work/Notes, Coordinator, bounded child/fan-out, Tool Gateway, approvals, Artifacts, and structured memory |
| P6-P8 | Sandbox evidence contracts, Skill Registry, Finding/Evidence/Report, SARIF, and CI projection |
| P9 / Desktop D0-D1 | HTTP/OpenAPI, React/TUI/Desktop, repository/diff/editor/verification/Handoff, and liquid-glass workbench |
| P10-A through P10-M | Go/Rust Analyzer protocol, vectors, embedded WASI execution, one-shot capability, and product integration |
| P11-A through P11-C / schema v119 | Browser permissions, Profiles, CDP/WFP evidence, and the gated source-bound UI-evidence product path |
| P12-A through P12-E | Interaction models, controlled Windows Runner, user terminal, four permission tiers, command approval, and host-execution ledger |
| P13-A through P13-H | Run Activity, public model stream, continuous chat, Markdown, diff review, Live Activity, and desktop visual consolidation |

The complete slice ledger remains in [PROGRESS_BOOK](PROGRESS_BOOK.md), current acceptance evidence in [PROJECT_STATUS](PROJECT_STATUS.md), and resume context in [PROJECT_MEMORY](PROJECT_MEMORY.md). These ledgers are history, not a queue of work to repeat.

## 历史开发记录

本节归档旧 README 顶部曾使用的切片与百分比口径。它们用于解释项目如何演进，不是性能 Benchmark、版本承诺或发布证明；当前能力以代码、测试、[`PROJECT_STATUS`](PROJECT_STATUS.md) 和具体 ADR 为准。

### 历史双指标快照

截至 **2026-08-13 / schema v96 / P13-H1 至 P13-H3**，旧任务书估算为：

| 历史指标 | 旧估算 | 说明 |
|---|---:|---|
| 架构完成度 | 约 99% | Go 控制平面、Run/Session、事件、审批、预算、Skills、报告与跨语言边界覆盖度 |
| 产品可用度 | 约 98% | 开发者操作者预览中的通用端到端工作流，不代表正式发行就绪 |
| 通用 Coding Agent | 约 98% | 代码工作区、对话、计划、审阅、提案、验证和交接能力 |
| Cyber 自动化 | 约 20% | 旧路线图估算，现已停止作为活跃指标；相关能力转为可选附加范围 |

### 阶段与切片索引

| 阶段 | 历史交付主题 |
|---|---|
| v0.1 / P0-P2 | CLI 骨架、Workspace、SQLite、Provider、Session，以及 Run-centric 可恢复 Supervisor |
| P3-P5 | Work/Note、Coordinator、受控 child/Fan-out、Tool Gateway、审批、Artifact 与结构化记忆 |
| P6-P8 | Sandbox 证据合同、非授权 Docker 生命周期探针、Skill Registry、Finding/Evidence/Report、SARIF 与 CI 投影 |
| P9 / Desktop D0-D1 | HTTP/OpenAPI、React/TUI/Desktop、仓库/Diff/编辑/验证/Handoff 与液态玻璃工作台 |
| P10-A 至 P10-M | Go/Rust Analyzer 协议、共享向量、内嵌 WASI 执行、一次性能力与产品接入 |
| P11-A 至 P11-C / schema v119 | 浏览器权限、Profile、CDP/WFP 证据链与显式门禁的源码绑定 UI-evidence 产品路径 |
| P12-A 至 P12-E | 交互模型、受控 Windows Runner、用户终端、四档权限、固定命令审批与宿主执行账本 |
| P13-A 至 P13-H | Run Activity、公开模型流、连续对话、Markdown、Diff 审阅、Live Activity 与桌面视觉收口 |

完整逐切片原始记录保留在 [`PROGRESS_BOOK.md`](PROGRESS_BOOK.md)，当前检查点与验收证据保留在 [`PROJECT_STATUS.md`](PROJECT_STATUS.md)，恢复上下文见 [`PROJECT_MEMORY.md`](PROJECT_MEMORY.md)。这些账本是历史记录，不应被当作待重新执行的任务列表。

<details>
<summary><strong>SQLite Schema 迁移审计表 / Migration ledger</strong></summary>

此表是 Store 防漏迁移测试使用的审计合同。新增 schema 时必须按顺序追加，不得改写或删除既有行。

| Schema | 中文记录 | English record |
|---|---|---|
| v1 | v0.1 基线存储 | v0.1 baseline |
| v2 | Mission/Run 中心化基础 | run-centric foundation |
| v3 | Run 与 Session 投影 | run session projection |
| v4 | 旧 Task 到 Run 的兼容映射 | legacy task run mapping |
| v5 | Supervisor 检查点 | supervisor checkpoints |
| v6 | Supervisor 预算账本 | supervisor budget ledger |
| v7 | Supervisor 待处理输入 | supervisor pending input |
| v8 | Supervisor 协议修复 | supervisor protocol repair |
| v9 | Run 工作看板 | run work board |
| v10 | Run Notes 结构化记忆 | run notes |
| v11 | 持久化工具审批 | durable tool approvals |
| v12 | Session Grant 与工具预算 | session grants and tool budgets |
| v13 | 类型化脚本进程提案 | typed script process proposals |
| v14 | Run 工具输出 Artifact | run tool output artifacts |
| v15 | 结构化记忆工具操作 | structured memory tool operations |
| v16 | Supervisor 结构化工具循环 | supervisor structured tool loop |
| v17 | Run execution lease | run execution leases |
| v18 | 跨进程模型取消 | cross-process model cancellation |
| v19 | 单 root Agent Coordinator | single-root agent coordinator |
| v20 | 幂等 Agent inbox 协议 | idempotent agent inbox protocol |
| v21 | 有界 Specialist 准入 | bounded specialist admission |
| v22 | Agent 归属的工作记忆 | agent-owned work memory |
| v23 | Specialist 完成报告 | specialist completion reports |
| v24 | 受 lease 保护的 Specialist Attempt | leased specialist attempts |
| v25 | root inbox 上下文交付 | root inbox context delivery |
| v26 | Specialist 模型调用账本 | specialist model call ledger |
| v27 | Specialist 上下文交付 | specialist context delivery |
| v28 | Specialist 协议修复 | specialist protocol repair |
| v29 | Specialist 调度与取消控制 | specialist schedule and cancellation control |
| v30 | 审阅门禁的 Specialist 委派提案 | review-gated specialist delegation proposals |
| v31 | 不可变 Specialist 委派审阅 | immutable specialist delegation reviews |
| v32 | 可恢复 Specialist 委派应用 | recoverable specialist delegation application |
| v33 | 不可变只读 Fan-out 计划 | immutable read-only fan-out plans |
| v34 | 有界只读 Fan-out 执行 | bounded read-only fan-out execution |
| v35 | 确定性 Finding 报告投影 | deterministic finding report projection |
| v36 | Artifact 支撑的 Finding 验证 | Artifact-backed finding validation |
| v37 | Finding 接受、修复生命周期 | accepted and fixed finding remediation lifecycle |
| v38 | 操作者控制的 Specialist 调度 | operator-controlled Specialist scheduling |
| v39 | 不可变 Run Skill 选择 | immutable Run Skill selection |
| v40 | root Skill 上下文来源 | root Skill context provenance |
| v41 | 不可变 Run 执行模式 | immutable Run execution mode |
| v42 | 审阅门禁的 Plan/Delivery 工作流 | review-gated Plan Delivery workflow |
| v43 | 不可变 Session 上下文来源 | immutable session context provenance |
| v44 | 不可变 Delivery 检查点门禁 | immutable Delivery checkpoint gates |
| v45 | 持久化操作者引导队列 | durable operator steering queue |
| v46 | 操作者引导队列控制 | operator steering queue controls |
| v47 | 最小化 Specialist Skill 上下文 | minimal Specialist Skill context |
| v48 | Go 主控 Sandbox Manifest 准备 | Go-owned Sandbox Manifest preparation |
| v49 | Sandbox 审批与禁用执行候选 | sandbox approval and disabled execution candidates |
| v50 | 禁用态 Sandbox 生命周期与 Artifact 绑定 | disabled Sandbox lifecycle and Artifact bindings |
| v51 | Sandbox 后端与输出禁用态预检 | disabled Sandbox backend and output preflight |
| v52 | 仅模拟的 Sandbox 后端证据与输出事务 | simulation-only Sandbox backend evidence and output transaction |
| v53 | 只读 Docker 生产环境观测 | read-only Docker production observation |
| v54 | 确定性 Docker 容器计划与假写事务 | deterministic Docker container plans and fake write transactions |
| v55 | 有界 Docker 创建、核验、删除演练 | bounded Docker create-inspect-remove rehearsals |
| v56 | 可恢复 Docker 演练意图、代际租约与检查矩阵 | recoverable Docker rehearsal intents, generation leases, and control matrix |
| v57 | 描述符固定与内核密封的宿主输入演练 | descriptor-pinned and kernel-sealed host-input rehearsal |
| v58 | daemon stage 前持久化宿主输入要求 | durable pre-stage host-input requirement |
| v59 | daemon 托管、回读核验的不可变宿主输入交接 | daemon-owned, readback-verified immutable host-input handoff |
| v60 | 确定性 Docker 运行时输入投影计划 | deterministic Docker runtime input projection plan |
| v61 | 可恢复 Docker 运行时输入卷应用 | recoverable Docker runtime input application |
| v62 | 保留运行时输入资源检查与精确清理 | retained runtime-input resource inspection and exact cleanup |
| v63 | 阻塞态 Docker 进程启动门设计审查 | blocked Docker process start-gate design review |
| v64 | 不可变 Run 执行环境档位选择 | immutable Run execution profile selection |
| v65 | 非授权 Docker 生产证据捕获账本 | non-authorizing Docker production evidence capture ledger |
| v66 | 可恢复 Docker 生产证据捕获 Attempt | recoverable Docker production-evidence capture attempts |
| v67 | Linux 只读 Docker 生产证据探针 | Linux read-only Docker production-evidence harness |
| v68 | 不可变 Docker 生产证据操作员审阅 | immutable Docker production-evidence operator review |
| v69 | 内容寻址惰性用户 Skill 安装账本 | content-addressed inert user Skill installation ledger |
| v70 | 外部 Skill 的 Run 固定选择与最小化上下文 | external-Skill Run selection and minimized context delivery |
| v71 | 有界外部 Skill 来源与交付只读投影 | bounded read-only external-Skill provenance and delivery projection |
| v72 | 幂等受控 Mission/Run/Session 创建账本 | idempotent controlled Mission/Run/Session creation ledger |
| v73 | 幂等 Run 生命周期与有界执行交接 | idempotent Run lifecycle and bounded execution handoff |
| v74 | 持久化 Run wake 重试意图与单一所有权 | durable Run wake retry intents and single-owner fencing |
| v75 | 显式前台 wake 消费与可恢复执行交接 | explicit foreground wake consumption and recoverable execution handoff |
| v76 | 已批准 FileEdit 的幂等独立 apply | idempotent independent apply for approved FileEdits |
| v77 | 非授权 Session 工作区证据挂载 | non-authorizing Session Workspace evidence attachments |
| v78 | 不可变操作者验证证据 | immutable operator verification evidence |
| v79 | 可恢复的 Run 无进展熔断 | recoverable Run livelock progress guard |
| v80 | 不可变操作者验证计划与检查清单 | immutable operator verification plans and checklists |
| v81 | 验证计划项与人工证据的不可变显式关联 | immutable explicit verification plan-item/evidence associations |
| v82 | 不可变累计上下文交接记忆 | immutable cumulative context handoff memory |
| v83 | 不可变验证快照回执历史 | immutable verification snapshot receipt history |
| v84 | 不可变且不授权的验证快照回执复核 | immutable non-authorizing verification snapshot receipt reviews |
| v85 | 可恢复且不启动的浏览器接纳、租约与人工复核门 | durable non-starting browser acceptance, lease, and operator-review gates |
| v86 | 操作者选择且不授权的执行交互边界 | operator-selected non-authorizing execution interaction boundaries |
| v87 | 受控命令的写前 intent 与不可变执行回执 | write-ahead intents and immutable receipts for controlled commands |
| v88 | 操作者选择、运行期重校验的四档执行权限 | operator-selected four-level execution permissions with runtime re-gating |
| v89 | Agent 固定命令提案、独立审批和不可信结果回送 | review-gated Agent fixed-command proposals with untrusted result projection |
| v90 | 非沙箱一次性宿主命令执行账本 | non-sandboxed one-shot host-command execution ledger |
| v91 | 独立的受限/完整调试 CDP 权限快照 | independent restricted/full-debug CDP permission snapshots |
| v92 | 可恢复且只追加的浏览器运行时生命周期记录 | recoverable append-only browser runtime lifecycle records |
| v93 | Analyzer 一次性请求、写前意图与恢复收据 | Analyzer one-shot request, write-ahead intent, and recovery receipts |
| v94 | 一次性 Analyzer 执行授权与原子消费防重放 | one-shot Analyzer execution capabilities with atomic replay-safe consumption |
| v95 | Analyzer 结果、Artifact 与审计事件原子提交 | atomic Analyzer result, Artifact, and audit-event commit |
| v96 | 用户审批档的精确宿主命令提案、审阅与恰好一次执行 | exact approval-mode host-command proposals, reviews, and exactly-once execution |
| v97 | 持久 Docker 生命周期所有权、代际租约与崩溃恢复 | durable Docker lifecycle ownership, generation leases, and crash recovery |
| v98 | 有界 Docker 容器 I/O 合同：只读输入投影、日志限额与原子输出提交 | bounded Docker container I/O contract: read-only input projection, log capture limits, and atomic output commit |
| v99 | 不可变 Docker Sandbox 产品准入、启动绑定、取消与终态回执 | immutable Docker Sandbox product admission, launch binding, cancellation, and terminal receipts |
| v100 | 算子价格快照与 Run 金额预算账本（预留/结算/释放） | operator price snapshots and the run monetary budget ledger (reserve/settle/release) |
| v101 | 结构化 Agent 依赖等待与唯一唤醒收据 | structured agent dependency waiting and unique wake receipts |
| v102 | 模型提议的有界 child 任务调度（core/readonly fan-out 分面、去重与准入） | model-proposed bounded child task scheduling (core/read-only fan-out surfaces, dedup, and admission) |
| v103 | 持久浏览器网络隔离证据与操作者 review | durable browser network containment evidence and operator review |
| v104 | 签名 Skill 包、团队 Catalog 与固定 URL/Git 导入（publisher 信任/撤销、版本 pin、审计） | signed skill packages, team catalog, and pinned URL/Git imports (publisher trust/revoke, version pins, audit) |
| v105 | 工作区一次性命令提案（不可变参数、审批指纹、操作者执行） | workspace one-shot command proposals (immutable parameters, approval fingerprints, operator execution) |
| v106 | typed 本地 Git 写操作台账（绑定指纹、幂等、回读收据） | typed local Git mutation operations (binding fingerprints, idempotency, readback receipts) |
| v107 | 网络作用域远端 Git 与 PR 操作台账（host/port/protocol/TTL/Run 绑定、脱敏收据） | network-scoped remote Git and PR operations (host/port/protocol/TTL/Run binding, redacted receipts) |
| v108 | Debug 终端会话台账（状态/cwd/resize/进程/Agent 输入态） | debug terminal session ledger (state/cwd/resize/process/agent-input status) |
| v109 | 完整 Supervisor 结构化工具注册表（child、Docker 与一次性命令） | complete Supervisor structured-tool registry (child, Docker, and one-shot commands) |
| v110 | 按 Run mode 固定的 root Skill 阶段子集与空交付账本 | Run-mode-bound root Skill phase subsets and empty-delivery ledger |
| v111 | 保存 Surface/Phase/Role 与调用策略的外部 Skill 安装账本 | external-Skill installation ledger preserving Surface/Phase/Role and invocation policy |
| v112 | 工具来源绑定、人工审查门禁的不可信 Skill 候选状态机 | tool-origin-bound, human-review-gated untrusted Skill candidate state machine |
| v113 | 允许 Debug 终端进入 Supervisor 持久工具调用账本 | admit the debug terminal into the durable Supervisor tool-call ledger |
| v114 | 层级项目指令快照、显式长期记忆与非授权会话连续性树 | hierarchical project-instruction snapshots, explicit long-term memory, and non-authorizing session continuity trees |
| v115 | 模型可调用的工作区工具与哈希保护文件变更 | model-callable workspace tools and hash-guarded file mutations |
| v116 | 增加 Run-owned command-runtime.v2 Job 与 Supervisor 调用账本 | add Run-owned command-runtime.v2 jobs and Supervisor call ledger support |
| v117 | 增加事务化 workspace-checkpoint.v1、恢复/Fork 账本与内容寻址 blob | add transactional workspace-checkpoint.v1, restore/Fork ledger, and content-addressed blobs |
| v118 | 增加 batch-delivery.v1、child Worktree/邮箱/交付复核与顺序合并队列 | add batch-delivery.v1, child worktrees/mailbox, delivery review, and ordered merge queues |
| v119 | 增加源码绑定的 ui-evidence.v1 Attempt、步骤与内容寻址真实浏览器产物 | add source-bound ui-evidence.v1 attempts, steps, and content-addressed real-browser artifacts |
| v120 | 增加两阶段 MCP Client Server、能力快照与 metadata-only 调用账本 | add two-stage MCP Client servers, capability snapshots, and metadata-only call audits |
| v121 | 增加签名 plugin.v1 安装、publisher 信任/撤销、回滚与受限 Hook 审计 | add signed plugin.v1 installs, publisher trust/revocation, rollback, and restricted-Hook audits |
| v122 | 增加 scheduled-job.v1、单实例租约/fencing、轮次与通知账本 | add scheduled-job.v1, singleton lease/fencing, round, and notification ledgers |
| v123 | 增加 git-advanced.v1 操作/序列审计与产品受管 worktree 注册表 | add git-advanced.v1 operation/sequence audits and the product-managed worktree registry |
| v124 | 增加 GitHub connection、PR/CI 快照、本地证据图及审批回写/恢复账本 | add GitHub connections, PR/CI snapshots, local evidence graphs, and approved write/recovery ledgers |
| v125 | 精确兼容旧 Windows 预览版 v97，并事务化重建 Docker lifecycle cleanup trigger | accept the exact legacy Windows preview v97 history and transactionally rebuild its Docker lifecycle cleanup trigger |
| v126 | 增加 Workspace Access 工作区执行权限合同与沙箱 readiness 闸门 | add the Workspace Access permission contract and sandbox-readiness gate |
| v127 | 增加 Run-owned Drydock、Workspace Trust、恢复/交付/清理事件与收据 | add Run-owned Drydocks, Workspace Trust, and recovery/delivery/cleanup events and receipts |
| v128 | 允许固定 Docker Standard Code 后端复用 Workspace Access admission | allow the fixed Docker Standard Code backend to reuse Workspace Access admission |
| v129 | 稳定 Thread 身份、Run succession 与无损生命周期投影 | stable Thread identity, Run succession, and lossless lifecycle projection |
| v130 | 增加 item 级流式工具调用的稳定响应、item 与 call 对齐标识 | add stable response, item, and call reconciliation identities for item-level streamed tool calls |
| v131 | 将 Command Runtime Job、Supervisor 广告与执行回执绑定到 adapter identity，并把旧记录投影为只读 legacy_unbound | bind Command Runtime jobs, Supervisor advertisements, and receipts to adapter identity while projecting legacy records as read-only legacy_unbound |
| v132 | 将 Docker Command Runtime 的进程内 stdin attach 绑定到不可变生命周期 WAL 与当前租约 | fence process-local Docker Command Runtime stdin attachment through the immutable lifecycle WAL and current lease |
| v133 | 原子提交 Standard Code 预设，并持久化等待静止边界的暂停配置意图 | atomically commit the Standard Code preset and persist pause-and-configure intents awaiting a quiescent boundary |
| v134 | 增加 Run 级 Web Search/Fetch/Citation 来源、不可变快照与幂等操作账本 | add Run-scoped Web Search/Fetch/Citation sources, immutable snapshots, and idempotent operation ledger |
| v135 | 持久化 Standard Code root Supervisor 的有界 Inspect→Edit→Execute→Verify 状态、预算、拒绝和结构化证据，并补全 Code Intel 调用账本约束 | persist bounded Inspect→Edit→Execute→Verify state, budgets, denials, and structural evidence for the Standard Code root Supervisor and complete the Code Intel call-ledger constraints |
| v136 | 增加精确高风险提案、有界 Run grant 消费、持久等待/恢复、write-ahead 不确定性与漂移失效账本 | add exact risk proposals, bounded Run-grant consumption, durable wait/resume, write-ahead uncertainty, and drift invalidation ledgers |
| v137 | 增加 Standard Code 最终 Checkpoint、Diff/Command Artifact 对齐、不可变完成收据与动态 stale 投影 | add the Standard Code final Checkpoint, aligned Diff/Command Artifacts, immutable completion receipts, and dynamic stale projection |
| v138 | 精确兼容 v136 Windows 中间预览历史，并重建缺失的 Supervisor 高风险提案权限触发器 | accept the exact intermediate Windows preview v136 history and rebuild its missing Supervisor risk-authority triggers |
| v139 | 为 Thread 增加保守默认、不可变且不授权的执行权限偏好；在安全边界同步当前 Run，并在未来后继 Run 中物化 | add conservative-by-default immutable non-authorizing Thread execution-permission preferences, synchronized to the current Run at a safe boundary and materialized into future successor Runs |
| v140 | 以 Thread 为唯一聊天生命周期边界，原子归档、恢复或删除其 Run 与 Session 投影，同时保留消息和审计证据 | make Thread the sole chat lifecycle boundary, atomically archiving, restoring, or deleting its Run and Session projections while retaining messages and audit evidence |
| v141 | 允许 Full CDP 子权限在任一非终态 Run 上立即降级并失效现有授权，同时继续要求升权前静止 | allow the Full CDP sub-permission to downgrade immediately and fence existing authority on any nonterminal Run while still requiring quiescence before escalation |
| v142 | 使 Debug 权限在不可变宿主命令与 Command Runtime 账本中继承 Full Access 的无状态执行能力 | make Debug inherit Full Access stateless execution in the immutable host-command and Command Runtime ledgers |
| v143 | 允许当前任务即时撤销高风险执行权限并原子释放活动执行租约，同时保持升权必须静止 | allow immediate current-task high-risk permission revocation with atomic lease release while keeping escalation quiescent |
| v144 | 为受控 Run/Thread 创建增加禁网或精确 HTTPS 主机 allowlist，并将请求意图绑定到幂等账本 | add disabled or exact-HTTPS-host allowlists to controlled Run/Thread creation and bind request intent to the idempotency ledger |
| v145 | 增加审计化的当前 Run 精确主机扩权、授权代际失效，以及后继 Run 的安全网络偏好继承 | add audited exact-host expansion for the current Run, authorization-generation fencing, and safe network-preference inheritance for successor Runs |
| v146 | 为 Thread 权限操作增加显式 deferred 效果，在不改写当前 Run 权限快照的情况下持久化下一执行 epoch 偏好 | add an explicit deferred effect to Thread permission operations, persisting the next execution-epoch preference without rewriting the current Run permission snapshot |
| v147 | 允许受控 Thread/Run 创建原子固定显式 Provider/model route，并保持 Mission、Run 与 Session 路由一致 | allow controlled Thread/Run creation to atomically pin an explicit Provider/model route while keeping Mission, Run, and Session routing consistent |
| v148 | 将 preparing Run 纳入 Thread 权限 deferred 绑定，使其保持原权限直到后继 Run 安全物化新偏好 | include preparing Runs in deferred Thread-permission binding so they retain their original authority until a successor safely materializes the preference |
| v149 | 增加绑定精确 Thread/Run/Turn/Supervisor call 与公网 HTTPS 主机的 Web Fetch 审批账本，支持允许一次、当前对话允许、拒绝及崩溃后原调用恢复 | add a Web Fetch approval ledger bound to the exact Thread/Run/Turn/Supervisor call and public HTTPS host, supporting allow-once, allow-for-thread, deny, and crash recovery of the original call |
| v150 | 将六个浏览器动作与 MCP 调用纳入 authority-bound Supervisor 工具账本，规范化精确 Run authority，并将历史无 authority 的 MCP 调用标记为不可恢复执行 | admit six browser actions and MCP calls to the authority-bound Supervisor tool ledger, canonicalize exact-Run authority, and mark historical authority-less MCP calls as non-resumable |
| v151 | 为 Supervisor 工具调用与 Command Runtime Job 增加不可变执行 Agent 归属账本；新记录保留精确 Agent/attempt，历史记录仅在可证明时标记 legacy root，否则明确标记 unknown | add immutable execution-Agent attribution ledgers for Supervisor tool calls and Command Runtime Jobs; retain exact Agent/attempt for new records and mark history as legacy root only when provable, otherwise explicitly unknown |
| v152 | 持久化 Thread 消息的文件准备意图，固定请求指纹和消息绑定，支持重启与后继 Run | persist Thread message file-preparation intent with fixed request fingerprints and message bindings across restarts and successor Runs |
| v153 | 将文件修改应用范围扩展至精确的 Run-owned Drydock，同时保留历史应用与审批身份 | extend file-edit application scope to the exact Run-owned Drydock while preserving historical application and approval identities |
| v154 | 增加相邻 Thread Run 共享工作目录的不可变绑定，并同步命令、检查点、交付与清理范围 | add immutable working-directory bindings between adjacent Thread Runs and align command, checkpoint, delivery, and cleanup scope |
| v155 | 增加同一 Thread 编程与计划续接的来源校验，复用既有事件和原始验收收据 | add provenance checks for coding and plan continuation within a Thread using existing events and original acceptance receipts |
| v156 | 允许一至三个有意义的计划选项及按需人工验收，保留原提案和请求收据 | allow one to three meaningful plan alternatives and on-demand manual acceptance while preserving original proposals and request receipts |
| v157 | 将审批后的续跑绑定到已提交的原用户输入，避免重复投递或新增消息 | bind post-approval continuation to the original committed user input without redelivery or an extra message |
| v158 | 增加不可变工作区图片、精确 Thread 消息绑定与待处理图片计数 | add immutable workspace images, exact Thread message bindings, and pending-image counts |
| v159 | 为 Git 和拉取请求操作增加一次性执行认领，固定首次开始时间 | add one-time execution claims for Git and pull-request operations with an immutable first-start timestamp |
| v160 | 增加不可变上传文件、可读性记录和精确 Thread 附件绑定，文件与图片分别计数 | add immutable uploaded files, readability records, and exact Thread attachment bindings with separate file and image counts |
| v161 | 将受限历史检索工具纳入 Supervisor 账本，保留旧调用、权限约束和历史游标的行身份 | admit fenced history-recall tools to the Supervisor ledger while preserving existing calls, authority constraints, and row identities used by history cursors |
| v162 | 增加不可变的 Full Access 文件自动授权来源 | add immutable automatic Full Access FileEdit authorization provenance |
| v163 | 增加宿主 Command Runtime 网络意图及运行时授权来源 | add host Command Runtime network intent and runtime-grant provenance |
| v164 | 将不可变 Full Access 文件自动授权扩展到不覆盖目标的移动 | extend immutable automatic Full Access FileEdit authorization to non-overwriting moves |
| v165 | 接入 GitHub、HN 与 RSS 来源连接器并保留原工具账本和引用身份 | admit GitHub, HN and RSS source connectors while preserving tool-ledger and citation identity |
| v166 | 恢复精确历史网页审批失败后 paused Run 的普通续聊，保留原调用与证据 | continue paused Runs after exact historical web-approval failures while retaining original calls and evidence |
| v167 | 增加只读零模型计划的不可变持久观察同意收据，不回填旧计划 | add immutable durable observation consent receipts for read-only zero-model jobs without backfilling existing jobs |
| v168 | 保留原消息身份，为待处理消息增加 CAS 修订记录和按消息绑定的附件提交证据 | preserve original message identity with CAS revisions and message-bound attachment delivery evidence |
| v169 | 增加绑定 Run 权限与 Supervisor 调用账本的 Agent 浏览器动作、敏感意图和截图收据 | add Run-authority-bound Agent browser actions, sensitive intents, and screenshot receipts to the Supervisor call ledger |
| v170 | 增加私有 Provider 工具回放与上下文超窗恢复账本，不重建旧工具调用 | add private Provider tool replay and context-limit recovery ledgers without rebuilding existing tool calls |
| v171 | 增加当前执行段的操作员文字纠正模式、attempt 绑定与不可变 claim | add current-turn operator text steering, attempt binding, and immutable claims |
| v172 | 原子转换排队文字为当前引导，持久保存原消息、替代消息和执行身份的不可变关联收据 | atomically promote queued text to steering with immutable source, replacement and execution identity receipts |
| v173 | 将只读内置技能发现与读取纳入已有 Supervisor 账本，保留旧行身份与权限约束，并从成功收据重建精确版本指导 | admit read-only built-in skill discovery and reads to the existing Supervisor ledger, preserve row identities and authority guards, and reconstruct exact-version guidance from successful receipts |
| v174 | 在原协议失败事务中原子保存有界脱敏的原生工具请求拒绝诊断，保持原 attempt 身份，不生成可执行调用、Provider 回放或历史回填 | atomically preserve bounded redacted rejected native request diagnostics in the original protocol-failure transaction, retaining attempt identity without executable calls, Provider replay or historical backfill |
| v175 | 为 Specialist 固定有界的 attempt 任务快照与原始来源绑定，保留完整有效委托和工作项，并支持精确替代或撤回；投递与消费仍绑定当前 attempt | pin bounded per-attempt Specialist task briefs and original source bindings, retain complete effective instructions and owned work, and support exact replacement or withdrawal while delivery and consumption stay attempt-bound |
| v176 | 将普通 assistant 的私有原生回放绑定到成功模型结果和已接受的 Root 历史，保持工具配对与重新打开时的来源，不回填旧历史 | bind private native ordinary assistant replay to successful model results and accepted Root history, preserving tool pairing and reopen provenance without backfilling old history |
| v177 | 在原插件安装表中支持可移植 Agent Plugin 快照，保留旧签名安装、对象与审核历史；来源声明不产生权限 | admit portable Agent Plugin snapshots in the existing installation table while preserving signed legacy installations, objects and reviews; source declarations confer no authority |
| v178 | 在原 Run 与 Thread 权限快照中加入 ask、auto、full，逐字保留旧五档数据与审核历史，不将恢复记录转换为执行授权 | add ask, auto and full to existing Run and Thread permission snapshots, preserving legacy rows and review history without converting recovery records into execution authority |
| v179 | 扩展原文件操作授权来源账本以绑定三档操作决策及运行时撤销围栏，保留旧行、不可变触发器和审核来源 | extend the existing file-operation authorization source ledger for three-mode decisions and runtime revocation fences while preserving legacy rows, immutable triggers and approval provenance |
| v180 | 扩展原 Command Runtime Job 账本以保存三档权限、适配器、运行时与执行围栏，保留旧 Job、行身份和所有权收据 | extend the existing Command Runtime Job ledger for three-mode permissions, adapter identity and runtime fences while preserving legacy Jobs, row identities and owner receipts |
| v181 | 在原 Docker 准入账本接入三档权限元组，保留旧记录与子记录，并继续强制 v131 的活动候选租约及原预算约束 | admit three-mode permission tuples in the existing Docker admission ledger, retaining legacy rows and children together with the v131 active-candidate lease gate and original budget constraints |
| v182 | 将八个 Sandbox 插入触发器的冻结用量相等约束替换为当前事务预算校验；活动候选允许单调增长，静止候选保持相等，精确租约与旧数据不可变，Docker 准入额度取当前余额 | replace frozen usage equality in eight Sandbox insert triggers with current transaction budget checks; allow monotonic usage for live candidates, retain equality for quiescent candidates, exact leases and immutable history, and derive Docker admission limits from current remaining budgets |
| v183 | 候选收据绑定到统一 Plugin 安装生命周期 | Candidate receipts bound to the unified Plugin installation lifecycle |
| v184 | Run 选择固定到真实 Plugin Skill 组件 | Run selections pinned to real Plugin Skill components |
| v185 | 现有命令 Job 账本记录显式操作者调用来源 | Explicit operator invocation provenance in the existing command Job ledger |
| v186 | 受控任务创建支持有界预算与只能收紧的项目配置，固定 Run 配置快照并保留旧创建约束 | admit bounded budgets and narrowing-only project configuration in controlled task creation, pin Run configuration snapshots and preserve legacy creation guards |
| v187 | 在现有 Hook 审计中追加版本绑定的 Plugin 指纹、声明动作、可空观测拒绝结果及工作区查询索引，保留旧 outcome 与未知决策，并约束新记录一致性 | append version-bound Plugin fingerprints, declared actions, nullable observed rejection decisions and a workspace query index to existing Hook audits; preserve historical outcomes and unknown decisions and enforce consistency for new records |
| v188 | 在现有 Run 执行配置、受控交互与 Standard Code 预设快照中接入显式 sbx 后端，保留旧记录、审批约束和 Thread 延续触发器 | admit an explicit sbx backend in existing Run execution profiles, controlled interaction and Standard Code preset snapshots while preserving historical records, approval constraints and Thread continuation triggers |
| v189 | 为 Command Runtime Job 的写入约束补充 docker_sandboxes 后端与 sbx 执行配置的映射，保留既有操作、权限、工作区和执行租约检查 | admit the docker_sandboxes backend with the sbx execution profile in Command Runtime Job insertion while preserving existing operation, permission, workspace and execution lease checks |
| v190 | 为子代理加入持久化工具绑定、调用轮次和执行收据，扩展模型调用记录以支持工具循环并保留历史行身份 | add durable child-agent tool bindings, rounds and execution receipts, extending model-call records for tool loops while preserving historical row identities |
| v191 | 为子代理连续调度增加不可变的操作者意图摘要及查询索引，支持原调度的精确重放 | add an immutable operator-intent digest and lookup index to continuous child-agent schedules for exact replay of the original schedule |
| v192 | 增加独立网页访问选择账本及 Run 范围校验，保留旧范围与授权历史，支持修订绑定的禁止、规则和完全访问及撤销 | add an independent web-access selection ledger and Run scope guards while preserving historical scopes and approvals; bind disabled, rules and full access and revocation to exact revisions |
| v193 | 增加项目依赖准备的操作、进度与来源授权记录，绑定 Run、项目文件指纹和执行租约，支持缓存复用、取消和重启后读取 | add project preparation operations, progress and source consent bound to the Run, inspected project fingerprint and execution lease, with cache reuse, cancellation and durable reads after restart |
| v194 | 将按需 MCP 工具搜索纳入现有 Supervisor 账本，保留旧调用行身份、权限围栏和不可变记录 | admit on-demand MCP tool search to the existing Supervisor ledger while preserving historical call identities, authority fences and immutable records |
| v195 | 为消息内选定的 MCP 资源与 Prompt 保存不可变参数、一次读取状态、结果收据和参考材料，支持恢复已保存结果并禁止未知结果自动重放 | retain immutable message-selected MCP resource and Prompt parameters, one-time read state, result receipts and evidence, recovering saved results without replaying unknown outcomes |
| v196 | 在 MCP 服务传输约束中接入 SSE 与 WebSocket，保留历史行身份、描述符、审批及审计关联 | admit SSE and WebSocket in MCP server transport constraints while preserving historical row identities, descriptors, approvals and audit relationships |
| v197 | 在既有 Skill 选择账本中支持原生组件的精确安装与代次绑定、可空作者版本和同包多组件选择，保留旧选择行身份、指纹与收据 | admit exact installation and generation bindings, optional author versions and multiple native Skill components per package in the existing selection ledger while preserving historical row identities, fingerprints and receipts |
| v198 | 增加有来源的待确认经验队列，确认事务写入既有项目记忆，终态仅保留无正文审核记录，防止重复确认或删除后复活 | add source-bound experience candidates, atomically promote explicitly confirmed entries to existing project memory, and retain content-free resolution records to prevent duplicate confirmation or revival after deletion |
| v199 | 复用既有审批与撤销账本，为 Computer Use 应用授权保存不可变的应用身份、来源审批及范围指纹 | persist immutable application identity, source approval and scope fingerprints for Computer Use app grants using the existing approval and revocation ledger |
| v200 | 保存逐子任务的读取路径、累计工具与费用额度，并为既有费用预留回填 Agent 归属；旧任务默认无读取授权 | persist per-child read paths, cumulative tool and cost limits, and backfill Agent ownership on monetary reservations; historical children retain no read authority |
| v201 | 在既有工具和命令账本中接入有界项目测试，保留历史调用行身份与 Agent 引用，并以精确测试调用授权 Plan 命令 Job | admit bounded project tests in existing tool and command ledgers while preserving historical call row identities and Agent references; authorize Plan command Jobs through exact pending test calls |
| v202 | 持久化执行型子 Agent 的工具配置、精确调用授权及命令和文件修改归属，保留历史子 Agent 的原有行为 | persist execution-worker tool profiles, exact child-call authority, command-job and file-edit attribution while preserving historical child behavior |
| v203 | 增加不可变工作流定义、执行状态和步骤结果账本，支持有界 worker、条件分支和循环的恢复与控制 | add immutable workflow definitions, execution state and step-result ledgers for recovery and control of bounded workers, conditional branches and loops |
| v204 | 为既有 Thread 保存有期限和次数上限的 PR 跟进记录、反馈游标、原轮次输入及执行租约，支持暂停和恢复并防止重复续跑 | persist bounded PR followups, feedback cursors, original turn inputs and execution leases on existing Threads, supporting pause and recovery without duplicate continuation |
| v205 | 保存项目自动动作的具体配置信任与修订，将编辑后格式化和结束前测试绑定到既有工具调用及原执行来源，支持配置变化后重新审阅和恢复去重 | persist reviewed project hook definitions and revisions, binding after-edit formatting and before-finish tests to existing tool calls and their original triggers for re-review and deduplicated recovery |
| v206 | 持久化按尝试绑定的子 Agent 上下文覆盖轮次、来源摘要和过长输入恢复状态，约束投影单调推进 | persist attempt-bound child Agent context coverage, source digests and oversized-input recovery state, enforcing monotonic projection progress |
| v207 | 保存用户确认的 Thread 固定要求、修订、提议和操作记录，并将提议接入既有工具账本 | persist user-confirmed Thread requirements, revisions, proposals and operations, admitting proposals to the existing tool ledger |
| v208 | 保存空闲 Thread 的压缩操作、模型用量和结果，以现有费用预留账本约束压缩调用 | persist idle Thread compaction operations, model usage and results, bounding compaction calls through the existing monetary reservation ledger |
| v209 | 保存子 Agent 委派时明确选入的父历史范围及来源摘要，回读时重新核对来源可见性，默认保持自身历史范围 | persist explicitly selected parent history scope and source digests at child admission, rechecking source visibility on recall while defaulting to the child's own history |
| v210 | 为定时任务加入自动执行模式、不可变执行同意和按轮次绑定的执行回执，保留既有计划与观察同意 | add automatic execution mode, immutable execution consent and occurrence-bound execution receipts for scheduled jobs while preserving existing schedules and observation consent |
| v211 | 仅为 Plan 中宿主准备的注册测试放行长后台 Job，保留 root/worker、精确命令、离线及当前权限条件 | admit long background Jobs only for host-prepared registered tests in Plan while retaining root/worker ownership, exact command, offline and current-permission conditions |

</details>
