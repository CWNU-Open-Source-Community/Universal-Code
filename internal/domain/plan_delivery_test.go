package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlanDeliverySpecKeepsThreeBoundedAcyclicDirectionsCompatible(t *testing.T) {
	raw := []byte(`{"version":"plan_delivery.v1","directions":[` +
		`{"title":"Conservative","summary":"Minimize change risk.","tradeoffs":["Slower delivery"],"modules":[` +
		`{"title":"Inspect","objective":"Read the current boundary.","acceptance_criteria":["Boundary documented"],"dependencies":[]},` +
		`{"title":"Implement","objective":"Make the focused change.","acceptance_criteria":["Focused tests pass"],"dependencies":[1]}]},` +
		`{"title":"Balanced","summary":"Balance speed and risk.","tradeoffs":["Moderate breadth"],"modules":[` +
		`{"title":"Vertical slice","objective":"Implement one end-to-end path.","acceptance_criteria":["Path is usable"],"dependencies":[]}]},` +
		`{"title":"Accelerated","summary":"Prefer broader parallel progress.","tradeoffs":["Higher review cost"],"modules":[` +
		`{"title":"Batch","objective":"Prepare bounded independent changes.","acceptance_criteria":["Changes remain bounded"],"dependencies":[]}]}]}`)
	spec, err := DecodePlanDeliverySpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Directions) != PlanDeliveryDirectionCount ||
		spec.Directions[0].Ordinal != 1 ||
		spec.Directions[0].Modules[1].Ordinal != 2 ||
		len(spec.Directions[0].Modules[1].Dependencies) != 1 {
		t.Fatalf("unexpected normalized Plan/Delivery spec: %#v", spec)
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	redecoded, err := DecodePlanDeliverySpec(canonical)
	if err != nil || redecodeFingerprint(spec) != redecodeFingerprint(redecoded) {
		t.Fatalf("canonical Plan/Delivery payload drifted: %s err=%v", canonical, err)
	}
}

func TestPlanDeliverySpecAcceptsOneToThreeDirectionsWithoutChangingCanonicalContent(t *testing.T) {
	for count := 1; count <= 4; count++ {
		spec := PlanDeliverySpec{Version: PlanDeliveryProtocolVersion}
		for i := 0; i < count; i++ {
			spec.Directions = append(spec.Directions, PlanDeliveryDirection{Title: fmt.Sprintf("Direction %d", i+1), Summary: "Make the requested bounded change",
				Tradeoffs: []string{"Keep the existing API"}, Modules: []PlanDeliveryModule{{Title: "Implement", Objective: "Apply the exact change", AcceptanceCriteria: []string{"The changed behavior is checked"}, Dependencies: []int{}}}})
		}
		original, _ := json.Marshal(spec)
		normalized, err := DecodePlanDeliverySpec(original)
		if count == 4 {
			if err == nil {
				t.Fatal("four directions accepted")
			}
			continue
		}
		if err != nil || len(normalized.Directions) != count {
			t.Fatalf("count %d: %+v %v", count, normalized, err)
		}
		canonical, _ := json.Marshal(normalized)
		if string(canonical) != string(original) {
			t.Fatalf("count %d changed canonical content: %s -> %s", count, original, canonical)
		}
		withMode := strings.TrimSuffix(string(original), "}") + `,"manual_acceptance":"on_demand"}`
		if _, err := DecodePlanDeliverySpec([]byte(withMode)); err == nil {
			t.Fatal("model proposal set the operator's manual acceptance policy")
		}
	}
}

func TestPlanDeliveryManualAcceptancePreservesRequiredFingerprint(t *testing.T) {
	original := PlanDeliverySelectionRequestFingerprint("proposal", "run", 1, "operator")
	for _, legacy := range []PlanDeliveryManualAcceptance{"", PlanDeliveryManualAcceptanceRequired} {
		if got := PlanDeliverySelectionRequestFingerprintForAcceptance("proposal", "run", 1, "operator", legacy); got != original {
			t.Fatalf("legacy fingerprint changed: %q", got)
		}
	}
	optional := PlanDeliverySelectionRequestFingerprintForAcceptance("proposal", "run", 1, "operator", PlanDeliveryManualAcceptanceOnDemand)
	if len(optional) != 64 || optional == original {
		t.Fatal("on-demand policy is not bound to its own intent")
	}
	for _, bad := range []PlanDeliveryManualAcceptance{"never", " required", "REQUIRED"} {
		if _, err := NormalizePlanDeliveryManualAcceptance(bad); err == nil {
			t.Fatalf("invalid policy accepted: %q", bad)
		}
	}
}

