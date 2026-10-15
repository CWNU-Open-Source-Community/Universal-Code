package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

type v166HistoricalStore struct{ *SQLiteStore }

// Message-selected MCP content was introduced in v195. This historical adapter
// has no selections while the current runner exercises the exact v165/v166 schema.
func (*v166HistoricalStore) ListSupervisorMCPReferences(context.Context, domain.SupervisorCheckpoint) ([]domain.ThreadMCPReference, error) {
	return nil, nil
}

func (*v166HistoricalStore) PrepareWebFetchAuthorizationHandoff(context.Context, string, string, domain.SupervisorPhase) (domain.RunExecutionHandoff, bool, error) {
	return domain.RunExecutionHandoff{}, false, nil
}

type v166FailureProvider struct{ calls int }

func (*v166FailureProvider) Name() string { return "v166-fixture" }
func (*v166FailureProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "v166-fixture", Capabilities: []string{"chat", "tools"}}}, nil
}
func (p *v166FailureProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	if p.calls > 1 {
		return nil, errors.New("v166 original provider failure")
	}
	return &llm.ChatResponse{Provider: p.Name(), Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		ToolCalls: []llm.ToolCall{{ID: "v166-fetch", Name: "web_fetch", Arguments: json.RawMessage(`{"version":"web_fetch.v1","url":"https://docs.example.com/v166"}`)}}}, nil
}
func (p *v166FailureProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 1)
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}
func (*v166FailureProvider) SupportsTools(string) bool    { return true }
func (*v166FailureProvider) SupportsVision(string) bool   { return false }
func (*v166FailureProvider) SupportsJSONMode(string) bool { return true }

type v166FetchBackend struct{ calls int }

func (f *v166FetchBackend) Fetch(_ context.Context, target string, _ webevidence.NetworkAuthority, _ webevidence.RobotsPolicy) (webevidence.FetchedContent, error) {
	f.calls++
	return webevidence.FetchedContent{RequestedURL: target, FinalURL: target, HTTPStatus: 200,
		RawDigest: webevidence.DigestBytes([]byte("original v166 evidence")), Robots: "allowed",
		Parsed: webevidence.ParsedDocument{Title: "Migration evidence", Body: "original v166 evidence", MIME: "text/html", Charset: "utf-8"}}, nil
}

