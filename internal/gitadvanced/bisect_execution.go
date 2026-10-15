package gitadvanced

import (
	"errors"
	"strings"

	"cyberagent-workbench/internal/domain"
)

// BisectExecutionReceipt associates a bounded runtime receipt with the exact
// commit tested. It contains no command output, environment, or host path.
type BisectExecutionReceipt struct {
	Commit    string                                 `json:"commit"`
	Ordinal   int                                    `json:"ordinal"`
	Execution domain.BatchValidationExecutionReceipt `json:"execution"`
}

func (r BisectExecutionReceipt) Validate() error {
	e := r.Execution
	if !ValidObjectID(r.Commit) || r.Ordinal < 0 || r.Ordinal >= MaxCommits ||
		!validIdentity(e.JobID) || !ValidDigest(e.WorkspaceSHA256) ||
		(e.Readiness != "ready" && e.Readiness != "explicit_host_execution") || e.ReadinessCheckedAt.IsZero() {
		return errors.New("bisect recipe backend receipt is invalid")
	}
	for _, value := range []string{e.Backend, e.BackendIdentity, e.BackendGeneration,
		e.IsolationGrade, e.NetworkPolicy, e.CredentialPolicy} {
		if value == "" || strings.TrimSpace(value) != value || !boundedText(value, 256) {
			return errors.New("bisect recipe backend identity is invalid")
		}
	}
	if !boundedText(e.ImageDigest, 256) || !boundedText(e.TemplateReference, 1024) {
		return errors.New("bisect recipe backend reference is invalid")
	}
	return nil
}
