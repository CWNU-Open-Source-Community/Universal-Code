package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

func (a *App) threadCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("thread subcommand is required")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	service := application.NewThreadService(a.store)
	switch args[0] {
	case "create":
		return a.threadCreate(ctx, args[1:])
	case "list":
		return a.threadList(ctx, service, args[1:])
	case "show":
		return a.threadShow(ctx, service, args[1:])
	case "send":
		return a.threadSend(ctx, service, args[1:])
	case "compact":
		return a.threadCompact(ctx, args[1:])
	case "history":
		return a.threadHistory(ctx, service, args[1:])
	case "archive", "restore", "delete":
		return a.threadLifecycle(ctx, service, args[0], args[1:])
	case "export":
		return a.threadExport(ctx, service, args[1:])
	default:
		return fmt.Errorf("unknown thread subcommand %q", args[0])
	}
}

func (a *App) threadCreate(ctx context.Context, args []string) error {
	fs := newFlagSet("thread create", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	profile := fs.String("profile", string(domain.ProfileCode), "mission profile")
	surface := fs.String("surface", string(domain.ExecutionSurfaceCode), "execution surface")
	phase := fs.String("phase", string(domain.ExecutionPhaseDeliver), "execution phase")
	route := fs.String("route", "", "model route")
	maxTurns := fs.Int("max-turns", domain.DefaultBudget().MaxTurns, "maximum agent turns")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "profile": true, "surface": true, "phase": true,
		"route": true, "max-turns": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New(`usage: cyberagent thread create "goal" [--workspace <name>] [--profile code|review|learn|script] [--surface code|cyber] [--phase plan|deliver] [--json]`)
	}
	workspaceID := ""
	var effective *projectconfig.Effective
	var instructions *projectconfig.InstructionSnapshot
	if strings.TrimSpace(*workspaceName) != "" {
		record, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
		if err != nil {
			return err
		}
		workspaceID = record.ID
		selectedProfile, err := domain.ParseProfile(*profile)
		if err != nil {
			return err
		}
		value, rejections, err := projectconfig.ResolveWorkspace(ctx, record.RootPath, selectedProfile,
			domain.Budget{MaxTurns: *maxTurns, MaxToolCalls: domain.DefaultBudget().MaxToolCalls}, toolgateway.TypedActionIDs())
		if err != nil {
			return fmt.Errorf("project config fail-closed: %w", err)
		}
		if len(rejections) != 0 {
			return fmt.Errorf("project config rejection: field=%s reason=%s", rejections[0].Field, rejections[0].Reason)
		}
		effective = value
		discovered, err := projectconfig.DiscoverInstructions(ctx, record.RootPath, ".")
		if err != nil {
			return fmt.Errorf("project instruction discovery fail-closed: %w", err)
		}
		instructions = &discovered
	}
	runService := application.NewRunService(a.store).
		WithLifecycleHooks(a.newLifecycleHookEngine())
	mission, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: strings.Join(fs.Args(), " "), Profile: *profile, Surface: *surface,
		Phase: *phase, WorkspaceID: workspaceID, ModelRoute: *route,
		Interactive: true, Budget: domain.Budget{MaxTurns: *maxTurns,
			MaxToolCalls: domain.DefaultBudget().MaxToolCalls},
		ProjectConfig: effective, ProjectInstructions: instructions,
		RequestedBy: "cli_thread_operator",
	})
	if err != nil {
		return err
	}
	threadRecord, err := a.store.GetThreadByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, struct {
			Thread  domain.Thread  `json:"thread"`
			Mission domain.Mission `json:"mission"`
			Run     domain.Run     `json:"run"`
		}{Thread: threadRecord, Mission: mission, Run: run})
	}
	fmt.Fprintf(a.out, "thread %s created\nmission: %s\nrun: %s\nsession: %s\nstatus: %s\nworkspace: %s\n",
		threadRecord.ID, mission.ID, run.ID, run.SessionID, threadRecord.Status,
		threadRecord.WorkspaceID)
	return nil
}

func (a *App) threadList(ctx context.Context, service *application.ThreadService,
	args []string,
) error {
	fs := newFlagSet("thread list", a.errOut)
	statusValue := fs.String("status", "", "Thread status")
	includeDeleted := fs.Bool("include-deleted", false, "include soft-deleted Threads")
	limit := fs.Int("limit", 100, "maximum rows")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"status": true, "include-deleted": false, "limit": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 0 || *limit <= 0 || *limit > 1000 {
		return errors.New("usage: cyberagent thread list [--status active|archived|deleted] [--include-deleted] [--limit <1..1000>] [--json]")
	}
	status := domain.ThreadStatus(strings.TrimSpace(*statusValue))
	if status != "" && !domain.ValidThreadStatus(status) {
		return fmt.Errorf("invalid Thread status %q", status)
	}
	items, err := service.List(ctx, domain.ThreadFilter{Status: status,
		IncludeDeleted: *includeDeleted, Limit: *limit}, time.Time{}, "")
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, items)
	}
	if len(items) == 0 {
		fmt.Fprintln(a.out, "no threads")
		return nil
	}
	for _, item := range items {
		fmt.Fprintf(a.out, "%s\t%s\tlast=%s\tactive=%s\t%s\n", item.ID,
			item.Status, item.LastRunID, item.ActiveRunID, item.Title)
	}
	return nil
}

