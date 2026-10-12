package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
	"cyberagent-workbench/internal/workspace"
)

type successEnvelope struct {
	Version   string `json:"version"`
	RequestID string `json:"request_id"`
	Data      any    `json:"data"`
	Page      *Page  `json:"page,omitempty"`
}

type errorEnvelope struct {
	Version   string       `json:"version"`
	RequestID string       `json:"request_id"`
	Error     apiErrorView `json:"error"`
}

type apiErrorView struct {
	Code                    string                          `json:"code"`
	Message                 string                          `json:"message"`
	MessageQueued           *bool                           `json:"message_queued,omitempty"`
	OperationKeyInvalidated *bool                           `json:"operation_key_invalidated,omitempty"`
	TurnFailed              *bool                           `json:"turn_failed,omitempty"`
	TurnFailure             *ThreadTurnFailureReferenceView `json:"turn_failure,omitempty"`
	RevisionUnchanged       *QueueRevisionUnchangedView     `json:"revision_unchanged,omitempty"`
}

func (a *API) route(request *http.Request) (any, *Page, error) {
	requestPath := request.URL.Path
	switch requestPath {
	case "/api/v1":
		if err := rejectQuery(request.URL.Query()); err != nil {
			return nil, nil, err
		}
		resources := []string{"threads", "runs", "sessions", "work-items", "notes", "artifacts",
			"agent-graph", "delegations", "readonly-fanout", "finding-reports",
			"external-skills", "workspaces", "workspace-explorer", "workspace-search", "models",
			"project-instructions", "long-term-memories", "session-tree",
			"repository-state", "repository-diff", "repository-history", "repository-file-history", "repository-commit-detail", "repository-commit-comparison", "repository-commit-file-preview", "verification-evidence", "verification-plan", "verification-plan-coverage", "verification-snapshot-export", "verification-snapshot-receipts", "code-handoff", "code-handoff-export",
			"operation-receipts", "operator-actions", "evidence-inventory",
			"run-activity", "web-evidence", "run-capability-readiness", "event-stream", "event-poll", "capabilities", "openapi",
			"doctor", "debug", "diagnostic-bundle"}
		if a.scheduledJobController != nil {
			resources = append(resources, "scheduled-jobs")
		}
		if a.dockerSandboxController != nil {
			resources = append(resources, "docker-sandbox-readiness", "docker-sandbox-status")
		}
		if a.batchDeliveryController != nil {
			resources = append(resources, "batch-deliveries")
		}
		if a.standardCodeDeliveryController != nil {
			resources = append(resources, "standard-code-delivery")
			if a.controlEnabled {
				resources = append(resources, "standard-code-delivery-control")
			}
		}
		if a.extensionController != nil {
			resources = append(resources, "extensions")
		}
		if a.dockerSandboxControlEnabled {
			resources = append(resources, "docker-sandbox-admission-control",
				"docker-sandbox-start-control", "docker-sandbox-cancel-control")
		}
		if a.controlEnabled {
			resources = append(resources, "model-cancellation-control",
				"specialist-model-cancellation-control", "execution-profile-control",
				"context-memory-control", "project-instruction-refresh-control",
				"continuity-checkpoint-control", "continuity-branch-control")
		}
		if a.batchDeliveryControlEnabled {
			resources = append(resources, "batch-delivery-control")
		}
		if a.executionPermissionControlEnabled {
			resources = append(resources, "execution-permission-control")
		}
		if a.verificationEvidenceEnabled {
			resources = append(resources, "verification-evidence-control", "verification-plan-control", "verification-plan-association-control", "verification-snapshot-receipt-control")
		}
		if a.runCreationEnabled {
			resources = append(resources, "run-creation-control")
		}
		if a.standardCodePresetEnabled {
			resources = append(resources, "standard-code-preset-control")
		}
		if a.runCreationEnabled && a.sessionMessageEnabled {
			resources = append(resources, "thread-creation-control", "thread-message-control",
				"thread-lifecycle-control")
		}
		if a.runLifecycleEnabled {
			resources = append(resources, "run-lifecycle-control")
		}
		if a.runExecutionEnabled {
			resources = append(resources, "run-execution-control")
		}
		if a.planDeliveryControlEnabled {
			resources = append(resources, "plan-delivery-control")
		}
		if a.approvalControlEnabled {
			resources = append(resources, "approval-control")
		}

		if a.modelControlEnabled {
			resources = append(resources, "model-control")
		}
		if a.providerDefinitionEnabled {
			resources = append(resources, "provider-definition-control")
		}
		if a.providerCredentialEnabled {
			resources = append(resources, "provider-credential-control")
		}
		if a.fileEditReviewEnabled {
			resources = append(resources, "file-edit-review-control")
		}
		if a.fileEditProposalEnabled {
			resources = append(resources, "file-edit-proposal-control")
		}
		if a.runWakeControlEnabled {
			resources = append(resources, "run-wake-control")
		}
		if a.fileEditApplyEnabled {
			resources = append(resources, "file-edit-apply-control")
		}
		if a.runWakeExecutionEnabled {
			resources = append(resources, "run-wake-execution-control")
		}
		if a.scheduledJobControlEnabled {
			resources = append(resources, "scheduled-job-control")
		}
		if a.skillInstallationEnabled {
			resources = append(resources, "skill-installation-control")
		}
		if a.evidenceAttachmentEnabled {
			resources = append(resources, "evidence-attachment-control")
		}
		if a.extensionControlEnabled {
			resources = append(resources, "extension-control")
		}
		if a.codeIntelSource != nil {
			resources = append(resources, "code-intel")
		}
		return IndexView{APIVersion: Version, AppVersion: a.appVersion, Resources: resources}, nil, nil
	case "/api/v1/health":
		if err := rejectQuery(request.URL.Query()); err != nil {
			return nil, nil, err
		}
		return a.health(request)
	case "/api/v1/capabilities":
		return a.runtimeCapabilities(request)
	case ExtensionInventoryPath:
		return a.extensionInventory(request)
	case CodeIntelInventoryPath:
		return a.codeIntelInventory(request)
	case "/api/v1/browser/safe-web-readiness":
		return a.browserSafeWebReadiness(request)
	case "/api/v1/operation-receipts":
		return a.operationReceiptHistory(request)
	case ScheduledJobsPath:
		return a.scheduledJobs(request, "")
	case DoctorSnapshotPath:
		return a.doctorSnapshot(request)
	case DebugQueryPath:
		return a.debugQuery(request)
	case DiagnosticBundlePath:
		return a.diagnosticBundle(request)
	}
	if !strings.HasPrefix(requestPath, "/api/v1/") {
		return nil, nil, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, "/api/v1/"), "/")
	for _, segment := range segments {
		if err := validatePathIdentity(segment); err != nil {
			return nil, nil, err
		}
	}
	switch segments[0] {
	case "threads":
		return a.routeThreads(request, segments)
	case "scheduled-jobs":
		if len(segments) == 2 {
			return a.scheduledJob(request, segments[1])
		}
	case "models":
		if len(segments) == 1 {
			return a.modelAvailability(request)
		}
		if len(segments) == 3 && segments[1] == "routes" && segments[2] == "available" {
			return a.modelRouteCatalog(request)
		}
		if len(segments) == 2 && segments[1] == "provider-definitions" {
			return a.providerDefinitions(request)
		}
		if len(segments) == 2 && segments[1] == "credentials" {
			return a.providerCredentialStatuses(request)
		}
	case "runs":
		return a.routeRuns(request, segments)
	case "workspaces":
		if len(segments) == 1 {
			return a.workspaces(request)
		}
		if len(segments) == 3 && segments[2] == "explore" {
			return a.workspaceExplorer(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "search" {
			return a.workspaceSearch(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "repository-state" {
			return a.workspaceRepositoryState(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "repository-diff" {
			return a.workspaceRepositoryDiff(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "repository-history" {
			return a.workspaceRepositoryHistory(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "repository-file-history" {
			return a.workspaceRepositoryFileHistory(request, segments[1])
		}
		if len(segments) == 3 && segments[2] == "repository-commit-comparison" {
			return a.workspaceRepositoryCommitComparison(request, segments[1])
		}
		if len(segments) == 4 && segments[2] == "repository-commits" {
			return a.workspaceRepositoryCommit(request, segments[1], segments[3])
		}
		if len(segments) == 5 && segments[2] == "repository-commits" &&
			segments[4] == "file-preview" {
			return a.workspaceRepositoryCommitFilePreview(request, segments[1], segments[3])
		}
	case "sessions":
		return a.routeSessions(request, segments)
	case "memories":
		return a.contextMemories(request, segments)
	case "work-items":
		if len(segments) == 2 {
			return a.workItem(request, segments[1])
		}
	case "notes":
		if len(segments) == 2 {
			return a.note(request, segments[1])
		}
	case "artifacts":
		if len(segments) == 2 {
			return a.artifact(request, segments[1])
		}
	}
	return nil, nil, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
}

func (a *API) workspaceRepositoryState(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	state, err := repository.Inspect(request.Context(), registered.RootPath, registered.ID)
	if err != nil {
		return nil, nil, err
	}
	changes := make([]RepositoryChangeView, len(state.Changes))
	for index, change := range state.Changes {
		changes[index] = RepositoryChangeView{Path: change.Path, Staging: change.Staging,
			Worktree: change.Worktree}
	}
	return RepositoryStateView{
		ProtocolVersion: state.ProtocolVersion, WorkspaceID: state.WorkspaceID,
		Kind: state.Kind, Available: state.Available, Clean: state.Clean,
		Detached: state.Detached, Branch: state.Branch, Head: state.Head, Changes: changes,
		StagedCount: state.StagedCount, WorktreeCount: state.WorktreeCount,
		UntrackedCount: state.UntrackedCount, ConflictedCount: state.ConflictedCount,
		RedactionCount: state.RedactionCount, Truncated: state.Truncated,
		ReadOnly: state.ReadOnly, RootPathExposed: state.RootPathExposed,
		ContentIncluded:      state.ContentIncluded,
		RemoteConfigIncluded: state.RemoteConfigIncluded,
		ProcessStarted:       state.ProcessStarted, NetworkUsed: state.NetworkUsed,
		HooksExecuted: state.HooksExecuted,
	}, nil, nil
}

func (a *API) operationReceiptHistory(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "run_id", "limit"); err != nil {
		return nil, nil, err
	}
	runID := ""
	if items, ok := values["run_id"]; ok {
		if len(items) != 1 || items[0] == "" || items[0] != strings.TrimSpace(items[0]) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"operation receipt history run_id must appear exactly once")
		}
		runID = items[0]
	}
	limit := 0
	if items, ok := values["limit"]; ok {
		if len(items) != 1 || items[0] == "" || items[0] != strings.TrimSpace(items[0]) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"operation receipt history limit must appear exactly once")
		}
		parsed, err := strconv.Atoi(items[0])
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"operation receipt history limit must be an integer")
		}
		limit = parsed
	}
	history, err := application.NewOperationReceiptHistoryService(a.store).List(
		request.Context(), application.ListOperationReceiptHistoryRequest{
			RunID: runID, Limit: limit,
		})
	if err != nil {
		return nil, nil, err
	}
	items := make([]OperationReceiptHistoryItemView, len(history.Items))
	for index, item := range history.Items {
		items[index] = OperationReceiptHistoryItemView{ID: item.ID, Scope: item.Scope,
			RunID: item.RunID, CompletedAt: item.CompletedAt,
			Receipt: operationReceiptView(item.Receipt)}
	}
	return OperationReceiptHistoryView{ProtocolVersion: history.ProtocolVersion,
		Items: items, Truncated: history.Truncated}, nil, nil
}

func (a *API) workspaceSearch(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "query"); err != nil {
		return nil, nil, err
	}
	values, ok := request.URL.Query()["query"]
	if !ok || len(values) != 1 {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"query parameter \"query\" is required exactly once")
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	snapshot, err := workspace.Search(registered.RootPath, registered.ID, values[0])
	if err != nil {
		return nil, nil, err
	}
	results := make([]WorkspaceSearchResultView, len(snapshot.Results))
	for index, result := range snapshot.Results {
		results[index] = WorkspaceSearchResultView{Path: result.Path,
			MatchKind: result.MatchKind, Line: result.Line, Snippet: result.Snippet,
			ContentTruncated: result.ContentTruncated,
			Provenance: WorkspaceExplorerProvenanceView{
				Version: result.Provenance.Version, SourceKind: result.Provenance.SourceKind,
				SourceRef:             result.Provenance.SourceRef,
				ContentSHA256:         result.Provenance.ContentSHA256,
				InstructionAuthorized: result.Provenance.InstructionAuthorized,
			}}
	}
	return WorkspaceSearchView{ProtocolVersion: snapshot.ProtocolVersion,
		WorkspaceID: snapshot.WorkspaceID, Results: results,
		ScannedEntries: snapshot.ScannedEntries, ScannedFiles: snapshot.ScannedFiles,
		ScannedBytes: snapshot.ScannedBytes, Truncated: snapshot.Truncated,
		RootPathExposed: snapshot.RootPathExposed}, nil, nil
}

func (a *API) workspaceExplorer(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "path"); err != nil {
		return nil, nil, err
	}
	requestedPath, present := "", false
	if items, ok := request.URL.Query()["path"]; ok {
		present = true
		if len(items) == 1 && items[0] == strings.TrimSpace(items[0]) {
			requestedPath = items[0]
		}
	}
	if present && requestedPath == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"query parameter \"path\" must appear exactly once and cannot be empty")
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	snapshot, err := workspace.Explore(registered.RootPath, registered.ID, requestedPath)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]WorkspaceExplorerEntryView, len(snapshot.Entries))
	for index, entry := range snapshot.Entries {
		entries[index] = WorkspaceExplorerEntryView{Name: entry.Name, Path: entry.Path,
			Kind: entry.Kind, SizeBytes: entry.SizeBytes, Readable: entry.Readable}
	}
	return WorkspaceExplorerView{
		ProtocolVersion: snapshot.ProtocolVersion, WorkspaceID: snapshot.WorkspaceID,
		Path: snapshot.Path, Kind: snapshot.Kind, Entries: entries,
		Content: snapshot.Content, TotalBytes: snapshot.TotalBytes,
		ReturnedBytes: snapshot.ReturnedBytes, Truncated: snapshot.Truncated,
		RedactionCount: snapshot.RedactionCount, RootPathExposed: snapshot.RootPathExposed,
		Provenance: WorkspaceExplorerProvenanceView{
			Version: snapshot.Provenance.Version, SourceKind: snapshot.Provenance.SourceKind,
			SourceRef:             snapshot.Provenance.SourceRef,
			ContentSHA256:         snapshot.Provenance.ContentSHA256,
			InstructionAuthorized: snapshot.Provenance.InstructionAuthorized,
		},
	}, nil, nil
}

