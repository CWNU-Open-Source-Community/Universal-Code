package application_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

func preparationFixture(t *testing.T, npm bool) (projectprep.Plan, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{"go.mod": "module example.org/fixture\ngo 1.26.9\n"}
	target := "."
	if npm {
		target = "web"
		files = map[string]string{"web/package.json": `{"scripts":{"test":"arbitrary repository script"}}`, "web/package-lock.json": `{"lockfileVersion":3,"packages":{"":{}}}`}
	}
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := projectprep.Detect(context.Background(), root, target)
	if err != nil || !plan.Ready {
		t.Fatalf("plan %#v %v", plan, err)
	}
	return plan, root
}

func TestPNPMPreparationSeparatesSeedWorkspaceAndTestDirectories(t *testing.T) {
	plan := projectprep.Plan{Version: projectprep.ProtocolVersion, Fingerprint: strings.Repeat("a", 64), Ready: true,
		Ecosystem: "pnpm", Target: "monorepo/apps/web", WorkspaceRoot: "monorepo", Test: projectprep.Command{ID: "test.pnpm"}}
	adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "fixture.v1", "generation")
	for _, test := range []struct {
		name, directory string
		prepare         func(projectprep.Plan, commandruntimeadapter.Identity, string) (runner.CommandRuntimeSpec, error)
	}{
		{"fetch", projectprep.PNPMSeedDirectory(plan.Target), application.PrepareProjectPreparationFetchCommand},
		{"install", plan.WorkspaceRoot, func(p projectprep.Plan, a commandruntimeadapter.Identity, r string) (runner.CommandRuntimeSpec, error) {
			return application.PrepareProjectPreparationCommand(p, a, r, true)
		}},
		{"rebuild", plan.WorkspaceRoot, application.PrepareProjectPreparationRebuildCommand},
		{"test", plan.Target, application.PrepareProjectPreparationTestCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, err := test.prepare(plan, adapter, t.TempDir())
			if err != nil || spec.WorkingDirectory != test.directory || spec.Executable != "/opt/toolchains/node/bin/node" || spec.Arguments[0] != sandbox.DockerStandardCodePNPMCLI || spec.Network != runner.CommandRuntimeNetworkDisabled || spec.Credentials != runner.CommandRuntimeCredentialsNone {
				t.Fatalf("pnpm command %+v %v", spec, err)
			}
			for _, required := range []string{"--offline", "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", "--store-dir=/workspace/" + projectprep.PNPMStoreDirectory(plan.Target)} {
				if !slices.Contains(spec.Arguments, required) {
					t.Fatalf("missing fixed option %s: %+v", required, spec.Arguments)
				}
			}
			if test.name == "install" || test.name == "fetch" {
				if !slices.Contains(spec.Arguments, "--ignore-scripts") || !slices.Contains(spec.Arguments, "--frozen-lockfile") {
					t.Fatal("default preparation runs scripts or changes lockfile", spec.Arguments)
				}
			}
			if test.name == "rebuild" {
				if !reflect.DeepEqual(spec.Arguments[1:4], []string{"--recursive", "rebuild", "--pending"}) || !slices.Contains(spec.Arguments, "--ignore-scripts=false") {
					t.Fatal("rebuild did not preserve pending builds", spec.Arguments)
				}
				for _, arg := range spec.Arguments {
					if strings.Contains(arg, "allow-all-build") {
						t.Fatal("rebuild bypasses pnpm allowlist")
					}
				}
			}
			if test.name == "test" && (spec.Arguments[len(spec.Arguments)-1] != "test" || !slices.Contains(spec.Arguments, "--config.verify-deps-before-run=false")) {
				t.Fatal("test forwards safety flags or auto-installs", spec.Arguments)
			}
		})
	}
	for _, adapter := range []commandruntimeadapter.Identity{
		commandruntimeadapter.HostUnsandboxed("host"),
		commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeLocalSandboxBackend, "fixture.v1", "generation"),
		commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeSBXBackend, "fixture.v1", "generation"),
	} {
		if _, err := application.PrepareProjectPreparationTestCommand(plan, adapter, t.TempDir()); err == nil || !strings.Contains(err.Error(), "Docker") {
			t.Fatalf("unsupported pnpm backend admitted: %+v %v", adapter, err)
		}
	}
	for _, workspace := range []string{"", "../outside", "another-workspace"} {
		plan.WorkspaceRoot = workspace
		if _, err := application.PrepareProjectPreparationCommand(plan, adapter, t.TempDir(), false); err == nil {
			t.Fatalf("invalid workspace admitted: %q", workspace)
		}
	}
}