func (a *App) threadShow(ctx context.Context, service *application.ThreadService,
	args []string,
) error {
	fs := newFlagSet("thread show", a.errOut)
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"json": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent thread show <thread-id> [--json]")
	}
	threadRecord, err := service.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	bindings, err := service.Runs(ctx, threadRecord.ID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, struct {
			Thread domain.Thread      `json:"thread"`
			Runs   []domain.ThreadRun `json:"runs"`
		}{Thread: threadRecord, Runs: bindings})
	}
	fmt.Fprintf(a.out, "thread: %s\nprotocol: %s\nstatus: %s\nmission: %s\nworkspace: %s\ntitle: %s\nactive_run: %s\nlast_run: %s\nversion: %d\nrun_count: %d\n",
		threadRecord.ID, threadRecord.ProtocolVersion, threadRecord.Status,
		threadRecord.MissionID, threadRecord.WorkspaceID, threadRecord.Title,
		threadRecord.ActiveRunID, threadRecord.LastRunID, threadRecord.Version,
		len(bindings))
	for _, binding := range bindings {
		fmt.Fprintf(a.out, "run[%d]: %s session=%s predecessor=%s\n", binding.Ordinal,
			binding.RunID, binding.SessionID, binding.PredecessorRunID)
	}
	return nil
}

func (a *App) threadSend(ctx context.Context, service *application.ThreadService,
	args []string,
) error {
	fs := newFlagSet("thread send", a.errOut)
	operationKey := fs.String("operation-key", "", "stable durable retry key")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"operation-key": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() < 2 || strings.TrimSpace(*operationKey) == "" {
		return errors.New(`usage: cyberagent thread send <thread-id> "message" --operation-key <key> [--json]`)
	}
	result, err := service.Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: fs.Arg(0),
		Content: strings.Join(fs.Args()[1:], " "), OperationKey: *operationKey,
		RequestedBy: "cli_thread_operator",
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, result)
	}
	fmt.Fprintf(a.out, "thread: %s\nrun: %s\nsession: %s\nsteering: %s\nstatus: %s\nsuccessor_created: %t\npredecessor_run: %s\nreplayed: %t\ncapability_grant: false\n",
		result.Thread.ID, result.Run.ID, result.Session.ID, result.Message.ID,
		result.Message.Status, result.SuccessorCreated, result.PredecessorRunID,
		result.Replayed)
	return nil
}

func (a *App) threadHistory(ctx context.Context, service *application.ThreadService,
	args []string,
) error {
	fs := newFlagSet("thread history", a.errOut)
	all := fs.Bool("all", false, "include compacted messages")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"all": false, "json": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent thread history <thread-id> [--all] [--json]")
	}
	messages, err := service.Messages(ctx, fs.Arg(0), *all, 0, 1000)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, messages)
	}
	if len(messages) == 0 {
		fmt.Fprintln(a.out, "no messages")
		return nil
	}
	for _, message := range messages {
		fmt.Fprintf(a.out, "run=%s session=%s %s %s/%s %s: %s\n", message.RunID,
			message.SessionID, message.ID, message.SourceKind, message.Status,
			message.Role, message.Content)
	}
	return nil
}

func (a *App) threadLifecycle(ctx context.Context, service *application.ThreadService,
	action string, args []string,
) error {
	fs := newFlagSet("thread "+action, a.errOut)
	expectedVersion := fs.Int64("expected-version", 0, "optimistic Thread version")
	operationKey := fs.String("operation-key", "", "stable durable retry key")
	operator := fs.String("operator", "cli_thread_operator", "operator identity")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"expected-version": true, "operation-key": true, "operator": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *expectedVersion <= 0 || strings.TrimSpace(*operationKey) == "" {
		return fmt.Errorf("usage: cyberagent thread %s <thread-id> --expected-version <n> --operation-key <key> [--operator <id>] [--json]", action)
	}
	threadRecord, err := service.Transition(ctx, fs.Arg(0),
		domain.ThreadLifecycleAction(action), *expectedVersion, *operator, *operationKey)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, threadRecord)
	}
	fmt.Fprintf(a.out, "thread: %s\nstatus: %s\nversion: %d\ncapability_grant: false\n",
		threadRecord.ID, threadRecord.Status, threadRecord.Version)
	return nil
}

func (a *App) threadExport(ctx context.Context, service *application.ThreadService,
	args []string,
) error {
	fs := newFlagSet("thread export", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent thread export <thread-id>")
	}
	value, err := service.Export(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return writeContextJSON(a, value)
}
