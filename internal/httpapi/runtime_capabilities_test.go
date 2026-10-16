package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/scheduler"
)

type wakeWorkerHealthFake struct {
	health application.RunWakeWorkerHealth
}

func (f wakeWorkerHealthFake) Health() application.RunWakeWorkerHealth { return f.health }

type scheduledWorkerHealthFake struct{ health scheduler.WorkerHealth }

func (f scheduledWorkerHealthFake) Health() scheduler.WorkerHealth { return f.health }
func (scheduledWorkerHealthFake) SelectionScope() string           { return "all_jobs" }

type runExecutionControllerFake struct{}

func (runExecutionControllerFake) ResumeRiskEscalation(context.Context, application.ResumeRiskEscalationRequest) (application.ResumeRiskEscalationResult, error) {
	return application.ResumeRiskEscalationResult{}, apperror.New(apperror.CodeFailedPrecondition, "unused risk resume test seam")
}

func (runExecutionControllerFake) Execute(context.Context,
	application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
	return application.ExecuteRunHandoffResult{}, nil
}

func TestRuntimeCapabilitiesAreReadOnlyAndDefaultClosed(t *testing.T) {
	fixture := newAPIFixture(t)
	response := performSessionMessageRequest(t, fixture.api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	var view RuntimeCapabilitiesView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if view.ProtocolVersion != RuntimeCapabilitiesProtocolVersion ||
		view.RunControlEnabled != fixture.api.controlEnabled ||
		view.RunCreationEnabled != fixture.api.runCreationEnabled ||
		view.WorkspaceImportEnabled ||
		view.SessionMessageEnabled != fixture.api.sessionMessageEnabled ||
		view.ThreadControlEnabled != (fixture.api.runCreationEnabled && fixture.api.sessionMessageEnabled) ||
		view.FileEditProposalEnabled || view.ProviderCredentialEnabled ||
		view.RunWakeWorkerEnabled || view.ScheduledJobControlEnabled ||
		view.ScheduledJobWorkerEnabled ||
		!view.AgentCodeToolsEnabled || view.CodeIntelEnabled ||
		!view.CommandRuntimeProtocolAvailable || view.CommandRuntimeAdapterInstalled ||
		view.CommandRuntimeAdapterReady || len(view.CommandRuntimeAdapters) != 0 ||
		view.CommandRuntimeEnabled || view.ProcessExecutionEnabled || view.ShellExecutionEnabled ||
		view.DockerExecutionEnabled || view.BatchDeliveryHostValidationEnabled ||
		view.WakeWorker.Enabled ||
		view.WakeWorker.State != "disabled" || view.WakeWorker.Active ||
		view.WakeWorker.RuntimeEnableSupported || view.WakeWorker.PersistentService ||
		view.WakeWorker.Concurrency != 1 || view.WakeWorker.MaxSteps != 1 ||
		view.ScheduledJobWorker.Enabled || view.ScheduledJobWorker.State != "disabled" ||
		view.ScheduledJobWorker.Active || view.ScheduledJobWorker.PersistentService ||
		view.ScheduledJobWorker.RuntimeEnableSupported ||
		view.ScheduledJobWorker.AuthorityEscalation ||
		view.ScheduledJobWorker.Concurrency != scheduler.WorkerConcurrency {
		t.Fatalf("default capability projection widened authority: %#v", view)
	}
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		"/api/v1/capabilities", testControlToken, "", "", nil),
		http.StatusUnauthorized, "POLICY_DENIED")
}

func TestRuntimeCapabilitiesProjectScheduledWorkerWithoutRuntimeAuthority(t *testing.T) {
	fixture := newAPIFixture(t)
	service := application.NewScheduledJobService(fixture.store)
	source := scheduledWorkerHealthFake{health: scheduler.WorkerHealth{
		ProtocolVersion: scheduler.WorkerHealthProtocolVersion,
		State:           scheduler.WorkerRunning, Active: false,
		PollIntervalMillis: scheduler.DefaultPollInterval.Milliseconds(),
		Concurrency:        scheduler.WorkerConcurrency,
	}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, ScheduledJobControlEnabled: true,
		ScheduledJobWorkerEnabled: true, ScheduledJobController: service,
		ScheduledJobWorkerHealthSource: source, AppVersion: "scheduled-worker-health-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	var view RuntimeCapabilitiesView
	decodeDataStatus(t, response, http.StatusOK, &view)
	worker := view.ScheduledJobWorker
	if !view.ScheduledJobControlEnabled || !view.ScheduledJobWorkerEnabled ||
		!worker.Enabled || worker.State != string(scheduler.WorkerRunning) ||
		worker.Active || worker.PollIntervalMillis != 2000 ||
		worker.Concurrency != scheduler.WorkerConcurrency || worker.PersistentService ||
		worker.RuntimeEnableSupported || worker.AuthorityEscalation {
		t.Fatalf("scheduled worker projection widened authority: %#v", view)
	}
	raw := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{"owner_id", "fence_token", "operation_key", "private_error"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("scheduled worker capability projection exposed %q: %s", forbidden, raw)
		}
	}
}

func TestRuntimeCapabilitiesProjectExplicitBatchHostValidation(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		BatchDeliveryControlEnabled:        true,
		BatchDeliveryController:            application.NewBatchDeliveryWorkbenchService(application.NewBatchDeliveryService(fixture.store)),
		BatchDeliveryHostValidationEnabled: true,
		ExecutionPermissionControlEnabled:  true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		},
		AppVersion: "batch-host-validation-capability-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	var view RuntimeCapabilitiesView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if !view.BatchDeliveryControlEnabled || !view.BatchDeliveryHostValidationEnabled {
		t.Fatalf("batch host validation capability projection is invalid: %#v", view)
	}
}

