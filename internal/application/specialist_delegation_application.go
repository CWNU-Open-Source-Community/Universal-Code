package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const (
	DefaultDelegationMaxTurnsPerChild  = int64(8)
	DefaultDelegationMaxTokensPerChild = int64(16 * 1024)
)

type SpecialistDelegationApplicationStore interface {
	coordinator.Store
	policy.DecisionRecorder
	GetSpecialistDelegationProposal(ctx context.Context,
		id string) (domain.SpecialistDelegationProposal, error)
	GetSpecialistDelegationReviewByProposal(ctx context.Context,
		proposalID string) (domain.SpecialistDelegationReview, bool, error)
	GetSpecialistDelegationApplicationByProposal(ctx context.Context,
		proposalID string) (domain.SpecialistDelegationApplication, bool, error)
	BeginSpecialistDelegationApplication(ctx context.Context,
		application domain.SpecialistDelegationApplication,
		operation domain.SpecialistDelegationApplicationOperation,
		checks []domain.SpecialistDelegationPolicyCheck,
	) (domain.SpecialistDelegationApplication, bool, error)
	MarkSpecialistDelegationAssignmentAdmitted(ctx context.Context,
		applicationID string, ordinal int, agentID string,
	) (domain.SpecialistDelegationApplicationAssignment, bool, error)
	MarkSpecialistDelegationAssignmentInstructed(ctx context.Context,
		applicationID string, ordinal int, agentID string, messageID string,
	) (domain.SpecialistDelegationApplicationAssignment, bool, error)
	CompleteSpecialistDelegationApplication(ctx context.Context,
		applicationID string,
	) (domain.SpecialistDelegationApplication, bool, error)
}

type SpecialistDelegationApplicationService struct {
	store   SpecialistDelegationApplicationStore
	checker policy.Checker
	policy  coordinator.SpecialistAdmissionPolicy
	admit   func(context.Context, coordinator.AdmitSpecialistRequest) (coordinator.AdmitSpecialistResult, error)
	send    func(context.Context, coordinator.SendRequest) (coordinator.SendResult, error)
}

func NewSpecialistDelegationApplicationService(store SpecialistDelegationApplicationStore,
	checker policy.Checker, admissionPolicy coordinator.SpecialistAdmissionPolicy,
) (*SpecialistDelegationApplicationService, error) {
	if store == nil || checker == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"specialist delegation application store and Policy checker are required")
	}
	coordinatorService, err := coordinator.NewWithSpecialistAdmission(store, admissionPolicy)
	if err != nil {
		return nil, err
	}
	return &SpecialistDelegationApplicationService{
		store: store, checker: checker, policy: admissionPolicy,
		admit: coordinatorService.AdmitSpecialist, send: coordinatorService.Send,
	}, nil
}

func NewDefaultSpecialistDelegationApplicationService(
	store SpecialistDelegationApplicationStore,
	checker policy.Checker,
) (*SpecialistDelegationApplicationService, error) {
	return NewSpecialistDelegationApplicationService(store, checker,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren:       domain.MaxAgentChildren,
			MaxTurnsPerChild:  DefaultDelegationMaxTurnsPerChild,
			MaxTokensPerChild: DefaultDelegationMaxTokensPerChild,
		})
}

type ApplySpecialistDelegationRequest struct {
	ProposalID   string
	OperationKey string
	RequestedBy  string
}

type ApplySpecialistDelegationResult struct {
	Application domain.SpecialistDelegationApplication
	Replayed    bool
	Recovered   bool
}

