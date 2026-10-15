package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/mcp"
)

type Store interface {
	CreatePluginInstallation(context.Context, Installation, []byte) (Installation, bool, error)
	GetPluginInstallation(context.Context, string) (Installation, error)
	ListPluginInstallations(context.Context, string, int) ([]Installation, error)
	UpdatePluginInstallation(context.Context, Installation, int64) (Installation, error)
	RollbackPluginInstallation(context.Context, Installation, int64, Installation, int64, PublisherAuthority) (
		Installation, Installation, error)
	GetPluginPublisherTrust(context.Context, string) (PublisherTrust, bool, error)
	SetPluginPublisherTrust(context.Context, PublisherTrust, int64) (PublisherTrust, error)
	RevokePluginPublisher(context.Context, PublisherTrust, int64) (PublisherTrust, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

// PublisherAuthority pins the Service's trust decision to the atomic version
// switch. Zero generation means no publisher trust record was observed.
type PublisherAuthority struct {
	ExpectedGeneration int64
	ConfirmUntrusted   bool
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("plugin service requires a durable store")
	}
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) Stage(ctx context.Context, raw []byte, source InstallSource,
	supersedes, stagedBy string,
) (Installation, bool, error) {
	pkg, err := ParsePackage(raw)
	if err != nil {
		return Installation{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"plugin package failed inert staging validation", err)
	}
	if err := source.Validate(); err != nil || source.SHA256 != pkg.ArchiveSHA256 ||
		!validText(stagedBy, 256, false) {
		return Installation{}, false, apperror.New(apperror.CodeInvalidArgument,
			"plugin source provenance or staging actor is invalid")
	}
	if supersedes != "" {
		previous, err := s.store.GetPluginInstallation(ctx, supersedes)
		if err != nil {
			return Installation{}, false, err
		}
		if previous.ProtocolVersion != InstallationProtocol || previous.PackageID() != pkg.Manifest.ID || previous.Revision() == pkg.Manifest.Version ||
			previous.State == StateRevoked {
			return Installation{}, false, apperror.New(apperror.CodeConflict,
				"plugin upgrade does not match its predecessor")
		}
	}
	installation, err := NewInstallation(idgen.New("plugin-install"), pkg, source,
		strings.TrimSpace(supersedes), strings.TrimSpace(stagedBy), s.now())
	if err != nil {
		return Installation{}, false, err
	}
	return s.store.CreatePluginInstallation(ctx, installation, pkg.Archive())
}

type ReviewAction string

const (
	ReviewApprove    ReviewAction = "approve"
	ReviewEnable     ReviewAction = "enable"
	ReviewDisable    ReviewAction = "disable"
	ReviewRevoke     ReviewAction = "revoke"
	ReviewQuarantine ReviewAction = "quarantine"
)

type ReviewRequest struct {
	Action                     ReviewAction `json:"action"`
	ExpectedPackageFingerprint string       `json:"expected_package_fingerprint"`
	ExpectedGeneration         int64        `json:"expected_generation"`
	Capabilities               []Capability `json:"capabilities,omitempty"`
	ConfirmUntrusted           bool         `json:"confirm_untrusted"`
	ReviewedBy                 string       `json:"reviewed_by"`
}

func (s *Service) Review(ctx context.Context, installationID string,
	request ReviewRequest,
) (Installation, error) {
	installation, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(installationID))
	if err != nil {
		return Installation{}, err
	}
	if request.ExpectedPackageFingerprint != installation.PackageFingerprint ||
		request.ExpectedGeneration != installation.Generation ||
		!validText(request.ReviewedBy, 256, false) {
		return Installation{}, apperror.New(apperror.CodeConflict,
			"plugin review does not match the current installation")
	}
	trusted, publisherRevoked, publisherGeneration, err := s.publisherTrust(ctx, installation)
	if err != nil {
		return Installation{}, err
	}
	if publisherRevoked && (request.Action == ReviewApprove || request.Action == ReviewEnable) {
		return Installation{}, apperror.New(apperror.CodePolicyDenied,
			"plugin publisher is revoked and must be explicitly trusted again")
	}
	now := s.now().UTC()
	before := installation.Generation
	switch request.Action {
	case ReviewApprove:
		if installation.State != StateStaged && installation.State != StateQuarantined {
			return Installation{}, apperror.New(apperror.CodeFailedPrecondition,
				"plugin cannot be approved from its current state")
		}
		if !trusted && !request.ConfirmUntrusted {
			return Installation{}, apperror.New(apperror.CodePolicyDenied,
				"unsigned or untrusted plugin requires explicit untrusted confirmation")
		}
		installation.State = StateApproved
		installation.EnabledCapabilities = []Capability{}
	case ReviewEnable:
		if installation.State != StateApproved && installation.State != StateDisabled &&
			installation.State != StateEnabled {
			return Installation{}, apperror.New(apperror.CodeFailedPrecondition,
				"plugin capabilities cannot be enabled from its current state")
		}
		capabilities, err := normalizeCapabilities(request.Capabilities,
			installation.Capabilities())
		if err != nil {
			return Installation{}, err
		}
		if !trusted && !request.ConfirmUntrusted {
			return Installation{}, apperror.New(apperror.CodePolicyDenied,
				"enabling an unsigned or untrusted plugin requires explicit confirmation")
		}
		installation.State = StateEnabled
		installation.EnabledCapabilities = capabilities
	case ReviewDisable:
		if installation.State == StateRevoked || installation.State == StateRolledBack {
			return Installation{}, apperror.New(apperror.CodeFailedPrecondition,
				"revoked or rolled-back plugin cannot be disabled")
		}
		installation.State = StateDisabled
	case ReviewQuarantine:
		if installation.State == StateRevoked || installation.State == StateRolledBack {
			return Installation{}, apperror.New(apperror.CodeFailedPrecondition,
				"terminal plugin cannot be quarantined")
		}
		installation.State = StateQuarantined
		installation.EnabledCapabilities = []Capability{}
	case ReviewRevoke:
		installation.State = StateRevoked
		installation.EnabledCapabilities = []Capability{}
	default:
		return Installation{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin review action is invalid")
	}
	installation.ReviewedBy = request.ReviewedBy
	installation.ReviewedAt = &now
	installation.UpdatedAt = now
	installation.Generation++
	if err := installation.Validate(); err != nil {
		return Installation{}, err
	}
	if request.Action == ReviewEnable {
		active, found, err := s.activeSibling(ctx, installation)
		if err != nil {
			return Installation{}, err
		}
		if found {
			if installation.SupersedesInstallationID != active.ID {
				return Installation{}, apperror.New(apperror.CodeConflict,
					"another plugin version is enabled and is not the reviewed predecessor")
			}
			activeBefore := active.Generation
			active.State = StateRolledBack
			active.EnabledCapabilities = []Capability{}
			active.Generation++
			active.ReviewedBy, active.ReviewedAt, active.UpdatedAt = request.ReviewedBy, &now, now
			if err := active.Validate(); err != nil {
				return Installation{}, err
			}
			_, enabled, err := s.store.RollbackPluginInstallation(ctx,
				active, activeBefore, installation, before, PublisherAuthority{
					ExpectedGeneration: publisherGeneration, ConfirmUntrusted: request.ConfirmUntrusted})
			return enabled, err
		}
	}
	return s.store.UpdatePluginInstallation(ctx, installation, before)
}

type RollbackRequest struct {
	ExpectedCurrentFingerprint string       `json:"expected_current_fingerprint"`
	ExpectedCurrentGeneration  int64        `json:"expected_current_generation"`
	ExpectedTargetFingerprint  string       `json:"expected_target_fingerprint"`
	ExpectedTargetGeneration   int64        `json:"expected_target_generation"`
	Capabilities               []Capability `json:"capabilities"`
	ConfirmUntrusted           bool         `json:"confirm_untrusted"`
	ReviewedBy                 string       `json:"reviewed_by"`
}

func (s *Service) Rollback(ctx context.Context, currentID, targetID string,
	request RollbackRequest,
) (Installation, Installation, error) {
	current, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(currentID))
	if err != nil {
		return Installation{}, Installation{}, err
	}
	target, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(targetID))
	if err != nil {
		return Installation{}, Installation{}, err
	}
	if current.PackageID() != target.PackageID() || current.ProtocolVersion != target.ProtocolVersion || current.Source.Surface != target.Source.Surface || current.ID == target.ID ||
		request.ExpectedCurrentFingerprint != current.PackageFingerprint ||
		request.ExpectedTargetFingerprint != target.PackageFingerprint ||
		request.ExpectedCurrentGeneration != current.Generation ||
		request.ExpectedTargetGeneration != target.Generation ||
		!validText(request.ReviewedBy, 256, false) || current.State != StateEnabled ||
		target.State == StateRevoked || target.State == StateEnabled {
		return Installation{}, Installation{}, apperror.New(apperror.CodeConflict,
			"plugin rollback bindings are invalid or stale")
	}
	trusted, publisherRevoked, publisherGeneration, err := s.publisherTrust(ctx, target)
	if err != nil {
		return Installation{}, Installation{}, err
	}
	if publisherRevoked {
		return Installation{}, Installation{}, apperror.New(apperror.CodePolicyDenied,
			"plugin publisher is revoked and must be explicitly trusted again")
	}
	if !trusted && !request.ConfirmUntrusted {
		return Installation{}, Installation{}, apperror.New(apperror.CodePolicyDenied,
			"rolling back to an untrusted plugin requires explicit confirmation")
	}
	capabilities, err := normalizeCapabilities(request.Capabilities,
		target.Capabilities())
	if err != nil {
		return Installation{}, Installation{}, err
	}
	now := s.now().UTC()
	currentBefore, targetBefore := current.Generation, target.Generation
	current.State, current.EnabledCapabilities = StateRolledBack, []Capability{}
	current.Generation++
	current.ReviewedBy, current.ReviewedAt, current.UpdatedAt = request.ReviewedBy, &now, now
	target.State, target.EnabledCapabilities = StateEnabled, capabilities
	target.Generation++
	target.ReviewedBy, target.ReviewedAt, target.UpdatedAt = request.ReviewedBy, &now, now
	if err := current.Validate(); err != nil {
		return Installation{}, Installation{}, err
	}
	if err := target.Validate(); err != nil {
		return Installation{}, Installation{}, err
	}
	return s.store.RollbackPluginInstallation(ctx, current, currentBefore, target, targetBefore,
		PublisherAuthority{ExpectedGeneration: publisherGeneration, ConfirmUntrusted: request.ConfirmUntrusted})
}

