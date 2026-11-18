package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type standardCodeSupervisorMemoryStore struct {
	snapshot     domain.StandardCodeSupervisorSnapshot
	ledger       []domain.StandardCodeSupervisorLedgerEntry
	transactions []workspacecheckpoint.Transaction
	checkpoints  map[string]workspacecheckpoint.Checkpoint
}

func (s *standardCodeSupervisorMemoryStore) GetConfiguredStandardCodePresetOperation(
	context.Context, string,
) (domain.StandardCodePresetOperation, bool, error) {
	return domain.StandardCodePresetOperation{}, false, nil
}

func (s *standardCodeSupervisorMemoryStore) GetRunExecutionProfile(
	context.Context, string,
) (domain.RunExecutionProfileSnapshot, error) {
	return domain.RunExecutionProfileSnapshot{ID: s.snapshot.ProfileSnapshotID,
		RunID: s.snapshot.RunID, MissionID: s.snapshot.MissionID,
		Revision: s.snapshot.ProfileRevision}, nil
}

func (s *standardCodeSupervisorMemoryStore) GetRunExecutionInteraction(
	context.Context, string,
) (domain.RunExecutionInteractionSnapshot, error) {
	return domain.RunExecutionInteractionSnapshot{ID: s.snapshot.InteractionSnapshotID,
		RunID: s.snapshot.RunID, MissionID: s.snapshot.MissionID,
		Revision: s.snapshot.InteractionRevision}, nil
}

func (s *standardCodeSupervisorMemoryStore) GetRunBrowserCDPPermission(
	context.Context, string,
) (domain.RunBrowserCDPPermissionSnapshot, error) {
	return domain.RunBrowserCDPPermissionSnapshot{ID: s.snapshot.BrowserCDPSnapshotID,
		RunID: s.snapshot.RunID, MissionID: s.snapshot.MissionID,
		Revision: s.snapshot.BrowserCDPRevision}, nil
}

func (s *standardCodeSupervisorMemoryStore) GetStandardCodeSupervisorSnapshot(
	context.Context, string,
) (domain.StandardCodeSupervisorSnapshot, bool, error) {
	return s.snapshot, s.snapshot.Version > 0, nil
}

func (s *standardCodeSupervisorMemoryStore) ListStandardCodeSupervisorLedger(
	context.Context, string, int,
) ([]domain.StandardCodeSupervisorLedgerEntry, error) {
	return append([]domain.StandardCodeSupervisorLedgerEntry(nil), s.ledger...), nil
}

func (s *standardCodeSupervisorMemoryStore) AppendStandardCodeSupervisorLedger(
	_ context.Context, expectedVersion int64, entry domain.StandardCodeSupervisorLedgerEntry,
) (domain.StandardCodeSupervisorLedgerEntry, bool, error) {
	if expectedVersion != s.snapshot.Version {
		return domain.StandardCodeSupervisorLedgerEntry{}, false,
			fmt.Errorf("version=%d want=%d", expectedVersion, s.snapshot.Version)
	}
	entry.EventSequence = int64(len(s.ledger) + 1)
	if err := entry.Validate(); err != nil {
		return domain.StandardCodeSupervisorLedgerEntry{}, false, err
	}
	s.snapshot = entry.Snapshot
	s.ledger = append(s.ledger, entry)
	return entry, false, nil
}

func (s *standardCodeSupervisorMemoryStore) GetPlanDeliverySelectionByRun(
	context.Context, string,
) (domain.PlanDeliverySelection, bool, error) {
	return domain.PlanDeliverySelection{}, false, nil
}

func (s *standardCodeSupervisorMemoryStore) ListWorkspaceCheckpointTransactions(
	context.Context, string, int,
) ([]workspacecheckpoint.Transaction, error) {
	return append([]workspacecheckpoint.Transaction(nil), s.transactions...), nil
}

func (s *standardCodeSupervisorMemoryStore) GetWorkspaceCheckpoint(
	_ context.Context, id string,
) (workspacecheckpoint.Checkpoint, error) {
	checkpoint, found := s.checkpoints[id]
	if !found {
		return workspacecheckpoint.Checkpoint{}, fmt.Errorf("checkpoint %s is missing", id)
	}
	return checkpoint, nil
}

func newStandardCodeSupervisorTestMachine(state domain.StandardCodeSupervisorState,
	phase domain.ExecutionPhase,
) (*standardCodeSupervisorTurn, *standardCodeSupervisorMemoryStore) {
	now := time.Now().UTC()
	inspectionComplete := phase == domain.ExecutionPhaseDeliver
	readRounds := 0
	selectionID := ""
	if inspectionComplete {
		readRounds = domain.StandardCodeSupervisorMinimumReadRounds
		selectionID = "selection-1"
	}
	snapshot := domain.StandardCodeSupervisorSnapshot{
		ProtocolVersion: domain.StandardCodeSupervisorProtocolVersion,
		RunID:           "run-1", MissionID: "mission-1", WorkspaceID: "workspace-1",
		RootAgentID: "root-1", PresetOperationKeyDigest: strings.Repeat("a", 64),
		State: state, ModeSnapshotID: "mode-1", ModeRevision: 1,
		ProfileSnapshotID: "profile-1", ProfileRevision: 1,
		InteractionSnapshotID: "interaction-1", InteractionRevision: 1,
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		BrowserCDPSnapshotID: "browser-cdp-1", BrowserCDPRevision: 1,
		WorkspaceRootFingerprint: strings.Repeat("f", 64), PlanSelectionID: selectionID,
		Turn: 1, AttemptID: "attempt-1", TotalToolRounds: readRounds,
		ConsecutiveReadRounds: readRounds, InspectionComplete: inspectionComplete,
		RunTokenLimit: 10_000, RunTimeoutMillis: 60_000, RunToolCallLimit: 100,
		Limits: domain.DefaultStandardCodeSupervisorLimits(), CreatedAt: now, UpdatedAt: now,
	}
	store := &standardCodeSupervisorMemoryStore{snapshot: snapshot,
		checkpoints: make(map[string]workspacecheckpoint.Checkpoint)}
	turn := domain.SupervisorTurn{
		Run: domain.Run{ID: snapshot.RunID, MissionID: snapshot.MissionID,
			Budget: domain.Budget{MaxTurns: 10, MaxTokens: snapshot.RunTokenLimit,
				MaxToolCalls: snapshot.RunToolCallLimit, TimeoutSeconds: 60}},
		Mission: domain.Mission{ID: snapshot.MissionID, WorkspaceID: snapshot.WorkspaceID},
		Mode: domain.RunModeSnapshot{ID: snapshot.ModeSnapshotID, Revision: 1,
			Surface: domain.ExecutionSurfaceCode, Phase: phase, Profile: domain.ProfileCode},
		Agent: domain.AgentNode{ID: snapshot.RootAgentID, Role: domain.AgentRoleRoot},
		Checkpoint: domain.SupervisorCheckpoint{RunID: snapshot.RunID,
			LeaseID: "lease-1", LeaseGeneration: 1, NextTurn: 1,
			Phase: domain.SupervisorTurnStarted, AttemptID: snapshot.AttemptID, UpdatedAt: now},
	}
	permission := domain.RunExecutionPermissionSnapshot{ID: snapshot.PermissionSnapshotID,
		RunID: snapshot.RunID, MissionID: snapshot.MissionID,
		Revision: snapshot.PermissionRevision,
		Mode:     domain.RunExecutionPermissionWorkspaceAccess}
	snapshot.CapabilityGeneration = toolgateway.AgentCodeCapabilities(
		toolgateway.AgentCodeCapabilityContext{
			RunID: snapshot.RunID, MissionID: snapshot.MissionID,
			RootAgentID: snapshot.RootAgentID, WorkspaceID: snapshot.WorkspaceID,
			RootFingerprint: snapshot.WorkspaceRootFingerprint,
			Surface:         turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
			Profile: turn.Mode.Profile, PermissionMode: permission.Mode,
			ModeRevision: turn.Mode.Revision, PermissionRevision: permission.Revision,
		}).Generation
	store.snapshot = snapshot
	return &standardCodeSupervisorTurn{store: store, turn: turn,
		permission: permission, snapshot: snapshot, fileWorkspaceID: snapshot.WorkspaceID}, store
}

func standardCodePendingCall(id, toolName, payload string) domain.SupervisorToolCall {
	return domain.SupervisorToolCall{RunID: "run-1", Turn: 1,
		AttemptID: "attempt-1", Round: 1, Position: 1, ModelAttempt: 1,
		CallID: id, ToolName: toolName, PayloadJSON: payload,
		Status: domain.SupervisorToolPending, CreatedAt: time.Now().UTC()}
}

