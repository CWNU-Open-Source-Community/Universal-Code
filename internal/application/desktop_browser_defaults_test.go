package application_test

import (
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func TestDesktopBrowserDefaultDefersBusyRunToNextIdleInput(t *testing.T) {
	st, seed, _, _ := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	mission, err := st.GetMission(t.Context(), seed.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.NewControlledRunCreationService(st).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "continue a legacy desktop browser task", WorkspaceID: mission.WorkspaceID,
		Profile: "code", Surface: "code", Phase: "deliver",
		OperationKey: "legacy-desktop-browser-task", RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := created.Run
	// An advanced local command environment already exists; browser defaults
	// must not depend on the separate initial-Preview command upgrade.
	if _, err := application.NewRunExecutionProfileService(st).Change(t.Context(), application.ChangeRunExecutionProfileRequest{
		RunID: run.ID, Profile: "local", OperationKey: "legacy-desktop-local", RequestedBy: "operator", Reason: "existing local environment",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "other-desktop-instance", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnableDesktopBrowserDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionFinish, "browser task ready", "done", "")),
	}}
	turns := commandDefaultsTurns(st, provider)
	input := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "continue browsing", OperationKey: "continue-legacy-browser", RequestedBy: "operator"}
	if _, err := turns.Execute(t.Context(), input); err == nil || len(provider.Requests()) != 0 {
		t.Fatalf("browser default displaced another execution: requests=%d err=%v", len(provider.Requests()), err)
	}
	unchanged, err := st.GetRun(t.Context(), run.ID)
	if err != nil || unchanged.Status != domain.RunRunning || unchanged.Config.BrowserNetworkAccess != domain.BrowserNetworkOff || unchanged.Config.BrowserNetworkRevision != 1 {
		t.Fatalf("busy Run was rewritten: %+v err=%v", unchanged, err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	result, err := turns.Execute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	next := result.Submission.Run
	if next.ID == run.ID || next.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet || next.Config.BrowserNetworkRevision != 1 || len(provider.Requests()) != 1 {
		t.Fatalf("idle input did not materialize a browser-enabled successor: %+v requests=%d", next, len(provider.Requests()))
	}
	previous, err := st.GetRun(t.Context(), run.ID)
	if err != nil || previous.Status != domain.RunCancelled || previous.Config.BrowserNetworkAccess != domain.BrowserNetworkOff || previous.Config.BrowserNetworkRevision != 1 {
		t.Fatalf("successor rewrote historical browser selection: %+v err=%v", previous, err)
	}
}
