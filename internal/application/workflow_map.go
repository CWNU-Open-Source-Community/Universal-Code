package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (s *WorkflowService) RetryFailed(ctx context.Context, id string, revision int64, operationKey string) (domain.WorkflowExecution, error) {
	return s.store.RetryFailedWorkflowExecution(ctx, id, revision, operationKey)
}

func workflowMapJob(e domain.WorkflowExecution, step domain.WorkflowStep, group domain.WorkflowMapState, item domain.WorkflowMapItem) (domain.WorkflowStepExecution, error) {
	inputs := group.Inputs
	if step.Kind == "map" {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(inputs, &values); err != nil {
			return domain.WorkflowStepExecution{}, err
		}
		values["item"] = item.Value
		values["index"], _ = json.Marshal(item.Index)
		inputs, _ = json.Marshal(values)
	}
	if len(inputs) > 32*1024 {
		return domain.WorkflowStepExecution{}, apperror.New(apperror.CodeInvalidArgument, "workflow item inputs exceed 32 KiB")
	}
	identity := strings.TrimPrefix(item.StepExecutionID, "workflowstep-")
	return domain.WorkflowStepExecution{ID: item.StepExecutionID, ExecutionID: e.ID, StepID: step.ID, AgentID: "agent-" + identity, SessionID: "sess-" + identity, Inputs: inputs}, nil
}

func (s *WorkflowService) freezeWorkflowTasks(ctx context.Context, e *domain.WorkflowExecution, step domain.WorkflowStep, key, owner string, limits domain.WorkflowLimits) (domain.WorkflowMapState, error) {
	if group, ok := e.State.Maps[key]; ok {
		return group, nil
	}
	inputs := map[string]json.RawMessage{}
	for name, ref := range step.Inputs {
		value, err := workflowValue(*e, ref)
		if err != nil {
			return domain.WorkflowMapState{}, apperror.Wrap(apperror.CodeInvalidArgument, "workflow input reference is invalid", err)
		}
		inputs[name] = value
	}
	encoded, _ := json.Marshal(inputs)
	values := []json.RawMessage{nil}
	if step.Kind == "map" {
		raw, err := workflowValue(*e, *step.Items)
		if err != nil {
			return domain.WorkflowMapState{}, apperror.Wrap(apperror.CodeInvalidArgument, "workflow map items reference is invalid", err)
		}
		if len(raw) == 0 || raw[0] != '[' {
			return domain.WorkflowMapState{}, apperror.New(apperror.CodeInvalidArgument, "workflow map items must resolve to a JSON array")
		}
		if err = json.Unmarshal(raw, &values); err != nil {
			return domain.WorkflowMapState{}, err
		}
		if len(values) > step.MaxItems {
			return domain.WorkflowMapState{}, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("workflow map %s has %d items, exceeding max_items=%d", step.ID, len(values), step.MaxItems))
		}
	}
	if max(e.State.AllocatedJobs, e.State.Jobs)+len(values) > limits.MaxJobs {
		return domain.WorkflowMapState{}, apperror.New(apperror.CodeResourceExhausted, "workflow cumulative job bound exhausted")
	}
	group := domain.WorkflowMapState{StepID: step.ID, Kind: step.Kind, Inputs: encoded, Items: make([]domain.WorkflowMapItem, len(values))}
	for i, value := range values {
		group.Items[i] = domain.WorkflowMapItem{Index: i, Value: value, Generation: 1, StepExecutionID: domain.WorkflowItemStepID(key, step.Kind, i, 1), Status: "pending"}
		if _, err := workflowMapJob(*e, step, group, group.Items[i]); err != nil {
			return group, err
		}
	}
	if e.State.Maps == nil {
		e.State.Maps = map[string]domain.WorkflowMapState{}
	}
	e.State.Maps[key] = group
	e.State.AllocatedJobs = max(e.State.AllocatedJobs, e.State.Jobs) + len(values)
	// Freeze source values and every attempt identity before any admission.
	updated, err := s.store.CheckpointWorkflowExecution(ctx, *e, owner)
	if err != nil {
		return group, err
	}
	*e = updated
	return e.State.Maps[key], nil
}