func standardCodeCompleteCall(t testing.TB, call domain.SupervisorToolCall,
	status domain.SupervisorToolCallStatus, metadata map[string]string,
	stdout, errorCode string,
) domain.SupervisorToolCall {
	t.Helper()
	encoded, err := json.Marshal(supervisorToolResultEnvelope{
		Version: supervisorToolResultVersion, Tool: call.ToolName,
		Status: string(status), Metadata: metadata, Code: errorCode, Stdout: stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	call.Status = status
	call.ResultJSON = string(encoded)
	call.ErrorCode = errorCode
	call.CompletedAt = &now
	return call
}

func standardCodeObserveCompleted(t testing.TB, machine *standardCodeSupervisorTurn,
	call domain.SupervisorToolCall, metadata map[string]string, stdout string,
) domain.SupervisorToolCall {
	t.Helper()
	decision, err := machine.Authorize(context.Background(), call)
	if err != nil || !decision.Allowed || decision.Result != nil {
		t.Fatalf("authorize %s: decision=%+v err=%v", call.CallID, decision, err)
	}
	call = standardCodeCompleteCall(t, call, domain.SupervisorToolCompleted,
		metadata, stdout, "")
	if err := machine.ObserveCall(context.Background(), call); err != nil {
		t.Fatalf("observe %s: %v", call.CallID, err)
	}
	return call
}

func standardCodeWorkspaceProposal(t testing.TB, id string) domain.SupervisorToolCall {
	t.Helper()
	payload, err := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
		Path: "issue-137.txt", Content: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return standardCodePendingCall(id, string(toolgateway.WorkspaceChangeTool), string(payload))
}

func standardCodeWorkspaceApply(t testing.TB, id, editID string) domain.SupervisorToolCall {
	t.Helper()
	payload, err := json.Marshal(toolgateway.WorkspaceApplyPayload{
		Version: toolgateway.AgentCodeRegistryVersion, EditID: editID,
		ExpectedAction: "create", ExpectedOriginalSHA256: strings.Repeat("0", 64),
		ExpectedProposedSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return standardCodePendingCall(id, string(toolgateway.WorkspaceApplyTool), string(payload))
}

func standardCodeAddCheckpoint(machine *standardCodeSupervisorTurn,
	store *standardCodeSupervisorMemoryStore, editID string,
) {
	afterID := "after-" + editID
	store.transactions = append(store.transactions, workspacecheckpoint.Transaction{
		ID: "transaction-" + editID, RunID: "run-1", WorkspaceID: "workspace-1",
		Kind: workspacecheckpoint.TransactionFileTool, TriggerReceiptID: editID,
		BeforeCheckpointID: "before-" + editID, AfterCheckpointID: afterID,
		Status: workspacecheckpoint.TransactionCompleted,
	})
	store.checkpoints[afterID] = workspacecheckpoint.Checkpoint{
		ID: afterID, RunID: machine.snapshot.RunID, MissionID: machine.snapshot.MissionID,
		WorkspaceID: machine.snapshot.WorkspaceID, AttemptID: machine.snapshot.AttemptID,
		CapabilityGeneration: machine.snapshot.CapabilityGeneration,
		TriggerReceiptID:     editID, Phase: workspacecheckpoint.PhaseAfter,
		RootFingerprint: runmutation.Fingerprint("standard_code_test_root.v1", editID),
	}
}

func standardCodeCommandCall(t testing.TB, id, action, script, jobID string,
	cursor uint64,
) domain.SupervisorToolCall {
	t.Helper()
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: action, JobID: jobID}
	switch action {
	case toolgateway.CommandRuntimeActionRun, toolgateway.CommandRuntimeActionStart:
		input.Commands = []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeBash,
			Script: script, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 1_000,
			Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4_096, ArtifactBytes: 4_096},
			Network:             runner.CommandRuntimeNetworkDisabled,
			Credentials:         runner.CommandRuntimeCredentialsNone, Purpose: "verify repository change",
		}}
		if action == toolgateway.CommandRuntimeActionRun {
			maxBytes := 4_096
			input.FailurePolicy = toolgateway.CommandRuntimeFailFast
			input.MaxBytes = &maxBytes
		}
	case toolgateway.CommandRuntimeActionRead, toolgateway.CommandRuntimeActionWait:
		maxBytes := 4_096
		wait := 0
		if action == toolgateway.CommandRuntimeActionWait {
			wait = 100
		}
		input.Cursor = &cursor
		input.MaxBytes = &maxBytes
		input.WaitMilliseconds = &wait
	case toolgateway.CommandRuntimeActionCancel:
		wait := 100
		input.WaitMilliseconds = &wait
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return standardCodePendingCall(id, string(toolgateway.CommandRuntimeTool), string(payload))
}

func standardCodeCommandOutput(t testing.TB, action string,
	jobs []runner.CommandRuntimeJobSnapshot, pages []runner.CommandRuntimeOutputPage,
) string {
	t.Helper()
	encoded, err := json.Marshal(standardCodeCommandProjection{
		Version: runner.CommandRuntimeResultVersion, Action: action, Jobs: jobs, Pages: pages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestStandardCodeSupervisorDeliverTransitionChecksContextAndPermissionDrift(
	t *testing.T,
) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorPlan, domain.ExecutionPhasePlan)
	machine.snapshot.ConsecutiveReadRounds = domain.StandardCodeSupervisorMinimumReadRounds
	machine.snapshot.InspectionComplete = true
	machine.turn.Mode.ID = "mode-2"
	machine.turn.Mode.Revision = 2
	machine.turn.Mode.Phase = domain.ExecutionPhaseDeliver
	profile := domain.RunExecutionProfileSnapshot{ID: machine.snapshot.ProfileSnapshotID,
		Revision: machine.snapshot.ProfileRevision}
	interaction := domain.RunExecutionInteractionSnapshot{
		ID: machine.snapshot.InteractionSnapshotID, Revision: machine.snapshot.InteractionRevision}
	browserCDP := domain.RunBrowserCDPPermissionSnapshot{
		ID: machine.snapshot.BrowserCDPSnapshotID, Revision: machine.snapshot.BrowserCDPRevision}
	deliverGeneration := toolgateway.AgentCodeCapabilities(
		toolgateway.AgentCodeCapabilityContext{
			RunID: machine.snapshot.RunID, MissionID: machine.snapshot.MissionID,
			RootAgentID: machine.snapshot.RootAgentID, WorkspaceID: machine.snapshot.WorkspaceID,
			RootFingerprint: machine.snapshot.WorkspaceRootFingerprint,
			Surface:         machine.turn.Mode.Surface, Phase: machine.turn.Mode.Phase,
			Role: machine.turn.Agent.Role, Profile: machine.turn.Mode.Profile,
			PermissionMode:     machine.permission.Mode,
			ModeRevision:       machine.turn.Mode.Revision,
			PermissionRevision: machine.permission.Revision,
		}).Generation

	reason, expected := standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, machine.permission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint,
		deliverGeneration)
	if reason != "" || !expected {
		t.Fatalf("exact Plan to Deliver transition was rejected: reason=%q expected=%t",
			reason, expected)
	}
	reason, expected = standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, machine.permission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint,
		strings.Repeat("c", 64))
	if reason != "workspace_context_drift" || !expected {
		t.Fatalf("Deliver transition bypassed capability drift: reason=%q expected=%t",
			reason, expected)
	}
	machine.turn.Mode.Revision = 3
	reason, expected = standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, machine.permission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint,
		machine.snapshot.CapabilityGeneration)
	if reason != "mode_or_context_drift" || expected {
		t.Fatalf("multi-revision Deliver jump was accepted: reason=%q expected=%t",
			reason, expected)
	}
	machine.turn.Mode.Revision = 2
	machine.turn.Mode.Surface = domain.ExecutionSurfaceCyber
	reason, expected = standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, machine.permission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint,
		machine.snapshot.CapabilityGeneration)
	if reason != "mode_or_context_drift" || expected {
		t.Fatalf("cross-surface Deliver transition was accepted: reason=%q expected=%t",
			reason, expected)
	}
	machine.turn.Mode.Surface = domain.ExecutionSurfaceCode
	profile.ID = "profile-drifted"
	reason, expected = standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, machine.permission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint,
		machine.snapshot.CapabilityGeneration)
	if reason != "execution_context_drift" || !expected {
		t.Fatalf("execution tuple drift was missed: reason=%q expected=%t",
			reason, expected)
	}
	profile.ID = machine.snapshot.ProfileSnapshotID
	driftedPermission := machine.permission
	driftedPermission.Revision++
	reason, expected = standardCodeTurnDriftReason(machine.snapshot, machine.turn,
		profile, interaction, driftedPermission, browserCDP, true, machine.fileAuthority,
		machine.snapshot.WorkspaceRootFingerprint, deliverGeneration)
	if reason != "permission_drift" || !expected {
		t.Fatalf("permission drift was missed: reason=%q expected=%t", reason, expected)
	}
}

