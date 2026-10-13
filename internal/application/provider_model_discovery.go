package application

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/modelregistry"
	"encoding/json"
)

type ProviderModelDiscoveryRequest struct {
	Version                    string          `json:"version"`
	ProviderID                 string          `json:"provider_id"`
	EndpointURL                string          `json:"endpoint_url"`
	Transport                  string          `json:"transport"`
	AdvancedConfig             json.RawMessage `json:"advanced_config"`
	Secret                     string          `json:"secret,omitempty"`
	ExpectedDefinitionRevision uint64          `json:"expected_definition_revision,omitempty"`
	ConfirmDiscovery           bool            `json:"confirm_discovery"`
}

// WithModelDiscoveryCredentials binds the same Go-owned credential store used
// for discovery and for credential cleanup when deleting a custom definition.
func (s *ProviderDefinitionService) WithModelDiscoveryCredentials(store credential.Store) *ProviderDefinitionService {
	s.discoveryCredentials = store
	return s
}

func (s *ProviderDefinitionService) DiscoverModels(ctx context.Context, r ProviderModelDiscoveryRequest) (modelregistry.ModelDiscoveryResult, error) {
	var empty modelregistry.ModelDiscoveryResult
	if s == nil || ctx == nil || r.Version != modelregistry.ModelDiscoveryVersion || !r.ConfirmDiscovery {
		return empty, apperror.New(apperror.CodeInvalidArgument, "confirmed model discovery request is required")
	}
	o := modelregistry.ModelDiscoveryOptions{ProviderID: r.ProviderID, EndpointURL: r.EndpointURL, Transport: r.Transport, AdvancedConfig: r.AdvancedConfig, Secret: r.Secret}
	if modelregistry.ValidateModelDiscoveryDraft(o) != nil {
		return empty, apperror.New(apperror.CodeInvalidArgument, "model discovery draft is invalid")
	}
	// An explicitly entered key authorizes only this transient draft request.
	// Reading an existing key requires the persisted endpoint/transport/revision
	// both before and after the read. Only the copied value enters HTTP headers.
	if r.Secret == "" && r.ExpectedDefinitionRevision != 0 {
		if s.discoveryCredentials == nil || !s.discoveryCredentials.Available() {
			return empty, apperror.New(apperror.CodeFailedPrecondition, "stored model discovery credential is unavailable")
		}
		bound := func() bool {
			collection, err := s.List(ctx)
			if err != nil {
				return false
			}
			for _, d := range collection.Providers {
				if d.ID == r.ProviderID {
					return d.Revision == r.ExpectedDefinitionRevision && d.EndpointURL == r.EndpointURL && d.Transport == r.Transport
				}
			}
			return false
		}
		if !bound() {
			return empty, apperror.New(apperror.CodeConflict, "stored key requires the unchanged saved Provider endpoint, transport and revision")
		}
		secret, found, err := s.discoveryCredentials.Get(ctx, r.ProviderID)
		if err != nil || !found || secret == "" {
			return empty, apperror.New(apperror.CodeFailedPrecondition, "stored model discovery credential is unavailable")
		}
		if !bound() {
			return empty, apperror.New(apperror.CodeConflict, "saved Provider changed while preparing model discovery")
		}
		o.Secret = secret
	}
	result, err := modelregistry.DiscoverProviderModels(ctx, o)
	if err != nil {
		return empty, apperror.New(apperror.CodeFailedPrecondition, err.Error())
	}
	return result, nil
}
