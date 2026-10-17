package application

import (
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestWorkflowReferencesAndConditionsPreserveExactNumbers(t *testing.T) {
	e := domain.WorkflowExecution{Inputs: json.RawMessage(`{"id":9007199254740993,"values":[1.0,0.10,-0,1e999999999]}`)}
	raw, err := workflowValue(e, domain.WorkflowValueRef{Step: "$input", Path: "/id"})
	if err != nil || string(raw) != "9007199254740993" {
		t.Fatalf("rounded input reference: %s %v", raw, err)
	}
	for _, tt := range []struct {
		path, expected string
		equal          bool
	}{
		{"/id", "9007199254740992", false},
		{"/id", "9007199254740993", true},
		{"/values", "[1,1e-1,0,10e999999998]", true},
	} {
		got, err := workflowCondition(e, domain.WorkflowCondition{Ref: domain.WorkflowValueRef{Step: "$input", Path: tt.path}, Equals: json.RawMessage(tt.expected)})
		if err != nil || got != tt.equal {
			t.Fatalf("%s equals %s: %v %v", tt.path, tt.expected, got, err)
		}
	}
}
