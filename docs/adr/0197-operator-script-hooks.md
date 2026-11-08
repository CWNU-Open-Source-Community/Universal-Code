# ADR 0197: Operator-owned script hooks

- Status: Proposed
- Date: 2026-11-08
- Scope: restricted lifecycle hooks runtime and operator extension surface
- Updates: ADR 0024 (strict inert skill package) and the declarative-only hook
  boundary recorded in the extensions documentation; complements ADR 0044
  (action center) for operator-owned configuration surfaces
- References: [issue #491](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/491)
  (parity pressure from Codex hooks), the 2026-09-12 capability-gap review
  (script hooks listed as a designed capability extension), Claude Code hooks
  reference and Codex hooks guide (external parity baselines)

## Problem

The lifecycle hook runtime is strictly declarative: plugin packages contribute
`deny`, `annotate`, `narrow`, and `record` rules, and the runtime executes no
code. This matches the inert-extension security model, but it leaves a
capability gap against both external harnesses: Claude Code and Codex both let
operators run their own commands at lifecycle events (Claude Code `command`
hooks with stdin JSON and exit-code semantics; Codex `notify` and hooks with
`type: command`). Operators cannot express legitimate house rules — a grep
guard for secrets before `pre_tool`, an audit tap on `run_completed`, a
checklist verifier before compaction — without patching the product.

The 2026-09-12 capability review explicitly recorded script hooks as a
designed capability extension and warned that renaming the declarative
runtime would not count as support.

## Decision (proposed)

1. **Operator-owned only.** Script hooks come from
   `<CYBERAGENT_HOME>/hooks.d/*.json` and script files inside that tree.
   Plugin packages, skills, project instructions, workspace content, and tool
   output can never contribute a script hook declaration, so prompt injection
   cannot turn repository files into executed commands. Plugin-contributed
   declarations remain limited to the four declarative actions.
2. **One action, two effects.** A script hook can only deny or annotate via a
   single bounded JSON output. It cannot rewrite or narrow tool arguments: the
   existing `narrow` action stays Go-owned and scripts never touch payloads.
   The model-facing denial text remains the fixed official message; the
   script-supplied reason is stored in the result for operator-owned views and
   is not added to model-visible errors.
3. **Pinned content, re-verified every run.** Each declaration pins the script
   content SHA-256. The loader and the executor re-read the script through a
   held `os.Root` on every fire, re-hash it, and fail closed on drift, links,
   non-regular files, path escapes, oversized or malformed declarations. This
   mirrors the MCP capability-fingerprint rule (drift quarantines) and the
   Codex hash trust gate, implemented without a review UI by making the
   operator the sole author of both files: changing a script requires updating
   its pinned digest, and a stale digest refuses to run.
4. **No shell, no environment inheritance.** The executor builds one argument
   vector per allowlisted interpreter (`powershell` Windows-only, `pwsh`,
   `sh` POSIX-only, `python -I`, `node`). Child processes run with the hooks
   directory as cwd and an explicit environment of OS-operational variables
   only — no parent-environment inheritance, so credentials, bearer tokens,
   and provider configuration are invisible to scripts. Stderr is bounded and
   reaches only failure error text; it is never persisted.
5. **Bounded protocol.** Input is the existing bounded hook input as JSON on
   stdin (event, run/workspace/tool identity, payload ≤ 256 KiB). Output is at
   most one JSON value ≤ 16 KiB with strict unknown-field and trailing-value
   rejection; `reason` and annotation messages reuse the declarative annotation
   bounds. Script hooks may use a 10-second timeout ceiling (declarative hooks
   remain at 2 seconds) and are rejected on `post_tool`, where no retroactive
   denial is meaningful.
6. **Same engine, same audit.** Script hooks are evaluated by the existing
   hook engine with the same stable ordering, generation fingerprinting,
   failure policies, and reentry guards. Denials and failures write the
   existing metadata-only `plugin_hook_audits` rows with `plugin_id=operator`
   and the script digest as fingerprint; schema v223 widens the audit table's
   `declared_action` constraint and binding trigger so the engine's observed
   script denial is authoritative for that action. The plugin path is
   enforced three times: `hooks.ValidateDeclarative` rejects script actions in
   plugin manifests, the plugin loader filters any pre-existing staged
   manifest that declares one, and `Engine.Replace` rejects script
   registrations from any source other than `operator`. Declarations and
   enabled plugin hooks share the engine's 64-entry ceiling; the merge fails
   closed with a diagnosable error instead of locking every boundary with an
   opaque registry failure. A missing executor or hooks directory fails
   closed only for script declarations; declarative hooks keep working when
   scripts are not configured.

## Alternatives considered

- Extending plugin packages to carry scripts was rejected: it would make
  publisher-trusted packages executable, defeating the inert-package model
  (ADR 0024) and duplicating the credential and lifecycle risk surface.
- Loading script hooks from the workspace (`.prayu/`) was rejected: workspace
  content is untrusted data in this threat model; repo-controlled script
  hooks would turn prompt injection into code execution.
- Supporting `updateInput`-style payload rewriting from scripts was deferred:
  external processes editing tool arguments create a second untrusted control
  channel; the Go-owned `narrow` action already covers the safe subset, and
  deny/annotate covers policy enforcement.
- Additional events (`user_prompt_submit`, model request hooks) and
  `additionalContext` injection are deferred until an operator surface and
  real use cases exist; the event set here stays identical to the existing
  nine boundaries.

## Residual bounds

- Verification and the interpreter's own open of the script cannot be atomic:
  a file swapped between them is a TOCTOU residual. Exploiting it requires
  writing inside `hooks.d`, which is already operator-level power; the pinned
  digest still bounds what a script may contain.
- The declared timeout kills the direct child only. A script that leaves a
  background child holding its stdout pipe fails the execution closed after
  the wait grace period instead of leaking the pipe.
- Operator script hooks whose home resolves to a relative path are disabled
  silently; the default home is always absolute.

## Verification

- `internal/hooks` unit tests cover spec/declaration validation, output
  parsing and bounds, engine deny/annotate/failure-policy/timeout mapping,
  audit binding for script denials, and generation changes on digest edits.
- Executor tests run a real interpreter (PowerShell on Windows, `sh`
  elsewhere, skipped when unavailable) end-to-end: stdin JSON delivery,
  minimal environment (ambient secrets excluded), deny/annotate output,
  invalid output rejection, exit failures, digest drift, symlink rejection,
  path escapes, timeout enforcement, and stdout beyond the output limit.
- Loader tests cover the happy path, missing/empty directories, oversized or
  malformed declarations, path escapes, hash drift, declaration-count
  ceilings, and directory/non-JSON ignoring.
- `internal/store` tests cover the schema v223 rebuild (historical receipts
  preserved, script bindings admitted, contradictory bindings rejected by the
  trigger) and the Go-side audit binding; `internal/plugins` tests cover
  manifest-level rejection of script declarations and declarative-hook
  acceptance; Windows-specific tests create real junctions to verify link
  rejection in both the loader and the executor.
- `go vet ./...` and `go test -count=1 ./internal/hooks/ ./internal/store/`
  pass locally; hook-related application tests pass.

## Consequences

Operators gain the parity capability of running their own bounded lifecycle
commands without plugins. The hook engine remains the single evaluation path;
no new schema, migration, HTTP surface, or UI is required for the v1 runtime.
A review/enablement UI and CLI commands (`hooks.d` listing, per-hook enable)
remain follow-up work; until then the filesystem contract in
`docs/extensions.md` is the operator interface.
