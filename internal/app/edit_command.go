package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/workspace"
)

type fileEditManager interface {
	Propose(context.Context, fileedit.Proposal) (fileedit.Edit, error)
	Get(context.Context, string) (fileedit.Edit, error)
	List(context.Context, fileedit.ListFilter) ([]fileedit.Edit, error)
	Approve(context.Context, string, string) (fileedit.Edit, error)
	Deny(context.Context, string, string) (fileedit.Edit, error)
}

func (a *App) editCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("edit subcommand is required")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	manager := a.newToolGateway().FileEdits()
	switch args[0] {
	case "propose":
		return a.editPropose(ctx, manager, args[1:])
	case "list":
		return a.editList(ctx, manager, args[1:])
	case "show":
		return a.editShow(ctx, manager, args[1:])
	case "approve":
		return a.editApprove(ctx, manager, args[1:])
	case "review-approve":
		return a.editReview(ctx, application.FileEditApproveIntent, args[1:])
	case "review-deny":
		return a.editReview(ctx, application.FileEditDeny, args[1:])
	case "apply":
		return a.editApply(ctx, args[1:])
	case "deny":
		return a.editDeny(ctx, manager, args[1:])
	default:
		return fmt.Errorf("unknown edit subcommand %q", args[0])
	}
}

func (a *App) editApply(ctx context.Context, args []string) error {
	fs := newFlagSet("edit apply", a.errOut)
	operationKey := fs.String("operation-key", "", "stable apply operation key")
	operator := fs.String("operator", "cli_operator", "operator identity")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"operation-key": true, "operator": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 2 || strings.TrimSpace(*operationKey) == "" {
		return errors.New("usage: cyberagent edit apply <run-id> <edit-id> --operation-key <key> [--operator <id>]")
	}
	drydocks, err := a.newRunFileDrydockService(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	result, err := application.NewFileEditApplyService(a.store, a.checker).WithRunWorktree(drydocks).Apply(ctx,
		application.ApplyFileEditRequest{
			Version: fileedit.FileEditApplyProtocolVersion, RunID: fs.Arg(0),
			EditID: fs.Arg(1), OperationKey: *operationKey, AppliedBy: *operator,
		})
	if result.Edit.ID != "" {
		fmt.Fprintf(a.out, "file edit %s %s\nrun: %s\npath: %s\napply_status: %s\nreplayed: %t\nfile_written: %t\npolicy_rechecked: true\n",
			result.Edit.ID, result.Edit.Status, fs.Arg(0), result.Edit.Path,
			result.Result.Status, result.Replayed, result.FileWritten)
	}
	return err
}

func (a *App) editReview(ctx context.Context, action application.FileEditReviewAction,
	args []string,
) error {
	name := "edit review-approve"
	if action == application.FileEditDeny {
		name = "edit review-deny"
	}
	fs := newFlagSet(name, a.errOut)
	enablePermission := fs.Bool("enable-permission-control", false, "enable the permission-control runtime gate")
	enableFull := fs.Bool("enable-danger-full-access", false, "enable the Full runtime gate")
	confirmFull := fs.Bool("confirm-full", false, "activate the current Full preference for this continuation only")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"enable-permission-control": false, "enable-danger-full-access": false, "confirm-full": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: cyberagent %s <run-id> <edit-id> [--confirm-full --enable-permission-control --enable-danger-full-access]", name)
	}
	drydocks, err := a.newRunFileDrydockService(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	result, err := application.NewFileEditReviewService(a.store).WithRunWorktree(drydocks).Review(ctx,
		application.ReviewFileEditRequest{
			Version: application.FileEditReviewProtocolVersion, RunID: fs.Arg(0),
			EditID: fs.Arg(1), Action: action,
		})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "file edit %s %s\nrun: %s\naction: %s\nreplayed: %t\nfile_written: false\n",
		result.Edit.ID, result.Edit.Status, fs.Arg(0), result.Action, result.Replayed)
	return a.continueReviewedFileEdit(ctx, fs.Arg(0), result.Edit.ID,
		cliExecutionPermissionCapabilities(*enablePermission, *enableFull), *confirmFull)
}

