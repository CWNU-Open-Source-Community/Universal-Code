# ADR 0192: Worker continuation and isolated delivery

Date: 2026-10-18
Status: Proposed for #374.

## Decision

Commands, browser sessions and MCP connections may remain live when the same
execution worker continues to another attempt. Resource ownership binds the Run,
child, workspace root, worker profile and Batch generation. Every new invocation
still requires the current attempt, current permission and its durable charged
tool intent. Another worker receives results and files, never the old resource
identity. Completion, cancellation or lost authority closes the resources.

`worker_resource.v1` fingerprints that resource binding. The
`mcp_worker_scope.v2` key selects a process-local connection by Run and child;
the v1 key included the attempt. Keeping a connection between attempts does not
reuse its old execution authorization. Roots and calls resolve current authority;
idle catalog notifications defer refresh until a current invocation is available.

`worker-batch-authority.v1` records the existing host-issued Batch binding without
the owner token. Batch file edits, commits and declared verification retain their
owned-path executor. General worker tools use the pinned isolated workspace and
the current Batch generation. Unknown dispatched outcomes are never replayed.

Saved workflows can select an already approved and prepared Batch plan, execute
its children, wait for independent review, and integrate accepted receipts into
a separate branch. Later shared workers continue in the Run workspace and receive
the integration result explicitly. A completed merge or cancellation retires the
Batch workers and releases their unused reservations; failed resource cleanup
preserves worktrees for inspection and retry.

## Compatibility and rollback

MCP v1 scope keys only indexed process-local sessions. No connection survives a
host restart. Tool calls, approval records and results retain their original
attempt identity, so there is no durable scope-key rewrite or reader migration.
The retired v1 identifier remains recorded in the protocol registry.

Reverting the resource scope implementation and restarting the host recreates
attempt-local connections. Existing durable records stay unchanged. Workflow
definitions containing the added `batch` step require this implementation; older
hosts reject that step instead of silently executing it as a shared worker.
