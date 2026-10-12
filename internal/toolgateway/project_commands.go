package toolgateway

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/workspaceidentity"
)

// These IDs are owned by the command registry, never interpreted as repository
// shell text. Execution still goes through command_runtime and its Job ledger.
var projectCommandPresets = map[string]string{
	"test.go":   "go",
	"test.node": "node",
	"test.npm":  "node",
	"format.go": "gofmt",
}

func ProjectCommandExecutable(id string) (string, bool) {
	name, found := projectCommandPresets[id]
	return name, found
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
		if target == "" || filepath.IsAbs(target) || filepath.Ext(target) != ".go" || strings.HasPrefix(target, "-") || strings.ContainsAny(target, "\r\n\x00") {
			return runner.CommandRuntimeSpec{}, fmt.Errorf("format.go requires one relative Go file")
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
