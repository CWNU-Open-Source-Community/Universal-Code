# Host-owned MCP OAuth onboarding

Status: implemented; parent issue #319.

Go owns the OAuth authorization-code flow, PKCE verifier, callback listener,
token refresh and OS credential entry. The operator discovers protected-resource
metadata, confirms an issuer and supplies a pre-registered public-client ID or
explicitly selects a hosted HTTPS Client ID Metadata Document. The latter uses
the SDK CIMD configuration and requires advertised authorization-server support.
The issuer must occur in the resource's validated metadata. SDK v1.8.0 implements
PKCE and issuer-response validation. HTTPS metadata and token requests prohibit
redirects; token requests bind the exact token endpoint and resource audience.
Endpoints may use different origins from the issuer when declared by its
validated metadata. The host projects SDK-generated matching client identities
with empty secrets into public-client form authentication; mismatched identities
or nonempty secrets are rejected before transmission.

A temporary IPv4 loopback listener has an unpredictable port and one callback
path. Host, method, state, issuer, duplicate parameters and lifetime are checked.
Login expires after five minutes; cancellation closes the listener and invalidates
the pending result. Restart discards pending logins. Public status distinguishes
pending, failed, cancelled, expired and locally stored credentials. The operator
handoff URL contains an ephemeral OAuth state; access/refresh tokens and the PKCE
verifier never enter HTTP replies, SQLite, logs or model-visible results.

OAuth credentials use a versioned envelope in the existing OS store, bound to
the descriptor fingerprint and endpoint with a fresh session revision per login.
Refresh rotates tokens while preserving that revision. Reauthentication, deletion
or static-token replacement changes the revision. Credential writes and refresh
share a process lock; final writes recheck the existing envelope and binding.
Token responses may omit expiry. Such entries omit public `expires_at`; the host
does not invent a lifetime or proactively refresh an unknown-expiry token.
OS-store size limits remain enforced; oversized tokens fail without plaintext
fallback. Cross-process OS-store writes cannot be transactional with SQLite;
conflicting readback is reported as unverified.

Login neither approves discovery nor enables capabilities. MCP discovery and
execution retain existing review, scope and dispatch guards. No SDK automatic
401 retry is installed: expired credentials refresh before authorized dispatch;
rejected remote credentials require an explicit operator action. Local removal
does not assert remote revocation. Public clients requiring fixed redirect ports,
confidential clients and dynamic registration are outside this initial profile.

Verification covers a local TLS issuer, a real browser loopback callback and a
synthetic OS-store adapter. The MCP fixture verifies that login leaves discovery
blocked until review, and that authorized discovery redacts returned token text.
External accounts and the platform OS-store adapter have not been exercised.
