package store

import (
	"strconv"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/threadtranscript"
)

func TestThreadInputBindingStaysAtQueueAcrossAutomaticToolBoundaries(t *testing.T) {
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "early-input-binding")
	_, lease := prepareThreadTask(t, st, run, 8)
	cp := beginThreadTask(t, st, lease, input)
	threadID := domain.InitialThreadID(run.ID)
	var anchor threadtranscript.Item
	check := func(status domain.OperatorSteeringStatus, bound bool) {
		t.Helper()
		var before, after int64
		if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		steering, err := st.GetOperatorSteering(t.Context(), input)
		if err != nil || steering.Status != status || (status == domain.OperatorSteeringPending && steering.SessionMessageID != 0) {
			t.Fatalf("projection changed input state: %+v %v", steering, err)
		}
		stats, err := st.GetThreadStatistics(t.Context(), threadID)
		if err != nil || stats.Turns != 1 {
			t.Fatalf("one operator input counted twice during %s: turns=%d err=%v", status, stats.Turns, err)
		}
		var sources []threadtranscript.Source
		var items []threadtranscript.Item
		var ordinal, sequence int64
		for {
			page, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, ordinal, sequence, 64)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			projected, err := threadtranscript.Build(threadID, page)
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, page...)
			items = append(items, projected...)
			last := page[len(page)-1]
			if last.Sequence == 0 {
				break
			}
			ordinal, sequence = last.Ordinal, last.Sequence
		}
		count := 0
		for _, item := range items {
			if item.Kind != runactivity.KindOperatorInput {
				continue
			}
			count++
			if item.CanonicalID != input || item.Status != string(status) || item.Detail != "same text" {
				t.Fatalf("input lost original queue identity/state: %+v", item)
			}
			if anchor.ID == "" {
				anchor = item
			} else if anchor.ID != item.ID || anchor.Sequence != item.Sequence || anchor.CreatedAt != item.CreatedAt {
				t.Fatalf("input moved after tools: before=%+v after=%+v", anchor, item)
			}
		}
		if count != 1 {
			t.Fatalf("input bubbles=%d want one", count)
		}
		// Each source must retain the same binding even on a page containing
		// only the queue or only its much later durable Session row.
		for _, source := range sources {
			if source.Event == nil || (source.Event.Type != events.OperatorSteeringQueuedEvent && source.Event.Type != events.SessionMessageEvent) {
				continue
			}
			if source.Event.Type == events.OperatorSteeringQueuedEvent && source.Event.SubjectID == input && source.OperatorMessageBound != bound {
				t.Fatalf("queue binding=%t want %t", source.OperatorMessageBound, bound)
			}
			paged, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, source.Ordinal, source.Sequence+1, 1)
			if err != nil || len(paged) != 1 || paged[0].Event == nil || paged[0].Event.EventID != source.Event.EventID {
				t.Fatalf("single-source page changed: %+v %v", paged, err)
			}
			page, err := threadtranscript.Build(threadID, paged)
			if err != nil {
				t.Fatal(err)
			}
			if source.Event.Type == events.SessionMessageEvent && source.OperatorMessageBound && len(page) != 0 {
				t.Fatalf("Session-only page duplicated input: %+v", page)
			}
		}
		if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || before != after {
			t.Fatalf("input read projection wrote state: %d -> %d %v", before, after, err)
		}
	}
	check(domain.OperatorSteeringPending, false)
	first := cp
	cp = completeThreadTaskSegment(t, st, cp, true)
	if cp.Phase != domain.SupervisorTurnStarted || cp.AttemptID == "" || cp.AttemptID == first.AttemptID || cp.NextTurn != first.NextTurn+1 {
		t.Fatalf("fixture did not start the next automatic segment: %+v", cp)
	}
	check(domain.OperatorSteeringPending, true)
	cp = completeThreadTaskSegment(t, st, cp, true)
	check(domain.OperatorSteeringPending, true)
	completeThreadTaskSegment(t, st, cp, false)
	check(domain.OperatorSteeringCommitted, true)
}

