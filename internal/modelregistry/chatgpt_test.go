package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/chatgptauth"
	"cyberagent-workbench/internal/llm"
)

type chatGPTFixture struct {
	mu           sync.Mutex
	accounts     map[string]chatgptauth.Auth
	active       string
	lookups      []string
	requests     []*http.Request
	bodies       []map[string]any
	snapshots    int
	wrongAccount string
}

func chatGPTTestAuth(client, token, model string) chatgptauth.Auth {
	return chatgptauth.Auth{ClientID: client, Subject: "subject-" + client,
		Issuer: chatgptauth.Issuer, Revision: 1, Email: "test@example.invalid",
		AccessToken: token, RefreshToken: "private-refresh-" + client,
		TokenType: "Bearer", Scopes: strings.Fields(chatgptauth.Scope),
		ExpiresAt: time.Now().Add(time.Hour),
		Models:    []chatgptauth.Model{{Slug: model, DisplayName: "Test model", Visibility: "list"}}}
}

func (f *chatGPTFixture) Credentials(ctx context.Context, _ chatgptauth.Store,
	account string,
) (*chatgptauth.Auth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, account)
	if f.wrongAccount != "" {
		account = f.wrongAccount
	}
	auth, found := f.accounts[account]
	if !found {
		return nil, chatgptauth.ErrNotFound
	}
	return &auth, nil
}

func (f *chatGPTFixture) snapshot(context.Context) (*chatgptauth.Database, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots++
	database := &chatgptauth.Database{Active: f.active}
	for _, auth := range f.accounts {
		database.Accounts = append(database.Accounts, auth)
	}
	return database, nil
}

type chatGPTRoundTripFunc func(*http.Request) (*http.Response, error)

func (f chatGPTRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func chatGPTTextSSE(model string) string {
	return fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"response-fixture\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":%q}}\n\n"+
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"message-fixture\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\"}}\n\n"+
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"message-fixture\",\"delta\":\"ok\"}\n\n"+
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"message-fixture\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n"+
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-fixture\",\"object\":\"response\",\"status\":\"completed\",\"model\":%q,\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", model, model)
}

func (f *chatGPTFixture) transport(request *http.Request) (*http.Response, error) {
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, request.Clone(request.Context()))
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	model, _ := body["model"].(string)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(chatGPTTextSSE(model))), Request: request}, nil
}

func chatGPTTestRegistry(t *testing.T, accounts ...chatgptauth.Auth) (*Registry, *chatGPTFixture) {
	t.Helper()
	fixture := &chatGPTFixture{accounts: make(map[string]chatgptauth.Auth)}
	for _, account := range accounts {
		fixture.accounts[account.ClientID] = account
	}
	if len(accounts) != 0 {
		fixture.active = accounts[0].ClientID
	}
	registry := New(nil).WithChatGPT(fixture, chatgptauth.Store{})
	registry.chatGPT.snapshot = fixture.snapshot
	registry.chatGPT.httpClient = &http.Client{Transport: chatGPTRoundTripFunc(fixture.transport)}
	if err := registry.LoadRouteSettings(t.Context(), routeSettings{}); err != nil {
		t.Fatal(err)
	}
	return registry, fixture
}

