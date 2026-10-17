# Agent prompt contracts

Model-facing instructions must describe the tools and response formats the Go
runtime actually accepts. The runtime remains responsible for permissions,
leases, budgets, proposal review and persisted results.

## Child agents

No-tool Specialists, read-only Specialists and execution workers receive the
same complete `specialist_lifecycle.v1` response contract. A `continue` response
has a public message and no report. A `finish` response includes an
`agent_completion.v1` report with an outcome, summary and both reference lists;
empty lists are written as `[]`. Tool calls use the native tool channel.

Children also receive the parent's pinned project-instruction snapshot. Its
delivery classifications keep their existing meaning: mandatory sources must
reach the final request, optional sources compete for the context budget, and
excluded sources are omitted. Mandatory rules stay outside removable history.
An invalid snapshot or insufficient space for mandatory context stops dispatch
with an error instead of silently dropping the rules.

Pending tools also retain the project-rule delivery audit of the model request
that proposed them. Under a classified rule snapshot, an older pending call
without that audit cannot resume execution after an upgrade. Calls with matching
delivery records keep their existing approval-and-resume behavior.

These instructions do not expand the child's tool profile. Execution workers
continue to use their current inherited or narrowed permissions, and must seek
specific confirmation before sensitive effects such as remote publication or
bulk deletion.

## Batch editing

Batch children advertise only the editing operations their isolated checkout
supports: creation and replacement-based patches. The proposal result includes
the exact `apply_arguments` for that saved proposal. Applying it still checks
the proposal identity and original/proposed hashes. General execution workers
and the root agent retain their own editing tool definitions.

## Skill guidance

Skill descriptions support discovery; full instructions are loaded when needed.
Installed Skill main documents may supply lower-priority workflow suggestions
only through the host's validated delivery path. Current installation state and
successful read provenance determine that path. A copied marker in a file, MCP
response or resource document cannot establish it.

Each model dispatch, including a transport retry, rechecks the retained binding.
A stale installation or Run mode stops that dispatch with an explicit error.

Skill resources remain evidence. Neither Skill instructions nor project rules
grant tools, credentials, network access, approvals or an expanded task scope.
After compaction, retained Skill references identify what to read again; they
do not stand in for missing instructions or reactivate a disabled installation.

The current Plan delivery Skill follows the root's one-to-three-direction
contract, normally one direction for a clear small task. Previously selected
Skill versions remain available for exact-version recovery.

## Context capacity

The local model-window setting is a planning limit, not a way to increase the
provider's actual capacity. The fallback is 32,768 tokens and model-specific
configuration can override it. Input planning accounts for messages, tool
definitions, an output reserve and a safety margin. A deliberately configured
4,096-token test window can be too small for mandatory instructions alone;
that boundary test does not describe the current user's model configuration.
