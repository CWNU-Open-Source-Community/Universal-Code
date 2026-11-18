package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func startedSkillRun(t *testing.T, st *store.SQLiteStore) domain.Run {
	t.Helper()
	svc := application.NewRunService(st)
	_, run, err := svc.Create(t.Context(), application.CreateRunRequest{Goal: "Inspect frontend behavior", Profile: "code", Surface: "code", Phase: "deliver", ModelRoute: "tool-loop/model", Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func builtinReadCall(t *testing.T, name, id string) llm.ToolCall {
	t.Helper()
	r, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get(name)
	raw, _ := json.Marshal(toolgateway.SkillReadRequest{Name: m.Name, Version: m.Version, ContentSHA256: m.ContentSHA256})
	return llm.ToolCall{ID: id, Name: "skill_read", Arguments: raw}
}

func TestBuiltinSkillSameBatchBudgetAndDuplicateRead(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "batch.db"))
	defer st.Close()
	run := startedSkillRun(t, st)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{builtinReadCall(t, "frontend-design", "front"), builtinReadCall(t, "run-verify", "verify"), builtinReadCall(t, "focused-checks", "overflow")}},
		{ToolCalls: []llm.ToolCall{builtinReadCall(t, "frontend-design", "same-front")}},
		textResponse(rootActionResponse(domain.RootActionContinue, "Bounded reads checked", "", "")),
	}}
	if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(reads) != 2 {
		t.Fatalf("successful unique reads=%d %v", len(reads), err)
	}
	for _, c := range reads {
		var pin toolgateway.SkillReadRequest
		_ = json.Unmarshal([]byte(c.PayloadJSON), &pin)
		if pin.Name == "focused-checks" {
			t.Fatal("overflow activated")
		}
	}
	requests := p.Requests()
	if len(requests) != 3 {
		t.Fatal("unexpected retries", len(requests))
	}
	if !hasToolResult(requests[1], string(apperror.CodeResourceExhausted)) {
		t.Fatal("budget rejection not reported to model")
	}
	for _, request := range requests[1:] {
		count := 0
		for _, m := range request.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Model-requested embedded Skill frontend-design") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("duplicate guidance count=%d", count)
		}
	}
}

func TestBuiltinSkillPendingReadRecoversOnceAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.db")
	st := openHistoryRecallStore(t, path)
	run := startedSkillRun(t, st)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{{ToolCalls: []llm.ToolCall{builtinReadCall(t, "frontend-design", "pending-front")}}, textResponse(rootActionResponse(domain.RootActionContinue, "Recovered exact guidance", "", ""))}}
	first, err := newToolLoopSupervisor(&failOnceToolResultStore{SQLiteStore: st, fail: true}, p).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeInternal || first.Checkpoint.Phase != domain.SupervisorTurnStarted {
		t.Fatalf("not recoverable: %v", err)
	}
	if reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID); err != nil || len(reads) != 0 {
		t.Fatal("uncommitted read activated", err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st = openHistoryRecallStore(t, path)
	defer st.Close()
	resumed, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID)
	if err != nil || !resumed.Recovered {
		t.Fatalf("not recovered: %+v %v", resumed, err)
	}
	reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(reads) != 1 {
		t.Fatalf("recovery success count=%d %v", len(reads), err)
	}
	requests := p.Requests()
	found := false
	for _, m := range requests[len(requests)-1].Messages {
		found = found || (m.Role == "system" && strings.Contains(m.Content, frontendBody(t)))
	}
	if !found {
		t.Fatal("recovered request omitted skill")
	}
}

type cancelBeforeSkillReceiptStore struct {
	*store.SQLiteStore
	stopped bool
}

