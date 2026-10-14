package application

import (
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"testing"
)

func TestDockerPreparationWritesUseBoundedToolchainVariant(t *testing.T) {
	for _, test := range []struct {
		executable string
		args       []string
		want       string
	}{
		{"/opt/toolchains/go/bin/go", []string{"mod", "download"}, sandbox.DockerStandardCodeToolchainGoPrepare},
		{"/opt/toolchains/go/bin/go", []string{"test", "./..."}, sandbox.DockerStandardCodeToolchainGo},
		{"/opt/toolchains/node/bin/node", []string{"/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js", "ci", "--offline", "--ignore-scripts"}, sandbox.DockerStandardCodeToolchainNodePrepare},
		{"/opt/toolchains/node/bin/node", []string{"/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js", "test"}, sandbox.DockerStandardCodeToolchainNode},
	} {
		command, err := commandRuntimeDockerCommand(runner.CommandRuntimeResolvedSpec{ExecutablePath: test.executable, CanonicalArgv: test.args, Spec: runner.CommandRuntimeSpec{WorkingDirectory: ".", TimeoutMilliseconds: 10000, Purpose: "Reviewed fixture command"}})
		if err != nil || command.Toolchain != test.want {
			t.Fatalf("toolchain %q want %q err=%v", command.Toolchain, test.want, err)
		}
	}
}
