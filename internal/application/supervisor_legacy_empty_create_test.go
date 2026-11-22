package application_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

const legacyEmptyCreateJSON = `{"version":"agent-code-tools.v1","action":"create","path":"empty.txt","expected_sha256":"missing"}`

func TestAgentRunnerRecoversAcceptedLegacyEmptyCreateAcrossRestart(t *testing.T) {
	f := newLegacyEmptyCreateFixture(t, legacyEmptyCreateJSON, false)
	f.reopen(t)
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionContinue, "empty proposal recovered", "", "")),
	}}
	failing := &failOnceToolResultStore{SQLiteStore: f.state, fail: true}
	first, err := newLegacyEmptyCreateSupervisor(failing, provider).
		WithExecutionPermissionCapabilities(f.capabilities).Step(t.Context(), f.run.ID)
	if apperror.CodeOf(err) != apperror.CodeInternal || first.Checkpoint.Phase != domain.SupervisorTurnStarted {
		t.Fatalf("injected result failure lost pending legacy call: %+v err=%v", first, err)
	}
	f.assertOneEmptyProposal(t)
	f.assertPendingIdentity(t)
	f.reopen(t)
	resumed, err := newLegacyEmptyCreateSupervisor(f.state, provider).
		WithExecutionPermissionCapabilities(f.capabilities).Step(t.Context(), f.run.ID)
	if err != nil || !resumed.Recovered || resumed.ToolRounds != 1 || resumed.ToolCalls != 1 || resumed.ModelAttempts != 2 {
		t.Fatalf("accepted legacy create was not recovered: %+v err=%v", resumed, err)
	}
	f.assertOneEmptyProposal(t)
	rounds, err := f.state.ListRunSupervisorToolRoundsPage(t.Context(), f.run.ID, 0, 2)
	if err != nil || len(rounds) != 1 || !rounds[0].Complete() {
		t.Fatalf("legacy result not sealed: %+v err=%v", rounds, err)
	}
	call := rounds[0].Calls[0]
	if call.Status != domain.SupervisorToolCompleted || call.PayloadJSON != f.payload || call.CallID != f.callID ||
		call.StreamResponseID != "legacy-response" || call.StreamItemID != "legacy-item" || call.StreamCallID != "legacy-wire-call" {
		t.Fatalf("execution compatibility changed durable payload or native pairing: %+v", call)
	}
	if len(provider.Requests()) != 1 || !hasToolResult(provider.Requests()[0], f.expectedEditID()) {
		t.Fatalf("legacy recovery failed exact proposal replay/context: result=%s model_requests=%d", call.ResultJSON, len(provider.Requests()))
	}
}

