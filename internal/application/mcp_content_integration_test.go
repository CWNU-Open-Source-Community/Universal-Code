package application

import (
	"cyberagent-workbench/internal/domain"
	"strings"
	"testing"
)

func TestMCPContentApprovalAndDurableRecovery(t *testing.T) {
	for _, method := range []string{"resources/read", "prompts/get"} {
		for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
			t.Run(method+"/"+string(mode), func(t *testing.T) {
				f := newMCPOperationApprovalFixture(t, mode, false, false, method)
				waiting, err := f.resume(t)
				if mode != domain.RunExecutionPermissionFull {
					if err != nil || !waiting || f.requests.Load() != 0 {
						t.Fatalf("approval bypass: %v %v", waiting, err)
					}
					if err = f.decide(t, ApprovalControlApproveOnce); err != nil {
						t.Fatal(err)
					}
					waiting, err = f.resume(t)
				}
				if err != nil || waiting || f.calls.Load() != 1 {
					t.Fatalf("content dispatch: %v %v calls=%d", waiting, err, f.calls.Load())
				}
				call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
				if err != nil || call.Status != domain.SupervisorToolCompleted || !strings.Contains(call.ResultJSON, "reviewed") {
					t.Fatalf("content receipt: %+v %v", call, err)
				}
				if waiting, err = f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
					t.Fatal("completed content replayed", err)
				}
			})
		}
		t.Run(method+"/lost_receipt", func(t *testing.T) {
			f := newMCPOperationApprovalFixture(t, domain.RunExecutionPermissionAsk, false, false, method)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing approval", err)
			}
			if err := f.decide(t, ApprovalControlApproveOnce); err != nil {
				t.Fatal(err)
			}
			fault := &mcpReceiptFaultStore{SQLiteStore: f.st, fail: true}
			f.supervisor = NewAgentRunner(fault, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
			if _, err := f.resume(t); err == nil || f.calls.Load() != 1 {
				t.Fatal("missing receipt fault", err)
			}
			f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
			for i := 0; i < 2; i++ {
				if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
					t.Fatal("uncertain content replayed", err)
				}
			}
			call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || call.ErrorCode != "outcome_unknown" || !strings.Contains(call.ResultJSON, `"automatic_retry":"forbidden"`) {
				t.Fatalf("missing uncertain receipt: %+v %v", call, err)
			}
		})
	}
}
