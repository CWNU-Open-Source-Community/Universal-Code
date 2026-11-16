package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type supervisorCandidateRanker func(context.Context, []contextmgr.SummaryRecord) ([]int, error)

func (f supervisorCandidateRanker) Rank(ctx context.Context, records []contextmgr.SummaryRecord) ([]int, error) {
	return f(ctx, records)
}

func supervisorCandidateOrder(records []contextmgr.SummaryRecord) []int {
	order := make([]int, len(records))
	for i := range order {
		order[i] = i
	}
	return order
}

func TestSupervisorContextCandidateDoesNotHoldConnectionOrWriterReservation(t *testing.T) {
	st, turn, lease := newSupervisorCompactionTest(t)
	var dbPath string
	if err := st.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other := &SQLiteStore{db: db}
	called := false
	result, err := st.CompactSupervisorContextWithStrategy(context.Background(), turn.Checkpoint, 2,
		supervisorCandidateRanker(func(ctx context.Context, records []contextmgr.SummaryRecord) ([]int, error) {
			called = true
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			// The original store permits only one connection. A retained read
			// transaction would deadlock this query; a writer reservation would
			// prevent a separate connection renewing the real lease below.
			var probe int
			if err := st.db.QueryRowContext(ctx, `SELECT 1`).Scan(&probe); err != nil {
				return nil, err
			}
			if _, err := other.RenewRunExecutionLease(ctx, lease, time.Minute); err != nil {
				return nil, err
			}
			return supervisorCandidateOrder(records), nil
		}))
	if err != nil || !called || !result.Compacted {
		t.Fatalf("candidate held database resources or rejected same-owner renewal: called=%v result=%#v err=%v", called, result, err)
	}
	assertSupervisorCompactionCounts(t, st, turn.Run.SessionID, 1, 4)
}

func TestSupervisorContextCandidateRejectsConcurrentSourceAndExecutionChanges(t *testing.T) {
	for _, change := range []string{"append", "tail_compacted", "input", "images", "attachments", "usage", "release", "takeover", "model_started", "body", "provenance"} {
		t.Run(change, func(t *testing.T) {
			st, turn, lease := newSupervisorCompactionTest(t)
			ctx := context.Background()
			var mutationErr error
			_, err := st.CompactSupervisorContextWithStrategy(ctx, turn.Checkpoint, 2,
				supervisorCandidateRanker(func(ctx context.Context, records []contextmgr.SummaryRecord) ([]int, error) {
					switch change {
					case "append":
						_, mutationErr = st.SaveSessionMessage(ctx, session.NewMessage(turn.Run.SessionID, "user", "new correction during candidate generation"))
					case "tail_compacted":
						_, mutationErr = st.db.ExecContext(ctx, `UPDATE session_messages SET compacted=1 WHERE id=(SELECT MAX(id) FROM session_messages WHERE session_id=?)`, turn.Run.SessionID)
					case "input", "images", "attachments", "usage":
						queries := map[string]string{
							"input":       `UPDATE run_supervisor_checkpoints SET pending_input='new input' WHERE run_id=?`,
							"images":      `UPDATE run_supervisor_checkpoints SET pending_image_count=1 WHERE run_id=?`,
							"attachments": `UPDATE run_supervisor_checkpoints SET pending_attachment_count=1 WHERE run_id=?`,
							"usage":       `UPDATE run_supervisor_checkpoints SET input_tokens=input_tokens+1,total_tokens=total_tokens+1 WHERE run_id=?`,
						}
						_, mutationErr = st.db.ExecContext(ctx, queries[change], turn.Run.ID)
					case "release":
						_, _, mutationErr = st.ReleaseRunExecutionLease(ctx, lease)
					case "takeover":
						expireTestRunExecutionLease(t, ctx, st, lease)
						_, mutationErr = st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: turn.Run.ID, OwnerID: "candidate-new-owner", TTL: time.Minute})
					case "model_started":
						_, mutationErr = st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"})
					case "body", "provenance":
						// Corruption fixture only: normal APIs and the immutable
						// trigger already forbid these rewrites. Preserve row IDs
						// and a valid digest to exercise full snapshot comparison.
						if _, mutationErr = st.db.ExecContext(ctx, `DROP TRIGGER trg_session_message_provenance_update_immutable`); mutationErr != nil {
							break
						}
						if change == "body" {
							message := session.NewMessage(turn.Run.SessionID, "user", "rewritten content with a valid new digest")
							_, mutationErr = st.db.ExecContext(ctx, `UPDATE session_messages SET content=?,content_sha256=?,token_estimate=? WHERE id=(SELECT MAX(id) FROM session_messages WHERE session_id=?)`, message.Content, message.Provenance.ContentSHA256, message.TokenEstimate, turn.Run.SessionID)
						} else {
							_, mutationErr = st.db.ExecContext(ctx, `UPDATE session_messages SET source_ref='different-real-call' WHERE session_id=? AND source_kind='tool_result'`, turn.Run.SessionID)
						}
					}
					if mutationErr != nil {
						return nil, mutationErr
					}
					return supervisorCandidateOrder(records), nil
				}))
			if mutationErr != nil {
				t.Fatalf("fixture mutation failed: %v", mutationErr)
			}
			if apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("stale candidate accepted or misclassified: %v", err)
			}
			compacted := 0
			if change == "tail_compacted" {
				compacted = 1
			}
			assertSupervisorCompactionCounts(t, st, turn.Run.SessionID, 0, compacted)
		})
	}
}

