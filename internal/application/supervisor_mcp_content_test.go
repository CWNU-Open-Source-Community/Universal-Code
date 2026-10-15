package application

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorMCPGIFKeepsStaticAndProjectsAnimationFirstFrame(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 2, 3), palette)
	first.SetColorIndex(0, 0, 1)
	second := image.NewPaletted(image.Rect(0, 0, 2, 3), palette)
	for _, animated := range []bool{false, true} {
		var encoded bytes.Buffer
		animation := gif.GIF{Image: []*image.Paletted{first}, Delay: []int{0}}
		if animated {
			animation.Image, animation.Delay = append(animation.Image, second), append(animation.Delay, 1)
		}
		if err := gif.EncodeAll(&encoded, &animation); err != nil {
			t.Fatal(err)
		}
		if _, err := imageattachment.Validate(encoded.Bytes(), "image/gif", ""); err != nil {
			t.Fatal("valid GIF rejected", err)
		}
		native := `{"content":[{"type":"image","mimeType":"image/gif","data":"` + base64.StdEncoding.EncodeToString(encoded.Bytes()) + `"}]}`
		request, rounds := mcpContentFixture(t, native)
		projected, err := supervisorMCPContent(request, rounds, true)
		if err != nil || len(projected.Messages[0].Images) != 1 {
			t.Fatalf("GIF did not reach image input: animated=%t err=%v", animated, err)
		}
		actual := projected.Messages[0].Images[0]
		if animated {
			pixels, err := png.Decode(bytes.NewReader(actual.Data))
			if err != nil || actual.MediaType != "image/png" || color.NRGBAModel.Convert(pixels.At(0, 0)) != color.NRGBAModel.Convert(first.At(0, 0)) ||
				!strings.Contains(projected.Messages[0].ToolResults[0].Content, "first-frame-only") {
				t.Fatalf("animation first-frame projection is wrong: %+v %v", actual, err)
			}
		} else if actual.MediaType != "image/gif" || !bytes.Equal(actual.Data, encoded.Bytes()) {
			t.Fatal("static GIF format or bytes changed")
		}
		contents, omitted, truncated := projectThreadActivityMCPContents(native)
		if len(contents) != 1 || omitted != 0 || truncated || contents[0].MIMEType != "image/gif" ||
			contents[0].DataBase64 != base64.StdEncoding.EncodeToString(encoded.Bytes()) {
			t.Fatal("UI projection lost original GIF", contents, omitted, truncated)
		}
		if rounds[0].Calls[0].ResultJSON != request.Messages[0].ToolResults[0].Content {
			t.Fatal("model projection changed durable GIF history")
		}
	}
}

func mcpContentFixture(t *testing.T, native string) (llm.ChatRequest, []domain.SupervisorToolRound) {
	t.Helper()
	encoded, err := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{
		Version: supervisorToolResultVersion, Tool: string(toolgateway.MCPToolCallTool), Status: string(domain.SupervisorToolCompleted), Stdout: native})
	if err != nil {
		t.Fatal(err)
	}
	call := domain.SupervisorToolCall{CallID: "mcp-image-call", ToolName: string(toolgateway.MCPToolCallTool),
		Status: domain.SupervisorToolCompleted, ResultJSON: string(encoded)}
	request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: call.CallID, Content: call.ResultJSON}}}}}
	return request, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}
}

