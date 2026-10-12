package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const defaultSpecialistOperatorSchedulePollInterval = 50 * time.Millisecond

type SpecialistOperatorScheduleStore interface {
	SubagentRunnerStore
	SpecialistScheduleStore
	policy.DecisionRecorder
	GetSpecialistDelegationProposal(ctx context.Context,
		id string) (domain.SpecialistDelegationProposal, error)
	GetSpecialistDelegationApplicationByProposal(ctx context.Context,
		proposalID string) (domain.SpecialistDelegationApplication, bool, error)
	CreateSpecialistOperatorScheduleRequest(ctx context.Context,
		request domain.SpecialistOperatorScheduleRequest,
		operation domain.SpecialistOperatorScheduleOperation,
		checks []domain.SpecialistDelegationPolicyCheck,
	) (domain.SpecialistOperatorScheduleRequest, bool, error)
	GetSpecialistOperatorScheduleRequestByOperation(ctx context.Context,
		keyDigest string) (domain.SpecialistOperatorScheduleRequest,
		domain.SpecialistOperatorScheduleOperation, bool, error)
	GetLatestSpecialistOperatorScheduleAttempt(ctx context.Context,
		requestID string) (domain.SpecialistSchedule,
		domain.SpecialistOperatorScheduleAttempt, bool, error)
}

// WithWorkspaceTools grants only the server-owned child read/search profile.
// Delegation review, schedule intent and lease ownership remain unchanged.
func (s *SpecialistOperatorScheduleService) WithWorkspaceTools() *SpecialistOperatorScheduleService {
	if s != nil && s.scheduler != nil && s.scheduler.runner != nil {
		s.scheduler.runner.WithWorkspaceTools()
	}
	return s
}

func (s *SpecialistOperatorScheduleService) WithRunWorktree(drydocks *RunWorktreeService) *SpecialistOperatorScheduleService {
	if s != nil && s.scheduler != nil && s.scheduler.runner != nil {
		s.scheduler.runner.WithRunWorktree(drydocks)
	}
	return s
}

type SpecialistOperatorScheduleService struct {
	store        SpecialistOperatorScheduleStore
	checker      policy.Checker
	scheduler    *SpecialistScheduler
	pollInterval time.Duration
}

func NewSpecialistOperatorScheduleService(store SpecialistOperatorScheduleStore,
	router *llm.Router, checker policy.Checker,
) *SpecialistOperatorScheduleService {
	runner := NewSubagentRunner(store, router, checker)
	if monetaryStore, ok := store.(MonetaryBudgetStore); ok {
		runner.WithMonetaryBudget(NewMonetaryBudgetService(monetaryStore))
	}
	return &SpecialistOperatorScheduleService{
		store: store, checker: checker, scheduler: NewSpecialistScheduler(runner),
		pollInterval: defaultSpecialistOperatorSchedulePollInterval,
	}
}

func (s *SpecialistOperatorScheduleService) WithLifecycleHooks(
	engine *hooks.Engine,
) *SpecialistOperatorScheduleService {
	if s != nil && s.scheduler != nil {
		s.scheduler.WithLifecycleHooks(engine)
	}
	return s
}

type ExecuteSpecialistOperatorScheduleRequest struct {
	ProposalID   string
	AgentIDs     []string
	MaxRounds    int
	OperationKey string
	RequestedBy  string
}

type ExecuteSpecialistOperatorScheduleResult struct {
	Request   domain.SpecialistOperatorScheduleRequest
	Schedule  domain.SpecialistSchedule
	Attempt   domain.SpecialistOperatorScheduleAttempt
	Replayed  bool
	Recovered bool
}

