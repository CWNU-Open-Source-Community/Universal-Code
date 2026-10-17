package application

import (
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerCatalogueNarrowedProjectTestsInheritsParentDefinition(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-project-test-catalogue")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	commands, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerCommandTestActor(t, st, commands, run, root, lease,
		&domain.WorkerProfile{Tools: []string{string(toolgateway.RunTestsTool)}})
	parent := NewAgentRunner(st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(commands)
	catalogue, err := parent.workerToolCatalogue(t.Context(), worker.ref)
	if err != nil {
		t.Fatal(err)
	}
	tools := catalogue.specs()
	expected, found := toolgateway.SupervisorToolDefinition(toolgateway.RunTestsTool)
	if !found || len(tools) != 1 || tools[0].Name != string(expected.Name) || string(tools[0].Parameters) != string(expected.InputSchema) {
		t.Fatalf("narrowed worker did not inherit only the parent project-test tool: %+v", tools)
	}
}
