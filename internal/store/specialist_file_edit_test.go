package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

type workerFileReviewPolicy struct {
	policy.Checker
	review bool
}

func (p workerFileReviewPolicy) CheckToolCall(call tools.Call) policy.Decision {
	d := p.Checker.CheckToolCall(call)
	if p.review && (call.Name == "replace_file" || call.Name == "delete_file") {
		d.Allowed = true
		d.NeedsApproval = true
		d.Reason = "review this exact worker file proposal"
	}
	return d
}

func TestSpecialistWorkerUsesRealFileEditAndChildApproval(t *testing.T) {
	for _, test := range []struct {
		mode     domain.RunExecutionPermissionMode
		deleting bool
	}{{domain.RunExecutionPermissionFull, false}, {domain.RunExecutionPermissionAsk, false}, {domain.RunExecutionPermissionFull, true}, {domain.RunExecutionPermissionAsk, true}} {
		mode := test.mode
		name := string(mode) + "_replace"
		if test.deleting {
			name = string(mode) + "_delete"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			st := openWorkItemTestStore(t)
			rootPath := t.TempDir()
			if err := os.WriteFile(filepath.Join(rootPath, "proof.txt"), []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveWorkspace(ctx, WorkspaceRecord{ID: "worker-file-workspace", Name: "worker files", RootPath: rootPath}); err != nil {
				t.Fatal(err)
			}
			mission, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "worker file mutation", WorkspaceID: "worker-file-workspace", Profile: "code", Budget: domain.Budget{MaxTurns: 10, MaxToolCalls: 20}})
			if err != nil {
				t.Fatal(err)
			}
			runtime := domain.NewExecutionPermissionRuntimeAuthority()
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: runtime}
			if mode == domain.RunExecutionPermissionFull {
				if _, err = application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(mode), OperationKey: "worker-file-full-permission", RequestedBy: "operator", ConfirmFull: true, Reason: "test worker file write"}); err != nil {
					t.Fatal(err)
				}
			}
			permission, err := st.GetRunExecutionPermission(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == domain.RunExecutionPermissionFull {
				if _, live := caps.FullAccessGeneration(permission); !live {
					if _, err = runtime.ActivateRunFullAccess(permission); err != nil {
						t.Fatal(err)
					}
				}
			}
			run, err = application.NewRunService(st).Start(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			parent, _, err := st.GetRootAgent(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			child, _, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID, ParentAgentID: parent.ID, Title: "file worker", Skills: []string{"model.chat"}, TurnLimit: 3, TokenLimit: 256, ToolCallLimit: 4, MaxChildren: 2, CreatedAt: time.Now().UTC(), Worker: &domain.WorkerProfile{}}, "worker-file-admission-key")
			if err != nil {
				t.Fatal(err)
			}
			acquired, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "worker-file-test", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			lease := acquired.Lease
			attempt, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: child.ID, ParentAgentID: parent.ID, Lease: lease, StartedAt: time.Now().UTC()}, "worker-file-attempt-key")
			if err != nil {
				t.Fatal(err)
			}
			ref := attemptRef(attempt)
			brief, err := st.PrepareSpecialistContext(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := workspace.AgentCodeRootFingerprint(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.BindSpecialistTools(ctx, domain.SpecialistToolBinding{Ref: ref, SessionID: child.SessionID, WorkspaceID: mission.WorkspaceID, RootFingerprint: fingerprint, BriefFingerprint: brief.TaskBrief.Fingerprint, Mode: "worker", WorkerProfileJSON: `{"tools":null}`}); err != nil {
				t.Fatal(err)
			}
			runMode, err := st.GetRunMode(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			generation, _ := caps.FullAccessGeneration(permission)
			fence, err := runtime.IssueRunAuthorizationFence(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			catalogue := toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID, RootAgentID: parent.ID, WorkspaceID: mission.WorkspaceID, RootFingerprint: fingerprint, Surface: runMode.Surface, Phase: runMode.Phase, Role: domain.AgentRoleRoot, Profile: runMode.Profile, PermissionMode: permission.Mode, PermissionSnapshotID: permission.ID, PermissionGeneration: generation, PermissionRuntimeEpoch: runtime.RuntimeEpoch(), RunAuthorizationFence: fence, ModeRevision: runMode.Revision, PermissionRevision: permission.Revision}
			parentAuthority, err := toolgateway.NewAgentCodeCallAuthority(catalogue, run.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			parentJSON, _ := toolgateway.EncodeAgentCodeCallAuthority(parentAuthority)
			executor := application.NewAgentCodeToolExecutor(st, workerFileReviewPolicy{Checker: policy.NewDefaultChecker(), review: mode == domain.RunExecutionPermissionAsk}).WithExecutionPermissionCapabilities(caps)
			prepare := func(number int, name toolgateway.ToolName, payload any) (domain.SpecialistToolCall, toolgateway.AgentCodeExecutionScope) {
				t.Helper()
				raw, _ := json.Marshal(payload)
				raw, err = toolgateway.NormalizeAgentCodePayload(name, raw)
				if err != nil {
					t.Fatal(err)
				}
				m := llm.ModelAttempt{Number: number, TransportAttempt: 1, MaxAttempts: 1, ToolRound: number - 1, Provider: "mock", Model: "model", Context: &llm.ModelContextAudit{TokenBudget: 1, EstimatedTokens: 1, Included: []llm.ModelContextSource{{Kind: "specialist_task_brief", SourceID: brief.TaskBrief.Fingerprint, Tokens: 1}}}}
				if _, err = st.RecordSpecialistModelStarted(ctx, ref, m); err != nil {
					t.Fatal(err)
				}
				callID := idgen.New("provider-call")
				if _, err = st.RecordSpecialistToolModelCompleted(ctx, ref, m, llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: callID, Name: string(name), Arguments: raw}}, Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}, "worker file input", policy.Decision{Allowed: true, Risk: "low", Reason: "allowed"}); err != nil {
					t.Fatal(err)
				}
				authority, err := executor.BindSpecialistAgentCodeAuthority(ctx, ref, name, parentJSON, raw)
				if err != nil {
					t.Fatal(err)
				}
				c, err := st.BindSpecialistToolAuthority(ctx, ref, callID, string(authority))
				if err != nil {
					t.Fatal(err)
				}
				a, err := toolgateway.DecodeAgentCodeCallAuthority(authority)
				if err != nil {
					t.Fatal(err)
				}
				scope := toolgateway.AgentCodeExecutionScope{AgentID: child.ID, AgentAttemptID: attempt.ID, SpecialistToolCallID: callID, OperationKey: idgen.New("worker-file-operation"), RunID: run.ID, MissionID: mission.ID, RootAgentID: parent.ID, SessionID: child.SessionID, WorkspaceID: mission.WorkspaceID, WorkspaceRoot: rootPath, RootFingerprint: a.RootFingerprint, Surface: a.Surface, Phase: a.Phase, Role: domain.AgentRoleSpecialist, Profile: a.Profile, PermissionMode: a.PermissionMode, PermissionSnapshotID: a.PermissionSnapshotID, PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence, ModeRevision: a.ModeRevision, PermissionRevision: a.PermissionRevision, CapabilityGeneration: a.CapabilityGeneration, LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "specialist_runner", PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "allowed"}}
				return c, scope
			}
			start := func(c domain.SpecialistToolCall, scope toolgateway.AgentCodeExecutionScope) toolgateway.AgentCodeExecutionScope {
				t.Helper()
				charge, err := st.ChargeToolCall(ctx, toolbudget.ChargeRequest{RunID: run.ID, AgentID: child.ID, AgentAttemptID: attempt.ID, ToolCallID: c.CallID, SessionID: child.SessionID, WorkspaceID: mission.WorkspaceID, ToolName: c.Name, ActionClass: "workspace_write", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "specialist_runner"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = st.MarkSpecialistToolStarted(ctx, ref, c.CallID, c.AuthorityJSON); err != nil {
					t.Fatal(err)
				}
				scope.InvocationID = charge.LastCharge
				scope.LeaseID = lease.LeaseID
				scope.LeaseGeneration = lease.Generation
				return scope
			}
			firstName := toolgateway.WorkspaceChangeTool
			var firstPayload any = toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "proof.txt", ExpectedSHA256: fileedit.HashText("before\n"), Replacements: []toolgateway.WorkspaceReplacement{{OldText: "before\n", NewText: "after\n", ExpectedOccurrences: 1}}}
			if test.deleting {
				firstName = toolgateway.WorkspaceDeleteTool
				firstPayload = toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose", Path: "proof.txt", ConfirmPath: "proof.txt", ExpectedSHA256: fileedit.HashText("before\n")}
			}
			first, scope := prepare(1, firstName, firstPayload)
			if waiting, result, err := executor.PreflightSpecialistFileEditApproval(ctx, ref, first.CallID); err != nil || waiting || result != nil {
				t.Fatalf("proposal waited before it existed: %v %+v %v", waiting, result, err)
			}
			scope = start(first, scope)
			forged := scope
			forged.SessionID = run.SessionID
			if _, err = executor.ExecuteAgentCode(ctx, forged, firstName, json.RawMessage(first.PayloadJSON)); err == nil {
				t.Fatal("worker file scope accepted root Session")
			}
			proposal, err := executor.ExecuteAgentCode(ctx, scope, firstName, json.RawMessage(first.PayloadJSON))
			if err != nil {
				t.Fatal(err)
			}
			first.Status = domain.SupervisorToolCompleted
			first.ResultJSON = proposal.JSON
			if err = st.CompleteSpecialistToolCall(ctx, first); err != nil {
				t.Fatal(err)
			}
			edits, err := st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: child.SessionID})
			if err != nil || len(edits) != 1 {
				t.Fatalf("worker proposal did not retain child Session: %+v %v", edits, err)
			}
			edit := edits[0]
			secondName := toolgateway.WorkspaceApplyTool
			var secondPayload any = toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.ID, ExpectedAction: "propose_patch", ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash}
			if test.deleting {
				secondName = toolgateway.WorkspaceDeleteTool
				secondPayload = toolgateway.WorkspaceDeletePayload{Version: toolgateway.AgentCodeRegistryVersion, Action: "apply", EditID: edit.ID, Path: "proof.txt", ConfirmPath: "proof.txt", ExpectedSHA256: edit.OriginalHash}
			}
			second, applyScope := prepare(2, secondName, secondPayload)
			waiting, result, err := executor.PreflightSpecialistFileEditApproval(ctx, ref, second.CallID)
			if err != nil || result != nil {
				t.Fatalf("file preflight: %v %+v %v", waiting, result, err)
			}
			if mode == domain.RunExecutionPermissionAsk {
				if !waiting {
					t.Fatal("Ask worker file proposal did not wait")
				}
				if _, _, err = st.ReleaseRunExecutionLease(ctx, lease); err != nil {
					t.Fatal(err)
				}
				if _, err = application.NewFileEditReviewService(st).Review(ctx, application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: application.FileEditApproveIntent}); err != nil {
					t.Fatal(err)
				}
				acquired, err = st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "worker-file-resumed", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				lease = acquired.Lease
				if _, err = st.ResumeSpecialistToolAttempt(ctx, ref, lease); err != nil {
					t.Fatal(err)
				}
				waiting, result, err = executor.PreflightSpecialistFileEditApproval(ctx, ref, second.CallID)
				if err != nil || waiting || result != nil {
					t.Fatalf("approved worker did not resume: %v %+v %v", waiting, result, err)
				}
			} else if waiting {
				t.Fatal("Full worker unnecessarily waited")
			}
			applyScope = start(second, applyScope)
			if _, err = executor.ExecuteAgentCode(ctx, applyScope, secondName, json.RawMessage(second.PayloadJSON)); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(rootPath, "proof.txt"))
			if test.deleting {
				if !os.IsNotExist(err) {
					t.Fatalf("worker delete did not remove the file: %q %v", body, err)
				}
			} else if err != nil || string(body) != "after\n" {
				t.Fatalf("worker write missing: %q %v", body, err)
			}
			audit, err := st.GetApprovalByProposal(ctx, edit.ID)
			if err != nil || audit.SessionID != child.SessionID || audit.RunID != run.ID {
				t.Fatalf("worker approval attribution lost: %+v %v", audit, err)
			}
		})
	}
}
