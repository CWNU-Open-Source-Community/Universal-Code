# ADR 0211: project-scoped hook declarations with operator approval staging

- Status: Proposed (design revision after delegate review; requires the
  script hook runtime and operator source defined by ADR 0197)
- Date: 2026-11-11
- Scope: hook sources, workspace trust boundaries, and the plugin review
  surface
- Updates: docs/extensions.md; extends the operator script hook contract,
  engages the `projectconfig.CommandHooks` precedent (ADR 0173), and
  reuses the plugin review surface
- References: Claude Code project-level hook configuration. Parity note:
  Claude Code also gates project hooks behind a workspace trust dialog
  ("holds back hooks from every settings file ... until you accept the
  workspace trust dialog"), so this design is parity-plus — a
  digest-pinned per-declaration durable approval is stronger than a
  per-folder trust acceptance

## Problem

Operator hook declarations live in one global location
(`<CYBERAGENT_HOME>/hooks.d/`), so a repository cannot ship its own hook
workflow — "always run this project's linter before finishing", "post the
summary to the project tracker" — without the operator hand-configuring
every machine. Claude Code solves this with project-level hook
configuration, gated by a workspace trust dialog.

Universal-Code's foundation says more: repository content is
untrusted (`project_workflow_untrusted`), project instructions can never
grant execution, and the plugin flow exists so repo-shipped extensions
get staged, reviewed, and operator-enabled before contributing anything.
The nearest prior art, `projectconfig.CommandHooks` (ADR 0173), is
project-scoped but can only *select* operator-registered commands — it
never defines new execution. A project-level hook channel would be the
first `.prayu` mechanism that defines *new* execution, which is exactly
why it needs this design decision before any implementation.

## Decision (proposed)

1. **Project hook declarations are discovered as proposals, never
   active.** Declarations live in `.prayu/hooks.d/*.json` inside the
   workspace, discovered by the same fail-closed rules as operator
   declarations (path escapes, size bounds, malformed JSON fail closed).
   Discovery stages each proposal through a new workspace-scoped
   staging record — content digest, `staged_by`, review state — instead
   of registering it. Nothing from `.prayu` executes without an
   operator act.
2. **The approval binds a whole-declaration manifest, not a single
   script digest.** The staged record's fingerprint covers the entire
   declaration: interpreter, arguments, relative script path, event,
   actions, tool filters, failure policy, AND a helper manifest — every
   file the declaration may resolve under its scope (imported modules
   included), each with its own digest. `ScriptSpec` pins only the
   script bytes, and its interpreter and arguments are declared fields:
   a script-digest-only approval would let repository content flip the
   interpreter, change arguments, or alter filters while the approval
   stands. The manifest closes that channel: any byte drift anywhere in
   the manifest voids the approval and re-stages the declaration for
   review. Each fire must construct an immutable execution snapshot from
   the manifest and verify every captured file against its approved
   digest. Execute only that verified snapshot, including imported
   helpers; interpreter arguments and module lookup must not reopen
   mutable workspace files. Reopening paths with os.Root and checking
   digests alone leaves a check/use race and does not guarantee that the
   executed bytes match the approved manifest. A changed or incomplete
   snapshot fails closed and re-stages the declaration.
3. **The approval record is new durable state, specified.** A
   workspace-scoped `hook_approvals` record: workspace ID, declaration
   fingerprint (the manifest above), script digest, helper manifest
   digest, approver identity, approval state, timestamps; one row per
   (workspace, declaration). UC's single-operator model makes "which
   operator approves" simple — the same operator identity that stages
   plugins — and the multi-checkout consequence is stated rather than
   hidden: the same declaration discovered in N workspaces requires N
   approvals, one per workspace, with no cross-workspace reuse (a
   workspace's content is independent).
4. **Failure isolation is per-source.** The shipped loader aborts the
   whole registration merge on any anomaly, which would let one bad
   repo commit — 65 declarations, one malformed JSON — deny all tool
   execution in the workspace and take operator hooks down with it. The
   three-source merge degrades per source instead: anomalies in the
   project source drop ONLY the project source to an error state
   surfaced in diagnostics; operator and plugin hooks keep working. The
   64-entry ceiling counts ACTIVE registrations only — inert unapproved
   proposals do not consume it (they cannot be a denial-of-service
   vector). The degradation is whole-source by design: one malformed
   unapproved proposal file deactivates every approved project hook in
   that workspace until the repository fixes the file — safe (repo
   content proposes, operator disposes; operator and plugin hooks are
   unaffected) and operator-visible in diagnostics.
   The approval surface refuses the 65th active registration: an
   approval that would exceed the ceiling is rejected before
   activation, keeping existing registrations unchanged.
5. **Approved project hooks join the engine merge under the same
   authority ceiling.** Approval gates existence only: an approved
   project hook gets the same action set, continue-only policies,
   bounds, integrity fail-closed, and per-fire re-verification as
   operator hooks, and its per-source anomalies degrade alone
   (Decision 4). The engine merge becomes four sources — operator
   global (active), plugin contributed (active), project approved
   (active), project unapproved (inert proposals in staging) — and
   `MergeRegistrations` extends from two sources to four with the
   ceiling counted per Decision 4. Plugin hooks remain active and are
   never displaced by project proposals. The deterministic
   execution order extends its key with the source scope as an explicit
   new requirement (the shipped sort is event, plugin ID, hook ID).
6. **This is a real trust transfer, stated.** The shipped contract
   reserves script hooks for the operator source exclusively
   (`Engine.Replace` rejects script declarations from any other source;
   the script-hooks ADR records that "prompt injection cannot turn
   repository files into executed commands"). An approved project
   script hook is therefore strictly more authority than an enabled
   plugin can ever carry, and the approver is reviewing repo-controlled
   code in a workspace the agent itself can edit mid-run. The design
   acknowledges this as a deliberate trust transfer and leans on the
   mitigations that make it honest: the manifest approval binds
   everything, every fire executes a verified immutable manifest snapshot,
   the script environment
   is minimal and non-inherited, and the revocation is immediate. The
   reserved operator audit convention extends with an explicit
   project-source marker so audit rows answer "which workspace's hook".
7. **Removal, rotation, and scope follow discovery.** Deleting the file
   deactivates the hook (discovery observes absence); the approval
   record becomes stale and is kept for audit history. Re-adding the
   same manifest re-activates under the existing approval; any drift
   re-stages. `instructions.ignore` does not touch `.prayu/hooks.d`
   staging — it remains an instruction-discovery mechanism; this ADR
   adds nothing to it.

## Alternatives considered

- Trusting project hooks like Claude Code was rejected, and the parity
  baseline corrected: Claude Code itself gates project hooks behind the
  workspace trust dialog; this design is parity-plus (digest-pinned
  per-declaration durable approval with revocation).
- Secrets- or path-scoped auto-approval was rejected: a "safe" directory
  is still repository content, and scoping rules regress into policy
  parsing that the manifest-pinned approval model handles honestly.
- Retaining only an approval-time copy was rejected: it would keep
  executing old workflow content after repository edits. Decision 2
  instead captures and verifies a fresh immutable snapshot for each fire.
  Dynamically resolving helpers from the workspace after verification
  was rejected because a helper can change between the check and use;
  all manifest-bound execution must read the verified snapshot.
- Denying project hooks entirely (the status quo) keeps the trust model
  but leaves the gap: every repository's workflow hooks depend on
  per-machine operator memory.
- Reusing the plugin installation records with a workspace column was
  rejected: plugin installations are global packages with their own
  review lifecycle, and overloading them with per-workspace hook
  approvals would entangle two different review contracts; the
  dedicated record shares only the review surface.
- Runtime-only approval (approve per run) was rejected: it multiplies
  prompts without adding safety over a digest-pinned durable approval
  that the operator can revoke.

## Verification (planned)

- Discovery: project declarations stage as proposals; malformed or
  escaping files fail closed; the manifest pins every resolved file.
- Approval: the operator act activates a manifest-pinned declaration;
  any drift voids approval and re-stages; revocation deactivates; the
  multi-workspace N-approval rule holds. Mutating a workspace script or
  helper after snapshot verification cannot alter the bytes executed;
  execution and module lookup use only the verified snapshot.
- Engine merge: approved project hooks execute under the same ceilings;
  unapproved proposals stay inert and ceiling-free; a malformed project
  source degrades alone while operator and plugin hooks keep working.
- Authority: project hooks cannot widen tools, permissions, budget, or
  their own authority; every result is audited with its scope marker.
- Documentation: docs/extensions.md hook sources section, ADR 0211.

## Consequences

Repositories can ship hook workflow, gated by a manifest-pinned,
workspace-scoped, revocable operator approval — the same
proposes-operator-disposes contract as every other repo-shipped
extension, strengthened for the fact that this channel defines new
execution. The hook surface becomes three-scope under one authority
ceiling with per-source failure isolation, and the operator's trust
decision is explicit, durable, and revocable. The design adds no new
execution substrate: project hooks run on the same script runtime, under
the same rules, with the manifest gate added in front.
