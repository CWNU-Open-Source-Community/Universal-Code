package application

import (
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/llm"
)

// Existing operator images and earlier projections share the router's byte
// allowance with tool images. Tool receipts remain in context if pixels omit.
func supervisorImageBytes(request llm.ChatRequest) (int, error) {
	total := 0
	for _, message := range request.Messages {
		for _, part := range message.Images {
			total += len(part.Data)
		}
	}
	if total > llm.MaxRequestImageBytes {
		return 0, apperror.New(apperror.CodeResourceExhausted, "existing model image input exceeds the request image bound")
	}
	return total, nil
}
