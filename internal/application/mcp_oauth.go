package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/mcpoauth"
)

func oauthBinding(b MCPCredentialBinding) mcpoauth.Binding {
	return mcpoauth.Binding{Ref: b.CredentialRef, Fingerprint: b.ExpectedDescriptorFingerprint, Target: b.Target}
}
func (s *MCPCredentialService) oauthStatus(ctx context.Context, b MCPCredentialBinding, status *MCPCredentialStatus) error {
	if flow := s.oauth[b.CredentialRef]; flow != nil {
		if !flow.MatchesBinding(oauthBinding(b)) {
			status.OAuth = &mcpoauth.OAuthStatus{State: "binding_changed"}
			return nil
		}
		value := flow.Status()
		// The handoff URL contains ephemeral state and is returned only to the
		// operator's start mutation, never through read-only status polling.
		value.AuthorizationURL = ""
		if value.State != "authorized" {
			status.OAuth = &value
			return nil
		}
	}
	if !status.StoreAvailable || !status.Configured {
		return nil
	}
	raw, found, err := s.store.Get(ctx, b.CredentialRef)
	if err != nil {
		return apperror.New(apperror.CodeUnavailable, "MCP OAuth presence could not be verified")
	}
	if found && mcpoauth.IsEnvelope(raw) {
		value, err := mcpoauth.StoredStatus(raw, oauthBinding(b))
		if err != nil {
			value = mcpoauth.OAuthStatus{State: "binding_changed"}
		}
		status.OAuth = &value
	}
	return nil
}
func (s *MCPCredentialService) changeOAuth(ctx context.Context, r ChangeMCPCredentialRequest) (MCPCredentialStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func() (MCPCredentialStatus, error) {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeFailedPrecondition, "MCP OAuth requires a dedicated HTTPS credential, current binding and explicit confirmation")
	}
	if r.Version != MCPCredentialProtocolVersion || !r.Confirm || r.Secret != "" {
		return fail()
	}
	status, err := s.status(ctx, r.Binding)
	if err != nil {
		return status, err
	}
	record, err := s.registry.GetMCPClientServer(ctx, r.Binding.ServerID)
	if err != nil {
		return status, err
	}
	if r.Action == "oauth-cancel" {
		if r.OAuthIssuer != "" || r.OAuthClientID != "" || r.OAuthClientIDMode != "" {
			return fail()
		}
		if flow := s.oauth[r.Binding.CredentialRef]; flow != nil {
			if !flow.MatchesBinding(oauthBinding(r.Binding)) || r.OAuthFlowID == "" || r.OAuthFlowID != flow.Status().FlowID {
				return fail()
			}
			flow.Cancel()
		}
		err = s.oauthStatus(ctx, r.Binding, &status)
		return status, err
	}
	if record.Descriptor.Transport != mcp.TransportStreamableHTTP || record.State == mcp.TrustRevoked || !status.StoreAvailable || status.EndpointConflict || status.RegistrationCount != 1 || status.ReferenceFingerprint != r.ExpectedReferenceFingerprint {
		return fail()
	}
	if r.OAuthFlowID != "" {
		return fail()
	}
	if r.Action == "oauth-discover" {
		if r.OAuthIssuer != "" || r.OAuthClientID != "" || r.OAuthClientIDMode != "" {
			return fail()
		}
		value, err := mcpoauth.Discover(ctx, r.Binding.Target, s.oauthHTTP)
		if err != nil {
			return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "OAuth resource metadata could not be verified")
		}
		status.OAuth = &value
		return status, nil
	}
	if r.Action != "oauth-start" {
		return fail()
	}
	if flow := s.oauth[r.Binding.CredentialRef]; flow != nil && (flow.Status().State == "authorizing" || flow.Status().State == "starting") {
		if flow.MatchesBinding(oauthBinding(r.Binding)) {
			return MCPCredentialStatus{}, apperror.New(apperror.CodeConflict, "MCP OAuth authorization is already pending; cancel it before restarting")
		}
		flow.Cancel()
	}
	// Record the prior entry, including absence, so completion cannot overwrite an
	// intervening static-token change or a login in another service instance.
	prior, priorFound, err := s.store.Get(ctx, r.Binding.CredentialRef)
	if err != nil {
		return fail()
	}
	flow, err := mcpoauth.Start(ctx, oauthBinding(r.Binding), r.OAuthIssuer, r.OAuthClientID, r.OAuthClientIDMode, s.oauthHTTP, func(ctx context.Context, raw string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		current, err := s.status(ctx, r.Binding)
		if err != nil || current.ReferenceFingerprint != r.ExpectedReferenceFingerprint || current.EndpointConflict || current.RegistrationCount != 1 {
			return mcpoauth.ErrOAuth
		}
		record, err := s.registry.GetMCPClientServer(ctx, r.Binding.ServerID)
		if err != nil || record.State == mcp.TrustRevoked {
			return mcpoauth.ErrOAuth
		}
		unlock := mcpoauth.CredentialLock(r.Binding.CredentialRef)
		defer unlock()
		value, found, err := s.store.Get(ctx, r.Binding.CredentialRef)
		if err != nil || found != priorFound || value != prior || ctx.Err() != nil {
			return mcpoauth.ErrOAuth
		}
		if err = s.store.Put(ctx, r.Binding.CredentialRef, raw); err != nil {
			return mcpoauth.ErrOAuth
		}
		value, found, err = s.store.Get(ctx, r.Binding.CredentialRef)
		if err != nil || !found || value != raw {
			return mcpoauth.ErrOAuth
		}
		verified, verifyErr := s.status(ctx, r.Binding)
		if verifyErr != nil || verified.ReferenceFingerprint != r.ExpectedReferenceFingerprint || verified.EndpointConflict || verified.RegistrationCount != 1 {
			return mcpoauth.ErrOAuth
		}
		latest, verifyErr := s.registry.GetMCPClientServer(ctx, r.Binding.ServerID)
		if verifyErr != nil || latest.State == mcp.TrustRevoked {
			return mcpoauth.ErrOAuth
		}
		return nil
	})
	if err != nil {
		return MCPCredentialStatus{}, apperror.New(apperror.CodeUnavailable, "MCP OAuth could not start; verify issuer, public client ID and resource metadata")
	}
	s.oauth[r.Binding.CredentialRef] = flow
	value := flow.Status()
	status.OAuth = &value
	return status, nil
}
