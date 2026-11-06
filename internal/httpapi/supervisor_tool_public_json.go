package httpapi

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

const recordRedactedValue = "[REDACTED:sensitive-field]"

var recordUnavailableJSON = json.RawMessage(`{"redacted":true,"unavailable":true}`)

// publicSupervisorToolJSON preserves the diagnostic shape of a durable tool
// payload/result while ensuring the HTTP record boundary never
// receives the raw JSON. Secret-bearing fields are removed structurally and
// every remaining string passes through the common secret redactor.
func publicSupervisorToolJSON(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	projected := redactSupervisorToolJSONValue(value, "")
	encoded, err := json.Marshal(projected)
	if err != nil {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	return encoded
}

// publicSupervisorToolCallPayload prevents the record details from becoming
// a raw durable-payload viewer. Command runtime calls retain an explicit,
// secret-free execution specification. Every other P0 tool is converted to the
// same typed facts used by the v2 conversation; legacy proposal tools expose no
// raw JSON and remain inspectable through their dedicated control surfaces.
func publicSupervisorToolCallPayload(call domain.SupervisorToolCall) json.RawMessage {
	if call.ToolName == string(toolgateway.CommandRuntimeTool) {
		return publicCommandRuntimePayload(call.PayloadJSON)
	}
	facts, found, err := application.ProjectThreadActivityToolFacts(call)
	if err != nil || !found {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	return marshalRecordJSON(struct {
		Version       string           `json:"version"`
		Kind          string           `json:"kind"`
		Operation     string           `json:"operation"`
		Target        string           `json:"target,omitempty"`
		Parameters    []recordToolFact `json:"parameters"`
		Authorization string           `json:"authorization"`
	}{Version: facts.Version, Kind: facts.Kind, Operation: facts.Operation,
		Target: facts.Target, Parameters: recordToolFacts(facts.Parameters),
		Authorization: facts.Authorization})
}

func publicSupervisorToolCallResult(call domain.SupervisorToolCall) json.RawMessage {
	if strings.TrimSpace(call.ResultJSON) == "" {
		return nil
	}
	if call.ToolName == string(toolgateway.CommandRuntimeTool) {
		return publicCommandRuntimeResult(call)
	}
	facts, found, err := application.ProjectThreadActivityToolFacts(call)
	if err != nil || !found {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	return marshalRecordJSON(struct {
		Version     string           `json:"version"`
		Kind        string           `json:"kind"`
		Result      []recordToolFact `json:"result"`
		ErrorCode   string           `json:"error_code,omitempty"`
		ArtifactRef string           `json:"artifact_ref,omitempty"`
		Untrusted   bool             `json:"untrusted"`
		Redacted    bool             `json:"redacted"`
	}{Version: facts.Version, Kind: facts.Kind, Result: recordToolFacts(facts.Result),
		ErrorCode: facts.ErrorCode, ArtifactRef: facts.ArtifactRef,
		Untrusted: facts.Untrusted, Redacted: true})
}

type recordToolFact struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Value string `json:"value"`
}

func recordToolFacts(values []application.ThreadActivityFactField) []recordToolFact {
	projected := make([]recordToolFact, len(values))
	for index, value := range values {
		projected[index] = recordToolFact{
			Name: value.Name, Label: value.Label, Value: value.Value}
	}
	return projected
}

type recordCommandRuntimePayload struct {
	Version          string                     `json:"version"`
	Action           string                     `json:"action"`
	Commands         []recordCommandRuntimeSpec `json:"commands,omitempty"`
	FailurePolicy    string                     `json:"failure_policy,omitempty"`
	JobID            string                     `json:"job_id,omitempty"`
	Cursor           *uint64                    `json:"cursor,omitempty"`
	MaxBytes         *int                       `json:"max_bytes,omitempty"`
	WaitMilliseconds *int                       `json:"wait_milliseconds,omitempty"`
	StdinProvided    bool                       `json:"stdin_provided"`
	CloseStdin       *bool                      `json:"close_stdin,omitempty"`
	Redacted         bool                       `json:"redacted"`
}

type recordCommandRuntimeSpec struct {
	Version              string                            `json:"version"`
	Profile              string                            `json:"profile"`
	Executable           string                            `json:"executable,omitempty"`
	Arguments            []string                          `json:"arguments,omitempty"`
	Script               string                            `json:"script,omitempty"`
	WorkingDirectory     string                            `json:"working_directory"`
	EnvironmentNames     []string                          `json:"environment_names"`
	StdinPolicy          string                            `json:"stdin_policy"`
	InitialStdinProvided bool                              `json:"initial_stdin_provided"`
	CloseInitialStdin    bool                              `json:"close_initial_stdin"`
	TimeoutMilliseconds  int64                             `json:"timeout_milliseconds"`
	Output               runner.CommandRuntimeOutputPolicy `json:"output"`
	Network              string                            `json:"network"`
	Credentials          string                            `json:"credentials"`
}

func publicCommandRuntimePayload(raw string) json.RawMessage {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input toolgateway.CommandRuntimeInput
	if decoder.Decode(&input) != nil {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || input.Validate() != nil {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	commands := make([]recordCommandRuntimeSpec, len(input.Commands))
	for index, spec := range input.Commands {
		normalized, err := runner.NormalizeCommandRuntimeIntent(spec)
		if err != nil {
			return append(json.RawMessage(nil), recordUnavailableJSON...)
		}
		environmentNames := make([]string, len(normalized.Environment))
		for environmentIndex, item := range normalized.Environment {
			environmentNames[environmentIndex] = redact.String(item.Name)
		}
		arguments := make([]string, len(normalized.Arguments))
		for argumentIndex, argument := range normalized.Arguments {
			arguments[argumentIndex] = redact.String(argument)
		}
		executable := normalized.Executable
		if executable != "" {
			executable = filepath.Base(executable)
		}
		commands[index] = recordCommandRuntimeSpec{
			Version: normalized.Version, Profile: string(normalized.Profile),
			Executable: redact.String(executable), Arguments: arguments,
			Script: redact.String(normalized.Script), WorkingDirectory: normalized.WorkingDirectory,
			EnvironmentNames: environmentNames, StdinPolicy: string(normalized.StdinPolicy),
			InitialStdinProvided: normalized.InitialStdin != "",
			CloseInitialStdin:    normalized.CloseInitialStdin,
			TimeoutMilliseconds:  normalized.TimeoutMilliseconds, Output: normalized.Output,
			Network: string(normalized.Network), Credentials: string(normalized.Credentials),
		}
	}
	return marshalRecordJSON(recordCommandRuntimePayload{
		Version: input.Version, Action: input.Action, Commands: commands,
		FailurePolicy: input.FailurePolicy, JobID: input.JobID, Cursor: input.Cursor,
		MaxBytes: input.MaxBytes, WaitMilliseconds: input.WaitMilliseconds,
		StdinProvided: input.Stdin != nil, CloseStdin: input.CloseStdin, Redacted: true,
	})
}

func publicCommandRuntimeResult(call domain.SupervisorToolCall) json.RawMessage {
	var envelope struct {
		Version   string `json:"version"`
		Tool      string `json:"tool"`
		Status    string `json:"status"`
		Code      string `json:"code"`
		Truncated bool   `json:"truncated"`
	}
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil ||
		envelope.Version != "supervisor_tool_result.v1" ||
		envelope.Tool != string(toolgateway.CommandRuntimeTool) ||
		envelope.Status != string(call.Status) {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	code := publicRecordErrorCode(envelope.Code)
	return marshalRecordJSON(struct {
		Version   string `json:"version"`
		Tool      string `json:"tool"`
		Status    string `json:"status"`
		ErrorCode string `json:"error_code,omitempty"`
		Truncated bool   `json:"truncated"`
		Redacted  bool   `json:"redacted"`
	}{Version: envelope.Version, Tool: envelope.Tool, Status: envelope.Status,
		ErrorCode: code, Truncated: envelope.Truncated, Redacted: true})
}

func publicRecordErrorCode(value string) string {
	if publicRunEventEnum("error_code", strings.TrimSpace(value)) {
		return strings.TrimSpace(value)
	}
	return ""
}

func marshalRecordJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return append(json.RawMessage(nil), recordUnavailableJSON...)
	}
	return encoded
}

func redactSupervisorToolJSONValue(value any, field string) any {
	if sensitiveSupervisorToolField(field) {
		return recordRedactedValue
	}
	switch typed := value.(type) {
	case map[string]any:
		projected := make(map[string]any, len(typed))
		for key, child := range typed {
			// JSON object names are data too. A hostile provider or MCP server can
			// place a credential in a field name, where value-only redaction would
			// otherwise leave it visible in Record. Omit such entries rather
			// than replacing the key and risking collisions between several
			// redacted fields.
			redactedKey := redact.Text(key)
			if len(redactedKey.Findings) > 0 || redactedKey.Text != key {
				continue
			}
			projected[key] = redactSupervisorToolJSONValue(child, key)
		}
		return projected
	case []any:
		projected := make([]any, len(typed))
		for index, child := range typed {
			projected[index] = redactSupervisorToolJSONValue(child, field)
		}
		return projected
	case string:
		return redact.String(typed)
	default:
		return typed
	}
}

func sensitiveSupervisorToolField(value string) bool {
	var normalized strings.Builder
	for _, current := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			normalized.WriteRune(current)
		}
	}
	field := normalized.String()
	switch field {
	case "authorization", "proxyauthorization", "apikey", "accesstoken",
		"refreshtoken", "token", "secret", "clientsecret", "password",
		"passwd", "privatekey", "credential", "credentials", "cookie",
		"cookies", "setcookie", "environment", "environmentvalues", "env",
		"stdin", "initialstdin", "rawstdin", "inputstream", "header", "headers",
		"requestheaders", "responseheaders", "httpheaders":
		return true
	}
	// Custom providers and MCP servers frequently use prefixed secret fields
	// such as x-api-key, github_token or anthropic-auth-token. Exact-name-only
	// filtering would make Record a credential exfiltration surface.
	return strings.HasSuffix(field, "apikey") || strings.HasSuffix(field, "token") ||
		strings.HasSuffix(field, "password") || strings.HasSuffix(field, "passwd") ||
		strings.HasSuffix(field, "secret") || strings.Contains(field, "credential") ||
		strings.HasSuffix(field, "privatekey") || strings.HasSuffix(field, "cookie")
}
