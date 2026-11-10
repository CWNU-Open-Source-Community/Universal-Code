package application

import (
	"context"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerCatalogueUsesIndependentBrowserNetworkSelection(t *testing.T) {
	for _, test := range []struct {
		permission domain.RunExecutionPermissionMode
		network    domain.BrowserNetworkAccess
	}{
		{domain.RunExecutionPermissionFull, domain.BrowserNetworkOff},
		{domain.RunExecutionPermissionAsk, domain.BrowserNetworkInternet},
		{domain.RunExecutionPermissionAuto, domain.BrowserNetworkInternet},
		{domain.RunExecutionPermissionFull, domain.BrowserNetworkRestricted},
		{domain.RunExecutionPermissionAsk, domain.BrowserNetworkRestricted},
	} {
		t.Run(string(test.permission)+"/"+string(test.network), func(t *testing.T) {
			st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
			if test.permission != domain.RunExecutionPermissionFull {
				if _, err := NewRunExecutionPermissionService(st, caps).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(test.permission), OperationKey: "worker-browser-permission-0001", RequestedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			if test.network != domain.BrowserNetworkOff {
				var origins []string
				if test.network == domain.BrowserNetworkRestricted {
					origins = []string{"https://example.org"}
				}
				if _, err := ChangeBrowserNetworkAccess(t.Context(), st, nil, domain.ChangeBrowserNetworkAccessRequest{Version: domain.BrowserNetworkAccessVersion, RunID: run.ID, ExpectedRevision: 1, BrowserNetworkAccess: test.network, BrowserOrigins: origins, OperationKey: "worker-browser-selection-0001", RequestedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			worker := newWorkerCommandTestActor(t, st, &CommandRuntimeService{}, run, root, lease, &domain.WorkerProfile{})
			browser := NewAgentBrowserService(st, AgentBrowserOptions{HomePath: t.TempDir(), Capabilities: caps})
			browser.available = true
			t.Cleanup(func() { _ = browser.Shutdown(context.Background()) })
			parent := NewAgentRunner(st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithAgentBrowser(browser)
			catalogue, err := parent.workerToolCatalogue(t.Context(), worker.ref)
			if err != nil {
				t.Fatal(err)
			}
			_, hasBrowser := catalogue.tools[string(toolgateway.BrowserNavigateTool)]
			if hasBrowser != (test.network != domain.BrowserNetworkOff) {
				t.Fatalf("browser=%t tools=%v", hasBrowser, catalogue.specs())
			}
			if _, hasRead := catalogue.tools[string(toolgateway.WorkspaceReadTool)]; !hasRead {
				t.Fatal("browser choice removed unrelated workspace tools")
			}
		})
	}
}
