package llm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// HTTPProviderRuntime is an immutable, credential-free request policy attached
// to one configured Provider generation. The credential resolver is invoked
// for every outbound request; implementations must not cache or expose the
// returned secret. Apply may only add configuration that was validated by the
// owning control plane.
type HTTPProviderRuntime interface {
	ResolveCredential(context.Context) (string, error)
	MapModel(string) (string, error)
	Apply(string, http.Header, map[string]any) error
	BindingDigest() string
}

// EndpointBoundHTTPProviderRuntime restricts a credential to the endpoint owned
// by its login flow, even if the runtime is attached to another HTTP adapter.
type EndpointBoundHTTPProviderRuntime interface {
	HTTPProviderRuntime
	AllowsCredentialEndpoint(string) bool
}

// KeylessHTTPProviderRuntime is an optional, endpoint-bound exception for a
// validated local provider. Static API-key configurations remain unchanged.
type KeylessHTTPProviderRuntime interface {
	HTTPProviderRuntime
	AllowsKeylessEndpoint(string) bool
}

func validateHTTPProviderRuntime(runtime HTTPProviderRuntime) error {
	if runtime == nil {
		return nil
	}
	digest := strings.TrimSpace(runtime.BindingDigest())
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 {
		return errors.New("provider request runtime binding is invalid")
	}
	return nil
}

func providerRequestCredential(ctx context.Context, provider string, static string,
	runtime HTTPProviderRuntime, endpoint string,
) (string, error) {
	secret := static
	if runtime != nil {
		if bound, ok := runtime.(EndpointBoundHTTPProviderRuntime); ok &&
			!bound.AllowsCredentialEndpoint(endpoint) {
			return "", errors.New("provider credential endpoint is unavailable")
		}
		resolved, err := runtime.ResolveCredential(ctx)
		if err != nil {
			return "", errors.New("provider credential is unavailable")
		}
		secret = resolved
	}
	if secret == "" {
		// The adapter independently checks its actual configured endpoint; a
		// runtime created for one local endpoint cannot authorize another URL.
		if local, ok := runtime.(KeylessHTTPProviderRuntime); ok {
			normalized, err := normalizeProviderBaseURL(endpoint, provider)
			parsed, parseErr := url.Parse(normalized)
			if err == nil && parseErr == nil && providerLoopbackHost(parsed.Hostname()) &&
				local.AllowsKeylessEndpoint(normalized) {
				return "", nil
			}
		}
	}
	if err := validateProviderAPIKey(secret, provider); err != nil {
		return "", errors.New("provider credential is unavailable")
	}
	return secret, nil
}

func providerRequestModel(runtime HTTPProviderRuntime, model string) (string, error) {
	if runtime == nil {
		return model, nil
	}
	mapped, err := runtime.MapModel(model)
	if err != nil {
		return "", errors.New("provider model mapping is invalid")
	}
	return mapped, nil
}

func providerRequestPayload(runtime HTTPProviderRuntime, secret string,
	wire any,
) ([]byte, error) {
	if runtime == nil {
		return json.Marshal(wire)
	}
	base, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(base)))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, errors.New("provider request body is invalid")
	}
	if err := runtime.Apply(secret, nil, body); err != nil {
		return nil, errors.New("provider request customization is invalid")
	}
	return json.Marshal(body)
}

func applyProviderRequestHeaders(runtime HTTPProviderRuntime, secret string,
	header http.Header,
) error {
	if runtime == nil {
		return nil
	}
	if err := runtime.Apply(secret, header, nil); err != nil {
		return errors.New("provider request headers are invalid")
	}
	return nil
}

func providerRuntimeBinding(runtime HTTPProviderRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.BindingDigest())
}

func providerHarnessBinding(runtime HTTPProviderRuntime, parts ...string) string {
	if runtime != nil {
		parts = append(parts, providerRuntimeBinding(runtime))
	}
	return harnessBindingDigest(parts...)
}
