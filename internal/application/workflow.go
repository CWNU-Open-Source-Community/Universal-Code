package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

type WorkflowStore interface {
	SubagentRunnerStore
	SaveWorkflowDefinition(context.Context, domain.WorkflowSpec) (domain.WorkflowDefinition, error)
	GetWorkflowDefinition(context.Context, string) (domain.WorkflowDefinition, error)
	ListWorkflowDefinitions(context.Context) ([]domain.WorkflowDefinition, error)
	StartWorkflowExecution(context.Context, domain.WorkflowExecution) (domain.WorkflowExecution, error)
	GetWorkflowExecution(context.Context, string) (domain.WorkflowExecution, error)
	ListWorkflowExecutions(context.Context, string) ([]domain.WorkflowExecution, error)
	ClaimWorkflowExecution(context.Context, string, string) (domain.WorkflowExecution, bool, error)
	ReleaseWorkflowExecution(context.Context, string, string) error
	CheckpointWorkflowExecution(context.Context, domain.WorkflowExecution, string) (domain.WorkflowExecution, error)
	ControlWorkflowExecution(context.Context, string, string, int64) (domain.WorkflowExecution, error)
	GetWorkflowStep(context.Context, string) (domain.WorkflowStepExecution, bool, error)
	PrepareWorkflowStep(context.Context, domain.WorkflowStepExecution, string) (domain.WorkflowStepExecution, error)
	CompleteWorkflowStep(context.Context, domain.WorkflowStepExecution, string) error
	AdmitSpecialist(context.Context, domain.SpecialistAdmission, string) (domain.AgentNode, bool, error)
	SendAgentMessage(context.Context, domain.AgentMessage, string) (domain.AgentMessage, bool, error)
	GetAgentCompletion(context.Context, string) (domain.AgentCompletion, bool, error)
	RegisterRootAgent(context.Context, string) (domain.AgentNode, bool, error)
}

// WorkflowService advances a saved program through the existing real child
// execution service. Each call runs at most one worker turn and checkpoints it.
type WorkflowService struct {
	store   WorkflowStore
	workers *SpecialistExecutionService
}

func NewWorkflowService(st WorkflowStore, workers *SpecialistExecutionService) *WorkflowService {
	return &WorkflowService{store: st, workers: workers}
}
func (s *WorkflowService) Save(ctx context.Context, spec domain.WorkflowSpec) (domain.WorkflowDefinition, error) {
	return s.store.SaveWorkflowDefinition(ctx, spec)
}
func (s *WorkflowService) Get(ctx context.Context, id string) (domain.WorkflowExecution, error) {
	return s.store.GetWorkflowExecution(ctx, id)
}
func (s *WorkflowService) Definition(ctx context.Context, id string) (domain.WorkflowDefinition, error) {
	return s.store.GetWorkflowDefinition(ctx, id)
}
func (s *WorkflowService) Definitions(ctx context.Context) ([]domain.WorkflowDefinition, error) {
	return s.store.ListWorkflowDefinitions(ctx)
}
func (s *WorkflowService) List(ctx context.Context, runID string) ([]domain.WorkflowExecution, error) {
	return s.store.ListWorkflowExecutions(ctx, runID)
}
func (s *WorkflowService) Pause(ctx context.Context, id string, revision int64) (domain.WorkflowExecution, error) {
	return s.store.ControlWorkflowExecution(ctx, id, "pause", revision)
}
func (s *WorkflowService) Resume(ctx context.Context, id string, revision int64) (domain.WorkflowExecution, error) {
	return s.store.ControlWorkflowExecution(ctx, id, "resume", revision)
}

type StartWorkflowRequest struct {
	RunID, DefinitionID, OperationKey string
	Inputs                            json.RawMessage
}

