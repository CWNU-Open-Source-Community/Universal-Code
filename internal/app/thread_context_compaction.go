package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

func (a *App) threadCompact(ctx context.Context, args []string) error {
	fs := newFlagSet("thread compact", a.errOut)
	focus := fs.String("focus", "", "optional summary focus")
	key := fs.String("operation-key", "", "stable retry key (generated when omitted)")
	expectedRun := fs.String("expected-run", "", "expected current Run")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"focus": true, "operation-key": true, "expected-run": true, "json": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent thread compact <thread-id> [--focus <text>] [--operation-key <key>] [--expected-run <run-id>] [--json]")
	}
	thread, err := a.store.GetThread(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if strings.TrimSpace(*expectedRun) == "" {
		*expectedRun = thread.LastRunID
	}
	if strings.TrimSpace(*key) == "" {
		*key = idgen.New("thread-context")
		fmt.Fprintf(a.errOut, "context operation key: %s\n", *key)
	}
	result, err := application.NewThreadContextCompactionService(a.store, a.router).Compact(ctx, domain.ThreadContextCompactionRequest{
		Version: domain.ThreadContextCompactionVersion, ThreadID: thread.ID, ExpectedRunID: *expectedRun, Focus: *focus, OperationKey: *key, RequestedBy: "cli_thread_operator"})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeContextJSON(a, result)
	}
	fmt.Fprintf(a.out, "thread: %s\nrun: %s\nsession: %s\nstatus: %s\nsummary_id: %d\nremoved_messages: %d\npreserved_messages: %d\ninput_messages_omitted: %d\nreplayed: %t\n", result.ThreadID, result.RunID, result.SessionID, result.Status, result.SummaryID, result.RemovedMessages, result.PreservedMessages, result.InputMessagesOmitted, result.Replayed)
	return nil
}
