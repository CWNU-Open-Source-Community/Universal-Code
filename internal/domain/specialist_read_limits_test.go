package domain

import "testing"

func TestSpecialistReadLimitsRequireLiteralPathsAndBoundedCalls(t *testing.T) {
	for _, p := range []string{"../secret", "/absolute", "a/../b", `C:\secret`, "src/**", "src/", "src\x00", ""} {
		if ValidateSpecialistReadLimits([]string{p}, 2, 1000) == nil {
			t.Fatalf("accepted path %q", p)
		}
	}
	if ValidateSpecialistReadLimits([]string{"src"}, 0, 0) == nil {
		t.Fatal("scope without tool limit")
	}
	if err := ValidateSpecialistReadLimits([]string{".", "src/a.go"}, 2, 1000); err != nil {
		t.Fatal(err)
	}
}

func TestScopedChildTaskRoutesToPersistentChild(t *testing.T) {
	spec := ChildTaskProposalSpec{Version: ChildTaskProposalVersion, Tasks: []ChildTask{{Title: "auth", Goal: "inspect guard", Skills: []string{"model.chat"}, SurfaceHint: ChildTaskSurfaceHintAuto, TurnLimit: 2, TokenLimit: 128, TimeoutMillis: 60000, AllowedReadPaths: []string{"src/auth"}, ToolCallLimit: 4, CostLimitMicros: 1000}}}
	surface, _, err := ResolveChildTaskSurface(spec)
	if err != nil || surface != ChildTaskSurfaceCore {
		t.Fatalf("scope limits lost to fanout: %s %v", surface, err)
	}
	spec.Tasks[0].SurfaceHint = ChildTaskSurfaceHintReadOnlyFanout
	if _, _, err = ResolveChildTaskSurface(spec); err == nil {
		t.Fatal("fanout ignored persistent child limits")
	}
}
