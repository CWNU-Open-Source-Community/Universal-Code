package apperror

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"testing"
)

func TestTypedErrorPreservesMessageCauseAndMappings(t *testing.T) {
	cause := errors.New("duplicate mapping")
	err := Wrap(CodeConflict, "task is already attached", cause)
	if err.Error() != "task is already attached" || !errors.Is(err, cause) {
		t.Fatalf("unexpected typed error: %v", err)
	}
	if CodeOf(err) != CodeConflict || ExitCode(err) != 4 || HTTPStatus(err) != http.StatusConflict {
		t.Fatalf("unexpected mappings code=%s exit=%d http=%d", CodeOf(err), ExitCode(err), HTTPStatus(err))
	}
}

func TestNormalizeClassifiesStandardAndLegacyErrors(t *testing.T) {
	tests := []struct {
		err  error
		code Code
	}{
		{sql.ErrNoRows, CodeNotFound},
		{&os.PathError{Op: "open", Path: "missing.txt", Err: fs.ErrNotExist}, CodeNotFound},
		{&os.SyscallError{Syscall: "stat", Err: fs.ErrNotExist}, CodeNotFound},
		// Repository code wraps the sentinel, so only errors.Is can see it.
		{fmt.Errorf("read skill file %q: %w", "SKILL.md", fs.ErrNotExist), CodeNotFound},
		{fmt.Errorf("open workspace file: %w",
			&os.PathError{Op: "open", Path: "missing.txt", Err: fs.ErrNotExist}), CodeNotFound},
		{context.Canceled, CodeCancelled},
		{context.DeadlineExceeded, CodeDeadlineExceeded},
		{errors.New("usage: cyberagent run show <run-id>"), CodeInvalidArgument},
		{errors.New("run list limit must be between 1 and 1000"), CodeInvalidArgument},
		{errors.New("policy denied script run: unsafe"), CodePolicyDenied},
		{errors.New("selected model is at capacity"), CodeResourceExhausted},
		{errors.New("run cannot transition from created to completed"), CodeFailedPrecondition},
		{errors.New("UNIQUE constraint failed: legacy_task_runs.task_id"), CodeConflict},
		{errors.New("unexpected sqlite failure"), CodeInternal},
	}
	for _, test := range tests {
		if got := CodeOf(Normalize(test.err)); got != test.code {
			t.Fatalf("Normalize(%q) code=%s want=%s", test.err, got, test.code)
		}
		if Normalize(test.err).Error() != test.err.Error() {
			t.Fatalf("Normalize changed human-readable error: %q", Normalize(test.err))
		}
	}
}
