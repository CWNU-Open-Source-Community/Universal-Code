package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type liveFollowupSettings struct{ db *sql.DB }

func (r liveFollowupSettings) GetProviderSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM provider_setting WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return value, err == nil, err
}

// Opt-in only. The manifest is created by scripts/github-followup-live.ps1.
// Uses the production Thread executor, SQLite, GitHub client, model registry,
// FileEdit and command runtime. The harness owns the final commit/push.
func TestLivePRFollowupRepairsRealGitHubCI(t *testing.T) {
	if os.Getenv("TRAVERSE_GITHUB_REPAIR_LIVE") != "1" {
		t.Skip("explicit live GitHub/model repair not enabled")
	}
	ctx := t.Context()
	var fixture struct {
		Protocol, Repository, Root, Base, Head, URL string
		Number                                      int64
	}
	data, err := os.ReadFile(os.Getenv("TRAVERSE_GITHUB_LIVE_FIXTURE"))
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Protocol != "github-followup-live-fixture.v1" || !strings.HasPrefix(fixture.Head, "Qiyuanqiii/pr-followup-live-head-") || fixture.Number <= 0 {
		t.Fatal("invalid dedicated acceptance manifest")
	}
	command := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = fixture.Root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %s", name, out)
		}
		return strings.TrimSpace(string(out))
	}
	if command("git", "branch", "--show-current") != fixture.Head {
		t.Fatal("fixture branch changed")
	}
	reportPath := os.Getenv("TRAVERSE_GITHUB_LIVE_REPORT")
	if reportPath == "" {
		t.Fatal("an evidence output path is required")
	}
	evidenceRoot := filepath.Dir(reportPath)
	if err := os.MkdirAll(evidenceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"protocol": "github-followup-live-repair.v1", "url": fixture.URL, "started_at": time.Now().UTC(), "production_github_https": true, "real_model": true, "host_owns_commit_and_push": true, "success": false}
	defer func() {
		report["finished_at"] = time.Now().UTC()
		report["test_failed"] = t.Failed()
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(reportPath, append(b, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}()
	oracles := map[string]string{}
	for _, path := range []string{"sum_test.go", "go.mod", ".github/workflows/acceptance.yml"} {
		b, e := os.ReadFile(filepath.Join(fixture.Root, path))
		if e != nil {
			t.Fatal(e)
		}
		oracles[path] = fileedit.HashText(string(b))
	}
	beforeHead := command("git", "rev-parse", "HEAD")
	report["before_head"] = beforeHead
	secret, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		t.Fatal("active gh credential unavailable")
	}
	credentials := credential.NewMemoryStore()
	ref := githubreview.CredentialReference{Name: "live-followup", Kind: githubreview.AuthOAuthUser}
	if err := credentials.Put(ctx, ref.Name, strings.TrimSpace(string(secret))); err != nil {
		t.Fatal("could not hold GitHub credential in memory")
	}

	// Read the user's existing model registry without opening a writable store.
	settingsPath := os.Getenv("TRAVERSE_LIVE_MODEL_SETTINGS")
	if settingsPath == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			t.Fatal(e)
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
	if err := registry.LoadRouteSettings(ctx, liveFollowupSettings{source}); err != nil {
		t.Fatal(err)
	}
	modelRoute := os.Getenv("TRAVERSE_LIVE_MODEL_ROUTE")
	if modelRoute == "" {
		modelRoute = "deepseek/deepseek-v4-flash"
	}
	modelRef, err := llm.ParseModelRef(modelRoute)
	if err != nil || modelRef.Provider == "mock" {
		t.Fatal("a real registered model route is required")
	}
	profile, err := registry.Router().HarnessProfile(modelRef)
	if err != nil {
		t.Fatal(err)
	}
	report["model_route"] = modelRoute
	runtimePath := filepath.Join(evidenceRoot, fmt.Sprintf("runtime-%d.db", time.Now().UnixNano()))
	report["runtime_database"] = filepath.Base(runtimePath)
	st, err := store.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !profile.ToolCallsQualified || !profile.ToolResultsQualified || !profile.StreamingQualified || !profile.StrictJSONQualified {
		qualified, e := registry.QualifyHarness(ctx, st, modelRef.Provider, modelRef.Model)
		if e != nil {
			t.Fatal(e)
		}
		report["model_qualification"] = qualified
		if !qualified.Harness.RootEligible {
			t.Fatalf("existing model qualification failed: %s", qualified.Status)
		}
	}
	if err := os.MkdirAll(filepath.Join(fixture.Root, ".prayu"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := "protocol: project_config.v1\nformat_command_id: format.go\ntest_command_id: test.go\nhooks:\n  after_edit_format: true\n  before_finish_test: true\n"
	if err := os.WriteFile(filepath.Join(fixture.Root, ".prayu", "config.yaml"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	oracles[".prayu/config.yaml"] = fileedit.HashText(configText)
	// The budget counts every repeated input, including tool schemas and hook
	// continuations. Keep sufficient room for the whole repair, not one request.
	budget := domain.Budget{MaxTurns: 4, MaxTokens: 300000, MaxToolCalls: 16}
	project, rejections, err := projectconfig.ResolveWorkspace(ctx, fixture.Root, domain.ProfileCode, budget, toolgateway.TypedActionIDs())
	if err != nil || len(rejections) > 0 {
		t.Fatalf("project configuration: %v %v", err, rejections)
	}
	workspace := store.WorkspaceRecord{ID: "live-followup-workspace", Name: "isolated live acceptance", RootPath: fixture.Root, CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(st).Create(ctx, CreateRunRequest{Goal: "Repair the Sum regression in this isolated acceptance PR. Only edit sum.go. Keep sum_test.go, go.mod, .github/workflows and .prayu unchanged. Use workspace_read, workspace_change and workspace_apply. The trusted project hooks run formatting and real go tests automatically. Finish after successful tests. The acceptance harness will commit and push after checking the FileEdit and test receipts. Do not run git, access credentials, post comments or merge.", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID, ModelRoute: modelRoute, Interactive: true, Budget: budget, ProjectConfig: project})
	if err != nil {
		t.Fatal(err)
	}
	report["run_id"] = run.ID
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err := NewRunExecutionProfileService(st).Change(ctx, ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "live-followup-profile", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	thread := domain.InitialThreadID(run.ID)
	if _, err := NewThreadExecutionPermissionService(st, caps).Change(ctx, ChangeThreadExecutionPermissionRequest{ThreadID: thread, Mode: string(domain.RunExecutionPermissionFull), ConfirmFull: true, OperationKey: "live-followup-full", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	hooks := NewProjectHooksService(st)
	view, err := hooks.Get(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Review(ctx, thread, "trust", ProjectHooksReviewRequest{Version: ProjectHooksVersion, ExpectedFingerprint: view.Fingerprint, ExpectedRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "live-followup-command-owner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if e := manager.Shutdown(shutdown); e != nil {
			t.Error(e)
		}
	}()
	commands, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	handoff := NewRunExecutionHandoffService(st, registry.Router(), policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(commands)
	handoff.supervisor.WithProjectHooks(hooks)
	turns := NewThreadTurnServiceWithExecutionCapabilities(st, NewRunLifecycleControlService(st), handoff, caps)
	local, err := repository.NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	git := NewThreadGitService(st, local, nil, nil, nil, caps)
	advanced, err := repository.NewAdvancedExecutor(filepath.Join(evidenceRoot, "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	review, err := NewGitHubReviewService(st, credentials, advanced, caps)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := githubreview.ParseRepository(fixture.Repository)
	if err != nil {
		t.Fatal(err)
	}
	// The signed log URL stays in memory; only its exact observed host is allowed.
	var runs struct {
		Runs []struct {
			ID         int64  `json:"id"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	requestJSON := func(path string, output any) {
		t.Helper()
		b, e := exec.CommandContext(ctx, "gh", "api", path).Output()
		if e != nil || json.Unmarshal(b, output) != nil {
			t.Fatal("read acceptance workflow metadata failed")
		}
	}
	requestJSON(fmt.Sprintf("repos/%s/actions/runs?head_sha=%s&per_page=5", fixture.Repository, beforeHead), &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].Conclusion != "failure" {
		t.Fatal("expected one completed failing acceptance workflow before repair")
	}
	failedRun := runs.Runs[0].ID
	report["failed_workflow_id"] = failedRun
	var jobs struct {
		Jobs []struct {
			ID int64 `json:"id"`
		} `json:"jobs"`
	}
	requestJSON(fmt.Sprintf("repos/%s/actions/runs/%d/jobs", fixture.Repository, failedRun), &jobs)
	if len(jobs.Jobs) != 1 {
		t.Fatal("expected one acceptance job")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/actions/jobs/%d/logs", fixture.Repository, jobs.Jobs[0].ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
	request.Header.Set("X-GitHub-Api-Version", githubreview.RESTAPIVersion)
	response, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		t.Fatal("observe signed log host failed")
	}
	response.Body.Close()
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil || response.StatusCode != 302 || location.Scheme != "https" || location.Hostname() == "" {
		t.Fatal("GitHub did not provide a signed log redirect")
	}
	logHost := location.Hostname()
	report["reviewed_log_host"] = logHost
	configured, err := review.Configure(ctx, GitHubReviewConfigureRequest{ProtocolVersion: GitHubReviewAPIProtocolVersion, Repository: repo, Credential: ref, AllowedLogHosts: []string{logHost}, Enabled: true, RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPRFollowupService(st, review, git, turns)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	record, err := service.Create(ctx, thread, "live-followup", PRFollowupCreateRequest{Version: PRFollowupVersion, ConnectionID: configured.Connection.ID, PullRequest: fixture.Number, IntervalSeconds: 60, DeadlineAt: now.Add(12 * time.Minute), MaxTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	tick := func() domain.PRFollowupState {
		t.Helper()
		now = now.Add(time.Minute)
		found, e := service.RunDue(ctx, "live-followup-worker", now)
		if e != nil || !found {
			t.Fatalf("worker claimed=%v err=%v", found, e)
		}
		s, e := st.GetPRFollowup(ctx, record.ID)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	state := tick()
	report["repair_state"] = state.Record
	if state.Pending == nil || state.Pending.State != domain.PRFollowupTurnCompleted || state.Record.TurnsStarted != 1 {
		t.Fatalf("repair did not complete: status=%s result=%s error=%s", state.Record.Status, state.Record.LastResult, state.Record.LastError)
	}
	modelCalls := func() int {
		t.Helper()
		es, e := st.ListRunEvents(ctx, run.ID)
		if e != nil {
			t.Fatal(e)
		}
		n := 0
		for _, event := range es {
			if event.Type == events.ModelStartedEvent {
				n++
			}
		}
		return n
	}
	calls := modelCalls()
	report["repair_model_calls"] = calls
	if calls == 0 {
		t.Fatal("repair made no real model call")
	}
	edits, err := st.ListFileEdits(ctx, fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) == 0 {
		t.Fatal("repair produced no FileEdit receipt")
	}
	report["file_edit_count"] = len(edits)
	for _, edit := range edits {
		if edit.Path != "sum.go" || edit.Status != fileedit.StatusApplied {
			t.Fatalf("unexpected FileEdit receipt: path=%s status=%s", edit.Path, edit.Status)
		}
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	hookReceipts := map[string]string{}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.ProjectHook != nil && projectHookCommandPassed(call) {
				hookReceipts[call.ProjectHook.Slot] = call.CallID
			}
		}
	}
	if hookReceipts["format"] == "" || hookReceipts["test"] == "" {
		t.Fatal("missing successful host format/test hook receipts")
	}
	report["host_hook_receipts"] = hookReceipts
	commandJobs, err := st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: run.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(commandJobs) < 2 {
		t.Fatal("expected real format and test jobs")
	}
	jobSummaries := []map[string]any{}
	for _, job := range commandJobs {
		jobSummaries = append(jobSummaries, map[string]any{"id": job.ID, "executable": job.ExecutablePath, "state": job.State, "exit_code": job.ExitCode, "tree_reaped": job.TreeReaped})
		if job.State != runner.CommandRuntimeJobCompleted || job.ExitCode == nil || *job.ExitCode != 0 || !job.TreeReaped {
			t.Fatalf("local command did not pass: %s state=%s", job.ID, job.State)
		}
	}
	report["command_jobs"] = jobSummaries
	for path, expectedHash := range oracles {
		b, e := os.ReadFile(filepath.Join(fixture.Root, path))
		if e != nil || fileedit.HashText(string(b)) != expectedHash {
			t.Fatalf("acceptance oracle changed: %s", path)
		}
	}
	command("go", "test", "./...")
	command("git", "add", "sum.go")
	command("git", "commit", "-m", "fix: repair isolated Sum regression after real PR follow-up\n\nCo-authored-by: Codex <codex@openai.com>")
	command("git", "push", "origin", fixture.Head)
	afterHead := command("git", "rev-parse", "HEAD")
	report["after_head"] = afterHead
	if afterHead == beforeHead {
		t.Fatal("repair did not create a new head")
	}
	deadline := time.Now().Add(4 * time.Minute)
	var after githubreview.FollowupSnapshot
	for time.Now().Before(deadline) {
		after, err = service.readGitHub(ctx, configured.Connection.ID, fixture.Number)
		if err != nil {
			t.Fatal(err)
		}
		observation := observePRFollowup(after, nil, false)
		if after.State == githubreview.EvidenceVerified && after.PullRequest.HeadSHA == afterHead && len(after.WorkflowRuns) == 1 && observation.hasChecks && !observation.pending {
			if observation.failed {
				t.Fatal("new remote head CI failed")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Second):
		}
	}
	if after.PullRequest.HeadSHA != afterHead || len(after.WorkflowRuns) != 1 || after.WorkflowRuns[0].Conclusion != "success" {
		t.Fatal("new head did not reach real remote CI success before deadline")
	}
	report["passed_workflow_id"] = after.WorkflowRuns[0].ID
	report["after_snapshot_fingerprint"] = after.Fingerprint
	state = tick()
	if state.Record.Status != domain.PRFollowupReady || modelCalls() != calls {
		t.Fatalf("green remote read invoked model or failed readiness: %+v", state.Record)
	}
	// Ready subscriptions require explicit resume before another read-only poll.
	if _, err := service.Transition(ctx, thread, record.ID, "resume", PRFollowupTransitionRequest{Version: PRFollowupVersion, ExpectedRevision: state.Record.Revision}); err != nil {
		t.Fatal(err)
	}
	state = tick()
	if modelCalls() != calls || state.Record.TurnsStarted != 1 {
		t.Fatal("unchanged remote state called model again")
	}
	report["unchanged_poll_model_calls"] = 0
	report["final_state"] = state.Record
	report["oracles_unchanged"] = true
	report["success"] = true
	t.Logf("live_api=true model_calls=%d file_edits=%d real_command_jobs=%d remote_ci=success unchanged_poll_model_calls=0", calls, len(edits), len(commandJobs))
}
