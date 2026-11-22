package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

type tier5PolicyChecker struct {
	base    policy.Checker
	decide  func(tools.Call, policy.Decision) policy.Decision
	mu      sync.Mutex
	history []tools.Call
}

func newTier5PolicyChecker(decide func(tools.Call, policy.Decision) policy.Decision) *tier5PolicyChecker {
	return &tier5PolicyChecker{base: policy.NewDefaultChecker(), decide: decide}
}

func (c *tier5PolicyChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c *tier5PolicyChecker) CheckToolCall(call tools.Call) policy.Decision {
	c.mu.Lock()
	c.history = append(c.history, call)
	c.mu.Unlock()
	d := c.base.CheckToolCall(call)
	if c.decide != nil {
		return c.decide(call, d)
	}
	return d
}

type tier5ExecutorFixture struct {
	state     *store.SQLiteStore
	root      string
	workspace store.WorkspaceRecord
	mission   domain.Mission
	run       domain.Run
	threadID  string
	authority *domain.ExecutionPermissionRuntimeAuthority
	caps      domain.ExecutionPermissionRuntimeCapabilities
	lease     domain.RunExecutionLease
	checker   *tier5PolicyChecker
}

func newTier5ExecutorFixture(t *testing.T, mode domain.RunExecutionPermissionMode, checker *tier5PolicyChecker) *tier5ExecutorFixture {
	t.Helper()
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "tier5-r3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ws := store.WorkspaceRecord{ID: "ws-tier5-r3", Name: "tier5-r3", RootPath: root, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        authority,
	}
	mission, created, err := NewRunService(state).Create(ctx, CreateRunRequest{
		Goal:        "Tier 5 R3 white-box verification",
		Profile:     "code",
		Surface:     "code",
		Phase:       "deliver",
		WorkspaceID: ws.ID,
		Interactive: true,
		Budget:      domain.Budget{MaxTurns: 12, MaxToolCalls: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" && mode != domain.RunExecutionPermissionAsk {
		if _, err := NewRunExecutionPermissionService(state, caps).Change(ctx, ChangeRunExecutionPermissionRequest{
			RunID:        created.ID,
			Mode:         string(mode),
			OperationKey: "tier5-permission-" + string(mode),
			RequestedBy:  "test_operator",
			Reason:       "tier 5 permission setup",
			ConfirmFull:  mode == domain.RunExecutionPermissionFull,
		}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if perm.Mode == domain.RunExecutionPermissionFull {
		if _, err := authority.ActivateRunFullAccess(perm); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "tier5-exec", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if checker == nil {
		checker = newTier5PolicyChecker(nil)
	}
	return &tier5ExecutorFixture{
		state:     state,
		root:      root,
		workspace: ws,
		mission:   mission,
		run:       run,
		threadID:  domain.InitialThreadID(run.ID),
		authority: authority,
		caps:      caps,
		lease:     lease.Lease,
		checker:   checker,
	}
}

func (f *tier5ExecutorFixture) currentScope(t *testing.T, opKey string) toolgateway.AgentCodeExecutionScope {
	t.Helper()
	ctx := t.Context()
	run, err := f.state.GetRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := f.state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := f.state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, found, err := f.state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	rootFP, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID, permGen, runtimeEpoch, fence, live := bindAgentCodeRuntime(f.caps, perm)
	if !live {
		t.Fatalf("runtime permission not live for mode=%s", perm.Mode)
	}
	capCtx := toolgateway.AgentCodeCapabilityContext{
		RunID: run.ID, MissionID: f.mission.ID, RootAgentID: rootAgent.ID,
		WorkspaceID: f.workspace.ID, RootFingerprint: rootFP,
		Surface: mode.Surface, Phase: mode.Phase, Role: rootAgent.Role, Profile: rootAgent.Profile,
		PermissionMode: perm.Mode, PermissionSnapshotID: snapshotID,
		PermissionGeneration: permGen, PermissionRuntimeEpoch: runtimeEpoch,
		RunAuthorizationFence: fence, ModeRevision: mode.Revision, PermissionRevision: perm.Revision,
	}
	return toolgateway.AgentCodeExecutionScope{
		InvocationID:           "inv-" + opKey,
		OperationKey:           opKey,
		RunID:                  run.ID,
		MissionID:              f.mission.ID,
		RootAgentID:            rootAgent.ID,
		SessionID:              run.SessionID,
		WorkspaceID:            f.workspace.ID,
		WorkspaceRoot:          f.root,
		RootFingerprint:        rootFP,
		Surface:                mode.Surface,
		Phase:                  mode.Phase,
		Role:                   rootAgent.Role,
		Profile:                rootAgent.Profile,
		PermissionMode:         perm.Mode,
		PermissionSnapshotID:   snapshotID,
		PermissionGeneration:   permGen,
		PermissionRuntimeEpoch: runtimeEpoch,
		RunAuthorizationFence:  fence,
		ModeRevision:           mode.Revision,
		PermissionRevision:     perm.Revision,
		CapabilityGeneration:   toolgateway.AgentCodeCapabilities(capCtx).Generation,
		LeaseID:                f.lease.LeaseID,
		LeaseGeneration:        f.lease.Generation,
		RequestedBy:            "run_supervisor",
		PolicyDecision:         toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "tier5 test"},
	}
}

func (f *tier5ExecutorFixture) executor() *AgentCodeToolExecutor {
	return NewAgentCodeToolExecutor(f.state, f.checker).WithExecutionPermissionCapabilities(f.caps)
}

func TestThroughputTier5_R3_OneStepReplayMoveCreatePatchAutoAndFull(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := t.Context()
			f := newTier5ExecutorFixture(t, mode, nil)
			exec := f.executor()

			// 1) 1-step create -> replay when target already exists on disk.
			createScope := f.currentScope(t, "tier5-create-0001")
			createPayload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
				Version:        toolgateway.AgentCodeRegistryVersion,
				Action:         "create",
				Path:           "nested/doc.txt",
				ExpectedSHA256: "missing",
				Content:        "alpha\nbeta\n",
			})
			res1, err := exec.ExecuteAgentCode(ctx, createScope, toolgateway.WorkspaceChangeTool, createPayload)
			if err != nil {
				t.Fatalf("create failed: %v", err)
			}
			var out1 map[string]any
			if err := json.Unmarshal([]byte(res1.JSON), &out1); err != nil {
				t.Fatal(err)
			}
			if out1["status"] != fileedit.StatusApplied || out1["file_written"] != true || out1["replayed"] != false ||
				out1["apply_authorized"] != true || out1["authorization_source"] != "operation_policy_automatic" ||
				out1["review_required"] != false {
				t.Fatalf("unexpected 1-step create output: %s", res1.JSON)
			}
			createEditID := out1["edit_id"].(string)

			// Verify ObservedSupervisorToolEffect recognizes 1-step applied workspace_change as recorded_applied.
			envJSON, _ := json.Marshal(supervisorToolResultEnvelope{
				Version: supervisorToolResultVersion, Tool: "workspace_change",
				Status: "completed", Stdout: res1.JSON, Metadata: res1.Metadata,
			})
			effect := domain.ObservedSupervisorToolEffect(domain.SupervisorToolCall{
				ToolName: "workspace_change", Status: domain.SupervisorToolCompleted, ResultJSON: string(envJSON),
			})
			if effect == nil || effect.Effect != "recorded_applied" || effect.FileWritten == nil || !*effect.FileWritten {
				t.Fatalf("expected recorded_applied effect for 1-step create, got %+v", effect)
			}

			// Replay 1-step create while target nested/doc.txt already exists on disk!
			createReplayScope := f.currentScope(t, "tier5-create-0001")
			createReplayScope.InvocationID = "inv-tier5-create-replay"
			res1Replay, err := exec.ExecuteAgentCode(ctx, createReplayScope, toolgateway.WorkspaceChangeTool, createPayload)
			if err != nil {
				t.Fatalf("create replay failed while target exists: %v", err)
			}
			var out1Replay map[string]any
			if err := json.Unmarshal([]byte(res1Replay.JSON), &out1Replay); err != nil {
				t.Fatal(err)
			}
			if !res1Replay.Replayed || out1Replay["status"] != fileedit.StatusApplied ||
				out1Replay["file_written"] != false || out1Replay["replayed"] != true || out1Replay["edit_id"] != createEditID {
				t.Fatalf("unexpected create replay output: replayed=%t body=%s", res1Replay.Replayed, res1Replay.JSON)
			}

			// Compat workspace_apply on already-applied 1-step edit must be idempotent no-op.
			compatScope := f.currentScope(t, "tier5-compat-apply-0001")
			compatRes, err := exec.ExecuteAgentCode(ctx, compatScope, toolgateway.WorkspaceApplyTool, mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceApplyPayload{
				Version:                toolgateway.AgentCodeRegistryVersion,
				EditID:                 createEditID,
				ExpectedAction:         "create",
				ExpectedOriginalSHA256: "missing",
				ExpectedProposedSHA256: fileedit.HashText("alpha\nbeta\n"),
			}))
			if err != nil || !compatRes.Replayed || !strings.Contains(compatRes.JSON, `"file_written":false`) {
				t.Fatalf("compat workspace_apply failed: res=%+v err=%v", compatRes, err)
			}

			// 2) 1-step propose_patch -> replay while disk now holds patched text.
			patchScope := f.currentScope(t, "tier5-patch-0001")
			patchPayload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
				Version:        toolgateway.AgentCodeRegistryVersion,
				Action:         "propose_patch",
				Path:           "nested/doc.txt",
				ExpectedSHA256: fileedit.HashText("alpha\nbeta\n"),
				Replacements:   []toolgateway.WorkspaceReplacement{{OldText: "beta", NewText: "gamma", ExpectedOccurrences: 1}},
			})
			res2, err := exec.ExecuteAgentCode(ctx, patchScope, toolgateway.WorkspaceChangeTool, patchPayload)
			if err != nil {
				t.Fatalf("propose_patch failed: %v", err)
			}
			if !strings.Contains(res2.JSON, `"status":"applied"`) || !strings.Contains(res2.JSON, `"file_written":true`) {
				t.Fatalf("expected 1-step patch applied: %s", res2.JSON)
			}
			patchReplayScope := f.currentScope(t, "tier5-patch-0001")
			patchReplayScope.InvocationID = "inv-tier5-patch-replay"
			res2Replay, err := exec.ExecuteAgentCode(ctx, patchReplayScope, toolgateway.WorkspaceChangeTool, patchPayload)
			if err != nil {
				t.Fatalf("propose_patch replay failed: %v", err)
			}
			if !res2Replay.Replayed || !strings.Contains(res2Replay.JSON, `"file_written":false`) || !strings.Contains(res2Replay.JSON, `"replayed":true`) {
				t.Fatalf("unexpected propose_patch replay output: %+v", res2Replay)
			}

			// 3) 1-step move -> replay while source is already gone and destination exists (and even after destination changes!).
			moveScope := f.currentScope(t, "tier5-move-00001")
			movePayload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
				Version:                   toolgateway.AgentCodeRegistryVersion,
				Action:                    "move",
				Path:                      "nested/doc.txt",
				ExpectedSHA256:            fileedit.HashText("alpha\ngamma\n"),
				DestinationPath:           "doc_v2.txt",
				DestinationExpectedSHA256: "missing",
			})
			res3, err := exec.ExecuteAgentCode(ctx, moveScope, toolgateway.WorkspaceChangeTool, movePayload)
			if err != nil {
				t.Fatalf("move failed: %v", err)
			}
			var out3 map[string]any
			_ = json.Unmarshal([]byte(res3.JSON), &out3)
			if out3["status"] != fileedit.StatusApplied || out3["file_written"] != true || out3["replayed"] != false {
				t.Fatalf("unexpected 1-step move output: %s", res3.JSON)
			}
			if _, statErr := os.Stat(filepath.Join(f.root, "nested/doc.txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("source still exists after 1-step move: %v", statErr)
			}
			moveReplayScope := f.currentScope(t, "tier5-move-00001")
			moveReplayScope.InvocationID = "inv-tier5-move-replay"
			res3Replay, err := exec.ExecuteAgentCode(ctx, moveReplayScope, toolgateway.WorkspaceChangeTool, movePayload)
			if err != nil {
				t.Fatalf("move replay failed with source gone: %v", err)
			}
			if !res3Replay.Replayed || !strings.Contains(res3Replay.JSON, `"file_written":false`) || !strings.Contains(res3Replay.JSON, `"replayed":true`) {
				t.Fatalf("unexpected move replay output: %+v", res3Replay)
			}

			// Even if destination is later modified externally, move replay must remain idempotent and never clobber disk.
			if err := os.WriteFile(filepath.Join(f.root, "doc_v2.txt"), []byte("later-edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			moveReplay2, err := exec.ExecuteAgentCode(ctx, moveReplayScope, toolgateway.WorkspaceChangeTool, movePayload)
			if err != nil {
				t.Fatalf("move replay after destination drift failed: %v", err)
			}
			if !moveReplay2.Replayed || !strings.Contains(moveReplay2.JSON, `"file_written":false`) {
				t.Fatalf("unexpected move replay after drift: %+v", moveReplay2)
			}
			if got, _ := os.ReadFile(filepath.Join(f.root, "doc_v2.txt")); string(got) != "later-edit\n" {
				t.Fatalf("move replay clobbered later edit: %q", string(got))
			}
		})
	}
}