func TestSchemaV166ObservesOnlyExactPausedWebFetchFailure(t *testing.T) {
	ctx := context.Background()
	state := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "web-fetch-observation-v166.db"), 165)
	defer state.Close()
	// Current services need queue columns introduced after this historical
	// boundary. Restore the exact v165 schema before exercising its migration.
	restoreHistoricalQueue := addV166FixtureQueueCompatibility(t, state)
	_, run, err := newMigrationFixtureRunService(t, state).Create(ctx, application.CreateRunRequest{
		Goal: "preserve historical fetch failure", Profile: "review", Surface: "code", Phase: "deliver",
		ModelRoute: "v166-fixture/model", Interactive: true, NetworkMode: "disabled",
		Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, backend := &v166FailureProvider{}, &v166FetchBackend{}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	handoff := application.NewRunExecutionHandoffService(&v166HistoricalStore{state}, router, checker).
		WithWebEvidence(webevidence.NewService(state, nil, backend)).WithWebFetchAuthorizationScheduler(true)
	turns := application.NewThreadTurnService(state, application.NewRunLifecycleControlService(state), handoff)
	first, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(run.ID), Content: "Preserve my exact original input", OperationKey: "v166-original-thread-turn", RequestedBy: "test_operator"})
	if err != nil || first.Execution == nil || first.Submission.Run.Status != domain.RunWaitingApproval {
		t.Fatalf("initial approval boundary: %#v err=%v", first, err)
	}
	original := first.Execution.Handoff
	queued, err := application.NewThreadService(state).Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
		Content: "Keep this separate queued follow-up", OperationKey: "v166-queued-thread-message", RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := state.ListApprovals(ctx, approval.ListFilter{RunID: run.ID, Status: approval.StatusPending, Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("approvals=%#v err=%v", records, err)
	}
	authorization, err := state.GetWebFetchAuthorizationByApproval(ctx, records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewApprovalControlService(state, toolgateway.New(state, checker), checker).Decide(ctx,
		application.DecideApprovalControlRequest{Version: application.ApprovalControlProtocolVersion, RunID: run.ID,
			ApprovalID: records[0].ID, Action: application.ApprovalControlApproveOnce, OperationKey: "v166-approve-exact-fetch", ReviewedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := handoff.ResumeWebFetchAuthorization(ctx, run.ID, authorization.ID); err == nil {
		t.Fatal("historical provider continuation unexpectedly succeeded")
	}
	cp, found, err := state.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil || !found || cp.Phase != domain.SupervisorTurnFailed {
		t.Fatalf("checkpoint=%#v err=%v", cp, err)
	}
	run, err = state.GetRun(ctx, run.ID)
	if err != nil || run.Status != domain.RunPaused || backend.calls != 1 {
		t.Fatalf("run=%#v fetches=%d err=%v", run, backend.calls, err)
	}
	beforeCalls := readV165SupervisorCallRows(t, ctx, state, run.ID)
	beforeAuthorization, err := state.GetWebFetchAuthorization(ctx, authorization.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.PrepareWebFetchAuthorizationHandoff(ctx, authorization.ID, cp.AttemptID, domain.SupervisorTurnFailed); err == nil || !strings.Contains(err.Error(), "operation binding is invalid") {
		t.Fatalf("v165 handoff guard with current queue reader did not reject observation: %v", err)
	}
	restoreHistoricalQueue()
	newOperation := func(actor string) domain.RunExecutionHandoffOperation {
		return domain.RunExecutionHandoffOperation{ID: "v166-observation-probe", ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
			KeyDigest:          runmutation.RunExecutionHandoffOperationDigest(run.ID, "v166-probe"),
			RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(run.ID, actor, 1),
			RunID:              run.ID, SessionID: run.SessionID, RequestedBy: actor, MaxSteps: 1, CreatedAt: time.Now().UTC()}
	}
	// This insertion uses only historical columns, so both sides of the
	// migration are checked on their actual schema without compatibility fields.
	probeExactHistoricalObservation := func() error {
		t.Helper()
		tx, err := state.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		op := newOperation("web_fetch_authorization")
		_, err = insertRunExecutionHandoffTx(ctx, tx, run, op, []domain.RunExecutionHandoffItem{{
			OperationID: op.ID, Ordinal: 1, MessageID: first.Submission.Message.ID,
			MessageSequence: first.Submission.Message.Sequence, Prepared: true,
		}})
		return err
	}
	if err := probeExactHistoricalObservation(); err == nil || !strings.Contains(err.Error(), "operation binding is invalid") {
		t.Fatalf("exact v165 schema did not reject paused observation: %v", err)
	}
	if err := state.applyMigration(ctx, migrationPlan()[165]); err != nil {
		t.Fatal(err)
	}
	if version, err := state.SchemaVersion(ctx); err != nil || version != 166 {
		t.Fatalf("schema=%d err=%v", version, err)
	}

	if err := probeExactHistoricalObservation(); err != nil {
		t.Fatalf("exact v166 schema rejected its historical observation: %v", err)
	}
	if _, _, err := state.PrepareRunExecutionHandoff(ctx, newOperation("web_fetch_authorization")); err == nil {
		t.Fatal("public handoff accepted a paused Run using an internal actor name")
	}
	for _, tc := range []struct{ name, actor, mutation string }{
		{name: "ordinary paused", actor: "cli_operator"},
		{name: "changed attempt", actor: "web_fetch_authorization", mutation: `UPDATE run_supervisor_checkpoints SET attempt_id='v166-other-attempt' WHERE run_id=?`},
		{name: "active lease", actor: "web_fetch_authorization", mutation: `UPDATE run_execution_leases SET status='active', released_at=NULL, expires_at='2099-01-01T00:00:00Z' WHERE run_id=?`},
		{name: "changed input", actor: "web_fetch_authorization", mutation: `UPDATE run_supervisor_checkpoints SET pending_input='different input' WHERE run_id=?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := state.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if tc.mutation != "" {
				if _, err := tx.ExecContext(ctx, tc.mutation, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			op := newOperation(tc.actor)
			_, err = insertRunExecutionHandoffTx(ctx, tx, run, op, []domain.RunExecutionHandoffItem{{OperationID: op.ID, Ordinal: 1,
				MessageID: first.Submission.Message.ID, MessageSequence: first.Submission.Message.Sequence, Prepared: true}})
			if err == nil || !strings.Contains(err.Error(), "operation binding is invalid") {
				t.Fatalf("invalid observation admitted or wrong refusal: %v", err)
			}
		})
	}
	t.Run("different queued message", func(t *testing.T) {
		tx, err := state.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		op := newOperation("web_fetch_authorization")
		_, err = insertRunExecutionHandoffTx(ctx, tx, run, op, []domain.RunExecutionHandoffItem{{OperationID: op.ID, Ordinal: 1,
			MessageID: queued.Message.ID, MessageSequence: queued.Message.Sequence, Prepared: false}})
		if err == nil || !strings.Contains(err.Error(), "Historical web fetch observation item binding is invalid") {
			t.Fatalf("observer selected a different queued input: %v", err)
		}
	})
	// The historical trigger probes above need no current queue reader. Restore
	// compatibility only for the existing service identity and replay assertions.
	restoreHistoricalQueue = addV166FixtureQueueCompatibility(t, state)
	observed, bound, err := state.PrepareWebFetchAuthorizationHandoff(ctx, authorization.ID, cp.AttemptID, domain.SupervisorTurnFailed)
	if err != nil || !bound || len(observed.Items) != 1 || observed.Items[0].MessageID != first.Submission.Message.ID || !observed.Items[0].Prepared {
		t.Fatalf("exact observation=%#v bound=%t err=%v", observed, bound, err)
	}
	replayed, rebound, err := state.PrepareWebFetchAuthorizationHandoff(ctx, authorization.ID, cp.AttemptID, domain.SupervisorTurnFailed)
	if err != nil || !rebound || !reflect.DeepEqual(observed, replayed) {
		t.Fatalf("observation replay changed: %#v err=%v", replayed, err)
	}
	stored, found, err := state.GetRunExecutionHandoff(ctx, original.Operation.KeyDigest)
	if err != nil || !found || !reflect.DeepEqual(stored, original) {
		t.Fatalf("old handoff changed: %#v err=%v", stored, err)
	}
	currentRun, err := state.GetRun(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(currentRun, run) {
		t.Fatalf("observation changed Run: %#v err=%v", currentRun, err)
	}
	currentCP, _, err := state.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(currentCP, cp) {
		t.Fatalf("observation changed checkpoint: %#v err=%v", currentCP, err)
	}
	currentAuthorization, err := state.GetWebFetchAuthorization(ctx, authorization.ID)
	if err != nil || !reflect.DeepEqual(currentAuthorization, beforeAuthorization) || !reflect.DeepEqual(readV165SupervisorCallRows(t, ctx, state, run.ID), beforeCalls) || backend.calls != 1 {
		t.Fatalf("observation rewrote authorization/tool evidence or reran fetch: %#v err=%v", currentAuthorization, err)
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE run_execution_handoff_operations SET requested_by='rewritten' WHERE id=?`, original.Operation.ID); err == nil {
		t.Fatal("old operation lost immutability")
	}
	restoreHistoricalQueue()
}
