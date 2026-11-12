# ADR 0207: exec --image attachments

- Status: Proposed
- Date: 2026-11-11
- Scope: CLI surfaces and prompt inputs
- Updates: docs/usage.md; completes ADR 0206's deferred image channel;
  extends ADR 0201 (one-shot exec mode) and ADR 0205 (exec --continue)
- References: Claude Code `-p --images` and Codex image input (external
  parity baselines)

## Problem

ADR 0206 shipped `exec --attach` for files and explicitly deferred images:
the image channel is separate from file attachments (its own storage, its
own reference type, and a vision-capability gate). Both external baselines
accept images on non-interactive prompts, and screenshot-driven automation
("what does this error dialog say?") is the second most common attachment
pattern after logs. The turn machinery already supports the full flow —
`ExecuteThreadTurnRequest.Images` carries workspace-scoped image
observations, the store decodes and bounds every upload, message preparation
refuses images without a confirmed vision model, and the supervisor
delivers image bytes with exact provenance to vision-capable models. The
gap is the CLI surface.

## Decision (proposed)

1. **The existing image contract is the delivery channel.** `--image <path>`
   (repeatable, at most `MaxThreadMessageImages` = 4) reads the file and
   saves it through `SaveWorkspaceImage` — the same store path every
   operator upload uses, restricted to the published formats (PNG, JPEG,
   WebP) and bounded by decode-time checks (5 MiB, 8192 px per side, 16
   Mpx). The returned references go on the one-shot request; the turn
   machinery pins them to the message and delivers the bytes to the model.
2. **Explicit format gate, no content sniffing.** The MIME type comes from
   the file extension mapped to exactly the three published formats; any
   other extension fails with a clear "unsupported format" error before
   import. Sniffing would silently accept renamed binaries that the store
   then rejects deeper in the flow with a less actionable message.
3. **Import before the turn, workspace-scoped.** Identical placement to
   ADR 0206's file attachments: fresh mode imports before Run creation,
   continue mode resolves the Thread's workspace through its Run's mission.
   A rejected image never strands a created Run.
4. **The vision gate is inherited and fail-closed.** Message preparation
   refuses images unless the selected model's capability is confirmed
   (`DescribeVision`, not protocol-level support), before any model call.
   The exec surface adds no way around it; the error tells the operator to
   select a confirmed vision model.
5. **No authority widening.** Image bytes are untrusted data with exact
   provenance; the evidence projection and the delivery path state it, and
   the CLI gains no capability channel other surfaces lack.

## Alternatives considered

- Content sniffing for the format decision was rejected: see Decision 2.
- Extending `--attach` to detect image files automatically was rejected:
  the two channels have different storage, validation, delivery, and
  capability semantics; an automatic split would make the failure modes
  (readable text vs vision gate) implicit instead of operator-visible.
- Bypassing the vision gate with a warning was rejected: delivering image
  bytes to a non-vision model degrades into provider errors or silent
  context pollution; the fail-closed refusal matches the machinery's own
  behavior for interactive sends.

## Verification

- `TestExecImageDeliversBytesToVisionModel`: fresh exec with `--image` of a
  generated PNG and a vision-capable stub; the recorded model requests
  carry an `llm.ImagePart` with the exact bytes, media type, and SHA-256 of
  the file.
- `TestExecImageRejectsUnsupportedFormat`: a `.txt` path fails with the
  explicit format error before any import.
- `TestExecImageRequiresVisionCapability`: the same image against a
  non-vision stub fails loudly with the vision gate error before any model
  call.
- Documentation: docs/usage.md (bilingual `--image` section) and this ADR.

## Consequences

Screenshot-driven automation gets the parity image entry point with zero
new delivery machinery: decode bounds, format gate, vision capability
check, provenance-carrying delivery, and evidence projection are inherited
from the shared image contract. The exec surface now covers the complete
attachment story — text files, other files, and images — with each channel
keeping its own semantics.