func (a *API) modelAvailability(request *http.Request) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	snapshot := a.modelRegistry.Snapshot()
	providers := make([]ProviderAvailabilityView, len(snapshot.Providers))
	for index, provider := range snapshot.Providers {
		harnesses := make([]ModelHarnessAvailabilityView, len(provider.Harnesses))
		for harnessIndex, harness := range provider.Harnesses {
			harnesses[harnessIndex] = modelHarnessAvailabilityView(harness)
		}
		providers[index] = ProviderAvailabilityView{
			Name: provider.Name, DisplayName: provider.DisplayName,
			Kind: provider.Kind, Status: provider.Status,
			Models: append([]string{}, provider.Models...), Harnesses: harnesses,
			CredentialSource: provider.CredentialSource, NetworkRequired: provider.NetworkRequired,
			ConfigurationError: provider.ConfigurationError, Custom: provider.Custom,
			Enabled: provider.Enabled, DefinitionRevision: provider.DefinitionRevision,
			Transport: provider.Transport, SearchMode: provider.SearchMode,
			NativeWebSearchCapability:     provider.NativeWebSearchCapability,
			NativeWebSearchRuntimeEnabled: provider.NativeWebSearchRuntimeEnabled,
		}
	}
	routes := make([]ModelRouteAvailabilityView, len(snapshot.Routes))
	for index, route := range snapshot.Routes {
		routes[index] = ModelRouteAvailabilityView{
			Name: route.Name, Provider: route.Provider, Model: route.Model,
			Available: route.Available, HarnessReady: route.HarnessReady,
		}
	}
	return ModelAvailabilityView{ProtocolVersion: snapshot.ProtocolVersion,
		Generation: snapshot.Generation, Providers: providers, Routes: routes}, nil, nil
}

