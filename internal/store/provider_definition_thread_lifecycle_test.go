package store

import (
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/modelregistry"
)

func TestProviderDefinitionMutationRespectsThreadLifecycle(t *testing.T) {
	for _, status := range []domain.ThreadStatus{domain.ThreadActive, domain.ThreadArchived, domain.ThreadDeleted} {
		for _, action := range []string{"delete", "disable", "remove model"} {
			t.Run(string(status)+"/"+action, func(t *testing.T) {
				ctx := t.Context()
				state, err := Open(filepath.Join(t.TempDir(), "provider-lifecycle.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer state.Close()
				definition := atomicityProviderDefinition(1)
				if err := state.CompareAndSwapProviderDefinitionCollection(ctx, 0, atomicityCollection(1, definition)); err != nil {
					t.Fatal(err)
				}
				_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{
					Goal: "retain historical provider model", Profile: "review",
					ModelRoute: "custom-atomic/model-one", Budget: domain.Budget{MaxTurns: 2},
				})
				if err != nil {
					t.Fatal(err)
				}
				thread, err := state.GetThreadByRun(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := state.ChangeThreadModelRoutePreference(ctx, atomicitySelectMutation(thread.ID, "select-provider")); err != nil {
					t.Fatal(err)
				}
				if status != domain.ThreadActive {
					transition := domain.ThreadArchive
					if status == domain.ThreadDeleted {
						transition = domain.ThreadDelete
					}
					if _, err := state.TransitionThread(ctx, thread.ID, transition, thread.Version, "test-operator", time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				preferences, err := state.ListSelectedThreadModelRoutePreferences(ctx, definition.ID)
				if err != nil || (len(preferences) == 0) != (status == domain.ThreadDeleted) {
					t.Fatalf("selected references for %s: %+v err=%v", status, preferences, err)
				}
				service, err := application.NewProviderDefinitionService(state, modelregistry.New(nil))
				if err != nil {
					t.Fatal(err)
				}
				var next modelregistry.ProviderDefinitionCollection
				if action == "delete" {
					next = atomicityCollection(2)
				} else {
					changed := definition
					changed.Revision = 2
					if action == "disable" {
						changed.Enabled = false
					} else {
						changed.Models, changed.DefaultModel = []string{"model-two"}, "model-two"
					}
					next = atomicityCollection(2, changed)
				}
				if status != domain.ThreadDeleted {
					if err := state.CompareAndSwapProviderDefinitionCollection(ctx, 1, next); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
						t.Fatalf("transaction guard did not protect %s Thread: %v", status, err)
					}
				}
				if action == "delete" {
					_, err = service.Delete(ctx, application.ProviderDefinitionDeleteRequest{
						Version: application.ProviderDefinitionControlProtocolVersion, ID: definition.ID,
						ExpectedCollectionRevision: 1, ExpectedDefinitionRevision: 1, Confirm: true,
					})
				} else {
					changed := next.Providers[0]
					changed.Revision = 1
					_, err = service.Upsert(ctx, application.ProviderDefinitionUpsertRequest{
						Version:                    application.ProviderDefinitionControlProtocolVersion,
						ExpectedCollectionRevision: 1, Definition: changed, Confirm: true,
					})
				}
				if status == domain.ThreadDeleted {
					if err != nil {
						t.Fatalf("deleted Thread blocked provider %s: %v", action, err)
					}
				} else if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
					t.Fatalf("service did not protect %s Thread: %v", status, err)
				}
				storedRun, err := state.GetRun(ctx, run.ID)
				if err != nil || storedRun.Config.ModelRoute != run.Config.ModelRoute {
					t.Fatalf("historical Run model changed: %+v err=%v", storedRun.Config, err)
				}
			})
		}
	}
}
