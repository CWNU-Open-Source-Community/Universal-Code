package toolgateway

import (
	"path/filepath"
	"reflect"
	"testing"

	"cyberagent-workbench/internal/runner"
)

func TestProjectCommandsAreNativeRegistryPresets(t *testing.T) {
	for _, test := range []struct {
		id, target string
		args       []string
	}{
		{"test.go", "", []string{"test", "./..."}},
		{"test.node", "", []string{"--test"}},
		{"format.go", "pkg/example.go", []string{"-w", "./pkg/example.go"}},
	} {
		if _, registered := TypedActionIDs()[test.id]; !registered {
			t.Fatalf("missing preset %s", test.id)
		}
		executable := filepath.Join(t.TempDir(), "native-tool.exe")
		spec, err := ProjectCommandSpec(test.id, executable, test.target)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Profile != runner.CommandRuntimeProcess || spec.Script != "" || spec.Executable != executable ||
			!reflect.DeepEqual(spec.Arguments, test.args) || spec.WorkingDirectory != "." || spec.Network != runner.CommandRuntimeNetworkDisabled {
			t.Fatalf("preset %s changed native contract: %#v", test.id, spec)
		}
	}
	for _, target := range []string{"", "../outside.go", "hidden/../example.go", "-flag.go", "x.go\n", "example.txt", `pkg\example.go`} {
		if _, err := ProjectCommandSpec("format.go", filepath.Join(t.TempDir(), "gofmt.exe"), target); err == nil {
			t.Fatalf("unsafe formatter target accepted: %q", target)
		}
	}
	if _, err := ProjectCommandSpec("read_file", filepath.Join(t.TempDir(), "go.exe"), ""); err == nil {
		t.Fatal("generic Tool ID was treated as an executable preset")
	}
	if _, err := ProjectCommandSpec("test.go", "go", ""); err == nil {
		t.Fatal("unresolved executable was accepted")
	}
	launcher := filepath.Join(t.TempDir(), "npm-cli.js")
	npmSpec, err := ProjectCommandSpec("test.npm", filepath.Join(t.TempDir(), "node.exe"), "web", launcher)
	if err != nil || npmSpec.WorkingDirectory != "web" || !reflect.DeepEqual(npmSpec.Arguments, []string{launcher, "test"}) || len(npmSpec.Environment) != 2 {
		t.Fatalf("npm test is not the fixed native launcher: %#v %v", npmSpec, err)
	}
	if _, err := ProjectCommandSpec("test.npm", filepath.Join(t.TempDir(), "node.exe"), "web"); err == nil {
		t.Fatal("npm silently substituted Node's built-in runner")
	}
}
