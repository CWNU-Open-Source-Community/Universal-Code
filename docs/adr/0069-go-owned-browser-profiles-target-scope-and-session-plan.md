# ADR 0069: Go-Owned Browser Profiles, Target Scope, and Session Plan

## Status

Accepted for P11-A1/A2/A3 on 2026-07-22. This is a non-schema browser-control
foundation; SQLite remains at v84 and OpenAPI remains unchanged.

## Context

Prayu needs a browser tool for Code-mode Web QA and authorized CTF Web analysis. The
Desktop currently embeds WebView2 only to render the React console. Loading adversarial
pages into that renderer would mix the trusted UI with target content, cookies, navigation,
and process lifetime. A second unsafe option would be to reuse a user's personal Chrome
profile or globally add Chromium flags such as `--disable-web-security` and
`--ignore-certificate-errors`.

The browser boundary must therefore be a Go-owned Run tool, not a TypeScript security
boundary or an extension of the Wails renderer. Web content is untrusted evidence and
cannot grant authority, change target scope, or instruct the Agent to weaken Policy.

## Decision

The new `internal/browserruntime` package fixes three immutable profiles:

- `safe-web` allows exact public origins and localhost for Code-mode inspection while
  retaining normal same-origin, CSP, mixed-content, and TLS behavior.
- `ctf-lab` requires approval and permits exact private/loopback targets plus planned
  interception, mutation, replay, and cookie editing. Browser-security relaxation remains
  unavailable.
- `ctf-instrumented` additionally permits explicit origin-policy, mixed-content, and
  certificate-error relaxation requests. It requires explicit risk acknowledgement, a
  future containerized worker, and `instrumented` evidence labeling.

Every fixed descriptor requires an ephemeral profile and forbids personal profiles,
extensions, password stores, and host-filesystem access. Descriptor authority is entirely
false. Profile selection describes a requested envelope and is not a process or network
grant.

`browser_target_scope.v1` canonicalizes at most eight exact HTTP(S) origins. It normalizes
IDNA names, schemes, ports, IPv4, and IPv6; rejects credentials, fragments, malformed hosts,
backslashes, and non-HTTP schemes; and sorts/deduplicates the approved origins. Every
navigation and redirect must match an exact origin. Every DNS result must be checked again.
Safe Web rejects public-name rebinding to private or loopback addresses. CTF profiles may
resolve an explicitly approved target to private space, but cloud metadata, link-local,
multicast, unspecified, and reserved address classes remain blocked. Literal-IP targets
must resolve to the identical address.

Operator narrowing adds optional `max_origins` and `max_url_bytes` to the target scope.
Zero/absent fields retain the builtin ceiling and keep existing v1 JSON and fingerprints
unchanged. Positive bounds cannot exceed the builtin ceiling and enter the scope digest;
construction, validation, navigation, redirects, and intercepted requests enforce them.
The URL byte bound covers both the supplied URL and its canonical form. A session plan
requires its scope bounds to match its effective limits. Older readers discard the new
fields and reject narrowed scopes on fingerprint mismatch; new readers accept existing
default scopes. Previously built plans with narrowed origin/URL limits but no matching
scope bounds must be rebuilt. The builtin profile descriptor and fingerprint stay fixed.

`browser_session_plan.v1` binds Session, Run, Workspace, fixed profile fingerprint, target
scope fingerprint, proxy endpoint, optional system credential reference, requested tooling,
isolation, evidence class, limits, backend class, and sorted launch blockers. Proxy URL
userinfo is forbidden and plaintext credentials are never retained. A disposable-profile
token is derived from non-secret identities and scope. The model never owns profile cleanup.

Every plan is start-blocked. Process start, network access, profile write, request mutation,
request replay, credential read, proxy connection, and Artifact commit remain false. The
fixed blockers include executable identity, runtime sandbox, process-tree lifecycle, exact
network enforcement, append-only audit, bounded Artifact handoff, operator approval when
required, and container isolation for the instrumented profile.

## Verification

Focused unit and race tests cover the fixed registry, authority tampering, exact-origin
navigation, scheme/port changes, private/loopback separation, cloud metadata, DNS rebinding,
literal-IP drift, proxy userinfo and credential references, instrumented acknowledgement,
isolation, launch blockers, and fingerprint reconstruction. Two ten-second fuzz runs made
about 8.2 million URL and proxy parser executions without a crash or accepted-value
reconstruction failure.

The first vulnerability scan found a reachable infinite-loop advisory, GO-2026-5970, in
`golang.org/x/text v0.37.0` through IDNA normalization. The dependency was upgraded to fixed
v0.39.0 before delivery. The repeated targeted scan reports zero reachable vulnerabilities;
the repository's pre-existing module-only unimported `x/crypto/openpgp` advisory remains
unreachable and has no fixed release.

The integrated gate passed full `go test ./...` in 364.5 seconds on the corrected dependency
graph. Full vet, targeted staticcheck/race,
38 files and 137 React tests, strict TypeScript/Vite production build, and zero-vulnerability
npm audit pass. No browser, Provider, Shell, LocalRunner, Docker container, attack traffic,
credential read, filesystem profile, SQLite/Event/Artifact write, or network request ran.

## Consequences

- Wails WebView2 remains a trusted UI renderer and cannot become the CTF browser.
- Code and Cyber browser behavior are separate fixed profiles under one Go control plane.
- Global unsafe Chromium switches are not exposed as a generic toggle.
- The next batch may add executable discovery/identity, disposable-profile ownership, and a
  Disabled/Fake CDP transport, but must not silently convert this plan into launch authority.
- Real navigation, interception, replay, and instrumented security relaxation require later
  product adapters, approval receipts, process cleanup, audit, and network-enforcement gates.
