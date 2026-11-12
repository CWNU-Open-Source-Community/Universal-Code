package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/runner"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestCommandRuntimeDefaultShellPublishedSchemaAndNormalization(t *testing.T) {
	adapter := commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
	definition := CommandRuntimeDefinitionForAdapter(adapter)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("default-shell.json", bytes.NewReader(definition.InputSchema)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("default-shell.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"default", `{"command":"echo ok"}`, true},
		{"foreground", `{"command":"echo ok","cwd":"src","timeout":25000,"run_in_background":false}`, true},
		{"background", `{"command":"node server.cjs","run_in_background":true}`, true},
		{"background_timeout", `{"command":"node server.cjs","timeout":40000,"run_in_background":true}`, true},
		{"empty", `{"command":""}`, false},
		{"null_command", `{"command":null}`, false},
		{"null_cwd", `{"command":"echo ok","cwd":null}`, false},
		{"null_timeout", `{"command":"echo ok","timeout":null}`, false},
		{"null_background", `{"command":"echo ok","run_in_background":null}`, false},
		{"empty_cwd", `{"command":"echo ok","cwd":""}`, false},
		{"mixed", `{"command":"echo ok","version":"command-runtime.v2","action":"run"}`, false},
		{"shell_override", `{"command":"echo ok","shell":"cmd.exe"}`, false},
		{"foreground_long", `{"command":"echo ok","timeout":25001}`, false},
		{"foreground_false_long", `{"command":"echo ok","timeout":25001,"run_in_background":false}`, false},
		{"negative_timeout", `{"command":"echo ok","timeout":-1}`, false},
		{"fractional_timeout", `{"command":"echo ok","timeout":1.5}`, false},
		{"background_long", `{"command":"echo ok","timeout":1800001,"run_in_background":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.payload), &value); err != nil {
				t.Fatal(err)
			}
			schemaErr := schema.Validate(value)
			canonical, err := NormalizeCommandRuntimePayloadForAdapter([]byte(tc.payload), adapter)
			if (schemaErr == nil) != tc.valid || (err == nil) != tc.valid {
				t.Fatalf("schema/runtime mismatch valid=%t schema=%v normalizer=%v", tc.valid, schemaErr, err)
			}
			if !tc.valid {
				return
			}
			input, durable, err := normalizeCommandRuntimePayload(canonical)
			if err != nil || !bytes.Equal(canonical, durable) || len(input.Commands) != 1 {
				t.Fatalf("not a canonical durable v2 call: %s err=%v", canonical, err)
			}
			command := input.Commands[0]
			if command.Profile != runner.DefaultCommandRuntimeShellProfile() || command.Network != runner.CommandRuntimeNetworkHost ||
				command.Executable != "" || command.Arguments != nil || command.Environment == nil ||
				command.StdinPolicy != runner.CommandRuntimeStdinClosed || !command.CloseInitialStdin || command.Credentials != runner.CommandRuntimeCredentialsNone {
				t.Fatalf("unsafe or incorrect host defaults: %+v", command)
			}
			background := strings.HasPrefix(tc.name, "background")
			if background != (input.Action == CommandRuntimeActionStart) || background != (command.WorkspaceMode == runner.CommandRuntimeWorkspaceSharedService) {
				t.Fatalf("incorrect job/workspace mode: %+v", input)
			}
			if tc.name == "default" && (command.WorkingDirectory != "." || command.TimeoutMilliseconds != 10000) {
				t.Fatal("foreground defaults changed")
			}
			if tc.name == "background" && command.TimeoutMilliseconds != 1800000 {
				t.Fatal("background default changed")
			}
		})
	}
	for _, raw := range []json.RawMessage{
		commandRuntimeObservationPayload("read", new(int)),
		commandRuntimeObservationPayload("wait", intPointer(3000)),
		commandRuntimeObservationPayload("cancel", intPointer(1000)),
	} {
		var value any
		_ = json.Unmarshal(raw, &value)
		if err := schema.Validate(value); err != nil {
			t.Fatalf("published mixed schema rejects existing Job API: %s %v", raw, err)
		}
		if _, err := NormalizeCommandRuntimePayloadForAdapter(raw, adapter); err != nil {
			t.Fatal(err)
		}
	}
}

func intPointer(value int) *int { return &value }

func TestCommandRuntimeDefaultShellDoesNotSelectHostForGuest(t *testing.T) {
	guest := commandruntimeadapter.SandboxedWorkspace("docker", "guest", strings.Repeat("a", 64))
	for _, adapter := range []commandruntimeadapter.Identity{guest, {}} {
		if _, err := NormalizeCommandRuntimePayloadForAdapter([]byte(`{"command":"echo ok"}`), adapter); err == nil {
			t.Fatal("simple form selected a host for an unbound/guest adapter")
		}
	}
	if strings.Contains(string(CommandRuntimeDefinitionForAdapter(guest).InputSchema), `"run_in_background"`) {
		t.Fatal("guest was offered native host shell form")
	}
}
