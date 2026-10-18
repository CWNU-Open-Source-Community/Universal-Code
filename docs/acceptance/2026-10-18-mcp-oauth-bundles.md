# MCP OAuth encrypted bundles and interoperability

The MCP OAuth credential view now supports encoded bundles up to 64 KiB, with
16 KiB limits on each access and refresh token and the existing 1024-byte client
secret limit. Windows Credential Manager continues to receive a small entry:
an AES-256-GCM key and opaque reference to a ciphertext file in `credentials/mcp`.
Static bearer and provider credentials retain their 2560-byte OS-store limit.

Legacy OS-resident OAuth bundles remain readable without migration on a read.
The next successful login or refresh stages and verifies ciphertext, compares
the prior credential under the file lock, publishes the OS entry, and reads back
the complete result. Descriptor, issuer/resource, client identity and login
revision checks remain in the existing OAuth and MCP authority paths. A refresh
preserves the login revision; a new login changes it. Static replacement and
local deletion use the same store view as CLI, HTTP, Desktop and runtime refresh.

Precommit failures preserve the old credential. An unverified OS write reports
failure without rollback or automatic retry and retains any possibly referenced
ciphertext for status recovery. Verified replacement removes its predecessor's
ciphertext. A crash can leave orphan encrypted files; those files are never
resolved without a matching OS key/reference. SQLite registration changes and
uncoordinated OS-store writers remain outside the credential-file transaction.
The process file lock coordinates stores using the same application home.

## Local verification

The focused Go commands use `GOMAXPROCS=2`, `GOGC=50`, and `-p 1`.

- `go test -p 1 ./internal/mcpoauth ./internal/credential ./internal/chatgptauth ./internal/privatefile`
  passed. ChatGPT storage continues to use the shared private atomic writer and
  Windows/Unix lock; its existing store/recovery tests remain unchanged.
- The MCP runtime tests passed with access tokens longer than 2560 bytes:
  refresh is checked against current execution authority, the rotated bearer is
  redacted from results, and a server's 401 response does not replay the MCP call
  or repeat refresh. Manual stdio configuration rejects both OAuth bundle and
  encrypted-key reference prefixes.
- Encrypted-store tests passed for restart, token rotation, static replacement,
  deletion, legacy migration, size limits, invalid tokens, tampered or missing
  files/keys, wrong-reference substitution, failed writes, failed readback and
  competing compare-and-swap operations.
- `CYBERAGENT_MCP_OS_STORE_TEST=1 go test -p 1 ./internal/mcpoauth -run
  '^TestOAuthSystemStoreLifecycle$' -count=1 -v` passed on Windows. It used a
  random synthetic credential name in the real Windows Credential Manager,
  a local TLS issuer, real loopback callback and code exchange, long access and
  refresh tokens, reconstruction of the store, eight concurrent resolutions
  producing one refresh, an authenticated MCP request and local deletion.
  The synthetic credential was cleaned up. No external account was involved.
- Frontend OAuth and credential controls: both focused test files passed
  (16 tests). The authorization text now describes encrypted local files and
  keys protected by the OS credential store.
- `go test -race -p 1 ./internal/mcpoauth -run
  'TestEncryptedStoreCAS|TestOAuthLarge|TestOAuthUnverified|TestOAuthRejected'
  -count=1` passed (1.620s).
- `go test -p 1 ./internal/application ./internal/httpapi ./internal/app
  ./internal/desktop -run
  'TestMCPOAuth|TestMCPCredential|TestMCPEnvironmentCredential|TestDesktopMCPCredential'
  -count=1` passed. Application: 0.481s; HTTP API: 3.807s; CLI: 1.069s;
  Desktop: 0.974s. This covers confidential-client CLI login with a long token,
  reconstructed CLI status, static replacement/ciphertext cleanup, deletion,
  DCR/CIMD/confidential HTTP login, discovery review, token redaction and the
  existing Desktop static-credential path.

### Browser and Windows credential-store acceptance

An isolated Chrome session drove the actual `MCPCredentialControls` and
`MCPOAuthControls` components against `TestMCPOAuthHTTPAndBrowserFixture/auto`.
The test used `CYBERAGENT_OAUTH_BROWSER_SYSTEM_STORE=1`, a random synthetic
credential name, temporary SQLite/ciphertext directories and a local TLS issuer.
Only this isolated browser trusted the fixture's self-signed certificate.

Observed steps: discover the issuer; start and cancel the first login; start
again with automatic DCR; open the issuer page; approve its synthetic account;
return through the real loopback callback; observe `authorized` and
`windows_credential_manager`; reload the page and observe the stored status;
remove the local token and observe verified absence. The test then exited with
PASS (218.701s), including OS-entry cleanup. Chrome, Vite and the fixture server
were stopped, and temporary frontend entry files and fixture credentials removed.

Screenshots were retained locally under `output/playwright/`:
`local-issuer.png`, `authorized-windows-store.png`, and
`deleted-windows-store.png`. They contain synthetic account/status information.
The temporary entry mounted the real controls directly rather than the full
desktop shell. The frontend test command was
`npm test -- --run src/components/mcp-oauth-controls.test.tsx
src/components/mcp-credential-controls.test.tsx`.

