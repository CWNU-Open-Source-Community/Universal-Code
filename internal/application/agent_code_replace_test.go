package application_test

import (
	"context"
	"crypto/sha256"
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
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

func TestAgentCodeReplaceProposesThenAppliesExactWholeFile(t *testing.T) {
	for _, content := range []string{"after\n", ""} {
		t.Run(map[bool]string{true: "empty", false: "nonempty"}[content == ""], func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			proposal := f.propose(t, content, "replace-proposal-0001")
			if !proposal.ApplyAuthorized || proposal.Operation != fileedit.OperationReplace || proposal.ApplyArguments.ExpectedAction != "replace" ||
				proposal.ApplyArguments.EditID != proposal.EditID || proposal.ApplyArguments.ExpectedOriginalSHA256 != fileedit.HashText("before\n") ||
				proposal.ApplyArguments.ExpectedProposedSHA256 != fileedit.HashText(content) {
				t.Fatalf("proposal lacks the exact apply contract: %+v", proposal)
			}
			f.assertFile(t, "before\n")
			payload := mustAgentCodePayload(t, proposal.ApplyArguments)
			if _, err := toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceApplyTool, payload); err != nil {
				t.Fatal(err)
			}
			applied, err := f.execute(t, toolgateway.WorkspaceApplyTool, payload, "replace-apply-0001")
			if err != nil || !strings.Contains(applied.JSON, `"file_written":true`) {
				t.Fatalf("apply=%s err=%v", applied.JSON, err)
			}
			f.assertFile(t, content)
		})
	}
}

func TestAgentCodeReplaceRejectsInvalidSourceAndBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing content", func(p map[string]any) { delete(p, "content") }},
		{"null content", func(p map[string]any) { p["content"] = nil }},
		{"missing hash", func(p map[string]any) { delete(p, "expected_sha256") }},
		{"missing sentinel", func(p map[string]any) { p["expected_sha256"] = "missing" }},
		{"absent file", func(p map[string]any) { p["path"] = "absent.txt" }},
		{"old hash", func(p map[string]any) { p["expected_sha256"] = fileedit.HashText("older\n") }},
		{"result receipt hash", func(p map[string]any) {
			receipt := sha256.Sum256([]byte(`{"content":"before\n","metadata":{"receipt":"read-only"}}`))
			p["expected_sha256"] = hex.EncodeToString(receipt[:])
		}},
		{"content byte limit", func(p map[string]any) { p["content"] = strings.Repeat("界", toolgateway.MaxAgentCodeCreateBytes/3+1) }},
		{"mixed patch fields", func(p map[string]any) { p["replacements"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			payload := f.payload("after\n")
			tc.mutate(payload)
			if result, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, payload), "invalid-replace-0001"); err == nil {
				t.Fatalf("invalid replace accepted: %s", result.JSON)
			}
			f.assertFile(t, "before\n")
			if edits, err := f.state.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: f.scope.SessionID}); err != nil || len(edits) != 0 {
				t.Fatalf("rejected replace persisted an edit: edits=%+v err=%v", edits, err)
			}
		})
	}
}

func TestAgentCodeReplacePreservesCASAndIdempotency(t *testing.T) {
	for _, mode := range []string{"cas", "replay"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			proposal := f.propose(t, "after\n", "replace-proposal-0001")
			replayed, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, f.payload("after\n")), "replace-proposal-0001")
			if err != nil || !replayed.Replayed {
				t.Fatalf("proposal replay=%s err=%v", replayed.JSON, err)
			}
			if _, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, f.payload("different\n")), "replace-proposal-0001"); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("same proposal key accepted another body: %v", err)
			}
			if mode == "cas" {
				f.write(t, "user changed\n")
				if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "replace-apply-0001"); apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatalf("CAS drift was not rejected: %v", err)
				}
				f.assertFile(t, "user changed\n")
				return
			}
			if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "replace-apply-0001"); err != nil {
				t.Fatal(err)
			}
			f.write(t, "later user change\n")
			replayed, err = f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "replace-apply-0001")
			if err != nil || !replayed.Replayed || !strings.Contains(replayed.JSON, `"file_written":false`) {
				t.Fatalf("apply replay=%s err=%v", replayed.JSON, err)
			}
			f.assertFile(t, "later user change\n")
			replayed, err = f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, f.payload("after\n")), "replace-proposal-0001")
			if err != nil || !replayed.Replayed {
				t.Fatalf("applied proposal replay=%s err=%v", replayed.JSON, err)
			}
			f.assertFile(t, "later user change\n")
		})
	}
}

