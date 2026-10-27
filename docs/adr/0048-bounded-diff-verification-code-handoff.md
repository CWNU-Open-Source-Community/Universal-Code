# ADR 0048: Bounded Repository Diff, Immutable Verification, And Code Handoff

- Status: Accepted
- Date: 2026-07-19
- Scope: D1-G2, schema v78 / D1-V1, and D1-F2

## Context

The Code surface already exposed exact-root repository status, independent FileEdit
review/apply, a five-stage Journey, operator actions, attached evidence, and durable
Run state. It still lacked the last three read/review surfaces needed for an ordinary
handoff: a bounded patch view of uncommitted repository changes, an operator-owned
record of what was actually checked, and one regenerable summary of the durable state
another operator or resumed process should inspect.

These conveniences must not turn repository text, a verification claim, or a summary
endpoint into execution authority. Go remains the sole control plane; TypeScript can
render or submit one explicitly enabled verification observation but cannot execute a
check, approve work, resume a Run, mutate a repository, or create a composite action.

## Decision

### D1-G2: repository Diff is bounded evidence

The limits and pagination/statistics behavior below are superseded by
[ADR 0194](0194-bounded-repository-diff-pagination.md); the read-only authority boundary remains.

`repository_diff.v1` uses `go-git/v5` and the existing Workspace explorer boundary at
the exact registered root. It follows no links, performs no parent discovery, starts
no process, reads no remote, executes no hook, and performs no network request. It
returns at most 50 changed paths, at most 64 KiB of patch text per item, and at most
512 KiB in aggregate. Inputs are valid bounded text or a closed
`binary_or_unsupported|size_limited|linked|unavailable` state.

Both the HEAD and Workspace sides pass through secret redaction before a unified patch
is formed. Paths remain canonical relative references and the DTO excludes host roots,
raw unredacted bodies, remote configuration, command output, and mutation controls.
Per-item truncation propagates to the top-level truncation fact. The projection fixes
read-only true and instruction, mutation, authority, process, network, and hook facts
false.

### D1-V1: verification is an immutable operator observation

Schema v78 adds `operator_verification_evidence.v1`. A separately enabled control
accepts only one exact Run, a closed `pass|fail|unknown` outcome, a normalized title
and summary, a memory-held idempotency key, and the authenticated operator identity.
Go redacts text and exact-binds the Run, Mission, active Session, and registered
Workspace before the Store transaction.

The Store rechecks the Session as active inside that transaction, commits one
metadata-bound `verification.evidence_recorded` event, and inserts one immutable row.
SQLite also requires the exact Run/Session/Workspace/event binding and fixes
`command_executed`, `model_assertion`, `approval`, and `authority_granted` to false.
Inventory is newest-first and capped at 100 items. Migration from v77 creates no
evidence, operation, event, or inferred result.

An operator observation is evidence, not proof that a command ran. It cannot satisfy a
Policy decision, authorize a tool, resume execution, change a Run, or turn model output
into a verified fact.

### D1-F2: handoff is a regenerable snapshot, not an action

`code_handoff.v1` is Code-only and exact-binds the Run, Mission, Session, Workspace,
and current mode. It compacts durable Plan/WorkItem counts, steering queue counts,
FileEdit change-set counts, verification counts and bounded references, pending action
references, and bounded Finding report references. It omits messages, verification
summaries, Diffs, file content, report bodies, operation keys, requesters, leases, and
capabilities.

Go reads the Run-event high-water mark before and after assembly and retries at most
four times. A continuously changing Run returns a conflict instead of a torn summary.
The response fixes `regenerable` and `durable_sources` true while
`private_bodies_included`, `composite_mutation`, `resume_authorized`, and
`execution_started` remain false. There is no handoff write or apply endpoint.

## Consequences

- SQLite advances from v77 to v78 solely for immutable verification evidence.
- Repository Diff and Code handoff are read-only GET projections; verification POST is
  a narrow independent capability behind the distinct control token.
- React adds Repository, Verify, and Handoff views but retains no host path, key,
  private body, Policy, process, or composite authority.
- Verification claims remain visibly distinct from model assertions, command receipts,
  approvals, and execution results.
- Real verification commands, xterm, LocalRunner, Docker process execution, signed
  distribution, Rust analyzers, and CTF automation remain separately gated.

## Verification

Implementation commit `cff7489` passed the final uncached ordinary Go suite in 308.1s;
the Store package completed in 299.8s. Desktop-tag tests, `go vet`, targeted
zero-warning staticcheck, module verification/tidy diff, deterministic OpenAPI and
TypeScript generation, strict TypeScript, 120 React tests across 35 files, the Vite
production build, and a zero-vulnerability npm audit are green. OpenAPI contains 62
paths, 67 operations, and 143 schemas with SHA-256
`652707A6D9CA72EBBD86B6FD407A382DFBE85B094927C82AFC3765D2648332B3`.

A reproducible Windows double build produced unsigned Desktop SHA-256
`2ab74a47794287bac71877172136f02631b5cc9a44febd930e8ee7b1913ba93f`.
Automated compatibility checks passed while the uncompleted Windows 10/WebView2
manual matrix correctly keeps `release_ready=false`. Production-bundle browser checks
recorded one isolated pass observation, confirmed the Handoff update, exercised the
non-Git empty state, found no page-level horizontal overflow at desktop/mobile widths,
and produced no console error.

The combined audit closed an active-Session validation/transaction race, a torn
multi-source Handoff snapshot, top-level Diff truncation under-reporting, strict-client
reference/action/truncation gaps, CR normalization drift, and a missing React
capability propagation path. No unresolved high- or medium-severity issue is known.
The batch used no real API key, Provider request, Shell, LocalRunner, Docker, Git hook,
attack traffic, installer, registry write, startup task, or external network request.

This is the first three-slice batch of a new six-slice cycle. The complete local
race/staticcheck/govulncheck robustness gate remains scheduled after the next batch.
GitHub Actions run 29682547524 passed implementation commit `cff7489`: TypeScript
console 42s, Windows Desktop shell 2m34s, and Go control plane including vet and
govulncheck 3m33s.

## 中文结论

D1-G2 把精确 Workspace 根目录的未提交变更投影为有界、脱敏、只读 Diff，不调用
`git` 进程、网络、remote 或 hook，也不返回宿主路径或原始未脱敏正文。schema v78 /
D1-V1 把操作者的 `pass|fail|unknown` 观察记录成不可变证据，但明确不是命令回执、模型
断言、审批或权限。D1-F2 从持久化 Plan、队列、Change Set、验证、待办行动和报告引用
重建 Code Handoff，并用事件高水位重试拒绝撕裂快照。三者都没有复合执行或恢复授权，
Go 继续是唯一控制平面。
