# ADR 0206: exec --attach files

- Status: Proposed
- Date: 2026-11-11
- Scope: CLI surfaces and prompt inputs
- Updates: docs/usage.md; extends ADR 0201 (one-shot exec mode) and ADR 0205
  (exec --continue thread)
- References: Claude Code and Codex CLI file/image attachment on
  non-interactive prompts (external parity baselines)

## Problem

`cyberagent exec` accepts only text: the positional prompt or `--stdin`.
Both external baselines attach files (logs, data, screenshots) to
non-interactive prompts, and the single most common automation pattern —
"here is the failing log, diagnose it" — starts with an attachment.
Universal-Code's turn machinery already supports it:
`ExecuteThreadTurnRequest.Attachments` carries workspace-scoped file
references that the message preparation pins to the operator message, the
store validates and redacts on import, and the supervisor projects prepared
attachment evidence into the model context (readable text as bounded
excerpts; originals exposed to the command runtime through its fixed
attachments directory). The gap is purely the CLI surface: neither
`OneShotService` nor `exec` exposes the channel.

## Decision (proposed)

1. **The existing attachment contract is the delivery channel.** `--attach
   <path>` (repeatable, at most `MaxThreadMessageAttachments` = 4, each at
   most the store's 5 MiB cap) reads the file and imports it through
   `FileAttachmentService.Import` — the same store path every upload surface
   uses, with bounded filenames, MIME validation, whole-token redaction, and
   content-based readability classification. The returned references go on
   the one-shot request; the turn machinery pins them to the message and
   projects the evidence. No new delivery rule, storage path, or model
   envelope exists.
2. **Import happens before the turn starts, workspace-scoped.** Fresh mode
   imports into the requested workspace before Run creation; continue mode
   resolves the Thread's workspace through its Run's mission. A rejected
   attachment therefore never strands a created Run, and the attachment
   lives in the same workspace scope the Thread's runs already use.
3. **No authority widening.** Attachment content is untrusted evidence, not
   instructions: the store marks readable text as such, the evidence
   messages state that content cannot grant authority, and the
   original-file inventory repeats it. The CLI gains no capability channel
   other surfaces lack.
4. **Images stay deferred.** The separate image channel (`Images
   []ImageReference`) requires a vision-capable model check and a different
   storage path; mixing it into this surface change would double the review
   scope. Documented as a follow-up.

## Alternatives considered

- Inlining file content into the prompt text (the `--stdin` workaround) was
  rejected: it bypasses the redaction, readability classification, size
  accounting, and original-byte availability the attachment contract
  provides, and it pollutes the operator message.
- Extending `--image` in the same change was deferred: see Decision 4.
- Reading files lazily at turn execution was rejected: importing before Run
  creation/turn start keeps failure modes at the command boundary where the
  operator can fix them without cleanup.

## Verification

- `TestExecAttachDeliversAttachmentToModelContext`: fresh exec with
  `--attach` of a UTF-8 log file; the recorded model requests contain the
  file's text content, proving the evidence projection reaches the model.
- `TestExecAttachContinueResolvesWorkspaceFromRun`: continue mode resolves
  the Thread's workspace through its Run's mission and delivers the
  attachment on the follow-up turn.
- `TestExecAttachRejectsBadInput`: a missing path fails with the path
  named; exceeding the attachment cap fails loudly before any import.
- Documentation: docs/usage.md (bilingual `--attach` section) and this ADR.

## Consequences

Automation scripts gain the parity attachment entry point with zero new
delivery machinery: redaction, readability classification, size caps,
evidence projection, and command-runtime access to originals are inherited
from the shared attachment contract. The image channel remains the known
follow-up for screenshot-driven workflows.