func TestAgentCodeReplaceCannotWriteWithoutCurrentApprovalAndAuthority(t *testing.T) {
	for _, mode := range []string{"review", "revoked", "restarted", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, mode != "review")
			proposal := f.propose(t, "after\n", "replace-proposal-0001")
			if mode == "review" && (proposal.ApplyAuthorized || !proposal.ReviewRequired) {
				t.Fatalf("review proposal granted authority: %+v", proposal)
			}
			switch mode {
			case "revoked":
				f.runtime.RevokeRun(f.scope.RunID)
			case "restarted":
				if err := f.state.Close(); err != nil {
					t.Fatal(err)
				}
				state, err := store.Open(f.databasePath)
				if err != nil {
					t.Fatal(err)
				}
				f.state = state
				t.Cleanup(func() { _ = state.Close() })
				f.capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
				f.executor = application.NewAgentCodeToolExecutor(state, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.capabilities)
			case "stopped":
				if _, err := application.NewRunService(f.state).Pause(t.Context(), f.scope.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if result, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "unauthorized-replace-apply-0001"); err == nil {
				t.Fatalf("unapproved apply=%s", result.JSON)
			}
			f.assertFile(t, "before\n")
			if mode == "review" {
				if _, err := application.NewFileEditReviewService(f.state).Review(t.Context(), application.ReviewFileEditRequest{
					Version: application.FileEditReviewProtocolVersion, RunID: f.scope.RunID, EditID: proposal.EditID, Action: application.FileEditApproveIntent,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "reviewed-replace-apply-0001"); err != nil {
					t.Fatal(err)
				}
				f.assertFile(t, "after\n")
			}
		})
	}
}

func TestAgentCodeReplaceManualReviewReplayReportsCurrentAuthorization(t *testing.T) {
	f := newAgentCodeReplaceFixtureForPermission(t, domain.RunExecutionPermissionAuto)
	proposal := f.propose(t, "after\n", "manual-review-replay-0001")
	if proposal.ApplyAuthorized || !proposal.ReviewRequired {
		t.Fatalf("pending manual proposal granted authority: %+v", proposal)
	}
	if _, err := application.NewFileEditReviewService(f.state).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: f.scope.RunID, EditID: proposal.EditID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, f.payload("after\n")), "manual-review-replay-0001")
	if err != nil {
		t.Fatal(err)
	}
	var replay replaceProposalResult
	if err := json.Unmarshal([]byte(result.JSON), &replay); err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || replay.EditID != proposal.EditID || replay.Status != fileedit.StatusApproved || !replay.ApplyAuthorized ||
		replay.ReviewRequired || replay.AuthorizationSource != "operator_review" {
		t.Fatalf("reviewed proposal replay misreported current authorization: %+v replayed=%t", replay, result.Replayed)
	}
	f.assertFile(t, "before\n")
	if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, replay.ApplyArguments), "manual-reviewed-apply-0001"); err != nil {
		t.Fatal(err)
	}
	f.assertFile(t, "after\n")
}

