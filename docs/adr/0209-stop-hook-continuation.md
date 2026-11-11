# ADR 0209: stop hooks with bounded continuation

- Status: Proposed (design revision after delegate review; not
  implementable until the dependencies below land)
- Date: 2026-11-11
- Scope: hook events, turn lifecycle, session provenance, and automation
- Dependencies: **requires the operator script hook runtime** (conditional
  `denied` script output, hook directory confinement) from the
  operator-script-hooks PR series — not on main yet; this ADR is not
  implementable until it lands. The `turn_end` / `post_compaction` PRs
  are relevant as the per-event validation precedent and for sequencing
  the audit-event migration (they currently ship no audit migration at
  all — flagged on those PRs)
- Updates: docs/extensions.md; extends ADR 0201 (one-shot exec mode),
  ADR 0205 (exec --continue thread), and the operator script hook
  contract
- References: Claude Code Stop hook (`decision: block` with `reason` →
  forced continuation, `stop_hook_active` guard, block cap — external
  parity baseline)

## Problem

"Keep going until the task is actually done" is the last automation
pattern Universal-Code cannot express inside one invocation. Today the
only fallback is external orchestration: a script re-invokes the one-shot
`exec` mode run by run — run, inspect the reply, decide whether more work
is needed, start the next run (each invocation today creates a fresh Run;
the proposed `--continue` thread flag of ADR 0205 is not yet available to
chain turns of one Run). That works
but moves the loop outside the agent — every iteration re-pays process
startup, the loop logic lives in the caller, and the decision point
cannot use the agent's own context. Claude Code's Stop hook moves the
decision inside the loop: when the agent finishes, a hook can block the
stop and force continuation with a reason, bounded by a configured
iteration cap and the run budget.

The reason this needs an explicit maintainer decision is control: today
only an operator message starts the next turn. A hook that can force
continuation changes who drives the turn loop, and — the harder half —
the continuation text a hook produces must reach the model without
acquiring operator authority.

## Decision (proposed)

1. **A `stop` event at the turn_finish boundary.** When the supervisor
   commits a finish action for a turn (stop reason `turn_finish`), the
   hook engine fires `stop` after the finish is durable but before the
   handoff settles — inside the handoff completion flow, so the
   failure-sealing paths (which must not fire stop) stay out of it. The
   bounded Go-owned stdin payload carries what the done-ness decision
   consumes: the finish reply and summary excerpts, the stop reason, and
   the current hook-continuation count (Claude Code's parity input is
   the last assistant message).
   waiting_approval never commits a finish and therefore never fires
   `stop`; this is an invariant the implementation pins with a test.
   Declaration actions: `record` and `script` — `deny` is the runtime
   effect of a script output that decides against stopping (Decision 2),
   not a declarative action. Annotate is rejected at validation
   (annotate is currently valid for any event — the new rule is genuinely
   needed), narrow is already pre_tool-only.
2. **The conditional decision comes from operator script hooks.** The
   hook engine on main is declarative-only: plugin rules carry a fixed
   declaration-time message and cannot inspect the reply. A stop hook
   that decides "is the task done" requires the operator script hook
   runtime's bounded script output (`denied: true` + reason) from the
   script-hooks PR series — this ADR depends on it and is not
   implementable without it. Declarative plugin rules MAY also declare
   `stop` with `record` action (observation only); a declarative `deny`
   would be an unconditional always-continue rule, which is rejected at
   validation to prevent a fixed-message loop.
3. **Hook execution failures never continue.** Stop hooks always run
   with the continue failure policy (validation rejects `FailureDeny`,
   mirroring the post_tool retroactive-policy rule): loader refresh
   failures, apply errors, and audit sink failures each record
   `failed_continue` and allow the stop. Only a *successful* script
   output with `denied: true` continues. A broken hook cannot loop the
   agent.