func TestStandardCodeSupervisorRequiresTwoConsecutiveReadRounds(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorInspect, domain.ExecutionPhasePlan)
	ctx := context.Background()
	for round := 1; round <= 2; round++ {
		toolName := string(toolgateway.WorkspaceReadTool)
		payload := `{"version":"agent-code-tools.v1"}`
		if round == 2 {
			toolName = string(toolgateway.CodeWorkspaceSymbolsTool)
			payload = `{"version":"code-intel-lsp.v1"}`
		}
		call := standardCodePendingCall(fmt.Sprintf("read-%d", round),
			toolName, payload)
		call.Round = round
		call = standardCodeObserveCompleted(t, machine, call, nil,
			fmt.Sprintf("evidence-%d", round))
		completedAt := time.Now().UTC()
		if err := machine.ObserveRound(ctx, domain.SupervisorToolRound{
			RunID: call.RunID, Turn: call.Turn, AttemptID: call.AttemptID,
			Round: round, ModelAttempt: 1, Calls: []domain.SupervisorToolCall{call},
			CreatedAt: call.CreatedAt, CompletedAt: &completedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if round == 1 && machine.snapshot.InspectionComplete {
			t.Fatal("one read round unexpectedly completed inspection")
		}
	}
	if !machine.snapshot.InspectionComplete ||
		machine.snapshot.State != domain.StandardCodeSupervisorPlan {
		t.Fatalf("inspection state=%s complete=%t rounds=%d",
			machine.snapshot.State, machine.snapshot.InspectionComplete,
			machine.snapshot.ConsecutiveReadRounds)
	}
	plan := standardCodePendingCall("plan-1", string(toolgateway.PlanDeliveryProposeTool), `{}`)
	if decision, err := machine.Authorize(ctx, plan); err != nil || !decision.Allowed {
		t.Fatalf("completed inspection did not authorize plan: %+v err=%v", decision, err)
	}
}

func TestStandardCodeSupervisorDeniedResultCannotAdvanceState(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorInspect, domain.ExecutionPhasePlan)
	call := standardCodeWorkspaceApply(t, "denied-apply", "unreviewed-edit")
	decision, err := machine.Authorize(context.Background(), call)
	if err != nil || decision.Allowed || decision.Result == nil {
		t.Fatalf("unreviewed Plan apply was not denied: decision=%+v err=%v",
			decision, err)
	}
	call = standardCodeCompleteCall(t, call, domain.SupervisorToolDenied, nil, "",
		string(apperror.CodePolicyDenied))
	if err := machine.ObserveCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if machine.snapshot.State != domain.StandardCodeSupervisorInspect ||
		machine.snapshot.MutationEpoch != 0 {
		t.Fatalf("denied result advanced completion state: %+v", machine.snapshot)
	}
}

func TestStandardCodeSupervisorStoppedRestartDoesNotReviveAuthorizedCommand(t *testing.T) {
	machine, store := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	machine.snapshot.MutationEpoch = 1
	store.snapshot = machine.snapshot
	call := standardCodeCommandCall(t, "authorized-before-stop",
		toolgateway.CommandRuntimeActionRun, "go test ./...", "", 0)
	decision, err := machine.Authorize(context.Background(), call)
	if err != nil || !decision.Allowed {
		t.Fatalf("initial command authorization failed: decision=%+v err=%v", decision, err)
	}
	previous := machine.snapshot.State
	machine.snapshot.State = domain.StandardCodeSupervisorStopped
	machine.snapshot.StopReason = "permission_drift"
	if err := machine.appendFrom(context.Background(), previous,
		domain.StandardCodeSupervisorStoppedRecord, domain.StandardCodeSupervisorDenied,
		"", "", "", domain.StandardCodeToolOther, "", "", "", "",
		machine.snapshot.StopReason); err != nil {
		t.Fatal(err)
	}
	decision, err = machine.Authorize(context.Background(), call)
	if err != nil || decision.Allowed || decision.Result == nil {
		t.Fatalf("stopped restart revived an authorized command: decision=%+v err=%v",
			decision, err)
	}
}

func TestStandardCodeSupervisorUnfencedReadCannotSatisfyInspection(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorInspect, domain.ExecutionPhasePlan)
	call := standardCodePendingCall("legacy-read", string(toolgateway.WorkspaceReadTool),
		`{"version":"agent-code-tools.v1"}`)
	call = standardCodeCompleteCall(t, call, domain.SupervisorToolCompleted, nil,
		"legacy evidence", "")
	if err := machine.ObserveCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	if err := machine.ObserveRound(context.Background(), domain.SupervisorToolRound{
		RunID: call.RunID, Turn: call.Turn, AttemptID: call.AttemptID,
		Round: call.Round, ModelAttempt: call.ModelAttempt,
		Calls: []domain.SupervisorToolCall{call}, CreatedAt: call.CreatedAt,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if machine.snapshot.ConsecutiveReadRounds != 0 || machine.snapshot.InspectionComplete {
		t.Fatalf("unfenced legacy read satisfied inspection: %+v", machine.snapshot)
	}
}

func TestStandardCodeSupervisorAcceptsOnlyCheckpointProjectedCapabilityRefresh(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	current := machine.snapshot.CapabilityGeneration
	next := runmutation.Fingerprint("next-checkpoint-capability")
	if !standardCodeCapabilityGenerationMatches(machine.snapshot, current) ||
		standardCodeCapabilityGenerationMatches(machine.snapshot, next) {
		t.Fatal("unmodified Workspace capability binding was not exact")
	}
	machine.snapshot.MutationEpoch = 1
	machine.snapshot.ExpectedCapabilityGeneration = next
	if !standardCodeCapabilityGenerationMatches(machine.snapshot, next) ||
		standardCodeCapabilityGenerationMatches(machine.snapshot, current) ||
		standardCodeCapabilityGenerationMatches(machine.snapshot,
			runmutation.Fingerprint("unrelated-workspace-drift")) {
		t.Fatal("checkpoint-projected capability refresh accepted stale or unrelated drift")
	}
}

func TestStandardCodeSupervisorMutationUsesOwnedFileWorkspace(t *testing.T) {
	for _, target := range []string{"drydock-ws-1", "workspace-1"} {
		t.Run(target, func(t *testing.T) {
			machine, state := newStandardCodeSupervisorTestMachine(
				domain.StandardCodeSupervisorCheckpoint, domain.ExecutionPhaseDeliver)
			machine.fileWorkspaceID = "drydock-ws-1"
			standardCodeObserveCompleted(t, machine, standardCodeWorkspaceProposal(t, "owned-proposal"), nil, "reviewed")
			standardCodeAddCheckpoint(machine, state, "owned-edit")
			state.transactions[0].WorkspaceID = target
			checkpoint := state.checkpoints["after-owned-edit"]
			checkpoint.WorkspaceID = target
			state.checkpoints[checkpoint.ID] = checkpoint
			standardCodeObserveCompleted(t, machine,
				standardCodeWorkspaceApply(t, "owned-apply", "owned-edit"),
				map[string]string{"status": "applied"}, "applied")
			if machine.snapshot.WorkspaceID != "workspace-1" {
				t.Fatal("mutation reinterpreted the supervisor source control identity")
			}
			if target == machine.fileWorkspaceID {
				if machine.snapshot.MutationEpoch != 1 || machine.snapshot.State != domain.StandardCodeSupervisorExecute ||
					machine.snapshot.WorkspaceRootFingerprint != checkpoint.RootFingerprint {
					t.Fatalf("owned mutation was not observed: %+v", machine.snapshot)
				}
			} else if machine.snapshot.MutationEpoch != 0 || machine.snapshot.StopReason != "workspace_mutation_checkpoint_missing" {
				t.Fatalf("historical source mutation was reinterpreted as owned: %+v", machine.snapshot)
			}
		})
	}
}

func TestStandardCodeSupervisorFailureFixAndCurrentVerification(t *testing.T) {
	machine, store := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorCheckpoint, domain.ExecutionPhaseDeliver)
	standardCodeObserveCompleted(t, machine, standardCodeWorkspaceProposal(t, "proposal-1"), nil, "reviewed")
	standardCodeAddCheckpoint(machine, store, "edit-1")
	apply := standardCodeWorkspaceApply(t, "apply-1", "edit-1")
	standardCodeObserveCompleted(t, machine, apply, map[string]string{"status": "applied"}, "applied")
	if machine.snapshot.MutationEpoch != 1 ||
		machine.snapshot.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("first mutation state=%s epoch=%d", machine.snapshot.State,
			machine.snapshot.MutationEpoch)
	}

	failedExit := 1
	failed := standardCodeCommandCall(t, "command-failed", toolgateway.CommandRuntimeActionRun,
		"go test ./...", "", 0)
	standardCodeObserveCompleted(t, machine, failed, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-failed", State: runner.CommandRuntimeJobFailed,
				ExitCode: &failedExit, TreeReaped: true}}, nil))
	if machine.snapshot.State != domain.StandardCodeSupervisorDiagnose ||
		machine.snapshot.VerifiedMutationEpoch != 0 ||
		len(machine.snapshot.VerificationJobIDs) != 1 || machine.snapshot.VerificationJobIDs[0] != "job-failed" {
		t.Fatalf("failure was not diagnosed: state=%s verified=%d",
			machine.snapshot.State, machine.snapshot.VerifiedMutationEpoch)
	}

	standardCodeObserveCompleted(t, machine, standardCodeWorkspaceProposal(t, "proposal-fix"), nil, "reviewed fix")
	standardCodeAddCheckpoint(machine, store, "edit-fix")
	standardCodeObserveCompleted(t, machine,
		standardCodeWorkspaceApply(t, "apply-fix", "edit-fix"),
		map[string]string{"status": "applied"}, "fixed")
	if machine.snapshot.MutationEpoch != 2 || machine.snapshot.FixRounds != 1 ||
		len(machine.snapshot.VerificationJobIDs) != 0 {
		t.Fatalf("fix was not recorded: epoch=%d fixes=%d",
			machine.snapshot.MutationEpoch, machine.snapshot.FixRounds)
	}

	successExit := 0
	success := standardCodeCommandCall(t, "command-success", toolgateway.CommandRuntimeActionRun,
		"go test ./...", "", 0)
	standardCodeObserveCompleted(t, machine, success, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-success",
				State: runner.CommandRuntimeJobCompleted, ExitCode: &successExit,
				TreeReaped: true, StdoutSHA256: strings.Repeat("d", 64)}}, nil))
	if !machine.snapshot.CanDeliver() || machine.snapshot.VerifiedMutationEpoch != 2 ||
		len(machine.snapshot.VerificationJobIDs) != 1 || machine.snapshot.VerificationJobIDs[0] != "job-success" {
		t.Fatalf("current mutation was not verified: %+v", machine.snapshot)
	}
	if err := machine.ValidateAction(context.Background(), domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: "done", Summary: "verified",
	}); err == nil || err.Error() != "finish_delivery_gate_unavailable" {
		t.Fatalf("structural verification bypassed the delivery truth gate: %v", err)
	}

	restarted := &standardCodeSupervisorTurn{store: store, turn: machine.turn,
		permission: machine.permission, snapshot: store.snapshot, fileWorkspaceID: machine.fileWorkspaceID,
		ledger: append([]domain.StandardCodeSupervisorLedgerEntry(nil), store.ledger...)}
	duplicate := standardCodeWorkspaceApply(t, "apply-duplicate", "edit-1")
	decision, err := restarted.Authorize(context.Background(), duplicate)
	if err != nil || !decision.Replayed || decision.Result == nil || decision.Allowed {
		t.Fatalf("restart did not suppress duplicate apply: %+v err=%v", decision, err)
	}
}

