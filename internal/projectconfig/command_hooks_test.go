package projectconfig

import "testing"

func TestCommandHooksRequireRegisteredProjectActions(t *testing.T) {
	for _, c := range []Config{
		{Hooks: &CommandHooks{AfterEditFormat: true}},
		{Hooks: &CommandHooks{AfterEditFormat: true}, FormatCommandID: "shell"},
		{Hooks: &CommandHooks{BeforeFinishTest: true}, TestCommandID: "read_file"},
	} {
		if err := c.validate(); err == nil {
			t.Fatal("unregistered hook action accepted")
		}
	}
	e := Effective{Hooks: &CommandHooks{AfterEditFormat: true, BeforeFinishTest: true}, FormatCommandID: "format.go", TestCommandID: "test.go"}
	first := e.HooksFingerprint()
	if first == "" {
		t.Fatal("missing hook identity")
	}
	e.MaxTurns, e.MaxToolCalls = 20, 100
	if e.HooksFingerprint() != first {
		t.Fatal("task budget changed project trust")
	}
	e.TestCommandID = "test.npm"
	if e.HooksFingerprint() == first {
		t.Fatal("changed test retained trust")
	}
	e.TestCommandID = "test.go"
	e.ExcludePaths = []string{"private/**"}
	if e.HooksFingerprint() == first {
		t.Fatal("changed path restrictions retained trust")
	}
}
