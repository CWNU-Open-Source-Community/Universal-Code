package httpapi

import (
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

// These interfaces group the routes installed by an existing capability gate.
// Keep the public controller seams narrow for read-only embedders and tests.
type extensionControlContracts interface {
	ExtensionScopedInventoryController
	ExtensionOnboardingController
	PluginLifecycleController
	MCPCredentialController
	MCPInputController
}

type threadExecutionContracts interface {
	ThreadExecutionController
	ThreadExecutionSnapshotController
	approvalContinuationController
}

func controllerContractError(name string) error {
	return apperror.New(apperror.CodeInvalidArgument,
		"HTTP API enabled capability requires controller contract: "+name)
}

func validateControllerContracts(store Store, config Config) error {
	if config.ExtensionControlEnabled {
		if _, ok := config.ExtensionController.(extensionControlContracts); !ok {
			return controllerContractError("ExtensionScopedInventoryController, ExtensionOnboardingController, PluginLifecycleController, MCPCredentialController, MCPInputController")
		}
	}
	if config.BatchDeliveryControlEnabled {
		if _, ok := config.BatchDeliveryController.(BatchDeliveryWorkbenchController); !ok {
			return controllerContractError("BatchDeliveryWorkbenchController")
		}
	}
	if config.RunExecutionEnabled {
		if _, ok := config.RunExecutionController.(RiskEscalationResumeController); !ok {
			return controllerContractError("RiskEscalationResumeController")
		}
		// Run-only embedders need no Thread controller. Once installed, its
		// execution reads, snapshots and approval continuation must agree.
		if config.ThreadTurnController != nil {
			if _, ok := config.ThreadTurnController.(threadExecutionContracts); !ok {
				return controllerContractError("ThreadExecutionController, ThreadExecutionSnapshotController, approvalContinuationController")
			}
		}
	}
	if config.RunCreationEnabled && config.SessionMessageEnabled && config.RunLifecycleEnabled &&
		config.RunExecutionEnabled && config.PlanDeliveryControlEnabled {
		if _, ok := config.ThreadTurnController.(ThreadPlanController); !ok {
			return controllerContractError("ThreadPlanController")
		}
	}
	if config.ScheduledJobControlEnabled {
		if _, ok := config.ScheduledJobController.(ScheduledJobObservationController); !ok {
			return controllerContractError("ScheduledJobObservationController")
		}
	}
	if config.ScheduledJobWorkerEnabled {
		if _, ok := config.ScheduledJobWorkerHealthSource.(ScheduledJobWorkerSelectionSource); !ok {
			return controllerContractError("ScheduledJobWorkerSelectionSource")
		}
	}
	if config.ProviderDefinitionEnabled {
		if _, ok := config.ProviderDefinitionController.(providerModelDiscoverer); !ok {
			return controllerContractError("providerModelDiscoverer")
		}
	}
	if config.ProviderSearchReadinessController != nil {
		if _, ok := config.ProviderSearchReadinessController.(searchDiagnosticsController); !ok {
			return controllerContractError("searchDiagnosticsController")
		}
	}
	if config.AgentBrowserController != nil {
		if _, ok := config.AgentBrowserController.(agentBrowserScreenshotReader); !ok {
			return controllerContractError("agentBrowserScreenshotReader")
		}
	}
	if config.FullCDPSessionControlEnabled {
		if _, ok := config.FullCDPSessionController.(application.FullCDPPreviewController); !ok {
			return controllerContractError("FullCDPPreviewController")
		}
	}
	if config.CommandRuntimeAdvertiser != nil {
		if _, ok := config.CommandRuntimeAdvertiser.(application.ThreadActivityCommandRuntimeSource); !ok {
			return controllerContractError("ThreadActivityCommandRuntimeSource")
		}
		if config.ThreadApplicationServiceController == nil {
			if _, ok := store.(application.ThreadApplicationServiceStore); !ok {
				return controllerContractError("ThreadApplicationServiceStore")
			}
			if _, ok := config.CommandRuntimeAdvertiser.(application.ThreadApplicationServiceCommandRuntime); !ok {
				return controllerContractError("ThreadApplicationServiceCommandRuntime")
			}
		}
	}
	return nil
}
