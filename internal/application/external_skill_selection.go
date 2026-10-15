package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

type ExternalSkillSelectionStore interface {
	GetMission(context.Context, string) (domain.Mission, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetInstalledPackageByRef(context.Context, string, string) (
		skills.InstalledPackage, bool, error)
	ActiveSkillCatalogPin(context.Context, string, domain.ExecutionSurface) (
		skills.CatalogPin, bool, error)
	GetExternalSkillSelection(context.Context, string) (skills.ExternalSelection, error)
	GetExternalSkillSelectionByRun(context.Context, string) (
		skills.ExternalSelection, bool, error)
	GetExternalSkillSelectionOperation(context.Context, string) (
		skills.ExternalSelectionOperation, bool, error)
	CreateExternalSkillSelection(context.Context, skills.ExternalSelection,
		skills.ExternalSelectionOperation, events.Event) (skills.ExternalSelection, bool, error)
}

type ExternalSkillSelectionService struct {
	store ExternalSkillSelectionStore
}

type SelectExternalSkillsRequest struct {
	RunID                   string
	PackageRefs             []string
	SpecialistRef           string
	TokenBudget             int
	OperationKey            string
	RequestedBy             string
	ConfirmUntrustedContext bool
}

type SelectExternalSkillsResult struct {
	Selection skills.ExternalSelection
	Replayed  bool
}

func NewExternalSkillSelectionService(
	store ExternalSkillSelectionStore,
) *ExternalSkillSelectionService {
	return &ExternalSkillSelectionService{store: store}
}

func (s *ExternalSkillSelectionService) Select(ctx context.Context,
	request SelectExternalSkillsRequest,
) (SelectExternalSkillsResult, error) {
	if s == nil || s.store == nil {
		return SelectExternalSkillsResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"external Skill selection store is required")
	}
	normalized, err := normalizeSelectExternalSkillsRequest(request)
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	if !normalized.ConfirmUntrustedContext {
		return SelectExternalSkillsResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"external Skill context selection requires explicit operator confirmation")
	}
	run, err := s.store.GetRun(ctx, normalized.RunID)
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Normalize(err)
	}
	keyDigest := runmutation.Fingerprint("external_skill_selection_operation.v1",
		run.ID, normalized.OperationKey)
	if existing, found, err := s.store.GetExternalSkillSelectionOperation(ctx,
		keyDigest); err != nil {
		return SelectExternalSkillsResult{}, apperror.Normalize(err)
	} else if found {
		if existing.RunID != run.ID || existing.RequestedBy != normalized.RequestedBy {
			return SelectExternalSkillsResult{}, apperror.New(apperror.CodeConflict,
				"external Skill selection operation key was already used for different intent")
		}
		stored, err := s.store.GetExternalSkillSelection(ctx, existing.SelectionID)
		if err != nil {
			return SelectExternalSkillsResult{}, apperror.Normalize(err)
		}
		if existing.SelectionID != stored.ID || existing.RunID != stored.RunID ||
			existing.RequestedBy != stored.RequestedBy ||
			!existing.CreatedAt.Equal(stored.CreatedAt) ||
			stored.MissionID != mission.ID ||
			skills.ExternalSelectionRequestFingerprint(stored) != existing.RequestFingerprint {
			return SelectExternalSkillsResult{}, apperror.New(apperror.CodeInternal,
				"stored external Skill selection operation binding is invalid")
		}
		if !externalSelectionMatchesRequest(stored, normalized) {
			return SelectExternalSkillsResult{}, apperror.New(apperror.CodeConflict,
				"external Skill selection operation key was already used for different intent")
		}
		return SelectExternalSkillsResult{Selection: stored, Replayed: true}, nil
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Wrap(apperror.CodeInternal,
			"built-in Skill Registry could not be loaded", err)
	}
	for _, ref := range normalized.PackageRefs {
		name, _, pin, parseErr := parseExternalSkillReference(ref)
		if parseErr != nil {
			return SelectExternalSkillsResult{}, apperror.Wrap(
				apperror.CodeInvalidArgument, "external Skill reference is invalid", parseErr)
		}
		if _, reserved := builtins.Get(name); !pin.Portable() && reserved {
			return SelectExternalSkillsResult{}, apperror.New(apperror.CodeConflict,
				"external Skill selections cannot use a built-in Skill name")
		}
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Normalize(err)
	}
	pluginValues, err := externalSkillPluginInventory(ctx, s.store)
	if err != nil {
		return SelectExternalSkillsResult{}, err
	}
	packages, sources, err := resolveExternalSkillReferences(ctx, s.store, mode, normalized.PackageRefs, pluginValues)
	if err != nil {
		return SelectExternalSkillsResult{}, err
	}
	now := time.Now().UTC()
	candidate, err := skills.ResolveExternalSelection(skills.ResolveExternalSelectionRequest{
		SelectionID: idgen.New("external-skill-selection"), RunID: run.ID,
		MissionID: mission.ID, ModeSnapshotID: mode.ID, ModeRevision: mode.Revision,
		Surface: mode.Surface, Phase: mode.Phase, Profile: mission.Profile, Packages: packages, Sources: sources,
		SpecialistRef: normalized.SpecialistRef, TokenBudget: normalized.TokenBudget,
		RequestedBy: normalized.RequestedBy, Confirmed: true, CreatedAt: now,
	})
	if err != nil {
		return SelectExternalSkillsResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"external Skill selection could not be resolved", err)
	}
	requestFingerprint := skills.ExternalSelectionRequestFingerprint(candidate)
	if run.Status != domain.RunCreated {
		return SelectExternalSkillsResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"external Skills can only be selected before a Run starts")
	}
	operation := skills.ExternalSelectionOperation{
		KeyDigest: keyDigest, RequestFingerprint: requestFingerprint,
		SelectionID: candidate.ID, RunID: run.ID, RequestedBy: candidate.RequestedBy,
		CreatedAt: candidate.CreatedAt,
	}
	event, err := events.New(run.ID, mission.ID,
		events.ExternalSkillSelectionCreatedEvent, "external_skills", candidate.ID,
		map[string]any{
			"protocol": candidate.ProtocolVersion, "surface": candidate.Surface,
			"profile": candidate.Profile, "item_count": candidate.ItemCount,
			"token_budget":       candidate.TokenBudget,
			"token_upper_bound":  candidate.TokenUpperBound,
			"operator_confirmed": true, "context_delivery": true,
			"tool_capability_grant": false,
		})
	if err != nil {
		return SelectExternalSkillsResult{}, err
	}
	event.CreatedAt = candidate.CreatedAt
	stored, replayed, err := s.store.CreateExternalSkillSelection(ctx, candidate,
		operation, event)
	return SelectExternalSkillsResult{Selection: stored, Replayed: replayed},
		apperror.Normalize(err)
}

