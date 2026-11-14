package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/repository"
)

const githubReviewCLIUsage = "usage: cyberagent github-review configure|credential|login|qualify|fetch|evidence|status|write|recover --enable-github-review --enable-permission-control [flags]"

func (a *App) githubReviewCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(githubReviewCLIUsage)
	}
	action := strings.TrimSpace(args[0])
	if action == "credential" {
		return a.githubReviewCredentialCommand(ctx, args[1:])
	}
	fs := newFlagSet("github-review "+action, a.errOut)
	enable := fs.Bool("enable-github-review", false,
		"enable the process-local GitHub Review Provider")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable exact operator approval checks")
	dangerFullAccess := fs.Bool("enable-danger-full-access", false,
		"allow an existing full-access Run permission")
	managedRoot := fs.String("managed-root", "", "product-managed Git worktree root")
	connectionID := fs.String("connection", "", "GitHub review connection identity")
	repositoryName := fs.String("repository", "", "exact GitHub owner/name")
	credentialName := fs.String("credential", "", "OS credential-store reference")
	authKind := fs.String("auth", string(githubreview.AuthGitHubAppDevice),
		"github_app_device, oauth_user, or fine_grained_pat")
	clientID := fs.String("client-id", "", "GitHub App public client ID")
	expectedGeneration := fs.Int64("expected-generation", 0,
		"connection compare-and-swap generation")
	enabled := fs.Bool("enabled", true, "enable the configured connection")
	allowWrite := fs.Bool("allow-write", false,
		"explicitly allow approval-gated GitHub review write-back for this connection")
	pullRequest := fs.Int64("pr", 0, "exact pull request number")
	runID := fs.String("run", "", "exact Code Surface Run identity")
	snapshotID := fs.String("snapshot", "", "persisted GitHub review snapshot identity")
	operationKey := fs.String("operation-key", "", "stable write idempotency key")
	targetID := fs.String("target", "", "review thread node identity")
	body := fs.String("body", "", "review body (prefer --body-file)")
	bodyFile := fs.String("body-file", "", "UTF-8 review body file")
	reviewEvent := fs.String("event", "", "COMMENT, APPROVE, or REQUEST_CHANGES")
	changeSummary := fs.String("change-summary", "", "bounded local change summary")
	validationSummary := fs.String("validation-summary", "", "bounded validation summary")
	confirmFull := fs.Bool("confirm-full", false,
		"activate the current Full preference only for this invocation")
	confirm := fs.Bool("confirm", false, "confirm the exact local or remote mutation")
	limit := fs.Int("limit", 100, "maximum audit records")
	loginTimeout := fs.Duration("timeout", 15*time.Minute, "device authorization deadline")
	var logHosts, reviewers multiStringFlag
	fs.Var(&logHosts, "log-host", "exact signed Actions log host (repeatable)")
	fs.Var(&reviewers, "reviewer", "reviewer login (repeatable)")
	values := map[string]bool{
		"enable-github-review": false, "enable-permission-control": false,
		"enable-danger-full-access": false, "managed-root": true, "connection": true, "repository": true,
		"credential": true, "auth": true, "client-id": true,
		"expected-generation": true, "enabled": true, "allow-write": false,
		"pr": true, "run": true,
		"snapshot": true, "operation-key": true, "target": true, "body": true,
		"body-file": true, "event": true, "change-summary": true,
		"validation-summary": true, "reviewer": true, "log-host": true,
		"confirm": false, "confirm-full": false, "limit": true, "timeout": true,
	}
	flagArgs := args[1:]
	var writeOperation githubreview.WriteOperation
	if action == "write" {
		if len(args) < 2 {
			return errors.New(githubReviewCLIUsage)
		}
		writeOperation = githubreview.WriteOperation(strings.TrimSpace(args[1]))
		flagArgs = args[2:]
		if !writeOperation.Valid() {
			return apperror.New(apperror.CodeInvalidArgument,
				"GitHub review write is not in the closed github-review-provider.v1 schema")
		}
	}
	if err := fs.Parse(reorderFlags(flagArgs, values)); err != nil {
		return err
	}
	if fs.NArg() != 0 || !*enable || !*permissionControl ||
		*confirmFull && (action != "write" || !*confirm) {
		return errors.New(githubReviewCLIUsage)
	}
	capabilities := cliExecutionPermissionCapabilities(*permissionControl, *dangerFullAccess)
	service, err := a.newGitHubReviewService(*managedRoot, capabilities)
	if err != nil {
		return err
	}
	switch action {
	case "configure":
		if !*confirm || strings.TrimSpace(*repositoryName) == "" ||
			strings.TrimSpace(*credentialName) == "" {
			return errors.New("usage: cyberagent github-review configure --repository <owner/name> --credential <name> [--auth github_app_device --client-id <id>] --enable-github-review --enable-permission-control --confirm")
		}
		repo, err := githubreview.ParseRepository(*repositoryName)
		if err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument, "invalid GitHub repository", err)
		}
		ref := githubreview.CredentialReference{Name: strings.TrimSpace(*credentialName),
			Kind: githubreview.AuthKind(strings.TrimSpace(*authKind))}
		value, err := service.Configure(ctx, application.GitHubReviewConfigureRequest{
			ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
			ConnectionID:    strings.TrimSpace(*connectionID), Repository: repo,
			Credential: ref, ClientID: strings.TrimSpace(*clientID),
			AllowedLogHosts: logHosts.values, WriteEnabled: *allowWrite, Enabled: *enabled,
			ExpectedGeneration: *expectedGeneration, RequestedBy: "cli_operator"})
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "login":
		if strings.TrimSpace(*connectionID) == "" || *loginTimeout < time.Minute ||
			*loginTimeout > 30*time.Minute {
			return errors.New("usage: cyberagent github-review login --connection <id> --timeout <1m..30m> --enable-github-review --enable-permission-control")
		}
		return a.githubReviewDeviceLogin(ctx, service, *connectionID, *loginTimeout)
	case "qualify":
		if strings.TrimSpace(*connectionID) == "" || *pullRequest < 0 {
			return errors.New("usage: cyberagent github-review qualify --connection <id> [--pr <number>] --enable-github-review --enable-permission-control")
		}
		value, err := service.Qualify(ctx, *connectionID, *pullRequest)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "fetch":
		if strings.TrimSpace(*connectionID) == "" || *pullRequest <= 0 {
			return errors.New("usage: cyberagent github-review fetch --connection <id> --pr <number> --enable-github-review --enable-permission-control")
		}
		value, err := service.Fetch(ctx, application.GitHubReviewFetchRequest{
			ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
			ConnectionID:    *connectionID, PullRequest: *pullRequest})
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "evidence":
		if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*snapshotID) == "" {
			return errors.New("usage: cyberagent github-review evidence --run <id> --snapshot <id> --enable-github-review --enable-permission-control")
		}
		value, err := service.BuildEvidence(ctx, application.GitHubReviewEvidenceRequest{
			ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
			RunID:           *runID, SnapshotID: *snapshotID})
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "status":
		if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*connectionID) == "" {
			return errors.New("usage: cyberagent github-review status --run <id> --connection <id> [--pr <number>] --enable-github-review --enable-permission-control")
		}
		value, err := service.Projection(ctx, *runID, *connectionID, *pullRequest, *limit)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "write":
		release, activationErr := a.activateCLIInvocationFull(ctx, *runID, capabilities, *confirmFull)
		if activationErr != nil {
			return activationErr
		}
		defer release()
		return a.githubReviewWrite(ctx, service, writeOperation, githubReviewWriteCLIValues{
			runID: *runID, connectionID: *connectionID, snapshotID: *snapshotID,
			operationKey: *operationKey, targetID: *targetID, body: *body,
			bodyFile: *bodyFile, reviewEvent: *reviewEvent, reviewers: reviewers.values,
			changeSummary: *changeSummary, validationSummary: *validationSummary,
			confirm: *confirm})
	case "recover":
		if !*confirm {
			return errors.New("usage: cyberagent github-review recover --enable-github-review --enable-permission-control --confirm")
		}
		value, err := service.ReconcileStartup(ctx, *limit)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	default:
		return errors.New(githubReviewCLIUsage)
	}
}

