# ADR 0170: Product task budgets and immutable project configuration

Date: 2026-10-09

## Status

Accepted for schema v186, shared Go product creation, read-only previews and the
standalone task configuration presentation. Application routing is integrated at
the existing Thread creation seam; this ADR grants no execution capability.

## Contract

HTTP and in-process Desktop `run_creation.v1` / `thread_creation.v1` requests may
include `budget` with optional `max_turns`, `max_tool_calls`, `max_tokens`,
`max_cost_usd` and `timeout_seconds`. Go canonicalizes omitted fields to the
existing defaults. Turns remain 1..10,000; tools remain 1..1,000,000; tokens are
0..1,000,000,000; cost is 0..100,000 USD; timeout is 0..604,800 seconds. Zero
disables an optional token/cost/timeout dimension. Positive cost must be at least
one micro-USD and is rounded once to micro-USD, matching the monetary ledger.
These are execution limits, never exact billing estimates or permission grants.
An active operator price snapshot remains necessary before a cost-capped model
call; task creation and preview make no model or account calls.

The normalized operator ceiling is pinned as `RunConfig.requested_budget`;
`Run.Budget` contains the effective narrowed limits. The canonical creation
fingerprint binds that operator input, independently of the live repository
file. Default-budget requests retain historical fingerprints. Initial-state
replay checks the durable operation before loading project files and returns the
original snapshot across restart. Changed explicit budgets under the same key
conflict. The existing refusal to replay a creation after its Run has advanced
remains; original-request observation provides that later recovery path.

Web/Desktop creation and Standard Code's initial Run use the same Go loader and
resolver as CLI: `.prayu/config.yaml` is bounded, strictly decoded, inert input;
profiles and read-only selection restrict admission and project budgets must be
strictly below the operator ceiling. Any rejected field blocks the complete
creation. No HTTP/Desktop ignore switch is added. The CLI's explicit
`--ignore-project-config` contract remains. The selected profile is checked
against the project profile set, which may contain several supported profiles.
This adds no global or user configuration file or renderer-selected host path.

Thread successors and explicit continuity branches inherit the stored effective
budget, normalized operator ceiling and project snapshot. They never reload
changed project configuration. Their existing authority reset is unchanged.

Schema v186 replaces only the controlled-creation trigger's fixed budget/root
limits. Its Mission/Session/mode/root/event/network and all-denied execution
checks are preserved. The new branch verifies bounded requested limits and
exact requested/effective/project relationships. New product snapshots reject
budget or project replacement in SQLite. Historical migrations remain frozen;
v185 upgrade preserves old Runs and creation operations without fabricated
operator input. Clean-install schema artifacts are generated from the full plan.

## Read surfaces and provenance

`POST /api/v1/task-configuration/preview` uses the **read bearer**, accepts only
workspace identity, profile and budget, and has no idempotency/mutation effect.
`GET /api/v1/runs/{run_id}/task-configuration` reads the immutable saved snapshot.
Both return `task_configuration.v1`, `capability_grant=false`, normalized
requested/effective budgets, closed field sources, a safe project summary and
fingerprints. A rejected preview has field-specific reasons and no partial
project/effective fingerprint. Decoder details and repository bytes are never
returned; exclusion paths are represented as counts. Skill suggestions expose
only their bounded exact `name@version` identities for explicit draft selection.
Malformed JSON, unknown/duplicate fields and unsupported bounds fail closed.

Sources describe normalized effective values: product defaults, operator
overrides or project restrictions. Historical Runs without a retained operator
ceiling use `snapshot` provenance rather than inventing their input history.
Their read projection preserves legal historical CLI ceilings, including zero
or omitted tool limits and values above current product input bounds. The
renderer validates these saved numbers independently of creation inputs and
requires requested/effective snapshot values to match; explicit JSON null is
never a zero/default value. Preview responses cannot claim snapshot provenance.
The configuration fingerprint includes the safe projection and full project
snapshot digest; later file edits do not alter it.

The standalone React `TaskConfiguration` component edits draft request fields
and shows the Go preview, or reads an existing Run snapshot without edits. Reads
are aborted on task changes and late results cannot replace the current view.
Connection identity also gates the first render so a replacement API client
cannot briefly expose the previous connection's saved configuration.
Draft inputs and validity belong to the workspace's parent draft state so closing
the panel cannot silently revert an invalid attempted budget to defaults.
Creation rechecks the file in Go; a preview is never admission authority.

## Runtime consumers

The pinned `exclude_paths` snapshot now narrows file-tool reads, listing, glob,
grep and mutations, Batch file access, editor proposals/apply/revert/recovery,
legacy file reads and CodeIntel evidence. Root-relative `*`, `?`, character
classes and whole `**` directory segments are supported. A matching directory
excludes its descendants. Hidden-file flags and `.gitignore` negation cannot
reinclude exclusions; pagination cursors bind the policy. This is a file-tool
filter, not an OS sandbox for arbitrary command execution or LSP processes.

Project Skill suggestions are selectable in draft task configuration. The
control route `POST /api/v1/runs/{run_id}/project-skills/select` admits only exact
refs in the saved snapshot and delegates to the existing built-in/external
selection services. Built-in versions must match; external packages must already
be installed and eligible, including catalog enablement and phase/surface checks.
External context requires explicit confirmation. No package is installed and no
capability is granted. Selection finishes before first-message handoff; a failure
preserves the Created task and durable creation intent for same-key recovery.

Registry presets `test.go` (`go test ./...`), `test.node` (`node --test`),
`test.npm` (installed npm CLI `test` through native Node) and
`format.go` (`gofmt -w` for one relative Go file) prepare native process intents,
never shell strings from config. The Root receives configured test input and a
format template when the existing host command runtime is offered. Operators
can request the action in chat and inspect the ordinary Job receipts, or use
`run project-action <run-id> test|format --prepare` / explicit CLI execution.
`test.npm` uses the same installed launcher resolver as Batch validation, with
offline/audit/fund settings and the runtime's fixed CI environment. It accepts an
exact relative package directory, such as `--path web`; it never replaces npm's
test script with Node's built-in runner or injects Jest-specific flags.
Read-only projects, excluded npm scopes and excluded formatter targets are rejected during intent
preparation. Toolchain availability and the runtime's existing permission,
approval, lease and native input checks still apply. Sandbox adapters receive
no fabricated host executable paths. Older typed Tool IDs without a command
preset remain non-executable metadata; no fallback shell execution is introduced.

This changes neither runtime permission selection nor leases, approvals,
execution backends, secrets, command launch, model eligibility or account access.
Tests separate actual Go creation/migration/replay/succession checks from React
presentation fixtures. Native Desktop delivery and live model billing remain
outside the evidence supplied by this slice.
