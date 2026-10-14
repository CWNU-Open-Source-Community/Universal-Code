package application

import (
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/webevidence"
)

func normalizeWebAccess(raw string, targets []string, legacyAllowed bool) (domain.WebAccessMode, []string, error) {
	mode := domain.WebAccessMode(raw)
	if mode == "" && legacyAllowed && len(targets) == 0 {
		return "", nil, nil
	}
	if !mode.Valid() {
		return "", nil, apperror.New(apperror.CodeInvalidArgument, "web access mode must be disabled, rules or full")
	}
	if mode != domain.WebAccessRules && len(targets) > 0 {
		return "", nil, apperror.New(apperror.CodeInvalidArgument, "web targets apply only to rules mode")
	}
	if len(targets) == 0 {
		return mode, nil, nil
	}
	normalized, err := webevidence.NormalizeExactAuthorityTargets(targets)
	if err != nil {
		return "", nil, apperror.Wrap(apperror.CodeInvalidArgument, "web rules require exact public HTTPS hosts", err)
	}
	return mode, normalized, nil
}

func webNetworkAuthority(scope domain.Scope) webevidence.NetworkAuthority {
	if scope.WebAccessMode == "" {
		return webevidence.NetworkAuthority{Mode: scope.NetworkMode, AllowedTargets: append([]string(nil), scope.AllowedTargets...)}
	}
	switch scope.WebAccessMode {
	case domain.WebAccessFull:
		return webevidence.NetworkAuthority{Mode: "allowlist", AllowedTargets: []string{webevidence.PublicHTTPSTarget}}
	case domain.WebAccessRules:
		if len(scope.WebAllowedTargets) > 0 {
			return webevidence.NetworkAuthority{Mode: "allowlist", AllowedTargets: append([]string(nil), scope.WebAllowedTargets...)}
		}
	}
	return webevidence.NetworkAuthority{Mode: "disabled"}
}

func webSearchAuthority(scope domain.Scope) webevidence.NetworkAuthority {
	if scope.WebAccessMode == domain.WebAccessDisabled {
		return webevidence.NetworkAuthority{Mode: "disabled"}
	}
	return webevidence.NetworkAuthority{Mode: "allowlist", AllowedTargets: []string{webevidence.PublicHTTPSTarget}}
}