func TestAgentRunnerLegacyEmptyCreateStillRequiresCurrentAuthority(t *testing.T) {
	for _, fullAccess := range []bool{false, true} {
		name := "stopped Run"
		if fullAccess {
			name = "cold runtime grant"
		}
		t.Run(name, func(t *testing.T) {
			f := newLegacyEmptyCreateFixture(t, legacyEmptyCreateJSON, fullAccess)
			capabilities := f.capabilities
			if fullAccess {
				capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
			} else {
				service := application.NewRunService(f.state)
				if _, err := service.Pause(t.Context(), f.run.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.reopen(t)
			provider := &scriptedToolProvider{}
			_, err := newLegacyEmptyCreateSupervisor(f.state, provider).
				WithExecutionPermissionCapabilities(capabilities).Step(t.Context(), f.run.ID)
			if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || len(provider.Requests()) != 0 {
				t.Fatalf("legacy compatibility bypassed current authority: err=%v requests=%+v", err, provider.Requests())
			}
			f.assertNoProposal(t)
			f.assertPendingIdentity(t)
		})
	}
}

func TestAgentRunnerRejectsNewCreateWithoutBodyBeforeEnqueue(t *testing.T) {
	for _, payload := range []string{legacyEmptyCreateJSON, strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"content":null}`} {
		name := "missing"
		if strings.Contains(payload, "null") {
			name = "null"
		}
		t.Run(name, func(t *testing.T) {
			f := newLegacyEmptyCreateFixture(t, "", false)
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				toolResponse("new-empty-create", string(toolgateway.WorkspaceChangeTool), payload),
				toolResponse("invalid-repair", string(toolgateway.WorkspaceChangeTool), payload),
			}}
			_, err := newLegacyEmptyCreateSupervisor(f.state, provider).
				WithExecutionPermissionCapabilities(f.capabilities).Step(t.Context(), f.run.ID)
			if err == nil || len(provider.Requests()) != 2 {
				t.Fatalf("new missing/null body was accepted: err=%v requests=%+v", err, provider.Requests())
			}
			if rounds, err := f.state.ListRunSupervisorToolRoundsPage(t.Context(), f.run.ID, 0, 2); err != nil || len(rounds) != 0 {
				t.Fatalf("new missing/null body was durably enqueued: rounds=%+v err=%v", rounds, err)
			}
			f.assertNoProposal(t)
		})
	}
}

func TestAgentRunnerLegacyEmptyCreateDoesNotCoverOtherPersistedShapes(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"null body", strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"content":null}`},
		{"extra field", strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"replacements":[]}`},
		{"duplicate field", strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"action":"create"}`},
		{"different order", `{"action":"create","version":"agent-code-tools.v1","path":"empty.txt","expected_sha256":"missing"}`},
		{"whitespace", legacyEmptyCreateJSON + " "},
		{"replace", strings.ReplaceAll(strings.ReplaceAll(legacyEmptyCreateJSON, `"create"`, `"replace"`), `"missing"`, `"`+fileedit.HashText("")+`"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLegacyEmptyCreateFixture(t, tc.payload, false)
			f.reopen(t)
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				textResponse(rootActionResponse(domain.RootActionContinue, "invalid legacy arguments rejected", "", "")),
			}}
			_, err := newLegacyEmptyCreateSupervisor(f.state, provider).
				WithExecutionPermissionCapabilities(f.capabilities).Step(t.Context(), f.run.ID)
			if err == nil || len(provider.Requests()) != 0 {
				t.Fatalf("compatibility accepted an unrelated shape: err=%v model_requests=%d", err, len(provider.Requests()))
			}
			f.assertNoProposal(t)
			f.assertPendingIdentity(t)
		})
	}
}

func TestAgentCodeGatewayRejectsNewEmptyCreateWithoutExplicitBody(t *testing.T) {
	f := newAgentCodeReplaceFixture(t, true)
	gateway := toolgateway.New(f.state, nil).
		WithAgentCodeWorkspaceResolver(application.NewAgentCodeWorkspaceResolver(f.state, nil)).
		WithAgentCodeExecutor(f.executor)
	scope := f.scope
	call := toolgateway.ToolCall{Name: toolgateway.WorkspaceChangeTool, RunID: scope.RunID, MissionID: scope.MissionID,
		AgentID: scope.RootAgentID, SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID, RootFingerprint: scope.RootFingerprint,
		Surface: scope.Surface, Phase: scope.Phase, Role: scope.Role, Profile: scope.Profile, PermissionMode: scope.PermissionMode,
		PermissionSnapshotID: scope.PermissionSnapshotID, PermissionGeneration: scope.PermissionGeneration, PermissionRuntimeEpoch: scope.PermissionRuntimeEpoch, RunAuthorizationFence: scope.RunAuthorizationFence,
		ModeRevision: scope.ModeRevision, PermissionRevision: scope.PermissionRevision, CapabilityGeneration: scope.CapabilityGeneration,
		LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequestedBy: scope.RequestedBy,
		OperationKey: "new-gateway-empty-create-0001"}
	for _, payload := range []string{legacyEmptyCreateJSON, strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"content":null}`} {
		call.Payload = json.RawMessage(payload)
		if _, err := gateway.Invoke(t.Context(), call); err == nil || !strings.Contains(err.Error(), "content") {
			t.Fatalf("direct gateway did not require explicit create body: err=%v", err)
		}
	}
	if edits, err := f.state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: scope.SessionID}); err != nil || len(edits) != 0 {
		t.Fatalf("rejected direct call persisted a proposal: edits=%+v err=%v", edits, err)
	}
	call.Payload = json.RawMessage(strings.TrimSuffix(legacyEmptyCreateJSON, "}") + `,"content":""}`)
	if _, err := gateway.Invoke(t.Context(), call); err != nil {
		t.Fatalf("direct gateway rejected explicit empty body control: err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "empty.txt")); err != nil || len(data) != 0 {
		t.Fatalf("explicit empty create did not write 0-byte file: data=%q err=%v", data, err)
	}
}

