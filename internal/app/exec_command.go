package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/headless"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

// execCommand implements `cyberagent exec`: one non-interactive prompt, one
// fresh Run, the ordinary Thread turn machinery executing the agent loop in
// this process, the final assistant reply on stdout. Approval waits, budget
// exhaustion, and failures surface as a non-zero exit with the run status;
// pre-arranged execution permissions are the operator's responsibility, and
// no flag here can widen them.
func (a *App) execCommand(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("exec", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name (required)")
	profile := fs.String("profile", string(domain.ProfileCode), "mission profile")
	route := fs.String("route", "", "model route; empty selects the profile's default route")
	maxTurns := fs.Int("max-turns", domain.DefaultBudget().MaxTurns, "maximum agent turns")
	maxTokens := fs.Int64("max-tokens", 0, "maximum model tokens; zero means unset")
	maxToolCalls := fs.Int64("max-tool-calls", domain.DefaultBudget().MaxToolCalls, "maximum tool calls; zero means unlimited")
	maxCostUSD := fs.Float64("max-cost-usd", 0, "maximum model cost in USD; zero means unset")
	timeout := fs.Duration("timeout", 0, "whole-execution deadline; zero means no deadline")
	stdin := fs.Bool("stdin", false, "read the prompt from stdin instead of the positional argument")
	jsonOutput := fs.Bool("json", false, "emit a structured JSON result")
	streamEvents := fs.Bool("stream", false, "stream run events to stdout in real time (JSON lines); reply goes to stderr")
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
		"stream":                    false,
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
		return errors.New(`usage: cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--timeout 30m] [--json]`)
	}
	if strings.TrimSpace(*workspaceName) == "" {
		return errors.New("exec requires --workspace <name>")
	}
	if *streamEvents && *jsonOutput {
		return errors.New("--stream and --json cannot be combined: --stream uses stdout for the event stream; --json uses stdout for the structured result")
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

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

	capabilities := cliExecutionPermissionCapabilities(*enablePermissionControl, *enableFullAccess)
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
	// The same foreground runtime contract the other in-process CLI surfaces
	// use (approval continuation, run wake): the invocation owns its adapters,
	// the Run's durable permission selection never changes, and the capability
	// ceiling comes from the Run's Ask/Auto/Full snapshot.
	releaseFull, err := a.activateCLIInvocationFull(ctx, run.ID, capabilities, *confirmFull)
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
	drydocks, err := a.newRunFileDrydockService(ctx, run.ID)
	if err != nil {
		return err
	}
	dependencies.Drydocks = drydocks
	if _, configured, err := a.store.GetConfiguredStandardCodePresetOperation(ctx, run.ID); err != nil {
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

	if *streamEvents {
		// Stream mode: events go to stdout as JSON lines in real time; the
		// final reply goes to stderr. The headless exporter runs on a
		// cancellable child context and is stopped after the turn ends — we
		// never force the Run into a terminal state to stop streaming, so
		// waiting_approval, paused, and failed outcomes all exit cleanly.
		streamCtx, streamCancel := context.WithCancel(ctx)
		defer streamCancel()
		exporter := headless.NewExporter(a.store)
		streamDone := make(chan error, 1)
		go func() {
			streamDone <- exporter.Export(streamCtx, a.out, headless.Request{
				RunID: run.ID, Follow: true, MaxEvents: headless.MaxEvents,
			})
		}()

		result, turnErr := oneShot.Run(ctx, application.OneShotRequest{
			Version:      domain.ThreadMessageProtocolVersion,
			ThreadID:     domain.InitialThreadID(run.ID),
			Content:      prompt,
			OperationKey: idgen.New("cli-exec"),
			RequestedBy:  "cli_exec_operator",
		})

		// Stop the stream after the turn ends — do not force the Run into a
		// terminal state. The exporter exits via ctx.Done() with a cancelled
		// end record.
		streamCancel()
		streamErr := <-streamDone

		// The turn result is the primary outcome; stream delivery errors are
		// secondary. Print the reply to stderr (stdout carries events).
		if result.Reply != "" {
			fmt.Fprintln(a.errOut, result.Reply)
		}
		if turnErr != nil {
			if streamErr != nil {
				return fmt.Errorf("%w (stream: %v)", turnErr, streamErr)
			}
			return turnErr
		}
		if streamErr != nil {
			return fmt.Errorf("event stream: %w", streamErr)
		}
		if result.StopReason != "turn_finish" {
			return fmt.Errorf("exec turn did not finish: stop_reason=%s turn_status=%s run_status=%s",
				result.StopReason, result.TurnStatus, result.RunStatus)
		}
		return nil
	}

	result, err := oneShot.Run(ctx, application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      prompt,
		OperationKey: idgen.New("cli-exec"),
		RequestedBy:  "cli_exec_operator",
	})
	if err != nil {
		return err
	}
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
	// The supervisor's turn_finish stop reason is the completion signal for a
	// one-shot task; waiting_approval, root_wait, and failures exit non-zero
	// with the reason and run status.
	if result.StopReason != "turn_finish" {
		return fmt.Errorf("exec turn did not finish: stop_reason=%s turn_status=%s run_status=%s",
			result.StopReason, result.TurnStatus, result.RunStatus)
	}
	return nil
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