func (a *App) newGitHubReviewService(managedRoot string,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*application.GitHubReviewService, error) {
	if err := a.ensureStore(); err != nil {
		return nil, err
	}
	root := managedRoot
	if root == "" {
		root = filepath.Join(a.home, "worktrees")
	}
	executor, err := repository.NewAdvancedExecutor(root, true)
	if err != nil {
		return nil, err
	}
	return application.NewGitHubReviewService(a.store, a.credentials, executor, capabilities)
}

func (a *App) githubReviewCredentialCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || a.credentials == nil {
		return errors.New("usage: cyberagent github-review credential status|set|delete <name>")
	}
	switch strings.TrimSpace(args[0]) {
	case "status":
		if len(args) != 2 || !credential.ValidName(args[1]) {
			return errors.New("usage: cyberagent github-review credential status <name>")
		}
		configured, err := a.credentials.Configured(ctx, args[1])
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, map[string]any{"name": args[1],
			"configured": configured, "store_kind": a.credentials.Kind(),
			"plaintext_returned": false})
	case "set":
		fs := newFlagSet("github-review credential set", a.errOut)
		fromEnvironment := fs.String("from-env", "", "environment variable containing the token")
		confirm := fs.Bool("confirm", false, "confirm OS credential-store mutation")
		if err := fs.Parse(reorderFlags(args[1:], map[string]bool{
			"from-env": true, "confirm": false})); err != nil {
			return err
		}
		if fs.NArg() != 1 || !credential.ValidName(fs.Arg(0)) || !*confirm ||
			strings.TrimSpace(*fromEnvironment) == "" || !a.credentials.Available() {
			return errors.New("usage: cyberagent github-review credential set <name> --from-env <variable> --confirm")
		}
		secret := os.Getenv(*fromEnvironment)
		if len([]byte(secret)) < 8 || !credential.ValidSecret(secret) {
			return errors.New("GitHub credential environment value is missing or invalid")
		}
		if err := a.credentials.Put(ctx, fs.Arg(0), secret); err != nil {
			return err
		}
		secret = ""
		return writeExtensionJSON(a.out, map[string]any{"name": fs.Arg(0),
			"configured": true, "store_kind": a.credentials.Kind(),
			"plaintext_returned": false})
	case "delete":
		fs := newFlagSet("github-review credential delete", a.errOut)
		confirm := fs.Bool("confirm", false, "confirm OS credential-store mutation")
		if err := fs.Parse(reorderFlags(args[1:], map[string]bool{"confirm": false})); err != nil {
			return err
		}
		if fs.NArg() != 1 || !credential.ValidName(fs.Arg(0)) || !*confirm ||
			!a.credentials.Available() {
			return errors.New("usage: cyberagent github-review credential delete <name> --confirm")
		}
		if err := a.credentials.Delete(ctx, fs.Arg(0)); err != nil {
			return err
		}
		return writeExtensionJSON(a.out, map[string]any{"name": fs.Arg(0),
			"configured": false, "store_kind": a.credentials.Kind(),
			"plaintext_returned": false})
	default:
		return fmt.Errorf("unknown github-review credential subcommand %q", args[0])
	}
}

