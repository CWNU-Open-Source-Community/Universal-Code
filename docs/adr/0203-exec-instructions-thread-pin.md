# ADR 0203: exec --instructions as a Thread context pin

- Status: Proposed
- Date: 2026-11-10
- Scope: CLI surfaces and per-invocation operator guidance
- Updates: docs/usage.md; extends ADR 0201 (one-shot exec mode); relies on the
  Thread context pin contract (operator controls, integrity-checked delivery)
- References: Claude Code `--append-system-prompt` (external parity baseline)

## Problem

`cyberagent exec` (ADR 0201) takes the task prompt but no per-invocation
guidance channel. Operators automating the CLI need to customize agent
behavior for one invocation — output format, commit style, focus constraints —
without writing files into the workspace or editing global configuration.
Claude Code exposes `--append-system-prompt` for exactly this; Codex reaches
for config overrides. Universal-Code has a native equivalent already: Thread
context pins are operator-confirmed fixed task requirements delivered as
protected sections on every turn. The gap is that exec has no way to fix one.

## Decision (proposed)

1. **The Thread context pin is the delivery channel.** `--instructions
   "<text>"` adds the text as an operator context pin (`ControlContextPin`,
   action `add`, `cli_exec_operator`) on the invocation's Thread before the
   first turn. The ordinary pin machinery does the rest: integrity-checked
   storage (`ContentSHA256`), protected-section selection, delivery on every
   turn including follow-ups, visibility in the Thread's pin state, and
   edit/remove through the same operator surface the interactive UI uses. No
   new envelope type, Run config field, or delivery rule is introduced.
2. **Validate before any Run exists.** The text is normalized up front
   (`normalizeExecInstructions`): an unset flag means no fixed instructions, a
   whitespace-only value fails loudly, and otherwise the exact Thread pin
   contract applies (nonempty UTF-8, no NUL, at most 8192 characters) plus the
   pin storage gate that rejects protected secret content. Failing early
   means a rejected text can never strand a created Run.
3. **No authority widening.** Pin text is delivered under the existing pin
   semantics — workflow guidance below system policy, never a grant of tools,
   permissions, or policy exceptions. The CLI operator's authority comes from
   the CLI process itself, not from the instruction text, so exec gains no
   capability channel other flags lack.
4. **Output contract unchanged.** The reply and `--json` shape stay exactly
   as ADR 0201 defined them; the pin is observable in the Thread's context
   pin state rather than in exec output.

## Alternatives considered

- Appending a synthetic source to the discovered project-instruction snapshot
  was rejected: the run's instruction state machine compares the pinned
  snapshot against live discovery, so a synthetic source would mark every
  exec run's instructions stale forever, and a later operator refresh would
  silently drop the fixed text.
- A new pinned `RunConfig` channel with a dedicated envelope type was
  deferred: it touches domain validation, both supervisor and worker context
  assembly, and the system-prompt policy text — a new design surface for a
  need the pin channel already covers.
- Prepending the text to the user prompt was rejected: it conflates guidance
  with the task message, is not delivered on follow-up turns, and bypasses
  the integrity and visibility machinery pins provide.

## Verification

- `TestNormalizeExecInstructions` covers the unset, trimmed, whitespace-only,
  rune-cap, NUL, and protected-secret cases.
- `TestExecInstructionsFixesThreadContextPinAndDeliversIt` drives exec through
  the offline provider and asserts, on durable state, exactly one active pin
  with the fixed text, `cli_exec_operator` provenance, and integrity fields;
  and, on the recorded model requests, that the text was actually delivered
  into the model context.
- `TestExecInstructionsWhitespaceOnlyFailsBeforeRunCreation` pins the
  fail-loud contract before run creation.
- Documentation: docs/usage.md (bilingual) and this ADR.

## Consequences

Scripts gain the parity `--instructions` flag with zero new delivery
machinery: every guarantee (per-turn delivery, integrity, visibility,
editability, no authority widening) is inherited from the Thread pin
contract. Each exec invocation owns a fresh Thread, so its fixed pin is
private to that invocation; any follow-up Thread work (steering, successor
Runs, interactive sends) sees and manages the same pin in the shared pin
state.