func (s *WorkflowService) Start(ctx context.Context, request StartWorkflowRequest) (domain.WorkflowExecution, error) {
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	if _, err = s.store.GetWorkflowDefinition(ctx, request.DefinitionID); err != nil {
		return domain.WorkflowExecution{}, err
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeFailedPrecondition, "workflow requires a running Run")
	}
	if _, _, err = s.store.RegisterRootAgent(ctx, run.ID); err != nil {
		return domain.WorkflowExecution{}, err
	}
	usage, err := s.store.GetRunAgentUsage(ctx, run.ID)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	nodes, err := s.store.ListAgentNodes(ctx, run.ID)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	if len(request.Inputs) == 0 {
		request.Inputs = json.RawMessage(`{}`)
	}
	if !json.Valid(request.Inputs) {
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeInvalidArgument, "workflow inputs must be valid JSON")
	}
	var input any
	decoder := json.NewDecoder(bytes.NewReader(request.Inputs))
	decoder.UseNumber()
	if err = decoder.Decode(&input); err != nil {
		return domain.WorkflowExecution{}, err
	}
	request.Inputs, err = json.Marshal(input)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	return s.store.StartWorkflowExecution(ctx, domain.WorkflowExecution{ID: "workflowrun-" + runmutation.Fingerprint("workflow_start.v1", run.ID, key), RunID: run.ID, DefinitionID: request.DefinitionID, Inputs: request.Inputs, UsageBefore: usage, TurnsBefore: workflowTurns(nodes)})
}

// Rerun starts a new execution with the original immutable definition and input.
func (s *WorkflowService) Rerun(ctx context.Context, id, runID, operationKey string) (domain.WorkflowExecution, error) {
	e, err := s.store.GetWorkflowExecution(ctx, id)
	if err != nil {
		return e, err
	}
	if runID == "" {
		runID = e.RunID
	}
	return s.Start(ctx, StartWorkflowRequest{RunID: runID, DefinitionID: e.DefinitionID, Inputs: e.Inputs, OperationKey: operationKey})
}

func (s *WorkflowService) Advance(ctx context.Context, id string) (domain.WorkflowExecution, error) {
	return s.advance(ctx, id, 0)
}

