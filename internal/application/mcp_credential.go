package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/mcpoauth"
)

const MCPCredentialProtocolVersion = "mcp-credential.v1"

type MCPCredentialBinding struct {
	ServerID                      string `json:"server_id"`
	WorkspaceID                   string `json:"workspace_id"`
	RunID                         string `json:"run_id,omitempty"`
	ExpectedDescriptorFingerprint string `json:"expected_descriptor_fingerprint"`
	Target                        string `json:"target"`
	CredentialRef                 string `json:"credential_ref"`
}

type MCPCredentialStatus struct {
	ProtocolVersion       string                `json:"protocol_version"`
	ServerID              string                `json:"server_id"`
	WorkspaceID           string                `json:"workspace_id"`
	RunID                 string                `json:"run_id,omitempty"`
	DescriptorFingerprint string                `json:"descriptor_fingerprint"`
	Target                string                `json:"target"`
	CredentialRef         string                `json:"credential_ref"`
	Configured            bool                  `json:"configured"`
	StoreKind             string                `json:"store_kind"`
	StoreAvailable        bool                  `json:"store_available"`
	PlaintextReturned     bool                  `json:"plaintext_returned"`
	RegistrationCount     int                   `json:"registration_count"`
	ReferenceFingerprint  string                `json:"reference_fingerprint"`
	EndpointConflict      bool                  `json:"endpoint_conflict"`
	OAuth                 *mcpoauth.OAuthStatus `json:"oauth,omitempty"`
}

type ChangeMCPCredentialRequest struct {
	Binding                      MCPCredentialBinding
	Version                      string
	Action                       string
	OAuthFlowID                  string
	OAuthIssuer                  string
	OAuthClientID                string
	OAuthClientIDMode            string
	OAuthClientSecret            string
	OAuthCallbackPort            int
	Secret                       string
	Confirm                      bool
	ExpectedReferenceFingerprint string
}

type MCPCredentialRegistry interface {
	GetMCPClientServer(context.Context, string) (mcp.ServerRecord, error)
	ListMCPClientServersByCredentialRef(context.Context, string) ([]mcp.ServerRecord, error)
}

// The OS store remains the sole secret owner. Presence is local metadata, not
// proof of remote authentication. No secret or backend error enters a reply.
type MCPCredentialService struct {
	mu        sync.Mutex
	registry  MCPCredentialRegistry
	store     credential.Store
	oauth     map[string]*mcpoauth.Flow
	oauthHTTP *http.Client
}

func NewMCPCredentialService(registry MCPCredentialRegistry, store credential.Store) *MCPCredentialService {
	return &MCPCredentialService{registry: registry, store: store, oauth: make(map[string]*mcpoauth.Flow)}
}

func (s *MCPCredentialService) Status(ctx context.Context, binding MCPCredentialBinding) (MCPCredentialStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.status(ctx, binding)
	if err == nil {
		err = s.oauthStatus(ctx, binding, &value)
	}
	return value, err
}

func (s *MCPCredentialService) status(ctx context.Context, binding MCPCredentialBinding) (MCPCredentialStatus, error) {
	if s.registry == nil || s.store == nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "MCP credential storage is unavailable")
	}
	if !credential.ValidName(binding.CredentialRef) || binding.ServerID == "" || binding.WorkspaceID == "" {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeInvalidArgument, "MCP credential binding is invalid")
	}
	record, err := s.registry.GetMCPClientServer(ctx, binding.ServerID)
	if err != nil {
		return MCPCredentialStatus{}, err
	}
	d := record.Descriptor
	if record.Validate() != nil || (d.ProtocolVersion != mcp.ClientProtocolVersion && d.ProtocolVersion != mcp.ConfigClientProtocolVersion) || d.NativeSource != nil ||
		(d.CredentialRef == "" && len(d.EnvCredentialRefs) == 0) {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "This MCP descriptor does not reference the managed credential")
	}
	if record.DescriptorFingerprint != binding.ExpectedDescriptorFingerprint || d.Target != binding.Target ||
		!d.ReferencesCredential(binding.CredentialRef) || d.WorkspaceID != binding.WorkspaceID || d.RunID != binding.RunID {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeConflict, "MCP credential descriptor or scope changed; refresh its binding")
	}
	registrations, err := s.registry.ListMCPClientServersByCredentialRef(ctx, binding.CredentialRef)
	if err != nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "MCP credential references could not be verified")
	}
	fingerprints := make([]string, 0, len(registrations))
	found, conflict := false, false
	for _, current := range registrations {
		if current.Validate() != nil || !current.Descriptor.ReferencesCredential(binding.CredentialRef) {
			return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "MCP credential reference metadata is invalid")
		}
		found = found || current.Descriptor.ID == d.ID
		conflict = conflict || current.Descriptor.Target != d.Target || current.Descriptor.Transport != d.Transport
		fingerprints = append(fingerprints, current.DescriptorFingerprint)
	}
	if !found {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeConflict, "MCP credential registration changed; refresh its binding")
	}
	sort.Strings(fingerprints)
	digest := sha256.Sum256([]byte(strings.Join(fingerprints, "\n")))
	result := MCPCredentialStatus{ProtocolVersion: MCPCredentialProtocolVersion, ServerID: d.ID,
		WorkspaceID: d.WorkspaceID, RunID: d.RunID, DescriptorFingerprint: record.DescriptorFingerprint,
		Target: d.Target, CredentialRef: binding.CredentialRef, StoreKind: s.store.Kind(), StoreAvailable: s.store.Available(),
		RegistrationCount: len(registrations), ReferenceFingerprint: hex.EncodeToString(digest[:]), EndpointConflict: conflict}
	if result.StoreAvailable {
		result.Configured, err = s.store.Configured(ctx, binding.CredentialRef)
		if err != nil {
			return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "MCP credential presence could not be verified")
		}
	}
	return result, nil
}