func TestAgentCodeReplaceManualAuthorizationProjectionFailsClosed(t *testing.T) {
	for _, mode := range []string{"pending", "denied", "reader error"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentCodeReplaceFixtureForPermission(t, domain.RunExecutionPermissionAuto)
			proposal := f.propose(t, "after\n", "manual-projection-denial-0001")
			if mode != "pending" {
				action := application.FileEditDeny
				if mode == "reader error" {
					action = application.FileEditApproveIntent
				}
				if _, err := application.NewFileEditReviewService(f.state).Review(t.Context(), application.ReviewFileEditRequest{
					Version: application.FileEditReviewProtocolVersion, RunID: f.scope.RunID, EditID: proposal.EditID, Action: action,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "reader error" {
				f.executor = application.NewAgentCodeToolExecutor(&manualProjectionErrorStore{SQLiteStore: f.state}, policy.NewDefaultChecker()).
					WithExecutionPermissionCapabilities(f.capabilities)
			}
			replay := f.propose(t, "after\n", "manual-projection-denial-0001")
			if replay.EditID != proposal.EditID || replay.ApplyAuthorized || !replay.ReviewRequired {
				t.Fatalf("unconfirmed manual authorization granted by projection: %+v", replay)
			}
			f.assertFile(t, "before\n")
		})
	}
	t.Run("old automatic", func(t *testing.T) {
		f := newAgentCodeReplaceFixture(t, true)
		proposal := f.propose(t, "after\n", "old-auto-projection-0001")
		f.capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
		permission, err := f.state.GetRunExecutionPermission(t.Context(), f.scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		// A new explicit activation allows a current tool call, but cannot rewrite
		// the persisted proposal's old runtime authorization into manual approval.
		if _, err := f.capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
			t.Fatal(err)
		}
		f.scope.PermissionGeneration, _ = f.capabilities.FullAccessGeneration(permission)
		f.scope.PermissionRuntimeEpoch = f.capabilities.RuntimeAuthority.RuntimeEpoch()
		f.scope.RunAuthorizationFence, err = f.capabilities.RuntimeAuthority.IssueRunAuthorizationFence(f.scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		f.scope.CapabilityGeneration = toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{
			RunID: f.scope.RunID, MissionID: f.scope.MissionID, RootAgentID: f.scope.RootAgentID, WorkspaceID: f.scope.WorkspaceID,
			RootFingerprint: f.scope.RootFingerprint, Surface: f.scope.Surface, Phase: f.scope.Phase, Role: f.scope.Role, Profile: f.scope.Profile,
			PermissionMode: f.scope.PermissionMode, PermissionSnapshotID: f.scope.PermissionSnapshotID, PermissionGeneration: f.scope.PermissionGeneration,
			PermissionRuntimeEpoch: f.scope.PermissionRuntimeEpoch, RunAuthorizationFence: f.scope.RunAuthorizationFence, ModeRevision: f.scope.ModeRevision, PermissionRevision: f.scope.PermissionRevision,
		}).Generation
		f.executor = application.NewAgentCodeToolExecutor(f.state, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.capabilities)
		replay := f.propose(t, "after\n", "old-auto-projection-0001")
		if replay.EditID != proposal.EditID || replay.Status != fileedit.StatusApproved || replay.ApplyAuthorized || replay.ReviewRequired ||
			replay.AuthorizationSource != "operation_policy_automatic" {
			t.Fatalf("expired automatic proposal was reinterpreted as manual approval: %+v", replay)
		}
		if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, replay.ApplyArguments), "old-auto-apply-0001"); err == nil {
			t.Fatal("old automatic proposal bypassed the current backend authorization gate")
		}
		f.assertFile(t, "before\n")
	})
}

type manualProjectionErrorStore struct{ *store.SQLiteStore }

func (*manualProjectionErrorStore) GetFileEditAutoAuthorization(context.Context, string) (fileedit.AutoAuthorization, bool, error) {
	return fileedit.AutoAuthorization{}, false, apperror.New(apperror.CodeInternal, "injected authorization lookup failure")
}

