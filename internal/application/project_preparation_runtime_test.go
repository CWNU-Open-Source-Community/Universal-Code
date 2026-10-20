package application

import (
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"strings"
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
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodePNPMCLI, "fetch", "--offline"}, sandbox.DockerStandardCodeToolchainNodePrepare},
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodePNPMCLI, "install", "--offline"}, sandbox.DockerStandardCodeToolchainNodePrepare},
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodeYarnCLI, "install", "--offline"}, sandbox.DockerStandardCodeToolchainNodePrepare},
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodeYarnCLI, "--offline", "run", "test"}, sandbox.DockerStandardCodeToolchainNode},
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodePNPMCLI, "--recursive", "rebuild", "--pending"}, sandbox.DockerStandardCodeToolchainNodePrepare},
		{"/opt/toolchains/node/bin/node", []string{sandbox.DockerStandardCodePNPMCLI, "--offline", "test"}, sandbox.DockerStandardCodeToolchainNode},
		{"/opt/toolchains/node/bin/node", []string{"/workspace/pnpm.cjs", "install"}, sandbox.DockerStandardCodeToolchainNode},
	} {
		command, err := commandRuntimeDockerCommand(runner.CommandRuntimeResolvedSpec{ExecutablePath: test.executable, CanonicalArgv: test.args, Spec: runner.CommandRuntimeSpec{WorkingDirectory: ".", TimeoutMilliseconds: 10000, Purpose: "Reviewed fixture command"}})
		if err != nil || command.Toolchain != test.want {
			t.Fatalf("toolchain %q want %q err=%v", command.Toolchain, test.want, err)
		}
	}
}

func TestPNPMGuestNormalizerAcceptsOnlyFixedCLIAndOlderImageExplainsRebuild(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	spec := runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: "/opt/toolchains/node/bin/node", Arguments: []string{sandbox.DockerStandardCodePNPMCLI, "--offline", "test"},
		WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed,
		CloseInitialStdin: true, TimeoutMilliseconds: 10000, Network: runner.CommandRuntimeNetworkDisabled,
		Output:      runner.CommandRuntimeOutputPolicy{InlineBytes: 16384, ArtifactBytes: 524288},
		Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "Reviewed pnpm test"}
	if _, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path); err != nil {
		t.Fatalf("fixed pnpm CLI rejected: %v", err)
	}
	for _, launcher := range []string{"/opt/toolchains/node/lib/node_modules/other/bin/pnpm.cjs", `C:\host\pnpm.cjs`} {
		spec.Arguments[0] = launcher
		if _, err := f.service.normalizeCommandRuntimeSpec(spec, f.owned.Path); err == nil {
			t.Fatalf("foreign launcher admitted: %s", launcher)
		}
	}
	if err := f.service.projectPreparationReady(t.Context(), "pnpm"); err == nil || !strings.Contains(err.Error(), "rebuild") || !strings.Contains(err.Error(), projectprep.PNPMVersion) {
		t.Fatalf("old pnpm image diagnostic: %v", err)
	}
}

func TestDetectedProjectChecksWorkspaceRootExclusions(t *testing.T) {
	run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, ExcludePaths: []string{"private"}})}
	plan := projectprep.Plan{Target: "apps/web", WorkspaceRoot: "private", Manifest: projectprep.File{Path: "apps/web/package.json"}}
	if err := checkDetectedProjectPaths(run, plan); err == nil {
		t.Fatal("excluded workspace root admitted")
	}
}

func TestDetectedProjectChecksNPMWorkspaceSiblingManifestExclusions(t *testing.T) {
	run := domain.Run{Config: pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, ExcludePaths: []string{"packages/private"}})}
	plan := projectprep.Plan{Ecosystem: "npm", Target: "apps/web", WorkspaceRoot: ".", Manifest: projectprep.File{Path: "apps/web/package.json"}, Lockfiles: []projectprep.File{{Path: "package.json"}, {Path: "package-lock.json"}, {Path: "packages/private/package.json"}}}
	if err := checkDetectedProjectPaths(run, plan); err == nil {
		t.Fatal("excluded sibling workspace member admitted")
	}
}
