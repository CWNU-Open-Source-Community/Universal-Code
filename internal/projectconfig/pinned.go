package projectconfig

import (
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/workspaceidentity"
)

// Pinned reads the task snapshot, never the current configuration file. A
// successor Run and its agents therefore use the same project settings.
func Pinned(config domain.RunConfig) (*Effective, error) {
	if len(config.ProjectConfig) == 0 && config.ProjectConfigFingerprint == "" {
		return nil, nil
	}
	var effective Effective
	if err := json.Unmarshal(config.ProjectConfig, &effective); err != nil ||
		effective.Protocol != ProtocolVersion || effective.Fingerprint() != config.ProjectConfigFingerprint {
		return nil, apperror.New(apperror.CodeConflict, "pinned project configuration binding is invalid")
	}
	if err := (workspaceidentity.PathPolicy{ExcludePaths: effective.ExcludePaths}).Validate(); err != nil {
		return nil, err
	}
	return &effective, nil
}

func PathPolicy(config domain.RunConfig) (workspaceidentity.PathPolicy, error) {
	effective, err := Pinned(config)
	if err != nil || effective == nil {
		return workspaceidentity.PathPolicy{}, err
	}
	return workspaceidentity.PathPolicy{ExcludePaths: effective.ExcludePaths}, nil
}
