package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func (s *SQLiteStore) ListSupervisorToolRounds(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint,
) ([]domain.SupervisorToolRound, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		r.run_id, r.turn, r.attempt_id, r.round, r.model_attempt, r.created_at, r.completed_at,
		c.run_id, c.turn, c.attempt_id, c.round, c.position, c.model_attempt, c.call_id,
		c.stream_response_id, c.stream_item_id, c.stream_call_id, c.tool_name,
		c.payload_json, c.authority_json, c.status, c.result_json, c.error_code, c.created_at, c.completed_at
		FROM run_supervisor_tool_rounds r
		JOIN run_supervisor_tool_calls c
			ON c.run_id = r.run_id AND c.turn = r.turn AND c.attempt_id = r.attempt_id AND c.round = r.round
		WHERE r.run_id = ? AND r.turn = ? AND r.attempt_id = ? ORDER BY r.round, c.position`,
		checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID)
	if err != nil {
		return nil, err
	}
	rounds, err := scanSupervisorToolRounds(rows, domain.MaxSupervisorExecutionRounds)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err := s.enrichSupervisorToolRoundAttribution(ctx, rounds); err != nil {
		return nil, err
	}
	return rounds, nil
}

func (s *SQLiteStore) ListRunSupervisorToolRoundsPage(ctx context.Context, runID string,
	offset int, limit int,
) ([]domain.SupervisorToolRound, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || len([]rune(runID)) > domain.MaxSupervisorToolIdentityRunes {
		return nil, apperror.New(apperror.CodeInvalidArgument, "run id is required and bounded")
	}
	if err := validateStoreReadPage(offset, limit); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	rows, err := s.db.QueryContext(ctx, `WITH selected AS (
		SELECT run_id, turn, attempt_id, round, model_attempt, created_at, completed_at
		FROM run_supervisor_tool_rounds WHERE run_id = ?
		ORDER BY turn DESC, created_at DESC, attempt_id DESC, round DESC LIMIT ? OFFSET ?
	)
	SELECT
		r.run_id, r.turn, r.attempt_id, r.round, r.model_attempt, r.created_at, r.completed_at,
		c.run_id, c.turn, c.attempt_id, c.round, c.position, c.model_attempt, c.call_id,
		c.stream_response_id, c.stream_item_id, c.stream_call_id, c.tool_name,
		c.payload_json, c.authority_json, c.status, c.result_json, c.error_code, c.created_at, c.completed_at
		FROM selected r
		JOIN run_supervisor_tool_calls c
			ON c.run_id = r.run_id AND c.turn = r.turn AND c.attempt_id = r.attempt_id AND c.round = r.round
		ORDER BY r.turn DESC, r.created_at DESC, r.attempt_id DESC, r.round DESC, c.position`,
		runID, limit, offset)
	if err != nil {
		return nil, err
	}
	rounds, err := scanSupervisorToolRounds(rows, limit)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err := s.enrichSupervisorToolRoundAttribution(ctx, rounds); err != nil {
		return nil, err
	}
	return rounds, nil
}

func scanSupervisorToolRounds(rows *sql.Rows, maxRounds int) ([]domain.SupervisorToolRound, error) {
	rounds := make([]domain.SupervisorToolRound, 0, maxRounds)
	for rows.Next() {
		round, call, err := scanSupervisorToolRoundCall(rows)
		if err != nil {
			return nil, err
		}
		if len(rounds) == 0 || !sameSupervisorToolRoundIdentity(rounds[len(rounds)-1], round) {
			rounds = append(rounds, round)
		}
		rounds[len(rounds)-1].Calls = append(rounds[len(rounds)-1].Calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if maxRounds <= 0 || len(rounds) > maxRounds {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"durable supervisor tool round limit was exceeded")
	}
	for index := range rounds {
		if err := rounds[index].Validate(); err != nil {
			return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
				"invalid durable supervisor tool round", err)
		}
	}
	return rounds, nil
}

func sameSupervisorToolRoundIdentity(left domain.SupervisorToolRound, right domain.SupervisorToolRound) bool {
	return left.RunID == right.RunID && left.Turn == right.Turn && left.AttemptID == right.AttemptID &&
		left.Round == right.Round && left.ModelAttempt == right.ModelAttempt
}

func insertSupervisorToolRoundTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, calls []llm.ToolCall,
	requestedAttribution *domain.AgentAttribution,
) error {
	return insertSupervisorToolRoundSourceTx(ctx, tx, run, checkpoint, attempt, calls, requestedAttribution, "run_supervisor")
}

func insertSupervisorToolRoundSourceTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, calls []llm.ToolCall,
	requestedAttribution *domain.AgentAttribution, source string,
) error {
	normalized, err := normalizeSupervisorToolCallsForStore(calls, checkpoint.RunID,
		checkpoint.NextTurn, attempt.ToolRound+1)
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return nil
	}
	var previous int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(round), 0) FROM run_supervisor_tool_rounds
		WHERE run_id = ? AND turn = ? AND attempt_id = ?`, checkpoint.RunID, checkpoint.NextTurn,
		checkpoint.AttemptID).Scan(&previous); err != nil {
		return err
	}
	round := previous + 1
	if round > domain.MaxSupervisorExecutionRounds || attempt.ToolRound != previous {
		return apperror.New(apperror.CodeResourceExhausted,
			"supervisor structured tool round limit was exhausted")
	}
	if source == "run_supervisor" {
		native, err := supervisorNativeToolRoundCountTx(ctx, tx, checkpoint, false)
		if err != nil {
			return err
		}
		if native >= domain.MaxSupervisorToolRounds {
			return apperror.New(apperror.CodeResourceExhausted, "supervisor native model tool round limit was exhausted")
		}
	}
	now := time.Now().UTC()
	attribution, err := inferredSupervisorRootAttributionTx(ctx, tx, run.ID,
		checkpoint.AttemptID)
	if err != nil {
		return err
	}
	if requestedAttribution != nil {
		attribution = *requestedAttribution
		if err := requireRecordedAgentAttributionTx(ctx, tx, run.ID,
			attribution); err != nil {
			return err
		}
	}
	roundSQL := `INSERT INTO run_supervisor_tool_rounds
		(run_id, turn, attempt_id, round, model_attempt, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)`
	if source == "project_hook" {
		roundSQL = `INSERT INTO run_supervisor_tool_rounds
		(run_id, turn, attempt_id, round, model_attempt, created_at, completed_at, origin)
		VALUES (?, ?, ?, ?, ?, ?, NULL, 'project_hook')`
	}
	if _, err := tx.ExecContext(ctx, roundSQL, checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID,
		round, attempt.Number, ts(now)); err != nil {
		return err
	}
	names := make([]string, 0, len(normalized))
	for index, call := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_supervisor_tool_calls
			(run_id, turn, attempt_id, round, position, model_attempt, call_id,
			 stream_response_id, stream_item_id, stream_call_id, tool_name, payload_json,
			 authority_json, status, result_json, error_code, created_at, completed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, NULL)`,
			checkpoint.RunID, checkpoint.NextTurn, checkpoint.AttemptID, round, index+1, attempt.Number,
			call.ID, call.StreamResponseID, call.StreamItemID, call.StreamCallID,
			call.Name, string(call.Arguments), string(call.Authority),
			domain.SupervisorToolPending, ts(now)); err != nil {
			return err
		}
		storedCall := domain.SupervisorToolCall{RunID: checkpoint.RunID,
			Turn: checkpoint.NextTurn, AttemptID: checkpoint.AttemptID,
			Round: round, Position: index + 1, ModelAttempt: attempt.Number,
			CallID: call.ID, CreatedAt: now}
		if err := insertSupervisorToolAgentAttributionTx(ctx, tx, storedCall,
			attribution); err != nil {
			return err
		}
		names = append(names, call.Name)
	}
	return appendSupervisorEventTx(ctx, tx, run, events.SupervisorToolBatchEvent, source,
		supervisorToolRoundSubject(checkpoint, round), map[string]any{
			"turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID, "round": round,
			"model_attempt": attempt.Number, "tool_count": len(normalized), "tools": names,
			"stream_response_id": normalized[0].StreamResponseID,
			"stream_item_ids":    supervisorToolStreamItemIDs(normalized),
			"stream_call_ids":    supervisorToolStreamCallIDs(normalized),
			"durable_call_ids":   supervisorToolDurableCallIDs(normalized),
		})
}

func normalizeSupervisorToolCallsForStore(calls []llm.ToolCall, runID string, turn int,
	round int,
) ([]llm.ToolCall, error) {
	if len(calls) == 0 || len(calls) > domain.MaxSupervisorToolCallsPerRound {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("supervisor tool batch must contain 1 to %d calls", domain.MaxSupervisorToolCallsPerRound))
	}
	normalized, err := llm.NormalizeToolCalls(calls)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid supervisor tool batch", err)
	}
	for index := range normalized {
		name := toolgateway.ToolName(normalized[index].Name)
		safe, err := toolgateway.NormalizeSupervisorToolPayload(name, normalized[index].Arguments)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInvalidArgument,
				"invalid supervisor structured tool payload", err)
		}
		if len(safe) > domain.MaxSupervisorToolPayloadBytes {
			return nil, apperror.New(apperror.CodeResourceExhausted,
				"supervisor tool payload exceeds its durable limit")
		}
		normalized[index].Arguments = append(json.RawMessage(nil), safe...)
		if toolgateway.IsAgentCodeTool(name) || toolgateway.IsCodeIntelTool(name) {
			authority, authorityErr := toolgateway.DecodeAgentCodeCallAuthority(
				normalized[index].Authority)
			if authorityErr != nil || authority.RunID != runID {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"agent code supervisor tool is missing its exact durable authority")
			}
			canonicalAuthority, authorityErr := toolgateway.EncodeAgentCodeCallAuthority(authority)
			if authorityErr != nil || len(canonicalAuthority) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"agent code supervisor tool authority is invalid")
			}
			normalized[index].Authority = canonicalAuthority
		} else if toolgateway.IsCommandExecutionTool(name) {
			authority, authorityErr := commandruntimeadapter.DecodeAuthority(
				normalized[index].Authority)
			if authorityErr != nil || authority.RunID != runID || (name == toolgateway.RunTestsTool) != (len(authority.ProjectTestCommand) != 0) {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"command runtime supervisor tool is missing its exact durable adapter authority")
			}
			canonicalAuthority, authorityErr := commandruntimeadapter.EncodeAuthority(authority)
			if authorityErr != nil || len(canonicalAuthority) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"command runtime supervisor tool authority is invalid")
			}
			normalized[index].Authority = canonicalAuthority
		} else if name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool {
			authority, authorityErr := mcp.DecodeSupervisorCallAuthority(
				normalized[index].Authority)
			validKind := name == toolgateway.MCPToolSearchTool && authority.Version == mcp.SupervisorCatalogAuthorityVersion ||
				name == toolgateway.MCPToolCallTool && authority.Version != mcp.SupervisorCatalogAuthorityVersion
			if authorityErr != nil || !validKind || authority.RunID != runID {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"MCP supervisor tool is missing its exact durable authority")
			}
			canonicalAuthority, authorityErr := mcp.EncodeSupervisorCallAuthority(authority)
			if authorityErr != nil || len(canonicalAuthority) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"MCP supervisor tool authority is invalid")
			}
			normalized[index].Authority = canonicalAuthority
		} else if toolgateway.IsWebEvidenceTool(name) {
			authority, authorityErr := toolgateway.DecodeWebEvidenceCallAuthority(
				normalized[index].Authority)
			if authorityErr != nil || authority.RunID != runID {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"web evidence supervisor tool is missing its exact durable authority")
			}
			canonicalAuthority, authorityErr := toolgateway.EncodeWebEvidenceCallAuthority(authority)
			if authorityErr != nil || len(canonicalAuthority) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"web evidence supervisor tool authority is invalid")
			}
			normalized[index].Authority = canonicalAuthority
		} else if toolgateway.IsBrowserActionTool(name) && toolgateway.IsAgentBrowserPayload(safe) {
			a, e := toolgateway.DecodeAgentBrowserAuthority(normalized[index].Authority)
			if e != nil || a.RunID != runID {
				return nil, apperror.New(apperror.CodeInvalidArgument, "Agent browser tool needs exact durable v2 authority")
			}
			b, e := json.Marshal(a)
			if e != nil || len(b) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument, "Agent browser authority invalid")
			}
			normalized[index].Authority = b
		} else if toolgateway.IsBrowserActionTool(name) {
			authority, authorityErr := toolgateway.DecodeBrowserActionCallAuthority(
				normalized[index].Authority)
			if authorityErr != nil || authority.RunID != runID {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"browser action supervisor tool is missing its exact durable authority")
			}
			canonicalAuthority, authorityErr := toolgateway.EncodeBrowserActionCallAuthority(authority)
			if authorityErr != nil || len(canonicalAuthority) > domain.MaxSupervisorToolAuthorityBytes {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					"browser action supervisor tool authority is invalid")
			}
			normalized[index].Authority = canonicalAuthority
		} else if len(normalized[index].Authority) != 0 {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"non-authority-bound supervisor tool cannot carry authority")
		}
		operationKey := runmutation.SupervisorToolOperationKey(runID, turn, normalized[index].Name, string(safe))
		expectedID, err := runmutation.SupervisorToolCallID(operationKey, round)
		if err != nil || normalized[index].ID != expectedID {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"supervisor tool call id does not match its normalized intent")
		}
	}
	return normalized, nil
}

func supervisorToolStreamItemIDs(calls []llm.ToolCall) []string {
	items := make([]string, len(calls))
	for index := range calls {
		items[index] = calls[index].StreamItemID
	}
	return items
}

func supervisorToolStreamCallIDs(calls []llm.ToolCall) []string {
	items := make([]string, len(calls))
	for index := range calls {
		items[index] = calls[index].StreamCallID
	}
	return items
}

func supervisorToolDurableCallIDs(calls []llm.ToolCall) []string {
	items := make([]string, len(calls))
	for index := range calls {
		items[index] = calls[index].ID
	}
	return items
}

func (s *SQLiteStore) RecordSupervisorToolExecutionStarted(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, callID string,
) (bool, error) {
	fresh, _, err := s.recordSupervisorToolExecutionStarted(ctx, checkpoint, callID, false, true)
	return fresh, err
}

// RecordSupervisorToolExecutionStartedWithSteering serializes the execution
// start against correction admission. A superseded call receives a terminal
// not-dispatched result without an execution-start or execution-completed event.
func (s *SQLiteStore) RecordSupervisorToolExecutionStartedWithSteering(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, callID string,
) (bool, bool, error) {
	return s.recordSupervisorToolExecutionStarted(ctx, checkpoint, callID, true, true)
}

func (s *SQLiteStore) SupersedeSupervisorToolIfSteeringChanged(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, callID string,
) (bool, error) {
	_, superseded, err := s.recordSupervisorToolExecutionStarted(ctx, checkpoint, callID, true, false)
	return superseded, err
}

func (s *SQLiteStore) recordSupervisorToolExecutionStarted(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, callID string, steeringFence bool, start bool,
) (bool, bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return false, false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return false, false, apperror.New(apperror.CodeFailedPrecondition,
			"only a started supervisor turn can execute a tool")
	}
	callID = strings.TrimSpace(callID)
	if callID == "" || len([]rune(callID)) > domain.MaxSupervisorToolIdentityRunes {
		return false, false, apperror.New(apperror.CodeInvalidArgument, "supervisor tool call id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return false, false, err
	}
	if err := lockRunningRunForSteeringTx(ctx, tx, run.ID); err != nil {
		return false, false, err
	}
	call, err := getSupervisorToolCallTx(ctx, tx, current, callID)
	if err != nil {
		return false, false, err
	}
	exists, err := supervisorModelEventExistsTx(ctx, tx, run.ID,
		events.SupervisorToolExecutionStartedEvent, call.CallID)
	if err != nil {
		return false, false, err
	}
	if exists {
		if err := tx.Commit(); err != nil {
			return false, false, err
		}
		return false, false, nil
	}
	if call.Status != domain.SupervisorToolPending {
		return false, false, apperror.New(apperror.CodeConflict,
			"only a pending supervisor tool can begin execution")
	}
	if steeringFence {
		var payloadJSON string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events
			WHERE run_id=? AND type=? AND source='model_gateway' AND subject_id=?`,
			run.ID, events.ModelStartedEvent,
			supervisorModelSubject(current, call.ModelAttempt)).Scan(&payloadJSON); err != nil {
			return false, false, err
		}
		payload, err := parseSupervisorModelStartedPayload(payloadJSON)
		if err != nil {
			return false, false, err
		}
		sequence, err := midturnSteeringSequenceTx(ctx, tx, run.ID, current.AttemptID)
		if err != nil {
			return false, false, err
		}
		if payload.SteeringSequence != sequence {
			result := domain.SupervisorToolResult{CallID: call.CallID,
				Status: domain.SupervisorToolDenied, ErrorCode: "steering_superseded",
				ResultJSON:  fmt.Sprintf(`{"version":"supervisor_tool_result.v1","tool":%q,"status":"denied","outcome":"not_dispatched","reason":"steering_superseded"}`, call.ToolName),
				CompletedAt: time.Now().UTC()}
			if _, err := recordSupervisorToolResultTx(ctx, tx, run, current, call, result, false); err != nil {
				return false, false, err
			}
			if err := tx.Commit(); err != nil {
				return false, false, err
			}
			return false, true, nil
		}
	}
	if !start {
		if err := tx.Commit(); err != nil {
			return false, false, err
		}
		return false, false, nil
	}
	if err := appendSupervisorEventTx(ctx, tx, run,
		events.SupervisorToolExecutionStartedEvent, "run_supervisor", call.CallID,
		supervisorToolStreamEventPayload(call, domain.SupervisorToolPending,
			llm.StreamToolExecutionStarted)); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func supervisorToolStreamEventPayload(call domain.SupervisorToolCall,
	status domain.SupervisorToolCallStatus, eventType llm.StreamEventType,
) map[string]any {
	payload := map[string]any{
		"turn": call.Turn, "attempt_id": call.AttemptID, "round": call.Round,
		"position": call.Position, "tool": call.ToolName, "status": status,
		"stream_response_id": call.StreamResponseID, "stream_item_id": call.StreamItemID,
		"stream_call_id": call.StreamCallID, "durable_call_id": call.CallID,
	}
	if call.ProjectHook != nil {
		payload["project_hook"] = call.ProjectHook
		payload["source_kind"] = "project_hook"
	}
	if call.ProjectHook == nil && (eventType == llm.StreamToolExecutionStarted || eventType == llm.StreamToolExecutionCompleted) {
		itemStatus := llm.StreamItemInProgress
		if eventType == llm.StreamToolExecutionCompleted {
			itemStatus = llm.StreamItemCompleted
			if status == domain.SupervisorToolFailed {
				itemStatus = llm.StreamItemFailed
			}
		}
		payload["item_stream_version"] = llm.ItemStreamProtocolVersion
		payload["item_event_type"] = eventType
		payload["item_type"] = llm.StreamItemToolCall
		payload["item_status"] = itemStatus
		payload["provisional"] = false
		payload["durable"] = true
	}
	if eventType == "" && toolgateway.IsWebEvidenceTool(toolgateway.ToolName(call.ToolName)) {
		if presentation, ok := supervisorWebEvidenceEventPresentation(call); ok {
			payload["web_evidence"] = presentation
		}
	}
	return payload
}