func TestThroughputTier5_R3_ReplayApprovedProposalBeforeApplyRechecksAuthority(t *testing.T) {
	t.Run("transient_apply_failure_then_retry_succeeds", func(t *testing.T) {
		ctx := t.Context()
		failApply := true
		createPolicyCalls := 0
		checker := newTier5PolicyChecker(func(call tools.Call, d policy.Decision) policy.Decision {
			// Calls 1 & 2 happen during propose/saveAutomaticallyAuthorizedPreparedProposal;
			// call 3 happens inside FileEditApplyService.checkCurrentPolicy during autoApplyChange.
			if call.Name == "create_file" {
				createPolicyCalls++
				if failApply && createPolicyCalls >= 3 {
					d.Allowed = false
					d.Reason = "transient apply denial"
				}
			}
			return d
		})
		f := newTier5ExecutorFixture(t, domain.RunExecutionPermissionAuto, checker)
		exec := f.executor()

		scope := f.currentScope(t, "tier5-transient-apply-0001")
		payload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: "retry.txt", ExpectedSHA256: "missing", Content: "recovered\n",
		})
		if _, err := exec.ExecuteAgentCode(ctx, scope, toolgateway.WorkspaceChangeTool, payload); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
			t.Fatalf("expected transient apply denial, got err=%v", err)
		}
		editID := agentCodeEditID(scope.OperationKey)
		stored, err := f.state.GetFileEdit(ctx, editID)
		if err != nil || stored.Status != fileedit.StatusApproved {
			t.Fatalf("expected StatusApproved after propose succeeded and apply failed transiently, got %+v err=%v", stored, err)
		}
		if _, statErr := os.Stat(filepath.Join(f.root, "retry.txt")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("file should not exist before retry: %v", statErr)
		}

		// Clear transient failure and replay with same OperationKey.
		failApply = false
		retryScope := f.currentScope(t, "tier5-transient-apply-0001")
		retryScope.InvocationID = "inv-tier5-transient-retry"
		res, err := exec.ExecuteAgentCode(ctx, retryScope, toolgateway.WorkspaceChangeTool, payload)
		if err != nil {
			t.Fatalf("retry after transient apply failure failed: %v", err)
		}
		if !strings.Contains(res.JSON, `"status":"applied"`) || !strings.Contains(res.JSON, `"file_written":true`) {
			t.Fatalf("expected retry to apply file, got %s", res.JSON)
		}
		if got, _ := os.ReadFile(filepath.Join(f.root, "retry.txt")); string(got) != "recovered\n" {
			t.Fatalf("retry file content=%q", string(got))
		}
	})

	t.Run("retry_after_authority_downgrade_or_fence_revocation_blocked", func(t *testing.T) {
		for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
			t.Run(string(mode), func(t *testing.T) {
				ctx := t.Context()
				failApply := true
				createPolicyCalls := 0
				checker := newTier5PolicyChecker(func(call tools.Call, d policy.Decision) policy.Decision {
					if call.Name == "create_file" {
						createPolicyCalls++
						if failApply && createPolicyCalls >= 3 {
							d.Allowed = false
							d.Reason = "transient apply failure"
						}
					}
					return d
				})
				f := newTier5ExecutorFixture(t, mode, checker)
				exec := f.executor()

				origScope := f.currentScope(t, "tier5-revoked-retry-0001")
				payload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
					Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
					Path: "blocked.txt", ExpectedSHA256: "missing", Content: "must-not-write\n",
				})
				if _, err := exec.ExecuteAgentCode(ctx, origScope, toolgateway.WorkspaceChangeTool, payload); err == nil {
					t.Fatal("expected initial apply failure")
				}
				stored, err := f.state.GetFileEdit(ctx, agentCodeEditID(origScope.OperationKey))
				if err != nil || stored.Status != fileedit.StatusApproved {
					t.Fatalf("expected StatusApproved before downgrade, got %+v err=%v", stored, err)
				}
				failApply = false

				if mode == domain.RunExecutionPermissionFull {
					// Revoke runtime fence before retry!
					f.authority.RevokeRun(f.run.ID)
					if _, err := exec.ExecuteAgentCode(ctx, origScope, toolgateway.WorkspaceChangeTool, payload); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied {
						t.Fatalf("expected CodePolicyDenied after fence revocation, got %v", err)
					}
				} else {
					// Downgrade Auto -> Ask before retry!
					if _, err := NewRunService(f.state).Pause(ctx, f.run.ID); err != nil {
						t.Fatal(err)
					}
					if _, _, err := f.state.ReleaseRunExecutionLease(ctx, f.lease); err != nil {
						t.Fatal(err)
					}
					if _, err := NewRunExecutionPermissionService(f.state, f.caps).Change(ctx, ChangeRunExecutionPermissionRequest{
						RunID: f.run.ID, Mode: string(domain.RunExecutionPermissionAsk),
						OperationKey: "tier5-downgrade-ask-0001", RequestedBy: "test_operator", Reason: "downgrade",
					}); err != nil {
						t.Fatal(err)
					}
					if _, err := NewRunService(f.state).Resume(ctx, f.run.ID); err != nil {
						t.Fatal(err)
					}
					newLease, err := f.state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
						RunID: f.run.ID, OwnerID: "tier5-exec-2", TTL: time.Minute,
					})
					if err != nil {
						t.Fatal(err)
					}
					f.lease = newLease.Lease
					// Retry with both stale Auto scope and updated Ask scope must fail closed!
					if _, err := exec.ExecuteAgentCode(ctx, origScope, toolgateway.WorkspaceChangeTool, payload); err == nil {
						t.Fatal("stale Auto scope succeeded after downgrade to Ask")
					}
					newScope := f.currentScope(t, "tier5-revoked-retry-0001")
					if _, err := exec.ExecuteAgentCode(ctx, newScope, toolgateway.WorkspaceChangeTool, payload); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied && apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition {
						t.Fatalf("expected CodePolicyDenied or CodeFailedPrecondition on retry with Ask scope against old auto-auth, got %v", err)
					}
				}
				if _, statErr := os.Stat(filepath.Join(f.root, "blocked.txt")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("revoked authority retry wrote blocked.txt: %v", statErr)
				}
			})
		}
	})

	t.Run("manual_approved_replay_does_not_auto_apply", func(t *testing.T) {
		ctx := t.Context()
		checker := newTier5PolicyChecker(func(call tools.Call, d policy.Decision) policy.Decision {
			if call.Name == "create_file" {
				d.NeedsApproval = true
			}
			return d
		})
		f := newTier5ExecutorFixture(t, domain.RunExecutionPermissionAuto, checker)
		exec := f.executor()

		scope := f.currentScope(t, "tier5-manual-approved-0001")
		payload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: "manual.txt", ExpectedSHA256: "missing", Content: "manual\n",
		})
		proposed, err := exec.ExecuteAgentCode(ctx, scope, toolgateway.WorkspaceChangeTool, payload)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(proposed.JSON), &out)
		editID := out["edit_id"].(string)
		if _, err := NewFileEditReviewService(f.state).Review(ctx, ReviewFileEditRequest{
			Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: editID, Action: FileEditApproveIntent,
		}); err != nil {
			t.Fatal(err)
		}
		// Replay workspace_change after manual approval: must NOT auto-apply!
		replayScope := f.currentScope(t, "tier5-manual-approved-0001")
		replayScope.InvocationID = "inv-tier5-manual-replay"
		replayed, err := exec.ExecuteAgentCode(ctx, replayScope, toolgateway.WorkspaceChangeTool, payload)
		if err != nil {
			t.Fatal(err)
		}
		var replayOut map[string]any
		_ = json.Unmarshal([]byte(replayed.JSON), &replayOut)
		if !replayed.Replayed || replayOut["status"] != fileedit.StatusApproved {
			t.Fatalf("expected replayed StatusApproved without auto-apply, got %s", replayed.JSON)
		}
		if _, hasWritten := replayOut["file_written"]; hasWritten {
			t.Fatalf("manual approved replay unexpectedly reported file_written: %s", replayed.JSON)
		}
		if _, statErr := os.Stat(filepath.Join(f.root, "manual.txt")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("manual approved replay wrote file without apply: %v", statErr)
		}
	})
}

