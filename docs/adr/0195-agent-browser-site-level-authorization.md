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
   document checks remain mandatory for document-bound actions. Origin addition
   uses a dedicated no-action operation; existing navigation, click, type, key,
   scroll and upload tools reject `browser_origin_add`. Approval mutates only the
   declared set and never dispatches an accompanying browser action. Any later
   action requires a separate invocation under the updated authority.
5. Isolation and effect approval. Disposable profile, download quarantine and
   release, evidence chain and credential restrictions remain in force. The current
   click/type/key approval path consults the ledger only when the model supplies
   `sensitive_intent`; this is insufficient as the scoped Ask/Auto boundary. The host
   action policy must require exact per-call approval for potential external writes
   or deletes, conservatively including actions it cannot establish as read-only.
   Omitting a model-supplied intent cannot bypass that decision. Uploads always
   require external-write approval. Page-initiated writes also need enforcement:
   scoped sessions initially block form submissions and fetch/XHR write requests
   before dispatch, even on declared origins. After `Fetch.requestPaused`, the host
   classifies the request and creates a dedicated durable pending request-approval
   proposal for a potential write. Its fingerprint binds the browser session,
   runtime instance, paused request ID, initiating document identity and epoch,
   authority generation, permission revision, method, canonical destination and
   digest of the exact complete request-body bytes. Requests whose effects cannot
   be classified, or whose complete body cannot be captured and bound, are blocked.
   The existing operator approval UI presents the proposal; the model and page
   cannot approve it. Tool approval cannot grant this request approval or ambient
   network-write authority.

   The host keeps the request paused only until a finite expiry recorded on the
   proposal. Approval can resume only that still-live paused request in the same
   runtime, with matching request/body, document, current authority and permission
   bindings. Under a dispatch fence shared with invalidation, the host atomically
   rechecks those bindings and consumes the approval once before issuing
   `Fetch.continueRequest` for the unchanged request. Denial, expiry, subsequent
   navigation or document change, session close, runtime restart, or authority or
   permission change invalidates the proposal and aborts any still-live paused
   request. Recovery invalidates outstanding proposals rather than reconstructing
   requests; neither a restart nor a failed continuation permits background replay.
   Phase 1 must implement this lifecycle before scoped Ask/Auto is exposed. Full
   unscoped sessions retain their existing activation and action policy.

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
   any narrower origin-count or URL-byte limits at all these boundaries. The network
   boundary must block undeclared destinations for browser-generated CORS preflight
   `OPTIONS` requests too, including traffic not intercepted by Fetch; literal-only
   mode retains this requirement. Enforce
   the page-initiated write rule from Decision 5 at the request boundary; URL
   membership alone cannot authorize a form submit or fetch/XHR write. Implement
   the dedicated durable request proposal and existing-UI operator review, bounded
   pause, fenced single-use continuation, and invalidation/abort on the lifecycle
   changes listed in Decision 5. Recovery must invalidate pending proposals without
   replaying requests; unbound complete bodies remain blocked.
2. Authority variant: ask/auto validation with scoped authorities; Full keeps the
   unscoped behavior; the desktop takeover mode interacts with scoped sessions
   unchanged. Before enabling scoped Ask/Auto, implement host-derived effect
   approval so omitted sensitive intent cannot turn an external write into an
   automatic action. Unclassified effects require per-call approval.
3. Per-origin additions: a dedicated no-action origin-add operation is the only
   operation accepting `browser_origin_add`. Its schema and normalization reject
   action fields, and existing browser-action schemas and normalizers reject this
   effect. Bind the operation identity into the approval fingerprint (the approval
   ActionClass derives as `browser_browser_origin_add`), with approval ledger
   integration, the distinct canonical origin field and cold-session preflight
   described above, and append-only declared-set mutation with receipts. The
   approved mutation dispatches no navigation or other browser action. Origin-set
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

### Phase 3 transition semantics (designed)

