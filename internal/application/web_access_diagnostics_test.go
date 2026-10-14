package application

import (
	"context"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/webevidence"
)

type webAccessDiagnosticBackend struct {
	action func(context.Context)
	calls  int
}

func (*webAccessDiagnosticBackend) Name() string     { return "searxng" }
func (*webAccessDiagnosticBackend) Endpoint() string { return "https://search.example.com/search" }
func (b *webAccessDiagnosticBackend) Search(ctx context.Context, _ string, _ int, _ webevidence.NetworkAuthority) ([]webevidence.ProviderResult, error) {
	b.calls++
	if b.action != nil {
		b.action(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []webevidence.ProviderResult{{URL: "https://docs.example.com/page", Title: "public docs"}}, nil
}

func TestWebAccessDiagnosticsSerializesSelectionAndReleasesCancelledLease(t *testing.T) {
	for _, cancelProbe := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cancel"}[cancelProbe], func(t *testing.T) {
			state, err := store.Open(filepath.Join(t.TempDir(), "web-diagnostics.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			_, run, err := NewRunService(state).Create(t.Context(), CreateRunRequest{Goal: "probe selected search service", Profile: "review", ModelRoute: "code", WebAccessMode: "rules", Budget: domain.Budget{MaxTurns: 2}})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := state.GetThreadByRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			definition := testProviderSearchDefinition("web-diag", modelregistry.ProviderSearchModeSearXNG)
			registry, settings, credentials := testProviderSearchRegistry(t, definition)
			backend := &webAccessDiagnosticBackend{}
			resolver, err := NewProviderSearchResolver(registry, settings, credentials, backend, nil)
			if err != nil {
				t.Fatal(err)
			}
			service := NewProviderSearchReadinessService(state, resolver)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := ChangeRunWebAccessRequest{Version: RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "disabled", OperationKey: "web-diag-disable-0001"}
			backend.action = func(context.Context) {
				lease, found, err := state.GetRunExecutionLease(t.Context(), run.ID)
				if err != nil || !found || !lease.ActiveAt(lease.AcquiredAt) {
					t.Fatalf("probe lacked active lease: %+v found=%t err=%v", lease, found, err)
				}
				if _, err := NewRunWebAccessService(state).Change(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatalf("mode changed during diagnostic dispatch: %v", err)
				}
				if cancelProbe {
					cancel()
				}
			}
			result, err := service.Check(ctx, thread.ID)
			if err != nil || backend.calls != 1 || !result.NetworkRequestAttempted {
				t.Fatalf("probe=%+v err=%v calls=%d", result, err, backend.calls)
			}
			if !cancelProbe && result.State != "succeeded" {
				t.Fatalf("successful diagnostic=%+v", result)
			}
			lease, found, err := state.GetRunExecutionLease(t.Context(), run.ID)
			if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased {
				t.Fatalf("lease leaked after cancellation=%t: %+v err=%v", cancelProbe, lease, err)
			}
			if _, err := NewRunWebAccessService(state).Change(t.Context(), request); err != nil {
				t.Fatalf("probe left selection blocked: %v", err)
			}
			disabled, err := service.Check(t.Context(), thread.ID)
			if err != nil || disabled.Code != "not_authorized" || disabled.NetworkRequestAttempted || disabled.Provider != definition.ID || disabled.Model != "model-a" || disabled.SearchPolicy != "disabled" || backend.calls != 1 {
				t.Fatalf("disabled diagnostic=%+v err=%v calls=%d", disabled, err, backend.calls)
			}
		})
	}
}

type webAccessDiagnosticRaceStore struct {
	*providerSearchReadinessStoreFake
	released bool
}

func (s *webAccessDiagnosticRaceStore) AcquireRunExecutionLease(context.Context, domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error) {
	s.mode.Revision++
	s.mode.Scope.WebAccessMode = domain.WebAccessDisabled
	return domain.RunExecutionLeaseAcquisition{}, nil
}
func (s *webAccessDiagnosticRaceStore) ReleaseRunExecutionLease(context.Context, domain.RunExecutionLease) (domain.RunExecutionLease, bool, error) {
	s.released = true
	return domain.RunExecutionLease{}, false, nil
}

func TestWebAccessDiagnosticsRejectsModeChangeBeforeDispatch(t *testing.T) {
	definition := testProviderSearchDefinition("web-diag-race", modelregistry.ProviderSearchModeSearXNG)
	registry, settings, credentials := testProviderSearchRegistry(t, definition)
	backend := &webAccessDiagnosticBackend{}
	resolver, err := NewProviderSearchResolver(registry, settings, credentials, backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &webAccessDiagnosticRaceStore{providerSearchReadinessStoreFake: diagnosticStore()}
	result, err := NewProviderSearchReadinessService(state, resolver).Check(t.Context(), state.thread.ID)
	if err != nil || result.Code != "configuration_changed" || result.NetworkRequestAttempted || backend.calls != 0 || !state.released {
		t.Fatalf("stale diagnostic=%+v err=%v calls=%d released=%t", result, err, backend.calls, state.released)
	}
}