func TestThroughputTier5_R3_StandardCodeDirectPostApprovalMultiTurnEpochAndContinuation(t *testing.T) {
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
	caps := standardCodeThreadTestRuntime().ExecutionPermissionCapabilities
	agentRunner := NewAgentRunner(f.state, router, checker).
		WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(caps)

	wait := func(msg string) *llm.ChatResponse {
		raw, _ := json.Marshal(domain.RootAction{
			Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: msg, Reason: "operator review",
		})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw)}
	}
	tool := func(name string, payload any) *llm.ChatResponse {
		var raw json.RawMessage
		if text, ok := payload.(string); ok {
			raw = json.RawMessage(text)
		} else {
			raw = mustAgentCodeDrydockJSON(t, payload)
		}
		return &llm.ChatResponse{
			Provider: p.Name(), Model: "model",
			ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("sc-t5-%d", p.calls+len(p.responses)+1), Name: name, Arguments: raw}},
		}
	}

	if _, err = NewRunService(f.state).Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":"plan_delivery.v1","directions":[
 {"title":"Multi-turn direct apply","summary":"Patch twice with approval","tradeoffs":["Bounded"],"modules":[{"title":"Update tracked","objective":"Update tracked file twice","acceptance_criteria":["Updated"],"dependencies":[]}]},
 {"title":"Option B","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]},
 {"title":"Option C","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]}]}`
	p.responses = []*llm.ChatResponse{
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`),
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":2}`),
		tool("plan_delivery_propose", plan),
		wait("Choose plan"),
	}
	if _, err = agentRunner.StepWithInput(ctx, f.run.ID, "Plan updates"); err != nil {
		t.Fatal(err)
	}
	proposals, err := f.state.ListPlanDeliveryProposals(ctx, f.run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
	control := NewPlanDeliveryControlService(f.state)
	if _, err = control.SelectDirection(ctx, ControlPlanDirectionRequest{
		Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID, ProposalID: proposals[0].ID,
		Direction: 1, OperationKey: "t5-sc-select-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = control.EnterDelivery(ctx, ControlPlanDeliveryTransitionRequest{
		Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID,
		OperationKey: "t5-sc-deliver-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunService(f.state).Resume(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}

	thread, err := f.state.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := NewRunLifecycleControlService(f.state)
	execution := NewRunExecutionHandoffService(f.state, router, checker).
		WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(caps)
	turns := NewThreadTurnServiceWithExecutionCapabilities(f.state, lifecycle, execution, caps)

	// Turn 1 in Deliver: propose first edit -> Wait -> Approve -> direct Go apply -> MutationEpoch == 1.
	p.responses = []*llm.ChatResponse{
		tool("workspace_change", toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "tracked.txt",
			ExpectedSHA256: fileedit.HashText("base\n"),
			Replacements:   []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "epoch-one", ExpectedOccurrences: 1}},
		}),
		wait("Await approval 1"),
		wait("Epoch 1 applied"),
	}
	if _, err := turns.Execute(ctx, ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
		Content: "Apply first approved change", OperationKey: "tier5-sc-turn-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	edits, err := f.state.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	edit1 := edits[0]
	if _, err := NewFileEditReviewService(f.state).WithRunWorktree(f.service).Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: edit1.ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	res1 := turns.ResumeApproval(ctx, ApprovalContinuationRequest{RunID: f.run.ID, Kind: "file_edit", ProposalID: edit1.ID})
	if res1.State != "completed" || res1.ToolCalled {
		t.Fatalf("first approval continuation=%+v", res1)
	}
	snap1, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snap1.MutationEpoch != 1 || snap1.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("expected MutationEpoch=1 Execute after first direct apply, got %+v err=%v", snap1, err)
	}

	// Turn 2 in Deliver (currently in Execute state): propose_revert -> Wait -> Approve -> direct Go apply -> MutationEpoch == 2!
	p.responses = []*llm.ChatResponse{
		tool("workspace_change", toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_revert",
			SourceRunID: f.run.ID, SourceEditID: edit1.ID, Path: "tracked.txt",
			ExpectedSHA256: fileedit.HashText("epoch-one\n"),
		}),
		wait("Await approval 2"),
		wait("Epoch 2 applied"),
	}
	if _, err := turns.Execute(ctx, ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
		Content: "Revert first approved change", OperationKey: "tier5-sc-turn-0002", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	edits, err = f.state.ListFileEdits(ctx, fileedit.ListFilter{SessionID: f.run.SessionID})
	if err != nil || len(edits) != 2 {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
	var edit2 fileedit.Edit
	for _, ed := range edits {
		if ed.ID != edit1.ID {
			edit2 = ed
		}
	}
	if _, err := NewFileEditReviewService(f.state).WithRunWorktree(f.service).Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: edit2.ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	res2 := turns.ResumeApproval(ctx, ApprovalContinuationRequest{RunID: f.run.ID, Kind: "file_edit", ProposalID: edit2.ID})
	if res2.State != "completed" || res2.ToolCalled {
		t.Fatalf("second approval continuation=%+v", res2)
	}
	snap2, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snap2.MutationEpoch != 2 || snap2.State != domain.StandardCodeSupervisorExecute || snap2.StopReason != "" {
		t.Fatalf("expected MutationEpoch=2 Execute after second direct apply, got %+v err=%v", snap2, err)
	}
	if got := readDrydockTestFile(t, filepath.Join(owned.Path, "tracked.txt")); got != "base\n" {
		t.Fatalf("disk=%q want=base\\n", got)
	}

	// Cancel Run 1 and submit a successor Run 2 on the same Thread: getStandardCodeContinuation must succeed!
	if _, err := NewRunService(f.state).Cancel(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	nextRun, err := NewThreadServiceWithExecutionCapabilities(f.state, caps).
		WithRunWorktree(f.service).
		Submit(ctx, SubmitThreadMessageRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
			Content: "Verify retained epoch-two change", OperationKey: "t5-sc-successor-0001", RequestedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := f.state.GetStandardCodeContinuation(ctx, nextRun.Run.ID)
	if err != nil {
		t.Fatalf("GetStandardCodeContinuation failed after direct Go apply: %v", err)
	}
	if cont == nil || cont.MutationEpoch != 2 || cont.MutationRunID != f.run.ID || cont.MutationCheckpointID == "" {
		t.Fatalf("unexpected continuation from direct Go apply: %+v", cont)
	}

	// Start successor Run 2 and verify StandardCodeSupervisor enters Execute at MutationEpoch 2.
	if _, err := NewRunService(f.state).Start(ctx, nextRun.Run.ID); err != nil {
		t.Fatal(err)
	}
	p.responses = []*llm.ChatResponse{wait("Ready to verify continued epoch 2")}
	if _, err := agentRunner.StepWithInput(ctx, nextRun.Run.ID, "Verify continued epoch 2"); err != nil {
		t.Fatal(err)
	}
	nextSnap, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, nextRun.Run.ID)
	if err != nil || !found || nextSnap.State != domain.StandardCodeSupervisorExecute ||
		nextSnap.MutationEpoch != 2 || nextSnap.Continuation == nil ||
		nextSnap.Continuation.MutationCheckpointID != cont.MutationCheckpointID {
		t.Fatalf("successor supervisor snapshot=%+v found=%t err=%v", nextSnap, found, err)
	}
}

func TestThroughputTier5_R3_StandardCodeOneStepAutoMultiTurnContinuation(t *testing.T) {
	f, owned := newFileEditDrydockFixture(t)
	ctx := t.Context()
	caps := standardCodeThreadTestRuntime().ExecutionPermissionCapabilities
	checkpoints, err := NewWorkspaceCheckpointService(f.state, caps)
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithCheckpointService(checkpoints)
	thread, err := f.state.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Switch thread/run to Auto mode.
	if _, err := NewThreadExecutionPermissionService(f.state, caps).Change(ctx, ChangeThreadExecutionPermissionRequest{
		ThreadID: thread.ID, Mode: string(domain.RunExecutionPermissionAuto),
		OperationKey: "t5-sc-auto-perm-0001", RequestedBy: "operator", Reason: "test auto 1-step",
	}); err != nil {
		t.Fatal(err)
	}

	p := &continuationCodeProvider{}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	router.SetRoute("code", ref)
	agentRunner := NewAgentRunner(f.state, router, policy.NewDefaultChecker()).
		WithRunWorktree(f.service).
		WithExecutionPermissionCapabilities(caps)

	wait := func(msg string) *llm.ChatResponse {
		raw, _ := json.Marshal(domain.RootAction{
			Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: msg, Reason: "operator checkpoint",
		})
		return &llm.ChatResponse{Provider: p.Name(), Model: "model", Text: string(raw)}
	}
	tool := func(name string, payload any) *llm.ChatResponse {
		var raw json.RawMessage
		if text, ok := payload.(string); ok {
			raw = json.RawMessage(text)
		} else {
			raw = mustAgentCodeDrydockJSON(t, payload)
		}
		return &llm.ChatResponse{
			Provider: p.Name(), Model: "model",
			ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("sc-auto-%d", p.calls+len(p.responses)+1), Name: name, Arguments: raw}},
		}
	}

	if _, err = NewRunService(f.state).Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	plan := `{"version":"plan_delivery.v1","directions":[
 {"title":"Auto 1-step","summary":"Patch in 1 step","tradeoffs":["Fast"],"modules":[{"title":"Patch","objective":"Patch tracked","acceptance_criteria":["Patched"],"dependencies":[]}]},
 {"title":"Option B","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]},
 {"title":"Option C","summary":"Alt","tradeoffs":["Alt"],"modules":[{"title":"Alt","objective":"Alt","acceptance_criteria":["Alt"],"dependencies":[]}]}]}`
	p.responses = []*llm.ChatResponse{
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":1}`),
		tool("workspace_read", `{"version":"agent-code-tools.v1","path":"tracked.txt","start_line":1,"end_line":2}`),
		tool("plan_delivery_propose", plan),
		wait("Select plan"),
	}
	if _, err = agentRunner.StepWithInput(ctx, f.run.ID, "Plan auto 1-step"); err != nil {
		t.Fatal(err)
	}
	proposals, err := f.state.ListPlanDeliveryProposals(ctx, f.run.ID, 10)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals=%+v err=%v", proposals, err)
	}
	control := NewPlanDeliveryControlService(f.state)
	if _, err = control.SelectDirection(ctx, ControlPlanDirectionRequest{
		Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID, ProposalID: proposals[0].ID,
		Direction: 1, OperationKey: "t5-auto-select-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = control.EnterDelivery(ctx, ControlPlanDeliveryTransitionRequest{
		Version: PlanDeliveryControlProtocolVersion, RunID: f.run.ID,
		OperationKey: "t5-auto-deliver-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunService(f.state).Resume(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}

	p.responses = []*llm.ChatResponse{
		tool("workspace_change", toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "tracked.txt",
			ExpectedSHA256: fileedit.HashText("base\n"),
			Replacements:   []toolgateway.WorkspaceReplacement{{OldText: "base", NewText: "auto-one-step", ExpectedOccurrences: 1}},
		}),
		wait("Applied in 1 step"),
	}
	if _, err = agentRunner.StepWithInput(ctx, f.run.ID, "Patch tracked.txt in 1 step"); err != nil {
		t.Fatal(err)
	}
	if got := readDrydockTestFile(t, filepath.Join(owned.Path, "tracked.txt")); got != "auto-one-step\n" {
		t.Fatalf("disk=%q", got)
	}
	snap, found, err := f.state.GetStandardCodeSupervisorSnapshot(ctx, f.run.ID)
	if err != nil || !found || snap.MutationEpoch != 1 || snap.State != domain.StandardCodeSupervisorExecute {
		t.Fatalf("snapshot=%+v found=%t err=%v", snap, found, err)
	}

	// Cancel Run 1 and verify GetStandardCodeContinuation builds valid continuation from 1-step workspace_change!
	if _, err := NewRunService(f.state).Cancel(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	nextRun, err := NewThreadServiceWithExecutionCapabilities(f.state, caps).
		WithRunWorktree(f.service).
		Submit(ctx, SubmitThreadMessageRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
			Content: "Continue verification", OperationKey: "t5-auto-successor-0001", RequestedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := f.state.GetStandardCodeContinuation(ctx, nextRun.Run.ID)
	if err != nil || cont == nil || cont.MutationEpoch != 1 || cont.MutationRunID != f.run.ID || cont.MutationCheckpointID == "" {
		t.Fatalf("1-step continuation=%+v err=%v", cont, err)
	}
}

func TestThroughputTier5_R3_StandardCodeDiagnoseFixBudgetExhaustionAllModes(t *testing.T) {
	modes := []domain.RunExecutionPermissionMode{
		domain.RunExecutionPermissionAsk,
		domain.RunExecutionPermissionAuto,
		domain.RunExecutionPermissionFull,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			machine, memStore := newStandardCodeSupervisorTestMachine(
				domain.StandardCodeSupervisorCheckpoint, domain.ExecutionPhaseDeliver)
			machine.permission.Mode = mode
			standardCodeAddCheckpoint(machine, memStore, "initial-edit")
			standardCodeObserveCompleted(t, machine,
				standardCodeWorkspaceProposal(t, "initial-change"),
				map[string]string{"status": fileedit.StatusApplied, "edit_id": "initial-edit"}, "applied")

			failedExit := 1
			for attempt := 0; attempt <= domain.StandardCodeSupervisorMaximumFixRounds; attempt++ {
				cmd := standardCodeCommandCall(t, fmt.Sprintf("fail-%s-%d", mode, attempt),
					toolgateway.CommandRuntimeActionRun, fmt.Sprintf("go test ./... #%d", attempt), "", 0)
				standardCodeObserveCompleted(t, machine, cmd, nil,
					standardCodeCommandOutput(t, toolgateway.CommandRuntimeActionRun,
						[]runner.CommandRuntimeJobSnapshot{{
							ID:           fmt.Sprintf("job-%s-%d", mode, attempt),
							State:        runner.CommandRuntimeJobFailed,
							ExitCode:     &failedExit,
							TreeReaped:   true,
							StderrSHA256: runmutation.Fingerprint("tier5-fail", string(mode), fmt.Sprint(attempt)),
						}}, nil))
				if machine.snapshot.State != domain.StandardCodeSupervisorDiagnose {
					t.Fatalf("mode=%s attempt=%d left Diagnose: %s", mode, attempt, machine.snapshot.State)
				}
				if attempt == domain.StandardCodeSupervisorMaximumFixRounds {
					break
				}
				fixID := fmt.Sprintf("fix-%s-%d", mode, attempt)
				standardCodeAddCheckpoint(machine, memStore, fixID)
				standardCodeObserveCompleted(t, machine,
					standardCodeWorkspaceProposal(t, fmt.Sprintf("fix-proposal-%s-%d", mode, attempt)),
					map[string]string{"status": fileedit.StatusApplied, "edit_id": fixID}, "fixed")
				if machine.snapshot.FixRounds != attempt+1 {
					t.Fatalf("mode=%s expected FixRounds=%d, got %d", mode, attempt+1, machine.snapshot.FixRounds)
				}
			}
			decision, err := machine.Authorize(context.Background(),
				standardCodeWorkspaceProposal(t, "exhausted-"+string(mode)))
			if err != nil || decision.Allowed || decision.Result == nil ||
				machine.snapshot.State != domain.StandardCodeSupervisorStopped ||
				machine.snapshot.StopReason != "fix_round_budget_exhausted" {
				t.Fatalf("mode=%s fix-round exhaustion did not block workspace_change: decision=%+v snapshot=%+v err=%v",
					mode, decision, machine.snapshot, err)
			}
		})
	}
}