func (s *cancelBeforeSkillReceiptStore) RecordSupervisorToolResult(ctx context.Context, cp domain.SupervisorCheckpoint, result domain.SupervisorToolResult) (domain.SupervisorToolCall, bool, error) {
	if !s.stopped {
		s.stopped = true
		if _, err := application.NewRunService(s.SQLiteStore).Cancel(ctx, cp.RunID); err != nil {
			return domain.SupervisorToolCall{}, false, err
		}
	}
	return s.SQLiteStore.RecordSupervisorToolResult(ctx, cp, result)
}
func TestBuiltinSkillCancellationBeforeReceiptCannotActivate(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "cancel.db"))
	defer st.Close()
	run := startedSkillRun(t, st)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{{ToolCalls: []llm.ToolCall{builtinReadCall(t, "frontend-design", "cancel-front")}}}}
	wrapper := &cancelBeforeSkillReceiptStore{SQLiteStore: st}
	_, err := newToolLoopSupervisor(wrapper, p).Step(t.Context(), run.ID)
	if err == nil || !wrapper.stopped {
		t.Fatalf("cancel did not stop publication: %v", err)
	}
	reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(reads) != 0 {
		t.Fatal("cancelled read activated", err)
	}
	if len(p.Requests()) != 1 {
		t.Fatal("model continued after cancelled read")
	}
	current, err := st.GetRun(t.Context(), run.ID)
	if err != nil || current.Status != domain.RunCancelled {
		t.Fatal("cancel state lost", err)
	}
}

func discoveredFrontend(t *testing.T, description string) toolgateway.SkillReadRequest {
	t.Helper()
	_, raw, ok := strings.Cut(description, " Available skills for this mode: ")
	var catalog []toolgateway.BuiltinSkillDescriptor
	if !ok || json.Unmarshal([]byte(raw), &catalog) != nil {
		t.Fatal("missing real skill catalog")
	}
	for _, item := range catalog {
		if item.Name == "plan-delivery" || item.Name == "loop-monitor" || item.Name == "run-skill-generator" {
			t.Fatalf("explicit-only skill advertised: %s", item.Name)
		}
	}
	for _, item := range catalog {
		if item.Name == "frontend-design" {
			return item.SkillReadRequest
		}
	}
	t.Fatal("frontend-design undiscoverable")
	return toolgateway.SkillReadRequest{}
}

