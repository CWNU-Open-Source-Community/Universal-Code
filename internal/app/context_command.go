package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/workspace"
)

func (a *App) contextCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("context subcommand is required")
	}
	switch args[0] {
	case "compact":
		return a.contextCompact(ctx, args[1:])
	case "show":
		return a.contextShow(ctx, args[1:])
	case "instructions":
		if len(args) > 1 && args[1] == "scaffold" {
			return a.contextInstructionsScaffold(ctx, args[2:])
		}
		return a.contextInstructions(ctx, args[1:])
	case "memory":
		return a.contextMemory(ctx, args[1:])
	default:
		return fmt.Errorf("unknown context subcommand %q", args[0])
	}
}

func (a *App) contextInstructions(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("context instructions", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	runID := fs.String("run", "", "Run identity for pinned/live inspection")
	target := fs.String("target", "", "workspace-relative target path")
	confirm := fs.Bool("confirm", false, "confirm a fingerprint-bound Run refresh")
	expectedFingerprint := fs.String("expected-fingerprint", "", "currently pinned fingerprint")
	expectedLiveFingerprint := fs.String("expected-live-fingerprint", "", "reviewed live fingerprint")
	operator := fs.String("operator", "cli_operator", "operator identity")
	jsonOutput := fs.Bool("json", false, "emit the full machine-readable snapshot")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "run": true, "target": true, "confirm": false,
		"expected-fingerprint": true, "expected-live-fingerprint": true,
		"operator": true, "json": false,
	})); err != nil {
		return err
	}
	workspaceValue := strings.TrimSpace(*workspaceName)
	runValue := strings.TrimSpace(*runID)
	if fs.NArg() != 0 || (workspaceValue == "") == (runValue == "") {
		return errors.New("usage: cyberagent context instructions (--workspace <name> | --run <run-id>) [--target <path>] [--confirm --expected-fingerprint <pinned-sha256> --expected-live-fingerprint <live-sha256>] [--json]")
	}
	if runValue != "" {
		if *confirm && (!flagWasSet(fs, "expected-fingerprint") ||
			!flagWasSet(fs, "expected-live-fingerprint")) {
			return errors.New("confirmed project instruction refresh requires pinned and live fingerprints")
		}
		if !*confirm && (flagWasSet(fs, "expected-fingerprint") ||
			flagWasSet(fs, "expected-live-fingerprint")) {
			return errors.New("expected project instruction fingerprints are valid only with --confirm")
		}
		service := application.NewProjectInstructionService(a.store)
		var state application.ProjectInstructionState
		var err error
		if *confirm {
			state, err = service.Refresh(ctx, runValue, *target, *expectedFingerprint,
				*expectedLiveFingerprint, *operator, true)
		} else {
			state, err = service.Inspect(ctx, runValue, *target)
		}
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeContextJSON(a, state)
		}
		fmt.Fprintf(a.out, "run: %s\nworkspace: %s\npinned_present: %t\npinned_fingerprint: %s\nlive_fingerprint: %s\nstale: %t\nrefresh_confirmed: %t\ncapability_grant: false\n",
			state.RunID, state.WorkspaceID, state.PinnedPresent,
			state.Pinned.Snapshot.Fingerprint, state.Live.Fingerprint,
			state.Stale, state.RefreshConfirmed)
		fmt.Fprintf(a.out, "diff_added: %s\ndiff_changed: %s\ndiff_removed: %s\norder_changed: %t\n",
			strings.Join(state.Diff.Added, ","), strings.Join(state.Diff.Changed, ","),
			strings.Join(state.Diff.Removed, ","), state.Diff.OrderChanged)
		return nil
	}
	if *confirm || flagWasSet(fs, "expected-fingerprint") ||
		flagWasSet(fs, "expected-live-fingerprint") {
		return errors.New("project instruction refresh requires --run <run-id>")
	}
	record, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(workspaceValue))
	if err != nil {
		return err
	}
	targetValue := strings.TrimSpace(*target)
	if targetValue == "" {
		targetValue = "."
	}
	snapshot, err := projectconfig.DiscoverInstructions(ctx, record.RootPath, targetValue)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, snapshot)
	}
	fmt.Fprintf(a.out, "protocol: %s\nworkspace: %s\ntarget: %s\nfingerprint: %s\nsource_count: %d\nconflict_count: %d\ncapability_grant: false\n",
		snapshot.ProtocolVersion, record.ID, snapshot.TargetPath, snapshot.Fingerprint,
		len(snapshot.Sources), len(snapshot.Conflicts))
	for _, source := range snapshot.Sources {
		fmt.Fprintf(a.out, "source: %s\n  scope: %s\n  kind: %s\n  precedence: %d\n  sha256: %s\n  trust: %s\n  why_effective: %s\n",
			source.Path, source.Scope, source.Kind, source.Precedence,
			source.ContentSHA256, source.Trust, source.WhyEffective)
	}
	for _, conflict := range snapshot.Conflicts {
		fmt.Fprintf(a.out, "conflict: %s -> %s\n  resolution: %s\n",
			conflict.LowerPrecedencePath, conflict.HigherPrecedencePath,
			conflict.Resolution)
	}
	return nil
}

