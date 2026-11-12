# ADR 0205: exec --continue thread

- Status: Proposed
- Date: 2026-11-11
- Scope: CLI surfaces and multi-turn automation
- Updates: docs/usage.md; extends ADR 0201 (one-shot exec mode, which
  explicitly deferred this mode)
- References: Claude Code `-p --continue` and Codex `codex resume` (external
  parity baselines)

## Problem

ADR 0201 shipped exec as fresh-only: every invocation creates a new Run and
Thread, so a script cannot chain turns ("analyze, then fix what you found")
without losing context between invocations. Both external harnesses make
continuation a first-class flag (`claude -p --continue`, `codex resume`), and
it is the second entry point every multi-step automation reaches for after
the one-shot call.

## Decision (proposed)

1. **The Thread turn machinery does the work.** `--continue <thread-id>`
   resolves the Thread's latest Run and submits the prompt through the
   ordinary `OneShotService` on that Thread. When the Run is still running
   the message becomes a follow-up turn on the same Run; when it is terminal
   the machinery's existing successor path creates a Run inheriting the
   workspace, permission selection, and budget. No new scheduling, steering,
   or succession logic is introduced.
2. **Capabilities come from fresh flags, never from the Run's mode.** The
   process-local runtime capabilities derive from the operator's fresh
   `--enable-permission-control` / `--enable-danger-full-access` /
   `--confirm-full` flags exactly as in fresh mode. An earlier draft derived
   them from the Run's durable mode, which produced an internally
   inconsistent capability tuple (approval enabled without full access) that
   the command runtime rejects, and an error message the operator could not
   satisfy — the mode was dead on arrival for every ask/auto/full Run. The
   flags enable this process's adapters; the Run's durable Ask/Auto/Full
   selection still governs every dispatch, and process-local grants are
   never persisted, so re-presenting the grant is part of the contract
   rather than an authority widening.
3. **Fresh-run creation flags are ignored in continue mode.** The Thread
   carries its workspace, profile, route, and budget through its runs;
   `--workspace`, `--profile`, `--route`, and the budget flags would either
   duplicate or contradict that state, so they are documented as ignored
   rather than silently re-applied.
4. **Identifiable failures.** An unknown Thread fails with an explicit
   not-found error; a Thread without any Run fails with an explicit no-Run
   error. Raw store errors never reach the operator.
5. **Output and exit contract unchanged.** `--json`, the bare reply, and the
   stop-reason-keyed exit codes are identical to fresh mode; on the
   successor path the reply comes from the successor's session (the
   machinery's `prepareRun` returns the successor Run and session, and the
   one-shot service reads the committed reply from there).

## Alternatives considered

- Deriving runtime capabilities from the Run's durable mode was attempted
  and rejected (see Decision 2): it conflated process-local runtime
  enablement with the Run's authority ceiling, broke the both-or-neither
  runtime invariant, and made the mode unusable for its primary targets.
- A `--resume` flag taking a Run id was rejected: the Thread is the
  conversation identity (stable across successor Runs), and continuing by
  Thread matches the external baselines and the interactive surfaces.
- Reusing `thread send` was rejected for the automation story: it commits
  the message and returns without driving the turn, so scripts would have to
  host the execution themselves — exactly the gap ADR 0201 closed for fresh
  runs.

## Verification

- `TestExecContinueDeliversFollowUpTurnOnRunningRun`: fresh exec, then
  continue on the still running Run — the second reply is distinct, the Run
  identity is unchanged (follow-up turn, no successor), and exactly one Run
  exists.
- `TestExecContinueCreatesSuccessorForTerminalRun`: after cancelling the
  first Run, continue creates a successor Run with its own identity and the
  reply comes from the successor.
- `TestExecContinueUnknownThreadFailsClearly`: unknown Thread fails with a
  not-found error and never leaks a raw SQL error.
- `TestExecJSONOutputWithUnfinishedTurnExitsNonZero` pins the shared
  `--json` exit contract on this branch.
- Documentation: docs/usage.md (bilingual `--continue` section) and this
  ADR.

## Consequences

Multi-step automation gets the parity continuation entry point: chain turns
on one Thread with a stable identity, inherit workspace and permission
state, and keep the fresh-mode output contract. The remaining automation
deltas for exec (per-invocation instructions and interruption semantics)
are covered by ADRs 0203 and 0204.
