# Architecture

Conversation web access is an independent Go-owned Run preference: disabled, exact-host rules, or full public HTTPS. Quiescent changes append a mode revision and fence old web calls; saved snapshots remain readable. See [ADR 0175](adr/0175-explicit-web-access-modes.md).

Universal Code is a run-centric, resumable Agent workbench. Go owns execution, policy and persistence; Desktop, Web and CLI use the same application services.

Task-title search applies in SQLite before the existing creation-keyset page.
Each list item carries a Go-owned live/durable execution observation distinct
from composer availability; uncertain ownership remains unknown. The sidebar
refreshes the list as a whole. See
[ADR 0167](adr/0167-task-title-search-and-execution-observation.md).

Application preview joins the existing Thread message and Command Runtime Job ledgers
through `thread_application_services.v1`. Its bounded read and exact Job cleanup adapters
retain source Run ownership and never launch a process or infer readiness from a URL.
Managed browser sessions keep their separate lifecycle and authority. See
[ADR 0166](adr/0166-thread-application-service-preview.md).

> **Current scope:** the active product is the general-purpose Agent Harness and Code workflow. CTF-specific solving and offensive automation are optional add-ons with no active implementation schedule. Only generic Provider, Tool, Skill, Analyzer, Sandbox, and Report extension seams remain in the core. User/control entries, backends, integrations, and extensions have separate support tiers; see [Product Scope](PRODUCT_SCOPE.md) and [ADR 0135](adr/0135-pre-1-0-product-convergence.md).

## Design Goals

- Go remains the sole control plane.
- One stable user task is a `Thread`; its `Mission` fixes intent and Scope; one execution attempt is a `Run` with one Run-local `Session`.
- Every state change is auditable and recoverable from SQLite.
- Agent concurrency is coordinated by one owner, not by agents calling each other directly.
- Privileged actions always cross policy, approval, scope, and sandbox boundaries.
- Active Desktop/React, CLI, and loopback HTTP/OpenAPI entries use the same Go-owned Application contract; maintenance TUI/headless adapters reuse it without creating a second product surface.
- Rust analyzers remain deterministic tools behind Go.
- CTF-specific behavior is outside the active core roadmap and may return only as a separately reviewed add-on profile.

## Architecture Cleanup Status

Implementation status for this revision:

