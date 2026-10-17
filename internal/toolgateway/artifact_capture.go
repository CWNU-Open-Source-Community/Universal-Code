package toolgateway

import (
	"context"
	"errors"
	"strconv"

	"cyberagent-workbench/internal/artifact"
)

func (g *Gateway) Artifacts() *artifact.Manager {
	if g == nil {
		return artifact.NewManager(nil)
	}
	return g.artifacts
}

func (g *Gateway) captureTerminalArtifacts(ctx context.Context, call ToolCall, sourceID string,
	stdout string, stderr string, stdoutMIME string,
) (map[string]string, error) {
	if stdout == "" && stderr == "" {
		return nil, nil
	}
	if call.RunID == "" {
		return nil, nil
	}
	if g == nil || g.artifactStore == nil || g.artifacts == nil {
		return nil, errors.New("run-bound terminal output requires an artifact store")
	}
	outputs := make([]artifact.Output, 0, 2)
	if stdout != "" {
		outputs = append(outputs, artifact.Output{Stream: artifact.StreamStdout, MIME: stdoutMIME, Content: stdout})
	}
	if stderr != "" {
		outputs = append(outputs, artifact.Output{
			Stream: artifact.StreamStderr, MIME: "text/plain; charset=utf-8", Content: stderr,
		})
	}
	request := artifact.CaptureRequest{
		RunID: call.RunID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		SourceID: sourceID, ToolName: string(call.Name), Outputs: outputs,
	}
	if call.SpecialistToolCallID != "" {
		request.AgentID, request.AgentAttemptID = call.AgentID, call.AgentAttemptID
		request.SpecialistToolCallID, request.InvocationID = call.SpecialistToolCallID, call.InvocationID
	}
	descriptors, err := g.artifacts.Capture(ctx, request)
	if err != nil {
		return nil, err
	}
	metadata := make(map[string]string, len(descriptors)*3+1)
	metadata["artifact_count"] = strconv.Itoa(len(descriptors))
	for _, descriptor := range descriptors {
		prefix := "artifact_" + string(descriptor.Stream)
		metadata[prefix+"_id"] = descriptor.ID
		metadata[prefix+"_sha256"] = descriptor.SHA256
		metadata[prefix+"_bytes"] = strconv.FormatInt(descriptor.SizeBytes, 10)
	}
	return metadata, nil
}
