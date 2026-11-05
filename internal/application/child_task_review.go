package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// ChildTaskReviewStore persists operator review decisions over child task
// proposals. Approve may pin the read-only fan-out tier to an explicit
// user/policy ceiling (1/2/4/6); the model can never raise it.
type ChildTaskReviewStore interface {
	GetChildTaskProposal(ctx context.Context, id string) (domain.ChildTaskProposal, bool, error)
	ReviewChildTaskProposal(ctx context.Context, review domain.ChildTaskReview,
		operationKey string) (domain.ChildTaskProposal, bool, error)
}

// ChildTaskReviewService is the operator-only review surface.
type ChildTaskReviewService struct {
	store ChildTaskReviewStore
}

func NewChildTaskReviewService(store ChildTaskReviewStore) *ChildTaskReviewService {
	return &ChildTaskReviewService{store: store}
}

// Review records one approve/deny decision with idempotency.
func (s *ChildTaskReviewService) Review(ctx context.Context,
	proposalID, action, reviewer string, fanoutTier domain.ReadOnlyFanoutTier,
	operationKey string,
) (domain.ChildTaskProposal, bool, error) {
	if s == nil || s.store == nil {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"child task review store is required")
	}
	review := domain.ChildTaskReview{
		ProposalID: proposalID, Action: action, Reviewer: reviewer, FanoutTier: fanoutTier,
	}
	proposal, replayed, err := s.store.ReviewChildTaskProposal(ctx, review, operationKey)
	return proposal, replayed, apperror.Normalize(err)
}

// Get returns one proposal.
func (s *ChildTaskReviewService) Get(ctx context.Context, proposalID string,
) (domain.ChildTaskProposal, bool, error) {
	if s == nil || s.store == nil {
		return domain.ChildTaskProposal{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"child task review store is required")
	}
	proposal, found, err := s.store.GetChildTaskProposal(ctx, proposalID)
	return proposal, found, apperror.Normalize(err)
}