func supervisorWebEvidenceEventPresentation(call domain.SupervisorToolCall) (map[string]any, bool) {
	if call.Status != domain.SupervisorToolCompleted || strings.TrimSpace(call.ResultJSON) == "" {
		return nil, false
	}
	if call.ToolName != string(toolgateway.WebFetchTool) &&
		call.ToolName != string(toolgateway.WebCitationTool) {
		return nil, false
	}
	var envelope struct {
		Version  string            `json:"version"`
		Tool     string            `json:"tool"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
	}
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil ||
		envelope.Version != "supervisor_tool_result.v1" || envelope.Tool != call.ToolName ||
		envelope.Status != string(domain.SupervisorToolCompleted) {
		return nil, false
	}
	metadata := envelope.Metadata
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(metadata["url"])
	if err != nil || canonical != metadata["url"] ||
		!validSupervisorWebEvidenceIdentity(metadata["source_id"], false) ||
		!validSupervisorWebEvidenceIdentity(metadata["snapshot_id"], false) ||
		!validSupervisorWebEvidenceIdentity(metadata["citation_id"],
			call.ToolName != string(toolgateway.WebCitationTool)) ||
		!validSupervisorWebEvidenceTitle(metadata["title"]) ||
		!validSupervisorWebEvidenceDigest(metadata["digest"]) {
		return nil, false
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, metadata["fetched_at"])
	if err != nil {
		return nil, false
	}
	staleAt, err := time.Parse(time.RFC3339Nano, metadata["stale_at"])
	if err != nil || staleAt.Before(fetchedAt) {
		return nil, false
	}
	partial, partialOK := supervisorWebEvidenceBool(metadata["partial"])
	stale, staleOK := supervisorWebEvidenceBool(metadata["stale"])
	citeable, citeableOK := supervisorWebEvidenceBool(metadata["citeable"])
	if !partialOK || !staleOK || !citeableOK {
		return nil, false
	}
	state := strings.TrimSpace(metadata["state"])
	switch state {
	case "fetched":
		if partial || stale || !citeable {
			return nil, false
		}
	case "partial":
		if !partial || stale || !citeable {
			return nil, false
		}
	case "stale":
		if !stale || !citeable {
			return nil, false
		}
	case "blocked", "failed":
		if partial || stale || citeable {
			return nil, false
		}
	default:
		return nil, false
	}
	presentation := map[string]any{
		"version": "web_evidence_presentation.v1", "url": canonical,
		"title": metadata["title"], "state": state,
		"source_id": metadata["source_id"], "snapshot_id": metadata["snapshot_id"],
		"citation_id": metadata["citation_id"], "fetched_at": metadata["fetched_at"],
		"stale_at": metadata["stale_at"], "digest": metadata["digest"],
		"partial": partial, "stale": stale, "citeable": citeable, "untrusted": true,
		"instruction_authorized": false,
	}
	robots := strings.TrimSpace(metadata["robots"])
	if robots != "" {
		switch robots {
		case "allowed", "blocked", "unknown", "not_checked", "not_present",
			"bypassed_disallow", "bypassed_unknown":
			presentation["robots"] = robots
		default:
			return nil, false
		}
	}
	return presentation, true
}

func validSupervisorWebEvidenceIdentity(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 256 {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validSupervisorWebEvidenceTitle(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1024 {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validSupervisorWebEvidenceDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func supervisorWebEvidenceBool(value string) (bool, bool) {
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (s *SQLiteStore) RecordSupervisorToolResult(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	result domain.SupervisorToolResult,
) (domain.SupervisorToolCall, bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return domain.SupervisorToolCall{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"only a started supervisor turn can record a tool result")
	}
	if err := result.Validate(); err != nil {
		return domain.SupervisorToolCall{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"invalid supervisor tool result", err)
	}
	originalResultJSON := result.ResultJSON
	redactResult := redactJSONPayload
	if supervisorWebEvidenceResultEnvelope(result.ResultJSON) {
		redactResult = redactJSONPayloadWithoutHTMLEscape
	}
	safeResult, err := redactResult(result.ResultJSON)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	result.ResultJSON = safeResult
	result.ErrorCode = strings.TrimSpace(result.ErrorCode)
	if err := result.Validate(); err != nil {
		return domain.SupervisorToolCall{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"redacted supervisor tool result is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	call, err := getSupervisorToolCallTx(ctx, tx, current, result.CallID)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	if result.Status == domain.SupervisorToolCompleted &&
		(call.ToolName == "history_search" || call.ToolName == "history_read") {
		result.ResultJSON, err = s.preserveSupervisorHistoryResultTx(ctx, tx, call, originalResultJSON, result.ResultJSON)
		if err != nil {
			return domain.SupervisorToolCall{}, false, err
		}
		if err := result.Validate(); err != nil {
			return domain.SupervisorToolCall{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
				"verified history tool result is invalid", err)
		}
	}
	if call.Status.Terminal() {
		if call.Status != result.Status || call.ResultJSON != result.ResultJSON || call.ErrorCode != result.ErrorCode {
			return domain.SupervisorToolCall{}, false, apperror.New(apperror.CodeConflict,
				"supervisor tool result replay does not match its durable value")
		}
		if err := tx.Commit(); err != nil {
			return domain.SupervisorToolCall{}, false, err
		}
		return call, true, nil
	}
	executionStarted, err := supervisorModelEventExistsTx(ctx, tx, run.ID,
		events.SupervisorToolExecutionStartedEvent, call.CallID)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	if !executionStarted {
		preflight, preflightErr := validateAgentBrowserPreflightResultTx(ctx, tx, call, result)
		if !preflight && preflightErr == nil {
			preflight, preflightErr = validatePendingOperationPreflightResultTx(ctx, tx, call, result)
		}
		if preflightErr != nil {
			return domain.SupervisorToolCall{}, false, preflightErr
		}
		if !preflight {
			return domain.SupervisorToolCall{}, false, apperror.New(apperror.CodeFailedPrecondition, "supervisor tool result requires a durable execution start")
		}
	}
	call, err = recordSupervisorToolResultTx(ctx, tx, run, current, call, result, executionStarted)
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	return call, false, nil
}

func recordSupervisorToolResultTx(ctx context.Context, tx *sql.Tx, run domain.Run, current domain.SupervisorCheckpoint,
	call domain.SupervisorToolCall, result domain.SupervisorToolResult, dispatched bool,
) (domain.SupervisorToolCall, error) {
	completedAt := result.CompletedAt.UTC()
	update, err := tx.ExecContext(ctx, `UPDATE run_supervisor_tool_calls
		SET status = ?, result_json = ?, error_code = ?, completed_at = ?
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND call_id = ? AND status = ?`,
		result.Status, result.ResultJSON, result.ErrorCode, ts(completedAt), current.RunID, current.NextTurn,
		current.AttemptID, result.CallID, domain.SupervisorToolPending)
	if err != nil {
		return domain.SupervisorToolCall{}, err
	}
	rows, err := update.RowsAffected()
	if err != nil {
		return domain.SupervisorToolCall{}, err
	}
	if rows != 1 {
		return domain.SupervisorToolCall{}, apperror.New(apperror.CodeConflict,
			"supervisor tool call changed before its result was recorded")
	}
	call.Status = result.Status
	call.ResultJSON = result.ResultJSON
	call.ErrorCode = result.ErrorCode
	call.CompletedAt = &completedAt
	if dispatched {
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.SupervisorToolExecutionCompletedEvent, "run_supervisor", call.CallID,
			supervisorToolStreamEventPayload(call, call.Status,
				llm.StreamToolExecutionCompleted)); err != nil {
			return domain.SupervisorToolCall{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorToolResultEvent, "run_supervisor",
		call.CallID, func() map[string]any {
			payload := supervisorToolStreamEventPayload(call, call.Status, "")
			payload["error_code"] = call.ErrorCode
			if !dispatched {
				payload["outcome"] = "not_dispatched"
				payload["reason"] = result.ErrorCode
			}
			return payload
		}()); err != nil {
		return domain.SupervisorToolCall{}, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_tool_calls
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND round = ? AND status = ?`,
		call.RunID, call.Turn, call.AttemptID, call.Round, domain.SupervisorToolPending).Scan(&pending); err != nil {
		return domain.SupervisorToolCall{}, err
	}
	if pending == 0 {
		updated, err := tx.ExecContext(ctx, `UPDATE run_supervisor_tool_rounds SET completed_at = ?
			WHERE run_id = ? AND turn = ? AND attempt_id = ? AND round = ? AND completed_at IS NULL`,
			ts(completedAt), call.RunID, call.Turn, call.AttemptID, call.Round)
		if err != nil {
			return domain.SupervisorToolCall{}, err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return domain.SupervisorToolCall{}, err
		}
		if changed == 1 {
			if err := appendSupervisorEventTx(ctx, tx, run, events.SupervisorToolCompleteEvent,
				"run_supervisor", supervisorToolRoundSubject(current, call.Round), map[string]any{
					"turn": call.Turn, "attempt_id": call.AttemptID, "round": call.Round,
				}); err != nil {
				return domain.SupervisorToolCall{}, err
			}
		}
	}
	return call, nil
}

