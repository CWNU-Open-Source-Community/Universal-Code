package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func (a *App) todoCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("todo subcommand is required")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	service := application.NewWorkItemService(a.store)
	switch args[0] {
	case "create":
		return a.todoCreate(ctx, service, args[1:])
	case "list":
		return a.todoList(ctx, service, args[1:])
	case "show":
		return a.todoShow(ctx, service, args[1:])
	case "update":
		return a.todoUpdate(ctx, service, args[1:])
	case "start":
		return a.todoTransition(ctx, service, domain.WorkItemInProgress, args[1:])
	case "block":
		return a.todoTransition(ctx, service, domain.WorkItemBlocked, args[1:])
	case "reopen", "unblock":
		return a.todoTransition(ctx, service, domain.WorkItemPending, args[1:])
	case "complete":
		return a.todoTransition(ctx, service, domain.WorkItemCompleted, args[1:])
	case "cancel":
		return a.todoTransition(ctx, service, domain.WorkItemCancelled, args[1:])
	default:
		return fmt.Errorf("unknown todo subcommand %q", args[0])
	}
}

func (a *App) todoCreate(ctx context.Context, service *application.WorkItemService, args []string) error {
	fs := newFlagSet("todo create", a.errOut)
	description := fs.String("description", "", "plan item description")
	priority := fs.String("priority", string(domain.WorkItemPriorityNormal), "low, normal, high, or critical")
	owner := fs.String("owner", "", "plan item owner")
	ownerAgent := fs.String("owner-agent", "", "same-Run Agent id that owns the plan item")
	var acceptance stringListFlag
	var dependencies stringListFlag
	fs.Var(&acceptance, "acceptance", "acceptance criterion; repeat for multiple values")
	fs.Var(&dependencies, "depends-on", "dependency plan item id; repeat for multiple values")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"description": true, "priority": true, "owner": true, "owner-agent": true,
		"acceptance": true, "depends-on": true,
	})); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New(`usage: cyberagent todo create <run-id> "title" [--priority normal] [--depends-on <work-id>]`)
	}
	item, err := service.Create(ctx, application.CreateWorkItemRequest{
		RunID: fs.Arg(0), Title: strings.Join(fs.Args()[1:], " "), Description: *description,
		Priority: *priority, Owner: *owner, OwnerAgentID: *ownerAgent,
		AcceptanceCriteria: acceptance.values, Dependencies: dependencies.values,
	})
	if err != nil {
		return err
	}
	printWorkItemSummary(a.out, "created", item)
	return nil
}

func (a *App) todoList(ctx context.Context, service *application.WorkItemService, args []string) error {
	fs := newFlagSet("todo list", a.errOut)
	statusValue := fs.String("status", "", "comma-separated plan item statuses")
	owner := fs.String("owner", "", "exact owner filter")
	ownerAgent := fs.String("owner-agent", "", "exact owner Agent id filter")
	limit := fs.Int("limit", 100, "maximum rows")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"status": true, "owner": true, "owner-agent": true, "limit": true,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent todo list <run-id> [--status pending,blocked] [--owner <owner>] [--limit <n>]")
	}
	if *limit <= 0 || *limit > 500 {
		return errors.New("plan item list limit must be between 1 and 500")
	}
	statuses, err := parseWorkItemStatuses(*statusValue)
	if err != nil {
		return err
	}
	items, err := service.List(ctx, domain.WorkItemFilter{
		RunID: fs.Arg(0), Statuses: statuses, Owner: *owner, OwnerAgentID: *ownerAgent, Limit: *limit,
	})
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(a.out, "no plan items")
		return nil
	}
	for _, item := range items {
		ownerValue := item.Owner
		if ownerValue == "" {
			ownerValue = item.OwnerAgentID
		}
		if ownerValue == "" {
			ownerValue = "-"
		}
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\tv%d\tdeps=%d\t%s\n",
			item.ID, item.Status, item.Priority, ownerValue, item.Version, len(item.Dependencies), item.Title)
	}
	return nil
}

func (a *App) todoShow(ctx context.Context, service *application.WorkItemService, args []string) error {
	fs := newFlagSet("todo show", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent todo show <work-id>")
	}
	item, err := service.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "id: %s\nrun: %s\ntitle: %s\nstatus: %s\npriority: %s\nowner: %s\nowner_agent: %s\nversion: %d\ndescription: %s\n",
		item.ID, item.RunID, item.Title, item.Status, item.Priority, item.Owner, item.OwnerAgentID,
		item.Version, item.Description)
	printStringList(a.out, "acceptance", item.AcceptanceCriteria)
	printStringList(a.out, "dependencies", item.Dependencies)
	if item.BlockedReason != "" {
		fmt.Fprintf(a.out, "blocked_reason: %s\n", item.BlockedReason)
	}
	fmt.Fprintf(a.out, "created_at: %s\nupdated_at: %s\n", item.CreatedAt.Format(time.RFC3339), item.UpdatedAt.Format(time.RFC3339))
	if item.CompletedAt != nil {
		fmt.Fprintf(a.out, "completed_at: %s\n", item.CompletedAt.Format(time.RFC3339))
	}
	return nil
}