func (a *API) workspaces(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	records, err := a.store.ListWorkspacesPage(request.Context(),
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]WorkspaceView, len(records))
	for index, record := range records {
		views[index] = WorkspaceView{ID: record.ID, Name: record.Name,
			CreatedAt: record.CreatedAt}
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) routeRuns(request *http.Request, segments []string) (any, *Page, error) {
	switch len(segments) {
	case 1:
		return a.runs(request)
	case 2:
		return a.run(request, segments[1])
	case 3:
		switch segments[2] {
		case "agent-graph":
			return a.runAgentGraph(request, segments[1])
		case "external-skills":
			return a.runExternalSkills(request, segments[1])
		case "delegations":
			return a.runDelegations(request, segments[1])
		case "fanout-plans":
			return a.runFanoutPlans(request, segments[1])
		case "fanout-executions":
			return a.listFanoutExecutions(request, segments[1])
		case "child-task-proposals":
			return a.listChildTaskProposals(request, segments[1])
		case "batch-deliveries":
			return a.listBatchDeliveries(request, segments[1])
		case "reports":
			return a.runFindingReports(request, segments[1])
		case "events":
			return a.runEvents(request, segments[1])
		case "activity":
			return a.runActivity(request, segments[1])
		case "web-evidence":
			return a.runWebEvidence(request, segments[1])
		case "capability-readiness":
			return a.runCapabilityReadiness(request, segments[1])
		case "work-items":
			return a.runWorkItems(request, segments[1])
		case "notes":
			return a.runNotes(request, segments[1])
		case "artifacts":
			return a.runArtifacts(request, segments[1])
		case "tool-rounds":
			return a.runToolRounds(request, segments[1])
		case "approvals":
			return a.runApprovals(request, segments[1])
		case "file-edits":
			return a.runFileEdits(request, segments[1])
		case "file-edit-change-set":
			return a.runFileEditChangeSet(request, segments[1])
		case "file-edit-proposal-source":
			return a.runFileEditProposalSource(request, segments[1])
		case "wake-intent":
			return a.runWakeIntent(request, segments[1])
		case "scheduled-jobs":
			return a.scheduledJobs(request, segments[1])
		case "operator-actions":
			return a.runOperatorActions(request, segments[1])
		case "evidence-attachments":
			return a.runEvidenceInventory(request, segments[1])
		case "verification-evidence":
			return a.runVerificationEvidence(request, segments[1])
		case "verification-plan":
			return a.runVerificationPlans(request, segments[1])
		case "verification-plan-coverage":
			return a.runVerificationPlanCoverage(request, segments[1])
		case "verification-snapshot-receipts":
			return a.runVerificationSnapshotReceipts(request, segments[1])
		case "verification-snapshot-receipt-reviews":
			return a.runVerificationSnapshotReceiptReviews(request, segments[1])
		case "code-handoff":
			return a.runCodeHandoff(request, segments[1])
		case "standard-code-delivery":
			return a.runStandardCodeDelivery(request, segments[1])
		case "project-instructions":
			return a.runProjectInstructions(request, segments[1])
		case "context-summary":
			return a.runContextSummary(request, segments[1])
		}
	case 4:
		if segments[2] == "batch-deliveries" {
			return a.getBatchDelivery(request, segments[1], segments[3])
		}
		if segments[2] == "code-handoff" && segments[3] == "export" {
			return a.runCodeHandoffExport(request, segments[1])
		}
		if segments[2] == "reports" {
			return a.runFindingReport(request, segments[1], segments[3])
		}
		if segments[2] == "file-edits" {
			return a.runFileEdit(request, segments[1], segments[3])
		}
		if segments[2] == "file-edit-proposal-recovery" {
			return a.runFileEditProposalRecovery(request, segments[1], segments[3])
		}
	case 5:
		if segments[2] == "batch-deliveries" && segments[4] == "workbench" {
			return a.getBatchWorkbench(request, segments[1], segments[3])
		}
		if segments[2] == "approvals" && segments[4] == "preview" {
			return a.runApprovalPreview(request, segments[1], segments[3])
		}
	case 6:
		if segments[2] == "verification-plan-coverage" && segments[4] == "items" {
			return a.runVerificationPlanItemCoverage(request, segments[1], segments[3], segments[5])
		}
	case 7:
		if segments[2] == "verification-plan-coverage" && segments[4] == "items" &&
			segments[6] == "snapshot-export" {
			return a.runVerificationPlanItemSnapshotExport(request, segments[1], segments[3],
				segments[5])
		}
	}
	return nil, nil, apperror.New(apperror.CodeNotFound, "Run HTTP API endpoint was not found")
}

func (a *API) runWebEvidence(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	limit := 100
	if raw, ok := singleQueryValue(values, "limit"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > webevidence.MaxInventoryItems {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				fmt.Sprintf("web evidence limit must be between 1 and %d",
					webevidence.MaxInventoryItems))
		}
		limit = parsed
	}
	inventory, err := webevidence.LoadInventory(request.Context(), a.store, runID,
		limit, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	return inventory, nil, nil
}

