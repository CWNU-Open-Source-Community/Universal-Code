# ADR 0202: Approval-requested hook boundary

- Status: Proposed
- Date: 2026-11-09
- Scope: restricted lifecycle hook events and the operator approval gate
- Updates: the hook boundary contract in docs/extensions.md; complements
  ADR 0039 (model plan and approval controls) and ADR 0103 (Full access start
  token)
- References: Claude Code `Notification` hook event (fires when Claude needs
  permission); the `PermissionRequest` hook is examined and deliberately
  narrowed (see Decision 1)

## Problem

The restricted hook runtime fires at tool, run, session, prompt, compaction,
subagent, and checkpoint boundaries — but nothing fires when a Run settles
into an approval gate. With the one-shot exec mode (ADR 0201), an unattended
Run that reaches an approval goes silent: no webhook, no metrics tap, no
operator notification channel. Claude Code fires its `Notification` hook at
exactly this moment.

Two durable settle shapes exist in this runtime, and the boundary covers
both: a turn-level settlement (command/MCP/browser preflight waits, inline
web fetch review) where the Supervisor turn pauses with pending approvals and
the Run may still be `running`, and the durable waiting gate where the Run
row itself is persisted as `RunWaitingApproval` (today the web fetch
authorization transaction). The hook fires whenever the Supervisor turn
settles into the gate, and again on `AgentRunner.Execute` re-entries into a
durably waiting Run.

## Decision (proposed)

1. **Observation-only, whitelist by action.** Only `record`-style hooks may
   subscribe; deny, annotate, and narrow are rejected at declaration time,
   and the check is written as a whitelist (`Action != ActionRecord` fails)
   so any future action type fails closed instead of silently gaining
   approval-gate authority. The approval decision stays with the operator:
   hooks never auto-deny, never auto-approve, never rewrite a pending human
   decision. Claude Code's `PermissionRequest` allow/deny behavior was
   examined and deliberately narrowed to observation — pre-tool deny hooks
   already cover pre-execution enforcement.
2. **Fires per settlement and per Execute re-entry — no dedup.** The
   boundary fires once per Supervisor-turn settlement into the gate (from
   the convergence function all three settle paths call) and again on every
   `AgentRunner.Execute` re-entry into a durably waiting Run. A polling host
   therefore re-notifies per poll: accepted and documented, because the
   current `Execute` has a single CLI caller; dedup/debounce is a follow-up
   if more hosts appear. Only `AgentRunner.Execute` implements the re-entry
   half today — thread-turn refresh, handoff continuation re-entries, drain
   loop heads, and session chat rejections do not fire, by design.
3. **Fail closed, stay recoverable.** A hook or loader failure at the
   boundary propagates as an error from the settlement/re-entry call. The
   turn-level settlement deliberately skips `recordFailure` so the
   checkpoint stays in `SupervisorTurnStarted` and
   `resumePendingToolApproval` can still resume the exact pending action —
   fail-closed for the hook, recoverable for the Run.
4. **Go-owned bounded payload.** `{run_id, state: "waiting_approval"}`.
   Deliberately minimal: no session id, pending approval id, or tool name,
   because the pending decision is the operator's input, not model-visible
   data. Enriching the payload is a follow-up with its own review.
5. **Identity consistency.** Every trigger site resolves the workspace from
   the Run's mission (the durable re-entry fetches the mission explicitly),
   so audit rows filter consistently by workspace.

## Alternatives considered

- `PermissionRequest`-style auto-deny was rejected (see Decision 1).
- Firing on `RunPaused` Supervisor waits was rejected: a Supervisor wait is a
  continuation pause, not an approval gate.
- Deduplicating re-entry notifications was deferred (Decision 2).
- A richer payload was deferred (Decision 4).

## Verification

- `internal/hooks` tests: record accepted, deny/annotate/narrow rejected by
  the whitelist, tool filters rejected.
- `internal/application` tests drive a Run into the durable waiting gate via
  the inline web fetch approval (Ask mode) and assert the hook fired exactly
  once with the run identity, and that a failing loader fails the Execute
  re-entry closed.
- Documentation: docs/extensions.md (bilingual) and this ADR.

## Consequences

Operators close the unattended loop: `exec` (ADR 0201) runs a task, this
boundary pages them when a decision is needed, and the audit ledger keeps
the trail. Auto-decision remains impossible by construction.
