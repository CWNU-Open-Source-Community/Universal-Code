package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileattachment"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
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
	// SIGINT (and SIGTERM where the platform delivers it) becomes a clean
	// cancellation instead of a hard process kill: the turn machinery records
	// the durable handoff failure through context.WithoutCancel, the Run stays
	// recoverable for follow-up thread sends, and exec exits non-zero
	// (cancelled → exit 7; a --timeout deadline → exit 9).
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
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
	var attachPaths repeatedString
	fs.Var(&attachPaths, "attach", "attach a file to the prompt; repeatable, at most 4, each at most 5 MiB")
	var imagePaths repeatedString
	fs.Var(&imagePaths, "image", "attach an image to the prompt; repeatable, at most 4, PNG/JPEG/WebP only")
	jsonOutput := fs.Bool("json", false, "emit a structured JSON result")
	instructions := fs.String("instructions", "",
		"fix extra operator instructions as a Thread context pin for this invocation")
	outputLastMessage := fs.String("output-last-message", "",
		"write the final assistant reply to this file; empty disables it")
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
		"output-last-message":       true,
		"continue":                  true,
		"attach":                    true,
		"image":                     true,
		"enable-permission-control": false, "enable-danger-full-access": false,
		"confirm-full": false,
	})); err != nil {
		return err
	}
	if *outputLastMessage != strings.TrimSpace(*outputLastMessage) {
		return errors.New("exec --output-last-message path cannot contain surrounding whitespace")
	}
	prompt := strings.Join(fs.Args(), " ")
	if *stdin {
		// Cancellation must unblock an idle stdin read: Ctrl-C while waiting
		// for piped input should exit with the cancelled code instead of
		// hanging until input arrives — io.ReadAll alone never observes the
		// context. The abandoned reader goroutine may stay blocked on EOF;
		// the process is exiting anyway.
		type stdinRead struct {
			data []byte
			err  error
		}
		read := make(chan stdinRead, 1)
		go func(input *os.File) {
			raw, readErr := io.ReadAll(input)
			read <- stdinRead{data: raw, err: readErr}
		}(os.Stdin)
		select {
		case result := <-read:
			if result.err != nil {
				return fmt.Errorf("read prompt from stdin: %w", result.err)
			}
			prompt = string(result.data)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New(`usage: cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--instructions <text>] [--timeout 30m] [--json] [--continue <thread-id>]`)
	}
	continueID := strings.TrimSpace(*continueThread)
	if continueID == "" && strings.TrimSpace(*workspaceName) == "" {
		return errors.New("exec requires --workspace <name> (or --continue <thread-id>)")
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

	var threadID, runID, workspaceID string
	var attachmentRefs []domain.FileAttachmentReference
	var imageRefs []domain.ImageReference

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
		workspaceID, err = a.resolveRunWorkspaceID(ctx, runID)
		if err != nil {
			return err
		}
		// Import the requested files before anything else runs, so a
		// rejected attachment fails before the turn machinery starts. The
		// turn machinery pins the references to the message and projects the
		// evidence (readable text excerpts, or the saved original bytes for
		// the command runtime's fixed attachments directory) with no new
		// authority.
		if len(imagePaths) > 0 {
			run, runErr := a.store.GetRun(ctx, runID)
			if runErr != nil {
				return fmt.Errorf("continue Run lookup: %w", runErr)
			}
			if err := a.checkExecVisionCapability(ctx, run.Config.ModelRoute); err != nil {
				return err
			}
		}
		attachmentRefs, err = a.importExecAttachments(ctx, workspaceID, attachPaths)
		if err != nil {
			return err
		}
		imageRefs, err = a.importExecImages(ctx, workspaceID, imagePaths)
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
		workspaceID = workspaceRecord.ID
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
		// Import before Run creation: a rejected attachment then fails with
		// no Run, Mission, or Session to clean up. The turn machinery pins
		// the references to the message and projects the evidence (readable
		// text excerpts, or the saved original bytes for the command
		// runtime's fixed attachments directory) with no new authority.
		if len(imagePaths) > 0 {
			if err := a.checkExecVisionCapability(ctx, *route); err != nil {
				return err
			}
		}
		attachmentRefs, err = a.importExecAttachments(ctx, workspaceID, attachPaths)
		if err != nil {
			return err
		}
		imageRefs, err = a.importExecImages(ctx, workspaceID, imagePaths)
		if err != nil {
			return err
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
	releaseFull, err := a.activateExecInvocationFull(ctx, threadID, runID, capabilities, *confirmFull, continueID != "")
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
	turns := application.NewThreadTurnServiceWithExecutionCapabilities(a.store,
		application.NewRunLifecycleControlService(a.store).WithLifecycleHooks(a.newLifecycleHookEngine()),
		application.NewRunExecutionHandoffWithRuntime(a.store, a.router, a.checker, dependencies), capabilities)
	if fixedInstructions != "" {
		if err := pinExecInstructions(ctx, turns, threadID,
			fixedInstructions); err != nil {
			// A created Run with a failed pin would sit idle until its budget
			// expires. Cancel it on a bounded detached context so the
			// operator gets a terminal Run, and report the Run ID if even
			// the cancellation fails. The pin error stays primary.
			if continueID == "" {
				cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if _, cancelErr := application.NewRunService(a.store).Cancel(cancelCtx, runID); cancelErr != nil {
					err = fmt.Errorf("%w (run %s could not be cancelled: %v)", err, runID, cancelErr)
				}
				cancel()
			}
			return err
		}
	}
	oneShot := application.NewOneShotService(a.store, turns)
	result, err := oneShot.Run(ctx, application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     threadID,
		Content:      prompt,
		OperationKey: idgen.New("cli-exec"),
		RequestedBy:  "cli_exec_operator",
		Attachments:  attachmentRefs,
		Images:       imageRefs,
	})
	// The last-message file is written before the standard output, so a
	// failed write fails the command instead of leaving a stale file beside
	// a success exit code. The reply may be empty when the turn paused
	// before producing one; the file then records that emptiness.
	if target := *outputLastMessage; target != "" {
		if err := os.WriteFile(target, []byte(result.Reply), 0o600); err != nil {
			return fmt.Errorf("write --output-last-message %s: %w", target, err)
		}
	}
	if *jsonOutput {
		// A failed handoff can transition the Run after the result snapshot
		// was taken; report the current durable status. A lookup failure
		// keeps the snapshot and never masks the turn error below.
		if run, runErr := a.store.GetRun(ctx, result.RunID); runErr == nil {
			result.RunStatus = string(run.Status)
		}
		encoder := json.NewEncoder(a.out)
		if encodeErr := encoder.Encode(map[string]any{"run_id": result.RunID,
			"thread_id": result.ThreadID, "status": result.RunStatus,
			"stop_reason": result.StopReason, "turn_status": result.TurnStatus,
			"reply": result.Reply,
			"usage": map[string]any{
				"input_tokens":  result.InputTokens,
				"output_tokens": result.OutputTokens,
				"total_tokens":  result.TotalTokens,
				"duration_ms":   result.DurationMs,
				"turns":         result.TurnCount,
			}}); encodeErr != nil {
			return encodeErr
		}
	} else if result.Reply != "" {
		fmt.Fprintln(a.out, result.Reply)
	}
	if err != nil {
		return err
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
	if thread.Status != domain.ThreadActive {
		return "", "", apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("continue thread %s: status is %s; only active threads can be continued",
				thread.ID, thread.Status))
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

// importExecAttachments reads each requested file and imports it into the
// Run's workspace as an immutable attachment before the turn starts. The
// store applies the same contract as every upload surface (5 MiB cap,
// bounded filename, MIME validation, whole-token redaction of readable
// text); each invocation creates its own attachment records, and the
// returned references carry no authority: attachment content is untrusted
// evidence, never instructions.
func (a *App) importExecAttachments(ctx context.Context,
	workspaceID string, paths []string,
) ([]domain.FileAttachmentReference, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > domain.MaxThreadMessageAttachments {
		return nil, fmt.Errorf("exec supports at most %d attachments, got %d",
			domain.MaxThreadMessageAttachments, len(paths))
	}
	service := application.NewFileAttachmentService(a.store)
	references := make([]domain.FileAttachmentReference, 0, len(paths))
	for index, rawPath := range paths {
		path := rawPath
		if path == "" {
			return nil, fmt.Errorf("exec --attach requires a file path (empty value at position %d)", index+1)
		}
		data, err := readExecInputFile(path, fileattachment.MaxBytes)
		if err != nil {
			return nil, fmt.Errorf("exec --attach %s: %w", path, err)
		}
		name := filepath.Base(path)
		mediaType := ""
		if declared := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); declared != "" {
			// The store's validation rejects media types with parameters, and
			// the Windows MIME registry commonly appends "; charset=utf-8".
			if parsed, _, parseErr := mime.ParseMediaType(declared); parseErr == nil {
				mediaType = parsed
			}
		}
		attachment, err := service.Import(ctx, application.FileAttachmentImportRequest{
			WorkspaceID:  workspaceID,
			OperationKey: idgen.New("cli-exec-attach"),
			Name:         name,
			MIMEType:     mediaType,
			Data:         data,
		})
		if err != nil {
			return nil, fmt.Errorf("exec --attach %s: %w", path, err)
		}
		references = append(references, domain.FileAttachmentReference{
			ID: attachment.ID, WorkspaceID: attachment.WorkspaceID,
			SHA256: attachment.SHA256, ByteSize: attachment.ByteSize,
		})
	}
	return references, nil
}

// importExecImages reads each requested image and imports it into the Run's
// workspace before the turn starts. Only the store's published operator
// upload formats are accepted (PNG, JPEG, WebP); the store decodes and
// bounds the image (5 MiB, 8192px, 16 Mpx). The references are
// workspace-scoped observations with no authority: the turn machinery
// delivers image bytes only to models with an established vision
// capability, and the evidence projection marks them as untrusted data.
func (a *App) importExecImages(ctx context.Context,
	workspaceID string, paths []string,
) ([]domain.ImageReference, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > domain.MaxThreadMessageImages {
		return nil, fmt.Errorf("exec supports at most %d images, got %d",
			domain.MaxThreadMessageImages, len(paths))
	}
	references := make([]domain.ImageReference, 0, len(paths))
	for index, rawPath := range paths {
		path := rawPath
		if path == "" {
			return nil, fmt.Errorf("exec --image requires a file path (empty value at position %d)", index+1)
		}
		data, err := readExecInputFile(path, imageattachment.MaxBytes)
		if err != nil {
			return nil, fmt.Errorf("exec --image %s: %w", path, err)
		}
		mediaType := imageMimeTypeForPath(path)
		if mediaType == "" {
			return nil, fmt.Errorf("exec --image %s: unsupported format; use PNG, JPEG, or WebP", path)
		}
		image, err := a.store.SaveWorkspaceImage(ctx, workspaceID,
			idgen.New("cli-exec-image"), mediaType, filepath.Base(path), data)
		if err != nil {
			return nil, fmt.Errorf("exec --image %s: %w", path, err)
		}
		references = append(references, domain.ImageReference{
			ID: image.ID, SHA256: image.SHA256,
		})
	}
	return references, nil
}

// imageMimeTypeForPath maps a file extension to one of the operator image
// upload formats; unknown extensions return empty and fail with a clear
// message instead of relying on content sniffing.
func imageMimeTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

// checkExecVisionCapability verifies that the model referenced by route
// supports image input before images are imported into the workspace. The
// check prevents orphaned images: without it, a non-vision model would
// cause the turn to fail after the images were already stored.
func (a *App) checkExecVisionCapability(ctx context.Context, route string) error {
	ref := a.router.Resolve(route)
	vision := a.router.DescribeVision(ref)
	if vision.State != llm.VisionSupported {
		return fmt.Errorf("exec --image: selected model image capability is %s; select a confirmed vision model before sending images", vision.State)
	}
	return nil
}

// resolveRunWorkspaceID resolves the workspace a Run belongs to, so continue
// mode can import attachments into the same workspace scope the Thread's
// runs already use.
func (a *App) resolveRunWorkspaceID(ctx context.Context, runID string) (string, error) {
	run, err := a.store.GetRun(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("continue Run lookup: %w", err)
	}
	mission, err := a.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return "", fmt.Errorf("continue mission lookup: %w", err)
	}
	return mission.WorkspaceID, nil
}
