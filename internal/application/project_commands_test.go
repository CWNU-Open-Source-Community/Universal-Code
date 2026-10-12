package application_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
)

func TestPrepareProjectCommandUsesPinnedPresetAndFormatPolicy(t *testing.T) {
	toolchain := t.TempDir()
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	for _, name := range []string{"go", "gofmt", "node"} {
		if err := os.WriteFile(filepath.Join(toolchain, name+extension), []byte("intent preparation never executes this file"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	npmName := "npm"
	if runtime.GOOS == "windows" {
		npmName += ".cmd"
	}
	if err := os.WriteFile(filepath.Join(toolchain, npmName), []byte("never executed"), 0755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(toolchain, "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.MkdirAll(filepath.Dir(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolchain)
	project := projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.go", FormatCommandID: "format.go", ExcludePaths: []string{"private/**"}}
	pin := func(value projectconfig.Effective) domain.Run {
		raw, _ := json.Marshal(value)
		return domain.Run{Config: domain.RunConfig{ProjectConfig: raw, ProjectConfigFingerprint: value.Fingerprint()}}
	}
	run := pin(project)
	spec, err := application.PrepareProjectCommand(run, "test", "")
	if err != nil || !reflect.DeepEqual(spec.Arguments, []string{"test", "./..."}) || spec.Executable != filepath.Join(toolchain, "go"+extension) {
		t.Fatalf("configured test changed: %#v %v", spec, err)
	}
	formatted, err := application.PrepareProjectCommand(run, "format", "pkg/example.go")
	if err != nil || !reflect.DeepEqual(formatted.Arguments, []string{"-w", "./pkg/example.go"}) {
		t.Fatalf("format spec %#v %v", formatted, err)
	}
	if _, err := application.PrepareProjectCommand(run, "format", "private/example.go"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("excluded formatter target admitted: %v", err)
	}
	project.ReadOnly = true
	if _, err := application.PrepareProjectCommand(pin(project), "format", "pkg/example.go"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("read-only formatter admitted: %v", err)
	}
	project.ReadOnly = false
	project.TestCommandID = "test.npm"
	npmSpec, err := application.PrepareProjectCommand(pin(project), "test", "web", t.TempDir())
	if err != nil || npmSpec.Executable != filepath.Join(toolchain, "node"+extension) || npmSpec.WorkingDirectory != "web" || !reflect.DeepEqual(npmSpec.Arguments, []string{launcher, "test"}) {
		t.Fatalf("npm test launcher changed: %#v %v", npmSpec, err)
	}
	if _, err := application.PrepareProjectCommand(pin(project), "test", "private", t.TempDir()); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("excluded npm scope admitted: %v", err)
	}
	if _, err := application.PrepareProjectCommand(pin(project), "test", "web", toolchain); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("workspace npm launcher admitted: %v", err)
	}
	project.TestCommandID = "read_file"
	if _, err := application.PrepareProjectCommand(pin(project), "test", ""); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("Tool ID became native command: %v", err)
	}
	run.Config.ProjectConfigFingerprint = "bad-binding"
	if _, err := application.PrepareProjectCommand(run, "test", ""); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed pin admitted: %v", err)
	}
}
