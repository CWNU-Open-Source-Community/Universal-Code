package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

// execCommand implements `cyberagent exec`: one non-interactive prompt
// executed through the ordinary Thread turn machinery, the final assistant
// reply on stdout. With --continue, the prompt continues an existing Thread:
// the turn machinery delivers it as a follow-up turn on the Thread's still
// running Run, or creates a successor Run inheriting workspace, permission,
// and budget when the previous one is terminal. Process-local runtime
// capabilities come from the operator's fresh permission flags in both
// modes; the Run's durable Ask/Auto/Full selection still governs every
// dispatch, and no flag here can widen it. Approval waits, budget
// exhaustion, and failures surface as a non-zero exit with the run status.
func (a *App) execCommand(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("exec", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name (required for fresh runs)")
	profile := fs.String("profile", string(domain.ProfileCode), "mission profile")
	route := fs.String("route", "", "model route; empty selects the profile's default route")
	maxTurns := fs.Int("max-turns", domain.DefaultBudget().MaxTurns, "maximum agent turns")
	maxTokens := fs.Int64("max-tokens", 0, "maximum model tokens; zero means unset")
	maxToolCalls := fs.Int64("max-tool-calls", domain.DefaultBudget().MaxToolCalls, "maximum tool calls; zero means unlimited")
	maxCostUSD := fs.Float64("max-cost-usd", 0, "maximum model cost in USD; zero means unset")
	timeout := fs.Duration("timeout", 0, "whole-execution deadline; zero means no deadline")
	stdin := fs.Bool("stdin", false, "read the prompt from stdin instead of the positional argument")
	continueThread := fs.String("continue", "", "continue an existing Thread by id; the Thread carries its own Run, workspace, profile, and permission selection")
	jsonOutput := fs.Bool("json", false, "emit a structured JSON result")
	enablePermissionControl := fs.Bool("enable-permission-control", false,
		"enable the operator-approval runtime for this process")
	enableFullAccess := fs.Bool("enable-danger-full-access", false,
		"enable the full-access runtime for this process")
	confirmFull := fs.Bool("confirm-full", false,
		"activate the Run's existing Full preference for this process")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "profile": true, "route": true,
		"max-turns": true, "max-tokens": true, "max-tool-calls": true,
		"max-cost-usd": true, "timeout": true, "stdin": false, "json": false,
		"continue":                  true,
		"enable-permission-control": false, "enable-danger-full-access": false,
		"confirm-full": false,
	})); err != nil {
		return err
	}
	prompt := strings.Join(fs.Args(), " ")
	if *stdin {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read prompt from stdin: %w", err)
		}
		prompt = string(raw)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New(`usage: cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--timeout 30m] [--json] [--continue <thread-id>]`)
	}
	continueID := strings.TrimSpace(*continueThread)
	if continueID == "" && strings.TrimSpace(*workspaceName) == "" {
		return errors.New("exec requires --workspace <name> (or --continue <thread-id>)")
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	var threadID, runID string
	var err error

	if continueID != "" {
		// Continue mode: resolve the Thread's latest Run. Process-local
		// runtime capabilities come from the operator's fresh flags exactly
		// as in fresh mode — the flags enable this process's adapters and
		// never widen the Run's durable permission selection, which still
		// governs every dispatch. The turn machinery creates a successor Run
		// when the previous one is terminal (inheriting workspace,
		// permission, and budget) or delivers the message as a follow-up
		// turn on the still running Run.
		threadID, runID, err = a.resolveContinueThread(ctx, continueID)
		if err != nil {
			return err
		}
	} else {
		selectedProfile, err := domain.ParseProfile(*profile)
		if err != nil {
			return err
		}
		workspaceRecord, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
		if err != nil {
			return err
		}
		budget := domain.Budget{MaxTurns: *maxTurns, MaxToolCalls: *maxToolCalls}
		effective, rejections, err := projectconfig.ResolveWorkspace(ctx, workspaceRecord.RootPath,
			selectedProfile, budget, toolgateway.TypedActionIDs())
		if err != nil {
			return fmt.Errorf("project config fail-closed: %w", err)
		}
		if len(rejections) != 0 {
			return fmt.Errorf("project config rejection: field=%s reason=%s",
				rejections[0].Field, rejections[0].Reason)
		}
		discovered, err := projectconfig.DiscoverInstructions(ctx, workspaceRecord.RootPath, ".")
		if err != nil {
			return fmt.Errorf("project instruction discovery fail-closed: %w", err)
		}
		_, run, err := application.NewRunService(a.store).
			WithLifecycleHooks(a.newLifecycleHookEngine()).Create(ctx, application.CreateRunRequest{
			Goal:        clipExecGoal(prompt),
			Profile:     *profile,
			WorkspaceID: workspaceRecord.ID,
			ModelRoute:  *route,
			Interactive: true,
			Budget: domain.Budget{MaxTurns: *maxTurns, MaxTokens: *maxTokens,
				MaxToolCalls: *maxToolCalls, MaxCostUSD: *maxCostUSD,
				TimeoutSeconds: int64(timeout.Seconds())},
			ProjectConfig:       effective,
			ProjectInstructions: &discovered,
			RequestedBy:         "cli_exec_operator",
		})
		if err != nil {
			return err
		}
		runID = run.ID
		threadID = domain.InitialThreadID(run.ID)
	}

	// The same foreground runtime contract the other in-process CLI surfaces
	// use (approval continuation, run wake): the invocation owns its adapters,
	// the Run's durable permission selection never changes, and the capability
	// ceiling comes from the Run's Ask/Auto/Full snapshot. The operator's
	// fresh flags enable this process's adapters in both modes; they never
	// widen the durable selection.
	capabilities := cliExecutionPermissionCapabilities(*enablePermissionControl, *enableFullAccess)
	releaseFull, err := a.activateCLIInvocationFull(ctx, runID, capabilities, *confirmFull)
	if err != nil {
		return err
	}
	runtime, err := a.newCLIExecutionRuntime(ctx, capabilities, true)
	if err != nil {
		releaseFull()
		return err
	}
	defer func() {
		releaseFull()
		_ = runtime.close()
	}()
	dependencies := runtime.dependencies(a)
	drydocks, err := a.newRunFileDrydockService(ctx, runID)
	if err != nil {
		return err
	}
	dependencies.Drydocks = drydocks
	if _, configured, err := a.store.GetConfiguredStandardCodePresetOperation(ctx, runID); err != nil {
		return err
	} else if configured {
		delivery, err := application.NewStandardCodeDeliveryService(a.store, drydocks)
		if err != nil {
			return err
		}
		dependencies.StandardCodeDelivery = delivery
	}
	turns := application.NewThreadTurnService(a.store,
		application.NewRunLifecycleControlService(a.store).WithLifecycleHooks(a.newLifecycleHookEngine()),
		application.NewRunExecutionHandoffWithRuntime(a.store, a.router, a.checker, dependencies))
	oneShot := application.NewOneShotService(a.store, turns)
	result, err := oneShot.Run(ctx, application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     threadID,
		Content:      prompt,
		OperationKey: idgen.New("cli-exec"),
		RequestedBy:  "cli_exec_operator",
	})
	if err != nil {
		return err
	}
	// Both output modes emit the structured result / reply to stdout first,
	// then the exit code reflects the outcome: the supervisor's turn_finish
	// stop reason is the completion signal for a one-shot task;
	// waiting_approval, root_wait, and failures exit non-zero with the reason
	// and run status.
	if *jsonOutput {
		encoder := json.NewEncoder(a.out)
		if err := encoder.Encode(map[string]any{"run_id": result.RunID,
			"thread_id": result.ThreadID, "status": result.RunStatus,
			"stop_reason": result.StopReason, "turn_status": result.TurnStatus,
			"reply": result.Reply}); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(a.out, result.Reply)
	}
	if result.StopReason != "turn_finish" {
		return fmt.Errorf("exec turn did not finish: stop_reason=%s turn_status=%s run_status=%s",
			result.StopReason, result.TurnStatus, result.RunStatus)
	}
	return nil
}