func (s *SpecialistOperatorScheduleService) Execute(ctx context.Context,
	request ExecuteSpecialistOperatorScheduleRequest,
) (ExecuteSpecialistOperatorScheduleResult, error) {
	if s == nil || s.store == nil || s.checker == nil || s.scheduler == nil ||
		s.scheduler.runner == nil || s.scheduler.runner.router == nil {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist operator schedule service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeSpecialistOperatorScheduleExecution(request)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument,
			"specialist operator schedule request is invalid", err)
	}
	proposal, err := s.store.GetSpecialistDelegationProposal(ctx,
		normalized.ProposalID)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.Normalize(err)
	}
	application, found, err := s.store.GetSpecialistDelegationApplicationByProposal(ctx,
		proposal.ID)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.Normalize(err)
	}
	if !found || application.Status != domain.SpecialistDelegationApplied {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist operator scheduling requires an applied delegation")
	}
	if normalized.RequestedBy != application.RequestedBy {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist scheduling must be requested by the application operator")
	}
	selected, assignments, err := selectSpecialistOperatorScheduleTargets(
		proposal, application, normalized.AgentIDs)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, err
	}
	fingerprint := runmutation.Fingerprint(
		"specialist_operator_schedule_request.v1", application.ID, proposal.ID,
		proposal.RunID, strings.Join(selected, "\x1f"), strconv.Itoa(normalized.MaxRounds),
		normalized.RequestedBy)
	if s.scheduler.runner.workspaceTools {
		fingerprint = runmutation.Fingerprint("specialist_tool_schedule.v1", fingerprint)
	}
	keyDigest := runmutation.OperationKeyDigest("specialist_operator_schedule",
		proposal.RunID, normalized.OperationKey)

	stored, operation, exists, err := s.store.
		GetSpecialistOperatorScheduleRequestByOperation(ctx, keyDigest)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, apperror.Normalize(err)
	}
	requestReplayed := exists
	if exists {
		if operation.RequestFingerprint != fingerprint ||
			operation.ApplicationID != application.ID || operation.ProposalID != proposal.ID ||
			operation.RunID != proposal.RunID ||
			operation.RequestedBy != normalized.RequestedBy {
			return ExecuteSpecialistOperatorScheduleResult{}, apperror.New(
				apperror.CodeConflict,
				"specialist operator schedule operation key was already used for different intent")
		}
		if schedule, attempt, terminal, err := s.terminalSchedule(ctx, stored.ID); err != nil {
			return ExecuteSpecialistOperatorScheduleResult{}, err
		} else if terminal {
			result := ExecuteSpecialistOperatorScheduleResult{
				Request: stored, Schedule: schedule, Attempt: attempt,
				Replayed: true, Recovered: attempt.Ordinal > 1,
			}
			return result, specialistOperatorScheduleTerminalError(schedule)
		}
	}

	checks, err := s.evaluateSpecialistOperatorSchedulePolicy(ctx, proposal,
		assignments, application, stored.ID)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleResult{}, err
	}
	if !exists {
		policyFingerprint, err := domain.SpecialistDelegationPolicyFingerprint(checks)
		if err != nil {
			return ExecuteSpecialistOperatorScheduleResult{}, err
		}
		now := time.Now().UTC()
		if application.CompletedAt != nil && now.Before(*application.CompletedAt) {
			now = *application.CompletedAt
		}
		candidate := domain.SpecialistOperatorScheduleRequest{
			ID: idgen.New("operator-schedule"), ApplicationID: application.ID,
			ProposalID: proposal.ID, RunID: proposal.RunID,
			RootAgentID: application.RootAgentID, AgentIDs: selected,
			MaxRounds: normalized.MaxRounds, PolicyFingerprint: policyFingerprint,
			RequestedBy: normalized.RequestedBy, CreatedAt: now,
		}
		candidateOperation := domain.SpecialistOperatorScheduleOperation{
			KeyDigest: keyDigest, RequestFingerprint: fingerprint,
			RequestID: candidate.ID, ApplicationID: application.ID,
			ProposalID: proposal.ID, RunID: proposal.RunID,
			RequestedBy: normalized.RequestedBy, CreatedAt: now,
		}
		stored, requestReplayed, err = s.store.CreateSpecialistOperatorScheduleRequest(
			ctx, candidate, candidateOperation, checks)
		if err != nil {
			return ExecuteSpecialistOperatorScheduleResult{}, apperror.Normalize(err)
		}
	}
	return s.executeOrJoinSpecialistOperatorSchedule(ctx, stored, requestReplayed)
}

