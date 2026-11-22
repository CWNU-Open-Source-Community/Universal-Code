package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
)

func TestSQLiteRunArtifactCaptureIsIdempotentAuditedAndTamperEvident(t *testing.T) {
	st := openRunArtifactTestStore(t)
	ctx := context.Background()
	_, run := createRunArtifactTestRun(t, ctx, st, "artifact lifecycle")
	gateway := toolgateway.New(st, policy.NewDefaultChecker())
	proposal, err := gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": "echo evidence"},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: "ws-artifact", RequestedBy: "artifact_test",
	})
	if err != nil || proposal.Proposal == nil {
		t.Fatalf("shell proposal failed: %#v err=%v", proposal, err)
	}
	reviewed, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposal.Proposal.ID, ReviewedBy: "artifact_test",
	})
	if err != nil || reviewed.Result == nil || reviewed.Result.Metadata["artifact_count"] != "1" {
		t.Fatalf("terminal output was not captured: %#v err=%v", reviewed, err)
	}
	artifactID := reviewed.Result.Metadata["artifact_stdout_id"]
	blob, err := st.GetRunArtifact(ctx, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if blob.RunID != run.ID || blob.SourceID != proposal.Proposal.ID || blob.ToolName != string(toolgateway.ShellTool) ||
		blob.Stream != artifact.StreamStdout || blob.Content != "dry run: echo evidence" ||
		blob.SHA256 != artifact.Hash(blob.Content) || blob.SizeBytes != int64(len(blob.Content)) {
		t.Fatalf("unexpected captured artifact: %#v", blob)
	}
	descriptors, err := st.ListRunArtifacts(ctx, artifact.ListFilter{RunID: run.ID, SourceID: proposal.Proposal.ID})
	if err != nil || len(descriptors) != 1 || descriptors[0].ID != artifactID {
		t.Fatalf("artifact list is inconsistent: %#v err=%v", descriptors, err)
	}

	replayed, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposal.Proposal.ID, ReviewedBy: "artifact_test",
	})
	if err != nil || replayed.Result == nil || replayed.Result.Metadata["artifact_stdout_id"] != artifactID {
		t.Fatalf("artifact replay changed identity: %#v err=%v", replayed, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countRunEventType(timeline, events.ArtifactCreatedEvent) != 1 ||
		countRunEventType(timeline, events.ToolCompletedEvent) != 1 {
		t.Fatalf("artifact replay duplicated events: %#v", timeline)
	}
	for _, event := range timeline {
		if event.Type == events.ArtifactCreatedEvent && strings.Contains(event.PayloadJSON, blob.Content) {
			t.Fatalf("artifact event copied output content: %s", event.PayloadJSON)
		}
	}

	_, otherRun := createRunArtifactTestRun(t, ctx, st, "cross-run artifact source")
	if _, err := st.CaptureToolOutput(ctx, artifact.CaptureRequest{
		RunID: otherRun.ID, SessionID: otherRun.SessionID, WorkspaceID: "ws-artifact",
		SourceID: proposal.Proposal.ID, ToolName: string(toolgateway.ShellTool),
		Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "text/plain; charset=utf-8", Content: blob.Content}},
	}); err == nil || !strings.Contains(err.Error(), "source scope") {
		t.Fatalf("cross-Run artifact source was not rejected: %v", err)
	}

	if _, err := st.CaptureToolOutput(ctx, artifact.CaptureRequest{
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: "ws-artifact",
		SourceID: proposal.Proposal.ID, ToolName: string(toolgateway.ShellTool),
		Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "text/plain; charset=utf-8", Content: "different"}},
	}); err == nil {
		t.Fatal("expected source-content mismatch rejection")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE run_artifacts SET content = ? WHERE id = ?`,
		strings.Repeat("x", int(blob.SizeBytes)), artifactID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("Artifact mutation was not frozen: %v", err)
	}
	if loaded, err := st.GetRunArtifact(ctx, artifactID); err != nil || loaded.Content != blob.Content {
		t.Fatalf("frozen Artifact changed after rejected mutation: %#v err=%v", loaded, err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM run_artifacts WHERE id = ?`,
		artifactID); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("Artifact deletion was not frozen: %v", err)
	}
}

