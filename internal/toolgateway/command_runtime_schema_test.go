package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestCommandRuntimePublishedProcessAndShellContractMatchesNormalizer(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("command-profiles.json", bytes.NewReader(commandRuntimeDefinition.InputSchema)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("command-profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	base := string(commandRuntimeValidPayload("Write-Output ok"))
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"shell", `"profile":"powershell","script":"Write-Output ok"`, true},
		{"missing_script", `"profile":"powershell"`, false},
		{"empty_script", `"profile":"powershell","script":""`, false},
		{"shell_with_executable", `"profile":"powershell","script":"Write-Output ok","executable":""`, false},
		{"shell_with_argv", `"profile":"powershell","script":"Write-Output ok","arguments":[]`, false},
		{"process", `"profile":"process","executable":"/usr/bin/node","arguments":["--version"]`, true},
		{"process_empty_argv", `"profile":"process","executable":"/usr/bin/node","arguments":[]`, true},
		{"relative_executable", `"profile":"process","executable":"python","arguments":["--version"]`, false},
		{"missing_executable", `"profile":"process","arguments":[]`, false},
		{"missing_argv", `"profile":"process","executable":"/usr/bin/node"`, false},
		{"null_argv", `"profile":"process","executable":"/usr/bin/node","arguments":null`, false},
		{"process_with_script", `"profile":"process","executable":"/usr/bin/node","arguments":[],"script":""`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.Replace(base, `"profile":"powershell","script":"Write-Output ok"`, tc.fields, 1))
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			schemaErr := schema.Validate(value)
			_, _, parserErr := normalizeCommandRuntimePayload(raw)
			if (schemaErr == nil) != tc.valid || (parserErr == nil) != tc.valid {
				t.Fatalf("published contract and runtime disagree: valid=%t schema=%v parser=%v", tc.valid, schemaErr, parserErr)
			}
		})
	}
}
