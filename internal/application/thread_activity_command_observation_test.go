package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestThreadActivityDetailObservesJobFromEarlierModelAttempt(t *testing.T) {
	for _, action := range []string{toolgateway.CommandRuntimeActionRead, toolgateway.CommandRuntimeActionWait, toolgateway.CommandRuntimeActionList} {
		t.Run(action, func(t *testing.T) {
			cursor, maxBytes, wait := uint64(0), runner.MinCommandRuntimeOutputRead, 0
			input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: action}
			if action == toolgateway.CommandRuntimeActionWait {
				wait = 100
			}
			if action != toolgateway.CommandRuntimeActionList {
				input.JobID, input.Cursor, input.MaxBytes, input.WaitMilliseconds = "job-observed", &cursor, &maxBytes, &wait
			}
			if err := input.Validate(); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := json.Marshal(map[string]any{"version": runner.CommandRuntimeResultVersion, "jobs": []map[string]string{{"id": "job-observed"}}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := json.Marshal(map[string]string{"version": "supervisor_tool_result.v1", "tool": string(toolgateway.CommandRuntimeTool), "stdout": string(projection)})
			if err != nil {
				t.Fatal(err)
			}
			finished, exitCode := time.Now().UTC(), 0
			job := runner.CommandRuntimeJob{ID: "job-observed", RunID: "run-observed", RootAgentID: "agent-activity",
				State: runner.CommandRuntimeJobCompleted, ExitCode: &exitCode, UpdatedAt: finished, CompletedAt: &finished,
				StdinPolicy: runner.CommandRuntimeStdinClosed, Network: runner.CommandRuntimeNetworkDisabled,
				Adapter:          commandruntimeadapter.Identity{Kind: commandruntimeadapter.KindHostUnsandboxed},
				WorkingDirectory: ".", Stdout: "ok example.com/preparation-uuid", StdoutObservedBytes: 31,
				IntentJSON: `{"version":"command-runtime.v2","profile":"process","executable":"go","arguments":["test","-mod=readonly","./..."],"working_directory":"."}`}
			store := threadActivityDetailStoreStub{threadID: "thread-observed", job: job,
				call: domain.SupervisorToolCall{RunID: job.RunID, CallID: "call-observed", ToolName: string(toolgateway.CommandRuntimeTool),
					PayloadJSON: string(payload), ResultJSON: string(result), Status: domain.SupervisorToolCompleted, CreatedAt: finished},
				jobAttributions: map[string]domain.AgentAttribution{job.ID: {AgentID: "agent-activity", AgentAttemptID: "attempt-started-job", Source: domain.AgentAttributionRecorded}}}
			detail, err := NewThreadActivityDetailService(store).Get(context.Background(), store.threadID, store.call.CallID)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Tools) != 1 || len(detail.Tools[0].Commands) != 1 || detail.Tools[0].Commands[0].ExitCode == nil ||
				*detail.Tools[0].Commands[0].ExitCode != 0 || detail.Tools[0].Commands[0].StdoutPreview != job.Stdout {
				t.Fatalf("earlier attempt's terminal result was lost: %#v", detail)
			}
			for _, mismatch := range []string{"agent", "operator", "run", "root"} {
				t.Run("reject-"+mismatch, func(t *testing.T) {
					changed := store
					attribution := store.jobAttributions[job.ID]
					switch mismatch {
					case "agent":
						attribution.AgentID = "agent-other"
					case "operator":
						attribution.AgentAttemptID, attribution.Source = "", domain.AgentAttributionOperatorRoot
					case "run":
						changed.job.RunID = "run-other"
					case "root":
						changed.job.RootAgentID = "agent-other-root"
					}
					changed.jobAttributions = map[string]domain.AgentAttribution{job.ID: attribution}
					if _, err := NewThreadActivityDetailService(changed).Get(context.Background(), changed.threadID, changed.call.CallID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
						t.Fatalf("unrelated command binding accepted: %v", err)
					}
				})
			}
		})
	}
}
