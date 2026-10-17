package domain

import "time"

// ProjectHookTrust stores a user decision about one workspace's exact hook
// definition, independently of execution permission and per-task budgets.
type ProjectHookTrust struct {
	WorkspaceID     string
	RootFingerprint string
	Fingerprint     string
	Trusted         bool
	Revision        int64
	UpdatedAt       time.Time
}
