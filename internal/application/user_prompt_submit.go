package application

import (
	"context"
	"cyberagent-workbench/internal/domain"
)

// A committed submission has already passed the prompt boundary. Retries must
// recover its durable outcome even after the operator changes hook policy.
func (s *ThreadService) promptAlreadySubmitted(ctx context.Context, request SubmitThreadMessageRequest) (bool, error) {
	reader, ok := s.store.(interface {
		InspectThreadMessageIntent(context.Context, domain.ThreadMessageIntentRequest) (domain.ThreadMessageIntent, bool, error)
	})
	if !ok {
		return false, nil
	}
	intent, found, err := reader.InspectThreadMessageIntent(ctx, threadMessageIntentRequest(request))
	return found && intent.MessageID != "", err
}

func sessionPromptAlreadySubmitted(ctx context.Context, source any, sessionID, operationKey string) (bool, error) {
	if operationKey == "" {
		return false, nil
	}
	reader, ok := source.(interface {
		InspectOperatorSteeringOperation(context.Context, string, string) (domain.OperatorSteeringMessage, bool, error)
	})
	if !ok {
		return false, nil
	}
	_, found, err := reader.InspectOperatorSteeringOperation(ctx, sessionID, operationKey)
	return found, err
}
