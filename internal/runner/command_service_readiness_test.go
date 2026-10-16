package runner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
)

type readinessTestProcess struct {
	commandRuntimeProcess
	owned bool
}

func (p *readinessTestProcess) OwnsTCPListener(net.IP, uint16) bool { return p.owned }

func readinessTestManager(rawURL string, process commandRuntimeProcess) (*CommandRuntimeManager, CommandRuntimeJobIdentity) {
	adapter := commandruntimeadapter.HostUnsandboxed("readiness-test")
	job := CommandRuntimeJob{ID: "job-service", OwnerID: "owner", OwnerGeneration: 1, Adapter: adapter,
		State: CommandRuntimeJobRunning, Network: CommandRuntimeNetworkHost, Stdout: rawURL}
	m := &CommandRuntimeManager{ownerID: job.OwnerID, ownerGeneration: job.OwnerGeneration, adapter: adapter,
		entries: map[string]*commandRuntimeEntry{job.ID: {record: job, process: process}}}
	return m, CommandRuntimeIdentity(job)
}

func TestCommandServiceReadinessHTTPAndOwnership(t *testing.T) {
	for _, code := range []int{200, 204, 302, 404, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("probe widened request")
				}
				w.Header().Set("Location", "http://example.invalid/")
				w.WriteHeader(code)
			}))
			defer server.Close()
			process := &readinessTestProcess{owned: true}
			manager, id := readinessTestManager(server.URL, process)
			authorize := func(context.Context) error { return nil }
			result, err := manager.CheckOwnedCommandService(t.Context(), id, server.URL, authorize)
			if err != nil || result.Verified != (code < 300) || result.HTTPStatus != code || result.JobID != id.ID || requests.Load() != 1 {
				t.Fatalf("readiness=%+v requests=%d err=%v", result, requests.Load(), err)
			}
			process.owned = false
			result, err = manager.CheckOwnedCommandService(t.Context(), id, server.URL, authorize)
			if err != nil || result.Verified || result.Reason != "listener_not_owned" || requests.Load() != 1 {
				t.Fatalf("contacted foreign listener: %+v %v", result, err)
			}
		})
	}
}

func TestCommandServiceReadinessRejectsUnboundAndRevokedRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	manager, id := readinessTestManager(server.URL, &readinessTestProcess{owned: true})
	allow := func(context.Context) error { return nil }
	for _, raw := range []string{"https://example.com/", server.URL + "?token=secret", "http://localhost:42/"} {
		if _, err := manager.CheckOwnedCommandService(t.Context(), id, raw, allow); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	result, err := manager.CheckOwnedCommandService(t.Context(), id, server.URL+"/unknown", allow)
	if err != nil || result.Reason != "address_not_in_job_output" {
		t.Fatalf("unbound URL: %+v %v", result, err)
	}
	denied := errors.New("revoked")
	if _, err = manager.CheckOwnedCommandService(t.Context(), id, server.URL, func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	foreign := id
	foreign.OwnerID = "another-owner"
	if _, err = manager.CheckOwnedCommandService(t.Context(), foreign, server.URL, allow); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid requests reached HTTP server")
	}
}

func TestCommandServiceReadinessRechecksAuthorizationAfterResponse(t *testing.T) {
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { revoked.Store(true) }))
	defer server.Close()
	manager, id := readinessTestManager(server.URL, &readinessTestProcess{owned: true})
	result, err := manager.CheckOwnedCommandService(t.Context(), id, server.URL, func(context.Context) error {
		if revoked.Load() {
			return ErrCommandRuntimeBoundary
		}
		return nil
	})
	if !errors.Is(err, ErrCommandRuntimeBoundary) || result.Verified {
		t.Fatalf("revoked readiness=%+v err=%v", result, err)
	}
}
