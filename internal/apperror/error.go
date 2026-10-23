package apperror

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

type Code string

const (
	CodeInvalidArgument    Code = "INVALID_ARGUMENT"
	CodeNotFound           Code = "NOT_FOUND"
	CodeConflict           Code = "CONFLICT"
	CodeFailedPrecondition Code = "FAILED_PRECONDITION"
	CodePolicyDenied       Code = "POLICY_DENIED"
	CodeResourceExhausted  Code = "RESOURCE_EXHAUSTED"
	CodeUnavailable        Code = "UNAVAILABLE"
	CodeCancelled          Code = "CANCELLED"
	CodeDeadlineExceeded   Code = "DEADLINE_EXCEEDED"
	CodeDeadlock           Code = "DEADLOCK"
	CodeLivelock           Code = "LIVELOCK"
	CodeInternal           Code = "INTERNAL"
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func New(code Code, message string) error {
	return &Error{Code: code, Message: strings.TrimSpace(message)}
}

func Wrap(code Code, message string, cause error) error {
	if cause == nil {
		return New(code, message)
	}
	return &Error{Code: code, Message: strings.TrimSpace(message), Cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return CodeInternal
}

func Normalize(err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(CodeCancelled, err.Error(), err)
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(CodeDeadlineExceeded, err.Error(), err)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, fs.ErrNotExist):
		return Wrap(CodeNotFound, err.Error(), err)
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "policy denied"), strings.Contains(message, "blocked by policy"):
		return Wrap(CodePolicyDenied, err.Error(), err)
	case strings.Contains(message, "at capacity"), strings.Contains(message, "resource exhausted"):
		return Wrap(CodeResourceExhausted, err.Error(), err)
	case strings.Contains(message, "temporarily unavailable"), strings.Contains(message, "connection refused"):
		return Wrap(CodeUnavailable, err.Error(), err)
	case strings.HasPrefix(message, "usage:"), strings.Contains(message, " is required"), strings.Contains(message, "must be positive"), strings.Contains(message, "must be between"), strings.Contains(message, "cannot be negative"), strings.Contains(message, "requires at least"), strings.Contains(message, "flag provided but not defined"), strings.HasPrefix(message, "invalid "), strings.HasPrefix(message, "unsupported "), strings.HasPrefix(message, "unknown command"), strings.HasPrefix(message, "unknown "):
		return Wrap(CodeInvalidArgument, err.Error(), err)
	case strings.Contains(message, "not found"), strings.Contains(message, "no such file"):
		return Wrap(CodeNotFound, err.Error(), err)
	case strings.Contains(message, "already attached"), strings.Contains(message, "already exists"), strings.Contains(message, "changed concurrently"), strings.Contains(message, "unique constraint failed"):
		return Wrap(CodeConflict, err.Error(), err)
	case strings.Contains(message, "cannot transition"), strings.Contains(message, "is not active"), strings.Contains(message, "must be active"):
		return Wrap(CodeFailedPrecondition, err.Error(), err)
	default:
		return Wrap(CodeInternal, err.Error(), err)
	}
}

func ExitCode(err error) int {
	switch CodeOf(Normalize(err)) {
	case CodeInvalidArgument:
		return 2
	case CodeNotFound:
		return 3
	case CodeConflict, CodeFailedPrecondition:
		return 4
	case CodePolicyDenied:
		return 5
	case CodeUnavailable:
		return 6
	case CodeCancelled:
		return 7
	case CodeResourceExhausted:
		return 8
	case CodeDeadlineExceeded:
		return 9
	case CodeDeadlock:
		return 10
	case CodeLivelock:
		return 11
	default:
		return 1
	}
}

func HTTPStatus(err error) int {
	switch CodeOf(Normalize(err)) {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeFailedPrecondition:
		return http.StatusPreconditionFailed
	case CodePolicyDenied:
		return http.StatusForbidden
	case CodeResourceExhausted:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeCancelled:
		return 499
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case CodeDeadlock, CodeLivelock:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
