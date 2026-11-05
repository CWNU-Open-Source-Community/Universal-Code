# ADR 0195: Agent browser site-level authorization for Ask and Auto

- Status: Proposed
- Date: 2026-11-05
- Scope: agent browser session authority and runtime navigation controls
- Updates: the live-Full-activation requirement for the agent browser; complements the
  TargetScope machinery the safe-web and Full CDP products use
- References: [issue #491](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/491) (P1 authorization granularity), ADR 0082 (CDP permission
  ceiling), ADR 0083 (restricted loopback runtime), ADR 0103 (Full access start token)

## Problem

The agent browser requires live Full activation at two layers — authority creation
(`AgentBrowserService.currentAuthority` refuses without `RunExecutionPermissionFull`)
and authority validation (`AgentBrowserCallAuthority.Validate` requires
`PermissionMode.IncludesFullAccess()`). Ask and Auto operators therefore cannot use the
browser at all, and issue #491 records the capability gap against Codex, whose in-app
browser pairs per-site allow/block lists with the same isolation boundary
Universal-Code already enforces (disposable profile, no identity, quarantine, hashed
evidence, per-call approvals for sensitive effects).

The agent browser runtime also has no origin enforcement of its own: any valid
http/https URL navigates. Origin controls exist (`scope.go` — declared origins,
redirect revalidation, resolved-address re-checks, link-local and cloud-metadata
denial) but were never wired into the agent browser; they serve the safe-web and
Full CDP products.

## Decision (proposed)

1. Operator-declared origins per browser session. A new operator surface (session
   start and the Desktop browser view) records 1..8 exact scheme+host+port origins
   under the existing TargetScope identity rules. One adaptation is required: the
   agent browser uses its own disposable `agentBrowserProfile`, which is not in the
   builtin registry that `NewTargetScope` looks up, so the scope constructor gains a
   variant that takes the limits explicitly (the 1..8 origin bound already matches
   the builtin `MaxOrigins`). The declaration is operator input only: the model,
   page content, project config, and skills can never add, widen, or re-declare
   origins.
2. Scoped sessions under Ask and Auto. A scoped variant of the agent-browser
   authority carries the declared origins as a fingerprint-bound field and validates
   for ask and auto modes. Full activation keeps today's unscoped behavior unchanged.
3. Runtime enforcement. Navigation and redirects authorize against the declared
   origins with the revalidation discipline the safe-web product uses. Out-of-scope
   navigation fails with a named error that states which origin was missing; page
   content remains untrusted evidence throughout. Subresource requests are a
   separate work item: the agent-browser transport speaks bare `Page.navigate` with
   an event whitelist and has no `Fetch.enable/requestPaused` interception today
   (that machinery lives in the restricted transport), so enforcing subresources
   requires extending the agent-browser transport with the Fetch domain and new
   whitelist entries, or migrating the enforcement point — Phase 1 names the
   choice.
4. Per-origin additions through the approval ledger. An out-of-scope navigation never
   silently widens the scope: the agent may propose adding one exact origin through
   the existing sensitive-intent per-call path (new effect `browser_origin_add`), and
   only an operator approval mutates the session's declared set (append-only,
   fingerprint-bound, audited).
5. Isolation unchanged. Disposable profile, download quarantine and release, evidence
   chain, per-call approvals for click/type/upload external effects, and the
   no-credentials rules are identical for scoped and unscoped sessions.

## Alternatives considered

- Chrome-extension identity (Codex's approach for signed-in sites): rejected —
  Universal-Code has no extension surface, and identity must never enter the isolated
  browser. The takeover mode remains the manual-login path.
- Explicit block lists (the deny half of Codex's per-site allow/block model):
  unnecessary — navigation outside the declared allow set already fails closed, so a
  block list adds no enforcement the default-deny set does not already provide.
- Origins from project config: rejected — `.prayu` is untrusted input and must never
  widen runtime reach; only an operator surface may declare origins.
- Per-navigation operator approval for every request: rejected — unusable for real
  workflows; per-origin declaration plus per-origin additions is the granularity the
  issue asks for and Codex validated.

## Consequences

- Ask/Auto operators gain browser workflows (staging previews, documentation, form
  checks) without global Full activation; Full sessions keep every current
  capability, including unscoped navigation.
- The approval ledger grows `browser_origin_add` records; the desktop takeover mode
  and the live view remain the manual paths for `waiting_user`.
- Prompt injection inside a declared origin remains possible; the untrusted-evidence
  rules, bounded receipts, quarantines, and per-call approvals carry over unchanged,
  and the declared set bounds the blast radius.
- DNS rebinding and redirect escapes are handled by the same revalidation machinery
  the safe-web product uses; `scope.go`'s link-local and metadata denials apply.
- The upload and download quarantine features are origin-independent and work
  identically in scoped sessions.

## Implementation phases (separate PRs, each reviewed)

1. Scope plumbing: declared origins on the start request and authority
   (fingerprint-bound), runtime enforcement on navigation and redirects, named
   out-of-scope errors, the TargetScope constructor variant for the agent-browser
   profile, and the subresource enforcement point (agent-browser transport Fetch
   extension or restricted-transport migration) with its transport whitelist
   changes.
2. Authority variant: ask/auto validation with scoped authorities; Full keeps the
   unscoped behavior; the desktop takeover mode interacts with scoped sessions
   unchanged.
3. Per-origin additions: `browser_origin_add` sensitive intent (new effect in
   `NormalizeAgentBrowserPayload`'s effect whitelist and the tool JSON schema enum —
   the only two enumeration points; the approval ActionClass derives as
   `browser_browser_origin_add` with no ledger conflicts), approval ledger
   integration, and append-only declared-set mutation with receipts. Origin-set
   changes bump the authority generation: calls carrying the previous generation
   fail with a named error, pending approvals for the old set are invalidated, and
   the takeover window resolves against the regenerated authority — the exact
   concurrency semantics are designed in Phase 3 before implementation.
4. Operator surfaces: session-start declaration (CLI and Desktop), live view origin
   display, docs.

## Verification outline

- Authority: scoped ask/auto accepted, unscoped ask/auto rejected, Full unchanged,
  fingerprint binds the exact origin set, tampered sets fail closed.
- Runtime: in-scope navigation proceeds, out-of-scope navigation fails with the
  missing origin named, redirects to undeclared origins fail, additions only through
  approved mutations.
- Real-browser E2E: declared-origin navigation and form flow works; out-of-scope
  navigation and addition flows produce the documented errors and receipts.
