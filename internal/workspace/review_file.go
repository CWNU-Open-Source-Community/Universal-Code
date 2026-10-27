package workspace

import (
	"io"
	"os"

	"cyberagent-workbench/internal/apperror"
)

// ReadReviewFile reads a complete, bounded regular file using the explorer's
// canonical-path and no-link checks. It does not change the explorer preview cap.
// Callers must redact the returned bytes before projecting them to a client.
func ReadReviewFile(root, requested string, limit int) ([]byte, bool, error) {
	if limit < 1 || limit > 4*1024*1024 {
		return nil, false, apperror.New(apperror.CodeInvalidArgument, "invalid review file limit")
	}
	relative, err := normalizeExplorerPath(requested)
	if err != nil {
		return nil, false, err
	}
	_, expected, err := resolveExplorerTarget(root, relative)
	if err != nil {
		return nil, false, err
	}
	if !expected.Mode().IsRegular() {
		return nil, false, apperror.New(apperror.CodeFailedPrecondition, "review requires a regular file")
	}
	if expected.Size() > int64(limit) {
		return nil, true, nil
	}
	// Root confines the open even if a parent directory changes after validation.
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, apperror.New(apperror.CodeFailedPrecondition, "review root could not be opened")
	}
	defer dir.Close()
	file, err := dir.Open(relative)
	if err != nil {
		return nil, false, apperror.New(apperror.CodeFailedPrecondition, "review file could not be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, false, apperror.New(apperror.CodePolicyDenied, "review file changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, false, apperror.New(apperror.CodeFailedPrecondition, "review file could not be read")
	}
	if len(data) > limit {
		return nil, true, nil
	}
	return data, false, nil
}
