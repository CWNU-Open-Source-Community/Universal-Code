package workspace

import (
	"cyberagent-workbench/internal/workspaceidentity"
	"sort"
	"strings"
)

type AgentCodePathPolicy = workspaceidentity.PathPolicy

func (w agentWorkspace) cursorFingerprint(parts ...string) string {
	if len(w.policy.ExcludePaths) != 0 {
		patterns := append([]string(nil), w.policy.ExcludePaths...)
		sort.Strings(patterns)
		parts = append(parts, "project-exclusions", strings.Join(patterns, "\n"))
	}
	return agentCursorFingerprint(parts...)
}
