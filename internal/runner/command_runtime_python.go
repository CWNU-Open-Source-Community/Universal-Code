package runner

import (
	"fmt"
	"os"
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
		if commandRuntimePythonFrameworkInvocation(spec) != "" {
			found := false
			for _, binding := range spec.Environment {
				if strings.HasPrefix(binding, "PYTHONEXECUTABLE=") {
					if binding != "PYTHONEXECUTABLE="+invocation {
						return fmt.Errorf("%w: Python launcher environment changed before launch", ErrCommandRuntimeBoundary)
					}
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%w: Python launcher environment is missing", ErrCommandRuntimeBoundary)
			}
		}
	}
	return nil
}

func commandRuntimePythonFrameworkInvocation(spec CommandRuntimeResolvedSpec) string {
	if runtime.GOOS != "darwin" || !strings.Contains(filepath.ToSlash(spec.ExecutablePath), ".framework/") {
		return ""
	}
	invocation := commandRuntimePythonInvocationPath(spec)
	if invocation == "" {
		return ""
	}
	for _, directory := range []string{filepath.Dir(invocation), filepath.Dir(filepath.Dir(invocation))} {
		if info, err := os.Lstat(filepath.Join(directory, "pyvenv.cfg")); err == nil && info.Mode().IsRegular() {
			return invocation
		}
	}
	return ""
}

func commandRuntimePythonEnvironment(spec *CommandRuntimeSpec, executable string, environment []string) ([]string, error) {
	invocation := commandRuntimePythonFrameworkInvocation(CommandRuntimeResolvedSpec{Spec: *spec, ExecutablePath: executable})
	if invocation == "" {
		return environment, nil
	}
	// Framework's pythonw stub replaces argv[0] and __PYVENV_LAUNCHER__ using
	// _NSGetExecutablePath. CPython gives PYTHONEXECUTABLE precedence over that
	// stub value. Derive it only from the already bound invocation alias, and
	// include it in the ordinary normalized environment digest.
	found := false
	for index, binding := range spec.Environment {
		if strings.EqualFold(binding.Name, "PYTHONEXECUTABLE") && binding.Value != invocation {
			return nil, fmt.Errorf("%w: Python launcher environment must match its invocation path", ErrCommandRuntimeBoundary)
		}
		if strings.EqualFold(binding.Name, "PYTHONEXECUTABLE") {
			spec.Environment[index].Name = "PYTHONEXECUTABLE"
			found = true
		}
	}
	if !found {
		bindings := append(spec.Environment, CommandRuntimeEnvironment{Name: "PYTHONEXECUTABLE", Value: invocation})
		normalized, _, _, err := normalizeCommandRuntimeEnvironment(bindings, spec.Network)
		if err != nil {
			return nil, err
		}
		spec.Environment = normalized
	}
	return replaceCommandRuntimeEnvironment(environment, "PYTHONEXECUTABLE", invocation), nil
}
