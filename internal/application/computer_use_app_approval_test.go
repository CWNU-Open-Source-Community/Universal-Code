package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

// The peer exercises the actual host authorizer without starting a desktop
// service. All Run, actor, permission, lease and approval state is real SQLite.
type computerUseApprovalPeer struct {
	*mcp.Manager
	f           *mcpApprovalFixture
	beforeGuard func()
}

func (p *computerUseApprovalPeer) RefreshCapabilities(ctx context.Context, runID, workspaceID string, check func(context.Context) error, _ <-chan struct{}) (mcp.ScopedCapabilities, error) {
	if err := check(ctx); err != nil {
		return mcp.ScopedCapabilities{}, err
	}
	return p.Manager.Capabilities(ctx, runID, workspaceID)
}

func (p *computerUseApprovalPeer) Invoke(ctx context.Context, request mcp.InvokeRequest) (mcp.ClientCallResult, error) {
	if p.beforeGuard != nil {
		p.beforeGuard()
	}
	operation := toolcontract.Operation{ID: request.OperationID, Kind: toolcontract.OperationToolCall,
		ToolID: mcp.OperationApprovalTool, AdapterID: mcp.RuntimeAdapterID, AdapterRevision: mcp.RuntimeAdapterRevision,
		Component:        toolcontract.ComponentRef{PackageID: mcp.ClientProtocolVersion, ComponentID: request.ServerID},
		InputFingerprint: approval.Fingerprint(request.ToolName, string(request.Arguments)), CapabilityFingerprint: request.CapabilityFingerprint,
		Targets: []toolcontract.Target{{Kind: "process", Locator: "test-peer"}}, Effects: []toolcontract.Effect{toolcontract.EffectUnknown}}
	decision, err := request.Authorizer.Authorize(ctx, request.Subject, operation, "")
	if err != nil {
		return mcp.ClientCallResult{}, err
	}
	if decision.Outcome != "allow" || decision.BeforeDispatch == nil {
		return mcp.ClientCallResult{}, errors.New("host authorizer did not permit the operation")
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return mcp.ClientCallResult{}, err
	}
	if err := decision.BeforeDispatch(ctx, fingerprint); err != nil {
		return mcp.ClientCallResult{}, err
	}
	p.f.calls.Add(1)
	return mcp.ClientCallResult{Content: `{"content":[]}`}, nil
}

func testComputerUseAppID(path, fileID string) string {
	return "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(path)) + ":" + strings.Repeat(fileID, 64)
}

type computerUseApprovalCall struct {
	name string
	args map[string]any
}

func computerUseObserve(app string) computerUseApprovalCall {
	return computerUseObserveWindow(app, "window-1")
}

func computerUseObserveWindow(app, window string) computerUseApprovalCall {
	return computerUseApprovalCall{"computer_observe", map[string]any{"app_id": app, "window_id": window}}
}

func newComputerUseApprovalFixture(t *testing.T, mode domain.RunExecutionPermissionMode, thirdParty bool, calls ...computerUseApprovalCall) (*mcpApprovalFixture, *computerUseApprovalPeer) {
	return newComputerUseApprovalFixtureInStore(t, nil, mode, thirdParty, calls...)
}

