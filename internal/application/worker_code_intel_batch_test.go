package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

func newWorkerCodeIntelBatchFixture(t *testing.T) (workerCodeIntelFixture, batchDeliveryApplicationFixture, BatchDeliveryWorkRequest) {
	t.Helper()
	base := newBatchDeliveryApplicationFixtureWithWorker(t, false, domain.RunExecutionPermissionFull, &domain.WorkerProfile{})
	_, work, _ := batchModelWorkerFixture(t, base, &batchEditTestProvider{}, base.store)
	lease, err := base.store.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{
		RunID: base.run.ID, OwnerID: "batch-code-intel-test", TTL: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := base.store.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{
		AttemptID: idgen.New("batch-lsp-attempt"), RunID: base.run.ID, AgentID: work.Workspace.AgentID,
		ParentAgentID: base.root.ID, Lease: lease.Lease, StartedAt: time.Now().UTC(),
	}, "batch-lsp-attempt-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	ref := specialistAttemptRef(attempt)
	brief, err := base.store.PrepareSpecialistContext(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	child, err := base.store.GetAgentNode(t.Context(), work.Workspace.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(work.Workspace.WorktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := domain.WorkerProfileJSON(child.Worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.store.BindSpecialistTools(t.Context(), domain.SpecialistToolBinding{
		Ref: ref, SessionID: child.SessionID, WorkspaceID: base.proposal.WorkspaceID, RootFingerprint: fingerprint,
		BriefFingerprint: brief.TaskBrief.Fingerprint, Mode: "worker", WorkerProfileJSON: profile,
		BatchPlanID: work.Plan.ID, BatchOrdinal: work.Workspace.Ordinal, BatchGeneration: work.Workspace.Generation,
	}); err != nil {
		t.Fatal(err)
	}
	actor := &workerCommandTestActor{store: base.store, run: base.run, root: base.root, child: child, lease: lease.Lease, ref: ref}
	runtime := NewAgentRunner(base.store, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(base.caps)
	executor := NewCodeIntelToolExecutor(base.store, policy.NewDefaultChecker(), &codeintel.Manager{}).WithExecutionPermissionCapabilities(base.caps)
	return workerCodeIntelFixture{actor: actor, runtime: runtime, executor: executor, caps: base.caps}, base, work
}

func TestWorkerCodeIntelBatchPinsRootAndCurrentOwnerGeneration(t *testing.T) {
	f, base, work := newWorkerCodeIntelBatchFixture(t)
	parentRaw := f.parentAuthority(t)
	parent, err := toolgateway.DecodeAgentCodeCallAuthority(parentRaw)
	if err != nil {
		t.Fatal(err)
	}
	payload := workerCodeIntelTestPayload(t, toolgateway.CodeHoverTool)
	stored, call := f.prepare(t, toolgateway.CodeHoverTool, payload)
	bound, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(stored.AuthorityJSON))
	if err != nil || bound.RootFingerprint == parent.RootFingerprint || bound.CapabilityGeneration == parent.CapabilityGeneration {
		t.Fatalf("Batch authority retained the parent physical root: %+v %v", bound, err)
	}
	forged := call
	forged.WorkspaceRoot = base.repository
	if _, _, err := f.executor.ResolveWorkerCodeIntelWorkspace(t.Context(), forged); err == nil {
		t.Fatal("worker accepted an explicit parent root")
	}
	sibling, found, err := base.store.GetBatchDeliveryWorkspace(t.Context(), work.Plan.ID, 2)
	if err != nil || !found || sibling.AgentID == f.actor.child.ID {
		t.Fatalf("missing sibling Batch owner: %+v %v", sibling, err)
	}
	forged = call
	forged.AgentID = sibling.AgentID
	if _, _, err := f.executor.ResolveWorkerCodeIntelWorkspace(t.Context(), forged); err == nil {
		t.Fatal("sibling worker reused another owner's semantic call")
	}
	read := json.RawMessage(`{"version":"agent-code-tools.v1","path":"internal/one/base.txt","start_line":1,"end_line":4}`)
	if _, err := f.executor.authority.BindSpecialistAgentCodeAuthority(t.Context(), f.actor.ref,
		toolgateway.WorkspaceReadTool, parentRaw, read); err == nil {
		t.Fatal("Batch worker escaped its native file authority path")
	}
	capture := &workerCodeIntelCapture{executor: f.executor}
	f.runtime.tools.WithCodeIntelExecutor(capture)
	if _, err := f.runtime.tools.Invoke(t.Context(), call); err != nil || capture.calls != 1 || capture.scope.WorkspaceRoot != work.Workspace.WorktreeRoot {
		t.Fatalf("Batch semantic dispatch used the wrong root: %+v %v", capture.scope, err)
	}
	if _, _, err := base.service.RenewOwner(t.Context(), RenewBatchDeliveryOwnerRequest{
		PlanID: work.Plan.ID, Ordinal: work.Workspace.Ordinal, ExpectedGeneration: work.Workspace.Generation,
		RequestedBy: "operator", Confirm: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.executor.ResolveWorkerCodeIntelWorkspace(t.Context(), call); err == nil {
		t.Fatal("retired Batch generation still resolved a semantic workspace")
	}
	if _, err := capture.ExecuteCodeIntel(t.Context(), capture.scope, call.Name, payload); err == nil || capture.calls != 1 {
		t.Fatalf("retired Batch generation returned semantic evidence: calls=%d err=%v", capture.calls, err)
	}
}

func TestWorkerCodeIntelBatchRealGoplsKeepsOwnedReadScope(t *testing.T) {
	executable := strings.TrimSpace(os.Getenv("CYBERAGENT_GOPLS_SMOKE_BINARY"))
	if executable == "" {
		t.Skip("CYBERAGENT_GOPLS_SMOKE_BINARY is not set")
	}
	f, base, work := newWorkerCodeIntelBatchFixture(t)
	for relative, content := range map[string]string{
		"internal/one/value.go": "package one\nimport \"example.invalid/batchfixture/internal/two\"\nconst WorkerOnly = two.ForeignOnly\n",
		"internal/two/doc.go":   "package two\nconst ForeignOnly = \"foreign\"\n",
	} {
		if err := os.WriteFile(filepath.Join(work.Workspace.WorktreeRoot, filepath.FromSlash(relative)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := newWorkerGoplsTestManager(t, executable, base.proposal.WorkspaceID)
	f.runtime.WithCodeIntel(manager)
	f.executor = NewCodeIntelToolExecutor(base.store, policy.NewDefaultChecker(), manager).WithExecutionPermissionCapabilities(base.caps)
	f.runtime.tools.WithCodeIntelExecutor(f.executor)
	mission, err := base.store.GetMission(t.Context(), base.run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := base.store.GetRunMode(t.Context(), base.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := domain.SupervisorTurn{Run: base.run, Mission: mission, Agent: base.root, Mode: mode}
	parent, err := f.runtime.supervisorCodeIntelCapabilities(t.Context(), turn)
	if err != nil || len(parent.Servers) != 1 {
		t.Fatalf("parent semantic catalogue: %+v %v", parent, err)
	}
	child, err := f.runtime.workerCodeIntelCapabilities(t.Context(), f.actor.ref, turn)
	if err != nil || len(child.Servers) != 1 || len(child.VisibleDefinitions()) != 10 || child.Servers[0].Generation == parent.Servers[0].Generation {
		t.Fatalf("Batch capabilities reused the source generation: %+v %v", child, err)
	}
	server := child.Servers[0]
	for _, test := range []struct {
		name            toolgateway.ToolName
		path, query     string
		line, character int
		stale           bool
		wantCount       int
		wantError       apperror.Code
	}{
		{name: toolgateway.CodeDocumentSymbolsTool, path: "internal/one/value.go", wantCount: 1},
		{name: toolgateway.CodeWorkspaceSymbolsTool, query: "Only", wantCount: 1},
		{name: toolgateway.CodeDefinitionTool, path: "internal/one/value.go", line: 2, character: 24, wantCount: 0},
		{name: toolgateway.CodeDocumentSymbolsTool, path: "internal/two/doc.go", wantError: apperror.CodePolicyDenied},
		{name: toolgateway.CodeDocumentSymbolsTool, path: "internal/one/value.go", stale: true, wantError: apperror.CodeConflict},
	} {
		input := toolgateway.CodeIntelPayload{Version: toolgateway.CodeIntelProtocolVersion, ServerID: server.ServerID,
			ServerGeneration: server.Generation, CapabilityFingerprint: server.CapabilityFingerprint,
			Path: test.path, Query: test.query, Line: test.line, Character: test.character, Limit: 20}
		if test.stale {
			input.ServerGeneration = parent.Servers[0].Generation
		}
		raw, _ := json.Marshal(input)
		_, payload, err := toolgateway.NormalizeCodeIntelPayload(test.name, raw)
		if err != nil {
			t.Fatal(err)
		}
		stored, call := f.prepare(t, test.name, payload)
		outcome, err := f.runtime.tools.Invoke(t.Context(), call)
		if test.wantError != "" {
			if apperror.CodeOf(err) != test.wantError || outcome.Result != nil {
				t.Fatalf("%s path=%s stale=%t: %+v %v", test.name, test.path, test.stale, outcome, err)
			}
			stored.Status, stored.ResultJSON, stored.ErrorCode = domain.SupervisorToolFailed, `{}`, string(test.wantError)
			t.Logf("worker=%s tool=%s path=%s stale=%t rejected=%s", f.actor.child.ID, test.name, test.path, test.stale, test.wantError)
		} else {
			if err != nil || outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted {
				t.Fatalf("Batch semantic %s: %+v %v", test.name, outcome, err)
			}
			var result codeintel.Result
			if err := json.Unmarshal([]byte(outcome.Result.Stdout), &result); err != nil || len(result.Items) != test.wantCount {
				t.Fatalf("Batch semantic result: %s %v", outcome.Result.Stdout, err)
			}
			for _, item := range result.Items {
				if item.Path != "internal/one/value.go" || strings.Contains(item.Name, "Foreign") {
					t.Fatalf("semantic evidence crossed Batch ownership: %+v", item)
				}
			}
			if result.Provenance.RootFingerprint != call.RootFingerprint || result.Provenance.ServerGeneration != server.Generation {
				t.Fatalf("semantic provenance used parent workspace: %+v", result.Provenance)
			}
			stored.Status, stored.ResultJSON = domain.SupervisorToolCompleted, outcome.Result.Stdout
			t.Logf("worker=%s tool=%s path=%s query=%s items=%d server=%s", f.actor.child.ID, test.name, test.path, test.query, len(result.Items), result.Provenance.ServerID)
		}
		if err := base.store.CompleteSpecialistToolCall(t.Context(), stored); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), base.proposal.WorkspaceID, base.repository,
		parent.Servers[0].ServerID, parent.Servers[0].Generation); err != nil {
		t.Fatalf("Batch queries retired the parent semantic runtime: %v", err)
	}
	if err := f.runtime.closeWorkerResources(f.actor.ref); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), base.proposal.WorkspaceID, work.Workspace.WorktreeRoot,
		server.ServerID, server.Generation); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("Batch cleanup retained its semantic runtime: %v", err)
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), base.proposal.WorkspaceID, base.repository,
		parent.Servers[0].ServerID, parent.Servers[0].Generation); err != nil {
		t.Fatalf("Batch cleanup retired the parent semantic runtime: %v", err)
	}
	t.Log("Batch cleanup retired the owned LSP root and preserved the parent generation")
}