func TestPlanDeliverySpecCanonicalizesMissingDependenciesToEmptyArray(t *testing.T) {
	const prefix = `{"version":"plan_delivery.v1","directions":[{"title":"Fix","summary":"Fix one source file","tradeoffs":["Narrow change"],"modules":[{"title":"Implement","objective":"Repair the original regression","acceptance_criteria":["Original tests pass"]`
	for _, field := range []string{"", `,"dependencies":null`, `,"dependencies":[]`} {
		spec, err := DecodePlanDeliverySpec([]byte(prefix + field + `}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(spec)
		if err != nil || !strings.Contains(string(encoded), `"dependencies":[]`) {
			t.Fatalf("empty dependencies must persist as a JSON array: field=%q encoded=%s err=%v", field, encoded, err)
		}
	}
}

func TestPlanDeliverySpecRejectsShapeAndDependencyDrift(t *testing.T) {
	invalid := [][]byte{
		[]byte(`{"version":"plan_delivery.v1","directions":[]}`),
		[]byte(`{"version":"plan_delivery.v2","directions":[]}`),
		[]byte(`{"version":"plan_delivery.v1","directions":[],"authority":true}`),
		[]byte(`{"version":"plan_delivery.v1","directions":[]} {}`),
		[]byte(`{"version":"plan_delivery.v1","directions":[` +
			`{"title":"A","summary":"A","tradeoffs":["A"],"modules":[{"title":"A","objective":"A","acceptance_criteria":["A"],"dependencies":[1]}]},` +
			`{"title":"B","summary":"B","tradeoffs":["B"],"modules":[{"title":"B","objective":"B","acceptance_criteria":["B"],"dependencies":[]}]},` +
			`{"title":"C","summary":"C","tradeoffs":["C"],"modules":[{"title":"C","objective":"C","acceptance_criteria":["C"],"dependencies":[]}]}]}`),
		[]byte(`{"version":" plan_delivery.v1 ","directions":[` +
			`{"title":"A","summary":"A","tradeoffs":["A"],"modules":[{"title":"A","objective":"A","acceptance_criteria":["A"],"dependencies":[]}]},` +
			`{"title":"B","summary":"B","tradeoffs":["B"],"modules":[{"title":"B","objective":"B","acceptance_criteria":["B"],"dependencies":[]}]},` +
			`{"title":"C","summary":"C","tradeoffs":["C"],"modules":[{"title":"C","objective":"C","acceptance_criteria":["C"],"dependencies":[]}]}]}`),
		[]byte(`{"version":"plan_delivery.v1","directions":[` +
			`{"title":"A","summary":"A","tradeoffs":["A"],"modules":[{"title":"A1","objective":"A","acceptance_criteria":["A"],"dependencies":[]},{"title":"A2","objective":"A","acceptance_criteria":["A"],"dependencies":[1,1]}]},` +
			`{"title":"B","summary":"B","tradeoffs":["B"],"modules":[{"title":"B","objective":"B","acceptance_criteria":["B"],"dependencies":[]}]},` +
			`{"title":"C","summary":"C","tradeoffs":["C"],"modules":[{"title":"C","objective":"C","acceptance_criteria":["C"],"dependencies":[]}]}]}`),
	}
	for _, payload := range invalid {
		if _, err := DecodePlanDeliverySpec(payload); err == nil {
			t.Fatalf("invalid Plan/Delivery payload was accepted: %s", payload)
		}
	}
}

func TestPlanDeliveryProposalAndSelectionFingerprintsAreBounded(t *testing.T) {
	spec, err := DecodePlanDeliverySpec([]byte(`{"version":"plan_delivery.v1","directions":[` +
		`{"title":"A","summary":"A","tradeoffs":["A"],"modules":[{"title":"A","objective":"A","acceptance_criteria":["A"],"dependencies":[]}]},` +
		`{"title":"B","summary":"B","tradeoffs":["B"],"modules":[{"title":"B","objective":"B","acceptance_criteria":["B"],"dependencies":[]}]},` +
		`{"title":"C","summary":"C","tradeoffs":["C"],"modules":[{"title":"C","objective":"C","acceptance_criteria":["C"],"dependencies":[]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	proposal := PlanDeliveryProposal{
		ID: "plan-1", RunID: "run-1", RootAgentID: "agent-root", SessionID: "session-1",
		WorkspaceID: "workspace-1", ModeRevision: 1,
		Status: PlanDeliveryProposalProposed, Spec: spec,
		RequestedBy: "run_supervisor", Version: 1, CreatedAt: now,
	}
	proposal.Fingerprint = PlanDeliveryProposalFingerprint(proposal)
	if err := proposal.Validate(); err != nil {
		t.Fatal(err)
	}
	selection := PlanDeliverySelection{
		ID: "selection-1", ProposalID: proposal.ID, RunID: proposal.RunID,
		RootAgentID: proposal.RootAgentID, DirectionOrdinal: 1, NoteID: "note-1",
		Items:       []PlanDeliverySelectionItem{{Ordinal: 1, ModuleOrdinal: 1, WorkItemID: "work-1"}},
		RequestedBy: "operator", Version: 1, CreatedAt: now,
	}
	if err := selection.Validate(); err != nil {
		t.Fatal(err)
	}
	first := PlanDeliverySelectionRequestFingerprint(proposal.ID, proposal.RunID, 1, "operator")
	second := PlanDeliverySelectionRequestFingerprint(proposal.ID, proposal.RunID, 2, "operator")
	if first == second || len(first) != 64 {
		t.Fatalf("selection fingerprint is not bound to the chosen direction: %q %q", first, second)
	}
}

func TestPlanDeliverySpecGuaranteesDurableHandoffNoteBounds(t *testing.T) {
	directions := make([]PlanDeliveryDirection, PlanDeliveryDirectionCount)
	for index := range directions {
		directions[index] = PlanDeliveryDirection{
			Title:   strings.Repeat(string(rune('A'+index)), MaxPlanDeliveryTitleRunes),
			Summary: "bounded summary", Tradeoffs: []string{"bounded tradeoff"},
			Modules: []PlanDeliveryModule{{
				Title: "Module", Objective: "Bounded objective",
				AcceptanceCriteria: []string{"Handoff remains durable"},
			}},
		}
	}
	spec, err := NormalizePlanDeliverySpec(PlanDeliverySpec{
		Version: PlanDeliveryProtocolVersion, Directions: directions,
	})
	if err != nil {
		t.Fatal(err)
	}
	direction := spec.Directions[0]
	title := PlanDeliveryHandoffTitle(direction)
	content := PlanDeliveryHandoffContent(PlanDeliveryProposal{ID: "proposal-1"},
		direction)
	if utf8.RuneCountInString(title) > MaxNoteTitleRunes ||
		len([]byte(content)) > MaxNoteContentBytes {
		t.Fatalf("handoff Note exceeds durable bounds: title=%d content=%d",
			utf8.RuneCountInString(title), len([]byte(content)))
	}
	if _, err := NormalizeNoteDetails(NoteDetails{
		Title: title, Content: content, Category: NoteDecision,
		Visibility: NoteVisibilityRun, OwnerAgentID: "agent-root",
	}); err != nil {
		t.Fatalf("maximum direction title produced an invalid handoff Note: %v", err)
	}
}

func TestPlanDeliverySpecRejectsDirectionThatCannotFitHandoffNote(t *testing.T) {
	largeModules := make([]PlanDeliveryModule, 7)
	for index := range largeModules {
		largeModules[index] = PlanDeliveryModule{
			Title:              fmt.Sprintf("Large module %d", index+1),
			Objective:          strings.Repeat("\U00010000", MaxPlanDeliveryObjectiveRunes),
			AcceptanceCriteria: []string{"bounded criterion"},
		}
	}
	_, err := NormalizePlanDeliverySpec(PlanDeliverySpec{
		Version: PlanDeliveryProtocolVersion,
		Directions: []PlanDeliveryDirection{
			{Title: "Large", Summary: "Large", Tradeoffs: []string{"Large"}, Modules: largeModules},
			{Title: "Small B", Summary: "B", Tradeoffs: []string{"B"}, Modules: []PlanDeliveryModule{{Title: "B", Objective: "B", AcceptanceCriteria: []string{"B"}}}},
			{Title: "Small C", Summary: "C", Tradeoffs: []string{"C"}, Modules: []PlanDeliveryModule{{Title: "C", Objective: "C", AcceptanceCriteria: []string{"C"}}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "handoff Note") {
		t.Fatalf("oversized handoff direction was not rejected: %v", err)
	}
}

func redecodeFingerprint(spec PlanDeliverySpec) string {
	proposal := PlanDeliveryProposal{
		RunID: "run", RootAgentID: "root", SessionID: "session", WorkspaceID: "workspace",
		ModeRevision: 1, Spec: spec, RequestedBy: "supervisor",
	}
	return PlanDeliveryProposalFingerprint(proposal)
}