func TestAgentCodeReplaceKeepsCreateNonOverwritingAndPatchAlias(t *testing.T) {
	f := newAgentCodeReplaceFixture(t, true)
	create := map[string]any{"version": toolgateway.AgentCodeRegistryVersion, "action": "create", "path": "target.txt", "expected_sha256": "missing", "content": "overwrite\n"}
	if _, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, create), "cannot-create-existing-0001"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("create overwrote an existing file: %v", err)
	}
	f.assertFile(t, "before\n")
	patch := toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "target.txt", ExpectedSHA256: fileedit.HashText("before\n"),
		Replacements: []toolgateway.WorkspaceReplacement{{OldText: "before\n", NewText: "patched\n", ExpectedOccurrences: 1}}}
	result, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, patch), "patch-proposal-0001")
	if err != nil {
		t.Fatal(err)
	}
	var proposal replaceProposalResult
	if err := json.Unmarshal([]byte(result.JSON), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.ApplyArguments.ExpectedAction != "replace" {
		t.Fatalf("patch lacks replace apply contract: %+v", proposal)
	}
	proposal.ApplyArguments.ExpectedAction = "propose_patch"
	if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "patch-alias-apply-0001"); err != nil {
		t.Fatal(err)
	}
	f.assertFile(t, "patched\n")
	revert := toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_revert", Path: "target.txt",
		ExpectedSHA256: fileedit.HashText("patched\n"), SourceRunID: f.scope.RunID, SourceEditID: proposal.EditID}
	result, err = f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, revert), "revert-proposal-0001")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result.JSON), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.ApplyArguments.ExpectedAction != "replace" {
		t.Fatalf("reversal lacks replace apply contract: %+v", proposal)
	}
	if _, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "revert-replace-apply-0001"); err != nil {
		t.Fatal(err)
	}
	f.assertFile(t, "before\n")
}

type replaceProposalResult struct {
	EditID              string                            `json:"edit_id"`
	Operation           string                            `json:"operation"`
	ApplyArguments      toolgateway.WorkspaceApplyPayload `json:"apply_arguments"`
	ApplyAuthorized     bool                              `json:"apply_authorized"`
	ReviewRequired      bool                              `json:"review_required"`
	Status              string                            `json:"status"`
	AuthorizationSource string                            `json:"authorization_source"`
}

type agentCodeReplaceFixture struct {
	state              *store.SQLiteStore
	databasePath, root string
	scope              toolgateway.AgentCodeExecutionScope
	runtime            *domain.ExecutionPermissionRuntimeAuthority
	capabilities       domain.ExecutionPermissionRuntimeCapabilities
	executor           *application.AgentCodeToolExecutor
}

func newAgentCodeReplaceFixture(t *testing.T, fullAccess bool) *agentCodeReplaceFixture {
	t.Helper()
	mode := domain.RunExecutionPermissionAsk
	if fullAccess {
		mode = domain.RunExecutionPermissionFull
	}
	return newAgentCodeReplaceFixtureForPermission(t, mode)
}

