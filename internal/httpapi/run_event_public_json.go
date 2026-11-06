package httpapi

import (
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	publicRunEventMaxDepth       = 8
	publicRunEventMaxArrayItems  = 64
	publicRunEventMaxStringRunes = 256
)

// publicRunEventJSON is the only event-payload projection allowed to cross the
// execution record HTTP boundary. Event payloads are an internal audit format:
// they can contain prompts, commands, tool arguments, paths, output, provider
// data, and extension-defined fields. Consequently this projection is an
// allowlist, not a best-effort secret scrubber. Unknown fields are omitted and
// represented only by a count so a future event cannot silently become public.
func publicRunEventJSON(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	projected, omitted := projectPublicRunEventObject(object, 0)
	if omitted > 0 {
		projected["redacted"] = true
		projected["redacted_fields"] = omitted
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return json.RawMessage(`{"redacted":true,"unavailable":true}`)
	}
	return encoded
}

func projectPublicRunEventObject(value map[string]any, depth int) (map[string]any, int) {
	if depth >= publicRunEventMaxDepth {
		return map[string]any{}, len(value)
	}
	projected := make(map[string]any, len(value))
	omitted := 0
	for key, child := range value {
		field := strings.ToLower(strings.TrimSpace(key))
		if field != key || !publicRunEventField(field) {
			omitted++
			continue
		}
		item, keep, childOmitted := projectPublicRunEventValue(child, field, depth+1)
		omitted += childOmitted
		if !keep {
			omitted++
			continue
		}
		projected[field] = item
	}
	return projected, omitted
}

func projectPublicRunEventValue(value any, field string, depth int) (any, bool, int) {
	if depth > publicRunEventMaxDepth {
		return nil, false, 0
	}
	switch typed := value.(type) {
	case nil:
		return nil, true, 0
	case bool:
		if !publicRunEventBooleanField(field) {
			return nil, false, 0
		}
		return typed, true, 0
	case json.Number:
		if !publicRunEventNumberField(field) {
			return nil, false, 0
		}
		return typed, true, 0
	case string:
		projected, ok := projectPublicRunEventString(field, typed)
		if !ok {
			return nil, false, 0
		}
		return projected, true, 0
	case map[string]any:
		if !publicRunEventContainerField(field) {
			return nil, false, 0
		}
		projected, omitted := projectPublicRunEventObject(typed, depth)
		return projected, true, omitted
	case []any:
		if !publicRunEventListField(field) {
			return nil, false, 0
		}
		limit := len(typed)
		omitted := 0
		if limit > publicRunEventMaxArrayItems {
			omitted += limit - publicRunEventMaxArrayItems
			limit = publicRunEventMaxArrayItems
		}
		projected := make([]any, 0, limit)
		for _, child := range typed[:limit] {
			var (
				item         any
				keep         bool
				childOmitted int
			)
			if object, ok := child.(map[string]any); ok {
				item, childOmitted = projectPublicRunEventObject(object, depth+1)
				keep = true
			} else if text, ok := child.(string); ok {
				if elementField := publicRunEventListElementField(field); elementField != "" {
					item, keep = projectPublicRunEventString(elementField, text)
				}
			} else {
				item, keep, childOmitted = projectPublicRunEventValue(child, field, depth+1)
			}
			omitted += childOmitted
			if keep {
				projected = append(projected, item)
			} else {
				omitted++
			}
		}
		return projected, true, omitted
	default:
		return nil, false, 0
	}
}

func publicRunEventField(field string) bool {
	return publicRunEventStringField(field) || publicRunEventNumberField(field) ||
		publicRunEventContainerField(field) || publicRunEventListField(field) ||
		publicRunEventBooleanField(field)
}

func publicRunEventStringField(field string) bool {
	switch field {
	case "version", "protocol_version", "schema_version", "status", "state",
		"phase", "mode", "profile", "role", "outcome", "decision",
		"error_code", "failure_code", "reason_code", "code", "backend",
		"adapter", "runtime", "transport", "surface", "stream", "mime",
		"encoding", "method", "permission", "permission_mode", "policy",
		"capability", "operation_status", "created_at", "updated_at",
		"completed_at", "started_at", "finished_at", "expires_at",
		"deadline_at", "lease_state", "sha256", "digest", "fingerprint", "content_sha256",
		"request_sha256", "response_sha256", "root_fingerprint":
		return true
	default:
		return false
	}
}

func publicRunEventNumberField(field string) bool {
	switch field {
	case "count", "total", "sequence", "ordinal", "position", "attempt",
		"round", "revision", "generation", "exit_code", "duration_ms",
		"elapsed_ms", "size_bytes", "token_estimate", "max_turns", "max_tokens",
		"turn", "model_attempt", "tool_round", "item_count", "success_count",
		"failure_count", "completed_count", "pending_count", "retry_count":
		return true
	default:
		return false
	}
}