This subsection is the binding contract for the Phase 3 implementation PR and
incorporates the OCR delegate design review against the Phase 1 code. It assumes the
Phase 2 ask/auto wiring (currentAuthority, readiness, worker catalogue gates) lands
first: this design binds the mechanism, and until that wiring exists no production
path reaches it, so the implementation PR delivers mechanism and tests only.

1. Single authority holder with a derivation chain. The browser slot owns the only
   mutable current authority, and it is the source of truth for the declared origin
   set in both states: a cold slot carries the pending declared set, a launched slot
   carries the live one. The derivation chain must carry that set into every call
   authority — `agentBrowserAuthorityForSlot` copies the scope fields alongside the
   identity fields, and the advertisement path derives scoped authorities for
   ask/auto from the slot's declared set — otherwise the first mutation leaves every
   subsequent advertisement and `check` failing ("authority changed") and the
   session is bricked. Because `currentAuthority` derives from the immutable
   `RunConfig.BrowserOrigins`, the slot's set overrides that derivation whenever the
   slot carries a mutated set: `authorityLocked` rebuilds the scope fields from the
   slot's declared origins (an absent slot or an unmutated slot falls back to the
   Run config), and the launch consumes the corrected set. The runtime authority
   record itself
   (`browserruntime.AgentBrowserAuthority`) intentionally stays without scope
   fields, so a scope mutation does not change the value the runtime monitor
   reports; the runtime holds its authority-and-scope pair in one atomic holder
   swapped through the same transition.
2. The real cancel points, fenced. The launch closure's first comparison
   (`received != runtimeAuthority`) is unaffected by a scope mutation because the
   runtime authority value does not change; the monitor would cancel the browser
   through the closure's second half, `checkResource`, which captured the launch
   call authority including its old declared origins. The transition therefore
   rebuilds that half without a captured-authority comparison at all: the closure
   fetches the slot's current authority under `s.mu` and runs the store-level live
   recheck (permission revocation, and the worker path's parent/workspace binding
   checks) against it — identity fields are transition-invariant, so no previous-
   acceptance window is needed and a monitor wakeup during or after the swap always
   validates against the current authority. Every runtime CheckAuthority consumer
   (monitor, `begin`, `check`) reads the atomic holder. Lock ordering: the
   transition runs under the slot action lock (an origin-add is itself an action, so
   no browser action is in flight) and takes `s.mu` inside it; the monitor reads the
   holder and flag without the action lock. Slot-side authority reads and writes
   stay under `s.mu` everywhere, including the `CloseWorker` session-id snapshot.
3. Fencing old generations. The durable call row binds the exact pre-mutation
   `AuthorityJSON`, and the dispatch fence is `s.agentBrowser.check` — at
   supervisor dispatch preparation and again inside the slot action lock — which
   fails an old-generation call with its existing named "Agent browser authority
   changed" error once the new authority is published. `checkDurableDispatch`
   re-encodes the row's own authority and must not be presented as the generation
   fence; it remains the started-durable-call identity check. There is no bypass
   path: worker dispatches converge on the same `ExecuteAgentBrowserAction` checks.
4. The origin-add call's own transition exemption. The approved origin-add
   operation mutates the scope mid-dispatch, so its own post-action recheck and the
   supervisor result-persistence recheck compare against the post-mutation
   authority instead of the call's pre-mutation authority (the approval bound that
   pre-mutation authority by design; the mutation is the call's approved effect).
   The exemption is identifier-bound: the slot records
   `{callID, preAuthority, postAuthority}` for the approved mutation, and both
   checkpoints accept a call exactly when its call identifier matches the recorded
   mutation, its authority equals the recorded pre-authority, and the slot's current
   equals the recorded post-authority — sibling calls prepared before the mutation
   carry the same pre-authority but a different identifier and stay fenced. Both
   checkpoints keep their revocation semantics for every other action. A
   failed mutation (bound exceeded, malformed origin) consumes the call and returns
   a named error; retrying requires a new call, consistent with existing actions.
