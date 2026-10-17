package application

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerBrowserReadinessCannotBorrowSiblingCommandJob(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-browser-job-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	service, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	a := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	b := newWorkerCommandTestActor(t, st, service, run, root, lease, &domain.WorkerProfile{})
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].Arguments = []string{"-e", `require('http').createServer((q,s)=>s.end('owned worker service')).listen(0,'127.0.0.1',function(){console.log('http://127.0.0.1:'+this.address().port+'/')})`}
	input.Commands[0].Network = runner.CommandRuntimeNetworkHost
	input.Commands[0].TimeoutMilliseconds = 60000
	call := a.prepare(t, input)
	result, err := service.ExecuteCommandRuntime(t.Context(), a.charge(t, call, true), input)
	if err != nil || len(result.Jobs) != 1 {
		t.Fatalf("owned service start: %+v %v", result, err)
	}
	a.complete(t, call)
	jobID := result.Jobs[0].ID
	url := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := st.GetCommandRuntimeJob(t.Context(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		candidates := runner.CommandServiceURLCandidates([]runner.CommandRuntimeFrame{{Text: job.Stdout}})
		if len(candidates) > 0 {
			url = candidates[0].URL
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if url == "" {
		t.Fatal("owned worker service did not publish its URL")
	}
	browser := NewAgentBrowserService(st, AgentBrowserOptions{HomePath: t.TempDir(), Capabilities: caps, Headless: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = browser.Shutdown(ctx)
	})
	// Readiness checks command ownership without launching a browser. Exercise
	// both availability branches on every OS; installed-browser coverage lives
	// in TestExecutionWorkersRealBrowserRejectSiblingSnapshot.
	browser.launch = func(context.Context, browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		t.Error("command readiness must not launch a platform browser")
		return nil, errors.New("platform browser launch is outside the readiness ownership test")
	}
	browser.available = false
	if _, err := browser.WorkerToolAuthority(t.Context(), a.ref); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("unavailable browser advertised worker authority: %v", err)
	}
	browser.available = true
	raw, err := browser.WorkerToolAuthority(t.Context(), a.ref)
	if err != nil {
		t.Fatal(err)
	}
	var owner toolgateway.AgentBrowserCallAuthority
	if err := json.Unmarshal(raw, &owner); err != nil {
		t.Fatal(err)
	}
	readiness, err := service.CheckAgentBrowserService(t.Context(), owner, jobID, url)
	if err != nil {
		t.Fatalf("own command service was inaccessible: %+v %v", readiness, err)
	}
	// Native listener ownership is implemented only for the Windows Job Object.
	// Unix must report the unsupported boundary without probing the service;
	// both sibling authorization checks below still run on every platform.
	if runtime.GOOS == "windows" {
		if !readiness.Verified {
			t.Fatalf("own command service was inaccessible: %+v", readiness)
		}
	} else if readiness.State != "unavailable" || readiness.Reason != "listener_ownership_unavailable" || readiness.Verified || readiness.HTTPStatus != 0 {
		t.Fatalf("unsupported listener ownership produced a readiness claim: %+v", readiness)
	}
	raw, err = browser.WorkerToolAuthority(t.Context(), b.ref)
	if err != nil {
		t.Fatal(err)
	}
	var sibling toolgateway.AgentBrowserCallAuthority
	if err := json.Unmarshal(raw, &sibling); err != nil {
		t.Fatal(err)
	}
	for _, forgeSession := range []bool{false, true} {
		candidate := sibling
		if forgeSession {
			candidate.SessionID = owner.SessionID
			candidate.Generation = candidate.Fingerprint()
		}
		if _, err := service.CheckAgentBrowserService(t.Context(), candidate, jobID, url); !errors.Is(err, runner.ErrCommandRuntimeBoundary) {
			t.Fatalf("sibling borrowed service job (forged session %v): %v", forgeSession, err)
		}
	}
}
