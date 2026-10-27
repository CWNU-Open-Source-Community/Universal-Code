package threadtranscript

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/events"
)

func TestBuildAttributesDurableReplyWithLatestModelCallAttempt(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	source := []Source{
		eventSource("run-1", 1, 1, events.ModelStartedEvent,
			`{"provider":"anthropic","model":"test-model","attempt_id":"attempt-7"}`, now),
		eventSource("run-1", 1, 2, events.ModelCompletedEvent,
			`{"provider":"anthropic","model":"test-model","attempt_id":"attempt-7","elapsed_millis":1200,"usage":{"total_tokens":42}}`, now.Add(time.Second)),
		eventSource("run-1", 1, 3, events.SessionMessageEvent,
			`{"role":"assistant","content":"public answer","source_kind":"model_response","instruction_authorized":false}`, now.Add(2*time.Second)),
		eventSource("run-1", 1, 4, events.ModelPublicCommentaryEvent,
			`{"text":"检查中","attempt_id":"attempt-7"}`, now.Add(3*time.Second)),
	}
	items, err := Build("thread-1", source)
	if err != nil {
		t.Fatal(err)
	}
	var call, reply, commentary *Item
	for index := range items {
		switch items[index].ID {
		case "event-run-1-model.started":
			call = &items[index]
		case "event-run-1-session.message_created":
			reply = &items[index]
		case "event-run-1-model.public_commentary":
			commentary = &items[index]
		}
	}
	if call == nil || reply == nil || commentary == nil {
		t.Fatalf("expected call, reply and commentary items: %#v", items)
	}
	if call.Model != "anthropic / test-model" || call.AttemptID != "attempt-7" {
		t.Fatalf("model call lost its identity or attempt: %#v", call)
	}
	if reply.AttemptID != "attempt-7" {
		t.Fatalf("durable reply did not inherit the call attempt: %#v", reply)
	}
	if commentary.AttemptID != "attempt-7" {
		t.Fatalf("commentary lost its attempt: %#v", commentary)
	}
}

func TestBuildOrdersRunBoundariesMessagesAndStructuredToolStages(t *testing.T) {
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	batch, _ := json.Marshal(map[string]any{
		"tools":              []string{"read_file", "replace_file"},
		"stream_response_id": "resp-stable", "stream_item_ids": []string{"item-read", "item-edit"},
		"stream_call_ids": []string{"call-read", "call-edit"},
	})
	source := []Source{
		eventSource("run-2", 2, 2, events.SupervisorToolExecutionStartedEvent,
			`{"tool":"read_file","status":"pending","stream_response_id":"resp-stable","stream_item_id":"item-read","stream_call_id":"call-read","durable_call_id":"durable-read"}`, now.Add(5*time.Second)),
		{RunID: "run-1", SessionID: "session-1", Ordinal: 1, RunStatus: "failed", CreatedAt: now},
		eventSource("run-1", 1, 1, events.SessionMessageEvent,
			`{"role":"assistant","content":"public answer","source_kind":"model_response","instruction_authorized":false}`, now.Add(time.Second)),
		{RunID: "run-2", SessionID: "session-2", Ordinal: 2,
			PredecessorRunID: "run-1", PredecessorRunStatus: "failed",
			RunStatus: "running", CreatedAt: now.Add(2 * time.Second)},
		eventSource("run-2", 2, 1, events.SupervisorToolBatchEvent, string(batch), now.Add(4*time.Second)),
	}
	items, err := Build("thread-1", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("unexpected transcript items: %#v", items)
	}
	if items[0].ID != "run-boundary:run-1" || items[1].Type != TypeMessage ||
		items[2].BoundaryReason != "predecessor_terminal_failed" {
		t.Fatalf("Run narrative is not stable: %#v", items[:3])
	}
	if items[3].CanonicalID != "item-read" || items[3].Type != TypeRead ||
		items[3].Stage != StageArgumentsReady || items[4].Type != TypeEdit {
		t.Fatalf("tool batch was not projected item-by-item: %#v", items[3:5])
	}
	if items[5].CanonicalID != "item-read" || items[5].Stage != StageRunning ||
		items[5].DurableCallID != "durable-read" || !items[5].Verifiable {
		t.Fatalf("durable tool execution identity was lost: %#v", items[5])
	}
}

