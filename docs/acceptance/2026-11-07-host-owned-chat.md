# Accepted chat lifetime — 2026-11-07

Base: `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.

An ordinary accepted chat used the HTTP or Desktop call context for its entire
execution. Reloading the browser cancelled the model call. A second accepted
message could also be left pending if its client disconnected immediately after
the SQLite commit but before notifying the active turn.

Web and Desktop hosts now own accepted ordinary turns until completion, Stop,
or host shutdown. Preparation remains tied to the submitting caller until its
message is committed. Queued input transfers to the existing turn at that same
boundary. A committed approval resumes the exact proposal under the host owner;
the review and file operation still finish through their existing request path.
Automatic execution and callers that do not opt into host lifetime retain their
existing cancellation behavior. This covers a live host process, not resuming
an interrupted model call after process restart.

Shutdown tracks complete mutating operations, including deferred receipt writes,
and uses the caller's deadline without waiting for the turn lock. Database
cleanup waits for those operations to finish.

## Real browser verification

The journey serves the production bundle from the real Go API and calls a local
HTTP model provider. A test-only provider gate holds the response while Chromium
reloads and reconnects. The original execution ID remains active, the original
input appears once, and the provider receives one request. Releasing the response
finishes the turn; a separate run clicks Stop and observes cancellation of that
same request. Removing the host opt-in makes both cases fail after refresh.

The stronger completion check found another visible bug: queue polling stopped
on idle before fetching its final state. The query now cancels the prior read
and refreshes the exact queue once when execution ends. This also covers an
unfinished initial read. It renders the server's actual queue, including pending
approval or retained edits.

Screenshots are unmodified 1280×900 Windows Chromium captures. The completion
Before image includes the Go lifetime fix but precedes the queue refresh fix;
the reconnect Before image disables the host opt-in. The browser notice visible
in these captures is separately removed by PR #546.

| Scenario | Before | After |
| --- | --- | --- |
| Reload during execution | [Before](../images/host-owned-chat/before-reconnected.png) | [After](../images/host-owned-chat/after-reconnected.png) |
| Completed turn queue | [Before](../images/host-owned-chat/before-completed-queue.png) | [After](../images/host-owned-chat/after-completed-queue.png) |
| Stop after reload | — | [Stopped](../images/host-owned-chat/after-stopped.png) |

## Verification and review

- Real TCP tests cover accepted message disconnect, approval commit disconnect,
  continuation disconnect, exact file effect and approval replay.
- SQLite tests cover text and uploaded-file queued messages at the commit
  boundary, shutdown during projection and deferred rejection, and bounded
  shutdown while a queued write holds the turn lock.
- Existing Stop and new-input tests run with host ownership to verify that late
  approval does not revive a stopped task or supersede newer input.
- Application, HTTP API, app and Desktop focused tests and vet passed.
- Queue component tests: 37 passed, including both initially stale and unfinished
  initial queue reads. Strict TypeScript and production build passed.
- Real refresh/complete and refresh/Stop Chromium journeys passed. Root repeated
  the final journeys in 10.6 seconds and checked the actual screenshots.

OCR Delegate selected production changes; tests were read explicitly in addition
to its default selection. Root and an independent agent reviewed the changed
lifetime, commit, queue, shutdown and approval paths and the regression tests.
This is scoped review of affected paths, not a whole-repository audit.
