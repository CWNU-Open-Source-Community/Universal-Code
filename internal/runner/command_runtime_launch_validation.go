package runner

import "fmt"

// ValidateCommandRuntimeLaunchSpec rechecks the normalized native executable and
// working directory immediately before an adapter creates its launch manifest.
// It shares the Host launch/normalization checks; it does not lock the executable
// or attest the contents of the surrounding toolchain directory.
func ValidateCommandRuntimeLaunchSpec(spec CommandRuntimeResolvedSpec) error {
	if spec.Spec.Version != CommandRuntimeProtocolVersion || !spec.ExecutablePinned || spec.ExecutableIdentityKind != "" {
		return ErrCommandRuntimeBoundary
	}
	if err := validateCommandRuntimeLaunchDirectory(spec); err != nil {
		return fmt.Errorf("%w: working directory or input binding changed before launch", ErrCommandRuntimeBoundary)
	}
	executable, err := resolveCommandRuntimeProcess(spec.ExecutablePath)
	if err != nil || !commandRuntimePathEqual(executable, spec.ExecutablePath) {
		return fmt.Errorf("%w: executable path changed before launch", ErrCommandRuntimeBoundary)
	}
	outside, err := commandRuntimeExecutableOutsideWorkspace(executable, spec.WorkspaceRoot)
	if err != nil || !outside {
		return fmt.Errorf("%w: runtime executable must remain outside the workspace", ErrCommandRuntimeBoundary)
	}
	actualSHA, err := commandRuntimeFileSHA256(executable)
	if err != nil || actualSHA != spec.ExecutableSHA256 {
		return fmt.Errorf("%w: executable content changed before launch", ErrCommandRuntimeBoundary)
	}
	if err := commandRuntimeExecutableAttributes(executable); err != nil {
		return fmt.Errorf("%w: executable is no longer a supported native image", ErrCommandRuntimeBoundary)
	}
	return validateCommandRuntimePythonInvocation(spec)
}