func TestSupervisorContextCandidateRejectsConcurrentSummaryAndCanRetry(t *testing.T) {
	st, turn, _ := newSupervisorCompactionTest(t)
	ctx := context.Background()
	var concurrent contextmgr.Result
	var saveErr error
	_, err := st.CompactSupervisorContextWithStrategy(ctx, turn.Checkpoint, 2,
		supervisorCandidateRanker(func(ctx context.Context, records []contextmgr.SummaryRecord) ([]int, error) {
			// Simulate the compatible legacy caller saving a summary before
			// marking its messages. The in-flight candidate must not append a
			// competing child or mark sources against an unexpected summary.
			messages, err := st.ListSessionMessages(ctx, turn.Run.SessionID, false)
			if err != nil {
				return nil, err
			}
			snapshot := supervisorCompactionSnapshot{Messages: messages}
			concurrent, saveErr = contextmgr.NewManager(st, contextmgr.Config{PreserveRecentMessages: 2}).Compact(ctx,
				turn.Run.SessionID, turn.Mission.WorkspaceID, snapshot.contextMessages())
			return supervisorCandidateOrder(records), saveErr
		}))
	if saveErr != nil || apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("concurrent summary not fenced: save=%v commit=%v", saveErr, err)
	}
	assertSupervisorCompactionCounts(t, st, turn.Run.SessionID, 1, 0)
	retry, err := st.CompactSupervisorContext(ctx, turn.Checkpoint, 2)
	if err != nil || retry.Summary.ID != concurrent.Summary.ID || retry.Summary.ContentSHA256 != concurrent.Summary.ContentSHA256 {
		t.Fatalf("retry did not reuse exact high-water summary: %#v %v", retry, err)
	}
	assertSupervisorCompactionCounts(t, st, turn.Run.SessionID, 1, 4)
}

func TestSupervisorContextCandidateErrorAndCancellationLeaveHistoryUntouched(t *testing.T) {
	for _, cancelCandidate := range []bool{false, true} {
		t.Run(map[bool]string{false: "strategy_error", true: "cancelled"}[cancelCandidate], func(t *testing.T) {
			st, turn, _ := newSupervisorCompactionTest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before, err := st.ListSessionMessages(ctx, turn.Run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			expected := errors.New("candidate ranking unavailable")
			_, err = st.CompactSupervisorContextWithStrategy(ctx, turn.Checkpoint, 2,
				supervisorCandidateRanker(func(_ context.Context, records []contextmgr.SummaryRecord) ([]int, error) {
					if cancelCandidate {
						cancel()
						return supervisorCandidateOrder(records), nil
					}
					return nil, expected
				}))
			if (cancelCandidate && !errors.Is(err, context.Canceled)) || (!cancelCandidate && !errors.Is(err, expected)) {
				t.Fatalf("candidate failure not returned: %v", err)
			}
			after, err := st.ListSessionMessages(context.Background(), turn.Run.SessionID, true)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("candidate failure changed original history: %v", err)
			}
			assertSupervisorCompactionCounts(t, st, turn.Run.SessionID, 0, 0)
		})
	}
}

