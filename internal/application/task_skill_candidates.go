package application

import (
	"context"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
)

const TaskSkillCandidatesVersion = "task_skill_candidates.v1"
const MaxTaskSkillCandidates = 1024

type TaskSkillCandidatesRequest struct {
	WorkspaceID string
	Surface     domain.ExecutionSurface
	Phase       domain.ExecutionPhase
	Profile     domain.Profile
}

type TaskSkillCandidateItem struct {
	Ref             string `json:"ref"`
	Name            string `json:"name"`
	Version         string `json:"version,omitempty"`
	TokenUpperBound int    `json:"token_upper_bound"`
}

// Display metadata is separate from the exact submitted ref. Native components
// reuse their skill_read pin, including the installation generation.
type TaskSkillCandidatesView struct {
	Version          string                   `json:"version"`
	WorkspaceID      string                   `json:"workspace_id"`
	Surface          domain.ExecutionSurface  `json:"surface"`
	Phase            domain.ExecutionPhase    `json:"phase"`
	Profile          domain.Profile           `json:"profile"`
	Items            []TaskSkillCandidateItem `json:"items"`
	UnavailableCount int                      `json:"unavailable_count"`
	CapabilityGrant  bool                     `json:"capability_grant"`
}

type TaskSkillCandidatesStore interface {
	taskConfigurationStore
	ExternalSkillSelectionStore
	skills.PackageObjectLoader
	ListInstalledPackages(context.Context, domain.ExecutionSurface, domain.Profile, bool) ([]skills.InstalledPackage, error)
}

// ListTaskSkillCandidates performs no selection or activation. A later select
// still checks the actual Created Run, its mode and operator confirmation.
func ListTaskSkillCandidates(ctx context.Context, store TaskSkillCandidatesStore,
	request TaskSkillCandidatesRequest,
) (TaskSkillCandidatesView, error) {
	view := TaskSkillCandidatesView{Version: TaskSkillCandidatesVersion,
		WorkspaceID: request.WorkspaceID, Surface: request.Surface, Phase: request.Phase,
		Profile: request.Profile, Items: []TaskSkillCandidateItem{}}
	profile, profileErr := domain.ParseProfile(string(request.Profile))
	if !domain.ValidAgentID(request.WorkspaceID) || strings.TrimSpace(request.WorkspaceID) != request.WorkspaceID ||
		!request.Surface.Valid() || !request.Phase.Valid() || profileErr != nil || profile != request.Profile {
		return view, apperror.New(apperror.CodeInvalidArgument, "an exact workspace, surface, phase and Profile are required")
	}
	if store == nil {
		return view, apperror.New(apperror.CodeFailedPrecondition, "task Skill candidate inventory is unavailable")
	}
	workspace, err := store.GetWorkspaceInfo(ctx, request.WorkspaceID)
	if err != nil {
		return view, apperror.Normalize(err)
	}
	if workspace.ID != request.WorkspaceID || workspace.RootPath == "" {
		return view, apperror.New(apperror.CodeFailedPrecondition, "task Skill workspace record is invalid")
	}
	pluginValues, err := externalSkillPluginInventory(ctx, store)
	if err != nil {
		return view, err
	}
	installed, err := store.ListInstalledPackages(ctx, request.Surface, request.Profile, false)
	if err != nil {
		return view, apperror.Normalize(err)
	}
	refs := map[string]bool{}
	for _, value := range installed {
		refs[skills.FormatInstalledPackageRef(value.Installation.Name, value.Installation.Version)] = true
	}
	for _, value := range pluginValues {
		if value.Snapshot == nil || value.Source.Surface != string(request.Surface) || value.State != plugins.StateEnabled {
			continue
		}
		if value.Snapshot.Legacy != nil {
			manifest := value.Snapshot.Legacy.Manifest
			refs[skills.FormatInstalledPackageRef(manifest.Name, manifest.Version)] = true
			continue
		}
		for _, skill := range value.Snapshot.Skills {
			source, err := plugins.SkillSelectionSourceForComponent(value, skill.Instructions.Component.ComponentID)
			if err != nil {
				view.UnavailableCount++
				continue
			}
			refs[skills.ExternalSelectionItemRef(source.Item)] = true
		}
	}
	if len(refs) > MaxTaskSkillCandidates {
		return view, apperror.New(apperror.CodeResourceExhausted, "task Skill candidates exceed the complete inventory bound")
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		return view, err
	}
	mode := domain.RunModeSnapshot{Surface: request.Surface, Phase: request.Phase, Profile: request.Profile}
	for ref := range refs {
		if err := ctx.Err(); err != nil {
			return view, err
		}
		name, _, pin, err := parseExternalSkillReference(ref)
		if _, reserved := builtins.Get(name); err != nil || (!pin.Portable() && reserved) {
			view.UnavailableCount++
			continue
		}
		packages, sources, err := resolveExternalSkillReferences(ctx, store, mode, []string{ref}, pluginValues)
		if err != nil {
			// Ineligible or ambiguous identities are withheld as a whole. Storage
			// failures must remain visible instead of looking like an empty list.
			switch apperror.CodeOf(err) {
			case apperror.CodeConflict, apperror.CodePolicyDenied, apperror.CodeNotFound, apperror.CodeInvalidArgument:
				view.UnavailableCount++
				continue
			default:
				return view, err
			}
		}
		selection, err := skills.ResolveExternalSelection(skills.ResolveExternalSelectionRequest{
			SelectionID: "task-skill-preview", RunID: "task-skill-preview-run", MissionID: "task-skill-preview-mission",
			ModeSnapshotID: "task-skill-preview-mode", ModeRevision: 1,
			Surface: request.Surface, Phase: request.Phase, Profile: request.Profile, Packages: packages, Sources: sources,
			TokenBudget: skills.MaxExternalSelectionTokenBudget, RequestedBy: "task_skill_preview", Confirmed: true, CreatedAt: time.Now().UTC(),
		})
		if err != nil || !taskSkillCandidateObjectAvailable(ctx, store, selection, pluginValues) {
			if err := ctx.Err(); err != nil {
				return view, err
			}
			view.UnavailableCount++
			continue
		}
		item := selection.Items[0]
		view.Items = append(view.Items, TaskSkillCandidateItem{Ref: ref, Name: item.Name, Version: item.Version, TokenUpperBound: item.TokenUpperBound})
	}
	sort.Slice(view.Items, func(i, j int) bool { return view.Items[i].Ref < view.Items[j].Ref })
	return view, nil
}

