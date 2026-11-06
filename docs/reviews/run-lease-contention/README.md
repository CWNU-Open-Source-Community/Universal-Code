# Run lease renewal under temporary SQLite contention

## Reproduction

Base: `2efafdf16490e66666997bc2eafb76932843b45c`.

`TestAgentRunnerSurvivesTemporarySQLitePoolContention` calls the public
`AgentRunner.Step` against a real migrated SQLite database. The model provider
waits on a channel; no workflow or workflow claim participates. The store wrapper
only records real renewal results. A real `database/sql.Conn` executes `SELECT 1`
and occupies the store's only connection for 3.5 seconds.

The test retains the production 30-second TTL and 2-second renewal timeout, and
accelerates only the renewal interval to 500 milliseconds. On the base revision,
the first renewal timed out 2.419 seconds into the connection hold. Reading the
lease through that held connection showed the original owner and generation,
with **27.499 seconds still valid**, but the model was cancelled. The test failed
with `healthy model was cancelled while its confirmed Run lease remained valid`.

## Change

Only `internal/application/execution_lease.go` changes production behavior:

- Retry `context.DeadlineExceeded` after 250 milliseconds while the last
  store-confirmed expiry remains in the future. Each call is bounded by both
  the existing renewal budget and the remaining confirmed lifetime.
- Accept the store's returned expiry after successful renewal. A late return
  cannot extend the previous confirmed deadline.
- An independent expiry timer cancels the operation even if a renewal backend
  does not return on context cancellation. An expired acquisition never enters
  the operation.
- Renewal inherits caller cancellation. Conflict, policy denial, and other
  non-timeout failures still cancel immediately. Release retains the original
  fencing identity and its existing bounded cleanup context.

## Verification

Actual SQLite tests cover temporary recovery without a second model call,
persistent contention until confirmed expiry, caller cancellation during the
second blocked SQL renewal, and owner/generation replacement. The replacement
is injected with SQL, and the test verifies the old owner's release leaves the
replacement active. The other database operations use the real store APIs.

Internal tests simulate an acquisition returning after expiry, a backend ignoring
its context and returning a late success, and explicit conflict/policy rejection.
These are controlled backend tests, not OS or provider-network verification.

Final Windows results:

- New SQLite tests, `go test -race ./internal/store -run
  '^(TestAgentRunnerSurvivesTemporarySQLitePoolContention|TestAgentRunnerRunLeaseContentionStops)$'
  -count=3 -v -timeout=120s`: passed in **65.203s**.
- Application focused race tests: passed in **63.337s**. Coverage includes the
  new expiry boundaries, existing long-model/long-web-search lease renewal,
  AgentRunner cancellation, specialist scheduler cancellation/budgets, and
  thread/approval Stop and bounded host Shutdown.
- Existing SQLite acquisition/fencing tests each passed three iterations in an
  earlier combined command. That command reached its overall 120s timeout during
  the third iteration of the new expiry case, after two complete new-test
  iterations had passed; this was a command-budget failure, not an assertion
  failure. The independent run above completed all three new-test iterations.
- `go vet ./internal/application ./internal/store` and `git diff --check`: passed.

OCR delegate rules were resolved for the production file and both test files;
tests excluded by OCR's default filter were read manually. The changed production
file, its new tests, store acquire/renew/release and identity fencing were read in
full. AgentRunner and specialist scheduler lease callers and Stop/Shutdown
consumers were read for the affected paths.