func (s *WorkflowService) advanceWorkflowTasks(ctx context.Context, e *domain.WorkflowExecution, step domain.WorkflowStep, key, owner string, limits domain.WorkflowLimits) (json.RawMessage, bool, error) {
	group, err := s.freezeWorkflowTasks(ctx, e, step, key, owner, limits)
	if err != nil {
		return nil, false, err
	}
	usage, err := s.store.GetRunAgentUsage(ctx, e.RunID)
	if err != nil {
		return nil, false, err
	}
	nodes, err := s.store.ListAgentNodes(ctx, e.RunID)
	if err != nil {
		return nil, false, err
	}
	usedTurns := workflowTurns(nodes) - e.TurnsBefore
	byID := map[string]domain.AgentNode{}
	for _, node := range nodes {
		byID[node.ID] = node
	}
	// Read every durable result before choosing pending slots. Recovery never
	// replays a sibling that already committed its completion.
	jobs := make(map[int]domain.WorkflowStepExecution, len(group.Items))
	pending := []int{}
	for i := range group.Items {
		item := &group.Items[i]
		proposed, err := workflowMapJob(*e, step, group, *item)
		if err != nil {
			return nil, false, err
		}
		job, err := s.store.PrepareWorkflowStep(ctx, proposed, owner)
		if err != nil {
			return nil, false, err
		}
		jobs[i] = job
		if item.Status == "completed" || item.Status == "failed" {
			continue
		}
		if job.Status == "completed" || job.Status == "failed" {
			if job.Status == "completed" {
				if err = s.checkWorkerVerification(ctx, e, job, step, owner, false); err != nil {
					if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
						return nil, false, apperror.Wrap(apperror.CodeFailedPrecondition, "workflow receipt cannot be replayed", err)
					}
					return nil, false, err
				}
				e.State.Jobs++
			}
			item.Status, item.Reason = job.Status, job.Reason
		} else {
			child := byID[job.AgentID]
			if child.ActiveAttemptID != "" {
				waiting, unresolved, readErr := s.store.SpecialistToolApprovalState(ctx, domain.AgentAttemptRef{RunID: e.RunID, AgentID: child.ID, AttemptID: child.ActiveAttemptID})
				if readErr != nil {
					return nil, false, readErr
				}
				if waiting && unresolved {
					item.Reason = "waiting_approval"
					continue
				}
			}
			pending = append(pending, i)
		}
	}
	selected := pending
	if len(selected) > step.WorkerParallelism() {
		selected = selected[:step.WorkerParallelism()]
	}
	if len(selected) > 0 {
		ids := make([]string, len(selected))
		newReservations := 0
		for n, i := range selected {
			job := jobs[i]
			ids[n] = job.ID
			if _, found := byID[job.AgentID]; !found {
				newReservations++
			}
		}
		if int64(newReservations)*step.Task.TokenLimit > limits.MaxTokens-(usage.TotalTokens-e.UsageBefore.TotalTokens) || int64(newReservations)*step.Task.TurnLimit > limits.MaxTurns-usedTurns {
			return nil, false, apperror.New(apperror.CodeResourceExhausted, "workflow remaining budget cannot reserve this worker round")
		}
		if err = s.store.ValidateWorkflowDispatch(ctx, e.ID, owner, e.Version, ids); err != nil {
			return nil, false, err
		}
		round := []workflowRoundItem{}
		turnResults := map[string]SpecialistTurnResult{}
		turnErrors := map[string]error{}
		var controlErr, roundFailure error
		for _, i := range selected {
			job := jobs[i]
			prepared, prepareErr := s.executeWorkerTurn(ctx, *e, step, job, limits, usage, usedTurns, func(turnCtx context.Context, request ExecuteSpecialistRequest) (SpecialistTurnResult, error) {
				deadline, _ := turnCtx.Deadline()
				round = append(round, workflowRoundItem{job: job, request: request, deadline: deadline})
				return SpecialistTurnResult{}, nil
			})
			jobs[i] = prepared
			if prepareErr != nil {
				if apperror.CodeOf(apperror.Normalize(prepareErr)) == apperror.CodeInternal {
					roundFailure = prepareErr
				}
				turnErrors[job.AgentID] = prepareErr
			}
		}
		if len(round) > 0 {
			outcomes, roundErr := s.executeWorkflowRound(ctx, *e, owner, limits, round)
			if roundErr != nil {
				if apperror.CodeOf(apperror.Normalize(roundErr)) == apperror.CodeConflict {
					controlErr = roundErr
				} else {
					roundFailure = roundErr
				}
			}
			for _, outcome := range outcomes {
				turnResults[outcome.agentID] = outcome.result
				turnErrors[outcome.agentID] = outcome.err
			}
		}
		// Harvest all children even when one fails. A child error never discards a
		// sibling's native completion or changes its attempt into a retry.
		for _, i := range selected {
			job := jobs[i]
			if turnErrors[job.AgentID] != nil && apperror.CodeOf(apperror.Normalize(turnErrors[job.AgentID])) == apperror.CodeInternal {
				// A recorded terminal child is a real item failure; a failed
				// storage read or dispatch without a terminal child is retryable IO.
				child, readErr := s.store.GetAgentNode(ctx, job.AgentID)
				if readErr != nil || !child.Terminal() {
					roundFailure = turnErrors[job.AgentID]
					continue
				}
			}
			observed, observeErr := s.observeWorkflowWorker(ctx, job, turnResults[job.AgentID], turnErrors[job.AgentID])
			if observeErr != nil {
				if apperror.CodeOf(apperror.Normalize(observeErr)) == apperror.CodeConflict {
					controlErr = observeErr
					continue
				}
				return nil, false, observeErr
			}
			job = observed
			jobs[i] = job
			if job.Status == "pending" {
				group.Items[i].Reason = job.Reason
				continue
			}
			if job.Status == "completed" {
				if err = s.checkWorkerVerification(ctx, e, job, step, owner, false); err != nil {
					if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
						controlErr = err
						continue
					}
					job.Status, job.Reason = "failed", err.Error()
					jobs[i] = job
				}
			}
			if err = s.store.ValidateWorkflowSettlement(ctx, e.ID, owner, e.Version, []string{job.ID}); err != nil {
				controlErr = err
				continue
			}
			if err = s.store.CompleteWorkflowStep(ctx, job, owner); err != nil {
				if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
					controlErr = err
					continue
				}
				return nil, false, err
			}
			group.Items[i].Status, group.Items[i].Reason = job.Status, job.Reason
			if job.Status == "completed" {
				e.State.Jobs++
			}
		}
		e.State.Maps[key] = group
		if controlErr != nil {
			return nil, false, controlErr
		}
		if roundFailure != nil {
			return nil, false, roundFailure
		}
	}
	e.State.Maps[key] = group
	failed, waiting := 0, false
	retryable := true
	for _, item := range group.Items {
		if item.Status == "pending" {
			waiting = true
		}
		if item.Status == "failed" {
			failed++
			retryable = retryable && item.Generation < step.AttemptLimit()
		}
	}
	if waiting {
		e.Status, e.Reason = "running", ""
		{
			allDormant := true
			approval := false
			for _, item := range group.Items {
				if item.Status == "pending" {
					if item.Reason == "" {
						allDormant = false
					}
					approval = approval || item.Reason == "waiting_approval"
				}
			}
			if allDormant {
				e.Status = "waiting"
				e.Reason = "workflow workers are waiting for a dependency or capacity"
				if approval {
					e.Status, e.Reason = "waiting_approval", "waiting_approval"
				}
			}
		}
		return nil, false, nil
	}
	if failed > 0 {
		currentUsage, readErr := s.store.GetRunAgentUsage(ctx, e.RunID)
		if readErr != nil {
			return nil, false, readErr
		}
		currentNodes, readErr := s.store.ListAgentNodes(ctx, e.RunID)
		if readErr != nil {
			return nil, false, readErr
		}
		currentRun, readErr := s.store.GetRun(ctx, e.RunID)
		if readErr != nil {
			return nil, false, readErr
		}
		retryable = retryable && currentRun.Status == domain.RunRunning && time.Now().Before(e.CreatedAt.Add(time.Duration(limits.TimeoutMillis)*time.Millisecond)) && int64(failed)*step.Task.TokenLimit <= limits.MaxTokens-(currentUsage.TotalTokens-e.UsageBefore.TotalTokens) && int64(failed)*step.Task.TurnLimit <= limits.MaxTurns-(workflowTurns(currentNodes)-e.TurnsBefore)
		e.Status = "failed"
		e.Reason = fmt.Sprintf("workflow step %s has %d failed item(s)", step.ID, failed)
		for _, item := range group.Items {
			if item.Status == "failed" && item.Reason != "" {
				e.Reason += "; " + item.Reason
				break
			}
		}
		e.State.Blocked = &domain.WorkflowBlocked{Key: key, StepID: step.ID, Retryable: retryable && max(e.State.AllocatedJobs, e.State.Jobs)+failed <= limits.MaxJobs}
		if step.Kind == "worker" {
			e.State.Outputs[step.ID] = jobs[0].Output
		}
		return nil, false, nil
	}
	e.State.Blocked = nil
	if step.Kind == "worker" {
		return jobs[0].Output, true, nil
	}
	result := struct {
		Outcome domain.CompletionOutcome `json:"outcome"`
		Items   []workflowMapOutputItem  `json:"items"`
	}{Outcome: domain.CompletionSucceeded, Items: []workflowMapOutputItem{}}
	for i, item := range group.Items {
		var report domain.CompletionReport
		if err = json.Unmarshal(jobs[i].Output, &report); err != nil {
			return nil, false, err
		}
		if report.Outcome == domain.CompletionPartial {
			result.Outcome = domain.CompletionPartial
		}
		result.Items = append(result.Items, workflowMapOutputItem{Index: item.Index, StepExecutionID: item.StepExecutionID, Outcome: report.Outcome, Data: report.Data, Summary: report.Summary})
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, false, err
	}
	if len(raw) > 32*1024 {
		return nil, false, apperror.New(apperror.CodeResourceExhausted, "workflow map output exceeds 32 KiB; no item results were dropped")
	}
	return raw, true, nil
}

