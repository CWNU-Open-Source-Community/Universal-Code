package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/chatgptauth"
	"cyberagent-workbench/internal/llm"
)

const CredentialSourceChatGPT = "chatgpt_oauth"

type ChatGPTCredentialClient interface {
	Credentials(context.Context, chatgptauth.Store, string) (*chatgptauth.Auth, error)
}

type chatGPTSource struct {
	client     ChatGPTCredentialClient
	store      chatgptauth.Store
	snapshot   func(context.Context) (*chatgptauth.Database, error)
	httpClient *http.Client
}

// WithChatGPT binds the protected account store before LoadRouteSettings.
// Reload preserves this source; no OAuth token enters the API-key reader.
func (r *Registry) WithChatGPT(client ChatGPTCredentialClient, store chatgptauth.Store) *Registry {
	if r != nil {
		r.chatGPT = &chatGPTSource{client: client, store: store,
			snapshot: func(ctx context.Context) (*chatgptauth.Database, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return store.Snapshot()
			}}
		r.chatGPTLoaded = false
	}
	return r
}

// ChatGPTProviderID is a bounded account route identity. Client IDs remain in
// the protected store and runtime; unusual or long issued IDs are never used as
// public Provider names or OS credential names.
func ChatGPTProviderID(clientID string) string {
	if clientID == "" {
		return "chatgpt"
	}
	digest := sha256.Sum256([]byte(clientID))
	return "chatgpt-" + hex.EncodeToString(digest[:16])
}

func (r *Registry) loadChatGPTProviders(ctx context.Context) error {
	if r.chatGPT == nil {
		return nil
	}
	if ctx == nil || r.chatGPT.snapshot == nil {
		return errors.New("ChatGPT account source is unavailable")
	}
	accounts, err := r.chatGPT.snapshot(ctx)
	if err != nil || accounts == nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Keep a broken or inaccessible optional account store visible while
		// allowing unrelated providers to start. No account or credential is
		// reconstructed from defaults when the protected store cannot be read.
		r.appendChatGPTUnavailable(true)
		return nil
	}
	if len(accounts.Accounts) == 0 {
		r.appendChatGPTUnavailable(false)
		return nil
	}
	seen := make(map[string]bool)
	for _, account := range accounts.Accounts {
		providerID := ChatGPTProviderID(account.ClientID)
		if account.ClientID == "" || account.ClientID == chatgptauth.DynamicClient ||
			account.Issuer != chatgptauth.Issuer || account.Subject == "" || seen[providerID] {
			return errors.New("ChatGPT registration identity is invalid")
		}
		seen[providerID] = true
		availability := ProviderAvailability{Name: providerID, DisplayName: "ChatGPT",
			Kind: ProviderKindOpenAICompatible, Status: ProviderNotConfigured,
			CredentialSource: CredentialSourceChatGPT, NetworkRequired: true,
			Transport: llm.HarnessTransportOpenAIResponses,
			Models:    []string{}, Harnesses: []HarnessAvailability{}}
		if validPublicProviderText(account.Email, maxPublicProviderNameBytes-12, false) && account.Email != "" {
			availability.DisplayName += " (" + account.Email + ")"
		}
		models := make([]llm.ModelInfo, 0, len(account.Models))
		modelIDs := make(map[string]bool)
		for _, model := range account.Models {
			if model.Visibility != "list" {
				continue
			}
			if !validAvailabilityIdentifier(model.Slug, maxPublicModelNameBytes) ||
				!validPublicProviderText(model.DisplayName, maxPublicModelNameBytes, true) ||
				modelIDs[model.Slug] || len(models) >= maxProviderDefinitionModels {
				return errors.New("ChatGPT account model catalog is invalid")
			}
			modelIDs[model.Slug] = true
			availability.Models = append(availability.Models, model.Slug)
			availability.Harnesses = append(availability.Harnesses,
				unqualifiedHarnessAvailability(model.Slug, llm.HarnessTransportOpenAIResponses,
					llm.HarnessJSONStrategyNative))
			models = append(models, llm.ModelInfo{ID: model.Slug, DisplayName: model.DisplayName,
				Provider: providerID, Capabilities: []string{"tools", "json"}})
		}
		canRefresh := account.RefreshToken != "" || account.PendingRefresh != nil
		if r.chatGPT.client != nil && len(models) != 0 && account.PlanEnabled() &&
			account.AccessToken != "" && strings.EqualFold(account.TokenType, "Bearer") &&
			(account.ExpiresAt.After(time.Now()) || canRefresh) {
			runtime := newChatGPTRequestRuntime(r.chatGPT, account, modelIDs)
			provider, err := llm.NewChatGPTProvider(llm.ChatGPTProviderConfig{
				Name: providerID, DefaultModel: models[0].ID, Models: models,
				Runtime: runtime, HTTPClient: r.chatGPT.httpClient,
			})
			if err != nil {
				return errors.New("ChatGPT model runtime could not be installed")
			}
			r.router.RegisterProvider(provider)
			r.available[providerID] = struct{}{}
			availability.Status = ProviderAvailable
		}
		r.providers = append(r.providers, availability)
	}
	sort.Slice(r.providers, func(i, j int) bool { return r.providers[i].Name < r.providers[j].Name })
	return nil
}