func TestStandardCodeSupervisorPreservesOnlyExplicitNonPassingVerificationJobs(t *testing.T) {
	exitZero, exitFailure := 0, 2
	for _, test := range []struct {
		name       string
		job        runner.CommandRuntimeJobSnapshot
		incomplete bool
		invalid    bool
		missing    bool
		wantJob    bool
	}{
		{name: "failed", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobFailed}, wantJob: true},
		{name: "timeout", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobTimedOut}, wantJob: true},
		{name: "cancelled", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobCancelled}, wantJob: true},
		{name: "interrupted", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobInterrupted}, wantJob: true},
		{name: "nonzero_exit", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobCompleted, ExitCode: &exitFailure, TreeReaped: true}, wantJob: true},
		{name: "truncated", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobCompleted, ExitCode: &exitZero, TreeReaped: true, TruncationReason: "inline_limit"}, wantJob: true},
		{name: "incomplete_all_success", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobCompleted, ExitCode: &exitZero, TreeReaped: true}, incomplete: true},
		{name: "invalid_projection", job: runner.CommandRuntimeJobSnapshot{State: runner.CommandRuntimeJobFailed}, invalid: true},
		{name: "missing_jobs", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			machine, state := newStandardCodeSupervisorTestMachine(
				domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
			machine.snapshot.MutationEpoch = 1
			state.snapshot = machine.snapshot
			test.job.ID = "observed-verification-job"
			projection := standardCodeCommandProjection{Version: runner.CommandRuntimeResultVersion,
				Action: toolgateway.CommandRuntimeActionRun, Jobs: []runner.CommandRuntimeJobSnapshot{test.job}}
			if test.incomplete {
				projection.IncompleteReasons = []string{"adapter verification evidence is incomplete"}
			}
			if test.invalid {
				projection.Version = "invalid"
			}
			if test.missing {
				projection.Jobs = nil
			}
			encoded, err := json.Marshal(projection)
			if err != nil {
				t.Fatal(err)
			}
			call := standardCodeCommandCall(t, "verification-observation", toolgateway.CommandRuntimeActionRun,
				"go test ./...", "", 0)
			standardCodeObserveCompleted(t, machine, call, nil, string(encoded))
			if machine.snapshot.State != domain.StandardCodeSupervisorDiagnose ||
				machine.snapshot.CanDeliver() || state.snapshot.CanDeliver() ||
				machine.snapshot.VerifiedMutationEpoch != 0 {
				t.Fatalf("nonpassing evidence authorized completion: %+v", machine.snapshot)
			}
			ids := state.snapshot.VerificationJobIDs
			if test.wantJob {
				if len(ids) != 1 || ids[0] != test.job.ID {
					t.Fatalf("actual nonpassing verification was lost: ids=%v", ids)
				}
			} else if len(ids) != 0 {
				t.Fatalf("unproven projection became verification evidence: ids=%v", ids)
			}
			if err := machine.ValidateAction(t.Context(), domain.RootAction{
				Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
				Message: "done", Summary: "verification attempted",
			}); err == nil || err.Error() != "finish_requires_current_structural_verification" {
				t.Fatalf("nonpassing evidence bypassed the completion gate: %v", err)
			}
		})
	}
}

func TestStandardCodeSupervisorFinalResponseUsesDeliveryProjection(t *testing.T) {
	machine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorDeliver, domain.ExecutionPhaseDeliver)
	machine.report = &standardcodedelivery.Report{Status: standardcodedelivery.StatusPassed,
		Verified: true, ReceiptSHA256: strings.Repeat("a", 64),
		Diff:            standardcodedelivery.Diff{ChangedCount: 3},
		Verifications:   []standardcodedelivery.Verification{{JobID: "job-1"}},
		FinalCheckpoint: standardcodedelivery.Checkpoint{ID: "checkpoint-final"},
		Links:           standardcodedelivery.Links{Self: "/api/v1/runs/run-1/standard-code-delivery"}}
	projected := machine.ProjectDeliveryAction(domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: "Agent says everything passed", Summary: "Agent-only claim"})
	if !strings.Contains(projected.Message, "3 affected file(s)") ||
		!strings.Contains(projected.Message, "1 verification command(s)") ||
		!strings.Contains(projected.Message, machine.report.Links.Self) ||
		strings.Contains(projected.Message, "Agent says") ||
		projected.Reason != "" {
		t.Fatalf("final response did not use delivery projection: %+v", projected)
	}
	if err := projected.Validate(); err != nil {
		t.Fatalf("projected delivery must remain a valid root action: %v", err)
	}
}

// The report below is a projection fixture, not proof of command execution.
// The real SQLite boundary must accept its structurally valid finish action.
func TestStandardCodeSupervisorDeliveryProjectionCompletesSQLiteTurn(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := NewRunService(st)
	_, run, err := service.Create(ctx, CreateRunRequest{Goal: "review delivery projection", Profile: "review", Budget: domain.Budget{MaxTurns: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "delivery-projection-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(ctx, claim.Lease, "Review the current delivery and finish this noninteractive Run.")
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Provider: "test", Model: "model", Text: "done", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	checkpoint, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := newStandardCodeSupervisorTestMachine(domain.StandardCodeSupervisorDeliver, domain.ExecutionPhaseDeliver)
	machine.report = &standardcodedelivery.Report{Status: standardcodedelivery.StatusPassed, Verified: true,
		ReceiptSHA256: strings.Repeat("a", 64), FinalCheckpoint: standardcodedelivery.Checkpoint{ID: "checkpoint-final"},
		Links: standardcodedelivery.Links{Self: "/api/v1/runs/" + run.ID + "/standard-code-delivery"}}
	action := machine.ProjectDeliveryAction(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish, Message: "done", Summary: "done"})
	completed, _, messages, err := st.CompleteSupervisorTurn(ctx, checkpoint, response, action, policy.Decision{Allowed: true, Reason: "allowed"}, 0)
	if err != nil {
		t.Fatalf("delivery projection was rejected at the real persistence boundary: %v", err)
	}
	if completed.Status != domain.RunCompleted || completed.FinishedAt == nil || messages.Assistant.Content != action.Message {
		t.Fatalf("finish or its receipt-backed message did not persist: run=%+v messages=%+v", completed, messages)
	}
}

func TestStandardCodeSupervisorPersistentFailureStopsAtFixBudget(t *testing.T) {
	machine, store := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorEdit, domain.ExecutionPhaseDeliver)
	standardCodeAddCheckpoint(machine, store, "initial-edit")
	standardCodeObserveCompleted(t, machine,
		standardCodeWorkspaceApply(t, "initial-apply", "initial-edit"),
		map[string]string{"status": "applied"}, "applied")
	failedExit := 1
	for attempt := 0; attempt <= domain.StandardCodeSupervisorMaximumFixRounds; attempt++ {
		command := standardCodeCommandCall(t, fmt.Sprintf("failure-%d", attempt),
			toolgateway.CommandRuntimeActionRun, fmt.Sprintf("go test ./... #%d", attempt), "", 0)
		standardCodeObserveCompleted(t, machine, command, nil,
			standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
				[]runner.CommandRuntimeJobSnapshot{{ID: fmt.Sprintf("failed-job-%d", attempt),
					State: runner.CommandRuntimeJobFailed, ExitCode: &failedExit, TreeReaped: true,
					StderrSHA256: runmutation.Fingerprint("distinct-failure", fmt.Sprint(attempt))}}, nil))
		if machine.snapshot.State != domain.StandardCodeSupervisorDiagnose {
			t.Fatalf("attempt %d left diagnose state: %s", attempt, machine.snapshot.State)
		}
		if attempt == domain.StandardCodeSupervisorMaximumFixRounds {
			break
		}
		editID := fmt.Sprintf("fix-edit-%d", attempt)
		standardCodeObserveCompleted(t, machine,
			standardCodeWorkspaceProposal(t, fmt.Sprintf("fix-proposal-%d", attempt)), nil, "reviewed")
		standardCodeAddCheckpoint(machine, store, editID)
		standardCodeObserveCompleted(t, machine,
			standardCodeWorkspaceApply(t, fmt.Sprintf("fix-apply-%d", attempt), editID),
			map[string]string{"status": "applied"}, "fixed")
	}
	standardCodeObserveCompleted(t, machine,
		standardCodeWorkspaceProposal(t, "exhausted-proposal"), nil, "reviewed")
	decision, err := machine.Authorize(context.Background(),
		standardCodeWorkspaceApply(t, "exhausted-apply", "exhausted-edit"))
	if err != nil || decision.Allowed || decision.Result == nil ||
		machine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		machine.snapshot.StopReason != "fix_round_budget_exhausted" ||
		machine.snapshot.CanDeliver() {
		t.Fatalf("persistent failure was not stopped: decision=%+v snapshot=%+v err=%v",
			decision, machine.snapshot, err)
	}
	if err := machine.ValidateAction(context.Background(), domain.RootAction{
		Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish,
		Message: "done", Summary: "not verified",
	}); err == nil {
		t.Fatal("persistent failure reported false success")
	}
}