func supervisorWebEvidenceResultEnvelope(raw string) bool {
	var envelope struct {
		Version string `json:"version"`
		Tool    string `json:"tool"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil ||
		envelope.Version != "supervisor_tool_result.v1" {
		return false
	}
	return toolgateway.IsWebEvidenceTool(toolgateway.ToolName(envelope.Tool))
}

func getSupervisorToolCallTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint,
	callID string,
) (domain.SupervisorToolCall, error) {
	call, err := scanSupervisorToolCall(tx.QueryRowContext(ctx, `SELECT run_id, turn, attempt_id, round, position,
		model_attempt, call_id, stream_response_id, stream_item_id, stream_call_id,
		tool_name, payload_json, authority_json, status, result_json, error_code, created_at, completed_at
		FROM run_supervisor_tool_calls
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND call_id = ?`, checkpoint.RunID,
		checkpoint.NextTurn, checkpoint.AttemptID, strings.TrimSpace(callID)))
	if err != nil {
		return call, err
	}
	rounds := []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}
	if err := enrichProjectHookRounds(ctx, tx, rounds); err != nil {
		return call, err
	}
	return rounds[0].Calls[0], nil
}

func scanSupervisorToolRoundCall(row scanner) (domain.SupervisorToolRound, domain.SupervisorToolCall, error) {
	var round domain.SupervisorToolRound
	var roundCreatedAt string
	var roundCompletedAt sql.NullString
	var call domain.SupervisorToolCall
	var callStatus string
	var callCreatedAt string
	var callCompletedAt sql.NullString
	if err := row.Scan(&round.RunID, &round.Turn, &round.AttemptID, &round.Round, &round.ModelAttempt,
		&roundCreatedAt, &roundCompletedAt, &call.RunID, &call.Turn, &call.AttemptID, &call.Round,
		&call.Position, &call.ModelAttempt, &call.CallID, &call.StreamResponseID,
		&call.StreamItemID, &call.StreamCallID, &call.ToolName, &call.PayloadJSON,
		&call.AuthorityJSON, &callStatus,
		&call.ResultJSON, &call.ErrorCode, &callCreatedAt, &callCompletedAt); err != nil {
		return domain.SupervisorToolRound{}, domain.SupervisorToolCall{}, err
	}
	round.CreatedAt = parseTS(roundCreatedAt)
	if roundCompletedAt.Valid {
		value := parseTS(roundCompletedAt.String)
		round.CompletedAt = &value
	}
	call.Status = domain.SupervisorToolCallStatus(callStatus)
	call.CreatedAt = parseTS(callCreatedAt)
	if callCompletedAt.Valid {
		value := parseTS(callCompletedAt.String)
		call.CompletedAt = &value
	}
	if err := call.Validate(); err != nil {
		return domain.SupervisorToolRound{}, domain.SupervisorToolCall{},
			apperror.Wrap(apperror.CodeFailedPrecondition, "invalid durable supervisor tool call", err)
	}
	return round, call, nil
}

func scanSupervisorToolCall(row scanner) (domain.SupervisorToolCall, error) {
	var call domain.SupervisorToolCall
	var status string
	var createdAt string
	var completedAt sql.NullString
	if err := row.Scan(&call.RunID, &call.Turn, &call.AttemptID, &call.Round, &call.Position,
		&call.ModelAttempt, &call.CallID, &call.StreamResponseID, &call.StreamItemID,
		&call.StreamCallID, &call.ToolName, &call.PayloadJSON, &call.AuthorityJSON,
		&status, &call.ResultJSON,
		&call.ErrorCode, &createdAt, &completedAt); err != nil {
		return domain.SupervisorToolCall{}, err
	}
	call.Status = domain.SupervisorToolCallStatus(status)
	call.CreatedAt = parseTS(createdAt)
	if completedAt.Valid {
		value := parseTS(completedAt.String)
		call.CompletedAt = &value
	}
	if err := call.Validate(); err != nil {
		return domain.SupervisorToolCall{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"invalid durable supervisor tool call", err)
	}
	return call, nil
}

func requireSupervisorToolsReadyTx(ctx context.Context, tx *sql.Tx,
	checkpoint domain.SupervisorCheckpoint,
) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_supervisor_tool_calls
		WHERE run_id = ? AND turn = ? AND attempt_id = ? AND status = ?`, checkpoint.RunID,
		checkpoint.NextTurn, checkpoint.AttemptID, domain.SupervisorToolPending).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"supervisor turn has unresolved structured tool calls")
	}
	var payloadJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM run_events
		WHERE run_id = ? AND type = ? AND source = ? AND subject_id LIKE ?
		ORDER BY sequence DESC LIMIT 1`, checkpoint.RunID, events.ModelCompletedEvent, "model_gateway",
		supervisorModelSubjectPrefix(checkpoint)+"%").Scan(&payloadJSON); err != nil {
		return err
	}
	var payload struct {
		ModelAttempt int `json:"model_attempt"`
		ToolCount    int `json:"tool_call_count"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"invalid durable completed model payload", err)
	}
	if payload.ModelAttempt <= 0 || payload.ToolCount != 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"latest completed model response is not a root lifecycle action")
	}
	return nil
}

func supervisorToolRoundSubject(checkpoint domain.SupervisorCheckpoint, round int) string {
	return fmt.Sprintf("%s/tool/%d", checkpoint.AttemptID, round)
}
