package application

import (
	"context"
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func TestAgentBrowserInternetSelectionIndependentOfExecutionPermission(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			service, st, r, supervisor, turn := newAgentBrowserFixture(t)
			st.base.executionPermission.Mode = mode
			st.base.run.Config.BrowserNetworkAccess, st.base.run.Config.BrowserNetworkRevision = domain.BrowserNetworkInternet, 1
			var launched browserruntime.AgentBrowserStartRequest
			original := service.launch
			service.launch = func(ctx context.Context, req browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
				launched = req
				return original(ctx, req)
			}
			caps, auth, err := supervisor.supervisorBrowserActionCapabilities(t.Context(), turn, st.base.executionPermission)
			if err != nil || !caps.Available {
				t.Fatalf("capabilities=%+v %v", caps, err)
			}
			var a toolgateway.AgentBrowserCallAuthority
			if err = json.Unmarshal(auth, &a); err != nil || a.Validate() != nil || a.BrowserNetworkAccess != domain.BrowserNetworkInternet || a.BrowserNetworkRevision != 1 {
				t.Fatalf("authority=%s %v", auth, err)
			}
			call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://example.org"}`)
			if _, err = runAgentBrowserFixtureCall(t, supervisor, turn, call); err != nil {
				t.Fatal(err)
			}
			if launched.NetworkMode != browserruntime.AgentBrowserNetworkInternet || len(launched.ScopedOrigins) != 0 || launched.ReviewNetworkWrite == nil {
				t.Fatalf("launch=%+v", launched)
			}
			fence := a.RunAuthorizationFence
			st.base.run.Config.BrowserNetworkAccess, st.base.run.Config.BrowserNetworkRevision = domain.BrowserNetworkOff, 2
			if err = service.check(t.Context(), a); err == nil {
				t.Fatal("disabled policy accepted old authority")
			}
			if err = service.InvalidateNetworkAccess(t.Context(), st.base.run.ID); err != nil {
				t.Fatal(err)
			}
			if !r.cancelled {
				t.Fatal("previous browser was not cancelled")
			}
			if !service.options.Capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(a.RunID, fence) {
				t.Fatal("browser change revoked unrelated command authority")
			}
		})
	}
}

func TestAgentBrowserRestrictedFullHonorsOriginsAndOffRetainsThem(t *testing.T) {
	s, st, _, _, _ := newAgentBrowserFixture(t)
	st.base.run.Config.BrowserNetworkAccess, st.base.run.Config.BrowserNetworkRevision = domain.BrowserNetworkRestricted, 1
	st.base.run.Config.BrowserOrigins = []string{"https://example.org"}
	a, err := s.authority(t.Context(), st.base.run.ID)
	if err != nil || a.Validate() != nil || len(a.DeclaredOrigins) != 1 {
		t.Fatalf("restricted Full=%+v %v", a, err)
	}
	mode, origins, err := normalizeBrowserNetworkAccess(domain.BrowserNetworkOff, []string{"https://example.org/path"})
	if err != nil || mode != domain.BrowserNetworkOff || len(origins) != 1 || origins[0] != "https://example.org:443" {
		t.Fatalf("off restriction=%s %v %v", mode, origins, err)
	}
}
