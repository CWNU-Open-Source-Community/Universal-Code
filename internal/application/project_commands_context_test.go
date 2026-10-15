package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorProjectCommandsExposeExactStartIntentOnlyForOfferedHostRuntime(t *testing.T) {
	directory := t.TempDir()
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("intent only"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	project := projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.go"}
	raw, _ := json.Marshal(project)
	run := domain.Run{Config: domain.RunConfig{ProjectConfig: raw, ProjectConfigFingerprint: project.Fingerprint()}}
	offered := supervisorCommandRuntimeTools{Adapter: commandruntimeadapter.HostUnsandboxed("test-context"), Authority: json.RawMessage("{}")}
	messages, err := supervisorProjectCommands(run, nil, offered, "")
	if err != nil || len(messages) != 1 {
		t.Fatalf("project context %#v %v", messages, err)
	}
	marker := "Exact configured test command_runtime start input: "
	_, encoded, found := strings.Cut(messages[0].Content, marker)
	if !found {
		t.Fatal("no actionable configured test input in task context")
	}
	encoded, _, _ = strings.Cut(encoded, "\n")
	var input toolgateway.CommandRuntimeInput
	if err := json.Unmarshal([]byte(encoded), &input); err != nil {
		t.Fatal(err)
	}
	if input.Action != toolgateway.CommandRuntimeActionStart || len(input.Commands) != 1 || input.Commands[0].Purpose != "Configured project action test.go" || input.Commands[0].Executable != filepath.Join(directory, name) {
		t.Fatalf("project ID did not select exact registered input: %#v", input)
	}
	unavailable, err := supervisorProjectCommands(run, nil, supervisorCommandRuntimeTools{}, "")
	if err != nil || len(unavailable) != 1 || strings.Contains(unavailable[0].Content, directory) || strings.Contains(unavailable[0].Content, marker) {
		t.Fatalf("unavailable runtime acquired host command context: %#v %v", unavailable, err)
	}
}

func TestSupervisorProjectCommandsDetectUnconfiguredSandboxTest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.org/detected\ngo 1.26.9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{}
	offered := supervisorCommandRuntimeTools{Adapter: commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "sbx-fixture.v1", "generation"), Authority: json.RawMessage("{}")}
	messages, err := supervisorProjectCommands(run, nil, offered, root)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "Detected project go from go.mod") || !strings.Contains(messages[0].Content, "/usr/bin/go") || !strings.Contains(messages[0].Content, `"-mod=readonly"`) {
		t.Fatalf("detected test context %#v %v", messages, err)
	}
	offered.Adapter = commandruntimeadapter.HostUnsandboxed("generation")
	messages, err = supervisorProjectCommands(run, nil, offered, root)
	if err != nil || len(messages) != 1 || strings.Contains(messages[0].Content, "Exact configured test command_runtime start input:") {
		t.Fatalf("detected test acquired host execution: %#v %v", messages, err)
	}
}