func (s *SpecialistOperatorScheduleService) executeOrJoinSpecialistOperatorSchedule(
	ctx context.Context, request domain.SpecialistOperatorScheduleRequest,
	requestReplayed bool,
) (ExecuteSpecialistOperatorScheduleResult, error) {
	joinedExisting := false
	for {
		if err := ctx.Err(); err != nil {
			return ExecuteSpecialistOperatorScheduleResult{Request: request,
				Replayed: requestReplayed || joinedExisting}, apperror.Normalize(err)
		}
		schedule, attempt, found, err :=
			s.store.GetLatestSpecialistOperatorScheduleAttempt(ctx, request.ID)
		if err != nil {
			return ExecuteSpecialistOperatorScheduleResult{Request: request},
				apperror.Normalize(err)
		}
		if found {
			switch schedule.Status {
			case domain.SpecialistScheduleCompleted, domain.SpecialistScheduleFailed,
				domain.SpecialistScheduleCancelled:
				result := ExecuteSpecialistOperatorScheduleResult{
					Request: request, Schedule: schedule, Attempt: attempt,
					Replayed:  requestReplayed || joinedExisting,
					Recovered: attempt.Ordinal > 1,
				}
				return result, specialistOperatorScheduleTerminalError(schedule)
			case domain.SpecialistScheduleRunning:
				lease, leaseFound, err := s.store.GetRunExecutionLease(ctx, request.RunID)
				if err != nil {
					return ExecuteSpecialistOperatorScheduleResult{Request: request},
						apperror.Normalize(err)
				}
				if leaseFound && lease.Status == domain.RunExecutionLeaseActive &&
					time.Now().UTC().Before(lease.ExpiresAt) {
					joinedExisting = true
					if err := waitSpecialistOperatorSchedulePoll(ctx, s.pollInterval); err != nil {
						return ExecuteSpecialistOperatorScheduleResult{Request: request,
							Schedule: schedule, Attempt: attempt, Replayed: true}, err
					}
					continue
				}
			case domain.SpecialistScheduleAbandoned:
				// The same immutable request may recover through a new fenced attempt.
			default:
				return ExecuteSpecialistOperatorScheduleResult{Request: request,
						Schedule: schedule, Attempt: attempt}, apperror.New(
						apperror.CodeConflict,
						"specialist operator schedule has an invalid durable status")
			}
		}
		scheduled, executeErr := s.scheduler.Execute(ctx, SpecialistScheduleRequest{
			RunID: request.RunID, AgentIDs: request.AgentIDs,
			MaxRounds: request.MaxRounds, OperatorRequestID: request.ID,
		})
		if scheduled.ScheduleID != "" {
			final, finalAttempt, finalFound, loadErr :=
				s.store.GetLatestSpecialistOperatorScheduleAttempt(ctx, request.ID)
			if loadErr != nil {
				return ExecuteSpecialistOperatorScheduleResult{Request: request},
					apperror.Normalize(loadErr)
			}
			if finalFound {
				result := ExecuteSpecialistOperatorScheduleResult{
					Request: request, Schedule: final, Attempt: finalAttempt,
					Replayed:  requestReplayed || joinedExisting,
					Recovered: scheduled.RecoveredSchedule || finalAttempt.Ordinal > 1,
				}
				if executeErr != nil {
					return result, apperror.Normalize(executeErr)
				}
				return result, specialistOperatorScheduleTerminalError(final)
			}
		}
		if executeErr == nil {
			return ExecuteSpecialistOperatorScheduleResult{Request: request},
				apperror.New(apperror.CodeInternal,
					"specialist operator schedule completed without a durable schedule")
		}
		if apperror.CodeOf(apperror.Normalize(executeErr)) != apperror.CodeConflict {
			return ExecuteSpecialistOperatorScheduleResult{Request: request},
				apperror.Normalize(executeErr)
		}
		joinedExisting = true
		if err := waitSpecialistOperatorSchedulePoll(ctx, s.pollInterval); err != nil {
			return ExecuteSpecialistOperatorScheduleResult{Request: request,
				Replayed: true}, err
		}
	}
}

