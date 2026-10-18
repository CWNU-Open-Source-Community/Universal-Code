package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type ThreadCommandServicePolicyStore interface {
	GetThreadCommandServicePolicy(context.Context, string) (domain.ThreadCommandServicePolicy, error)
	SaveThreadCommandServicePolicy(context.Context, string, int64, domain.CommandServicePolicy) (domain.ThreadCommandServicePolicy, error)
}

type ThreadCommandServicePolicyService struct {
	store ThreadCommandServicePolicyStore
}

func NewThreadCommandServicePolicyService(store ThreadCommandServicePolicyStore) *ThreadCommandServicePolicyService {
	return &ThreadCommandServicePolicyService{store: store}
}

func (s *ThreadCommandServicePolicyService) Get(ctx context.Context, threadID string) (domain.ThreadCommandServicePolicy, error) {
	return s.store.GetThreadCommandServicePolicy(ctx, threadID)
}

func (s *ThreadCommandServicePolicyService) Save(ctx context.Context, threadID string, expectedRevision int64, policy domain.CommandServicePolicy) (domain.ThreadCommandServicePolicy, error) {
	return s.store.SaveThreadCommandServicePolicy(ctx, threadID, expectedRevision, policy)
}

func requireSharedCommandServicePolicy(ctx context.Context, store any, runID string) error {
	reader, ok := store.(interface {
		SharedCommandServicesAllowed(context.Context, string) (bool, error)
	})
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "shared service policy reader is unavailable")
	}
	allowed, err := reader.SharedCommandServicesAllowed(ctx, runID)
	if err != nil {
		return err
	}
	if !allowed {
		return apperror.New(apperror.CodePolicyDenied, "this Thread requires exclusive commands; shared service mode is disabled in Settings > Permissions")
	}
	return nil
}
