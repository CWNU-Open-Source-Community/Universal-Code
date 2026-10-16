package application

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
)

func largeMCPImageFixture(t *testing.T, text string) (*mcpApprovalFixture, domain.SupervisorToolCall, artifact.Blob, []byte) {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	random := rand.New(rand.NewSource(1))
	for index := 0; index < len(pixels.Pix); index += 4 {
		value := random.Uint32()
		pixels.Pix[index], pixels.Pix[index+1], pixels.Pix[index+2], pixels.Pix[index+3] = byte(value), byte(value>>8), byte(value>>16), 255
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= mcp.MaxClientResultBytes {
		t.Fatal("fixture PNG must exceed the MCP preview byte limit")
	}
	f := newMCPOperationApprovalFixture(t, domain.RunExecutionPermissionFull, false, false, "large_result")
	raw, err := json.Marshal(map[string]any{"content": []any{map[string]any{
		"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(encoded.Bytes()),
	}, map[string]any{"type": "resource_link", "uri": "https://invalid.example/screenshot", "name": "inert reference"},
		map[string]any{"type": "text", "text": text}}})
	if err != nil {
		t.Fatal(err)
	}
	f.response.Store(json.RawMessage(raw))
	if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
		t.Fatalf("large image call did not complete once: waiting=%t err=%v", waiting, err)
	}
	call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
	if err != nil || call.Status != domain.SupervisorToolCompleted {
		t.Fatalf("large image call was not persisted: %v", err)
	}
	var envelope supervisorToolResultEnvelope
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || !envelope.Truncated ||
		!strings.Contains(envelope.Stdout, "Binary content omitted") || strings.Contains(envelope.Stdout, `"data"`) ||
		len(envelope.Stdout) > f.server.Descriptor.MaxResultBytes {
		t.Fatal("fixture did not exercise omitted image bytes")
	}
	blob, err := f.st.GetRunArtifact(t.Context(), envelope.Metadata["artifact_stdout_id"])
	if err != nil || blob.SizeBytes <= mcp.MaxClientResultBytes {
		t.Fatalf("full image result was not retained: %v", err)
	}
	return f, call, blob, encoded.Bytes()
}

func mcpArtifactModelRequest(call domain.SupervisorToolCall) (llm.ChatRequest, []domain.SupervisorToolRound) {
	return llm.ChatRequest{Messages: []llm.Message{{Role: "user", ToolResults: []llm.ToolResult{{
		ToolCallID: call.CallID, Content: call.ResultJSON,
	}}}}}, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}
}

func TestSupervisorMCPContentRestoresLargeImageFromDurableArtifact(t *testing.T) {
	f, call, blob, pixels := largeMCPImageFixture(t, "")
	request, rounds := mcpArtifactModelRequest(call)
	requests := f.requests.Load()
	projected, err := f.supervisor.supervisorMCPContent(t.Context(), f.turn, request, rounds, true)
	if err != nil || len(projected.Messages[0].Images) != 1 || !bytes.Equal(projected.Messages[0].Images[0].Data, pixels) {
		t.Fatalf("retained PNG did not reach the image channel: %v", err)
	}
	message := projected.Messages[0]
	if strings.Contains(message.ToolResults[0].Content, base64.StdEncoding.EncodeToString(pixels)[:128]) ||
		message.ToolResults[0].Content != call.ResultJSON ||
		!strings.Contains(message.ToolResults[0].Content, "https://invalid.example/screenshot") ||
		!strings.Contains(message.Content, `"instruction_authorized":false`) ||
		!strings.Contains(message.Content, `"images_supplied":1`) {
		t.Fatal("restored pixels leaked into text, lost provenance, or changed the inert URL")
	}
	if request.Messages[0].ToolResults[0].Content != call.ResultJSON || rounds[0].Calls[0].ResultJSON != call.ResultJSON {
		t.Fatal("image projection changed durable tool history")
	}
	// A fresh runner has no MCP connection or image cache. A permitted historical
	// round must retain its pixels even after the current turn/attempt changes.
	recovered := NewAgentRunner(f.st, nil, policy.NewDefaultChecker())
	later := f.turn
	later.Checkpoint.NextTurn++
	later.Checkpoint.AttemptID = "later-model-attempt"
	projected, err = recovered.supervisorMCPContent(t.Context(), later, request, rounds, true)
	if err != nil || len(projected.Messages[0].Images) != 1 || !bytes.Equal(projected.Messages[0].Images[0].Data, pixels) {
		t.Fatalf("recovered historical image was lost: %v", err)
	}
	if f.calls.Load() != 1 || f.requests.Load() != requests {
		t.Fatal("model image recovery repeated an MCP operation or fetched the URL")
	}
	stored, err := f.st.GetRunArtifact(t.Context(), blob.ID)
	if err != nil || stored.Content != blob.Content || stored.SHA256 != blob.SHA256 {
		t.Fatal("model image recovery modified its stored artifact", err)
	}
}

