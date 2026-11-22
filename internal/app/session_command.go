package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

func (a *App) sessionCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("session subcommand is required")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	manager := a.newSessionManager()
	switch args[0] {
	case "create":
		return a.sessionCreate(ctx, manager, args[1:])
	case "list":
		return a.sessionList(ctx, manager)
	case "send":
		return a.sessionSend(ctx, manager, args[1:])
	case "history":
		return a.sessionHistory(ctx, manager, args[1:])
	case "tree":
		return a.sessionTree(ctx, args[1:])
	case "checkpoint":
		return a.sessionContinuityCheckpoint(ctx, args[1:])
	case "fork", "resume":
		return a.sessionContinuityBranch(ctx, args[0], args[1:])
	default:
		return fmt.Errorf("unknown session subcommand %q", args[0])
	}
}

func (a *App) sessionTree(ctx context.Context, args []string) error {
	fs := newFlagSet("session tree", a.errOut)
	jsonOutput := fs.Bool("json", false, "emit the full machine-readable tree")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"json": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent session tree <session-id> [--json]")
	}
	tree, err := application.NewContextContinuityService(a.store).
		WithLifecycleHooks(a.newLifecycleHookEngine()).Tree(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, tree)
	}
	fmt.Fprintf(a.out, "protocol: %s\nsession: %s\nworkspace: %s\nnode_count: %d\ncapability_grant: false\n",
		tree.ProtocolVersion, tree.SessionID, tree.WorkspaceID, len(tree.Nodes))
	for _, node := range tree.Nodes {
		link := node.ParentID
		if node.SourceNodeID != "" {
			link = "source=" + node.SourceNodeID
		} else if link != "" {
			link = "parent=" + link
		}
		fmt.Fprintf(a.out, "%s\t%s\tstatus=%s\trun=%s\t%s\t%s\n",
			node.ID, node.Kind, node.Status, node.RunID, link, node.Title)
		for _, warning := range node.Warnings {
			fmt.Fprintf(a.out, "  warning: %s\n", warning)
		}
	}
	return nil
}

func (a *App) sessionContinuityCheckpoint(ctx context.Context, args []string) error {
	fs := newFlagSet("session checkpoint", a.errOut)
	title := fs.String("title", "Operator checkpoint", "checkpoint title")
	summary := fs.String("summary", "", "bounded operator summary")
	operator := fs.String("operator", "cli_operator", "operator identity")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"title": true, "summary": true, "operator": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent session checkpoint <run-id> [--title <text>] [--summary <text>] [--operator <id>] [--json]")
	}
	node, err := application.NewContextContinuityService(a.store).
		WithLifecycleHooks(a.newLifecycleHookEngine()).Checkpoint(ctx,
		application.CreateContinuityCheckpointRequest{RunID: fs.Arg(0), Title: *title,
			Summary: *summary, RequestedBy: *operator})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, node)
	}
	fmt.Fprintf(a.out, "checkpoint: %s\nrun: %s\nsession: %s\nparent: %s\nfingerprint: %s\ngit_branch: %s\ngit_head: %s\ncontext_items: %d\ncapability_grant: false\n",
		node.ID, node.RunID, node.SessionID, node.ParentID, node.ContextSHA256,
		node.GitBranch, node.GitHead, len(node.Snapshot.InheritedContext))
	return nil
}

func (a *App) sessionContinuityBranch(ctx context.Context, action string, args []string) error {
	fs := newFlagSet("session "+action, a.errOut)
	goal := fs.String("goal", "", "new Run goal; defaults to the source Run goal")
	operator := fs.String("operator", "cli_operator", "operator identity")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"goal": true, "operator": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cyberagent session %s <continuity-node-id> [--goal <text>] [--operator <id>] [--json]", action)
	}
	kind := contextmgr.ContinuityNodeFork
	if action == "resume" {
		kind = contextmgr.ContinuityNodeResume
	}
	result, err := application.NewContextContinuityService(a.store).
		WithLifecycleHooks(a.newLifecycleHookEngine()).Branch(ctx,
		application.BranchContinuityRequest{SourceNodeID: fs.Arg(0), Kind: kind,
			Goal: *goal, RequestedBy: *operator})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, result)
	}
	fmt.Fprintf(a.out, "%s: %s\nrun: %s\nsession: %s\nsource: %s\ncontext_fingerprint: %s\ninherited_items: %d\nnot_inherited: %s\ncapability_grant: false\n",
		action, result.Node.ID, result.Run.ID, result.Run.SessionID,
		result.Node.SourceNodeID, result.Node.ContextSHA256, len(result.Inherited),
		strings.Join(result.NotInherited, ","))
	return nil
}

