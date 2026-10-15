# 高级 Git 工作流 / Advanced Git Workflows

Current execution uses Ask / Auto / Full. A confirmed mutation for a Full Run
also needs `--enable-permission-control --enable-danger-full-access --confirm-full`
in the same CLI invocation. The existing `--confirm` and exact approval checks
remain required. Full activation ends when the command returns; read-only and
receipt recovery operations do not activate it.

[中文 README](../README.md) | [English README](../README.en.md) | [HTTP API](http-api.md) | [ADR 0122](adr/0122-go-owned-advanced-git-lifecycle.md)

Schema v123 introduces the default-off `git-advanced.v1` control plane for exact hunk
operations, stash inspection and mutation, rebase/cherry-pick/bisect state machines, and
product-managed worktrees. It is a closed set of Go-owned Git templates. It is not a
generic Git or shell escape hatch.

Schema v123 新增默认关闭的 `git-advanced.v1` 控制面，覆盖精确 hunk、stash、
rebase/cherry-pick/bisect 状态机与产品管理的 worktree。它只执行 Go 拥有的封闭 Git
模板，不提供通用 Git argv 或 Shell 逃生口。

## Enablement and authority / 启用与授权

Advanced Git is available only when all of these independent gates are true:

- the process starts with `--enable-git-advanced`; the managed worktree root is either an
  explicit operator-selected path or the product-owned directory below `CYBERAGENT_HOME`;
- permission control, operator approval, approval control, and Workspace Checkpoint
  control are enabled in the same process;
- the Run is active in `Code/Deliver`, uses Local, Docker Engine, or Docker Sandboxes
  with network disabled, and
  holds a current non-conservative execution permission plus active Workspace lease;
- a preview was rendered against the current process capability generation, and an
  operator approved that exact approval fingerprint once.

高级 Git 只有在以下独立门禁同时成立时才可用：进程显式启用 capability，受管
worktree 根由操作者指定或使用 `CYBERAGENT_HOME` 下的产品目录；权限控制、操作者审批、
Approval 控制和 Workspace Checkpoint 控制全部开启；
Run 处于 `Code/Deliver`，选择 Local、Docker Engine 或 Docker Sandboxes，禁网并持有当前非 conservative 权限和 active lease；
最后还必须针对当前进程 generation 渲染 preview，并由操作者一次性批准它的精确指纹。

API/Desktop hosts reject an inconsistent partial configuration at composition time. CLI
commands additionally require `--enable-git-advanced --enable-permission-control`; the
danger/full-access startup switch must match the Run's existing permission tier. No row in
SQLite can re-enable a capability after process restart.

## Contract and lifecycle / 合同与生命周期

Every request uses `git-advanced.v1`. The union is strict: fields belonging to a different
operation are rejected even when they would otherwise be ignored. Paths are normalized
workspace-relative values and are always passed with Git's literal-pathspec mode. Commits
and stash targets are full lowercase SHA-1 or SHA-256 object IDs; branch and worktree names
use closed validation. Raw argv, ref expressions, host paths, environment, hooks, helpers,
merge drivers, and test-command arguments are absent from the public contract.

每个 mutation follows the same durable flow:

1. read-only discovery/preview captures repository/common-dir identity, exact HEAD and
   branch, raw index digest, worktree/status/stash/sequencer digests, configured upstream,
   capability generation, and the bounded impact graph;
2. review adds current permission revision and private lease identity, creates an immutable
   operation record, and creates a pending one-time `git.advanced` Approval;
3. execution re-renders the preview, rechecks every repository and authority binding,
   verifies durable sequence/worktree state, opens a Workspace Checkpoint boundary, and
   wins a compare-and-swap transition from `proposed` to `running`; schema v123 also permits
   only one `running` advanced operation per exact Git common directory, including across
   different Runs and linked worktrees;
4. only the CAS winner invokes Git; completion stores a typed receipt, sequence/worktree
   transition, append-only Run events, and the after-checkpoint boundary;
5. retries replay a terminal receipt. A `running` record is never executed again.

The public projection returns parsed preview/receipt evidence and non-secret lease and
capability generations. It never returns the raw persisted JSON, lease ID, managed host
path, command line, environment, or process identity.

## Operation matrix / 操作矩阵

