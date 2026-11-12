package domain

import "fmt"

const BrowserNetworkAccessVersion = "browser_network_access.v1"

type BrowserNetworkAccess string

const (
	BrowserNetworkOff        BrowserNetworkAccess = "off"
	BrowserNetworkInternet   BrowserNetworkAccess = "internet"
	BrowserNetworkRestricted BrowserNetworkAccess = "restricted"
)

func (m BrowserNetworkAccess) Valid() bool {
	return m == BrowserNetworkOff || m == BrowserNetworkInternet || m == BrowserNetworkRestricted
}

// EffectiveBrowserNetworkAccess preserves the pre-selection behavior of stored
// Runs. New Runs always record an explicit choice, including the default off.
func (c RunConfig) EffectiveBrowserNetworkAccess(permission RunExecutionPermissionMode) BrowserNetworkAccess {
	if c.BrowserNetworkAccess != "" {
		return c.BrowserNetworkAccess
	}
	if permission.IncludesFullAccess() {
		return BrowserNetworkInternet
	}
	if len(c.BrowserOrigins) > 0 {
		return BrowserNetworkRestricted
	}
	return BrowserNetworkOff
}

func (c RunConfig) EffectiveBrowserNetworkRevision() int64 {
	if c.BrowserNetworkRevision > 0 {
		return c.BrowserNetworkRevision
	}
	return 1
}

func (c RunConfig) ValidateBrowserNetworkAccess() error {
	if c.BrowserNetworkAccess == "" {
		return nil
	}
	if !c.BrowserNetworkAccess.Valid() || c.BrowserNetworkRevision < 1 {
		return fmt.Errorf("invalid browser network access selection")
	}
	if (c.BrowserNetworkAccess == BrowserNetworkRestricted && len(c.BrowserOrigins) == 0) || (c.BrowserNetworkAccess == BrowserNetworkInternet && len(c.BrowserOrigins) > 0) {
		return fmt.Errorf("restricted browser access requires origins; internet access requires an empty list")
	}
	return nil
}

type BrowserNetworkPreference struct {
	WorkspaceID          string               `json:"workspace_id"`
	Revision             int64                `json:"revision"`
	BrowserNetworkAccess BrowserNetworkAccess `json:"browser_network_access"`
	BrowserOrigins       []string             `json:"browser_origins,omitempty"`
}

type BrowserNetworkAccessSelection struct {
	Version              string                    `json:"version"`
	RunID                string                    `json:"run_id,omitempty"`
	WorkspaceID          string                    `json:"workspace_id"`
	Revision             int64                     `json:"revision"`
	BrowserNetworkAccess BrowserNetworkAccess      `json:"browser_network_access"`
	BrowserOrigins       []string                  `json:"browser_origins,omitempty"`
	ProjectPreference    *BrowserNetworkPreference `json:"project_preference,omitempty"`
	Replayed             bool                      `json:"replayed"`
	ForNextRun           bool                      `json:"for_next_run"`
}

type ChangeBrowserNetworkAccessRequest struct {
	Version              string
	RunID                string
	WorkspaceID          string
	ExpectedRevision     int64
	BrowserNetworkAccess BrowserNetworkAccess
	BrowserOrigins       []string
	RememberForProject   bool
	OperationKey         string
	RequestedBy          string
}