func externalSkillPluginInventory(ctx context.Context, store any) ([]plugins.Installation, error) {
	inventory, ok := store.(interface {
		ListPluginInstallations(context.Context, string, int) ([]plugins.Installation, error)
	})
	if !ok {
		return nil, nil
	}
	values, err := inventory.ListPluginInstallations(ctx, "", portableSkillCatalogInstallLimit)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	if len(values) >= portableSkillCatalogInstallLimit {
		return nil, apperror.New(apperror.CodeResourceExhausted, "explicit Skill resolution requires a complete Plugin inventory")
	}
	return values, nil
}

// Candidate discovery and selection resolve the same exact identities. In
// particular, discovery must never replace an ambiguous ref with a newer copy.
func resolveExternalSkillReferences(ctx context.Context, store ExternalSkillSelectionStore,
	mode domain.RunModeSnapshot, refs []string, pluginValues []plugins.Installation,
) ([]skills.InstalledPackage, []skills.ExternalSelectionSource, error) {
	packages := make([]skills.InstalledPackage, 0, len(refs))
	var sources []skills.ExternalSelectionSource
	for _, ref := range refs {
		name, version, pin, err := parseExternalSkillReference(ref)
		if err != nil {
			return nil, nil, apperror.Wrap(
				apperror.CodeInvalidArgument, "external Skill reference is invalid", err)
		}
		if pin.Portable() {
			var matched *plugins.Installation
			for index := range pluginValues {
				if pluginValues[index].ID == pin.InstallationID {
					matched = &pluginValues[index]
					break
				}
			}
			if matched == nil {
				return nil, nil, apperror.New(apperror.CodeNotFound, "installed Skill component was not found")
			}
			if matched.Snapshot == nil || matched.Snapshot.Legacy != nil || matched.Source.Surface != string(mode.Surface) ||
				matched.PackageID() != pin.PackageID || matched.Revision() != pin.Revision || matched.Generation != pin.InstallationGeneration {
				return nil, nil, apperror.New(apperror.CodePolicyDenied, "installed Skill component scope, revision or generation changed")
			}
			source, err := plugins.SkillSelectionSourceForComponent(*matched, pin.ComponentID)
			if err != nil {
				return nil, nil, apperror.Wrap(apperror.CodePolicyDenied, "Plugin Skill cannot be selected", err)
			}
			if skills.ExternalSelectionItemRef(source.Item) != ref {
				return nil, nil, apperror.New(apperror.CodePolicyDenied, "installed Skill component reference changed")
			}
			sources = append(sources, source)
			continue
		}
		installed, found, err := store.GetInstalledPackageByRef(ctx, name, version)
		if err != nil {
			return nil, nil, apperror.Normalize(err)
		}
		var pluginMatch *plugins.Installation
		for i := range pluginValues {
			value := &pluginValues[i]
			if value.Snapshot == nil || value.Snapshot.Legacy == nil || value.Source.Surface != string(mode.Surface) || value.Snapshot.Legacy.Manifest.Name != name || value.Snapshot.Legacy.Manifest.Version != version {
				continue
			}
			// Only the currently enabled version is eligible; reject ambiguity with
			// an old record or another enabled installation instead of substituting.
			if value.State != plugins.StateEnabled {
				continue
			}
			if found || pluginMatch != nil {
				return nil, nil, apperror.New(apperror.CodeConflict, "external Skill reference is ambiguous: "+ref)
			}
			pluginMatch = value
		}
		// A catalog pin, when present, is the enable/disable and active-version
		// decision: a disabled or non-pinned version cannot be selected. Skills
		// without any pin keep the existing operator-confirmed flow.
		if pin, pinned, err := store.ActiveSkillCatalogPin(ctx, name, mode.Surface); err != nil {
			return nil, nil, apperror.Normalize(err)
		} else if pinned {
			if !pin.Enabled {
				return nil, nil, apperror.New(apperror.CodePolicyDenied,
					"external Skill is disabled by its catalog pin: "+ref)
			}
			if pin.Version != version {
				return nil, nil, apperror.New(apperror.CodePolicyDenied,
					"external Skill version is not the catalog-pinned version: "+ref)
			}
		}
		if pluginMatch != nil {
			source, err := plugins.SkillSelectionSource(*pluginMatch)
			if err != nil {
				return nil, nil, apperror.Wrap(apperror.CodePolicyDenied, "Plugin Skill cannot be selected", err)
			}
			sources = append(sources, source)
			continue
		}
		if !found {
			return nil, nil, apperror.New(apperror.CodeNotFound, "installed external Skill package was not found: "+ref)
		}
		packages = append(packages, installed)
	}
	return packages, sources, nil
}