func TestRuntimeCapabilitiesRejectBatchHostValidationWithoutFullAccess(t *testing.T) {
	fixture := newAPIFixture(t)
	_, err := New(fixture.store, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		BatchDeliveryControlEnabled:        true,
		BatchDeliveryController:            application.NewBatchDeliveryWorkbenchService(application.NewBatchDeliveryService(fixture.store)),
		BatchDeliveryHostValidationEnabled: true,
		ExecutionPermissionControlEnabled:  true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true,
		},
		AppVersion: "batch-host-validation-rejection-test",
	})
	if err == nil || apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("batch host validation without full access error=%v", err)
	}
}

func TestRuntimeCapabilitiesEnableCommandRuntimeOnlyForFullAccessExecution(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		RunExecutionEnabled: true, RunExecutionController: runExecutionControllerFake{},
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		},
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{
			commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))},
		AppVersion: "command-runtime-capability-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	var view RuntimeCapabilitiesView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if !view.RunExecutionEnabled || !view.ExecutionPermissionControlEnabled ||
		!view.OperatorApprovalEnabled || !view.DangerFullAccessEnabled ||
		!view.CommandRuntimeProtocolAvailable || !view.CommandRuntimeAdapterInstalled ||
		!view.CommandRuntimeAdapterReady || len(view.CommandRuntimeAdapters) != 1 ||
		!view.CommandRuntimeEnabled || !view.ProcessExecutionEnabled ||
		!view.ShellExecutionEnabled || !view.AgentCodeToolsEnabled {
		t.Fatalf("command runtime capability projection is invalid: %#v", view)
	}
}

func TestRuntimeCapabilitiesProjectBoundedWorkerHealthWithoutPrivateState(t *testing.T) {
	fixture := newAPIFixture(t)
	source := wakeWorkerHealthFake{health: application.RunWakeWorkerHealth{
		ProtocolVersion: application.RunWakeWorkerHealthProtocolVersion,
		State:           application.RunWakeWorkerDraining, Active: true,
		PollIntervalMillis: (2 * time.Second).Milliseconds(),
		Concurrency:        application.RunWakeWorkerConcurrency,
		MaxSteps:           application.RunWakeWorkerMaxSteps,
	}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken:         testControlToken,
		RunWakeWorkerEnabled: true, RunWakeWorkerHealthSource: source,
		AppVersion: "worker-health-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	var view RuntimeCapabilitiesView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if !view.RunWakeWorkerEnabled || !view.WakeWorker.Enabled ||
		view.WakeWorker.State != "draining" || !view.WakeWorker.Active ||
		view.WakeWorker.PollIntervalMillis != 2000 ||
		view.WakeWorker.RuntimeEnableSupported || view.WakeWorker.PersistentService {
		t.Fatalf("worker health projection is invalid: %#v", view)
	}
	raw := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{"owner", "lease", "token", "private_error", "run_id"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("worker capability projection exposed %q: %s", forbidden, raw)
		}
	}
}

func TestRuntimeCapabilitiesRejectWorkerWithoutControlToken(t *testing.T) {
	fixture := newAPIFixture(t)
	_, err := New(fixture.store, Config{AccessToken: testAccessToken,
		RunWakeWorkerEnabled: true, RunWakeWorkerHealthSource: wakeWorkerHealthFake{
			health: application.RunWakeWorkerHealth{
				ProtocolVersion:    application.RunWakeWorkerHealthProtocolVersion,
				State:              application.RunWakeWorkerReady,
				PollIntervalMillis: application.DefaultRunWakeWorkerInterval.Milliseconds(),
				Concurrency:        application.RunWakeWorkerConcurrency,
				MaxSteps:           application.RunWakeWorkerMaxSteps,
			},
		}, AppVersion: "worker-control-token-test"})
	if err == nil || apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("worker without control token error=%v", err)
	}
}

func TestRuntimeCapabilitiesRejectImpossibleWorkerHealth(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunWakeWorkerEnabled: true,
		RunWakeWorkerHealthSource: wakeWorkerHealthFake{health: application.RunWakeWorkerHealth{
			ProtocolVersion: application.RunWakeWorkerHealthProtocolVersion,
			State:           application.RunWakeWorkerStopped, Active: true,
			PollIntervalMillis: application.DefaultRunWakeWorkerInterval.Milliseconds(),
			Concurrency:        application.RunWakeWorkerConcurrency,
			MaxSteps:           application.RunWakeWorkerMaxSteps,
		}}, AppVersion: "invalid-worker-health-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/capabilities", testAccessToken, "", "", nil)
	assertAPIError(t, response, http.StatusInternalServerError, "INTERNAL")
}

func TestRuntimeCapabilitiesRejectMismatchedWorkerConfiguration(t *testing.T) {
	fixture := newAPIFixture(t)
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		RunWakeWorkerEnabled: true}); err == nil {
		t.Fatal("enabled worker without health source was accepted")
	}
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		RunWakeWorkerHealthSource: wakeWorkerHealthFake{}}); err == nil {
		t.Fatal("disabled worker retained a health source")
	}
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ScheduledJobWorkerEnabled: true}); err == nil {
		t.Fatal("enabled scheduled worker without health source was accepted")
	}
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ScheduledJobWorkerHealthSource: scheduledWorkerHealthFake{}}); err == nil {
		t.Fatal("disabled scheduled worker retained a health source")
	}
}