func TestChatGPTRegistryUsesDynamicTokenAndRejectsSignedOutRuntime(t *testing.T) {
	auth := chatGPTTestAuth("oaiapp-first", "private-access-first", "gpt-fixture")
	registry, fixture := chatGPTTestRegistry(t, auth)
	ref := llm.ModelRef{Provider: ChatGPTProviderID(auth.ClientID), Model: auth.Models[0].Slug}
	request := llm.ChatRequest{Messages: []llm.Message{{Role: "system", Content: "Local policy"},
		{Role: "user", Content: "Hello"}}, MaxTokens: 256, Temperature: 0.7,
		Tools: []llm.ToolSpec{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	before, err := registry.Router().HarnessProfile(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"private-access-first", "private-access-refreshed"} {
		fixture.mu.Lock()
		latest := fixture.accounts[auth.ClientID]
		latest.AccessToken = token
		fixture.accounts[auth.ClientID] = latest
		fixture.mu.Unlock()
		response, err := registry.Router().ChatModelRef(t.Context(), ref, request)
		if err != nil || response == nil || response.Text != "ok" || response.Usage.TotalTokens != 3 {
			t.Fatalf("completed ChatGPT request = %#v, %v", response, err)
		}
	}
	fixture.mu.Lock()
	if len(fixture.requests) != 2 {
		t.Fatalf("inference request count = %d", len(fixture.requests))
	}
	for i, outbound := range fixture.requests {
		expected := []string{"private-access-first", "private-access-refreshed"}[i]
		body := fixture.bodies[i]
		if outbound.URL.String() != llm.ChatGPTResponsesEndpoint ||
			outbound.Header.Get("Authorization") != "Bearer "+expected ||
			body["stream"] != true || body["store"] != false ||
			body["temperature"] != nil || body["max_output_tokens"] != nil ||
			body["instructions"] != "Local policy" {
			t.Fatalf("ChatGPT request policy = %s %#v", outbound.URL, body)
		}
		namespace := body["tools"].([]any)[0].(map[string]any)
		if namespace["type"] != "namespace" || namespace["name"] != llm.ChatGPTToolNamespace {
			t.Fatalf("function tools were not grouped: %#v", namespace)
		}
	}
	delete(fixture.accounts, auth.ClientID)
	fixture.mu.Unlock()
	after, _ := registry.Router().HarnessProfile(ref)
	if before.BindingDigest != after.BindingDigest {
		t.Fatal("token refresh changed model Harness binding")
	}
	if _, err := registry.Router().ChatModelRef(t.Context(), ref, request); err == nil ||
		strings.Contains(err.Error(), "private-access") {
		t.Fatalf("signed-out runtime did not reject safely: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 2 {
		t.Fatal("signed-out runtime dispatched an inference request")
	}
}

func TestChatGPTAccountRoutesRemainIndependentAcrossReloadAndReplacement(t *testing.T) {
	first := chatGPTTestAuth("oaiapp-first", "private-first-account", "shared-model")
	second := chatGPTTestAuth("oaiapp-second", "private-second-account", "shared-model")
	registry, fixture := chatGPTTestRegistry(t, first, second)
	firstRef := llm.ModelRef{Provider: ChatGPTProviderID(first.ClientID), Model: "shared-model"}
	secondRef := llm.ModelRef{Provider: ChatGPTProviderID(second.ClientID), Model: "shared-model"}
	request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "Hello"}}}
	for _, ref := range []llm.ModelRef{firstRef, secondRef} {
		if _, err := registry.Router().ChatModelRef(t.Context(), ref, request); err != nil {
			t.Fatal(err)
		}
	}
	fixture.mu.Lock()
	fixture.active = second.ClientID
	fixture.mu.Unlock()
	if _, err := registry.Router().ChatModelRef(t.Context(), firstRef, request); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	for i, account := range []string{first.ClientID, second.ClientID, first.ClientID} {
		if fixture.lookups[i] != account || fixture.requests[i].Header.Get("Authorization") != "Bearer "+fixture.accounts[account].AccessToken {
			t.Fatalf("account selection retargeted an existing route: %#v", fixture.lookups)
		}
	}
	fixture.wrongAccount = second.ClientID
	fixture.mu.Unlock()
	if _, err := registry.Router().ChatModelRef(t.Context(), firstRef, request); err == nil {
		t.Fatal("another account was accepted for a captured runtime")
	}
	fixture.mu.Lock()
	fixture.wrongAccount = ""
	withoutConsent := first
	withoutConsent.Scopes = []string{"resource.invoke"}
	fixture.accounts[first.ClientID] = withoutConsent
	fixture.mu.Unlock()
	if _, err := registry.Router().ChatModelRef(t.Context(), firstRef, request); err == nil {
		t.Fatal("captured runtime used an account whose plan permission was removed")
	}
	fixture.mu.Lock()
	replaced := first
	replaced.Revision++
	replaced.AccessToken = "private-replacement-token"
	fixture.accounts[first.ClientID] = replaced
	fixture.mu.Unlock()
	oldProfile, _ := registry.Router().HarnessProfile(firstRef)
	if _, err := registry.Router().ChatModelRef(t.Context(), firstRef, request); err == nil {
		t.Fatal("new login reused the old registration runtime")
	}
	if _, err := registry.Reload(t.Context(), routeSettings{"route.code": firstRef.Provider + "/" + firstRef.Model}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Router().ChatModelRef(t.Context(), firstRef, request); err != nil {
		t.Fatalf("reload lost the ChatGPT source: %v", err)
	}
	newProfile, _ := registry.Router().HarnessProfile(firstRef)
	if oldProfile.BindingDigest == newProfile.BindingDigest {
		t.Fatal("replacement registration reused the prior Harness binding")
	}
	fixture.mu.Lock()
	if fixture.snapshots != 2 || len(fixture.requests) != 4 ||
		fixture.requests[3].Header.Get("Authorization") != "Bearer private-replacement-token" {
		t.Fatalf("reload requests=%d snapshots=%d", len(fixture.requests), fixture.snapshots)
	}
	fixture.mu.Unlock()
	snapshot := registry.Snapshot()
	provider, found := providerByName(snapshot, firstRef.Provider)
	if !found || provider.Custom || provider.Status != ProviderAvailable ||
		provider.Transport != llm.HarnessTransportOpenAIResponses ||
		provider.CredentialSource != CredentialSourceChatGPT || !strings.HasPrefix(provider.DisplayName, "ChatGPT") {
		t.Fatalf("managed account projection = %#v", provider)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, private := range []string{first.AccessToken, first.RefreshToken, replaced.AccessToken, first.ClientID} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("availability exposed account credential or registration: %s", private)
		}
	}
}