func (r *Registry) appendChatGPTUnavailable(configurationError bool) {
	status := ProviderNotConfigured
	if configurationError {
		status = ProviderInvalidConfiguration
	}
	r.providers = append(r.providers, ProviderAvailability{Name: "chatgpt",
		DisplayName: "ChatGPT", Kind: ProviderKindOpenAICompatible,
		Status: status, ConfigurationError: configurationError,
		CredentialSource: CredentialSourceChatGPT, NetworkRequired: true,
		Models: []string{}, Harnesses: []HarnessAvailability{},
		Transport: llm.HarnessTransportOpenAIResponses})
}

type chatGPTRequestRuntime struct {
	source   *chatGPTSource
	account  string
	subject  string
	revision uint64
	models   map[string]bool
	binding  string
}

var _ llm.EndpointBoundHTTPProviderRuntime = (*chatGPTRequestRuntime)(nil)

func newChatGPTRequestRuntime(source *chatGPTSource, account chatgptauth.Auth,
	models map[string]bool,
) *chatGPTRequestRuntime {
	binding := sha256.Sum256([]byte(strings.Join([]string{"chatgpt_request_runtime.v1",
		chatgptauth.Issuer, account.ClientID, account.Subject,
		strconv.FormatUint(account.Revision, 10), llm.ChatGPTResponsesEndpoint,
		llm.ChatGPTToolNamespace}, "\x00")))
	return &chatGPTRequestRuntime{source: source, account: account.ClientID,
		subject: account.Subject, revision: account.Revision, models: models,
		binding: hex.EncodeToString(binding[:])}
}

func (r *chatGPTRequestRuntime) ResolveCredential(ctx context.Context) (string, error) {
	if ctx == nil || ctx.Err() != nil || r.source == nil || r.source.client == nil {
		return "", errors.New("ChatGPT account is unavailable")
	}
	auth, err := r.source.client.Credentials(ctx, r.source.store, r.account)
	if err != nil || auth == nil || auth.ClientID != r.account || auth.Subject != r.subject ||
		auth.Revision != r.revision || auth.Issuer != chatgptauth.Issuer ||
		!auth.PlanEnabled() || !strings.EqualFold(auth.TokenType, "Bearer") ||
		auth.AccessToken == "" || !auth.ExpiresAt.After(time.Now()) || auth.PendingRefresh != nil {
		return "", errors.New("ChatGPT account is unavailable; sign in or enable plan usage")
	}
	return auth.AccessToken, nil
}

func (r *chatGPTRequestRuntime) AllowsCredentialEndpoint(endpoint string) bool {
	return endpoint == llm.ChatGPTResponsesEndpoint
}

func (r *chatGPTRequestRuntime) MapModel(model string) (string, error) {
	if !r.models[model] {
		return "", errors.New("model is absent from the ChatGPT account catalog")
	}
	return model, nil
}

func (r *chatGPTRequestRuntime) Apply(_ string, _ http.Header, _ map[string]any) error {
	return nil
}

func (r *chatGPTRequestRuntime) BindingDigest() string { return r.binding }
