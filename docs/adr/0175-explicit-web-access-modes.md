# ADR 0175: Explicit conversation web access modes

Date: 2026-10-14

Status: Accepted for the implementation tracked by [Issue #313](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/313).

The existing `Scope.network_mode=disabled` means that no fetch hosts are
preauthorized. It still permits configured search providers and inline public
HTTPS fetch approvals. Users need a separate setting that can actually stop
these network requests, preserve exact-host rules, or authorize anonymous public
HTTPS reading without repeatedly approving each domain.

Go owns an explicit `web_access_mode` with three supported values:

- `disabled` (禁止搜索) rejects `web_search`, `source_search`, network `web_fetch`,
  and search connection probes. Saved snapshot reads and citations stay available.
- `rules` (规则搜索) uses `web_allowed_targets` as exact public DNS hostnames for
  direct webpage fetches. An empty list is valid. Out-of-scope fetches use the
  existing inline approval flow. Search retains the selected provider's behavior
  and explicitly supported per-query domain filters; fetch rules do not silently
  become a search-result filter or search-engine endpoint whitelist.
- `full` (完全访问) derives the internal `public_https` transport authority for
  anonymous public HTTPS fetches. It still uses the configured real search
  provider and public source connectors. A missing search provider remains a
  visible unavailable state. Private addresses, localhost, metadata hosts,
  unsafe redirects and DNS answers retain their existing transport rejection.

缓存搜索 and 索引搜索 have no implementation or API enum values. The UI shows them
as unavailable future options.

The setting is independent of `network_mode`/`allowed_targets`, operation
approval policy, file permissions, command runtime, browser controls, and sandbox
selection. Selecting `full` grants the described web-reading authority only.
Cookies, browser login sessions and private credentials are not passed to webpage
fetches. Provider-native search retains its separately configured provider
credential boundary.

New UI Run and Thread creation sends an explicit mode. API callers that omit it
retain the legacy behavior. Optional fields are appended to Scope JSON; migration
192 preserves existing Mission and Run snapshots byte-for-byte. Public scope
views show the effective legacy mode (`rules`, or `full` for historical transport
snapshots that actually contained `public_https`) without inventing stored intent.
Controlled creation fingerprints include the explicit selection and normalized
targets, while legacy retry fingerprints remain stable.

`POST /api/v1/runs/{run_id}/web-access` accepts
`run_web_access_control.v1`, `expected_mode_revision`, `web_access_mode`, optional
`web_allowed_targets` and `reason`, with `Idempotency-Key` in the header and a
Go-owned `http_control` requester. It appends an audited Run mode snapshot and a
digest-only replay record in one SQLite transaction. A created or paused Run with
no active execution lease is required. Exact-key retries return the original
snapshot; a changed intent or stale revision returns conflict. SQLite independently
rejects broad rule values, unauthorized scope changes and modifications of the
operation ledger.

Selection replaces the complete webpage rule list, enabling narrowing and
revocation. Mode revision and capability generation fence old calls. A selection
also invalidates older conversation fetch grants; subsequent explicit approvals
can establish new grants. Search diagnostics takes a bounded ordinary Run lease
before dispatch and rechecks the mode, so a concurrent mode change cannot
authorize a stale probe. An active Turn or workspace mutation can make the probe
temporarily unavailable; retry after that work stops. Cancellation releases the
probe lease without using the cancelled request context.

Thread successors inherit the latest explicit web preference and rule list.
Explicit context Fork/Resume carries that preference separately from historical
context's all-false authority projection. Historical approval records, command
network grants, credentials and execution leases remain governed by their
existing ownership and lifecycle checks.

Keeping only the old network flag was rejected because it cannot distinguish
disabled search from empty fetch preauthorization. Exposing `public_https` as an
allowlist entry was rejected because it would broaden the exact-host parser and
mix web policy with command scope. A separate next-run preference ledger is not
needed for the initial slice: changes apply to a quiescent current Run and its
successors.

Rollback must retain the new Scope readers and migration. Removing the product
control must not reinterpret explicit `disabled` records as legacy search-enabled
runs, or replay a committed selection with a new digest. Focused tests cover
creation, replacement, refused broad rules, replay/reopen, stale fences, legacy
compatibility, successor inheritance, transport boundaries and saved reads.
