# MCP OAuth login

Register a manual HTTPS MCP server with a dedicated `credential_ref`. In its
extension settings card, open **OAuth browser authorization**, confirm metadata
access and select **Discover OAuth issuers**. Review the returned issuer, enter
the service's client registration method, and start authorization.
**Automatic registration** uses a supplied client ID, or Dynamic Client Registration
(DCR) when the ID is blank. **Dynamic client registration** explicitly requests a
public native client at the issuer's advertised registration endpoint. The server
must return the requested `none` token authentication and callback URI.
For a pre-registered confidential client, enter its optional client secret; the
host supports `client_secret_post` and `client_secret_basic`. The secret is cleared
from the form when login starts and is saved with the token bundle only after
successful authorization. Enter it again when starting a new login.
Alternatively, select **Client metadata URL** and enter an already hosted HTTPS
Client ID Metadata Document URL. The issuer must advertise CIMD support, and the
document must declare the matching client ID and permitted loopback callbacks.
Open the returned link in a browser on the Go host computer. The client registration
must allow `http://127.0.0.1:<dynamic-port>/oauth/callback`. To use a registered fixed
URI, enter its port under **Callback port**; the exact URI is displayed before
login. An occupied port fails instead of silently selecting another port.
Registration method and callback port apply to this login; they are not saved as
server configuration. An existing authorized session uses its stored client
credentials for refresh.

The Go host verifies protected-resource and authorization-server metadata,
requires PKCE S256, and owns the temporary callback listener. The current profile
requires HTTPS metadata and endpoints declared by the confirmed issuer;
authorization and token endpoints may use different origins. Redirects on host
metadata and token requests are rejected. Both OAuth server metadata and OpenID discovery are
supported through the MCP SDK. Public clients have no client secret. DCR requests also reject redirects.

The settings state comes from Go. Pending login expires after five minutes;
**Cancel authorization** targets that exact login. Refreshing or reopening the
page retains the pending status, but its one-time handoff link is available only
in the initiating control response; cancel and restart if that link was lost.
After the callback, `authorized` means the token was saved and read back from the
system credential store. Approve discovery, rediscover capabilities, and review
them before using the MCP server in a task.

The CLI uses the same service:

```text
cyberagent mcp credential oauth-discover <server-id> --confirm
cyberagent mcp credential oauth-login <server-id> --issuer https://issuer.example --client-id public-client-id --confirm
cyberagent mcp credential oauth-login <server-id> --issuer https://issuer.example --client-id-mode metadata_url --client-id https://client.example/metadata.json --confirm
cyberagent mcp credential oauth-login <server-id> --issuer https://issuer.example --client-id-mode auto --callback-port 8766 --confirm
cyberagent mcp credential oauth-login <server-id> --issuer https://issuer.example --client-id private-client-id --client-secret-env MCP_CLIENT_SECRET --callback-port 8766 --confirm
cyberagent mcp credential oauth-status <server-id>
```

`oauth-login` prints the browser handoff and waits for completion; Ctrl+C cancels
its callback. Tokens stay in the system store. The entry's encoded token bundle
must fit the store's 2560-byte limit, including the confidential client secret;
larger bundles report failure and leave the previous credential intact. Refresh-token
rotation happens under a credential lock when an authorized MCP request needs a
fresh access token. Failed refresh or server rejection requires operator recovery;
the client does not automatically replay an MCP operation.
If the issuer omits token expiry, status omits the expiry timestamp; no lifetime
is invented and the token is used until the server rejects it or it is removed.

**Remove local token** also removes an OAuth entry. It does not claim remote
revocation; use the service's account settings to revoke remote access. Changing
the descriptor invalidates the saved OAuth binding. Pending login is transient
and must restart after the Go process exits.

Local validation uses synthetic accounts, a TLS issuer, an isolated SQLite
database and an in-memory credential-store adapter. Browser acceptance exercises
the actual React controls and Go HTTP handlers. No external account was used.
See [ADR 0179](adr/0179-mcp-oauth-onboarding.md).

The registration choices and fixed-port setup follow the concepts documented by
[Codex](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) and
[Claude Code](https://code.claude.com/docs/en/mcp#authenticate-with-remote-mcp-servers).
Universal Code has no hosted CIMD document: use a metadata URL for your own client
registration, never another product's client identity. Its callback path is
`/oauth/callback`, with a listener bound only to `127.0.0.1`.
