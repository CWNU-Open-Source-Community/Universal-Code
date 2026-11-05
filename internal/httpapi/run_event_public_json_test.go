package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/events"
)

func TestPublicRunEventJSONKeepsOnlyAllowlistedAuditMetadata(t *testing.T) {
	raw := `{
		"version":"v1",
		"status":"failed",
		"run_id":"run-1",
		"sequence":7,
		"completed":true,
		"usage":{"max_tokens":1024,"private_note":"do not expose this"},
		"message":"operator secret text",
		"stdout":"token=stdout-secret",
		"arguments":{"path":"D:/private/project","api_key":"argument-secret"},
		"custom_field":"extension-secret",
		"headers":{"Authorization":"Bearer header-secret"}
	}`
	projected := publicRunEventJSON(raw)
	if !json.Valid(projected) {
		t.Fatalf("invalid projection: %s", projected)
	}
	public := string(projected)
	for _, required := range []string{`"version":"v1"`, `"status":"failed"`,
		`"sequence":7`, `"completed":true`, `"max_tokens":1024`,
		`"redacted":true`, `"redacted_fields":`} {
		if !strings.Contains(public, required) {
			t.Fatalf("projection omitted %q: %s", required, public)
		}
	}
	for _, forbidden := range []string{"run-1", "operator secret text", "stdout-secret", "D:/private/project",
		"argument-secret", "extension-secret", "header-secret", "custom_field", "private_note"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("projection exposed %q: %s", forbidden, public)
		}
	}
}

func TestPublicRunEventJSONRejectsArbitraryValuesInAllowlistedStringFields(t *testing.T) {
	projected := string(publicRunEventJSON(`{"status":"plain-secret-value","backend":"run private command",` +
		`"error_code":"sk-example-secret","version":"not-a-version","statuses":["ready","secret-value"],` +
		`"completed_at":"not-a-time","sha256":"not-a-digest"}`))
	for _, forbidden := range []string{"plain-secret-value", "run private command", "sk-example-secret",
		"not-a-version", "secret-value", "not-a-time", "not-a-digest"} {
		if strings.Contains(projected, forbidden) {
			t.Fatalf("projection exposed %q: %s", forbidden, projected)
		}
	}
	if !strings.Contains(projected, `"statuses":["ready"]`) ||
		!strings.Contains(projected, `"redacted":true`) {
		t.Fatalf("projection did not preserve the vetted list item: %s", projected)
	}
}

func TestPublicRunEventJSONRejectsMalformedTrailingAndNonObjectPayloads(t *testing.T) {
	for _, raw := range []string{"", `{"status":`, `{"status":"ok"}{}`, `"secret"`, `["secret"]`} {
		if got := string(publicRunEventJSON(raw)); got != `{"redacted":true,"unavailable":true}` {
			t.Fatalf("raw=%q projection=%s", raw, got)
		}
	}
}

func TestPublicRunEventJSONBoundsArraysDepthAndStrings(t *testing.T) {
	long := strings.Repeat("a", publicRunEventMaxStringRunes+20)
	projected := string(publicRunEventJSON(`{"status":"` + long + `","receipts":[` +
		strings.Repeat(`{"status":"ok"},`, publicRunEventMaxArrayItems) + `{"status":"overflow"}]}`))
	if strings.Contains(projected, "overflow") || strings.Contains(projected, long) {
		t.Fatalf("projection was not bounded: %s", projected)
	}
	if !strings.Contains(projected, `"redacted":true`) {
		t.Fatalf("bounded projection did not report redaction: %s", projected)
	}
}

func TestEventViewNeverCarriesTheRawEventPayload(t *testing.T) {
	view := eventView(events.Event{EventID: "event-1", Version: events.EnvelopeVersion,
		RunID: "run-1", MissionID: "mission-1", Sequence: 1,
		Type: "extension.future_event", Source: "test", SubjectID: "agent-root",
		PayloadJSON: `{"status":"completed","future":"raw-extension-secret","stdout":"raw-output"}`,
		CreatedAt:   time.Now().UTC()})
	public := string(view.Payload)
	if strings.Contains(public, "raw-extension-secret") || strings.Contains(public, "raw-output") {
		t.Fatalf("EventView crossed the record detail boundary with raw payload: %s", public)
	}
	if !strings.Contains(public, `"status":"completed"`) ||
		!strings.Contains(public, `"redacted":true`) {
		t.Fatalf("EventView omitted safe lifecycle metadata: %s", public)
	}
}
