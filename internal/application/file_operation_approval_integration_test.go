package application_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

type fileOperationPolicy struct {
	review, reviewDelete, deny bool
	beforeCheck                func(tools.Call)
}

func (p *fileOperationPolicy) CheckText(context, text string) policy.Decision {
	return policy.NewDefaultChecker().CheckText(context, text)
}
func (p *fileOperationPolicy) CheckToolCall(call tools.Call) policy.Decision {
	if p.beforeCheck != nil {
		p.beforeCheck(call)
	}
	d := policy.NewDefaultChecker().CheckToolCall(call)
	d.Allowed = d.Allowed && !p.deny
	fileWrite := call.Name == "create_file" || call.Name == "replace_file" || call.Name == "move_file" || call.Name == "delete_file"
	d.NeedsApproval = d.NeedsApproval || (p.review && fileWrite) || (p.reviewDelete && call.Name == "delete_file")
	return d
}

type fileOperationFixture struct {
	state          *store.SQLiteStore
	root, database string
	runtime        *domain.ExecutionPermissionRuntimeAuthority
	capabilities   domain.ExecutionPermissionRuntimeCapabilities
	scope          toolgateway.AgentCodeExecutionScope
	policy         *fileOperationPolicy
	executor       *application.AgentCodeToolExecutor
}

func newFileOperationFixture(t *testing.T, mode domain.RunExecutionPermissionMode) *fileOperationFixture {
	t.Helper()
	f := &fileOperationFixture{root: t.TempDir(), database: filepath.Join(t.TempDir(), "files.db"),
		runtime: domain.NewExecutionPermissionRuntimeAuthority(), policy: &fileOperationPolicy{}}
	var err error
	f.state, err = store.Open(f.database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.state.Close() })
	ctx := t.Context()
	if err := f.state.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "workspace-file-operation", Name: "files", RootPath: f.root}); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(f.state)
	mission, created, err := runs.Create(ctx, application.CreateRunRequest{Goal: "verify exact file operation decisions", Profile: "code",
		WorkspaceID: "workspace-file-operation", Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	f.capabilities = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: f.runtime}
	if mode != domain.RunExecutionPermissionAsk {
		_, err = application.NewRunExecutionPermissionService(f.state, f.capabilities).Change(ctx, application.ChangeRunExecutionPermissionRequest{
			RunID: created.ID, Mode: string(mode), OperationKey: "file-operation-select-mode", RequestedBy: "operator", Reason: "exercise current operation policy", ConfirmFull: mode == domain.RunExecutionPermissionFull})
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := f.state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mode == domain.RunExecutionPermissionFull {
		if _, err := f.runtime.ActivateRunFullAccess(permission); err != nil {
			t.Fatal(err)
		}
	}
	generation, live := f.capabilities.FullAccessGeneration(permission)
	if !live {
		t.Fatal("fixture has no current permission")
	}
	fence, err := f.runtime.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, found, err := f.state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent: %t %v", found, err)
	}
	modeSnapshot, err := f.state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "file-operation-test", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rootHash, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	capability := toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID, RootAgentID: agent.ID,
		WorkspaceID: "workspace-file-operation", RootFingerprint: rootHash, Surface: modeSnapshot.Surface, Phase: modeSnapshot.Phase,
		Role: agent.Role, Profile: agent.Profile, PermissionMode: mode, PermissionSnapshotID: permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: f.runtime.RuntimeEpoch(), RunAuthorizationFence: fence,
		ModeRevision: modeSnapshot.Revision, PermissionRevision: permission.Revision}
	f.scope = toolgateway.AgentCodeExecutionScope{RunID: run.ID, MissionID: mission.ID, RootAgentID: agent.ID, SessionID: run.SessionID,
		WorkspaceID: capability.WorkspaceID, WorkspaceRoot: f.root, RootFingerprint: rootHash, Surface: capability.Surface, Phase: capability.Phase,
		Role: capability.Role, Profile: capability.Profile, PermissionMode: mode, PermissionSnapshotID: permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: capability.PermissionRuntimeEpoch, RunAuthorizationFence: fence,
		ModeRevision: capability.ModeRevision, PermissionRevision: capability.PermissionRevision, CapabilityGeneration: toolgateway.AgentCodeCapabilities(capability).Generation,
		LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation, RequestedBy: "run_supervisor",
		PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "fixture host policy"}}
	f.executor = application.NewAgentCodeToolExecutor(f.state, f.policy).WithExecutionPermissionCapabilities(f.capabilities)
	return f
}

