package plugins

import (
	"errors"
	"reflect"
	"slices"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

// SkillSelectionSource projects a real legacy-format Plugin component into the
// existing Run selection resolver, without an old installation or object receipt.
func SkillSelectionSource(value Installation) (skills.ExternalSelectionSource, error) {
	if value.Validate() != nil || value.Snapshot == nil || value.Snapshot.Legacy == nil ||
		value.State != StateEnabled || !slices.Contains(value.EnabledCapabilities, CapabilitySkills) || len(value.Snapshot.Skills) != 1 {
		return skills.ExternalSelectionSource{}, errors.New("Plugin Skill is not enabled for explicit selection")
	}
	return SkillSelectionSourceForComponent(value, value.Snapshot.Skills[0].Instructions.Component.ComponentID)
}

// SkillSelectionSourceForComponent adapts one acquired Skill without inventing
// a versioned legacy package. Native source metadata supplies no host execution
// policy: the explicit selection is bounded by the installation surface and the
// existing context budgets; declared tools never become grants.
func SkillSelectionSourceForComponent(value Installation, componentID string) (skills.ExternalSelectionSource, error) {
	if value.Validate() != nil || value.Snapshot == nil || value.State != StateEnabled ||
		!slices.Contains(value.EnabledCapabilities, CapabilitySkills) {
		return skills.ExternalSelectionSource{}, errors.New("Plugin Skill is not enabled for explicit selection")
	}
	index := slices.IndexFunc(value.Snapshot.Skills, func(item SnapshotSkill) bool {
		return item.Instructions.Component.ComponentID == componentID
	})
	if index < 0 {
		return skills.ExternalSelectionSource{}, errors.New("Plugin Skill component is unavailable")
	}
	skill := value.Snapshot.Skills[index]
	component := skill.Instructions
	native := value.Snapshot.Legacy == nil
	var m skills.Manifest
	if !native {
		if len(value.Snapshot.Skills) != 1 {
			return skills.ExternalSelectionSource{}, errors.New("legacy Skill component binding is invalid")
		}
		m = value.Snapshot.Legacy.Manifest
	} else {
		profiles := []domain.Profile{domain.ProfileCode, domain.ProfileLearn, domain.ProfileReview, domain.ProfileScript}
		if value.Source.Surface == string(domain.ExecutionSurfaceCyber) {
			profiles = []domain.Profile{domain.ProfileScript}
		}
		m = skills.Manifest{Protocol: skills.ProtocolVersion, Name: skill.Name, Version: value.Snapshot.AuthorVersion,
			Description: skill.Description, Profiles: profiles,
			Surfaces:      []domain.ExecutionSurface{domain.ExecutionSurface(value.Source.Surface)},
			Phases:        []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver},
			Roles:         []domain.AgentRole{domain.AgentRoleRoot, domain.AgentRoleSpecialist},
			UserInvocable: true, ExplicitOnly: true, ToolDependencies: []toolgateway.ToolName{},
			ContentPath: component.Path, ContentSHA256: component.SHA256}
		for _, entry := range value.Snapshot.Inventory {
			if entry.Path == component.Path && entry.Kind == "file" && entry.SHA256 == component.SHA256 {
				m.ContentBytes, m.ContentTokenUpperBound = entry.Bytes, entry.Bytes
				break
			}
		}
	}
	if component.SHA256 != m.ContentSHA256 || m.ContentBytes <= 0 {
		return skills.ExternalSelectionSource{}, errors.New("Plugin Skill content identity changed")
	}
	return skills.ExternalSelectionSource{Manifest: m, Item: skills.ExternalSelectionItem{
		Plugin: &skills.PluginSkillBinding{Native: native, PackageID: component.Component.PackageID,
			ComponentID: component.Component.ComponentID, Revision: value.Revision(), Generation: value.Generation},
		InstallationID: value.ID, InstallationFingerprint: InstallationFingerprint(value),
		Name: m.Name, Version: m.Version, Surface: domain.ExecutionSurface(value.Source.Surface),
		ContentSHA256: m.ContentSHA256, ContentBytes: m.ContentBytes, TokenUpperBound: m.ContentTokenUpperBound,
		ArchiveSHA256: value.ArchiveSHA256, ArchiveBytes: value.ArchiveBytes, PackageFingerprint: value.PackageFingerprint,
		TrustClass: skills.PackageTrustOperatorInstalledUntrusted, ToolDependencyCount: len(m.ToolDependencies),
	}}, nil
}

// ValidateSkillSelection rechecks every pinned field against current lifecycle
// state. A durable user selection cannot renew a revoked or changed generation.
func ValidateSkillSelection(value Installation, item skills.ExternalSelectionItem,
	mode domain.RunModeSnapshot, role domain.AgentRole,
) (skills.Manifest, error) {
	if item.Plugin == nil {
		return skills.Manifest{}, errors.New("Plugin Skill component binding is required")
	}
	source, err := SkillSelectionSourceForComponent(value, item.Plugin.ComponentID)
	if err != nil {
		return skills.Manifest{}, err
	}
	expected := source.Item
	expected.SelectionID, expected.Ordinal, expected.SpecialistEligible = item.SelectionID, item.Ordinal, item.SpecialistEligible
	if !reflect.DeepEqual(expected, item) || mode.Surface != item.Surface ||
		(role != domain.AgentRoleRoot && role != domain.AgentRoleSpecialist) ||
		(role == domain.AgentRoleSpecialist && !item.SpecialistEligible) ||
		!source.Manifest.AllowsInvocation(skills.InvocationSourceUser, true) ||
		!source.Manifest.SupportsContext(skills.ExecutionContext{Surface: mode.Surface, Phase: mode.Phase, Profile: mode.Profile, Role: role}) {
		return skills.Manifest{}, errors.New("Plugin Skill selection no longer matches its component, generation, mode or role")
	}
	return source.Manifest, nil
}
