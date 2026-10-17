package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func validWorkflowForTest() WorkflowSpec {
	return WorkflowSpec{Version: WorkflowVersion, Name: "bounded", Limits: WorkflowLimits{MaxSteps: 12, MaxJobs: 4, MaxTokens: 1000, MaxTurns: 8, TimeoutMillis: 60000}, Steps: []WorkflowStep{{ID: "inspect", Kind: "worker", Task: &ChildTask{Title: "inspect", Goal: "inspect files", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 100, TimeoutMillis: 10000}}}}
}
func TestWorkflowRejectsUnboundedOrAmbiguousPrograms(t *testing.T) {
	valid := validWorkflowForTest()
	raw, _ := json.Marshal(valid)
	if _, err := DecodeWorkflowSpec(raw); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*WorkflowSpec){func(s *WorkflowSpec) { s.Limits.MaxJobs = 0 }, func(s *WorkflowSpec) { s.Limits.MaxTokens = 0 }, func(s *WorkflowSpec) { s.Steps = append(s.Steps, s.Steps[0]) }, func(s *WorkflowSpec) { s.Steps[0].Kind = "javascript" }, func(s *WorkflowSpec) { s.Steps[0].Task.DependencyOrdinals = []int{1} }, func(s *WorkflowSpec) {
		s.Steps[0].Inputs = map[string]WorkflowValueRef{"x": {Step: "inspect", Path: "field"}}
	}} {
		spec := validWorkflowForTest()
		mutate(&spec)
		if _, err := NormalizeWorkflowSpec(spec); err == nil {
			t.Fatalf("accepted invalid spec: %#v", spec)
		}
	}
	if _, err := DecodeWorkflowSpec([]byte(strings.Replace(string(raw), `"version":`, `"execute_js":"danger","version":`, 1))); err == nil {
		t.Fatal("accepted unknown field")
	}
	if _, err := DecodeWorkflowSpec(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}