func newComputerUseApprovalFixtureInStore(t *testing.T, previous *mcpApprovalFixture, mode domain.RunExecutionPermissionMode, thirdParty bool, calls ...computerUseApprovalCall) (*mcpApprovalFixture, *computerUseApprovalPeer) {
	t.Helper()
	f := &mcpApprovalFixture{checker: &mcpReviewChecker{Checker: policy.NewDefaultChecker()}}
	var err error
	workspace := store.WorkspaceRecord{ID: "computer-use-workspace", Name: "Computer Use approval", RootPath: t.TempDir()}
	if previous == nil {
		f.st, err = store.Open(filepath.Join(t.TempDir(), "computer-use-approval.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.st.Close() })
		if err := f.st.SaveWorkspace(t.Context(), workspace); err != nil {
			t.Fatal(err)
		}
	} else {
		f.st = previous.st
	}
	runs := NewRunService(f.st)
	_, run, err := runs.Create(t.Context(), CreateRunRequest{Goal: "Use the selected application", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	f.capabilities = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if previous != nil {
		f.capabilities = previous.capabilities
	}
	if mode != domain.RunExecutionPermissionAsk {
		_, err = NewRunExecutionPermissionService(f.st, f.capabilities).Change(t.Context(), ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(mode), ConfirmFull: mode == domain.RunExecutionPermissionFull,
			OperationKey: "computer-use-mode-0001", RequestedBy: "test_operator", Reason: "exercise host permission"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "computer-use-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(t.Context(), lease.Lease, "")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.manager, err = mcp.NewClientManager(f.st, &mcpRuntimeCredentialFixture{}, mcp.ManagerOptions{ComputerUseExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.EnsureComputerUse(t.Context(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	f.server, err = f.st.GetMCPClientServer(t.Context(), mcp.ComputerUseServerID(workspace.ID))
	if err != nil {
		t.Fatal(err)
	}
	if thirdParty {
		// A manual server advertises identical tools and arguments. Its catalog
		// must never acquire the built-in application's authorization semantics.
		d := f.server.Descriptor
		d.ID, d.Name, d.Target, d.Source = "third-party-computer-tools", "Third-party computer tools", executable, mcp.Source{Kind: "manual", URI: "operator://third-party"}
		now := time.Now().UTC()
		staged, _, err := f.st.CreateMCPClientServer(t.Context(), mcp.ServerRecord{ProtocolVersion: mcp.ServerRecordProtocolVersion,
			Descriptor: d, DescriptorFingerprint: d.Fingerprint(), State: mcp.TrustStaged, Health: mcp.HealthUnknown, Generation: 1, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		staged.Capabilities, staged.ApprovedCapabilityFingerprint = f.server.Capabilities, f.server.Capabilities.Fingerprint
		staged.State, staged.Health, staged.Generation, staged.ReviewedAt, staged.ReviewedBy = mcp.TrustEnabled, mcp.HealthHealthy, 2, &now, "operator"
		f.server, err = f.st.UpdateMCPClientServer(t.Context(), staged, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	peer := &computerUseApprovalPeer{Manager: f.manager, f: f}
	f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(peer)
	permission, err := f.st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	advertisement, err := f.supervisor.supervisorMCPCapabilities(t.Context(), f.turn, permission)
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls []llm.ToolCall
	for i, call := range calls {
		arguments, _ := json.Marshal(call.args)
		payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion,
			ServerID: f.server.Descriptor.ID, ToolName: call.name, CapabilityFingerprint: f.server.ApprovedCapabilityFingerprint, Arguments: arguments})
		modelCalls = append(modelCalls, llm.ToolCall{ID: fmt.Sprintf("computer-provider-%d", i), Name: mcp.OperationApprovalTool, Arguments: payload})
	}
	prepared, err := prepareSupervisorToolCalls(modelCalls, run.ID, f.turn.Checkpoint.NextTurn, 1, f.turn.Mode.Surface, f.turn.Mode.Phase,
		mode, false, false, supervisorToolOptions{MCP: advertisement})
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-computer-use", Model: "fixture"}
	if _, err := f.st.RecordSupervisorModelStarted(t.Context(), f.turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	f.turn.Checkpoint, err = f.st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, attempt, llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, ToolCalls: prepared})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != len(calls) {
		t.Fatal("missing durable Computer Use calls", err)
	}
	f.call = rounds[0].Calls[0]
	return f, peer
}

func TestComputerUseAskApplicationApprovalContinuesOnlySameApp(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	other := testComputerUseAppID(`c:\windows\other.exe`, "b")
	f, _ := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false,
		computerUseObserve(app), computerUseApprovalCall{"computer_click", map[string]any{"app_id": app, "window_id": "window-1", "observation_id": "observation-1", "x": 4, "y": 8}}, computerUseObserve(other))
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 0 {
		t.Fatal("screenshot ran before application approval", waiting, err)
	}
	record, _ := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
	target, err := MCPComputerUseApprovalPreview(t.Context(), f.st, record)
	if err != nil || target == nil || target.AppID != app || target.Executable != `c:\windows\notepad.exe` ||
		!slices.Contains(MCPApprovalDecisionActions(t.Context(), f.st, record, false), ApprovalControlApproveForRunApp) {
		t.Fatalf("incorrect application approval preview: %+v %v", target, err)
	}
	if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal(err)
	}
	if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal("exact application decision replay failed", err)
	}
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 2 {
		t.Fatal("same application did not continue or different application escaped review", waiting, f.calls.Load(), err)
	}
	rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	first, _ := f.st.GetApprovalByProposal(t.Context(), rounds[0].Calls[0].CallID)
	second, _ := f.st.GetApprovalByProposal(t.Context(), rounds[0].Calls[1].CallID)
	third, _ := f.st.GetApprovalByProposal(t.Context(), rounds[0].Calls[2].CallID)
	if first.GrantID == "" || second.GrantID != first.GrantID || second.Status != approval.StatusApproved || third.Status != approval.StatusPending || third.GrantID != "" {
		t.Fatal("application grant lost exact call attribution", first, second, third)
	}
	if _, err := f.st.AuthorizeApprovalWithSessionGrant(t.Context(), third.ProposalID, first.GrantID); err == nil {
		t.Fatal("generic session grant API bypassed the application verifier")
	}
}

func TestComputerUseOnceAutoFullAndThirdPartySemantics(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	for _, scenario := range []string{"ask_once", "auto", "full", "third_party"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			mode := domain.RunExecutionPermissionAsk
			if scenario == "auto" {
				mode = domain.RunExecutionPermissionAuto
			}
			if scenario == "full" {
				mode = domain.RunExecutionPermissionFull
			}
			f, _ := newComputerUseApprovalFixture(t, mode, scenario == "third_party", computerUseObserve(app), computerUseObserveWindow(app, "window-2"))
			waiting, err := f.resume(t)
			if scenario == "full" {
				if err != nil || waiting || f.calls.Load() != 2 {
					t.Fatal("Full did not dispatch directly", waiting, err)
				}
				return
			}
			if err != nil || !waiting || f.calls.Load() != 0 {
				t.Fatal("initial approval missing", waiting, err)
			}
			record, _ := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
			if scenario != "ask_once" {
				if slices.Contains(MCPApprovalDecisionActions(t.Context(), f.st, record, false), ApprovalControlApproveForRunApp) {
					t.Fatal("app scope offered outside built-in Ask")
				}
				if err := f.decide(t, ApprovalControlApproveForRunApp); err == nil {
					t.Fatal("app grant established outside built-in Ask")
				}
			}
			if err := f.decide(t, ApprovalControlApproveOnce); err != nil {
				t.Fatal(err)
			}
			if waiting, err = f.resume(t); err != nil || !waiting || f.calls.Load() != 1 {
				t.Fatal("once approval broadened to another call", waiting, f.calls.Load(), err)
			}
		})
	}
}

func TestComputerUseMetadataDoesNotAuthorizeScreenshots(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			f, _ := newComputerUseApprovalFixture(t, mode, false, computerUseApprovalCall{"computer_list_windows", map[string]any{}}, computerUseObserve(app))
			if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 1 {
				t.Fatal("window metadata or screenshot authorization incorrect", waiting, f.calls.Load(), err)
			}
			record, _ := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
			if record.Status != approval.StatusApproved || record.ReviewedBy != "computer_use_metadata" || record.GrantID != "" {
				t.Fatal("metadata acquired application authority", record)
			}
		})
	}
}

