package store

import (
	"context"
	"testing"

	"cyberagent-workbench/internal/artifact"
)

func TestWorkspaceImageArtifactRetainsExactInvocationScope(t *testing.T) {
	st := openRunArtifactTestStore(t)
	ctx := context.Background()
	_, run := createRunArtifactTestRun(t, ctx, st, "workspace image metadata artifact")
	const invocationID = "image-invocation"
	if _, err := st.db.ExecContext(ctx, `INSERT INTO run_tool_calls
		(id,run_id,session_id,workspace_id,tool_name,action_class,sequence,created_at)
		VALUES (?,?,?,?,?,?,1,?)`, invocationID, run.ID, run.SessionID, "ws-artifact",
		"workspace_view_image", "workspace_read", ts(run.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	request := artifact.CaptureRequest{RunID: run.ID, SessionID: run.SessionID,
		WorkspaceID: "ws-artifact", SourceID: invocationID, ToolName: "workspace_view_image",
		Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "application/json",
			Content: `{"path":"output/preview.png"}`}}}
	descriptors, err := st.CaptureToolOutput(ctx, request)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("image metadata artifact=%+v err=%v", descriptors, err)
	}
	blob, err := st.GetRunArtifact(ctx, descriptors[0].ID)
	if err != nil || blob.SourceID != invocationID || blob.ToolName != request.ToolName {
		t.Fatalf("image artifact binding=%+v err=%v", blob, err)
	}
	request.SessionID = "other-session"
	if _, err := st.CaptureToolOutput(ctx, request); err == nil {
		t.Fatal("cross-session image artifact capture was accepted")
	}
}
