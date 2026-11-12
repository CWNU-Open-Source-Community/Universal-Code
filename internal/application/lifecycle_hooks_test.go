package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/store"
)

func TestRunServiceLifecycleHooksGuardRealTransactions(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "lifecycle-hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	engine := hooks.NewEngine(state)
	service := application.NewRunService(state).WithLifecycleHooks(engine)
	ctx := context.Background()

	setDenyHook(t, engine, hooks.SessionOpened, "deny-session")
	if _, _, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "must remain absent", Profile: "code", Budget: domain.DefaultBudget(),
	}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("session hook did not fail closed: %v", err)
	}
	sessions, err := state.ListSessions(ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("denied Session reached storage: sessions=%#v err=%v", sessions, err)
	}

	if err := engine.Replace(nil); err != nil {
		t.Fatal(err)
	}
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "guard lifecycle", Profile: "code", Budget: domain.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	setDenyHook(t, engine, hooks.RunStarted, "deny-start")
	if _, err := service.Start(ctx, run.ID); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("Run start hook did not fail closed: %v", err)
	}
	stored, err := state.GetRun(ctx, run.ID)
	if err != nil || stored.Status != domain.RunCreated {
		t.Fatalf("denied Run start changed state: run=%#v err=%v", stored, err)
	}

	if err := engine.Replace(nil); err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, run.ID)
	if err != nil || started.Status != domain.RunRunning {
		t.Fatalf("allowed Run did not start: run=%#v err=%v", started, err)
	}
	setDenyHook(t, engine, hooks.RunCompleted, "deny-completion")
	if _, err := service.Complete(ctx, run.ID); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("Run completion hook did not fail closed: %v", err)
	}
	stored, err = state.GetRun(ctx, run.ID)
	if err != nil || stored.Status != domain.RunRunning {
		t.Fatalf("denied completion changed state: run=%#v err=%v", stored, err)
	}
}

func setDenyHook(t *testing.T, engine *hooks.Engine, event hooks.Event, id string) {
	t.Helper()
	digest := sha256.Sum256([]byte(id))
	policy := hooks.FailureDeny
	if event == hooks.RunCompleted || event == hooks.SessionClosed {
		policy = hooks.FailureContinue
	}
	if err := engine.Replace([]hooks.Registration{{PluginID: "fixture-plugin",
		PluginFingerprint: hex.EncodeToString(digest[:]), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: id, Event: event,
			Action: hooks.ActionDeny, FailurePolicy: policy,
			TimeoutMillis: 100, Message: "operator policy",
		}}}); err != nil {
		t.Fatal(err)
	}
}