5. Cold-session preflight and payload. The operation's payload is
   `{version, origin, sensitive_intent}` where `origin` is the canonical new origin
   (the normalizer canonicalizes and rejects action fields — url, snapshot,
   element_ref, deltas, key, value, mode, workspace_path, target and viewport are
   all absent), `sensitive_intent.effect` is `browser_origin_add`, its `target` is
   the same canonical origin (the live-document target contract is not overloaded),
   and `document_epoch` is 0 — the shared normalizer waives the epoch-positive rule
   for this effect only. The "navigate first" gate and the live-document sensitive
   gate admit this operation with no runtime, and the dedicated preflight (supervisor
   and worker variants) skips the runtime and document checks, validating the
   canonical origin, the prior scope fingerprint, the authority generation and the
   permission revision instead. The approval proposal keeps
   `ToolName: agent_browser_sensitive` with `ActionClass: browser_browser_origin_add`
   (derived from the effect), and the existing payload-plus-authority fingerprint
   already binds origin, prior scope fingerprint, generation and permission revision,
   so no fingerprint scheme changes. Phase 3 is root-only: the worker catalogue and
   worker preflight do not offer the operation; extending it to specialists is later
   work.
6. Receipts and replay. The approved mutation dispatches no browser action; its
   result envelope `agent_browser_origin_add.v1` (the registered protocol literal;
   the payload version is the family's dynamic `.v2` and needs no registration)
   carries the canonical origin, the prior and new scope fingerprints, and the new
   authority generation. The consumed-call registry still refuses a re-dispatch of
   the same call ("no changes were made" wording — a replay must never read as the
   origin having been added); a second, new origin-add call for an already-declared
   origin is a set-membership no-op receipt under the post-mutation authority.
   A cold-slot rotation inherits the slot's declared set into the successor slot, so
   an approved addition survives mode or permission rotation. The service
   `Capabilities.SupportedActions` gains the operation; the runtime `Status`
   action table does not (it lists runtime-executed actions only). The desktop
   approval panel gains a label mapping for the new action class.
7. Wiring inventory. Gateway registration, supervisor and worker catalogue paths
   (the latter wired for the capability but not offering the operation), refresh
   path, capability list, recorded-actor case, domain classifier, artifact source
   scopes, the Supervisor ledger CHECK migration (v221: three enum sites, rowid
   preservation, `LatestSchemaVersion` 221), protocol registration of the receipt
   envelope literal, the preflight branches above, the two post-action exemptions,
   the derivation-chain change in point 1, the `CloseWorker` snapshot fix, the
   slot-key comment rewrite, and the desktop approval label mapping.

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
  rejected under the initial literal-only policy. A controlled undeclared target
  must receive no CORS preflight `OPTIONS` request, including in literal-only mode.
- Effects: click/type/key calls omitting sensitive intent still require approval
  when they may write externally; rejection dispatches no action.
- Request approvals: page-triggered form submissions and fetch/XHR POSTs remain
  paused without network dispatch while a durable proposal awaits operator review,
  including on declared origins and without a tool call. Only approval matching the
  live session/runtime/request, initiating document, authority and permission
  bindings, method, canonical destination and complete body digest continues the
  unchanged request once. Altered bindings/bodies, incomplete body capture, model
  or page approval, duplicate consumption and late approval are rejected. Denial,
  expiry, navigation/document change, close, restart and authority/permission
  changes invalidate the proposal and abort the live request; approval racing these
  changes cannot dispatch a stale request, and recovery performs no replay.
- Additions: cold-session approval works before first navigation; denied, stale or
  modified origins cannot expand scope; an approved addition preserves the live
  runtime and takeover state while invalidating old-generation calls and approvals.
  Origin-add intent on any existing action is rejected; approval of the dedicated
  operation changes the set without navigation, clicks or other browser effects.
- Real-browser E2E: declared-origin navigation works, and a form write with exact
  operator request approval dispatches once; an unapproved or invalidated form
  request never dispatches. Out-of-scope navigation and addition flows produce the
  documented errors and receipts.
