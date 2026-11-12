package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestBrowserNetworkAccessOpenAPIOriginConstraints(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	if err := compiler.AddResource("browser-network-openapi.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BrowserNetworkAccessRequestView", "WorkspaceBrowserNetworkAccessRequestView", "BrowserNetworkAccessSelection", "BrowserNetworkPreference"} {
		t.Run(name, func(t *testing.T) {
			schema, err := compiler.Compile("browser-network-openapi.json#/components/schemas/" + name)
			if err != nil {
				t.Fatal(err)
			}
			origins := document.Components.Schemas[name]["properties"].(map[string]any)["browser_origins"].(map[string]any)
			items := origins["items"].(map[string]any)
			if _, exists := items["maxLength"]; exists || !strings.Contains(origins["description"].(string), "4096 UTF-8 bytes") {
				t.Fatal("URL byte limit must be documented without a character-count maxLength")
			}
			eight := make([]any, 8)
			for index := range eight {
				eight[index] = "https://example.org"
			}
			for _, tc := range []struct {
				mode    string
				origins []any
				valid   bool
			}{
				{"off", nil, true}, {"off", []any{}, true}, {"off", []any{"https://example.org/path"}, true},
				{"internet", nil, true}, {"internet", []any{}, true}, {"internet", []any{"https://example.org"}, false},
				{"restricted", nil, false}, {"restricted", []any{}, false}, {"restricted", []any{"https://example.org"}, true},
				{"restricted", []any{"https://example.org", "https://example.org"}, true},
				{"restricted", []any{""}, false},
				{"off", eight, true}, {"restricted", eight, true},
				{"off", append(eight, "https://example.org"), false},
				{"restricted", append(eight, "https://example.org"), false},
			} {
				value := map[string]any{"browser_network_access": tc.mode, "workspace_id": "workspace-a", "revision": 1,
					"version": "browser_network_access.v1", "expected_revision": 1, "replayed": false, "for_next_run": false}
				if name == "BrowserNetworkAccessRequestView" || name == "WorkspaceBrowserNetworkAccessRequestView" {
					delete(value, "workspace_id")
					delete(value, "revision")
					delete(value, "replayed")
					delete(value, "for_next_run")
				} else {
					delete(value, "expected_revision")
					if name == "BrowserNetworkPreference" {
						delete(value, "version")
						delete(value, "replayed")
						delete(value, "for_next_run")
					}
				}
				if tc.origins != nil {
					value["browser_origins"] = tc.origins
				}
				if err := schema.Validate(value); (err == nil) != tc.valid {
					t.Fatalf("%s origins=%v: schema validation=%v, want valid=%t", tc.mode, tc.origins, err, tc.valid)
				}
			}
		})
	}
}
