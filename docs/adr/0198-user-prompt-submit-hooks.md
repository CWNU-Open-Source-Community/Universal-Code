# ADR 0198: User prompt submission hook boundary

- Status: Proposed
- Date: 2026-11-09
- Scope: restricted lifecycle hook events and operator prompt submission surfaces
- Updates: the hook boundary table in docs/extensions.md; complements
  ADR 0197 (operator script hooks) and ADR 0037 (controlled session message
  submission)
- References: Claude Code `UserPromptSubmit` and Codex `UserPromptSubmit` hook
  events (external parity baselines); the 2026-09-12 capability-gap review

## Problem

The restricted hook runtime fires at nine boundaries — tool pre/post, run
start/finish, session open/close, compaction, specialist scheduling, and
checkpoints — but nothing fires when the operator submits a prompt. Both
external harnesses expose a `UserPromptSubmit` event for exactly this moment,
and it is among their most-used hooks: gating prompts that contain secrets
before they reach the model or the record, enforcing prompt-format house
rules, and adding operator review taps. Without it, an operator cannot express
"never let a prompt containing a private key reach the provider" without
patching the product.

## Decision (proposed)

1. **One event, every prompt surface.** `user_prompt_submit` fires when an
   operator prompt enters a submission surface: the Thread turn endpoint
   (`executeWithPreparation`, which also routes scheduled jobs, PR follow-up
   workers, and Plan control corrections), CLI `thread send`
   (`ThreadService.Submit`), Session run chat
   (`SessionRunChatExecutor.ExecuteSessionTurnWithOptions`), and Session
   message submission (`SessionMessageSubmissionService.Submit`, including the
   current-turn steering path). Every path marks its delegated Submit calls so
   the boundary fires exactly once per attempt, and the engine propagates to
   the embedded ThreadService so the busy-turn follow-up path fires too.
   Peer-delivered messages that arrive already queued skip this boundary in
   v1 — their enqueue surface is separate and does not fire yet; this is a
   documented limitation.
2. **Before persistence.** Every surface fires the boundary after protocol
   normalization and before the message intent is reserved, the steering
   entry is queued, or any run state transition happens. A deny therefore
   rejects the submission cleanly with the fixed policy error and nothing is
   recorded; a failed hook under `failure_policy=deny` fails the submission
   closed the same way. Annotations stay in the result and the audit and are
   never injected into model context. The boundary is attempt-scoped: the
   same operation key re-fires it on every retry, matching the other
   lifecycle events.
3. **Go-encoded bounded payload.** Thread surfaces carry `thread_id`; Session
   surfaces carry `session_id` and `run_id`; both carry `content`. Oversized
   prompts clip on a UTF-8 boundary with a `truncated` flag, invalid UTF-8
   bytes are dropped once, and the encoding stays inside the existing 256 KiB
   hook-input limit. The turn and CLI surfaces fire before the run binding is
   resolved, so `run_id` is empty there; the Session surfaces resolve the
   run's workspace through its mission, the same source the `run_started`
   boundary uses. Tool-name filters are invalid on this event. Image-only
   submissions skip the boundary (empty content), and MCP-only submissions
   fire with the synthesized text while the real MCP arguments remain outside
   the payload.
4. **Same engine, same trust.** Declarative deny/annotate/record hooks and
   operator script hooks (ADR 0197) all fire; plugin manifests cannot declare
   anything beyond the declarative actions. The audit ledger records the
   event; schema v224 rebuilds `plugin_hook_audits` to extend its event CHECK
   constraint to the ten boundaries while preserving rowids and every other
   constraint.

## Alternatives considered

- Firing after persistence and rolling back the intent was rejected: rollback
  across the reserve/prepare/commit chain reintroduces the partial-state
  problems the intent machinery exists to prevent.
- A separate `steering_submit` event was rejected: the operator's intent is
  the same across all four surfaces; per-surface events would fragment the
  contract and the audit trail.
- Injecting hook output as `additionalContext` was deferred with the other
  parity items: model-context injection needs its own ADR for the trust
  boundary, while deny/annotate covers the enforcement use cases here.

## Verification

- `internal/hooks` tests cover event validation, tool-filter rejection,
  script-hook admission, and payload round-trip/clipping including multibyte
  content.
- `internal/application` tests cover all four surfaces: a record hook fires
  exactly once per turn (the delegated Submit path skips its own boundary), a
  deny rejects before persistence (no user message stored, audit marked
  rejected), and a script hook without an executor fails the submission
  closed.
- `internal/store` tests cover the schema v224 rebuild: historical receipts
  preserved, `user_prompt_submit` rows admitted, unknown events rejected by
  the CHECK constraint, immutability intact.

## Consequences

Operators gain the highest-traffic parity hook: their rules now run before a
prompt reaches the provider or the record, on every submission surface. The
event joins the existing engine path with no new configuration surface, and
the four-surface contract is documented in docs/extensions.md.