// resolveContinueThread looks up an existing Thread and its latest Run for
// --continue mode. It returns identities only: the process-local runtime
// capabilities come from the operator's fresh flags exactly as in fresh
// mode — the flags enable this process's adapters and never widen the Run's
// durable permission selection, which still governs every dispatch.
func (a *App) resolveContinueThread(ctx context.Context,
	threadID string,
) (string, string, error) {
	thread, err := a.store.GetThread(ctx, strings.TrimSpace(threadID))
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeNotFound ||
			errors.Is(err, sql.ErrNoRows) {
			return "", "", apperror.New(apperror.CodeNotFound,
				fmt.Sprintf("continue thread %s: not found", strings.TrimSpace(threadID)))
		}
		return "", "", fmt.Errorf("continue thread: %w", err)
	}
	runLookupID := thread.ActiveRunID
	if runLookupID == "" {
		runLookupID = thread.LastRunID
	}
	run, err := a.store.GetRun(ctx, runLookupID)
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeNotFound ||
			errors.Is(err, sql.ErrNoRows) {
			return "", "", apperror.New(apperror.CodeNotFound,
				fmt.Sprintf("continue thread %s: no Run to continue", thread.ID))
		}
		return "", "", fmt.Errorf("continue thread Run lookup: %w", err)
	}
	return thread.ID, run.ID, nil
}

// clipExecGoal clips the prompt to the run-creation goal limit without
// splitting a multi-byte character, so the goal survives every downstream
// UTF-8 sensitive projection while the thread message carries the full text.
func clipExecGoal(prompt string) string {
	if len(prompt) <= domain.MaxRunCreationGoalBytes {
		return prompt
	}
	clipped := prompt[:domain.MaxRunCreationGoalBytes]
	for len(clipped) > 0 && !utf8.ValidString(clipped) {
		clipped = clipped[:len(clipped)-1]
	}
	return clipped
}
