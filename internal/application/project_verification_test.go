package application

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func newProjectVerificationFixture(t *testing.T, phase domain.ExecutionPhase) *commandApprovalFixture {
	t.Helper()
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionFull, false, phase)
	pin := pinActionConfig(projectconfig.Effective{Protocol: projectconfig.ProtocolVersion, TestCommandID: "test.node"})
	f.turn.Run.Config.ProjectConfig, f.turn.Run.Config.ProjectConfigFingerprint = pin.ProjectConfig, pin.ProjectConfigFingerprint
	raw, _ := json.Marshal(f.turn.Run.Config)
	db, err := sql.Open("sqlite3", f.path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE runs SET config_json=? WHERE id=?`, string(raw), f.turn.Run.ID)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	writeVerificationTest(t, f, `const test=require('node:test');test('source',()=>{});`)
	return f
}

func writeVerificationTest(t *testing.T, f *commandApprovalFixture, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "source.test.js"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func verificationAuthority(t *testing.T, f *commandApprovalFixture) commandruntimeadapter.Authority {
	t.Helper()
	permission, err := f.st.GetRunExecutionPermission(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := f.supervisor.supervisorCommandRuntimeTools(t.Context(), f.turn.Run.ID, permission.Mode)
	if err != nil {
		t.Fatal(err)
	}
	a, err := commandruntimeadapter.DecodeAuthority(runtime.Authority)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func runVerificationCall(t *testing.T, f *commandApprovalFixture, input toolgateway.RunTestsInput, round int) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	f.recordTool(t, toolgateway.RunTestsTool, raw, round)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("test call waiting=%v err=%v", waiting, err)
	}
}

func waitVerificationCall(t *testing.T, f *commandApprovalFixture, receipt toolgateway.ProjectVerificationReceipt, round int) {
	t.Helper()
	cursor, maxBytes, wait := receipt.Cursor, toolgateway.MaxCommandRuntimePageBytes, int(runner.MaxCommandRuntimeWait.Milliseconds())
	runVerificationCall(t, f, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "wait", JobID: receipt.JobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &wait}, round)
}

func TestProjectVerificationBackgroundReusesAndExpiresSource(t *testing.T) {
	for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
		t.Run(string(phase), func(t *testing.T) {
			f := newProjectVerificationFixture(t, phase)
			runVerificationCall(t, f, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "start", Target: ".", TimeoutMilliseconds: 60000}, 1)
			a := verificationAuthority(t, f)
			receipt, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
			if err != nil || !found {
				t.Fatalf("start receipt=%+v found=%v err=%v", receipt, found, err)
			}
			jobID := receipt.JobID
			waitVerificationCall(t, f, receipt, 2)
			receipt, found, err = f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
			if err != nil || !found || !receipt.Reusable || receipt.State != "passed" || receipt.SourceVersion != receipt.CompletedSourceVersion {
				t.Fatalf("terminal receipt=%+v found=%v err=%v", receipt, found, err)
			}
			if _, err = f.resume(t); err != nil {
				t.Fatal(err)
			}
			jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if len(jobs) != 1 || jobs[0].ID != jobID {
				t.Fatalf("observation relaunched: %+v", jobs)
			}
			// Reconstruct only the service: receipt identity survives process-local maps.
			recovered, err := NewCommandRuntimeService(f.st, f.manager, f.caps)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := recovered.ReadProjectVerificationReceipt(t.Context(), a, jobID); err != nil || !got.Reusable {
				t.Fatalf("receipt was not durable: %+v %v", got, err)
			}
			writeVerificationTest(t, f, `const test=require('node:test');test('changed',()=>{});`)
			if got, err := recovered.ReadProjectVerificationReceipt(t.Context(), a, jobID); err != nil || got.Reusable || got.State != "stale" {
				t.Fatalf("changed source reused: %+v %v", got, err)
			}
		})
	}
}

func TestProjectVerificationFailsWhenTestsChangeSource(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	writeVerificationTest(t, f, `const test=require('node:test');test('writes',()=>{require('fs').writeFileSync('new-source.js','changed');});`)
	runVerificationCall(t, f, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: 10000}, 1)
	a := verificationAuthority(t, f)
	jobs, _ := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if len(jobs) != 1 || jobs[0].ExitCode == nil || *jobs[0].ExitCode != 0 {
		t.Fatalf("native test did not pass: %+v", jobs)
	}
	r, err := f.service.ReadProjectVerificationReceipt(t.Context(), a, jobs[0].ID)
	if err != nil || r.Reusable || r.State != "stale" {
		t.Fatalf("source-writing test certified: %+v %v", r, err)
	}
	completedSource := r.CompletedSourceVersion
	if err := os.Remove(filepath.Join(f.root, "new-source.js")); err != nil {
		t.Fatal(err)
	}
	// Restoring the pre-test bytes cannot rewrite this Job's already observed
	// terminal source version. A later wait must retain its failed proof.
	waitVerificationCall(t, f, r, 2)
	r, err = f.service.ReadProjectVerificationReceipt(t.Context(), a, jobs[0].ID)
	if err != nil || r.Reusable || r.State != "stale" || r.CompletedSourceVersion != completedSource {
		t.Fatalf("later wait replaced terminal source evidence: %+v %v", r, err)
	}
}

func TestProjectVerificationLaterFailureSupersedesPass(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	// The gate is an execution condition outside the source tree. Source bytes
	// stay identical while the second actual test fails.
	gate := filepath.Join(t.TempDir(), "fail")
	encoded, _ := json.Marshal(gate)
	writeVerificationTest(t, f, `const test=require('node:test');test('external condition',()=>{if(require('fs').existsSync(`+string(encoded)+`))throw Error('failed');});`)
	i := toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: 10000}
	runVerificationCall(t, f, i, 1)
	a := verificationAuthority(t, f)
	first, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
	if err != nil || !found || !first.Reusable {
		t.Fatalf("first pass=%+v %v", first, err)
	}
	if err := os.WriteFile(gate, []byte("fail"), 0600); err != nil {
		t.Fatal(err)
	}
	runVerificationCall(t, f, i, 2)
	last, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
	if err != nil || !found || last.Reusable || last.State != "failed" || last.JobID == first.JobID {
		t.Fatalf("old success replaced failure: %+v %v", last, err)
	}
	if older, err := f.service.ReadProjectVerificationReceipt(t.Context(), a, first.JobID); err != nil || older.Reusable || older.State != "superseded" {
		t.Fatalf("explicit older receipt washed failure: %+v %v", older, err)
	}
	a.Adapter.BackendIdentity = "another-backend"
	if _, err := f.service.ReadProjectVerificationReceipt(t.Context(), a, first.JobID); err == nil {
		t.Fatal("receipt crossed backend identity")
	}
}

func TestRunTestsBackgroundStandardCodeKeepsPlanEpochAndJobsBudget(t *testing.T) {
	m, _ := newStandardCodeSupervisorTestMachine(domain.StandardCodeSupervisorPlan, domain.ExecutionPhasePlan)
	d := standardCodeCallDescriptor{ProjectTest: true, Kind: domain.StandardCodeToolCommandStart, CommandCount: 1, Action: "start"}
	if allowed, reason, _ := m.authorizeDescriptor(d); !allowed {
		t.Fatalf("Plan background test denied: %s", reason)
	}
	m.snapshot.JobsStarted = m.snapshot.Limits.MaximumJobs
	if allowed, _, stop := m.authorizeDescriptor(d); allowed || stop != "background_job_budget_exhausted" {
		t.Fatalf("test escaped jobs budget: %v %s", allowed, stop)
	}
	job := domain.StandardCodeSupervisorJob{ProjectTest: true, JobID: "plan-test-job", PermissionRevision: 1, MutationEpoch: 0, State: "running"}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.ProjectTest = false
	if err := job.Validate(); err == nil {
		t.Fatal("ordinary Job escaped mutation ownership")
	}
}

func TestProjectVerificationSameSourceReceiptReusesCurrentMutationEpoch(t *testing.T) {
	m, _ := newStandardCodeSupervisorTestMachine(domain.StandardCodeSupervisorExecute, domain.ExecutionPhaseDeliver)
	m.snapshot.MutationEpoch, m.snapshot.VerifiedMutationEpoch = 2, 1
	zero := 0
	r := toolgateway.ProjectVerificationReceipt{Version: toolgateway.ProjectVerificationReceiptVersion, RunID: m.snapshot.RunID, AgentID: m.snapshot.RootAgentID, WorkspaceID: m.snapshot.WorkspaceID,
		JobID: "previous-test-job", SourceVersion: strings.Repeat("a", 64), CompletedSourceVersion: strings.Repeat("a", 64), State: "passed", Reusable: true, ExitCode: &zero, TreeReaped: true}
	if err := m.reuseProjectVerificationReceipt(t.Context(), r); err != nil || !m.snapshot.CanDeliver() || m.snapshot.VerifiedMutationEpoch != 2 || m.snapshot.CommandsUsed != 0 {
		t.Fatalf("same source receipt was not reused: %+v %v", m.snapshot, err)
	}
	r.Reusable = false
	if err := m.reuseProjectVerificationReceipt(t.Context(), r); err == nil {
		t.Fatal("nonpassing receipt credited verification")
	}
}

func TestProjectVerificationWorkerCannotReadSiblingReceipt(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runVerificationCall(t, f, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: 10000}, 1)
	a := verificationAuthority(t, f)
	rootReceipt, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
	if err != nil || !found || !rootReceipt.Reusable {
		t.Fatalf("root receipt=%+v %v", rootReceipt, err)
	}
	idleVerificationRoot(t, f, 2, 1)
	worker := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
	a.Worker = &commandruntimeadapter.WorkerAuthority{AgentID: worker.child.ID, AttemptID: worker.ref.AttemptID, SessionID: worker.child.SessionID}
	if _, err := f.service.ReadProjectVerificationReceipt(t.Context(), a, rootReceipt.JobID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("worker read root receipt: %v", err)
	}
	workerAuthority := workerVerificationCall(t, f, worker, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Target: ".", TimeoutMilliseconds: 10000})
	workerReceipt, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), workerAuthority, ".")
	if err != nil || !found || !workerReceipt.Reusable {
		t.Fatalf("worker receipt=%+v %v", workerReceipt, err)
	}
	sibling := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
	a.Worker = &commandruntimeadapter.WorkerAuthority{AgentID: sibling.child.ID, AttemptID: sibling.ref.AttemptID, SessionID: sibling.child.SessionID}
	if _, err := f.service.ReadProjectVerificationReceipt(t.Context(), a, workerReceipt.JobID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("worker read sibling receipt: %v", err)
	}
}

func idleVerificationRoot(t *testing.T, f *commandApprovalFixture, number, round int) {
	t.Helper()
	model := projectHookModelBoundary(t, f, number, round)
	model.Attempt.Outcome = llm.OutcomeSuccess
	model.Response.Text = `{"version":"root_lifecycle.v1","action":"continue","message":"Delegate the accepted test to the worker."}`
	cp, err := f.st.RecordSupervisorModelCompleted(t.Context(), f.turn.Checkpoint, model.Attempt, *model.Response)
	if err != nil {
		t.Fatal(err)
	}
	_, cp, _, err = f.st.CompleteSupervisorTurn(t.Context(), cp, *model.Response, domain.RootAction{Version: "root_lifecycle.v1", Kind: domain.RootActionContinue, Message: "Delegate the accepted test to the worker."}, policy.Decision{Allowed: true, Risk: "low", Reason: "fixture continuation"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.turn.Checkpoint = cp
}

func workerVerificationCall(t *testing.T, f *commandApprovalFixture, worker *workerCommandTestActor, input toolgateway.RunTestsInput) commandruntimeadapter.Authority {
	t.Helper()
	raw, _ := json.Marshal(input)
	_, raw, err := toolgateway.NormalizeRunTestsPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	base, err := commandruntimeadapter.EncodeAuthority(verificationAuthority(t, f))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := f.service.BindSpecialistCommandToolAuthority(t.Context(), worker.ref, toolgateway.RunTestsTool, base, raw)
	if err != nil {
		t.Fatal(err)
	}
	model := llm.ModelAttempt{Number: worker.round + 1, ToolRound: worker.round, TransportAttempt: 1, MaxAttempts: 1, Provider: "worker-verification", Model: "fixture"}
	if _, err := f.st.RecordSpecialistModelStarted(t.Context(), worker.ref, model); err != nil {
		t.Fatal(err)
	}
	model.Outcome = llm.OutcomeSuccess
	model.Elapsed = time.Millisecond
	callID := idgen.New("worker-verification-call")
	response := llm.ChatResponse{Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.RunTestsTool), Arguments: raw}}}
	if _, err := f.st.RecordSpecialistToolModelCompleted(t.Context(), worker.ref, model, response, "execute the authenticated worker brief", policy.Decision{Allowed: true, Risk: "low", Reason: "worker test fixture"}); err != nil {
		t.Fatal(err)
	}
	worker.round++
	call, err := f.st.BindSpecialistToolAuthority(t.Context(), worker.ref, callID, string(bound))
	if err != nil {
		t.Fatal(err)
	}
	scope := worker.charge(t, call, true)
	scope.ProjectTest = true
	a, err := commandruntimeadapter.DecodeAuthority(bound)
	if err != nil {
		t.Fatal(err)
	}
	native, _, err := toolgateway.CommandToolInput(toolgateway.RunTestsTool, raw, a)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ExecuteCommandRuntime(t.Context(), scope, native)
	if err != nil {
		t.Fatal(err)
	}
	projection := map[string]any{"version": runner.CommandRuntimeResultVersion, "action": result.Action, "jobs": result.Jobs, "pages": result.Pages, "verification_receipt": result.VerificationReceipt}
	encoded, _ := json.Marshal(map[string]any{"status": "completed", "result": projection})
	call.Status, call.ResultJSON = domain.SupervisorToolCompleted, string(encoded)
	if err := f.st.CompleteSpecialistToolCall(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestProjectVerificationWorkerPlanStartWaitRetainsActor(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhasePlan)
	lease, _, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	idleVerificationRoot(t, f, 1, 0)
	worker := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
	a := workerVerificationCall(t, f, worker, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "start", Target: ".", TimeoutMilliseconds: 60000})
	r, found, err := f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
	if err != nil || !found {
		t.Fatalf("worker start: %+v %v", r, err)
	}
	cursor, maxBytes, wait := r.Cursor, toolgateway.MaxCommandRuntimePageBytes, int(runner.MaxCommandRuntimeWait.Milliseconds())
	a = workerVerificationCall(t, f, worker, toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "wait", JobID: r.JobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &wait})
	r, found, err = f.service.LatestProjectVerificationReceipt(t.Context(), a, ".")
	if err != nil || !found || !r.Reusable || r.AgentID != worker.child.ID || r.AgentAttemptID != worker.ref.AttemptID {
		t.Fatalf("worker verification: %+v %v", r, err)
	}
	if _, err := f.service.ReadProjectVerificationReceipt(t.Context(), verificationAuthority(t, f), r.JobID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("root adopted worker receipt: %v", err)
	}
}

func TestProjectVerificationLongNativeJobLive(t *testing.T) {
	if os.Getenv("PRAYU_PROJECT_TEST_LONG_LIVE") != "1" {
		t.Skip("set PRAYU_PROJECT_TEST_LONG_LIVE=1 for a real >25 second test")
	}
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	writeVerificationTest(t, f, `const test=require('node:test');test('long native verification',async()=>{await new Promise(r=>setTimeout(r,27000));});`)
	f.supervisor.WithProjectHooks(&projectHookTestTrust{selection: ProjectHookSelection{Fingerprint: strings.Repeat("a", 64), BeforeFinishTest: true}})
	started := time.Now()
	var rounds []domain.SupervisorToolRound
	completed := false
	for boundary := 1; boundary <= 8; boundary++ {
		result, retry, err := projectHookFinish(t, f, boundary, rounds)
		if err != nil {
			t.Fatal(err)
		}
		rounds, err = f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if !retry {
			if result.Status != LifecycleTurnCompleted {
				t.Fatalf("unexpected finish: %+v", result)
			}
			completed = true
			break
		}
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || !completed || len(jobs) != 1 || jobs[0].TimeoutMilliseconds <= 25000 || !projectHookCommandPassed(rounds[len(rounds)-1].Calls[0]) || time.Since(started) < 25*time.Second {
		t.Fatalf("long Hook completed=%v jobs=%+v elapsed=%s err=%v", completed, jobs, time.Since(started), err)
	}
	t.Logf("Deliver Hook retained original Job %s through %d observations and finished after %s", jobs[0].ID, len(rounds)-1, time.Since(started))
}