func TestComputerUseApplicationGrantPinsExecutableFileIdentity(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	replacement := testComputerUseAppID(`c:\windows\notepad.exe`, "b")
	f, _ := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false, computerUseObserve(app), computerUseObserve(replacement))
	if waiting, err := f.resume(t); err != nil || !waiting {
		t.Fatal("initial review missing", err)
	}
	if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 1 {
		t.Fatal("replacement executable inherited application grant", waiting, f.calls.Load(), err)
	}
}

func TestComputerUseApplicationGrantDoesNotCrossRuns(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	first, _ := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false, computerUseObserve(app))
	if waiting, err := first.resume(t); err != nil || !waiting {
		t.Fatal("initial review missing", err)
	}
	if err := first.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal(err)
	}
	second, _ := newComputerUseApprovalFixtureInStore(t, first, domain.RunExecutionPermissionAsk, false, computerUseObserve(app))
	if waiting, err := second.resume(t); err != nil || !waiting || second.calls.Load() != 0 {
		t.Fatal("application access escaped its original Run", waiting, err)
	}
	approval, err := second.st.GetApprovalByProposal(t.Context(), second.call.CallID)
	if err != nil || approval.GrantID != "" {
		t.Fatal("successor Run acquired the old application grant", approval, err)
	}
}

