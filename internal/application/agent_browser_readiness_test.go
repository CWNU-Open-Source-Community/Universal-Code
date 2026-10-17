package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type browserReadinessTest struct{ ready bool }

func (c browserReadinessTest) CheckAgentBrowserService(_ context.Context, a toolgateway.AgentBrowserCallAuthority, jobID, url string) (runner.CommandServiceReadiness, error) {
	state := "not_ready"
	if c.ready {
		state = "ready"
	}
	return runner.CommandServiceReadiness{JobID: jobID, URL: url, State: state, Verified: c.ready}, nil
}

type browserReadinessNavigation struct {
	*agentBrowserTestRuntime
	canonicalURL string
}

func (r *browserReadinessNavigation) Navigate(ctx context.Context, requested string) (browserruntime.AgentBrowserNavigation, error) {
	n, err := r.agentBrowserTestRuntime.Navigate(ctx, requested)
	n.CanonicalURL = r.canonicalURL
	return n, err
}

func TestAgentBrowserServiceNavigationCanonicalOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, requested, observed string
		allowed                   bool
	}{
		{"http default port omitted", "http://127.0.0.1:80/", "http://127.0.0.1/", true},
		{"https default port omitted", "https://127.0.0.1:443/", "https://127.0.0.1/", true},
		{"different port", "http://127.0.0.1:34567/", "http://127.0.0.1:34568/", false},
		{"different scheme", "http://127.0.0.1:80/", "https://127.0.0.1/", false},
		{"different host", "http://127.0.0.1:34567/", "http://127.0.0.2:34567/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, st, runtime, supervisor, turn := newAgentBrowserFixture(t)
			service.options.ServiceReadiness = browserReadinessTest{ready: true}
			launch := service.launch
			service.launch = func(ctx context.Context, req browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
				_, err := launch(ctx, req)
				return &browserReadinessNavigation{runtime, tc.observed}, err
			}
			payload, _ := json.Marshal(toolgateway.AgentBrowserPayload{Version: "browser_navigate.v2", URL: tc.requested, ServiceJobID: "job-service"})
			call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, string(payload))
			waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call)
			if err != nil || waiting {
				t.Fatalf("waiting=%t err=%v", waiting, err)
			}
			result := st.calls[call.CallID]
			if tc.allowed {
				if result.Status != domain.SupervisorToolCompleted || !strings.Contains(result.ResultJSON, `\"navigated\":true`) {
					t.Fatalf("canonical same-origin navigation rejected: %+v", result)
				}
			} else if result.Status != domain.SupervisorToolFailed || result.ErrorCode != "FAILED_PRECONDITION" || strings.Contains(result.ResultJSON, `\"navigated\":true`) {
				t.Fatalf("origin change accepted: %+v", result)
			}
		})
	}
}

func TestAgentBrowserServiceReadinessBeforeAutomaticNavigation(t *testing.T) {
	for _, ready := range []bool{false, true} {
		service, st, runtime, supervisor, turn := newAgentBrowserFixture(t)
		service.options.ServiceReadiness = browserReadinessTest{ready: ready}
		call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"http://127.0.0.1:34567/","service_job_id":"job-service"}`)
		waiting, err := runAgentBrowserFixtureCall(t, supervisor, turn, call)
		if err != nil || waiting {
			t.Fatalf("waiting=%t err=%v", waiting, err)
		}
		if ready {
			if len(runtime.actions) != 1 || !strings.Contains(st.calls[call.CallID].ResultJSON, `\"navigated\":true`) {
				t.Fatalf("missing automatic navigation: %+v", st.calls[call.CallID])
			}
		} else {
			if len(runtime.actions) != 0 || !strings.Contains(st.calls[call.CallID].ResultJSON, "not_ready") {
				t.Fatalf("navigated before ready: %+v", st.calls[call.CallID])
			}
		}
	}
}
