package application

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func TestConsumedToolCorrectionAtBoundaryAllowsSchedulingContinue(t *testing.T) {
	continuation, _ := domain.NewSupervisorThreadContinueRepairReason(1)
	textTool, _ := domain.NewSupervisorTextToolRepairReason(1)
	toolRequest, _ := domain.NewSupervisorToolRequestRepairReason(1, "missing argument")
	for _, reason := range []string{continuation, textTool, toolRequest} {
		request := supervisorToolBoundaryRequest(llm.ChatRequest{
			Tools:    []llm.ToolSpec{{Name: "workspace_apply"}},
			Messages: []llm.Message{{Role: "system", Content: "original policy"}, {Role: "user", Content: "accepted task"}},
		})
		repair := supervisorProtocolRepairRequest(request, reason,
			supervisorRepairContext{ThreadEndTurn: true, ToolRounds: domain.MaxSupervisorToolRounds, BoundaryReached: true, AllowContinue: true})
		if len(repair.Tools) != 0 || repair.Metadata["protocol_repair"] != "1" {
			t.Fatal("boundary changed tools or the consumed repair allowance")
		}
		last := repair.Messages[len(repair.Messages)-1].Content
		if !strings.Contains(repair.Messages[1].Content, "scheduling boundary") ||
			!strings.Contains(last, "Return continue") || !strings.Contains(last, "No tools are offered") ||
			strings.Contains(last, "submit that tool now") || strings.Contains(last, "Perform the needed next action") {
			t.Errorf("consumed repair %q contradicts the current no-tool boundary: %s", reason, last)
		}
	}
}