func (s *SpecialistOperatorScheduleService) terminalSchedule(ctx context.Context,
	requestID string,
) (domain.SpecialistSchedule, domain.SpecialistOperatorScheduleAttempt, bool, error) {
	schedule, attempt, found, err :=
		s.store.GetLatestSpecialistOperatorScheduleAttempt(ctx, requestID)
	if err != nil || !found {
		return schedule, attempt, false, apperror.Normalize(err)
	}
	terminal := schedule.Status == domain.SpecialistScheduleCompleted ||
		schedule.Status == domain.SpecialistScheduleFailed ||
		schedule.Status == domain.SpecialistScheduleCancelled
	return schedule, attempt, terminal, nil
}

func (s *SpecialistOperatorScheduleService) evaluateSpecialistOperatorSchedulePolicy(
	ctx context.Context, proposal domain.SpecialistDelegationProposal,
	assignments []domain.SpecialistDelegationAssignment,
	application domain.SpecialistDelegationApplication, requestID string,
) ([]domain.SpecialistDelegationPolicyCheck, error) {
	checks := make([]domain.SpecialistDelegationPolicyCheck, len(assignments))
	for index, assignment := range assignments {
		decision := s.checker.CheckText("specialist_operator_schedule",
			assignment.Title+"\n"+assignment.Goal)
		checks[index] = domain.SpecialistDelegationPolicyCheck{
			Ordinal: index + 1, Allowed: decision.Allowed,
			NeedsApproval: decision.NeedsApproval,
			Risk:          strings.TrimSpace(redact.String(decision.Risk)),
			Reason:        strings.TrimSpace(redact.String(decision.Reason)),
		}
		if err := checks[index].Validate(); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternal,
				"Policy returned an invalid Specialist schedule decision", err)
		}
		if decision.Allowed {
			continue
		}
		subjectID := application.ID
		if requestID != "" {
			subjectID = requestID
		}
		if err := s.store.RecordPolicyDecision(ctx, policy.DecisionRecord{
			SessionID: proposal.SessionID, SubjectID: subjectID,
			Context: "specialist_operator_schedule", Decision: policy.Decision{
				Allowed: false, NeedsApproval: decision.NeedsApproval,
				Risk: checks[index].Risk, Reason: checks[index].Reason,
			},
		}); err != nil {
			return nil, apperror.Normalize(err)
		}
		return nil, apperror.New(apperror.CodePolicyDenied, checks[index].Reason)
	}
	return checks, nil
}

func selectSpecialistOperatorScheduleTargets(
	proposal domain.SpecialistDelegationProposal,
	application domain.SpecialistDelegationApplication,
	requested []string,
) ([]string, []domain.SpecialistDelegationAssignment, error) {
	if len(proposal.Spec.Assignments) != len(application.Assignments) {
		return nil, nil, apperror.New(apperror.CodeConflict,
			"specialist delegation proposal and application assignments drifted")
	}
	byAgent := make(map[string]domain.SpecialistDelegationAssignment,
		len(application.Assignments))
	for index, state := range application.Assignments {
		if state.Status != domain.SpecialistDelegationAssignmentInstructed ||
			state.AgentID == "" || state.Ordinal != proposal.Spec.Assignments[index].Ordinal {
			return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
				"specialist operator scheduling requires fully instructed assignments")
		}
		byAgent[state.AgentID] = proposal.Spec.Assignments[index]
	}
	selected := slices.Clone(requested)
	if len(selected) == 0 {
		for agentID := range byAgent {
			selected = append(selected, agentID)
		}
	}
	slices.Sort(selected)
	assignments := make([]domain.SpecialistDelegationAssignment, len(selected))
	for index, agentID := range selected {
		assignment, found := byAgent[agentID]
		if !found {
			return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Agent %s is not an instructed assignment of this application",
					agentID))
		}
		assignments[index] = assignment
	}
	return selected, assignments, nil
}

