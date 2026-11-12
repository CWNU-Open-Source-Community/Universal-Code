# ADR 0210: user_prompt_submit hooks with additional context

- Status: Proposed (design for a maintainer decision; depends on the
  operator script hook runtime and the user_prompt_submit boundary, both
  PR-carried, and on the hook-provenance mechanism defined in ADR 0209's
  Decision 4)
- Date: 2026-11-11
- Scope: hook events, session provenance, and per-turn context
- Updates: docs/extensions.md; extends ADR 0197 (script hook output
  contract), ADR 0209 (hook-provenance delivery mechanism), and the
  user_prompt_submit boundary
- References: Claude Code UserPromptSubmit hook
  (`hookSpecificOutput.additionalContext` — text injected into the model's
  context for that turn; external parity baseline)

## Problem

Per-turn dynamic context — current ticket state, environment facts, the
output of a status command — cannot enter the agent's context unless the
operator pastes it into the prompt on every turn. Claude Code's
UserPromptSubmit hook solves this: a script runs when the prompt is
submitted and its `additionalContext` output is injected into that turn's
model context. Universal-Code has the boundary (the user_prompt_submit
event fires before prompt persistence and can reject the prompt) but no
injection channel: the hook can deny or record, and its text never
reaches the model.

The reason this needs a design decision is the same as ADR 0209's: hook
text reaching the model must not acquire operator authority. The naive
version — append the hook output to the prompt — would commit
script-authored text as instruction-authorized operator input.

## Decision (proposed)

1. **A bounded `additional_context` field in the script output
   contract.** A script hook declaring the user_prompt_submit event may
   include `additional_context` in its bounded JSON output: a UTF-8
   string whose final stored form is truncated at 8 KiB per hook
   (truncation, not rejection — and
   the field counts toward the executor's existing 16 KiB whole-output
   cap, which still fails the script when the total overflows; JSON
   escaping can inflate an 8 KiB decoded string well past 8 KiB raw).
   The existing output fields (reason, annotations) are bounds-checked
   but not redacted — this field is model-bound, so the design adds
   redaction for it explicitly: the executor applies the shared
   whole-token redaction to the context before storage, mirroring the
   file-attachment import order (redact the whole bounded text before
   any truncation, so a secret crossing an excerpt boundary cannot
   escape). The output stays a single JSON value with the existing
   unknown-field rejection — no protocol change beyond one field plus
   its redaction step. The redaction placement differs from ADR 0209's
   stop-boundary envelope: here the executor is strictly earlier.
   Redact the complete decoded context accepted under the whole-output
   cap before taking any excerpt. Then truncate the redacted result to
   a prefix of at most 8 KiB, ending only at a complete UTF-8 code point
   and outside every redaction marker. If the byte limit would split a
   marker, omit that whole marker and the following text. Complete all
   required redaction before this final truncation; a later stage must
   not re-expand the sealed text. Seal the digest over the exact final
   bytes handed to session storage, which contain only complete markers
   and remain at most 8 KiB even when redaction expands the input.
2. **Commit atomically with the operator message as
   `SourceHookContext` provenance.** The text is stored as a session
   message of a new closed-enum source kind carrying
   `InstructionAuthorized: false` — the same authority class as the
   hook-continuation provenance ADR 0209 defines, delivered here for the
   prompt turn instead of as a steering continuation. The commit happens
   in the submission transaction, so replay-by-operation-key returns the
   committed state and a retried submission can never duplicate the
   context. A retried submission still re-executes the hook script (the
   boundary fires before the replay check) — its fresh output is
   discarded in favor of the committed state, and the intent fingerprint
   must not bind the re-run context, so racing retries with differing
   script output cannot conflict. The context is excluded from
   instruction-authorized recent-operator history retention.
3. **Go-authored trusted framing, not raw injection.** At model delivery
   the text is wrapped in a Go-authored envelope — the pattern the
   specialist continuation uses for service-generated model-visible text
   — stating that the content is hook-produced untrusted guidance below
   system policy, operator requests, and Go safety policy, and that it
   grants no tools, permissions, or policy exceptions. The envelope
   carries the hook ID, the content digest, and the turn identity —
   stamped at commit time (the run and turn do not exist when the hook
   executes; the payload carries ThreadID and prompt text only).