func TestComputerUseApplicationGrantRechecksRulesAndRevocation(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	for _, scenario := range []string{"force_review", "deny", "revoke_grant", "revoke_run", "cold_epoch", "disable_source", "dispatch_revoke", "mode_change_request", "run_end"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, peer := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false, computerUseObserve(app), computerUseObserveWindow(app, "window-2"))
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("initial review missing", err)
			}
			if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "force_review":
				f.checker.require = true
			case "deny":
				f.checker.deny = true
			case "revoke_grant":
				record, _ := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
				if _, err := f.st.RevokeSessionGrant(t.Context(), approval.RevokeGrantRequest{GrantID: record.GrantID, Reason: "operator revoked application access", RevokedBy: "operator", IdempotencyKey: "revoke-app-0001"}); err != nil {
					t.Fatal(err)
				}
			case "revoke_run":
				f.capabilities.RuntimeAuthority.RevokeRun(f.call.RunID)
			case "cold_epoch":
				f.capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
				f.supervisor.WithExecutionPermissionCapabilities(f.capabilities)
			case "disable_source":
				if _, err := f.manager.Review(t.Context(), f.server.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			case "dispatch_revoke":
				peer.beforeGuard = func() { f.capabilities.RuntimeAuthority.RevokeRun(f.call.RunID) }
			case "mode_change_request":
				if _, err := NewRunService(f.st).Pause(t.Context(), f.call.RunID); err != nil {
					t.Fatal(err)
				}
				_, err := NewRunExecutionPermissionService(f.st, f.capabilities).Change(t.Context(), ChangeRunExecutionPermissionRequest{
					RunID: f.call.RunID, Mode: string(domain.RunExecutionPermissionAuto), OperationKey: "change-app-mode-0001", RequestedBy: "operator", Reason: "change approval mode"})
				// Pending native work prevents escalation. The real permission
				// service still revokes its old Run fence before rejecting it.
				if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
					t.Fatal("pending Run permission boundary changed", err)
				}
				if _, live := f.capabilities.RuntimeAuthority.RunAuthorizationFence(f.call.RunID); live {
					t.Fatal("permission change request kept the old app authority live")
				}
				waiting, result, err := f.supervisor.preflightMCPApproval(t.Context(), f.call)
				if err != nil || waiting || result == nil || result.ErrorCode != "mcp_authority_expired" {
					t.Fatal("permission change request failed to fence the app grant", result, err)
				}
				return
			case "run_end":
				if _, err := NewRunService(f.st).Cancel(t.Context(), f.call.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "run_end" {
				if err := f.st.CheckMCPComputerUseAppGrant(t.Context(), f.call.CallID); err == nil {
					t.Fatal("expired application grant remained usable")
				}
				return
			}
			waiting, err := f.resume(t)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "force_review" {
				if !waiting || f.calls.Load() != 1 {
					t.Fatal("explicit rule was bypassed by app grant", waiting, f.calls.Load())
				}
			} else if f.calls.Load() != 0 {
				t.Fatal("revoked or denied application reached native peer", scenario, f.calls.Load())
			}
		})
	}
}