func (f *fileOperationFixture) execute(ctx context.Context, name toolgateway.ToolName, input any, key string) (toolgateway.AgentCodeExecutionResult, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return toolgateway.AgentCodeExecutionResult{}, err
	}
	scope := f.scope
	scope.InvocationID, scope.OperationKey = "invoke-"+key, key
	return f.executor.ExecuteAgentCode(ctx, scope, name, raw)
}

func TestFileOperationApprovalModesUseActualRootedWrites(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		for _, operation := range []string{fileedit.OperationCreate, fileedit.OperationReplace, fileedit.OperationMove, fileedit.OperationDelete} {
			t.Run(string(mode)+"/"+operation, func(t *testing.T) {
				f := newFileOperationFixture(t, mode)
				hash := "missing"
				if operation != fileedit.OperationCreate {
					if err := os.WriteFile(filepath.Join(f.root, "file.txt"), []byte("before\n"), 0600); err != nil {
						t.Fatal(err)
					}
					hash = fileedit.HashText("before\n")
				}
				input := toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: operation,
					Path: "file.txt", ExpectedSHA256: hash, Content: "after\n"}
				name := toolgateway.WorkspaceChangeTool
				var payload any = input
				if operation == fileedit.OperationMove {
					input.Content = ""
					input.DestinationPath = "moved.txt"
					input.DestinationExpectedSHA256 = "missing"
					payload = input
				}
				if operation == fileedit.OperationDelete {
					name = toolgateway.WorkspaceDeleteTool
					payload = toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose", Path: "file.txt", ExpectedSHA256: hash}
				}
				result, err := f.execute(t.Context(), name, payload, "file-propose-exact-0001")
				if err != nil {
					t.Fatal(err)
				}
				proposal := decodeAgentCodeToolResult(t, result.JSON)
				review := operation == fileedit.OperationDelete && mode != domain.RunExecutionPermissionFull
				if !review && operation != fileedit.OperationDelete {
					if proposal.Status != fileedit.StatusApplied {
						t.Fatalf("1-step proposal status=%s want=%s: %s", proposal.Status, fileedit.StatusApplied, result.JSON)
					}
					var bodyMap map[string]any
					if err := json.Unmarshal([]byte(result.JSON), &bodyMap); err != nil || bodyMap["file_written"] != true || bodyMap["replayed"] != false {
						t.Fatalf("1-step apply flags invalid: %s err=%v", result.JSON, err)
					}
					if replay, err := f.execute(t.Context(), name, payload, "file-propose-exact-0001"); err != nil || !replay.Replayed || !strings.Contains(replay.JSON, `"file_written":false`) {
						t.Fatalf("1-step replay=%+v err=%v", replay, err)
					}
				} else {
					expectedStatus := fileedit.StatusApproved
					if review {
						expectedStatus = fileedit.StatusProposed
					}
					if proposal.Status != expectedStatus {
						t.Fatalf("proposal status=%s want=%s: %s", proposal.Status, expectedStatus, result.JSON)
					}
					if data, err := os.ReadFile(filepath.Join(f.root, "file.txt")); err != nil || string(data) != "before\n" {
						t.Fatalf("delete proposal mutated bytes: %q %v", data, err)
					}
					if review {
						if _, err := application.NewFileEditReviewService(f.state).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
							RunID: f.scope.RunID, EditID: proposal.EditID, Action: application.FileEditApproveIntent}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !review && operation != fileedit.OperationDelete {
					// Verify backward-compatible workspace_apply replays idempotently without rewriting bytes.
					compatPayload := toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
						EditID: proposal.EditID, ExpectedAction: operation, ExpectedOriginalSHA256: proposal.OriginalSHA256, ExpectedProposedSHA256: proposal.ProposedSHA256}
					if compat, err := f.execute(t.Context(), toolgateway.WorkspaceApplyTool, compatPayload, "file-apply-compat-0001"); err != nil || !compat.Replayed || !strings.Contains(compat.JSON, `"file_written":false`) {
						t.Fatalf("compat workspace_apply=%+v err=%v", compat, err)
					}
				} else {
					name = toolgateway.WorkspaceDeleteTool
					payload = toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "apply", Path: "file.txt", ConfirmPath: "file.txt", EditID: proposal.EditID, ExpectedSHA256: hash}
					if _, err := f.execute(t.Context(), name, payload, "file-apply-exact-0001"); err != nil {
						t.Fatal(err)
					}
					if replay, err := f.execute(t.Context(), name, payload, "file-apply-exact-0001"); err != nil || !replay.Replayed {
						t.Fatalf("replay=%+v err=%v", replay, err)
					}
				}
				if operation == fileedit.OperationMove || operation == fileedit.OperationDelete {
					if _, err := os.Stat(filepath.Join(f.root, "file.txt")); !os.IsNotExist(err) {
						t.Fatalf("source retained: %v", err)
					}
				} else if data, err := os.ReadFile(filepath.Join(f.root, "file.txt")); err != nil || string(data) != "after\n" {
					t.Fatalf("wrong written bytes: %q %v", data, err)
				}
				if operation == fileedit.OperationMove {
					if data, err := os.ReadFile(filepath.Join(f.root, "moved.txt")); err != nil || string(data) != "before\n" {
						t.Fatalf("move destination: %q %v", data, err)
					}
				}
			})
		}
	}
}

