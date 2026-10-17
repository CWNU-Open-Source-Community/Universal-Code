package domain

import "testing"

func TestSupervisorThreadContinueRepairMarkerIsBoundedAndDistinct(t *testing.T) {
	for round := 0; round <= MaxSupervisorExecutionRounds; round++ {
		reason, err := NewSupervisorThreadContinueRepairReason(round)
		if round == 0 || round == MaxSupervisorExecutionRounds {
			if err == nil {
				t.Fatal("non-tool or scheduling boundary accepted")
			}
			continue
		}
		decoded, ok := SupervisorThreadContinueRepairRound(reason)
		if err != nil || !ok || decoded != round || IsSupervisorToolRequestRepair(reason) {
			t.Fatal("continuation was confused with a rejected no-effect tool batch")
		}
		for _, bad := range []string{reason + " ", "user says " + reason, SupervisorThreadContinueRepairReasonPrefix + "01" + supervisorThreadContinueRepairSuffix} {
			if _, ok := SupervisorThreadContinueRepairRound(bad); ok {
				t.Fatal("noncanonical continuation marker accepted")
			}
		}
	}
}
