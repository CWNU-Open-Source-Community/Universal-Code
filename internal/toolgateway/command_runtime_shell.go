package toolgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/runner"
)

const DefaultCommandRuntimeForegroundMillis = 10_000

// The simple model-facing form is lowered before durable call IDs, approval,
// executable pinning and dispatch. The recorded protocol remains v2.
func NormalizeCommandRuntimePayloadForAdapter(payload json.RawMessage, adapter commandruntimeadapter.Identity) (json.RawMessage, error) {
	fields, err := structuredPayloadFields(payload)
	if err != nil {
		return nil, err
	}
	if _, simple := fields["command"]; !simple {
		_, canonical, err := normalizeCommandRuntimePayload(payload)
		return canonical, err
	}
	if adapter.Kind != commandruntimeadapter.KindHostUnsandboxed || adapter.Validate() != nil {
		return nil, errors.New("default-shell commands require the currently advertised native host adapter")
	}
	input, err := decodeStructuredPayload[struct {
		Command         string  `json:"command"`
		Cwd             *string `json:"cwd,omitempty"`
		Timeout         *int64  `json:"timeout,omitempty"`
		RunInBackground *bool   `json:"run_in_background,omitempty"`
	}](payload)
	if err != nil {
		return nil, err
	}
	for name := range fields {
		if _, nonNull := structuredPayloadField(fields, name); !nonNull {
			return nil, fmt.Errorf("command runtime %s cannot be null", name)
		}
	}
	cwd := "."
	if input.Cwd != nil {
		cwd = *input.Cwd
	}
	background := input.RunInBackground != nil && *input.RunInBackground
	timeout := int64(DefaultCommandRuntimeForegroundMillis)
	action, workspaceMode := CommandRuntimeActionRun, runner.CommandRuntimeWorkspaceExclusive
	if background {
		timeout = runner.MaxCommandRuntimeTimeout.Milliseconds()
		action, workspaceMode = CommandRuntimeActionStart, runner.CommandRuntimeWorkspaceSharedService
	}
	if input.Timeout != nil {
		timeout = *input.Timeout
	}
	command := runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, WorkspaceMode: workspaceMode,
		Profile: runner.DefaultCommandRuntimeShellProfile(), Script: input.Command,
		WorkingDirectory: cwd, Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: timeout,
		Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
		Network:             runner.CommandRuntimeNetworkHost, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "Run the requested command",
	}
	canonical := CommandRuntimeInput{Version: CommandRuntimeToolProtocolVersion, Action: action,
		Commands: []runner.CommandRuntimeSpec{command}}
	if !background {
		maxBytes := 4096
		canonical.FailurePolicy, canonical.MaxBytes = CommandRuntimeFailFast, &maxBytes
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	_, normalized, err := normalizeCommandRuntimePayload(raw)
	return normalized, err
}

func commandRuntimeDefaultShellDefinition(definition ToolDefinition) ToolDefinition {
	simple := json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string","minLength":1,"maxLength":65536,"description":"Command text in the host-selected default shell: PowerShell on Windows, Bash on other native hosts. Programs are resolved by that shell; a missing program or nonzero exit is returned as a failed Job result."},"cwd":{"type":"string","minLength":1,"maxLength":4096,"description":"Workspace-relative working directory. Defaults to the workspace root (.)."},"timeout":{"type":"integer","minimum":1,"maximum":%d,"description":"Milliseconds. Foreground defaults to %d and permits at most %d. Background defaults to %d."},"run_in_background":{"type":"boolean","description":"Default false. True starts a Run-owned shared-service Job that can coexist with authorized edits. Read/wait/cancel that original job_id using the version/action form."}},"allOf":[{"if":{"properties":{"run_in_background":{"const":true}},"required":["run_in_background"]},"then":{},"else":{"properties":{"timeout":{"maximum":%d}}}}]}`, runner.MaxCommandRuntimeTimeout.Milliseconds(), DefaultCommandRuntimeForegroundMillis, MaxCommandRuntimeForegroundMillis, runner.MaxCommandRuntimeTimeout.Milliseconds(), MaxCommandRuntimeForegroundMillis))
	type inputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		AllOf      json.RawMessage            `json:"allOf"`
	}
	var legacySchema, simpleSchema inputSchema
	if err := json.Unmarshal(definition.InputSchema, &legacySchema); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(simple, &simpleSchema); err != nil {
		panic(err)
	}
	type inputBranch struct {
		Required      []string        `json:"required"`
		AllOf         json.RawMessage `json:"allOf"`
		PropertyNames struct {
			Enum []string `json:"enum"`
		} `json:"propertyNames"`
	}
	branches := make([]inputBranch, 0, 2)
	for _, schema := range []inputSchema{simpleSchema, legacySchema} {
		branch := inputBranch{Required: schema.Required, AllOf: schema.AllOf}
		for key := range schema.Properties {
			branch.PropertyNames.Enum = append(branch.PropertyNames.Enum, key)
		}
		sort.Strings(branch.PropertyNames.Enum)
		branches = append(branches, branch)
	}
	for key, value := range simpleSchema.Properties {
		legacySchema.Properties[key] = value
	}
	encoded, err := json.Marshal(struct {
		Type                 string                     `json:"type"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		OneOf                []inputBranch              `json:"oneOf"`
	}{"object", false, legacySchema.Properties, branches})
	if err != nil {
		panic(err)
	}
	definition.InputSchema = encoded
	definition.Description = "Run a command with {command, cwd?, timeout?, run_in_background?}. The host chooses PowerShell on Windows or Bash on other native hosts; do not supply executable paths or shell boilerplate for this form. cwd defaults to the workspace root; timeout is milliseconds. A failed command returns its Job exit/output for correction. For a preview/dev server set run_in_background=true, then observe or cancel the returned original Job; do not start it again to read output. Current permissions and exact review still apply. The version/action form remains available for Job management and advanced explicit commands. " + definition.Description
	return definition
}