func newAgentCodeReplaceFixtureForPermission(t *testing.T, mode domain.RunExecutionPermissionMode, projects ...*projectconfig.Effective) *agentCodeReplaceFixture {
	t.Helper()
	fullAccess := mode == domain.RunExecutionPermissionFull
	f := &agentCodeReplaceFixture{root: t.TempDir(), databasePath: filepath.Join(t.TempDir(), "replace.db"), runtime: domain.NewExecutionPermissionRuntimeAuthority()}
	state, err := store.Open(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.state = state
	t.Cleanup(func() { _ = state.Close() })
	if err := state.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "workspace-replace", Name: "replace", RootPath: f.root}); err != nil {
		t.Fatal(err)
	}
	f.write(t, "before\n")
	runs := application.NewRunService(state)
	var project *projectconfig.Effective
	if len(projects) != 0 {
		project = projects[0]
	}
	mission, created, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "replace existing file", Profile: "code", WorkspaceID: "workspace-replace", Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20}, ProjectConfig: project})
	if err != nil {
		t.Fatal(err)
	}
	f.capabilities = domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: f.runtime}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err := application.NewRunExecutionPermissionService(state, f.capabilities).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{
			RunID: created.ID, Mode: string(mode), OperationKey: "replace-permission-0001", RequestedBy: "operator", Reason: "test replace gates", ConfirmFull: fullAccess}); err != nil {
			t.Fatal(err)
		}
	}
	permission, err := state.GetRunExecutionPermission(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var generation uint64
	if fullAccess {
		var live bool
		generation, live = f.capabilities.FullAccessGeneration(permission)
		if !live {
			if _, err := f.runtime.ActivateRunFullAccess(permission); err != nil {
				t.Fatal(err)
			}
			generation, _ = f.capabilities.FullAccessGeneration(permission)
		}
	}
	lease, err := state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "replace-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	agent, found, err := state.GetRootAgent(t.Context(), run.ID)
	if err != nil || !found {
		t.Fatalf("root found=%t err=%v", found, err)
	}
	runMode, err := state.GetRunMode(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootHash, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	capabilityContext := toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID, RootAgentID: agent.ID,
		WorkspaceID: "workspace-replace", RootFingerprint: rootHash, Surface: runMode.Surface, Phase: runMode.Phase, Role: agent.Role, Profile: agent.Profile,
		PermissionMode: permission.Mode, ModeRevision: runMode.Revision, PermissionRevision: permission.Revision}
	capabilityContext.PermissionSnapshotID = permission.ID
	capabilityContext.PermissionGeneration = generation
	capabilityContext.PermissionRuntimeEpoch = f.runtime.RuntimeEpoch()
	capabilityContext.RunAuthorizationFence, err = f.runtime.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.scope = toolgateway.AgentCodeExecutionScope{RunID: run.ID, MissionID: mission.ID, RootAgentID: agent.ID, SessionID: run.SessionID,
		WorkspaceID: "workspace-replace", WorkspaceRoot: f.root, RootFingerprint: rootHash, Surface: runMode.Surface, Phase: runMode.Phase, Role: agent.Role, Profile: agent.Profile,
		PermissionMode: permission.Mode, PermissionSnapshotID: capabilityContext.PermissionSnapshotID, PermissionGeneration: generation,
		PermissionRuntimeEpoch: capabilityContext.PermissionRuntimeEpoch, RunAuthorizationFence: capabilityContext.RunAuthorizationFence, ModeRevision: runMode.Revision, PermissionRevision: permission.Revision,
		CapabilityGeneration: toolgateway.AgentCodeCapabilities(capabilityContext).Generation, LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation,
		RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "test allowed"}}
	// These fixtures explicitly exercise operator review; Ask/Auto routine auto writes have a separate real matrix.
	f.executor = application.NewAgentCodeToolExecutor(state, &fileOperationPolicy{review: !fullAccess}).WithExecutionPermissionCapabilities(f.capabilities)
	return f
}

func (f *agentCodeReplaceFixture) payload(content string) map[string]any {
	return map[string]any{"version": toolgateway.AgentCodeRegistryVersion, "action": "replace", "path": "target.txt", "expected_sha256": fileedit.HashText("before\n"), "content": content}
}

func (f *agentCodeReplaceFixture) execute(t *testing.T, name toolgateway.ToolName, payload json.RawMessage, key string) (toolgateway.AgentCodeExecutionResult, error) {
	t.Helper()
	scope := f.scope
	scope.OperationKey, scope.InvocationID = key, "invocation-"+key
	return f.executor.ExecuteAgentCode(t.Context(), scope, name, payload)
}

func (f *agentCodeReplaceFixture) propose(t *testing.T, content, key string) replaceProposalResult {
	t.Helper()
	result, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, f.payload(content)), key)
	if err != nil {
		t.Fatal(err)
	}
	var proposal replaceProposalResult
	if err := json.Unmarshal([]byte(result.JSON), &proposal); err != nil {
		t.Fatal(err)
	}
	return proposal
}

func (f *agentCodeReplaceFixture) write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "target.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func (f *agentCodeReplaceFixture) assertFile(t *testing.T, body string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(f.root, "target.txt")); err != nil || string(data) != body {
		t.Fatalf("file=%q want=%q err=%v", data, body, err)
	}
}