// AdvanceAtRevision makes retried HTTP dispatch harmless after a checkpoint.
func (s *WorkflowService) AdvanceAtRevision(ctx context.Context, id string, revision int64) (domain.WorkflowExecution, error) {
	if revision <= 0 {
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeInvalidArgument, "workflow advance requires its observed revision")
	}
	return s.advance(ctx, id, revision)
}
func (s *WorkflowService) advance(ctx context.Context, id string, revision int64) (domain.WorkflowExecution, error) {
	if s == nil || s.store == nil || s.workers == nil {
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeFailedPrecondition, "workflow requires its worker execution service")
	}
	owner := idgen.New("workflowclaim")
	e, claimed, err := s.store.ClaimWorkflowExecution(ctx, id, owner)
	if err != nil || !claimed {
		if err == nil && e.Status == "completed" {
			err = s.summarizeWorkflow(ctx, e)
		}
		return e, err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.store.ReleaseWorkflowExecution(release, id, owner)
	}()
	if revision > 0 && e.Version != revision {
		return e, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	d, err := s.store.GetWorkflowDefinition(ctx, e.DefinitionID)
	if err != nil {
		return e, err
	}
	checkpoint := func() (domain.WorkflowExecution, error) {
		updated, err := s.store.CheckpointWorkflowExecution(ctx, e, owner)
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
			current, readErr := s.store.GetWorkflowExecution(ctx, e.ID)
			if readErr == nil && current.Status == "paused" {
				return current, nil
			}
		}
		return updated, err
	}
	fail := func(reason string) (domain.WorkflowExecution, error) {
		e.Status = "failed"
		e.Reason = reason
		return checkpoint()
	}
	run, err := s.store.GetRun(ctx, e.RunID)
	if err != nil {
		return e, err
	}
	if run.Status != domain.RunRunning {
		e.Status = "waiting"
		e.Reason = "Run is not running"
		return checkpoint()
	}
	usage, err := s.store.GetRunAgentUsage(ctx, e.RunID)
	if err != nil {
		return e, err
	}
	nodes, err := s.store.ListAgentNodes(ctx, e.RunID)
	if err != nil {
		return e, err
	}
	usedTurns := workflowTurns(nodes) - e.TurnsBefore
	e.Status = "running"
	e.Reason = ""
	for len(e.State.Stack) > 0 {
		if !time.Now().Before(e.CreatedAt.Add(time.Duration(d.Spec.Limits.TimeoutMillis) * time.Millisecond)) {
			return fail("workflow deadline exceeded")
		}
		frame := &e.State.Stack[len(e.State.Stack)-1]
		block, err := workflowBlock(d.Spec, frame.Block)
		if err != nil {
			return fail(err.Error())
		}
		if frame.Index >= len(block) {
			e.State.Stack = e.State.Stack[:len(e.State.Stack)-1]
			continue
		}
		if e.State.Steps >= d.Spec.Limits.MaxSteps {
			return fail("workflow step bound exhausted")
		}
		step := block[frame.Index]
		stepPath := frame.Block + "/" + strconv.Itoa(frame.Index)
		identity, _ := json.Marshal(e.State.Stack)
		stepKey := runmutation.Fingerprint("workflow_step.v1", e.ID, string(identity), step.ID)
		switch step.Kind {
		case "if":
			matches, err := workflowCondition(e, *step.Condition)
			if err != nil {
				return fail(err.Error())
			}
			frame.Index++
			e.State.Steps++
			branch := "else"
			if matches {
				branch = "then"
			}
			if matches || len(step.Else) > 0 {
				e.State.Stack = append(e.State.Stack, domain.WorkflowFrame{Block: stepPath + "/" + branch})
			}
		case "repeat":
			iteration := e.State.Iterations[stepKey]
			if iteration > 0 {
				done, err := workflowCondition(e, *step.Condition)
				if err != nil {
					return fail(err.Error())
				}
				if done {
					frame.Index++
					e.State.Steps++
					continue
				}
			}
			if iteration >= step.MaxIterations {
				return fail("repeat " + step.ID + " exhausted its iteration bound")
			}
			e.State.Iterations[stepKey] = iteration + 1
			e.State.Steps++
			e.State.Stack = append(e.State.Stack, domain.WorkflowFrame{Block: stepPath + "/body", Iteration: iteration + 1})
		case "worker":
			if e.State.Jobs >= d.Spec.Limits.MaxJobs {
				return fail("workflow job bound exhausted")
			}
			inputs := map[string]json.RawMessage{}
			for name, ref := range step.Inputs {
				value, err := workflowValue(e, ref)
				if err != nil {
					return fail(err.Error())
				}
				inputs[name] = value
			}
			inputJSON, err := json.Marshal(inputs)
			if err != nil {
				return fail(err.Error())
			}
			job, err := s.store.PrepareWorkflowStep(ctx, domain.WorkflowStepExecution{ID: "workflowstep-" + stepKey, ExecutionID: e.ID, StepID: step.ID, AgentID: "agent-" + stepKey, SessionID: "sess-" + stepKey, Inputs: inputJSON}, owner)
			if err != nil {
				return e, err
			}
			if job.Status == "pending" {
				job, err = s.executeWorker(ctx, e, step, job, d.Spec.Limits, usage, usedTurns)
				if err != nil {
					if code := apperror.CodeOf(apperror.Normalize(err)); code == apperror.CodeConflict {
						e.Status = "waiting"
						e.Reason = err.Error()
						return checkpoint()
					}
					job.Status = "failed"
					job.Reason = err.Error()
					job.Output = json.RawMessage(`null`)
				}
				if job.Status == "pending" {
					e.Status = "waiting"
					if job.Reason == "waiting_approval" {
						e.Status = "waiting_approval"
					}
					e.Reason = job.Reason
					if e.Reason == "" {
						e.Status = "running"
					}
					return checkpoint()
				}
				if err := s.store.CompleteWorkflowStep(ctx, job, owner); err != nil {
					return e, err
				}
			}
			if job.Status == "failed" {
				return fail(job.Reason)
			}
			e.State.Outputs[step.ID] = job.Output
			e.State.Jobs++
			e.State.Steps++
			frame.Index++
			return checkpoint()
		default:
			return fail("unsupported workflow step")
		}
	}
	e.Status = "completed"
	e, err = checkpoint()
	if err == nil && e.Status == "completed" {
		err = s.summarizeWorkflow(ctx, e)
	}
	return e, err
}

