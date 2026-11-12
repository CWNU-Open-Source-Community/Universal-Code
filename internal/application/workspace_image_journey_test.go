package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

type workspaceImageJourneyProvider struct {
	boundaryJourneyProvider
	vision llm.VisionSupport
}

func (p *workspaceImageJourneyProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: p.vision, Source: "operator_declared"}
}

// Real SQLite + workspace + gateway + model request assembly. The provider is
// scripted so this checks actual pixels without a paid model request.
func TestWorkspaceViewImageReachesModelAndSurvivesSourceRemoval(t *testing.T) {
	for _, vision := range []llm.VisionSupport{llm.VisionSupported, llm.VisionUnsupported, llm.VisionUnknown} {
		t.Run(string(vision), func(t *testing.T) {
			st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			pixels := threadImagePNG(t, 37)
			path := filepath.Join(root, "render.png")
			if err := os.WriteFile(path, pixels, 0600); err != nil {
				t.Fatal(err)
			}
			provider := &workspaceImageJourneyProvider{vision: vision}
			var providerError error
			provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (response *llm.ChatResponse, failure error) {
				defer func() { providerError = failure }()
				if index == 1 {
					visible := false
					for _, tool := range request.Tools {
						if tool.Name == "workspace_view_image" {
							visible = true
						}
					}
					if !visible {
						return nil, fmt.Errorf("workspace image tool is absent from the real request")
					}
					return toolResponse("inspect-png", "workspace_view_image", `{"version":"agent-code-tools.v1","path":"render.png"}`), nil
				}
				if index > 3 {
					return nil, fmt.Errorf("unexpected model request %d", index)
				}
				count, note, paired := 0, false, false
				for _, message := range request.Messages {
					for _, result := range message.ToolResults {
						var envelope struct {
							Tool string `json:"tool"`
						}
						if json.Unmarshal([]byte(result.Content), &envelope) == nil && envelope.Tool == "workspace_view_image" {
							if result.IsError {
								return nil, fmt.Errorf("image tool failed: %s", result.Content)
							}
							paired = true
						}
					}
					for _, image := range message.Images {
						if !bytes.Equal(image.Data, pixels) {
							return nil, fmt.Errorf("image pixels differ from executed read")
						}
						count++
					}
					note = note || strings.Contains(message.Content, "Image pixels were not supplied")
				}
				if !paired || vision == llm.VisionSupported && count != 1 || vision != llm.VisionSupported && (count != 0 || !note) {
					return nil, fmt.Errorf("vision=%s images=%d note=%t paired=%t", vision, count, note, paired)
				}
				if index == 2 {
					if err := os.Remove(path); err != nil {
						return nil, err
					}
					return boundaryRead("read-after-image", 1), nil
				}
				return textResponse(rootActionResponse(domain.RootActionFinish, "Image evidence processed", "done", "")), nil
			}
			if _, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input); err != nil {
				events, _ := st.ListRunEvents(t.Context(), run.ID)
				for _, event := range events {
					if strings.Contains(event.Type, "fail") {
						t.Logf("failure: %+v", event)
					}
				}
				t.Fatalf("%v (scripted provider: %v)", err, providerError)
			}
			if len(provider.Requests()) != 3 {
				t.Fatalf("requests=%d", len(provider.Requests()))
			}
		})
	}
}
