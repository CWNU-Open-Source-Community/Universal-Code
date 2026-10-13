package application

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/modelregistry"
)

type definitionDeleteStore struct {
	*providerDefinitionMemoryStore
	beforeCAS func()
}

func (s *definitionDeleteStore) CompareAndSwapProviderDefinitionCollection(ctx context.Context,
	revision uint64, next modelregistry.ProviderDefinitionCollection,
) error {
	if before := s.beforeCAS; before != nil {
		s.beforeCAS = nil
		before()
	}
	return s.providerDefinitionMemoryStore.CompareAndSwapProviderDefinitionCollection(ctx, revision, next)
}

type definitionDeleteCredentials struct {
	credential.Store
	deletes     int
	afterDelete func()
	deleteErr   error
}

func (s *definitionDeleteCredentials) Delete(ctx context.Context, name string) error {
	s.deletes++
	if err := s.Store.Delete(ctx, name); err != nil {
		return err
	}
	if s.afterDelete != nil {
		s.afterDelete()
	}
	return s.deleteErr
}

func definitionDeleteFixture(t *testing.T) (*ProviderDefinitionService, *definitionDeleteStore,
	*definitionDeleteCredentials, *modelregistry.Registry, ProviderDefinitionDeleteRequest,
) {
	t.Helper()
	for _, name := range []string{"MIMO_API_KEY", "DEEPSEEK_API_KEY", "CYBERAGENT_ANTHROPIC_API_KEY",
		"CYBERAGENT_OPENAI_API_KEY", "CYBERAGENT_OLLAMA_BASE_URL"} {
		t.Setenv(name, "")
	}
	settings := &definitionDeleteStore{providerDefinitionMemoryStore: newProviderDefinitionMemoryStore()}
	credentials := &definitionDeleteCredentials{Store: credential.NewMemoryStore()}
	if err := credentials.Put(t.Context(), "delete-target", "original-test-credential"); err != nil {
		t.Fatal(err)
	}
	registry, err := modelregistry.NewFromEnvironmentWithCredentials(credentials)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewProviderDefinitionService(settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	service.WithModelDiscoveryCredentials(credentials)
	created, err := service.Upsert(t.Context(), ProviderDefinitionUpsertRequest{
		Version: ProviderDefinitionControlProtocolVersion, Definition: customProviderDefinition("delete-target"), Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, settings, credentials, registry, ProviderDefinitionDeleteRequest{
		Version: ProviderDefinitionControlProtocolVersion, ID: created.Definition.ID,
		ExpectedCollectionRevision: created.Collection.Revision,
		ExpectedDefinitionRevision: created.Definition.Revision, Confirm: true,
	}
}

func TestProviderDefinitionDeleteRejectionPreservesOriginalCredential(t *testing.T) {
	for _, scenario := range []string{"thread route", "named route", "stale collection", "stale definition", "concurrent revision", "concurrent thread route"} {
		t.Run(scenario, func(t *testing.T) {
			service, settings, credentials, _, request := definitionDeleteFixture(t)
			expectedCode := apperror.CodeConflict
			switch scenario {
			case "thread route":
				settings.selectThreadModelRoute(request.ID, "code-model")
				expectedCode = apperror.CodeFailedPrecondition
			case "named route":
				if err := settings.SetProviderSetting(t.Context(), "route."+modelregistry.SupportedRouteNames()[0], request.ID+"/code-model"); err != nil {
					t.Fatal(err)
				}
				expectedCode = apperror.CodeFailedPrecondition
			case "stale collection":
				request.ExpectedCollectionRevision = 0
			case "stale definition":
				request.ExpectedDefinitionRevision++
			case "concurrent revision":
				settings.beforeCAS = func() {
					current, err := modelregistry.ReadProviderDefinitions(t.Context(), settings)
					if err != nil {
						t.Fatal(err)
					}
					current.Revision++
					current.Providers[0].Revision++
					if err := settings.providerDefinitionMemoryStore.CompareAndSwapProviderDefinitionCollection(t.Context(), current.Revision-1, current); err != nil {
						t.Fatal(err)
					}
				}
			case "concurrent thread route":
				settings.beforeCAS = func() { settings.selectThreadModelRoute(request.ID, "code-model") }
				expectedCode = apperror.CodeFailedPrecondition
			}
			if _, err := service.Delete(t.Context(), request); apperror.CodeOf(err) != expectedCode {
				t.Fatalf("delete error=%v, expected %s", err, expectedCode)
			}
			secret, found, err := credentials.Get(t.Context(), request.ID)
			if err != nil || !found || secret != "original-test-credential" || credentials.deletes != 0 {
				t.Fatal("rejected deletion touched the original credential")
			}
			collection, err := service.List(t.Context())
			if err != nil || len(collection.Providers) != 1 {
				t.Fatalf("rejected deletion removed the definition: %+v, %v", collection, err)
			}
		})
	}
}

func TestProviderDefinitionDeleteRemovesDefinitionCredentialAndLiveRegistry(t *testing.T) {
	service, settings, credentials, registry, request := definitionDeleteFixture(t)
	result, err := service.Delete(t.Context(), request)
	if err != nil || result.DeletedID != request.ID || !result.RegistryReloaded || len(result.Collection.Providers) != 0 {
		t.Fatalf("delete result=%+v, error=%v", result, err)
	}
	if configured, err := credentials.Configured(t.Context(), request.ID); err != nil || configured || credentials.deletes != 1 {
		t.Fatal("successful deletion retained its credential")
	}
	if providerStatus(registry.Snapshot(), request.ID) != "missing" {
		t.Fatal("deleted Provider remains in the live Registry")
	}
	control := NewProviderCredentialService(credentials).WithRegistryReload(registry, settings)
	if _, err := control.Change(t.Context(), ChangeProviderCredentialRequest{
		Version: credential.ProtocolVersion, Provider: request.ID, Action: ProviderCredentialSet,
		Secret: "orphan-test-credential", Confirm: true,
	}); err == nil {
		t.Fatal("deleted definition still accepts credentials")
	}
}

func TestProviderDefinitionDeleteRestoresOriginalCredentialAfterAmbiguousCleanup(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup error", true: "cancelled request"}[cancelled], func(t *testing.T) {
			service, _, credentials, registry, request := definitionDeleteFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			credentials.deleteErr = errors.New("ambiguous credential cleanup")
			if cancelled {
				credentials.afterDelete = cancel
			}
			_, err := service.Delete(ctx, request)
			if apperror.CodeOf(err) != apperror.CodeUnavailable || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("cleanup failure did not report restoration: %v", err)
			}
			secret, found, err := credentials.Get(t.Context(), request.ID)
			if err != nil || !found || secret != "original-test-credential" {
				t.Fatal("failed deletion lost the original credential")
			}
			collection, err := service.List(t.Context())
			if err != nil || len(collection.Providers) != 1 || collection.Revision != request.ExpectedCollectionRevision+2 ||
				providerStatus(registry.Snapshot(), request.ID) != modelregistry.ProviderAvailable {
				t.Fatalf("failed deletion did not restore a usable definition: %+v, %v", collection, err)
			}
			credentials.deleteErr, credentials.afterDelete = nil, nil
			request.ExpectedCollectionRevision = collection.Revision
			if _, err := service.Delete(t.Context(), request); err != nil {
				t.Fatalf("restored definition cannot be retried: %v", err)
			}
		})
	}
}

