# CI checks and execution time

PRs and main pushes select checks from their changed inputs. **Go control
plane** aggregates the selected Go checks, Store shards, and authority race
checks. **Full CI verification** is a separate result produced only by a complete
run; releases require that result for their exact source commit.

## What runs

| Change | Central CI | Desktop release workflow |
| --- | --- | --- |
| Documentation | Module, protocol, Surface and documentation contract checks | No PR package build |
| Frontend | Frontend types, API drift, tests, build, dependency audit, embedded bundle checks, and real workbench browser journeys | No PR package build |
| OpenAPI snapshot | Frontend checks plus the Go DTO-to-OpenAPI golden check | No PR package build |
| Go source or resources | Direct package tests, affected consumer tests or compilation, and relevant integration tests; workbench journeys when App, Application, HTTP or WebUI consumers are affected | No PR package build |
| Store or its dependencies | The applicable Go checks plus all eight Store shards | No PR package build |
| LSP, analyzer, browser or native execution inputs | Their corresponding real runtime / platform checks | No package build unless packaging inputs change |
| Packaging / release tooling | Native boundaries and packaging checks | Existing PR package validation |
| Go module dependencies, CI machinery or unclassified inputs | Full checks | Only if release tooling also changes |
| Nightly or manual CI | Full checks, platform matrix, packaging and Go vulnerability audit | Version tags / manual releases still use the release workflow |

PNG, JPEG and WebP screenshots under `docs/acceptance/`, `docs/evidence/` and
`docs/images/` count as documentation. Adding before/after screenshots to a
frontend PR keeps its frontend and workbench-journey checks without starting
unrelated backend/platform checks. Runtime images under `assets/branding/`,
`web/` or Go package test data retain their existing checks. Other files in
the evidence directories still use the normal classification rules.

The selection rules live in `scripts/ci/classify_changes.py`. Main uses the push
before/after commits, so merging a frontend change does not rerun the entire
backend matrix. Nightly CI runs at 19:23 UTC (03:23 Asia/Hong_Kong). The Actions
page's **CI → Run workflow** starts a full run for a selected branch.

An unreadable or empty change set fails selection. Failed selection, a failed
required job, cancellation, or an unexpected required-job skip cannot become a
successful aggregate result. Unclassified build inputs select full checks.

## Go security baseline

The supported build toolchain is Go **1.26.10**, pinned by `go.mod`. Both central
CI and release builders read that exact version with `go-version-file`.
This avoids selecting an older patch while the setup-go version catalog lags
behind a new Go security release. Local desktop builders require the same
version; set `GOTOOLCHAIN=go1.26.10` when another Go toolchain is installed.
The module's minimum version alone cannot exclude vulnerable newer release
branches, such as Go 1.27.0 and 1.27.1; a larger version is not security evidence.
The Windows fixed-toolchain fixture must match the exact version selected by
`setup-go` and retains its official archive and executable hash checks.
Update the module pin and fixture guard together when moving to another
supported Go release, then rerun the full matrix and current vulnerability audit.

`govulncheck` uses the current Go vulnerability database. A successful scan is
evidence for its recorded toolchain, source revision and database timestamp;
newly published advisories can make a later scan of the same commit fail.
Affected-check PR runs may skip the Go audit, while nightly and manual full
runs always select it. Check the job selection and scan log before treating a
green PR as current full security evidence.

