package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func workflowVerificationSpec(code bool, count int) domain.WorkflowSpec {
	spec := domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "verified native workflow", Limits: domain.WorkflowLimits{MaxSteps: 8, MaxJobs: 4, MaxTokens: 1000, MaxTurns: 8, TimeoutMillis: 600000}}
	for i := 0; i < count; i++ {
		step := domain.WorkflowStep{ID: string(rune('a' + i)), Kind: "worker", Task: &domain.ChildTask{Title: "deliver code", Goal: "Deliver the requested change", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 200, ToolCallLimit: 6, TimeoutMillis: 60000, Worker: &domain.WorkerProfile{}}}
		if code {
			step.Completion = &domain.WorkflowCompletion{Mode: "verified_code"}
		}
		spec.Steps = append(spec.Steps, step)
	}
	return spec
}

func workflowNativeFinishResponse() *llm.ChatResponse {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "worker claims success", WorkItemIDs: []string{}, NoteIDs: []string{}}
	raw, _ := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "finished", Report: &report})
	return &llm.ChatResponse{Text: string(raw), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
}

func startNativeVerificationWorkflow(t *testing.T, f *commandApprovalFixture, p *projectHookFinishProvider, spec domain.WorkflowSpec) (*WorkflowService, *CommandRuntimeMultiplexer, domain.WorkflowExecution) {
	t.Helper()
	idleVerificationRoot(t, f, 1, 0)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("fixture lease: %v", err)
	}
	if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "fixture"})
	router.RegisterProvider(p)
	mux, err := NewCommandRuntimeMultiplexer(f.service)
	if err != nil {
		t.Fatal(err)
	}
	host := NewAgentRunner(f.st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(mux)
	service := NewWorkflowService(f.st, NewSpecialistExecutionService(f.st, router, policy.NewDefaultChecker()).WithWorkerRuntime(host)).WithProjectVerification(mux)
	d, err := service.Save(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	e, err := service.Start(t.Context(), StartWorkflowRequest{RunID: f.turn.Run.ID, DefinitionID: d.ID, OperationKey: "start-native-verification-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	return service, mux, e
}

func nativeWorkflowTestsProvider() *projectHookFinishProvider {
	p := &projectHookFinishProvider{}
	p.respond = func(_ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index%2 == 0 {
			return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("test-%d", index), Name: "run_tests", Arguments: json.RawMessage(`{"version":"run-tests.v1","target":".","timeout_milliseconds":10000}`)}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		}
		return workflowNativeFinishResponse(), nil
	}
	return p
}

func advanceVerificationWorkflow(t *testing.T, service *WorkflowService, e domain.WorkflowExecution) domain.WorkflowExecution {
	t.Helper()
	next, err := service.Advance(t.Context(), e.ID)
	if err != nil {
		t.Fatalf("advance: %v (%+v)", err, next)
	}
	return next
}

func TestWorkflowCodeCompletionUsesNativeReceiptsAndRetainsReports(t *testing.T) {
	for _, scenario := range []string{"report", "missing", "passed", "failed", "fake_projection", "source_changed", "permission_changed", "backend_changed", "unreaped"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
			p := nativeWorkflowTestsProvider()
			if scenario == "report" || scenario == "missing" {
				p.respond = func(_ llm.ChatRequest, _ int) (*llm.ChatResponse, error) { return workflowNativeFinishResponse(), nil }
			}
			if scenario == "failed" || scenario == "fake_projection" {
				writeVerificationTest(t, f, `const test=require('node:test');test('fails',()=>{throw Error('real native failure')});`)
			}
			service, _, e := startNativeVerificationWorkflow(t, f, p, workflowVerificationSpec(scenario != "report", 1))
			if scenario == "fake_projection" {
				f.service.store = &workflowVerificationReadStore{SQLiteStore: f.st, fakePassProjection: true}
			}
			e = advanceVerificationWorkflow(t, service, e)
			if scenario == "source_changed" {
				writeVerificationTest(t, f, `const test=require('node:test');test('new source',()=>{});`)
			}
			if scenario == "permission_changed" {
				f.caps.RuntimeAuthority.RevokeRun(f.turn.Run.ID)
			}
			if scenario == "backend_changed" {
				f.service.adapter.Generation = strings.Repeat("f", 64)
			}
			if scenario == "unreaped" {
				f.service.store = &workflowVerificationReadStore{SQLiteStore: f.st, unreaped: true}
			}
			if e.Status != "failed" {
				e = advanceVerificationWorkflow(t, service, e)
			}
			want := "failed"
			if scenario == "report" || scenario == "passed" {
				want = "completed"
			}
			if e.Status != want {
				t.Fatalf("workflow status=%s want=%s reason=%s proofs=%+v", e.Status, want, e.Reason, e.State.Verifications)
			}
			if !strings.Contains(string(e.State.Outputs["a"]), "worker claims success") {
				t.Fatalf("native report was lost: %+v", e.State.Outputs)
			}
			if scenario != "report" {
				proof, ok := e.State.Verifications["."]
				if !ok || proof.CheckedAt.IsZero() || (want == "failed" && (proof.Reusable || proof.Reason == "")) || (want == "completed" && (!proof.Reusable || proof.State != "passed" || proof.JobID == "")) {
					t.Fatalf("verification result missing or incorrectly reusable: %+v", proof)
				}
			}
			if scenario == "passed" {
				all, _ := json.Marshal(p.requests)
				if !strings.Contains(string(all), "Workflow code completion requirement") || !strings.Contains(string(all), "run_tests") {
					t.Fatal("worker was not told the actual completion requirement")
				}
			}
		})
	}
}