func (s *MCPCredentialService) Change(ctx context.Context, request ChangeMCPCredentialRequest) (MCPCredentialStatus, error) {
	if strings.HasPrefix(request.Action, "oauth-") {
		return s.changeOAuth(ctx, request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.Version != MCPCredentialProtocolVersion || !request.Confirm || request.OAuthIssuer != "" || request.OAuthClientID != "" || request.OAuthClientIDMode != "" || request.OAuthFlowID != "" || request.OAuthClientSecret != "" || request.OAuthCallbackPort != 0 ||
		(request.Action != "set" && request.Action != "delete") ||
		(request.Action == "set" && (len([]byte(request.Secret)) < 8 || !credential.ValidSecret(request.Secret) || mcpoauth.IsEnvelope(request.Secret))) ||
		(request.Action == "delete" && request.Secret != "") {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeInvalidArgument, "MCP credential change requires an explicit action, confirmation and valid bounded input")
	}
	status, err := s.status(ctx, request.Binding)
	if err != nil {
		return MCPCredentialStatus{}, err
	}
	if !status.StoreAvailable {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "System credential storage is unavailable; no plaintext fallback exists")
	}
	if status.EndpointConflict || status.ReferenceFingerprint != request.ExpectedReferenceFingerprint {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeConflict, "MCP credential references changed or bind different endpoints; refresh before changing the shared credential")
	}
	if flow := s.oauth[request.Binding.CredentialRef]; flow != nil {
		flow.Cancel()
		delete(s.oauth, request.Binding.CredentialRef)
	}
	unlock := mcpoauth.CredentialLock(request.Binding.CredentialRef)
	defer unlock()
	if request.Action == "set" {
		err = s.store.Put(ctx, request.Binding.CredentialRef, request.Secret)
	} else {
		err = s.store.Delete(ctx, request.Binding.CredentialRef)
	}
	request.Secret = ""
	if err != nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "System credential change could not be verified; refresh presence before retrying")
	}
	result, err := s.status(ctx, request.Binding)
	if err != nil || result.Configured != (request.Action == "set") || result.EndpointConflict ||
		result.ReferenceFingerprint != request.ExpectedReferenceFingerprint {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "System credential change completed without verified presence; refresh before retrying")
	}
	return result, nil
}

func (s *ExtensionControlService) WithCredentials(store credential.Store) *ExtensionControlService {
	if registry, ok := s.store.(MCPCredentialRegistry); ok && store != nil {
		s.credentials = NewMCPCredentialService(registry, store)
	}
	return s
}

func (s *ExtensionControlService) HasMCPCredentialControl() bool { return s.credentials != nil }

func (s *ExtensionControlService) MCPCredentialStatus(ctx context.Context, binding MCPCredentialBinding) (MCPCredentialStatus, error) {
	if s.credentials == nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "MCP credential control is unavailable")
	}
	return s.credentials.Status(ctx, binding)
}

func (s *ExtensionControlService) ChangeMCPCredential(ctx context.Context, request ChangeMCPCredentialRequest) (MCPCredentialStatus, error) {
	if s.credentials == nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "MCP credential control is unavailable")
	}
	return s.credentials.Change(ctx, request)
}
