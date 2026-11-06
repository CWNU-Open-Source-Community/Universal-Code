package application

import (
	"context"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type BrowserNetworkAccessStore interface {
	GetBrowserNetworkAccess(context.Context, string, string) (domain.BrowserNetworkAccessSelection, error)
	ChangeBrowserNetworkAccess(context.Context, domain.ChangeBrowserNetworkAccessRequest) (domain.BrowserNetworkAccessSelection, error)
}

type browserNetworkPreferenceReader interface {
	GetBrowserNetworkPreference(context.Context, string) (domain.BrowserNetworkPreference, error)
}

// Repository files and continuity payloads never enter this selection path.
func inheritProjectBrowserNetworkAccess(ctx context.Context, st any, req CreateRunRequest) (CreateRunRequest, error) {
	if req.BrowserNetworkAccess != "" || len(req.BrowserOrigins) > 0 || req.WorkspaceID == "" {
		return req, nil
	}
	if reader, ok := st.(browserNetworkPreferenceReader); ok {
		p, err := reader.GetBrowserNetworkPreference(ctx, req.WorkspaceID)
		if err != nil {
			return req, err
		}
		req.BrowserNetworkAccess, req.BrowserOrigins = p.BrowserNetworkAccess, p.BrowserOrigins
	}
	return req, nil
}

func normalizeBrowserNetworkAccess(mode domain.BrowserNetworkAccess, origins []string) (domain.BrowserNetworkAccess, []string, error) {
	if mode == "" {
		mode = domain.BrowserNetworkOff
		if len(origins) > 0 {
			mode = domain.BrowserNetworkRestricted
		}
	}
	if !mode.Valid() {
		return "", nil, apperror.New(apperror.CodeInvalidArgument, "Unsupported browser network access mode")
	}
	if mode == domain.BrowserNetworkInternet && len(origins) > 0 {
		return "", nil, apperror.New(apperror.CodeInvalidArgument, "Internet browser access requires an empty origin list")
	}
	if mode == domain.BrowserNetworkRestricted && len(origins) == 0 {
		return "", nil, apperror.New(apperror.CodeInvalidArgument, "Restricted browser access requires at least one origin")
	}
	canonical, err := normalizeBrowserOrigins(origins)
	if err != nil {
		return "", nil, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	return mode, canonical, nil
}

func ChangeBrowserNetworkAccess(ctx context.Context, st BrowserNetworkAccessStore, browsers interface {
	InvalidateNetworkAccess(context.Context, string) error
}, r domain.ChangeBrowserNetworkAccessRequest) (domain.BrowserNetworkAccessSelection, error) {
	var empty domain.BrowserNetworkAccessSelection
	if r.Version != domain.BrowserNetworkAccessVersion || !r.BrowserNetworkAccess.Valid() || r.ExpectedRevision < 0 || (r.RunID != "" && r.ExpectedRevision < 1) {
		return empty, apperror.New(apperror.CodeInvalidArgument, "Browser network version, mode and expected revision are required")
	}
	if (r.RunID == "") == (r.WorkspaceID == "") {
		return empty, apperror.New(apperror.CodeInvalidArgument, "Select exactly one Run or project")
	}
	for _, id := range []string{r.RunID, r.WorkspaceID, r.RequestedBy} {
		if id != "" && (!domain.ValidAgentID(id) || id != strings.TrimSpace(id)) {
			return empty, apperror.New(apperror.CodeInvalidArgument, "Invalid browser network identity")
		}
	}
	key, err := domain.NormalizeAgentOperationKey(r.OperationKey)
	if err != nil || containsSpaceOrControl(key) {
		return empty, apperror.New(apperror.CodeInvalidArgument, "Invalid browser network operation key")
	}
	r.OperationKey = key
	r.BrowserNetworkAccess, r.BrowserOrigins, err = normalizeBrowserNetworkAccess(r.BrowserNetworkAccess, r.BrowserOrigins)
	if err != nil {
		return empty, err
	}
	v, err := st.ChangeBrowserNetworkAccess(ctx, r)
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	if r.RunID != "" && browsers != nil {
		if err := browsers.InvalidateNetworkAccess(ctx, r.RunID); err != nil {
			return empty, apperror.Wrap(apperror.CodeInternal, "Browser network selection saved but previous browser cleanup failed", err)
		}
	}
	return v, nil
}
