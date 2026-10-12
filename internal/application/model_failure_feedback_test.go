package application

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/llm"
)

func TestProviderRefusalHasActionableConversationFeedback(t *testing.T) {
	err := providerApplicationError(&llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureRefusal,
		Provider: "test", Message: "the configured model refused the request"})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "模型拒绝请求") {
		t.Fatalf("refusal feedback=%v", err)
	}
}
