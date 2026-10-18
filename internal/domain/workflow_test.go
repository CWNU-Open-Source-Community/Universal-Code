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

func TestWorkflowIsolatedDeliveryRequiresAnExplicitPlanReference(t *testing.T) {
	valid := validWorkflowForTest()
	valid.Steps = []WorkflowStep{{ID: "deliver", Kind: "batch", BatchPlan: &WorkflowValueRef{Step: "$input", Path: "/plan_id"}}}
	if _, err := NormalizeWorkflowSpec(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*WorkflowStep){
		func(s *WorkflowStep) { s.BatchPlan = nil },
		func(s *WorkflowStep) { s.BatchPlan = &WorkflowValueRef{Step: "$input", Path: "plan_id"} },
		func(s *WorkflowStep) { s.Task = validWorkflowForTest().Steps[0].Task },
		func(s *WorkflowStep) { s.Inputs = map[string]WorkflowValueRef{"extra": {Step: "$input"}} },
		func(s *WorkflowStep) { s.Kind = "worker"; s.Task = validWorkflowForTest().Steps[0].Task },
	} {
		spec := valid
		spec.Steps = append([]WorkflowStep(nil), valid.Steps...)
		change(&spec.Steps[0])
		if _, err := NormalizeWorkflowSpec(spec); err == nil {
			t.Fatalf("accepted mixed or missing isolation contract: %+v", spec.Steps[0])
		}
	}
}

func TestWorkflowCodeCompletionRequiresDelegatedTests(t *testing.T) {
	for _, profile := range []*WorkerProfile{nil, {Tools: []string{}}, {Tools: []string{"workspace_read"}}} {
		spec := validWorkflowForTest()
		spec.Steps[0].Task.Worker = profile
		spec.Steps[0].Completion = &WorkflowCompletion{Mode: "verified_code"}
		if _, err := NormalizeWorkflowSpec(spec); err == nil {
			t.Fatalf("accepted code completion without tests: %+v", profile)
		}
	}
	for _, profile := range []*WorkerProfile{{}, {Tools: []string{"run_tests"}}} {
		spec := validWorkflowForTest()
		spec.Steps[0].Task.Worker = profile
		spec.Steps[0].Completion = &WorkflowCompletion{Mode: "verified_code"}
		got, err := NormalizeWorkflowSpec(spec)
		if err != nil || !got.Steps[0].RequiresCodeVerification() || got.Steps[0].VerificationTarget() != "." {
			t.Fatalf("valid code completion: %+v %v", got, err)
		}
	}
	for _, completion := range []WorkflowCompletion{{Mode: "succeeded"}, {Mode: "report", Target: "."}, {Mode: "verified_code", Target: "../other"}, {Mode: "verified_code", Target: " /tmp"}} {
		spec := validWorkflowForTest()
		spec.Steps[0].Task.Worker = &WorkerProfile{}
		spec.Steps[0].Completion = &completion
		if _, err := NormalizeWorkflowSpec(spec); err == nil {
			t.Fatalf("accepted invalid completion: %+v", completion)
		}
	}
	spec := validWorkflowForTest()
	spec.Steps[0].Completion = &WorkflowCompletion{Mode: "report"}
	if _, err := NormalizeWorkflowSpec(spec); err != nil {
		t.Fatalf("analysis report requires no tests: %v", err)
	}
}

func TestWorkflowMapBoundsAndLegacyDefaults(t *testing.T) {
	legacy, _ := NormalizeWorkflowSpec(validWorkflowForTest())
	before, _ := json.Marshal(legacy)
	normalized, err := NormalizeWorkflowSpec(legacy)
	after, _ := json.Marshal(normalized)
	if err != nil || string(before) != string(after) || normalized.Steps[0].AttemptLimit() != 3 {
		t.Fatalf("legacy normalization changed: %s %s %v", before, after, err)
	}
	for _, change := range []func(*WorkflowStep){
		func(s *WorkflowStep) { s.MaxItems = 0 }, func(s *WorkflowStep) { s.MaxItems = 65 }, func(s *WorkflowStep) { s.Items = nil }, func(s *WorkflowStep) { s.Parallelism = 3 }, func(s *WorkflowStep) { s.MaxAttempts = 4 }, func(s *WorkflowStep) { s.Inputs = map[string]WorkflowValueRef{"item": {Step: "$input"}} },
	} {
		spec := validWorkflowForTest()
		spec.Steps[0].Kind = "map"
		spec.Steps[0].Items = &WorkflowValueRef{Step: "$input", Path: "/items"}
		spec.Steps[0].MaxItems = 8
		change(&spec.Steps[0])
		if _, err := NormalizeWorkflowSpec(spec); err == nil {
			t.Fatalf("accepted invalid map: %+v", spec.Steps[0])
		}
	}
	spec := validWorkflowForTest()
	spec.Steps[0].Kind = "map"
	spec.Steps[0].Items = &WorkflowValueRef{Step: "$input", Path: "/items"}
	spec.Steps[0].MaxItems = 8
	spec.Steps[0].Parallelism = 2
	if _, err := NormalizeWorkflowSpec(spec); err != nil {
		t.Fatal(err)
	}
	if WorkflowItemStepID("occurrence", "worker", 0, 1) != "workflowstep-occurrence" || WorkflowItemStepID("occurrence", "map", 0, 1) == WorkflowItemStepID("occurrence", "map", 0, 2) {
		t.Fatal("workflow generation identity changed or collided")
	}
}
