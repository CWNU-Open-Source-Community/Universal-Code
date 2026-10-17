package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolbudget"
)

func TestRunTestsRejectsExecutionFieldsAndUnboundedTargets(t *testing.T) {
	for _, payload := range []string{
		`{"version":"run-tests.v1","target":"../outside","timeout_milliseconds":1000}`,
		`{"version":"run-tests.v1","target":"/outside","timeout_milliseconds":1000}`,
		`{"version":"run-tests.v1","target":"C:/outside","timeout_milliseconds":1000}`,
		`{"version":"run-tests.v1","target":".","timeout_milliseconds":25001}`,
		`{"version":"run-tests.v1","target":".","timeout_milliseconds":0}`,
		`{"version":"run-tests.v1","target":".","timeout_milliseconds":1000,"script":"whoami"}`,
		`{"version":"run-tests.v1","target":".","timeout_milliseconds":1000,"network":"host"}`,
		`{"version":"run-tests.v1","target":".","timeout_milliseconds":1000,"commands":[]}`,
	} {
		if _, err := NormalizeSupervisorToolPayload(RunTestsTool, json.RawMessage(payload)); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	if _, err := NormalizeSupervisorToolPayload(RunTestsTool, json.RawMessage(`{"version":"run-tests.v1","target":"internal/pkg","timeout_milliseconds":25000}`)); err != nil {
		t.Fatal(err)
	}
}

type projectTestBudgetStore struct {
	*memoryStore
	request toolbudget.ChargeRequest
}

func (s *projectTestBudgetStore) ChargeToolCall(_ context.Context, r toolbudget.ChargeRequest) (toolbudget.Usage, error) {
	s.request = r
	return toolbudget.Usage{}, errors.New("budget exhausted")
}

type projectTestPreparerProbe struct {
	commandRuntimeExecutorStub
	preparations int
}

func (p *projectTestPreparerProbe) PrepareProjectTest(context.Context, ToolCall, RunTestsInput) (CommandRuntimeInput, error) {
	p.preparations++
	return CommandRuntimeInput{}, errors.New("unexpected preparation")
}

func TestRunTestsBudgetStopsBeforePreparation(t *testing.T) {
	s := &projectTestBudgetStore{memoryStore: newMemoryStore()}
	p := &projectTestPreparerProbe{}
	g := New(s, nil).WithCommandRuntimeExecutor(p)
	c := commandRuntimeToolCall(json.RawMessage(`{"version":"run-tests.v1","target":".","timeout_milliseconds":1000}`))
	c.Name = RunTestsTool
	c.PermissionMode = domain.RunExecutionPermissionAsk
	_, err := g.Invoke(t.Context(), c)
	if err == nil || err.Error() != "budget exhausted" || p.preparations != 0 || p.calls != 0 {
		t.Fatalf("budget bypass: %v %+v", err, p)
	}
	if s.request.ToolName != string(RunTestsTool) || s.request.AgentID != c.AgentID {
		t.Fatalf("lost tool or actor budget binding: %+v", s.request)
	}
}