func (a *App) newSessionManager() *session.Manager {
	engine := a.newLifecycleHookEngine()
	executor := application.NewSessionRunChatExecutor(a.store, a.router, a.checker).
		WithActiveCalls(a.calls).WithLifecycleHooks(engine)
	if a.codeIntel != nil {
		executor.WithCodeIntel(a.codeIntel)
	}
	return session.NewManager(a.store, a.router, a.checker).
		WithRunChatExecutor(executor).WithLifecycleHooks(engine)
}

func (a *App) sessionCreate(ctx context.Context, manager *session.Manager, args []string) error {
	fs := newFlagSet("session create", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	title := fs.String("title", "New session", "session title")
	route := fs.String("route", "learn", "model route")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"workspace": true, "title": true, "route": true})); err != nil {
		return err
	}
	workspaceID := ""
	if strings.TrimSpace(*workspaceName) != "" {
		rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
		if err != nil {
			return err
		}
		workspaceID = rec.ID
	}
	if fs.NArg() > 0 && *title == "New session" {
		*title = strings.Join(fs.Args(), " ")
	}
	sess, err := manager.Create(ctx, workspaceID, *title, *route)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "session %s created\nroute: %s\nworkspace: %s\ntitle: %s\n", sess.ID, sess.Route, sess.WorkspaceID, sess.Title)
	return nil
}

func (a *App) sessionList(ctx context.Context, manager *session.Manager) error {
	sessions, err := manager.List(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintln(a.out, "no sessions")
		return nil
	}
	for _, sess := range sessions {
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n", sess.ID, sess.Route, sess.Status, sess.Title)
	}
	return nil
}

func (a *App) sessionSend(ctx context.Context, manager *session.Manager, args []string) error {
	fs := newFlagSet("session send", a.errOut)
	operationKey := fs.String("operation-key", "", "stable durable steering retry key")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"operation-key": true})); err != nil {
		return err
	}
	if flagWasSet(fs, "operation-key") && strings.TrimSpace(*operationKey) == "" {
		return apperror.New(apperror.CodeInvalidArgument, "session operation key cannot be blank")
	}
	if fs.NArg() < 2 {
		return errors.New(`usage: cyberagent session send <session-id> "message" [--operation-key <key>]`)
	}
	result, err := manager.SendWithOptions(ctx, fs.Arg(0), strings.Join(fs.Args()[1:], " "),
		session.SendOptions{OperationKey: *operationKey})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, result.Text)
	if result.RunID != "" {
		fmt.Fprintf(a.out, "\n[run %s: action=%s status=%s]\n", result.RunID, result.RunAction, result.RunStatus)
	}
	if result.Queued {
		fmt.Fprintf(a.out, "[steering %s: sequence=%d status=%s queued=true replayed=%t]\n",
			result.SteeringID, result.SteeringSequence, result.SteeringStatus,
			result.SteeringReplayed)
	}
	if result.Compacted {
		fmt.Fprintf(a.out, "\n[context compacted: summary=%d]\n", result.SummaryID)
	}
	return nil
}

func (a *App) sessionHistory(ctx context.Context, manager *session.Manager, args []string) error {
	fs := newFlagSet("session history", a.errOut)
	all := fs.Bool("all", false, "include compacted messages")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"all": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent session history <session-id> [--all]")
	}
	messages, err := manager.History(ctx, fs.Arg(0), *all)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		fmt.Fprintln(a.out, "no messages")
		return nil
	}
	for _, msg := range messages {
		marker := ""
		if msg.Compacted {
			marker = " compacted"
		}
		sourceRef := ""
		if msg.Provenance.SourceRef != "" {
			sourceRef = " ref=" + msg.Provenance.SourceRef
		}
		fmt.Fprintf(a.out, "#%d%s %s [%s authorized=%t%s]: %s\n", msg.ID, marker, msg.Role,
			msg.Provenance.SourceKind, msg.Provenance.InstructionAuthorized, sourceRef, msg.Content)
	}
	return nil
}
