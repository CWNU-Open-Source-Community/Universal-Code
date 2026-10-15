# Manual MCP connection configuration

Status: accepted.

## Context

Manual registrations currently retain only an absolute stdio executable and
arguments, or a fixed HTTPS endpoint and one managed bearer credential. Native
Agent Plugins already resolve working directories, explicit environments and
HTTP headers through Go's frozen launch contract. Manual operators need the same
connection inputs without acquiring a Plugin package.

## Decision

Add `mcp-client.v3` for manual descriptors. Historical `mcp-client.v1` descriptors
and source-bound native `mcp-client.v2` descriptors retain their validation and
fingerprints. The new optional fields are `cwd`, `env`, `env_credential_refs`, and
`headers`. Only stdio accepts the first three; only HTTP accepts headers and the
existing `credential_ref` bearer binding. Native descriptors retain source
references and cannot carry these manual launch fields.

Go resolves a configured working directory to an existing absolute directory,
then reuses the existing fixed executable, executable digest and SDK transport.
Arguments remain an ordered array; command strings are never shell-parsed.
Only the existing small host environment allowlist and explicitly declared
values reach the subprocess. Environment credential references name OS-store
entries; plaintext is resolved into a transient launch, redacted from peer
responses, and rechecked before dispatch. OAuth envelopes cannot become stdio
environment values. Changes or missing credentials fail before dispatch.

Manual HTTP accepts HTTPS or HTTP on exact localhost/loopback hosts, while
retaining the existing restrictions on userinfo, queries, fragments, redirects
and alternate SSE endpoints. Fixed headers are bounded, case-unambiguous and
non-sensitive. Authentication, proxy, HTTP framing and MCP session headers are
host-owned and rejected in manual configuration. Secrets use credential
references rather than literal headers, arguments or environment values.

The extension registration form exposes these fields and reports registration
errors without losing input. Stdio environment credentials use the existing
metadata-only OS-store lifecycle endpoint with exact descriptor, scope and
reference bindings. Credential entry does not approve discovery or tool calls.
The CLI stages the same bounded descriptor JSON and stores secrets separately.

## Persistence, authority and failure

The existing immutable descriptor JSON column stores only non-secret values and
references. No SQL shape or retained row protocol changes, so no migration or
rewriting of historical rows is needed. New fields participate in the reviewed
descriptor fingerprint. Reusing an ID with changed configuration conflicts;
operators stage a new ID and review it. Credentials and resolved launches never
enter SQLite, events, models or returned errors.

Existing discovery review, capability review, scope, Policy, Approval, execution
leases and send guards remain authoritative. Registration stays inert. Invalid
configuration rejects registration; unavailable directories, executables or
credentials reject connection. Shared OS-store names targeting different
transports or endpoints cannot be mutated through the settings lifecycle.

## Alternatives

Extending v1 would let older implementations silently ignore new connection
fields and would obscure persisted semantics. A second launch framework would
duplicate the existing digest, environment and SDK transport boundaries. The
selected versioned adapter reuses those contracts while keeping native package
resolution and manual registration distinct.