func TestSupervisorMCPArtifactRecoveryPreservesConfiguredTextLimit(t *testing.T) {
	const tail = "ARTIFACT_ONLY_TEXT_TAIL"
	text := strings.Repeat("x", 100*1024) + tail
	f, call, blob, pixels := largeMCPImageFixture(t, text)
	if f.server.Descriptor.MaxResultBytes != 8192 || !strings.Contains(blob.Content, text) || strings.Contains(call.ResultJSON, tail) {
		t.Fatal("fixture must retain the long text outside the configured 8KiB preview")
	}
	request, rounds := mcpArtifactModelRequest(call)
	projected, err := f.supervisor.supervisorMCPContent(t.Context(), f.turn, request, rounds, true)
	if err != nil {
		t.Fatal(err)
	}
	message := projected.Messages[0]
	if len(message.Images) != 1 || !bytes.Equal(message.Images[0].Data, pixels) {
		t.Fatal("preserving the text preview lost the retained image")
	}
	if message.ToolResults[0].Content != call.ResultJSON || strings.Contains(message.Content, tail) ||
		strings.Contains(message.ToolResults[0].Content, tail) {
		t.Fatal("recovering an image widened the server's bounded text result")
	}
	if !strings.Contains(message.Content, `"images_supplied":1`) {
		t.Fatal("restored pixels lost their model provenance note")
	}
}

type mcpImageArtifactStore struct {
	AgentRunnerStore
	blob  artifact.Blob
	err   error
	reads int
}

func (s *mcpImageArtifactStore) GetRunArtifact(context.Context, string) (artifact.Blob, error) {
	s.reads++
	return s.blob, s.err
}

func TestSupervisorMCPContentRejectsMismatchedArtifactBindings(t *testing.T) {
	f, call, blob, _ := largeMCPImageFixture(t, "")
	for _, scenario := range []string{"run", "session", "workspace", "source", "digest", "authority", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			st := &mcpImageArtifactStore{AgentRunnerStore: f.st, blob: blob}
			turn, saved := f.turn, call
			switch scenario {
			case "run":
				turn.Run.ID = "another-run"
			case "session":
				st.blob.SessionID = "another-session"
			case "workspace":
				st.blob.WorkspaceID = "another-workspace"
			case "source":
				st.blob.SourceID = "another-tool-invocation"
			case "digest":
				st.blob.Content += " "
			case "authority":
				authority, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(saved.AuthorityJSON))
				if err != nil {
					t.Fatal(err)
				}
				authority.WorkspaceID = "another-workspace"
				raw, _ := mcp.EncodeSupervisorCallAuthority(authority)
				saved.AuthorityJSON = string(raw)
			case "missing":
				st.err = errors.New("artifact unavailable")
			}
			request, rounds := mcpArtifactModelRequest(saved)
			result, err := (&AgentRunner{store: st}).supervisorMCPContent(t.Context(), turn, request, rounds, true)
			if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || len(result.Messages) != 0 {
				t.Fatalf("mismatched artifact became model evidence: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSupervisorMCPContentRetainsImageValidationAfterArtifactRecovery(t *testing.T) {
	f, call, blob, _ := largeMCPImageFixture(t, "")
	request, rounds := mcpArtifactModelRequest(call)
	st := &mcpImageArtifactStore{AgentRunnerStore: f.st, blob: blob, err: errors.New("must not read for a text-only model")}
	result, err := (&AgentRunner{store: st}).supervisorMCPContent(t.Context(), f.turn, request, rounds, false)
	if err != nil || st.reads != 0 || len(result.Messages[0].Images) != 0 ||
		!strings.Contains(result.Messages[0].ToolResults[0].Content, "vision support is not established") {
		t.Fatal("text-only model read retained pixels or lost its explicit omission", err)
	}
	st.err = nil
	st.blob.Content = strings.Replace(blob.Content, `"mimeType":"image/png"`, `"mimeType":"image/jpeg"`, 1)
	st.blob.SHA256, st.blob.SizeBytes = artifact.Hash(st.blob.Content), int64(len(st.blob.Content))
	var envelope supervisorToolResultEnvelope
	_ = json.Unmarshal([]byte(call.ResultJSON), &envelope)
	envelope.Metadata["artifact_stdout_sha256"] = st.blob.SHA256
	envelope.Metadata["artifact_stdout_bytes"] = strconv.FormatInt(st.blob.SizeBytes, 10)
	raw, _ := marshalSupervisorToolResultEnvelope(envelope)
	call.ResultJSON = string(raw)
	request, rounds = mcpArtifactModelRequest(call)
	result, err = (&AgentRunner{store: st}).supervisorMCPContent(t.Context(), f.turn, request, rounds, true)
	if err != nil || len(result.Messages[0].Images) != 0 ||
		result.Messages[0].ToolResults[0].Content != call.ResultJSON ||
		!strings.Contains(result.Messages[0].ToolResults[0].Content, "Binary content omitted") ||
		strings.Contains(result.Messages[0].Content, "images_supplied") ||
		strings.Contains(result.Messages[0].ToolResults[0].Content, `"data"`) {
		t.Fatal("artifact recovery bypassed MIME validation or leaked rejected pixels into text", err)
	}
}