func TestStandardCodeSupervisorCommandJobAndOutputBudgetsStop(t *testing.T) {
	commandMachine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	commandMachine.snapshot.MutationEpoch = 1
	commandMachine.snapshot.CommandsUsed = commandMachine.snapshot.Limits.MaximumCommands
	decision, err := commandMachine.Authorize(context.Background(),
		standardCodeCommandCall(t, "command-budget", toolgateway.CommandRuntimeActionRun,
			"go test ./...", "", 0))
	if err != nil || decision.Allowed || decision.Result == nil ||
		commandMachine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		commandMachine.snapshot.StopReason != "command_budget_exhausted" {
		t.Fatalf("command budget did not stop: decision=%+v snapshot=%+v err=%v",
			decision, commandMachine.snapshot, err)
	}

	jobMachine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	jobMachine.snapshot.MutationEpoch = 1
	jobMachine.snapshot.JobsStarted = jobMachine.snapshot.Limits.MaximumJobs
	decision, err = jobMachine.Authorize(context.Background(),
		standardCodeCommandCall(t, "job-budget", toolgateway.CommandRuntimeActionStart,
			"go test ./...", "", 0))
	if err != nil || decision.Allowed || decision.Result == nil ||
		jobMachine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		jobMachine.snapshot.StopReason != "background_job_budget_exhausted" {
		t.Fatalf("Job budget did not stop: decision=%+v snapshot=%+v err=%v",
			decision, jobMachine.snapshot, err)
	}

	outputMachine, _ := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorInspect, domain.ExecutionPhasePlan)
	outputMachine.snapshot.OutputBytes = outputMachine.snapshot.Limits.MaximumOutputBytes - 1
	standardCodeObserveCompleted(t, outputMachine,
		standardCodePendingCall("output-budget", string(toolgateway.WorkspaceReadTool),
			`{"version":"agent-code-tools.v1"}`), nil, "xx")
	if outputMachine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		outputMachine.snapshot.StopReason != "output_budget_exhausted" ||
		outputMachine.snapshot.OutputBytes != outputMachine.snapshot.Limits.MaximumOutputBytes {
		t.Fatalf("output budget did not stop: %+v", outputMachine.snapshot)
	}
}

func TestStandardCodeSupervisorEquivalentFailuresStopAtRepeatBudget(t *testing.T) {
	machine, store := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorEdit, domain.ExecutionPhaseDeliver)
	standardCodeAddCheckpoint(machine, store, "repeat-initial")
	standardCodeObserveCompleted(t, machine,
		standardCodeWorkspaceApply(t, "repeat-initial-apply", "repeat-initial"),
		map[string]string{"status": "applied"}, "applied")
	failedExit := 1
	failureDigest := runmutation.Fingerprint("same-failure-output")
	for attempt := 0; attempt < domain.StandardCodeSupervisorMaximumRepeatedFailures; attempt++ {
		standardCodeObserveCompleted(t, machine,
			standardCodeCommandCall(t, fmt.Sprintf("repeat-command-%d", attempt),
				toolgateway.CommandRuntimeActionRun, "go test ./...", "", 0), nil,
			standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
				[]runner.CommandRuntimeJobSnapshot{{ID: fmt.Sprintf("repeat-job-%d", attempt),
					State: runner.CommandRuntimeJobFailed, ExitCode: &failedExit,
					TreeReaped: true, StderrSHA256: failureDigest}}, nil))
		if attempt == domain.StandardCodeSupervisorMaximumRepeatedFailures-1 {
			break
		}
		editID := fmt.Sprintf("repeat-fix-%d", attempt)
		standardCodeObserveCompleted(t, machine,
			standardCodeWorkspaceProposal(t, fmt.Sprintf("repeat-proposal-%d", attempt)),
			nil, "reviewed")
		standardCodeAddCheckpoint(machine, store, editID)
		standardCodeObserveCompleted(t, machine,
			standardCodeWorkspaceApply(t, fmt.Sprintf("repeat-apply-%d", attempt), editID),
			map[string]string{"status": "applied"}, "fixed")
	}
	if machine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		machine.snapshot.StopReason != "repeated_failure_budget_exhausted" ||
		machine.snapshot.RepeatedFailureCount !=
			domain.StandardCodeSupervisorMaximumRepeatedFailures ||
		machine.snapshot.CanDeliver() {
		t.Fatalf("equivalent failures did not stop safely: %+v", machine.snapshot)
	}
}

