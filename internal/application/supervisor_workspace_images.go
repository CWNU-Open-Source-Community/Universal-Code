package application

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

// Pixels come from the immutable image saved by an authorized completed call,
// never from a model-supplied image ID or a later read of the mutable file.
func (s *AgentRunner) supervisorWorkspaceImages(ctx context.Context, turn domain.SupervisorTurn, request llm.ChatRequest, rounds []domain.SupervisorToolRound, vision bool) (llm.ChatRequest, error) {
	calls := map[string]domain.SupervisorToolCall{}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.ToolName == string(toolgateway.WorkspaceViewImageTool) && call.Status == domain.SupervisorToolCompleted {
				calls[call.CallID] = call
			}
		}
	}
	if len(calls) == 0 {
		return request, nil
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	for index, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		for _, result := range message.ToolResults {
			call, found := calls[result.ToolCallID]
			if !found {
				continue
			}
			fail := func() (llm.ChatRequest, error) {
				return llm.ChatRequest{}, apperror.New(apperror.CodeFailedPrecondition, "workspace image does not match its completed tool evidence")
			}
			authority, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(call.AuthorityJSON))
			var envelope supervisorToolResultEnvelope
			var output workspaceImageResult
			var input toolgateway.WorkspaceViewImagePayload
			canonical, payloadErr := toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceViewImageTool, json.RawMessage(call.PayloadJSON))
			if err != nil || payloadErr != nil || call.Validate() != nil || result.IsError || result.Content != call.ResultJSON ||
				call.RunID != turn.Run.ID || call.AttemptID != turn.Checkpoint.AttemptID || call.Turn != turn.Checkpoint.NextTurn ||
				call.AgentAttribution != domain.AgentAttributionSupervisorRoot || call.AgentID != authority.RootAgentID ||
				authority.RootAgentID != turn.Agent.ID || call.AgentAttemptID != call.AttemptID || authority.Worker != nil ||
				authority.RunID != turn.Run.ID || authority.MissionID != turn.Mission.ID || authority.SessionID != turn.Run.SessionID ||
				authority.WorkspaceID != turn.Mission.WorkspaceID || string(canonical) != call.PayloadJSON ||
				json.Unmarshal(canonical, &input) != nil || json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil ||
				envelope.Version != supervisorToolResultVersion || envelope.Tool != call.ToolName || envelope.Status != "completed" ||
				envelope.Code != "" || envelope.Truncated || json.Unmarshal([]byte(envelope.Stdout), &output) != nil ||
				output.Version != "workspace_image_result.v1" || output.Path != input.Path || output.Image.WorkspaceID != authority.WorkspaceID ||
				output.FileWorkspaceID != envelope.Metadata["workspace_id"] || output.RootFingerprint != authority.RootFingerprint ||
				envelope.Metadata["root_fingerprint"] != output.RootFingerprint {
				return fail()
			}
			reader, ok := s.store.(interface {
				GetWorkspaceImageByOperationKey(context.Context, string, string) (domain.WorkspaceImage, bool, error)
				GetWorkspaceImage(context.Context, string, string) (domain.WorkspaceImage, []byte, error)
			})
			if !ok {
				return fail()
			}
			key := workspaceImageSaveKey(call.RunID, call.CallID, supervisorWorkspaceImageOperationKey(call), canonical)
			saved, found, err := reader.GetWorkspaceImageByOperationKey(ctx, authority.WorkspaceID, key)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			if !found || saved != output.Image {
				return fail()
			}
			note := map[string]any{"source_kind": "workspace_image", "source_ref": call.CallID, "path": output.Path, "pixels_supplied": false, "instruction_authorized": false}
			if vision {
				metadata, data, err := reader.GetWorkspaceImage(ctx, output.Image.WorkspaceID, output.Image.ID)
				if err != nil {
					return llm.ChatRequest{}, err
				}
				validated, validationErr := imageattachment.Validate(data, metadata.MIMEType, metadata.Name)
				if metadata != output.Image || validationErr != nil || validated.SHA256 != metadata.SHA256 || validated.ByteSize != metadata.ByteSize || validated.Width != metadata.Width || validated.Height != metadata.Height {
					return fail()
				}
				message.Images = append(append([]llm.ImagePart(nil), message.Images...), llm.ImagePart{MediaType: metadata.MIMEType, Data: data, SHA256: metadata.SHA256, Width: metadata.Width, Height: metadata.Height})
				note["pixels_supplied"] = true
			} else {
				note["limitation"] = "This model has no confirmed vision support. Image pixels were not supplied; do not claim visual inspection. Use a vision-capable model to inspect this saved image."
			}
			encoded, _ := json.Marshal(note)
			message.Content += "\n" + string(encoded)
		}
		if err := llm.ValidateMessageImages(message); err != nil {
			return llm.ChatRequest{}, err
		}
		request.Messages[index] = message
	}
	return request, nil
}