func (a *App) contextInstructionsScaffold(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("context instructions scaffold", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	target := fs.String("target", "", "workspace-relative directory to scaffold")
	printOnly := fs.Bool("print", false, "render the draft to stdout without writing")
	jsonOutput := fs.Bool("json", false, "emit the full machine-readable result")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "target": true, "print": false, "json": false,
	})); err != nil {
		return err
	}
	workspaceValue := strings.TrimSpace(*workspaceName)
	if workspaceValue == "" || fs.NArg() != 0 {
		return errors.New("usage: cyberagent context instructions scaffold --workspace <name> [--target <dir>] [--print] [--json]")
	}
	record, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(workspaceValue))
	if err != nil {
		return err
	}
	result, err := projectconfig.ScaffoldProjectInstructions(ctx, record.RootPath, *target, !*printOnly)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, result)
	}
	status := "preview"
	if result.Written {
		status = "written"
	}
	fmt.Fprintf(a.out, "protocol: %s\nworkspace: %s\nfile: %s\nstatus: %s\nsha256: %s\nbytes: %d\ncapability_grant: false\n",
		result.ProtocolVersion, record.ID, result.FilePath, status,
		result.ContentSHA256, result.ContentBytes)
	if len(result.Detected) > 0 {
		labels := make([]string, 0, len(result.Detected))
		for _, detection := range result.Detected {
			labels = append(labels, fmt.Sprintf("%s (%s)", detection.Path, detection.Label))
		}
		fmt.Fprintf(a.out, "detected: %s\n", strings.Join(labels, ", "))
	} else {
		fmt.Fprint(a.out, "detected: none\n")
	}
	if !result.Written {
		fmt.Fprint(a.out, result.Content)
	}
	return nil
}

func (a *App) contextMemory(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cyberagent context memory create|list|show|edit|enable|disable|delete|export")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	switch args[0] {
	case "create":
		return a.contextMemoryCreate(ctx, args[1:])
	case "list":
		return a.contextMemoryList(ctx, args[1:], false)
	case "export":
		return a.contextMemoryList(ctx, args[1:], true)
	case "show":
		return a.contextMemoryShow(ctx, args[1:])
	case "edit":
		return a.contextMemoryEdit(ctx, args[1:])
	case "enable", "disable":
		return a.contextMemoryStatus(ctx, args[0], args[1:])
	case "delete":
		return a.contextMemoryDelete(ctx, args[1:])
	default:
		return fmt.Errorf("unknown context memory subcommand %q", args[0])
	}
}

