package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const MaxStructuredMemoryPayloadBytes = 96 * 1024

type WorkItemCreateInput struct {
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	Owner              string   `json:"owner,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Dependencies       []string `json:"dependencies,omitempty"`
}

type NoteCreateInput struct {
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Category    string   `json:"category,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	SourceRefs  []string `json:"source_refs,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Pinned      bool     `json:"pinned,omitempty"`
}

type StructuredMemoryContext struct {
	Tool            ToolName
	InvocationID    string
	OperationKey    string
	RunID           string
	AgentID         string
	SessionID       string
	WorkspaceID     string
	LeaseID         string
	LeaseGeneration int64
	RequestedBy     string
	PolicyDecision  Decision
}

func (c StructuredMemoryContext) Validate() error {
	if c.Tool != WorkItemCreateTool && c.Tool != NoteCreateTool {
		return fmt.Errorf("unsupported structured memory tool %q", c.Tool)
	}
	for label, value := range map[string]string{
		"invocation id": c.InvocationID, "operation key": c.OperationKey, "run id": c.RunID,
		"agent id":   c.AgentID,
		"session id": c.SessionID, "workspace id": c.WorkspaceID, "requester": c.RequestedBy,
		"lease id": c.LeaseID,
	} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value || len([]rune(value)) > MaxToolIdentityRunes {
			return fmt.Errorf("structured memory %s must be normalized and bounded UTF-8", label)
		}
	}
	if c.InvocationID == "" || c.OperationKey == "" || c.RunID == "" || c.SessionID == "" || c.RequestedBy == "" {
		return errors.New("structured memory invocation, operation, Run, Session, and requester are required")
	}
	if c.AgentID != "" && !domain.ValidAgentID(c.AgentID) {
		return errors.New("structured memory Agent identity is invalid")
	}
	if (c.LeaseID == "") != (c.LeaseGeneration == 0) || c.LeaseGeneration < 0 {
		return errors.New("structured memory execution lease identity and generation are inconsistent")
	}
	if c.RequestedBy == "run_supervisor" && (c.LeaseID == "" || c.AgentID == "") {
		return errors.New("supervisor structured memory execution requires a Run lease and Agent identity")
	}
	if err := c.PolicyDecision.Validate(); err != nil {
		return err
	}
	if !c.PolicyDecision.Allowed || c.PolicyDecision.Approval != ApprovalAutomatic {
		return errors.New("structured memory execution requires an automatic allowed decision")
	}
	return nil
}

type StructuredMutationResult struct {
	EntityID   string
	EntityKind string
	Status     string
	Version    int64
	Replayed   bool
}

func (r StructuredMutationResult) Validate() error {
	for label, value := range map[string]string{
		"entity id": r.EntityID, "entity kind": r.EntityKind, "status": r.Status,
	} {
		if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
			len([]rune(value)) > MaxToolIdentityRunes {
			return fmt.Errorf("structured mutation %s must be normalized and bounded UTF-8", label)
		}
	}
	if r.EntityKind != "work_item" && r.EntityKind != "note" {
		return fmt.Errorf("invalid structured mutation entity kind %q", r.EntityKind)
	}
	if r.Version <= 0 {
		return errors.New("structured mutation result version must be positive")
	}
	return nil
}

type StructuredMemoryExecutor interface {
	CreateWorkItem(ctx context.Context, scope StructuredMemoryContext, input WorkItemCreateInput) (StructuredMutationResult, error)
	CreateNote(ctx context.Context, scope StructuredMemoryContext, input NoteCreateInput) (StructuredMutationResult, error)
}

