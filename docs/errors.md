# Stable Error Contract

CyberAgent Workbench preserves human-readable error text while assigning every failure a stable machine code. CLI callers use exit codes; the Go `api.v1` API uses the same code through `apperror.CodeOf` and `apperror.HTTPStatus`.

| Code | CLI exit | HTTP status | Meaning |
| --- | ---: | ---: | --- |
| `INTERNAL` | 1 | 500 | Unexpected internal failure |
| `INVALID_ARGUMENT` | 2 | 400 | Invalid command, flag, input, or domain value |
| `NOT_FOUND` | 3 | 404 | Requested durable object or file does not exist |
| `CONFLICT` | 4 | 409 | Concurrent or uniqueness conflict |
| `FAILED_PRECONDITION` | 4 | 412 | Current state does not permit the operation |
| `POLICY_DENIED` | 5 | 403 | Safety policy rejected the operation |
| `UNAVAILABLE` | 6 | 503 | Dependency is temporarily unavailable |
| `CANCELLED` | 7 | 499 | Caller cancelled the operation |
| `RESOURCE_EXHAUSTED` | 8 | 429 | Capacity, quota, or budget is exhausted |
| `DEADLINE_EXCEEDED` | 9 | 504 | Operation exceeded its deadline |

`apperror.Normalize` provides a compatibility bridge for legacy plain Go errors. New application services should return typed errors directly and must not branch on human-readable text.

The HTTP API returns these codes in its versioned error envelope and hides internal error details. Protocol boundaries may use a more precise transport status without changing the stable code: missing/invalid bearer authorization returns HTTP 401 with `POLICY_DENIED`, unsupported methods return 405 with `INVALID_ARGUMENT`, an oversized request target returns 414 with `RESOURCE_EXHAUSTED`, and cancellation-body/media failures use 413/415 with `RESOURCE_EXHAUSTED`/`INVALID_ARGUMENT`. A stale or inactive cancellation target returns 409 or 412 according to whether identity changed or the operation is no longer possible. See [http-api.md](http-api.md).

Provider failures use a separate `llm.Outcome` classification before mapping into this contract. Exhausted retryable transport failures map to `UNAVAILABLE`; rate limits map to `RESOURCE_EXHAUSTED`; invalid/permanent responses map to `FAILED_PRECONDITION`; caller cancellation and model deadlines map to `CANCELLED` and `DEADLINE_EXCEEDED`. The original typed Provider error remains available through Go error unwrapping, while persisted and user-facing text is redacted.

An HTTP body interrupted before its declared length or final transfer chunk is a retryable network failure. It uses the existing attempt, backoff and Run-budget limits. Malformed complete response data, oversized events and cleanly closed streams missing a provider completion marker remain non-retryable protocol failures.

Run tool-call budget exhaustion also returns `RESOURCE_EXHAUSTED`/CLI exit 8 from direct Gateway-backed commands. The first rejected call beyond the configured limit appends one `tool.budget_exhausted` Run event; repeated attempts remain rejected without duplicating that event. Use `cyberagent run usage <run-id>` to inspect the durable counter and exhaustion timestamp.

A second worker attempting to execute a Run with an active schema v17 execution lease receives `CONFLICT`/CLI exit 4. The caller should inspect `cyberagent run lease <run-id>` and retry after the current execution finishes or the lease expires. Stale lease generations also return `CONFLICT`; they are never silently accepted and do not consume tool budget.

`cyberagent headless events` writes a `headless.v1` `stream.end` record before returning a normal outcome code. A completed Run returns 0; a failed Run returns `FAILED_PRECONDITION`/4; a cancelled Run or caller cancellation returns `CANCELLED`/7; an event-bound stop returns `RESOURCE_EXHAUSTED`/8; and a follow timeout returns `DEADLINE_EXCEEDED`/9. A nonterminal one-shot snapshot returns 0. Setup, cursor, Store, and output failures retain the same table above; failures detected before framing write no stdout, while human-readable diagnostics remain on stderr.
