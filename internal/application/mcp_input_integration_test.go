package application

import (
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMCPInputApprovalContinuesSameDurableCall(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newMCPOperationApprovalFixture(t, mode, false, false, "", "interactive")
			f.response.Store(json.RawMessage(`{"resultType":"input_required","requestState":"private-state","inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"Enter project label","requestedSchema":{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}}}}}`))
			f.onRequest = func(method string) {
				if method == "tools/call" && f.calls.Load() == 1 {
					f.response.Store(json.RawMessage(`{"resultType":"complete","content":[{"type":"text","text":"private-operator-label"}]}`))
				}
			}
			if mode != domain.RunExecutionPermissionFull {
				if waiting, err := f.resume(t); err != nil || !waiting || f.calls.Load() != 0 {
					t.Fatal("approval bypass", err)
				}
				if err := f.decide(t, ApprovalControlApproveOnce); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				waiting, err := f.resume(t)
				if waiting && err == nil {
					err = errors.New("call unexpectedly waiting for a second approval")
				}
				done <- err
			}()
			var item mcp.InputView
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				items, err := f.manager.InputBroker().List(t.Context(), f.call.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					item = items[0]
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if item.ID == "" {
				select {
				case err := <-done:
					t.Fatal("no input request", err)
				default:
					t.Fatal("input request not published")
				}
			}
			call, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || !started || call.Status != domain.SupervisorToolPending {
				t.Fatal("original durable call did not remain live", err)
			}
			if err = f.manager.InputBroker().Respond(t.Context(), f.call.RunID, item.ID, mcp.InputResponse{Version: mcp.InputProtocolVersion, Action: "accept", Content: json.RawMessage(`{"label":"private-operator-label"}`)}); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			call, _, err = f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || call.Status != domain.SupervisorToolCompleted || f.calls.Load() != 2 || strings.Contains(call.ResultJSON, "private-operator-label") || strings.Contains(call.PayloadJSON, "private-state") {
				t.Fatalf("continuation persistence: %+v %v calls=%d", call, err, f.calls.Load())
			}
			if _, err = f.resume(t); err != nil || f.calls.Load() != 2 {
				t.Fatal("completed interaction replayed", err)
			}
		})
	}
}
