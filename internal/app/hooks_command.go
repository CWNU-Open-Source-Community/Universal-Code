package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/plugins"
)

func (a *App) hooksCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("hooks subcommand is required")
	}
	if a.scriptHooksDir() == "" {
		return apperror.New(apperror.CodeInvalidArgument, "CYBERAGENT_HOME must be an absolute path")
	}
	switch args[0] {
	case "list":
		return a.hooksList(ctx, args[1:])
	case "validate":
		return a.hooksValidate(ctx, args[1:])
	default:
		return fmt.Errorf("unknown hooks subcommand %q", args[0])
	}
}

// hooksValidate runs the operator script hook loader against hooks.d and
// reports the result: every declaration is checked (JSON shape, script
// presence, hash pin) exactly as a run would load them, so operators get
// feedback while editing instead of at the first hook execution.
func (a *App) hooksValidate(ctx context.Context, args []string) error {
	fs := newFlagSet("hooks validate", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: cyberagent hooks validate")
	}
	registrations, err := hooks.LoadOperatorScriptHooks(ctx, a.scriptHooksDir())
	if err != nil {
		return fmt.Errorf("hooks configuration is invalid: %w", err)
	}
	out := bufio.NewWriter(a.out)
	fmt.Fprintln(out, "hooks configuration valid")
	fmt.Fprintf(out, "directory: %s\n", a.scriptHooksDir())
	fmt.Fprintf(out, "declarations: %d\n", len(registrations))
	for _, registration := range registrations {
		declaration := registration.Declaration
		fmt.Fprintf(out, "hook=%s\tevent=%s\tscript=%s\n",
			declaration.ID, declaration.Event, hookScript(declaration))
	}
	return out.Flush()
}

// hooksList reports the lifecycle hook registrations exactly as the engine
// merges them: enabled plugin declarations followed by operator-owned
// script hooks. Read-only inspection; it never executes or modifies hooks.
func (a *App) hooksList(ctx context.Context, args []string) error {
	fs := newFlagSet("hooks list", a.errOut)
	asJSON := fs.Bool("json", false, "print the registrations as JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"json": false})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: cyberagent hooks list [--json]")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	service, err := plugins.NewService(a.store)
	if err != nil {
		return err
	}
	registrations, err := a.hookRegistrations(service)(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(registrations, func(a, b hooks.Registration) int {
		if compared := strings.Compare(a.PluginID, b.PluginID); compared != 0 {
			return compared
		}
		return strings.Compare(a.Declaration.ID, b.Declaration.ID)
	})
	if *asJSON {
		encoder := json.NewEncoder(a.out)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(registrations)
	}
	out := bufio.NewWriter(a.out)
	fmt.Fprintf(out, "registered: %d\n", len(registrations))
	for _, registration := range registrations {
		declaration := registration.Declaration
		fmt.Fprintf(out, "plugin=%s\thook=%s\tevent=%s\taction=%s\tasync=%t\ttimeout_ms=%d\tmatcher=%s\tscript=%s\n",
			registration.PluginID, declaration.ID, declaration.Event, declaration.Action,
			declaration.Async, declaration.TimeoutMillis, hookMatcher(declaration), hookScript(declaration))
	}
	return out.Flush()
}

func hookMatcher(declaration hooks.Declaration) string {
	if len(declaration.ToolNames) > 0 {
		return strings.Join(declaration.ToolNames, ",")
	}
	if declaration.ToolPattern != "" {
		return declaration.ToolPattern
	}
	return "-"
}

func hookScript(declaration hooks.Declaration) string {
	if declaration.Script == nil {
		return "-"
	}
	return declaration.Script.Interpreter + ":" + declaration.Script.Path
}