func TestThreadInputBindingRejectsUnrelatedRowsAndReceiptClaims(t *testing.T) {
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "strict-input-binding")
	_, lease := prepareThreadTask(t, st, run, 8)
	cp := beginThreadTask(t, st, lease, input)
	save := func(message session.Message) session.Message {
		t.Helper()
		stored, err := st.SaveSessionMessage(t.Context(), message)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	equalText := save(session.NewMessage(run.SessionID, "user", "same text"))
	different := save(session.NewMessage(run.SessionID, "user", "another explicit input"))
	evidence := save(session.NewEvidenceMessage(run.SessionID, session.SourceWorkspaceFile, "README.md", "same text"))
	_, other, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "another Run and Session", Profile: "review", WorkspaceID: "thread-task-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := save(session.NewMessage(other.SessionID, "user", "same text"))
	completeThreadTaskSegment(t, st, cp, true)
	// These records deliberately fail one durable identity/provenance check.
	// Equal content alone must not hide an independent direct user message.
	for _, claim := range []struct {
		source, attempt, input string
		message                int64
	}{
		{"model_gateway", cp.AttemptID, input, equalText.ID},
		{"run_supervisor", "unowned-attempt", input, equalText.ID},
		{"run_supervisor", cp.AttemptID, "another-operator-input", equalText.ID},
		{"run_supervisor", cp.AttemptID, input, different.ID},
		{"run_supervisor", cp.AttemptID, input, evidence.ID},
		{"run_supervisor", cp.AttemptID, input, foreign.ID},
	} {
		threadStatisticsReceipt(t, st, run, events.AgentTurnCompletedEvent, claim.source, claim.attempt,
			map[string]any{"turn": cp.NextTurn, "tool_round_boundary": true, "lifecycle_action": "continue", "operator_message_id": claim.input, "user_message_id": claim.message})
	}
	threadID := domain.InitialThreadID(run.ID)
	stats, err := st.GetThreadStatistics(t.Context(), threadID)
	if err != nil || stats.Turns != 3 {
		t.Fatalf("unrelated inputs were merged: %+v %v", stats, err)
	}
	sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, 0, 0, 101)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.Event != nil && source.Event.Type == events.SessionMessageEvent {
			for _, independent := range []int64{equalText.ID, different.ID, evidence.ID} {
				if source.Event.SubjectID == strconv.FormatInt(independent, 10) && source.OperatorMessageBound {
					t.Fatalf("unrelated message was bound: %+v", source)
				}
			}
		}
	}
	var count int
	if err := st.db.QueryRowContext(t.Context(), threadOperatorMessageBindingsCTE+` SELECT COUNT(DISTINCT session_message_id) FROM bound_operator_messages`, threadID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid receipt produced an extra binding: count=%d %v", count, err)
	}
}

func TestThreadInputBindingRetainsConsumedInputAfterRunTermination(t *testing.T) {
	for _, terminal := range []string{"cancelled", "failed"} {
		for _, consumed := range []bool{true, false} {
			t.Run(terminal+"/consumed_"+strconv.FormatBool(consumed), func(t *testing.T) {
				st, run := newThreadTaskTest(t)
				input := queueThreadTask(t, st, run, "terminal-input-binding")
				if consumed {
					_, lease := prepareThreadTask(t, st, run, 8)
					cp := beginThreadTask(t, st, lease, input)
					cp = completeThreadTaskSegment(t, st, cp, true)
					if cp.Phase != domain.SupervisorTurnStarted {
						t.Fatalf("next automatic segment did not start: %+v", cp)
					}
				}
				var err error
				if terminal == "cancelled" {
					_, err = application.NewRunService(st).Cancel(t.Context(), run.ID)
				} else {
					_, err = application.NewRunService(st).Fail(t.Context(), run.ID, "terminal input binding regression")
				}
				if err != nil {
					t.Fatal(err)
				}
				message, err := st.GetOperatorSteering(t.Context(), input)
				if err != nil || message.Status != domain.OperatorSteeringCancelled || message.SessionMessageID != 0 {
					t.Fatalf("fixture did not cancel the original steering input: %+v %v", message, err)
				}
				threadID := domain.InitialThreadID(run.ID)
				stats, err := st.GetThreadStatistics(t.Context(), threadID)
				want := int64(0)
				if consumed {
					want = 1
				}
				if err != nil || stats.Turns != want {
					t.Fatalf("terminal input count=%d want=%d err=%v", stats.Turns, want, err)
				}
				sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, 0, 0, 101)
				if err != nil {
					t.Fatal(err)
				}
				userRows := 0
				for _, source := range sources {
					if source.Event == nil {
						continue
					}
					if source.Event.Type == events.OperatorSteeringQueuedEvent && source.Event.SubjectID == input && source.OperatorMessageBound != consumed {
						t.Fatalf("consumed cancelled input lost its exact binding: %+v", source)
					}
					if source.Event.Type == events.SessionMessageEvent {
						var role string
						if err := st.db.QueryRow(`SELECT role FROM session_messages WHERE id=?`, source.Event.SubjectID).Scan(&role); err != nil {
							t.Fatal(err)
						}
						if role == "user" {
							userRows++
						}
					}
				}
				if int64(userRows) != want {
					t.Fatalf("terminal projection invented user rows=%d want=%d", userRows, want)
				}
				items, err := threadtranscript.Build(threadID, sources)
				if err != nil {
					t.Fatal(err)
				}
				inputs := 0
				for _, item := range items {
					if item.Kind == runactivity.KindOperatorInput {
						inputs++
						if item.CanonicalID != input || item.Status != "cancelled" {
							t.Fatalf("terminal input gained a duplicate Session bubble: %+v", item)
						}
					}
				}
				if inputs != 1 {
					t.Fatalf("queue cancellation/input notifications=%d want one", inputs)
				}
			})
		}
	}
}