func (a *App) editPropose(ctx context.Context, manager fileEditManager, args []string) error {
	fs := newFlagSet("edit propose", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	path := fs.String("path", "", "workspace-relative file path")
	content := fs.String("content", "", "replacement content")
	contentFile := fs.String("content-file", "", "local file containing replacement content")
	sessionID := fs.String("session", "", "optional session id")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace":    true,
		"path":         true,
		"content":      true,
		"content-file": true,
		"session":      true,
	})); err != nil {
		return err
	}
	contentSet := flagWasSet(fs, "content")
	contentFileSet := flagWasSet(fs, "content-file")
	if strings.TrimSpace(*workspaceName) == "" || strings.TrimSpace(*path) == "" || contentSet == contentFileSet {
		return errors.New("usage: cyberagent edit propose --workspace <name> --path <path> (--content <text> | --content-file <path>) [--session <id>]")
	}
	replacement := *content
	if contentFileSet {
		loaded, err := readEditContent(*contentFile)
		if err != nil {
			return err
		}
		replacement = loaded
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
	if err != nil {
		return err
	}
	edit, err := manager.Propose(ctx, fileedit.Proposal{
		SessionID:     *sessionID,
		WorkspaceID:   rec.ID,
		WorkspaceRoot: rec.RootPath,
		Path:          *path,
		ProposedText:  replacement,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "file edit %s proposed\n", edit.ID)
	printFileEdit(a.out, edit)
	return nil
}

func (a *App) editList(ctx context.Context, manager fileEditManager, args []string) error {
	fs := newFlagSet("edit list", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	sessionID := fs.String("session", "", "session id")
	status := fs.String("status", "", "file edit status")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"workspace": true, "session": true, "status": true})); err != nil {
		return err
	}
	filter := fileedit.ListFilter{SessionID: *sessionID, Status: *status}
	if strings.TrimSpace(*workspaceName) != "" {
		rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
		if err != nil {
			return err
		}
		filter.WorkspaceID = rec.ID
	}
	edits, err := manager.List(ctx, filter)
	if err != nil {
		return err
	}
	if len(edits) == 0 {
		fmt.Fprintln(a.out, "no file edits")
		return nil
	}
	for _, edit := range edits {
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n", edit.ID, edit.Status, edit.WorkspaceID, edit.Path)
	}
	return nil
}

func (a *App) editShow(ctx context.Context, manager fileEditManager, args []string) error {
	fs := newFlagSet("edit show", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent edit show <edit-id>")
	}
	edit, err := manager.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	printFileEdit(a.out, edit)
	return nil
}

func (a *App) editApprove(ctx context.Context, manager fileEditManager, args []string) error {
	fs := newFlagSet("edit approve", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent edit approve <edit-id>")
	}
	edit, err := manager.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if strings.TrimSpace(edit.SessionID) != "" {
		if run, found, lookupErr := a.store.GetRunBySession(ctx, edit.SessionID); lookupErr != nil {
			return lookupErr
		} else if found {
			return fmt.Errorf("run-bound file edit %s cannot use legacy approve; use `cyberagent edit review-approve %s %s`, then `cyberagent edit apply %s %s --operation-key <key>`",
				edit.ID, run.ID, edit.ID, run.ID, edit.ID)
		}
	}
	rec, err := a.store.GetWorkspaceByID(ctx, edit.WorkspaceID)
	if err != nil {
		return err
	}
	edit, err = manager.Approve(ctx, edit.ID, rec.RootPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "file edit %s %s\npath: %s\n", edit.ID, edit.Status, edit.Path)
	return nil
}

func (a *App) editDeny(ctx context.Context, manager fileEditManager, args []string) error {
	fs := newFlagSet("edit deny", a.errOut)
	reason := fs.String("reason", "", "denial reason")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"reason": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent edit deny <edit-id> [--reason <reason>]")
	}
	edit, err := manager.Deny(ctx, fs.Arg(0), *reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "file edit %s %s\n", edit.ID, edit.Status)
	if strings.TrimSpace(edit.Reason) != "" {
		fmt.Fprintf(a.out, "reason: %s\n", edit.Reason)
	}
	return nil
}

func printFileEdit(out io.Writer, edit fileedit.Edit) {
	fmt.Fprintf(out, "id: %s\n", edit.ID)
	fmt.Fprintf(out, "status: %s\n", edit.Status)
	fmt.Fprintf(out, "workspace: %s\n", edit.WorkspaceID)
	fmt.Fprintf(out, "session: %s\n", edit.SessionID)
	fmt.Fprintf(out, "path: %s\n", edit.Path)
	fmt.Fprintf(out, "secrets_redacted: %t\n", edit.SecretsRedacted)
	if strings.TrimSpace(edit.Reason) != "" {
		fmt.Fprintf(out, "reason: %s\n", edit.Reason)
	}
	fmt.Fprintf(out, "created_at: %s\n", edit.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(out, "updated_at: %s\n", edit.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(out, "diff:\n%s", edit.Diff)
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(current *flag.Flag) {
		if current.Name == name {
			found = true
		}
	})
	return found
}

func readEditContent(path string) (string, error) {
	if path != strings.TrimSpace(path) {
		return "", errors.New("content file path cannot contain surrounding whitespace")
	}
	if path == "" {
		return "", errors.New("content file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("content file %s is a directory", path)
	}
	if info.Size() > fileedit.MaxContentBytes {
		return "", fmt.Errorf("content file exceeds %d bytes", fileedit.MaxContentBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("content file is not valid UTF-8 text")
	}
	return string(data), nil
}
