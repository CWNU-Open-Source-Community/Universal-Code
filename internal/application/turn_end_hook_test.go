package application_test

import (
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"strings"
	"testing"
)

func TestThreadTurnEndHookPersistsWorkspaceScopeAndDoesNotRepeatOnReplay(t *testing.T) {
	base := openPromptHookStore(t, "turn-end-hook.db")
	const workspaceID = "workspace-turn-hook"
	if err := base.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: workspaceID, Name: "Hook workspace", RootPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionFinish, "Ready", "done", "")}}
	_, created, err := application.NewRunService(base).Create(t.Context(), application.CreateRunRequest{Goal: "Observe turn", Profile: "review", WorkspaceID: workspaceID, Interactive: true, ModelRoute: provider.Name() + "/model", Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	engine := hooks.NewEngine(base)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch", PluginFingerprint: strings.Repeat("b", 64), Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion, ID: "turn-tap", Event: hooks.TurnEnd, Action: hooks.ActionRecord, FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	execution := application.NewRunExecutionHandoffWithRuntime(base, router, policy.NewDefaultChecker(), application.RunRuntimeDependencies{LifecycleHooks: engine})
	turns := application.NewThreadTurnService(base, application.NewRunLifecycleControlService(base), execution)
	request := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(created.ID), Content: "Finish", OperationKey: "turn-end-hook-operation", RequestedBy: "test_operator"}
	if _, err := turns.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	audits, err := base.ListHookAudits(t.Context(), created.ID, workspaceID, 20)
	if err != nil || len(audits) != 1 || audits[0].Event != hooks.TurnEnd || audits[0].WorkspaceID != workspaceID {
		t.Fatalf("scoped turn audit=%+v err=%v", audits, err)
	}
	replay, err := turns.Execute(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	audits, err = base.ListHookAudits(t.Context(), created.ID, workspaceID, 20)
	if err != nil || len(audits) != 1 {
		t.Fatalf("replayed audit=%+v err=%v", audits, err)
	}
}