func TestStandardCodeSupervisorBackgroundJobOwnershipAcrossRestart(t *testing.T) {
	machine, store := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	machine.snapshot.MutationEpoch = 1
	store.snapshot = machine.snapshot
	start := standardCodeCommandCall(t, "start-1", toolgateway.CommandRuntimeActionStart,
		"go test ./...", "", 0)
	standardCodeObserveCompleted(t, machine, start, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionStart,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1", State: runner.CommandRuntimeJobRunning,
				OutputBaseCursor: 0, OutputCursor: 0}}, nil))
	if len(machine.snapshot.Jobs) != 1 || machine.snapshot.Jobs[0].JobID != "job-1" {
		t.Fatalf("background owner was not persisted: %+v", machine.snapshot.Jobs)
	}

	restarted := &standardCodeSupervisorTurn{store: store, turn: machine.turn,
		permission: machine.permission, snapshot: store.snapshot, fileWorkspaceID: machine.fileWorkspaceID,
		ledger: append([]domain.StandardCodeSupervisorLedgerEntry(nil), store.ledger...)}
	read := standardCodeCommandCall(t, "read-1", toolgateway.CommandRuntimeActionRead,
		"", "job-1", 0)
	standardCodeObserveCompleted(t, restarted, read, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRead,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1", State: runner.CommandRuntimeJobRunning,
				OutputBaseCursor: 0, OutputCursor: 5}},
			[]runner.CommandRuntimeOutputPage{{JobID: "job-1", BaseCursor: 0,
				NextCursor: 5, EndCursor: 5, State: runner.CommandRuntimeJobRunning}}))
	if restarted.snapshot.Jobs[0].LastCursor != 5 {
		t.Fatalf("read cursor was not persisted: %+v", restarted.snapshot.Jobs[0])
	}

	stale := standardCodeCommandCall(t, "wait-stale", toolgateway.CommandRuntimeActionWait,
		"", "job-1", 0)
	if decision, err := restarted.Authorize(context.Background(), stale); err != nil ||
		decision.Allowed || decision.Result == nil {
		t.Fatalf("stale cursor was not denied: %+v err=%v", decision, err)
	}
	wait := standardCodeCommandCall(t, "wait-1", toolgateway.CommandRuntimeActionWait,
		"", "job-1", 5)
	exitCode := 0
	standardCodeObserveCompleted(t, restarted, wait, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionWait,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1", State: runner.CommandRuntimeJobCompleted,
				ExitCode: &exitCode, OutputBaseCursor: 0, OutputCursor: 5, TreeReaped: true}},
			[]runner.CommandRuntimeOutputPage{{JobID: "job-1", BaseCursor: 0,
				NextCursor: 5, EndCursor: 5, State: runner.CommandRuntimeJobCompleted,
				ExitCode: &exitCode}}))
	if !restarted.snapshot.CanDeliver() {
		t.Fatalf("terminal background verification did not deliver: %+v", restarted.snapshot)
	}
	restarted.snapshot.MutationEpoch++
	restarted.snapshot.State = domain.StandardCodeSupervisorExecute
	staleEpoch := standardCodeCommandCall(t, "wait-stale-epoch",
		toolgateway.CommandRuntimeActionWait, "", "job-1", 5)
	standardCodeObserveCompleted(t, restarted, staleEpoch, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionWait,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1",
				State: runner.CommandRuntimeJobCompleted, ExitCode: &exitCode,
				OutputBaseCursor: 0, OutputCursor: 5, TreeReaped: true}},
			[]runner.CommandRuntimeOutputPage{{JobID: "job-1", BaseCursor: 0,
				NextCursor: 5, EndCursor: 5, State: runner.CommandRuntimeJobCompleted,
				ExitCode: &exitCode}}))
	if restarted.snapshot.State != domain.StandardCodeSupervisorExecute ||
		restarted.snapshot.VerifiedMutationEpoch == restarted.snapshot.MutationEpoch ||
		restarted.snapshot.CanDeliver() {
		t.Fatalf("stale background Job verified a newer mutation: %+v", restarted.snapshot)
	}

	drifted := &standardCodeSupervisorTurn{store: store, turn: restarted.turn,
		permission: restarted.permission, snapshot: store.snapshot, fileWorkspaceID: restarted.fileWorkspaceID,
		ledger: append([]domain.StandardCodeSupervisorLedgerEntry(nil), store.ledger...)}
	drifted.permission.Revision++
	if decision, err := drifted.Authorize(context.Background(),
		standardCodeCommandCall(t, "read-drift", toolgateway.CommandRuntimeActionRead,
			"", "job-1", 5)); err != nil || decision.Allowed || decision.Result == nil {
		t.Fatalf("permission drift did not deny job access: %+v err=%v", decision, err)
	}

	// Rehydrate after the concurrent denied decision, just as the supervisor
	// does at the next durable continuation boundary.
	restarted.snapshot = store.snapshot
	restarted.ledger = append([]domain.StandardCodeSupervisorLedgerEntry(nil), store.ledger...)
	cancel := standardCodeCommandCall(t, "cancel-1", toolgateway.CommandRuntimeActionCancel,
		"", "job-1", 0)
	standardCodeObserveCompleted(t, restarted, cancel, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionCancel,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1", State: runner.CommandRuntimeJobCancelled,
				ExitCode: &exitCode, OutputCursor: 5, TreeReaped: true}}, nil))
	replayed := &standardCodeSupervisorTurn{store: store, turn: restarted.turn,
		permission: restarted.permission, snapshot: store.snapshot, fileWorkspaceID: restarted.fileWorkspaceID,
		ledger: append([]domain.StandardCodeSupervisorLedgerEntry(nil), store.ledger...)}
	decision, err := replayed.Authorize(context.Background(),
		standardCodeCommandCall(t, "cancel-duplicate", toolgateway.CommandRuntimeActionCancel,
			"", "job-1", 0))
	if err != nil || !decision.Replayed || decision.Result == nil || decision.Allowed {
		t.Fatalf("duplicate cancel was not suppressed: %+v err=%v", decision, err)
	}
	previous := replayed.snapshot.State
	replayed.snapshot.State = domain.StandardCodeSupervisorStopped
	replayed.snapshot.StopReason = "no_progress_budget_exhausted"
	replayed.snapshot.NoProgressCount = replayed.snapshot.Limits.MaximumNoProgress
	if err := replayed.appendFrom(context.Background(), previous,
		domain.StandardCodeSupervisorStoppedRecord, domain.StandardCodeSupervisorDenied,
		"", "", "", domain.StandardCodeToolOther, "", "", "", "",
		replayed.snapshot.StopReason); err != nil {
		t.Fatal(err)
	}
	kill := standardCodeCommandCall(t, "kill-after-stop", toolgateway.CommandRuntimeActionKill,
		"", "job-1", 0)
	standardCodeObserveCompleted(t, replayed, kill, nil,
		standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionKill,
			[]runner.CommandRuntimeJobSnapshot{{ID: "job-1", State: runner.CommandRuntimeJobKilled,
				ExitCode: &exitCode, OutputCursor: 5, TreeReaped: true}}, nil))
	if replayed.snapshot.State != domain.StandardCodeSupervisorStopped ||
		replayed.snapshot.StopReason != "no_progress_budget_exhausted" ||
		replayed.snapshot.Jobs[0].State != string(runner.CommandRuntimeJobKilled) {
		t.Fatalf("stopped cleanup changed completion decision or lost Job state: %+v",
			replayed.snapshot)
	}
}