func TestChatGPTRegistryProjectsMissingConsentAndEmptyCatalogAsUnavailable(t *testing.T) {
	registry, _ := chatGPTTestRegistry(t)
	provider, found := providerByName(registry.Snapshot(), "chatgpt")
	if !found || provider.Status != ProviderNotConfigured || len(provider.Models) != 0 {
		t.Fatalf("unconfigured ChatGPT projection = %#v", provider)
	}
	for _, reason := range []string{"consent", "catalog", "logout"} {
		t.Run(reason, func(t *testing.T) {
			auth := chatGPTTestAuth("oaiapp-account", "private-access", "test-model")
			switch reason {
			case "consent":
				auth.Scopes = []string{"openid", "resource.invoke"}
			case "catalog":
				auth.Models = nil
			case "logout":
				auth.AccessToken = ""
				auth.RefreshToken = ""
			}
			registry, fixture := chatGPTTestRegistry(t, auth)
			provider, found := providerByName(registry.Snapshot(), ChatGPTProviderID(auth.ClientID))
			if !found || provider.Status != ProviderNotConfigured || len(fixture.requests) != 0 {
				t.Fatalf("%s account projection = %#v", reason, provider)
			}
			if _, err := registry.Router().ChatModelRef(t.Context(), llm.ModelRef{
				Provider: provider.Name, Model: "test-model"}, llm.ChatRequest{}); err == nil {
				t.Fatal("unavailable account was callable")
			}
		})
	}
}

func TestChatGPTRuntimeCannotSendOAuthToCustomEndpointsOrUnknownModels(t *testing.T) {
	auth := chatGPTTestAuth("oaiapp-account", "private-oauth-access", "allowed-model")
	registry, fixture := chatGPTTestRegistry(t, auth)
	runtime := newChatGPTRequestRuntime(registry.chatGPT, auth, map[string]bool{"allowed-model": true})
	var requests int
	client := &http.Client{Transport: chatGPTRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected HTTP request")
	})}
	provider, err := llm.NewOpenAIResponsesProvider(llm.OpenAIResponsesConfig{
		Name: "custom-endpoint", BaseURL: "https://proxy.example.invalid/v1", DefaultModel: "allowed-model",
		Runtime: runtime, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Chat(t.Context(), llm.ChatRequest{}); err == nil || requests != 0 || len(fixture.lookups) != 0 {
		t.Fatalf("custom endpoint resolved or dispatched an OAuth credential: %v", err)
	}
	if _, err := llm.FetchModelCatalog(t.Context(), llm.ModelCatalogRequest{Provider: "custom-endpoint",
		EndpointURL: "https://proxy.example.invalid/v1", Transport: llm.HarnessTransportOpenAIResponses,
		Runtime: runtime, HTTPClient: client}); err == nil || requests != 0 || len(fixture.lookups) != 0 {
		t.Fatalf("custom model discovery borrowed the OAuth credential: %v", err)
	}
	if _, err := registry.Router().ChatModelRef(t.Context(), llm.ModelRef{
		Provider: ChatGPTProviderID(auth.ClientID), Model: "unknown-model"}, llm.ChatRequest{}); err == nil {
		t.Fatal("model outside the account catalog was accepted")
	}
	for _, id := range []string{"chatgpt", ChatGPTProviderID(auth.ClientID), "chatgpt-session-owned-key"} {
		if ValidCustomProviderID(id) {
			t.Fatalf("custom provider could occupy protected ChatGPT namespace: %s", id)
		}
	}
}

func TestChatGPTUnreadableStoreKeepsOtherProvidersAvailable(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "CYBERAGENT_OPENAI_API_KEY" {
			return "private-ordinary-api-key", true
		}
		return "", false
	}
	registry := New(lookup).WithChatGPT(&chatGPTFixture{}, chatgptauth.Store{})
	registry.chatGPT.snapshot = func(context.Context) (*chatgptauth.Database, error) {
		return nil, errors.New("OS credential store is unavailable; private-internal-value")
	}
	settings := routeSettings{"route.code": "openai/" + DefaultOpenAIModel}
	if err := registry.LoadRouteSettings(t.Context(), settings); err != nil {
		t.Fatalf("optional ChatGPT store prevented application startup: %v", err)
	}
	for _, reload := range []bool{false, true} {
		if reload {
			if _, err := registry.Reload(t.Context(), settings); err != nil {
				t.Fatal(err)
			}
		}
		snapshot := registry.Snapshot()
		chatgpt, found := providerByName(snapshot, "chatgpt")
		openai, apiFound := providerByName(snapshot, "openai")
		if !found || chatgpt.Status != ProviderInvalidConfiguration || !chatgpt.ConfigurationError ||
			!apiFound || openai.Status != ProviderAvailable || registry.Router().Resolve("code").Provider != "openai" {
			t.Fatalf("unreadable account store escaped its provider: %#v", snapshot)
		}
		encoded, _ := json.Marshal(snapshot)
		if strings.Contains(string(encoded), "private-internal-value") || strings.Contains(string(encoded), "private-ordinary-api-key") {
			t.Fatal("private account store failure entered public availability")
		}
	}
}
