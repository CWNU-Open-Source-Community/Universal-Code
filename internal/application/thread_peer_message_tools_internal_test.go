package application

import (
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

func TestThreadPeerToolsRequireRuntimeBindingInBothPhases(t *testing.T) {
	for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
		for _, enabled := range []bool{false, true} {
			options := supervisorToolOptions{ThreadPeerMessages: enabled}
			specs := supervisorStructuredToolSpecs(domain.ExecutionSurfaceCode, phase, domain.RunExecutionPermissionAsk, false, false, options)
			count := 0
			for _, spec := range specs {
				if toolgateway.IsThreadPeerMessageTool(toolgateway.ToolName(spec.Name)) {
					count++
				}
			}
			if enabled && count != 2 || !enabled && count != 0 {
				t.Fatalf("phase=%s enabled=%t peer tools=%d", phase, enabled, count)
			}
			_, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "call-1", Name: string(toolgateway.ThreadSendMessageTool), Arguments: json.RawMessage(`{"target_thread_id":"target","content":"hello"}`)}}, "run", 1, 1, domain.ExecutionSurfaceCode, phase, domain.RunExecutionPermissionAsk, false, false, options)
			if enabled && err != nil || !enabled && err == nil {
				t.Fatalf("phase=%s enabled=%t prepare error=%v", phase, enabled, err)
			}
		}
	}
}
