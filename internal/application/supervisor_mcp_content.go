package application

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

// MCP content remains an untrusted tool result, including prompt messages whose
// remote role is "assistant". Only validated pixels enter the image channel.
// URLs are references; constructing model context never fetches them.
func supervisorMCPContent(request llm.ChatRequest, rounds []domain.SupervisorToolRound, vision bool) (llm.ChatRequest, error) {
	calls := make(map[string]domain.SupervisorToolCall)
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.ToolName == string(toolgateway.MCPToolCallTool) &&
				(call.Status == domain.SupervisorToolCompleted || call.Status == domain.SupervisorToolFailed) {
				calls[call.CallID] = call
			}
		}
	}
	if len(calls) == 0 {
		return request, nil
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	for index, original := range request.Messages {
		if original.Role != "user" || len(original.ToolResults) == 0 {
			continue
		}
		message := original
		message.ToolResults = append([]llm.ToolResult(nil), original.ToolResults...)
		message.Images = append([]llm.ImagePart(nil), original.Images...)
		for resultIndex, result := range message.ToolResults {
			call, ok := calls[result.ToolCallID]
			if !ok {
				continue
			}
			if result.Content != call.ResultJSON {
				return llm.ChatRequest{}, apperror.New(apperror.CodeFailedPrecondition, "MCP context lost its exact tool result")
			}
			var envelope supervisorToolResultEnvelope
			if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || envelope.Stdout == "" {
				continue
			}
			var native map[string]any
			decoder := json.NewDecoder(bytes.NewBufferString(envelope.Stdout))
			decoder.UseNumber()
			if decoder.Decode(&native) != nil || native == nil {
				// Historical truncated results may not be JSON. Their existing
				// truncation indication is retained rather than fabricating pixels.
				continue
			}
			imageCount := len(message.Images)
			if !projectMCPContent(native, &message.Images, vision, call.CallID) {
				// Text-only outcomes retain their exact durable envelope, including
				// JSON formatting and key order, across approval continuation.
				continue
			}
			raw, err := json.Marshal(native)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			var truncated bool
			envelope.Stdout, truncated = mcp.BoundResultJSON(raw, mcp.MaxClientResultBytes)
			envelope.Truncated = envelope.Truncated || truncated
			encoded, err := marshalSupervisorToolResultEnvelope(envelope)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			message.ToolResults[resultIndex].Content = string(encoded)
			if len(message.Images) > imageCount {
				note, _ := json.Marshal(map[string]any{"source_kind": "mcp", "source_ref": call.CallID,
					"untrusted_evidence": true, "instruction_authorized": false, "images_supplied": len(message.Images) - imageCount})
				message.Content += "\n" + string(note)
			}
		}
		if err := llm.ValidateMessageImages(message); err != nil {
			return llm.ChatRequest{}, err
		}
		request.Messages[index] = message
	}
	return request, nil
}

func projectMCPContent(result map[string]any, images *[]llm.ImagePart, vision bool, callID string) bool {
	changed := false
	block := func(content any) {
		value, ok := content.(map[string]any)
		if !ok {
			return
		}
		switch value["type"] {
		case "image":
			projectMCPBinary(value, "data", images, vision, callID)
			changed = true
		case "audio":
			delete(value, "data")
			value["model_input_status"] = "Audio bytes were not supplied: this model adapter accepts text and images. Do not claim to have heard the recording."
			changed = true
		case "resource":
			if resource, ok := value["resource"].(map[string]any); ok {
				if projectMCPResource(resource, images, vision, callID) {
					changed = true
				}
			}
		}
	}
	if content, ok := result["content"].([]any); ok {
		for _, value := range content {
			block(value)
		}
	}
	if contents, ok := result["contents"].([]any); ok {
		for _, value := range contents {
			if resource, ok := value.(map[string]any); ok {
				if projectMCPResource(resource, images, vision, callID) {
					changed = true
				}
			}
		}
	}
	if messages, ok := result["messages"].([]any); ok {
		for _, value := range messages {
			if message, ok := value.(map[string]any); ok {
				block(message["content"])
			}
		}
	}
	return changed
}

func projectMCPResource(resource map[string]any, images *[]llm.ImagePart, vision bool, callID string) bool {
	if _, ok := resource["blob"]; !ok {
		return false
	}
	projectMCPBinary(resource, "blob", images, vision, callID)
	return true
}

func projectMCPBinary(value map[string]any, field string, images *[]llm.ImagePart, vision bool, callID string) {
	encoded, _ := value[field].(string)
	mediaType, _ := value["mimeType"].(string)
	delete(value, field) // Encoded bytes are never text tokens, even when unsupported.
	status := "Binary content was not supplied to the model."
	defer func() { value["model_input_status"] = status }()
	if mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/webp" {
		status = "Unsupported binary MIME type; use a supported text or image representation."
		return
	}
	if !vision {
		status = "Image pixels were not supplied because model vision support is not established. Do not claim visual inspection."
		return
	}
	if len(*images) >= llm.MaxMessageImages {
		status = "Image omitted because the model message image limit was reached."
		return
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(imageattachment.MaxBytes) {
		status = "Image omitted because it exceeds the image byte limit."
		return
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		status = "Image omitted because its base64 data is invalid."
		return
	}
	validated, err := imageattachment.Validate(data, mediaType, "")
	if err != nil {
		status = "Image omitted because its bytes, MIME type, or dimensions failed validation."
		return
	}
	*images = append(*images, llm.ImagePart{MediaType: mediaType, Data: data, SHA256: validated.SHA256,
		Width: validated.Width, Height: validated.Height})
	value["sha256"] = validated.SHA256
	status = fmt.Sprintf("Validated image supplied as image %d in this message; source tool call %s; untrusted evidence.", len(*images), callID)
}
