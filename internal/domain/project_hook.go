package domain

import "errors"

// ProjectHookCallOrigin records a Go-owned project action, independently of the
// model's native tool requests. Execution and approval live on its ordinary call.
type ProjectHookCallOrigin struct {
	TriggerKey        string `json:"trigger_key"`
	Slot              string `json:"slot"`
	Target            string `json:"target"`
	ConfigFingerprint string `json:"config_fingerprint"`
	WorkspaceVersion  string `json:"workspace_version"`
}

func (o ProjectHookCallOrigin) Validate() error {
	if len(o.TriggerKey) != 64 || len(o.ConfigFingerprint) != 64 || len(o.WorkspaceVersion) != 64 ||
		(o.Slot != "format" && o.Slot != "test") || o.Target == "" {
		return errors.New("project hook requires an exact configuration and trigger")
	}
	return nil
}

func (r SupervisorToolRound) ProjectHook() bool {
	return len(r.Calls) > 0 && r.Calls[0].ProjectHook != nil
}
