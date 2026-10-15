package application_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
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
