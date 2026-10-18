package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

type workerHistoryScope struct {
	ref    domain.AgentAttemptRef
	child  domain.AgentNode
	brief  domain.SpecialistTaskBrief
	parent []workerHistoryParentSource
}

type workerHistoryRef struct {
	Run   string `json:"r"`
	Agent string `json:"a"`
	Kind  string `json:"k"`
	ID    string `json:"i"`
	SHA   string `json:"s"`
}

type workerHistoryCursor struct {
	Ref        domain.AgentAttemptRef `json:"ref"`
	Brief      string                 `json:"brief"`
	QuerySHA   string                 `json:"q"`
	MaxMessage int64                  `json:"m"`
	MaxTool    int64                  `json:"t"`
	MaxEvent   int64                  `json:"e"`
	Offset     int                    `json:"o"`
}

func (s *SQLiteStore) workerHistoryScopeTx(ctx context.Context, tx *sql.Tx, ref domain.AgentAttemptRef, name string) (workerHistoryScope, error) {
	value := workerHistoryScope{ref: ref}
	attempt, child, _, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return value, err
	}
	if _, err := validateSpecialistWorkerBindingTx(ctx, tx, ref, name); err != nil {
		return value, err
	}
	brief, found, err := loadSpecialistTaskBriefTx(ctx, tx, attempt)
	if err != nil || !found {
		if err == nil {
			err = errors.New("worker history requires its prepared task brief")
		}
		return value, err
	}
	value.child, value.brief = child, brief
	value.parent, err = loadWorkerParentHistoryTx(ctx, tx, child)
	return value, err
}

func workerHistoryPartsSHA(parts map[string]string) string {
	digests := make(map[string]string, len(parts))
	for part, content := range parts {
		digests[part] = session.ContentSHA256(content)
	}
	raw, _ := json.Marshal(digests)
	return session.ContentSHA256(string(raw))
}

func workerHistorySourceID(scope workerHistoryScope, kind, id string, parts map[string]string) string {
	return "worker-history:" + encodeHistoryToken(workerHistoryRef{Run: scope.ref.RunID, Agent: scope.ref.AgentID,
		Kind: kind, ID: id, SHA: workerHistoryPartsSHA(parts)})
}

func validateWorkerHistoryToolAuthorityTx(ctx context.Context, tx *sql.Tx, scope workerHistoryScope, call domain.SpecialistToolCall) error {
	var authority struct {
		Version      string                       `json:"version"`
		Ref          domain.AgentAttemptRef       `json:"ref"`
		ParentID     string                       `json:"parent_id"`
		ModeID       string                       `json:"mode_id"`
		ModeRevision int64                        `json:"mode_revision"`
		Binding      domain.SpecialistToolBinding `json:"binding"`
	}
	if err := json.Unmarshal([]byte(call.AuthorityJSON), &authority); err != nil {
		return err
	}
	if authority.Version != "worker_skill_read.v1" && authority.Version != "worker-batch-authority.v1" {
		return validateSpecialistApprovalPermissionTx(ctx, tx, scope.ref.RunID, call.AuthorityJSON)
	}
	// These two host authorities bind guidance or an isolated checkout, not an
	// execution-permission snapshot. Match their original child binding before
	// returning historical evidence; this does not reactivate either authority.
	binding, err := validateSpecialistWorkerBindingTx(ctx, tx, call.Ref, call.Name)
	if err != nil {
		return err
	}
	if binding.SessionID != scope.child.SessionID {
		return apperror.New(apperror.CodePolicyDenied, "worker receipt belongs to another child Session")
	}
	if authority.Version == "worker_skill_read.v1" {
		if call.Name != "skill_read" || authority.Ref != call.Ref || authority.ParentID != scope.child.ParentID ||
			authority.ModeID == "" || authority.ModeRevision < 1 {
			return apperror.New(apperror.CodePolicyDenied, "worker Skill receipt authority differs from its recorded child")
		}
		mode, err := getRunModeSnapshot(ctx, tx, authority.ModeID)
		if err != nil {
			return err
		}
		if mode.RunID != call.Ref.RunID || mode.Revision != authority.ModeRevision {
			return apperror.New(apperror.CodePolicyDenied, "worker Skill receipt mode belongs to another scope")
		}
		return nil
	}
	switch call.Name {
	case "workspace_list", "workspace_read", "workspace_glob", "workspace_grep", "workspace_change", "workspace_apply", "git_status", "git_diff", "git_commit", "verify":
	default:
		return apperror.New(apperror.CodePolicyDenied, "worker Batch receipt authority does not match its tool")
	}
	if authority.Binding != binding || binding.BatchPlanID == "" || binding.BatchOrdinal < 1 || binding.BatchGeneration < 1 {
		return apperror.New(apperror.CodePolicyDenied, "worker Batch receipt authority differs from its recorded checkout")
	}
	return nil
}

