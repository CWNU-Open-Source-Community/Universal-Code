package domain

import (
	"strings"
	"testing"
)

func TestChildTaskProposalSpecIsStrict(t *testing.T) {
	valid := `{"version":"child_task_proposal.v1","tasks":[{"title":"Read parser","goal":"Inspect parser files","skills":["model.chat","read_file"],"input_refs":["src/parser"],"surface_hint":"auto","turn_limit":2,"token_limit":512,"timeout_millis":120000,"expected_artifacts":[{"path_hint":"notes/parser.md","kind":"note"}]}]}`
	spec, err := DecodeChildTaskProposalSpec([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Tasks[0].Ordinal != 1 || len(spec.Tasks[0].InputRefs) != 1 ||
		spec.Tasks[0].InputRefs[0] != "src/parser" {
		t.Fatalf("spec did not normalize: %#v", spec)
	}
	for _, invalid := range []string{
		`{"version":"child_task_proposal.v1","tasks":[],"extra":true}`,
		`{"version":"child_task_proposal.v1","tasks":[{"title":"A","goal":"B","skills":["model.chat"],"turn_limit":1,"token_limit":1,"timeout_millis":1,"dependency_ordinals":[1]}]}`,
		`{"version":"child_task_proposal.v1","tasks":[{"title":"A","goal":"B","skills":["model.chat"],"turn_limit":1,"token_limit":1,"timeout_millis":1,"input_refs":["../escape"]}]}`,
	} {
		if _, err := DecodeChildTaskProposalSpec([]byte(invalid)); err == nil {
			t.Fatalf("invalid spec was accepted: %s", invalid)
		}
	}
}

func TestChildTaskDependencyCyclesRejected(t *testing.T) {
	raw := `{"version":"child_task_proposal.v1","tasks":[
		{"title":"A","goal":"first","skills":["model.chat","note_create"],"turn_limit":1,"token_limit":64,"timeout_millis":60000,"dependency_ordinals":[2]},
		{"title":"B","goal":"second","skills":["model.chat","note_create"],"turn_limit":1,"token_limit":64,"timeout_millis":60000,"dependency_ordinals":[1]}]}`
	if _, err := DecodeChildTaskProposalSpec([]byte(raw)); err == nil ||
		!strings.Contains(err.Error(), "cycle") {
		t.Fatalf("dependency cycle was not rejected: %v", err)
	}
}

func TestResolveChildTaskSurface(t *testing.T) {
	core := ChildTask{Title: "fix", Goal: "edit the file", Skills: []string{"model.chat", "replace_file"},
		TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000}
	core2 := core
	core2.Title, core2.Goal = "fix2", "edit the second file"
	surface, tier, err := ResolveChildTaskSurface(ChildTaskProposalSpec{
		Version: ChildTaskProposalVersion, Tasks: []ChildTask{core, core2}})
	if err != nil || surface != ChildTaskSurfaceCore || tier != "" {
		t.Fatalf("core resolution failed: %q %q %v", surface, tier, err)
	}
	readTasks := make([]ChildTask, 4)
	for index := range readTasks {
		readTasks[index] = ChildTask{Title: "read" + string(rune('a'+index)),
			Goal:      "inspect files " + string(rune('a'+index)),
			Skills:    []string{"model.chat", "read_file"},
			TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000}
	}
	surface, tier, err = ResolveChildTaskSurface(ChildTaskProposalSpec{
		Version: ChildTaskProposalVersion, Tasks: readTasks})
	if err != nil || surface != ChildTaskSurfaceReadOnlyFanout || tier != ReadOnlyFanoutFour {
		t.Fatalf("fan-out resolution failed: %q %q %v", surface, tier, err)
	}
	// Three write-capable tasks cannot fit the core surface.
	core3 := core2
	core3.Title, core3.Goal = "fix3", "edit the third file"
	if _, _, err := ResolveChildTaskSurface(ChildTaskProposalSpec{
		Version: ChildTaskProposalVersion, Tasks: []ChildTask{core, core2, core3}}); err == nil {
		t.Fatal("three core tasks were accepted")
	}
	// Mixed hints are rejected.
	mixedRead := readTasks[0]
	mixedRead.SurfaceHint = ChildTaskSurfaceHintReadOnlyFanout
	if _, _, err := ResolveChildTaskSurface(ChildTaskProposalSpec{
		Version: ChildTaskProposalVersion, Tasks: []ChildTask{core, mixedRead}}); err == nil {
		t.Fatal("mixed surface hints were accepted")
	}
}

func TestResolveChildTaskSurfaceHonorsReadOnlyCoreHint(t *testing.T) {
	for _, test := range []struct {
		name  string
		count int
	}{
		{name: "one child", count: 1},
		{name: "two children", count: 2},
		{name: "three children rejected", count: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			tasks := make([]ChildTask, test.count)
			for index := range tasks {
				tasks[index] = ChildTask{
					Title:  "read " + string(rune('a'+index)),
					Goal:   "inspect files " + string(rune('a'+index)),
					Skills: []string{"model.chat"}, SurfaceHint: ChildTaskSurfaceHintCore,
					TurnLimit: 4, TokenLimit: 4096, TimeoutMillis: 60000,
				}
			}
			surface, tier, err := ResolveChildTaskSurface(ChildTaskProposalSpec{
				Version: ChildTaskProposalVersion, Tasks: tasks,
			})
			if test.count > MaxAgentChildren {
				if err == nil || !strings.Contains(err.Error(), "core child tasks cannot exceed") {
					t.Fatalf("core capacity was not enforced: %q %q %v", surface, tier, err)
				}
				return
			}
			if err != nil || surface != ChildTaskSurfaceCore || tier != "" {
				t.Fatalf("read-only core hint was ignored: %q %q %v", surface, tier, err)
			}
		})
	}
}