func (a *App) githubReviewDeviceLogin(ctx context.Context,
	service *application.GitHubReviewService, connectionID string,
	timeout time.Duration,
) error {
	authorization, err := service.BeginDeviceFlow(ctx, connectionID)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "verification_uri: %s\nuser_code: %s\nexpires_at: %s\nsession_id: %s\n",
		authorization.VerificationURI, authorization.UserCode,
		authorization.ExpiresAt.Format(time.RFC3339), authorization.SessionID)
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		result, err := service.PollDeviceFlow(loginCtx, connectionID,
			authorization.SessionID)
		if err != nil {
			return err
		}
		switch result.State {
		case githubreview.DeviceAuthorized:
			return writeExtensionJSON(a.out, result)
		case githubreview.DeviceDenied, githubreview.DeviceExpired:
			return apperror.New(apperror.CodeFailedPrecondition,
				"GitHub device authorization was not completed")
		}
		delay := time.Until(result.NextPollAt)
		if delay < time.Second {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-loginCtx.Done():
			timer.Stop()
			return loginCtx.Err()
		case <-timer.C:
		}
	}
}

type githubReviewWriteCLIValues struct {
	runID, connectionID, snapshotID, operationKey string
	targetID, body, bodyFile, reviewEvent         string
	reviewers                                     []string
	changeSummary, validationSummary              string
	confirm                                       bool
}

