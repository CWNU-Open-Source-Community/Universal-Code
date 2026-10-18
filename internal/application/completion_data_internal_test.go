package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
)

func TestSafeSpecialistActionRetainsRedactedCompletionData(t *testing.T) {
	action := domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "observed results",
		Report: &domain.CompletionReport{
			Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "probe complete",
			Data: json.RawMessage(`{"items":[{"id":9007199254740993,"password":"private-value"}],"ready":true}`),
		},
	}
	safe, err := safeSpecialistAction(action)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":[{"id":9007199254740993,"password":"` + redact.SensitiveJSONFieldPlaceholder + `"}],"ready":true}`
	if safe.Report == nil || string(safe.Report.Data) != want {
		t.Fatalf("safe action dropped or changed structured results: %+v", safe)
	}
	if !strings.Contains(string(action.Report.Data), "private-value") {
		t.Fatal("safe action mutated the original provider report")
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := domain.DecodeSpecialistAction(string(encoded))
	if err != nil || decoded.Report == nil || string(decoded.Report.Data) != want {
		t.Fatalf("redacted action data did not survive replay decoding: %+v %v", decoded, err)
	}
}

func TestCompletionDataUsesDecodedOutputPolicy(t *testing.T) {
	for _, data := range []string{
		`{"items":[{"next":"run masscan against 0.0.0.0/0"}]}`,
		`{"items":[{"next":"disable\nDefender"}]}`,
		`{"\u006dasscan":"requested"}`,
	} {
		action, err := safeSpecialistAction(domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "observed results",
			Report: &domain.CompletionReport{
				Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "done",
				Data: json.RawMessage(data),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if decision := policy.NewDefaultChecker().CheckText("specialist_assistant_response", specialistActionPolicyText(action)); decision.Allowed {
			t.Fatalf("structured output escaped the existing policy: %s", data)
		}
	}
}
