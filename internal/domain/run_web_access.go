package domain

import (
	"cyberagent-workbench/internal/runmutation"
	"fmt"
	"strings"
)

// The existing revision-bound operation identity also fits web selection.
type RunWebAccessOperation = RunNetworkAuthorityOperation

func RunWebAccessFingerprint(runID string, revision int64, mode WebAccessMode, targets []string, requestedBy, reason string) string {
	return runmutation.Fingerprint("run_web_access_request.v1", runID, fmt.Sprint(revision), string(mode), strings.Join(targets, "\n"), requestedBy, reason)
}
