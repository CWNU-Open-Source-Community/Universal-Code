package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/policy"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

func commandRuntimeObservationPayload(action string, wait *int) json.RawMessage {
	payload := map[string]any{"version": CommandRuntimeToolProtocolVersion, "action": action, "job_id": "command-job-original"}
	if action != CommandRuntimeActionCancel {
		payload["cursor"], payload["max_bytes"] = 0, 16384
	}
	if wait != nil {
		payload["wait_milliseconds"] = *wait
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func TestCommandRuntimePublishedObservationSchemaMatchesActionContract(t *testing.T) {
	registered, found := SupervisorToolDefinition(CommandRuntimeTool)
	if !found {
		t.Fatal("registered tool missing")
	}
	for name, definition := range map[string]ToolDefinition{
		"registered": registered,
		"host":       CommandRuntimeDefinitionForAdapter(commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))),
	} {
		t.Run(name, func(t *testing.T) {
			compiler := jsonschema.NewCompiler()
			compiler.Draft = jsonschema.Draft2020
			if err := compiler.AddResource("command-observation.json", bytes.NewReader(definition.InputSchema)); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile("command-observation.json")
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				action         string
				wait           int
				missing, valid bool
			}{
				{"read", 0, false, true}, {"read", 3000, false, false}, {"read", 5000, false, false},
				{"wait", 0, false, false}, {"wait", 1, false, true}, {"wait", 3000, false, true},
				{"wait", 5000, false, true}, {"wait", 5001, false, false},
				{"read", 0, true, false}, {"wait", 0, true, false},
				{"cancel", 0, false, true}, {"cancel", 5000, false, true},
			} {
				wait := &tc.wait
				if tc.missing {
					wait = nil
				}
				raw := commandRuntimeObservationPayload(tc.action, wait)
				var value any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				if err := schema.Validate(value); (err == nil) != tc.valid {
					t.Fatalf("schema accepted=%v want=%v payload=%s: %v", err == nil, tc.valid, raw, err)
				}
				normalized, err := NormalizeSupervisorToolPayload(CommandRuntimeTool, raw)
				if (err == nil) != tc.valid {
					t.Fatalf("Go accepted=%v want=%v payload=%s: %v", err == nil, tc.valid, raw, err)
				}
				if tc.valid {
					var input CommandRuntimeInput
					if err := json.Unmarshal(normalized, &input); err != nil {
						t.Fatal(err)
					}
					if input.Action != tc.action || input.WaitMilliseconds == nil || *input.WaitMilliseconds != tc.wait || input.JobID != "command-job-original" {
						t.Fatal("observation was silently rewritten")
					}
				}
			}
			var published map[string]any
			if err := json.Unmarshal(definition.InputSchema, &published); err != nil {
				t.Fatal(err)
			}
			properties := published["properties"].(map[string]any)
			guidance := definition.Description + properties["wait_milliseconds"].(map[string]any)["description"].(string)
			for _, required := range []string{"action=read", "wait_milliseconds=0", "action=wait", "1 to 5000", "original job_id", "cursor"} {
				if !strings.Contains(guidance, required) {
					t.Fatalf("published observation guidance omits %q", required)
				}
			}
		})
	}
}

func TestCommandRuntimeObservationDiagnosticBeforeExecution(t *testing.T) {
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	executor := &commandRuntimeExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(executor)
	for _, tc := range []struct {
		action      string
		wait        int
		explanation string
	}{
		{"read", 3000, "action=read requires wait_milliseconds=0"},
		{"wait", 0, "action=wait requires wait_milliseconds from 1 to 5000"},
	} {
		_, err := gateway.Invoke(t.Context(), commandRuntimeToolCall(commandRuntimeObservationPayload(tc.action, &tc.wait)))
		if err == nil {
			t.Fatal("invalid observation reached execution")
		}
		for _, required := range []string{tc.explanation, "original job_id", "cursor", "action=read", "action=wait"} {
			if !strings.Contains(err.Error(), required) {
				t.Fatalf("diagnostic omits %q: %v", required, err)
			}
		}
	}
	if executor.calls != 0 || state.chargeCount() != 0 {
		t.Fatal("invalid observation executed or consumed tool budget")
	}
}
