# ADR 0204: exec signal interruption

- Status: Proposed
- Date: 2026-11-10
- Scope: CLI surfaces and interruption semantics
- Updates: docs/usage.md; extends ADR 0201 (one-shot exec mode)
- References: Claude Code and Codex CLI graceful Ctrl-C behavior (external
  parity baselines)

## Problem

The graceful interruption behavior for `cyberagent exec` exists only as an
accident of process wiring. The CLI binary's root context is already signal
aware (`cmd/cyberagent/main.go` wraps `context.Background()` in
`signal.NotifyContext` before `app.ExecuteContext`), so the production entry
point inherits a clean cancellation through the ordinary turn machinery —
recorded durably and already regression-tested through `run step`
(`TestExecuteContextCancelsProviderAndPersistsFailure`). But the exec command
itself carries no interruption contract:

- An embedder invoking `execCommand` (or `ExecuteContext`) with a plain,
  non-signal-aware context gets none of that behavior — Ctrl-C kills the
  process at an arbitrary point with no recorded turn outcome.
- The exec surface documents no interruption semantics at all: which exit
  code a cancellation produces, whether the Run stays recoverable, and how
  `--timeout` interacts with a signal are all discoverable only by reading
  the turn machinery.
- No test drives the cancellation through the exec entry point specifically,
  so a refactor of the exec path could silently lose the inherited behavior.

Both external harnesses treat interruption as a first-class flow — stop the
current turn, preserve state, exit with a recognizable code — and document
it.

## Decision (proposed)

1. **The command boundary is self-contained.** `execCommand` wraps its
   context with `signal.NotifyContext` (`os.Interrupt`, plus `SIGTERM` where
   the platform delivers it). For the production binary this nests with the
   existing root registration and changes nothing; for any other entry path
   it makes the command carry its own guarantee. Cancellation flows into the
   ordinary turn machinery, which records the durable outcome through
   `context.WithoutCancel` (ADR 0201's detached-completion contract): the
   handoff completes with the `cancelled` error code, the turn failure is
   stored with `CodeCancelled`, and the Run keeps its lifecycle — recoverable
   by a follow-up `thread send` instead of orphaned `running`.
2. **Exit codes stay in the project's own mapping.** `apperror.ExitCode`
   already maps `CodeCancelled` → `7` and `CodeDeadlineExceeded` → `9`, so
   `exec` needs no new exit-code convention: scripts distinguish a cancelled
   invocation (7) from a timed-out one (9) from every other failure code in
   the same table. Signal cancellation and `--timeout` produce an identical
   durable lifecycle (the differing durable records carry the differing
   error codes); the exit codes rest solely on the context cause, which the
   composition order (signal wrapper first, timeout second) preserves.
3. **No new machinery.** The change is one context wrapper at the command
   boundary plus the contract documentation; every durable guarantee
   (recorded turn failure, recoverable Run, preserved history) is inherited
   from the existing turn failure path, which the new exec-path test proves
   by cancelling mid-model-call and asserting the recoverable Run plus the
   `CodeCancelled` surface.

## Alternatives considered

- Closing the gap as "already handled by main.go" was rejected: a command
  whose only interruption guarantee lives in one binary's wiring is not a
  contract. `ExecuteContext` is the documented embedding surface, and the
  exec-path test would not exist.
- A dedicated exec shutdown handler (drain the model stream, then stop) was
  rejected: the turn machinery already persists every state transition
  durably; a bespoke drain would duplicate its detached-completion logic.
- Mapping the interruption to POSIX exit 130 was rejected: the project
  defines its own exit-code table (`apperror.ExitCode`) and every CLI
  consumer reads that mapping; introducing a second convention for one
  signal would break the uniformity.
- Second-Ctrl-C force-kill behavior was deferred: repeated Ctrl-C is
  currently swallowed entirely by the process-level registration (there is
  no force-kill path short of SIGKILL — pre-existing behavior, unchanged
  here), and the durable write is bounded by the 2-second detached
  completion window, so the first Ctrl-C already returns control promptly.

## Verification

- `TestExecCancellationKeepsRunRecoverable` drives the exec entry point with
  a provider that blocks inside the model call, cancels the invocation
  context, and asserts: the returned error carries
  `apperror.CodeCancelled`, exactly one Run exists, and that Run is
  non-terminal (recoverable). The wrapper's own effect is one
  `signal.NotifyContext` whose failure mode is compile-time; the behavioral
  contract it makes explicit is what the test proves.
- Documentation: docs/usage.md (bilingual Ctrl-C paragraph) and this ADR.

## Consequences

The exec interruption contract is stated, tested, and independent of the
binary's wiring: scripts get exit 7 on cancel and 9 on timeout, the Run
stays recoverable for the `thread send` continuation ADR 0201 anticipated,
and embedders inherit the same behavior without replicating main.go. The
process-level registration in main.go remains the outer layer; the command
boundary no longer depends on it.
