# Project instruction delivery

Issue #214 adds explicit delivery requirements to the existing pinned project
instruction snapshot. This entry describes the local candidate implementation;
independent review and exact-HEAD CI are still required for acceptance.

The first version covers one Run TargetPath and classifies complete discovered
sources. A required root or nested `AGENTS.md` is sent in full, with its original
source, scope, precedence and hashes. The application does not interpret natural
language to find critical clauses, infer overrides or grant execution authority.

Discovery checks each directory from the Workspace root to the target. A non-empty
`AGENTS.override.md` replaces that directory's `AGENTS.md`; an absent, empty or
explicitly ignored override falls back to `AGENTS.md`. Other invalid override
files fail discovery. Superseded and skipped sources remain visible in the
discovery explanation. `CLAUDE.md` and `.claude/CLAUDE.md` are both included,
followed by `.prayu` instructions and rules. The hidden Claude file has the scope
of its containing project directory. All selected sources retain the existing
path, size, UTF-8, redaction and workflow-only authority checks. New discovery
results still require the existing confirmed refresh to replace a pinned Run.

## Confirm a source classification

Read `GET /api/v1/runs/{run_id}/project-instructions` first. Its `pinned` snapshot
is the Run's current confirmed state; `live` is a preview of the current files.
Review the target, source paths, content SHA-256 values, precedence and diff. Use
the returned paths exactly, including Windows path canonicalization.

For a created or paused Run, send a control-authenticated request to
`POST /api/v1/runs/{run_id}/project-instructions/refresh`:

```json
{
  "target_path": "src",
  "expected_fingerprint": "<reviewed pinned snapshot fingerprint>",
  "expected_live_fingerprint": "<reviewed live snapshot fingerprint>",
  "confirm": true,
  "classifications": [
    {
      "path": "agents.md",
      "content_sha256": "<reviewed source hash>",
      "requirement": "mandatory"
    },
    {
      "path": "src/agents.md",
      "content_sha256": "<reviewed source hash>",
      "requirement": "optional"
    }
  ]
}
```

Every live source must appear exactly once. Requirements are `mandatory`,
`optional` or `excluded`. An excluded source also needs `exclusion_reason` set to
`superseded`, `not_applicable` or `withdrawn`, reflecting the operator's explicit
decision about that exact source version. No source text determines this decision.
The response includes the newly pinned immutable revision and confirming actor.
Classification changes appear in the snapshot diff even when content is unchanged.

`confirm: false` leaves the pinned revision unchanged. Missing sources, duplicate
paths, changed hashes or invalid requirements reject classification. A stale
pinned or live fingerprint requires another inspection; the application does not
silently retry against newer files. Pause an active Run before changing an existing
delivery contract.

## Pinned content, refresh and continuation

Mandatory means the selected complete pinned source must reach the actual root
model request. If required content or the complete request cannot fit the configured
context window, the Run stops before that model or tool dispatch instead of silently
dropping a rule. Existing history remains available for recovery.

Editing a file on disk does not refresh a Run. The old confirmed source remains
effective until a confirmed refresh. When classified files change, supply a complete
renewed classification using the new live hashes; an ordinary refresh cannot strip
the contract. Unchanged live files retain their pinned classifications in the preview.
Refresh waits for the Run execution lease to be released; a pause request alone
does not prove that an in-flight model or tool has stopped. An active lease causes
`FAILED_PRECONDITION` and preserves the original confirmed revision.

Compaction and reopening the local database reconstruct required rules from the
pinned snapshot. Generated-summary model requests also receive the complete
mandatory envelopes. If an auxiliary request cannot fit, it makes no model call;
the existing extractive fallback still checks mandatory delivery before the root
continuation. Pending tools also require the source-bound model-start receipt
from the same Run, turn, attempt and classified snapshot. Refreshing the contract
invalidates old pending tool origins. They stop with `FAILED_PRECONDITION` and keep
the original evidence rather than acquiring the new rule state automatically.

## Read the evidence accurately

- **Configured:** an operator confirmed `delivery.sources` in the immutable pinned
  snapshot. This proves the classification and source version.
- **Delivered:** a matching `model.started` context audit identifies a source
  included in the checked outbound request. Classified IDs include requirement,
  snapshot fingerprint, source hash and ordinal. Local regression tests additionally
  capture the actual Provider messages and verify the complete envelope content.
  Auxiliary summary starts use purpose `context_compaction`; they establish
  delivery to that summary call, separately from the root continuation.
- **Followed:** the model's actions and results satisfy the rule. Delivery evidence
  alone does not establish this; inspect the actual execution and validation evidence.

Optional omissions retain token estimates and use `omitted/budget/` audit identities.
Explicit exclusions use `omitted/operator_excluded/`. Resolve either identity against
the immutable snapshot for source path, scope, precedence and exclusion reason.
An omission is never recorded as successful delivery.
Auxiliary summaries omit optional sources as `omitted/auxiliary_scope/`, because
they are outside that request's scope, and retain the same explicit exclusion reason.

Unclassified existing Runs retain their previous optional rule-selection behavior.
No-rule Runs require no classification. Older binaries cannot validate classified
snapshot fingerprints; retain the new reader and dispatch checks when rolling back.
This issue does not implement persistent child briefs or child read-only tool loops.

See [ADR 0162](adr/0162-required-pinned-project-instruction-delivery.md) for the
contract, compatibility and recovery boundaries.
