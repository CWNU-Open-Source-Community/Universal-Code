package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/toolgateway"
)

type threadWithoutPlans struct {
	ThreadTurnController
	threadExecutionContracts
}

type runtimeWithoutServices struct {
	toolgateway.CommandRuntimeAdvertiser
	application.ThreadActivityCommandRuntimeSource
}

// These are the concrete controllers installed by CLI/Desktop composition.
// Expanding a narrow route interface must also update its production owner.
var (
	_ application.FullCDPPreviewController               = (*application.FullCDPProductionService)(nil)
	_ extensionControlContracts                          = (*application.ExtensionControlService)(nil)
	_ threadExecutionContracts                           = (*application.ThreadTurnService)(nil)
	_ ThreadPlanController                               = (*application.ThreadTurnService)(nil)
	_ RiskEscalationResumeController                     = (*application.RunExecutionHandoffService)(nil)
	_ BatchDeliveryWorkbenchController                   = (*application.BatchDeliveryWorkbenchService)(nil)
	_ ScheduledJobObservationController                  = (*application.ScheduledJobService)(nil)
	_ providerModelDiscoverer                            = (*application.ProviderDefinitionService)(nil)
	_ searchDiagnosticsController                        = (*application.ProviderSearchReadinessService)(nil)
	_ agentBrowserScreenshotReader                       = (*application.AgentBrowserService)(nil)
	_ application.ThreadActivityCommandRuntimeSource     = (*application.CommandRuntimeMultiplexer)(nil)
	_ application.ThreadApplicationServiceCommandRuntime = (*application.CommandRuntimeMultiplexer)(nil)
)

func TestControllerContractsRejectNarrowedEnabledControllers(t *testing.T) {
	// Wrapping the base interface reproduces method-set loss without replacing
	// the production implementation with a permissive all-methods fake.
	for _, tc := range []struct {
		name     string
		config   Config
		contract string
	}{
		{"extensions", Config{ExtensionControlEnabled: true, ExtensionController: struct{ ExtensionController }{&application.ExtensionControlService{}}}, "ExtensionScopedInventoryController"},
		{"batch workbench", Config{BatchDeliveryControlEnabled: true, BatchDeliveryController: struct{ BatchDeliveryController }{&application.BatchDeliveryWorkbenchService{}}}, "BatchDeliveryWorkbenchController"},
		{"risk resume", Config{RunExecutionEnabled: true, RunExecutionController: struct{ RunExecutionController }{&application.RunExecutionHandoffService{}}}, "RiskEscalationResumeController"},
		{"thread execution", Config{RunExecutionEnabled: true, RunExecutionController: &application.RunExecutionHandoffService{}, ThreadTurnController: struct{ ThreadTurnController }{&application.ThreadTurnService{}}}, "ThreadExecutionController"},
		{"thread plan", Config{RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true, PlanDeliveryControlEnabled: true, RunExecutionController: &application.RunExecutionHandoffService{}, ThreadTurnController: threadWithoutPlans{&application.ThreadTurnService{}, &application.ThreadTurnService{}}}, "ThreadPlanController"},
		{"activity runtime", Config{CommandRuntimeAdvertiser: struct {
			toolgateway.CommandRuntimeAdvertiser
		}{&application.CommandRuntimeMultiplexer{}}}, "ThreadActivityCommandRuntimeSource"},
		{"scheduled observations", Config{ScheduledJobControlEnabled: true, ScheduledJobController: struct{ ScheduledJobController }{&application.ScheduledJobService{}}}, "ScheduledJobObservationController"},
		{"worker selection", Config{ScheduledJobWorkerEnabled: true, ScheduledJobWorkerHealthSource: struct{ ScheduledJobWorkerHealthSource }{scheduledWorkerHealthFake{}}}, "ScheduledJobWorkerSelectionSource"},
		{"model discovery", Config{ProviderDefinitionEnabled: true, ProviderDefinitionController: struct{ ProviderDefinitionController }{&application.ProviderDefinitionService{}}}, "providerModelDiscoverer"},
		{"search diagnostics", Config{ProviderSearchReadinessController: struct {
			ProviderSearchReadinessController
		}{&application.ProviderSearchReadinessService{}}}, "searchDiagnosticsController"},
		{"Full CDP preview", Config{FullCDPSessionControlEnabled: true, FullCDPSessionController: struct {
			application.FullCDPSessionController
		}{&application.FullCDPProductionService{}}}, "FullCDPPreviewController"},
		{"browser screenshot", Config{AgentBrowserController: struct{ AgentBrowserController }{&application.AgentBrowserService{}}}, "agentBrowserScreenshotReader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateControllerContracts(nil, tc.config)
			if apperror.CodeOf(err) != apperror.CodeInvalidArgument || !strings.Contains(err.Error(), tc.contract) {
				t.Fatalf("missing contract diagnostic: %v", err)
			}
		})
	}
}

