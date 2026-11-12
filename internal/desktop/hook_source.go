package desktop

import (
	"context"
	"path/filepath"

	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/plugins"
)

// hookRegistrations merges enabled plugin hook declarations with the
// operator-owned script hook declarations. Operator scripts live under the
// desktop home hooks directory and never come from workspace or repository
// content.
func hookRegistrations(service *plugins.Service,
	scriptHooksDir string,
) func(context.Context) ([]hooks.Registration, error) {
	return func(ctx context.Context) ([]hooks.Registration, error) {
		registrations, err := service.ActiveHooks(ctx)
		if err != nil {
			return nil, err
		}
		operator, err := hooks.LoadOperatorScriptHooks(ctx, scriptHooksDir)
		if err != nil {
			return nil, err
		}
		return hooks.MergeRegistrations(registrations, operator)
	}
}

func operatorScriptHooksDir(home string) string {
	if home == "" || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Clean(filepath.Join(home, "hooks.d"))
}

func newScriptHookExecutor(scriptHooksDir string) hooks.ScriptExecutor {
	if scriptHooksDir == "" {
		return nil
	}
	executor, err := hooks.NewProcessScriptExecutor(scriptHooksDir)
	if err != nil {
		return nil
	}
	return executor
}
