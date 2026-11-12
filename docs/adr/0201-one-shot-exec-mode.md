# ADR 0201: One-shot exec mode

- Status: Proposed
- Date: 2026-11-09
- Scope: CLI surfaces and non-interactive execution
- Updates: docs/usage.md; complements ADR 0036 (idempotent controlled run
  creation), ADR 0037 (controlled session message submission), and ADR 0049
  (runtime guards)
- References: Claude Code `-p` headless mode and Codex `codex exec` (external
  parity baselines)

## Problem

Universal-Code has no one-shot, non-interactive execution surface. `learn ask`
bypasses the agent loop entirely (a raw model chat with no tools), `run create`
only creates a Run, and `thread send` returns as soon as the message is
committed — nothing takes one prompt, drives the agent loop to completion, and
prints the final reply. Both external harnesses make this their automation
story (`claude -p "prompt"`, `codex exec "prompt"` with structured output and
exit codes), and it is the entry point every CI job, cron task, and script
starts with. Today a script must create a Run through the API, host the
execution itself, and scrape the reply — the harness does the work a
one-shot command should.

## Decision (proposed)

1. **Composition, not a new runtime.** `cyberagent exec` composes existing
   primitives: ordinary Run creation (workspace, profile, route, budget,
   project config and instruction discovery — the same fail-closed rules as
   `run create`), then one Thread turn executed synchronously in-process by
   the ordinary Thread turn machinery over the same foreground runtime the
   other in-process CLI surfaces use (`newCLIExecutionRuntime`: command
   runtime, lifecycle and project hooks, MCP client, drydocks, browser when
   enabled). No new execution path, scheduler, or permission surface exists;
   every approval, budget, and policy rule of the underlying Run applies
   unchanged. The invocation owns its adapters and never changes the Run's
   durable permission selection; `--enable-permission-control`,
   `--enable-danger-full-access`, and `--confirm-full` map onto the same
   foreground activation contract `run wake consume` uses. Network stays
   disabled unless the Run's mode grants it, so web-dependent tasks are out
   of scope for v1.
2. **One prompt, one fresh Run and Thread.** Each invocation creates a new
   Run bound to the required `--workspace`; there is no implicit session
   reuse. Follow-ups go through `thread send` or a future `--continue` flag;
   keeping v1 fresh-only makes output and exit codes unambiguous.
3. **The supervisor's finish action is the completion signal.** A run may
   legitimately stay open (`running`) for follow-up turns after the task's
   turn succeeds; exec therefore keys its exit code on the supervisor's stop
   reason for this turn (`turn_finish` → exit 0; `waiting_approval`,
   `root_wait`, and failures exit non-zero with the reason and the run
   lifecycle status). On the success path stdout carries the last committed
   assistant reply; a turn that fails before committing a reply exits with an
   error instead. A ctx deadline (`--timeout`) does not cancel the durable
   Run: the handoff failure persists through `context.WithoutCancel` and the
   Run stays recoverable, while the durable budget timeout travels in
   `Budget.TimeoutSeconds` like every other Run.
4. **No authority widening.** `exec` exposes only Run-creation flags
   (`--workspace`, `--profile`, `--route`, budget ceilings, `--timeout`).
   Approval waits, budget exhaustion, and policy denials surface as non-zero
   exits with the run status; pre-arranging execution grants remains an
   explicit operator action outside the command. `--json` emits
   `{run_id, thread_id, status, reply}` for scripts; `--stdin` reads the
   prompt from standard input.

## Alternatives considered

- Reusing `learn ask` was rejected: it bypasses the agent loop, tools, and
  permissions, so it is not an execution surface at all.
- Making `exec` wait on the Run lifecycle reaching `RunCompleted` was
  rejected: session-bound runs stay open by design after a successful task
  turn, so the lifecycle status is neither necessary nor sufficient as a
  completion signal for one invocation.
- Streaming events (the `headless` exporter) as the default output was
  deferred: the parity baselines print the final reply, and event streaming
  is already available separately for consumers who want it.
- An `--continue <thread-id>` mode was deferred to a follow-up: fresh-only
  keeps the v1 output contract unambiguous.

## Verification

- `internal/application` tests drive the one-shot service through the offline
  provider: terminal reply extraction with the `root_finish`/`turn_finish`
  stop reason, the unfinished-turn case (a Continue response never reports
  finish), empty-prompt rejection, and the failed-handoff path (no provider
  registered → error, no result).
- `clipExecGoal` guards the goal-limit clip; command wiring reuses the
  standard foreground runtime helpers and is exercised by the application
  tests through the same handoff service.
- Documentation: docs/usage.md (bilingual) and this ADR.

## Consequences

Scripts, CI jobs, and cron tasks get the parity entry point in one command
with no new authority surface. The remaining automation deltas — streaming
event output for exec, a `--continue` flag, and a Run completion lifecycle
that terminal tasks could opt into — are follow-ups that build on this
composition rather than new design surfaces.
