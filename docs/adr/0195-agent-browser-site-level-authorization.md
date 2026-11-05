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
evidence, an approval ledger for declared sensitive effects).

The agent browser runtime also has no origin enforcement of its own: any valid
http/https URL navigates. Origin controls exist (`scope.go` — declared origins,
redirect revalidation, resolved-address re-checks, link-local and cloud-metadata
denial) but were never wired into the agent browser; they serve the safe-web and
Full CDP products.

## Decision (proposed)

1. Operator-declared origins per browser session. A new operator surface (session
   start and the Desktop browser view) records 1..8 exact scheme+host+port origins
   under the existing TargetScope identity rules. The agent browser uses its own
   disposable `agentBrowserProfile`, which is not in the
   builtin registry that `NewTargetScope`, `TargetScope.Validate`, and the navigation
   and resolved-address authorizers look up. All four paths must use a trusted,
   fingerprint-bound agent-browser policy and effective limits; adapting only the
   constructor would leave the resulting scope unusable. Policy comes from the host,
   never from a caller-supplied profile descriptor. The declaration is operator input
   only: the model,
   page content, project config, and skills can never add, widen, or re-declare
   origins.
2. Scoped sessions under Ask and Auto. A scoped variant of the agent-browser
   authority carries the declared origins as a fingerprint-bound field and validates
   for ask and auto modes. Full activation keeps today's unscoped behavior unchanged.
3. Runtime enforcement. Navigation and redirects authorize against the declared
   origins with the revalidation discipline the safe-web product uses. Out-of-scope
   navigation fails with a named error that states which origin was missing; page
   content remains untrusted evidence throughout. Subresource requests are a
   required part of the scope boundary: the agent-browser transport speaks bare
   `Page.navigate` with
   an event whitelist and has no `Fetch.enable/requestPaused` interception today
   (that machinery lives in the restricted transport), so enforcing subresources
   requires extending the agent-browser transport with the Fetch domain and new
   whitelist entries, or migrating the enforcement point — Phase 1 names the
   choice. Scope enforcement for documents, redirects, and subresources must be in
   place before scoped Ask/Auto sessions are exposed. URL authorization alone does
   not bind Chromium's actual connection address: Phase 1 must enforce the resolved
   address used by each target connection, or initially restrict all targets and
   redirects to authorized literal addresses until that enforcement is available.
4. Per-origin additions through the approval ledger. An out-of-scope navigation never
   silently widens the scope: the agent may propose adding one exact origin through
   the existing sensitive-intent per-call path (new effect `browser_origin_add`), and
   only an operator approval mutates the session's declared set (append-only,
   fingerprint-bound, audited). A distinct `origin` payload field identifies the
   addition; it must not overload `sensitive_intent.target`, whose current contract
   is the live document URL. The approval binds the canonical new origin, prior
   origin-set fingerprint, authority generation, session identity and permission
   revision, and rejects additions beyond the effective origin-count bound. A
   dedicated preflight validates those fields even before first
   navigation, without requiring a running browser or a document epoch. Existing
   document checks remain mandatory for document-bound actions.
5. Isolation and effect approval. Disposable profile, download quarantine and
   release, evidence chain and credential restrictions remain in force. The current
   click/type/key approval path consults the ledger only when the model supplies
   `sensitive_intent`; this is insufficient as the scoped Ask/Auto boundary. The host
   action policy must require exact per-call approval for potential external writes
   or deletes, conservatively including actions it cannot establish as read-only.
   Omitting a model-supplied intent cannot bypass that decision. Uploads always
   require external-write approval. Full unscoped sessions retain their existing
   activation and action policy.

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
- Prompt injection inside a declared origin remains possible; untrusted-evidence
  rules, bounded receipts, quarantines and host-enforced effect approval constrain
  the actions available to scoped sessions.
- DNS-rebinding protection depends on checking the address used for the actual
  target connection. `scope.go` provides address policy, while restricted CDP's
  `Fetch.requestPaused` handler currently authorizes URLs before continuing requests;
  its local CDP dialer does not enforce Chromium target addresses. Phase 1 must close
  this gap or use the literal-target restriction before claiming this protection.
- The upload and download quarantine features are origin-independent and work
  identically in scoped sessions.

## Implementation phases (separate PRs, each reviewed)

1. Scope plumbing: declared origins on the start request and authority
   (fingerprint-bound), runtime enforcement on navigation and redirects, named
   out-of-scope errors, compatible TargetScope construction, validation and both
   authorizers for the agent-browser policy, and the subresource enforcement point
   (agent-browser transport Fetch
   extension or restricted-transport migration) with its transport whitelist
   changes. Bind resolved-address checks to actual target connections, or restrict
   the initial implementation to literal targets. Preserve profile ceilings and
   any narrower origin-count or URL-byte limits at all these boundaries.
2. Authority variant: ask/auto validation with scoped authorities; Full keeps the
   unscoped behavior; the desktop takeover mode interacts with scoped sessions
   unchanged. Before enabling scoped Ask/Auto, implement host-derived effect
   approval so omitted sensitive intent cannot turn an external write into an
   automatic action. Unclassified effects require per-call approval.
3. Per-origin additions: `browser_origin_add` sensitive intent (new effect in
   `NormalizeAgentBrowserPayload`'s effect whitelist and the tool JSON schema enum —
   the approval ActionClass derives as
   `browser_browser_origin_add`), approval ledger
   integration, the distinct canonical origin field and cold-session preflight
   described above, and append-only declared-set mutation with receipts. Origin-set
   changes bump the authority generation: calls carrying the previous generation
   fail with a named error, pending approvals for the old set are invalidated, and
   the takeover window resolves against the regenerated authority. The runtime's
   launch callback currently captures the original authority; changing only the
   stored generation would make its monitor cancel the browser. Phase 3 must define
   an atomic scope/authority transition with safe callback rebinding or an equivalent
   owned transition, preserving the live browser while all subsequent checks use the
   new authority. Concurrent old-generation dispatch must be fenced before the new
   scope is used; the exact locking and in-flight-call semantics are designed and
   reviewed before implementation.
4. Operator surfaces: session-start declaration (CLI and Desktop), live view origin
   display, docs.

## Verification outline

- Authority: scoped ask/auto accepted, unscoped ask/auto rejected, Full unchanged,
  fingerprint binds the exact origin set, tampered sets fail closed.
- Scope compatibility: agent-browser scopes pass construction and both authorizers;
  forged policies, widened limits and scopes exceeding effective limits fail.
- Runtime: in-scope navigation proceeds, out-of-scope navigation fails with the
  missing origin named, redirects to undeclared origins fail, additions only through
  approved mutations.
- Network: undeclared subresources are blocked; hostname resolution changes to
  forbidden addresses fail at the actual connection boundary, or hostnames are
  rejected under the initial literal-only policy.
- Effects: click/type/key calls omitting sensitive intent still require approval
  when they may write externally; rejection dispatches no action.
- Additions: cold-session approval works before first navigation; denied, stale or
  modified origins cannot expand scope; an approved addition preserves the live
  runtime and takeover state while invalidating old-generation calls and approvals.
- Real-browser E2E: declared-origin navigation and form flow works; out-of-scope
  navigation and addition flows produce the documented errors and receipts.
