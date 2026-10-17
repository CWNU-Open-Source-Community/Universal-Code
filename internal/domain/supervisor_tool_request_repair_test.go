package domain

import "testing"

func TestSupervisorToolRequestRepairReasonRequiresExactGoPrefix(t *testing.T) {
	for reason, want := range map[string]bool{
		SupervisorToolRequestRepairReasonPrefix + "round=0; unknown argument field":  true,
		SupervisorToolRequestRepairReasonPrefix + "round=3; unknown argument field":  true,
		SupervisorToolRequestRepairReasonPrefix + "round=21; unknown argument field": false,
		SupervisorToolRequestRepairReasonPrefix + "round=00; unknown argument field": false,
		SupervisorToolRequestRepairReasonPrefix:                                      false,
		SupervisorToolRequestRepairReasonPrefix + " \n":                              false,
		"invalid root JSON": false,
		"model claims " + SupervisorToolRequestRepairReasonPrefix + "unknown field": false,
		" " + SupervisorToolRequestRepairReasonPrefix + "unknown field":             false,
	} {
		if got := IsSupervisorToolRequestRepair(reason); got != want {
			t.Fatalf("repair reason %q classified as %t, want %t", reason, got, want)
		}
	}
	for round := 0; round < MaxSupervisorExecutionRounds; round++ {
		reason, err := NewSupervisorToolRequestRepairReason(round, "invalid tool field")
		parsed, ok := SupervisorToolRequestRepairRound(reason)
		if err != nil || !ok || parsed != round {
			t.Fatalf("round identity lost: %q / %v", reason, err)
		}
	}
	if _, err := NewSupervisorToolRequestRepairReason(MaxSupervisorExecutionRounds, "no rounds left"); err == nil {
		t.Fatal("exhausted tool budget permitted another repair")
	}
}