func (s *Service) TrustPublisher(ctx context.Context, installationID, actor string) (
	PublisherTrust, error,
) {
	installation, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(installationID))
	if err != nil {
		return PublisherTrust{}, err
	}
	if !installation.SignatureValid || !validText(actor, 256, false) {
		return PublisherTrust{}, apperror.New(apperror.CodeInvalidArgument,
			"publisher trust requires a valid signed plugin and review actor")
	}
	existing, found, err := s.store.GetPluginPublisherTrust(ctx,
		installation.PublisherFingerprint)
	if err != nil {
		return PublisherTrust{}, err
	}
	expected := int64(0)
	if found {
		expected = existing.Generation
	}
	return s.trustPublisher(ctx, installation, expected, actor)
}

// TrustInstallationPublisher binds a grant to the exact signed installation and
// publisher-trust generation the operator reviewed, mirroring
// RevokeInstallationPublisher so a stale view cannot trust another record.
func (s *Service) TrustInstallationPublisher(ctx context.Context, installationID,
	expectedFingerprint string, expectedGeneration int64, actor string,
) (PublisherTrust, error) {
	installation, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(installationID))
	if err != nil {
		return PublisherTrust{}, err
	}
	if !installation.SignatureValid || installation.PublisherFingerprint != expectedFingerprint {
		return PublisherTrust{}, apperror.New(apperror.CodeConflict,
			"publisher trust differs from the selected signed installation")
	}
	return s.trustPublisher(ctx, installation, expectedGeneration, actor)
}

