package store

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolbudget"
)

func TestWorkerArtifactsRequireExactStartedChildInvocation(t *testing.T) {
	for _, native := range []llm.ToolCall{
		{Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"proof.txt","start_line":1,"end_line":2}`)},
		{Name: "mcp_tool_call", Arguments: json.RawMessage(`{"version":"mcp-client.v1","server_id":"server","tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) + `","arguments":{}}`)},
		{Name: "browser_snapshot", Arguments: json.RawMessage(`{"version":"browser_snapshot.v2"}`)},
	} {
		t.Run(native.Name, func(t *testing.T) {
			ctx := t.Context()
			st := openWorkItemTestStore(t)
			run, child, attempt, binding, call, lease := workerToolFixture(t, st, &domain.WorkerProfile{}, native)
			call, err := st.BindSpecialistToolAuthority(ctx, call.Ref, call.CallID, `{"issued_by":"host"}`)
			if err != nil {
				t.Fatal(err)
			}
			charge, err := st.ChargeToolCall(ctx, toolbudget.ChargeRequest{RunID: run.ID, AgentID: child.ID, AgentAttemptID: attempt.ID, ToolCallID: call.CallID, SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID, ToolName: call.Name, ActionClass: "workspace_read", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "specialist_runner"})
			if err != nil {
				t.Fatal(err)
			}
			request := artifact.CaptureRequest{RunID: run.ID, SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID, SourceID: charge.LastCharge, ToolName: call.Name, AgentID: child.ID, AgentAttemptID: attempt.ID, SpecialistToolCallID: call.CallID, InvocationID: charge.LastCharge, Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "application/json", Content: `{"result":"child evidence"}`}}}
			if _, err = st.CaptureToolOutput(ctx, request); err == nil {
				t.Fatal("unstarted child call captured an artifact")
			}
			if _, err = st.MarkSpecialistToolStarted(ctx, call.Ref, call.CallID, call.AuthorityJSON); err != nil {
				t.Fatal(err)
			}
			captured, err := st.CaptureToolOutput(ctx, request)
			if err != nil || len(captured) != 1 || captured[0].SessionID != child.SessionID {
				t.Fatalf("child output capture: %+v %v", captured, err)
			}
			replayed, err := st.CaptureToolOutput(ctx, request)
			if err != nil || len(replayed) != 1 || replayed[0].ID != captured[0].ID {
				t.Fatalf("child output replay: %+v %v", replayed, err)
			}
			for _, corrupt := range []func(*artifact.CaptureRequest){
				func(r *artifact.CaptureRequest) { r.SessionID = run.SessionID },
				func(r *artifact.CaptureRequest) { r.AgentID = child.ParentID },
				func(r *artifact.CaptureRequest) { r.SpecialistToolCallID = "another-call" },
				func(r *artifact.CaptureRequest) { r.InvocationID = "another-charge" },
				func(r *artifact.CaptureRequest) { r.SourceID = "another-source" },
				func(r *artifact.CaptureRequest) { r.ToolName = "workspace_list" },
				func(r *artifact.CaptureRequest) {
					r.AgentID, r.AgentAttemptID, r.SpecialistToolCallID, r.InvocationID = "", "", "", ""
				},
			} {
				forged := request
				corrupt(&forged)
				if _, err = st.CaptureToolOutput(ctx, forged); err == nil {
					t.Fatalf("forged child artifact accepted: %+v", forged)
				}
			}
			if _, _, err = st.ReleaseRunExecutionLease(ctx, lease); err != nil {
				t.Fatal(err)
			}
			if _, err = st.CaptureToolOutput(ctx, request); err == nil {
				t.Fatal("child artifact captured after its execution lease ended")
			}
		})
	}
}