| Family | Operations | Required exact input | Default recovery/fence |
|---|---|---|---|
| Hunk | `hunk_stage`, `hunk_unstage`, `hunk_revert` | selected SHA-256 hunk IDs; optional literal paths | full binding recheck; revert is destructive; checkpoint before every write |
| Stash | `stash_create`, `stash_apply`, `stash_pop`, `stash_drop` | audit message or exact stash object ID | tracked/index/untracked roles; ignored always excluded; apply/pop reject ignored-path collisions; pop retains stash on conflict |
| Rebase | `rebase_start`, `rebase_continue`, `rebase_skip`, `rebase_abort` | exact upstream/onto or durable sequence ID | clean attached local branch; protected/shared branch rewrite denied |
| Cherry-pick | `cherry_pick_start`, `cherry_pick_continue`, `cherry_pick_skip`, `cherry_pick_abort` | ordered exact single-parent commits or durable sequence ID | clean attached non-protected branch; conflict state persists |
| Bisect | `bisect_start`, `bisect_good`, `bisect_bad`, `bisect_skip`, `bisect_run`, `bisect_reset` | exact good/bad/current commit, sequence ID, optional closed recipe | bounded steps/timeout; whole process tree must be reaped; reset restores original reference |
| Worktree | `worktree_create`, `worktree_lock`, `worktree_unlock`, `worktree_remove`, `worktree_prune` | safe name/branch/commit or durable worktree ID | derived path below managed root; registry/common-dir/head/cleanliness rechecked |

Every family receives a Checkpoint because even nominally additive index/ref changes can
interact with concurrent Workspace state. The preview lists any incomplete recovery claim.
For example, Checkpoint bytes cannot resurrect a dropped stash ref, and removal/prune acts
under an external product-managed root that the source Workspace snapshot does not archive.

## Hunk identity / Hunk 身份

Discovery renders a bounded textual, no-rename, full-index unified diff. Binary, combined,
oversized, quoted/unsafe path, symlink, submodule, ambiguous index-stage, or unsupported-mode
hunks fail closed. Each hunk identity covers:

- operation, normalized path, exact base blob, index blob, and whole worktree-file SHA-256;
- context-line SHA-256 and exact patch SHA-256;
- the repository-wide HEAD/index/worktree/stash/sequencer/upstream binding.

Execution requires IDs copied from a discovery result. It rebuilds the diff and applies
only the selected patch with `git apply`; old line numbers are never trusted in isolation.
Each file impact binds the actual source bytes in `before_sha256` and the in-memory result of
applying exactly the selected hunks in `after_sha256`, including missing-final-newline state.
Any selected hunk disappearance or any reviewed repository drift requires a new discovery.

```powershell
# Discover first. This path is read-only and creates no Approval.
cyberagent git-advanced discover-hunks hunk_stage --run <run-id> `
  --path internal/example.go --enable-git-advanced --enable-permission-control --json

# Preview only; omit --confirm to avoid creating Approval or mutation state.
cyberagent git-advanced run hunk_stage --run <run-id> --hunk <sha256> `
  --operation-key <stable-key> --enable-git-advanced --enable-permission-control

# Re-render, create/approve the exact proposal, checkpoint, and execute.
cyberagent git-advanced run hunk_stage --run <run-id> --hunk <sha256> `
  --operation-key <stable-key> --enable-git-advanced --enable-permission-control --confirm