func TestSupervisorContextEvidenceExcludesRecallButRetainsOriginalToolLedger(t *testing.T) {
	st, turn, _ := newSupervisorCompactionTest(t)
	ctx := context.Background()
	var calls []llm.ToolCall
	for _, name := range []string{"history_search", "history_read", "note_create"} {
		payload := json.RawMessage(`{"title":"read fact","content":"original evidence"}`)
		if name == "history_search" {
			payload = json.RawMessage(`{"query":"original"}`)
		} else if name == "history_read" {
			payload = json.RawMessage(`{"source_id":"message:1","part":"content"}`)
		}
		var normalizeErr error
		if name == "note_create" {
			payload, normalizeErr = toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, payload)
		} else {
			payload, normalizeErr = toolgateway.NormalizeHistoryRecallPayload(toolgateway.ToolName(name), payload)
		}
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		key := runmutation.SupervisorToolOperationKey(turn.Run.ID, turn.Checkpoint.NextTurn, name, string(payload))
		callID, err := runmutation.SupervisorToolCallID(key, 1)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, llm.ToolCall{ID: callID, Name: name, Arguments: payload})
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, llm.ChatResponse{
		Provider: "test", Model: "model", ToolCalls: calls,
	})
	if err != nil {
		t.Fatal(err)
	}
	var originalSHA string
	for _, call := range calls {
		if _, err := st.RecordSupervisorToolExecutionStarted(ctx, checkpoint, call.ID); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"stdout": "retained observation " + call.Name})
		status := domain.SupervisorToolCompleted
		errorCode := ""
		if call.Name != "note_create" {
			// Failed recall remains in the ledger too; it must not become a
			// synthetic observation about the original historical tool.
			status = domain.SupervisorToolFailed
			errorCode = string(apperror.CodeFailedPrecondition)
		}
		stored, _, err := st.RecordSupervisorToolResult(ctx, checkpoint, domain.SupervisorToolResult{
			CallID: call.ID, Status: status, ErrorCode: errorCode, ResultJSON: string(raw), CompletedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if call.Name == "note_create" {
			originalSHA = session.ContentSHA256(stored.ResultJSON)
		}
	}
	before, err := st.ListSupervisorToolRounds(ctx, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Number, attempt.ToolRound, attempt.Outcome = 2, 1, ""
	if _, err := st.RecordSupervisorModelStarted(ctx, checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Text: "Original evidence remains available", Provider: "test", Model: "model"}
	checkpoint, err = st.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.CompleteSupervisorTurn(ctx, checkpoint, response,
		domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: response.Text}, policy.Decision{Allowed: true}, 0); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListSupervisorToolRounds(ctx, checkpoint)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("recall ledger changed during completion: %v", err)
	}
	messages, err := st.ListSessionMessages(ctx, turn.Run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, message := range messages {
		if message.Provenance.SourceRef != "supervisor-tools:"+checkpoint.AttemptID {
			continue
		}
		found = true
		if message.Provenance.InstructionAuthorized || strings.Contains(message.Content, "history_search") || strings.Contains(message.Content, "history_read") ||
			!strings.Contains(message.Content, "note_create") || !strings.Contains(message.Content, originalSHA) {
			t.Fatalf("recall evidence recursively summarized or real evidence lost: %s", message.Content)
		}
	}
	if !found {
		t.Fatal("normal tool evidence was not preserved")
	}
}