var structuredMemoryDefinitions = []ToolDefinition{
	{
		Name: WorkItemCreateTool, Class: ClassRunMemory, Approval: ApprovalAutomatic,
		Description: "Create one pending WorkItem on the current Run work board. For a status report or handoff, describe previously recorded tasks in the reply. Add an item during that turn only if the current request asks for a board entry.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"string","minLength":1,"maxLength":240},"description":{"type":"string","maxLength":8192},"priority":{"type":"string","enum":["low","normal","high","critical"]},"owner":{"type":"string","maxLength":128},"acceptance_criteria":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":8192}},"dependencies":{"type":"array","maxItems":64,"items":{"type":"string","pattern":"^work-[0-9]{14}(-[0-9a-f]{12})?$"}}}}`),
	},
	{
		Name: NoteCreateTool, Class: ClassRunMemory, Approval: ApprovalAutomatic,
		Description: "Create one active, secret-redacted Note in the current Run memory.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["title","content"],"properties":{"title":{"type":"string","minLength":1,"maxLength":240},"content":{"type":"string","minLength":1,"maxLength":65536,"x-maxBytes":65536},"category":{"type":"string","enum":["observation","hypothesis","decision","summary","reference"]},"visibility":{"type":"string","enum":["run","root","owner"]},"owner":{"type":"string","maxLength":128},"tags":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":64}},"source_refs":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":512}},"evidence_ids":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1,"maxLength":128}},"pinned":{"type":"boolean"}}}`),
	},
}

func StructuredMemoryToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, len(structuredMemoryDefinitions))
	for index, definition := range structuredMemoryDefinitions {
		definitions[index] = definition
		definitions[index].InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	}
	return definitions
}

func StructuredMemoryToolDefinition(name ToolName) (ToolDefinition, bool) {
	for _, definition := range structuredMemoryDefinitions {
		if definition.Name == name {
			definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
			return definition, true
		}
	}
	return ToolDefinition{}, false
}

func NormalizeStructuredMemoryPayload(name ToolName, payload json.RawMessage) (json.RawMessage, error) {
	var canonical json.RawMessage
	var err error
	switch name {
	case WorkItemCreateTool:
		_, canonical, err = decodeWorkItemCreateInput(payload)
	case NoteCreateTool:
		_, canonical, err = decodeNoteCreateInput(payload)
	default:
		return nil, fmt.Errorf("unsupported structured memory tool %q", name)
	}
	if err != nil {
		return nil, err
	}
	safe := redactRunMutationPayload(name, canonical)
	switch name {
	case WorkItemCreateTool:
		_, canonical, err = decodeWorkItemCreateInput(safe)
	case NoteCreateTool:
		_, canonical, err = decodeNoteCreateInput(safe)
	}
	if err != nil {
		return nil, fmt.Errorf("redacted structured memory payload is invalid: %w", err)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(canonical)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("canonical structured memory payload is invalid: %w", err)
	}
	canonical, err = json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical structured memory payload: %w", err)
	}
	return canonical, nil
}

func (g *Gateway) WithStructuredMemoryExecutor(executor StructuredMemoryExecutor) *Gateway {
	if g != nil {
		g.structuredMemory = executor
	}
	return g
}

func (g *Gateway) invokeStructuredMemory(ctx context.Context, call ToolCall) (Outcome, error) {
	if g.structuredMemory == nil {
		return Outcome{}, errors.New("structured memory executor is required")
	}
	var (
		workInput WorkItemCreateInput
		noteInput NoteCreateInput
		canonical json.RawMessage
		err       error
	)
	switch call.Name {
	case WorkItemCreateTool:
		workInput, canonical, err = decodeWorkItemCreateInput(call.Payload)
	case NoteCreateTool:
		noteInput, canonical, err = decodeNoteCreateInput(call.Payload)
	default:
		return Outcome{}, fmt.Errorf("unsupported structured memory tool %q", call.Name)
	}
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	policyDecision := g.checker.CheckToolCall(tools.Call{
		Name: string(call.Name), Args: map[string]string{"payload": string(canonical)},
	})
	if !policyDecision.Allowed {
		if err := g.recordStructuredMemoryPolicyDecision(ctx, call, policyDecision); err != nil {
			return Outcome{}, err
		}
		return deniedOutcome(call, policyDecision)
	}
	if policyDecision.NeedsApproval {
		policyDecision.Allowed = false
		policyDecision.Risk = defaultRisk(policyDecision.Risk, "medium")
		policyDecision.Reason = "structured memory mutation required approval and was not applied: " + policyDecision.Reason
		if err := g.recordStructuredMemoryPolicyDecision(ctx, call, policyDecision); err != nil {
			return Outcome{}, err
		}
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	scope := StructuredMemoryContext{
		Tool: call.Name, InvocationID: call.InvocationID, OperationKey: call.OperationKey,
		RunID: call.RunID, AgentID: call.AgentID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration,
		RequestedBy: call.RequestedBy, PolicyDecision: decision,
	}
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	var result StructuredMutationResult
	switch call.Name {
	case WorkItemCreateTool:
		result, err = g.structuredMemory.CreateWorkItem(ctx, scope, workInput)
	case NoteCreateTool:
		result, err = g.structuredMemory.CreateNote(ctx, scope, noteInput)
	}
	if err != nil {
		return Outcome{}, err
	}
	if err := result.Validate(); err != nil {
		return Outcome{}, err
	}
	completed := time.Now().UTC()
	outcome := Outcome{
		Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "run_memory", Status: StatusCompleted, StartedAt: started, CompletedAt: &completed},
		Result: &Result{
			Status: StatusCompleted, ExitCode: 0, MIME: "application/json", CompletedAt: completed,
			Metadata: map[string]string{
				"entity_id": result.EntityID, "entity_kind": result.EntityKind,
				"status": result.Status, "version": strconv.FormatInt(result.Version, 10),
				"replayed": strconv.FormatBool(result.Replayed),
			},
		},
	}
	return validateOutcome(outcome, nil)
}