func TestSQLiteRunArtifactEventFailureRecoversWithoutRepeatingToolCompletion(t *testing.T) {
	st := openRunArtifactTestStore(t)
	ctx := context.Background()
	_, run := createRunArtifactTestRun(t, ctx, st, "artifact rollback recovery")
	gateway := toolgateway.New(st, policy.NewDefaultChecker())
	proposal, err := gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": "echo recover"},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: "ws-artifact", RequestedBy: "artifact_test",
	})
	if err != nil || proposal.Proposal == nil {
		t.Fatalf("shell proposal failed: %#v err=%v", proposal, err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_artifact_created
		BEFORE INSERT ON run_events WHEN NEW.type = 'artifact.created'
		BEGIN SELECT RAISE(ABORT, 'injected artifact event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	failed, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposal.Proposal.ID, ReviewedBy: "artifact_test",
	})
	if err == nil || failed.Result == nil || failed.Result.Status != toolgateway.StatusCompleted {
		t.Fatalf("expected recoverable post-completion capture failure: %#v err=%v", failed, err)
	}
	persisted, err := st.GetToolRun(ctx, proposal.Proposal.ID)
	if err != nil || persisted.Status != toolrun.StatusCompleted {
		t.Fatalf("tool completion was not durable before capture retry: %#v err=%v", persisted, err)
	}
	ledger, err := st.GetApprovalByProposal(ctx, proposal.Proposal.ID)
	if err != nil || ledger.Status != approval.StatusApproved {
		t.Fatalf("approval was not durable before capture retry: %#v err=%v", ledger, err)
	}
	descriptors, err := st.ListRunArtifacts(ctx, artifact.ListFilter{RunID: run.ID})
	if err != nil || len(descriptors) != 0 {
		t.Fatalf("failed artifact event left rows: %#v err=%v", descriptors, err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_artifact_created`); err != nil {
		t.Fatal(err)
	}
	recovered, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposal.Proposal.ID, ReviewedBy: "artifact_test",
	})
	if err != nil || recovered.Result == nil || recovered.Result.Metadata["artifact_stdout_id"] == "" {
		t.Fatalf("artifact capture did not recover: %#v err=%v", recovered, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countRunEventType(timeline, events.ToolCompletedEvent) != 1 ||
		countRunEventType(timeline, events.ArtifactCreatedEvent) != 1 ||
		countRunEventType(timeline, events.ApprovalDecidedEvent) != 1 {
		t.Fatalf("capture recovery duplicated lifecycle events: %#v", timeline)
	}
}

func TestSQLiteRunArtifactCapturesAutomaticWorkspaceReadByInvocationID(t *testing.T) {
	st := openRunArtifactTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := st.SaveWorkspace(ctx, WorkspaceRecord{ID: "ws-artifact", Name: "artifact", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	token := "s" + "k-" + strings.Repeat("r", 28)
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("TOKEN="+token+"\ncomplete output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, run := createRunArtifactTestRun(t, ctx, st, "automatic read artifact")
	gateway := toolgateway.New(st, policy.NewDefaultChecker()).WithWorkspaceRootResolver(
		func(context.Context, string) (string, error) { return root, nil },
	)
	outcome, err := gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ReadFileTool, Arguments: map[string]string{"path": "result.txt"},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: "ws-artifact", RequestedBy: "artifact_test",
	})
	if err != nil || outcome.Result == nil || outcome.Call.InvocationID == "" ||
		outcome.Result.Metadata["artifact_stdout_id"] == "" || strings.Contains(outcome.Result.Stdout, token) {
		t.Fatalf("automatic read artifact failed: %#v err=%v", outcome, err)
	}
	blob, err := st.GetRunArtifact(ctx, outcome.Result.Metadata["artifact_stdout_id"])
	if err != nil || blob.SourceID != outcome.Call.InvocationID || blob.ToolName != string(toolgateway.ReadFileTool) ||
		strings.Contains(blob.Content, token) || !strings.Contains(blob.Content, "[REDACTED:") || !blob.Redacted {
		t.Fatalf("automatic read artifact is unsafe or unbound: %#v err=%v", blob, err)
	}
	var calls int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_tool_calls WHERE id = ? AND run_id = ?`,
		outcome.Call.InvocationID, run.ID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("artifact invocation source was not backed by the budget ledger: %d", calls)
	}
}

func TestSQLiteUpgradesSchemaV13ToRunArtifactsWithoutLosingScriptProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v13.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	request := scriptProcessTestRequest("preserve-v13-process", "value")
	_, run, err := newMigrationFixtureRunService(t, st).Create(ctx, request.Run)
	if err != nil {
		t.Fatal(err)
	}
	_, seedGateway := newScriptProcessTestService(t, st)
	created, err := seedGateway.ProposeScriptProcess(ctx, toolgateway.ToolCall{
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: request.Run.WorkspaceID,
		RequestedBy: request.RequestedBy,
	}, request.Process)
	if err != nil || created.Proposal == nil {
		t.Fatalf("seed historical ScriptProcess: %#v err=%v", created, err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 13)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	persisted, err := st.GetScriptProcess(ctx, created.Proposal.ID)
	if err != nil || persisted.Status != scriptprocess.StatusProposed {
		t.Fatalf("v13 ScriptProcess was not preserved: %#v err=%v", persisted, err)
	}
	gateway := toolgateway.New(st, policy.NewDefaultChecker())
	reviewed, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ScriptProcessTool,
		ProposalID: created.Proposal.ID, ReviewedBy: "migration_test",
	})
	if err != nil || reviewed.Result == nil || reviewed.Result.Metadata["artifact_stdout_id"] == "" {
		t.Fatalf("upgraded artifact capture is unusable: %#v err=%v", reviewed, err)
	}
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("v13 database did not upgrade to latest: version=%d err=%v", version, err)
	}
}

func openRunArtifactTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "run-artifact.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createRunArtifactTestRun(t *testing.T, ctx context.Context, st *SQLiteStore, goal string) (domain.Mission, domain.Run) {
	t.Helper()
	mission, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: goal, Profile: "code", WorkspaceID: "ws-artifact",
		Budget: domain.Budget{MaxTurns: 5, MaxToolCalls: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mission, run
}

func TestSQLiteRunArtifactCapturesRootCodeIntelToolsByInvocationID(t *testing.T) {
	st := openRunArtifactTestStore(t)
	ctx := context.Background()
	_, run := createRunArtifactTestRun(t, ctx, st, "root code_* artifact capture")
	if _, err := application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)

	codeTools := []string{
		"code_workspace_symbols",
		"code_document_symbols",
		"code_definition",
		"code_references",
		"code_implementation",
		"code_hover",
		"code_signature_help",
		"code_diagnostics",
		"code_call_hierarchy",
		"code_type_hierarchy",
	}
	for _, toolName := range codeTools {
		t.Run(toolName, func(t *testing.T) {
			usage, err := st.ChargeToolCall(ctx, toolbudget.ChargeRequest{
				RunID:           run.ID,
				SessionID:       run.SessionID,
				WorkspaceID:     "ws-artifact",
				ToolName:        toolName,
				ActionClass:     string(toolgateway.ClassWorkspaceRead),
				LeaseID:         lease.LeaseID,
				LeaseGeneration: lease.Generation,
				RequestedBy:     toolbudget.RequesterRunSupervisor,
			})
			if err != nil {
				t.Fatalf("charge %s: %v", toolName, err)
			}
			captured, err := st.CaptureToolOutput(ctx, artifact.CaptureRequest{
				RunID:       run.ID,
				SessionID:   run.SessionID,
				WorkspaceID: "ws-artifact",
				SourceID:    usage.LastCharge,
				ToolName:    toolName,
				Outputs: []artifact.Output{{
					Stream:   artifact.StreamStdout,
					MIME:     "application/json",
					Content:  `{"tool":"` + toolName + `","ok":true}`,
					Redacted: true,
				}},
			})
			if err != nil || len(captured) != 1 {
				t.Fatalf("capture %s artifact failed: captured=%+v err=%v", toolName, captured, err)
			}
			blob, err := st.GetRunArtifact(ctx, captured[0].ID)
			if err != nil || blob.ToolName != toolName || blob.SourceID != usage.LastCharge {
				t.Fatalf("loaded %s artifact mismatch: blob=%+v err=%v", toolName, blob, err)
			}
		})
	}
}

