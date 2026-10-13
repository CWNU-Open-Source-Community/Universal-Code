package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// ChildTaskControlStore is the operator-facing child task control surface.
type ChildTaskControlStore interface {
	ListChildTaskProposals(context.Context, string, int) ([]domain.ChildTaskProposal, error)
	GetChildTaskProposal(context.Context, string) (domain.ChildTaskProposal, bool, error)
	ListChildTaskAssignments(context.Context, string) ([]domain.ChildTaskAssignment, error)
	ReviewChildTaskProposal(context.Context, domain.ChildTaskReview, string) (domain.ChildTaskProposal, bool, error)
	AdmitChildTaskProposal(context.Context, string, string) (domain.ChildTaskProposal, []domain.ChildTaskAssignment, error)
}

// ChildTaskControlService wires the operator review and admission loop for
// model-proposed child task proposals.
type ChildTaskControlService struct {
	store ChildTaskControlStore
}

func NewChildTaskControlService(store ChildTaskControlStore) *ChildTaskControlService {
	return &ChildTaskControlService{store: store}
}

func (s *ChildTaskControlService) ListChildTaskProposals(ctx context.Context, runID string,
	limit int,
) ([]domain.ChildTaskProposal, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "child task control store is required")
	}
	proposals, err := s.store.ListChildTaskProposals(ctx, runID, limit)
	return proposals, apperror.Normalize(err)
}

func (s *ChildTaskControlService) ListChildTaskAssignments(ctx context.Context,
	proposalID string,
) ([]domain.ChildTaskAssignment, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "child task control store is required")
	}
	assignments, err := s.store.ListChildTaskAssignments(ctx, proposalID)
	return assignments, apperror.Normalize(err)
}

func (s *ChildTaskControlService) ReviewChildTaskProposal(ctx context.Context,
	runID string, review domain.ChildTaskReview, operationKey string,
) (domain.ChildTaskProposal, bool, error) {
	if s == nil || s.store == nil {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"child task control store is required")
	}
	if err := s.requireProposalRun(ctx, runID, review.ProposalID); err != nil {
		return domain.ChildTaskProposal{}, false, err
	}
	proposal, replayed, err := s.store.ReviewChildTaskProposal(ctx, review, operationKey)
	return proposal, replayed, apperror.Normalize(err)
}

func (s *ChildTaskControlService) AdmitChildTaskProposal(ctx context.Context, runID, proposalID,
	operationKey string,
) (domain.ChildTaskProposal, []domain.ChildTaskAssignment, error) {
	if s == nil || s.store == nil {
		return domain.ChildTaskProposal{}, nil, apperror.New(apperror.CodeFailedPrecondition,
			"child task control store is required")
	}
	if err := s.requireProposalRun(ctx, runID, proposalID); err != nil {
		return domain.ChildTaskProposal{}, nil, err
	}
	proposal, assignments, err := s.store.AdmitChildTaskProposal(ctx, proposalID, operationKey)
	return proposal, assignments, apperror.Normalize(err)
}

// A proposal's Run is fixed when it is created. Check that binding before
// review replay or admission can read or change another Run's state.
func (s *ChildTaskControlService) requireProposalRun(ctx context.Context, runID, proposalID string) error {
	proposal, found, err := s.store.GetChildTaskProposal(ctx, proposalID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !found || proposal.RunID != runID {
		return apperror.New(apperror.CodeNotFound, "child task proposal is not part of the run")
	}
	return nil
}