func (a *App) githubReviewWrite(ctx context.Context,
	service *application.GitHubReviewService, operation githubreview.WriteOperation,
	values githubReviewWriteCLIValues,
) error {
	if strings.TrimSpace(values.runID) == "" || strings.TrimSpace(values.connectionID) == "" ||
		strings.TrimSpace(values.snapshotID) == "" || strings.TrimSpace(values.operationKey) == "" {
		return errors.New("usage: cyberagent github-review write <operation> --run <id> --connection <id> --snapshot <id> --operation-key <key> [typed write flags]")
	}
	snapshot, found, err := a.store.GetGitHubReviewSnapshot(ctx, values.snapshotID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review snapshot was not found")
		}
		return err
	}
	connection, found, err := a.store.GetGitHubReviewConnection(ctx, values.connectionID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review connection was not found")
		}
		return err
	}
	body := strings.TrimSpace(values.body)
	if strings.TrimSpace(values.bodyFile) != "" {
		if body != "" {
			return apperror.New(apperror.CodeInvalidArgument,
				"use only one of --body or --body-file")
		}
		raw, err := os.ReadFile(filepath.Clean(values.bodyFile))
		if err != nil {
			return err
		}
		if len(raw) > githubreview.MaxTextBytes {
			return apperror.New(apperror.CodeResourceExhausted,
				"GitHub review body file exceeds its bound")
		}
		body = strings.TrimSpace(string(raw))
	}
	spec := githubreview.WriteSpec{ProtocolVersion: githubreview.WriteProtocolVersion,
		Operation: operation, Identity: snapshot.Identity, Credential: connection.Credential,
		CapabilityGeneration: snapshot.Capability.Generation,
		TargetID:             strings.TrimSpace(values.targetID), Body: body,
		ReviewEvent:        strings.TrimSpace(values.reviewEvent),
		Reviewers:          append([]string(nil), values.reviewers...),
		LocalChangeSummary: values.changeSummary, ValidationSummary: values.validationSummary}
	spec.Normalize()
	preview, err := githubreview.NewWritePreview(spec, time.Now().UTC())
	if err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"GitHub review write is invalid", err)
	}
	if !values.confirm {
		return writeExtensionJSON(a.out, map[string]any{"protocol_version": application.GitHubReviewAPIProtocolVersion, "review_only": true,
			"preview": preview})
	}
	reviewed, err := service.ReviewWrite(ctx, application.GitHubReviewWriteReviewRequest{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
		RunID:           values.runID, ConnectionID: values.connectionID,
		SnapshotID: values.snapshotID, OperationKey: values.operationKey,
		RequestedBy: "cli_operator", Spec: spec})
	if err != nil {
		return err
	}
	if reviewed.Preview.ID != preview.ID {
		return apperror.New(apperror.CodeConflict,
			"GitHub review state changed after the displayed preview")
	}
	decision, err := a.store.DecideApproval(ctx, approval.DecisionRequest{
		ProposalID:     reviewed.Operation.ID,
		IdempotencyKey: values.operationKey + "-cli-approval",
		Action:         approval.ActionApprove, ReviewedBy: "cli_operator"})
	if err != nil {
		return err
	}
	executed, executeErr := service.ExecuteWrite(ctx,
		application.GitHubReviewWriteExecuteRequest{
			ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
			RunID:           values.runID, OperationID: reviewed.Operation.ID,
			ApprovalID: decision.Approval.ID, RequestedBy: "cli_operator"})
	if printErr := writeExtensionJSON(a.out, map[string]any{"review": reviewed,
		"result": executed}); printErr != nil && executeErr == nil {
		executeErr = printErr
	}
	return executeErr
}
