package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
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
	instructions := fs.String("instructions", "",
		"fix extra operator instructions as a Thread context pin for this invocation")
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
		"instructions":              true,
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
		return errors.New(`usage: cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--instructions <text>] [--timeout 30m] [--json]`)
	}
	if strings.TrimSpace(*workspaceName) == "" {
		return errors.New("exec requires --workspace <name>")
	}
	// Validate the fixed instructions before any Run exists: a late pin
	// failure would otherwise strand a created Run, and the operator gets the
	// same text contract (UTF-8, no NUL, 8192-rune cap, no redacted secrets)
	// the interactive Thread pin surface enforces.
	fixedInstructions, err := normalizeExecInstructions(*instructions)
	if err != nil {
		return err
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
	if fixedInstructions != "" {
		if err := pinExecInstructions(ctx, turns, domain.InitialThreadID(run.ID),
			fixedInstructions); err != nil {
			// A created Run with a failed pin would sit idle until its budget
			// expires. Cancel it on a bounded detached context so the
			// operator gets a terminal Run, and report the Run ID if even
			// the cancellation fails. The pin error stays primary.
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if _, cancelErr := application.NewRunService(a.store).Cancel(cancelCtx, run.ID); cancelErr != nil {
				err = fmt.Errorf("%w (run %s could not be cancelled: %v)", err, run.ID, cancelErr)
			}
			cancel()
			return err
		}
	}
	oneShot := application.NewOneShotService(a.store, turns)
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

// normalizeExecInstructions validates the exec --instructions value before any
// Run is created. An unset flag means no fixed instructions; a whitespace-only
// value is an operator mistake and fails loudly. Otherwise the text must
// satisfy the Thread context pin contract and carry no protected secret
// content, mirroring the interactive pin surface's storage gate.
func normalizeExecInstructions(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("exec --instructions requires nonempty text")
	}
	if err := domain.ValidateContextPinText(text); err != nil {
		return "", fmt.Errorf("exec --instructions: %w", err)
	}
	if text != redact.String(text) {
		return "", errors.New("exec --instructions: remove protected secrets before fixing them as context guidance")
	}
	return text, nil
}

// pinExecInstructions fixes the invocation's extra operator instructions as a
// Thread context pin before the first turn. The ordinary pin delivery — an
// integrity-checked, operator-confirmed fixed requirement selected as a
// protected section every turn — carries them for this Run and any successor
// the Thread creates, with the same authority ceiling as every other pin: the
// text never grants tools, permissions, or policy exceptions.
func pinExecInstructions(ctx context.Context, turns *application.ThreadTurnService,
	threadID, text string) error {
	state, err := turns.InspectContextPins(ctx, threadID)
	if err != nil {
		return fmt.Errorf("exec instructions pin inspect: %w", err)
	}
	if _, err := turns.ControlContextPin(ctx, domain.ThreadContextPinControl{
		Version:          domain.ThreadContextPinsVersion,
		ThreadID:         threadID,
		Action:           "add",
		ExpectedRevision: state.Revision,
		Text:             text,
		OperationKey:     idgen.New("cli-exec-pin"),
		RequestedBy:      "cli_exec_operator",
	}); err != nil {
		return fmt.Errorf("exec instructions pin: %w", err)
	}
	return nil
}
