package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const SpecialistTool ToolName = "specialist_tool"

type SpecialistToolPayload struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func NormalizeSpecialistToolPayload(raw json.RawMessage) (SpecialistToolPayload, json.RawMessage, error) {
	var input SpecialistToolPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return input, nil, errors.New("trailing Specialist tool data")
	}
	switch ToolName(input.Tool) {
	case WorkspaceListTool, WorkspaceReadTool, WorkspaceGlobTool, WorkspaceGrepTool, WorkspaceChangeTool, WorkspaceApplyTool, GitHubEvidenceListTool, GitHubEvidenceReadTool:
		canonical, err := NormalizeAgentCodePayload(ToolName(input.Tool), input.Arguments)
		if err != nil {
			return input, nil, err
		}
		input.Arguments = canonical
	case "git_status", "git_diff", "verify":
		if string(bytes.TrimSpace(input.Arguments)) != "{}" {
			return input, nil, errors.New("this child operation accepts no arguments")
		}
	case "git_commit":
		var args struct {
			Message string `json:"message"`
		}
		d := json.NewDecoder(bytes.NewReader(input.Arguments))
		d.DisallowUnknownFields()
		if err := d.Decode(&args); err != nil {
			return input, nil, err
		}
		if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return input, nil, errors.New("trailing child commit arguments")
		}
		if args.Message == "" || len(args.Message) > 4096 {
			return input, nil, errors.New("invalid child commit message")
		}
	default:
		return input, nil, errors.New("unsupported Specialist tool operation")
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}

func SpecialistToolDefinition(batch bool) ToolDefinition {
	description := "Use an independent child tool to inspect the registered project. Allowed operations: workspace_list, workspace_read, workspace_glob, workspace_grep. All workspace arguments require version=agent-code-tools.v1. Other required fields: read: path/start_line/end_line; list: path/limit, optional cursor; glob: pattern/limit, optional cursor; grep: query/pattern/limit/case_sensitive, optional cursor. List root path is an empty string, limits are 1..200. Results are bounded observations and grant no authority."
	if batch {
		description += " This isolated Batch child also allows workspace_change (version/action/path/expected_sha256; action=propose_patch uses replacements [{old_text,new_text,expected_occurrences}], action=create uses content and expected_sha256=missing), workspace_apply (version/edit_id/expected_action/expected_original_sha256/expected_proposed_sha256), git_status and git_diff (empty arguments), git_commit (message), and verify (empty arguments). verify runs only the operator-declared validations. Read exact current file hashes before changing or applying."
	}
	return ToolDefinition{Name: SpecialistTool, Description: description, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"}},"required":["tool","arguments"],"additionalProperties":false}`)}
}

// SpecialistNativeToolDefinitions reuses the authoritative workspace schemas.
// The internal specialist_tool envelope is never advertised to a provider.
func SpecialistNativeToolDefinitions(batch bool) []ToolDefinition {
	definitions := []ToolDefinition{}
	for _, definition := range AgentCodeToolDefinitions() {
		switch definition.Name {
		case WorkspaceListTool, WorkspaceReadTool, WorkspaceGlobTool, WorkspaceGrepTool:
			definitions = append(definitions, definition)
		case WorkspaceChangeTool, WorkspaceApplyTool:
			if batch {
				definitions = append(definitions, definition)
			}
		}
	}
	if batch {
		for _, operation := range []struct {
			name, description string
			class             ActionClass
			schema            string
		}{
			{"git_status", "Inspect the current isolated child checkout status.", ClassWorkspaceRead, `{"type":"object","properties":{},"additionalProperties":false}`},
			{"git_diff", "Inspect the bounded diff of the isolated child checkout.", ClassWorkspaceRead, `{"type":"object","properties":{},"additionalProperties":false}`},
			{"git_commit", "Commit the exact owned isolated changes with a concise message.", ClassWorkspaceWrite, `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`},
			{"verify", "Run only the operator-declared Batch validations and return real receipts.", ClassProcess, `{"type":"object","properties":{},"additionalProperties":false}`},
		} {
			definitions = append(definitions, ToolDefinition{Name: ToolName(operation.name), Description: operation.description, Class: operation.class, Approval: ApprovalAutomatic, InputSchema: json.RawMessage(operation.schema)})
		}
	}
	return definitions
}

func NormalizeSpecialistNativePayload(name string, arguments json.RawMessage) (json.RawMessage, error) {
	raw, err := json.Marshal(SpecialistToolPayload{Tool: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	input, _, err := NormalizeSpecialistToolPayload(raw)
	return input.Arguments, err
}

type SpecialistToolExecutor interface {
	ExecuteSpecialistTool(context.Context, ToolCall, SpecialistToolPayload) (json.RawMessage, error)
}

func (g *Gateway) WithSpecialistToolExecutor(executor SpecialistToolExecutor) *Gateway {
	g.specialist = executor
	return g
}

func validateSpecialistCall(call ToolCall) error {
	if call.RequestedBy != "specialist_runner" || call.Role != domain.AgentRoleSpecialist || call.AgentID == "" || call.AgentAttemptID == "" || call.SpecialistToolCallID == "" || call.RunID == "" || call.SessionID == "" || call.WorkspaceID == "" || call.LeaseID == "" || call.OperationKey == "" || len(call.Arguments) != 0 {
		return errors.New("Specialist tools require their recorded child attempt, call and execution lease")
	}
	_, _, err := NormalizeSpecialistToolPayload(call.Payload)
	return err
}

func (g *Gateway) invokeSpecialist(ctx context.Context, call ToolCall) (Outcome, error) {
	input, canonical, err := NormalizeSpecialistToolPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	policyDecision := g.checker.CheckToolCall(tools.Call{Name: input.Tool, Args: map[string]string{"payload": string(input.Arguments)}})
	if !policyDecision.Allowed || policyDecision.NeedsApproval {
		policyDecision.Allowed = false
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	raw, err := g.specialist.ExecuteSpecialistTool(ctx, call, input)
	if err != nil {
		return Outcome{}, err
	}
	completed := time.Now().UTC()
	// The executor returns already value-redacted JSON. Keep it valid and fail
	// closed instead of truncating a receipt into invalid structured evidence.
	if !json.Valid(raw) || len(raw) > MaxResultStdoutBytes {
		return Outcome{}, errors.New("Specialist tool result exceeds its bounded JSON receipt")
	}
	out := Outcome{Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "specialist_workspace", Status: StatusCompleted, StartedAt: started, CompletedAt: &completed},
		Result:    &Result{Status: StatusCompleted, Stdout: string(raw), ExitCode: 0, MIME: "application/json", CompletedAt: completed, Metadata: map[string]string{"agent_id": call.AgentID, "agent_attempt_id": call.AgentAttemptID, "operation": redact.String(input.Tool)}}}
	return validateOutcome(out, nil)
}
