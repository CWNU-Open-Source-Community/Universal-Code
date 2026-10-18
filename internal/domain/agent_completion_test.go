package domain

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/redact"
)

func TestCompletionDataCanonicalTypesPrecisionAndAbsence(t *testing.T) {
	base := CompletionReport{Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: "observed results"}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(" \n null ")} {
		base.Data = raw
		report, err := NormalizeCompletionReport(base)
		if err != nil || report.Data != nil {
			t.Fatalf("absent data was not normalized: %s %v", report.Data, err)
		}
		encoded, err := json.Marshal(report)
		if err != nil || strings.Contains(string(encoded), `"data"`) {
			t.Fatalf("absent data changed the legacy report JSON: %s %v", encoded, err)
		}
	}
	base.Data = json.RawMessage(` { "z": [true,null,"text",{"count":9007199254740993}], "a": 0.1234567890123456789 } `)
	report, err := NormalizeCompletionReport(base)
	want := `{"a":0.1234567890123456789,"z":[true,null,"text",{"count":9007199254740993}]}`
	if err != nil || string(report.Data) != want {
		t.Fatalf("structured data changed types or numeric precision: %s %v", report.Data, err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCompletionReport(string(encoded))
	if err != nil || string(decoded.Data) != want {
		t.Fatalf("structured data did not survive strict report decoding: %s %v", decoded.Data, err)
	}
}

func TestCompletionDataBoundsAndObjectContract(t *testing.T) {
	report := CompletionReport{Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: "observed results"}
	boundary := `{"value":"` + strings.Repeat("x", MaxCompletionDataBytes-len(`{"value":""}`)) + `"}`
	depthBoundary := strings.Repeat(`{"child":`, MaxCompletionDataDepth-1) + `{}` + strings.Repeat(`}`, MaxCompletionDataDepth-1)
	arrayBoundary := `{"items":` + strings.Repeat(`[`, MaxCompletionDataDepth-1) + `0` + strings.Repeat(`]`, MaxCompletionDataDepth-1) + `}`
	for _, raw := range []string{boundary, depthBoundary, arrayBoundary} {
		report.Data = json.RawMessage(raw)
		if _, err := NormalizeCompletionReport(report); err != nil {
			t.Fatalf("valid boundary rejected: bytes=%d %v", len(raw), err)
		}
	}
	for _, raw := range []string{
		`[]`, `"text"`, `true`, `123`, `{} {}`,
		`{"value":"` + strings.Repeat("x", MaxCompletionDataBytes-len(`{"value":""}`)+1) + `"}`,
		`{"child":` + depthBoundary + `}`,
		`{"items":` + strings.Repeat(`[`, MaxCompletionDataDepth) + `0` + strings.Repeat(`]`, MaxCompletionDataDepth) + `}`,
		`{"value":"` + strings.Repeat("<", MaxCompletionDataBytes/6) + `"}`,
	} {
		report.Data = json.RawMessage(raw)
		if _, err := NormalizeCompletionReport(report); err == nil {
			t.Fatalf("invalid data accepted: %s", raw)
		}
	}
}

func TestCompletionDataRedactsSensitiveFieldsBeforePersistence(t *testing.T) {
	secret := "sk-" + strings.Repeat("s", 32)
	report, err := RedactCompletionReport(CompletionReport{
		Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: "observed " + secret,
		Data: json.RawMessage(`{"api_key":"plain-credential","nested":[{"password":"pw","value":"` + secret + `"}],"count":9007199254740993,"token_count":12}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(report.Summary, secret) || strings.Contains(string(report.Data), secret) ||
		strings.Contains(string(report.Data), "plain-credential") || strings.Contains(string(report.Data), `"pw"`) ||
		!strings.Contains(string(report.Data), redact.SensitiveJSONFieldPlaceholder) ||
		!strings.Contains(string(report.Data), `"count":9007199254740993`) ||
		!strings.Contains(string(report.Data), `"token_count":12`) {
		t.Fatalf("data redaction lost safe types or retained credentials: %+v", report)
	}
	// Replacing many short credentials can grow the sanitized result past its bound.
	large := make(map[string]string)
	for index := 0; index < 120; index++ {
		large["password"+strconv.Itoa(index)] = "x"
	}
	report.Data, err = json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCompletionReport(report); err != nil {
		t.Fatal(err)
	}
	if _, err := RedactCompletionReport(report); err == nil {
		t.Fatal("oversized redacted structured data was accepted")
	}
}

func TestCompletionReportNormalizationAndStrictDecode(t *testing.T) {
	report, err := NormalizeCompletionReport(CompletionReport{
		Version: CompletionReportVersion,
		Outcome: CompletionOutcome(" SUCCEEDED "), Summary: "  completed the focused review  ",
		WorkItemIDs: []string{"work-2", "work-1", "work-2"},
		NoteIDs:     []string{"note-2", "note-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != CompletionReportVersion || report.Outcome != CompletionSucceeded ||
		report.Summary != "completed the focused review" ||
		strings.Join(report.WorkItemIDs, ",") != "work-1,work-2" ||
		strings.Join(report.NoteIDs, ",") != "note-1,note-2" {
		t.Fatalf("completion report was not normalized: %#v", report)
	}
	decoded, err := DecodeCompletionReport(`{
		"version":"agent_completion.v1","outcome":"partial","summary":"handoff prepared",
		"work_item_ids":[],"note_ids":[]}`)
	if err != nil || decoded.Outcome != CompletionPartial || decoded.WorkItemIDs == nil || decoded.NoteIDs == nil {
		t.Fatalf("strict completion decode failed: report=%#v err=%v", decoded, err)
	}
	if _, err := DecodeCompletionReport(`{
		"version":"agent_completion.v1","outcome":"succeeded","summary":"done",
		"work_item_ids":[],"note_ids":[],"extra":true}`); err == nil {
		t.Fatal("completion report accepted an unknown field")
	}
}

func TestCompletionReportRejectsInvalidBoundsAndMetadata(t *testing.T) {
	cases := []CompletionReport{
		{Outcome: CompletionSucceeded, Summary: "missing version"},
		{Version: "agent_completion.v2", Outcome: CompletionSucceeded, Summary: "done"},
		{Version: CompletionReportVersion, Outcome: CompletionOutcome("failed"), Summary: "done"},
		{Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: ""},
		{Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: strings.Repeat("a", MaxCompletionSummaryBytes+1)},
		{Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: "done", WorkItemIDs: []string{strings.Repeat("x", MaxCompletionReferenceBytes+1)}},
	}
	for index, report := range cases {
		if _, err := NormalizeCompletionReport(report); err == nil {
			t.Fatalf("invalid completion report %d was accepted: %#v", index, report)
		}
	}
	now := time.Now().UTC()
	report, err := NormalizeCompletionReport(CompletionReport{
		Version: CompletionReportVersion, Outcome: CompletionSucceeded, Summary: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := AgentCompletion{
		ID: "completion-1", RunID: "run-1", AgentID: "agent-child",
		ParentAgentID: "agent-root", AttemptID: "attempt-1", Report: report,
		MessageID: "message-1", CreatedAt: now,
	}
	if err := completion.Validate(); err != nil {
		t.Fatalf("valid Agent completion was rejected: %v", err)
	}
	completion.ParentAgentID = completion.AgentID
	if err := completion.Validate(); err == nil {
		t.Fatal("completion accepted the child as its own parent")
	}
}
