package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolgateway"
)

const portableSkillCatalogPageSize = 32
const portableSkillCatalogInstallLimit = 1000

type portableSkillCatalogPage struct {
	Skills          []toolgateway.BuiltinSkillDescriptor `json:"skills"`
	Total           int                                  `json:"total"`
	Offset          int                                  `json:"offset"`
	Revision        string                               `json:"catalog_revision"`
	Next            *toolgateway.SkillReadRequest        `json:"next_request,omitempty"`
	Diagnostic      string                               `json:"diagnostic,omitempty"`
	CapabilityGrant bool                                 `json:"capability_grant"`
}

func portableSkillCatalog(ctx context.Context, source any, mode domain.RunModeSnapshot,
	request toolgateway.SkillReadRequest,
) (portableSkillCatalogPage, error) {
	page := portableSkillCatalogPage{Skills: []toolgateway.BuiltinSkillDescriptor{}, Offset: request.Offset}
	store, ok := source.(portableSkillReadStore)
	if !ok {
		return page, nil
	}
	values, err := store.ListPluginInstallations(ctx, "", portableSkillCatalogInstallLimit)
	if err != nil {
		return page, err
	}
	// Stable ordering plus a revision prevents continuation across changed
	// enablement/generation from silently skipping or substituting a component.
	slices.SortFunc(values, func(a, b plugins.Installation) int { return strings.Compare(a.ID, b.ID) })
	bindings := []string{mode.RunID, mode.ID, string(mode.Surface), string(mode.Phase), string(mode.Profile)}
	for _, value := range values {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if !portableSkillAvailable(value, mode) {
			continue
		}
		automatic := portableSkillEnabled(value, mode)
		skills := slices.Clone(value.Snapshot.Skills)
		slices.SortFunc(skills, func(a, b plugins.SnapshotSkill) int {
			return strings.Compare(a.Instructions.Component.ComponentID, b.Instructions.Component.ComponentID)
		})
		for _, skill := range skills {
			if !automatic {
				if _, _, err := selectedPortableSkill(ctx, source, value, portableSkillPin(value, skill), mode, domain.AgentRoleRoot); err != nil {
					if apperror.CodeOf(err) != apperror.CodePolicyDenied {
						return page, err
					}
					continue
				}
			}
			if err := requireWorkerSkillCompatibility(ctx, source, value, portableSkillPin(value, skill), mode); err != nil {
				if apperror.CodeOf(err) != apperror.CodePolicyDenied {
					return page, err
				}
				continue
			}
			bindings = append(bindings, plugins.InstallationFingerprint(value), skill.Instructions.Component.ComponentID)
			position := page.Total
			page.Total++
			if position < request.Offset || len(page.Skills) >= portableSkillCatalogPageSize {
				continue
			}
			size := 0
			for _, entry := range value.Snapshot.Inventory {
				if entry.Path == skill.Instructions.Path {
					size = entry.Bytes
					break
				}
			}
			description := []rune(redact.String(skill.Name + ": " + skill.Description))
			if len(description) > 256 {
				description = append(description[:256], []rune("…")...)
			}
			page.Skills = append(page.Skills, toolgateway.BuiltinSkillDescriptor{
				SkillReadRequest: portableSkillPin(value, skill), Description: string(description), ContentBytes: size})
		}
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return page, err
	}
	page.Revision = portableIdentity(string(raw))
	if request.CatalogRevision != "" && request.CatalogRevision != page.Revision {
		return portableSkillCatalogPage{}, apperror.New(apperror.CodeConflict,
			"installed Skill catalog changed; restart discovery with {\"catalog\":true}")
	}
	if next := request.Offset + len(page.Skills); next < page.Total {
		page.Next = &toolgateway.SkillReadRequest{Catalog: true, Offset: next, CatalogRevision: page.Revision}
	}
	if page.Total > len(page.Skills) {
		page.Diagnostic = fmt.Sprintf("Installed Skill summaries: %d of %d at offset %d. Metadata reads do not activate instructions.", len(page.Skills), page.Total, request.Offset)
		if page.Next != nil {
			page.Diagnostic += " More summaries are available through skill_read; copy next_request."
		}
	}
	if len(values) == portableSkillCatalogInstallLimit {
		page.Diagnostic += " Installation scan reached its 1000-record bound; this is a partial inventory. Operators can inspect package-specific records with plugin list --plugin <package-id> and plugin show; exact enabled references remain readable."
	}
	return page, nil
}

func (e *builtinSkillReader) readPortableSkillCatalog(ctx context.Context, call toolgateway.ToolCall,
	request toolgateway.SkillReadRequest, mode domain.RunModeSnapshot,
) (json.RawMessage, error) {
	page, err := portableSkillCatalog(ctx, e.store, mode, request)
	if err != nil {
		return nil, err
	}
	current, err := e.validateScope(ctx, call)
	if err != nil {
		return nil, err
	}
	if current.ID != mode.ID || current.Revision != mode.Revision {
		return nil, apperror.New(apperror.CodeConflict, "Skill catalog mode changed during discovery")
	}
	return json.Marshal(page)
}