func (a *API) runFileEditChangeSet(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	run, mission, err := a.fileEditRunBinding(request, runID)
	if err != nil {
		return nil, nil, err
	}
	target, err := application.ResolveRunFileWorkspace(request.Context(), a.store, run, mission, a.fileWorkspaceDrydocks)
	if err != nil {
		return nil, nil, err
	}
	values, err := a.store.ListFileEditPreviewsPage(request.Context(), fileedit.ListFilter{
		SessionID: run.SessionID, WorkspaceID: target.Workspace.ID,
	}, 0, application.MaxFileEditChangeSetItems+1)
	if err != nil {
		return nil, nil, err
	}
	truncated := len(values) > application.MaxFileEditChangeSetItems
	if truncated {
		values = values[:application.MaxFileEditChangeSetItems]
	}
	changeSet, err := application.BuildFileEditChangeSetForWorkspace(run, mission, target.Workspace.ID, values)
	if err != nil {
		return nil, nil, err
	}
	items := make([]FileEditChangeSetItemView, len(changeSet.Items))
	for index, value := range changeSet.Items {
		preview := fileEditPreviewView(value, run.Terminal())
		applyEnabled := a.fileEditApplyEnabled && value.Status == fileedit.StatusApproved &&
			!run.Terminal()
		items[index] = FileEditChangeSetItemView{
			ID: value.ID, Path: value.Path, Operation: value.Operation,
			DestinationPath: value.DestinationPath, Status: value.Status,
			DiffBytes: len([]byte(value.Diff)), SecretsRedacted: value.SecretsRedacted,
			AllowedActions: preview.AllowedActions, ApplyEnabled: applyEnabled,
			UpdatedAt: value.UpdatedAt,
		}
	}
	return FileEditChangeSetView{
		ProtocolVersion: application.FileEditChangeSetProtocolVersion,
		RunID:           changeSet.RunID, SessionID: changeSet.SessionID,
		WorkspaceID: changeSet.WorkspaceID, Items: items,
		ProposedCount: changeSet.Counts.Proposed, ApprovedCount: changeSet.Counts.Approved,
		AppliedCount: changeSet.Counts.Applied, DeniedCount: changeSet.Counts.Denied,
		FailedCount: changeSet.Counts.Failed, ReturnedCount: len(items),
		TotalDiffBytes: changeSet.TotalDiffBytes, Truncated: truncated,
		ReviewIndependent: true, ApplyIndependent: true, AtomicApply: false,
		BatchMutationSupported: false, PartialApplyVisible: true,
		DiffContentIncluded: false,
	}, nil, nil
}

func (a *API) runFileEdits(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	run, mission, err := a.fileEditRunBinding(request, runID)
	if err != nil {
		return nil, nil, err
	}
	const limit = 100
	values, err := a.store.ListRunFileEditPreviewsPage(request.Context(), run.ID, 0, limit+1)
	if err != nil {
		return nil, nil, err
	}
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}
	items := make([]FileEditPreviewView, len(values))
	// History remains reviewable if today's target is unavailable. A successful
	// current resolution is required only for advertising new edit controls.
	target, targetErr := application.ResolveRunFileWorkspace(request.Context(), a.store, run, mission, a.fileWorkspaceDrydocks)
	for index, value := range values {
		belongs, err := a.store.FileEditWorkspaceBelongsToRun(request.Context(), run.ID, value.SessionID, value.WorkspaceID)
		if err != nil {
			return nil, nil, err
		}
		if value.SessionID != run.SessionID || !belongs {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"file edit queue contains a mismatched record")
		}
		readOnly := run.Terminal() || targetErr != nil || value.WorkspaceID != target.Workspace.ID
		items[index] = fileEditPreviewView(value, readOnly)
		items[index].ApplyEnabled = a.fileEditApplyEnabled &&
			value.Status == fileedit.StatusApproved && !readOnly
	}
	return FileEditQueueView{ProtocolVersion: application.FileEditReviewProtocolVersion,
		RunID: run.ID, Items: items, Truncated: truncated,
		ApplyEnabled: a.fileEditApplyEnabled}, nil, nil
}

func (a *API) runFileEdit(request *http.Request, runID string,
	editID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	run, mission, err := a.fileEditRunBinding(request, runID)
	if err != nil {
		return nil, nil, err
	}
	value, err := a.store.GetFileEditPreview(request.Context(), editID)
	if err != nil {
		return nil, nil, err
	}
	belongs, err := a.store.FileEditWorkspaceBelongsToRun(request.Context(), run.ID, value.SessionID, value.WorkspaceID)
	if err != nil {
		return nil, nil, err
	}
	if value.SessionID != run.SessionID || !belongs {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"file edit does not belong to the requested Run")
	}
	target, targetErr := application.ResolveRunFileWorkspace(request.Context(), a.store, run, mission, a.fileWorkspaceDrydocks)
	readOnly := run.Terminal() || targetErr != nil || value.WorkspaceID != target.Workspace.ID
	view := fileEditPreviewView(value, readOnly)
	if reader, ok := a.store.(fileEditDetailReader); ok && value.Operation == fileedit.OperationDelete {
		full, readErr := reader.GetFileEdit(request.Context(), editID)
		if readErr != nil {
			return nil, nil, readErr
		}
		if full.ID != value.ID || full.SessionID != run.SessionID ||
			full.WorkspaceID != value.WorkspaceID {
			return nil, nil, apperror.New(apperror.CodeNotFound,
				"file edit does not belong to the requested Run")
		}
		view = fileEditView(full, readOnly)
	}
	view.ApplyEnabled = a.fileEditApplyEnabled && view.Status == fileedit.StatusApproved &&
		!readOnly
	return view, nil, nil
}