func (a *App) todoUpdate(ctx context.Context, service *application.WorkItemService, args []string) error {
	fs := newFlagSet("todo update", a.errOut)
	title := fs.String("title", "", "replacement title")
	description := fs.String("description", "", "replacement description")
	priority := fs.String("priority", "", "replacement priority")
	owner := fs.String("owner", "", "replacement owner; empty clears it")
	ownerAgent := fs.String("owner-agent", "", "replacement owner Agent id; empty clears it")
	version := fs.Int64("version", 0, "expected version; zero uses the current version")
	clearAcceptance := fs.Bool("clear-acceptance", false, "clear acceptance criteria")
	clearDependencies := fs.Bool("clear-dependencies", false, "clear dependencies")
	var acceptance stringListFlag
	var dependencies stringListFlag
	fs.Var(&acceptance, "acceptance", "replacement acceptance criterion; repeat for multiple values")
	fs.Var(&dependencies, "depends-on", "replacement dependency id; repeat for multiple values")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"title": true, "description": true, "priority": true, "owner": true, "owner-agent": true,
		"version":    true,
		"acceptance": true, "depends-on": true, "clear-acceptance": false, "clear-dependencies": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent todo update <work-id> [--title <text>] [--priority <value>] [--version <n>]")
	}
	visited := visitedFlags(fs)
	req := application.UpdateWorkItemRequest{ID: fs.Arg(0), ExpectedVersion: *version}
	if visited["title"] {
		req.Title = title
	}
	if visited["description"] {
		req.Description = description
	}
	if visited["priority"] {
		req.Priority = priority
	}
	if visited["owner"] {
		req.Owner = owner
	}
	if visited["owner-agent"] {
		req.OwnerAgentID = ownerAgent
	}
	if acceptance.set || *clearAcceptance {
		values := acceptance.values
		if *clearAcceptance {
			values = []string{}
		}
		req.AcceptanceCriteria = &values
	}
	if dependencies.set || *clearDependencies {
		values := dependencies.values
		if *clearDependencies {
			values = []string{}
		}
		req.Dependencies = &values
	}
	item, err := service.Update(ctx, req)
	if err != nil {
		return err
	}
	printWorkItemSummary(a.out, "updated", item)
	return nil
}

func (a *App) todoTransition(ctx context.Context, service *application.WorkItemService, target domain.WorkItemStatus, args []string) error {
	// Usage text and flag-set names must reference the CLI subcommand the
	// operator actually typed: the domain status names (completed, cancelled,
	// blocked) are not dispatchable subcommands, and following the old text
	// landed on "unknown todo subcommand".
	name := string(target)
	switch target {
	case domain.WorkItemInProgress:
		name = "start"
	case domain.WorkItemPending:
		name = "reopen"
	case domain.WorkItemCompleted:
		name = "complete"
	case domain.WorkItemCancelled:
		name = "cancel"
	case domain.WorkItemBlocked:
		name = "block"
	}
	fs := newFlagSet("todo "+name, a.errOut)
	version := fs.Int64("version", 0, "expected version; zero uses the current version")
	reason := fs.String("reason", "", "blocking reason")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"version": true, "reason": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cyberagent todo %s <work-id> [--version <n>]", name)
	}
	if target == domain.WorkItemBlocked && strings.TrimSpace(*reason) == "" {
		return errors.New("plan item block reason is required")
	}
	item, err := service.Transition(ctx, fs.Arg(0), *version, target, *reason)
	if err != nil {
		return err
	}
	printWorkItemSummary(a.out, name, item)
	return nil
}

type stringListFlag struct {
	values []string
	set    bool
}

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(f.values, ",")
}

func (f *stringListFlag) Set(value string) error {
	if f == nil {
		return errors.New("string list flag is nil")
	}
	f.set = true
	value = strings.TrimSpace(value)
	if value != "" {
		f.values = append(f.values, value)
	}
	return nil
}

func parseWorkItemStatuses(value string) ([]domain.WorkItemStatus, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	statuses := make([]domain.WorkItemStatus, 0, len(parts))
	for _, part := range parts {
		status, err := domain.ParseWorkItemStatus(part)
		if err != nil {
			return nil, fmt.Errorf("invalid plan item status %q", strings.TrimSpace(part))
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	return visited
}

func printWorkItemSummary(out interface {
	Write([]byte) (int, error)
}, action string, item domain.WorkItem) {
	fmt.Fprintf(out, "plan item %s %s\nrun: %s\nstatus: %s\npriority: %s\nversion: %d\n",
		item.ID, action, item.RunID, item.Status, item.Priority, item.Version)
}

func printStringList(out interface {
	Write([]byte) (int, error)
}, label string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(out, "%s: -\n", label)
		return
	}
	for index, value := range values {
		fmt.Fprintf(out, "%s[%s]: %s\n", label, strconv.Itoa(index+1), value)
	}
}
