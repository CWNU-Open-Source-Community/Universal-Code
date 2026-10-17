# Plan/Delivery workflow

Use this workflow to turn a broad objective into a reviewable delivery path. This Skill is guidance only. Go owns the Run mode, state, budgets, Policy, approvals, tool availability, direction choice, and completion gates.

## Plan phase

Understand the objective and available context before proposing work. When the boundary is clear, submit one to three tailored directions through `plan_delivery_propose`. Prefer one direction for a clear, bounded task; add alternatives only when they offer meaningful tradeoffs in scope, risk, or independent preparation.

Each direction must explain its tradeoffs and contain one to eight ordered modules. Every module needs a concrete objective, observable acceptance criteria, and only backward references to earlier module ordinals. When offering alternatives, keep them meaningfully different; do not vary labels while keeping the same plan.

The proposal does not select a direction, create execution authority, or switch phase. After it is recorded, return `wait`, summarize the actual proposed directions, and ask the operator to confirm one of them and its manual acceptance mode.

## Delivery phase

Treat the accepted WorkItems and pinned decision Note as the durable delivery contract. Advance one bounded slice at a time. After each slice, run focused functional checks, inspect the diff and security boundary, record the result, and update handoff context. At the end of a larger module, run broader regression, race/static, recovery, and robustness checks appropriate to the affected surface.

Do not claim file changes, commands, network activity, child-Agent work, approvals, or successful verification unless Go offered and completed the corresponding operation. Never use this Skill to expand Scope or bypass Policy.