func (s *Service) trustPublisher(ctx context.Context, installation Installation,
	expectedGeneration int64, actor string,
) (PublisherTrust, error) {
	if !validText(actor, 256, false) {
		return PublisherTrust{}, apperror.New(apperror.CodeInvalidArgument,
			"publisher trust requires a review actor")
	}
	existing, found, err := s.store.GetPluginPublisherTrust(ctx,
		installation.PublisherFingerprint)
	if err != nil {
		return PublisherTrust{}, err
	}
	if (found && existing.Generation != expectedGeneration) ||
		(!found && expectedGeneration != 0) {
		return PublisherTrust{}, apperror.New(apperror.CodeConflict,
			"plugin publisher trust is missing or stale")
	}
	record := PublisherTrust{ProtocolVersion: PublisherTrustProtocol,
		Fingerprint: installation.PublisherFingerprint,
		Publisher:   installation.Manifest.Publisher, PublicKey: installation.PublisherPublicKey,
		State: PublisherTrusted, Generation: expectedGeneration + 1, ReviewedBy: actor,
		ReviewedAt: s.now().UTC()}
	return s.store.SetPluginPublisherTrust(ctx, record, expectedGeneration)
}

func (s *Service) RevokePublisher(ctx context.Context, fingerprint string,
	expectedGeneration int64, actor string,
) (PublisherTrust, error) {
	existing, found, err := s.store.GetPluginPublisherTrust(ctx, strings.TrimSpace(fingerprint))
	if err != nil {
		return PublisherTrust{}, err
	}
	if !found || existing.Generation != expectedGeneration ||
		!validText(actor, 256, false) {
		return PublisherTrust{}, apperror.New(apperror.CodeConflict,
			"plugin publisher revocation is missing or stale")
	}
	existing.State = PublisherRevoked
	existing.Generation++
	existing.ReviewedBy = actor
	existing.ReviewedAt = s.now().UTC()
	return s.store.RevokePluginPublisher(ctx, existing, expectedGeneration)
}