// Negative evidence is injected at the read seam: SQLite's immutable ledgers
// independently prevent mutating completed receipts and native Job ownership.
type workflowVerificationReadStore struct {
	*store.SQLiteStore
	fakePassProjection bool
	unreaped           bool
	actorID            string
}

func (s *workflowVerificationReadStore) ListProjectVerificationCalls(ctx context.Context, runID, actorID string) ([]domain.ProjectVerificationCall, error) {
	calls, err := s.SQLiteStore.ListProjectVerificationCalls(ctx, runID, actorID)
	if err != nil || !s.fakePassProjection {
		return calls, err
	}
	for i := range calls {
		c := calls[i].Specialist
		if c == nil {
			continue
		}
		var envelope map[string]any
		if json.Unmarshal([]byte(c.ResultJSON), &envelope) != nil {
			continue
		}
		result, _ := envelope["result"].(map[string]any)
		r, _ := result["verification_receipt"].(map[string]any)
		if r == nil {
			continue
		}
		r["state"], r["reusable"], r["exit_code"] = "passed", true, 0
		raw, _ := json.Marshal(envelope)
		c.ResultJSON = string(raw)
	}
	return calls, nil
}

func (s *workflowVerificationReadStore) GetCommandRuntimeJob(ctx context.Context, id string) (runner.CommandRuntimeJob, error) {
	job, err := s.SQLiteStore.GetCommandRuntimeJob(ctx, id)
	if s.unreaped {
		job.TreeReaped = false
	}
	return job, err
}

func (s *workflowVerificationReadStore) GetCommandRuntimeJobAttribution(ctx context.Context, id string) (domain.AgentAttribution, error) {
	actor, err := s.SQLiteStore.GetCommandRuntimeJobAttribution(ctx, id)
	if s.actorID != "" {
		actor.AgentID = s.actorID
	}
	return actor, err
}

