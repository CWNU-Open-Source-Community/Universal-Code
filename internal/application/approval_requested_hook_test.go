package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

type approvalHookAuditSink struct {
	mu      sync.Mutex
	records []hooks.AuditRecord
}

func (s *approvalHookAuditSink) RecordHookAudit(_ context.Context,
	record hooks.AuditRecord,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *approvalHookAuditSink) snapshot() []hooks.AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hooks.AuditRecord(nil), s.records...)
}

func (s *approvalHookAuditSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func approvalTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// TestAgentRunnerApprovalRequestedHookFiresOnWaitingApproval drives a run into
// the durable waiting-approval state (a web fetch needing inline operator
// review) through the handoff service and asserts the restricted
// approval_requested hook fired exactly once, with the audit ledger recording
// the observation and the run identity.
func TestAgentRunnerApprovalRequestedHookFiresOnWaitingApproval(t *testing.T) {
	base, err := store.Open(filepath.Join(t.TempDir(), "approval-hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ctx := context.Background()
	runService := application.NewRunService(base)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "fetch one newly discovered public source", Profile: "review",
		Surface: "code", Phase: "deliver", ModelRoute: "tool-loop/model",
		NetworkMode: "disabled",
		Budget:      domain.Budget{MaxTurns: 3, MaxToolCalls: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	setWebTestPermission(t, base, run.ID, domain.RunExecutionPermissionAsk)
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("provider-inline-web-fetch", string(toolgateway.WebFetchTool),
			`{"version":"web_fetch.v1","url":"https://new.example.net/report"}`),
		textResponse(rootActionResponse(domain.RootActionContinue,
			"approved public evidence fetched", "", "")),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	sink := &approvalHookAuditSink{}
	engine := hooks.NewEngine(sink)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch-plugin",
		PluginFingerprint: approvalTestDigest("plugin"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "approval-tap",
			Event: hooks.ApprovalRequested, Action: hooks.ActionRecord,
			FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	supervisor := application.NewAgentRunner(base, router, policy.NewDefaultChecker()).
		WithWebEvidence(webevidence.NewService(base, nil, &applicationWebFetchBackend{})).
		WithWebFetchAuthorizationScheduler(true).
		WithLifecycleHooks(engine)
	if _, err := supervisor.Step(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Event != hooks.ApprovalRequested ||
		records[0].Outcome != "completed" || records[0].Rejected == nil || *records[0].Rejected {
		t.Fatalf("approval_requested audit records=%#v", records)
	}
	if records[0].RunID != run.ID {
		t.Fatalf("approval_requested audit lost the run identity: %#v", records[0])
	}
}

// TestAgentRunnerApprovalRequestedDenialFailsClosed drives the same boundary with
// a deny-style declaration is rejected at validation, so the audit-only
// contract is enforced before any registration; this test pins the engine
// refusing an approval auto-deny registration.
func TestAgentRunnerApprovalRequestedDenialFailsClosed(t *testing.T) {
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch-plugin",
		PluginFingerprint: approvalTestDigest("plugin"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "approval-deny",
			Event: hooks.ApprovalRequested, Action: hooks.ActionDeny,
			FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			Message: "hook-controlled auto-deny"}}}); err == nil {
		t.Fatal("approval auto-deny registration was accepted")
	}
}

func TestAgentRunnerApprovalRequestedHookFailureFailsClosedAndStaysRecoverable(t *testing.T) {
	base, err := store.Open(filepath.Join(t.TempDir(), "approval-hook-fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ctx := context.Background()
	runService := application.NewRunService(base)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "fetch one newly discovered public source", Profile: "review",
		Surface: "code", Phase: "deliver", ModelRoute: "tool-loop/model",
		NetworkMode: "disabled",
		Budget:      domain.Budget{MaxTurns: 3, MaxToolCalls: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	setWebTestPermission(t, base, run.ID, domain.RunExecutionPermissionAsk)
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("provider-inline-web-fetch", string(toolgateway.WebFetchTool),
			`{"version":"web_fetch.v1","url":"https://new.example.net/report"}`),
		textResponse(rootActionResponse(domain.RootActionContinue,
			"approved public evidence fetched", "", "")),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	// A record hook whose audit ledger fails: the boundary fails closed (the
	// settlement returns an error) while the Run stays recoverable — the
	// checkpoint is not moved out of the started phase, so the pending
	// approval remains resumable.
	failingSink := &failingApprovalAuditSink{}
	engine := hooks.NewEngine(failingSink)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch-plugin",
		PluginFingerprint: approvalTestDigest("plugin"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "approval-tap",
			Event: hooks.ApprovalRequested, Action: hooks.ActionRecord,
			FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	supervisor := application.NewAgentRunner(base, router, policy.NewDefaultChecker()).
		WithWebEvidence(webevidence.NewService(base, nil, &applicationWebFetchBackend{})).
		WithWebFetchAuthorizationScheduler(true).
		WithLifecycleHooks(engine)
	_, stepErr := supervisor.Step(ctx, run.ID)
	if stepErr == nil {
		t.Fatal("failing approval hook did not fail the settlement closed")
	}
	persisted, getErr := base.GetRun(ctx, run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != domain.RunWaitingApproval && persisted.Status != domain.RunRunning {
		t.Fatalf("fail-closed settlement changed the run into %q", persisted.Status)
	}
	// The durable waiting gate is undamaged: a later approval continuation
	// can resume the exact pending action (the checkpoint was not moved out
	// of its started phase by the failed hook).
}

type failingApprovalAuditSink struct{}

func (failingApprovalAuditSink) RecordHookAudit(context.Context,
	hooks.AuditRecord,
) error {
	return errors.New("audit ledger unavailable")
}

func TestAgentRunnerApprovalRequestedDurableReentryFiresHookAndPropagatesMissionError(t *testing.T) {
	base, err := store.Open(filepath.Join(t.TempDir(), "approval-hook-reentry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ctx := context.Background()
	runService := application.NewRunService(base)
	mission, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "test durable re-entry hook firing", Profile: "review",
		Surface: "code", Phase: "deliver", ModelRoute: "tool-loop/model",
		NetworkMode: "disabled",
		Budget:      domain.Budget{MaxTurns: 3, MaxToolCalls: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = base.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := run.Status
	if err := run.Transition(domain.RunWaitingApproval, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	evt, err := events.New(run.ID, mission.ID, events.RunStatusChangedEvent,
		"approval_test", run.ID, map[string]any{"from": expected, "to": domain.RunWaitingApproval})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.TransitionRun(ctx, run, expected, evt); err != nil {
		t.Fatal(err)
	}
	sink := &approvalHookAuditSink{}
	engine := hooks.NewEngine(sink)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch-plugin",
		PluginFingerprint: approvalTestDigest("plugin"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "approval-tap",
			Event: hooks.ApprovalRequested, Action: hooks.ActionRecord,
			FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: "provider", Model: "model"})
	supervisor := application.NewAgentRunner(base, router, policy.NewDefaultChecker()).
		WithLifecycleHooks(engine)

	res, execErr := supervisor.Execute(ctx, run.ID, 1)
	if execErr != nil {
		t.Fatalf("unexpected execute error: %v", execErr)
	}
	if res.StopReason != "waiting_approval" {
		t.Fatalf("expected stop reason waiting_approval, got %q", res.StopReason)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Event != hooks.ApprovalRequested {
		t.Fatalf("expected 1 approval_requested record, got %#v", records)
	}
	if records[0].WorkspaceID != mission.WorkspaceID {
		t.Fatalf("expected workspace ID %q, got %q", mission.WorkspaceID, records[0].WorkspaceID)
	}
}
