package toolgateway

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/workspaceidentity"
)

// These IDs are owned by the command registry, never interpreted as repository
// shell text. Execution still goes through command_runtime and its Job ledger.
var projectCommandPresets = map[string]string{
	"test.go":     "go",
	"test.node":   "node",
	"test.npm":    "node",
	"test.python": "python",
	"test.pytest": "python",
	"test.rust":   "cargo",
	"format.go":   "gofmt",
}

func ProjectCommandExecutable(id string) (string, bool) {
	name, found := projectCommandPresets[id]
	return name, found
}

// IsProjectGoFile identifies Go source files without changing the native path.
func IsProjectGoFile(target string) bool {
	return path.Ext(strings.TrimRightFunc(target, unicode.IsSpace)) == ".go"
}

func ProjectCommandSpec(id, executable, target string, launcher ...string) (runner.CommandRuntimeSpec, error) {
	if _, found := projectCommandPresets[id]; !found || (!filepath.IsAbs(executable) && !path.IsAbs(executable)) {
		return runner.CommandRuntimeSpec{}, fmt.Errorf("project command preset or native executable is unavailable")
	}
	arguments := []string{}
	workingDirectory := "."
	environment := []runner.CommandRuntimeEnvironment{}
	switch id {
	case "test.go":
		arguments = []string{"test", "./..."}
	case "test.node":
		arguments = []string{"--test"}
	case "test.python":
		arguments = []string{"-m", "unittest", "discover", "-v"}
	case "test.pytest":
		arguments = []string{"-m", "pytest"}
	case "test.rust":
		arguments = []string{"test", "--offline"}
	case "test.npm":
		if len(launcher) != 1 || (!filepath.IsAbs(launcher[0]) && !path.IsAbs(launcher[0])) || filepath.Base(launcher[0]) != "npm-cli.js" {
			return runner.CommandRuntimeSpec{}, fmt.Errorf("test.npm requires the installed npm CLI entrypoint")
		}
		arguments = []string{launcher[0], "test"}
		environment = []runner.CommandRuntimeEnvironment{{Name: "npm_config_audit", Value: "false"}, {Name: "npm_config_fund", Value: "false"}}
	case "format.go":
		if err := (workspaceidentity.PathPolicy{}).Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		if !IsProjectGoFile(target) || strings.HasPrefix(target, "-") {
			return runner.CommandRuntimeSpec{}, fmt.Errorf("format.go requires one relative Go file")
		}
		if err := rootfs.CheckExternalDirectory(target); err != nil {
			return runner.CommandRuntimeSpec{}, apperror.New(apperror.CodeInvalidArgument, err.Error())
		}
		arguments = []string{"-w", "./" + filepath.ToSlash(target)}
	}
	if id != "test.npm" && len(launcher) != 0 {
		return runner.CommandRuntimeSpec{}, fmt.Errorf("unexpected project command launcher")
	}
	if id != "format.go" && target != "" {
		if err := (workspaceidentity.PathPolicy{}).Check(target); err != nil {
			return runner.CommandRuntimeSpec{}, err
		}
		workingDirectory = target
	}
	return runner.NormalizeCommandRuntimeIntent(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: executable, Arguments: arguments, WorkingDirectory: workingDirectory,
		Environment: environment, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 120000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288},
		Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "Configured project action " + id,
	})
}
