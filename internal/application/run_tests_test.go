package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func projectTestRequest() json.RawMessage {
	return json.RawMessage(`{"version":"run-tests.v1","target":".","timeout_milliseconds":25000}`)
}

func TestRunTestsCannotBePreparedByChild(t *testing.T) {
	s := &CommandRuntimeService{}
	_, err := s.PrepareProjectTest(t.Context(), toolgateway.ToolCall{
		Name: toolgateway.RunTestsTool, RequestedBy: "run_supervisor", Role: domain.AgentRoleSpecialist,
	}, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: 1000})
	if err == nil {
		t.Fatal("child acquired project execution authority")
	}
}

func seedProjectTest(t *testing.T, f *commandApprovalFixture) {
	t.Helper()
	for name, content := range map[string]string{
		"go.mod":          "module testfixture\n\ngo 1.23\n",
		"fixture_test.go": "package testfixture\nimport(\"testing\";\"os\")\nfunc TestFixture(t *testing.T) { if err:=os.WriteFile(\"test-effect.txt\", []byte(\"tested\"),0600); err!=nil { t.Fatal(err) } }\n",
	} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunTestsRealNativeApprovalAndPhases(t *testing.T) {
	for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
		for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
			t.Run(string(phase)+"/"+string(mode), func(t *testing.T) {
				f := newCommandApprovalFixture(t, mode, false, phase)
				seedProjectTest(t, f)
				f.recordTool(t, toolgateway.RunTestsTool, projectTestRequest(), 1)
				waiting, err := f.resume(t)
				if err != nil {
					t.Fatal(err)
				}
				if mode != domain.RunExecutionPermissionFull {
					if !waiting {
						t.Fatal("native test must wait for existing Ask/Auto approval")
					}
					f.assertNoMarker(t, "test-effect.txt")
					f.decide(t, ApprovalControlApproveOnce)
					waiting, err = f.resume(t)
				}
				if err != nil || waiting {
					t.Fatalf("resume: waiting=%v err=%v", waiting, err)
				}
				jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
				if err != nil || len(jobs) != 1 {
					rounds, _ := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
					t.Fatalf("jobs=%+v err=%v rounds=%+v", jobs, err, rounds)
				}
				if jobs[0].State != runner.CommandRuntimeJobCompleted || jobs[0].ExitCode == nil || *jobs[0].ExitCode != 0 || !jobs[0].TreeReaped {
					t.Fatalf("test did not complete: %+v", jobs[0])
				}
				if content, err := os.ReadFile(filepath.Join(f.root, "test-effect.txt")); err != nil || string(content) != "tested" {
					t.Fatalf("project test effect=%s err=%v", content, err)
				}
				if _, err = f.resume(t); err != nil {
					t.Fatal(err)
				}
				jobs, _ = f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
				if len(jobs) != 1 {
					t.Fatal("replay started another Job")
				}
			})
		}
	}
}

func TestRunTestsDeniedAndPreparedCommandChange(t *testing.T) {
	for _, change := range []string{"denied", "manifest"} {
		t.Run(change, func(t *testing.T) {
			f := newCommandApprovalFixture(t, domain.RunExecutionPermissionAsk, false, domain.ExecutionPhasePlan)
			seedProjectTest(t, f)
			f.recordTool(t, toolgateway.RunTestsTool, projectTestRequest(), 1)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatalf("waiting=%v err=%v", waiting, err)
			}
			if change == "denied" {
				f.decide(t, ApprovalControlDeny)
			} else {
				f.decide(t, ApprovalControlApproveOnce)
				if err := os.Remove(filepath.Join(f.root, "go.mod")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.resume(t); err != nil {
				t.Fatal(err)
			}
			f.assertNoMarker(t, "test-effect.txt")
			jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if len(jobs) != 0 {
				t.Fatalf("unexpected Jobs: %+v", jobs)
			}
		})
	}
}

func TestRunTestsDoesNotAdvertiseGeneralCommandInPlan(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, false, domain.ExecutionPhasePlan)
	advertised, err := f.supervisor.supervisorCommandRuntimeTools(t.Context(), f.turn.Run.ID, domain.RunExecutionPermissionFull)
	if err != nil {
		t.Fatal(err)
	}
	tools := supervisorStructuredToolSpecs(domain.ExecutionSurfaceCode, domain.ExecutionPhasePlan, domain.RunExecutionPermissionFull, false, false, supervisorToolOptions{CommandRuntime: advertised})
	found := false
	for _, tool := range tools {
		if tool.Name == string(toolgateway.CommandRuntimeTool) {
			t.Fatal("general command runtime escaped Plan gate")
		}
		found = found || tool.Name == string(toolgateway.RunTestsTool)
	}
	if !found {
		t.Fatal("run_tests missing")
	}
}

func TestRunTestsRequiresCurrentLeaseAtDispatch(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, false, domain.ExecutionPhasePlan)
	seedProjectTest(t, f)
	f.recordTool(t, toolgateway.RunTestsTool, projectTestRequest(), 1)
	scope, input := f.scope(t)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("lease: %v", err)
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ExecuteCommandRuntime(t.Context(), scope, input); err == nil {
		t.Fatal("expired lease dispatched test")
	}
	f.assertNoMarker(t, "test-effect.txt")
	jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if len(jobs) != 0 {
		t.Fatal("expired lease created Job")
	}
}

func TestRunTestsStandardCodePreservesPhaseAndCommandBudget(t *testing.T) {
	m, _ := newStandardCodeSupervisorTestMachine(domain.StandardCodeSupervisorPlan, domain.ExecutionPhasePlan)
	d := standardCodeCallDescriptor{ProjectTest: true, Kind: domain.StandardCodeToolCommandRun, CommandCount: 1, Action: "run"}
	if allowed, reason, _ := m.authorizeDescriptor(d); !allowed {
		t.Fatalf("Plan test denied: %s", reason)
	}
	ordinary := d
	ordinary.ProjectTest = false
	if allowed, _, _ := m.authorizeDescriptor(ordinary); allowed {
		t.Fatal("general command allowed during Plan")
	}
	m.snapshot.CommandsUsed = m.snapshot.Limits.MaximumCommands
	if allowed, _, stop := m.authorizeDescriptor(d); allowed || stop != "command_budget_exhausted" {
		t.Fatalf("command budget escaped: %v %s", allowed, stop)
	}
	call := domain.SupervisorToolCall{Status: domain.SupervisorToolFailed}
	if _, _, err := m.observeCommand(t.Context(), call, d, supervisorToolResultEnvelope{}); err != nil {
		t.Fatal(err)
	}
	if m.snapshot.State != domain.StandardCodeSupervisorPlan || m.snapshot.VerifiedMutationEpoch != 0 {
		t.Fatalf("test changed Plan state: %+v", m.snapshot)
	}
}

func TestRunTestsPlanUsesSelectedSandboxWithoutHostFallback(t *testing.T) {
	f := newCommandApprovalSandboxPhaseFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, domain.ExecutionPhasePlan, func(base drydockApplicationFixture) {
		writeDrydockTestFile(t, filepath.Join(base.sourceRoot, "go.mod"), "module fixture\ngo 1.23\n")
		runDrydockTestGit(t, base.sourceRoot, "add", "go.mod")
		runDrydockTestGit(t, base.sourceRoot, "commit", "-m", "test manifest")
	})
	f.recordTool(t, toolgateway.RunTestsTool, projectTestRequest(), 1)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("waiting=%v err=%v", waiting, err)
	}
	f.assertDispatchCounts(t, 1, 1)
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobCompleted {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}