func (a *API) fileEditRunBinding(request *http.Request,
	runID string,
) (domain.Run, domain.Mission, error) {
	run, err := a.store.GetRun(request.Context(), runID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if run.SessionID == "" {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run has no attached Session")
	}
	mission, err := a.store.GetMission(request.Context(), run.MissionID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	if mission.WorkspaceID == "" {
		return domain.Run{}, domain.Mission{}, apperror.New(
			apperror.CodeFailedPrecondition, "Run Mission has no Workspace")
	}
	return run, mission, nil
}

func (a *API) runWakeIntent(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if a.runWakeController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"Run wake intent is unavailable")
	}
	intent, found, err := a.runWakeController.Get(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	return RunWakeStateView{ProtocolVersion: domain.RunWakeIntentProtocolVersion,
		RunID: runID, Found: found, Intent: runWakeIntentView(intent, found)}, nil, nil
}

func (a *API) runApprovals(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	run, err := a.store.GetRun(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	records, err := a.store.ListApprovals(request.Context(), approval.ListFilter{
		RunID: run.ID, Status: approval.StatusPending,
		Limit: application.MaxApprovalQueueItems + 1,
	})
	if err != nil {
		return nil, nil, err
	}
	recoverableByApproval := map[string]domain.WebFetchAuthorization{}
	recordIndexByApproval := make(map[string]int, len(records))
	for index, record := range records {
		recordIndexByApproval[record.ID] = index
	}
	if source, ok := a.store.(interface {
		ListRecoverableWebFetchAuthorizations(context.Context, string, int) (
			[]domain.WebFetchAuthorization, error)
	}); ok {
		recoverable, loadErr := source.ListRecoverableWebFetchAuthorizations(
			request.Context(), run.ID, application.MaxApprovalQueueItems+1)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		for _, value := range recoverable {
			if _, exists := recoverableByApproval[value.ApprovalID]; exists {
				continue
			}
			record, loadErr := a.store.GetApproval(request.Context(), value.ApprovalID)
			if loadErr != nil {
				return nil, nil, loadErr
			}
			recoverableByApproval[value.ApprovalID] = value
			records = upsertApprovalQueueRecord(records, recordIndexByApproval, record)
		}
	}
	truncated := len(records) > application.MaxApprovalQueueItems
	if truncated {
		records = records[:application.MaxApprovalQueueItems]
	}
	items := make([]ApprovalQueueItemView, len(records))
	for index, record := range records {
		recoverable, recovering := recoverableByApproval[record.ID]
		if record.RunID != run.ID || (record.Status != approval.StatusPending && !recovering) {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"approval queue contains a mismatched record")
		}
		item := ApprovalQueueItemView{
			ID: record.ID, ProposalID: record.ProposalID, RunID: record.RunID,
			SessionID: record.SessionID, WorkspaceID: record.WorkspaceID,
			ToolName: record.ToolName, ActionClass: record.ActionClass,
			Mode: record.Mode, Status: string(record.Status),
			AllowedActions: application.ApprovalDecisionActions(record, run.Terminal()),
			Version:        record.Version, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		}
		if recovering {
			item.AllowedActions = recoverableWebFetchApprovalActions(recoverable)
			item.CanonicalURL, item.ExactTarget = recoverable.CanonicalURL,
				recoverable.ExactTarget
		}
		if record.ToolName == string(toolgateway.CommandRuntimeTool) && len(item.AllowedActions) != 0 {
			if source, ok := a.store.(interface {
				GetCommandApprovalGrantScope(context.Context, string) (approval.GrantQuery, error)
			}); ok {
				if _, err := source.GetCommandApprovalGrantScope(request.Context(), record.ProposalID); err == nil {
					item.AllowedActions = []application.ApprovalControlAction{application.ApprovalControlApproveOnce, application.ApprovalControlApproveForRun, application.ApprovalControlDeny}
				}
			}
		}
		if record.ToolName == "web_fetch" {
			if recovering {
				items[index] = item
				continue
			}
			if source, ok := a.store.(interface {
				GetWebFetchAuthorizationByApproval(context.Context, string) (
					domain.WebFetchAuthorization, error)
			}); ok {
				value, loadErr := source.GetWebFetchAuthorizationByApproval(
					request.Context(), record.ID)
				if loadErr != nil {
					return nil, nil, loadErr
				}
				item.CanonicalURL, item.ExactTarget = value.CanonicalURL, value.ExactTarget
			}
		}
		items[index] = item
	}
	return ApprovalQueueView{ProtocolVersion: application.ApprovalQueueProtocolVersion,
		RunID: run.ID, Items: items, Truncated: truncated}, nil, nil
}

// upsertApprovalQueueRecord closes the read race between the pending approval
// query and the recoverable Web authorization query. The latter is read after
// the former and therefore carries the newer durable decision; it must replace
// a stale pending snapshot instead of creating a duplicate queue card.
func upsertApprovalQueueRecord(records []approval.Record, indexByID map[string]int,
	record approval.Record,
) []approval.Record {
	if index, found := indexByID[record.ID]; found {
		records[index] = record
		return records
	}
	indexByID[record.ID] = len(records)
	return append(records, record)
}

func recoverableWebFetchApprovalActions(value domain.WebFetchAuthorization) []application.ApprovalControlAction {
	switch value.Status {
	case domain.WebFetchAuthorizationApproved, domain.WebFetchAuthorizationConsumed:
		if value.Scope == domain.WebFetchAuthorizationThread {
			return []application.ApprovalControlAction{application.ApprovalControlApproveForThread}
		}
		return []application.ApprovalControlAction{application.ApprovalControlApproveOnce}
	case domain.WebFetchAuthorizationDenied:
		return []application.ApprovalControlAction{application.ApprovalControlDeny}
	default:
		return nil
	}
}

func (a *API) routeSessions(request *http.Request, segments []string) (any, *Page, error) {
	if len(segments) == 6 && segments[2] == "messages" && segments[4] == "promotions" {
		return a.sessionSteeringPromotionObservation(request, segments[1], segments[3], segments[5])
	}
	if len(segments) == 6 && segments[2] == "messages" && segments[4] == "revisions" {
		return a.sessionSteeringRevisionObservation(request, segments[1], segments[3], segments[5])
	}
	if len(segments) == 6 && segments[2] == "messages" && segments[4] == "cancellations" {
		return a.sessionSteeringCancellationObservation(request, segments[1], segments[3], segments[5])
	}
	switch len(segments) {
	case 1:
		return a.sessions(request)
	case 2:
		return a.session(request, segments[1])
	case 3:
		if segments[2] == "messages" {
			return a.sessionMessages(request, segments[1])
		}
		if segments[2] == "tree" {
			return a.sessionContinuityTree(request, segments[1])
		}
	}
	return nil, nil, apperror.New(apperror.CodeNotFound, "Session HTTP API endpoint was not found")
}

func (a *API) health(request *http.Request) (any, *Page, error) {
	version, err := a.store.SchemaVersion(request.Context())
	if err != nil {
		return nil, nil, err
	}
	view := HealthView{Status: "ok", APIVersion: Version, AppVersion: a.appVersion,
		SchemaVersion: version}
	if identity, ok := a.store.(interface{ DataStoreID() string }); ok {
		view.DataStoreID = identity.DataStoreID()
	}
	return view, nil, nil
}

func (a *API) runs(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "status", "mission_id"); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parseStableListPage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	filter := domain.RunFilter{Limit: stableListStoreLimit(pageRequest)}
	if raw, ok := singleQueryValue(values, "mission_id"); ok {
		if err := validateIdentity(raw, "mission id"); err != nil {
			return nil, nil, err
		}
		filter.MissionID = raw
	}
	if raw, ok := singleQueryValue(values, "status"); ok {
		status := domain.RunStatus(strings.ToLower(raw))
		if !domain.ValidRunStatus(status) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "invalid Run status filter")
		}
		filter.Status = status
	}
	runs, err := a.store.ListRunsByCreationPage(request.Context(), filter,
		pageRequest.Anchor.BeforeCreatedAt, pageRequest.Anchor.BeforeID)
	if err != nil {
		return nil, nil, err
	}
	views := make([]RunView, len(runs))
	for index := range runs {
		views[index] = runView(runs[index])
	}
	views, page := trimStableListPage(views, pageRequest, runStableListPosition)
	return views, page, nil
}

