# Runtime dependencies during host turn drain

2026-11-07. Base: `dac396f56757399d3c3f2048528536d603d464ff`.

`App.Close` cancels host-owned chat and waits up to five seconds. Previously, a
pending drain retained SQLite but immediately closed the shared MCP and code
intelligence managers. A turn still unwinding could therefore encounter a closed
dependency before its own deferred work returned.

The same owned cleanup now waits for the full turn drain, closes MCP and code
intelligence, then closes SQLite. The five-second foreground wait is preserved.
The ordinary already-drained path uses the same cleanup immediately.

## Reproduction

The test uses real SQLite, `ThreadTurnService`, `App.Close`, `mcp.Manager` and an
empty `codeintel.Manager`. A model fixture invokes the real MCP manager while a
store wrapper delays its server lookup past cancellation. A separate input's
committed intent is held before returning, so its real rejection write happens
after foreground Close returns.

The MCP catalog record is a validated fixture written through the public SQLite
API. The delayed read deliberately returns after cancellation; the test does not
contact a remote MCP server. Code intelligence's empty manager is probed through
its public initialization API, without starting an LSP process.

Before, the real MCP invocation's underlying cause was `MCP manager is closed`,
and code intelligence reported `manager is shutting down` while both host
operations were still pending. The regression failed in 5.48s. The public chat
result was already normalized to “This turn was stopped”; this experiment does
not demonstrate an incorrect public provider-failure message.

After, MCP retains `context.Canceled`, code intelligence remains open through
drain, the late real `RejectThreadMessageIntent` succeeds, and the owned cleanup
eventually closes both managers and SQLite. The test passes in 5.516s, and its
race run passes in 8.161s.

## Cancellation without global manager closure

MCP calls pass their context to `sdk.ClientSession.CallTool`. Existing HTTP and
real stdio subprocess tests cancel one request, observe it return, then use the
same connection successfully before closing it. A blocked stdio writer closes
its own guarded connection on context cancellation and waits for writer exit.

LSP `transport.request` selects `ctx.Done`, removes its pending request, sends a
250ms-bounded cancel notification, and returns the context error. A cancelled
write also wakes its caller through the transport's own failure handling.
Existing real helper-process tests confirm cancellation before manager cleanup.
These paths do not wait for `Manager.Close` to unblock the turn.

## Verification

```powershell
go test ./internal/app -run '^TestAppCloseKeepsRuntimeDependenciesUntilTurnDrain$' -count=1 -v -timeout=30s
go test -race ./internal/app -run '^TestAppCloseKeepsRuntimeDependenciesUntilTurnDrain$' -count=1 -timeout=45s
go test ./internal/app ./internal/application -run '^(TestAPIServeOwnsAcceptedChatAcrossDisconnect|TestIndependentHostShutdown|TestHostOwnedTurnShutdown|TestHostOwnedThreadTurnShutdown|TestAppCloseKeepsRuntimeDependenciesUntilTurnDrain)' -count=1 -timeout=60s
go test ./internal/application -run '^TestHostOwnedThreadShutdownDeadlineLeavesDrainObservable$' -count=1 -timeout=30s
go test ./internal/mcp ./internal/codeintel -run '^(TestSDKModernHTTPCancellationClosesOnlyTheRequestStream|TestSDKCancellationKeepsOtherStdioCallsAlive|TestResolvedCancellationKeepsReceiptAndConnection|TestStdioClientTransportCancelsBlockedWrite|TestGuardedClientTransportCancellationWaitsForWriterExit|TestManagerBoundsCrashTimeoutCancellationProtocolAndOversizeFailures)$' -count=1 -timeout=90s
go vet ./internal/app ./internal/application
git diff --check
```

The API/shutdown group passes in app 6.159s and application 1.274s; the additional
host drain case in 0.628s. Transport cancellation passes in mcp 0.342s and
codeintel 0.802s. `go vet` and whitespace checks exit zero.

Logs: [before](before.txt), [after](after.txt), [race](race.txt),
[host regressions](regressions.txt), [transport cancellation](transport-cancellation.txt).
