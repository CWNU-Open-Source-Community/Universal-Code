# ADR 0199: Hook tool matchers

- Status: Proposed
- Date: 2026-11-09
- Scope: restricted lifecycle hook declarations and tool-event filtering
- Updates: ADR 0197 (operator script hooks) and the hook boundary contract in
  docs/extensions.md
- References: Claude Code matcher syntax and Codex hook matchers (external
  parity baselines)

## Problem

Tool-event hooks filter by an exact `tool_names` list capped at 64 entries.
Both external harnesses match tools with regular expressions instead, and real
tool families break the exact-list model: MCP servers expose dynamic tool
names (`mcp_github_create_issue`, `mcp_github_create_pr`, …), so a reviewed
plugin cannot deny a whole write family without enumerating every name or
consuming the entire list budget on one server.

## Decision (proposed)

1. **One filter per declaration, either form.** A tool-event declaration
   declares either the exact `tool_names` list or a single `tool_pattern`
   regular expression — never both. The pattern matches unanchored against the
   tool name (the same effective behavior as the external matchers), so
   `mcp_github_(create|delete)_` covers a write family with one rule.
2. **Bounded and linear-time.** Patterns are at most 256 bytes, reject NUL and
   control characters, and must compile as Go regexp. Go's regexp is a
   linear-time engine, so a hostile or sloppy pattern cannot cause
   catastrophic backtracking in a hook boundary. Compilation happens at
   registry replacement, and the compiled forms are stored beside the sorted
   registrations; a pattern that fails to compile at replacement time fails
   closed.
3. **Same generation contract.** The pattern is part of the declaration
   fingerprint: changing it advances the registry generation exactly like any
   other declaration edit, and the plugin diagnostics projection exposes the
   declared pattern next to `tool_names` so operator views can render the real
   filter.
4. **Non-tool events unchanged.** `user_prompt_submit`, run, session,
   compaction, subagent, and checkpoint boundaries have no tool name to match;
   they reject both filters exactly as before.

## Alternatives considered

- Anchored full-match semantics were rejected: the external matchers match
  unanchored, and anchor-free patterns are strictly more expressive anyway
  (`^…$` is one escape away).
- Glob-style matchers (`mcp_github_*`) were rejected: regular expressions
  cover globs and more, and the repository already standardizes on Go regexp
  elsewhere.
- Allowing both the list and the pattern per declaration was rejected: the
  union is expressible with a pattern alternation, and one filter keeps the
  audit and the operator views unambiguous.

## Verification

- `internal/hooks` tests cover validation (mutual exclusion, size, syntax,
  NUL, non-tool events), unanchored matching against matching, non-matching,
  and unrelated tools, registry generation changes on pattern edits, and
  fail-closed compilation at replacement.
- `internal/httpapi` and the web client validate the extended declaration
  projection (the `tool_pattern` field, its bounds, mutual exclusion, and
  tool-event restriction); the OpenAPI snapshot and the generated TypeScript
  schema are regenerated.
- Documentation: the hook contract in docs/extensions.md (bilingual) and this
  ADR.

## Consequences

Reviewed plugins can express per-family policy in one rule, and the 64-entry
exact-list budget stops being the binding constraint for dynamic tool
families. The matcher joins the existing engine path with no new surfaces,
and the diagnostics views render the actual filter instead of an empty list.
