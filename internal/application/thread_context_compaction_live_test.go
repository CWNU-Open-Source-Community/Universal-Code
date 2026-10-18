package application_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

const liveCompactionPin = "所有后续答复使用中文，接口名 LegacyLookup 必须原样保留；只报告已知事实，不调用工具，不修改文件，不执行命令，不访问网络。"
const liveCompactionPending = "未完成工作：核验 LegacyLookup 返回 418 时旧客户端的兼容性。尚未读取实现或运行验证，兼容性审查仍未完成；后续背景记录不代表这项工作已经完成。"

type liveCompactionSettings struct{ db *sql.DB }

func (r liveCompactionSettings) GetProviderSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM provider_setting WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

type liveCompactionHTTPCall struct {
	Phase              string `json:"phase"`
	Round              int    `json:"round"`
	Host               string `json:"host"`
	RequestSHA256      string `json:"request_sha256"`
	RequestBytes       int    `json:"request_bytes"`
	HasFixedPin        bool   `json:"has_fixed_pin"`
	HasPreviousSummary bool   `json:"has_previous_summary"`
	HTTPStatus         int    `json:"http_status"`
	TransportError     bool   `json:"transport_error"`
}

// Instrument the existing production HTTP transport, without replacing a model
// response, credentials, provider configuration, or the Thread execution chain.
// No headers, URLs with queries, raw requests, or credentials enter the report.
type liveCompactionTransport struct {
	base        http.RoundTripper
	mu          sync.Mutex
	phase       string
	round       int
	previousSHA string
	calls       []liveCompactionHTTPCall
	blocked     int
}

func (p *liveCompactionTransport) mark(phase string, round int, previousSHA string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase, p.round, p.previousSHA = phase, round, previousSHA
}

func (p *liveCompactionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	p.mu.Lock()
	if len(p.calls) >= 16 {
		p.blocked++
		p.mu.Unlock()
		return nil, errors.New("live compaction acceptance reached its 16-request limit")
	}
	index := len(p.calls)
	p.calls = append(p.calls, liveCompactionHTTPCall{
		Phase: p.phase, Round: p.round, Host: request.URL.Hostname(),
		RequestSHA256: session.ContentSHA256(string(body)), RequestBytes: len(body),
		HasFixedPin:        strings.Contains(string(body), liveCompactionPin),
		HasPreviousSummary: p.previousSHA != "" && strings.Contains(string(body), p.previousSHA),
	})
	p.mu.Unlock()
	response, err := p.base.RoundTrip(request)
	p.mu.Lock()
	p.calls[index].TransportError = err != nil
	if response != nil {
		p.calls[index].HTTPStatus = response.StatusCode
	}
	p.mu.Unlock()
	return response, err
}

type liveCompactionRound struct {
	Round          int                                  `json:"round"`
	Compaction     domain.ThreadContextCompactionResult `json:"compaction"`
	Summary        contextmgr.Summary                   `json:"summary"`
	AssistantReply string                               `json:"assistant_reply"`
	Checks         map[string]bool                      `json:"checks"`
}

func liveCompactionSummaryMarker(t *testing.T, summary contextmgr.Summary) string {
	t.Helper()
	if summary.ID == 0 {
		return ""
	}
	var envelope struct {
		Generated struct {
			Text   string `json:"text"`
			SHA256 string `json:"text_sha256"`
		} `json:"generated"`
	}
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil || envelope.Generated.Text == "" || envelope.Generated.SHA256 != session.ContentSHA256(envelope.Generated.Text) {
		t.Fatal("generated summary marker does not match its stored text")
	}
	return envelope.Generated.SHA256
}

