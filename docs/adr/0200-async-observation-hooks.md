# ADR 0200: Asynchronous observation hooks

- Status: Proposed
- Date: 2026-11-09
- Scope: restricted lifecycle hook runtime and hook declaration semantics
- Updates: ADR 0197 (operator script hooks), ADR 0199 (hook tool matchers),
  and the hook contract in docs/extensions.md
- References: Claude Code `async: true` hooks and Codex background hooks with
  their per-session concurrency ceiling (external parity baselines)

## Problem

Every hook execution is synchronous: the boundary waits up to 2 seconds
(declarative) or 10 seconds (script) for the hook to finish. Both external
harnesses support `async: true` background hooks for observation taps —
telemetry, webhook notification, metrics — precisely so a slow tap does not
tax every tool call. Without an asynchronous path, an operator's half-second
webhook script pays its latency on every boundary it subscribes to.

## Decision (proposed)

1. **Background means observation-only.** Only `record` and `script` actions
   may declare `async: true`, and both require the `continue` failure policy.
   Deny, annotate, and narrow must stay synchronous: a hook the boundary
   never waits for cannot block a call, and it cannot annotate a result the
   caller has already received. A fail-closed policy has nothing to close in
   the background, so declaring one is rejected rather than silently degraded
   to fail-open. A background script's entire result — including its own
   `denied` output — is discarded; the audit ledger records only the run
   outcome, never the discarded decision.
2. **Bounded concurrency, no queueing.** The engine keeps an eight-slot
   semaphore (the same ceiling Codex documents per session). A fire that finds
   the ceiling occupied is skipped with a `failed_continue` audit row instead
   of blocking or queueing; the outcome vocabulary stays unchanged so the web
   hook diagnostics parse without drift.
3. **The boundary never waits.** Background executions run on a detached
   context derived from `context.Background()` (values are dropped with the
   cancellation; the deadline comes from the declaration) after the boundary
   has returned, and their audit rows are written on their own detached
   contexts so a saturated engine or an expired hook deadline can never
   suppress them. Results are discarded; only the metadata-only audit records
   `completed` or `failed_continue` with `rejected=false`. Unfinished
   background hooks are lost at process exit — documented, not persisted.
4. **Same trust and bounds.** Declarations validate exactly as before (script
   specs, tool filters, identity); `async` participates in the registry
   generation fingerprint. Timeouts still kill background script children at
   their declared ceiling, and the plugin diagnostics projection exposes the
   `async` flag so operator views render the execution mode.

## Alternatives considered

- Allowing asynchronous deny/annotate was rejected: a decision the caller
  never observes is either dead weight (annotate) or a false promise (deny);
  both would make the audit lie about what influenced the call.
- Queueing fires past the ceiling was rejected: unbounded queues behind a
  boundary that never waits turn into silent memory growth, and dropped work
  is more honest for an observation tap.
- Making the outcome vocabulary distinguish `skipped` was rejected: it would
  ripple through the store CHECK constraint, the frontend validators, and the
  OpenAPI snapshot for a marginal distinction. The cost is accepted and
  documented: an audit reader cannot tell a saturated-engine skip from a
  script failure because both record `failed_continue` — in the synchronous
  path that outcome means "the hook ran and failed, the boundary continued",
  so the shared value is a deliberate, documented overload.
- A session-scoped ceiling like Codex was deferred: the engine is the natural
  scope in this runtime (one per app/desktop host), and per-run attribution
  stays available through the audit rows.

## Verification

- `internal/hooks` tests cover validation (async deny/annotate/narrow
  rejected; record/script on any event accepted), boundary results untouched,
  audit rows for completion and failure, the concurrency ceiling with a skip
  audit row, and failure recording — all under the race detector.
- The plugin diagnostics projection and its web parser gain the `async` flag
  with drift tests; the OpenAPI snapshot and the generated TypeScript schema
  are regenerated.
- Documentation: docs/extensions.md (bilingual) and this ADR.

## Consequences

Operators can attach slow observation taps — webhooks, metrics, exports —
without taxing the boundaries they subscribe to. The enforcement surface is
unchanged: background hooks add no authority, cannot influence calls, and are
fully audited. The remaining parity deltas (Stop continuation,
`additionalContext` injection, hooks configuration layering and review UI)
still need their own design work.