To start the bounded browser backend again, set an absolute path in
`CYBERAGENT_OAUTH_BROWSER_FIXTURE` and opt in with
`CYBERAGENT_OAUTH_BROWSER_SYSTEM_STORE=1`, then run
`go test -p 1 ./internal/httpapi -run
'^TestMCPOAuthHTTPAndBrowserFixture/auto$' -count=1 -v`.
Its private JSON supplies local API connection data and the server descriptor
for a temporary controls page. Set `CYBERAGENT_API_TARGET` for Vite, keep the
fixture JSON private, and create `<fixture-path>.stop` to terminate the test and
clean the synthetic credential. No account login or permanent MCP registration
is needed. OS-store execution was verified on Windows; Unix lock code was moved
unchanged and was not executed on this host.

## Anonymous external metadata

On 2026-10-18 the opt-in production discovery check passed against the
[Sentry MCP endpoint](https://mcp.sentry.dev/), using a proxy supplied only to the
test process. The check performs anonymous GET requests and does not dynamically
register a client, open account authorization or call an authenticated tool.

| Request | Observed result |
| --- | --- |
| `https://mcp.sentry.dev/.well-known/oauth-protected-resource/mcp` | 200; resource `https://mcp.sentry.dev/mcp`, issuer `https://mcp.sentry.dev` |
| `https://mcp.sentry.dev/.well-known/oauth-authorization-server` | 200; authorization-code/refresh-token, S256, DCR, CIMD, and `none`/`client_secret_post`/`client_secret_basic` advertised |
| `https://mcp.sentry.dev/mcp` without credentials | 401 |
| Root protected-resource metadata URL | 404; the endpoint-specific metadata above works |

Reproduction in PowerShell, with proxy lines only when required by the host:

```powershell
$env:GOMAXPROCS = '2'
$env:GOGC = '50'
$env:HTTP_PROXY = 'http://127.0.0.1:7890'
$env:HTTPS_PROXY = 'http://127.0.0.1:7890'
$env:CYBERAGENT_MCP_PUBLIC_METADATA_TEST = '1'
go test -p 1 ./internal/mcpoauth -run '^TestOAuthPublicMetadataInterop$' -count=1 -v
```

The default production OAuth HTTP client still disables proxy inheritance;
this test explicitly injects the transport. Direct connections on this host
returned TLS connection resets. The result establishes discovery compatibility
through that injected transport, not a successful default-CLI connection.

[Sentry requires OAuth account authorization](https://mcp.sentry.dev/).
Real external account login, externally issued long-token persistence and
external refresh/revocation remain unverified. The local TLS issuer demonstrates
the implemented protocol lifecycle using synthetic accounts.

## Review coverage

OCR delegate 1.12.9 ran `preview --format json` and `rule --format json` in
workspace mode against base `24162e3d`. Its Go and TypeScript rule groups were
applied by the host reviewer. All 14 selected path/status entries were reviewed;
none were skipped (selected-file coverage 100%). The 12 default exclusions
(7 tests, 3 Markdown files, 2 deleted source paths) were also checked manually.
There were no changed generated files. This accounts for all 26 path/status
entries: 15 full-file reads and 11 change/context reads, not a full-file review
of every large host file. The two platform-lock moves count as separate old and
new paths in this workspace preview.

| File | Coverage |
| --- | --- |
| `internal/app/api_command.go` | Partial: changed wiring, constructor context and lifecycle ownership |
| `internal/app/app.go` | Partial: changed manager construction and application-home initialization |
| `internal/app/mcp_command.go` | Partial: credential view, status/set/delete and OAuth command lifecycle |
| `internal/application/mcp_credential.go` | Partial: changed comment plus status/mutation and lock ordering |
| `internal/application/mcp_oauth.go` | Full |
| `internal/chatgptauth/store.go` | Partial: extracted write/lock bodies, caller contract and failure injection |
| `internal/chatgptauth/lock_unix.go` (deleted) | Full; compared to relocated implementation |
| `internal/chatgptauth/lock_windows.go` (deleted) | Full; compared to relocated implementation |
| `internal/desktop/control_plane.go` | Partial: home, raw provider store, MCP manager/control wiring and cleanup ownership |
| `internal/mcp/client_manual_config.go` | Partial: all changed literal/environment credential rejection paths |
| `internal/mcpoauth/oauth.go` | Partial: all changes, bundle bounds, binding/revision, persistence and refresh callers |
| `internal/mcpoauth/store.go` | Full |
| `internal/privatefile/file.go` | Full |
| `internal/privatefile/lock_unix.go` | Full |
| `internal/privatefile/lock_windows.go` | Full |
| `web/src/components/mcp-oauth-controls.tsx` | Full |
| `internal/app/mcp_oauth_command_test.go` | Full |
| `internal/httpapi/mcp_oauth_control_test.go` | Full |
| `internal/mcp/client_manual_config_test.go` | Partial: changed cases and their fixture/assertion context |
| `internal/mcp/client_oauth_runtime_test.go` | Partial: changed scenarios and current-authority/no-replay assertions |
| `internal/mcpoauth/oauth_test.go` | Partial: all changes and lifecycle fixture context |
| `internal/mcpoauth/public_metadata_test.go` | Full |
| `internal/mcpoauth/store_test.go` | Full |
| `docs/mcp-oauth.md` | Full |
| `docs/adr/0179-mcp-oauth-onboarding.md` | Full |
| This acceptance record | Full |

No unresolved blocking finding remained. Review corrected stale ADR wording
about supported registration methods and clarified same-home file-lock scope.
The encrypted view retains no open file handle or background worker; each file
operation closes its own handle. Raw preview/rules and the per-file coverage
manifest were retained locally in `.tmp/mcp-oauth-review/`.