func TestControllerContractsValidateDefaultRuntimeServices(t *testing.T) {
	f := newAPIFixture(t)
	runtime := &application.CommandRuntimeMultiplexer{}
	if err := validateControllerContracts(f.store, Config{CommandRuntimeAdvertiser: runtime}); err != nil {
		t.Fatal(err)
	}
	narrow := runtimeWithoutServices{runtime, runtime}
	if err := validateControllerContracts(f.store, Config{CommandRuntimeAdvertiser: narrow}); apperror.CodeOf(err) != apperror.CodeInvalidArgument || !strings.Contains(err.Error(), "ThreadApplicationServiceCommandRuntime") {
		t.Fatalf("missing service runtime accepted: %v", err)
	}
	if err := validateControllerContracts(struct{ Store }{f.store}, Config{CommandRuntimeAdvertiser: runtime}); apperror.CodeOf(err) != apperror.CodeInvalidArgument || !strings.Contains(err.Error(), "ThreadApplicationServiceStore") {
		t.Fatalf("missing service store accepted: %v", err)
	}
	// An explicit service controller replaces the default store/runtime bridge.
	if err := validateControllerContracts(struct{ Store }{f.store}, Config{CommandRuntimeAdvertiser: narrow, ThreadApplicationServiceController: application.NewThreadApplicationService(f.store)}); err != nil {
		t.Fatalf("explicit service controller rejected: %v", err)
	}
}

func TestControllerContractsPreserveDisabledOptionalCapabilities(t *testing.T) {
	f := newAPIFixture(t)
	_, err := New(f.store, Config{AccessToken: testAccessToken,
		FullCDPSessionController: struct {
			application.FullCDPSessionController
		}{&application.FullCDPProductionService{}},
		ExtensionController:          struct{ ExtensionController }{&application.ExtensionControlService{}},
		BatchDeliveryController:      struct{ BatchDeliveryController }{&application.BatchDeliveryWorkbenchService{}},
		RunExecutionController:       struct{ RunExecutionController }{&application.RunExecutionHandoffService{}},
		ThreadTurnController:         struct{ ThreadTurnController }{&application.ThreadTurnService{}},
		ScheduledJobController:       struct{ ScheduledJobController }{&application.ScheduledJobService{}},
		ProviderDefinitionController: struct{ ProviderDefinitionController }{&application.ProviderDefinitionService{}},
	})
	if err != nil {
		t.Fatalf("disabled capabilities required extra contracts: %v", err)
	}
}

func TestProductionExtensionContractsPublishCapabilitiesAndServeReads(t *testing.T) {
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	assertCapabilities := func(credentials bool) {
		t.Helper()
		var inventory ExtensionInventoryView
		decodeDataStatus(t, f.get(t, ExtensionInventoryPath+"?workspace_id="+f.workspace.ID), http.StatusOK, &inventory)
		c := inventory.Onboarding
		if c == nil || !c.MCPRegistration || !c.PluginImport || !c.PluginLifecycle || !c.HookDiagnostics || c.MCPCredentials != credentials {
			t.Fatalf("production controller silently lost a capability: %+v", c)
		}
	}
	assertCapabilities(false) // The credential backend remains explicitly optional.
	controller.WithCredentials(credential.NewMemoryStore())
	assertCapabilities(true)
	for _, path := range []string{ExtensionHookDiagnosticsPath + "?workspace_id=" + f.workspace.ID, "/api/v1/runs/" + f.run.ID + "/mcp-inputs"} {
		response := f.get(t, path)
		if response.Code != http.StatusOK {
			t.Fatalf("production route %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestProductionThreadContractsPublishExecutionRead(t *testing.T) {
	f := newAPIFixture(t)
	controller := application.NewThreadTurnService(f.store, nil, nil)
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		RunExecutionEnabled: true, RunExecutionController: &application.RunExecutionHandoffService{}, ThreadTurnController: controller})
	if err != nil {
		t.Fatal(err)
	}
	var capabilities RuntimeCapabilitiesView
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/capabilities", testAccessToken, "", "", nil), http.StatusOK, &capabilities)
	if capabilities.ThreadExecutionReadEnabled == nil || !*capabilities.ThreadExecutionReadEnabled {
		t.Fatal("production Thread execution read silently disabled")
	}
	thread, err := f.store.GetThreadByRun(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var state application.ThreadExecutionState
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/threads/"+thread.ID+"/execution", testAccessToken, "", "", nil), http.StatusOK, &state)
	if state.ThreadID != thread.ID || state.CapabilityGrant {
		t.Fatalf("invalid read projection: %+v", state)
	}
}
