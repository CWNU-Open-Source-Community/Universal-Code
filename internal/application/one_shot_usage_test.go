package application_test

import (
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestOneShotUsageCountsOnlyThisInvocation(t *testing.T) {
	_, service, run := oneShotFixture(t, []string{
		rootActionResponse(domain.RootActionFinish, "First reply.", "done", ""),
		rootActionResponse(domain.RootActionFinish, "Second reply.", "done", ""),
	})
	request := application.OneShotRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
		Content: "First task", OperationKey: "one-shot-first-usage", RequestedBy: "cli_exec_operator",
	}
	if _, err := service.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Content, request.OperationKey = "Second task", "one-shot-second-usage"
	result, err := service.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.TurnCount != 1 {
		t.Fatalf("second invocation performed one turn, reported %d", result.TurnCount)
	}
}
