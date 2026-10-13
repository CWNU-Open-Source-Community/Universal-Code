package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
)

func TestProviderDefinitionHTTPPersistsCredentialReferencesAndDynamicCredentialStatus(t *testing.T) {
	fixture := newAPIFixture(t)
	definitions, err := application.NewProviderDefinitionService(
		fixture.store, fixture.api.modelRegistry)
	if err != nil {
		t.Fatal(err)
	}
	credentialStore := credential.NewMemoryStore()
	definitions.WithModelDiscoveryCredentials(credentialStore)
	credentials := application.NewProviderCredentialService(credentialStore).
		WithRegistryReload(fixture.api.modelRegistry, fixture.store)
	api, err := New(fixture.store, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		ProviderDefinitionEnabled: true, ProviderDefinitionController: definitions,
		ProviderCredentialEnabled: true, ProviderCredentialController: credentials,
		AppVersion: "provider-definition-http-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	list := performSessionMessageRequest(t, api, http.MethodGet, ProviderDefinitionsPath,
		testAccessToken, "", "", nil)
	var initial ProviderDefinitionCollectionView
	decodeDataStatus(t, list, http.StatusOK, &initial)
	if initial.Version != "provider_definition_collection.v1" || initial.Revision != 0 ||
		len(initial.Providers) != 0 {
		t.Fatalf("unexpected initial custom Provider collection: %#v", initial)
	}

	const provider = "team-gateway"
	path := "/api/v1/models/provider-definitions/" + provider
	const exposedSecret = "sk-provider-definition-must-not-persist"
	unsafeBody := `{"version":"provider_definition_control.v1",` +
		`"expected_collection_revision":0,"definition":{` +
		`"version":"provider_definition.v1","id":"team-gateway",` +
		`"display_name":"Team Gateway","note":"","website_url":"",` +
		`"endpoint_url":"https://models.example.org/v1/chat/completions",` +
		`"default_model":"team-model","models":["team-model"],` +
		`"transport":"openai_chat_completions","search_mode":"auto",` +
		`"native_web_search_capability":"declared_unverified",` +
		`"advanced_config":{"request_headers":{"Authorization":"Bearer ` + exposedSecret + `"}},` +
		`"enabled":true,"revision":0},"confirm":true}`
	unsafe := performSessionMessageRequest(t, api, http.MethodPost, path,
		testControlToken, "", "application/json", strings.NewReader(unsafeBody))
	if unsafe.Code != http.StatusBadRequest || strings.Contains(unsafe.Body.String(), exposedSecret) {
		t.Fatalf("plaintext advanced JSON was not rejected safely: status=%d body=%s",
			unsafe.Code, unsafe.Body.String())
	}

	validBody := `{"version":"provider_definition_control.v1",` +
		`"expected_collection_revision":0,"definition":{` +
		`"version":"provider_definition.v1","id":"team-gateway",` +
		`"display_name":"Team Gateway","note":"Responses-compatible company endpoint",` +
		`"website_url":"https://models.example.org",` +
		`"endpoint_url":"https://models.example.org/v1/chat/completions",` +
		`"default_model":"team-model","models":["team-model"],` +
		`"transport":"openai_chat_completions","search_mode":"auto",` +
		`"native_web_search_capability":"declared_unverified",` +
		`"advanced_config":{"request_headers":{"Authorization":{` +
		`"$credential":"team-gateway","template":"Bearer ${secret}"}},` +
		`"vendor":{"model":"provider-owned-metadata"}},` +
		`"enabled":true,"revision":0},"confirm":true}`
	readOnly := performSessionMessageRequest(t, api, http.MethodPost, path,
		testAccessToken, "", "application/json", strings.NewReader(validBody))
	assertAPIError(t, readOnly, http.StatusUnauthorized, "POLICY_DENIED")

	createdResponse := performSessionMessageRequest(t, api, http.MethodPost, path,
		testControlToken, "", "application/json", strings.NewReader(validBody))
	var created ProviderDefinitionMutationView
	decodeDataStatus(t, createdResponse, http.StatusAccepted, &created)
	if created.Definition == nil || created.Definition.ID != provider ||
		created.Definition.Revision != 1 || created.Collection.Revision != 1 ||
		!created.RegistryReloaded || created.RegistryGeneration < 2 ||
		strings.Contains(createdResponse.Body.String(), exposedSecret) {
		t.Fatalf("unexpected custom Provider mutation: %#v", created)
	}

	credentialList := performSessionMessageRequest(t, api, http.MethodGet,
		ProviderCredentialsPath, testAccessToken, "", "", nil)
	var statuses ProviderCredentialListView
	decodeDataStatus(t, credentialList, http.StatusOK, &statuses)
	foundCustom := false
	for _, status := range statuses.Items {
		foundCustom = foundCustom || status.Provider == provider
	}
	if !foundCustom || len(statuses.Items) != 5 {
		t.Fatalf("dynamic Provider credential inventory is incomplete: %#v", statuses)
	}

	const transientSecret = "temporary-team-gateway-key"
	credentialBody := `{"version":"provider_credential.v1","action":"set",` +
		`"secret":"` + transientSecret + `","confirm":true}`
	credentialResponse := performSessionMessageRequest(t, api, http.MethodPost,
		"/api/v1/models/credentials/"+provider, testControlToken, "", "application/json",
		strings.NewReader(credentialBody))
	if strings.Contains(credentialResponse.Body.String(), transientSecret) ||
		strings.Contains(credentialResponse.Body.String(), `"secret"`) {
		t.Fatalf("custom Provider credential response exposed plaintext: %s",
			credentialResponse.Body.String())
	}
	var credentialStatus ProviderCredentialStatusView
	decodeDataStatus(t, credentialResponse, http.StatusAccepted, &credentialStatus)
	if credentialStatus.Provider != provider || !credentialStatus.Configured ||
		credentialStatus.PlaintextReturned || !credentialStatus.RegistryReloaded ||
		credentialStatus.RestartRequired {
		t.Fatalf("unexpected custom Provider credential status: %#v", credentialStatus)
	}
	deleteBody := `{"version":"provider_definition_control.v1",` +
		`"expected_collection_revision":1,"expected_definition_revision":1,"confirm":true}`
	deletedResponse := performSessionMessageRequest(t, api, http.MethodPost, path+"/delete",
		testControlToken, "", "application/json", strings.NewReader(deleteBody))
	var deleted ProviderDefinitionMutationView
	decodeDataStatus(t, deletedResponse, http.StatusAccepted, &deleted)
	if deleted.DeletedID != provider || deleted.Collection.Revision != 2 ||
		len(deleted.Collection.Providers) != 0 || !deleted.RegistryReloaded {
		t.Fatalf("unexpected custom Provider deletion: %#v", deleted)
	}
	if configured, err := credentialStore.Configured(t.Context(), provider); err != nil || configured {
		t.Fatal("coordinated Provider deletion retained its credential")
	}
}

func TestProviderDefinitionHTTPIsDefaultOff(t *testing.T) {
	fixture := newAPIFixture(t)
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		ProviderDefinitionsPath, testAccessToken, "", "", nil),
		http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodPost,
		"/api/v1/models/provider-definitions/team-gateway", testControlToken,
		"", "application/json", strings.NewReader(`{}`)),
		http.StatusNotFound, "NOT_FOUND")
}
