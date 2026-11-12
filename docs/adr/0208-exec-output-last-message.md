# ADR 0208: exec --output-last-message

- Status: Proposed
- Date: 2026-11-11
- Scope: CLI surfaces and output contract
- Updates: docs/usage.md; extends ADR 0201 (one-shot exec mode)
- References: Codex `exec --output-last-message <file>` (external parity
  baseline)

## Problem

The exec reply reaches stdout, so a cron job or CI step that wants the
reply as an artifact must redirect and parse the command's whole output —
which also carries usage hints or structured JSON depending on the format
flags. (When a streaming-output mode lands, the same file sink removes the
stream-parsing workaround there too.) Codex solves this with an
explicit `--output-last-message <file>`: the final agent message lands in a
file, independent of the output format, and downstream steps consume the
file without parsing anything.

## Decision (proposed)

1. **A file sink beside the unchanged output contract.** `--output-last-message
   <file>` writes the final committed assistant reply to the file (created or
   truncated, Unix permission bits 0600 — Windows stays ACL-controlled)
   before the standard output is produced, in every output mode. The
   existing output formats behave exactly as before; the file is additive.
2. **Write failures fail the command before any output.** The file is
   written first, so a failed write (unwritable path, missing directory)
   returns an error naming the flag instead of printing a reply the caller
   believes was persisted.
3. **Pauses record their committed reply.** The write happens before the
   stop-reason check, so a paused turn records whatever reply was committed
   at the pause point (a wait action commits its public message), and a
   turn that failed before committing any reply leaves an empty file —
   scripts debugging a failed invocation get the same durable file either
   way.

## Alternatives considered

- Writing the file only on `turn_finish` was rejected: scripts handling a
  paused invocation would see a stale file from a previous run; recording
  the empty/partial state is more honest.
- A separate `exec reply <run-id>` read-back command was rejected: it adds
  a second surface and a lookup step where one flag closes the loop.
- Directing the reply to stderr alone was rejected as the general
  solution: mixing streams is fragile in shells that conflate them, and the
  file is parse-free for downstream steps.

## Verification

- `TestExecOutputLastMessageWritesReply`: `--json` mode writes the reply to
  the file and the JSON output still carries it.
- `TestExecOutputLastMessageRecordsPauseReply`: a paused turn (root_wait)
  exits non-zero and its file records the committed pause reply.
- `TestExecOutputLastMessageWriteFailureFailsBeforeOutput`: an unwritable
  target fails the command with the flag named and nothing on stdout.
- Documentation: docs/usage.md (bilingual paragraph).

## Consequences

Cron and CI consumers get a parse-free artifact for the reply without
waiting on the stacked exec PR series — this change is based on current
main and independent of it. The output contract is unchanged otherwise.