// This fixture simulates the historic persisted format with first INSERTs only.
// Normal Store APIs create the Run, active turn, model attempt and authority.
// No existing ledger row is changed, and no production normalization is relaxed.
type legacyEmptyCreateFixture struct {
	state        *store.SQLiteStore
	path, root   string
	run          domain.Run
	payload      string
	callID       string
	operationKey string
	capabilities domain.ExecutionPermissionRuntimeCapabilities
}

func newLegacyEmptyCreateSupervisor(st application.AgentRunnerStore, provider *scriptedToolProvider) *application.AgentRunner {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	// These cases recover an explicitly reviewed proposal, including its
	// original status. Current Ask also permits verified routine operations.
	return application.NewAgentRunner(st, router, &fileOperationPolicy{review: true})
}

func newLegacyEmptyCreateFixture(t *testing.T, payload string, fullAccess bool) *legacyEmptyCreateFixture {
	t.Helper()
	f := &legacyEmptyCreateFixture{path: filepath.Join(t.TempDir(), "legacy.db"), root: t.TempDir(), payload: payload,
		capabilities: domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
			RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}}
	var err error
	f.state, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.state.Close() })
	if err := f.state.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "legacy-workspace", Name: "legacy-workspace", RootPath: f.root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	service := application.NewRunService(f.state)
	_, f.run, err = service.Create(t.Context(), application.CreateRunRequest{Goal: "recover an accepted empty create", Profile: "code",
		Surface: "code", Phase: "deliver", WorkspaceID: "legacy-workspace", ModelRoute: "tool-loop/model", Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 5}})
	if err != nil {
		t.Fatal(err)
	}
	permission, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullAccess {
		selected, err := application.NewRunExecutionPermissionService(f.state, f.capabilities).
			Change(t.Context(), application.ChangeRunExecutionPermissionRequest{RunID: f.run.ID, Mode: string(domain.RunExecutionPermissionFull),
				OperationKey: "legacy-permission-select-0001", RequestedBy: "test_operator", Reason: "recover legacy empty proposal", ConfirmFull: true})
		if err != nil {
			t.Fatal(err)
		}
		permission = selected.Permission
	}
	if _, err := service.Start(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	if payload == "" {
		return f
	}
	lease, err := f.state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.run.ID, OwnerID: "legacy-seed", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := f.state.BeginSupervisorTurn(t.Context(), lease.Lease, "recover an accepted empty create")
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 3, Provider: "tool-loop", Model: "model"}
	if _, err := f.state.RecordSupervisorModelStarted(t.Context(), turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	if _, err := f.state.RecordSupervisorModelCompleted(t.Context(), turn.Checkpoint, attempt, llm.ChatResponse{Provider: "tool-loop", Model: "model",
		Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}); err != nil {
		t.Fatal(err)
	}
	rootHash, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	scope := toolgateway.AgentCodeCapabilityContext{RunID: f.run.ID, MissionID: turn.Mission.ID, RootAgentID: turn.Agent.ID,
		WorkspaceID: "legacy-workspace", RootFingerprint: rootHash, Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile, PermissionMode: permission.Mode, ModeRevision: turn.Mode.Revision, PermissionRevision: permission.Revision}
	scope.PermissionSnapshotID = permission.ID
	scope.PermissionRuntimeEpoch = f.capabilities.RuntimeAuthority.RuntimeEpoch()
	scope.RunAuthorizationFence, err = f.capabilities.RuntimeAuthority.IssueRunAuthorizationFence(f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullAccess {
		generation, live := f.capabilities.FullAccessGeneration(permission)
		if !live {
			t.Fatal("legacy full authority was not active at acceptance")
		}
		scope.PermissionGeneration = generation
	}
	authority, err := toolgateway.NewAgentCodeCallAuthority(scope, f.run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := toolgateway.EncodeAgentCodeCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	f.operationKey = runmutation.SupervisorToolOperationKey(f.run.ID, turn.Checkpoint.NextTurn, string(toolgateway.WorkspaceChangeTool), payload)
	f.callID, err = runmutation.SupervisorToolCallID(f.operationKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite3", f.path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyDB.Close()
	tx, err := legacyDB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_supervisor_tool_rounds (run_id,turn,attempt_id,round,model_attempt,created_at,completed_at) VALUES(?,?,?,1,1,?,NULL)`, f.run.ID, turn.Checkpoint.NextTurn, turn.Checkpoint.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_supervisor_tool_calls
		(run_id,turn,attempt_id,round,position,model_attempt,call_id,stream_response_id,stream_item_id,stream_call_id,tool_name,payload_json,authority_json,status,result_json,error_code,created_at,completed_at)
		VALUES(?,?,?,1,1,1,?,'legacy-response','legacy-item','legacy-wire-call',?,?,?,'pending','','',?,NULL)`,
		f.run.ID, turn.Checkpoint.NextTurn, turn.Checkpoint.AttemptID, f.callID, string(toolgateway.WorkspaceChangeTool), payload, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO run_supervisor_tool_call_agents (run_id,turn,attempt_id,call_id,agent_id,agent_attempt_id,attribution_source,created_at) VALUES(?,?,?,?,?,?,'recorded',?)`,
		f.run.ID, turn.Checkpoint.NextTurn, turn.Checkpoint.AttemptID, f.callID, turn.Agent.ID, turn.Checkpoint.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.state.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	f.assertPendingIdentity(t)
	return f
}

func (f *legacyEmptyCreateFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.state.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.state, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *legacyEmptyCreateFixture) expectedEditID() string {
	sum := sha256.Sum256([]byte("agent-code-edit.v1\x00" + f.operationKey))
	return "edit-" + hex.EncodeToString(sum[:16])
}

func (f *legacyEmptyCreateFixture) assertOneEmptyProposal(t *testing.T) {
	t.Helper()
	edits, err := f.state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 1 || edits[0].ID != f.expectedEditID() || edits[0].Operation != fileedit.OperationCreate ||
		edits[0].ProposedText != "" || edits[0].OriginalHash != "missing" || edits[0].ProposedHash != fileedit.HashText("") || edits[0].Status != fileedit.StatusProposed {
		t.Fatalf("legacy empty creation did not preserve exact accepted key/proposal: edits=%+v err=%v", edits, err)
	}
	f.assertFileAbsent(t)
}

func (f *legacyEmptyCreateFixture) assertNoProposal(t *testing.T) {
	t.Helper()
	if edits, err := f.state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: f.run.SessionID}); err != nil || len(edits) != 0 {
		t.Fatalf("invalid legacy/new create persisted an edit: edits=%+v err=%v", edits, err)
	}
	f.assertFileAbsent(t)
}

func (f *legacyEmptyCreateFixture) assertFileAbsent(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.root, "empty.txt")); !os.IsNotExist(err) {
		t.Fatalf("proposal created a file before explicit apply: err=%v", err)
	}
}

func (f *legacyEmptyCreateFixture) assertPendingIdentity(t *testing.T) {
	t.Helper()
	rounds, err := f.state.ListRunSupervisorToolRoundsPage(t.Context(), f.run.ID, 0, 2)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 || rounds[0].Calls[0].Status != domain.SupervisorToolPending ||
		rounds[0].Calls[0].PayloadJSON != f.payload || rounds[0].Calls[0].CallID != f.callID {
		t.Fatalf("seeded pending payload/identity changed: rounds=%+v err=%v", rounds, err)
	}
}
