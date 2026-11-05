package projectconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestBrowserLimitsSchemaMatchesConfigValidation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "configs", "project-config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("project-config.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("project-config.json")
	if err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		name, limits string
		valid        bool
	}
	tests := []testCase{
		{"absent", "", true},
		{"empty", `{}`, false},
		{"all zero", `{"max_origins":0,"max_url_bytes":0,"max_requests":0,"max_response_bytes":0,"max_download_bytes":0,"timeout_ms":0}`, false},
		{"zero and positive", `{"max_origins":0,"max_requests":1}`, true},
		{"negative and positive", `{"max_origins":-1,"max_requests":1}`, false},
	}
	for _, field := range []string{"max_origins", "max_url_bytes", "max_requests", "max_response_bytes", "max_download_bytes", "timeout_ms"} {
		tests = append(tests,
			testCase{field + " zero", `{"` + field + `":0}`, false},
			testCase{field + " positive", `{"` + field + `":1}`, true})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := `{"protocol":"project_config.v1"`
			if tc.limits != "" {
				content += `,"browser_limits":` + tc.limits
			}
			content += `}`
			var value any
			if err := json.Unmarshal([]byte(content), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("schema validation err=%v, want valid=%t", err, tc.valid)
			}
			path := writeConfig(t, t.TempDir(), content)
			if _, err := Load(t.Context(), path); (err == nil) != tc.valid {
				t.Fatalf("Go config validation err=%v, want valid=%t", err, tc.valid)
			}
		})
	}
}