func (a *App) contextMemoryCreate(ctx context.Context, args []string) error {
	fs := newFlagSet("context memory create", a.errOut)
	scopeValue := fs.String("scope", "user", "memory scope: user or project")
	workspaceName := fs.String("workspace", "", "workspace name for project scope")
	title := fs.String("title", "", "memory title")
	content := fs.String("content", "", "memory content")
	sourceRef := fs.String("source-ref", "", "non-sensitive provenance reference")
	retention := fs.Duration("retention", 0, "retention duration; zero means no expiry")
	operator := fs.String("operator", "cli_operator", "operator identity")
	redactSensitive := fs.Bool("redact-sensitive", false, "explicitly redact detected sensitive text")
	var references repeatedString
	fs.Var(&references, "reference", "non-sensitive reference; repeatable")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"scope": true, "workspace": true, "title": true, "content": true,
		"source-ref": true, "retention": true, "operator": true,
		"redact-sensitive": false, "reference": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*title) == "" || strings.TrimSpace(*content) == "" {
		return errors.New("usage: cyberagent context memory create --title <text> --content <text> [--scope user|project --workspace <name>] [--retention <duration>]")
	}
	scope, scopeID, err := a.contextMemoryScope(ctx, *scopeValue, *workspaceName)
	if err != nil {
		return err
	}
	var retentionUntil *time.Time
	if *retention < 0 {
		return errors.New("memory retention cannot be negative")
	}
	if *retention > 0 {
		value := time.Now().UTC().Add(*retention)
		retentionUntil = &value
	}
	memory, err := application.NewContextMemoryService(a.store).Create(ctx,
		contextmgr.CreateMemoryRequest{Scope: scope, ScopeID: scopeID, Title: *title,
			Content: *content, SourceRef: *sourceRef, References: references,
			RetentionUntil: retentionUntil, RequestedBy: *operator,
			RedactSensitive: *redactSensitive})
	if err != nil {
		return err
	}
	return writeContextJSON(a, memory)
}

func (a *App) contextMemoryList(ctx context.Context, args []string, export bool) error {
	name := "context memory list"
	if export {
		name = "context memory export"
	}
	fs := newFlagSet(name, a.errOut)
	scopeValue := fs.String("scope", "user", "memory scope: user or project")
	workspaceName := fs.String("workspace", "", "workspace name for project scope")
	all := fs.Bool("all", false, "include disabled and expired memories")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"scope": true, "workspace": true, "all": false, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: cyberagent %s [--scope user|project --workspace <name>] [--all] [--json]", name)
	}
	scope, scopeID, err := a.contextMemoryScope(ctx, *scopeValue, *workspaceName)
	if err != nil {
		return err
	}
	service := application.NewContextMemoryService(a.store)
	filter := contextmgr.MemoryFilter{Scope: scope, ScopeID: scopeID,
		IncludeDisabled: *all, IncludeExpired: *all, Limit: contextmgr.MaxMemoryListItems}
	if export {
		value, err := service.Export(ctx, filter)
		if err != nil {
			return err
		}
		return writeContextJSON(a, value)
	}
	values, err := service.List(ctx, filter)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, values)
	}
	if len(values) == 0 {
		fmt.Fprintln(a.out, "no long-term memories")
		return nil
	}
	for _, value := range values {
		retention := "none"
		if value.RetentionUntil != nil {
			retention = value.RetentionUntil.Format(time.RFC3339)
		}
		fmt.Fprintf(a.out, "%s\t%s/%s\t%s\tv%d\tretention=%s\t%s\n",
			value.ID, value.Scope, value.ScopeID, value.Status, value.Version,
			retention, value.Title)
	}
	return nil
}

func (a *App) contextMemoryShow(ctx context.Context, args []string) error {
	fs := newFlagSet("context memory show", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent context memory show <memory-id>")
	}
	value, err := application.NewContextMemoryService(a.store).Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return writeContextJSON(a, value)
}

