package desktop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/store"
)

func TestControlPlaneObservationSelectionScopeIsProjectedFromComposition(t *testing.T) {
	plane, err := OpenControlPlane(ControlPlaneConfig{
		DatabasePath: filepath.Join(t.TempDir(), "observation-scope.db"),
		ReadToken:    desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		ScheduledJobControlEnabled: true, ScheduledJobWorkerEnabled: true,
		ScheduledJobConfirmedOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	response := desktopAPIRequest(plane.Handler(), "/api/v1/capabilities")
	var envelope struct {
		Data httpapi.RuntimeCapabilitiesView `json:"data"`
	}
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	worker := envelope.Data.ScheduledJobWorker
	if worker.SelectionScope != "confirmed_jobs" || worker.State != "ready" || !worker.Enabled {
		t.Fatalf("ordinary observer was misrepresented: %+v", worker)
	}
}

// Uses the production Desktop composition, real SQLite, HTTP controls and the
// real serial worker clock. No model, tool executor or external network exists.
func TestControlPlaneObservationSurvivesReopenWithoutReconfirmingLegacyJobs(t *testing.T) {
	config := ControlPlaneConfig{
		DatabasePath: filepath.Join(t.TempDir(), "observation.db"),
		ReadToken:    desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		ScheduledJobControlEnabled: true, ScheduledJobWorkerEnabled: true,
		ScheduledJobConfirmedOnly: true, AppVersion: "desktop-observation-test",
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
	workspace := store.WorkspaceRecord{ID: "workspace-observation-reopen", Name: "observe",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := first.stateStore.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, created, err := application.NewRunService(first.stateStore).Create(t.Context(),
		application.CreateRunRequest{Goal: "Observe state without model calls", Profile: string(domain.ProfileCode),
			Surface: string(domain.ExecutionSurfaceCode), Phase: string(domain.ExecutionPhasePlan),
			WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(first.stateStore).Start(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().UTC().Truncate(time.Second)
	path := "/api/v1/runs/" + created.ID + "/scheduled-jobs"
	body := fmt.Sprintf(`{"version":"scheduled-job.v1","schedule":{"kind":"periodic","timezone":"UTC","anchor_at":%q,"interval_seconds":60,"misfire_policy":"run_once"},"deadline_at":%q,"stop_on_target_terminal":true,"max_rounds":4,"max_model_calls":0,"max_elapsed_seconds":600,"retry":{"max_attempts":1,"initial_backoff_seconds":1,"max_backoff_seconds":1},"notification":"all","execution_mode":"read_only","confirm_repair":false`,
		anchor.Format(time.RFC3339), anchor.Add(10*time.Minute).Format(time.RFC3339))
	create := func(key, suffix string) domain.ScheduledJob {
		t.Helper()
		response := desktopControlRequest(first.Handler(), http.MethodPost, path, key, body+suffix)
		var envelope struct {
			Data httpapi.ScheduledJobControlView `json:"data"`
		}
		if response.Code != http.StatusAccepted {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data.Job
	}
	legacy := create("desktop-observation-legacy", `}`)
	confirmed := create("desktop-observation-confirmed", `,"observation_consent_version":1}`)
	read := func(plane *ControlPlane, id string) application.ScheduledJobSnapshot {
		t.Helper()
		response := desktopAPIRequest(plane.Handler(), "/api/v1/scheduled-jobs/"+id)
		var envelope struct {
			Data httpapi.ScheduledJobDetailView `json:"data"`
		}
		if response.Code != http.StatusOK {
			t.Fatalf("read: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data.Snapshot
	}
	waitRounds := func(plane *ControlPlane, count int, limit time.Duration) application.ScheduledJobSnapshot {
		t.Helper()
		end := time.Now().Add(limit)
		for time.Now().Before(end) {
			snapshot := read(plane, confirmed.ID)
			if snapshot.Job.RoundsCompleted >= count {
				return snapshot
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("production worker did not reach %d rounds within %s", count, limit)
		return application.ScheduledJobSnapshot{}
	}
	if err := first.StartWakeWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitRounds(first, 1, 5*time.Second)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open()
	if err := reopened.StartWakeWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := waitRounds(reopened, 2, 65*time.Second)
	if snapshot.Job.ObservationConsentVersion != 1 || snapshot.Job.ModelCalls != 0 {
		t.Fatalf("persisted consent or zero-model boundary changed: %+v", snapshot.Job)
	}
	for _, round := range snapshot.Rounds {
		if round.ModelCalled || round.ToolCalled {
			t.Fatal("observer executed a model or tool")
		}
	}
	legacyAfter := read(reopened, legacy.ID).Job
	if legacyAfter.Revision != legacy.Revision || legacyAfter.RoundsCompleted != 0 ||
		legacyAfter.ObservationConsentVersion != 0 || legacyAfter.Status != legacy.Status {
		t.Fatal("opening the desktop activated or reconciled an unconfirmed legacy job")
	}
	pause := desktopControlRequest(reopened.Handler(), http.MethodPost, path+"/"+confirmed.ID+"/pause",
		"desktop-observation-pause", fmt.Sprintf(`{"version":"scheduled-job-control.v1","expected_revision":%d}`, snapshot.Job.Revision))
	if pause.Code != http.StatusAccepted {
		t.Fatalf("pause: %d %s", pause.Code, pause.Body.String())
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	paused := open()
	// Exercise the worker actually installed by OpenControlPlane after restart.
	if _, err := paused.scheduledJobWorker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	final := read(paused, confirmed.ID).Job
	if final.Status != domain.ScheduledJobPaused || final.RoundsCompleted != snapshot.Job.RoundsCompleted || final.ObservationConsentVersion != 1 {
		t.Fatalf("reopening lost pause or consent: %+v", final)
	}
	t.Logf("real Desktop API: %d rounds across restart; legacy=%s/%d; confirmed=%s; model_calls=%d", final.RoundsCompleted, legacyAfter.Status, legacyAfter.RoundsCompleted, final.Status, final.ModelCalls)
}
