package httpapi

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceImageToolAppearsInCapabilityAndActivityEnums(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []any
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"AgentCodeToolCapabilityView", "ThreadActivityToolDetailView"} {
		found := false
		for _, name := range document.Components.Schemas[component].Properties["name"].Enum {
			found = found || name == "workspace_view_image"
		}
		if !found {
			t.Errorf("%s.name omits workspace_view_image", component)
		}
	}
}
