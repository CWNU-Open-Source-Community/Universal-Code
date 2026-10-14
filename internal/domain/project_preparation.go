package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ProjectPreparationOperation is private durable control-plane state. Public
// projections omit request keys, authority pins and the original request body.
type ProjectPreparationOperation struct {
	ID, RunID, KeyDigest, RequestFingerprint      string
	Target, PlanFingerprint, Backend, RequestedBy string
	RequestJSON, OwnerID                          string
	PermissionRevision                            int64
	State                                         string
	CompletedPackages, TotalPackages, CacheHits   int
	DownloadedBytes                               int64
	JobID, ScriptJobID, Error                     string
	CancelRequested                               bool
	Revision                                      int64
	CreatedAt, UpdatedAt                          time.Time
}

func (o ProjectPreparationOperation) Terminal() bool {
	return o.State == "ready" || o.State == "failed" || o.State == "cancelled" || o.State == "interrupted"
}

func (o ProjectPreparationOperation) Validate() error {
	for _, value := range []string{o.ID, o.RunID, o.RequestedBy, o.OwnerID} {
		if !ValidAgentID(value) {
			return errors.New("project preparation identity is invalid")
		}
	}
	for _, value := range []string{o.KeyDigest, o.RequestFingerprint, o.PlanFingerprint} {
		if len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
			return errors.New("project preparation fingerprint is invalid")
		}
	}
	if o.Target == "" || len(o.Target) > 4096 || o.Backend == "" || len(o.Backend) > 100 || o.PermissionRevision < 1 ||
		o.Revision < 1 || o.CreatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) ||
		o.TotalPackages < 0 || o.TotalPackages > 6000 || o.CompletedPackages < 0 || o.CompletedPackages > o.TotalPackages ||
		o.CacheHits < 0 || o.CacheHits > o.CompletedPackages || o.DownloadedBytes < 0 || o.DownloadedBytes > 512*1024*1024 ||
		len(o.RequestJSON) > 64*1024 || !json.Valid([]byte(o.RequestJSON)) || len(o.Error) > 4096 {
		return errors.New("project preparation record is invalid")
	}
	if o.JobID != "" && !ValidAgentID(o.JobID) || o.ScriptJobID != "" && !ValidAgentID(o.ScriptJobID) {
		return errors.New("project preparation Job identity is invalid")
	}
	switch o.State {
	case "pending", "downloading", "installing", "ready", "failed", "cancelled", "interrupted":
		return nil
	default:
		return errors.New("project preparation state is invalid")
	}
}