| Area | Status | Current implementation |
| --- | --- | --- |
| Product entry and names | Implemented | One V2 React entry; `AgentRunner`, `SubagentRunner`, `RunWorktreeService` and `APIClient` name their responsibilities. |
| Retired code | Removed | Unreachable UI, unused Kernel/Planner/Executor/Critic scaffolding, unused tool Schema descriptions and retired execution constructors were deleted in [PR #245](https://github.com/CWNU-Open-Source-Community/Universal-Code/pull/245). Historical readers remain supported. |
| Built-in tools | Unified | [Tool registry](../internal/toolgateway/registry.go) supplies definitions, names, classes, normalization, model membership, executor availability and Gateway handlers. Runtime capabilities select the advertised subset. |
| Execution checks | Shared | [Dispatch guard](../internal/executionauth/dispatch_guard.go) handles first dispatch and later rechecks for GitHub review, advanced Git, thread Git and PR operations. Business authorization stays with each operation. |
| Command binding | Deduplicated | [Command authority](../internal/application/command_runtime_authority.go) validates one loaded binding snapshot. Each actual execution and later recheck reloads current state. |
| Migration fixtures | Simplified | [Historical fixtures](../internal/store/test_database_fixture_test.go) build real migration prefixes. [Seed import](../internal/store/historical_fixture_import_test.go) copies compatible test data; the cumulative reverse-DDL chain is gone. |
| CI execution | Deduplicated | [CI](../.github/workflows/ci.yml) runs analyzer vectors within the Go suite and desktop boundary tests through the platform build scripts. Platform, race and standalone release checks remain distinct. |
| Model and extension integrations | Existing boundaries | `llm` owns provider protocols; MCP shares its SDK execution path. Skills and Plugins use their supported formats. Compatibility is tracked per integration. |
| Command runtime construction and teardown | Shared for Desktop and HTTP | [Command runtime set](../internal/application/command_runtime_set.go) assembles Host, Local, Docker Engine and SBX adapters and shuts down their managers. Each entry selects its enabled backends; CLI retains its invocation-owned lifetime. Windows SBX uses a fixed namespace, a verified zero-tool helper and conditional UUID cleanup; see [ADR 0172](adr/0172-sbx-owned-namespace-execution.md). |
| Ecosystem product validation | Representative paths accepted | [2026-10-05 acceptance](acceptance/2026-10-05-core-integrations.md) covers real DeepSeek/Thread/Skill execution, cancellation and restart, Skill lifecycle and stdio MCP entry points. Ordinary native Skill ZIP upload remains a scoped product gap; third-party compatibility stays integration-specific. |

Unused mode placeholders and the forwarding Agent ID helper are removed.
Execution-control handlers share their HTTP authorization and method checks.
OpenAI transports share request headers; Responses and Anthropic share SSE
framing while retaining their protocol-specific message and completion handling.

The Gateway extension target is concrete: add a tool implementation and one
registration; Gateway discovery and dispatch derive from that entry. A new tool
family can provide its own normalizer and handler. Tools that introduce durable
protocol names, authority rules or runtime capabilities also update those
explicit contracts; historical migration SQL stays frozen. Common policy and
lifecycle behavior stay in the existing Gateway and execution services. New
integrations should establish a reusable boundary with an actual implementation
before introducing another Kernel or adapter layer.

## Control Plane

Current source names describe their responsibilities directly:

| Name | Responsibility | Former source name |
| --- | --- | --- |
| `application.AgentRunner` | Main Agent model/tool loop and Run lifecycle | `RunSupervisor` |
| `application.SubagentRunner` | Delegated child Agent execution | `SpecialistRunner` |
| `runworktree` / `application.RunWorktreeService` | Run-owned Git worktrees, checkpoints and recovery | `drydock` / `DrydockService` |
| `APIClient` | Frontend HTTP client | `CyberAgentClient` |

`Thread` is the continuing conversation, `Run` is its execution lifecycle, and
`Session` stores model messages. `runworktree` owns a Run's workspace lifecycle;
the lower-level `gitadvanced.ManagedWorktree` describes the actual Git worktree.
Existing protocol IDs, serialized fields, database names and user-data locations
retain their established identifiers. Historical ADRs use the names recorded at
the time. Product display strings use Universal Code.

```text
CLI / React Web + Desktop / loopback API / maintenance TUI + headless
                              |
                    Go Application Services
                              |
        +---------------------+---------------------+
        |                     |                     |
  Mission Service       Approval Service       Report Service
        |                     |                     |
        +--------------- Agent Runner --------------+
                              |
                     Agent Coordinator
                              |
          +-------------------+-------------------+
          |                   |                   |
      Root Agent        Specialist Agent     Specialist Agent
          |                   |                   |
          +------------ Tool Gateway -------------+
                              |
              Policy -> Scope -> Approval -> Runtime
                              |
        +---------------------+---------------------+
        |                     |                     |
  Workspace/File       Sandbox Backend       Rust Analyzer Bridge
        |                     |                     |
        +---------------------+---------------------+
                              |
                  SQLite Event and State Store
```

Desktop D0-A/D0-B is a presentation adapter, not another control plane. Wails v2.13.0 embeds the production React bundle and routes requests to the existing Go HTTP Handler in process without a listening socket. Its native binding surface uses a memory-only bootstrap and pathless, Go-owned selectors. The direct default exposes bounded controls and, only after a current platform proof, the sandboxed command runtime; it grants no unsandboxed host process, arbitrary Shell, Docker, Scope, Policy, or renderer path-input authority. Schema v99 adds an explicit startup-only `--enable-docker-execution` option, which requires the independent permission capability and composes the same `DockerSandboxService` used by CLI/HTTP; the renderer still cannot manufacture the capability or submit Docker options. Ordinary Web retains SSE while Windows Desktop uses `run-event-poll.v1` with the same durable Run-bound high-water cursor and real event frames. Go owns same-database reopen, serialized window lifecycle, WebView2 prerequisite failure, and exact renderer-origin handling; renderer memory and navigation guards add no authority. See ADR 0034, ADR 0035, and ADR 0099.

Allowed external directions remain:

```text
TypeScript -> Go -> Rust
TypeScript -> Go -> Docker
TypeScript -> Go -> LLM
```

TypeScript and Rust never bypass Go for secrets, persistence, policy, workspace permissions, shell execution, network scope, or Docker. See [ADR 0001](adr/0001-go-control-plane.md).

`run_capability_readiness.v1` is the Go-owned presentation boundary for persisted
Permission/Profile/Interaction/CDP intent, Run and lease state, process startup
gates, and backend proof. CLI calls the Application service directly; browser and
Desktop React consume its authenticated HTTP projection. TypeScript accepts the
exact version and canonical disposition vocabulary but does not recompute
authorization. `selected`, `selectable`, and `runtime_available` remain independent,
and the envelope fixes `capability_grant=false`; it is never consumed as execution
authority. See [ADR 0128](adr/0128-go-owned-run-capability-readiness.md).

The zero-argument Desktop launch installs the safe control-plane bundle, probes
the platform Workspace Sandbox adapter, and installs it only after a current
readiness proof; `--safe-view` is the explicit mutually exclusive read-only
entry. When that proof exposes Standard Code, a zero-Run database opens a
renderer-only first-run guide that gathers Provider availability, Harness
qualification, a pathless native Workspace selection, exact Trust confirmation,
and Go-owned backend readiness before submitting `standard_code_preset.v1`. Reopening an older
database does not trust a Workspace, rewrite Run permission intent, start a
durable worker, or reinterpret persisted approval, lease, or process records as
authority. High-risk host, Debug, Full CDP, Cyber, and custom startup gates stay
explicit and outside the default bundle. See [ADR 0137](adr/0137-direct-safe-first-run-and-explainable-readiness.md).

`code-intel-lsp.v1` adds one more Go-owned process boundary without changing that direction. An explicitly reviewed local language server communicates only through bounded stdio JSON-RPC. Go owns qualification, executable hashing, minimal environment, initialize/document synchronization, timeout/cancellation, restart, and process-tree cleanup; Tool Gateway reuses the exact `agent-code-tools.v1` Workspace-read authority. Server output is untrusted evidence and every returned URI is resolved again below the Workspace root. Capability inventory is process-local and metadata-only, so this feature adds no SQLite migration and no renderer-owned authority. The configured Server remains a real local process rather than an OS sandbox; see [Code Intelligence](code-intelligence.md).

Extension onboarding adds control-authenticated adapters to those existing
services: inert manual MCP registration, content-pinned Plugin ZIP import and
staged LSP configuration with a separate fingerprint-bound review. Web/Desktop
read the same process-local onboarding capabilities and safe source/scope/review
metadata. LSP keeps one stable manager shared by HTTP and model tools and persists
the existing operator configuration format; explicit testing returns a real
bounded semantic query result. MCP invocation remains in the ordinary task and
Approval path, with durable call metadata read back by settings. See
[ADR 0168](adr/0168-extension-first-onboarding.md).

The production React bundle is built by Vite but hosted only when Go receives an explicit `--ui-dir`. Go validates and snapshots the static tree before serving it from the same loopback origin as `api.v1`; `/api` remains a reserved authenticated namespace. Vite's loopback proxy is a development adapter, not a second control plane.

## Core Domain

`Run` is a domain aggregate, not a programming language, operating-system process, or replacement for Go/TypeScript/Rust. Go creates and owns Run lifecycle state; TypeScript may display and control it through Go APIs. Rust never owns a Run. Go application services bind the fixed embedded digest analyzer to a Run and commit its execution evidence. The Rust wire contract remains free of Run identity and authority. See the [Analyzer README](../analyzers/README.md) for the current execution boundary.

Budget, events, sandbox sessions, and reports are Run-scoped rather than embedded into one large object. Their modules remain independent and persist references by `run_id`; `AgentRunner` coordinates their lifecycle.

| Aggregate | Purpose | Durable owner |
|---|---|---|
| `Mission` | Stable user goal, workspace, profile, and authorization scope | Mission Store |
| `Run` | One resumable execution attempt with config snapshot, budget, and status | Run Store |
| `AgentNode` | Addressable root or specialist worker inside a run | Coordinator Store |
| `BatchDelivery` | Admitted child DAG, isolated worktrees, mailbox, receipts, reviews, and local integration queue | Batch Delivery Store |
| `GitAdvancedOperation` | Immutable exact preview/Approval/Checkpoint/receipt for one closed advanced Git mutation | Git Advanced Store |
| `GitAdvancedSequence` / `ManagedWorktree` | Durable conflict sequence generations and product-owned worktree identities | Git Advanced Store |
| `GitHubReviewSnapshot` / `EvidenceGraph` / `WriteRecord` | Sanitized remote PR/CI evidence, exact local mappings, Approval-bound remote receipts | GitHub Review Store |
| `WorkItem` | Structured unit of planned work with dependency and completion state | Work Board Store |
| `Note` | Durable observation or reasoning aid, scoped to run and optional agent | Memory Store |
| `Finding` | Structured, evidence-backed result awaiting validation/acceptance | Finding Store |
| `Evidence` | Immutable reference to logs, commands, files, diffs, or test output | Artifact Store |
| `Approval` | Decision record for a privileged proposed action | Approval Store |
| `Event` | Append-only lifecycle and activity record | Event Store |

Existing `agent.Task` and `session.Session` are not discarded. During migration, `Task` becomes a compatibility view of `Mission`/`WorkItem`, while `Session` remains conversation history attached to a `Run` and optionally an `AgentNode`.

## State Machines

Run lifecycle:

```text
created -> preparing -> running -> completed
                       |   |  |
                       |   |  +-> failed
                       |   +----> waiting_approval -> running
                       +--------> paused -> running
                                   |
                                   +-> cancelled
```

Agent lifecycle:

```text
pending -> running -> waiting -> running
             |          |
             |          +-> cancelled
             +-> completed / failed / crashed / cancelled
```

Work item lifecycle:

```text
pending -> ready -> in_progress -> done
                         |
                         +-> blocked / failed / cancelled
```

Finding lifecycle:

```text
draft -> validating -> validated -> accepted -> fixed
             |             |
             +-> rejected  +-> duplicate
```

Transitions are checked in Go domain services and written with an event. UI code and model output cannot directly mutate status fields.

## Agent Runner

`AgentRunner` is the application-level owner of a run. It is responsible for:

1. loading the mission and immutable run configuration snapshot;
2. validating authorization scope and budget;
3. provisioning or reconnecting the sandbox;
4. restoring the coordinator, agent sessions, work board, notes, and pending approvals;
5. starting the root agent or resuming parked agents;
6. forwarding normalized events to CLI/TUI/WebSocket subscribers;
7. stopping all workers on cancellation, budget exhaustion, or fatal runtime errors;
8. finalizing findings, artifacts, reports, and cleanup state.

The supervisor owns process handles and channels only in memory. Durable IDs, statuses, inbox messages, checkpoints, and events live in SQLite so a process restart can reconstruct the run.

Schema v17 adds one durable execution lease row per Run. A Supervisor must acquire the lease before entering a turn or operator finalization; `Step` holds it for one turn and `Execute` holds it across the complete bounded loop. The default 30-second lease is renewed every 10 seconds while a Provider call or Store operation is in flight. An expired or released lease may be replaced only by a higher generation. Same-owner acquisition is not implicitly reentrant: only a retry that presents the current `lease_id` can replay the acquisition, preventing concurrent calls from one worker identity from sharing a lease.

The pair `(lease_id, generation)` is a fencing token. It is copied into the durable Supervisor checkpoint and verified inside every model/tool/checkpoint mutation transaction. AgentRunner structured-memory calls also carry it through Tool Gateway; the budget charge checks it before incrementing, and the entity transaction checks it again before replay or creation. A takeover can rebind an unfinished checkpoint to the new generation while preserving its attempt and pending input, but the old worker can no longer append events, consume budget, or write entities. Heartbeat loss cancels the Go operation context, and release uses a short context independent of caller cancellation. Lease lifecycle events expose owner/generation/timestamps only; the token is absent from events, CLI output, HTTP DTOs, and Gateway outcomes.

The current single-Agent slice persists cumulative input/output/total tokens, model-call execution milliseconds, and the redacted pending user input in the Supervisor checkpoint. A bounded executor performs only an operator-selected number of durable steps. Root model output uses strict `root_lifecycle.v1` JSON: `continue` returns to idle, `finish` completes the Run, and `wait` pauses it for explicit resume. Turn data, status, checkpoint, Session messages, and events commit in one transaction; arbitrary assistant text cannot mutate lifecycle state.

Provider calls use typed outcomes: `retryable`, `rate_limited`, `invalid_response`, `cancelled`, or `permanent`. AgentRunner retries only the first two, at most three transport attempts per protocol phase by default, with cancellation-aware exponential backoff. Server `Retry-After` is honored only when it is within the local maximum wait; a longer delay returns a stable rate-limit result instead of retrying early. Each call receives a durable global sequence number plus a phase-local transport number and emits `model.started` plus exactly one terminal model event. Terminal event persistence, token usage, and model execution time share one transaction, so restart recovery neither loses nor double-charges completed calls.

Every Supervisor model attempt uses `StreamChat`. The stream aggregator reconstructs UTF-8 across transport chunk boundaries, caps model output at 64 KiB, requires one final completion chunk with valid usage, rejects ToolCalls on non-final chunks, and forwards normalized final ToolCalls to the bounded structured-memory loop before lifecycle parsing. Mid-stream transport failures use the same typed retry policy, lifecycle-protocol repair, budget accounting, and terminal transactions as a non-stream response.

Incremental persistence is deliberately metadata-only. One attempt may append at most 32 ordered `model.delta` events carrying sequence, chunk count, byte count, cumulative bytes, and completion state. The Store validates idempotent replay, strict ordering, size limits, and exact agreement between the durable delta ledger and the terminal model event. Model text remains in bounded process memory until the validated turn transaction writes the final redacted assistant message.

The Go application layer owns an in-process `ActiveCallRegistry`. A call is reserved before `model.started` to reject concurrent Provider calls for the same Run, but it becomes queryable and cancellable only after that durable start succeeds. Registry entries are keyed by Run plus Supervisor/model-attempt identity, own the Provider cancellation function, and are removed on every Provider terminal path. Explicit cancellation writes one idempotent, redacted `model.cancel_requested` event before signalling the context.

The active-call registry still owns the actual Provider cancellation function inside its worker process. Schema v18 bridges processes without exposing that registry: a separately authorized HTTP control request persists an exact Run/Supervisor/model-attempt intent, and the worker polls it using its own schema v17-fenced checkpoint. Observation commits `model.cancel_observed` before the registry signals the Provider context. The client never receives or supplies the lease id; later model attempts atomically mark orphaned older requests `superseded` instead of inheriting them.

Schema v29 applies the same audit-first principle to concurrent Specialist calls without reusing the Run-keyed root registry. A separate cancellation row is bound to `Run + Specialist Agent + AgentAttempt + model attempt`; the child worker polls under the Attempt's private execution lease and owns that call's `context.CancelFunc`. Observation commits before signalling, model terminal state resolves the request atomically, and Attempt crash/interruption/takeover resolves leftovers as `attempt_terminated` or `worker_lost`. Root and child ledgers remain separate so two concurrent children cannot alias one Run-level registry key. API responses and events contain neither lease identity nor model text.

Schema v30 adds a proposal-only bridge from root reasoning to the Go-owned Agent graph. `specialist_delegation_propose` accepts strict `specialist_delegation.v1` JSON with one or two assignments. The Tool Gateway canonicalizes and redacts the payload before deriving its semantic operation key; SQLite then verifies the exact active root, Supervisor checkpoint, Run execution lease, charged `agent_proposal` invocation, trusted scope, parent-Skill subsets, child capacity, and root budget headroom. Proposal, assignments, Policy decision, metadata-only `agent.delegation_proposed`, `tool.completed`, and digest-only operation fact commit together. The row is immutable and remains `proposed`; it is deliberately not a capacity reservation or authorization fact. No model response can move it into admission, create an Agent/Session, or select/start the scheduler.

Schema v31 appends one independent operator review fact without mutating that proposal. `specialist_delegation_reviews` stores a redacted `approved` or `rejected` decision, while a separate digest-only operation table provides exact replay and changed-intent conflict handling. The SQLite writer transaction rebinds the review to the immutable proposal, Run, and root; approval requires a running Run, rejection requires a reason and remains available after termination. Review and operation rows reject update and delete. The strict metadata event excludes the reason and declares `admission_authorized=false` plus `application_required=true`. There is no Provider tool, HTTP endpoint, Agent capability, admission call, instruction send, or scheduler call for review.

Schema v32 adds a recoverable operator application state machine. `specialist_delegation_applications` freezes the approved review, application policy, and one applying/applied/aborted lifecycle; ordered assignment rows move only `pending -> admitted -> instructed`. Before those transitions, each row stores the exact admission and instruction operation digests derived from its application ID and ordinal. The Coordinator remains authoritative for Agent/Session creation and message delivery, while application transitions verify the corresponding Coordinator operation ledger, Agent budget/Skill/session projection, and strict instruction payload. A crash between either Coordinator commit and application-state commit therefore replays the same Agent or Message. One applying application reserves the Run against root turns, unrelated admission/messages, Specialist schedules, and direct Attempts. Terminal Run projection aborts it and records counts; normal completion leaves every child ready and starts no schedule.

Schema v33 deliberately does not widen that graph. `readonly_fanout_plans`, ordered file manifests, deterministic shards, and digest-only operations form a separate planning aggregate with a fixed `readonly_fanout.v1` capability fingerprint. The only true capabilities are workspace list/read; Shell, write, process, network, external tools, and recursive child spawn are all false in both Go validation and the SQLite CHECK. Requested tiers are `auto/1/2/4/6`, while effective shard count is bounded by eligible files. Snapshot hashing uses stable relative paths, sizes, and content hashes; scanner scope is canonicalized below the trusted workspace root, symlinks are skipped, and bounded exclusions are explicit. Planning requires a running network-disabled Run plus its active workspace Session. Rows and metadata events are immutable, but file bytes are not copied, so the future executor must reread and verify every stored hash before any Provider call. v33 has no execution transition and no AgentNode, model, tool, scheduler, HTTP, or write entry point.

Schema v34 implements that reread gate without changing the Agent graph. `ReadOnlyFanoutExecutionService` acquires the existing Run execution lease, rebuilds the full plan, then opens each admitted path through `os.Root` and verifies regular-file identity, byte count, and content digest again. Only a redacted in-memory copy reaches a Provider. Every request has no tools, requires JSON mode, and must decode as strict `readonly_fanout_report.v1`; a finding path must belong to the exact shard. Go starts at most the immutable plan parallelism, shares cancellation across the batch, and waits for every shard to become durable before finalizing. SQLite keeps separate execution, shard, model-call, finding, and digest-only operation ledgers. The private lease generation fences stale commits. Takeover marks uncertain calls `abandoned`, conservatively retains their reserved token/time charge, resets only their running shards, and never replays completed reports. `RunAgentUsage` reconciles root, Specialist, and Fan-out calls, while the existing core scheduler remains capped at two children. Fan-out still creates no AgentNode, AgentAttempt, Session, schedule, tool call, file edit, process, or network operation.

Schema v35 adds the first generic Finding/Evidence/Report projection without another model boundary. A completed read-only Fan-out execution is the immutable source. Go fingerprints severity, category, title, detail, path, and line range exactly; only equal facts deduplicate, severity disagreement remains separate, and duplicate confidence becomes the conservative minimum. Each source assertion remains an immutable `model_assertion` Evidence record bound to its v34 fingerprint and shard report digest. `finding_reports` is inserted as `building`, then can transition once to `generated` only when SQLite verifies source bindings, grouped/source counts, severity totals, and contiguous Finding/Evidence ordinals. Generated rows cannot be updated or deleted. Renderers rebuild byte-stable Markdown/JSON from SQLite. Every Finding starts `draft`: this projection records a model claim and provenance, not validation or acceptance.

Schema v36 adds validation as an additive overlay instead of mutating that source projection. `finding_artifact_evidence` snapshots the identity, SHA-256, size, MIME, stream, tool, source, and redaction flag of one same-Run `run_artifacts` row after Go rereads and validates the full blob. Update/delete triggers freeze all Run Artifacts and every new evidence, decision, and operation row. A Finding may receive one `draft -> validated|rejected` decision; validation requires at least one Artifact Evidence record, while rejection may have none. The decision stores the exact ordered Evidence count and digest. Evidence cannot be appended after a decision. Separate digest-only operation ledgers make both mutations replay-safe across processes, while `finding.evidence_attached` and `finding.validation_decided` omit notes, reasons, and Artifact content. The v35 source projection digest deliberately excludes this overlay.

Schema v37 adds acceptance and remediation as further additive overlays. Acceptance is a separate immutable operator fact, not a side effect of validation, and snapshots the exact validated decision plus its Evidence count and digest. Remediation Evidence must reference a different same-Run Artifact whose durable `artifact.created` sequence is strictly later than the acceptance event; wall-clock ordering alone is not trusted. A fix requires at least one such record and freezes the ordered remediation count and domain-separated digest. Writer-lock serialization and digest-only operation ledgers make all three mutations converge across processes. SQLite binds scope, source snapshots, event order, ordinals, timestamps, and immutable rows; domain validation reconstructs the complete chain on every read. The source Finding remains `draft`, and the v35 projection digest excludes acceptance, remediation, and fix overlays.

Live call subscribers receive a versioned metadata-only envelope for snapshot, progress, cancellation request, completion, and failure. Each subscriber has a 32-event buffer; a slow subscriber is closed instead of blocking the Provider. This transient stream has no replay guarantee and intentionally has no model-text field. Future user-facing text streaming needs a separate Go-owned redaction and lifecycle-projection boundary.

If a response fails strict `root_lifecycle.v1` parsing, the Supervisor persists a redacted diagnostic and requests exactly one protocol repair without replaying the raw output. Repair transport retries use their own bounded counter. Pending repair resumes after restart, exhausted repair never calls the Provider again, and request/start/completion/failure transitions are append-only Run events. Only a validated repaired response can reach Session history.

Ordinary text sent to a Run-bound Session uses this same Supervisor path. A `created` Run starts automatically, a paused Run resumes for follow-up input, and terminal or approval-waiting Runs reject new model turns. The input is checkpointed before the Provider call and is recovered after process restart without duplicating the committed user/assistant pair. Sessions without a Run retain an explicit legacy Router path during migration; slash commands remain command adapters rather than implicit Agent turns.

## Blocking And Progress Guards

Enabled in-process Tool calls have a default 15-second hard deadline and a five-minute
configuration ceiling. Caller cancellation, timeout, and panic return bounded results.
Built-in workspace reads check context while walking and reading, enforce configured
and platform byte limits, and open only regular files; FIFO, device, and socket inputs
fail instead of blocking. A timeout releases the caller but cannot forcibly kill
third-party Go code that ignores context, so future plugin isolation should prefer a
cancellable process boundary.

The process-wide `waitgraph` records bounded reference-counted synchronous edges for
Agent, Tool, Retriever, Store, Runner, Model, and external nodes. It rejects self,
direct, and indirect cycles before insertion and permanently forbids Tool/Retriever/
Store/Runner callbacks that wait synchronously on an Agent. Root Supervisor context,
Specialist parent-to-child execution, and Tool Gateway invocation use this graph now;
future RAG, Store callback, Model adapter, and Runner boundaries must enter it too.

Schema v79 adds one metadata-only `run_progress_guard.v1` per observed Run. Three
identical `continue` actions or six turns without selected structured-state progress
atomically convert the completed action to a recoverable wait and move the Run to
`paused`. Session messages, checkpoint, events, and status commit together; completion
replay remains exactly once. Only a later durable operator `paused -> running`
transition resets a detected guard. The migration fabricates no progress record and
the guard stores no model text. Real Local/Docker processes remain disabled, so
process-tree start/wait/TERM/KILL/orphan handling remains a separate future gate.

## Agent Coordinator

One `AgentCoordinator` owns the graph for a run:

- root/parent relationships;
- agent identity, role, profile, and assigned skills;
- status transitions and cancellation;
- per-agent inboxes and pending message counts;
- child creation limits and concurrency limits;
- budget and turn allocation;
- completion reports from child to parent;
- snapshot/recovery metadata.

Agents communicate through coordinator messages. A child never invokes a parent callback and siblings do not share mutable memory. Multi-agent execution is opt-in; the first implementation runs one root agent through the same coordinator API.

Schema v19 implements that first single-root slice. Every new Run receives one stable root `AgentNode`; existing databases register it lazily on the next Coordinator/Supervisor operation. `BeginSupervisorTurn`, lifecycle completion/failure/finalization, ordinary Run transitions, inbox changes, and graph recovery snapshots share their existing SQLite business transaction. Root `continue`, `wait`, `finish`, operator failure, and Run cancellation therefore cannot leave the Run and Agent projection in different committed states. `run graph` validates the current node/inbox projection against its latest `agent_graph.v1` snapshot.

The current hard limits are one root plus at most two active depth-one children (with up to 256 historical children retained), at most two assignments per core delegation proposal, one immutable review and application per proposal, eight turns and 16,384 tokens per application-created child, 16 assigned execution capabilities, at most one parent-selected built-in Skill guide per Specialist Attempt, 128 pending and 4,096 historical messages per inbox, 32 messages per manual consume batch, four root-context messages per Supervisor turn, four parent instructions per Specialist attempt, one child protocol repair per Attempt, 32 retained snapshots, and 32 internal scheduling rounds. The separate read-only Fan-out accepts execution caps of 1/2/4/6 without creating Agent nodes and permits at most three crash-recovery attempts per shard. Inbox payloads must be JSON objects with bounded ASCII field names; secret-shaped keys are rejected while string values pass through recursive redaction. Snapshot metadata includes a hash of each pending payload rather than a second payload copy. Schema v20 makes send intent idempotent through digest-only operation facts and gives `wake`/`dependency` strict semantics. Schema v21 keeps child admission absent from the default Coordinator and enables it only through an explicit Go-internal policy with parent-capability subsets, dedicated Sessions, positive budget reservations, and root headroom. Schema v25 admits only protocol-backed direct-child messages into root context. Schema v26 adds one explicitly invoked internal child turn, schema v27 gives that turn recoverable direct-parent instructions plus bounded child-owned memory, schema v28 adds one isolated child lifecycle repair, schema v29 persists schedule boundaries plus exact child cancellation, schema v30 persists review-required root delegation suggestions, schema v31 records a separate non-authorizing operator decision, schema v32 applies it through existing admission/instruction paths without starting execution, schema v33 freezes a read-only Fan-out manifest, schema v34 executes it through a lease-fenced no-tool gate, and schema v47 gives each child Attempt a separately budgeted minimal Skill subset derived from the parent's pinned selection and immutable Run mode. The Specialist scheduler can run two explicit ready children concurrently. Schemas v202/v203 add admitted execution workers and saved bounded workflows through the existing control plane; see [execution workers](execution-workers.md) for the current tools, approval recovery and UI/API entry points.

The first v35 report projection accepts at most 192 source Evidence rows and 192 draft Findings, matching six shards times the v34 per-shard limit of 32. A report with no model findings is valid and still carries a stable projection digest. Schema v36 permits at most 64 validation Artifact Evidence records per Finding and exactly one validation decision. Schema v37 permits at most 64 separate remediation Artifact Evidence records, one acceptance decision, and one fix decision per Finding.

Schema v22 establishes Agent-owned Run memory without granting the model an identity selector. WorkItems and Notes retain the legacy bounded `owner` label and add nullable `owner_agent_id` references. Application and Store validation require normalized identity; transactional Store checks reject missing, terminal, or cross-Run owners; SQLite foreign keys and same-Run insert/update triggers defend direct writes. A Note may retain Agent ownership under `run` or `root` visibility, while `owner` visibility is evaluated against the viewer Agent. Agent-only private Notes mirror the Agent ID into the legacy label so v10's established CHECK constraint and old readers remain valid. Supervisor and CLI structured-memory calls inject the root identity from Go-owned Run state, and the model-facing tool schema has no `owner_agent_id` property.

Schema v23 establishes the child completion boundary without starting a child model loop. `agent_completion.v1` permits only `succeeded` or `partial`, an 8 KiB/4,096-rune redacted summary, up to 16 child-owned WorkItem references, and up to 16 active parent-visible Note references. A successful report is rejected while the child owns active WorkItems; a partial report must reference every such item. The Store binds completion to the exact running Specialist attempt and direct root parent, then atomically inserts the immutable report and digest-only operation fact, writes one parent result inbox message, completes the child, archives its Session, appends metadata-only events, and snapshots the graph. Same-key concurrent retries return the original report, changed intent conflicts, stale attempts fail, and graph recovery rejects a completed Specialist whose report has been removed.

Schema v24 establishes the internal child scheduling boundary without exposing spawn or starting a model loop. `agent_attempt.v1` records one immutable turn attempt with its Run lease id/generation, turn number, optional exact-once model usage, terminal status, bounded redacted failure, and parent notification identity. Scheduling charges the child turn before external work; usage atomically updates the child token counter. Continuation returns the child to ready only when another turn and token headroom remain. Completion requires the current lease and recorded usage, then terminalizes the Attempt before the child in the same transaction. A crash sends one bounded notification and either schedules retry or fails and archives the child according to persisted budgets. After lease takeover, recovery crashes stale attempts once; all former-worker usage, completion, and crash commits fail the lease fence. Run pause/wait/terminal projection first interrupts a running Attempt, and restart validation recomputes contiguous turns and token totals before accepting the graph snapshot. The runtime capability is separate from admission and disabled by default.

Schema v25 establishes the root inbox-to-context boundary. A writer transaction prepares up to four sequence-ordered messages from direct Specialist children and records immutable attempt/turn/ordinal identity in `root_inbox_deliveries`. Dependency messages must pass their strict protocol; result and failure messages must match an immutable CompletionReport or crashed AgentAttempt. A successful root lifecycle transaction first commits each delivery and then consumes its message before Session/checkpoint commit. Failure or a Run transition away from running supersedes prepared rows without consuming the messages. Cancellation and lease takeover keep the started Supervisor attempt recoverable, so the same batch is replayed rather than rebound. Context construction exposes bounded typed task state and durable sender provenance but excludes message IDs, sequence values, cursors, and consumption controls. Prepared metadata participates in graph snapshots and restore validation.

The following schema notes describe the original analysis profile. Current native read, Batch and execution-worker profiles are described above.

Schema v26 establishes the first child Provider boundary. `SubagentRunner` is constructed only by internal Go code and executes one no-tool turn under the same Run execution-lease heartbeat and generation fence as the root. `specialist_lifecycle.v1` accepts only `continue` or `finish` with `agent_completion.v1`; usage, retry, identity, Policy, lease, and lifecycle commits are never model fields. `specialist_model_calls` records each started/completed/failed transport attempt. A successful or invalid usage-bearing response atomically updates the model row, child Attempt usage and token counter, Policy audit, graph snapshot, and, only when allowed, a redacted child Session message pair. Transport failures may retry without charging tokens. Context cancellation records failure and crashes the Attempt before releasing the lease; a hard-lost worker is recovered by the next generation. Child history is queried as the latest 12 messages and capped again at 64 KiB before Provider dispatch. It still provides no tool specifications, public admission/spawn, or autonomous scheduling; schema v29 later adds only exact-call cancellation control.

Schema v27 establishes the parent-to-child context boundary. Only a strict `specialist_instruction.v1` message routed from the direct root parent to the child can enter `specialist_context_deliveries`; SQLite also verifies the active AgentAttempt, Run lease generation, payload shape, and pending status. Up to four sequence-ordered messages are prepared. `continue` and `finish` commit deliveries and consume messages in their existing lifecycle transaction, while crash, interruption, and lease takeover supersede deliveries after terminalizing the Attempt so the messages remain pending for a fresh attempt. Prepared metadata participates in graph snapshots and restore validation. The child context builder adds active WorkItems owned by the child and active `run`/`owner` Notes owned by and visible to that child under a 4,096-token estimate and 32 KiB input cap. Mandatory mission and parent instructions must fit; lower-priority memory is deterministically omitted. Message IDs stay out of the model input, while content-free source IDs and token estimates enter `model.started` provenance.

Schema v175 adds the persistent Specialist task brief ([ADR 0163](adr/0163-persistent-specialist-task-brief.md)). Consumed direct-parent instructions remain current task constraints until a strict `specialist_instruction.v2` replacement or withdrawal retires an exact earlier source/hash. The v1 additive writer and delegation-application binding remain readable and unchanged. One immutable `specialist_task_brief.v1` snapshot pins complete effective instructions, unfinished child-owned WorkItems and their source/version/hash manifest to the current attempt, including empty preparations. A fresh attempt refreshes current task truth under its own lease; re-preparing the same attempt recovers its snapshot. Required task text precedes optional Notes and survives recent-history and final-window fitting, or dispatch stops explicitly. Old child-context user envelopes do not replay retired task text. Source fingerprints enter attempt-bound `model.started` receipts; successful input must match the prepared task content. Existing exactly-once inbox consumption, authority and no-tool constraints remain intact. No automatic summarization or new tool loop is introduced.

Schema v28 establishes the child lifecycle-repair boundary. `specialist_model_calls` now carries a durable global model sequence, a phase-local transport sequence, and a bounded `protocol_repair` phase. A usage-bearing invalid primary response atomically records its generic diagnostic, cumulative Attempt/Agent usage, one pending `specialist_protocol_repairs` row, and a metadata-only repair-request event. The one repair request reuses trusted context but never includes the invalid response; its transport retry counter restarts at one. Success resolves the repair and may enter the child Session, a second invalid response exhausts it, and cancellation, budget exhaustion, interruption, crash, or stale-worker takeover aborts a pending repair before the Attempt becomes terminal. SQLite triggers reject skipped phase calls, uncharged repair requests, invalid resolution, terminal mutation, and `continue`/`finish` while repair is unresolved. The Runner rechecks Run-wide total-token and execution-time remainder before dispatch and gives repair only the post-primary remainder.

The internal `SpecialistScheduler` lifts lease ownership above an individual child turn. One schedule holds one Run execution lease and starts at most two ready direct children per round. A shared cancellable context fans parent cancellation, heartbeat loss, or the first child failure out to every active sibling; the scheduler waits for each child to persist its Attempt terminal state before returning and releasing the lease. It stops on all-terminal, no-ready, round-limit, cancellation, child-error, aggregate-token, or aggregate-execution conditions. Aggregate usage is rebuilt transactionally from the root Supervisor checkpoint, Specialist Agent/Attempt token projections, and every Specialist model-call duration before and after each round. Remaining total-token and execution allowance is split deterministically by sorted Agent ID.

Schema v29 wraps that invocation in `specialist_schedules` and ordered `specialist_schedule_agents`. Start validates the exact active Run lease and direct-child targets. Stop records a terminal status, bounded stop reason, rounds, started turns, recovered Attempts, and monotonic before/after `RunAgentUsage`; terminal rows are immutable and events omit the stored lease identity. If a process disappears, the next lease generation marks its running schedule `abandoned/worker_lost` before starting a replacement. At this boundary the scheduler had no public CLI/HTTP/model-spawn path and granted no tools. Schema v38 later adds only an operator CLI request gate for applied/instructed children; the HTTP POST still controls only an already-started exact child model call and cannot create or select a child.

Schema v30 does not call that scheduler. It stores `specialist_delegation_proposals`, ordered assignments, and a one-to-one operation ledger only after every assignment is a normalized parent-Skill subset and the suggested aggregate leaves capacity for the active root turn plus one future root turn. The delegation capability itself is non-delegable. Replays under a fresh Provider call ID converge through the redacted semantic fingerprint, including across independent SQLite connections. The operation ledger stores digests and non-secret scope only, not `lease_id` or fencing generation; CLI commands expose redacted proposal state, while events also omit titles/goals, lease identity, and operation keys. Schema v31 may append one immutable review. Schema v32 then revalidates current Policy, review-operation backing, capacity, Skills, Sessions, idle execution, and budgets before an approved proposal can reach existing admission and instruction paths; it never calls the scheduler.

## Structured Dependency Waiting

Schema v101 gives "task A waits for task B" a durable, Go-adjudicated contract
(`agent_dependency.v1`). An edge carries source/target identity (Agent-only in
this slice), a bounded reason, a no-progress deadline, a generation, and a
closed failure policy (`fail|notify`); its state is the five-value set
`wait|satisfied|failed|cancelled|expired`. Before any insert, the store
reloads the Run's open edges into a `waitgraph.DAG` and rejects self-loops,
ancestor and multi-node cycles, reverse runtime→Agent waits, cross-Run
endpoints, and chains deeper than 64 inside the write transaction.

Settlement, parent-cancel fan-down, deadline expiry, and crash recovery all
share one settle transaction: a unique wake receipt (`agent_dependency_wakes`,
one per edge) is inserted first, so replays and concurrent settles can never
wake a source twice; the edge then CAS-transitions and the source receives a
dependency notification (and a wake control, or a `dependency_failed` node
transition under the fail policy). `ReconcileDependencyEdges` settles open
edges whose target, deadline, or Run already reached a terminal state, and the
RunService terminal hook fans a run-level cancellation down automatically.
No-progress deadlines emit `dependency.deadlock_detected`; a source that
wakes and re-waits beyond the polling bound is rejected with a stable
`LIVELOCK` error and `dependency.livelock_detected`. Models can only propose
intent: the Go control plane validates graph, budget, scope, and ownership.
Run Activity projects the seven dependency events as a collapsed `dependency`
kind showing source→target and release reason only, never raw child output.
Model-driven child scheduling (Issue #51) consumes this contract.

## Work Board and Notes

Conversation history is not enough for long-running work. Each run therefore has two structured memory surfaces:

- `WorkItem`: actionable work, dependencies, owner, priority, status, and acceptance criteria.
- `Note`: observations, hypotheses, decisions, summaries, and references to evidence.

Work items and notes are stored independently from LLM messages. Context construction selects only relevant summaries, active work, recent messages, and explicitly loaded notes.

The current P3/P4 implementation persists both surfaces. Schema v9 WorkItems use optimistic versions, composite same-Run dependency keys, cycle checks, legal transitions, and transactional `work_item.created/changed` events. Schema v10 Notes add category, visibility, Owner, tags, source references, Evidence IDs, pinning, archive/restore, and transactional `note.created/changed` events. Schema v22 adds authoritative same-Run Agent ownership and Agent-aware Note visibility while preserving label-only rows. Root context includes `run`, `root`, and Notes owned by the root Agent, but excludes owner-only Specialist memory.

Before each root model call, a generic Context Section selector ranks a prepared root inbox batch, the latest compacted summary, bounded active Work Board, pinned Notes, and category-weighted Notes under an 8,192-token estimate. Every prepared inbox message must fit or the turn fails without consuming it. Specialist calls use the same selector under a separate 4,096-token/32 KiB bound for mandatory parent instructions and child-owned active memory. `model.started` records included and omitted `kind/source_id/tokens` metadata so provenance survives restart, while Note and inbox bodies remain outside the event. Model-driven root `finish` is rejected through protocol repair while active work remains and checked again under the final SQLite write transaction. Schema v16 lets AgentRunner dispatch only the schema v15 create-only WorkItem/Note tools through the same Gateway; all other Provider tools remain denied.

## Lifecycle Protocol

Autonomous/headless execution cannot finish with an arbitrary assistant paragraph. The root protocol now validates one versioned JSON lifecycle result:

- root: `continue`, `finish`, or `wait`; `finish` includes a summary and `wait` includes a reason;
- child: `specialist_lifecycle.v1` with `continue` or `finish`; `finish` carries a structured `agent_completion.v1` report for its parent;
- blocked agent: `agent.wait` with reason and awaited dependency;
- cancellation: coordinator-owned transition, never model-owned.

The current root implementation maps `wait` to a durable paused Run and resumes at the next turn, and it permits one bounded automatic repair for an invalid root response. The child path now executes no-tool Provider turns with strict lifecycle decoding, one isolated repair, lease fencing, cumulative exactly-once usage, Policy, Session history, recoverable parent instructions, child-owned memory, CompletionReport finish, parent inbox delivery, and optional two-child internal scheduling. A model or public client still cannot invoke spawn/finish; structured dependency waiting and autonomous scheduling remain future work.

## Tool Gateway

`internal/toolgateway/registry.go` is the static registration point for built-in
tools. Each entry binds its definition, action class, payload normalizer and
Gateway handler, together with its Supervisor and project-config eligibility.
Definition lookup, the CLI schema catalog, model tool membership and Gateway
dispatch derive from this registry. Adding a tool to an existing family reuses
that family's normalizer and handler instead of adding another dispatch branch.

Registration describes the tool; current capabilities determine whether the
model receives it. Skill catalogs, language servers, MCP tool fingerprints,
command adapters and browser backends still provide their runtime schemas and
authority checks. The registry preserves model advertisement order and phase
selection. Historical ledger names and migration SQL remain independent so
stored calls can still be read after a tool is retired.

Project action presets in `internal/toolgateway/project_commands.go` share this
configuration ID catalog without adding model tools. `test.go`, `test.node`,
`test.npm` and `format.go` build fixed native intents; the existing
`command_runtime` owns execution, authorization and Job receipts. npm uses the
same installed native Node/npm CLI launcher resolver as Batch and advanced Git
validation. Repository configuration supplies IDs only, never executable paths,
arguments or shell text.

Every tool invocation uses one pipeline:

```text
Model proposal
  -> schema validation
  -> scope resolution
  -> Run tool-call budget charge
  -> policy decision
  -> approval classification
  -> sandbox/runtime execution
  -> output limits and redaction
  -> evidence/artifact capture
  -> event persistence
  -> result returned to agent
```

The first P5 slice implements this boundary in `internal/toolgateway`. It defines normalized `ToolCall`, `Decision`, `Proposal`, `Execution`, `Result`, and `Outcome` values with bounded UTF-8 fields and legal status combinations. Production CLI, Session, and TUI paths use compatibility adapters over the same Gateway; direct construction of workspace read tools, `toolrun.Manager`, and `fileedit.Manager` is confined to the Gateway.

Workspace IDs are resolved to Store-owned roots before production reads or writes, and a mismatched caller path is rejected before policy or filesystem access. Run-bound calls are atomically charged against `MaxToolCalls`; legacy unbound Sessions remain untracked for compatibility. Scoped low-risk reads use automatic approval. Shell and whole-file replacement normally use per-call approval, while policy rejection maps to permanent denial. Shell completion remains dry-run.

The non-schema protected-delete guard adds an execution-context-only permanent Policy denial before those approval modes are considered. Raw Shell requests and decoded ScriptProcess/Sandbox executable envelopes are rejected when they express recursive deletion or deletion of absolute, traversing, wildcarded, environment-derived, command-substituted, or current-home targets. The stable critical decision contains no protected path and cannot be widened by per-call approval or a Session Grant. Repository text and model explanations remain evidence rather than executable intent. This classifier is defense in depth only: arbitrary scripts and build tools can hide filesystem effects, so a future production executor must keep protected host roots absent/read-only and route deletion through a canonical Go-owned workspace operation. See [ADR 0025](adr/0025-protected-delete-command-guard.md).

Schema v11 makes per-call review a durable two-phase operation. Creating a Shell or FileEdit proposal inserts one fingerprint-bound `tool_approvals` record in the same SQLite transaction as the compatibility proposal and appends `approval.requested`. Review first commits an immutable domain-separated SHA-256 digest of the client key in `approval_operations` plus `approval.decided`, then advances the ToolRun/FileEdit state. The raw client key is not persisted. If the process exits between those commits, replaying the same key resumes the proposal transition. A legacy approval created before its Session gains a Run is transactionally adopted later with `approval.bound`. Reusing a key with different intent, changing a proposal fingerprint, creating a ghost approval, or writing `approved`/`applied`/`completed` without the matching durable decision is rejected at the Store boundary.

Schema v12 adds `approval_session_grants`, grant-operation idempotency, `run_tool_usage`, and ordered `run_tool_calls`. A grant is exact-scope authorization over Run, active Session, Workspace, Tool, and ActionClass. Gateway proposal creation still runs Policy first; only an allowed proposal may consume a matching active grant, and `tool_approvals.grant_id` records that authorization fact. Revocation is optimistic, durable, and immediately removes the grant from lookup while leaving already completed actions auditable. Tool charging uses a transactionally serialized counter and ordered event. The first rejected call beyond the limit records one `tool.budget_exhausted`; repeated rejection does not duplicate that terminal budget fact.

For schema v17 Supervisor calls, the same tool-budget transaction first validates the active Run lease fencing token. A stale worker therefore cannot consume a call before its later structured-memory write is rejected. Non-Supervisor CLI/Session calls retain their established budget path and do not synthesize execution credentials.

`toolgateway.Store` requires the grant and tool-budget contracts at compile time. Script persistence is an explicit optional Gateway capability (`scriptprocess.Store` plus `toolgateway.ScriptRunStore`), so Session-only backends are not coupled to Run-creation methods they never use. A backend cannot execute the script workflow unless it implements both typed Process persistence and the atomic Run/Process transaction.

Schema v13 promotes scripts out of legacy Shell ToolRuns into `script_process_proposals`. `script run` requires a persisted workspace and a workspace-relative existing file, then submits executable/argv through the Gateway as a validated `script_process.v1` envelope whose execution mode is fixed to `disabled`. Mission, Session, Run, initial budget charge, Policy decision, Process, Approval, and Run events commit in one SQLite transaction. A domain-separated digest of `--idempotency-key` supports recoverable replay without storing the raw key; changed intent under the same key is rejected. Multiple Process proposals may belong to one Run, while Store checks require every Process Run, Session, and Workspace binding to agree.

`--local` changes only the recorded requested backend. Approval first commits the durable decision, advances the typed Process through `approved`, then completes it with a dry-run JSON result. The intermediate approved state is recoverable after interruption. No CLI path constructs a Local/Noop runner, and Policy-denied processes can never be promoted by review.

Schema v14 adds `run_artifacts`. A Run-bound terminal Shell or ScriptProcess, a failed FileEdit diagnostic, or an automatic workspace read/list invocation captures each non-empty stdout/stderr stream before the ordinary Result is truncated. The Artifact Store requires exact Run/Session/Workspace and persisted source linkage, normalizes UTF-8, applies redaction again at the Gateway and Store boundaries, stores at most 4 MiB per stream, and computes SHA-256 over the redacted content. The row and metadata-only `artifact.created` event commit atomically. Replaying a completed proposal reuses `(run_id, source_id, stream)`; different content or metadata conflicts. A capture failure after terminal proposal completion is recoverable by replay without repeating the approval or tool lifecycle event. The legacy v1 `artifacts` table remains a generated-file path registry for old Task workflows; it is intentionally separate from the content-bearing, Run-scoped v14 table.

Schema v15 adds automatic, create-only `work_item_create` and `note_create` actions under the new `run_memory` class. Calls carry a typed JSON payload and a non-serializable operation key, while Run, Session, Workspace, and requester identity come from the Go control plane. Strict decoding, normalization, dependency-ID shape validation, required identity checks, and executor availability happen before the budget charge. Policy and the authoritative persisted Run/Session/Workspace binding check happen after charging because a well-formed attempted call consumes budget. Denied calls append a durable decision without mutation; allowed calls atomically commit the redacted entity, allowed decision, domain event, `tool.completed`, and `structured_tool_operations` ledger row. The ledger stores only a domain-separated operation-key digest and normalized request fingerprint. Same-key/same-intent retries return the original target, changed intent conflicts, and concurrent calls from independent SQLite connections converge on one row. SQLite connections use immediate write transactions with the existing busy timeout to avoid deferred read-to-write lock races. Replays, conflicts, scope failures, and denials still count as invocations; malformed input does not.

Structured-memory Results contain metadata only and therefore do not create output Artifacts. Create is automatic because it is additive and reversible through operator lifecycle commands; update, complete, cancel, archive, and restore remain outside the model tool surface.

Schema v16 persists `run_supervisor_tool_rounds` and `run_supervisor_tool_calls`. One successful primary model response and its pending batch commit in the same transaction; each result and round-completion event is also transactional. Restart recovery executes only pending calls, while terminal results are replayed into the Provider transcript as Anthropic-compatible `tool_use`/`tool_result` blocks. A response is limited to four calls and a turn to four rounds. Provider call IDs are validated at ingress but replaced with deterministic local protocol IDs; the idempotency operation key derives from Run, turn, tool name, and redacted canonical arguments, so changed Provider IDs and repeated semantic intent converge. Policy denial and tool-budget exhaustion become bounded error results; storage, cancellation, and internal failures leave the call pending. Protocol repair advertises no tools, and Shell, file, process, network, update, delete, completion, and archive actions remain outside the Provider surface.

Schema v30 rebuilds only the Supervisor-call constraint so `specialist_delegation_propose` can share those same bounded rounds without changing v16 rows. The new `agent_proposal` class has a dedicated executor and operation table rather than masquerading as Run memory. Syntactic protocol failures are rejected before tool-budget charging; a well-formed proposal attempt is charged, then Policy and authoritative root/capability/budget checks run. A Policy denial or semantic rejection is returned as bounded tool-result metadata so the model may correct its suggestion, but no failed attempt leaves a proposal row. Successful output includes a proposal ID, assignment count, and explicit `admission_authorized=false`, never assignment text or execution credentials.

Existing `tool_runs` and `file_edits` remain compatibility proposal records, while typed script processes use their own v13 table. `tool_approvals` is the single authorization fact used to gate every privileged transition, and transactional Run-event projection is preserved. Gateway Results enforce 128 KiB stdout, 32 KiB stderr, valid UTF-8, MIME metadata, truncation flags, secret redaction, and Artifact reference metadata; the larger redacted Artifact remains separately inspectable and hash-verifiable. Store JSON redaction parses payloads with exact numbers, redacts string values recursively, and re-encodes them so escaped nested JSON cannot be corrupted. Payloads are capped at 1 MiB, 64 levels, and 100,000 nodes.

## Sandbox

Schema v48 introduces `sandbox_manifest.v1` as a descriptive protocol before any backend is enabled. Go strictly decodes and normalizes executable/ordered argv, a virtual working directory, workspace-relative mounts, exact network scope, resource limits, environment literal/secret-reference bindings, input Artifact IDs, output capture/paths, timeout, and cancellation grace. Unknown or duplicate fields, invalid UTF-8, trailing data, traversal, overlapping mounts, wildcard or non-canonical network targets, credential-shaped literals, and values outside hard bounds fail closed.

The application binds one normalized fingerprint to an exact non-terminal Run, Mission, persisted Workspace root, normalized Mission Scope, Policy decision, optional exact approval, requester, and generated cancellation identity. A Manifest may only narrow the Mission network allowlist. Docker/Local intent, writable mounts, network, or secret references require approval when Policy allows them; permanent denial remains final.

SQLite stores immutable preparation, validation, and digest-keyed operation metadata plus content-free events. It does not retain executable, argv, paths, environment values, secret references, network targets, or Manifest JSON. Same-key and cross-process replay converge; changed intent conflicts. `NoopRunner` is the only application validator. Go types, SQL checks/triggers, events, and CLI output all fix `backend_enabled=false` and `execution_authorized=false`, including for approved records. `LocalRunner` and `DockerRunner` fail closed and start no process.

The target model remains one isolated environment per Run, shared only by Agents in that Run through the Go coordinator. A future execution step must resupply the Manifest, reproduce its fingerprint, re-resolve mount sources under the Workspace root, and recheck Policy, Scope, approval, budget, and execution lease before it can even become a candidate.

Schema v49 implements that candidate boundary without enabling a backend. Sandbox approval requests use the existing `tool_approvals` table and bind the preparation ID, Run Session, Workspace, `sandbox.manifest/sandbox_execute`, and authorization fingerprint. Operator review is explicit; pending or denied records cannot pass candidate validation. Approval never overrides permanent Policy denial.

Candidate validation receives the full Manifest again and recomputes every v48 binding. Go `os.Root` opens each mount source relative to the resolved Workspace root and rejects escaping symlinks or non-file/non-directory objects. The application then snapshots aggregate root/Specialist/Fan-out usage and tool-call usage. Candidate insertion takes the Run write lock, recomputes those counters, rejects an active execution lease, and commits an immutable digest-keyed fact only when nothing changed. SQL triggers enforce the same approval, budget, lease, and disabled-backend invariants against direct writes. Raw Manifest, executable, argv, paths, Workspace root, environment values, secret references, and network targets remain transient.

`sandbox_execution_candidate.v1` records only that these checks passed at one point. It always has `backend_enabled=false` and `execution_authorized=false`; no Runner is called. See [ADR 0009](adr/0009-sandbox-approval-candidate.md).

Schema v50 adds a disabled lifecycle above that candidate. `sandbox_execution.v1` is immutable and one-to-one with its v49 candidate. Creation resupplies the complete Manifest and rechecks every prior binding, then pins each input Artifact by exact Run/Session/Workspace, ordinal, identity, content digest, size, MIME, stream, source, and redaction state under a 16 MiB aggregate limit. The output plan retains only capture flags, path count, byte limit, and a digest. Neither raw output paths nor Artifact content enter the lifecycle ledger.

Sandbox ownership is deliberately separate from the Run execution lease. A generation-fenced `sandbox_execution_leases` row permits cleanup after the Run becomes terminal and prevents a stale worker from releasing or committing over a successor. The private lease/cleanup rows necessarily retain opaque lease and worker identities for fencing, while events and CLI projections omit both. The initial generation can only prepare the disabled root and is released immediately. Immutable cancellation and cleanup operations converge under digest-only idempotency keys. Cleanup revalidates all inputs while holding the active Sandbox lease; v50 accepts only `backend_disabled`, with no started backend, orphan, or output Artifact. See [ADR 0010](adr/0010-disabled-sandbox-lifecycle.md).

Schema v51 adds a one-to-one disabled preflight above each eligible v50 lifecycle. The Application must resupply and normalize the complete Manifest, then revalidate preparation, candidate, lifecycle, Mission Scope, Policy, exact approval, workspace mounts, aggregate budgets, Run-lease quiescence, and every bound input Artifact before it can append the fact. SQLite independently binds the immutable v48-v50 identities, current usage, released Sandbox lease, non-terminal Run, absence of cancellation/cleanup, and exact input rows. Digest-only operations and a unique execution binding make same-intent retries converge while rejecting a second preflight identity.

The preflight freezes a 16-item backend threat model and a metadata-only output-export design. `DisabledBackendInspector` is the sole production inspector: all checks are required but unverified, backend availability is false, and container identity is explicitly unbound. Output slots retain only domain-separated locator fingerprints and kinds; file slots require regular files and reject symlinks/special files, while every slot requires MIME detection and redaction. The plan is all-or-nothing, aggregate-byte bounded, restart-reconciled, export-disabled, and unable to commit an Artifact. Root/check/slot/operation rows are immutable, and CLI/events omit locators, raw paths, commands, Manifest content, container identity fingerprints, operation digests, and private leases. See [ADR 0011](adr/0011-disabled-sandbox-preflight.md).

Schema v52 exercises the next protocol boundary without introducing production authority. `SimulationBackendClient` derives a metadata-only `sandbox_backend_evidence.v1` report entirely in memory and has no daemon transport. It requires a canonical OCI image digest, separately fingerprints daemon capabilities, mounts, network, secrets, container configuration, resources, termination, orphan recovery, and the v51 output plan, then emits exactly 16 ordered `simulated_pass` items. Those items are satisfied only for harness assertions and remain `verified=false`; `trust_class=simulation_only`, production verification, backend availability, execution authorization, and Artifact authorization are fixed false in Go and SQLite.

The same schema adds strict `sandbox_output_fixture.v1` input and immutable `sandbox_output_simulation.v1` facts. The in-memory harness matches exact output slot order and kind, applies aggregate limits, MIME detection and redaction, accepts only regular files for file slots, rejects links and special files, and stages every redacted output before one fake atomic commit. Injected failure or cancellation leaves zero fake outputs; the Store independently requires zero production Artifacts and never inserts `run_artifacts`. Evidence and output creation each resubmit the complete Manifest and revalidate all v48-v51 authority, budget, lease, mount, and input-Artifact bindings. Persistence and public projections omit fixture bodies, locators, raw paths, commands, Manifest data, secrets, container IDs, operation digests, and lease identities. See [ADR 0012](adr/0012-simulation-only-sandbox-evidence.md).

Schema v53 adds the first real-daemon boundary, but only as a production metadata observation. `DockerReadOnlyTransport` exposes a fixed endpoint plus ping, daemon version/info, and exact-digest image inspection. The Linux implementation disables proxies and redirects, dials only `/var/run/docker.sock`, and allows GET requests only to `/_ping`, `/version`, `/info`, and `/images/<digest>/json`; it ignores `DOCKER_HOST` and accepts no TCP or caller-selected socket. The Windows implementation records a bounded unsupported-transport result until a separate named-pipe audit exists. No Docker CLI or full client is used, and the interface has no create, pull, start, run, exec, stop, or remove operation.

An explicit CLI confirmation is required before probing. The Application first resupplies the complete Manifest and revalidates the matching v52 evidence and output simulation plus every v48-v51 identity, Scope, Policy, approval, mount, budget, Run/Sandbox lease, input Artifact, cancellation, and cleanup condition. SQLite repeats current-state authority checks before committing an immutable root, six ordered items, and digest-only operation. Same-intent replay returns the prior result without a second probe, cross-process races converge, and each output simulation accepts at most eight observations.

The only durable statuses are complete observation, unavailable daemon, and unavailable image. Complete observation means that bounded daemon and image metadata were read; it does not verify the v51 controls. Private mount propagation cannot be established through these read-only APIs and is explicitly `not_observable_read_only`. Production verification, backend availability/enabling, execution authorization, and Artifact-commit authorization remain false in Go and SQLite. Raw daemon ID/name/root, socket, security-option text, image ID/RepoDigests, GraphDriver details, Manifest, command, operation key, and private leases are transient and absent from events and CLI. See [ADR 0013](adr/0013-read-only-docker-observation.md).

Schema v54 freezes the next boundary without adding a write-capable transport. `CompileDockerContainerSpec` accepts only a complete v53 observation and a resubmitted exact Manifest after Application and SQLite independently revalidate every v48-v53 authority binding. It deterministically fixes user `65532:65532`, read-only root and input mounts, exactly one writable output mount, `rprivate` propagation, no-new-privileges, all capabilities dropped, init, network `none` or managed default-deny exact allowlisting, ephemeral secret mounts, CPU/memory/PID/output/time limits, SIGTERM/SIGKILL behavior, and authority-derived orphan labels.

The complete specification and its commands, arguments, paths, targets, environment values, secret references, labels, and container name remain in memory. Immutable plan roots retain only bounded counts, control results, fake transaction steps, and domain-separated fingerprints. Sixteen controls remain `compiled_not_applied` with `applied=false` and `verified=false`. The seven-step fake transaction orders reconcile, create, start, wait, stop, export, and remove; failure, simulated crash, or cancellation publishes nothing. Even its successful result fixes daemon writes, backend contact, production submission, execution/export, and Artifact-commit authority to zero. See [ADR 0014](adr/0014-deterministic-docker-container-plan.md).

Schema v55 introduces a separate, default-disabled `DockerContainerWriteTransport` without extending the v53 observer. The Linux implementation fixes `/var/run/docker.sock`, Docker API `1.40`, no proxy or redirects, and a closed allowlist containing exact image inspect, deterministic-name create, exact container inspect, and returned-ID non-forced delete with fixed `v=1` anonymous-volume cleanup. It rejects `DOCKER_HOST`, TCP, caller-selected sockets, pull, start, exec, attach, logs, archive/export, volume management, image build, and generic requests. Windows remains explicitly unsupported.

Only an explicit operator confirmation and a complete current v54 plan can enter the strict no-network/no-environment/no-secret profile. Before create, the local image RepoDigest must match the plan and its config must declare no `VOLUME`. Host mount sources are resolved component by component beneath the trusted workspace without symlinks. The stopped container must exactly match non-root/read-only/no-new-privileges/drop-all/init/resource/private-mount plus attachment/device/port controls before it is removed. A stale deterministic name is reconciled only when the existing container is an exact never-started authority match. Cancellation, failure, and uncertain create responses use an independent five-second context to re-inspect by ID/name and never delete blindly.

The immutable v55 rehearsal records five steps, three daemon reads, two normal writes or three writes after exact stale reconciliation, and metadata/fingerprints only. Raw IDs, paths, commands, environment values, secrets, socket paths, and complete specifications remain transient. Replay returns before transport access and concurrent Stores converge. Go and SQLite fix container/process start, image pull, output export, production verification, backend enablement, execution authority, and Artifact authority to false. See [ADR 0015](adr/0015-bounded-docker-write-rehearsal.md).

Schema v56 closes the process-crash window between a v55 daemon mutation and its final durable result. Before the first mutation, the Application writes one immutable attempt intent and acquires a bounded generation-fenced lease. The transport then stages one deterministic-name, never-started container, either by creating it once or adopting an exact authority match left by an uncertain response or prior generation. A durable stage checkpoint freezes 19 exact configuration controls, including an actually empty inherited environment, but every item is fixed to `execution_evidence=false`.

Cleanup begins only after the stage checkpoint is durable. It re-inspects the deterministic name and removes only an exact authority, request, configuration, and container-ID-fingerprint match; absence is idempotent and a mismatched same-name container is never removed. Bounded failure codes release the lease, and an operator can resume from the durable attempt ID only by resubmitting the full Manifest and explicit daemon-write confirmation. Stale generations cannot replay checkpoints under a newer owner. The legacy rehearsal, operation, v56 completion, and final lease release commit atomically. Persistence and projections remain metadata-only, while the v55 fixed endpoint and closed no-start/no-exec/no-pull/no-export operation set are unchanged. See [ADR 0016](adr/0016-recoverable-docker-rehearsal-attempt.md).

Schema v57 inserts a separately confirmed host-input capture gate between v56 stage and cleanup. `DockerHostInputStager` is default-disabled. On Linux, the local implementation opens the absolute workspace root and every normalized read-only source with `openat2` no-symlink/no-magic-link/beneath-only/no-cross-device resolution. Each entry is first inspected through `O_PATH`; only a matching regular-file or directory descriptor is then opened for traversal or reading, so FIFOs and other special files fail before a potentially blocking content open. It supports directory and single-file mounts, bounds directory enumeration before allocation, observes cancellation during file reads, rejects hard links, traversal and resource-limit violations, then rechecks device, inode, mode, link count, size, mtime, and ctime after the complete tree has been pinned.

The stager combines those entries with input Artifacts reverified by exact Run/Session/Workspace, digest, size, MIME, stream, source, redaction state, and order. A deterministic tar uses sanitized archive names, modes, ownership, and timestamps. It exists only in a sealable `memfd`, receives write/grow/shrink/seal kernel seals, and is reread to verify its digest. The immutable v57 intent/result binds current attempt, stopped-container fingerprint, plan, input/authority/spec fingerprints, requester, and lease generation. Semantic fingerprints exclude generated row IDs, so independent workers converge on one intent/result. SQL blocks completion while evidence is missing; failure performs bounded stopped-container cleanup before releasing the lease, and takeover can resume without another create. Missing resume confirmation is rejected before lease acquisition. Durable facts retain only counts, sizes, digests, and false authority claims.

The v57 archive is not handed to Docker. `daemon_consumed=false` and `execution_evidence=false` are protocol and SQL invariants, so this closes replacement during descriptor capture but does not prove the bytes a future container would consume. A daemon-owned immutable handoff remains an independent gate. See [ADR 0017](adr/0017-descriptor-sealed-host-input-staging.md).

Schema v58 closes the recovery downgrade window before that capture gate. For every new v56 attempt, Application derives one immutable `sandbox_docker_host_input_requirement.v1` from the reviewed v54 authority and the two explicit staging flags. The requirement, attempt, first generation lease, and audit events commit in one SQLite transaction before any daemon stage. It binds the attempt and plan to Run, Mission, Workspace, requester, operation digest, Manifest/mount/input/authority fingerprints, and bounded input counts. Candidate row IDs and timestamps are excluded from the semantic fingerprint so independent workers converge.

On recovery, the durable requirement is authoritative: `required=true` forces v57 capture even when the caller omits the old staging flags, while `required=false` cannot be widened later. Go validation and SQLite triggers both reject a required completion without matching staging evidence, false-to-staging expansion, cross-attempt or cross-plan reuse, mutation, and deletion. The migration deliberately does not backfill legacy v57 attempts because historical operator intent cannot be reconstructed. It records only their existing IDs in an immutable compatibility set before disabling marker insertion; every later stage, staging intent, and completion must reference a requirement or one of those migration-created markers. Durable facts contain only counts, digests, identities already present in the authority chain, and booleans; they contain no source paths, bytes, descriptors, raw container IDs, operation keys, or private lease owners.

Schema v58 still does not hand bytes to Docker. Direct archive upload to a read-only target conflicts with Docker Engine API behavior, so the target root or input mount is not made writable merely to satisfy the handoff. Schema v59 must separately prove a daemon-owned writable carrier, fixed-destination upload, daemon readback against the v57 digest, carrier removal, and recreation of the never-started target with the verified carrier mounted read-only. Start and every execution/backend/Artifact authority remain false. See [ADR 0018](adr/0018-durable-pre-stage-host-input-requirement.md).

Schema v59 adds that independently gated handoff. An immutable handoff requirement is committed with every new attempt, and a write-ahead intent binds the required v58 choice, exact v57 report and bundle digest, stopped target, current lease generation, plan, requester, and authority before archive or volume mutation. The default-disabled Linux transport accepts only the fixed local Unix endpoint and Docker API `1.40`. Deterministic request-derived names identify one local volume and one never-started carrier; `/cyberagent-input/bundle.tar` is the only archive destination and the complete destination tree is reserved from Manifest mounts.

The carrier receives the exact sealed archive into its writable volume. A daemon GET reads the file back, after which Go checks exact length and SHA-256. Only then are the carrier and original stopped target removed and the target recreated with that volume read-only. The recreated target is fully re-inspected and removed, followed by non-forced volume deletion. A retry first reconciles only exact label/configuration/fingerprint matches, including carrier, volume, or final-target residue. Foreign same-name resources fail closed and are never deleted. Independent failure cleanup removes only exact owned resources; a pending intent can resume under a later generation without repeating v55 stage.

The result is immutable and metadata-only. SQLite gates required cleanup and completion on it and fixes container start, process execution, output export, backend enablement, execution authority, and Artifact commit authority to false. Schema v59 proves bounded daemon consumption/readback followed by cleanup; it does not define runtime input projection, process isolation, termination, orphan recovery for running containers, or production output export. See [ADR 0019](adr/0019-daemon-owned-host-input-handoff.md).

Schema v60 defines that runtime-input mapping as a separately confirmed plan without contacting Docker. Application revalidates the complete v48-v59 authority chain, recompiles the exact resubmitted Manifest and v54 container specification, and recaptures a short-lived v57 sealed bundle whose report, digest, length, counts, and Artifact payload identity must match the completed v59 handoff. Go accepts only byte-for-byte canonical v57 PAX tar data and rejects links, devices, traversal, duplicate or parentless entries, unexpected roots, empty Artifacts, trailing data, and non-canonical headers. The first profile intentionally accepts only directory-root read-only mounts; input Artifacts map to fixed `/cyberagent-input/artifacts`.

Each source root is compiled in memory into an ordered relative tar projection and future read-only/no-copy volume binding. Future volume identity includes the immutable handoff fingerprint, giving retry convergence without cross-Run name collisions. SQLite atomically commits the operator-confirmed plan, metadata-only items, aggregate completion marker, digest-only idempotency operation, and event. Raw targets, host paths, archive names, file names/content, volume names, and bytes are not durable. Status remains `compiled_not_applied`; daemon contact/application, start, exec, export, backend, execution, and Artifact authority are fixed false. See [ADR 0020](adr/0020-deterministic-runtime-input-projection.md).

Schema v61 applies those projections behind a second dual-confirmation gate. A write-ahead intent and independent generation lease commit before daemon mutation. Application then revalidates v48-v60, recompiles the exact specification, re-resolves the one writable output bind, recaptures the sealed bundle, and reproduces the v60 projection set. Completion and failure are fenced to the current active generation; replay of a completed operation returns metadata without recapture or Docker access.

The fixed local-Unix transport creates one local volume and one never-started writable carrier per projection, uploads only at `/cyberagent-input`, reads the archive back, and verifies the complete expected tar semantics before deleting the carrier. The retained target mounts every volume read-only with `NoCopy` and preserves the reviewed output bind as its sole writable mount. Image, identity, read-only root, capability, resource, network, environment, port, device, attachment, label, mount, and never-started configuration are inspected exactly. Retry and bounded independent cleanup operate only on full request/role/item authority matches; foreign collisions are protected. Operation work has a pre-expiry cutoff and cleanup reserve before a later generation may take over.

Durable v61 facts contain only bounded IDs, digests, counts, timestamps, typed failure codes, and false authority flags. Targets, paths, file and resource names, raw daemon IDs, archives, sockets, operation keys, and lease owners remain private or transient. The final status `volumes_applied_target_never_started` proves input-volume materialization and target configuration, not process isolation. Start, exec, attach, logs, export, backend, execution, and Artifact commit remain unavailable. See [ADR 0021](adr/0021-recoverable-runtime-input-application.md).

Schema v62 separates retained-resource observation from deletion. A metadata-only inspection proves only whether the never-started target and every deterministic input volume are exact-owned, absent, or foreign. Cleanup requires a separate dual confirmation, write-ahead intent, and generation lease; it preflights every resource before any DELETE, refuses all deletion on one foreign collision, removes only exact-owned resources, and verifies final absence. It adds no start, exec, attach, logs, export, backend, execution, or Artifact authority. See [ADR 0022](adr/0022-retained-runtime-input-resource-lifecycle.md).

Schema v63 freezes the denied start boundary as an immutable design review. All sixteen v51 checks map to explicit blockers and all eleven future start/wait/TERM/KILL/orphan transitions remain unimplemented and unauthorized. Schema v64 separately records an operator-selected `preview|docker|local` profile, but every profile remains non-authorizing and process-disabled. See [ADR 0023](adr/0023-blocked-docker-start-gate-review.md) and [ADR 0026](adr/0026-run-execution-profile-selection.md).

Schemas v65-v67 create a non-authorizing production-evidence chain. v65 stores a fixed sixteen-item machine receipt; v66 places an immutable attempt, generation-fenced lease, and quiescent checkpoint before collection; v67 permits an explicitly opted-in Linux collector to perform one exact-label inventory GET plus `_ping`, `version`, `info`, and exact existing-image inspection. The transport is local/read-only, ignores `DOCKER_HOST`, never pulls, and has no mutation method. All sixteen results are deliberately `observed_failed`, production verification remains zero, and every process/output/Artifact authority bit remains false. See [ADR 0027](adr/0027-non-authorizing-docker-production-evidence-ledger.md), [ADR 0028](adr/0028-recoverable-docker-production-evidence-attempts.md), and [ADR 0029](adr/0029-bounded-linux-read-only-docker-evidence-harness.md).

Schema v68 adds one immutable operator acceptance/rejection decision over an exact v67 receipt. A digest-only operation and review commit atomically; reciprocal SQL gates prevent either half from committing alone, reject legacy/in-flight sources, and bind all sixteen insufficient evidence items. Acceptance classifies only the bounded metadata receipt. It leaves zero production verification, sixteen blockers, and false start/process/output/Artifact authority, and the review path performs no Docker or process call. See [ADR 0030](adr/0030-immutable-docker-production-evidence-review.md).

Schema v97 adds a separate non-authorizing lifecycle aggregate for the fixed-endpoint Docker transport. An immutable launch intent and generation-one lease commit before create; each daemon mutation has an append-only prepared action, and exact observations form a hash-chained `created -> started -> exited/cleaning -> cleaned` ledger with explicit failure facts and one immutable cleanup receipt. Every action, transition, receipt, and daemon mutation is fenced by the complete active lease identity and expiry. Recovery is database-led, never enumerates the daemon, and mutates only the deterministic name after the complete nine-label ownership set and exact configuration match. Partial, legacy, foreign, or inconsistent containers remain untouched. This supplies crash-safe engineering mechanics for start/wait/TERM/KILL/delete, but still grants no Run, Agent, Tool, CLI, HTTP, Desktop, production execution, output, or Artifact authority. See [ADR 0096](adr/0096-durable-docker-lifecycle-ownership-and-recovery.md).

Schema v125 is a forward compatibility repair for one exact pre-final Windows
preview history of v97. It accepts only the pinned v97 name and checksum,
preserves that historical migration row, and transactionally replaces the one
affected cleanup trigger with the canonical v97 definition. Unknown v97
histories still fail closed. See
[ADR 0126](adr/0126-legacy-v97-docker-trigger-compatibility.md).

Schema v126 adds the non-authorizing `workspace_access` Run permission ceiling.
It admits only a `sandboxed_workspace_command` decision when an independent,
process-local Workspace Sandbox adapter is ready; host process, network,
credential, home, terminal, Agent-input, and Full-CDP facts remain false.
Windows x64 installs the Local adapter only behind an explicit startup request
and a real AppContainer/WFP/Job/ACL readiness proof. Failure keeps the mode
unavailable and no existing host runner may serve as a fallback. Schema v131
connects that backend and the separately gated fixed Docker `network=none`
Standard Code path to the exact `sandboxed_workspace` Command Runtime adapter;
daemon, image, or Local readiness failure still never selects a host runner.
Selecting any new
permission revision atomically releases the active Run execution lease and fences
old Job/tool authority. See
[ADR 0127](adr/0127-workspace-access-permission-contract.md) and
[ADR 0130](adr/0130-windows-local-sandbox-backend.md).

Schema v127 adds `drydock-workspace.v1` as a Run-owned ownership and recovery
boundary around one product-created Git worktree. The first create is a read-only
Workspace Trust review over the exact source root/repository/common-dir/branch/base,
raw index, dirty state, and special-entry counts. A second call must pin the exact
digest; the immutable receipt grants no process authority. Dirty source content is
not copied, and v1 rejects source symlink entries, submodule gitlinks, and linked or
reparse-point roots.

Creation persists a `preparing` owner before Git materialization. Use, Checkpoint,
Rewind, Undo, Fork, Deliver, Cleanup, startup reconciliation, and expiry GC revalidate
the complete Run/source/worktree/registry/branch/base/binding/generation identity.
Tracked, untracked, staged, and raw-index state flows through the existing checkpoint
store. Any uncertainty or observed user change is preserved as `recovery_required`.
Delivery is a bounded review patch with merge/push/force/source-overwrite authority
fixed false. Cleanup uses non-force removal only for an exact clean owner, retains the
branch and audit rows, and never enumerates or removes unknown directories. See
[Drydock Workspaces](drydock.md) and
[ADR 0129](adr/0129-run-owned-drydock-workspaces.md).

Schema v128 lets the existing immutable Docker admission ledger accept the v126
`workspace_access` permission; it preserves historical admissions and restores every
cross-table and immutability trigger. Issue #133 composes the existing ledgers through
`standard-code-command.v1`. The Docker adapter recognizes one exact manifest: the
current Drydock is the sole writable host projection at `/workspace`; an exact
application-owned file is mounted read-only over `/workspace/.git`; container rootfs/toolchains
are read-only; user/resources/cancellation are fixed; network is none; and environment,
credentials, arbitrary mounts/endpoints/flags, and image pulls are absent. Current
Drydock generation/Checkpoint/binding, Docker profile, `workspace_access` permission,
per-call approval, process gates, Policy, budgets, fixed image, and readiness are
revalidated immediately before start. Drydock is not the isolation boundary; the
fixed Docker container is. Terminal writes become one idempotent Drydock Checkpoint,
while bounded logs retain the existing receipt. See
[Standard Code Docker](standard-code-docker.md) and
[ADR 0131](adr/0131-standard-code-docker-network-none-backend.md).

Schema v133 adds the Go-owned `standard_code_preset.v1` composition layer. A single
intent binds its operation digest, complete request fingerprint, requested and actual
Run, Workspace, selected backend and reason, and final Drydock/snapshot/event range.
`auto` selects only a currently ready Local adapter; Docker remains explicit. Existing
Runs must be created or paused with no active execution lease. A running Run uses a
separate durable pause-and-configure intent; the final transaction rechecks true
Supervisor quiescence, pauses the Run, and commits Code/Plan, Local or Docker,
controlled, `workspace_access`, restricted CDP, and the exact ready trusted Drydock
together. An incompatible Surface produces a new Code/Plan Run rather than changing
immutable Run identity.

The CLI, control-token HTTP/OpenAPI, and Desktop in-process handler all call the same
Application service. React submits one operation instead of sequencing lower-level
endpoints. Results contain actual selection/readiness, stable blockers and next steps,
but no bearer or process authority. Models, Skills, MCP, hooks, plugins, and repository
configuration have no invocation route. The Drydock/worktree remains an ownership and
recovery boundary; Local OS or fixed Docker supplies isolation. See
[Standard Code atomic preset](standard-code-preset.md) and
[ADR 0136](adr/0136-atomic-standard-code-preset.md).

Schema v135 adds `standard_code_supervisor.v1` inside the existing root
`AgentRunner`. It persists a bounded Inspect/Plan/Checkpoint/Edit/Execute/
Observe/Diagnose/Deliver projection and gates every Standard Code call before the
ordinary tool gateway. Two consecutive read-only rounds and an explicit selected
Plan precede mutation. A mutation is counted only through its exact completed
after-Checkpoint; Command Runtime exit/Job/cursor/digest/Artifact facts verify only
that mutation epoch. Text output remains untrusted evidence.

The append-only ledger binds each transition to the preset, root, Run/Mission/
Workspace, mode, execution-profile, interaction, permission, and browser-CDP
revisions, turn/attempt, active lease, existing Supervisor call, and Run event.
Deterministic intent fingerprints suppress new calls
that repeat handled writes or process actions, while recovery of the same call uses
the existing execution and runtime receipts. Background Jobs retain permission and
cursor ownership across turns. Fixed budgets and drift produce a durable stop rather
than an inferred success. Other Profiles, Cyber, Plan mutation, child, and Specialist
matrices are unchanged and fail closed. See [Standard Code bounded completion loop](standard-code-supervisor.md)
and [ADR 0137](adr/0137-bounded-standard-code-supervisor.md).

Schema v98 adds the bounded I/O contract without changing that authority boundary. Read-only input projection, fixed non-streaming attach, per-stream byte/line/deadline limits, strict output-archive walking, process-local staging, re-hashing, and atomic output commit are all bound to the exact lifecycle attempt/generation. Raw logs do not persist and the Workspace is never a writable container mount. See [ADR 0098](adr/0098-bounded-docker-container-io-contract.md).

Schema v99 is the distinct product-composition layer. One Go-owned
`DockerSandboxService` supplies readiness, admission, status, start,
cancellation, and startup recovery. CLI invokes it directly; authenticated
HTTP/OpenAPI and the in-process Desktop handler project it; the fenced model
tool `sandbox_docker_run_propose` may call only admission. No adapter can
construct a Docker request or replace the service's current Run/Profile/
permission/per-call approval/Policy/budget checks.

```text
CLI ----+
HTTP ---+                         +--> fixed local Docker lifecycle
Desktop +--> DockerSandboxService +--> bounded logs/output commit
Model --+    (Admit only)         +--> schema v99 immutable receipts
```

The runtime capability and its random epoch are process-local. SQLite stores
only a fingerprint, so reopening or editing the database cannot restore start
authority. `sandbox.readiness.v1` is a fresh, 30-second, read-only proof over
the fixed local endpoint, API/Linux/PIDs/resource support, and exact existing
image. Admission and the final start fence both repeat current authority and
readiness checks. Before any lifecycle or daemon write, an independent Start
WAL and metadata-only event commit atomically. Admission, Start, and
Cancellation have separate idempotency domains; same-endpoint exact replay
converges, while a second different Start key for one admission conflicts. The
WAL binds the current epoch but cannot restore start authority after restart.
A missing daemon produces stable unavailable evidence and never falls back to
`LocalRunner` or an unrestricted host process.

The v99 product profile accepts only environment-free, secret-free
`network.mode=disabled`. Docker create explicitly selects network `none`,
omits ports/DNS/extra-hosts/links/endpoints/proxy environment, and inspection
requires the complete absence of address, gateway, alias, and DNS-name state.
Although the generic Manifest and v54 compiler can describe an allowlist,
scoped egress is not implemented: exact host/port/protocol enforcement still
needs a Go-owned egress guard with its own lifecycle and recovery proof.
Allowlist requests therefore return
`managed_egress_unavailable/use_network_disabled`.

After an exact exited checkpoint, v99 captures bounded logs before cleanup.
Only natural exit zero plus a fresh Artifact-authority check may stage,
re-read, re-hash, and atomically commit outputs; timeout, cancellation,
non-zero exit, I/O failure, or authority change never commits an output.
Cleanup follows regardless of outcome. Sticky cancellation persists before
the active context is signalled. Startup recovery acts only on an admission
that already has a launch binding, and may reconcile/terminate/clean the exact
owned resource without restoring start capability; an admission-only record
is never auto-started after restart. See
[ADR 0099](adr/0099-docker-sandbox-product-admission-and-recovery.md).

- local sources are copied or mounted read-only by explicit manifest entries;
- writable outputs use dedicated run directories;
- network access starts disabled or scope-limited;
- CPU, memory, process, output, and wall-clock limits are part of run configuration;
- teardown is idempotent and records cleanup failures;
- the Docker client is introduced only with the real backend.

`LocalSandbox` remains development-only and must use the same approval/event pipeline. It is never treated as an isolation boundary. None of the target-model bullets are execution claims for schemas v48-v68; v51 names required evidence, v52 exercises its shape against fakes, v53 observes only daemon/image metadata, v54 compiles and fake-stages a plan in memory, v55-v56 operate only on never-started containers, v57-v61 capture and apply read-only input material to a retained never-started target, v62 inspects and removes exact-owned retained resources, v63 records a blocked design, v64 records non-authorizing profile intent, v65-v67 collect still-insufficient metadata, and v68 only records an operator decision over that receipt. Schema v97 and v98 remain non-authorizing internal lifecycle/I/O facts; schema v99 alone composes their exact network-none subset behind fresh product admission. It does not implement daemon-wide orphan discovery, secret materialization, stdin/TTY, image pull/build, managed egress, or general Docker/Local execution. See [ADR 0008](adr/0008-sandbox-manifest-boundary.md) through [ADR 0030](adr/0030-immutable-docker-production-evidence-review.md), [ADR 0096](adr/0096-durable-docker-lifecycle-ownership-and-recovery.md), [ADR 0098](adr/0098-bounded-docker-container-io-contract.md), and [ADR 0099](adr/0099-docker-sandbox-product-admission-and-recovery.md).

## Scope and Safety

Authorization scope is a system-owned run snapshot. User instructions and model messages may narrow scope but cannot expand it.

Scope includes:

- permitted workspace roots and file access mode;
- approved domains, IP ranges, ports, and protocols;
- whether network access is disabled, allowlisted, or unrestricted by an authorized operator;
- allowed tool classes and required approvals;
- secret-handling and artifact-export rules.

The initial product remains conservative: real public-network attack automation and unapproved shell execution stay disabled.

## LLM, Context, and Skills

The LLM router remains independent from orchestration. Run snapshots record the selected provider/model route without persisting API keys. Providers normalize HTTP, network, protocol, and cancellation errors into typed outcomes; only AgentRunner decides whether a side-effect-free model request may be retried. Legacy unbound Session chat receives typed errors through Router but does not gain an implicit retry loop.

Environment adapters expose `mimo`, `deepseek`, and `anthropic` over the shared
Anthropic-compatible transport, plus the canonical `openai` adapter over OpenAI Chat
Completions, and the keyless `ollama` adapter over the native local daemon protocol.
Each adapter reads only its dedicated API-key/base-URL/model namespace.
For `openai`, that namespace is `CYBERAGENT_OPENAI_API_KEY`,
`CYBERAGENT_OPENAI_BASE_URL`, and `CYBERAGENT_OPENAI_MODEL`; its defaults are
`https://api.openai.com` and `gpt-4.1-mini`. For `ollama`, the namespace is the
loopback-only `CYBERAGENT_OLLAMA_BASE_URL` plus `CYBERAGENT_OLLAMA_MODEL`; there is
no credential, and the endpoint stays off unless both variables are set explicitly.
The Provider object and secrets remain
inside the Go control plane and never enter Run configuration or event payloads.
The production Registry supplies a 60-second client timeout, and internal injected
clients are clamped to that maximum. The adapter disables redirects and emits only the
bearer authorization plus content negotiation headers required by Chat Completions;
custom headers and repository-selected compatibility modes are deliberately absent.
Repository files, including `configs/models.yaml`, are documentation rather than a
runtime Provider configuration source, so workspace content cannot inject endpoints,
headers, or credential references.

Connectivity diagnostics and Harness qualification publish a closed, content-free
failure reason (`none`, `not_configured`, `authentication`, `network`, `rate_limit`,
`capacity`, `model_not_found`, or `protocol_incompatible`) separately from the retry
outcome. HTTP/OpenAPI, CLI, Web, and Desktop project that safe category without
returning endpoint URLs, response bodies, raw errors, prompts, tool arguments, or keys.

Context is assembled from:

- system safety and run scope;
- agent identity and assigned work;
- compacted conversation summary;
- recent messages;
- active work items;
- selected notes/evidence.

Schema v43 puts a provenance boundary in front of every persisted Session message. `context_provenance.v1` distinguishes operator intent, model output, Go control text, workspace files/listings/diffs, tool results, and Go command results. Current rows carry a SHA-256 of the redacted content plus an explicit `instruction_authorized` bit; SQLite enforces the role/source/authority matrix and immutable content/provenance, while Go recomputes the digest on read. Legacy rows are conservatively backfilled as `context_provenance.v0`; recognizable `/read`, `/ls`, `/write`, and `/run` replies are downgraded from assistant history to tool evidence.

Model projection is separate from persistence. Trusted operator, model, and Go-control records retain their conversational roles. Every file, tool, diff, listing, command, or unclassified legacy record is rendered as a user-role `untrusted_context.v1` JSON envelope containing source kind, bounded reference, digest, `instruction_authorized=false`, and redacted content. Compaction writes provenance-preserving JSON transcript records and replays the transcript as user data, never as a fresh system instruction. Root WorkBoard/Note/inbox memory is likewise user-role untrusted context; embedded Skills and Go mode/policy contracts remain the only additional system guidance. This boundary applies to direct Session chat, AgentRunner history, and Specialist history. Read-only Fan-out already uses a separate no-tool prompt that labels file bytes as untrusted data.

This design contains authority rather than attempting to classify every malicious sentence. A README can still contain false or contradictory facts, and a model can still reason badly about them, but document text cannot acquire Go capabilities or silently become system/assistant history. Policy, scope, approvals, budgets, leases, and Tool Gateway remain authoritative even if a model follows an indirect injection semantically.

Schema v114 extends that boundary with three deliberately non-authorizing context layers.
`project_instruction_snapshot.v1` discovers bounded root-to-target `AGENTS.md`,
`CLAUDE.md`, and `.prayu` guidance, records path/hash/scope/trust/applicability and
why-effective metadata, and pins the full fingerprint to the Run. Live file drift is
read-only until an operator confirms a diff bound to both the old pinned fingerprint and the
reviewed live fingerprint, then appends a new
immutable revision. `context_memory.v1` is explicit user/project data with provenance,
retention, optimistic edit/disable, export, Secret/source rejection, and physical delete;
schema v198 adds a separate bounded queue of source-linked decision/summary Note candidates.
Only explicit operator confirmation creates formal memory; source/version checks and candidate
resolution share a SQLite transaction. Root history recall reads active memory and visible notes
within its Workspace/Thread boundaries, and each logical model call budgets and audits current
memory versions. `continuity_snapshot.v1` and immutable
root/checkpoint/fork/resume nodes capture bounded summary/message provenance, memory
references, project fingerprints, and exact Git identity. Fork/Resume creates a fresh
Mission/Run/Session and explicitly resets approvals, capabilities, credentials, network,
process, Debug/terminal/execution leases, and execution profiles. All three protocols have
closed all-false authority projections. See [ADR 0115](adr/0115-non-authorizing-durable-context-continuity.md)
and the [bilingual operating/threat guide](context-continuity.md).

Skills are versioned knowledge packages with metadata, applicability rules, prompt content, and optional tool prerequisites. The embedded `skill.v1` Registry strictly pins metadata and content identity; a bounded version index retains at most eight embedded versions per Skill, exposes only the current version for new selection, and resolves historical content only by an already-persisted exact version. Schema v39 adds one immutable, Profile-compatible, aggregate-budgeted `skill_selection.v1` per Run with digest-only operation replay. Schema v40 adds root-only `skill_context.v1`: each Supervisor turn reloads only the persisted selection, rechecks exact version/hash/bytes/Profile, redacts before independent budgeting, and injects deterministic in-memory system guidance. Schema v47 derives at most one already-pinned, surface-compatible guide for each Specialist Attempt and commits its metadata-only delivery with the first child model call. Prerequisites grant no tool capability, and the root policy remains authoritative.

ADR 0024 defines external `skill_package.v1` as strict untrusted input. Schema v69 adds a separate content-addressed user Registry: Go records an immutable installation intent before publishing the validated archive, verifies complete readback before recording completion, and represents removal only as an object-retaining tombstone. Code and Cyber catalogs are separate, Cyber accepts exactly `script`, built-in names are reserved, and an exact Run-pinned version cannot be removed. The object interface has only `Put` and `Verify`. Import does not execute or expose content, call a Provider/network/tool, or grant Run selection/context injection. External packages are therefore stored and auditable but remain unavailable to the Agent runtime until a later exact selection and minimized-load protocol. ADR 0031 records this boundary.

Schema v41 separates product behavior from permission through immutable Run modes: one fixed `code|cyber` surface and an operator-controlled `plan|deliver` phase. Schema v42 then adds strict `plan_delivery.v1` under that boundary. A fenced root Plan turn can persist exactly three bounded directions through the `agent_proposal` tool class, but cannot select or execute one. After the Run pauses and releases its lease, the operator-only selection service atomically projects one direction into existing WorkItems, backward dependency edges, a pinned decision Note, an immutable selection, and metadata-only events. Selection remains in Plan and grants no capability; phase transition is a separate schema v41 operation. HTTP, TUI, and React read the same bounded projection and have no selection route. The embedded cross-Profile `plan-delivery` Skill is subordinate guidance rather than workflow authority.

## Monetary Budget, Price Snapshots, and Qualification Status

Monetary accounting is integer micro-USD end to end. `internal/pricing` owns the
`price_snapshot.v1` wire format: operator-authored tables bounded to 64 KiB and
512 unique provider/model entries with re-computed content fingerprints, RFC3339
validity windows that must cover the import time and last at most one year, and
unknown-field rejection. Only the Go control plane can import a snapshot
(`POST /api/v1/models/prices`, `provider price-import`); a Provider response,
README, Skill, or repository file can never reach that surface. A same-content
import replays idempotently, and a new import atomically rotates the single active
table. The content fingerprint does not bind the validity window, so a
fingerprint replay with a different window is rejected and requires a new
snapshot id.

Schema v100 stores the run monetary aggregate (`run_monetary_usage`) and per-attempt
reservations (`run_monetary_reservations`). Every tracked model call follows
reserve-before-call, settle-or-release-after-call: the reserve is a conservative
upper bound from the serialized request bytes and `MaxTokens`, the settle uses
actual usage with the unused reserve released in the same CAS transaction, and
failures or terminal runs release the whole reservation. Root, Specialist, and
read-only Fan-out share one run aggregate, so `open = reserved - settled - released`
can never oversell the cap. Open reservations self-heal against terminal
`model.completed`/`model.failed` events on the next reserve or usage read and are
force-released when the run becomes terminal. The gate activates only when
`MaxCostUSD > 0`; a tracked run then requires an exact price entry (fail closed),
and a settle without a current entry conservatively charges the full reservation.

Qualification status is a durable, per-provider/model classification persisted in
`provider_setting` under `qualification_status.<provider>.<model>`: eight closed
values from `not_configured` through `available` to `protocol_mismatch`,
`auth_failed`, `network_failed`, `rate_limit`, `capacity`, and
`model_unsupported`. Diagnostics and Harness qualifications persist the latest
observation; the model availability view, diagnostics, and qualification responses
all project it, and a never-diagnosed model omits the field. Mission-level monetary
caps and vendor price feeds are future work; the run aggregate covers the current
"Mission total" budget.

## Findings and Reports

A finding is not accepted because a model stated it. Schema v35 therefore projects every Fan-out result as `draft` and labels its provenance `model_assertion`. Schema v36 lets an operator attach frozen, same-Run Artifact Evidence and make one immutable `validated` or `rejected` decision. Schema v37 keeps validation distinct from acceptance: an operator explicitly appends `accepted`, attaches fresh post-acceptance remediation Evidence, and only then appends `fixed`. No lifecycle overlay rewrites the model assertion or earlier decisions. Generic finding categories include code defect, security weakness, failed test, policy violation, and improvement opportunity.

Reports are projections built from persisted state, not mutable globals. Schema v35 provides deterministic Markdown and JSON with a stable source projection digest; schemas v36-v37 render validation, acceptance, remediation, and fix overlays without changing that digest. The read-only SARIF 2.1.0 renderer exports only confirmed unresolved `validated` and `accepted` Findings as `results`, while retaining draft/fixed/rejected counts as metadata. This stricter boundary is intentional because GitHub Code Scanning consumes only a SARIF subset and ignores `result.kind`; unconfirmed or already-fixed claims therefore cannot become alerts by parser behavior. Stable severity rules, workspace-relative escaped URIs, v35 Finding fingerprints, and separate validation/lifecycle properties support portable identity without exposing Artifact content or operator narratives. The adjacent CI gate defaults to validated/high, includes accepted unresolved items, admits drafts only through explicit `active`, and never matches fixed/rejected. Its GitHub Actions renderer consumes the exact JSON-hidden matches already selected into `GateResult`, emits escaped workflow commands, and performs no second lifecycle decision. None of these paths writes Store state or calls a Provider. Deduplication, lifecycle validation, rendering, and gate rules are Go-owned. Optional model-assisted comparison cannot become authoritative.

## Events and Interfaces

All user-facing surfaces consume normalized events:

```text
run.created
run.status_changed
run.execution_lease_acquired / run.execution_lease_taken_over / run.execution_lease_released
agent.created
agent.status_changed
agent.message
model.started / model.completed / model.failed
model.cancel_requested / model.cancel_observed
agent.schedule_started / agent.schedule_stopped
agent.delegation_proposed
skill.selection_created
skill.context_prepared / skill.context_committed
readonly_fanout.planned
readonly_fanout.execution_started / readonly_fanout.execution_recovered
readonly_fanout.shard_started / readonly_fanout.shard_completed
readonly_fanout.shard_failed / readonly_fanout.shard_cancelled
readonly_fanout.execution_completed / readonly_fanout.execution_failed / readonly_fanout.execution_cancelled
report.generated
finding.evidence_attached / finding.validation_decided
supervisor.protocol_repair_requested / supervisor.protocol_repair_started
supervisor.protocol_repair_completed / supervisor.protocol_repair_failed
model.delta (bounded, text-free stream progress)
work_item.created / work_item.changed
note.created / note.changed
tool.proposed / tool.approved / tool.completed / tool.failed
tool.budget_charged / tool.budget_exhausted
file_edit.proposed / file_edit.applied
approval.requested / approval.decided
approval.grant_created / approval.grant_revoked
finding.changed
artifact.created
standard_code.delivery_recorded
policy.decided
budget.changed
supervisor.action_committed
supervisor.run_waiting / supervisor.run_completed / supervisor.run_failed
```

CLI and `headless.v1` consume persisted events. The Headless adapter emits one bounded NDJSON `run.event` per durable sequence plus a final `stream.end`, supports numeric sequence resume and optional local SQLite follow, and maps terminal/bound/deadline outcomes to the existing stable CLI exits. It validates continuity and record bounds but never executes a Run or writes Store state. Bubble Tea polls the same local Store sequence in batches of at most 32, retains the latest 50 metadata-only events, validates contiguous Run/Mission-bound sequences, discards stale asynchronous results, and stops after a terminal Run. Each refresh brackets Session messages, ToolRuns, WorkItems, Notes, Agent nodes/completions, and bounded Finding report summaries with the durable event tail; a changing tail retries the composite snapshot up to eight times instead of displaying a torn projection. Event payloads and complete Finding/Evidence bodies are not rendered. The Events, Agents, and Findings tabs are read-only; existing Tool approval and active-call cancellation still cross the Go application service, and Bubble Tea never receives a Provider context. The Go HTTP adapter exposes persisted metadata over bounded resumable SSE. `model_public_stream.v3` is the separate Go-owned, redacted process-local text/item projection. Persisted `model.delta` retains counters plus content-free item boundaries, never model text or tool arguments.

The same Go adapter owns read projections for the bounded Agent graph, operator-gated delegation lifecycle, read-only Fan-out plans/latest execution summaries, and Finding/Report state. These are intentionally separate DTOs rather than serialized Store records. Fan-out summary queries do not select model report JSON, input/snapshot/report digests, error narratives, or lease/fencing identities; Finding views expose immutable assertion facts and Artifact descriptors but omit Artifact content and private operator narratives. React consumes only generated OpenAPI shapes. It may request the explicitly enabled Run/Session/Plan/approval/Diff/wake controls, but Go reloads authoritative state and performs every transition; TypeScript cannot supply derived authority, private leases, paths, or file bodies. A cross-surface contract test creates one real SQLite Run through the CLI and proves that CLI JSON, the TUI projection, authenticated loopback HTTP, and Headless NDJSON agree on Run, Mission, Session, status, durable event tail, and Agent count.

## Persistence

SQLite remains the local source of truth. Schema migration `v1` records the legacy baseline, `v2`-`v18` establish the Run/Supervisor/memory/tool/Artifact/lease control plane, `v19`-`v38` add bounded Agent coordination, reviewed delegation, read-only Fan-out, Findings, and operator scheduling, and `v39`-`v47` add immutable Skill selection/context, Run modes, Plan/Delivery, provenance, checkpoints, steering, and Specialist minimization. Schemas `v48`-`v63` build the still-disabled Sandbox evidence and recovery chain; `v64`-`v68` add non-authorizing execution-profile and Docker production-evidence decisions; `v69`-`v71` add the inert user Skill Registry, exact Run selection/context, and read-only provenance; `v72`-`v113` continue the audited Run, Desktop, Provider, browser, Analyzer, Docker, dependency, MCP, project-config, Git, external-Skill, and Debug-terminal control planes; `v114` adds explicit long-term memory plus immutable instruction/continuity ledgers without authority restoration; `v115` adds model-callable workspace tools and hash-guarded file mutations; `v116` adds the Run-owned ordinary command runtime and its Supervisor call ledger; `v117` adds content-addressed transactional Workspace Checkpoints; `v118` adds isolated batch delivery; `v119` adds source-bound real-browser UI-evidence attempts, steps, and artifacts; `v120` adds the reviewed MCP Client ledger; `v121` adds inert Plugin installation, publisher trust, rollback, and restricted-Hook audits; `v122` adds durable bounded scheduled monitoring and structured-diagnostics records; `v123` adds immutable advanced-Git operations/sequences plus the managed-worktree registry; `v124` adds generation-CAS GitHub connections, immutable PR/CI snapshots and local evidence graphs, plus Approval-bound remote-write recovery receipts; `v125` repairs one pinned preview migration history; `v126` adds Workspace Access; `v127` adds Run-owned Drydocks; `v128` admits fixed Docker Standard Code under Workspace Access; `v129` adds stable Thread identity and Run succession; `v130` adds item-level streamed tool reconciliation identities; `v131` binds Command Runtime jobs and advertisements to exact sandboxed/host adapter identities while projecting legacy rows as non-executable evidence; `v132` fences process-local Docker stdin attachment; and `v133` adds atomic Standard Code preset and pause-intent receipts. Non-schema D1-B1 exposes the existing v69 Registry through inert HTTP/Desktop confirmation and adds no migration. Migrations are ordered, checksummed, transactional, and safe to apply repeatedly; legacy databases are upgraded without deleting their data or fabricating new operator decisions.

Schema `v134` adds Run-scoped Web search, fetched snapshots, and citations.
Schema `v135` adds the bounded Standard Code root completion ledger; `v136` adds
durable risk escalation, grants, consumption and uncertain-execution recovery;
`v137` adds immutable Standard Code delivery truth receipts; and `v138` accepts
one exact intermediate Windows preview v136 checksum before rebuilding its two
missing Supervisor risk-authority triggers. Every migration
preserves earlier history and fabricates no completion, approval, or verification fact.

## Standard Code delivery truth gate

`standard_code_delivery.v1` is the sole Standard Code completion projection.
The Application captures a final Run-owned Drydock Checkpoint, renders one
combined committed/index/worktree/untracked/conflict Diff from the exact base,
and re-observes the Workspace before sealing. Each verification binds a durable
Command Runtime Job, terminal state and exit, tree-reaped fact, executable and
environment digests, permission and backend generation, post-command
Checkpoint revision, bounded output digests and Artifact references. Store
foreign keys and triggers align the Run/Mission/Session/Workspace/Drydock,
event sequence, final Checkpoint, Diff and receipt hashes and make the row
immutable.

The closed public states are `passed|failed|partial|not_run|blocked|stale`.
Only complete terminal success for the current revision is verified. Freshness
is re-evaluated on every read against Workspace content, Drydock generation,
permission/capability/backend bindings and Supervisor mutation epoch; a mismatch
adds a stale observation without rewriting the historical receipt. Agent prose
and CI check names do not participate in evaluation.

Desktop Delivery, CLI/HTTP, Code Handoff/export, GitHub Review and the final
Standard Code response all consume the same Go model. Relative changed files,
Artifact output and Checkpoint/Undo/Rewind/Fork are navigable. Public projection
validation excludes control characters, secrets, absolute host paths, raw
environment/output and private reasoning. Recording does not commit, push,
merge, overwrite source files, or claim that Checkpoint can reverse effects
outside the Workspace. See [Standard Code Delivery Truth Gate](standard-code-delivery.md)
and [ADR 0142](adr/0142-standard-code-delivery-truth-gate.md).

## Packaged Standard Code security matrix

`standard_code_packaged_security_evidence.v1` is the independent #181 security
slice for the #140 release gate. A byte-identical portable `TraverseBoard.exe`
executes the frozen 40-case/75-backend matrix through Tool Gateway and the
Application-owned Local/Docker Command Runtime path. Evidence binds the clean
source revision, EXE/ZIP, fixture/matrix, OS, backend identity/generation,
durable Jobs, events, Artifacts, Drydock observations, Checkpoints, cleanup and
an append-only record chain. Unavailable or unexecuted work fails closed and
never becomes a pass or Full Access fallback.

Recovery uses a mutually exclusive, fixed packaged worker rather than a
general command runner. The parent injects normal exit, force termination,
restart-equivalent, lease expiry, renderer detach and concurrent edits; a
fresh process reopens the same SQLite/Drydock state twice and verifies terminal
tree-reaped Jobs and preserved user changes. The worker and evidence are
conformance-test inputs only and cannot grant runtime or #140 release
authority. See [packaged E2E](standard-code-packaged-e2e.md) and
[ADR 0143](adr/0143-standard-code-packaged-security-matrix.md).

## Go-Owned GitHub Review Provider

ADR 0123 keeps GitHub authentication, REST versioning, pagination, sanitization, evidence mapping and write recovery behind Go. The fixed `github.com` network adapter resolves only OS credential references and returns immutable bounded evidence. React receives connection/credential status, never token material. Model tools read only evidence records already bound to their exact Code/root Run and Workspace; network fetch and write-back are not model tools. Review writes use a separate exact preview and Approval, persist `running` before I/O, and recover by observing idempotency markers without replay. Typed Git push and PR create/update retain their existing local-Git authority and receipts. See [GitHub Review Provider](github-review.md).

```text
missions
runs
run_events
run_execution_leases
work_items
work_item_dependencies
notes
note_tags
note_sources
note_evidence
tool_approvals
approval_session_grants
run_tool_usage
run_tool_calls
script_process_proposals
run_artifacts
structured_tool_operations
agent_nodes
agent_messages
agent_message_operations
agent_admission_operations
agent_attempts
agent_attempt_mutations
agent_completion_reports
agent_completion_operations
root_inbox_deliveries
specialist_model_calls
specialist_context_deliveries
specialist_protocol_repairs
specialist_schedules
specialist_schedule_agents
specialist_model_cancellations
specialist_model_cancellation_operations
specialist_delegation_proposals
specialist_delegation_assignments
specialist_delegation_operations
readonly_fanout_plans
readonly_fanout_files
readonly_fanout_shards
readonly_fanout_executions
readonly_fanout_execution_shards
readonly_fanout_model_calls
readonly_fanout_findings
finding_reports
findings
finding_evidence
finding_artifact_evidence
finding_artifact_evidence_operations
finding_validation_decisions
finding_validation_operations
sandbox_manifest_preparations
sandbox_manifest_validations
sandbox_manifest_operations
sandbox_execution_candidates
sandbox_execution_candidate_operations
sandbox_disabled_executions
sandbox_execution_inputs
sandbox_execution_leases
sandbox_execution_operations
sandbox_execution_cancellations
sandbox_execution_cancellation_operations
sandbox_cleanup_results
sandbox_cleanup_operations
agent_graph_snapshots
```

Schema v37 now stores acceptance/remediation/fix history. GitHub Actions annotations are a later read-only renderer over the same GateResult and require no schema migration; additional platform adapters remain future work.

Existing tables remain available during migration. JSON files may be exported for portability but are not authoritative state.

## Current Package Responsibilities

```text
cmd/cyberagent/             CLI entrypoint
internal/domain/            Mission, Run, AgentNode, WorkItem, Note and Finding
internal/application/       AgentRunner, SubagentRunner and use-case services
internal/coordinator/       Agent graph, inbox, scheduling, cancellation
internal/events/            Event envelope, subscriptions, projections
internal/contextmgr/        Context selection, summaries and memory policy
internal/approval/          Unified privileged-action decisions
internal/report/            Findings, evidence, report projections
internal/skills/            Skill registry and loading
internal/llm/               Provider interfaces and routing
internal/tools/             Workspace-safe file executors
internal/toolgateway/       Unified scope, policy, approval, budget, execution, and result boundary
internal/executionauth/     Operation authorization and dispatch rechecks
internal/runworktree/       Run-owned worktrees, checkpoints and recovery
internal/runmutation/       Content-free idempotency identity and fingerprints
internal/sandbox/           Backend interfaces and Docker/local runners
internal/store/             SQLite stores and migrations
internal/session/           Compatibility conversation service
internal/tui/               Bubble Tea adapter
internal/httpapi/           Loopback-only read/control API, OpenAPI contract, and bounded Run-event SSE
internal/analyzer/          Go-owned analyzer protocol, validation, and embedded WASI execution boundary
```

Packages split when a concrete implementation establishes different ownership. New tools and integrations reuse these services and add focused behavior tests for their contracts.

## Reference and Independence

The redesign was informed by the public architecture and product behavior of [usestrix/strix](https://github.com/usestrix/strix), especially its resumable run state, addressable agent graph, per-agent work memory, sandbox lifecycle, explicit completion tools, event-driven UI, skills, and structured reports.

CyberAgent Workbench does not copy Strix source code or reproduce its Python architecture. The implementation remains original Go code with stricter approval defaults, SQLite as the authoritative state store, a separate Rust analyzer boundary, and a broader generic-agent scope.

## Schema v80 Code Delivery Projections

The Code workbench now treats repository history, verification intent, verification outcome, and portable handoff as four distinct facts:

- `repository_history.v1` is a pure-Go, exact-root, first-parent-only local metadata projection. It exposes no host root, author identity, email, commit body, remote, subprocess, network, or hook behavior.
- `operator_verification_plan.v1` is immutable operator guidance with ordered checks. Schema v80 binds it to one Code Run, active Session, Workspace, event, and content digest while explicitly denying command, model-result, approval, and authority semantics.
- `operator_verification_evidence.v1` remains the separate v78 operator observation. A plan never creates or changes evidence.
- `code_handoff_export.v1` renders one stable high-water `code_handoff.v1` snapshot as digest-bound Markdown or JSON. It is a download projection, not a resume or mutation protocol.

TypeScript consumes all four through strict generated contracts and cannot bypass Go to read Git metadata, write verification state, or construct an authoritative export. ADR 0050 records the detailed bounds.

## Schema v81 Exact Commit And Explicit Verification Coverage

The Code workbench adds two more facts without merging their authority:

- `repository_commit_detail.v1` accepts one exact lowercase SHA-1 object at the exact registered Workspace root and compares its tree with the first parent. It returns only bounded canonical path/status/mode metadata. Missing objects, malformed trees, redirected metadata, and links fail closed; commit identity text, blob content, remote/root, checkout, ref mutation, subprocess, network, and hooks remain absent.
- `operator_verification_plan_evidence_association.v1` is one immutable operator association between an earlier plan item and one later evidence record. Schema v81 exact-binds every owning identity, event, operation, and digest while fixing execution, model assertion, result inference, approval, and authority false.
- `operator_verification_plan_coverage.v1` counts explicit pass/fail/unknown observations per item and leaves contradictory evidence visible. It has no aggregate result field and cannot rewrite plans or evidence.
- `runner_lifecycle_contract.v1` is a non-schema, simulation-only Go contract for wait-graph admission, start/wait/cancel/timeout, TERM/KILL grace, inspection/reaping, partial-start cleanup, and orphan cleanup. No concrete Local/Docker backend or product control path imports it yet.

React consumes the first three through strict Go/OpenAPI contracts. R1 remains internal and non-product until platform process-tree conformance and Sandbox authorization are independently accepted. ADR 0051 records the detailed bounds.

## Schema v82 Model Context And Cumulative Handoff Memory

- `model_context_window.v1` is a conservative Go planning boundary for complete root and Specialist requests. The default is 32K total with explicit safety and output reservations. Only ordinary oldest history is optional; current input, trusted control, memory, Skills, and tool schemas fail closed if they cannot fit.
- Token estimation deliberately overcounts non-ASCII UTF-8 content so CJK and emoji cannot bypass a character-oriented budget.
- `handoff_memory.v1` replaces one-shot summaries with a bounded cumulative chain. Each row binds its exact predecessor, cumulative compacted count, monotonic record ordinal, Session-message high-water, content digest, and provenance-labelled records. A crash after summary insertion but before message marking reuses the same summary instead of duplicating memory.
- Schema v82 preserves old summaries as `handoff_memory.v0`, rejects v1 update/delete/stale forks, and lets the next compaction fold legacy text as non-authoritative evidence.
- Arbitrary project documents are not automatically loaded as instructions. Selected Skills and persisted memory remain separate Go-owned context sources, and external text keeps user-role untrusted provenance.

ADR 0052 records the limits and migration behavior.

## Exact Commit Preview, Handoff Coverage, And OS Conformance

- `repository_commit_file_preview.v1` reads one exact regular/executable UTF-8 file
  from one exact commit at the registered Workspace root. Its 64 KiB input is
  secret-redacted before projection, bound to a projected-content SHA-256, and marked
  as non-authorizing evidence. Raw blobs, links, binary/oversized content, roots,
  remotes, checkout/ref mutation, processes, network, and hooks remain absent.
- `code_handoff.v1` now carries at most 100 flat verification coverage references.
  They contain plan/item digests and explicit pass/fail/unknown counts, not private
  guidance/evidence bodies or an inferred aggregate result. Contradictions remain
  visible instead of being flattened into pass or fail.
- `runner_lifecycle_contract.v1` accepts only `NonProductOnly` backends. Production
  still has deterministic simulation only. Windows Job Object and Unix process-group
  adapters exist exclusively in `_test.go` and start only the current Go test binary
  to prove cooperative termination, forced kill, and parent-first orphan cleanup.

React consumes the first two through strict Go/OpenAPI and TypeScript revalidation.
No CLI, HTTP, Desktop, Agent, Sandbox, LocalRunner, Docker, approval, profile, or
capability path can construct the OS conformance adapters. ADR 0053 records the full
boundary and evidence.

## Exact File History, Verification Drilldown, And Exit Evidence

- `repository_file_history.v1` binds one exact canonical path to one exact registered
  Workspace root and walks current HEAD first-parent history. It scans at most 512
  commits and returns at most 50 actual changes with bounded redacted subject,
  object/time, status, and previous/current mode metadata. It has no raw blob, patch,
  identity/body, remote/root, rename inference, checkout/ref mutation, process,
  network, or hook surface.
- `operator_verification_plan_item_coverage.v1` binds one exact Run, plan, and ordinal.
  It returns the item digest/counts plus at most 100 exact immutable association
  records with explicit outcomes. Guidance/evidence bodies, operator identity,
  aggregate verdicts, mutation, commands, model calls, approval, and authority are
  absent. SQLite, Go application logic, HTTP, and TypeScript each validate ownership,
  digests, counts, strict event order, uniqueness, and truncation.
- `runner_exit_evidence.v1` extends only the internal `NonProductOnly` lifecycle. Once
  the process tree is proven reaped, it may report exit code and per-stream observed
  bytes, a maximum 64 KiB captured-prefix count/SHA-256, and truncation. Raw output is
  never included; malformed evidence fails separately from orphan classification.

React uses the two read-only Code projections through strict Go/OpenAPI contracts.
R3 has no product process starter and is unreachable from CLI, HTTP, Desktop, Agent,
Sandbox profiles, approvals, LocalRunner, or Docker. ADR 0054 records the complete
limits and verification evidence.

## History Navigation, Verification Pagination, And Runtime Evidence

`repository_file_history.v1` rows are now addressable navigation facts. React may send
an already projected Workspace/object/path tuple back through the existing exact-commit
detail and redacted-preview contracts. It does not construct a new Git reader. Preview
remains limited to regular/executable content; deleted, symlink, and submodule history
rows are metadata-only.

`operator_verification_plan_item_coverage.v1` now uses the shared strict pagination
envelope. The Store reads immutable association event/ID order with a one-row lookahead;
the application layer independently compares aggregate counts and exact requested
offset/limit; HTTP binds an opaque cursor to the exact route; TypeScript validates each
page and React validates aggregate identity, latest-event high-water, uniqueness, and
strict ordering across pages. The 100,000-row starting-offset cap prevents unbounded
scans. Pagination is a live projection, not a snapshot, so detected drift fails visibly
and requires a first-page refresh.

`runner_runtime_evidence.v1` is a second internal post-reap protocol beside
`runner_exit_evidence.v1`. It carries only bounded stdin count/digest, closed and
non-inherited stdio descriptor facts, and bounded wall/parent-CPU/optional peak-resident
metadata. Raw input, environment values or names, descriptor identities or paths,
network telemetry, and product authority are structurally rejected. Exit and runtime
evidence use separate bounded collection contexts but are assigned atomically after
both validate; repeated collection must remain byte-for-byte stable.

The only concrete OS lifecycle implementations remain in `_test.go` and run the current
Go test binary. R4 adds no CLI, HTTP, Desktop, Agent, Sandbox, approval, profile,
LocalRunner, Docker, or product process-start dependency. ADR 0055 records these bounds.

## Exact Commit Comparison, Snapshot Verification, And Runner Control Evidence

`repository_commit_comparison.v1` compares two exact lowercase local commit objects in
one registered Workspace without requiring ancestry. It reuses the bounded tree-entry
collector and changed-path projector, then exposes only redacted subject/time and
canonical path/status/kind/content-change/mode-change metadata. Author identity, commit
body, blob content, patch, remote configuration, root path, rename inference, checkout,
reference mutation, process, network, hooks, and authority are structurally absent.

Exact-item `operator_verification_plan_item_coverage.v1` pagination is now a multi-
request snapshot over immutable associations. The first request freezes the latest
association event sequence and recomputes aggregate counts at that high-water. Later
requests use a descending `(event_sequence, association_id)` keyset. Their route-scoped
opaque cursor carries high-water, anchor tuple, and consumed count; SQLite recomputes
the anchor's frozen-range rank and Go requires an exact match. New associations have
higher event sequences and cannot shift prior pages. The finite 100,000-row read window
ends with `page.truncated=true` rather than an unusable continuation cursor.

`runner_resource_limit_evidence.v1` and `runner_termination_cause_evidence.v1` extend
only the internal post-reap `NonProductOnly` result. The first binds normalized wall
timeout and termination/kill grace while fixing CPU/memory limits and verified OS quotas
to false. The second binds Go's process-exit/cancel/deadline/wait-failure/orphan/partial-
start trigger to wait/terminate/kill and fixes OS-cause inference and signal identity to
false. Exit, runtime, limit, and cause evidence validate before atomic assignment.

Concrete OS adapters remain test-only and start only the current Go test binary. R5
adds no CLI, HTTP, Desktop, Agent, Sandbox, approval, profile, LocalRunner, Docker, or
product process-start dependency. ADR 0056 records these bounds.

## Comparison Preview, Verification Snapshot Export, And Runner Timeline Evidence

Comparison-side preview is a renderer composition over two existing Go contracts. The
comparison projection supplies exact base/head object IDs, path, and kind; only regular
or executable sides may call `repository_commit_file_preview.v1`. Preview selection is
Workspace/object/path-bound and independent of the currently selected detail commit.
The returned hash/path is displayed verbatim. No new Git content, revision-expression,
mutation, process, network, or hook boundary exists.

`operator_verification_plan_item_snapshot_export.v1` wraps deterministic JSON or
Markdown `operator_verification_plan_item_snapshot.v1` content. Application first reads
the current exact-item detail, which freezes the latest association event high-water
and caps references at 100. The export revalidates exact Run/Session/Workspace/plan/item
digests, outcome counts, order, truncation, and closed-authority fields, then adds the
content SHA-256, UTF-8 byte count, safe filename/MIME, and a 256 KiB cap. It contains no
generated timestamp, private body, operator identity, inferred result, durable
acceptance, mutation, approval, authority, or execution.

`runner_lifecycle_timeline_evidence.v1` is a canonical logical ordering of control
facts, not a clock. It binds the Go trigger and final wait/terminate/kill mechanism to
start, optional escalation, tree reap, exit/runtime evidence, and evidence-set commit,
while structurally excluding wall time, backend duration, and process identity.

`runner_deadline_budget_evidence.v1` inventories the independent Go context ceilings
for run, terminate/kill calls and waits, tree inspection, and exit/runtime evidence. Its
applied flags must match the path, while cumulative wall-deadline, CPU/memory limit, and
OS-enforcement claims remain false. Exit, runtime, configured-limit, termination-cause,
timeline, and deadline-budget records validate before atomic assignment; drift leaves
no partial replacement. Concrete adapters remain `_test.go` only and R6 adds no product
Runner import or starter. ADR 0057 records these bounds.

## Paired Preview, Snapshot Receipt History, And Evidence-Set Digest

The paired comparison workspace is TypeScript composition over two existing
`repository_commit_file_preview.v1` calls. Its state binds the registered Workspace,
exact base/head objects, and canonical path. Each pane repeats the Go-returned identity;
an unavailable side has no query and renders an explicit absent state. This adds no Git
protocol, raw blob/patch, revision expression, checkout, mutation, process, network,
hook, or authority.

Schema v83 adds `operator_verification_plan_item_snapshot_receipt.v1`. Application
rebuilds the deterministic snapshot from durable facts and compares the submitted
format, association-event high-water, and content digest. Store then obtains a Run
writer lock and rechecks the active Code Session/Workspace, exact plan/item digests,
current high-water, counts, and truncation before one transaction appends the event and
metadata-only receipt. SQLite binds the row to that event and current aggregate, rejects
updates/deletes, and creates no row during v82 upgrade. Exact committed intent may
replay; a changed intent or stale new snapshot fails closed.

The receipt table has no content column. Private `recorded_by` remains Store-only;
public inventory is capped at 100 and fixes content/private-body/identity inclusion,
snapshot/result acceptance, result inference, rewrite, approval, authority, and
execution to false. Recording and future review are intentionally different protocols.

`runner_evidence_set_receipt.v1` hashes a fixed, map-free, bounded canonical JSON tuple
of exit, runtime, configured-limit, termination-cause, logical-timeline, and independent-
deadline evidence. Canonical bytes are discarded. Result retains only the six protocol
versions, SHA-256, byte count, and closed semantic flags. The six records plus receipt
validate before atomic assignment, and drift leaves the old Result untouched. It claims
no wall-clock ordering, raw output, process identity, verified OS limits, or product
execution. Test-only adapters and the no-product-starter boundary remain unchanged.
ADR 0058 records these bounds.

## Paired Navigation, Receipt Review, And Golden Compatibility

Paired-preview navigation is renderer-only state over the already bounded
`repository_commit_comparison.v1` response. Candidate files must have at least one
regular or executable side. Previous/next replaces the exact Workspace/base/head/path
selection and reuses `repository_commit_file_preview.v1` for each available side.
Absent sides produce no request. The Go Repository authority, accepted object/path
syntax, redaction, byte bounds, and no-process/network/hook guarantees are unchanged.

Schema v84 adds
`operator_verification_plan_item_snapshot_receipt_review.v1` as a protocol separate
from both the v83 record-only receipt and any future result-acceptance decision. Its
closed decision set is `metadata_confirmed|metadata_disputed`. New writes require an
active Code Session, explicit non-authorizing confirmation, exact receipt ID/content
SHA-256/event sequence, and one normalized operation key. Exact committed intent may
replay before current-state checks; a different intent cannot reuse that key.

Store serializes review creation on the Run, rechecks Run/Session/Workspace/latest Code
mode/receipt chronology in one transaction, appends the event, then inserts the row.
SQLite verifies the exact receipt and event payload, permits only one review per
receipt, and rejects update/delete. Reviewer identity is private Store data. Public
inventory is bounded to 100 and fixes content/identity inclusion, snapshot/result
acceptance or inference, rewrite, approval, authority, and execution to false.
TypeScript requires exact response keys and these same false semantics before rendering.

R8 does not add a protocol or product execution path. It adds versioned golden
descriptors for two existing `runner_evidence_set_receipt.v1` canonical inputs. Windows
and Linux reconstruct the typed six-record tuple and compare exact byte count and
SHA-256 while rechecking protocol order and closed claims. Raw output, environment,
process identity, canonical runtime bytes, and a product process starter remain absent.
ADR 0059 records these bounds.

## Keyboard Preview, Handoff Review Projection, And Receipt Compatibility

D1-G12 changes only renderer interaction state. The paired exact-redacted preview is a
focusable region with plain ArrowLeft/ArrowRight navigation over the comparison response
already in memory. Escape and the close control clear the selection and restore focus
to the exact opener. Modified shortcuts, disabled bounds, unavailable sides, and paths
outside the bounded candidate set produce no request. Repository protocols and Go
authorization remain unchanged.

D1-V11 reads existing schema-v84 receipt-review rows while rebuilding Code Handoff.
The read participates in the same source-event high-water retry as all other handoff
sections. Application rejects cross-Run/Session/Workspace records, duplicate review or
receipt IDs, invalid digests/decisions, and non-descending review events. The public
aggregate contains bounded counts and at most twenty references with opaque IDs,
receipt digest, receipt/review sequences, decision, and time. Reviewer identity,
operation keys, request fingerprints, bodies, acceptance, inference, rewrite, approval,
authority, and execution are excluded. Markdown and JSON exports preserve the same
metadata-only semantics.

R9 adds an internal compatibility boundary around
`runner_evidence_set_receipt.v1`. A complete envelope is limited to 8 KiB and decoded
with exact field accounting: missing, unknown, duplicate, trailing, non-UTF-8, wrong-
type, unsupported-protocol, canonical-record mismatch, digest mismatch, and semantic
authority widening all fail closed. It recomputes the existing canonical receipt rather
than trusting imported summary fields. The decoder has no product caller, filesystem,
network, subprocess, LocalRunner, Docker, or state mutation. ADR 0060 records these
bounds.

## Exact Review Navigation, Journey Audit Facts, And Accepted Envelopes

D1-G13 adds no protocol. A Handoff review reference becomes ephemeral renderer state
containing the existing opaque IDs, digest, event sequences, decision, and time. Verify
independently resolves it through strict review and receipt inventories, then requires
the receipt's plan/item digests to match current verification coverage before expanding
or focusing a row. Any missing, truncated, stale, or drifting field fails closed without
fallback. Leaving Verify clears the target; it never enters a URL, browser storage,
SQLite, an event, or an authority decision.

D1-V12 reuses the same strict Code Handoff query and passes at most three metadata-only,
non-authorizing review facts into the presentational Code Journey component. Source
truncation remains explicit. The component owns no API client or mutation and routes a
fact through the same exact Verify matching boundary.

R10 adds accepted transport-envelope golden vectors around the existing R9 decoder.
Normal empty exit and bounded forced timeout each encode to exactly 660 bytes with a
pinned SHA-256. Strict decode, typed compatibility, and byte-identical re-encoding are
required on Linux and Windows CI. These tests are pure and internal; no product import,
subprocess, filesystem write, network, Docker, or Runner starter is introduced. ADR 0061
records these bounds.

## Go-Owned Analyzer Protocol And Rust Fixture

The following P10-A/B sections record the initial implementation stages. For the
current product execution path, see the [Analyzer README](../analyzers/README.md).

P10-A1 establishes `internal/analyzer` as a pure Go protocol owner rather than a process
launcher. `analyzer_protocol.v1` accepts one canonical inline Base64 payload plus explicit
limits and four capability declarations that must all be false. Strict decoding rejects
unknown, duplicate, missing, trailing, invalid UTF-8, future, malformed, oversized, and
authority-widening input. Go also owns the bounded metadata-only `analyzer_result.v1`, the
stable `analyzer_error.v1`, exit-code mapping, safe identifiers, media types, and JSON depth.

P10-A2 adds a Rust 1.97.1 fixture behind that contract. It reads only bounded stdin, writes
only one bounded stdout envelope, and computes content byte count, SHA-256, UTF-8 status,
and line count. The wire has no filesystem path, URL, command, environment, key, Provider,
Run, Session, network, persistence, or tool authority. Source bytes are never emitted.
`clap`, `serde`, `serde_json`, `base64`, and `sha2` are locked in `Cargo.lock`.

P10-A3 stores one shared versioned vector document under `analyzers/testdata`. Go and Rust
load it independently and must agree on every version, limit, exit code, error code, exact
JSON output, byte count, and SHA-256; neither implementation shells out to the other. The
Rust CI job runs fmt, locked tests, and clippy with warnings denied. No Registry, executable
path, product process adapter, file reader, Run/Event/SQLite writer, result persistence, or
Artifact commit exists. ADR 0062 records the complete boundary.

P10-B1 adds a fixed `analyzer_descriptor.v1` Registry owned by Go. It exposes only sorted
list and clone-isolated lookup for the digest and ZIP analyzers. All capability and
product-authority values are false, and there is no dynamic registration, executable,
command, path, URL, or starter field.

P10-B2 adds `archive.inventory.v1` as a bounded pure function. Go parses only a maximum-
64-KiB inline ZIP central directory and never opens entry data. Entry count/name bytes,
declared sizes, integer compression ratios, result size, path risks, and false semantic
claims are fixed and independently recomputed during strict result decoding. No declared
archive size is used for allocation or extraction.

P10-B3 pins `rawzip 0.5.1` for the Rust equivalent. It iterates only in-memory central
records, has no filesystem/process/network API, and is checked against the same five exact
ZIP/result byte-and-SHA vectors as Go. The Registry and pure functions are still not a
product bridge: CLI, HTTP, Desktop, Tool Gateway, Runner, Run/Event/SQLite, persistence,
and Artifact flows cannot invoke them. ADR 0063 records these bounds.

## Browser Publisher Acceptance, Launch Lease, And Review

P11-C1 extends fixed-location browser discovery with a same-open-handle acceptance boundary.
The exact file is opened read-only, bounded bytes and PE architecture are verified, SHA-256 and
file identity are bound, and Windows Authenticode runs cache-only with no UI against that same
handle. Chrome accepts only `Google LLC` or legacy `Google Inc`; Edge accepts only
`Microsoft Corporation`. Chromium remains unsupported because arbitrary distributions do not
share a fixed publisher policy. All identities are revalidated before the handle closes.
`accepted_for_review` is not complete launch trust, and no revocation/timestamp freshness is
claimed without a network-backed policy.

P11-C2 adds schema v85 immutable `browser_launch_attempt.v1`,
`browser_launch_lease.v1`, preparation-operation, and bounded event records. An attempt
fingerprint binds the exact Session, Run, Workspace, accepted executable, disposable-profile
owner/generation, scope, budgets, backend, and process-tree contract. Generation fencing rejects
stale workers. Cancellation, restart observation, reconciliation, termination, and cleanup are
currently implemented only by package-sealed Disabled/Fake lifecycle adapters and therefore
cannot start or control a process.

P11-C3 adds one immutable `browser_launch_review.v1` while the exact lease is active. The
reviewer must be independent from the lease owner, the full attempt fingerprint is recomputed,
and replay is digest-idempotent. Raw operation keys, owner identities, and reviewer identities are
not persisted. Accepted review is only an eligibility fact for a future adapter: process,
network, profile-write, termination, cleanup, CDP, and Artifact authority remain false. No CLI,
HTTP, Desktop, model, Skill, Tool, or Runner surface consumes these facts to start a browser.
ADR 0073 records these bounds.

## Canonical Shell Review And Supervised Debug Terminal

ADR 0114 extends the Code execution boundary without introducing a general
always-on model Shell. Approval-mode `host_command_propose` selects either the
existing exact process transport or a canonical one-line PowerShell/Git Bash
transport. Go resolves and hashes the interpreter, freezes the no-profile argv,
and reuses the immutable proposal, independent review, write-ahead intent, Windows
Job Object, and metadata receipt chain. A model cannot select the executable,
approve the proposal, provide environment values, persist the process, or retry an
uncertain execution.

The separate Debug path starts with a user-owned terminal. Desktop can request a
15-second-to-15-minute process-local input lease, but the bearer never crosses the
Go boundary. Only a root Code/Deliver Supervisor with the current Debug permission
and installed runtime adapter sees `debug_terminal`. Schema v113 admits that tool to
the durable Supervisor call ledger with a data-preserving table migration. Each input rechecks all durable
bindings and host Policy; command writes also pass Shell Policy. The fixed `interrupt`
action sends only Ctrl+C through the same lease, audit, deduplication and native authorization.
Each read is cursor-addressed, bounded, and clamped to the
output watermark captured when the lease was granted. Canonical model commands and
sanitized, explicitly untrusted post-grant results are durable Supervisor evidence, while user input,
raw PTY bytes, process environment, root paths, and bearer tokens stay in memory. A
process-local root digest and exact mode revision revoke bindings on Workspace or
phase drift. Windows uses
ConPTY plus a creation-time Job Object; macOS/POSIX uses Bash PTY plus owned process-
group cleanup. Cyber and Plan do not expose the tool.

## Run-Owned Ordinary Command Runtime

ADR 0117 adds `command-runtime.v2` as a fourth, separately owned execution path.
Schema v131 keeps that one model schema and Job state machine while splitting its
authority into exact `sandboxed_workspace` and `host_unsandboxed` adapters. The
former accepts only Code/Deliver/root `workspace_access` with ready Local or fixed
Docker isolation and an exact Drydock; the latter accepts Code/Local/Deliver
`full_access` or its strict superset `debug`, a live execution lease, and the process-local danger-full-access
capability. It is neither the user terminal nor its Debug input lease and neither
an approval proposal nor a model-selectable Docker/host switch. The Gateway
validates an action-tagged union, adapter authority, and ordinary Policy on each
exact command. cwd, restricted env, stdin, timeout, output, `network=disabled`, and
`credentials=none` are mandatory contract fields; executable, environment, and
canonical Workspace/Drydock root are hashed.

Foreground requests run one-to-four commands sequentially under explicit
fail-fast/continue semantics. Background Jobs have a monotonic interleaved
stdout/stderr cursor, bounded stdin writes, wait/cancel/kill, a byte-and-frame-bounded
inline ring, terminal hashes, and bounded Artifact capture. Output is sanitized by
a stateful chunk decoder before storage and again at the model boundary. The
projection always marks it as untrusted and carries the exact adapter, backend
generation, isolation grade, and effective network/credential policy. All three
adapters retain the bounded stdin protocol. Windows Local forwards the manager-owned
pipe through the AppContainer handle list; fixed Docker binds the pipe policy into
runner v2 and records a metadata-only, lease-fenced `attach_stdin` action in schema
v132. No sandbox input bytes or handles survive restart.

Schema v116 stores a write-ahead immutable launch intent fenced by the starting
Supervisor generation. Live handles then belong to a distinct random process owner
and generation whose heartbeat expires after 15 seconds. Releasing the turn lease
does not kill the Job, so a later turn in the same host process can continue it;
a second process cannot adopt it. Desktop/API keep that host alive across client
disconnects; opt-in CLI `run step/execute` owns Jobs only until invocation exit.
Durable root/mode/profile/permission/root-hash
drift does kill it. Windows assigns a kill-on-close Job Object in the creation call.
POSIX uses an owned process group, a fixed parent-pipe guardian, and Linux parent-
death signaling. Restart waits out a still-live owner heartbeat and then records
`interrupted`; it never executes the stored intent or signals a durable PID. Schema
v131 makes pre-v131 Jobs readable only through the non-executable `legacy_unbound`
projection and stores no host PID/process group for sandbox Jobs. A
deliberately new POSIX session can escape the inherited process group and remains
an explicit unsandboxed Full Access risk, including when inherited by Debug, rather
than a safely adoptable Job.

For `host_unsandboxed`, the network declaration is an intent and policy boundary,
not a portable packet-containment claim. Profile/helper/proxy/credential environment paths and common
explicit network commands are denied, and any Policy result requiring approval is
routed away from this automatic tool. Because `full_access` and `debug` are still
unsandboxed host execution, their receipts report host network and credentials as available.
Network or credential use requires a separate exact review. Sandboxed receipts
report denial/no-credentials only from current Local or Docker isolation readiness.
The complete split is documented in
[Command Runtime adapter split](architecture/command-runtime-adapter-split.md).

## Durable Exact Risk Escalation

Schema v136 adds `risk_escalation.v1` as a separate Workspace Access path for an
exceptional network target/purpose, credential kind, exact host path, Policy refusal,
non-whitelisted tool, or other bounded high-risk request. It extends the existing
immutable host-command proposal and Approval/Grant ledgers rather than widening the
ordinary Command Runtime or creating a second decision store. The proposal binds the
exact executable, argv, cwd, environment-name digest, resource budget, normalized risk
scope, Run/Session/Workspace, Supervisor turn/call/invocation, all execution snapshots,
Workspace-root fingerprint, and capability generation.

Only an operator may deny, approve once, or create an exact current-Run grant bounded
to 1-900 seconds and 1-8 uses. A grant is metadata rather than a bearer; every use is an
immutable consumption record, and model, Skill, MCP, repository content, or another Run
cannot consume it. While review is pending, `AgentRunner` persists the original call,
sets `waiting_approval`, and releases its lease. Decision handling resumes only that
same call. A write-ahead intent precedes host start; if no terminal result follows, the
operation becomes permanently uncertain and is never retried. Restart restores records,
not process-local authority. Permission, mode, Profile, Workspace/root, executable, or
capability drift invalidates the proposal and related active grant. See
[Durable risk escalation](risk-escalation.md) and
[ADR 0140](adr/0140-durable-risk-escalation.md).

## Transactional Workspace Checkpoints

ADR 0118 and schema v117 add `workspace-checkpoint.v1` as a storage and Application
boundary below all product surfaces. A checkpoint binds one Run/Workspace/root
fingerprint to its base commit, branch, exact raw Git index, deterministically ordered
tracked/untracked manifest, source receipt, attempt/capability generation, and recovery
grade. File and index content is SHA-256 addressed and deduplicated; SQLite sealing
triggers make referenced blobs immutable and maintain references atomically. Per-entry,
per-index, per-checkpoint, preview/conflict, and global-store ceilings are fixed in Go,
with the 2-GiB store bound also enforced by SQLite so concurrent writers cannot bypass
it. Ignored, generated, large, sensitive-looking, linked/reparse, special, unreadable,
and external content is explicit evidence, not an implicit recovery promise.

FileEdit/model Workspace writes, Run-owned foreground batches/background Jobs, and
typed Git writes open and close one operation-keyed transaction around the logical
mutation. The same public boundary is available to the agent-merge writer. Manifest,
Git-status/index, receipt, invocation, attempt, capability-generation, and lease facts
provide attribution. Since no portable filesystem watcher is installed, Shell-derived
checkpoints deliberately remain `partial`; effects outside the canonical root are never
claimed as reversible.

Restore is an append-only transaction under current authority. Preview compares the
reviewed cursor, historical target, and a fresh live capture. Path/index/root/branch/
commit/case/link drift conflicts before any write. Confirmed Undo/Redo/Rewind requires
a paused Code/Deliver Run, active Session, no current execution lease, exact process
capability, non-conservative permission, explicit operator, expected cursor, and stable
operation key. Application uses root-confined atomic file writes plus exact raw-index
replacement, never `git reset --hard` or blanket untracked cleanup. Terminal capture
must exactly verify the target before the CAS cursor advances.

Fork creates and verifies a distinct Git worktree and branch, then atomically registers
a new Workspace/Mission/Run/Session/continuity node. It copies file/index state but no
approval, credential, capability, execution lease, process, terminal, or network grant;
the source history and cursor stay immutable. CLI may provide an explicit operator path;
HTTP/Desktop never accept one and instead use a Go-derived sibling keyed by the operation
digest, with no root path in the response. Prepared operations reconcile on startup:
ordinary interrupted boundaries close with explicit partial evidence, restores resume
only under the same live identity, and a registered Fork finishes its existing Run
instead of creating a duplicate. Desktop, CLI, and OpenAPI are thin projections over
this single service. Operational details are in [Workspace Checkpoints](workspace-checkpoints.md).

## Go-Owned Advanced Git Lifecycle

ADR 0122 and schema v123 add `git-advanced.v1` beside the existing whole-file local Git and
network-scoped remote Git services. The protocol is a strict union of hunk, stash,
rebase/cherry-pick/bisect, and managed-worktree operations. It contains exact object IDs,
stable hunk IDs, bounded recipe choices, and safe logical names, but no raw argv, shell text,
ref expression, host path, executable, environment, or arbitrary validation command.

`repository.AdvancedExecutor` owns all command templates. Its preview binding covers the
canonical repository and common Git directory, HEAD/branch, raw index, worktree/status,
stash, sequencer, upstream, and object format. Hunk identity additionally includes base/index
blobs, whole-file worktree content, context, and patch. Every Git invocation uses literal
pathspecs and a replaced environment; executable repository-local filter/diff/merge drivers,
hooks, helpers, prompts, editors, pagers, external diff, fsmonitor, system attributes, and
LFS smudge are disabled or rejected. Binary/combined hunks, symlink/submodule paths, ambiguous
index stages, detached/protected history changes, shared-upstream rebase, and force-style
operations are outside the closed contract.

Application review binds the executor preview to current permission revision, private
Workspace lease, and process-random capability generation, then stores immutable spec/preview
JSON and a one-time `git.advanced` Approval. Execute re-renders the repository, revalidates
durable sequence/worktree targets, opens a Workspace Checkpoint boundary, and CAS-transitions
`proposed -> running`; only that winner calls Git. Receipt validation, terminal immutability,
append-only events, and idempotency prevent a second caller or restart from duplicating the
mutation.

Rebase, cherry-pick, and bisect use a separate generation-fenced durable sequence containing
original HEAD/branch, exact targets, current HEAD, sequencer digest, and base/ours/theirs
conflicts. Continue/skip/abort/mark/reset requires the same live digest. Bisect automation is
limited to fixed Go/npm recipes with step/timeout budgets, an offline stripped environment,
closed stdin, and mandatory whole-process-tree reap evidence. Protected original branches may
be restored by bisect reset, but cannot be selected for rebase/cherry-pick start.

Managed worktree paths are derived below one startup-configured real root from common-dir
identity and a safe name. Registry rows bind path digest, source repository/common-dir,
branch/commit, Run/Workspace, and creating operation; names are never reused. Parent and target
symlink/junction identity, Git registration, stored/live HEAD and branch, lock, and tracked plus
untracked cleanliness are checked before removal. Prune accepts only missing administrative
entries that are already in the product registry. Public projections blank host paths and
private lease IDs.

Startup reconciliation observes every `running` operation and never invokes Git. A matching
active sequence is persisted as interrupted/conflicted for a fresh explicit control action. A
provably exact created worktree may be recovered into the registry, while its old operation
remains failed/interrupted so uncertain execution is never represented as success. Desktop,
CLI, and authenticated OpenAPI are projections over this one service. See
[Advanced Git Workflows](git-advanced.md) for operations and residual recovery limits.

## Deliverable Batch Agents

ADR 0119 and schema v118 add `batch-delivery.v1` beside, rather than inside,
the existing no-tool Specialist runtime. Preparation consumes only an approved and
admitted core `child_task_proposal.v1`; Agent identities, ordinal/DAG, budgets, and
expected artifacts must exactly match admission. A clean branch-backed source HEAD is
frozen as the batch base, then each of at most two children receives a separate
branch/worktree, generation lease, digest-only owner-token record, and the exact closed
`batch-delivery-tools.v1` profile. Ordinary projections omit worktree/integration roots,
owner-token digests, and operation fingerprints. Plaintext authority is returned once
at preparation or exact-generation rotation and is never reconstructed after restart.

The child profile permits only owned-scope list/read/glob/grep, reviewed create/replace
application, fixed Git status/diff, and a fixed local commit. It structurally denies
delete/rename, Shell, arbitrary process, network, credentials, Debug, approval, and
recursive delegation. Every call rebinds Agent, plan, generation, token digest, lease,
dependency state, root/branch/base identity, tool-profile fingerprint, and normalized
file/directory ownership. Cross-task ownership overlap rejects the plan before any
worktree is created.

Mailbox facts are generation-scoped, monotonic, immutable, and operation-keyed.
Commit uses a write-ahead intent: after a crash, recovery accepts only one clean direct
non-merge commit from the recorded prior HEAD, with fixed author/message and no
delete/rename/copy. Submit and independent review each recompute the full merge-base
diff, function-context call-chain digest, changed paths, and declared validations, then
re-attest the exact branch/HEAD/diff/clean state after validation. A receipt binds
base/head, diff/call-chain hashes, diffstat, test-output hashes, evidence, and limitations;
dirty or post-validation-drifted worktrees cannot submit. Acceptance requires explicit
reviewer attestation to the complete diff, call chain, and tests.

An ordered queue creates another isolated integration worktree at the latest explicitly
confirmed source base. Changed-file overlap blocks before merge; base drift requires a
separate replay confirmation. Each DAG-valid step applies one accepted head, persists
pre/post HEAD, reruns cumulative validation for the complete merged prefix, and re-attests
the exact source, deterministic merge, integration, and every child receipt. State drift
blocks and preserves evidence; an exactly attested text or semantic/test failure rolls
back only that integration step, leaving prior steps and every child worktree untouched.
Recovery accepts only the expected two-parent merge tree/metadata, not an arbitrary
descendant. Completion produces a local integration branch/head only; it does not mutate
the source branch, push, open a PR, or merge a remote.

Only fixed `git diff --check` is enabled by default. Go/npm validation executes
child-authored code and therefore requires permission control, danger-full-access, the
distinct process-start flag `--enable-batch-validation-execution`, and a currently running
Run whose live permission remains `full_access` or the explicitly higher `debug` mode. Desktop mutation separately requires
`--enable-batch-delivery-control`. Fixed argv, cache-bypassed Go tests, Windows Job /
Unix inherited-process-group termination, offline environment, temporary HOME/cache,
credential stripping, timeout, and complete-stream digest-only output reduce exposure
but do not constitute an OS filesystem, network, or POSIX daemon-containment sandbox.

Cancellation fences generations before cleanup and removes only an exact clean
worktree. Dirty, committed, missing, or identity-drifted evidence is preserved as a
branch or `orphaned` directory. Startup reconciliation converges durable materialization,
commit, expiry, and merge intents without restoring tokens, processes, or authority; a
non-running Run is reported for operator attention before any filesystem or Git effect.
Operational details are in [Deliverable Multi-Agent Batches](batch-delivery.md).

## Source-Bound Real-Browser UI Evidence

ADR 0120 and schema v119 add `ui-evidence.v1` above the v116 command runtime and
the existing Safe Web lifecycle. The immutable manifest is the join point between
one Run/Mission/Session/Workspace, a fresh source checkpoint, optional build and
required start `command-runtime.v2` recipes, readiness, an executable/version/hash-
pinned browser, literal loopback URL and route, viewport/DPR, locale/theme/reduced
motion, deterministic fixture/seed/page state, ordered steps, masks, and a mandatory
fail-closed diagnostic policy. Source identity is captured before persistence and
revalidated before build, after readiness, and immediately before pass. Build or
application mutation of tracked/untracked/index/commit/branch/root identity therefore
cannot be hidden by a successful screenshot.

The lifecycle is `not_run -> running -> passed|failed|cancelled|timed_out`; startup
reconciliation maps a stranded `running` row to `interrupted/cleanup` without reviving
authority. `not_run` never satisfies `Passed()`. Stable failure stages are build,
launch, readiness, navigation, selector, assertion, console, network, capture, and
cleanup. Every terminal pass requires zero console/page/request/HTTP failures and a
complete browser-tree/application-tree/Profile/network/port cleanup receipt. SQLite
seals the manifest, append-only step receipts, and content-addressed artifacts while
enforcing 32-MiB artifact, 128-MiB attempt, and 2-GiB store limits.

Execution is scoped ownership, not browser adoption. The Application service probes
the exact readiness port before start and refuses an existing listener. It runs the
reviewed application through the Run-owned command manager, launches a newly verified
fixed-location Edge/Chrome executable in an attempt-private Profile, and binds the
browser tree at creation to Safe Web network containment and a kill-on-close Job.
Cancellation and service shutdown use a context independent of the HTTP request and
wait for owned cleanup. A restart may reconcile SQLite state but never adopts a PID,
port, browser, Profile, cookie store, or old capability.

`restricted-cdp-ui-evidence.v1` is a separate method allowlist for viewport/emulation,
navigation, click/type through fixed DOM methods, selector assertions, screenshot,
DOM/accessibility, performance, and bounded diagnostics. It excludes
`Runtime.evaluate`, cookies, credentials, response bodies, request mutation/replay,
and `Fetch.fulfillRequest`; all observed page data stays untrusted and non-authorizing.
Network evidence retains only bounded redacted origin/path metadata, method, resource
type, status, MIME, and failure summaries. Screenshots require every declared dynamic
mask to resolve before capture.

Windows Desktop installs a read-only service unconditionally and upgrades it to the
execution service only when Run execution, permission control, danger-full-access,
restricted browser CDP, and the dedicated UI-evidence process gate all hold. React and
OpenAPI are projections over the same Application service; the CLI deliberately offers
only list/show/hash-verified exclusive export. CI exercises the production restricted
driver against a deterministic loopback fixture with desktop/mobile, theme, locale,
and reduced-motion cells and writes a source/browser/artifact receipt. Its deliberate
missing-click-handler route establishes that real interaction evidence catches a
regression that source/build checks alone do not prove. Operational details are in
[Real-browser UI Evidence](ui-evidence.md).

## Provider-Neutral Item Streaming

ADR 0133 and schema v130 add `llm.item_stream.v1` between provider wire adapters and
the Supervisor. OpenAI interleaved calls, Anthropic content blocks, and complete-item
Ollama/Mock/legacy streams share one ordered response/item/call state machine. The
Application replaces upstream IDs with stable attempt-owned IDs; tool deltas carry no
authority, and only Go may record execution start/completion after a complete call has
passed validation. Text retains 2 KiB/250 ms aggregation, while content-free item
boundaries are persisted independently so aggregation cannot lose lifecycle state.

`model_public_stream.v3` exposes only stable IDs, item status, tool name, and argument
byte count. Raw text/argument event fields cannot be JSON-marshaled, and neither public
nor `model.delta` shapes can represent private reasoning, credentials, raw wire data,
or tool arguments. Schema v130 immutably binds provisional response/item/call IDs to
the deterministic Supervisor call ledger; old Session messages are projected as
durable completed items without rewriting history. Cancellation, EOF, and provider
errors end in failed/cancelled items and never synthesize successful completion.