4. **The continuation text is NOT an operator message.** This is the
   design's hardest requirement, and the naive version is unsafe: the
   steering delivery path commits delivered input as a `user` message
   with `SourceOperatorMessage` provenance and `InstructionAuthorized:
   true` — a hook-deny reason delivered that way would reach the model
   as an instruction-authorized operator instruction, an authority
   escalation the codebase explicitly prevents elsewhere (the hook
   engine's own `DeniedError` exists precisely so plugin deny text never
   reaches model-visible paths). The design therefore adds:
   - a new closed-enum session source kind, `SourceHookContinuation`,
     validated like the existing kinds (`InstructionAuthorized: false` —
     workflow guidance, the same authority class as project
     instruction envelopes);
   - a delivery projection for that kind that wraps the hook reason in
     Go-authored trusted framing around the bounded, redacted hook text —
     the pattern the specialist continuation already uses for
     service-generated model-visible text — instead of passing raw text
     through the operator path;
   - retention outside the instruction-authorized recent-operator
     history (it is evidence-like guidance, not operator instruction).
   ADR 0197 keeps script-supplied reasons out of model-visible denial
   errors; the stop continuation deliberately introduces a new,
   non-authoritative untrusted-envelope channel for the reason — an
   extension, not a contradiction. The deny reason is also bounded
   (script output cap); the design adds
   whole-token redaction in the stop-boundary envelope code — the
   script executor only bounds its output (existing fields are
   bounds-checked, not redacted), so the redaction belongs where the
   text becomes model-bound, mirroring the file-attachment import order
   (redact the whole bounded text before any truncation).
5. **The continuation rides the existing steering queue with a
   deterministic key and an explicit row marker.** A denied stop
   enqueues one operator-steering message via `EnqueueOperatorSteering`
   with `requested_by = "hook:<hook-id>"` — the row-level marker the
   delivery projection reads to stamp `SourceHookContinuation` — and a
   **deterministic, derivable operation key** (a fingerprint of run ID,
   turn number, and hook ID) so the deny→enqueue pair is idempotent
   under crash-replay (replaying the same enqueue returns the same
   queued message instead of duplicating it), matching the store's
   replay-by-key contract. Delivery is not the only reader: both read
   projections must recognize the marker too. `ListThreadMessagesPage`
   projects every pending or cancelled steering row as an operator
   message (`instruction_authorized = 1`), and the transcript projection
   treats pending steering as operator input — hook text staged for
   delivery would stay operator-attributed until delivery and
   permanently after a cancellation. Both projections key off the same
   `hook:` prefix and stamp `SourceHookContinuation` (with
   `InstructionAuthorized: false`) for those rows. The queue bound
   (`MaxPendingOperatorSteering`) applies; if enqueueing fails, the
   stop proceeds and the failure is audited. Binding note for the exec
   loop: the ordinary operator-actor `Execute` path requires
   `message.RequestedBy == request.RequestedBy`, so the continuation
   turn replays the enqueue directly under the hook actor and drives
   execution through a dedicated entry point rather than the operator
   submission path.
6. **Continuation is bounded three ways.** The ordinary run budget
   (MaxTurns, MaxTokens, MaxToolCalls, wall clock) still caps every
   turn — and budget exhaustion during a continuation surfaces as a
   sealed failed turn (the standard `CodeResourceExhausted` →
   `closeFailedProductTurn` path), not a graceful cap; scripts must
   handle the failed-turn advance contract. A durable per-run counter
   caps hook-requested continuations at 8, persisted in the supervisor
   checkpoint (which already carries durable per-run counters such as
   NextTurn) and incremented in the handoff completion transaction so
   replay tolerates it; the ninth denied stop proceeds as a normal stop
   with the cap recorded in the audit and the end record. The cap is
   per-run — for exec (fresh run per invocation) that equals
   per-invocation; for long-lived interactive Threads it is a
   run-lifetime cap, stated deliberately (a thread whose hook
   continuations are exhausted keeps working; only the hook loop stops).
   Each continuation also consumes one steering slot.