func TestBuildProjectsPendingAndCancelledComposerInputWithoutDuplicatingCommittedInput(t *testing.T) {
	now := time.Now().UTC()
	queued := eventSource("run-1", 1, 1, events.OperatorSteeringQueuedEvent, `{}`, now)
	queued.Event.SubjectID = "steering-1"
	queued.OperatorStatus = "pending"
	queued.OperatorContent = "continue safely"
	items, err := Build("thread-1", []Source{queued})
	if err != nil || len(items) != 1 || items[0].SourceRef != "steering-1" ||
		items[0].Detail != "continue safely" || !items[0].InstructionAuthorized {
		t.Fatalf("pending Composer projection is wrong: items=%#v err=%v", items, err)
	}
	queued.OperatorStatus = "cancelled"
	items, err = Build("thread-1", []Source{queued})
	if err != nil || len(items) != 1 || items[0].Status != "cancelled" ||
		items[0].Stage != StageBlocked || items[0].InstructionAuthorized ||
		items[0].Title != "用户消息已取消" || items[0].Detail != "continue safely" {
		t.Fatalf("cancelled Composer projection is wrong: items=%#v err=%v", items, err)
	}
	queued.OperatorStatus = "pending"
	queued.OperatorDeliveryMode = "steer"
	items, err = Build("thread-1", []Source{queued})
	if err != nil || len(items) != 1 || items[0].DeliveryMode != "steer" {
		t.Fatalf("steering delivery mode was not projected: items=%#v err=%v", items, err)
	}
	queued.OperatorDeliveryMode = ""
	queued.OperatorStatus = "committed"
	items, err = Build("thread-1", []Source{queued})
	if err != nil || len(items) != 0 {
		t.Fatalf("committed steering duplicated Session history: items=%#v err=%v", items, err)
	}
}

func TestBuildAnchorsExactlyBoundCommittedInputAcrossPages(t *testing.T) {
	now := time.Now().UTC()
	queued := eventSource("run-1", 1, 30, events.OperatorSteeringQueuedEvent, `{}`, now)
	queued.Event.SubjectID = "steering-2"
	queued.OperatorContent, queued.OperatorStatus = "read README.md", "committed"
	queued.OperatorMessageBound = true
	tool := eventSource("run-1", 1, 40, events.SupervisorToolExecutionStartedEvent,
		`{"tool":"workspace_read","status":"pending","durable_call_id":"read-2"}`, now.Add(time.Second))
	recorded := eventSource("run-1", 1, 50, events.SessionMessageEvent,
		`{"role":"user","content":"read README.md","source_kind":"operator_message","instruction_authorized":true}`, now.Add(2*time.Second))
	recorded.OperatorMessageBound = true
	for _, page := range [][]Source{{queued, tool, recorded}, {queued}, {recorded}} {
		before, _ := json.Marshal(page)
		items, err := Build("thread-1", page)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 3 && (len(items) != 2 || items[0].Sequence != 30 || items[1].Sequence != 40) {
			t.Fatalf("tools preceded the original input: %#v", items)
		}
		if page[0].Sequence == 30 && (len(items) == 0 || items[0].CanonicalID != "steering-2" ||
			items[0].CreatedAt != now || items[0].Stage != StageResult || items[0].Status != "committed") {
			t.Fatalf("committed input lost its queue identity: %#v", items)
		}
		if page[0].Sequence == 50 && len(items) != 0 {
			t.Fatalf("later page duplicated the bound input: %#v", items)
		}
		after, _ := json.Marshal(page)
		if string(before) != string(after) {
			t.Fatal("projection mutated source records")
		}
	}
	// Equal text is not a binding: legacy/direct Session input stays visible.
	recorded.OperatorMessageBound = false
	items, err := Build("thread-1", []Source{recorded})
	if err != nil || len(items) != 1 || items[0].Sequence != 50 {
		t.Fatalf("unbound history disappeared: items=%#v err=%v", items, err)
	}
}