func TestThroughputTier5_R3_RunProgressGuardCountsOneStepWorkspaceChangeAsProgress(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "progress-1step.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws := store.WorkspaceRecord{ID: "ws-progress-1step", Name: "progress-1step", RootPath: root, CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	provider := &continuationCodeProvider{}
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(provider)

	_, created, err := NewRunService(st).Create(ctx, CreateRunRequest{
		Goal: "Multi-turn 1-step workspace writes", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: ws.ID, ModelRoute: provider.Name() + "/model", Interactive: false,
		Budget: domain.Budget{MaxTurns: 12, MaxToolCalls: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionPermissionService(st, caps).Change(ctx, ChangeRunExecutionPermissionRequest{
		RunID: created.ID, Mode: string(domain.RunExecutionPermissionAuto),
		OperationKey: "tier5-progress-perm-0001", RequestedBy: "operator", Reason: "auto 1-step",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunService(st).Start(ctx, created.ID); err != nil {
		t.Fatal(err)
	}

	// Execute RunProgressStagnantThreshold + 1 turns; each turn writes a distinct new file via 1-step workspace_change
	// and requests RootActionContinue. Real durable file writes MUST count as observable progress in RunProgressGuard!
	totalWriteTurns := domain.RunProgressStagnantThreshold + 1
	for i := 0; i < totalWriteTurns; i++ {
		payload := mustAgentCodeDrydockJSON(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: fmt.Sprintf("step_%d.txt", i), ExpectedSHA256: "missing", Content: fmt.Sprintf("content %d\n", i),
		})
		provider.responses = append(provider.responses,
			&llm.ChatResponse{
				Provider: provider.Name(), Model: "model",
				ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("w-%d", i), Name: "workspace_change", Arguments: payload}},
			},
			&llm.ChatResponse{
				Provider: provider.Name(), Model: "model",
				Text: fmt.Sprintf(`{"version":"root_lifecycle.v1","action":"continue","message":"Completed write step %d"}`, i),
			},
		)
	}
	provider.responses = append(provider.responses, &llm.ChatResponse{
		Provider: provider.Name(), Model: "model",
		Text: `{"version":"root_lifecycle.v1","action":"finish","message":"All 1-step writes complete","summary":"done"}`,
	})

	agentRunner := NewAgentRunner(st, router, policy.NewDefaultChecker()).
		WithExecutionPermissionCapabilities(caps).
		WithGeneratedContextCompaction(false)
	execRes, err := agentRunner.Execute(ctx, created.ID, totalWriteTurns+2)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if execRes.RunStatus != domain.RunCompleted {
		guard, _, _ := st.GetRunProgressGuard(ctx, created.ID)
		t.Fatalf("1-step workspace_change writes falsely triggered RunProgressGuard pause/stagnation: runStatus=%s stopReason=%q guard=%+v",
			execRes.RunStatus, execRes.StopReason, guard)
	}
}