7. **The one-shot loop reads a dedicated signal.** A hook-requested
   continuation surfaces as a distinct stop reason (`hook_continuation`)
   on the turn result — not as `turn_finish` — so `exec` loops while
   that reason is present, submitting the continuation turn bound to the
   already-enqueued hook message (replay by the deterministic key above,
   never a duplicate submission), and the invocation's final reply is
   the last committed one. Interactive Threads need no loop: the queued
   continuation is delivered lazily on the next operator- or
   workflow-initiated turn (pending queue content gates automatic
   admission, per the existing automatic-turn rules), and the transcript
   renders the hook provenance.
8. **A dedicated boundary adapter consumes the raw result.** The
   existing lifecycle boundary adapter maps any deny to a policy error
   and drops the reason; the stop boundary needs its own adapter that
   treats deny as success-with-continuation, applies the trusted framing
   of Decision 4, and audits the outcome. Multi-hook denial is already
   deterministic (registrations are sorted; the first deny wins), so one
   reason drives each continuation.
9. **Audit, migration, and diagnostics.** The audit table's event
   constraint has never been widened on main — a migration (following
   the table-rebuild pattern the script-hooks PR series introduces for
   its own events) adds `stop` to the event constraint. The web
   diagnostics `hookEvents` list and label map gain the event in the
   same PR (the post_tool_failure review caught exactly this class of
   omission). The continuation counter surfaces in the end record so
   scripts can tell "finished" from "stopped: continuation cap reached".
   Without stop hooks configured, both surfaces behave exactly as today.

## Alternatives considered

- Orchestrator-side chaining (today's pattern) remains fully supported
  and is the fallback if maintainers reject the in-loop semantics: it
  gives the caller more control and zero new in-loop semantics, at the
  cost of process startup per iteration and loop logic in every caller.
- Model-requested continuation (the agent asks to continue in its finish
  action) was rejected: it grants the loop control to the model, which
  the architecture reserves to Go.
- Extending `turn_end` with a deny action was rejected: turn_end fires
  for every turn boundary and its record-only, continue-only contract is
  the right observation semantics; a separate `stop` event keeps the two
  contracts distinct.
- Delivering the hook reason as a plain operator message was rejected:
  see Decision 4 — it would escalate hook text to instruction-authorized
  operator input.

## Verification (planned)

- Engine validation: `stop` rejects annotate, narrow, FailureDeny, and
  the declarative `deny` action (an unconditional deny would make every
  stop an always-continue rule, Decision 2); `record` and `script`
  declarations pass.
- Provenance: `SourceHookContinuation` messages carry
  `InstructionAuthorized: false`, render with hook provenance in the
  transcript, stay outside instruction-authorized history retention, and
  never carry tool grants; unknown kinds keep failing validation.
- Turn loop: a denied stop enqueues exactly one hook-provenance steering
  message (deterministic key replay returns the same message); audit
  records every result; the counter increments in the completion
  transaction and the cap forces a normal stop; budget exhaustion during
  a continuation seals a failed turn per the standard path.
- One-shot loop: a stub whose finish replies alternate with hook denials
  drives exec to the cap; the final reply and exit code follow the last
  real turn; without stop hooks the behavior is byte-identical to today.
- waiting_approval never fires `stop` (pinned invariant).
- Documentation: docs/extensions.md hook table, ADR 0209.

## Consequences

Automation gains the parity "until done" loop inside one invocation,
with the loop owned by operator-configured script hooks and bounded by
budget, a durable cap, and the steering queue. The control question is
answered explicitly: the model still cannot continue itself, the
operator still owns hook configuration, hook text never acquires
operator authority, and a hook can only continue — never widen tools,
permissions, or budget. The design depends on the operator script hook
runtime and its migrations landing first; if maintainers prefer the
orchestrator-side pattern, this ADR records the full design — including
the provenance work that any future continuation mechanism must reuse —
for reconsideration instead of an unreviewed feature landing in the turn
loop.
