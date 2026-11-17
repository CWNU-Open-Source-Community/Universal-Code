package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash"
	"path"
	"sort"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

type progressToolEnvelope struct {
	Version   string            `json:"version"`
	Tool      string            `json:"tool"`
	Status    string            `json:"status"`
	Stdout    string            `json:"stdout"`
	Truncated bool              `json:"truncated"`
	Metadata  map[string]string `json:"metadata"`
}

type progressReadObservation struct {
	Encoding   string
	Newline    string
	TotalLines int
	TotalBytes int64
	Lines      map[int][sha256.Size]byte
}

// Completed ledger rows are immutable. Rebuild their semantic evidence in the
// same transaction as the guard, using the existing run_id index prefix. IDs,
// timestamps, receipt counts and arbitrary result metadata are not progress.
// The cumulative sets deliberately stop rewarding cycles through old content.
// Command output needs its own trustworthy semantics and is not counted here.
func writeSupervisorToolProgressTx(ctx context.Context, tx *sql.Tx, runID string, digest hash.Hash) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_id, turn, attempt_id, round, position,
		model_attempt, call_id, stream_response_id, stream_item_id, stream_call_id,
		tool_name, payload_json, authority_json, status, result_json, error_code, created_at, completed_at
		FROM run_supervisor_tool_calls WHERE run_id=? AND status='completed' AND error_code=''
		AND completed_at IS NOT NULL AND tool_name IN ('workspace_read','workspace_change','workspace_apply','workspace_delete','browser_snapshot')
		ORDER BY turn,rowid`, runID)
	if err != nil {
		return fmt.Errorf("read sealed tool progress: %w", err)
	}
	defer rows.Close()
	reads := map[string]*progressReadObservation{}
	writes := map[string]bool{}
	browserStates := map[string]bool{}
	for rows.Next() {
		call, err := scanSupervisorToolCall(rows)
		if err != nil {
			return err
		}
		var envelope progressToolEnvelope
		if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil ||
			envelope.Version != "supervisor_tool_result.v1" || envelope.Tool != call.ToolName ||
			envelope.Status != "completed" || envelope.Truncated {
			continue
		}
		if call.ToolName == "browser_snapshot" {
			if observation, ok := progressBrowserStateObservation(call, envelope); ok {
				browserStates[observation] = true
			}
		} else if call.ToolName == "workspace_read" {
			addProgressReadObservation(call, envelope, reads)
		} else if observation, ok := progressAppliedObservation(call, envelope); ok {
			writes[observation] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Keep the old fingerprint unchanged when this Run has no accepted evidence.
	if len(reads)+len(writes)+len(browserStates) == 0 {
		return nil
	}
	writeProgressHashValue(digest, "sealed_tool_progress.v1")
	for _, key := range sortedProgressKeys(reads) {
		observation := reads[key]
		writeProgressHashValue(digest, "read")
		writeProgressHashValue(digest, key)
		writeProgressHashValue(digest, progressTuple(observation.Encoding, observation.Newline,
			observation.TotalLines, observation.TotalBytes))
		lines := make([]int, 0, len(observation.Lines))
		for line := range observation.Lines {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		for _, line := range lines {
			bodyHash := observation.Lines[line]
			writeProgressHashValue(digest, strconv.Itoa(line))
			writeProgressHashValue(digest, fmt.Sprintf("%x", bodyHash))
		}
	}
	for _, key := range sortedProgressKeys(writes) {
		writeProgressHashValue(digest, "applied")
		writeProgressHashValue(digest, key)
	}
	for _, key := range sortedProgressKeys(browserStates) {
		writeProgressHashValue(digest, "observed_browser_controls.v1")
		writeProgressHashValue(digest, key)
	}
	return nil
}

func sortedProgressKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func addProgressReadObservation(call domain.SupervisorToolCall, envelope progressToolEnvelope,
	reads map[string]*progressReadObservation,
) {
	var page struct {
		Version         string  `json:"protocol_version"`
		WorkspaceID     string  `json:"workspace_id"`
		RootFingerprint string  `json:"root_fingerprint"`
		Path            string  `json:"path"`
		ContentSHA256   string  `json:"content_sha256"`
		Content         *string `json:"content"`
		Encoding        string  `json:"encoding"`
		Newline         string  `json:"newline"`
		StartLine       *int    `json:"start_line"`
		EndLine         *int    `json:"end_line"`
		TotalLines      *int    `json:"total_lines"`
		TotalBytes      *int64  `json:"total_bytes"`
		RedactionCount  *int    `json:"redaction_count"`
		Truncated       *bool   `json:"truncated"`
	}
	if json.Unmarshal([]byte(envelope.Stdout), &page) != nil || page.Version != "agent-code-tools.v1" ||
		!progressIdentity(page.WorkspaceID) || !progressSHA256(page.RootFingerprint) ||
		!progressRelativePath(page.Path) || !progressSHA256(page.ContentSHA256) || page.Content == nil ||
		page.StartLine == nil || page.EndLine == nil || page.TotalLines == nil || page.TotalBytes == nil ||
		page.RedactionCount == nil || *page.RedactionCount != 0 || page.Truncated == nil ||
		*page.StartLine < 1 || *page.EndLine < *page.StartLine || *page.EndLine > *page.TotalLines ||
		*page.EndLine-*page.StartLine >= 2000 || *page.TotalBytes < 0 ||
		(page.Encoding != "utf-8" && page.Encoding != "utf-8-bom") ||
		(page.Newline != "lf" && page.Newline != "crlf" && page.Newline != "mixed" && page.Newline != "none") {
		return
	}
	if !progressScopeMatches(call, envelope.Metadata, page.WorkspaceID, page.RootFingerprint) ||
		!progressMetadataMatches(envelope.Metadata, map[string]string{"path": page.Path,
			"content_sha256": page.ContentSHA256, "encoding": page.Encoding, "newline": page.Newline}) {
		return
	}
	var request toolgateway.WorkspaceReadPayload
	if json.Unmarshal([]byte(call.PayloadJSON), &request) != nil || request.Version != "agent-code-tools.v1" ||
		path.Clean(strings.ReplaceAll(request.Path, "\\", "/")) != page.Path ||
		request.StartLine != *page.StartLine || min(request.EndLine, *page.TotalLines) != *page.EndLine {
		return
	}
	// The inner truncated flag describes pagination; the outer flag describes a
	// cut result. Only complete line bodies may contribute to coverage. Redacted
	// pages are conservatively excluded because redaction may cross page edges.
	lines := strings.Split(*page.Content, "\n")
	if len(lines) != *page.EndLine-*page.StartLine+1 {
		return
	}
	key := progressTuple(page.WorkspaceID, page.RootFingerprint, page.Path, page.ContentSHA256)
	known := reads[key]
	if known == nil {
		known = &progressReadObservation{Encoding: page.Encoding, Newline: page.Newline,
			TotalLines: *page.TotalLines, TotalBytes: *page.TotalBytes, Lines: map[int][sha256.Size]byte{}}
	} else if known.Encoding != page.Encoding || known.Newline != page.Newline ||
		known.TotalLines != *page.TotalLines || known.TotalBytes != *page.TotalBytes {
		return
	}
	for index, body := range lines {
		if previous, found := known.Lines[*page.StartLine+index]; found && previous != sha256.Sum256([]byte(body)) {
			return // Contradictory overlap cannot introduce any part of this page.
		}
	}
	for index, body := range lines {
		known.Lines[*page.StartLine+index] = sha256.Sum256([]byte(body))
	}
	reads[key] = known
}

func progressAppliedObservation(call domain.SupervisorToolCall, envelope progressToolEnvelope) (string, bool) {
	effect := domain.ObservedSupervisorToolEffect(call)
	if effect == nil || effect.Effect != "recorded_applied" || effect.FileWritten == nil || !*effect.FileWritten ||
		effect.Replayed == nil || *effect.Replayed {
		return "", false
	}
	var result struct {
		Operation           string `json:"operation"`
		Original            string `json:"original_sha256"`
		Proposed            string `json:"proposed_sha256"`
		DestinationPath     string `json:"destination_path"`
		DestinationOriginal string `json:"destination_original_sha256"`
		DestinationProposed string `json:"destination_proposed_sha256"`
	}
	if json.Unmarshal([]byte(envelope.Stdout), &result) != nil || !progressRelativePath(effect.Path) ||
		(result.Original != "missing" && !progressSHA256(result.Original)) {
		return "", false
	}
	isMove := result.Operation == "move"
	if isMove {
		if !progressRelativePath(result.DestinationPath) || result.DestinationPath == effect.Path ||
			!progressSHA256(result.Original) || result.Proposed != "missing" ||
			(result.DestinationOriginal != "" && result.DestinationOriginal != "missing") ||
			!progressSHA256(result.DestinationProposed) || result.DestinationProposed != effect.DestinationProposedSHA256 {
			return "", false
		}
	} else if !progressSHA256(result.Proposed) || result.Proposed != effect.ProposedSHA256 || result.Original == result.Proposed {
		return "", false
	}
	switch call.ToolName {
	case "workspace_apply":
		var request toolgateway.WorkspaceApplyPayload
		if json.Unmarshal([]byte(call.PayloadJSON), &request) != nil ||
			request.Version != "agent-code-tools.v1" || request.EditID != effect.EditID ||
			request.ExpectedOriginalSHA256 != result.Original || request.ExpectedProposedSHA256 != result.Proposed ||
			!((request.ExpectedAction == "create" && result.Operation == "create" && result.Original == "missing") ||
				((request.ExpectedAction == "propose_patch" || request.ExpectedAction == "replace") && result.Operation == "replace" && progressSHA256(result.Original)) ||
				(request.ExpectedAction == "move" && result.Operation == "move" && progressSHA256(result.Original))) {
			return "", false
		}
	case "workspace_change":
		var request toolgateway.WorkspaceChangePayload
		if json.Unmarshal([]byte(call.PayloadJSON), &request) != nil ||
			request.Version != "agent-code-tools.v1" ||
			path.Clean(strings.ReplaceAll(request.Path, "\\", "/")) != effect.Path {
			return "", false
		}
		validAction := (request.Action == "create" && result.Operation == "create" && result.Original == "missing" && request.ExpectedSHA256 == "missing") ||
			((request.Action == "replace" || request.Action == "propose_patch") && result.Operation == "replace" && progressSHA256(result.Original) && request.ExpectedSHA256 == result.Original) ||
			(request.Action == "move" && result.Operation == "move" && progressSHA256(result.Original) && request.ExpectedSHA256 == result.Original &&
				request.DestinationExpectedSHA256 == "missing" && path.Clean(strings.ReplaceAll(request.DestinationPath, "\\", "/")) == result.DestinationPath) ||
			(request.Action == "propose_revert" && request.ExpectedSHA256 == result.Original &&
				((result.Operation == "replace" && progressSHA256(result.Original)) || (result.Operation == "create" && result.Original == "missing")))
		if !validAction {
			return "", false
		}
	default:
		return "", false
	}
	metadata := map[string]string{
		"path":            effect.Path,
		"original_sha256": result.Original,
		"proposed_sha256": result.Proposed,
		"file_written":    "true",
		"replayed":        "false",
	}
	if isMove {
		metadata["destination_path"] = result.DestinationPath
		metadata["destination_proposed_sha256"] = result.DestinationProposed
	}
	authority, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(call.AuthorityJSON))
	if err != nil || !progressScopeMatches(call, envelope.Metadata, authority.WorkspaceID, authority.RootFingerprint) ||
		!progressMetadataMatches(envelope.Metadata, metadata) {
		return "", false
	}
	// An exact replay, a duplicate receipt with a new ID, or an already seen
	// A->B transition contributes no additional effect. This is a past observed
	// write, never a claim about the current filesystem or a grant of authority.
	if isMove {
		return progressTuple(authority.WorkspaceID, authority.RootFingerprint, effect.Path,
			result.DestinationPath, result.Operation, result.Original, result.DestinationProposed), true
	}
	return progressTuple(authority.WorkspaceID, authority.RootFingerprint, effect.Path,
		result.Operation, result.Original, result.Proposed), true
}

func progressScopeMatches(call domain.SupervisorToolCall, metadata map[string]string,
	workspaceID, rootFingerprint string,
) bool {
	if !progressMetadataMatches(metadata, map[string]string{"workspace_id": workspaceID, "root_fingerprint": rootFingerprint}) {
		return false
	}
	if call.AuthorityJSON != "" {
		authority, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(call.AuthorityJSON))
		if err != nil || authority.RunID != call.RunID || authority.WorkspaceID != workspaceID || authority.RootFingerprint != rootFingerprint {
			return false
		}
	}
	return true
}

func progressMetadataMatches(metadata, expected map[string]string) bool {
	for key, value := range expected {
		if actual, found := metadata[key]; found && actual != value {
			return false
		}
	}
	return true
}

func progressSHA256(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func progressIdentity(value string) bool {
	return value != "" && len(value) <= 4096 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func progressRelativePath(value string) bool {
	return progressIdentity(value) && !strings.Contains(value, "\\") && value != "." &&
		!strings.HasPrefix(value, "/") && value != ".." && !strings.HasPrefix(value, "../") && path.Clean(value) == value
}

func progressTuple(values ...any) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}