type workflowMapOutputItem struct {
	Index           int                      `json:"index"`
	StepExecutionID string                   `json:"step_execution_id"`
	Outcome         domain.CompletionOutcome `json:"outcome"`
	Data            json.RawMessage          `json:"data,omitempty"`
	Summary         string                   `json:"summary"`
}

func (s *WorkflowService) observeWorkflowWorker(ctx context.Context, job domain.WorkflowStepExecution, turn SpecialistTurnResult, turnErr error) (domain.WorkflowStepExecution, error) {
	completion, found, err := s.store.GetAgentCompletion(ctx, job.AgentID)
	if err != nil {
		return job, err
	}
	if found {
		job.Status = "completed"
		job.Reason = ""
		job.Output, err = json.Marshal(completion.Report)
		return job, err
	}
	child, err := s.store.GetAgentNode(ctx, job.AgentID)
	if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
		if turnErr != nil {
			job.Status, job.Reason, job.Output = "failed", turnErr.Error(), json.RawMessage(`null`)
		}
		return job, nil
	}
	if err != nil {
		return job, err
	}
	if child.Terminal() {
		job.Status, job.Reason, job.Output = "failed", fmt.Sprintf("worker ended %s: %s", child.Status, child.StatusReason), json.RawMessage(`null`)
		if turnErr != nil {
			job.Reason = turnErr.Error()
		}
		return job, nil
	}
	if turnErr != nil {
		if apperror.CodeOf(apperror.Normalize(turnErr)) == apperror.CodeConflict {
			return job, turnErr
		}
		job.Status, job.Reason, job.Output = "failed", turnErr.Error(), json.RawMessage(`null`)
		return job, nil
	}
	if turn.WaitingApproval {
		job.Reason = "waiting_approval"
	} else if child.Status == domain.AgentWaiting {
		job.Reason = "worker is waiting for a dependency or authorized wake"
	}
	return job, nil
}
