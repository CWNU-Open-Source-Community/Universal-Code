```text
Completion notification fault reproduction
Baseline: 21f56e6b390abf1c2a7de86f9ae8af68d34edb91
Branch: Qiyuanqiii/workflow-repro-completion
Recorded: 2026-11-06T00:13:02.9304093+08:00
Toolchain: go version go1.26.10 windows/amd64
CGO_ENABLED: 1
CC: C:\Users\22977\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin\gcc.exe
Command: $env:UC_WORKFLOW_FAULT_REPRO=1; go test ./internal/application -run ^TestWorkflowFaultCompletion -count=1 -v

=== RUN   TestWorkflowFaultCompletionNormalExactlyOnce
    workflow_fault_completion_test.go:166: before completion: {Status:running Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1
    workflow_fault_completion_test.go:173: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
    workflow_fault_completion_test.go:186: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
--- PASS: TestWorkflowFaultCompletionNormalExactlyOnce (1.02s)
=== RUN   TestWorkflowFaultCompletionStorageFailureRecovery
=== RUN   TestWorkflowFaultCompletionStorageFailureRecovery/get-run-once
    workflow_fault_completion_test.go:195: before completion: {Status:running Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1
    workflow_fault_completion_test.go:204: method-level one-shot get-run-once: ran=true injections=1 error=workflow completed; main-conversation summary is pending: UNAVAILABLE state={Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:209: healthy same-process RunDue retry: ran=false injections=1 state={Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:213: SQLite reopened with healthy store and summary handoff: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=16m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=24h0m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: HOST RECOVERY DEFECT: completed workflow has no automatic main-conversation summary after healthy database reopen and RunDue: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1; want one committed notification and one summary without operator Advance
    workflow_fault_completion_test.go:213: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
    workflow_fault_completion_test.go:213: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
=== RUN   TestWorkflowFaultCompletionStorageFailureRecovery/enqueue-once
    workflow_fault_completion_test.go:195: before completion: {Status:running Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1
    workflow_fault_completion_test.go:204: method-level one-shot enqueue-once: ran=true injections=1 error=workflow completed; main-conversation summary is pending: UNAVAILABLE state={Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:209: healthy same-process RunDue retry: ran=false injections=1 state={Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:213: SQLite reopened with healthy store and summary handoff: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=16m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: host RunDue scheduler_offset=24h0m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:213: HOST RECOVERY DEFECT: completed workflow has no automatic main-conversation summary after healthy database reopen and RunDue: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1; want one committed notification and one summary without operator Advance
    workflow_fault_completion_test.go:213: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
    workflow_fault_completion_test.go:213: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
--- FAIL: TestWorkflowFaultCompletionStorageFailureRecovery (2.19s)
    --- FAIL: TestWorkflowFaultCompletionStorageFailureRecovery/get-run-once (1.07s)
    --- FAIL: TestWorkflowFaultCompletionStorageFailureRecovery/enqueue-once (1.11s)
=== RUN   TestWorkflowFaultCompletionProcessExitRecovery
    workflow_fault_completion_test.go:220: before completion: {Status:running Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1
    workflow_fault_completion_test.go:235: real subprocess terminated with exit=86 (no Close/deferred claim release):
        === RUN   TestWorkflowFaultCompletionCrashChild
        FAULT: completed checkpoint committed; exiting process before summary (exit=86)
    workflow_fault_completion_test.go:236: SQLite reopened with healthy store and summary handoff: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5}
    workflow_fault_completion_test.go:236: host RunDue scheduler_offset=0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:236: host RunDue scheduler_offset=16m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:236: host RunDue scheduler_offset=24h0m0s candidates=0 ran=false provider_calls=1
    workflow_fault_completion_test.go:236: HOST RECOVERY DEFECT: completed workflow has no automatic main-conversation summary after healthy database reopen and RunDue: {Status:completed Jobs:1 Notifications:0 Committed:0 Summaries:0 WorkerTokens:5} provider_calls=1; want one committed notification and one summary without operator Advance
    workflow_fault_completion_test.go:236: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
    workflow_fault_completion_test.go:236: exactly one delivered summary: {Status:completed Jobs:1 Notifications:1 Committed:1 Summaries:1 WorkerTokens:5} provider_calls=2
--- FAIL: TestWorkflowFaultCompletionProcessExitRecovery (1.12s)
=== RUN   TestWorkflowFaultCompletionCrashChild
    workflow_fault_completion_test.go:243: only run as the completion-checkpoint crash subprocess
--- SKIP: TestWorkflowFaultCompletionCrashChild (0.00s)
FAIL
FAIL	cyberagent-workbench/internal/application	4.452s
FAIL
EXIT_CODE=1

Default gate check command: Remove-Item Env:UC_WORKFLOW_FAULT_REPRO; go test ./internal/application -run ^TestWorkflowFaultCompletion -count=1 -v
=== RUN   TestWorkflowFaultCompletionNormalExactlyOnce
    workflow_fault_completion_test.go:165: set UC_WORKFLOW_FAULT_REPRO=1 to run the completion fault reproductions
--- SKIP: TestWorkflowFaultCompletionNormalExactlyOnce (0.00s)
=== RUN   TestWorkflowFaultCompletionStorageFailureRecovery
    workflow_fault_completion_test.go:192: set UC_WORKFLOW_FAULT_REPRO=1 to run the completion fault reproductions
--- SKIP: TestWorkflowFaultCompletionStorageFailureRecovery (0.00s)
=== RUN   TestWorkflowFaultCompletionProcessExitRecovery
    workflow_fault_completion_test.go:219: set UC_WORKFLOW_FAULT_REPRO=1 to run the completion fault reproductions
--- SKIP: TestWorkflowFaultCompletionProcessExitRecovery (0.00s)
=== RUN   TestWorkflowFaultCompletionCrashChild
    workflow_fault_completion_test.go:240: set UC_WORKFLOW_FAULT_REPRO=1 to run the completion fault reproductions
--- SKIP: TestWorkflowFaultCompletionCrashChild (0.00s)
PASS
ok  	cyberagent-workbench/internal/application	0.091s
DEFAULT_GATE_EXIT_CODE=0
```