type delayedDefinitionCredentialCatalog struct {
	registry  *modelregistry.Registry
	first     atomic.Bool
	inspected chan struct{}
	proceed   chan struct{}
}

func (c *delayedDefinitionCredentialCatalog) Snapshot() modelregistry.Snapshot {
	snapshot := c.registry.Snapshot()
	if c.first.CompareAndSwap(false, true) {
		close(c.inspected)
		<-c.proceed
	}
	return snapshot
}

func TestProviderDefinitionDeleteRejectsCredentialChangeAdmittedBeforeDeletion(t *testing.T) {
	service, settings, credentials, registry, request := definitionDeleteFixture(t)
	catalog := &delayedDefinitionCredentialCatalog{registry: registry,
		inspected: make(chan struct{}), proceed: make(chan struct{})}
	control := NewProviderCredentialService(credentials).WithRegistryReload(registry, settings).
		WithProviderCatalog(catalog)
	result := make(chan error, 1)
	go func() {
		_, err := control.Change(t.Context(), ChangeProviderCredentialRequest{
			Version: credential.ProtocolVersion, Provider: request.ID, Action: ProviderCredentialSet,
			Secret: "queued-test-credential", Confirm: true,
		})
		result <- err
	}()
	<-catalog.inspected
	_, deleteErr := service.Delete(t.Context(), request)
	close(catalog.proceed)
	if err := <-result; err == nil || deleteErr != nil {
		t.Fatalf("queued change=%v, deletion=%v", err, deleteErr)
	}
	if configured, err := credentials.Configured(t.Context(), request.ID); err != nil || configured {
		t.Fatal("queued credential change recreated an orphan credential")
	}
}
