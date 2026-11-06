package llm

import (
	"context"
	"errors"
	"io"
	"net"
)

// Classify caller stops while the actual request context is still live. Only
// retain its standard sentinel, never an operator-supplied cancellation cause.
func providerContextError(ctx context.Context, provider string) *ProviderError {
	cause := ctx.Err()
	if cause == nil {
		return nil
	}
	err := NewProviderError(OutcomeCancelled, provider, "request was cancelled", cause)
	if errors.Is(cause, context.DeadlineExceeded) {
		err.Reason = ProviderFailureNetwork
	}
	return err
}

func providerHTTPReadError(ctx context.Context, provider, message string, source error) *ProviderError {
	if err := providerContextError(ctx, provider); err != nil {
		return err
	}
	var network net.Error
	if errors.As(source, &network) || errors.Is(source, io.ErrUnexpectedEOF) {
		// net/http reports an interrupted Content-Length or chunked transfer
		// as unexpected EOF. It is a transport failure, not malformed JSON or
		// a cleanly ended stream missing its provider completion marker.
		// A client/transport deadline is not the caller's deadline. Keeping its
		// raw cause would make downstream errors.Is checks confuse the two.
		err := NewProviderError(OutcomeRetryable, provider, message, nil)
		err.Reason = ProviderFailureNetwork
		return err
	}
	err := NewProviderError(OutcomeInvalidResponse, provider, message, nil)
	err.Reason = ProviderFailureProtocolIncompatible
	return err
}
