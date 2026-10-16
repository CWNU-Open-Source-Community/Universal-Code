package runner

import "context"

type commandRuntimeDispatchKey struct{}
type commandRuntimeAdmissionKey struct{}

type commandRuntimeDispatchBinding struct {
	request context.Context
	check   func(context.Context, CommandRuntimeResolvedSpec) error
}

func withCommandRuntimeDispatchCheck(ctx context.Context,
	check func(context.Context, CommandRuntimeResolvedSpec) error,
) context.Context {
	if check == nil {
		return ctx
	}
	// Jobs own their process lifetime independently of a request context, but
	// detaching that context must not erase cancellation before actual dispatch.
	return context.WithValue(ctx, commandRuntimeDispatchKey{}, commandRuntimeDispatchBinding{request: ctx, check: check})
}

// A sandbox process has the same manager-owned lifetime as a native process.
// Its asynchronous admission may continue after Start returns. Only this
// explicit handoff may detach request cancellation: check the original request
// first, then retain the same host callback on the owned execution context.
// The callback still re-reads current authority at every actual dispatch.
func ownedCommandRuntimeDispatchContext(ctx context.Context, actual CommandRuntimeResolvedSpec) (context.Context, error) {
	if err := CheckCommandRuntimeDispatch(ctx, actual); err != nil {
		return nil, err
	}
	owned := context.WithoutCancel(ctx)
	if binding, ok := ctx.Value(commandRuntimeDispatchKey{}).(commandRuntimeDispatchBinding); ok {
		owned = withCommandRuntimeDispatchCheck(owned, binding.check)
	}
	return owned, nil
}

// CheckCommandRuntimeDispatch is used at native process/sandbox boundaries.
// The callback was injected by the host Start request, not decoded from input.
// Direct legacy runner callers retain their existing admission checks; the
// application always injects the common operation authorizer for new starts.
func CheckCommandRuntimeDispatch(ctx context.Context, actual CommandRuntimeResolvedSpec) error {
	if ctx == nil {
		return ErrCommandRuntimeBoundary
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding, ok := ctx.Value(commandRuntimeDispatchKey{}).(commandRuntimeDispatchBinding); ok {
		if err := binding.request.Err(); err != nil {
			return err
		}
		checkContext := ctx
		if binding.request.Done() != nil && binding.request.Done() != ctx.Done() {
			// An arbitrary WithoutCancel wrapper is not an ownership handoff.
			// Original request cancellation must also interrupt a live recheck.
			combined, cancel := context.WithCancelCause(ctx)
			stop := context.AfterFunc(binding.request, func() { cancel(binding.request.Err()) })
			defer func() { stop(); cancel(context.Canceled) }()
			checkContext = combined
		}
		if err := binding.check(checkContext, actual); err != nil {
			return err
		}
		if err := binding.request.Err(); err != nil {
			return err
		}
		return ctx.Err()
	}
	return nil
}

// ConfirmCommandRuntimeDispatch is called by a sandbox adapter at its final
// native dispatch boundary. Only the manager's process-local execution context
// carries this acknowledgement; tool input and stored receipts cannot supply it.
// Ordinary preflight checks do not release the originating Start call.
func ConfirmCommandRuntimeDispatch(ctx context.Context, actual CommandRuntimeResolvedSpec) error {
	if err := CheckCommandRuntimeDispatch(ctx, actual); err != nil {
		return err
	}
	if acknowledge, ok := ctx.Value(commandRuntimeAdmissionKey{}).(func()); ok {
		acknowledge()
	}
	return ctx.Err()
}
