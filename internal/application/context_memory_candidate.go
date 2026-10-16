package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/session"
)

type ContextMemoryCandidateStore interface {
	ListContextMemoryCandidates(context.Context, contextmgr.MemoryCandidateFilter, time.Time) ([]contextmgr.MemoryCandidate, error)
	GetContextMemoryCandidate(context.Context, string, string, time.Time) (contextmgr.MemoryCandidate, error)
	ReviewContextMemoryCandidate(context.Context, string, contextmgr.ReviewMemoryCandidateRequest, contextmgr.MemoryCandidateStatus, time.Time) (contextmgr.MemoryCandidateReviewResult, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

type ContextMemoryCandidateService struct{ store ContextMemoryCandidateStore }

func NewContextMemoryCandidateService(store ContextMemoryCandidateStore) *ContextMemoryCandidateService {
	return &ContextMemoryCandidateService{store: store}
}

// Listing derives a bounded review queue from already persisted conclusions.
// It never writes context_memories or requests a model/tool execution.
func (s *ContextMemoryCandidateService) List(ctx context.Context, filter contextmgr.MemoryCandidateFilter) ([]contextmgr.MemoryCandidate, error) {
	if err := filter.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if err := s.workspace(ctx, filter.WorkspaceID); err != nil {
		return nil, err
	}
	return s.store.ListContextMemoryCandidates(ctx, filter, time.Now().UTC())
}

func (s *ContextMemoryCandidateService) Get(ctx context.Context, id, workspaceID string) (contextmgr.MemoryCandidate, error) {
	if err := s.workspace(ctx, workspaceID); err != nil {
		return contextmgr.MemoryCandidate{}, err
	}
	return s.store.GetContextMemoryCandidate(ctx, id, workspaceID, time.Now().UTC())
}

func (s *ContextMemoryCandidateService) Confirm(ctx context.Context, id string, request contextmgr.ReviewMemoryCandidateRequest) (contextmgr.MemoryCandidateReviewResult, error) {
	if !request.Confirm {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.New(apperror.CodeInvalidArgument, "memory candidate confirmation must be explicit")
	}
	return s.review(ctx, id, request, contextmgr.MemoryCandidateConfirmed)
}

func (s *ContextMemoryCandidateService) Reject(ctx context.Context, id string, request contextmgr.ReviewMemoryCandidateRequest) (contextmgr.MemoryCandidateReviewResult, error) {
	return s.review(ctx, id, request, contextmgr.MemoryCandidateRejected)
}

func (s *ContextMemoryCandidateService) review(ctx context.Context, id string, request contextmgr.ReviewMemoryCandidateRequest, status contextmgr.MemoryCandidateStatus) (contextmgr.MemoryCandidateReviewResult, error) {
	if err := request.Validate(); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if err := s.workspace(ctx, request.WorkspaceID); err != nil {
		return contextmgr.MemoryCandidateReviewResult{}, err
	}
	return s.store.ReviewContextMemoryCandidate(ctx, id, request, status, time.Now().UTC())
}

func (s *ContextMemoryCandidateService) workspace(ctx context.Context, id string) error {
	if s == nil || s.store == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "memory candidate store is required")
	}
	if err := (contextmgr.MemoryCandidateFilter{WorkspaceID: id}).Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, id)
	if err != nil {
		return err
	}
	if workspace.ID != id || workspace.RootPath == "" {
		return apperror.New(apperror.CodeFailedPrecondition, "memory candidate Workspace binding is invalid")
	}
	return nil
}