// Explicitly opt in: this spends real model calls using the existing DeepSeek
// setup. Four ordinary Thread turns are followed by three manual compactions
// and three continuations. Expected: 10 calls, plus at most 2 qualification
// probes if the saved qualification has expired. The HTTP hard cap is 16.
// Manual triggering is deliberate; automatic pressure is covered separately by
// TestThreadGeneratedCompactionDefaultTwoPassesKeepSourcesAndUsage.
func TestLiveThreadThreeCompactionsPreservePinsAndUnfinishedWork(t *testing.T) {
	if os.Getenv("TRAVERSE_CONTEXT_COMPACTION_LIVE") != "1" {
		t.Skip("real-model compaction acceptance is opt-in")
	}
	reportPath := os.Getenv("TRAVERSE_CONTEXT_COMPACTION_LIVE_REPORT")
	if reportPath == "" {
		t.Fatal("TRAVERSE_CONTEXT_COMPACTION_LIVE_REPORT is required")
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	report := map[string]any{
		"protocol": "thread-compaction-live.v1", "started_at": time.Now().UTC(),
		"real_model": true, "trigger": "manual_thread_compaction", "http_request_limit": 16,
		"fixed_pin": liveCompactionPin, "original_unfinished_work": liveCompactionPending, "settings_read_only": true,
		"judgment_boundary": "One bounded DeepSeek conversation; literal language/interface/418/unfinished markers and real tool/file effects are checked. This is not an estimate of general long-conversation reliability or automatic-trigger coverage.",
		"passed":            false,
	}
	trace := &liveCompactionTransport{base: http.DefaultTransport}
	priorTransport := http.DefaultTransport
	http.DefaultTransport = trace
	defer func() { http.DefaultTransport = priorTransport }()
	var rounds []liveCompactionRound
	defer func() {
		trace.mu.Lock()
		report["http_calls"], report["http_request_count"], report["blocked_requests"] = trace.calls, len(trace.calls), trace.blocked
		trace.mu.Unlock()
		report["rounds"], report["finished_at"], report["test_failed"] = rounds, time.Now().UTC(), t.Failed()
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(reportPath, append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()

	settingsPath := os.Getenv("TRAVERSE_LIVE_MODEL_SETTINGS")
	if settingsPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		settingsPath = filepath.Join(home, ".cyberagent-workbench", "cyberagent.db")
	}
	source, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(settingsPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	registry, err := modelregistry.NewFromEnvironmentWithCredentials(credential.NewSystemStore())
	if err != nil {
		t.Fatal("existing model credentials unavailable")
	}
	if err := registry.LoadRouteSettings(ctx, liveCompactionSettings{source}); err != nil {
		t.Fatal(err)
	}
	modelRoute := os.Getenv("TRAVERSE_LIVE_MODEL_ROUTE")
	if modelRoute == "" {
		modelRoute = "deepseek/deepseek-v4-flash"
	}
	ref, err := llm.ParseModelRef(modelRoute)
	if err != nil || ref.Provider != "deepseek" {
		t.Fatal("an existing DeepSeek model route is required")
	}
	report["model_route"] = modelRoute
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "live-compaction.db"))
	defer st.Close()
	profile, err := registry.Router().HarnessProfile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.ToolCallsQualified || !profile.ToolResultsQualified || !profile.StrictJSONQualified || !profile.StreamingQualified {
		trace.mark("qualification", 0, "")
		result, err := registry.QualifyHarness(ctx, st, ref.Provider, ref.Model)
		report["qualification"] = result
		if err != nil || !result.Harness.RootEligible {
			t.Fatalf("existing model qualification failed: status=%s err=%v", result.Status, err)
		}
	}
	workspace := t.TempDir()
	const fileBody = "Live acceptance workspace: no model tools or writes are requested.\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(fileBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "live-compaction-workspace", Name: "live compaction", RootPath: workspace, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "记录接口兼容性审查的已有状态，等待后续输入，不执行审查。", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "live-compaction-workspace", ModelRoute: modelRoute, Interactive: true, NetworkMode: "disabled",
		Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 2, MaxTokens: 300000},
	})
	if err != nil {
		t.Fatal(err)
	}
	permission, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	report["run_id"], report["session_id"] = run.ID, run.SessionID
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, registry.Router(), policy.NewDefaultChecker()))
	submit := func(key, content string) {
		t.Helper()
		result, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: content, OperationKey: key, RequestedBy: "live_acceptance"})
		assertHistoryRecallSettled(t, result, err)
		if result.Submission.Run.ID != run.ID || len(historyRecallCalls(t, st, run.ID)) != 0 {
			t.Fatal("the live continuation changed Run or executed an unrequested tool")
		}
	}
	inputs := []string{
		liveCompactionPin + " 仅确认收到这些持续要求，本轮简短答复即可。",
		liveCompactionPending + " 仅确认已记录，不执行核验。",
		"背景记录甲：这份审查计划先记录现状，再等待另行安排；目前没有新增实现证据。仅回复收到。",
		"背景记录乙：本轮只补充会议记录，参与者计划下周讨论测试环境；没有新增验证结果。仅回复收到。",
	}
	for index, content := range inputs {
		trace.mark("initial", 0, "")
		submit(fmt.Sprintf("live-compaction-initial-%02d", index+1), content)
		if index == 0 {
			messages, err := st.ListSessionMessages(ctx, run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			var sourceID int64
			for _, message := range messages {
				if message.Role == "user" && strings.Contains(message.Content, liveCompactionPin) {
					sourceID = message.ID
				}
			}
			add := pinControl(domain.InitialThreadID(run.ID), "add", "live-compaction-fixed-pin", liveCompactionPin, 0)
			add.SourceMessageID = sourceID
			pinned, err := turns.ControlContextPin(ctx, add)
			if err != nil || len(pinned.State.Pins) != 1 || sourceID == 0 {
				t.Fatalf("fixed user requirement could not be pinned: %v", err)
			}
			report["pin"] = pinned.State.Pins[0]
		}
	}
	var previous contextmgr.Summary
	for round := 1; round <= 3; round++ {
		trace.mark("compaction", round, liveCompactionSummaryMarker(t, previous))
		request := manualCompactRequest(run, fmt.Sprintf("live-%02d", round))
		request.Focus = "" // The model receives no repeated hint of the expected unfinished work.
		compacted, err := application.NewThreadContextCompactionService(st, registry.Router()).Compact(ctx, request)
		if err != nil || compacted.Status != "completed" || !compacted.Generated || compacted.InputMessagesOmitted != 0 {
			t.Fatalf("round %d did not commit a complete generated summary: %+v err=%v", round, compacted, err)
		}
		summary, found, err := st.LatestContextSummary(ctx, run.SessionID)
		if err != nil || !found || summary.PreviousSummaryID != previous.ID || summary.ID != compacted.SummaryID {
			t.Fatalf("round %d summary chain mismatch: %v", round, err)
		}
		trace.mark("continuation", round, liveCompactionSummaryMarker(t, summary))
		submit(fmt.Sprintf("live-compaction-followup-%02d", round), "请继续：用最多三句报告目前的接口、未完成工作和下一步。")
		messages, err := st.ListSessionMessages(ctx, run.SessionID, true)
		if err != nil {
			t.Fatal(err)
		}
		var reply string
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Role == "assistant" {
				// CompleteSupervisorTurn stores RootAction.Message as public
				// history, after validating the provider's internal JSON.
				reply = messages[index].Content
				break
			}
		}
		checks := map[string]bool{
			"exact_interface":      strings.Contains(reply, "LegacyLookup"),
			"unfinished_work":      strings.Contains(reply, "未完成") || strings.Contains(reply, "尚未") || strings.Contains(reply, "待核验"),
			"original_status_code": strings.Contains(reply, "418"),
			"chinese_reply":        regexp.MustCompile(`[\p{Han}]`).MatchString(reply) && !regexp.MustCompile(`[A-Za-z]{3,}`).MatchString(strings.ReplaceAll(reply, "LegacyLookup", "")),
		}
		rounds = append(rounds, liveCompactionRound{Round: round, Compaction: compacted, Summary: summary, AssistantReply: reply, Checks: checks})
		for name, passed := range checks {
			if !passed {
				t.Errorf("round %d failed %s: %s", round, name, reply)
			}
		}
		t.Logf("real compaction round=%d summary=%d previous=%d removed=%d reply=%s", round, summary.ID, summary.PreviousSummaryID, compacted.RemovedMessages, reply)
		previous = summary
	}
	trace.mu.Lock()
	phaseCounts := map[string]int{}
	for _, call := range trace.calls {
		phaseCounts[call.Phase]++
		if call.Phase == "continuation" && (!call.HasFixedPin || !call.HasPreviousSummary) || call.Phase == "compaction" && call.Round > 1 && !call.HasPreviousSummary {
			t.Error("an actual outgoing request lost the fixed pin or inherited summary")
		}
	}
	trace.mu.Unlock()
	report["calls_by_phase"] = phaseCounts
	if phaseCounts["initial"] < 4 || phaseCounts["compaction"] != 3 || phaseCounts["continuation"] < 3 {
		t.Error("HTTP observations did not cover the real Thread and compaction calls")
	}
	all, err := st.ListSessionMessages(ctx, run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	originalsRetained := true
	for _, content := range inputs[:2] {
		found := false
		for _, message := range all {
			if message.Role == "user" && message.Content == content && message.Compacted && message.Provenance.ContentSHA256 == session.ContentSHA256(content) {
				found = true
			}
		}
		originalsRetained = originalsRetained && found
	}
	afterPermission, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil || permission != afterPermission {
		t.Fatal("model compaction changed execution permission")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "README.md"))
	if err != nil || string(data) != fileBody || !originalsRetained {
		t.Fatal("workspace bytes or original compacted user messages changed")
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil || usage.ContextCompactionTokens <= 0 || usage.RootTokens <= 0 || usage.Validate() != nil {
		t.Fatalf("real model usage was not accounted for: %+v err=%v", usage, err)
	}
	report["usage"], report["originals_retained"], report["tool_calls"], report["workspace_unchanged"], report["permission_unchanged"] = usage, originalsRetained, 0, true, true
	report["passed"] = !t.Failed()
}
