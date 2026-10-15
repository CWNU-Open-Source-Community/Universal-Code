package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

func TestThreadActivityMCPContentViewExportsValidatedFixture(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 96, 54))
	for y := range 54 {
		for x := range 96 {
			pixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 4), B: 145, A: 255})
		}
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, pixels); err != nil {
		t.Fatal(err)
	}
	wave := make([]byte, 44+16000)
	copy(wave, "RIFF")
	copy(wave[8:], "WAVEfmt ")
	copy(wave[36:], "data")
	binary.LittleEndian.PutUint32(wave[4:], uint32(len(wave)-8))
	binary.LittleEndian.PutUint32(wave[16:], 16)
	binary.LittleEndian.PutUint16(wave[20:], 1)
	binary.LittleEndian.PutUint16(wave[22:], 1)
	binary.LittleEndian.PutUint32(wave[24:], 8000)
	binary.LittleEndian.PutUint32(wave[28:], 16000)
	binary.LittleEndian.PutUint16(wave[32:], 2)
	binary.LittleEndian.PutUint16(wave[34:], 16)
	binary.LittleEndian.PutUint32(wave[40:], 16000)
	output, _ := json.Marshal(map[string]any{"contents": []any{
		map[string]any{"uri": "notes://guide", "mimeType": "text/plain", "text": "Resource text preview\nStored content stays available for inspection."},
		map[string]any{"uri": "media://preview", "mimeType": "image/png", "blob": base64.StdEncoding.EncodeToString(imageBytes.Bytes())},
		map[string]any{"uri": "media://audio", "mimeType": "audio/wav", "blob": base64.StdEncoding.EncodeToString(wave)},
		map[string]any{"uri": "html://sample", "mimeType": "text/html", "text": "<script>window.mcpPreviewExecuted=true</script>"},
	}, "content": []any{map[string]any{"type": "resource_link", "name": "Reference", "uri": "https://docs.example.com/guide"}}})
	payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPContentToolProtocolVersion, Method: "resources/read", ServerID: "media-server", ToolName: "resources.read",
		CapabilityFingerprint: strings.Repeat("a", 64), Arguments: json.RawMessage(`{"uri":"notes://guide"}`)})
	result, _ := json.Marshal(map[string]any{"version": "supervisor_tool_result.v1", "tool": "mcp_tool_call", "status": "completed", "stdout": string(output), "metadata": map[string]string{"untrusted": "true"}})
	call := domain.SupervisorToolCall{ToolName: "mcp_tool_call", PayloadJSON: string(payload), ResultJSON: string(result), Status: domain.SupervisorToolCompleted}
	typed, found, err := application.ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || len(typed.MCP.Contents) != 5 {
		t.Fatal("validated content projection unavailable", err)
	}
	view := ThreadActivityDetailView{Version: "thread_activity_detail.v2", ActivityRef: "call-media", RunID: "run-media", Tools: []ThreadActivityToolDetailView{{
		Name: "mcp_tool_call", Label: "MCP resource read", AgentID: "agent-media", AgentRole: "root", AgentLabel: "Root Agent", Status: "completed", StartedAt: time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC),
		DurationMilliseconds: 12, Detail: threadActivityTypedDetailView(typed, nil)}}}
	server := extensionTestMCPServer("media-server")
	server.State = mcp.TrustCapabilitiesPending
	server.Descriptor.DeclaredCapabilities = []mcp.CapabilityKind{mcp.CapabilityResources, mcp.CapabilityPrompts}
	server.Descriptor.CredentialRef = ""
	server.DescriptorFingerprint = server.Descriptor.Fingerprint()
	server.Capabilities, err = mcp.NewCapabilitySnapshot("content-fixture", "1.0.0",
		[]mcp.CapabilityKind{mcp.CapabilityResources, mcp.CapabilityPrompts}, nil,
		[]mcp.RemoteResource{{URI: "notes://guide", Name: "Guide"}},
		[]mcp.RemotePrompt{{Name: "prepare", Arguments: []mcp.RemotePromptArgument{{Name: "topic", Description: "Subject to prepare", Required: true}, {Name: "style", Required: false}}}}, server.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	projected := extensionMCPServerView(server)
	if len(projected.Capabilities.PromptDetails) != 1 || !projected.Capabilities.PromptDetails[0].Arguments[0].Required {
		t.Fatal("prompt parameter projection was lost")
	}
	fixture := map[string]any{"version": "api.v1", "request_id": "media-fixture", "data": view}
	if directory := os.Getenv("UC_MCP_CONTENT_EVIDENCE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{"media-fixture.json": fixture, "prompt-fixture.json": projected} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