The 2026-10-08 database update exposed the retired Go 1.25 baseline in the
2026-10-09 nightly run ([#289](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/289)).
The repair upgrades Go and `golang.org/x/net` without suppressing advisories or
changing the schedule. Go 1.26 requires macOS 12+, so native build flags, bundle
metadata and package verification use that same minimum; the earlier macOS 11
declaration was already inconsistent with Go 1.25's supported platforms.

## October 2026 runtime security updates

CI and desktop release jobs use Node.js **24.22.1**. Local frontend builds should
use the same version. The [October 21 Node security release](https://nodejs.org/en/blog/vulnerability/october-2026-security-releases)
fixes TLS/DNS validation, Brotli decoding, and other runtime issues, and updates
certificate trust. Updating npm packages alone does not update the Node executable.

Before downloading pinned Rust toolchains, CI and release jobs run
`rustup self update`. Local builders should also update rustup to **1.29.2 or
newer** before installing toolchains or targets; [rustup 1.29.2](https://blog.rust-lang.org/2026/10/20/Rustup-1.29.2/)
updates certificate verification dependencies for the eMudhra/emSign incident.
The analyzer compiler remains pinned to Rust 1.97.1 so the embedded WASI build
continues to use its established reproducible toolchain.

The [Python security announcement](https://discuss.python.org/t/information-for-python-users-regarding-emudhra-emsign-ca-certificate-security-incident/109576)
requires pip **26.2.2**, certifi **2026.10.19**, and uv **0.13.4** or newer where
those tools perform HTTPS requests. Update existing virtual environments as well
as base installations and rebuild container images with updated CA certificates.
UC's project-preparation pip path uses local wheel URLs, `--no-index`,
`--no-deps`, `--require-hashes`, and `--disable-pip-version-check`; downloads go
through the Go dependency broker. Its pip >=25.3 requirement describes offline
lockfile compatibility, not a safe version for general network use. The Python
3.9 CI job remains an offline macOS archive compatibility test.

Updating build configuration does not replace installed application binaries,
user-managed interpreters, or existing sandbox images. Go-built releases must
be rebuilt with the fixed toolchain; image owners must supply refreshed digests.

## Go impact selection

`scripts/ci/run_go_checks.py` uses `go list -json ./...` to identify package
ownership, embedded resources and the real import graph. Production imports
propagate impact; test imports add the corresponding test consumer without
turning a test-only dependency into a production dependency. A change confined
to test files or local testdata stays with its package. Shared fixture paths in
`TEST_INPUT_CONSUMERS` explicitly include their other test consumers; the agent-package launch-handoff
fixture therefore runs both the agent-package and MCP suites. Upstream skill
fixtures also select their snapshot, import, CLI or Store consumers.

Directly changed packages run their complete tests. Indirect consumers also run
their tests except the four large integration packages: `application`, `app`,
`httpapi` and `desktop`. Those compile their tests and run explicitly selected
existing provider, MCP and fixture integration tests where relevant. An affected `httpapi`
package also runs its Go DTO-to-OpenAPI golden check, including when only
`docs/openapi.json` changes. Store runs separately through its existing shards.
Vet covers the entire affected set; subsequent tests disable duplicate automatic vet.

This deliberately reserves the entire large integration suites for direct
changes and full CI. A direct `application` change can still take significantly
longer than a frontend or leaf-package change. A removed package or an input
with no current Go owner selects full checks, including Store and all platforms.

The native jobs retain their real platform checks. On a runtime-only change they
run desktop boundary and tagged adapter tests without producing release archives
or running the second reproducibility build. These adapter tests still build the
renderer because they consume the embedded production assets.

The frontend lane also runs the desktop-tagged `web` package tests on Linux.
These pass the built assets through the same `LoadEmbeddedFS` loader used at
Desktop startup, so invalid production assets fail without a native package
build. Native and packaging checks run that same test alongside the desktop
entry tests. The Rust analyzer lane also runs the Go consumers of the shared
analyzer protocol and archive inventory golden vectors.

## Release verification

Ordinary Go, frontend, documentation and dependency changes no longer launch the
separate Desktop release workflow on every PR. Changes to actual release tools,
packaging scripts, packaging assets and branding still do.

Before a tag or manual release, run full CI for that exact commit (or use a
successful nightly run of the same commit). The release workflow checks a
successful **Full CI verification** job from the same run attempt and commit.
A successful partial run, a PR run, or a skipped full gate is not release evidence.

## Workbench browser journeys

The TypeScript console job exercises the production renderer against a real
loopback Go API, SQLite database and disposable Git workspace. A deterministic
local model endpoint provides repeatable responses without external credentials.
Browser requests use the production API; the tests do not replace those routes.

The short journeys cover a task's file-tool result, retained manual file drafts,
and child-agent completion followed by a main-conversation summary. They also
check keyboard interaction, narrow layouts and bounded interaction times. Traces,
screenshots and timing results are retained for five days in the
`workbench-journeys` artifact.

Frontend inputs select these journeys. Backend changes select them through the
Go impact graph when App, Application, HTTP or WebUI consumers are affected. Backend
journey runs build the renderer without repeating the whole frontend unit suite.
Documentation changes do not select them; nightly and manual full runs do.
The separate Windows Real Edge job continues to verify the browser execution
and evidence machinery against its controlled pages.

Run locally from `web` with `npm run test:journeys` after installing Chromium
with `npx playwright install chromium`. The command builds the renderer and the
test-only `workbenchjourney` Go harness. Set `WORKBENCH_JOURNEY_SKIP_BUILD=1`
to reuse an existing production renderer build, as CI does. The harness uses a
separate build tag and does not add test endpoints to the product.
To use an installed Edge locally, set `WORKBENCH_JOURNEY_BROWSER_CHANNEL=msedge`.

## Store tests

Eight Linux jobs enumerate the compiled package with `go test -list`, then run
disjoint exact-name partitions. Every Test, Example and Fuzz seed entry belongs
to one shard; each shard must produce one terminal result for every selected
root and a successful package result. Existing conditional skips are recorded.
Subtests stay with their parent. The normal uncached execution and 60 minute
package deadline remain in effect. Race checks run separately as before.

Each shard uploads its inventory, partition, raw JSON events and summary for
five days. Splitting tests reduces the longest sequential job; it does not
remove the underlying work and uses additional runners and compilation time.
The slowest shard and runner queue still determine completion time.

For a local shard:

```sh
python scripts/ci/run_store_shard.py --index 0 --count 8 --output-dir /tmp/store-shard-0
```

Use `--index 0 --count 1` for a verified full-package run. This mode omits the
name filter to avoid Windows command-line length limits, but still checks every
compiled test's terminal result. Configure Go and temporary directories for
the local development environment before running either command.

## Test database setup

Only opted-in fixture helpers reuse closed schema snapshots. Every test gets a
new independent database file, and current-schema copies still call production
`Open` to create distinct recovery identities. Historical copies are built with
real migrations; the migration under test still executes against each copy.

Production database code, raw migration oracles, fresh-open, rollback, disk-full,
identity, permission and foreign-key checks retain their original paths. The
inverse historical fixture must reject modern queue revision or attachment
history that cannot be represented in the requested old schema.

## Superseded PR runs

Central CI and PR-only release validation cancel superseded runs. Heavy jobs use
`!cancelled()` so cancellation does not start further work. Tag and manually
requested release runs retain their non-cancelling behavior.

PR/push, nightly and manual runs use separate concurrency groups, so a new PR
commit does not cancel a full nightly or manually requested run.