4. **Delivery before the operator message, one session message per
   hook.** The context sections precede the prompt in the turn's
   context, in the deterministic registration order (sorted by event,
   plugin ID, hook ID — "lowest precedence" means latest in that order
   drops first), each as its own bounded section — never concatenated
   into the operator message itself. The cap is per submission (the
   boundary fires on every content-bearing submission, including
   mid-turn steering and scheduled turns): 16 KiB per submission;
   overflow is dropped and recorded in the audit. Each context message
   consumes the turn's context/token budget and is subject to
   compaction like any session message.
5. **Failure never blocks the prompt; a denied prompt discards
   context.** user_prompt_submit hooks always run with the continue
   failure policy (validation rejects `FailureDeny`, mirroring turn_end
   and post_compaction): a script error, timeout, or audit failure
   yields no context and the prompt proceeds. The existing deny
   semantics are unchanged — a script may still reject the prompt
   outright, which remains the boundary's primary purpose — and a deny
   aborts the submission before any persistence, so context from the
   denying hook and from every later-ordered hook is discarded (the
   no-intent-reserve invariant wins; Claude Code permits block+context
   together — this design deliberately does not).
6. **Latency is explicit and bounded.** The hook runs synchronously in
   the submission path (the deny gate requires it), so a configured
   context hook adds its `timeoutMillis` to every prompt submission on
   the Threads it covers. The declaration's timeout is the operator's
   explicit latency budget; the docs state it.
7. **Diagnostics and audit.** Every result is audited (including
   context-sized and dropped-for-budget outcomes); the web diagnostics
   label map and `hookEvents` list gain nothing new (the event already
   exists) but the observation rendering shows the context digest and
   size.

## Alternatives considered

- Appending the text to the prompt (Claude Code's literal mechanism) was
  rejected: it commits script-authored text as instruction-authorized
  operator input — the authority escalation ADR 0209's review identified
  as the design's hardest requirement.
- Tool-based fetch (the agent calls a tool to read the status) was
  rejected as the alternative it is, not a replacement: it costs a turn
  decision and tool budget per fact, requires the agent to know it
  should look, and changes the authority profile (tool results are
  evidence, which is fine — but the hook's value is zero-decision,
  zero-budget injection).
- Long-term memory was rejected: memory is durable and curated; this is
  per-turn dynamic state.
- Reusing the annotation channel was rejected: annotations are
  diagnostics surfaced to the operator, not model context, and their
  bound (16 entries) serves a different contract.

## Verification (planned)

- Script contract: `additional_context` validation (UTF-8, 8 KiB,
  full bounded-input redaction before truncation), unknown-field rejection
  intact. A secret crossing an excerpt boundary is fully redacted;
  expanding markers remain complete and the final stored bytes stay
  within 8 KiB, with the digest computed from those exact bytes.
- Submission: a context-bearing hook commits exactly one
  `SourceHookContext` message in the submission transaction; replay by
  operation key returns the committed state without duplication.
- Provenance: `InstructionAuthorized: false`, excluded from
  instruction-authorized retention, envelope framing present at delivery,
  digest binds the text.
- Bounds: the 16 KiB per-turn cap drops lowest-precedence sections and
  audits the drop; per-hook 8 KiB enforced at the executor.
- Failure: script error/timeout yields no context and the prompt
  proceeds; deny still rejects the prompt.
- Boundary scope: peer-delivered turns skip the boundary (peer text is
  untrusted cross-thread data); `--continue` fires it naturally through
  the ordinary submission path.
- Documentation: docs/extensions.md hook section, ADR 0210.

## Consequences

Automation gains per-turn dynamic context with zero model-decision
overhead, under the same provenance regime ADR 0209 establishes: hook
text stays workflow guidance below operator authority, bounded, redacted,
replay-safe, and audited. The provenance mechanism is now shared by two
consumers (steering continuation and prompt context), which is the
argument for landing it as a general session source kind rather than a
one-off.