func TestStandardCodeSupervisorPersistsInitialStateWithExactPresetBinding(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "standard-code-supervisor-ledger")
	runtime := CapabilityReadinessRuntime{
		RunControlEnabled:                 true,
		RunExecutionEnabled:               true,
		ExecutionPermissionControlEnabled: true,
		StandardCodePresetEnabled:         true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
		},
		LocalSandboxInstalled: true, LocalSandboxProven: true, LocalBackendReady: true,
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{
			commandruntimeadapter.SandboxedWorkspace(
				CommandRuntimeLocalSandboxBackend, "local-windows-lpac.v1",
				"standard-code-supervisor-test-generation"),
		},
	}
	presets, err := NewStandardCodePresetService(fixture.state, fixture.service, runtime)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := presets.Configure(t.Context(), ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: fixture.run.ID,
		BackendIntent: "auto", Action: "configure",
		OperationKey: "supervisor-ledger-preview-0001", RequestedBy: "operator",
	})
	if err != nil || !preview.TrustRequired || preview.TrustDigest == "" {
		t.Fatalf("preset preview=%+v err=%v", preview, err)
	}
	if _, err := presets.Configure(t.Context(), ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: fixture.run.ID,
		BackendIntent: "auto", Action: "configure",
		OperationKey: "supervisor-ledger-configure-0001", RequestedBy: "operator",
		ConfirmWorkspaceTrust: true, ExpectedTrustDigest: preview.TrustDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(fixture.state).Start(t.Context(), fixture.run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.state.AcquireRunExecutionLease(t.Context(),
		domain.AcquireRunExecutionLeaseRequest{RunID: fixture.run.ID,
			OwnerID: "standard-code-supervisor-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := fixture.state.BeginSupervisorTurn(t.Context(), lease.Lease,
		"inspect the repository")
	if err != nil {
		t.Fatal(err)
	}
	permission, err := fixture.state.GetRunExecutionPermission(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &AgentRunner{store: fixture.state, drydocks: fixture.service}
	capabilities, authority, err := supervisor.supervisorAgentCodeCapabilities(
		t.Context(), turn, permission)
	if err != nil || capabilities.Generation == "" {
		t.Fatalf("agent code capabilities=%+v err=%v", capabilities, err)
	}
	machine, err := supervisor.prepareStandardCodeSupervisor(t.Context(), turn,
		permission, capabilities.Generation, authority)
	if err != nil || machine == nil || machine.snapshot.Version != 1 ||
		machine.snapshot.State != domain.StandardCodeSupervisorInspect {
		t.Fatalf("prepared machine=%+v err=%v", machine, err)
	}
	ledger, err := fixture.state.ListStandardCodeSupervisorLedger(t.Context(),
		fixture.run.ID, domain.StandardCodeSupervisorMaximumLedgerEntries)
	if err != nil || len(ledger) != 1 ||
		ledger[0].Kind != domain.StandardCodeSupervisorInitialized ||
		ledger[0].Snapshot.PresetOperationKeyDigest == "" {
		t.Fatalf("persisted ledger=%+v err=%v", ledger, err)
	}
	replayed, err := supervisor.prepareStandardCodeSupervisor(t.Context(), turn,
		permission, capabilities.Generation, authority)
	if err != nil || replayed == nil || replayed.snapshot.Version != 1 {
		t.Fatalf("same turn preparation did not replay: machine=%+v err=%v", replayed, err)
	}
	payload, err := toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceReadTool,
		json.RawMessage(`{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`))
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.SupervisorToolOperationKey(turn.Run.ID,
		turn.Checkpoint.NextTurn, string(toolgateway.WorkspaceReadTool), string(payload))
	callID, err := runmutation.SupervisorToolCallID(operationKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1,
		Provider: "test", Model: "model"}
	if inserted, err := fixture.state.RecordSupervisorModelStarted(t.Context(),
		turn.Checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("record model start: inserted=%t err=%v", inserted, err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := fixture.state.RecordSupervisorModelCompleted(t.Context(),
		turn.Checkpoint, attempt, llm.ChatResponse{Provider: "test", Model: "model",
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{{ID: callID,
				Name: string(toolgateway.WorkspaceReadTool), Arguments: payload,
				Authority: authority}}})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := fixture.state.ListSupervisorToolRounds(t.Context(), checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
		t.Fatalf("pending read round=%+v err=%v", rounds, err)
	}
	call := rounds[0].Calls[0]
	if decision, err := machine.Authorize(t.Context(), call); err != nil || !decision.Allowed {
		t.Fatalf("persist call authorization: %+v err=%v", decision, err)
	}
	if inserted, err := fixture.state.RecordSupervisorToolExecutionStarted(t.Context(),
		checkpoint, call.CallID); err != nil || !inserted {
		t.Fatalf("record tool start: inserted=%t err=%v", inserted, err)
	}
	resultJSON, err := json.Marshal(supervisorToolResultEnvelope{
		Version: supervisorToolResultVersion, Tool: call.ToolName,
		Status: string(domain.SupervisorToolCompleted), Stdout: "bounded read evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	storedCall, replayedResult, err := fixture.state.RecordSupervisorToolResult(t.Context(),
		checkpoint, domain.SupervisorToolResult{CallID: call.CallID,
			Status: domain.SupervisorToolCompleted, ResultJSON: string(resultJSON),
			CompletedAt: time.Now().UTC()})
	if err != nil || replayedResult {
		t.Fatalf("record tool result: replayed=%t call=%+v err=%v",
			replayedResult, storedCall, err)
	}
	if err := machine.ObserveCall(t.Context(), storedCall); err != nil {
		t.Fatal(err)
	}
	rounds, err = fixture.state.ListSupervisorToolRounds(t.Context(), checkpoint)
	if err != nil || len(rounds) != 1 || !rounds[0].Complete() {
		t.Fatalf("completed read round=%+v err=%v", rounds, err)
	}
	if err := machine.ObserveRound(t.Context(), rounds[0]); err != nil {
		t.Fatal(err)
	}
	ledger, err = fixture.state.ListStandardCodeSupervisorLedger(t.Context(),
		fixture.run.ID, domain.StandardCodeSupervisorMaximumLedgerEntries)
	if err != nil || len(ledger) != 4 ||
		ledger[1].Kind != domain.StandardCodeSupervisorCallAuthorized ||
		ledger[2].Kind != domain.StandardCodeSupervisorCallObserved ||
		ledger[3].Kind != domain.StandardCodeSupervisorRoundObserved {
		t.Fatalf("durable call/observation ledger=%+v err=%v", ledger, err)
	}
	forged := *machine
	forged.snapshot = machine.snapshot
	forged.snapshot.ProfileSnapshotID = "forged-profile-snapshot"
	forged.ledger = append([]domain.StandardCodeSupervisorLedgerEntry(nil), machine.ledger...)
	if err := forged.appendFrom(t.Context(), forged.snapshot.State,
		domain.StandardCodeSupervisorActionRecorded, domain.StandardCodeSupervisorAllowed,
		"", "", string(domain.RootActionContinue), domain.StandardCodeToolOther,
		"", "", "", "", "forged_execution_tuple"); err == nil {
		t.Fatal("durable ledger accepted a non-current execution profile snapshot")
	}
}

type standardCodeAlwaysReviewPolicy struct {
	base policy.Checker
}

func (p standardCodeAlwaysReviewPolicy) CheckText(ctx, text string) policy.Decision {
	return p.base.CheckText(ctx, text)
}

func (p standardCodeAlwaysReviewPolicy) CheckToolCall(call tools.Call) policy.Decision {
	d := p.base.CheckToolCall(call)
	if call.Name == "create_file" || call.Name == "replace_file" || call.Name == "move_file" || call.Name == "delete_file" {
		d.NeedsApproval = true
	}
	return d
}

func TestStandardCodeSupervisorOneStepWorkspaceChangeAndDiagnoseBudget(t *testing.T) {
	machine, memStore := newStandardCodeSupervisorTestMachine(
		domain.StandardCodeSupervisorCheckpoint, domain.ExecutionPhaseDeliver)
	machine.permission.Mode = domain.RunExecutionPermissionAuto
	standardCodeAddCheckpoint(machine, memStore, "one-step-edit")
	standardCodeObserveCompleted(t, machine,
		standardCodeWorkspaceProposal(t, "one-step-change"),
		map[string]string{"status": fileedit.StatusApplied, "edit_id": "one-step-edit"}, "applied")
	if machine.snapshot.MutationEpoch != 1 || machine.snapshot.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("1-step workspace_change did not advance MutationEpoch/Execute: %+v", machine.snapshot)
	}
	failedExit := 1
	for attempt := 0; attempt <= domain.StandardCodeSupervisorMaximumFixRounds; attempt++ {
		command := standardCodeCommandCall(t, fmt.Sprintf("one-step-fail-%d", attempt),
			toolgateway.CommandRuntimeActionRun, fmt.Sprintf("go test ./... #%d", attempt), "", 0)
		standardCodeObserveCompleted(t, machine, command, nil,
			standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
				[]runner.CommandRuntimeJobSnapshot{{ID: fmt.Sprintf("one-step-job-%d", attempt),
					State: runner.CommandRuntimeJobFailed, ExitCode: &failedExit, TreeReaped: true,
					StderrSHA256: runmutation.Fingerprint("one-step-failure", fmt.Sprint(attempt))}}, nil))
		if machine.snapshot.State != domain.StandardCodeSupervisorDiagnose {
			t.Fatalf("attempt %d left diagnose state: %s", attempt, machine.snapshot.State)
		}
		if attempt == domain.StandardCodeSupervisorMaximumFixRounds {
			break
		}
		editID := fmt.Sprintf("one-step-fix-%d", attempt)
		standardCodeAddCheckpoint(machine, memStore, editID)
		standardCodeObserveCompleted(t, machine,
			standardCodeWorkspaceProposal(t, fmt.Sprintf("one-step-fix-proposal-%d", attempt)),
			map[string]string{"status": fileedit.StatusApplied, "edit_id": editID}, "fixed")
	}
	decision, err := machine.Authorize(context.Background(),
		standardCodeWorkspaceProposal(t, "one-step-exhausted"))
	if err != nil || decision.Allowed || decision.Result == nil ||
		machine.snapshot.State != domain.StandardCodeSupervisorStopped ||
		machine.snapshot.StopReason != "fix_round_budget_exhausted" {
		t.Fatalf("1-step fix round exhaustion did not stop on workspace_change: decision=%+v snapshot=%+v err=%v",
			decision, machine.snapshot, err)
	}
}

func TestStandardCodeSupervisorDirectPostApprovalApplyReconcilesCheckpoint(t *testing.T) {
	f, owned := newFileEditDrydockFixture(t)
	ctx := t.Context()
	checkpoints, err := NewWorkspaceCheckpointService(f.state, standardCodeThreadTestRuntime().ExecutionPermissionCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithCheckpointService(checkpoints)
	p := &continuationCodeProvider{}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	router.SetRoute("code", ref)
	checker := standardCodeAlwaysReviewPolicy{base: policy.NewDefaultChecker()}
	agentRunner := NewAgentRunner(f.state, router, checker).WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(standardCodeThreadTestRuntime().ExecutionPermissionCapabilities)
	wait := func(msg string) *llm.ChatResponse {
		raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: msg, Reason: "operator review"})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw)}
	}
	tool := func(name string, payload any) *llm.ChatResponse {
		var raw json.RawMessage
		if text, ok := payload.(string); ok {
			raw = json.RawMessage(text)
		} else {
			raw = mustAgentCodeDrydockJSON(t, payload)
		}
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("call-%d", p.calls+len(p.responses)+1), Name: name, Arguments: raw}}}
	}
	if _, err = NewRunService(f.state).Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":"plan_delivery.v1","directions":[
 {"title":"Small change","summary":"Change the tracked line","tradeoffs":["One scope"],"modules":[{"title":"Update line","objective":"Update tracked text","acceptance_criteria":["Text updated"],"dependencies":[]}]},
 {"title":"Broader review","summary":"Review surrounding text","tradeoffs":["More review"],"modules":[{"title":"Review text","objective":"Review text","acceptance_criteria":["Reviewed"],"dependencies":[]}]},
 {"title":"Defer change","summary":"Document limit","tradeoffs":["Later"],"modules":[{"title":"Document limit","objective":"Record boundary","acceptance_criteria":["Recorded"],"dependencies":[]}]}]}`
	p.responses = []*llm.ChatResponse{
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`),
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":2}`),
		tool("plan_delivery_propose", plan),
		wait("Choose direction"),
	}
	if _, err = agentRunner.StepWithInput(ctx, f.run.ID, "Plan tracked.txt update"); err != nil {
		t.Fatal(err)
	}
	proposals, err := f.state.ListPlanDeliveryProposals(ctx, f.run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("plan=%+v err=%v", proposals, err)
	}
	control := NewPlanDeliveryControlService(f.state)
	if _, err = control.SelectDirection(ctx, ControlPlanDirectionRequest{Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID, ProposalID: proposals[0].ID,
		Direction: 1, OperationKey: "sc-direct-select-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err = control.EnterDelivery(ctx, ControlPlanDeliveryTransitionRequest{Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID,
		OperationKey: "sc-direct-deliver-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.state.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunService(f.state).Resume(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	caps := standardCodeThreadTestRuntime().ExecutionPermissionCapabilities
	lifecycle := NewRunLifecycleControlService(f.state)
	execution := NewRunExecutionHandoffService(f.state, router, checker).
		WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(caps)
	turns := NewThreadTurnServiceWithExecutionCapabilities(f.state, lifecycle, execution, caps)
	p.responses = []*llm.ChatResponse{
		tool("workspace_change", toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
			Action: "propose_patch", Path: "tracked.txt", ExpectedSHA256: fileedit.HashText("base\n"),
			Replacements: []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "approved direct apply", ExpectedOccurrences: 1}}}),
		wait("Await approval"),
		wait("Direct apply observed; ready to verify"),
	}
	if _, err := turns.Execute(ctx, ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "Update tracked.txt", OperationKey: "sc-direct-exec-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	edits, err := f.state.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	if _, err = NewFileEditReviewService(f.state).WithRunWorktree(f.service).Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: edits[0].ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	resumed := turns.ResumeApproval(ctx, ApprovalContinuationRequest{RunID: f.run.ID, Kind: "file_edit", ProposalID: edits[0].ID})
	if resumed.State != "completed" || resumed.ToolCalled {
		t.Fatalf("direct approval continuation=%+v", resumed)
	}
	if got, readErr := os.ReadFile(filepath.Join(owned.Path, "tracked.txt")); readErr != nil || string(got) != "approved direct apply\n" {
		t.Fatalf("direct apply bytes=%q err=%v", got, readErr)
	}
	snapshot, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snapshot.MutationEpoch != 1 || snapshot.State != domain.StandardCodeSupervisorExecute || snapshot.StopReason != "" {
		t.Fatalf("supervisor did not reconcile direct post-approval checkpoint: found=%t snapshot=%+v err=%v", found, snapshot, err)
	}
}