func workflowVerificationSQL(t *testing.T, f *commandApprovalFixture, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite3", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowVerificationRechecksAfterRestartAndLatestTargetReplacesOldSource(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	p := nativeWorkflowTestsProvider()
	service, _, e := startNativeVerificationWorkflow(t, f, p, workflowVerificationSpec(true, 2))
	e = advanceVerificationWorkflow(t, service, e)
	first := e.State.Verifications["."]
	writeVerificationTest(t, f, `const test=require('node:test');test('changed by next step',()=>{});`)
	e = advanceVerificationWorkflow(t, service, e)
	latest := e.State.Verifications["."]
	if e.Status == "failed" || first.JobID == latest.JobID || first.SourceVersion == latest.SourceVersion || latest.StepID != "b" {
		t.Fatalf("later current-source pass did not replace previous target: first=%+v latest=%+v execution=%+v", first, latest, e)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.st = reopened
	recovered, err := NewCommandRuntimeService(reopened, f.manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	mux, err := NewCommandRuntimeMultiplexer(recovered)
	if err != nil {
		t.Fatal(err)
	}
	service = NewWorkflowService(reopened, NewSpecialistExecutionService(reopened, nil, policy.NewDefaultChecker())).WithProjectVerification(mux)
	e = advanceVerificationWorkflow(t, service, e)
	if e.Status != "completed" || e.State.Verifications["."].JobID != latest.JobID || p.calls != 4 {
		t.Fatalf("restart did not reuse freshly rechecked native evidence: %+v calls=%d", e, p.calls)
	}
}

func TestWorkflowCodeVerificationStartWaitAcrossAttempts(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	p := &projectHookFinishProvider{}
	var first toolgateway.ProjectVerificationReceipt
	p.respond = func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		testCall := func(input toolgateway.RunTestsInput) (*llm.ChatResponse, error) {
			raw, _ := json.Marshal(input)
			return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "test", Name: "run_tests", Arguments: raw}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		}
		switch index {
		case 0:
			return testCall(toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "start", Target: ".", TimeoutMilliseconds: 60000})
		case 1:
			for _, message := range request.Messages {
				for _, result := range message.ToolResults {
					var projection struct {
						Result struct {
							Receipt toolgateway.ProjectVerificationReceipt `json:"verification_receipt"`
						} `json:"result"`
					}
					if json.Unmarshal([]byte(result.Content), &projection) == nil && projection.Result.Receipt.JobID != "" {
						first = projection.Result.Receipt
					}
				}
			}
			if first.JobID == "" {
				t.Fatal("start produced no original Job receipt")
			}
			return &llm.ChatResponse{Text: `{"version":"specialist_lifecycle.v1","action":"continue","message":"Observe the original test Job next turn."}`, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		case 2:
			cursor, maxBytes, wait := first.Cursor, toolgateway.MaxCommandRuntimePageBytes, int(runner.MaxCommandRuntimeWait.Milliseconds())
			return testCall(toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: "wait", JobID: first.JobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &wait})
		default:
			return workflowNativeFinishResponse(), nil
		}
	}
	spec := workflowVerificationSpec(true, 1)
	spec.Steps[0].Task.TurnLimit = 2
	service, _, e := startNativeVerificationWorkflow(t, f, p, spec)
	e = advanceVerificationWorkflow(t, service, e)
	if e.Status == "failed" || e.State.Jobs != 0 {
		t.Fatalf("continued test step: %+v", e)
	}
	e = advanceVerificationWorkflow(t, service, e)
	proof := e.State.Verifications["."]
	completion, found, err := f.st.GetAgentCompletion(t.Context(), proof.AgentID)
	if err != nil || !found || completion.AttemptID == first.AgentAttemptID || proof.JobID != first.JobID || !proof.Reusable {
		t.Fatalf("origin was rebound to Finish attempt: first=%+v proof=%+v completion=%+v err=%v", first, proof, completion, err)
	}
	e = advanceVerificationWorkflow(t, service, e)
	if e.Status != "completed" || p.calls != 4 {
		t.Fatalf("cross-attempt verification did not complete: %+v calls=%d", e, p.calls)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: e.RunID, Limit: 10})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("observation started another Job: %+v %v", jobs, err)
	}
}