func TestBuildRejectsUnboundedOrInconsistentSource(t *testing.T) {
	now := time.Now().UTC()
	source := make([]Source, MaxSourceRecords+1)
	if _, err := Build("thread-1", source); err == nil {
		t.Fatal("unbounded source page was accepted")
	}
	broken := eventSource("run-1", 1, 1, events.RunStatusChangedEvent,
		`{"status":"running"}`, now)
	broken.Event.RunID = "other-run"
	if _, err := Build("thread-1", []Source{broken}); err == nil {
		t.Fatal("cross-Run source event was accepted")
	}
}

func TestClassifyToolUsesExactCurrentToolRegistryNames(t *testing.T) {
	tests := map[string]ActivityType{
		"workspace_list":              TypeSearch,
		"workspace_glob":              TypeSearch,
		"code_references":             TypeSearch,
		"web_search":                  TypeSearch,
		"workspace_read":              TypeRead,
		"web_fetch":                   TypeRead,
		"web_citation":                TypeRead,
		"github_review_evidence_read": TypeRead,
		"workspace_apply":             TypeEdit,
		"code_diagnostics":            TypeVerify,
		"work_item_create":            TypeCheckpoint,
		"plan_delivery_propose":       TypeDelivery,
		"shell":                       TypeExecute,
		"please_read_everything":      TypeExecute,
	}
	for name, want := range tests {
		if got := classifyTool(name); got != want {
			t.Errorf("classifyTool(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildProjectsOnlyValidatedWebEvidencePresentation(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(map[string]any{
		"tool": "web_citation", "status": "completed",
		"stream_response_id": "response-web", "stream_item_id": "item-web",
		"stream_call_id": "stream-call-web", "durable_call_id": "call-web",
		"web_evidence": map[string]any{
			"version": "web_evidence_presentation.v1", "source_id": "source-web",
			"snapshot_id": "snapshot-web", "citation_id": "citation-web",
			"url": "https://docs.example.com/report", "title": "Fetched report",
			"state": "partial", "fetched_at": now,
			"stale_at": now.Add(24 * time.Hour),
			"digest":   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"partial":  true, "stale": false, "citeable": true,
			"untrusted": true, "instruction_authorized": false,
		},
	})
	var raw map[string]any
	_ = json.Unmarshal(payload, &raw)
	presentation := raw["web_evidence"].(map[string]any)
	items, err := Build("thread-web", []Source{eventSource("run-web", 1, 1,
		events.SupervisorToolResultEvent, string(payload), now)})
	if err != nil || len(items) != 1 || items[0].WebEvidence == nil ||
		items[0].WebEvidence.URL != "https://docs.example.com/report" ||
		!items[0].WebEvidence.Untrusted || items[0].WebEvidence.InstructionAuthorized {
		t.Fatalf("web presentation items=%#v err=%v", items, err)
	}
	presentation["url"] = "http://127.0.0.1/private"
	payload, _ = json.Marshal(raw)
	items, err = Build("thread-web", []Source{eventSource("run-web", 1, 1,
		events.SupervisorToolResultEvent, string(payload), now)})
	if err != nil || len(items) != 1 || items[0].WebEvidence != nil {
		t.Fatalf("unsafe presentation items=%#v err=%v", items, err)
	}
}

func TestBuildProjectsWebEvidenceSecurityOutcomeInsteadOfGenericCompletion(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	base := map[string]any{
		"version": "web_evidence_presentation.v1", "source_id": "source-web",
		"snapshot_id": "snapshot-web", "url": "https://docs.example.com/report",
		"title": "Report", "fetched_at": now, "stale_at": now.Add(24 * time.Hour),
		"digest": strings.Repeat("a", 64), "stale": false,
		"untrusted": true, "instruction_authorized": false,
	}
	tests := []struct {
		name, state, robots, wantTitle, wantStatus string
		partial, citeable                          bool
	}{
		{name: "fetched", state: "fetched", robots: "allowed", citeable: true,
			wantTitle: "网页已抓取", wantStatus: "fetched"},
		{name: "robots not present", state: "fetched", robots: "not_present", citeable: true,
			wantTitle: "网页已抓取", wantStatus: "fetched"},
		{name: "partial", state: "partial", robots: "allowed", partial: true, citeable: true,
			wantTitle: "网页已部分抓取", wantStatus: "partial"},
		{name: "blocked", state: "blocked", robots: "blocked",
			wantTitle: "Robots 规则阻止抓取", wantStatus: "blocked"},
		{name: "robots ignored", state: "fetched", robots: "not_checked", citeable: true,
			wantTitle: "网页已抓取（未检查 Robots）", wantStatus: "robots_ignored"},
		{name: "robots disallow bypassed", state: "fetched", robots: "bypassed_disallow", citeable: true,
			wantTitle: "Full Access 已忽略站点 Robots 限制", wantStatus: "robots_ignored"},
		{name: "robots unknown bypassed", state: "fetched", robots: "bypassed_unknown", citeable: true,
			wantTitle: "Robots 无法验证，已按 Full Access 继续", wantStatus: "robots_ignored"},
		{name: "verification unavailable", state: "failed", robots: "unknown",
			wantTitle: "网页验证不可用", wantStatus: "verification_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation := make(map[string]any, len(base)+5)
			for key, value := range base {
				presentation[key] = value
			}
			presentation["state"], presentation["robots"] = test.state, test.robots
			presentation["partial"], presentation["citeable"] = test.partial, test.citeable
			payload, _ := json.Marshal(map[string]any{
				"tool": "web_fetch", "status": "completed", "stream_item_id": "item-web",
				"web_evidence": presentation,
			})
			items, err := Build("thread-web", []Source{eventSource("run-web", 1, 1,
				events.SupervisorToolResultEvent, string(payload), now)})
			if err != nil || len(items) != 1 || items[0].WebEvidence == nil ||
				items[0].Title != test.wantTitle || items[0].Status != test.wantStatus ||
				items[0].Title == "工具结果已记录" {
				t.Fatalf("items=%#v err=%v", items, err)
			}
		})
	}
}

func TestImportedBranchReplyRetainsItsOrigin(t *testing.T) {
	source := eventSource("branch-run", 1, 1, events.ModelPublicCommentaryEvent,
		`{"text":"Earlier answer","source_event_id":"original-reply"}`, time.Now().UTC())
	source.Event.Source = "thread_branch"
	items, err := Build("branch-thread", []Source{source})
	if err != nil || len(items) != 1 || items[0].SourceRef != "branch-assistant:original-reply" {
		t.Fatalf("imported reply lost its origin: items=%#v err=%v", items, err)
	}
}

func eventSource(runID string, ordinal, sequence int64, eventType, payload string,
	createdAt time.Time,
) Source {
	event := &events.Event{EventID: "event-" + runID + "-" + eventType,
		Version: events.EnvelopeVersion, RunID: runID, MissionID: "mission-1",
		Sequence: sequence, Type: eventType, Source: "test", PayloadJSON: payload,
		CreatedAt: createdAt}
	return Source{RunID: runID, SessionID: "session-" + runID, Ordinal: ordinal,
		RunStatus: "running", Sequence: sequence, CreatedAt: createdAt, Event: event}
}
