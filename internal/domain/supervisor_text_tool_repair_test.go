package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSupervisorTextToolRepairRecognizesOnlyRootTailEnvelope(t *testing.T) {
	root := `{"version":"root_lifecycle.v1","action":"continue","message":"next action"}`
	tail := `<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name="note_create">ignored arguments</｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>`
	if !RootActionHasTrailingToolCalls(root + "\n" + tail) {
		t.Fatal("actual provider envelope was not recognized")
	}
	message, _ := json.Marshal(RootAction{Version: RootLifecycleVersion, Kind: RootActionContinue, Message: "DSML example: " + tail})
	for _, text := range []string{
		string(message), root + "\nDSML is a tool markup format.", root + "\nExample: " + tail,
		root + "\n" + strings.TrimSuffix(tail, "</｜｜DSML｜｜ calls>"),
		strings.Replace(root, `"message":`, `"action":"finish","message":`, 1) + tail,
		strings.Replace(root, `"message":`, `"unknown":true,"message":`, 1) + tail,
	} {
		if RootActionHasTrailingToolCalls(text) {
			t.Fatalf("ordinary discussion or invalid root acquired tool repair: %.100s", text)
		}
	}
	for round := -1; round <= MaxSupervisorExecutionRounds; round++ {
		reason, err := NewSupervisorTextToolRepairReason(round)
		if round < 0 || round == MaxSupervisorExecutionRounds {
			if err == nil {
				t.Fatal("out-of-range correction accepted")
			}
			continue
		}
		got, ok := SupervisorTextToolRepairRound(reason)
		if err != nil || !ok || got != round || IsSupervisorToolRequestRepair(reason) {
			t.Fatal("text confused with native rejected batch")
		}
		if _, ok := SupervisorTextToolRepairRound(reason + " "); ok {
			t.Fatal("noncanonical marker accepted")
		}
	}
}
