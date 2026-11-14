package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

func TestWorkspaceViewImageCanObserveChangedImageAtTheSamePathInOneTurn(t *testing.T) {
	st, _, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	first, second := threadImagePNG(t, 31), threadImagePNG(t, 209)
	path := filepath.Join(root, "render.png")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	provider := &workspaceImageJourneyProvider{vision: llm.VisionSupported}
	provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 1 {
			return toolResponse("first-image", "workspace_view_image", `{"version":"agent-code-tools.v1","path":"render.png"}`), nil
		}
		var pixels [][]byte
		imageIDs := map[string]bool{}
		for _, message := range request.Messages {
			for _, image := range message.Images {
				pixels = append(pixels, image.Data)
			}
			for _, result := range message.ToolResults {
				var envelope struct{ Tool, Stdout string }
				if json.Unmarshal([]byte(result.Content), &envelope) != nil || envelope.Tool != "workspace_view_image" {
					continue
				}
				if result.IsError {
					return nil, fmt.Errorf("image observation failed: %s", result.Content)
				}
				var output struct{ Image domain.WorkspaceImage }
				if json.Unmarshal([]byte(envelope.Stdout), &output) != nil || output.Image.ID == "" {
					return nil, fmt.Errorf("image receipt is missing")
				}
				imageIDs[output.Image.ID] = true
			}
		}
		if index == 2 {
			if len(pixels) != 1 || !bytes.Equal(pixels[0], first) || len(imageIDs) != 1 {
				return nil, fmt.Errorf("first image pixels or receipt mismatch")
			}
			if err := os.WriteFile(path, second, 0600); err != nil {
				return nil, err
			}
			return toolResponse("second-image", "workspace_view_image", `{"version":"agent-code-tools.v1","path":"render.png"}`), nil
		}
		if index != 3 || len(pixels) != 2 || len(imageIDs) != 2 || !bytes.Equal(pixels[0], first) || !bytes.Equal(pixels[1], second) {
			return nil, fmt.Errorf("second observation reused image evidence: index=%d pixels=%d ids=%d", index, len(pixels), len(imageIDs))
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Compared both image observations", "done", "")), nil
	}
	if _, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != 3 {
		t.Fatalf("requests=%d", len(provider.Requests()))
	}
}
