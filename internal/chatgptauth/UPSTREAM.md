This package adapts the Apache-2.0 licensed `internal/chatgptauth` from
[alibaba/open-code-review PR #1718](https://github.com/alibaba/open-code-review/pull/1718),
merge commit `3694a716c30e09a724ae39a37d4e307bbd6e902b` (following #1622).
Original source copyright and SPDX headers are retained.

Universal-Code changes: an explicit directory and OS credential key store;
AES-256-GCM encryption of the complete account database, including pending
refresh responses; caller-owned browser presentation through a one-use local
URL whose ticket differs from OAuth state; and model discovery caching guarded
by registration identity and revision. Production endpoints remain OpenAI's
official authorization issuer and public API. Test fixtures use only local
HTTP servers and synthetic keys/tokens.

The local revision represents a login session: login, logout and terminal
revocation invalidate it; ordinary token rotation preserves it. Concurrent
login recovery compares the persisted token response as well as that revision,
so a stale login cannot overwrite a completed refresh. This lets Universal-Code
keep an existing model runtime bound to the same account across token refreshes.

The refresh-response commit and verification ordering, receipt-time JWT
verification, account revision checks, cross-process session lock, and strict
callback parsing are retained from upstream. Signing out clears credentials
and cached models while preserving the issued registration and host identifier.