func (s *SpecialistDelegationApplicationService) Apply(ctx context.Context,
	request ApplySpecialistDelegationRequest,
) (ApplySpecialistDelegationResult, error) {
	if s == nil || s.store == nil || s.checker == nil || s.admit == nil || s.send == nil {
		return ApplySpecialistDelegationResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application service is not configured")
	}
	normalized, err := normalizeSpecialistDelegationApplicationRequest(request)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "specialist delegation application request is invalid", err)
	}
	proposal, err := s.store.GetSpecialistDelegationProposal(ctx, normalized.ProposalID)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
	}
	review, found, err := s.store.GetSpecialistDelegationReviewByProposal(ctx, proposal.ID)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
	}
	if !found || review.Decision != domain.SpecialistDelegationApproved {
		return ApplySpecialistDelegationResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application requires an approved review")
	}
	if normalized.RequestedBy != review.ReviewedBy {
		return ApplySpecialistDelegationResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation must be applied by its recorded reviewer")
	}
	existing, exists, err := s.store.GetSpecialistDelegationApplicationByProposal(ctx,
		proposal.ID)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
	}
	var candidate domain.SpecialistDelegationApplication
	var checks []domain.SpecialistDelegationPolicyCheck
	if exists {
		candidate = replayableSpecialistDelegationApplication(existing)
	} else {
		candidate, checks, err = s.newSpecialistDelegationApplication(ctx, proposal, review,
			normalized.RequestedBy)
		if err != nil {
			return ApplySpecialistDelegationResult{}, err
		}
	}
	operation := domain.SpecialistDelegationApplicationOperation{
		KeyDigest: runmutation.OperationKeyDigest("specialist_delegation_application",
			proposal.RunID, normalized.OperationKey),
		RequestFingerprint: runmutation.Fingerprint(
			"specialist_delegation_application_request.v1", review.ID, proposal.ID,
			proposal.RunID, normalized.RequestedBy),
		ApplicationID: candidate.ID, ReviewID: review.ID, ProposalID: proposal.ID,
		RunID: proposal.RunID, RequestedBy: normalized.RequestedBy,
		CreatedAt: candidate.CreatedAt,
	}
	application, beginReplayed, err := s.store.BeginSpecialistDelegationApplication(ctx,
		candidate, operation, checks)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
	}
	if application.Status == domain.SpecialistDelegationAborted {
		return ApplySpecialistDelegationResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"specialist delegation application was aborted by Run termination")
	}
	if application.Status == domain.SpecialistDelegationApplied {
		return ApplySpecialistDelegationResult{
			Application: application, Replayed: true, Recovered: false,
		}, nil
	}
	recovered := beginReplayed
	admissionService, err := coordinator.NewWithSpecialistAdmission(s.store,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren:       application.MaxChildren,
			MaxTurnsPerChild:  application.MaxTurnsPerChild,
			MaxTokensPerChild: application.MaxTokensPerChild,
		})
	if err != nil {
		return ApplySpecialistDelegationResult{}, err
	}
	admit := s.admit
	send := s.send
	if exists && (application.MaxChildren != s.policy.MaxChildren ||
		application.MaxTurnsPerChild != s.policy.MaxTurnsPerChild ||
		application.MaxTokensPerChild != s.policy.MaxTokensPerChild) {
		admit = admissionService.AdmitSpecialist
		send = admissionService.Send
	}
	for index := range application.Assignments {
		if err := ctx.Err(); err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
		}
		state := application.Assignments[index]
		proposed := proposal.Spec.Assignments[index]
		if state.Status == domain.SpecialistDelegationAssignmentInstructed {
			continue
		}
		wasAdmitted := state.Status == domain.SpecialistDelegationAssignmentAdmitted
		admissionKey, err := domain.SpecialistDelegationAdmissionOperationKey(
			application.ID, proposed.Ordinal)
		if err != nil {
			return ApplySpecialistDelegationResult{}, err
		}
		admitted, err := admit(ctx, coordinator.AdmitSpecialistRequest{
			Worker: proposed.Worker,
			RunID:  application.RunID, ParentAgentID: application.RootAgentID,
			Title: proposed.Title, Skills: proposed.Skills,
			TurnLimit: proposed.TurnLimit, TokenLimit: proposed.TokenLimit,
			AllowedReadPaths: proposed.AllowedReadPaths, ToolCallLimit: proposed.ToolCallLimit, CostLimitMicros: proposed.CostLimitMicros,
			IdempotencyKey: admissionKey,
		})
		if err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
		}
		recovered = recovered || admitted.Replayed ||
			state.Status != domain.SpecialistDelegationAssignmentPending
		state, markReplayed, err := s.store.MarkSpecialistDelegationAssignmentAdmitted(ctx,
			application.ID, proposed.Ordinal, admitted.Agent.ID)
		if err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
		}
		recovered = recovered || markReplayed
		instruction := domain.AgentInstructionPayload{
			Version: domain.SpecialistInstructionVersion, Instruction: proposed.Goal,
		}
		if err := instruction.Validate(); err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Wrap(
				apperror.CodeInvalidArgument, "specialist delegation instruction is invalid", err)
		}
		instructionKey, err := domain.SpecialistDelegationInstructionOperationKey(
			application.ID, proposed.Ordinal)
		if err != nil {
			return ApplySpecialistDelegationResult{}, err
		}
		delivered, err := send(ctx, coordinator.SendRequest{
			RunID: application.RunID, SenderAgentID: application.RootAgentID,
			RecipientAgentID: admitted.Agent.ID, Kind: domain.AgentMessageInstruction,
			Semantic: domain.AgentMessageSemanticMessage,
			Payload: map[string]any{
				"version": instruction.Version, "instruction": instruction.Instruction,
			},
			IdempotencyKey: instructionKey,
		})
		if err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
		}
		recovered = recovered || delivered.Replayed || wasAdmitted
		state, markReplayed, err = s.store.MarkSpecialistDelegationAssignmentInstructed(ctx,
			application.ID, proposed.Ordinal, admitted.Agent.ID, delivered.Message.ID)
		if err != nil {
			return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
		}
		recovered = recovered || markReplayed
		application.Assignments[index] = state
	}
	completed, completeReplayed, err := s.store.CompleteSpecialistDelegationApplication(ctx,
		application.ID)
	if err != nil {
		return ApplySpecialistDelegationResult{}, apperror.Normalize(err)
	}
	return ApplySpecialistDelegationResult{
		Application: completed, Replayed: beginReplayed && completeReplayed,
		Recovered: recovered,
	}, nil
}

