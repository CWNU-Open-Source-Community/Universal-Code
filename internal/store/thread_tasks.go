package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/threadtranscript"
)

type threadTaskSpan struct {
	task        threadtranscript.Task
	start, end  int64
	latestStart int64
	inputRowID  int64
	completions []threadTaskCompletion
}

type threadTaskCompletion struct {
	sequence int64
	message  string
	at       time.Time
}

type threadTaskStart struct {
	sequence int64
	input    string
}

type threadTaskIndex struct {
	tasks    map[string]*threadTaskSpan
	attempts map[string]string
	inputs   map[string]string
	starts   []threadTaskStart
}

type threadTaskQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Task ownership comes from the delivery ledger and exact root-turn receipts.
// Read the boundaries outside the requested transcript page: a tail page must
// retain the same task ID and terminal facts as a page containing its input.
func loadThreadTaskIndex(ctx context.Context, q threadTaskQuery, threadID, runID string) (*threadTaskIndex, error) {
	index := &threadTaskIndex{tasks: map[string]*threadTaskSpan{}, attempts: map[string]string{}, inputs: map[string]string{}}
	rows, err := q.QueryContext(ctx, `SELECT started.sequence,started.subject_id,started.created_at,
		message.id,COALESCE(message.session_message_id,0)
		FROM thread_runs binding JOIN run_events started ON started.run_id=binding.run_id
		LEFT JOIN operator_steering_deliveries delivery ON delivery.run_id=started.run_id
			AND delivery.attempt_id=started.subject_id AND delivery.turn=json_extract(started.payload_json,'$.turn')
		LEFT JOIN run_execution_handoff_operations approval ON approval.id=json_extract(started.payload_json,'$.approval_continuation_handoff_id')
			AND approval.run_id=binding.run_id AND approval.session_id=binding.session_id
		LEFT JOIN run_execution_handoff_items approval_item ON approval_item.operation_id=approval.id
		JOIN operator_steering_messages message ON message.id=COALESCE(delivery.message_id,approval_item.message_id)
			AND message.run_id=binding.run_id AND message.session_id=binding.session_id AND message.delivery_mode='next_turn'
		WHERE binding.thread_id=? AND binding.run_id=? AND started.type=? AND started.source='run_supervisor'
		AND NOT EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=message.id)
		ORDER BY started.sequence`, threadID, runID, events.AgentTurnStartedEvent)
	if err != nil {
		return nil, err
	}
	type binding struct {
		sequence, inputRowID    int64
		attempt, input, created string
	}
	bindings := []binding{}
	for rows.Next() {
		var sequence, inputRowID int64
		var attempt, created, input string
		if err = rows.Scan(&sequence, &attempt, &created, &input, &inputRowID); err != nil {
			break
		}
		if old, exists := index.attempts[attempt]; exists && old != input {
			// An ambiguous historical binding cannot identify a product task.
			index.attempts[attempt] = ""
		} else {
			index.attempts[attempt] = input
		}
		bindings = append(bindings, binding{sequence, inputRowID, attempt, input, created})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		owner := index.attempts[b.attempt]
		index.starts = append(index.starts, threadTaskStart{sequence: b.sequence, input: owner})
		if owner == "" {
			continue
		}
		span := index.tasks[owner]
		if span == nil {
			at := parseTS(b.created)
			span = &threadTaskSpan{task: threadtranscript.Task{ID: owner, InputMessageID: owner, State: "running", StartedAt: &at}, start: b.sequence, inputRowID: b.inputRowID}
			index.tasks[owner] = span
		}
		index.inputs[owner] = owner
		span.latestStart = b.sequence
	}
	if len(index.tasks) == 0 {
		return index, nil
	}
	// Corrections are attached to their exact active attempt, never to the next
	// queued request or to a message with equal text.
	rows, err = q.QueryContext(ctx, `SELECT id,target_attempt_id FROM operator_steering_messages
		WHERE run_id=? AND delivery_mode='steer'`, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var input, attempt string
		if err = rows.Scan(&input, &attempt); err != nil {
			break
		}
		if owner := index.attempts[attempt]; owner != "" {
			index.inputs[input] = owner
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	// A Session assistant row alone is not a final reply. Its root receipt must
	// consume this exact input and must not be a tool-loop yield boundary.
	rows, err = q.QueryContext(ctx, `SELECT completed.sequence,completed.subject_id,completed.created_at,
		assistant_event.event_id,json_extract(completed.payload_json,'$.user_message_id')
		FROM thread_runs binding JOIN run_events completed ON completed.run_id=binding.run_id
		JOIN session_messages assistant ON assistant.id=json_extract(completed.payload_json,'$.assistant_message_id')
			AND assistant.session_id=binding.session_id AND assistant.role='assistant' AND assistant.source_kind='model_response'
		JOIN run_events assistant_event ON assistant_event.run_id=completed.run_id AND assistant_event.source='session_store'
			AND assistant_event.type=? AND assistant_event.subject_id=CAST(assistant.id AS TEXT)
			AND json_extract(assistant_event.payload_json,'$.session_id')=binding.session_id
			AND json_extract(assistant_event.payload_json,'$.role')='assistant'
			AND json_extract(assistant_event.payload_json,'$.source_kind')='model_response'
		WHERE binding.thread_id=? AND binding.run_id=? AND completed.type=? AND completed.source='run_supervisor'
		AND COALESCE(json_extract(completed.payload_json,'$.tool_round_boundary'),0)=0
		AND COALESCE(json_extract(completed.payload_json,'$.lifecycle_action'),'')<>'wait'
		ORDER BY completed.sequence`, events.SessionMessageEvent, threadID, runID, events.AgentTurnCompletedEvent)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sequence, userID int64
		var attempt, created, message string
		if err = rows.Scan(&sequence, &attempt, &created, &message, &userID); err != nil {
			break
		}
		if span := index.tasks[index.attempts[attempt]]; span != nil && span.inputRowID > 0 && span.inputRowID == userID {
			span.completions = append(span.completions, threadTaskCompletion{sequence: sequence, message: message, at: parseTS(created)})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT item.message_id,item.ordinal,result.steps_completed,operation.event_sequence,result.status,result.stop_reason,result.error_code,
		ending.event_id,ending.sequence,ending.created_at
		FROM thread_runs binding JOIN run_execution_handoff_operations operation ON operation.run_id=binding.run_id AND operation.session_id=binding.session_id
		JOIN run_execution_handoff_items item ON item.operation_id=operation.id
		JOIN run_execution_handoff_results result ON result.operation_id=operation.id
		JOIN run_events ending ON ending.run_id=operation.run_id AND ending.subject_id=operation.id
			AND ending.sequence=result.completion_event_sequence AND ending.type=? AND ending.source='run_execution_handoff'
			AND json_extract(ending.payload_json,'$.status')=result.status
			AND json_extract(ending.payload_json,'$.stop_reason')=result.stop_reason
			AND json_extract(ending.payload_json,'$.error_code')=result.error_code
			AND json_extract(ending.payload_json,'$.steps_completed')=result.steps_completed
		WHERE binding.thread_id=? AND binding.run_id=? ORDER BY ending.sequence,item.ordinal`,
		events.RunExecutionHandoffCompletedEvent, threadID, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var input, status, reason, code, eventID, created string
		var requested, sequence int64
		var ordinal, stepsCompleted int
		if err = rows.Scan(&input, &ordinal, &stepsCompleted, &requested, &status, &reason, &code, &eventID, &sequence, &created); err != nil {
			break
		}
		span := index.tasks[input]
		if span == nil || sequence < span.latestStart {
			continue
		}
		finishedSelection := status == "completed" && (reason == "turn_finish" || reason == "selection_drained" || reason == "root_finish" || reason == "run_terminal")
		// A later selected input may fail or wait after this one was consumed.
		// The durable step count and next input's start confirm this earlier
		// exact reply without declaring the interrupted input completed.
		finishedEarlierInput := ordinal <= stepsCompleted && index.ownerAt(sequence) != input
		if finishedSelection || finishedEarlierInput {
			for _, completion := range span.completions {
				if completion.sequence > requested && completion.sequence < sequence {
					at := completion.at
					span.task.State, span.task.FinishedAt, span.end = "completed", &at, sequence
					span.task.FinalMessageID, span.task.ChangeSourceEventID = completion.message, completion.message
				}
			}
		} else if status == "failed" && index.ownerAt(sequence) == input {
			at := parseTS(created)
			span.task.State, span.task.FinishedAt, span.end = "failed", &at, sequence
			if strings.EqualFold(code, "cancelled") {
				span.task.State = "interrupted"
			}
			span.task.FinalMessageID, span.task.ChangeSourceEventID = "", eventID
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return index, err
}

func (index *threadTaskIndex) ownerAt(sequence int64) string {
	position := sort.Search(len(index.starts), func(i int) bool { return index.starts[i].sequence > sequence })
	if position == 0 {
		return ""
	}
	return index.starts[position-1].input
}

func (index *threadTaskIndex) forSource(source threadtranscript.Source) *threadtranscript.Task {
	if source.Event == nil {
		return nil
	}
	owner := index.ownerAt(source.Sequence)
	directInput := strings.HasPrefix(source.Event.Type, "operator.steering_") ||
		(source.Event.Type == events.ThreadTurnFailedEvent && source.Event.Source == "thread_turn")
	if directInput {
		owner = index.inputs[source.Event.SubjectID]
	}
	span := index.tasks[owner]
	if span == nil || (!directInput && span.end > 0 && source.Sequence > span.end) {
		return nil
	}
	copy := span.task
	return &copy
}