func (a *API) run(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	run, err := a.store.GetRun(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	mission, err := a.store.GetMission(request.Context(), run.MissionID)
	if err != nil {
		return nil, nil, err
	}
	mode, err := a.store.GetRunMode(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	executionProfile, err := a.store.GetRunExecutionProfile(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	executionPermission, err := a.store.GetRunExecutionPermission(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	browserCDPPermission, err := a.store.GetRunBrowserCDPPermission(
		request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	executionInteraction, err := a.store.GetRunExecutionInteraction(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	usage, err := a.store.GetToolCallUsage(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	agentCodeTools, err := a.runAgentCodeCapabilities(request.Context(), run, mission,
		mode, executionPermission)
	if err != nil {
		return nil, nil, err
	}
	detail := RunDetailView{Run: runView(run), Mission: missionView(mission),
		Mode: runModeView(mode), ExecutionProfile: runExecutionProfileView(executionProfile),
		ExecutionPermission: runExecutionPermissionView(
			executionPermission, a.executionPermissionCapabilities),
		BrowserCDPPermission: runBrowserCDPPermissionView(browserCDPPermission,
			a.browserCDPPermissionCapabilities, executionPermission,
			a.executionPermissionCapabilities),
		ExecutionInteraction: runExecutionInteractionView(executionInteraction),
		ToolUsage:            toolUsageView(usage), AgentCodeTools: agentCodeTools}
	if err := a.projectStandardCodePreset(request.Context(), &detail.Run); err != nil {
		return nil, nil, err
	}
	checkpoint, found, err := a.store.GetSupervisorCheckpoint(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		view := checkpointView(checkpoint)
		detail.Checkpoint = &view
	}
	if source, ok := a.store.(interface {
		GetModelUsage(context.Context, string) (domain.ModelUsage, error)
	}); ok {
		usage, err := source.GetModelUsage(request.Context(), run.ID)
		if err != nil {
			return nil, nil, err
		}
		detail.ModelUsage = &usage
	}
	if source, ok := a.store.(interface {
		GetMonetaryUsage(context.Context, string) (domain.MonetaryUsage, error)
	}); ok {
		usage, err := source.GetMonetaryUsage(request.Context(), run.ID)
		if err != nil {
			return nil, nil, err
		}
		detail.MonetaryUsage = &MonetaryUsageView{Tracked: usage.Tracked, Currency: usage.Currency,
			CapMicros: usage.CapMicros, ReservedMicros: usage.ReservedMicros, SettledMicros: usage.SettledMicros,
			ReleasedMicros: usage.ReleasedMicros, RemainingMicros: usage.RemainingMicros, UnpricedCalls: usage.UnpricedCalls}
	}
	lease, found, err := a.store.GetRunExecutionLease(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		view := runExecutionLeaseView(lease, time.Now().UTC())
		detail.Lease = &view
	}
	steeringMessages, err := a.store.ListOperatorSteering(request.Context(), run.ID, 20)
	if err != nil {
		return nil, nil, err
	}
	steeringSummary, err := a.store.GetOperatorSteeringQueueSummary(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	detail.Steering = operatorSteeringQueueView(steeringSummary, steeringMessages)
	externalSkills, found, err := a.store.GetExternalSkillProjectionByRun(
		request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		view := externalSkillProjectionView(externalSkills)
		detail.ExternalSkills = &view
	}
	selection, selected, err := a.store.GetPlanDeliverySelectionByRun(request.Context(), run.ID)
	if err != nil {
		return nil, nil, err
	}
	proposals, err := a.store.ListPlanDeliveryProposals(request.Context(), run.ID, 1)
	if err != nil {
		return nil, nil, err
	}
	var proposal *domain.PlanDeliveryProposal
	if selected {
		value, err := a.store.GetPlanDeliveryProposal(request.Context(), selection.ProposalID)
		if err != nil {
			return nil, nil, err
		}
		proposal = &value
	} else if len(proposals) != 0 {
		proposal = &proposals[0]
	}
	if proposal != nil || selected {
		state := PlanDeliveryStateView{
			OperatorChoiceNeeded: proposal != nil && !selected,
			PhaseChangeNeeded:    selected && mode.Phase == domain.ExecutionPhasePlan,
			CapabilityGrant:      false,
			Checkpoints:          []DeliveryCheckpointView{},
		}
		if proposal != nil {
			view := planDeliveryProposalView(*proposal)
			state.Proposal = &view
		}
		if selected {
			view := planDeliverySelectionView(selection)
			state.Selection = &view
			state.RequiredCheckpoints = len(selection.Items)
			state.DeliveryGateEnforced, err = a.store.DeliveryGateEnforced(
				request.Context(), run.ID)
			if err != nil {
				return nil, nil, err
			}
			checkpoints, err := a.store.ListDeliveryCheckpoints(request.Context(), run.ID, 500)
			if err != nil {
				return nil, nil, err
			}
			readyItems := make(map[string]struct{}, len(selection.Items))
			state.Checkpoints = make([]DeliveryCheckpointView, len(checkpoints))
			for index, checkpoint := range checkpoints {
				item, err := a.store.GetWorkItem(request.Context(), checkpoint.WorkItemID)
				if err != nil {
					return nil, nil, err
				}
				ready := domain.DeliveryCheckpointReady(checkpoint, item, mode)
				state.Checkpoints[index] = deliveryCheckpointView(checkpoint, ready)
				if ready {
					readyItems[checkpoint.WorkItemID] = struct{}{}
				}
			}
			if reader, ok := a.store.(interface {
				ListThreadPlanCompletionSources(context.Context, string) ([]domain.ThreadPlanCompletionSource, error)
			}); ok {
				sources, err := reader.ListThreadPlanCompletionSources(request.Context(), run.ID)
				if err != nil {
					return nil, nil, err
				}
				for _, source := range sources {
					state.ContinuedCompletions = append(state.ContinuedCompletions, PlanCompletionSourceView{
						WorkItemID: source.WorkItemID, SourceRunID: source.SourceRunID,
						SourceWorkItemID: source.SourceWorkItemID, CheckpointID: source.CheckpointID,
						HandoffNoteID: source.HandoffNoteID, CompletedAt: source.CompletedAt,
						CompletionEventID: source.CompletionEventID,
					})
					if source.CheckpointID != "" {
						readyItems[source.WorkItemID] = struct{}{}
					}
				}
			}
			state.ReadyCheckpoints = len(readyItems)
		}
		detail.PlanDelivery = &state
	}
	return detail, nil, nil
}

func (a *API) runAgentCodeCapabilities(ctx context.Context, run domain.Run,
	mission domain.Mission, mode domain.RunModeSnapshot,
	permission domain.RunExecutionPermissionSnapshot,
) (AgentCodeCapabilitiesView, error) {
	rootFingerprint := ""
	unavailableReason := ""
	if mission.WorkspaceID == "" {
		unavailableReason = "Run has no registered Workspace"
	} else {
		registered, err := a.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
		if err != nil {
			return AgentCodeCapabilitiesView{}, err
		}
		rootFingerprint, err = workspace.AgentCodeRootFingerprint(registered.RootPath)
		if err != nil {
			unavailableReason = "registered Workspace root is unavailable"
		}
	}
	nodes, err := a.store.ListAgentNodes(ctx, run.ID)
	if err != nil {
		return AgentCodeCapabilitiesView{}, err
	}
	var root domain.AgentNode
	for _, node := range nodes {
		if node.Role != domain.AgentRoleRoot {
			continue
		}
		if root.ID != "" {
			return AgentCodeCapabilitiesView{}, apperror.New(apperror.CodeFailedPrecondition,
				"Run has more than one root Agent")
		}
		root = node
	}
	if root.ID == "" {
		if unavailableReason == "" {
			unavailableReason = "Run root Agent is unavailable"
		}
		root.Role = domain.AgentRoleRoot
		root.Profile = mode.Profile
	}
	if !permission.Mode.IsApprovalMode() {
		unavailableReason = "historical execution permission requires selecting Ask, Auto, or Full"
	}
	snapshot := toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{
		RunID: run.ID, MissionID: mission.ID, RootAgentID: root.ID,
		WorkspaceID: mission.WorkspaceID, RootFingerprint: rootFingerprint,
		Surface: mode.Surface, Phase: mode.Phase, Role: root.Role, Profile: root.Profile,
		PermissionMode: permission.Mode, ModeRevision: mode.Revision,
		PermissionRevision: permission.Revision, UnavailableReason: unavailableReason})
	tools := make([]AgentCodeToolCapabilityView, len(snapshot.Tools))
	for index, item := range snapshot.Tools {
		tools[index] = AgentCodeToolCapabilityView{Name: string(item.Name),
			Class: string(item.Class), Source: item.Source, ReadOnly: item.ReadOnly,
			Approval: item.Approval, Available: item.Available,
			RefusalReason: item.Refusal}
	}
	return AgentCodeCapabilitiesView{ProtocolVersion: snapshot.ProtocolVersion,
		Generation: snapshot.Generation, Surface: snapshot.Surface, Phase: snapshot.Phase,
		Role: snapshot.Role, Profile: snapshot.Profile,
		PermissionMode: snapshot.PermissionMode, Tools: tools}, nil
}

func (a *API) runEvents(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	events, err := a.store.ListRunEventsPage(request.Context(), runID,
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]EventView, len(events))
	for index := range events {
		views[index] = eventView(events[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) runActivity(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	limit := runactivity.MaxSourceEvents
	if raw, ok := singleQueryValue(values, "limit"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > runactivity.MaxSourceEvents {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				fmt.Sprintf("run activity limit must be between 1 and %d",
					runactivity.MaxSourceEvents))
		}
		limit = parsed
	}
	latest, err := a.store.LatestRunEventSequence(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	afterSequence := latest - int64(limit)
	if afterSequence < 0 {
		afterSequence = 0
	}
	source, err := a.store.ListRunEventsAfterSequence(request.Context(), runID,
		afterSequence, limit)
	if err != nil {
		return nil, nil, err
	}
	projection, err := runactivity.Build(runID, source, afterSequence > 0)
	if err != nil {
		return nil, nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Run activity could not be projected", err)
	}
	return runActivityView(projection), nil, nil
}

func (a *API) runWorkItems(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "status", "owner", "owner_agent_id"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	statuses, err := workItemStatuses(values["status"])
	if err != nil {
		return nil, nil, err
	}
	filter := domain.WorkItemFilter{RunID: runID, Statuses: statuses,
		Limit: pageRequest.Limit + 1, Offset: pageRequest.Offset}
	if raw, ok := singleQueryValue(values, "owner"); ok {
		if err := validateOptionalLabel(raw, "work item owner", domain.MaxWorkItemOwnerRunes); err != nil {
			return nil, nil, err
		}
		filter.Owner = raw
	}
	if raw, ok := singleQueryValue(values, "owner_agent_id"); ok {
		if !domain.ValidAgentID(raw) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"work item owner_agent_id filter is invalid")
		}
		filter.OwnerAgentID = raw
	}
	items, err := a.store.ListWorkItems(request.Context(), filter)
	if err != nil {
		return nil, nil, err
	}
	views := make([]WorkItemView, len(items))
	for index := range items {
		views[index] = workItemView(items[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) runNotes(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "status", "category",
		"visibility", "owner", "owner_agent_id", "tag", "pinned"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	filter := domain.NoteFilter{RunID: runID, Limit: pageRequest.Limit + 1, Offset: pageRequest.Offset}
	if filter.Statuses, err = noteStatuses(values["status"]); err != nil {
		return nil, nil, err
	}
	if filter.Categories, err = noteCategories(values["category"]); err != nil {
		return nil, nil, err
	}
	if filter.Visibilities, err = noteVisibilities(values["visibility"]); err != nil {
		return nil, nil, err
	}
	if raw, ok := singleQueryValue(values, "owner"); ok {
		if err := validateOptionalLabel(raw, "note owner", domain.MaxNoteOwnerRunes); err != nil {
			return nil, nil, err
		}
		filter.Owner = raw
	}
	if raw, ok := singleQueryValue(values, "owner_agent_id"); ok {
		if !domain.ValidAgentID(raw) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"note owner_agent_id filter is invalid")
		}
		filter.OwnerAgentID = raw
	}
	if filter.Tags, err = queryTokens(values["tag"], domain.MaxNoteTags, domain.MaxNoteTagRunes, "note tag"); err != nil {
		return nil, nil, err
	}
	if raw, ok := singleQueryValue(values, "pinned"); ok {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "note pinned filter must be true or false")
		}
		filter.Pinned = &value
	}
	notes, err := a.store.ListNotes(request.Context(), filter)
	if err != nil {
		return nil, nil, err
	}
	views := make([]NoteView, len(notes))
	for index := range notes {
		views[index] = noteView(notes[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) runArtifacts(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "source_id", "stream"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	filter := artifact.ListFilter{RunID: runID, Limit: pageRequest.Limit + 1, Offset: pageRequest.Offset}
	if raw, ok := singleQueryValue(values, "source_id"); ok {
		if err := validateIdentity(raw, "artifact source id"); err != nil {
			return nil, nil, err
		}
		filter.SourceID = raw
	}
	if raw, ok := singleQueryValue(values, "stream"); ok {
		filter.Stream = artifact.Stream(strings.ToLower(raw))
		if !filter.Stream.Valid() {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "invalid artifact stream filter")
		}
	}
	descriptors, err := a.store.ListRunArtifacts(request.Context(), filter)
	if err != nil {
		return nil, nil, err
	}
	views := make([]ArtifactView, len(descriptors))
	for index := range descriptors {
		views[index] = artifactView(descriptors[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) runToolRounds(request *http.Request, runID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	rounds, err := a.store.ListRunSupervisorToolRoundsPage(request.Context(), runID,
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	threadID := ""
	threadRecord, threadErr := a.store.GetThreadByRun(request.Context(), runID)
	if threadErr == nil {
		threadID = threadRecord.ID
	} else if apperror.CodeOf(apperror.Normalize(threadErr)) != apperror.CodeNotFound {
		return nil, nil, threadErr
	}
	detailService := application.NewThreadActivityDetailService(a.store).
		WithCommandRuntimeSource(a.commandActivitySource)
	views := make([]SupervisorToolRoundView, len(rounds))
	for index := range rounds {
		details := make(map[string]*ThreadActivityToolDetailView, len(rounds[index].Calls))
		if threadID != "" {
			for _, call := range rounds[index].Calls {
				if !application.SupportsThreadActivityDetail(call.ToolName) {
					continue
				}
				projected, projectionErr := detailService.Get(request.Context(), threadID, call.CallID)
				if projectionErr != nil {
					code := apperror.CodeOf(apperror.Normalize(projectionErr))
					if code == apperror.CodeNotFound || code == apperror.CodeFailedPrecondition {
						continue
					}
					return nil, nil, projectionErr
				}
				view := threadActivityDetailView(projected)
				if len(view.Tools) != 1 {
					continue
				}
				detail := view.Tools[0]
				details[call.CallID] = &detail
			}
		}
		views[index] = supervisorToolRoundView(rounds[index], threadID, details)
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) sessions(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parseStableListPage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := a.store.ListSessionsByCreationPage(request.Context(),
		pageRequest.Anchor.BeforeCreatedAt, pageRequest.Anchor.BeforeID,
		stableListStoreLimit(pageRequest))
	if err != nil {
		return nil, nil, err
	}
	views := make([]SessionView, len(sessions))
	for index := range sessions {
		views[index] = sessionView(sessions[index])
	}
	views, page := trimStableListPage(views, pageRequest, sessionStableListPosition)
	return views, page, nil
}

func (a *API) session(request *http.Request, sessionID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	sess, err := a.store.GetSession(request.Context(), sessionID)
	if err != nil {
		return nil, nil, err
	}
	detail := SessionDetailView{Session: sessionView(sess)}
	run, found, err := a.store.GetRunBySession(request.Context(), sess.ID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		view := runView(run)
		detail.Run = &view
	}
	return detail, nil, nil
}

func (a *API) sessionMessages(request *http.Request, sessionID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "include_compacted"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetSession(request.Context(), sessionID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	includeCompacted := false
	if raw, ok := singleQueryValue(values, "include_compacted"); ok {
		includeCompacted, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"include_compacted must be true or false")
		}
	}
	messages, err := a.store.ListSessionMessagesPage(request.Context(), sessionID, includeCompacted,
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]MessageView, len(messages))
	for index := range messages {
		views[index] = messageView(messages[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) workItem(request *http.Request, id string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	item, err := a.store.GetWorkItem(request.Context(), id)
	if err != nil {
		return nil, nil, err
	}
	return workItemView(item), nil, nil
}

func (a *API) note(request *http.Request, id string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	note, err := a.store.GetNote(request.Context(), id)
	if err != nil {
		return nil, nil, err
	}
	return noteView(note), nil, nil
}

func (a *API) artifact(request *http.Request, id string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	descriptor, err := a.store.GetRunArtifactDescriptor(request.Context(), id)
	if err != nil {
		return nil, nil, err
	}
	return artifactView(descriptor), nil, nil
}

func (a *API) writeSuccess(writer http.ResponseWriter, requestID string, data any, page *Page) {
	a.writeSuccessStatus(writer, requestID, data, page, http.StatusOK)
}

func (a *API) writeSuccessStatus(writer http.ResponseWriter, requestID string, data any, page *Page, status int) {
	encoded, err := json.Marshal(successEnvelope{Version: Version, RequestID: requestID, Data: data, Page: page})
	if err != nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInternal, "internal server error"), 0)
		return
	}
	if len(encoded) > MaxResponseBytes {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeResourceExhausted, "HTTP API response exceeds its limit"), 0)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}

func (a *API) writeOpenAPI(writer http.ResponseWriter, requestID string) {
	if len(a.openAPI) == 0 || len(a.openAPI) > MaxResponseBytes {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInternal, "internal server error"), 0)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.oai.openapi+json;version=3.1; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(a.openAPI)
}

func (a *API) writeError(writer http.ResponseWriter, requestID string, err error, statusOverride int) {
	classified := apperror.Normalize(err)
	code := apperror.CodeOf(classified)
	message := "internal server error"
	if code != apperror.CodeInternal {
		message = redact.String(strings.Join(strings.Fields(classified.Error()), " "))
		if runes := []rune(message); len(runes) > 1024 {
			message = string(runes[:1024])
		}
	}
	status := statusOverride
	if status == 0 {
		status = apperror.HTTPStatus(classified)
	}
	view := apiErrorView{Code: string(code), Message: message}
	var unchanged *domain.OperatorSteeringRevisionUnchangedError
	if errors.As(err, &unchanged) {
		view.Code, view.Message = string(apperror.CodeInvalidArgument), "修改后的正文与当前消息相同，未产生修订。"
		status = http.StatusBadRequest
		view.RevisionUnchanged = &QueueRevisionUnchangedView{Version: domain.QueueRevisionUnchangedProtocolVersion,
			RunID: unchanged.RunID, SessionID: unchanged.SessionID, MessageID: unchanged.MessageID, ExpectedRevision: unchanged.ExpectedRevision,
			OperationKeySHA256: unchanged.OperationKeySHA256, RequestContentSHA256: unchanged.RequestContentSHA256,
			NormalizedContentSHA256: unchanged.NormalizedContentSHA256, CurrentContentSHA256: unchanged.CurrentContentSHA256}
	}
	var notQueued *application.ThreadMessageNotQueuedError
	if errors.As(err, &notQueued) {
		value := false
		view.MessageQueued = &value
	}
	var turnFailed *application.ThreadTurnFailedError
	if errors.As(err, &turnFailed) {
		value := true
		view.TurnFailed = &value
		view.TurnFailure = threadTurnFailureReference(turnFailed.Failure)
	}
	if code == apperror.CodeConflict && errors.Is(err, domain.ErrStandardCodePresetThreadPreferenceChanged) {
		value := true
		view.OperationKeyInvalidated = &value
	}
	encoded, marshalErr := json.Marshal(errorEnvelope{Version: Version, RequestID: requestID,
		Error: view})
	if marshalErr != nil {
		encoded = []byte(`{"version":"api.v1","error":{"code":"INTERNAL","message":"internal server error"}}`)
		status = http.StatusInternalServerError
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}

func rejectQuery(values url.Values) error {
	if len(values) != 0 {
		return apperror.New(apperror.CodeInvalidArgument, "HTTP API endpoint does not accept query parameters")
	}
	return nil
}

func validatePathIdentity(value string) error {
	if value == "" || value == "." || value == ".." {
		return apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
	}
	return validateIdentity(value, "path identity")
}

func validateIdentity(value string, label string) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]rune(value)) > 256 || strings.ContainsAny(value, "/\\") {
		return apperror.New(apperror.CodeInvalidArgument, label+" is invalid")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return apperror.New(apperror.CodeInvalidArgument, label+" is invalid")
		}
	}
	return nil
}

func validateOptionalLabel(value string, label string, maxRunes int) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]rune(value)) > maxRunes {
		return apperror.New(apperror.CodeInvalidArgument, label+" is invalid")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return apperror.New(apperror.CodeInvalidArgument, label+" is invalid")
		}
	}
	return nil
}

func queryTokens(values []string, maxItems int, maxRunes int, label string) ([]string, error) {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if err := validateOptionalLabel(token, label, maxRunes); err != nil {
				return nil, err
			}
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			out = append(out, token)
			if len(out) > maxItems {
				return nil, apperror.New(apperror.CodeInvalidArgument,
					fmt.Sprintf("%s filter exceeds %d values", label, maxItems))
			}
		}
	}
	return out, nil
}

