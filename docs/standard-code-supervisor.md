# Standard Code bounded completion loop

`standard_code_supervisor.v1` is the Go-owned completion protocol used only by
the root Supervisor of an exactly configured Standard Code Run. It composes the
existing Supervisor, `agent-code-tools.v1`, Code Intel, Plan delivery,
Workspace Checkpoints, and `command-runtime.v2`; it is not a second Agent loop.

## State contract

```text
Inspect -> Plan -> Checkpoint -> Edit/Apply -> Execute -> Observe
                                  ^                 |
                                  +-- Diagnose <----+ failure
                                                    |
                                                 success
                                                    v
                                                 Deliver
```

- `Inspect` requires at least two consecutive completed tool rounds containing
  only Go-authorized Workspace or Code Intel reads. A mixed, unfenced, denied,
  failed, or empty round resets the consecutive proof.
- `Plan` may record a Plan delivery proposal, but Plan never exposes Workspace
  mutation or Command Runtime tools. An operator must select a direction and
  explicitly enter Deliver.
- `Checkpoint` and `Edit` use the existing reviewed proposal/apply flow. An
  apply counts as a mutation only when its exact edit receipt has a completed
  `file_tool` transaction and bound after-Checkpoint.
- `Execute` selects repository-derived build, test, or lint commands through
  the existing sandboxed Command Runtime. The state machine does not embed a
  language-specific script and never installs dependencies or enables network
  or credentials.
- `Observe` trusts only structural runtime fields: action, Job identity/state,
  exit code, output cursors and digests, truncation/tree-reap facts, Artifact
  identities, and Checkpoint bindings. Stdout, stderr, repository files, and
  tool text remain untrusted evidence.
- A non-zero, incomplete, truncated, or otherwise failed result enters
  `Diagnose`. A reviewed fix creates a new mutation epoch, so verification of an
  older epoch cannot authorize `Deliver`.
- `Deliver` and root `finish` require successful structural verification of the
  current mutation epoch. A stopped loop can only return `wait` while owned Job
  cleanup remains available under the current permission revision.

## Fixed budgets and stable stops

The protocol fixes, and validates on every persisted snapshot, these limits:

| Budget | Limit |
| --- | ---: |
| Consecutive read rounds | 2 minimum |
| Tool rounds per Supervisor turn | 24 native (217 physical execution rounds) |
| Command invocations | 12 |
| Background Jobs | 2 |
| Fix rounds | 3 |
| Captured result text | 1 MiB |
| No-progress observations | 3 |
| Repeated equivalent failures | 3 |
| Completion-ledger entries | 512 |
| One state snapshot | 64 KiB |

The Run's immutable token, time, and total tool-call limits remain enforced by
the existing Supervisor and tool-budget ledgers and are copied into every
completion snapshot. Exhaustion, repeated failure, missing mutation evidence,
context drift, permission drift, or ledger exhaustion moves the protocol to a
durable `stopped` state. It cannot report success from that state.

## Recovery, drift, and background Jobs

Every decision is fenced to the exact Run, Mission, Workspace, root Agent,
configured Standard Code preset, mode, execution-profile, interaction, permission,
and browser-CDP revisions, Supervisor turn/attempt, and active execution lease.
A completed Workspace mutation records the root identity and capability generation
derived from its after-Checkpoint. The next turn must present that exact projection.
The only permitted mode change is the exact next Plan-to-Deliver revision with the
selected Plan; its new capability generation is recomputed from the still-bound root
instead of accepting arbitrary drift.

Side-effect intents have deterministic fingerprints. The same call can resume
through the existing execution receipt, while a new call repeating an already
handled apply, command launch, stdin write, cancel, or kill is recorded as a
replay and is not invoked. Pause, steering, cancellation, compaction, and process
restart therefore reuse the ordinary Supervisor and runtime recovery paths.

A background Job record retains its Run-scoped Job ID, permission revision,
mutation epoch, state, and last output cursor. `read` and `wait` require the exact
next cursor; `write_stdin`, `cancel`, and `kill` require the same owner and current
permission revision. A Job from an older mutation epoch may be observed or stopped,
but cannot accept new stdin or verify a newer edit. Budget stop does not create new process authority, but it
does not prevent an otherwise valid cancel/kill of an already owned Job.

## Durable evidence

Schema v135 adds the append-only `standard_code_supervisor_ledger`. Each row
stores one decision or transition plus the complete bounded state projection,
structural fingerprints, fixed budgets, and its exact Run event. Raw tool
arguments and results remain in the existing Supervisor call ledger and bounded
Artifacts; v135 does not duplicate them. SQL triggers reject updates, deletes,
wrong preset/root/turn/lease bindings, and call decisions that do not match the
pending or terminal Supervisor call.

Runs without the exact configured Standard Code preset do not activate this
coordinator. Code/Plan, Review, Learn, Cyber, child, and Specialist tool matrices
continue to be derived from their existing Go capability rules and remain
fail-closed.

The Drydock and its Git worktree provide product ownership and recovery. They
are not a security sandbox; process isolation comes from the current Local OS
backend or fixed Docker backend.