func (a *App) contextMemoryEdit(ctx context.Context, args []string) error {
	fs := newFlagSet("context memory edit", a.errOut)
	title := fs.String("title", "", "replacement title")
	content := fs.String("content", "", "replacement content")
	sourceRef := fs.String("source-ref", "", "replacement provenance reference")
	retention := fs.Duration("retention", 0, "replacement retention from now; zero clears")
	version := fs.Int64("version", 0, "expected memory version")
	operator := fs.String("operator", "cli_operator", "operator identity")
	redactSensitive := fs.Bool("redact-sensitive", false, "explicitly redact detected sensitive text")
	var references repeatedString
	fs.Var(&references, "reference", "replacement non-sensitive reference; repeatable")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"title": true, "content": true, "source-ref": true, "retention": true,
		"version": true, "operator": true, "redact-sensitive": false, "reference": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *version < 1 ||
		(!flagWasSet(fs, "title") && !flagWasSet(fs, "content") &&
			!flagWasSet(fs, "source-ref") && !flagWasSet(fs, "retention") &&
			!flagWasSet(fs, "reference")) {
		return errors.New("usage: cyberagent context memory edit <memory-id> --version <n> [--title <text>] [--content <text>] [--source-ref <ref>] [--reference <ref>] [--retention <duration>]")
	}
	request := contextmgr.UpdateMemoryRequest{ExpectedVersion: *version,
		RequestedBy: *operator, RedactSensitive: *redactSensitive}
	if flagWasSet(fs, "title") {
		request.Title = title
	}
	if flagWasSet(fs, "content") {
		request.Content = content
	}
	if flagWasSet(fs, "source-ref") {
		request.SourceRef = sourceRef
	}
	if flagWasSet(fs, "reference") {
		values := []string(references)
		request.References = &values
	}
	if flagWasSet(fs, "retention") {
		if *retention < 0 {
			return errors.New("memory retention cannot be negative")
		}
		var value *time.Time
		if *retention > 0 {
			at := time.Now().UTC().Add(*retention)
			value = &at
		}
		request.RetentionUntil = &value
	}
	memory, err := application.NewContextMemoryService(a.store).Update(ctx, fs.Arg(0), request)
	if err != nil {
		return err
	}
	return writeContextJSON(a, memory)
}

func (a *App) contextMemoryStatus(ctx context.Context, action string, args []string) error {
	fs := newFlagSet("context memory "+action, a.errOut)
	version := fs.Int64("version", 0, "expected memory version")
	operator := fs.String("operator", "cli_operator", "operator identity")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"version": true, "operator": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *version < 1 {
		return fmt.Errorf("usage: cyberagent context memory %s <memory-id> --version <n> [--operator <id>]", action)
	}
	status := contextmgr.MemoryStatusActive
	if action == "disable" {
		status = contextmgr.MemoryStatusDisabled
	}
	memory, err := application.NewContextMemoryService(a.store).Update(ctx, fs.Arg(0),
		contextmgr.UpdateMemoryRequest{Status: &status, ExpectedVersion: *version,
			RequestedBy: *operator})
	if err != nil {
		return err
	}
	return writeContextJSON(a, memory)
}

func (a *App) contextMemoryDelete(ctx context.Context, args []string) error {
	fs := newFlagSet("context memory delete", a.errOut)
	version := fs.Int64("version", 0, "expected memory version")
	operator := fs.String("operator", "cli_operator", "operator identity")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"version": true, "operator": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *version < 1 {
		return errors.New("usage: cyberagent context memory delete <memory-id> --version <n> [--operator <id>]")
	}
	deleted, err := application.NewContextMemoryService(a.store).Delete(ctx, fs.Arg(0),
		*version, *operator)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "deleted: %t\nmemory: %s\nrecoverable: false\n", deleted, fs.Arg(0))
	return nil
}