func TestProjectPreparationCommandUsesOnlySandboxFixedOfflinePresets(t *testing.T) {
	goPlan, root := preparationFixture(t, false)
	npmPlan, npmRoot := preparationFixture(t, true)
	for _, tc := range []struct{ backend, goExecutable, nodeExecutable, npmLauncher string }{
		{application.CommandRuntimeDockerSandboxBackend, "/opt/toolchains/go/bin/go", "/opt/toolchains/node/bin/node", "/opt/toolchains/node/lib/node_modules/npm/bin/npm-cli.js"},
		{application.CommandRuntimeSBXBackend, "/usr/bin/go", "/usr/bin/node", "/usr/share/nodejs/npm/bin/npm-cli.js"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			adapter := commandruntimeadapter.SandboxedWorkspace(tc.backend, "fixture.v1", "fixture-generation")
			spec, err := application.PrepareProjectPreparationCommand(goPlan, adapter, root, false)
			if err != nil || spec.Executable != tc.goExecutable || !reflect.DeepEqual(spec.Arguments, []string{"mod", "download"}) || spec.Network != runner.CommandRuntimeNetworkDisabled || spec.Credentials != runner.CommandRuntimeCredentialsNone {
				t.Fatalf("go install %#v %v", spec, err)
			}
			testSpec, err := application.PrepareProjectPreparationTestCommand(goPlan, adapter, root)
			if err != nil || testSpec.Executable != tc.goExecutable || !reflect.DeepEqual(testSpec.Arguments, []string{"test", "-mod=readonly", "./..."}) {
				t.Fatalf("go test %#v %v", testSpec, err)
			}
			npmSpec, err := application.PrepareProjectPreparationCommand(npmPlan, adapter, npmRoot, false)
			if err != nil || npmSpec.Executable != tc.nodeExecutable || npmSpec.Arguments[0] != tc.npmLauncher || npmSpec.WorkingDirectory != "web" {
				t.Fatalf("npm install %#v %v", npmSpec, err)
			}
			arguments := strings.Join(npmSpec.Arguments, " ")
			for _, required := range []string{"--offline", "--ignore-scripts", "--bin-links=false", "--global=false", "--prefix=.", "--cache=../.traverse-preparation/"} {
				if !strings.Contains(arguments, required) {
					t.Fatalf("fixed option missing %s: %s", required, arguments)
				}
			}
			if strings.Contains(arguments, "arbitrary") {
				t.Fatal("manifest script became install arguments")
			}
			explicit, err := application.PrepareProjectPreparationCommand(npmPlan, adapter, npmRoot, true)
			if err != nil || !strings.Contains(strings.Join(explicit.Arguments, " "), "--ignore-scripts=false") {
				t.Fatalf("explicit scripts choice missing: %#v %v", explicit, err)
			}
			npmTest, err := application.PrepareProjectPreparationTestCommand(npmPlan, adapter, npmRoot)
			if err != nil || npmTest.Executable != tc.nodeExecutable || npmTest.Arguments[0] != tc.npmLauncher || npmTest.Arguments[1] != "test" || npmTest.Network != runner.CommandRuntimeNetworkDisabled {
				t.Fatalf("npm test %#v %v", npmTest, err)
			}
		})
	}
	if _, err := application.PrepareProjectPreparationCommand(goPlan, commandruntimeadapter.HostUnsandboxed("fixture-generation"), root, false); err == nil {
		t.Fatal("preparation admitted host execution")
	}
	goPlan.Ready = false
	if _, err := application.PrepareProjectPreparationCommand(goPlan, commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeSBXBackend, "fixture.v1", "generation"), root, false); err == nil {
		t.Fatal("blocked preparation plan admitted")
	}
}

func TestProjectPreparationLocalUsesNativeToolchainWithoutExecutingProbe(t *testing.T) {
	plan, root := preparationFixture(t, false)
	toolchain := t.TempDir()
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	executable := filepath.Join(toolchain, "go"+extension)
	if err := os.WriteFile(executable, []byte("intent preparation never starts this file"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolchain)
	adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeLocalSandboxBackend, "fixture.v1", "generation")
	spec, err := application.PrepareProjectPreparationCommand(plan, adapter, root, false)
	if err != nil || spec.Executable != executable {
		t.Fatalf("native preparation intent %#v %v", spec, err)
	}
}