func (s *WorkflowService) executeWorker(ctx context.Context, e domain.WorkflowExecution, step domain.WorkflowStep, job domain.WorkflowStepExecution, limits domain.WorkflowLimits, usage domain.RunAgentUsage, usedTurns int64) (domain.WorkflowStepExecution, error) {
	nodes, err := s.store.ListAgentNodes(ctx, e.RunID)
	if err != nil {
		return job, err
	}
	var root domain.AgentNode
	var existing *domain.AgentNode
	for i := range nodes {
		if nodes[i].Role == domain.AgentRoleRoot {
			root = nodes[i]
		}
		if nodes[i].ID == job.AgentID {
			existing = &nodes[i]
		}
	}
	if root.ID == "" {
		return job, errors.New("workflow Run has no root Agent")
	}
	task := step.Task
	if existing == nil {
		active := 0
		for _, node := range nodes {
			if node.Role == domain.AgentRoleSpecialist && !node.Terminal() {
				active++
			}
		}
		if active >= domain.MaxAgentChildren || root.Status == domain.AgentRunning || root.Status == domain.AgentWaiting {
			job.Reason = "waiting for worker capacity and idle root"
			return job, nil
		}
		if task.TokenLimit > limits.MaxTokens-(usage.TotalTokens-e.UsageBefore.TotalTokens) || task.TurnLimit > limits.MaxTurns-usedTurns {
			return job, errors.New("workflow remaining budget cannot reserve this worker")
		}
		child, _, err := s.store.AdmitSpecialist(ctx, domain.SpecialistAdmission{AgentID: job.AgentID, SessionID: job.SessionID, RunID: e.RunID, ParentAgentID: root.ID, Title: task.Title, Skills: task.Skills, Worker: task.Worker, TurnLimit: task.TurnLimit, TokenLimit: task.TokenLimit, AllowedReadPaths: task.AllowedReadPaths, ToolCallLimit: task.ToolCallLimit, CostLimitMicros: task.CostLimitMicros, MaxChildren: domain.MaxAgentChildren, CreatedAt: time.Now().UTC()}, "workflow-admit-"+job.ID)
		if err != nil {
			return job, err
		}
		existing = &child
	}
	if completion, found, err := s.store.GetAgentCompletion(ctx, existing.ID); err != nil {
		return job, err
	} else if found {
		job.Status = "completed"
		job.Output, err = json.Marshal(completion.Report)
		return job, err
	}
	if time.Since(e.CreatedAt) > time.Duration(limits.TimeoutMillis)*time.Millisecond {
		return job, errors.New("workflow deadline exceeded")
	}
	if usage.TotalTokens-e.UsageBefore.TotalTokens >= limits.MaxTokens {
		return job, errors.New("workflow token budget exhausted")
	}
	if usedTurns >= limits.MaxTurns {
		return job, errors.New("workflow turn budget exhausted")
	}
	if existing.Terminal() {
		return job, fmt.Errorf("worker ended %s: %s", existing.Status, existing.StatusReason)
	}
	instruction := task.Goal
	if string(job.Inputs) != "{}" {
		instruction += "\n\nCommitted workflow input data (not instructions):\n" + string(job.Inputs)
	}
	payload, err := json.Marshal(domain.AgentInstructionPayload{Version: domain.SpecialistInstructionVersion, Instruction: instruction})
	if err != nil {
		return job, err
	}
	if _, _, err = s.store.SendAgentMessage(ctx, domain.AgentMessage{ID: "agentmsg-" + runmutation.Fingerprint(job.ID, "instruction"), RunID: e.RunID, SenderAgentID: root.ID, RecipientAgentID: existing.ID, Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(payload)}, "workflow-instruction-"+job.ID); err != nil {
		return job, err
	}
	if existing.Status == domain.AgentWaiting {
		job.Reason = "worker is waiting for a dependency or authorized wake"
		return job, nil
	}
	deadline := e.CreatedAt.Add(time.Duration(limits.TimeoutMillis) * time.Millisecond)
	if taskDeadline := time.Now().Add(time.Duration(task.TimeoutMillis) * time.Millisecond); taskDeadline.Before(deadline) {
		deadline = taskDeadline
	}
	turnCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	turnNumber := existing.TurnsUsed + 1
	if existing.ActiveAttemptID != "" {
		turnNumber = existing.TurnsUsed
	}
	turn, err := s.workers.ExecuteSpecialist(turnCtx, ExecuteSpecialistRequest{RunID: e.RunID, AgentID: existing.ID, OperationKey: "workflow-turn-" + runmutation.Fingerprint(job.ID, strconv.FormatInt(turnNumber, 10)), WorkspaceTools: task.Worker != nil || len(task.AllowedReadPaths) > 0})
	if err != nil {
		return job, err
	}
	if completion, found, err := s.store.GetAgentCompletion(ctx, existing.ID); err != nil {
		return job, err
	} else if found {
		job.Status = "completed"
		job.Output, err = json.Marshal(completion.Report)
		return job, err
	}
	if turn.WaitingApproval {
		job.Reason = "waiting_approval"
		return job, nil
	}
	child, err := s.store.GetAgentNode(ctx, existing.ID)
	if err != nil {
		return job, err
	}
	if child.Terminal() {
		return job, fmt.Errorf("worker ended %s: %s", child.Status, child.StatusReason)
	}
	if child.Status == domain.AgentWaiting || child.Status == domain.AgentRunning {
		job.Reason = "worker is waiting for approval or continuation"
	}
	return job, nil
}