// A missing retained object hides only that ref. Metadata discovery never
// exposes source bytes, and cannot authorize reading a different installation.
func taskSkillCandidateObjectAvailable(ctx context.Context, store TaskSkillCandidatesStore,
	selection skills.ExternalSelection, pluginValues []plugins.Installation,
) bool {
	for _, item := range selection.Items {
		if item.Plugin == nil {
			descriptor := skills.PackageObjectDescriptor{ProtocolVersion: skills.PackageObjectProtocolVersion,
				ArchiveSHA256: item.ArchiveSHA256, PackageFingerprint: item.PackageFingerprint, ArchiveBytes: item.ArchiveBytes}
			loaded, err := store.Load(ctx, descriptor)
			if err != nil || loaded.Validate(descriptor) != nil {
				return false
			}
			continue
		}
		reader, ok := store.(portableSkillReadStore)
		if !ok {
			return false
		}
		found := false
		for _, value := range pluginValues {
			if value.ID != item.InstallationID {
				continue
			}
			for _, skill := range value.Snapshot.Skills {
				if skill.Instructions.Component.ComponentID != item.Plugin.ComponentID {
					continue
				}
				body, _, err := readPortableSkillContent(ctx, reader, value, portableSkillPin(value, skill), ctx.Err)
				source, sourceErr := plugins.SkillSelectionSourceForComponent(value, skill.Instructions.Component.ComponentID)
				if err != nil || sourceErr != nil || skills.ValidateExternalSelectionContent(source.Manifest, body, item.Plugin.Native) != nil {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