func TestStandardCodeSupervisorSupportsMoreThanFourToolRoundsInSingleTurn(t *testing.T) {
	f, _ := newFileEditDrydockFixture(t)
	ctx := t.Context()
	p := &continuationCodeProvider{}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	router.SetRoute("code", ref)
	agentRunner := NewAgentRunner(f.state, router, policy.NewDefaultChecker()).WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(standardCodeThreadTestRuntime().ExecutionPermissionCapabilities)
	wait := func(msg string) *llm.ChatResponse {
		raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: msg, Reason: "operator review"})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw)}
	}
	tool := func(id, name, payload string) *llm.ChatResponse {
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(payload)}}}
	}
	if _, err := NewRunService(f.state).Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	// Execute 6 tool rounds in a single Plan turn (> legacy 4-round cap).
	p.responses = []*llm.ChatResponse{
		tool("r1", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":5}`),
		tool("r2", "workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`),
		tool("r3", "workspace_glob", `{"version":"agent-code-tools.v1","pattern":"*.txt","limit":5}`),
		tool("r4", "workspace_grep", `{"version":"agent-code-tools.v1","query":"base","pattern":"*.txt","limit":5}`),
		tool("r5", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":10}`),
		tool("r6", "workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":2}`),
		wait("Completed 6 read rounds cleanly"),
	}
	res, err := agentRunner.StepWithInput(ctx, f.run.ID, "Inspect tracked.txt across 6 rounds")
	if err != nil {
		t.Fatalf("6-round Standard Code turn failed: %v", err)
	}
	if res.ToolRounds != 6 {
		t.Fatalf("expected 6 tool rounds, got %d", res.ToolRounds)
	}
	snapshot, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snapshot.TurnToolRounds != 6 || snapshot.TotalToolRounds != 6 || snapshot.StopReason != "" {
		t.Fatalf("unexpected 6-round Standard Code snapshot: found=%t snapshot=%+v err=%v", found, snapshot, err)
	}
}

func TestStandardCodeSupervisorMultiProposalBatchDirectApplyReconcilesChronologically(t *testing.T) {
	f, owned := newFileEditDrydockFixture(t)
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(owned.Path, "extra.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := NewWorkspaceCheckpointService(f.state, standardCodeThreadTestRuntime().ExecutionPermissionCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithCheckpointService(checkpoints)
	p := &continuationCodeProvider{}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	router.SetRoute("code", ref)
	checker := standardCodeAlwaysReviewPolicy{base: policy.NewDefaultChecker()}
	agentRunner := NewAgentRunner(f.state, router, checker).WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(standardCodeThreadTestRuntime().ExecutionPermissionCapabilities)
	wait := func(msg string) *llm.ChatResponse {
		raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: msg, Reason: "operator review"})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw)}
	}
	tool := func(id, name string, payload any) *llm.ChatResponse {
		var raw json.RawMessage
		if text, ok := payload.(string); ok {
			raw = json.RawMessage(text)
		} else {
			raw = mustAgentCodeDrydockJSON(t, payload)
		}
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: raw}}}
	}
	if _, err = NewRunService(f.state).Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":"plan_delivery.v1","directions":[
 {"title":"Batch update","summary":"Update both files","tradeoffs":["Two files"],"modules":[{"title":"Update files","objective":"Update tracked and extra","acceptance_criteria":["Both updated"],"dependencies":[]}]},
 {"title":"Single file","summary":"Only tracked","tradeoffs":["Partial"],"modules":[{"title":"Tracked","objective":"Tracked","acceptance_criteria":["One"],"dependencies":[]}]},
 {"title":"Defer","summary":"Defer","tradeoffs":["Later"],"modules":[{"title":"Defer","objective":"Defer","acceptance_criteria":["Deferred"],"dependencies":[]}]}]}`
	p.responses = []*llm.ChatResponse{
		tool("p1", "workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`),
		tool("p2", "workspace_read", `{"version":"agent-code-tools.v1","path":"extra.txt","start_line":1,"end_line":1}`),
		tool("p3", "plan_delivery_propose", plan),
		wait("Choose direction"),
	}
	if _, err = agentRunner.StepWithInput(ctx, f.run.ID, "Plan two-file update"); err != nil {
		t.Fatal(err)
	}
	proposals, err := f.state.ListPlanDeliveryProposals(ctx, f.run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("plan=%+v err=%v", proposals, err)
	}
	control := NewPlanDeliveryControlService(f.state)
	if _, err = control.SelectDirection(ctx, ControlPlanDirectionRequest{Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID, ProposalID: proposals[0].ID,
		Direction: 1, OperationKey: "sc-batch-select-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err = control.EnterDelivery(ctx, ControlPlanDeliveryTransitionRequest{Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID,
		OperationKey: "sc-batch-deliver-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.state.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunService(f.state).Resume(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	caps := standardCodeThreadTestRuntime().ExecutionPermissionCapabilities
	lifecycle := NewRunLifecycleControlService(f.state)
	execution := NewRunExecutionHandoffService(f.state, router, checker).
		WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(caps)
	turns := NewThreadTurnServiceWithExecutionCapabilities(f.state, lifecycle, execution, caps)
	p.responses = []*llm.ChatResponse{
		{
			Provider: p.Name(),
			Model:    "model",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "batch-1",
					Name: "workspace_change",
					Arguments: mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
						Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch",
						Path: "tracked.txt", ExpectedSHA256: fileedit.HashText("base\n"),
						Replacements: []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "batch one", ExpectedOccurrences: 1}},
					}),
				},
				{
					ID:   "batch-2",
					Name: "workspace_change",
					Arguments: mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
						Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch",
						Path: "extra.txt", ExpectedSHA256: fileedit.HashText("alpha\n"),
						Replacements: []toolgateway.WorkspaceReplacement{{OldText: "alpha", NewText: "batch two", ExpectedOccurrences: 1}},
					}),
				},
			},
		},
		wait("Await batch approval"),
		wait("Both batch edits applied without workspace drift"),
	}
	if _, err := turns.Execute(ctx, ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "Update both files", OperationKey: "sc-batch-exec-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	edits, err := f.state.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 2 {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	var editTracked, editExtra fileedit.Edit
	for _, item := range edits {
		if item.Path == "tracked.txt" {
			editTracked = item
		} else if item.Path == "extra.txt" {
			editExtra = item
		}
	}
	revSvc := NewFileEditReviewService(f.state).WithRunWorktree(f.service)
	if _, err = revSvc.Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: editTracked.ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	firstResume := turns.ResumeApproval(ctx, ApprovalContinuationRequest{RunID: f.run.ID, Kind: "file_edit", ProposalID: editTracked.ID})
	if firstResume.State != "not_started" {
		t.Fatalf("expected not_started after first of two reviews, got %+v", firstResume)
	}
	if _, err = revSvc.Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: editExtra.ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	secondResume := turns.ResumeApproval(ctx, ApprovalContinuationRequest{RunID: f.run.ID, Kind: "file_edit", ProposalID: editExtra.ID})
	if secondResume.State != "completed" || secondResume.ToolCalled {
		t.Fatalf("expected completed batch continuation, got %+v", secondResume)
	}
	if got1, err1 := os.ReadFile(filepath.Join(owned.Path, "tracked.txt")); err1 != nil || string(got1) != "batch one\n" {
		t.Fatalf("tracked.txt not direct-applied in batch: got=%q err=%v", got1, err1)
	}
	if got2, err2 := os.ReadFile(filepath.Join(owned.Path, "extra.txt")); err2 != nil || string(got2) != "batch two\n" {
		t.Fatalf("extra.txt not direct-applied in batch: got=%q err=%v", got2, err2)
	}
	snapshot, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snapshot.MutationEpoch != 2 || snapshot.State != domain.StandardCodeSupervisorExecute || snapshot.StopReason != "" {
		t.Fatalf("supervisor failed chronological multi-edit reconciliation: found=%t snapshot=%+v err=%v", found, snapshot, err)
	}
}