func workItemStatuses(values []string) ([]domain.WorkItemStatus, error) {
	tokens, err := queryTokens(values, 5, 32, "work item status")
	if err != nil {
		return nil, err
	}
	out := make([]domain.WorkItemStatus, len(tokens))
	for index, token := range tokens {
		status, err := domain.ParseWorkItemStatus(token)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid work item status filter")
		}
		out[index] = status
	}
	return out, nil
}

func noteStatuses(values []string) ([]domain.NoteStatus, error) {
	tokens, err := queryTokens(values, 2, 32, "note status")
	if err != nil {
		return nil, err
	}
	out := make([]domain.NoteStatus, len(tokens))
	for index, token := range tokens {
		status, err := domain.ParseNoteStatus(token)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid note status filter")
		}
		out[index] = status
	}
	return out, nil
}

func noteCategories(values []string) ([]domain.NoteCategory, error) {
	tokens, err := queryTokens(values, 5, 32, "note category")
	if err != nil {
		return nil, err
	}
	out := make([]domain.NoteCategory, len(tokens))
	for index, token := range tokens {
		category, err := domain.ParseNoteCategory(token)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid note category filter")
		}
		out[index] = category
	}
	return out, nil
}

func noteVisibilities(values []string) ([]domain.NoteVisibility, error) {
	tokens, err := queryTokens(values, 3, 32, "note visibility")
	if err != nil {
		return nil, err
	}
	out := make([]domain.NoteVisibility, len(tokens))
	for index, token := range tokens {
		visibility, err := domain.ParseNoteVisibility(token)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid note visibility filter")
		}
		out[index] = visibility
	}
	return out, nil
}
