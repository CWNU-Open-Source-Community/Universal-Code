package application

import (
	"context"
	"slices"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/skills"
)

// Project suggestions are inert, exact package identities. Selection uses the
// ordinary selection services; repository input can never install a package.
type ProjectSkillSelectionRequest struct {
	RunID                   string
	Refs                    []string
	OperationKey            string
	RequestedBy             string
	ConfirmUntrustedContext bool
}

type ProjectSkillSelectionResult struct {
	RunID               string   `json:"run_id"`
	Refs                []string `json:"refs"`
	BuiltinSelectionID  string   `json:"builtin_selection_id,omitempty"`
	ExternalSelectionID string   `json:"external_selection_id,omitempty"`
	Replayed            bool     `json:"replayed"`
	CapabilityGrant     bool     `json:"capability_grant"`
}

type ProjectSkillSelectionStore interface {
	SkillSelectionStore
	ExternalSkillSelectionStore
}

func SelectProjectSkills(ctx context.Context, store ProjectSkillSelectionStore, request ProjectSkillSelectionRequest) (ProjectSkillSelectionResult, error) {
	result := ProjectSkillSelectionResult{RunID: request.RunID, Refs: slices.Clone(request.Refs), Replayed: true}
	if store == nil || len(request.Refs) == 0 || len(request.Refs) > projectconfig.MaxSkillSuggestions {
		return result, apperror.New(apperror.CodeInvalidArgument, "a bounded project Skill selection is required")
	}
	run, err := store.GetRun(ctx, request.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	project, err := projectconfig.Pinned(run.Config)
	if err != nil {
		return result, err
	}
	if project == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition, "this Run has no pinned project Skill suggestions")
	}
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		return result, err
	}
	builtins, external := []string{}, []string{}
	seen := map[string]bool{}
	for _, ref := range request.Refs {
		if seen[ref] || !slices.Contains(project.SkillSuggestions, ref) {
			return result, apperror.New(apperror.CodeInvalidArgument, "selected Skill must be an exact, unique pinned project suggestion")
		}
		seen[ref] = true
		name, version, err := skills.ParseInstalledPackageRef(ref)
		if err != nil {
			return result, apperror.New(apperror.CodeInvalidArgument, "project Skill reference is invalid")
		}
		if manifest, builtin := registry.Get(name); builtin {
			if manifest.Version != version {
				return result, apperror.New(apperror.CodeConflict, "suggested built-in Skill version is unavailable: "+ref)
			}
			builtins = append(builtins, name)
		} else {
			external = append(external, ref)
		}
	}
	if len(builtins) > skills.MaxSelectionItems || len(external) > skills.MaxExternalSelectionItems {
		return result, apperror.New(apperror.CodeInvalidArgument, "project Skill selection exceeds the existing selection limit")
	}
	// Validate shared identities and confirmation before mutation. The services retain
	// their version/enablement, mode, confirmation and replay checks.
	if len(external) != 0 && !request.ConfirmUntrustedContext {
		return result, apperror.New(apperror.CodeFailedPrecondition, "external project Skill selection requires explicit untrusted-context confirmation")
	}
	validationNames := builtins
	if len(validationNames) == 0 {
		validationNames = []string{"code"}
	}
	if _, err := normalizeSelectSkillsRequest(SelectSkillsRequest{RunID: request.RunID, Names: validationNames, OperationKey: request.OperationKey + "-builtin", RequestedBy: request.RequestedBy}); err != nil {
		return result, apperror.New(apperror.CodeInvalidArgument, "project Skill selection identity or operation key is invalid")
	}
	// External first: an unavailable installation leaves the built-in selection
	// untouched. A later failure preserves the Created Run for same-key recovery.
	if len(external) != 0 {
		selected, err := NewExternalSkillSelectionService(store).Select(ctx, SelectExternalSkillsRequest{
			RunID: request.RunID, PackageRefs: external, TokenBudget: skills.MaxExternalSelectionTokenBudget,
			OperationKey: request.OperationKey + "-external", RequestedBy: request.RequestedBy, ConfirmUntrustedContext: request.ConfirmUntrustedContext})
		if err != nil {
			return result, err
		}
		result.ExternalSelectionID = selected.Selection.ID
		result.Replayed = result.Replayed && selected.Replayed
	}
	if len(builtins) != 0 {
		selected, err := NewSkillSelectionService(store, registry).Select(ctx, SelectSkillsRequest{
			RunID: request.RunID, Names: builtins, TokenBudget: skills.MaxSelectionTokenBudget,
			OperationKey: request.OperationKey + "-builtin", RequestedBy: request.RequestedBy})
		if err != nil {
			return result, err
		}
		result.BuiltinSelectionID = selected.Selection.ID
		result.Replayed = result.Replayed && selected.Replayed
	}
	return result, nil
}
