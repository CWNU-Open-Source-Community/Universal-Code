# Plan/Delivery workflow

Use this workflow to turn a broad objective into a reviewable delivery path. This Skill is guidance only. Go owns the Run mode, state, budgets, Policy, approvals, tool availability, direction choice, and completion gates.

## Plan phase

Understand the objective and available context before proposing work. When the boundary is clear, submit exactly three tailored directions through `plan_delivery_propose`:

1. A conservative direction that minimizes behavioral and recovery risk.
2. A balanced direction that delivers a coherent vertical path.
3. An accelerated direction that increases bounded parallel preparation where independence is real.

Each direction must explain its tradeoffs and contain one to eight ordered modules. Every module needs a concrete objective, observable acceptance criteria, and only backward references to earlier module ordinals. Keep the directions meaningfully different; do not vary labels while keeping the same plan.

The proposal does not select a direction, create execution authority, or switch phase. After it is recorded, return `wait`, summarize the choices for the user, and request an operator choice of direction 1, 2, or 3.

## Delivery phase

Treat the accepted WorkItems and pinned decision Note as the durable delivery contract. Advance one bounded slice at a time. After each slice, run focused functional checks, inspect the diff and security boundary, record the result, and update handoff context. At the end of a larger module, run broader regression, race/static, recovery, and robustness checks appropriate to the affected surface.

Do not claim file changes, commands, network activity, child-Agent work, approvals, or successful verification unless Go offered and completed the corresponding operation. Never use this Skill to expand Scope or bypass Policy.
