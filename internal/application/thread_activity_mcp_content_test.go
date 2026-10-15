package application

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"cyberagent-workbench/internal/toolgateway"
)

func contentTestPNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	pixels := image.NewNRGBA(image.Rect(0, 0, 32, 18))
	for y := range 18 {
		for x := range 32 {
			pixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 7), G: uint8(y * 11), B: 120, A: 255})
		}
	}
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func contentTestWAV() []byte {
	data := make([]byte, 44+16000)
	copy(data, "RIFF")
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 16000)
	return data
}

func TestThreadActivityMCPContentProjectsMediaTextAndInertURIs(t *testing.T) {
	imageData := contentTestPNG(t)
	raw, _ := json.Marshal(map[string]any{"content": []any{
		map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(imageData)},
		map[string]any{"type": "audio", "mimeType": "audio/wav", "data": base64.StdEncoding.EncodeToString(contentTestWAV())},
		map[string]any{"type": "text", "text": `{"password":"private-content-canary","message":"Visible text"}`},
		map[string]any{"type": "resource_link", "name": "Reference", "uri": "https://user:private@docs.example.com/guide?token=private-content-canary&page=2#private"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "notes://guide", "mimeType": "text/html", "text": "<script>window.mcpPreviewExecuted=true</script>"}},
		map[string]any{"type": "image", "mimeType": "image/jpeg", "data": base64.StdEncoding.EncodeToString(imageData)},
	}})
	contents, omitted, cut := projectThreadActivityMCPContents(string(raw))
	if len(contents) != 5 || omitted != 1 || cut || contents[0].Kind != "image" || contents[0].Width != 32 || contents[0].Height != 18 || contents[1].Kind != "audio" {
		t.Fatal("media projection changed expected content", len(contents), omitted, cut)
	}
	if strings.Contains(contents[2].Text, "private-content-canary") {
		t.Fatal("text projection leaked credentials")
	}
	if contents[3].URI != "https://docs.example.com/guide?page=2" || contents[4].URI != "notes://guide" {
		t.Fatalf("URI projection lost safe resource provenance: %q %q", contents[3].URI, contents[4].URI)
	}
	if err := validThreadActivityMCPContents(contents); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []map[string]any{
		{"type": "image", "mimeType": "image/svg+xml", "data": base64.StdEncoding.EncodeToString([]byte(`<svg onload="alert(1)"/>`))},
		{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(imageData[:len(imageData)-12])},
		{"type": "audio", "mimeType": "text/html", "data": base64.StdEncoding.EncodeToString([]byte("<html>bad</html>"))},
	} {
		if _, _, valid := projectThreadActivityMCPContent(wrong, "", false); valid {
			t.Fatal("invalid or mislabeled media was exposed")
		}
	}
}

func TestThreadActivityMCPContentBoundsAndV2PromptProjection(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": map[string]any{"type": "text", "text": "Prepared prompt content"}}}})
	payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPContentToolProtocolVersion, Method: "prompts/get", ServerID: "media-server", ToolName: "prompts.get",
		CapabilityFingerprint: strings.Repeat("a", 64), Arguments: json.RawMessage(`{"name":"prepare","arguments":{}}`)})
	call := threadActivityFactsCall(toolgateway.MCPToolCallTool, payload, map[string]string{"untrusted": "true"}, string(raw))
	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || len(detail.MCP.Contents) != 1 || detail.MCP.Contents[0].Role != "assistant" {
		t.Fatal("v2 prompt messages lost their typed content", err)
	}
	items := []any{}
	for range 20 {
		items = append(items, map[string]any{"type": "text", "text": "visible"})
	}
	raw, _ = json.Marshal(map[string]any{"content": items})
	contents, omitted, _ := projectThreadActivityMCPContents(string(raw))
	if len(contents) != 16 || omitted != 4 {
		t.Fatal("content count was not bounded")
	}
	raw, _ = json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("中", 15000)}}})
	contents, _, cut := projectThreadActivityMCPContents(string(raw))
	if !cut || len(contents) != 1 || len(contents[0].Text) > MaxThreadActivityMCPTextBytes || validThreadActivityMCPContents(contents) != nil {
		t.Fatal("text preview failed its UTF-8/byte bound")
	}
	contents, _, cut = projectThreadActivityMCPContents(strings.Repeat("x", MaxThreadActivityMCPContentBytes+1))
	if len(contents) != 0 || !cut {
		t.Fatal("oversized saved result became a content projection")
	}
}
