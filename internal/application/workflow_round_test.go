package application

import (
	"context"
	"errors"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkflowFileWriteQueueCancellationDoesNotDispatch(t *testing.T) {
	queue := make(chan struct{}, 1)
	queue <- struct{}{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), workflowFileWriteQueueKey{}, queue))
	cancel()
	_, err := invokeWorkflowWorkerTool(ctx, nil, toolgateway.ToolCall{Name: toolgateway.WorkspaceApplyTool})
	if !errors.Is(err, context.Canceled) || apperror.CodeOf(err) != apperror.CodeCancelled || len(queue) != 1 {
		t.Fatalf("cancelled waiter dispatched or released another writer: %v queue=%d", err, len(queue))
	}
}