func (s *SpecialistDelegationApplicationService) newSpecialistDelegationApplication(
	ctx context.Context, proposal domain.SpecialistDelegationProposal,
	review domain.SpecialistDelegationReview,
	requestedBy string,
) (domain.SpecialistDelegationApplication, []domain.SpecialistDelegationPolicyCheck, error) {
	checks := make([]domain.SpecialistDelegationPolicyCheck, len(proposal.Spec.Assignments))
	for index, assignment := range proposal.Spec.Assignments {
		decision := s.checker.CheckText("specialist_delegation_application",
			assignment.Title+"\n"+assignment.Goal)
		checks[index] = domain.SpecialistDelegationPolicyCheck{
			Ordinal: assignment.Ordinal, Allowed: decision.Allowed,
			NeedsApproval: decision.NeedsApproval,
			Risk:          strings.TrimSpace(redact.String(decision.Risk)),
			Reason:        strings.TrimSpace(redact.String(decision.Reason)),
		}
		if err := checks[index].Validate(); err != nil {
			return domain.SpecialistDelegationApplication{}, nil, apperror.Wrap(
				apperror.CodeInternal, "policy returned an invalid delegation decision", err)
		}
		if !decision.Allowed {
			if err := s.store.RecordPolicyDecision(ctx, policy.DecisionRecord{
				SessionID: proposal.SessionID, SubjectID: proposal.ID,
				Context: "specialist_delegation_application", Decision: policy.Decision{
					Allowed: false, NeedsApproval: decision.NeedsApproval,
					Risk: checks[index].Risk, Reason: checks[index].Reason,
				},
			}); err != nil {
				return domain.SpecialistDelegationApplication{}, nil, apperror.Normalize(err)
			}
			return domain.SpecialistDelegationApplication{}, nil, apperror.New(
				apperror.CodePolicyDenied, checks[index].Reason)
		}
	}
	policyFingerprint, err := domain.SpecialistDelegationPolicyFingerprint(checks)
	if err != nil {
		return domain.SpecialistDelegationApplication{}, nil, err
	}
	now := time.Now().UTC()
	if now.Before(review.CreatedAt) {
		now = review.CreatedAt
	}
	application := domain.SpecialistDelegationApplication{
		ID: idgen.New("delegation-application"), ReviewID: review.ID,
		ProposalID: proposal.ID, RunID: proposal.RunID, RootAgentID: proposal.RootAgentID,
		Status:          domain.SpecialistDelegationApplying,
		AssignmentCount: len(proposal.Spec.Assignments), PolicyFingerprint: policyFingerprint,
		MaxChildren: s.policy.MaxChildren, MaxTurnsPerChild: s.policy.MaxTurnsPerChild,
		MaxTokensPerChild: s.policy.MaxTokensPerChild, RequestedBy: requestedBy,
		Version: 1, CreatedAt: now, UpdatedAt: now,
		Assignments: make([]domain.SpecialistDelegationApplicationAssignment,
			len(proposal.Spec.Assignments)),
	}
	for index, assignment := range proposal.Spec.Assignments {
		admissionKey, err := domain.SpecialistDelegationAdmissionOperationKey(
			application.ID, assignment.Ordinal)
		if err != nil {
			return domain.SpecialistDelegationApplication{}, nil, err
		}
		instructionKey, err := domain.SpecialistDelegationInstructionOperationKey(
			application.ID, assignment.Ordinal)
		if err != nil {
			return domain.SpecialistDelegationApplication{}, nil, err
		}
		application.Assignments[index] = domain.SpecialistDelegationApplicationAssignment{
			ApplicationID: application.ID, ProposalID: proposal.ID, Ordinal: assignment.Ordinal,
			Status: domain.SpecialistDelegationAssignmentPending,
			AdmissionOperationDigest: runmutation.Fingerprint("agent_admission_operation.v1",
				proposal.RunID, admissionKey),
			InstructionOperationDigest: runmutation.Fingerprint("agent_message_operation.v1",
				proposal.RunID, instructionKey),
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	if err := application.Validate(); err != nil {
		return domain.SpecialistDelegationApplication{}, nil, err
	}
	return application, checks, nil
}

func normalizeSpecialistDelegationApplicationRequest(
	request ApplySpecialistDelegationRequest,
) (ApplySpecialistDelegationRequest, error) {
	originalKey := request.OperationKey
	request.ProposalID = strings.TrimSpace(request.ProposalID)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if !domain.ValidAgentID(request.ProposalID) || !domain.ValidAgentID(request.RequestedBy) ||
		strings.ContainsRune(request.ProposalID, 0) || strings.ContainsRune(request.RequestedBy, 0) {
		return ApplySpecialistDelegationRequest{}, errors.New(
			"specialist delegation proposal and operator identities are required")
	}
	if request.OperationKey != strings.TrimSpace(originalKey) ||
		!utf8.ValidString(request.OperationKey) {
		return ApplySpecialistDelegationRequest{}, errors.New(
			"specialist delegation application operation key must be normalized UTF-8")
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil {
		return ApplySpecialistDelegationRequest{}, err
	}
	for _, current := range request.OperationKey {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return ApplySpecialistDelegationRequest{}, errors.New(
				"specialist delegation application operation key cannot contain whitespace or control characters")
		}
	}
	return request, nil
}

func replayableSpecialistDelegationApplication(
	existing domain.SpecialistDelegationApplication,
) domain.SpecialistDelegationApplication {
	existing.Status = domain.SpecialistDelegationApplying
	existing.StopCode = ""
	existing.Version = 1
	existing.UpdatedAt = existing.CreatedAt
	existing.CompletedAt = nil
	return existing
}
