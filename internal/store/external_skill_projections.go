package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
)

func (s *SQLiteStore) GetExternalSkillProjectionByRun(ctx context.Context,
	runID string,
) (skills.ExternalSkillProjection, bool, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return skills.ExternalSkillProjection{}, false, apperror.New(
			apperror.CodeInvalidArgument, "external Skill projection Run id is invalid")
	}
	var value skills.ExternalSkillProjection
	var operatorConfirmed, contextDelivery, toolGrant int
	var createdAt string
	err := s.db.QueryRowContext(ctx, `SELECT protocol_version, run_id, mode_revision,
		surface, profile, token_budget, token_upper_bound, item_count,
		operator_confirmed, context_delivery_authorized, tool_capability_grant,
		root_prepared_count, root_committed_count, specialist_prepared_count,
		specialist_committed_count, created_at
		FROM run_external_skill_projections WHERE run_id = ?`, runID).Scan(
		&value.ProtocolVersion, &value.RunID, &value.ModeRevision, &value.Surface,
		&value.Profile, &value.TokenBudget, &value.TokenUpperBound, &value.ItemCount,
		&operatorConfirmed, &contextDelivery, &toolGrant, &value.RootPreparedCount,
		&value.RootCommittedCount, &value.SpecialistPreparedCount,
		&value.SpecialistCommittedCount, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return skills.ExternalSkillProjection{}, false, nil
	}
	if err != nil {
		return skills.ExternalSkillProjection{}, false, err
	}
	value.OperatorConfirmed = operatorConfirmed != 0
	value.ContextDeliveryAuthorized = contextDelivery != 0
	value.ToolCapabilityGrant = toolGrant != 0
	value.CreatedAt = parseTS(createdAt)
	rows, err := s.db.QueryContext(ctx, `SELECT projection.ordinal, projection.name, projection.version,
		projection.token_upper_bound, projection.trust_class, projection.declared_tool_count, projection.specialist_eligible,
		item.installation_id, item.plugin_binding_json
		FROM run_external_skill_projection_items projection
		JOIN run_external_skill_selections selection ON selection.run_id = projection.run_id
		JOIN run_external_skill_selection_items item ON item.selection_id = selection.id AND item.ordinal = projection.ordinal
		WHERE projection.run_id = ? ORDER BY projection.ordinal`, runID)
	if err != nil {
		return skills.ExternalSkillProjection{}, false, err
	}
	defer rows.Close()
	value.Items = make([]skills.ExternalSkillProjectionItem, 0, value.ItemCount)
	for rows.Next() {
		var item skills.ExternalSkillProjectionItem
		var specialistEligible int
		var installationID, binding string
		if err := rows.Scan(&item.Ordinal, &item.Name, &item.Version,
			&item.TokenUpperBound, &item.TrustClass, &item.DeclaredToolCount,
			&specialistEligible, &installationID, &binding); err != nil {
			return skills.ExternalSkillProjection{}, false, err
		}
		item.SpecialistEligible = specialistEligible != 0
		if binding != "{}" {
			var plugin skills.PluginSkillBinding
			if err := json.Unmarshal([]byte(binding), &plugin); err != nil || plugin.Validate() != nil {
				return skills.ExternalSkillProjection{}, false, errors.New("external Skill projection component binding is invalid")
			}
			if plugin.Native {
				item.Native = true
				item.SelectionRef = skills.ExternalSelectionItemRef(skills.ExternalSelectionItem{InstallationID: installationID, Plugin: &plugin})
			}
		}
		value.Items = append(value.Items, item)
	}
	if err := rows.Err(); err != nil {
		return skills.ExternalSkillProjection{}, false, err
	}
	if err := value.Validate(); err != nil {
		return skills.ExternalSkillProjection{}, false, err
	}
	return skills.CloneExternalSkillProjection(value), true, nil
}
