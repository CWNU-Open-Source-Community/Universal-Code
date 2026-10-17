# Scoped read-only Specialists

This page describes legacy/scoped readers (`worker` omitted). For admitted
execution workers and saved orchestration, see [execution workers](execution-workers.md).

Schema v200 stores each child's `allowed_read_paths`, `tool_call_limit`, and
`cost_limit_micros` at admission. The specialist-delegation and child-task proposal
schemas accept these optional fields; operator review and admission still precede
execution. Empty paths keep the no-tool default. Read tools require explicit
literal workspace-relative paths and a positive tool-call limit. A file authorizes
that file, a directory authorizes its descendants, and `.` explicitly authorizes
the workspace. Globs are search filters, not authorization paths.

Each read, list, glob, and grep checks the current child's scope and the attempt's
persisted binding. Run exclusions, hidden-file rules, redirect rejection, current
lease, and Stop checks still apply. Searches traverse only the admitted paths.
Ordinary scoped children do not receive GitHub evidence, write, shell, network,
approval, or spawn tools. Batch execution retains its separate authority.

Tool calls accumulate across all of a child's attempts in the existing charged
call ledger. Denied tool invocations consume a call when they reached the charging
gateway; attempts denied by an exhausted budget do not consume another call.
Cost includes settled usage and unresolved reservations from the existing monetary
ledger. A positive child cost limit enables pricing even when the Run has no total
cost cap; missing prices reject dispatch. Zero cost limit means no independent
child monetary ceiling. Run total limits continue to apply when configured. A
tracked monetary projection with `cap_micros: 0` means no Run-wide cost ceiling.

`POST /api/v1/runs/{run_id}/agents/{agent_id}/revoke-read-tools` with
`{"version":"specialist_tools.v1"}` uses the existing control bearer and model
control setting. It clears read authority without resetting usage or receipts.
The child card exposes this action, the admitted paths, and cumulative limits.
Future explicit tool execution fails; no-tool continuation may still analyze
previously delegated material. Reopening a database never infers scopes for old
children or old bindings; operators must admit a new scoped child.

Focused verification:

```powershell
go test ./internal/application -run 'TestSpecialistScoped|TestSpecialistChildScopes|TestSpecialistScopeRechecked|TestSpecialistGatewayRejects' -count=1
go test ./internal/store -run 'TestSpecialistIndependentCost|TestSchemaV200' -count=1
go test ./internal/httpapi -run 'TestSpecialistReadRevocation' -count=1
```

The two-step acceptance provider derives its second grep query from the first
production read result, then validates the next request's evidence. Both steps
use the real gateway and SQLite tool receipts. It is a deterministic provider
fixture, not a live model evaluation.
