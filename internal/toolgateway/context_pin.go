package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const ContextPinProposeTool ToolName = "context_pin_propose"

type ContextPinExecutor interface {
	ProposeContextPin(context.Context, domain.ContextPinProposalScope, domain.ContextPinProposalSpec) (domain.ContextPinProposal, bool, error)
}

var contextPinProposeDefinition = ToolDefinition{
	Name: ContextPinProposeTool, Class: ClassAgentProposal, Approval: ApprovalAutomatic,
	Description: "Propose exact text for the user to fix as guidance for this task. This persists a pending proposal only; it never activates a pin or changes permissions. source_message_id is optional and must identify an exact span of an actual user message in this Thread; omit it for a model-written suggestion. After proposing, use root action=wait for the user's pin/discussion/custom choice; do not repeat earlier tools.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","text"],"properties":{"version":{"const":"context_pin_proposal.v1"},"text":{"type":"string","minLength":1,"maxLength":8192},"question":{"type":"string","maxLength":1024},"source_message_id":{"type":"integer","minimum":1}}}`),
}

func normalizeContextPinPayload(payload json.RawMessage) (domain.ContextPinProposalSpec, json.RawMessage, error) {
	spec, err := decodeStructuredPayload[domain.ContextPinProposalSpec](payload)
	if err != nil {
		return spec, nil, err
	}
	if err := spec.Validate(); err != nil {
		return spec, nil, err
	}
	if spec.Text != redact.String(spec.Text) || spec.Question != redact.String(spec.Question) {
		return spec, nil, errors.New("remove protected secrets from the context pin proposal")
	}
	encoded, err := json.Marshal(spec)
	return spec, encoded, err
}

func (g *Gateway) WithContextPinExecutor(executor ContextPinExecutor) *Gateway {
	if g != nil {
		g.contextPins = executor
	}
	return g
}

func (g *Gateway) invokeContextPin(ctx context.Context, call ToolCall) (Outcome, error) {
	spec, canonical, err := normalizeContextPinPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	if call.RequestedBy != "run_supervisor" || call.AgentID == "" || call.LeaseID == "" || call.LeaseGeneration <= 0 {
		return Outcome{}, errors.New("context pin proposals require a fenced root Supervisor")
	}
	policyDecision := g.checker.CheckToolCall(tools.Call{Name: string(call.Name), Args: map[string]string{"payload": string(canonical)}})
	if err := g.recordChildTaskPolicyDecision(ctx, call, policyDecision); err != nil {
		return Outcome{}, err
	}
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	proposal, replayed, err := g.contextPins.ProposeContextPin(ctx, domain.ContextPinProposalScope{RunID: call.RunID, RootAgentID: call.AgentID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID, InvocationID: call.InvocationID, OperationKey: call.OperationKey, LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration, RequestedBy: call.RequestedBy}, spec)
	if err != nil {
		return Outcome{}, err
	}
	completed := time.Now().UTC()
	encoded, _ := json.Marshal(map[string]any{"proposal_id": proposal.ID, "status": proposal.Status, "version": proposal.Version, "user_confirmation_required": true, "pin_activated": false, "next_action": "wait for the user; do not repeat previous tools"})
	return validateOutcome(Outcome{Call: safeToolCall(call), Decision: decision, Execution: &Execution{Backend: "agent_proposal", Status: StatusCompleted, StartedAt: started, CompletedAt: &completed}, Result: &Result{Status: StatusCompleted, ExitCode: 0, MIME: "application/json", Stdout: string(encoded), CompletedAt: completed, Metadata: map[string]string{"proposal_id": proposal.ID, "pin_activated": "false", "replayed": strconv.FormatBool(replayed)}}}, nil)
}
