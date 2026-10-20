package runner

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// CPython on POSIX locates pyvenv.cfg relative to its invocation path before
// dereferencing interpreter symlinks. Keep that path as argv[0], while the
// executable itself remains the canonical, digest-pinned native image.
func commandRuntimePythonInvocationPath(spec CommandRuntimeResolvedSpec) string {
	if runtime.GOOS == "windows" || spec.Spec.Profile != CommandRuntimeProcess || spec.ExecutableIdentityKind != "" {
		return ""
	}
	version, python := strings.CutPrefix(strings.ToLower(filepath.Base(spec.ExecutablePath)), "python")
	if !python || (version != "" && (version[0] < '0' || version[0] > '9' || strings.Trim(version, "0123456789.") != "")) {
		return ""
	}
	invocation := filepath.Clean(strings.TrimSpace(spec.Spec.Executable))
	if !filepath.IsAbs(invocation) || commandRuntimePathEqual(invocation, spec.ExecutablePath) {
		return ""
	}
	return invocation
}

func validateCommandRuntimePythonInvocation(spec CommandRuntimeResolvedSpec) error {
	if invocation := commandRuntimePythonInvocationPath(spec); invocation != "" {
		canonical, err := resolveCommandRuntimeProcess(invocation)
		if err != nil || !commandRuntimePathEqual(canonical, spec.ExecutablePath) {
			return fmt.Errorf("%w: Python invocation path changed before launch", ErrCommandRuntimeBoundary)
		}
	}
	return nil
}