func externalSelectionMatchesRequest(selection skills.ExternalSelection,
	request SelectExternalSkillsRequest,
) bool {
	if selection.RunID != request.RunID || selection.RequestedBy != request.RequestedBy ||
		selection.TokenBudget != request.TokenBudget ||
		len(selection.Items) != len(request.PackageRefs) {
		return false
	}
	specialistRef := ""
	for index, item := range selection.Items {
		if skills.ExternalSelectionItemRef(item) !=
			request.PackageRefs[index] {
			return false
		}
		if item.SpecialistEligible {
			if specialistRef != "" {
				return false
			}
			specialistRef = request.PackageRefs[index]
		}
	}
	return specialistRef == request.SpecialistRef
}

func (s *ExternalSkillSelectionService) GetForRun(ctx context.Context,
	runID string,
) (skills.ExternalSelection, error) {
	if s == nil || s.store == nil {
		return skills.ExternalSelection{}, apperror.New(apperror.CodeFailedPrecondition,
			"external Skill selection store is required")
	}
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return skills.ExternalSelection{}, apperror.New(apperror.CodeInvalidArgument,
			"external Skill selection Run id is invalid")
	}
	selection, found, err := s.store.GetExternalSkillSelectionByRun(ctx, runID)
	if err != nil {
		return skills.ExternalSelection{}, apperror.Normalize(err)
	}
	if !found {
		return skills.ExternalSelection{}, apperror.New(apperror.CodeNotFound,
			"external Skill selection was not found")
	}
	return selection, nil
}

