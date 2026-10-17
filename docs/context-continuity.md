# Project Instructions, Long-Term Memory, And Session Continuity

状态 / Status: instructions and explicit memory in schema v114; experience candidates in schema v198

2026-09-11 更新：普通 Thread 的 Supervisor 主链已接入现有持久压缩器；本批没有新增 Schema。超过历史阈值或模型窗口压力时先保存摘要，再组织模型请求，不发送逐条静默删历史后的请求。摘要、原文提取和后继继承均有容量边界；完整原始历史继续保留，摘要不授予权限。实现、重启/重复压缩/工具证据及失败边界验收见 [普通 Thread 上下文连续性验收](UX_CONTEXT_CONTINUITY_VALIDATION.md)。下文 v114 说明仍描述各自原有接口，不代表这些入口自动等同普通 Thread 主链。

协议 / Protocols: `project_instruction_snapshot.v1`, `context_memory.v1`, `context_memory_candidate.v1`,
`continuity_snapshot.v1`, `session_continuity_node.v1`, `session_tree.v1`

## 中文说明

### 当前任务的固定要求与主动压缩

2026-10-17 更新（schema v204–v206）：普通 Thread 在自动摘要之外，按剩余预算保留
近期用户消息的完整原文。更早的原文仍在历史记录中，可通过 `history_search` / `history_read`
回读；有界近期原文不能替代持续要求的显式固定。

在对话的 **查看上下文 → 本次任务始终保留** 中，可以直接添加、编辑或取消固定要求。
模型也可以调用 `context_pin_propose` 提出建议；建议显示原文，并等待用户选择：

1. **本次任务始终保留**：确认并固定所展示的原文。
2. **仅当前讨论**：发送普通答复，不创建固定项。
3. **自定义回复**：自行回答；只有勾选“将这段回复固定到本次任务”才会固定。

固定项属于当前 Thread，在同一任务的后继执行中继续生效。Supervisor 和执行中的子 Agent
都在可裁剪历史之外加载固定原文；新 Thread 不自动继承。最多保留 32 项，单项 8192 个字符，
合计 32768 个字符；模型窗口装不下必需上下文时，会明确提示删减固定项或更换模型。
它们提供任务要求，工具调用仍执行当前 Ask / Auto / Full、禁用规则和审批。

**查看上下文 → 摘要与压缩 → 压缩当前对话** 支持填写压缩重点。CLI 等价入口为：

```powershell
cyberagent thread compact <thread-id> --focus "保留持续限制、已做决定和未完成工作" --json
```

压缩只在当前任务没有活动执行或待处理用户输入时开始；保留最近 4 条消息，摘要与较早
消息的压缩标记同时保存，完整历史不删除。历史已经超窗时，只缩小这次摘要的临时输入，
并在结果和摘要中显示未被摘要模型读取的消息数量。生成失败或期间来源改变时不覆盖旧上下文。
重复同一操作键会回读既有结果；CLI 会先打印操作键，重试可传入 `--operation-key`。
模型调用启动后进程异常中断、结果无法确认时，该压缩操作保持待核对并拒绝自动重发，
以免重复调用和费用；普通对话仍可继续。

子 Agent 的较大已完成工具结果可以投影成简短回执，并用 `specialist_result_read` 在当前
Run / Agent / attempt 范围内分页读回原文。未完成、待审批或结果未知的调用不被当作完成记录。
每页重新检查执行租约和当前权限，读取也计入工具预算；原始工具记录一直保留。

Kimi 继续保留要求的原生推理历史，不因普通的消息条数阈值提前停止。真实窗口不足且无法
保留必需历史时返回明确的上下文超限提示；当前实现不裁掉这些原生推理内容。

