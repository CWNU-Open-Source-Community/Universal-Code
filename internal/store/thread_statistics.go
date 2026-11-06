package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

// GetThreadStatistics reads the entire root-agent ledger, independently of the
// transcript's bounded pages. Child agents and auxiliary compaction are excluded.
func (s *SQLiteStore) GetThreadStatistics(ctx context.Context, threadID string) (domain.ThreadStatistics, error) {
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return domain.ThreadStatistics{}, err
	}
	defer finish()
	threadID = strings.TrimSpace(threadID)
	var result domain.ThreadStatistics
	// Committed steering has a Session message too. Count that input once,
	// retaining older direct Session input that predates the steering ledger.
	err = tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM thread_runs binding JOIN operator_steering_messages input
		 ON input.run_id=binding.run_id WHERE binding.thread_id=? AND input.status<>'cancelled') +
		(SELECT COUNT(*) FROM thread_runs binding JOIN session_messages message
		 ON message.session_id=binding.session_id WHERE binding.thread_id=?
		 AND message.role='user' AND message.source_kind='operator_message' AND message.instruction_authorized=1
		 AND NOT EXISTS (SELECT 1 FROM operator_steering_messages input WHERE input.session_message_id=message.id))`,
		threadID, threadID).Scan(&result.Turns)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event.run_id,event.type,event.subject_id,event.payload_json
		FROM thread_runs binding JOIN run_events event ON event.run_id=binding.run_id
		WHERE binding.thread_id=? AND event.source='model_gateway'
		AND event.type IN ('model.started','model.completed','model.failed')
		AND COALESCE(json_extract(event.payload_json,'$.purpose'),'')=''
		AND COALESCE(json_extract(event.payload_json,'$.dispatch'),'')<>'not_sent'
		ORDER BY binding.ordinal,event.sequence`, threadID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	steps := make(map[string]struct{})
	startedSteps := make(map[string]string)
	latestContextCall := ""
	for rows.Next() {
		var runID, kind, subject, raw string
		if err := rows.Scan(&runID, &kind, &subject, &raw); err != nil {
			return result, err
		}
		var receipt struct {
			Turn                int        `json:"turn"`
			AttemptID           string     `json:"attempt_id"`
			ToolRound           int        `json:"tool_round"`
			SteeringSequence    int64      `json:"steering_sequence"`
			InputEstimate       int64      `json:"input_estimate"`
			ContextWindowTokens int64      `json:"context_window_tokens"`
			ContextWindowSource string     `json:"context_window_source"`
			Usage               *llm.Usage `json:"usage"`
			UsageUnknown        bool       `json:"usage_unknown"`
			DecodeMillis        *int64     `json:"decode_millis"`
		}
		if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
			return result, err
		}
		// Transport retries and protocol corrections share one logical step.
		// The immutable terminal receipt for each sent call still contributes its
		// actual usage, including a failed call with a provider usage receipt.
		attempt := receipt.AttemptID
		if attempt == "" {
			attempt = fmt.Sprintf("turn:%d", receipt.Turn)
			if receipt.Turn == 0 {
				attempt = subject
			}
		}
		step := fmt.Sprintf("%s/%s/%d/%d", runID, attempt, receipt.ToolRound, receipt.SteeringSequence)
		call := runID + "/" + subject
		if kind == events.ModelStartedEvent {
			startedSteps[call] = step
			latestContextCall = call
			result.Context = nil
			if receipt.ContextWindowTokens > 0 && receipt.InputEstimate >= 0 {
				result.Context = &domain.ThreadContextUsage{InputTokens: receipt.InputEstimate,
					WindowTokens: receipt.ContextWindowTokens, Estimated: true, WindowSource: receipt.ContextWindowSource}
			}
			continue
		}
		if started, ok := startedSteps[call]; ok {
			step = started
		}
		steps[step] = struct{}{}
		if receipt.Usage == nil {
			continue
		}
		input, output, total, err := supervisorUsage(*receipt.Usage)
		if err != nil {
			return result, err
		}
		// Only the latest request's completed receipt can replace its estimate.
		// Canonical input already includes cache reads and writes. Missing or
		// partial usage (including accounting-only failures) stays estimated.
		if kind == events.ModelCompletedEvent && call == latestContextCall && result.Context != nil &&
			input > 0 && !receipt.UsageUnknown && !supervisorModelUsageUnknown(*receipt.Usage) {
			result.Context.InputTokens = input
			result.Context.Estimated = false
		}
		for _, counter := range []struct {
			target *int64
			value  int64
		}{
			{&result.InputTokens, input}, {&result.OutputTokens, output}, {&result.TotalTokens, total},
			{&result.CacheReadTokens, int64(receipt.Usage.CacheReadTokens)},
		} {
			*counter.target, err = supervisorAddCounter(*counter.target, counter.value, "thread model usage")
			if err != nil {
				return result, err
			}
		}
		result.UsageCalls++
		if receipt.Usage.CacheUsageReported {
			result.CacheReportedCalls++
		}
		if receipt.DecodeMillis != nil && *receipt.DecodeMillis > 0 && output > 0 {
			result.DecodeMillis, err = supervisorAddCounter(result.DecodeMillis, *receipt.DecodeMillis, "thread generation time")
			if err != nil {
				return result, err
			}
			result.TimedOutputTokens, err = supervisorAddCounter(result.TimedOutputTokens, output, "thread timed output")
			if err != nil {
				return result, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	result.Steps = int64(len(steps))
	return result, nil
}
