package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
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
	case SpecialistResultReadTool:
		_, canonical, err := NormalizeSpecialistResultRead(input.Arguments)
		if err != nil {
			return input, nil, err
		}
		input.Arguments = canonical
	case WorkspaceListTool, WorkspaceReadTool, WorkspaceGlobTool, WorkspaceGrepTool, WorkspaceChangeTool, WorkspaceApplyTool, GitHubEvidenceListTool, GitHubEvidenceReadTool:
		canonical, err := NormalizeAgentCodePayload(ToolName(input.Tool), input.Arguments)
		if err != nil {
			return input, nil, err
		}
		if input.Tool == string(WorkspaceChangeTool) {
			var change WorkspaceChangePayload
			_ = json.Unmarshal(canonical, &change)
			if change.Action != "create" && change.Action != "propose_patch" {
				return input, nil, errors.New("Batch child workspace_change supports only create or propose_patch")
			}
		} else if input.Tool == string(WorkspaceApplyTool) {
			var apply WorkspaceApplyPayload
			_ = json.Unmarshal(canonical, &apply)
			if apply.ExpectedAction != "create" && apply.ExpectedAction != "propose_patch" {
				return input, nil, errors.New("Batch child workspace_apply supports only create or propose_patch")
			}
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
		if strings.TrimSpace(args.Message) == "" || len([]rune(args.Message)) > 256 ||
			strings.ContainsAny(args.Message, "\r\n\x00") {
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
		description += " This isolated Batch child also allows workspace_change (version/action/path/expected_sha256; action=propose_patch uses replacements [{old_text,new_text,expected_occurrences}], action=create uses content and expected_sha256=missing). Review the returned Diff, then pass its apply_arguments unchanged to workspace_apply. Batch apply records authorization for the owned proposal and checks its exact hashes. git_status and git_diff take empty arguments, git_commit takes message, and verify takes empty arguments and runs only the operator-declared validations. Read exact current file hashes before proposing patches."
	}
	return ToolDefinition{Name: SpecialistTool, Description: description, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"}},"required":["tool","arguments"],"additionalProperties":false}`)}
}

// SpecialistNativeToolDefinitions reuses workspace read schemas and narrows
// mutation tools to the operations implemented by the isolated Batch executor.
// The internal specialist_tool envelope is never advertised to a provider.
func SpecialistNativeToolDefinitions(batch bool) []ToolDefinition {
	definitions := []ToolDefinition{SpecialistResultReadDefinition()}
	for _, definition := range AgentCodeToolDefinitions() {
		switch definition.Name {
		case WorkspaceListTool, WorkspaceReadTool, WorkspaceGlobTool, WorkspaceGrepTool:
			definitions = append(definitions, definition)
		case WorkspaceChangeTool, WorkspaceApplyTool:
			if batch {
				definitions = append(definitions, specialistBatchWorkspaceDefinition(definition.Name))
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
			{"git_commit", "Commit the exact owned isolated changes with a non-blank single-line message of at most 256 Unicode characters. Surrounding whitespace is trimmed; CR, LF and NUL are forbidden.", ClassWorkspaceWrite, `{"type":"object","properties":{"message":{"type":"string","minLength":1,"maxLength":256,"pattern":"^[^\\r\\n\u0000]*[^\\s\u0000\u000b\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000][^\\r\\n\u0000]*$"}},"required":["message"],"additionalProperties":false}`},
			{"verify", "Run only the operator-declared Batch validations and return real receipts.", ClassProcess, `{"type":"object","properties":{},"additionalProperties":false}`},
		} {
			definitions = append(definitions, ToolDefinition{Name: ToolName(operation.name), Description: operation.description, Class: operation.class, Approval: ApprovalAutomatic, InputSchema: json.RawMessage(operation.schema)})
		}
	}
	return definitions
}

func specialistBatchWorkspaceDefinition(name ToolName) ToolDefinition {
	if name == WorkspaceChangeTool {
		return ToolDefinition{Name: name, Class: ClassWorkspaceWrite, Approval: ApprovalPerCall,
			Description: "Prepare a patch or new file inside this isolated Batch child's owned scope. All paths are workspace-relative. For propose_patch, use replacements [{old_text,new_text,expected_occurrences}] matching exact observed text in order, and expected_sha256 from workspace_read content_sha256. For create, use UTF-8 content and expected_sha256 'missing'; empty content is allowed. This tool prepares a proposal without writing the file. The result includes ID, Operation, Diff, Status, OriginalHash, ProposedHash and apply_arguments. Review Diff, then pass apply_arguments unchanged to workspace_apply.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","action","path","expected_sha256"],"properties":{"version":{"const":"agent-code-tools.v1"},"action":{"enum":["propose_patch","create"]},"path":{"type":"string","minLength":1,"maxLength":512},"expected_sha256":{"type":"string","pattern":"^(missing|[0-9a-f]{64})$"},"content":{"type":"string","maxLength":65536},"replacements":{"type":"array","minItems":1,"maxItems":64,"items":{"type":"object","additionalProperties":false,"required":["old_text","new_text","expected_occurrences"],"properties":{"old_text":{"type":"string","minLength":1,"maxLength":32768},"new_text":{"type":"string","maxLength":32768},"expected_occurrences":{"type":"integer","minimum":1,"maximum":1024}}}}},"oneOf":[{"properties":{"action":{"const":"propose_patch"},"expected_sha256":{"pattern":"^[0-9a-f]{64}$"}},"required":["replacements"],"not":{"required":["content"]}},{"properties":{"action":{"const":"create"},"expected_sha256":{"const":"missing"}},"required":["content"],"not":{"required":["replacements"]}}]}`)}
	}
	return ToolDefinition{Name: name, Class: ClassWorkspaceWrite, Approval: ApprovalPerCall,
		Description: "Apply a reviewed Batch patch or create proposal using the exact apply_arguments returned by workspace_change. The Batch controller records authorization for the owned proposal while applying and rechecks the proposal ID, original hash and proposed hash. The result includes Status, OriginalHash and ProposedHash for the applied edit.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","edit_id","expected_action","expected_original_sha256","expected_proposed_sha256"],"properties":{"version":{"const":"agent-code-tools.v1"},"edit_id":{"type":"string","minLength":1,"maxLength":256},"expected_action":{"enum":["propose_patch","create"]},"expected_original_sha256":{"type":"string","pattern":"^(missing|[0-9a-f]{64})$"},"expected_proposed_sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"oneOf":[{"properties":{"expected_action":{"const":"propose_patch"},"expected_original_sha256":{"pattern":"^[0-9a-f]{64}$"}}},{"properties":{"expected_action":{"const":"create"},"expected_original_sha256":{"const":"missing"}}}]}`)}
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