func (a *App) contextMemoryScope(ctx context.Context, scopeValue,
	workspaceName string,
) (contextmgr.MemoryScope, string, error) {
	scope := contextmgr.MemoryScope(strings.ToLower(strings.TrimSpace(scopeValue)))
	if scope == contextmgr.MemoryScopeUser {
		return scope, contextmgr.LocalUserMemoryScope, nil
	}
	if scope != contextmgr.MemoryScopeProject || strings.TrimSpace(workspaceName) == "" {
		return "", "", errors.New("project memory scope requires --workspace <name>")
	}
	record, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(workspaceName))
	if err != nil {
		return "", "", err
	}
	return scope, record.ID, nil
}

func writeContextJSON(a *App, value any) error {
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (a *App) contextCompact(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("context compact", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	taskID := fs.String("task", "", "task id")
	var rawMessages repeatedString
	fs.Var(&rawMessages, "message", "message as role: content; repeatable")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"workspace": true, "task": true, "message": true})); err != nil {
		return err
	}
	if strings.TrimSpace(*taskID) == "" {
		return errors.New("usage: cyberagent context compact --task <id> --message \"user: ...\" [--workspace <name>]")
	}
	if len(rawMessages) == 0 {
		return errors.New("context compact requires at least one --message")
	}

	workspaceID := ""
	if strings.TrimSpace(*workspaceName) != "" {
		rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
		if err != nil {
			return err
		}
		workspaceID = rec.ID
	}

	messages := make([]contextmgr.Message, 0, len(rawMessages))
	for _, raw := range rawMessages {
		messages = append(messages, contextmgr.ParseMessage(raw))
	}
	engine := a.newLifecycleHookEngine()
	manager := contextmgr.NewManager(a.store, contextmgr.DefaultConfig()).
		WithBeforeCompactGuard(func(ctx context.Context, taskID, workspaceID string,
			sourceMessages, preservedMessages int,
		) error {
			_, err := hooks.ExecuteBoundary(ctx, engine, hooks.Input{
				Event: hooks.Compaction, WorkspaceID: workspaceID,
			}, map[string]any{
				"task_id": taskID, "source_messages": sourceMessages,
				"preserved_messages": preservedMessages, "source": "context_cli",
			})
			return err
		})
	result, err := manager.Compact(ctx, *taskID, workspaceID, messages)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "context compacted\nsummary_id: %d\ntask: %s\nworkspace: %s\nsource_messages: %d\npreserved_messages: %d\nremoved_messages: %d\ntoken_estimate: %d\n",
		result.Summary.ID, result.Summary.TaskID, result.Summary.WorkspaceID, result.Summary.SourceMessageCount,
		result.Summary.PreservedMessageCount, result.RemovedMessages, result.Summary.TokenEstimate)
	return nil
}

func (a *App) contextShow(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("context show", a.errOut)
	taskID := fs.String("task", "", "task id")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"task": true})); err != nil {
		return err
	}
	if strings.TrimSpace(*taskID) == "" {
		return errors.New("usage: cyberagent context show --task <id>")
	}
	manager := contextmgr.NewManager(a.store, contextmgr.DefaultConfig())
	summary, ok, err := manager.Latest(ctx, *taskID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(a.out, "no context summary")
		return nil
	}
	fmt.Fprintf(a.out, "summary_id: %d\ntask: %s\nworkspace: %s\nsource_messages: %d\npreserved_messages: %d\ntoken_estimate: %d\ncreated_at: %s\n\n%s",
		summary.ID, summary.TaskID, summary.WorkspaceID, summary.SourceMessageCount,
		summary.PreservedMessageCount, summary.TokenEstimate, summary.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), summary.Content)
	if !strings.HasSuffix(summary.Content, "\n") {
		fmt.Fprintln(a.out)
	}
	return nil
}

type repeatedString []string

func (r *repeatedString) String() string {
	return strings.Join(*r, ",")
}

func (r *repeatedString) Set(value string) error {
	*r = append(*r, value)
	return nil
}