```

## Stash semantics / Stash 语义

`stash_create` previews staged/index, tracked-worktree, and optionally untracked roles
separately. Ignored files are never included. `keep_index` preserves staged content after
creation. List/show output is selector-free: the projection exposes the exact stash commit,
base parent, index parent, optional untracked parent, audit subject, file roles, and hashes.

Apply and pop use the exact stash object ID rather than unstable `stash@{n}` input.
`restore_index` requests restoration of the index parent. Safe pop is implemented as exact
apply followed by resolving and dropping the same current stash entry. If apply conflicts,
the drop is not attempted, the original stash remains present, and the receipt exposes
base/ours/theirs conflict objects plus continue/abort availability where applicable.
Because Git can otherwise overwrite an ignored untracked file when a stash contains the same
path, apply/pop first compare the exact stash impact with the current ignored-path inventory
and fail closed on a file/directory collision. The same fence is recomputed immediately before
Git is invoked, so a collision introduced after review cannot pass on stale evidence.

`stash_drop` is explicitly lossy: the checkpoint documents that a deleted ref/object is not
guaranteed recoverable. There is no include-ignored or arbitrary pathspec mode.

## Rebase and cherry-pick / Rebase 与 cherry-pick

Start operations require a clean index/worktree and existing exact HEAD. Rebase additionally
requires the reviewed upstream to be an ancestor of HEAD. Rebase and cherry-pick start are
denied on detached or protected branches; rebase is also denied whenever the current branch
has a configured upstream because it is treated as shared history. Force push is not part of
this protocol.

A start derives a durable sequence ID before Git is invoked and stores original HEAD/branch,
target commits, current HEAD, sequencer SHA-256, conflict graph, generation, and originating
operation. Continue/skip/abort accepts only that sequence ID and requires the live sequencer
digest and current HEAD to match the durable row. External `git rebase --continue` or
`git cherry-pick --continue` therefore cannot be silently adopted by a stale preview.
Start previews derive the bounded paths that Git may check out. They reject collisions with
ignored worktree content; active-sequence controls conservatively reject ignored paths that
are recorded anywhere in reachable repository history, preventing an abort/skip/continue
checkout from silently replacing content that `git status` normally hides. Execution repeats
the collision check, and each automated bisect step checks again after its repository test and
before Git advances to another commit.

On process restart, every `running` operation is observed but never replayed. If the exact
rebase/cherry-pick sequence is still active, its current state and conflicts are persisted;
the operation receives an `interrupted` or `conflicted` terminal receipt and requires a fresh
explicit continue/skip/abort. If no sequencer remains because Git may have completed just
before the crash, the command is not guessed successful: the durable sequence is terminalized
as `failed`, the exact observed HEAD is retained, and the stale active fence is released.
Terminal receipts and sequence generations are immutable.

## Bounded bisect / 有界 bisect

Bisect start binds distinct exact good/bad commits and the original HEAD/reference. Manual
marks also bind `expected_current`, preventing a stale UI from classifying another commit.
`bisect_run` accepts one of two Go-owned recipes only:

- `go_test`: `go test -count=1 ./...`;
- `npm_test`: the repository's fixed npm validation launcher with offline npm settings.

The caller can select only `max_steps` (1–128) and per-step timeout (1–900 seconds). It cannot
supply argv, shell text, executable paths, environment, or network settings. Each step binds
the exact commit and uses the Run-selected Command Runtime with a stripped offline
environment and closed stdin. Missing `stdin_closed` or `tree_reaped` evidence, timeout, cancel,
or budget exhaustion produces a typed terminal failure. Exit 0 marks good, 125 marks skip,
and other program exits from 1 through 127 mark bad. Adapter failures and unknown exits
stop the operation without classifying the commit. `bisect_reset` restores the original reference/worktree state and
is permitted even when that original branch is protected; external sequence drift is denied.

API/Desktop recipes use the backend already selected for the Run: Local sandbox, Docker
Engine, or Docker Sandboxes (SBX). An explicitly configured Local host runtime records
`host_unsandboxed` / `explicit_host_execution`; a missing selected backend never falls back
to it. The foreground CLI currently assembles only the existing Local host runtime, requiring
`--enable-danger-full-access` in addition to the Git gates; Docker/SBX execution is available
through the Desktop's configured runtime.

Git operations, approvals and checkpoints remain bound to the source Workspace. A private
native authorization binds each recipe's exact source directory, current commit, fixed
command and operation record before passing that directory to the selected runtime. Ordinary
Run commands retain their Drydock binding. Docker masks `.git` with a read-only empty
directory (or the existing mask file for a linked worktree). SBX mounts the source `.git`
read-only: tests may inspect its history and configuration, but only the approved native
Git operation may switch or classify revisions. Each step records `bisect_executions`: zero-based
ordinal, tested commit, Job ID, backend identity/generation, isolation/network/credential
policy, readiness, workspace digest, image/template reference and process cleanup evidence.
Raw test output is discarded; only byte counts and digests enter the runtime Job. Permission,
lease, backend or repository drift stops further execution and marking. Cancellation reaps
the owned process/container/VM; restart recovery cleans owned resources and never replays
an uncertain recipe.

高级 Git 继续绑定原 Workspace，每步测试在 Run 所选后端内执行。收据记录实际提交、
Job 与后端身份；后端不可用、权限变化、取消或结果不确定时停止，不标记该提交，也不重放测试。

## Managed worktrees / 受管 Worktree

The client supplies no destination. Go derives
`<managed-root>/<common-dir-fingerprint-prefix>/<safe-name>` and permanently binds its path
hash, source repository, common Git directory, branch, commit, Run, Workspace, and creating
operation. Names are never reused, even after removal. Create always makes a new local branch
and refuses an existing path, Git worktree, or branch.

The managed root, repository subdirectory, and target are checked for symlinks and Windows
junction/reparse traversal during preview and immediately before mutation. Lock/unlock/remove
requires the exact durable ID and name. Remove has no `--force`; it rechecks stored and live
HEAD/branch/common-dir plus tracked and untracked cleanliness, so even a clean external commit
drift prevents deletion. Ignored content is also treated as unknown worktree state and blocks
removal, with cleanliness recomputed immediately before the remove command. Prune touches only
already-missing Git administrative entries under
the derived root and cross-checks each path hash against the product registry. An external or
unregistered worktree is never adopted by ordinary prune.

If restart occurs after exact create but before registry persistence, reconciliation may
adopt only a clean, unlocked, attached worktree whose path/common-dir/branch/commit all match
the reviewed preview. The operation still receives `failed/interrupted`, not success; the
receipt states that registry state was recovered and the operator must perform a new review.
Lock/unlock/remove/prune reconciliation similarly updates only provable registry state and
never replays Git.

## Desktop and HTTP / Desktop 与 HTTP

Desktop exposes **高级 Git / Advanced Git** only when the bootstrap capability is enabled.
The panel displays authority generations, repository/branch/upstream identity, hunk patches,
stash roles, base/ours/theirs conflicts, sequence actions, bisect progress, managed worktree
state, Checkpoint limitations, Approval status, and immutable audit receipts. Host paths and
private lease IDs remain absent.

HTTP uses the same Application service:

- `GET /api/v1/runs/{run_id}/git-advanced?limit=N` — read bearer, public projection;
- `POST .../git-advanced/discover-hunks` — read bearer, read-only exact hunk discovery;
- `POST .../git-advanced/review` — control bearer, immutable proposal + Approval;
- `POST .../git-advanced/execute` — control bearer, exact approved execution.

All bodies are bounded closed JSON objects; unknown or duplicate fields, repeated query
values, bodies on GET, URL/body Run mismatch, and stale generations fail closed. See
[HTTP API](http-api.md) and generated [OpenAPI](openapi.json).

## Security boundary and limitations / 安全边界与限制

- Git runs with system/global config ignored and a fully replaced environment. Hooks,
  fsmonitor, credential helpers, prompts, interactive sequence editors, pagers, external
  diff, system attributes, LFS smudge, and repository-local executable filter/diff/merge
  drivers are disabled or rejected before use. Commit signing and configured signing agents
  are disabled; recursive submodule checkout, rebase autostash/update-refs, and rerere
  auto-resolution are forced off. The ordinary editor is a fixed no-op so
  `rebase --continue` can reuse the already-reviewed commit message without launching a
  user-configured program.
- All caller paths use literal pathspecs. No force push, remote deletion, reset-hard,
  clean-force, arbitrary refspec, shared-branch rebase, arbitrary command recipe, or external
  worktree path can be expressed.
- Repository/common-dir, HEAD, branch, index, worktree/status, stash, sequencer, upstream,
  permission revision, capability generation, and lease generation are revalidated at call
  time. Drift produces a typed failure and no retry.
- Windows case aliases, unsafe or non-normalized names, `.git` path components,
  symlink/junction parents, POSIX symlinks, submodules, detached HEAD, protected branches,
  configured upstream/fork drift, literal pathspec metacharacters,
  cancellation, process-tree failure, and real conflict fixtures are covered by tests.
- Workspace Checkpoint covers bounded Workspace files and exact raw index bytes, including
  stage 1/2/3 entries while a merge conflict is active. If a bounded file projection is
  unavailable, the checkpoint records partial recovery instead of upgrading the claim. It
  cannot promise recovery
  of external processes/services/files, network side effects, deleted refs, or data below the
  separate managed-worktree root. Such limits are visible before approval and in receipts.
- Advanced Git does not create remote review comments, inspect CI logs, force-update a remote,
  or choose ours/theirs automatically. Those remain separate provider/review workflows.

The built-in `review` 1.4.0 and `focused-checks` 1.1.0 skills consume the full merge-base
diff, stable hunk graph, conflict objects, branch/worktree state, Checkpoint limitations, and
recovery receipts. Exact prior packages remain archived at `review` 1.3.0 and
`focused-checks` 1.0.0 for compatibility tests.