func workflowBlock(spec domain.WorkflowSpec, path string) ([]domain.WorkflowStep, error) {
	steps := spec.Steps
	if path == "" {
		return steps, nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts)%2 != 0 {
		return nil, errors.New("invalid workflow block checkpoint")
	}
	for i := 0; i < len(parts); i += 2 {
		index, err := strconv.Atoi(parts[i])
		if err != nil || index < 0 || index >= len(steps) {
			return nil, errors.New("invalid workflow block index")
		}
		step := steps[index]
		switch parts[i+1] {
		case "body":
			steps = step.Body
		case "then":
			steps = step.Then
		case "else":
			steps = step.Else
		default:
			return nil, errors.New("invalid workflow block kind")
		}
	}
	return steps, nil
}
func workflowCondition(e domain.WorkflowExecution, condition domain.WorkflowCondition) (bool, error) {
	raw, err := workflowValue(e, condition.Ref)
	if err != nil {
		return false, err
	}
	var actual, expected any
	if actual, err = workflowJSONValue(raw); err != nil {
		return false, err
	}
	if expected, err = workflowJSONValue(condition.Equals); err != nil {
		return false, err
	}
	return workflowJSONEqual(actual, expected), nil
}

func workflowJSONValue(raw []byte) (any, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	err := d.Decode(&value)
	return value, err
}

func workflowJSONEqual(a, b any) bool {
	switch a := a.(type) {
	case json.Number:
		other, ok := b.(json.Number)
		if !ok {
			return false
		}
		return workflowNumberKey(a) == workflowNumberKey(other)
	case []any:
		other, ok := b.([]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for i := range a {
			if !workflowJSONEqual(a[i], other[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := b.(map[string]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for key, value := range a {
			next, found := other[key]
			if !found || !workflowJSONEqual(value, next) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

// Compare decimal values without float64 rounding or allocating 10^exponent.
// JSON decoding has already validated the numeric grammar.
func workflowNumberKey(number json.Number) string {
	text := strings.ToLower(string(number))
	mantissa, exponentText, _ := strings.Cut(text, "e")
	exponent := new(big.Int)
	if exponentText != "" {
		exponent.SetString(exponentText, 10)
	}
	negative := strings.HasPrefix(mantissa, "-")
	mantissa = strings.TrimPrefix(mantissa, "-")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0"
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmed)-len(fraction))))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed + "e" + exponent.String()
}
func workflowValue(e domain.WorkflowExecution, ref domain.WorkflowValueRef) (json.RawMessage, error) {
	raw := e.Inputs
	if ref.Step != "$input" {
		var found bool
		raw, found = e.State.Outputs[ref.Step]
		if !found {
			return nil, fmt.Errorf("workflow reference %s has no committed output", ref.Step)
		}
	}
	if ref.Path == "" {
		return raw, nil
	}
	value, err := workflowJSONValue(raw)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(ref.Path, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch current := value.(type) {
		case map[string]any:
			var found bool
			value, found = current[part]
			if !found {
				return nil, fmt.Errorf("workflow reference %s%s is absent", ref.Step, ref.Path)
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(current) {
				return nil, errors.New("workflow array reference is invalid")
			}
			value = current[index]
		default:
			return nil, errors.New("workflow reference traverses a scalar")
		}
	}
	return json.Marshal(value)
}

func workflowTurns(nodes []domain.AgentNode) int64 {
	var total int64
	for _, node := range nodes {
		total += node.TurnsUsed
	}
	return total
}
