package workspace

import (
	"cyberagent-workbench/internal/apperror"
	"strings"
)

// AgentCodeReadScopes resolves literal admitted paths using the same redirect,
// hidden-file and project-policy checks as individual reads.
func AgentCodeReadScopes(root string, paths []string, policy AgentCodePathPolicy) ([]AgentCodeScopePath, error) {
	w, err := openAgentWorkspace(root, policy)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 || len(paths) > 32 {
		return nil, apperror.New(apperror.CodePolicyDenied, "child read scope is empty")
	}
	scopes := make([]AgentCodeScopePath, 0, len(paths))
	for _, p := range paths {
		normalized, err := normalizeAgentPath(p, true)
		if err != nil {
			return nil, err
		}
		_, info, err := w.resolve(normalized, false)
		if err != nil {
			return nil, err
		}
		if normalized != "." && w.blocked(normalized, info.IsDir(), false) {
			return nil, apperror.New(apperror.CodePolicyDenied, "child read scope is excluded")
		}
		scopes = append(scopes, AgentCodeScopePath{Path: normalized, Directory: info.IsDir()})
	}
	return scopes, nil
}

func AgentCodeCheckReadScope(requested string, scopes []AgentCodeScopePath) error {
	p, err := normalizeAgentPath(requested, true)
	if err != nil {
		return err
	}
	for _, s := range scopes {
		if p == s.Path || s.Directory && (s.Path == "." || strings.HasPrefix(p, s.Path+"/")) {
			return nil
		}
	}
	return apperror.New(apperror.CodePolicyDenied, "path is outside this child's admitted read scope")
}
