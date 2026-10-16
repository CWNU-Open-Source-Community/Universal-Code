package application

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func computerUseKey(app, key string) computerUseApprovalCall {
	return computerUseApprovalCall{"computer_key", map[string]any{
		"app_id": app, "window_id": "window-1", "observation_id": "observation-1", "key": key}}
}

func decideComputerUseCall(t *testing.T, f *mcpApprovalFixture, call domain.SupervisorToolCall, action ApprovalControlAction, key string) error {
	t.Helper()
	record, err := f.st.GetApprovalByProposal(t.Context(), call.CallID)
	if err != nil {
		return err
	}
	_, err = NewApprovalControlService(f.st, toolgateway.New(nil, f.checker), f.checker).Decide(t.Context(), DecideApprovalControlRequest{
		Version: ApprovalControlProtocolVersion, RunID: call.RunID, ApprovalID: record.ID, Action: action,
		OperationKey: key, ReviewedBy: "operator"})
	return err
}

func TestComputerUseAppGrantKeepsSystemShortcutPendingAndAllowsOrdinaryKeys(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	f, _ := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false,
		computerUseObserve(app), computerUseKey(app, "CTRL+A"), computerUseKey(app, "WIN+D"))
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 0 {
		t.Fatal("initial application review missing", waiting, err)
	}
	if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 2 {
		t.Fatal("ordinary key did not continue or system shortcut escaped exact review", waiting, f.calls.Load(), err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _ := f.st.GetApprovalByProposal(t.Context(), rounds[0].Calls[1].CallID)
	shortcut := rounds[0].Calls[2]
	record, _ := f.st.GetApprovalByProposal(t.Context(), shortcut.CallID)
	if ordinary.Status != approval.StatusApproved || ordinary.GrantID == "" || record.Status != approval.StatusPending || record.GrantID != "" {
		t.Fatal("shortcut inherited application approval", ordinary, record)
	}
	target, err := MCPComputerUseApprovalPreview(t.Context(), f.st, record)
	if err != nil || target == nil || !target.SystemShortcut || target.AppID != app {
		t.Fatal("system shortcut preview lost its exact target", target, err)
	}
	actions := MCPApprovalDecisionActions(t.Context(), f.st, record, false)
	if !slices.Equal(actions, []ApprovalControlAction{ApprovalControlApproveOnce, ApprovalControlDeny}) {
		t.Fatal("system shortcut advertised application scope", actions)
	}
	if err := decideComputerUseCall(t, f, shortcut, ApprovalControlApproveForRunApp, "shortcut-app-decision-0001"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatal("controller permitted app scope for a system shortcut", err)
	}
	if _, _, _, err := f.st.DecideMCPComputerUseAppApproval(t.Context(), approval.DecisionRequest{
		ProposalID: shortcut.CallID, IdempotencyKey: "shortcut-direct-app-0001", Action: approval.ActionApprove, ReviewedBy: "operator"}); err == nil {
		t.Fatal("direct store review created an app grant for a system shortcut")
	}
	if _, found, err := f.st.TryAuthorizeMCPComputerUseAppApproval(t.Context(), shortcut.CallID); err == nil || found {
		t.Fatal("direct store consumption inherited the app grant", found, err)
	}
	if err := decideComputerUseCall(t, f, shortcut, ApprovalControlApproveOnce, "shortcut-once-decision-0001"); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 3 {
		t.Fatal("exact shortcut approval did not dispatch once", waiting, f.calls.Load(), err)
	}
}

type computerUseForgedApprovalStore struct {
	*store.SQLiteStore
	forged approval.Record
}

func (s *computerUseForgedApprovalStore) GetApprovalByProposal(ctx context.Context, proposalID string) (approval.Record, error) {
	if proposalID == s.forged.ProposalID {
		return s.forged, nil
	}
	return s.SQLiteStore.GetApprovalByProposal(ctx, proposalID)
}

func TestComputerUseSystemShortcutDispatchRejectsForgedApplicationApproval(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	f, peer := newComputerUseApprovalFixture(t, domain.RunExecutionPermissionAsk, false,
		computerUseObserve(app), computerUseKey(app, "WIN+D"))
	if waiting, err := f.resume(t); err != nil || !waiting {
		t.Fatal("initial application review missing", err)
	}
	if err := f.decide(t, ApprovalControlApproveForRunApp); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 1 {
		t.Fatal("system shortcut did not wait for exact review", waiting, err)
	}
	origin, _ := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
	rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	f.call = rounds[0].Calls[1]
	forged, err := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	forged.Status, forged.GrantID, forged.ReviewedBy = approval.StatusApproved, origin.GrantID, "session_grant"
	forged.Version++
	forged.UpdatedAt, forged.DecidedAt = now, &now
	if err := forged.Validate(); err != nil {
		t.Fatal("forgery must reach the semantic dispatch guard", err)
	}
	if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
		t.Fatal(err)
	}
	base := &computerUseForgedApprovalStore{SQLiteStore: f.st, forged: forged}
	executor, err := NewMCPClientToolExecutor(peer, base, f.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	scope, payload := f.dispatchScope(t)
	scope.PolicyDecision.Approval = toolgateway.ApprovalAutomatic
	if _, err := executor.ExecuteMCP(t.Context(), scope, payload); err == nil || !strings.Contains(err.Error(), "MCP dispatch approval identity changed") || f.calls.Load() != 1 {
		t.Fatal("forged app approval bypassed actual shortcut dispatch guard", f.calls.Load(), err)
	}
}

func TestComputerUseSystemShortcutsPreserveFullAndAutoModes(t *testing.T) {
	app := testComputerUseAppID(`c:\windows\notepad.exe`, "a")
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionFull, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			f, _ := newComputerUseApprovalFixture(t, mode, false, computerUseKey(app, "WIN+D"))
			waiting, err := f.resume(t)
			if mode == domain.RunExecutionPermissionFull {
				if err != nil || waiting || f.calls.Load() != 1 {
					t.Fatal("Full shortcut no longer executes directly", waiting, f.calls.Load(), err)
				}
				if _, err := f.st.GetApprovalByProposal(t.Context(), f.call.CallID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("Full shortcut fabricated an approval", err)
				}
				return
			}
			if err != nil || !waiting || f.calls.Load() != 0 {
				t.Fatal("Auto shortcut bypassed its existing exact review", waiting, f.calls.Load(), err)
			}
			if err := decideComputerUseCall(t, f, f.call, ApprovalControlApproveOnce, "auto-shortcut-once-0001"); err != nil {
				t.Fatal(err)
			}
			if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
				t.Fatal("Auto exact shortcut approval changed", waiting, f.calls.Load(), err)
			}
		})
	}
}