func publicRunEventBooleanField(field string) bool {
	switch field {
	case "approved", "available", "enabled", "terminal", "completed", "success",
		"failed", "cancelled", "retriable", "retryable", "recoverable", "durable",
		"truncated", "partial", "stale", "citeable", "untrusted",
		"instruction_authorized", "active", "selected", "required":
		return true
	default:
		return false
	}
}

func publicRunEventContainerField(field string) bool {
	switch field {
	case "budget", "usage", "counts", "limits", "metadata", "checkpoint",
		"authority", "permission", "scope", "selection", "receipt", "timing":
		return true
	default:
		return false
	}
}

func publicRunEventListField(field string) bool {
	switch field {
	case "statuses", "states", "phases", "capabilities", "permissions", "receipts":
		return true
	default:
		return false
	}
}

func publicRunEventListElementField(field string) string {
	switch field {
	case "statuses":
		return "status"
	case "states":
		return "state"
	case "phases":
		return "phase"
	case "capabilities":
		return "capability"
	case "permissions":
		return "permission_mode"
	default:
		return ""
	}
}

func projectPublicRunEventString(field, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	switch field {
	case "version", "protocol_version", "schema_version":
		if !strings.HasPrefix(value, "v") || !publicRunEventMachineToken(value, 32) {
			return "", false
		}
	case "created_at", "updated_at", "completed_at", "started_at", "finished_at",
		"expires_at", "deadline_at":
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return "", false
		}
	case "sha256", "digest", "fingerprint", "content_sha256", "request_sha256",
		"response_sha256", "root_fingerprint":
		if !publicRunEventHexDigest(value) {
			return "", false
		}
	default:
		if !publicRunEventEnum(field, value) {
			return "", false
		}
	}
	return boundPublicRunEventString(redact.String(value)), true
}

func publicRunEventEnum(field, value string) bool {
	switch field {
	case "method":
		switch value {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
			return true
		}
		return false
	case "mime":
		switch value {
		case "application/json", "application/octet-stream", "text/plain", "text/html",
			"text/markdown", "image/png", "image/jpeg", "image/webp":
			return true
		}
		return false
	case "encoding":
		return value == "utf-8" || value == "base64" || value == "binary"
	case "stream":
		return value == "stdout" || value == "stderr" || value == "combined"
	}
	if !publicRunEventMachineToken(value, 64) {
		return false
	}
	_, ok := publicRunEventEnums[value]
	return ok
}

var publicRunEventEnums = map[string]struct{}{
	"accepted": {}, "acquired": {}, "active": {}, "allowed": {}, "applied": {},
	"approved": {}, "automatic": {}, "available": {}, "blocked": {}, "cancelled": {},
	"claimed": {}, "closed": {}, "closing": {}, "code": {}, "committed": {},
	"completed": {}, "conservative": {}, "consumed": {}, "created": {}, "debug": {},
	"deadline_exceeded": {}, "denied": {}, "disabled": {}, "direct": {}, "docker": {},
	"enabled": {}, "exhausted": {}, "expired": {}, "failed": {}, "conflict": {},
	"failed_precondition": {}, "fetched": {}, "full_access": {}, "full_cdp": {},
	"headless": {}, "healthy": {}, "host": {}, "http": {}, "https": {},
	"inactive": {}, "internal": {}, "interrupted": {}, "invalid": {}, "invalid_argument": {}, "manual": {},
	"mcp": {}, "minimal": {}, "missing": {}, "model": {}, "native": {}, "none": {},
	"not_found": {}, "opened": {}, "operator": {}, "partial": {}, "passed": {},
	"paused": {}, "pending": {}, "policy_denied": {}, "prepared": {}, "proposed": {},
	"queued": {}, "quiescent": {}, "ready": {}, "recorded": {}, "recoverable": {},
	"recovered": {}, "rejected": {}, "released": {}, "restricted": {}, "retriable": {},
	"retryable": {}, "retrying": {}, "review": {}, "revoked": {}, "root": {},
	"running": {}, "scheduled": {}, "selected": {}, "skipped": {}, "specialist": {},
	"started": {}, "starting": {}, "stdio": {}, "stopped": {}, "succeeded": {},
	"superseded": {}, "terminal": {}, "timed_out": {}, "tool": {}, "unavailable": {},
	"unhealthy": {}, "valid": {}, "waiting": {}, "wasi": {}, "webview2": {},
	"workspace": {}, "workspace_access": {}, "workspace_sandbox": {},
}

func publicRunEventMachineToken(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' ||
			current == '_' || current == '-' || current == '.' {
			continue
		}
		return false
	}
	return true
}

func publicRunEventHexDigest(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, current := range value {
		if current >= '0' && current <= '9' || current >= 'a' && current <= 'f' {
			continue
		}
		return false
	}
	return true
}

func boundPublicRunEventString(value string) string {
	if utf8.RuneCountInString(value) <= publicRunEventMaxStringRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:publicRunEventMaxStringRunes]) + "…"
}
