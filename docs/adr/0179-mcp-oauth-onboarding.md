# Host-owned MCP OAuth onboarding

Status: implemented; parent issue #319.

Go owns the OAuth authorization-code flow, PKCE verifier, callback listener,
token refresh and OS-protected encrypted storage. The operator discovers protected-resource
metadata, confirms an issuer and chooses dynamic registration, a pre-registered
client, or a hosted HTTPS Client ID Metadata Document. The latter uses
the SDK CIMD configuration and requires advertised authorization-server support.
The issuer must occur in the resource's validated metadata. SDK v1.8.0 implements
PKCE and issuer-response validation. HTTPS metadata and token requests prohibit
redirects; token requests bind the exact token endpoint and resource audience.
Endpoints may use different origins from the issuer when declared by its
validated metadata. The host projects SDK-generated matching client identities
with empty secrets into public-client form authentication; mismatched identities
are rejected before transmission. Confidential clients use their selected
`client_secret_post` or `client_secret_basic` method at the exact token endpoint.

A temporary IPv4 loopback listener has an unpredictable port and one callback
path. Host, method, state, issuer, duplicate parameters and lifetime are checked.
Login expires after five minutes; cancellation closes the listener and invalidates
the pending result. Restart discards pending logins. Public status distinguishes
pending, failed, cancelled, expired and locally stored credentials. The operator
handoff URL contains an ephemeral OAuth state; access/refresh tokens and the PKCE
verifier never enter HTTP replies, SQLite, logs or model-visible results.

OAuth credentials use a versioned envelope, bound to
the descriptor fingerprint and endpoint with a fresh session revision per login.
Refresh rotates tokens while preserving that revision. Reauthentication, deletion
or static-token replacement changes the revision. Credential writes and refresh
share a process lock; final writes recheck the existing envelope and binding.
The MCP credential-store view writes AES-256-GCM ciphertext under `credentials/mcp`
and keeps only a fresh key and opaque file reference in the dedicated OS entry.
Static credentials still use the original OS store and 2560-byte bound. Legacy
OS-resident OAuth envelopes are read without mutation and migrate on the next
successful login/refresh write. Encoded OAuth envelopes are bounded to 64 KiB,
individual access/refresh tokens to 16 KiB and confidential client secrets to 1024 bytes.
The encrypted file is atomically written and read back before publishing its OS
reference; successful commits read back the exact reference and decrypted bundle.
A process file lock covers comparison and publication for cooperating MCP stores
using the same application home.
The private atomic-file writer and platform lock are shared with ChatGPT storage;
the two features retain separate data models, keys and directories.

Token responses may omit expiry. Such entries omit public `expires_at`; the host
does not invent a lifetime or proactively refresh an unknown-expiry token.
Oversized tokens fail without plaintext fallback. Failed precommit writes preserve
the previous credential; unverified publication is never rolled back or retried.
Superseded files are removed after verified replacement. A crash can leave orphan
ciphertext, which cannot be resolved without its matching OS key/reference.
OS-store writes cannot be transactional with SQLite or non-cooperating writers;
conflicting readback is reported as unverified.

Login neither approves discovery nor enables capabilities. MCP discovery and
execution retain existing review, scope and dispatch guards. No SDK automatic
401 retry is installed: expired credentials refresh before authorized dispatch;
rejected remote credentials require an explicit operator action. Local removal
does not assert remote revocation. Fixed callback ports, confidential clients and
explicit dynamic registration are supported by the current login controls.

Verification covers a local TLS issuer, a real Chrome loopback callback and
synthetic accounts in Windows Credential Manager. The MCP fixture verifies that login leaves discovery
blocked until review, and that authorized discovery redacts returned token text.
The Windows OS-store lifecycle check is opt-in; default tests use an in-memory key store.
External account login remains separate from local protocol and public metadata checks.