func frontendBody(t *testing.T) string {
	t.Helper()
	r, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get("frontend-design")
	i, err := r.ReadForModel(m.Name, m.Version, m.ContentSHA256, skills.ExecutionContext{Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Profile: domain.ProfileCode, Role: domain.AgentRoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	return i.Content
}

// This captures HTTP bytes from the production adapter, not just ChatRequest.
func TestBuiltinSkillDiscoveryAndBodyReachActualProviderWire(t *testing.T) {
	body := frontendBody(t)
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		mu.Lock()
		count++
		index := count
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta any, finish any) {
			v := map[string]any{"model": "model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if index == 1 {
			var pin toolgateway.SkillReadRequest
			for _, tool := range wire.Tools {
				if tool.Function.Name == "skill_read" {
					pin = discoveredFrontend(t, tool.Function.Description)
				}
			}
			if pin.Name == "" {
				t.Error("wire omitted skill discovery")
			}
			for _, m := range wire.Messages {
				var content string
				_ = json.Unmarshal(m.Content, &content)
				if strings.Contains(content, body) {
					t.Error("body eagerly loaded before read")
				}
			}
			args, _ := json.Marshal(pin)
			send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "skill-wire-read", "type": "function", "function": map[string]any{"name": "skill_read", "arguments": string(args)}}}}, nil)
			send(map[string]any{}, "tool_calls")
		} else {
			found := false
			for _, m := range wire.Messages {
				var content string
				_ = json.Unmarshal(m.Content, &content)
				if m.Role == "system" && strings.Contains(content, body) && strings.Contains(content, "Model-requested embedded Skill") {
					found = true
				}
			}
			if !found {
				t.Error("next HTTP request omitted exact Go-restored skill guidance")
			}
			if index != 2 {
				t.Errorf("unexpected wire call %d", index)
			}
			send(map[string]any{"role": "assistant", "content": rootActionResponse(domain.RootActionContinue, "Inspected guidance", "", "")}, nil)
			send(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: {\"model\":\"model\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p, err := llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{Name: "skill-wire", BaseURL: server.URL, APIKey: "synthetic", DefaultModel: "model"})
	if err != nil {
		t.Fatal(err)
	}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	profile, err := router.HarnessProfile(ref)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = router.SetHarnessQualification(ref, llm.HarnessQualification{ProtocolVersion: llm.ModelHarnessProtocolVersion, BindingDigest: profile.BindingDigest, ToolCallsQualified: true, ToolResultsQualified: true, StrictJSONQualified: true, StreamingQualified: true, QualifiedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "wire.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "Inspect frontend skill delivery", Profile: "code", Surface: "code", Phase: "deliver", ModelRoute: p.Name() + "/model", Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewAgentRunner(st, router, policy.NewDefaultChecker()).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(reads) != 1 {
		t.Fatalf("no durable read: %v %v", reads, err)
	}
	if _, found, err := st.GetSkillSelectionByRun(t.Context(), run.ID); err != nil || found {
		t.Fatal("model read changed operator selection", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("wire requests=%d", count)
	}
}

func TestBuiltinSkillPersistsAcrossSegmentsCompactionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skill-recovery.db")
	st := openHistoryRecallStore(t, path)
	t.Cleanup(func() { _ = st.Close() })
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "ws-skill-recovery", Name: "skill-recovery", RootPath: workspace, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	wantNotes := domain.MaxSupervisorBoundaryContextCalls + 32
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Review the project using the complete conversation requirements", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "ws-skill-recovery", ModelRoute: "tool-loop/model", Interactive: true, NetworkMode: "disabled",
		Budget: domain.Budget{MaxTurns: 100, MaxToolCalls: int64(wantNotes + 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := frontendBody(t)
	p := newHistoryProtocolProvider(t, "tool-loop", "model")
	loaded := false
	notes := 0
	segments := 0
	p.handler = func(req llm.ChatRequest) (*llm.ChatResponse, error) {
		if !loaded {
			for _, spec := range req.Tools {
				if spec.Name == "skill_read" {
					pin := discoveredFrontend(t, spec.Description)
					raw, _ := json.Marshal(pin)
					loaded = true
					return toolResponse("discover-read", "skill_read", string(raw)), nil
				}
			}
			return nil, fmt.Errorf("no catalog")
		}
		n := 0
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, body) {
				n++
			}
		}
		if n != 1 {
			return nil, fmt.Errorf("restored body count=%d", n)
		}
		if len(req.Tools) == 0 {
			segments++
			return textResponse(rootActionResponse(domain.RootActionContinue, "Continue the same bounded work", "", "")), nil
		}
		if notes < wantNotes {
			batch := 6
			if rem := wantNotes - notes; rem < batch {
				batch = rem
			}
			calls := make([]llm.ToolCall, 0, batch)
			for i := 0; i < batch; i++ {
				notes++
				if notes == 1 {
					args, _ := json.Marshal(map[string]string{"title": "observation-1", "content": "Observed isolated fixture stage 1"})
					calls = append(calls, llm.ToolCall{ID: "note-1", Name: "note_create", Arguments: args})
				} else {
					calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("note-%d", notes), Name: "history_search", Arguments: json.RawMessage(historyRecallJSON(domain.HistorySearchRequest{Query: fmt.Sprintf("stage-%d", notes), Limit: 1}))})
				}
			}
			return toolBatchResponse(calls...), nil
		}
		return historyRecallFinish(), nil
	}
	turns := historyRecallTurns(st, p)
	submitHistoryRecall(t, turns, run.ID, "skill-load-and-boundaries", "Inspect this frontend fixture and keep the relevant guidance.")
	if notes != wantNotes || segments < 2 {
		t.Fatalf("did not cross receipt retention: notes=%d segments=%d", notes, segments)
	}
	for i := 0; i < 24; i++ {
		submitHistoryRecall(t, turns, run.ID, fmt.Sprintf("skill-pressure-%d", i), strings.Repeat("Historical context for the bounded rendering task. ", 45))
	}
	summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !found || summary.ID == 0 {
		t.Fatal("no actual durable compaction", err)
	}
	before, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(before) != 1 {
		t.Fatal("read not preserved", err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st = openHistoryRecallStore(t, path)
	submitHistoryRecall(t, historyRecallTurns(st, p), run.ID, "skill-after-restart", "Continue from the actual stored evidence.")
	after, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(after) != 1 || after[0].CallID != before[0].CallID || after[0].ResultJSON != before[0].ResultJSON {
		t.Fatal("restart repeated or rewrote successful read", err)
	}
	t.Logf("segments=%d notes=%d summary=%d exact read=%s", segments, notes, summary.ID, after[0].CallID)
}