func normalizeSpecialistOperatorScheduleExecution(
	request ExecuteSpecialistOperatorScheduleRequest,
) (ExecuteSpecialistOperatorScheduleRequest, error) {
	request.ProposalID = strings.TrimSpace(request.ProposalID)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if !domain.ValidAgentID(request.ProposalID) ||
		!domain.ValidAgentID(request.RequestedBy) ||
		strings.ContainsRune(request.ProposalID, 0) ||
		strings.ContainsRune(request.RequestedBy, 0) {
		return ExecuteSpecialistOperatorScheduleRequest{}, errors.New(
			"proposal and operator identities are required")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil {
		return ExecuteSpecialistOperatorScheduleRequest{}, err
	}
	request.OperationKey = operationKey
	if request.MaxRounds <= 0 || request.MaxRounds > MaxSpecialistScheduleRounds {
		return ExecuteSpecialistOperatorScheduleRequest{}, fmt.Errorf(
			"max rounds must be between 1 and %d", MaxSpecialistScheduleRounds)
	}
	if len(request.AgentIDs) > domain.MaxAgentChildren {
		return ExecuteSpecialistOperatorScheduleRequest{}, fmt.Errorf(
			"at most %d Specialist Agents may be selected", domain.MaxAgentChildren)
	}
	seen := make(map[string]struct{}, len(request.AgentIDs))
	request.AgentIDs = slices.Clone(request.AgentIDs)
	for index := range request.AgentIDs {
		agentID := strings.TrimSpace(request.AgentIDs[index])
		if !domain.ValidAgentID(agentID) || strings.ContainsRune(agentID, 0) {
			return ExecuteSpecialistOperatorScheduleRequest{}, errors.New(
				"selected Specialist Agent id is invalid")
		}
		if _, found := seen[agentID]; found {
			return ExecuteSpecialistOperatorScheduleRequest{}, errors.New(
				"selected Specialist Agent ids must be unique")
		}
		seen[agentID] = struct{}{}
		request.AgentIDs[index] = agentID
	}
	slices.Sort(request.AgentIDs)
	return request, nil
}

func waitSpecialistOperatorSchedulePoll(ctx context.Context,
	interval time.Duration,
) error {
	if interval <= 0 {
		interval = defaultSpecialistOperatorSchedulePollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return apperror.Normalize(ctx.Err())
	case <-timer.C:
		return nil
	}
}

func specialistOperatorScheduleTerminalError(schedule domain.SpecialistSchedule) error {
	switch schedule.Status {
	case domain.SpecialistScheduleCompleted:
		return nil
	case domain.SpecialistScheduleCancelled:
		return apperror.New(apperror.CodeCancelled,
			"specialist operator schedule was cancelled")
	case domain.SpecialistScheduleFailed:
		code := apperror.Code(schedule.ErrorCode)
		switch code {
		case apperror.CodeInvalidArgument, apperror.CodeNotFound, apperror.CodeConflict,
			apperror.CodeFailedPrecondition, apperror.CodePolicyDenied,
			apperror.CodeResourceExhausted, apperror.CodeUnavailable,
			apperror.CodeCancelled, apperror.CodeDeadlineExceeded, apperror.CodeInternal:
		default:
			code = apperror.CodeInternal
		}
		return apperror.New(code, "specialist operator schedule failed")
	case domain.SpecialistScheduleAbandoned, domain.SpecialistScheduleRunning:
		return apperror.New(apperror.CodeUnavailable,
			"specialist operator schedule is not terminal")
	default:
		return apperror.New(apperror.CodeConflict,
			"specialist operator schedule status is invalid")
	}
}