func (g *Gateway) recordStructuredMemoryPolicyDecision(ctx context.Context, call ToolCall,
	decision policy.Decision,
) error {
	if g == nil || g.policyRecorder == nil {
		return errors.New("structured memory policy decision recorder is required")
	}
	return g.policyRecorder.RecordPolicyDecision(ctx, policy.DecisionRecord{
		SessionID: call.SessionID, SubjectID: call.InvocationID,
		Context: "tool_run." + string(call.Name), Decision: decision,
	})
}

func decodeWorkItemCreateInput(payload json.RawMessage) (WorkItemCreateInput, json.RawMessage, error) {
	input, err := decodeStructuredPayload[WorkItemCreateInput](payload)
	if err != nil {
		return WorkItemCreateInput{}, nil, err
	}
	priority, err := domain.ParseWorkItemPriority(input.Priority)
	if err != nil {
		return WorkItemCreateInput{}, nil, errors.New("invalid structured WorkItem priority")
	}
	details, err := domain.NormalizeWorkItemDetails("", domain.WorkItemDetails{
		Title: input.Title, Description: input.Description, Priority: priority, Owner: input.Owner,
		AcceptanceCriteria: slices.Clone(input.AcceptanceCriteria), Dependencies: slices.Clone(input.Dependencies),
	})
	if err != nil {
		return WorkItemCreateInput{}, nil, err
	}
	for index, dependencyID := range details.Dependencies {
		if !validStructuredWorkItemID(dependencyID) {
			return WorkItemCreateInput{}, nil,
				fmt.Errorf("invalid structured WorkItem dependency id at index %d", index)
		}
	}
	input = WorkItemCreateInput{
		Title: details.Title, Description: details.Description, Priority: string(details.Priority), Owner: details.Owner,
		AcceptanceCriteria: details.AcceptanceCriteria, Dependencies: details.Dependencies,
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}

func validStructuredWorkItemID(value string) bool {
	if !strings.HasPrefix(value, "work-") || (len(value) != 19 && len(value) != 32) {
		return false
	}
	for _, current := range value[5:19] {
		if current < '0' || current > '9' {
			return false
		}
	}
	if len(value) == 19 {
		return true
	}
	if value[19] != '-' {
		return false
	}
	for _, current := range value[20:] {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func decodeNoteCreateInput(payload json.RawMessage) (NoteCreateInput, json.RawMessage, error) {
	input, err := decodeStructuredPayload[NoteCreateInput](payload)
	if err != nil {
		return NoteCreateInput{}, nil, err
	}
	category, err := domain.ParseNoteCategory(input.Category)
	if err != nil {
		return NoteCreateInput{}, nil, errors.New("invalid structured Note category")
	}
	visibility, err := domain.ParseNoteVisibility(input.Visibility)
	if err != nil {
		return NoteCreateInput{}, nil, errors.New("invalid structured Note visibility")
	}
	details, err := domain.NormalizeNoteDetails(domain.NoteDetails{
		Title: input.Title, Content: input.Content, Category: category, Visibility: visibility,
		Owner: input.Owner, Tags: slices.Clone(input.Tags), SourceRefs: slices.Clone(input.SourceRefs),
		EvidenceIDs: slices.Clone(input.EvidenceIDs), Pinned: input.Pinned,
	})
	if err != nil {
		return NoteCreateInput{}, nil, err
	}
	input = NoteCreateInput{
		Title: details.Title, Content: details.Content, Category: string(details.Category),
		Visibility: string(details.Visibility), Owner: details.Owner, Tags: details.Tags,
		SourceRefs: details.SourceRefs, EvidenceIDs: details.EvidenceIDs, Pinned: details.Pinned,
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}

func decodeStructuredPayload[T any](payload json.RawMessage) (T, error) {
	var zero T
	if len(payload) == 0 || len(payload) > MaxStructuredMemoryPayloadBytes || !utf8.Valid(payload) {
		return zero, fmt.Errorf("invalid structured memory payload: must be valid UTF-8 JSON and at most %d bytes",
			MaxStructuredMemoryPayloadBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("invalid structured memory payload: %s", redact.String(err.Error()))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return zero, errors.New("invalid structured memory payload: trailing JSON")
		}
		return zero, fmt.Errorf("invalid structured memory payload trailing data: %s", redact.String(err.Error()))
	}
	return value, nil
}

func structuredPayloadFields(payload json.RawMessage) (
	map[string]json.RawMessage, error,
) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, errors.New("invalid structured memory payload: expected a JSON object")
	}
	return fields, nil
}

func structuredPayloadField(fields map[string]json.RawMessage,
	name string,
) (present bool, nonNull bool) {
	value, present := fields[name]
	return present, present && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func redactRunMutationPayload(name ToolName, payload json.RawMessage) json.RawMessage {
	switch name {
	case WorkItemCreateTool:
		input, _, err := decodeWorkItemCreateInput(payload)
		if err != nil {
			break
		}
		input.Title = redact.String(input.Title)
		input.Description = redact.String(input.Description)
		input.Owner = redact.String(input.Owner)
		for index := range input.AcceptanceCriteria {
			input.AcceptanceCriteria[index] = redact.String(input.AcceptanceCriteria[index])
		}
		encoded, err := json.Marshal(input)
		if err == nil {
			return encoded
		}
	case NoteCreateTool:
		input, _, err := decodeNoteCreateInput(payload)
		if err != nil {
			break
		}
		input.Title = redact.String(input.Title)
		input.Content = redact.String(input.Content)
		input.Owner = redact.String(input.Owner)
		for _, values := range [][]string{input.Tags, input.SourceRefs, input.EvidenceIDs} {
			for index := range values {
				values[index] = redact.String(values[index])
			}
		}
		encoded, err := json.Marshal(input)
		if err == nil {
			return encoded
		}
	case SpecialistDelegationProposeTool:
		spec, err := domain.DecodeSpecialistDelegationSpec(payload)
		if err != nil {
			break
		}
		for index := range spec.Assignments {
			spec.Assignments[index].Title = redact.String(spec.Assignments[index].Title)
			spec.Assignments[index].Goal = redact.String(spec.Assignments[index].Goal)
		}
		encoded, err := json.Marshal(spec)
		if err == nil {
			return encoded
		}
	case PlanDeliveryProposeTool:
		spec, err := domain.DecodePlanDeliverySpec(payload)
		if err != nil {
			break
		}
		for directionIndex := range spec.Directions {
			direction := &spec.Directions[directionIndex]
			direction.Title = redact.String(direction.Title)
			direction.Summary = redact.String(direction.Summary)
			for index := range direction.Tradeoffs {
				direction.Tradeoffs[index] = redact.String(direction.Tradeoffs[index])
			}
			for moduleIndex := range direction.Modules {
				module := &direction.Modules[moduleIndex]
				module.Title = redact.String(module.Title)
				module.Objective = redact.String(module.Objective)
				for index := range module.AcceptanceCriteria {
					module.AcceptanceCriteria[index] = redact.String(
						module.AcceptanceCriteria[index])
				}
			}
		}
		encoded, err := json.Marshal(spec)
		if err == nil {
			return encoded
		}
	}
	return json.RawMessage(`{"redacted":true}`)
}
