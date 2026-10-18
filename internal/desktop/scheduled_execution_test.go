package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
)

// Exercises the real desktop composition and HTTP controls with a deterministic
// model transport. The timer, Thread executor and persistence are production code.
func TestControlPlaneAutomaticScheduleExecutesWithoutRendererAndDoesNotReplayOnReopen(t *testing.T) {
	const model = "scheduled-desktop-model"
	const prompt = "Report the scheduled task result"
	var calls atomic.Int32
	qualification := &anthropicHarnessFixture{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 128*1024))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		phase, nonce, err := qualification.phase(raw)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch phase {
		case "tool_result":
			writeAnthropicTextSSE(t, w, model, fmt.Sprintf(`{"version":"model_harness_probe.v1","status":"ok","nonce":%q}`, nonce))
		case "tool_call":
			writeAnthropicToolSSE(t, w, model, nonce)
		default:
			if !strings.Contains(string(raw), prompt) {
				t.Error("the scheduled instruction did not reach the model")
			}
			writeAnthropicTextSSE(t, w, model, `{"version":"root_lifecycle.v1","action":"finish","message":"Scheduled task complete","summary":"Scheduled task complete"}`)
		}
	}))
	defer provider.Close()
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "scheduled-desktop-secret")
	t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
	t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
	root := t.TempDir()
	config := ControlPlaneConfig{
		DatabasePath: filepath.Join(root, "automatic.db"), HomePath: root,
		ReadToken: desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true,
		RunExecutionEnabled: true, ModelControlEnabled: true,
		ScheduledJobControlEnabled: true, ScheduledJobWorkerEnabled: true, ScheduledJobConfirmedOnly: true,
		CredentialStore: credential.NewMemoryStore(), AppVersion: "scheduled-execution-test",
	}
	open := func() *ControlPlane {
		t.Helper()
		plane, err := OpenControlPlane(config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = plane.Close() })
		return plane
	}
	first := open()
	workspace, err := first.RegisterWorkspaceDirectory(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	qualified := desktopControlRequest(first.Handler(), http.MethodPost, httpapi.ModelHarnessQualificationPath,
		"scheduled-model-qualification", fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`, modelregistry.HarnessQualificationProtocolVersion, model))
	if qualified.Code != http.StatusAccepted {
		t.Fatalf("qualification: %d %s", qualified.Code, qualified.Body.String())
	}
	route := desktopControlRequest(first.Handler(), http.MethodPost, "/api/v1/models/routes/code",
		"scheduled-model-route", fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`, modelregistry.RouteControlProtocolVersion, model))
	if route.Code != http.StatusAccepted {
		t.Fatalf("route: %d %s", route.Code, route.Body.String())
	}
	created := desktopControlRequest(first.Handler(), http.MethodPost, "/api/v1/threads", "scheduled-desktop-thread",
		fmt.Sprintf(`{"version":%q,"goal":"Run a scheduled check","workspace_id":%q,"profile":"code","surface":"code","phase":"deliver"}`, domain.ThreadCreationProtocolVersion, workspace.ID))
	if created.Code != http.StatusAccepted {
		t.Fatalf("thread: %d %s", created.Code, created.Body.String())
	}
	var thread httpapi.ThreadCreationControlView
	decodeDesktopControlData(t, created, &thread)
	anchor := time.Now().UTC().Add(-time.Second)
	body, err := json.Marshal(httpapi.ScheduledJobCreateRequestView{
		Version:    domain.ScheduledJobProtocolVersion,
		Schedule:   httpapi.ScheduledJobScheduleRequestView{Kind: domain.ScheduledJobOnce, Timezone: "UTC", AnchorAt: anchor, MisfirePolicy: domain.ScheduledJobMisfireRunOnce},
		DeadlineAt: anchor.Add(time.Hour), MaxRounds: 1, MaxElapsedSeconds: 3600,
		Retry:        domain.ScheduledJobRetryPolicy{MaxAttempts: 2, InitialBackoffSeconds: 1, MaxBackoffSeconds: 2},
		Notification: domain.ScheduledJobNotifyAll, ExecutionMode: domain.ScheduledJobAutomatic,
		ExecutionTarget:         &domain.ScheduledJobExecutionTarget{Kind: "thread", ThreadID: thread.Thread.ID, Prompt: prompt},
		ExecutionConsentVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + thread.Run.ID + "/scheduled-jobs"
	response := desktopControlRequest(first.Handler(), http.MethodPost, path, "scheduled-desktop-create", string(body))
	if response.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var control httpapi.ScheduledJobControlView
	decodeDesktopControlData(t, response, &control)
	if control.ExecutionStarted || control.AuthorityBypass || calls.Load() != 2 {
		t.Fatalf("creation executed work or changed authority: %+v calls=%d", control, calls.Load())
	}
	if err := first.StartWakeWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	read := func(plane *ControlPlane) application.ScheduledJobSnapshot {
		t.Helper()
		response := desktopAPIRequest(plane.Handler(), "/api/v1/scheduled-jobs/"+control.Job.ID)
		if response.Code != http.StatusOK {
			t.Fatalf("read: %d %s", response.Code, response.Body.String())
		}
		var detail httpapi.ScheduledJobDetailView
		decodeDesktopControlData(t, response, &detail)
		return detail.Snapshot
	}
	end := time.Now().Add(10 * time.Second)
	var snapshot application.ScheduledJobSnapshot
	for time.Now().Before(end) {
		snapshot = read(first)
		if snapshot.Job.Status.Terminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snapshot.Job.Status != domain.ScheduledJobCompleted || snapshot.Job.RoundsCompleted != 1 ||
		len(snapshot.Executions) != 1 || snapshot.Executions[0].State != "completed" || len(snapshot.Notifications) == 0 || calls.Load() != 3 {
		runEvents, _ := first.stateStore.ListRunEvents(t.Context(), thread.Run.ID)
		for _, event := range runEvents {
			if strings.Contains(event.Type, "failed") || strings.Contains(event.Type, "blocked") || strings.Contains(event.Type, "rejected") {
				t.Logf("%s: %s", event.Type, event.PayloadJSON)
			}
		}
		t.Fatalf("host did not complete exactly one scheduled task: %+v calls=%d", snapshot, calls.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open()
	if _, err := reopened.scheduledJobWorker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	final := read(reopened)
	if final.Job.RoundsCompleted != 1 || len(final.Executions) != 1 || calls.Load() != 3 {
		t.Fatalf("reopening duplicated execution: %+v calls=%d", final, calls.Load())
	}
}