func normalizeSelectExternalSkillsRequest(
	request SelectExternalSkillsRequest,
) (SelectExternalSkillsRequest, error) {
	originalKey := request.OperationKey
	request.RunID = strings.TrimSpace(request.RunID)
	request.SpecialistRef = strings.TrimSpace(request.SpecialistRef)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if request.RequestedBy == "" {
		request.RequestedBy = "cli_operator"
	}
	if request.TokenBudget == 0 {
		request.TokenBudget = skills.DefaultExternalSelectionTokenBudget
	}
	if !domain.ValidAgentID(request.RunID) || strings.ContainsRune(request.RunID, 0) ||
		!domain.ValidAgentID(request.RequestedBy) || strings.ContainsRune(request.RequestedBy, 0) {
		return SelectExternalSkillsRequest{}, errors.New("bounded Run and operator identities are required")
	}
	if request.OperationKey != strings.TrimSpace(originalKey) ||
		!utf8.ValidString(request.OperationKey) {
		return SelectExternalSkillsRequest{}, errors.New("external Skill selection operation key must be normalized UTF-8")
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil {
		return SelectExternalSkillsRequest{}, err
	}
	for _, current := range request.OperationKey {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return SelectExternalSkillsRequest{}, errors.New("external Skill selection operation key cannot contain whitespace or control characters")
		}
	}
	if len(request.PackageRefs) == 0 ||
		len(request.PackageRefs) > skills.MaxExternalSelectionItems {
		return SelectExternalSkillsRequest{}, errors.New("external Skill selection requires a bounded non-empty package list")
	}
	refs := make([]string, len(request.PackageRefs))
	for index, ref := range request.PackageRefs {
		if strings.TrimSpace(ref) != ref || !utf8.ValidString(ref) {
			return SelectExternalSkillsRequest{}, errors.New("external Skill references must be normalized UTF-8")
		}
		if _, _, _, err := parseExternalSkillReference(ref); err != nil {
			return SelectExternalSkillsRequest{}, err
		}
		refs[index] = ref
	}
	sort.Strings(refs)
	for index := 1; index < len(refs); index++ {
		if refs[index] == refs[index-1] {
			return SelectExternalSkillsRequest{}, errors.New("external Skill references must be unique")
		}
	}
	if request.SpecialistRef != "" {
		if _, _, _, err := parseExternalSkillReference(request.SpecialistRef); err != nil {
			return SelectExternalSkillsRequest{}, err
		}
		if index := sort.SearchStrings(refs, request.SpecialistRef); index >= len(refs) ||
			refs[index] != request.SpecialistRef {
			return SelectExternalSkillsRequest{}, errors.New("specialist external Skill must also appear in the selected package list")
		}
	}
	request.PackageRefs = refs
	return request, nil
}

// Native selections copy the existing exact skill_read pin. Requiring its
// canonical JSON prevents alternate spellings from bypassing duplicate/replay
// checks; catalog and resource reads cannot stand in for a selected component.
func parseExternalSkillReference(ref string) (string, string, toolgateway.SkillReadRequest, error) {
	if strings.HasPrefix(ref, "{") {
		pin, canonical, err := toolgateway.NormalizeSkillReadPayload([]byte(ref))
		if err != nil || !pin.Portable() || pin.Resource != "" || string(canonical) != ref {
			return "", "", toolgateway.SkillReadRequest{}, errors.New("external Skill component requires its exact canonical skill_read reference")
		}
		return "", "", pin, nil
	}
	name, version, err := skills.ParseInstalledPackageRef(ref)
	return name, version, toolgateway.SkillReadRequest{}, err
}