实现取舍参考 [Claude Code 上下文窗口](https://code.claude.com/docs/en/context-window) 的工具输出
缩减、规则重载与手动压缩，以及 [Codex 的压缩实现](https://github.com/openai/codex/tree/ce0323f2f2d3b25e82806cd555237da3db4e1897/codex-rs/core/src)
的有界近期用户原文与压缩替换。这里的固定项、弹窗确认和恢复行为由本项目的 Thread、权限
与费用记录共同约束。

### 一条不可跨越的边界

“长期存在”不代表“更高优先级”。针路簿按以下顺序解释上下文：

1. 系统与操作者当前输入；
2. Go 运行时安全策略、Scope、Policy、预算和当前权限快照；
3. Run 的显式模式、配置与用户选择；
4. 固定到 Run 的项目指令与显式 user/project memory；
5. 普通仓库文件、历史消息和工具输出。

项目文本与记忆只能提供工作流、格式、偏好、事实和验证建议。它们不能授予或恢复
Tool、Network、Secret、Debug、Plugin、Hook、MCP、审批、Credential、进程、终端租约、
执行档位或 Policy override。模型声称“恢复权限”不产生任何 Go authority。

### 层级项目指令

发现器只在已注册 Workspace 边界内读取：

- 每一级目录中的 `AGENTS.md` 与 `CLAUDE.md`；
- 每一级目录中的 `.prayu/instructions.md`；
- 每一级目录下 `.prayu/rules/**/*.md` 的稳定字典序集合；
- Workspace 根的 `.prayu/instructions.ignore`，用于排除规范的相对路径 glob。

顺序从 Workspace 根到目标所在目录，越近的目录优先级越高；同一级依次为
`AGENTS.md`、`CLAUDE.md`、针路簿 instructions、排序后的 rules。每个 source 保存相对
path、content SHA-256、scope、depth、precedence、加载时间、trust、适用 target、
`why_effective`、脱敏状态和全部 false 的授权字段。冲突保留双方并解释最近目录获胜，
不会静默删除低优先级来源。

边界为最多 64 个文件、单文件 64 KiB、总计 256 KiB、目标/规则深度 32、ignore
16 KiB/128 条。绝对路径、`..` 逃逸、NUL、无效 UTF-8、空文件、特殊文件、symlink、
junction/reparse、并发修改、超限和非法 ignore 均 fail closed。Windows 使用大小写不敏感
的规范路径比较；Linux/macOS 保留大小写语义。内容在 hash 和交付前经过 Secret 脱敏。

Run 创建时把完整快照和 fingerprint 固定在 `RunConfig`，并在 schema v114 的不可变
revision ledger 中保存。磁盘变化只会让检查视图显示 `stale` 和 added/changed/removed/
order diff；运行中的 Run 仍使用旧快照。只有操作者以旧 fingerprint 为乐观并发条件进行
显式确认旧 pinned fingerprint 与已查看的 live fingerprint，才会追加新 revision。

```powershell
cyberagent context instructions --workspace demo --target internal/parser/input.go --json
cyberagent run create --workspace demo --goal "review parser" --profile review
cyberagent context instructions --run <run-id> --json
cyberagent context instructions --run <run-id> --confirm `
  --expected-fingerprint <pinned-sha256> `
  --expected-live-fingerprint <reviewed-live-sha256> --json
```

Desktop 的 **Context / 上下文** 页显示固定/磁盘 fingerprint、为何生效、适用范围、
优先级冲突和漂移确认。OpenAPI 等价入口是：

- `GET /api/v1/runs/{run_id}/project-instructions?target_path=...`
- `POST /api/v1/runs/{run_id}/project-instructions/refresh`

### 显式 user/project memory

只有 CLI、Desktop 或带独立 control bearer 的 HTTP 操作者动作可以创建、编辑、启用、
禁用或删除正式长期记忆。Go 可以从有来源的决策/总结笔记生成待确认经验；操作者查看并
确认后，才以 `operator_explicit` 写入项目记忆。候选本身不参与模型上下文或记忆检索。

每条记录保存 scope、title、脱敏后的 content/hash、status、来源引用、排序去重引用、
可选 retention、创建/更新操作者、版本和时间。user scope 固定为 `local-user`；project
scope 固定到 Workspace ID。写操作使用 optimistic `expected_version`，防止两个界面覆盖。
保留期不得早于创建时间，也不得超过十年；到期或 disabled 的记录不进入新 prompt。

```powershell
cyberagent context memory create --scope project --workspace demo `
  --title "Test convention" --content "Run unit tests before packaging" --retention 720h
cyberagent context memory list --scope project --workspace demo --all --json
cyberagent context memory edit <memory-id> --version 1 --content "Run unit and API tests" `
  --reference docs/testing.md
cyberagent context memory disable <memory-id> --version 2
cyberagent context memory enable <memory-id> --version 3
cyberagent context memory export --scope project --workspace demo --all
cyberagent context memory delete <memory-id> --version 4
```

Secret-like content 默认拒绝；只有 `--redact-sensitive` 或 Desktop 的显式脱敏选项允许
保存脱敏结果。`.env`、credentials、private keys、Secret、stdin、terminal input、
keystrokes 等敏感来源/引用始终拒绝，不能用脱敏选项绕过来源边界。正文上限 16 KiB，
引用最多 32 条、每条 512 bytes。

#### 从任务结论生成待确认经验

**Context / 上下文 → 当前项目 → 待确认经验** 显示来源 Run、Note 版本、引用和完成记录。
展开候选可查看原文，选择“确认并保存到项目”或“忽略此候选”。确认后沿用下方记忆的编辑、
禁用和删除入口。只读连接可检查候选，确认和忽略需要 control bearer。

Go 在列表读取时有界检查当前 Workspace 的来源笔记，最多扫描 200 个来源、返回 50 条
候选。笔记须为 active 的 decision/summary，具有来源引用或证据，且对 root 可见。
完成依据包括 Run 完成、已完成的 Thread 回复、交付检查点或通过的交付报告。
Thread 回复完成只说明本次回复结束；候选的置信说明会明确要求核实结论及适用范围。
单独把 Run 暂停不会被当成完成依据。

候选保留源 Note 版本、完成依据和内容指纹。确认同时提交已查看的候选版本、指纹、
源 Note 版本和 `confirm=true`；来源发生变化时要求重新检查。SQLite 在同一事务中创建
正式记忆并处理候选。处理后的候选清除标题、正文和引用内容，只保留来源身份、指纹及
审核记录；重复请求不会重复写入，删除正式记忆后也不会从候选恢复旧正文。

接口为 `GET /memory-candidates?workspace_id=...`、
`GET /memory-candidates/{candidate_id}?workspace_id=...` 和
`POST /memory-candidates/{candidate_id}/confirm|reject`，均在 `/api/v1` 下。
读取只维护派生待审队列，不调用模型或工具，不创建正式记忆。

#### 模型按需回读和上下文预算

root Supervisor 的 `history_search` / `history_read` 可检索并分页读取：

- 当前本机用户和当前 Workspace 中 active、未过期的已确认记忆；
- 当前 Thread 的合法 Run 沿革内、对 root 可见的 active 笔记；
- 原有消息、工具回执、历史摘要及连续性快照。

同一 Workspace 的新 Thread 可取回已确认项目记忆。原始笔记仍受 Thread 沿革和
可见性限制。记忆和笔记的读取标识绑定当前 Run、Workspace、版本、正文与来源状态；
每页重新检查。来源改变返回冲突，已禁用、过期、删除或不可见时不返回正文，模型需重新
搜索。旧搜索游标继续读取旧来源集合，新游标按稳定排序键续页，避免删除导致偏移跳项。

背景上下文为正式记忆预留最多 `min(2048, 总背景预算 / 4, 优先上下文后剩余空间)`
token。真正必需的任务目标、历史摘要、连续性、inbox 和项目指令保持既有要求；普通
笔记不会先耗尽记忆预算。记忆按 user 优先、再按 ID 确定排序，单条放不下会继续尝试
后续较小记录。每个 scope 最多加载 100 条；需要其他记录时可使用检索工具。

每次逻辑模型调用前重新加载 active、未过期记忆；同一轮工具调用后也重新检查。
transport 对同一次请求的重试保留该请求。已发送给模型的历史文本仍保留其历史身份，
不能用删除当前记忆反向撤回已经发送的内容。

审计需区分三个事实：

1. **已配置**：当前记忆表中的内容、版本、状态及保留期。
2. **实际注入**：`model.started` 的 context 审计中，included 来源记录记忆 ID、版本、
   SHA-256 和估算 token；omitted 来源有明确的配额、必需上下文或项目指令挤占原因。
3. **模型遵循**：由实际回复及工具结果判断。配置或注入记录本身不证明模型遵循。

#### 删除与隐私

`delete` 是物理删除，不是 tombstone；成功结果固定 `recoverable=false`。之后的列表、读取、
export 和新 prompt 均无法返回正文。已存在的不可变 checkpoint 只保存 memory ID、版本和
content hash，不保存 memory 正文；树会把缺失引用标为 deleted。数据库文件、用户自行
制作的 export、备份、日志转储或已经发送到外部 Provider 的旧 prompt 不由这次 SQLite
删除反向擦除，操作者需按自己的备份/Provider 保留策略处理。删除 API 需要 exact ID、
current version、control bearer 和明确的 DELETE body。

### Checkpoint、Fork、Resume 与 session tree

每个绑定 Workspace 的新 Run 有一个 root continuity node。显式 checkpoint 捕获一个有界、
脱敏的 `continuity_snapshot.v1`：最新 compaction summary、最多 20 条近期消息及来源、最多
200 条当前 active memory 引用、项目配置/指令 fingerprint、Git branch/full HEAD，以及
明确列出的继承项。快照 fingerprint 不包含加载时间，因此相同语义可稳定比较。

```powershell
cyberagent session checkpoint <run-id> --title "Parser reviewed" --summary "Ready for tests" --json
cyberagent session tree <session-id> --json
cyberagent session fork <continuity-node-id> --goal "Try alternate implementation" --json
cyberagent session resume <continuity-node-id> --json
```

Fork/Resume 原子创建新的 Mission、Run、Session 和 branch marker，并把选定 snapshot 固定到
新 Run。它们继承的只有有界历史上下文、有效 memory 引用、固定项目配置/指令和任务预算/
模式；明确不继承 approvals、capability grants、credentials、debug sessions、execution
leases、network authorization、processes、terminal leases 或 execution profiles。新 Run
从普通 `created` 状态和默认非授权执行快照开始。

树还投影 compaction、关键决策 Note、Artifact 和 Delivery checkpoint，并标注 memory
disabled/expired/deleted/version drift、Git branch/HEAD drift。衍生节点仅用于浏览，不能作为
Fork/Resume authority。Desktop 支持检查点、分支动作和两个节点的 fingerprint/Run/Session/
状态以及 project config/instructions 指纹和 Git branch/HEAD 比较；CLI `--json` 与 OpenAPI
提供等价机器导出。

### 威胁模型

| 威胁 | 控制 |
|---|---|
| 恶意 `AGENTS.md` 声称自己是 system 或要求联网/读 Secret | trust 固定为 untrusted，Go 类型中的授权位必须全 false，安全策略优先 |
| 路径逃逸、symlink/junction 替换、特殊文件或超大 rules tree | exact-root、逐级 `Lstat`/resolve、类型/深度/大小/数量限制，失败不降级 |
| 读取期间文件被替换 | 读取前后 metadata 比较并二次读取字节比较；任何差异拒绝整个发现 |
| 文件修改静默影响 active Run | 创建时不可变快照；refresh 同时绑定旧 pinned 与已查看的 live fingerprint、diff 和显式 confirm |
| Prompt/Tool 自动种植长期记忆 | store service 要求显式 operator actor；model/tool/system actor 被拒绝 |
| 凭据、终端输入或敏感文件被长期保存 | 内容 Secret 检测与默认拒绝；敏感 source/reference 永久拒绝 |
| 过期/已删除记忆继续影响分支 | 新 prompt 只选 active/unexpired；树显示 drift；checkpoint 只存引用/hash |
| Fork 恢复旧审批、网络或终端控制 | snapshot authority 全 false；新 Run 重新创建所有权限/lease/profile 状态 |
| 数据库重启把进程权限复活 | continuity 是数据，不是 runtime handle；进程、bearer、credential 不持久化 |
| 并发编辑覆盖 memory 或 refresh | expected version/fingerprint 乐观并发检查，冲突要求重新读取 |

### 迁移和恢复

schema v114 在一个校验和事务中创建 `context_memories`、
`run_instruction_snapshots`、`session_continuity_nodes` 和索引/不可变触发器；不改写既有
Session 消息、Run Note 或 compaction。旧 Run 没有固定项目指令时可先只读检查 live diff，
再显式确认创建 revision 1。所有新对象从 SQLite 重启后可读取，但 continuity 永远不能恢复
进程内 capability。迁移测试验证 v113 数据保留与 v114 重开。

## English Guide

### Non-authority boundary

Durability does not imply precedence or authority. System/operator input and Go-owned safety
policy remain above explicit Run choices; pinned project instructions and memories remain below
those controls and above ordinary repository/tool evidence. Their Go representations cannot grant
tools, network, secrets, Debug, plugins, hooks, MCP, approvals, credentials, processes, terminal
leases, execution profiles, or policy overrides.

### Hierarchical discovery and immutable Run snapshots

Within the exact registered Workspace, Traverse Board discovers `AGENTS.md`, `CLAUDE.md`,
`.prayu/instructions.md`, and sorted `.prayu/rules/**/*.md` from root to the target directory.
Nearer directories have higher precedence. Each source retains its canonical relative path,
content digest, scope, depth, precedence, load time, trust class, target applicability,
why-effective explanation, redaction state, and an all-false authority projection. Conflicts remain
visible rather than silently dropping the lower source.

Discovery fails closed on escapes, invalid UTF-8/NUL, special files, symlinks/reparse points,
concurrent modification, malformed ignore rules, or the 64-file/64-KiB-each/256-KiB-total/32-depth
bounds. Run creation pins the complete snapshot and fingerprint. Later disk drift produces an
explainable diff but cannot change an active Run; an operator must confirm against the exact prior
fingerprint and the reviewed live fingerprint to append a new immutable revision.

### Explicit memory and deletion

User/project memory is created only by an explicit operator surface. Go derives a separate
review queue from source-linked decision/summary notes after a recorded completion boundary.
Only operator confirmation promotes a candidate to project memory. Records carry provenance, optimistic version,
status, optional retention, references, redaction metadata, and audit timestamps. Disabled or
expired records do not enter new prompts. Sensitive content is rejected unless explicit redaction
is requested; sensitive sources such as credentials, private keys, `.env`, stdin, and terminal
input are always rejected.

Deletion physically removes the exact current version and is reported as unrecoverable. Existing
immutable checkpoints contain only the memory identity/version/digest, never its body, and report a
deleted-reference warning afterward. Independent exports, backups, and text previously transmitted
to a Provider remain subject to their own retention policy.

### Experience review and model recall

The Context panel's project scope shows pending candidates with their exact content, source Run,
Note version, references, and completion record. A finished reply establishes only the reply boundary;
the operator still verifies accuracy and applicability. Listing scans at most 200 sources and returns
at most 50 candidates. Confirmation requires the reviewed candidate version, fingerprint, source
version, and explicit consent. Source validation and promotion occur in one SQLite transaction.
Resolved candidate tombstones retain audit identities but clear copied content and references,
so deletion of the resulting memory cannot be undone through candidate replay.

The existing root-only `history_search` and `history_read` tools also read active, unexpired local-user
and current-Workspace memories, plus active root-visible notes in the current Thread's validated Run
lineage. New Threads can recall confirmed project memory; raw notes remain Thread-scoped.
Versioned source handles bind the current Run and Workspace and recheck content, state, visibility,
and retention on every page. Changed sources require a new search. Legacy cursors retain their
original source set; new cursors use stable keyset pagination.

The memory subbudget is `min(2048, background budget / 4, space after priority context)`. Required
goals, summaries, continuity, inbox, and project instructions keep their existing contracts.
User memory precedes project memory, then IDs determine selection order; oversized entries do not
prevent smaller entries from fitting. At most 100 active records per scope enter automatic selection.
Every logical model call reloads current memory, including calls after tools within one turn;
transport retries retain the original request. Previously delivered historical text cannot be retracted.

Configuration, delivery, and model compliance are separate evidence. `model.started` context audit
records the included memory ID/version/digest and estimates; omitted records carry a quota,
required-context, or project-instruction reason. Actual replies and tool receipts establish compliance.

### Continuity tree

Checkpoints capture bounded redacted summaries, provenance-bearing recent messages, active memory
references, pinned configuration fingerprints, and exact Git identity. Fork and Resume create a new
Mission/Run/Session atomically from that snapshot. They never inherit approvals, capability grants,
credentials, Debug sessions, execution or terminal leases, network authorization, processes, or
execution profiles. The browsing tree combines stored nodes with compaction, decision, Artifact,
and Delivery projections and reports memory/Git drift. Desktop branch comparison includes context,
project-config, project-instruction, and Git identities; CLI JSON and OpenAPI expose the same
non-authorizing state.

### API surface

- Read: `GET /memories`, `/memories/export`, `/memories/{id}`,
  `/memory-candidates?workspace_id=...`, `/memory-candidates/{id}?workspace_id=...`,
  `/runs/{run_id}/project-instructions`, `/sessions/{session_id}/tree`.
- Control: `POST /memories`, `PATCH|DELETE /memories/{id}`,
  `POST /memory-candidates/{id}/confirm|reject`,
  `POST /runs/{run_id}/project-instructions/refresh`,
  `POST /runs/{run_id}/continuity-checkpoints`, and
  `POST /continuity-nodes/{node_id}/fork|resume`.
- All paths live under `/api/v1`; mutations require the distinct control bearer, strict JSON,
  bounded bodies, duplicate/unknown-field rejection, and server-owned operator provenance.

See [ADR 0115](adr/0115-non-authorizing-durable-context-continuity.md) for the design decision and
[http-api.md](http-api.md) for the common authentication/envelope contract.
