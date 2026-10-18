package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

func TestThreadCommandServicePolicyHTTPControlAndNativeRevocation(t *testing.T) {
	f := newThreadApplicationServicesLiveFixture(t)
	job := f.start(t, "shared")
	ordinary := f.start(t, "ordinary")
	path := "/api/v1/threads/" + job.threadID + "/command-service-policy"
	valid := `{"version":"thread_command_service_policy.v1","policy":"exclusive","expected_revision":1}`
	// Run control alone does not grant permission-setting control. Reading the
	// preference remains available with the ordinary access token.
	policyReadOnly, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunControlEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, policyReadOnly, http.MethodPut, path, testControlToken, "", "application/json", strings.NewReader(valid))
	if response.Code != http.StatusNotFound {
		t.Fatalf("policy control was available without its capability: %d %s", response.Code, response.Body)
	}
	if response := performRequest(t, policyReadOnly, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil); response.Code != http.StatusOK {
		t.Fatalf("read-only policy lookup: %d %s", response.Code, response.Body)
	}
	for _, tc := range []struct {
		name, token, body, query string
		status                   int
	}{
		{"read token cannot change preference", testAccessToken, valid, "", http.StatusUnauthorized},
		{"unknown field", testControlToken, `{"version":"thread_command_service_policy.v1","policy":"exclusive","expected_revision":1,"approved":true}`, "", http.StatusBadRequest},
		{"duplicate field", testControlToken, `{"version":"thread_command_service_policy.v1","policy":"auto","policy":"exclusive","expected_revision":1}`, "", http.StatusBadRequest},
		{"missing revision", testControlToken, `{"version":"thread_command_service_policy.v1","policy":"exclusive"}`, "", http.StatusBadRequest},
		{"foreign version", testControlToken, `{"version":"future","policy":"exclusive","expected_revision":1}`, "", http.StatusBadRequest},
		{"extra query", testControlToken, valid, "?run_id=another-run", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := performSessionMessageRequest(t, f.api, http.MethodPut, path+tc.query, tc.token, "", "application/json", strings.NewReader(tc.body))
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
	var preference domain.ThreadCommandServicePolicy
	decodeDataStatus(t, performRequest(t, f.api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusOK, &preference)
	if preference.Policy != domain.CommandServicePolicyAuto || preference.Revision != 1 {
		t.Fatalf("rejected request changed preference: %+v", preference)
	}
	detail := f.detail(t, job)
	if detail.Service.WorkspaceMode != runner.CommandRuntimeWorkspaceSharedService || detail.Service.TreeReaped == nil || *detail.Service.TreeReaped || detail.Service.State != "running" {
		t.Fatalf("live service state: %+v", detail.Service)
	}
	before, err := f.store.GetRunExecutionPermission(t.Context(), job.runID)
	if err != nil {
		t.Fatal(err)
	}
	response = performSessionMessageRequest(t, f.api, http.MethodPut, path, testControlToken, "", "application/json", strings.NewReader(valid))
	decodeDataStatus(t, response, http.StatusOK, &preference)
	if preference.Policy != domain.CommandServicePolicyExclusive || preference.Revision != 2 {
		t.Fatalf("saved preference: %+v", preference)
	}
	if _, err := f.runtime.Reconcile(t.Context()); err != nil {
		t.Fatalf("preference revocation must not fail the adapter reconciler: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := f.store.GetCommandRuntimeJob(t.Context(), job.jobID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() && current.TreeReaped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exclusive preference did not reap the native service: %+v", current)
		}
		if _, _, err := f.manager.Wait(t.Context(), job.jobID, 100*time.Millisecond, ^uint64(0), runner.MinCommandRuntimeOutputRead); err != nil {
			t.Fatal(err)
		}
	}
	detail = f.detail(t, job)
	if detail.Service.TreeReaped == nil || !*detail.Service.TreeReaped || detail.Service.State == "running" {
		t.Fatalf("cleanup state did not reach UI: %+v", detail.Service)
	}
	otherDetail := f.waitDetail(t, ordinary, func(d threadApplicationServicesLiveDetail) bool { return len(d.CandidateURLs) > 0 })
	if otherDetail.Service.State != "running" {
		t.Fatalf("another Thread's ordinary command was stopped: %+v", otherDetail.Service)
	}
	assertThreadApplicationServicesLoopbackResponse(t, otherDetail.CandidateURLs[0].URL)
	after, err := f.store.GetRunExecutionPermission(t.Context(), job.runID)
	if err != nil || after.ID != before.ID || after.Revision != before.Revision || after.Mode != before.Mode {
		t.Fatalf("service preference changed permission: %+v %v", after, err)
	}
	response = performSessionMessageRequest(t, f.api, http.MethodPut, path, testControlToken, "", "application/json", strings.NewReader(strings.Replace(valid, `"exclusive"`, `"auto"`, 1)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale preference update: %d %s", response.Code, response.Body)
	}
}