func (s *SQLiteStore) readWorkerHistorySource(ctx context.Context, tx *sql.Tx, scope workerHistoryScope, kind, id string) (domain.HistoryRecord, map[string]string, error) {
	var record domain.HistoryRecord
	var parts map[string]string
	var err error
	switch kind {
	case "message":
		messageID, parseErr := strconv.ParseInt(id, 10, 64)
		if parseErr != nil || messageID < 1 {
			return record, nil, historyNotFound()
		}
		message, readErr := scanSessionMessage(tx.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at
 FROM session_messages WHERE id=? AND session_id=?`, messageID, scope.child.SessionID))
		if readErr != nil {
			return record, nil, readErr
		}
		record = domain.HistoryRecord{Kind: "message", RunID: scope.ref.RunID, SessionID: scope.child.SessionID,
			MessageID: message.ID, Role: message.Role, SourceKind: message.Provenance.SourceKind,
			SourceRef: message.Provenance.SourceRef, ProvenanceVersion: message.Provenance.Version,
			ContentSHA256: session.ContentSHA256(message.Content), Compacted: message.Compacted, CreatedAt: ts(message.CreatedAt)}
		parts = map[string]string{"content": message.Content}
	case "tool":
		rowID, parseErr := strconv.ParseInt(id, 10, 64)
		if parseErr != nil || rowID < 1 {
			return record, nil, historyNotFound()
		}
		call, readErr := scanSpecialistToolCall(tx.QueryRowContext(ctx, specialistToolCallSelect+`
 WHERE c.rowid=? AND a.run_id=? AND a.agent_id=? AND c.status IN ('completed','failed','denied')
 AND c.completed_at IS NOT NULL AND c.name NOT IN ('history_search','history_read','specialist_result_read')`, rowID, scope.ref.RunID, scope.ref.AgentID))
		if readErr != nil {
			return record, nil, readErr
		}
		if !scope.child.Worker.AllowsTool(call.Name) {
			return record, nil, historyNotFound()
		}
		if call.AuthorityJSON != "" {
			if err := validateWorkerHistoryToolAuthorityTx(ctx, tx, scope, call); err != nil {
				return record, nil, err
			}
		}
		record = domain.HistoryRecord{Kind: "tool_call", RunID: scope.ref.RunID, SessionID: scope.child.SessionID,
			SourceKind: session.SourceToolResult, SourceRef: call.CallID, CallID: call.CallID,
			AttemptID: call.Ref.AttemptID, Round: call.Round, ToolName: call.Name, Status: string(call.Status),
			ErrorCode: call.ErrorCode, ArgumentsSHA256: session.ContentSHA256(call.PayloadJSON),
			ResultSHA256: session.ContentSHA256(call.ResultJSON), CreatedAt: ts(call.CreatedAt), CompletedAt: ts(*call.CompletedAt)}
		parts = map[string]string{"arguments": call.PayloadJSON, "result": call.ResultJSON}
	case "instruction":
		found := false
		for _, instruction := range scope.brief.Instructions {
			if instruction.SourceID == id {
				found = true
				break
			}
		}
		if !found {
			return record, nil, historyNotFound()
		}
		message, readErr := scanAgentMessage(tx.QueryRowContext(ctx, agentMessageSelect+` WHERE id=? AND run_id=? AND sender_agent_id=? AND recipient_agent_id=?`, id, scope.ref.RunID, scope.child.ParentID, scope.child.ID))
		if readErr != nil {
			return record, nil, readErr
		}
		// The current authenticated brief was revalidated against all retained
		// sources above; later undelivered parent messages cannot enter here.
		payload, err := domain.DecodeAgentInstructionPayload(message.PayloadJSON)
		if err != nil {
			return record, nil, err
		}
		content := domain.SpecialistTaskInstructionProjection(payload.Instruction)
		record = domain.HistoryRecord{Kind: "parent_instruction", RunID: scope.ref.RunID, SessionID: scope.child.SessionID,
			SourceKind: "delegated_parent_instruction", SourceRef: id, ContentSHA256: session.ContentSHA256(content), CreatedAt: ts(message.CreatedAt)}
		parts = map[string]string{"content": content}
	case "brief":
		if id != scope.brief.Fingerprint {
			return record, nil, historyNotFound()
		}
		// Match the delivered task projection; WorkItem relations and retired
		// source payloads are not made visible merely by appearing in a digest.
		projection := struct {
			Version      string                              `json:"version"`
			Fingerprint  string                              `json:"fingerprint"`
			Instructions []domain.SpecialistBriefInstruction `json:"instructions"`
			Work         []domain.SpecialistTaskWorkContext  `json:"work_items"`
		}{Version: scope.brief.Version, Fingerprint: scope.brief.Fingerprint,
			Instructions: append([]domain.SpecialistBriefInstruction{}, scope.brief.Instructions...),
			Work:         []domain.SpecialistTaskWorkContext{}}
		for _, item := range scope.brief.WorkItems {
			projection.Work = append(projection.Work, domain.SpecialistTaskWorkProjection(item))
		}
		raw, marshalErr := domain.MarshalSpecialistDeliveryContext(projection)
		if marshalErr != nil {
			return record, nil, marshalErr
		}
		record = domain.HistoryRecord{Kind: "task_brief", RunID: scope.ref.RunID, SessionID: scope.child.SessionID,
			AttemptID: scope.ref.AttemptID, SourceKind: domain.SpecialistTaskBriefVersion,
			SourceRef: scope.brief.Fingerprint, ContentSHA256: session.ContentSHA256(string(raw)), CreatedAt: ts(scope.child.CreatedAt)}
		parts = map[string]string{"content": string(raw)}
	case "parent":
		ordinal, parseErr := strconv.Atoi(id)
		if parseErr != nil || ordinal < 1 || ordinal > len(scope.parent) {
			return record, nil, historyNotFound()
		}
		record, parts, err = s.readWorkerParentHistorySource(ctx, tx, scope.child, scope.parent[ordinal-1])
		if err != nil {
			return record, nil, err
		}
	default:
		return record, nil, historyNotFound()
	}
	record.SourceID = workerHistorySourceID(scope, kind, id, parts)
	record.OriginalInstructionAuthorized = false
	// Parent readers already project provenance metadata. Applying redaction a
	// second time would replace the original reference digest with a digest of
	// its display text and erase source_ref_redacted.
	if kind != "parent" {
		record, err = safeHistoryMetadata(record)
	}
	return record, parts, err
}

func (s *SQLiteStore) SearchWorkerHistory(ctx context.Context, ref domain.AgentAttemptRef, request domain.HistorySearchRequest) (domain.HistorySearchResult, error) {
	value := domain.HistorySearchResult{Version: domain.HistoryRecallVersion, RunID: ref.RunID,
		SearchMode: "bounded_literal_substring", ScanLimit: domain.HistorySearchScanLimit, Records: []domain.HistoryRecord{}}
	if !utf8.ValidString(request.Query) || utf8.RuneCountInString(request.Query) > 256 || strings.ContainsRune(request.Query, 0) || request.Limit < 0 || request.Limit > domain.HistorySearchMaxResults {
		return value, apperror.New(apperror.CodeInvalidArgument, "worker history requires a bounded literal query and page")
	}
	if request.Limit == 0 {
		request.Limit = domain.HistorySearchMaxResults
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return value, err
	}
	defer tx.Rollback()
	scope, err := s.workerHistoryScopeTx(ctx, tx, ref, "history_search")
	if err != nil {
		return value, err
	}
	cursor := workerHistoryCursor{Ref: ref, Brief: scope.brief.Fingerprint, QuerySHA: session.ContentSHA256(request.Query)}
	if request.Cursor != "" {
		if len(request.Cursor) > 2048 || decodeHistoryToken(request.Cursor, &cursor) != nil || cursor.Ref != ref ||
			cursor.Brief != scope.brief.Fingerprint || cursor.QuerySHA != session.ContentSHA256(request.Query) ||
			cursor.Offset < 0 || cursor.Offset > 1000000 || cursor.MaxMessage < 0 || cursor.MaxTool < 0 || cursor.MaxEvent < 0 {
			return value, apperror.New(apperror.CodeInvalidArgument, "worker history cursor belongs to a different attempt, brief or query")
		}
	} else if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COALESCE(MAX(id),0) FROM session_messages WHERE session_id=?),
 (SELECT COALESCE(MAX(c.rowid),0) FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id WHERE a.run_id=? AND a.agent_id=?),
 (SELECT COALESCE(MAX(id),0) FROM run_events WHERE run_id=?)`, scope.child.SessionID, ref.RunID, ref.AgentID, ref.RunID).
		Scan(&cursor.MaxMessage, &cursor.MaxTool, &cursor.MaxEvent); err != nil {
		return value, err
	}
	value.SnapshotCursor = encodeHistoryToken(cursor)
	instructions := []string{}
	for _, instruction := range scope.brief.Instructions {
		instructions = append(instructions, instruction.SourceID)
	}
	parentIDs := make([]int, len(scope.parent))
	for i := range parentIDs {
		parentIDs[i] = i + 1
	}
	messageJSON, _ := json.Marshal(instructions)
	parentJSON, _ := json.Marshal(parentIDs)
	rows, err := tx.QueryContext(ctx, `SELECT kind,id FROM (
 SELECT 'message' kind,CAST(id AS TEXT) id FROM session_messages WHERE session_id=? AND id<=?
 UNION ALL SELECT 'tool',CAST(c.rowid AS TEXT) FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id
 WHERE a.run_id=? AND a.agent_id=? AND c.rowid<=? AND c.status IN ('completed','failed','denied') AND c.completed_at IS NOT NULL
 AND c.name NOT IN ('history_search','history_read','specialist_result_read')
 AND EXISTS(SELECT 1 FROM run_events e WHERE e.run_id=a.run_id AND e.id<=? AND e.type='agent.tool.completed' AND e.source='specialist_runner'
  AND e.subject_id=c.call_id AND json_extract(e.payload_json,'$.agent_id')=a.agent_id AND json_extract(e.payload_json,'$.agent_attempt_id')=a.id)
 UNION ALL SELECT 'instruction',value FROM json_each(?)
 UNION ALL SELECT 'brief',?
 UNION ALL SELECT 'parent',CAST(value AS TEXT) FROM json_each(?)
 ) ORDER BY kind,id LIMIT ? OFFSET ?`, scope.child.SessionID, cursor.MaxMessage, ref.RunID, ref.AgentID, cursor.MaxTool,
		cursor.MaxEvent, string(messageJSON), scope.brief.Fingerprint, string(parentJSON), domain.HistorySearchScanLimit+1, cursor.Offset)
	if err != nil {
		return value, err
	}
	type candidate struct{ kind, id string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.kind, &c.id); err != nil {
			_ = rows.Close()
			return value, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return value, err
	}
	for _, c := range candidates {
		if value.Scanned == domain.HistorySearchScanLimit || len(value.Records) == request.Limit {
			value.HasMore = true
			break
		}
		value.Scanned++
		record, parts, err := s.readWorkerHistorySource(ctx, tx, scope, c.kind, c.id)
		if errors.Is(err, sql.ErrNoRows) || apperror.CodeOf(err) == apperror.CodeNotFound || apperror.CodeOf(err) == apperror.CodeConflict || apperror.CodeOf(err) == apperror.CodePolicyDenied {
			continue
		}
		if err != nil {
			return value, err
		}
		if request.Query != "" && strings.Contains(record.Title, request.Query) {
			record.MatchedPart, record.Excerpt = "title", historyExcerpt(record.Title, request.Query)
		} else {
			for _, part := range []string{"content", "arguments", "result", "summary"} {
				if content, ok := parts[part]; ok && strings.Contains(content, request.Query) {
					record.MatchedPart, record.Excerpt = part, historyExcerpt(content, request.Query)
					break
				}
			}
		}
		if record.MatchedPart != "" {
			value.Records = append(value.Records, record)
		}
	}
	if value.HasMore {
		cursor.Offset += value.Scanned
		value.NextCursor = encodeHistoryToken(cursor)
	}
	return value, tx.Commit()
}

func (s *SQLiteStore) ReadWorkerHistory(ctx context.Context, ref domain.AgentAttemptRef, request domain.HistoryReadRequest) (domain.HistoryReadResult, error) {
	value := domain.HistoryReadResult{Version: domain.HistoryRecallVersion, RunID: ref.RunID, Offset: request.Offset}
	if request.Offset < 0 || request.Limit < 0 || request.Limit > domain.HistoryReadMaxBytes ||
		(request.Offset > 0 && request.ExpectedSHA256 == "") || len(request.SourceID) > 2048 {
		return value, apperror.New(apperror.CodeInvalidArgument, "worker history page is invalid")
	}
	if request.Limit == 0 {
		request.Limit = domain.HistoryReadDefaultBytes
	}
	if request.Limit < 4 {
		return value, apperror.New(apperror.CodeInvalidArgument, "worker history page must fit one UTF-8 character")
	}
	var source workerHistoryRef
	if !strings.HasPrefix(request.SourceID, "worker-history:") || decodeHistoryToken(strings.TrimPrefix(request.SourceID, "worker-history:"), &source) != nil ||
		source.Run != ref.RunID || source.Agent != ref.AgentID || !validStoreDigest(source.SHA) {
		return value, historyNotFound()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return value, err
	}
	defer tx.Rollback()
	scope, err := s.workerHistoryScopeTx(ctx, tx, ref, "history_read")
	if err != nil {
		return value, err
	}
	record, parts, err := s.readWorkerHistorySource(ctx, tx, scope, source.Kind, source.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = historyNotFound()
	}
	if err != nil {
		return value, err
	}
	if workerHistoryPartsSHA(parts) != source.SHA {
		return value, apperror.New(apperror.CodeConflict, "worker history source changed")
	}
	if request.Part == "" {
		request.Part = "content"
		if record.Kind == "tool_call" {
			request.Part = "result"
		}
	}
	content, ok := parts[request.Part]
	if !ok {
		return value, apperror.New(apperror.CodeInvalidArgument, "worker history source does not have this part")
	}
	value.Record, value.Part, value.ContentSHA256 = record, request.Part, session.ContentSHA256(content)
	if request.ExpectedSHA256 != "" && request.ExpectedSHA256 != value.ContentSHA256 {
		return value, apperror.New(apperror.CodeConflict, "worker history content differs from its expected digest")
	}
	if request.Offset > len(content) || request.Offset < len(content) && !utf8.RuneStart(content[request.Offset]) {
		return value, apperror.New(apperror.CodeInvalidArgument, "worker history offset is outside a UTF-8 boundary")
	}
	end := min(len(content), request.Offset+request.Limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	value.Content, value.NextOffset, value.TotalBytes, value.HasMore = content[request.Offset:end], end, len(content), end < len(content)
	return value, tx.Commit()
}