func TestWorkflowVerificationRequiresCurrentHostClaimAndOwnedJob(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	service, mux, e := startNativeVerificationWorkflow(t, f, nativeWorkflowTestsProvider(), workflowVerificationSpec(true, 1))
	e = advanceVerificationWorkflow(t, service, e)
	proof := e.State.Verifications["."]
	e, claimed, err := f.st.ClaimWorkflowExecution(t.Context(), e.ID, "host-verification-owner")
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	scope := workflowVerificationScope{executionID: e.ID, stepExecutionID: proof.StepExecutionID, owner: "host-verification-owner", revision: e.Version}
	if r, found, err := mux.latestWorkflowVerificationReceipt(t.Context(), scope); err != nil || !found || !r.Reusable {
		t.Fatalf("host-owned completed child: %+v %v", r, err)
	}
	for _, mutate := range []func(*workflowVerificationScope){
		func(s *workflowVerificationScope) { s.owner = "different-host" },
		func(s *workflowVerificationScope) { s.revision++ },
		func(s *workflowVerificationScope) { s.stepExecutionID = "workflowstep-sibling" },
		func(s *workflowVerificationScope) { s.executionID = "workflowrun-other-run" },
	} {
		other := scope
		mutate(&other)
		if _, _, err = mux.latestWorkflowVerificationReceipt(t.Context(), other); err == nil {
			t.Fatalf("accepted a foreign or stale host read: %+v", other)
		}
	}
	// The private seam must not broaden the ordinary root receipt reader.
	lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: e.RunID, OwnerID: "ordinary-receipt-reader", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ReadProjectVerificationReceipt(t.Context(), verificationAuthority(t, f), proof.JobID); err == nil {
		t.Fatal("root adopted terminal child evidence through the ordinary reader")
	}
	if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
		t.Fatal(err)
	}
	f.service.store = &workflowVerificationReadStore{SQLiteStore: f.st, actorID: f.turn.Agent.ID}
	if _, _, err = mux.latestWorkflowVerificationReceipt(t.Context(), scope); err == nil {
		t.Fatal("host accepted a different child's Job attribution")
	}
	f.service.store = f.preparedStore
	workflowVerificationSQL(t, f, `UPDATE workflow_executions SET claim_until=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), e.ID)
	if _, _, err = mux.latestWorkflowVerificationReceipt(t.Context(), scope); err == nil {
		t.Fatal("expired host claim read a child receipt")
	}
}

func TestWorkflowVerificationReplayRejectsChangedSourceAndLaterFailedTests(t *testing.T) {
	for _, scenario := range []string{"replay_changed_source", "later_failed_test"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
			p := nativeWorkflowTestsProvider()
			if scenario == "later_failed_test" {
				// An external marker changes the second native outcome while the
				// project source and registered test definition remain identical.
				marker, _ := json.Marshal(filepath.Join(t.TempDir(), "second-invocation"))
				writeVerificationTest(t, f, `const test=require('node:test'),fs=require('fs');const marker=`+string(marker)+`;test('second invocation fails',()=>{if(fs.existsSync(marker))throw Error('later failure');fs.writeFileSync(marker,'first passed');});`)
				p.respond = func(_ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
					if index < 2 {
						return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("test-%d", index), Name: "run_tests", Arguments: json.RawMessage(`{"version":"run-tests.v1","target":".","timeout_milliseconds":10000}`)}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
					}
					return workflowNativeFinishResponse(), nil
				}
			}
			service, _, e := startNativeVerificationWorkflow(t, f, p, workflowVerificationSpec(true, 1))
			initial := e.State
			e = advanceVerificationWorkflow(t, service, e)
			if scenario == "replay_changed_source" {
				if e.Status == "failed" {
					t.Fatalf("initial proof failed: %+v", e)
				}
				// Recreate a crash after CompleteWorkflowStep, before checkpoint.
				raw, _ := json.Marshal(initial)
				workflowVerificationSQL(t, f, `UPDATE workflow_executions SET state_json=? WHERE id=?`, string(raw), e.ID)
				writeVerificationTest(t, f, `const test=require('node:test');test('new current source',()=>{});`)
				// A new interpreter has neither the accepted in-memory receipt nor
				// any worker route to call; it must verify the persisted native Job.
				service = NewWorkflowService(f.st, NewSpecialistExecutionService(f.st, nil, policy.NewDefaultChecker())).WithProjectVerification(f.service)
				e = advanceVerificationWorkflow(t, service, e)
			}
			if e.Status != "failed" || e.State.Verifications["."].Reusable {
				t.Fatalf("accepted obsolete or superseded proof: %+v", e)
			}
		})
	}
}

func TestWorkflowFinalVerificationIncludesLaterReportWorkerTests(t *testing.T) {
	f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
	marker, _ := json.Marshal(filepath.Join(t.TempDir(), "report-fails"))
	writeVerificationTest(t, f, `const test=require('node:test'),fs=require('fs');const marker=`+string(marker)+`;test('report sees failure',()=>{if(fs.existsSync(marker))throw Error('later report failure');fs.writeFileSync(marker,'first passed');});`)
	spec := workflowVerificationSpec(true, 2)
	spec.Steps[1].Completion = &domain.WorkflowCompletion{Mode: "report"}
	service, _, e := startNativeVerificationWorkflow(t, f, nativeWorkflowTestsProvider(), spec)
	e = advanceVerificationWorkflow(t, service, e)
	first := e.State.Verifications["."]
	e = advanceVerificationWorkflow(t, service, e)
	if e.Status == "failed" || e.State.Jobs != 2 {
		t.Fatalf("report step should retain its report semantics: %+v", e)
	}
	e = advanceVerificationWorkflow(t, service, e)
	latest := e.State.Verifications["."]
	if e.Status != "failed" || latest.JobID == first.JobID || latest.AgentID == first.AgentID || latest.State != "failed" || latest.Reusable || latest.ExitCode == nil || *latest.ExitCode == 0 {
		t.Fatalf("later report test failure was hidden by the code pass: first=%+v latest=%+v execution=%+v", first, latest, e)
	}
}

func TestWorkflowVerificationControlConflictPreservesPendingAcceptance(t *testing.T) {
	for _, scenario := range []string{"pause", "pause_resume", "lost_claim"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProjectVerificationFixture(t, domain.ExecutionPhaseDeliver)
			p := nativeWorkflowTestsProvider()
			service, _, e := startNativeVerificationWorkflow(t, f, p, workflowVerificationSpec(true, 1))
			respond := p.respond
			var paused domain.WorkflowExecution
			p.respond = func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index == 1 {
					if scenario == "lost_claim" {
						workflowVerificationSQL(t, f, `UPDATE workflow_executions SET claim_owner='new-host-owner' WHERE id=?`, e.ID)
					} else {
						var err error
						paused, err = service.Pause(t.Context(), e.ID, e.Version)
						if err != nil {
							t.Fatal(err)
						}
						if scenario == "pause_resume" {
							if _, err = service.Resume(t.Context(), e.ID, paused.Version); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return respond(request, index)
			}
			_, err := service.Advance(t.Context(), e.ID)
			if scenario != "pause" && apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
				t.Fatalf("control change should return its conflict: %v", err)
			}
			if scenario == "pause" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = service.Resume(t.Context(), e.ID, paused.Version); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "lost_claim" {
				if err = f.st.ReleaseWorkflowExecution(t.Context(), e.ID, "new-host-owner"); err != nil {
					t.Fatal(err)
				}
			}
			// The child has already finished, so no second model or native Job
			// may be run when the interpreter retries the acceptance.
			e = advanceVerificationWorkflow(t, service, e)
			e = advanceVerificationWorkflow(t, service, e)
			if e.Status != "completed" || p.calls != 2 || !e.State.Verifications["."].Reusable {
				t.Fatalf("control conflict poisoned the completed native candidate: %+v calls=%d", e, p.calls)
			}
		})
	}
}