func (s *Service) ActiveHooks(ctx context.Context) ([]hooks.Registration, error) {
	installations, err := s.store.ListPluginInstallations(ctx, "", 1_000)
	if err != nil {
		return nil, err
	}
	result := make([]hooks.Registration, 0)
	for _, installation := range installations {
		if installation.State != StateEnabled ||
			!slices.Contains(installation.EnabledCapabilities, CapabilityHooks) {
			continue
		}
		for _, declaration := range installation.Manifest.Hooks {
			result = append(result, hooks.Registration{PluginID: installation.Manifest.ID,
				PluginFingerprint: installation.PackageFingerprint, Declaration: declaration})
		}
	}
	return result, nil
}

type MCPStager interface {
	Stage(context.Context, mcp.ServerDescriptor) (mcp.ServerRecord, bool, error)
}

func (s *Service) StageMCPServers(ctx context.Context, installationID string,
	scope mcp.ScopeKind, runID, workspaceID string, stager MCPStager,
) ([]mcp.ServerRecord, error) {
	if stager == nil {
		return nil, errors.New("MCP server stager is required")
	}
	installation, err := s.store.GetPluginInstallation(ctx, strings.TrimSpace(installationID))
	if err != nil {
		return nil, err
	}
	if installation.State != StateEnabled ||
		!slices.Contains(installation.EnabledCapabilities, CapabilityMCP) {
		return nil, apperror.New(apperror.CodePolicyDenied,
			"plugin MCP capability is not enabled")
	}
	if installation.Snapshot != nil {
		return s.stagePortableMCPServers(ctx, installation, scope, runID, workspaceID, stager)
	}
	values := make([]mcp.ServerRecord, 0, len(installation.Manifest.MCPServers))
	for _, contribution := range installation.Manifest.MCPServers {
		descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion,
			ID: installation.Manifest.ID + "." + contribution.ID, Name: contribution.Name,
			Transport: contribution.Transport, Target: contribution.Target,
			Arguments: contribution.Arguments, CredentialRef: contribution.CredentialRef,
			DeclaredCapabilities: contribution.DeclaredCapabilities, Scope: scope,
			RunID: runID, WorkspaceID: workspaceID,
			Source: mcp.Source{Kind: "plugin", URI: installation.Source.URI,
				Version: installation.Manifest.Version, Commit: installation.Source.Commit,
				SHA256: installation.ArchiveSHA256, PluginID: installation.Manifest.ID,
				Publisher:   installation.Manifest.Publisher,
				Fingerprint: installation.PackageFingerprint},
			CallTimeoutMillis: contribution.CallTimeoutMillis,
			MaxResultBytes:    contribution.MaxResultBytes}
		record, _, err := stager.Stage(ctx, descriptor)
		if err != nil {
			return values, err
		}
		values = append(values, record)
	}
	return values, nil
}

func (s *Service) publisherTrust(ctx context.Context, installation Installation) (
	trusted, revoked bool, generation int64, err error,
) {
	if !installation.SignatureValid {
		return false, false, 0, nil
	}
	record, found, err := s.store.GetPluginPublisherTrust(ctx,
		installation.PublisherFingerprint)
	if err != nil || !found {
		return false, false, 0, err
	}
	bound := record.Publisher == installation.Manifest.Publisher &&
		record.PublicKey == installation.PublisherPublicKey
	// A revoked signing-key fingerprint stays revoked across publisher display
	// names. Positive trust additionally binds the reviewed name and public key.
	return bound && record.State == PublisherTrusted,
		record.State == PublisherRevoked, record.Generation, nil
}

func (s *Service) activeSibling(ctx context.Context, installation Installation) (
	Installation, bool, error,
) {
	values, err := s.store.ListPluginInstallations(ctx, installation.PackageID(), 1_000)
	if err != nil {
		return Installation{}, false, err
	}
	for _, value := range values {
		if value.ID != installation.ID && value.State == StateEnabled {
			return value, true, nil
		}
	}
	return Installation{}, false, nil
}

func normalizeCapabilities(values, declared []Capability) ([]Capability, error) {
	if len(values) == 0 || len(values) > len(declared) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"plugin enablement requires one or more declared capabilities")
	}
	result := slices.Clone(values)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	for index, value := range result {
		if !value.Valid() || !slices.Contains(declared, value) ||
			(index > 0 && result[index-1] == value) {
			return nil, fmt.Errorf("plugin capability %q is invalid or repeated", value)
		}
	}
	return result, nil
}