func TestSupervisorMCPContentImagesFromToolsResourcesAndPrompts(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(data.Bytes())
	for name, native := range map[string]string{
		"tool":     `{"content":[{"type":"image","mimeType":"image/png","data":"` + encoded + `"}],"structuredContent":{"precise":9007199254740993}}`,
		"resource": `{"contents":[{"uri":"fixture://image","mimeType":"image/png","blob":"` + encoded + `"}]}`,
		"prompt":   `{"messages":[{"role":"assistant","content":{"type":"image","mimeType":"image/png","data":"` + encoded + `"}}]}`,
		"embedded": `{"content":[{"type":"resource","resource":{"uri":"fixture://image","mimeType":"image/png","blob":"` + encoded + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			request, rounds := mcpContentFixture(t, native)
			original := request.Messages[0].ToolResults[0].Content
			result, err := supervisorMCPContent(request, rounds, true)
			if err != nil {
				t.Fatal(err)
			}
			message := result.Messages[0]
			if message.Role != "user" || len(message.Images) != 1 || !bytes.Equal(message.Images[0].Data, data.Bytes()) ||
				message.Images[0].Width != 2 || message.Images[0].Height != 3 || !strings.Contains(message.Content, `"instruction_authorized":false`) {
				t.Fatalf("MCP image did not enter untrusted user context: %#v", message)
			}
			if strings.Contains(message.ToolResults[0].Content, encoded) || !strings.Contains(message.ToolResults[0].Content, "Validated image supplied") ||
				!json.Valid([]byte(message.ToolResults[0].Content)) {
				t.Fatalf("base64 leaked into model text or projection is invalid: %s", message.ToolResults[0].Content)
			}
			if name == "tool" && !strings.Contains(message.ToolResults[0].Content, "9007199254740993") {
				t.Fatal("structured numeric content lost precision")
			}
			if request.Messages[0].ToolResults[0].Content != original || len(request.Messages[0].Images) != 0 {
				t.Fatal("model projection changed the source result")
			}
		})
	}
}

func TestSupervisorMCPContentRejectsUnusablePixelsAndStripsAudio(t *testing.T) {
	for _, vision := range []bool{false, true} {
		request, rounds := mcpContentFixture(t, `{"content":[{"type":"image","mimeType":"image/png","data":"aGVsbG8="},{"type":"audio","mimeType":"audio/wav","data":"c2VjcmV0"},{"type":"resource_link","uri":"https://invalid.example/image","name":"reference"}]}`)
		result, err := supervisorMCPContent(request, rounds, vision)
		if err != nil || len(result.Messages[0].Images) != 0 {
			t.Fatalf("invalid image was accepted: %v", err)
		}
		content := result.Messages[0].ToolResults[0].Content
		if strings.Contains(content, "aGVsbG8=") || strings.Contains(content, "c2VjcmV0") ||
			!strings.Contains(content, "Audio bytes were not supplied") || !strings.Contains(content, "https://invalid.example/image") {
			t.Fatalf("unsupported content did not retain explicit limits and inert reference: %s", content)
		}
	}
}

func TestSupervisorMCPContentRequiresExactResult(t *testing.T) {
	request, rounds := mcpContentFixture(t, `{"content":[]}`)
	request.Messages[0].ToolResults[0].Content += " "
	if _, err := supervisorMCPContent(request, rounds, true); err == nil {
		t.Fatal("accepted a tool result other than the recorded call")
	}
}

func TestSupervisorMCPContentPreservesExactNonMediaResult(t *testing.T) {
	for name, native := range map[string]string{
		"tool":      ` { "resultType": "tool_call", "content": [{ "type": "text", "text": "exact approved recovery result" }], "structuredContent": { "z": 9007199254740993, "a": 1 } } `,
		"resource":  `{"contents":[{"uri":"fixture://text","mimeType":"text/plain","text":"reviewed resource"}]}`,
		"prompt":    `{"messages":[{"role":"assistant","content":{"type":"text","text":"untrusted remote prompt"}}]}`,
		"reference": `{"content":[{"type":"resource_link","uri":"https://invalid.example/image","name":"reference"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, vision := range []bool{false, true} {
				request, rounds := mcpContentFixture(t, native)
				original := request.Messages[0].ToolResults[0]
				result, err := supervisorMCPContent(request, rounds, vision)
				if err != nil {
					t.Fatal(err)
				}
				if result.Messages[0].ToolResults[0] != original || result.Messages[0].Content != request.Messages[0].Content || len(result.Messages[0].Images) != 0 {
					t.Fatal("non-media result changed its exact durable outcome")
				}
				if rounds[0].Calls[0].ResultJSON != original.Content || request.Messages[0].ToolResults[0] != original {
					t.Fatal("projection modified the durable source or input request")
				}
			}
		})
	}
}