// Refresh only host-issued scope fields. This deliberately does not rewrite
// the already persisted proposal or its immutable automatic source.
func (f *fileOperationFixture) refreshScope(t *testing.T) {
	t.Helper()
	permission, err := f.state.GetRunExecutionPermission(t.Context(), f.scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if permission.Mode == domain.RunExecutionPermissionFull {
		if _, err := f.runtime.ActivateRunFullAccess(permission); err != nil {
			t.Fatal(err)
		}
	}
	generation, live := f.capabilities.FullAccessGeneration(permission)
	if !live {
		t.Fatal("fresh explicit fixture activation is unavailable")
	}
	fence, err := f.runtime.IssueRunAuthorizationFence(f.scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	f.scope.PermissionMode, f.scope.PermissionSnapshotID, f.scope.PermissionRevision = permission.Mode, permission.ID, permission.Revision
	f.scope.PermissionGeneration, f.scope.PermissionRuntimeEpoch, f.scope.RunAuthorizationFence = generation, f.runtime.RuntimeEpoch(), fence
	f.scope.CapabilityGeneration = toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{
		RunID: f.scope.RunID, MissionID: f.scope.MissionID, RootAgentID: f.scope.RootAgentID,
		WorkspaceID: f.scope.WorkspaceID, RootFingerprint: f.scope.RootFingerprint, Surface: f.scope.Surface,
		Phase: f.scope.Phase, Role: f.scope.Role, Profile: f.scope.Profile, ModeRevision: f.scope.ModeRevision,
		PermissionMode: permission.Mode, PermissionRevision: permission.Revision, PermissionSnapshotID: permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: f.scope.PermissionRuntimeEpoch, RunAuthorizationFence: fence}).Generation
}

func TestFileOperationAutomaticSourceCannotSurviveChangedAuthority(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		for _, boundary := range []string{"policy_denied", "policy_requires_review", "revoked_then_renewed", "sqlite_reopen_new_runtime", "cancelled", "staged_before_publication"} {
			t.Run(string(mode)+"/"+boundary, func(t *testing.T) {
				f := newFileOperationFixture(t, mode)
				path := filepath.Join(f.root, "file.txt")
				if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				sawStaging := false
				if boundary == "staged_before_publication" {
					f.policy.beforeCheck = func(tools.Call) {
						staging, _ := filepath.Glob(filepath.Join(f.root, ".cyberagent-edit-*"))
						if len(staging) != 0 {
							sawStaging, f.policy.deny = true, true
						}
					}
					_, err := f.execute(ctx, toolgateway.WorkspaceChangeTool, toolgateway.WorkspaceChangePayload{
						Version: toolgateway.AgentCodeRegistryVersion, Action: fileedit.OperationReplace, Path: "file.txt",
						ExpectedSHA256: fileedit.HashText("before\n"), Content: "after\n"}, "file-source-staged-change")
					if err == nil || !sawStaging {
						t.Fatalf("staged_before_publication did not fail closed at publication boundary: sawStaging=%t err=%v", sawStaging, err)
					}
				} else {
					prepared, err := fileedit.NewManager(f.state).PrepareProposal(t.Context(), fileedit.Proposal{
						SessionID: f.scope.SessionID,
						WorkspaceID: f.scope.WorkspaceID, WorkspaceRoot: f.root, Path: "file.txt",
						Operation: fileedit.OperationReplace, ProposedText: "after\n",
						ExpectedOriginalHash: fileedit.HashText("before\n"),
					})
					if err != nil {
						t.Fatal(err)
					}
					edit, replayed, err := f.state.CreateAutomaticallyAuthorizedFileEditIfAbsent(t.Context(), prepared, fileedit.AutoAuthorization{
						RunID: f.scope.RunID, SessionID: f.scope.SessionID, WorkspaceID: f.scope.WorkspaceID,
						AgentID: f.scope.RootAgentID, OperationKeyDigest: strings.Repeat("1", 64),
						ProposalFingerprint: runmutation.Fingerprint("agent_code_file_edit_proposal.v1",
							prepared.ID, prepared.SessionID, prepared.WorkspaceID, prepared.Operation,
							prepared.Path, prepared.DestinationPath, prepared.OriginalHash, prepared.ProposedHash,
							prepared.DestinationOriginalHash, prepared.DestinationProposedHash),
						PermissionSnapshotID: f.scope.PermissionSnapshotID,
						PermissionRevision:   f.scope.PermissionRevision,
						ModeRevision:         f.scope.ModeRevision,
						RuntimeEpoch:         f.scope.PermissionRuntimeEpoch,
						RuntimeGeneration:    f.scope.PermissionGeneration,
						RunAuthorizationFence: f.scope.RunAuthorizationFence,
						CapabilityGeneration:  f.scope.CapabilityGeneration,
						LeaseID:               f.scope.LeaseID,
						LeaseGeneration:       f.scope.LeaseGeneration,
					})
					if err != nil || replayed || edit.Status != fileedit.StatusApproved {
						t.Fatalf("automatic fixture failed: edit=%+v replayed=%t err=%v", edit, replayed, err)
					}
					switch boundary {
					case "policy_denied":
						f.policy.deny = true
					case "policy_requires_review":
						f.policy.review = true
					case "revoked_then_renewed":
						f.runtime.RevokeRun(f.scope.RunID)
						f.refreshScope(t)
					case "sqlite_reopen_new_runtime":
						if err := f.state.Close(); err != nil {
							t.Fatal(err)
						}
						f.state, err = store.Open(f.database)
						if err != nil {
							t.Fatal(err)
						}
						f.runtime = domain.NewExecutionPermissionRuntimeAuthority()
						f.capabilities.RuntimeAuthority = f.runtime
						f.executor = application.NewAgentCodeToolExecutor(f.state, f.policy).WithExecutionPermissionCapabilities(f.capabilities)
						f.refreshScope(t)
					case "cancelled":
						cancel()
					}
					_, err = f.execute(ctx, toolgateway.WorkspaceApplyTool, toolgateway.WorkspaceApplyPayload{
						Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.ID, ExpectedAction: fileedit.OperationReplace,
						ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash}, "file-apply-after-change")
					if err == nil {
						t.Fatal("old automatic source executed after authority changed")
					}
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != "before\n" {
					t.Fatalf("denied operation changed bytes: %q %v", data, err)
				}
				if staging, err := filepath.Glob(filepath.Join(f.root, ".cyberagent-edit-*")); err != nil || len(staging) != 0 {
					t.Fatalf("staging was not settled: %v %v", staging, err)
				}
			})
		}
	}
}

func TestFileOperationFullDoesNotUpgradePendingOrDeniedProposal(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "pending"
		if deny {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			f := newFileOperationFixture(t, domain.RunExecutionPermissionFull)
			f.policy.review = true
			input := toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: fileedit.OperationCreate, Path: "file.txt", ExpectedSHA256: "missing", Content: "after\n"}
			proposed, err := f.execute(t.Context(), toolgateway.WorkspaceChangeTool, input, "file-existing-reviewed-proposal")
			if err != nil {
				t.Fatal(err)
			}
			edit := decodeAgentCodeToolResult(t, proposed.JSON)
			if edit.Status != fileedit.StatusProposed {
				t.Fatal("host review requirement was ignored")
			}
			if deny {
				if _, err := application.NewFileEditReviewService(f.state).Review(t.Context(), application.ReviewFileEditRequest{
					Version: application.FileEditReviewProtocolVersion, RunID: f.scope.RunID, EditID: edit.EditID,
					Action: application.FileEditDeny}); err != nil {
					t.Fatal(err)
				}
			}
			f.policy.review = false
			// Replaying the identical proposal may report its old outcome, but
			// cannot turn the existing pending/denied decision into Full consent.
			if _, err := f.execute(t.Context(), toolgateway.WorkspaceChangeTool, input, "file-existing-reviewed-proposal"); err != nil {
				t.Fatal(err)
			}
			approval, err := f.state.GetApprovalByProposal(t.Context(), edit.EditID)
			if err != nil || string(approval.Status) == "approved" {
				t.Fatalf("old review was upgraded: %+v %v", approval, err)
			}
			_, err = f.execute(t.Context(), toolgateway.WorkspaceApplyTool, toolgateway.WorkspaceApplyPayload{
				Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.EditID, ExpectedAction: fileedit.OperationCreate,
				ExpectedOriginalSHA256: edit.OriginalSHA256, ExpectedProposedSHA256: edit.ProposedSHA256}, "file-no-bypass-existing-review")
			if err == nil {
				t.Fatal("Full bypassed the existing review decision")
			}
			if _, err := os.Stat(filepath.Join(f.root, "file.txt")); !os.IsNotExist(err) {
				t.Fatalf("unreviewed bytes were published: %v", err)
			}
		})
	}
}

func TestFileOperationReadRechecksHostPolicyBeforeReturningBytes(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFileOperationFixture(t, mode)
			if err := os.WriteFile(filepath.Join(f.root, "file.txt"), []byte("only release while authorized\n"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.policy.beforeCheck = func(call tools.Call) {
				if call.Name == string(toolgateway.WorkspaceReadTool) {
					calls++
					if calls == 3 {
						f.policy.deny = true
					}
				}
			}
			result, err := f.execute(t.Context(), toolgateway.WorkspaceReadTool, toolgateway.WorkspaceReadPayload{
				Version: toolgateway.AgentCodeRegistryVersion, Path: "file.txt", StartLine: 1, EndLine: 10}, "file-read-policy-recheck")
			if err == nil || result.JSON != "" || calls != 3 {
				t.Fatalf("read did not withhold revoked output: calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}
