package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/verification"
)

const (
	VerificationEvidencePathTemplate              = "/api/v1/runs/{run_id}/verification-evidence"
	VerificationPlanPathTemplate                  = "/api/v1/runs/{run_id}/verification-plan"
	VerificationAssociationPathTemplate           = "/api/v1/runs/{run_id}/verification-plan-associations"
	VerificationCoveragePathTemplate              = "/api/v1/runs/{run_id}/verification-plan-coverage"
	VerificationCoverageDetailPathTemplate        = "/api/v1/runs/{run_id}/verification-plan-coverage/{plan_id}/items/{ordinal}"
	VerificationSnapshotExportPathTemplate        = "/api/v1/runs/{run_id}/verification-plan-coverage/{plan_id}/items/{ordinal}/snapshot-export"
	VerificationSnapshotReceiptPathTemplate       = "/api/v1/runs/{run_id}/verification-snapshot-receipts"
	VerificationSnapshotReceiptReviewPathTemplate = "/api/v1/runs/{run_id}/verification-snapshot-receipt-reviews"
	CodeHandoffPathTemplate                       = "/api/v1/runs/{run_id}/code-handoff"
	CodeHandoffExportPathTemplate                 = "/api/v1/runs/{run_id}/code-handoff/export"
	MaxVerificationEvidenceBodyBytes              = 16 * 1024
	MaxVerificationPlanBodyBytes                  = 64 * 1024
	MaxVerificationAssociationBodyBytes           = 8 * 1024
	MaxVerificationSnapshotReceiptBodyBytes       = 8 * 1024
	MaxVerificationSnapshotReceiptReviewBodyBytes = 8 * 1024
)

func (a *API) workspaceRepositoryDiff(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	for key, values := range request.URL.Query() {
		if key != "after" || len(values) != 1 || values[0] == "" {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "repository diff accepts one after path")
		}
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	projection, err := repository.InspectDiff(request.Context(), registered.RootPath,
		registered.ID, repository.DiffOptions{After: request.URL.Query().Get("after")})
	if err != nil {
		return nil, nil, err
	}
	items := make([]RepositoryDiffItemView, len(projection.Items))
	for index, item := range projection.Items {
		items[index] = RepositoryDiffItemView{
			Path: item.Path, Staging: item.Staging, Worktree: item.Worktree,
			ContentState: item.ContentState, Patch: item.Patch, PatchBytes: item.PatchBytes,
			AddedLines: item.AddedLines, DeletedLines: item.DeletedLines,
			Redacted: item.Redacted, Truncated: item.Truncated,
			StatisticsComplete: item.StatisticsComplete,
		}
	}
	return RepositoryDiffView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		Kind: projection.Kind, Available: projection.Available, BaseHead: projection.BaseHead,
		Items: items, ReturnedCount: projection.ReturnedCount,
		NextAfter:    projection.NextAfter,
		OmittedCount: projection.OmittedCount, RedactionCount: projection.RedactionCount,
		TotalPatchBytes: projection.TotalPatchBytes, Truncated: projection.Truncated,
		ReadOnly: projection.ReadOnly, InstructionAuthorized: projection.InstructionAuthorized,
		MutationSupported:    projection.MutationSupported,
		AuthorityGranted:     projection.AuthorityGranted,
		RootPathExposed:      projection.RootPathExposed,
		RawContentIncluded:   projection.RawContentIncluded,
		PatchContentIncluded: projection.PatchContentIncluded,
		RemoteConfigIncluded: projection.RemoteConfigIncluded,
		ProcessStarted:       projection.ProcessStarted, NetworkUsed: projection.NetworkUsed,
		HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) workspaceRepositoryHistory(request *http.Request,
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
	projection, err := repository.InspectHistory(request.Context(), registered.RootPath,
		registered.ID)
	if err != nil {
		return nil, nil, err
	}
	commits := make([]RepositoryHistoryCommitView, len(projection.Commits))
	for index, commit := range projection.Commits {
		commits[index] = RepositoryHistoryCommitView{
			Hash: commit.Hash, ObjectID: commit.ObjectID, Subject: commit.Subject,
			ParentCount: commit.ParentCount,
			CommittedAt: commit.CommittedAt, Redacted: commit.Redacted,
			SubjectBounded: commit.SubjectBound,
		}
	}
	branches := make([]RepositoryHistoryBranchView, len(projection.Branches))
	for index, branch := range projection.Branches {
		branches[index] = RepositoryHistoryBranchView{
			Name: branch.Name, Head: branch.Head, Current: branch.Current,
		}
	}
	return RepositoryHistoryView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		Kind: projection.Kind, Available: projection.Available, Head: projection.Head,
		Detached: projection.Detached, Commits: commits, Branches: branches,
		ReturnedCommitCount: projection.ReturnedCommitCount,
		ReturnedBranchCount: projection.ReturnedBranchCount,
		OmittedBranchCount:  projection.OmittedBranchCount,
		RedactionCount:      projection.RedactionCount, Truncated: projection.Truncated,
		FirstParentOnly: projection.FirstParentOnly, ReadOnly: projection.ReadOnly,
		RootPathExposed:        projection.RootPathExposed,
		AuthorIdentityIncluded: projection.AuthorIdentityIncluded,
		CommitBodyIncluded:     projection.CommitBodyIncluded,
		RemoteConfigIncluded:   projection.RemoteConfigIncluded,
		ProcessStarted:         projection.ProcessStarted, NetworkUsed: projection.NetworkUsed,
		HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) workspaceRepositoryFileHistory(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "path"); err != nil {
		return nil, nil, err
	}
	paths, present := values["path"]
	if !present || len(paths) != 1 || paths[0] == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"repository file history path must appear exactly once")
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	projection, err := repository.InspectFileHistory(request.Context(), registered.RootPath,
		registered.ID, paths[0])
	if err != nil {
		return nil, nil, err
	}
	entries := make([]RepositoryFileHistoryEntryView, len(projection.Entries))
	for index, entry := range projection.Entries {
		entries[index] = RepositoryFileHistoryEntryView{
			ObjectID: entry.ObjectID, Hash: entry.Hash, Subject: entry.Subject,
			CommittedAt: entry.CommittedAt, Change: entry.Change,
			PreviousKind: entry.PreviousKind, CurrentKind: entry.CurrentKind,
			ContentChanged: entry.ContentChanged, ModeChanged: entry.ModeChanged,
			Redacted: entry.Redacted, SubjectBounded: entry.SubjectBounded,
		}
	}
	return RepositoryFileHistoryView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		Kind: projection.Kind, Available: projection.Available, Head: projection.Head,
		Path: projection.Path, Entries: entries,
		ScannedCommitCount: projection.ScannedCommitCount,
		ReturnedEntryCount: projection.ReturnedEntryCount,
		RedactionCount:     projection.RedactionCount, Observed: projection.Observed,
		Truncated: projection.Truncated, FirstParentOnly: projection.FirstParentOnly,
		RenameInferred: projection.RenameInferred, MetadataOnly: projection.MetadataOnly,
		ReadOnly: projection.ReadOnly, AuthorityGranted: projection.AuthorityGranted,
		RootPathExposed:        projection.RootPathExposed,
		AuthorIdentityIncluded: projection.AuthorIdentityIncluded,
		CommitBodyIncluded:     projection.CommitBodyIncluded,
		FileContentIncluded:    projection.FileContentIncluded,
		PatchIncluded:          projection.PatchIncluded,
		RemoteConfigIncluded:   projection.RemoteConfigIncluded,
		CheckoutPerformed:      projection.CheckoutPerformed,
		ReferenceUpdated:       projection.ReferenceUpdated,
		ProcessStarted:         projection.ProcessStarted, NetworkUsed: projection.NetworkUsed,
		HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) workspaceRepositoryCommit(request *http.Request,
	workspaceID string, objectID string,
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
	projection, err := repository.InspectCommitDetail(request.Context(),
		registered.RootPath, registered.ID, objectID)
	if err != nil {
		return nil, nil, err
	}
	changes := make([]RepositoryCommitFileChangeView, len(projection.Changes))
	for index, change := range projection.Changes {
		changes[index] = RepositoryCommitFileChangeView{
			Path: change.Path, Change: change.Change, PreviousKind: change.PreviousKind,
			CurrentKind: change.CurrentKind, ContentChanged: change.ContentChanged,
			ModeChanged: change.ModeChanged,
		}
	}
	return RepositoryCommitDetailView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		Kind: projection.Kind, Available: projection.Available, ObjectID: projection.ObjectID,
		Hash: projection.Hash, Subject: projection.Subject, CommittedAt: projection.CommittedAt,
		ParentCount: projection.ParentCount, Changes: changes,
		ChangedFileCount:    projection.ChangedFileCount,
		ReturnedChangeCount: projection.ReturnedChangeCount,
		OmittedChangeCount:  projection.OmittedChangeCount,
		RedactionCount:      projection.RedactionCount, Truncated: projection.Truncated,
		FirstParentOnly: projection.FirstParentOnly, ReadOnly: projection.ReadOnly,
		RootPathExposed:        projection.RootPathExposed,
		AuthorIdentityIncluded: projection.AuthorIdentityIncluded,
		CommitBodyIncluded:     projection.CommitBodyIncluded,
		FileContentIncluded:    projection.FileContentIncluded,
		PatchIncluded:          projection.PatchIncluded,
		RemoteConfigIncluded:   projection.RemoteConfigIncluded,
		CheckoutPerformed:      projection.CheckoutPerformed,
		ReferenceUpdated:       projection.ReferenceUpdated,
		ProcessStarted:         projection.ProcessStarted, NetworkUsed: projection.NetworkUsed,
		HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) workspaceRepositoryCommitComparison(request *http.Request,
	workspaceID string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "base_object_id", "head_object_id"); err != nil {
		return nil, nil, err
	}
	baseValues, basePresent := values["base_object_id"]
	headValues, headPresent := values["head_object_id"]
	if !basePresent || len(baseValues) != 1 || baseValues[0] == "" ||
		!headPresent || len(headValues) != 1 || headValues[0] == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"repository comparison object identities must appear exactly once")
	}
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	projection, err := repository.InspectCommitComparison(request.Context(),
		registered.RootPath, registered.ID, baseValues[0], headValues[0])
	if err != nil {
		return nil, nil, err
	}
	changes := make([]RepositoryCommitFileChangeView, len(projection.Changes))
	for index, change := range projection.Changes {
		changes[index] = RepositoryCommitFileChangeView{
			Path: change.Path, Change: change.Change, PreviousKind: change.PreviousKind,
			CurrentKind: change.CurrentKind, ContentChanged: change.ContentChanged,
			ModeChanged: change.ModeChanged,
		}
	}
	return RepositoryCommitComparisonView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		Kind: projection.Kind, Available: projection.Available,
		BaseObjectID: projection.BaseObjectID, BaseHash: projection.BaseHash,
		BaseSubject: projection.BaseSubject, BaseCommittedAt: projection.BaseCommittedAt,
		BaseRedacted: projection.BaseRedacted, BaseSubjectBounded: projection.BaseSubjectBounded,
		HeadObjectID: projection.HeadObjectID, HeadHash: projection.HeadHash,
		HeadSubject: projection.HeadSubject, HeadCommittedAt: projection.HeadCommittedAt,
		HeadRedacted: projection.HeadRedacted, HeadSubjectBounded: projection.HeadSubjectBounded,
		SameObject: projection.SameObject, Changes: changes,
		ChangedFileCount:    projection.ChangedFileCount,
		ReturnedChangeCount: projection.ReturnedChangeCount,
		OmittedChangeCount:  projection.OmittedChangeCount,
		RedactionCount:      projection.RedactionCount, Truncated: projection.Truncated,
		MetadataOnly: projection.MetadataOnly, ReadOnly: projection.ReadOnly,
		RenameInferred: projection.RenameInferred, AncestorRequired: projection.AncestorRequired,
		AuthorityGranted: projection.AuthorityGranted, RootPathExposed: projection.RootPathExposed,
		AuthorIdentityIncluded: projection.AuthorIdentityIncluded,
		CommitBodyIncluded:     projection.CommitBodyIncluded,
		FileContentIncluded:    projection.FileContentIncluded, PatchIncluded: projection.PatchIncluded,
		RemoteConfigIncluded: projection.RemoteConfigIncluded,
		CheckoutPerformed:    projection.CheckoutPerformed,
		ReferenceUpdated:     projection.ReferenceUpdated, ProcessStarted: projection.ProcessStarted,
		NetworkUsed: projection.NetworkUsed, HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) workspaceRepositoryCommitFilePreview(request *http.Request,
	workspaceID string, objectID string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "path"); err != nil {
		return nil, nil, err
	}
	paths, present := values["path"]
	if !present || len(paths) != 1 || paths[0] == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"repository commit file preview path must appear exactly once")
	}
	path := paths[0]
	registered, err := a.store.GetWorkspaceInfo(request.Context(), workspaceID)
	if err != nil {
		return nil, nil, apperror.Normalize(err)
	}
	if registered.ID != workspaceID {
		return nil, nil, apperror.New(apperror.CodeInternal,
			"workspace lookup returned a mismatched identity")
	}
	projection, err := repository.InspectCommitFilePreview(request.Context(),
		registered.RootPath, registered.ID, objectID, path)
	if err != nil {
		return nil, nil, err
	}
	return RepositoryCommitFilePreviewView{
		ProtocolVersion: projection.ProtocolVersion, WorkspaceID: projection.WorkspaceID,
		ObjectID: projection.ObjectID, Hash: projection.Hash, Path: projection.Path,
		Kind: projection.Kind, Content: projection.Content, TotalBytes: projection.TotalBytes,
		ReturnedBytes: projection.ReturnedBytes, RedactionCount: projection.RedactionCount,
		Redacted: projection.Redacted,
		Provenance: RepositoryCommitFilePreviewProvenanceView{
			Version: projection.Provenance.Version, SourceKind: projection.Provenance.SourceKind,
			SourceRef:             projection.Provenance.SourceRef,
			ContentSHA256:         projection.Provenance.ContentSHA256,
			InstructionAuthorized: projection.Provenance.InstructionAuthorized,
		},
		ReadOnly: projection.ReadOnly, MutationSupported: projection.MutationSupported,
		AuthorityGranted: projection.AuthorityGranted,
		RootPathExposed:  projection.RootPathExposed, RawBlobIncluded: projection.RawBlobIncluded,
		RedactedContentIncluded: projection.RedactedContentIncluded,
		RemoteConfigIncluded:    projection.RemoteConfigIncluded,
		CheckoutPerformed:       projection.CheckoutPerformed,
		ReferenceUpdated:        projection.ReferenceUpdated, ProcessStarted: projection.ProcessStarted,
		NetworkUsed: projection.NetworkUsed, HooksExecuted: projection.HooksExecuted,
	}, nil, nil
}

func (a *API) runVerificationEvidence(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	inventory, err := application.NewVerificationEvidenceService(a.store).Inventory(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]VerificationEvidenceItemView, len(inventory.Items))
	for index, value := range inventory.Items {
		items[index] = verificationEvidenceItemView(value)
	}
	return VerificationEvidenceInventoryView{
		ProtocolVersion: inventory.ProtocolVersion, RunID: inventory.RunID,
		SessionID: inventory.SessionID, WorkspaceID: inventory.WorkspaceID,
		Items: items, PassCount: inventory.PassCount, FailCount: inventory.FailCount,
		UnknownCount: inventory.UnknownCount, Truncated: inventory.Truncated,
	}, nil, nil
}

func (a *API) runVerificationPlans(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	inventory, err := application.NewVerificationPlanService(a.store).Inventory(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]VerificationPlanView, len(inventory.Items))
	for index, value := range inventory.Items {
		items[index] = verificationPlanView(value)
	}
	return VerificationPlanInventoryView{
		ProtocolVersion: inventory.ProtocolVersion, RunID: inventory.RunID,
		SessionID: inventory.SessionID, WorkspaceID: inventory.WorkspaceID,
		Items: items, Truncated: inventory.Truncated,
	}, nil, nil
}

func (a *API) runVerificationPlanCoverage(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	inventory, err := application.NewVerificationAssociationService(a.store).Coverage(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	plans := make([]VerificationPlanCoverageView, len(inventory.Plans))
	for index, plan := range inventory.Plans {
		items := make([]VerificationPlanItemCoverageView, len(plan.Items))
		for itemIndex, item := range plan.Items {
			items[itemIndex] = VerificationPlanItemCoverageView{
				Ordinal: item.Ordinal, ItemSHA256: item.ItemSHA256,
				AssociatedEvidenceCount: item.AssociatedEvidenceCount,
				PassCount:               item.PassCount, FailCount: item.FailCount,
				UnknownCount:                   item.UnknownCount,
				LatestAssociationEventSequence: item.LatestAssociationEventSequence,
			}
		}
		plans[index] = VerificationPlanCoverageView{
			PlanID: plan.PlanID, PlanSHA256: plan.PlanSHA256, ItemCount: plan.ItemCount,
			ObservedItemCount:       plan.ObservedItemCount,
			AssociatedEvidenceCount: plan.AssociatedEvidenceCount, Items: items,
		}
	}
	associations := make([]VerificationAssociationReferenceView, len(inventory.Associations))
	for index, association := range inventory.Associations {
		associations[index] = VerificationAssociationReferenceView{
			ID: association.ID, PlanID: association.PlanID,
			PlanItemOrdinal: association.PlanItemOrdinal,
			PlanItemSHA256:  association.PlanItemSHA256, EvidenceID: association.EvidenceID,
			EvidenceOutcome:          string(association.EvidenceOutcome),
			EvidenceEventSequence:    association.EvidenceEventSequence,
			AssociationEventSequence: association.AssociationSequence,
			AssociatedAt:             association.CreatedAt,
		}
	}
	return VerificationPlanCoverageInventoryView{
		ProtocolVersion: inventory.ProtocolVersion, RunID: inventory.RunID,
		SessionID: inventory.SessionID, WorkspaceID: inventory.WorkspaceID,
		Plans: plans, PlanCount: inventory.PlanCount, PlanItemCount: inventory.PlanItemCount,
		ObservedPlanItemCount:   inventory.ObservedPlanItemCount,
		AssociatedEvidenceCount: inventory.AssociatedEvidenceCount,
		Associations:            associations, PlansTruncated: inventory.PlansTruncated,
		AssociationsTruncated: inventory.AssociationsTruncated,
		MetadataOnly:          inventory.MetadataOnly, ReadOnly: inventory.ReadOnly,
		ResultInferred: inventory.ResultInferred, CommandExecuted: inventory.CommandExecuted,
		ModelAssertion: inventory.ModelAssertion, RecordRewritten: inventory.RecordRewritten,
		Approval: inventory.Approval, AuthorityGranted: inventory.AuthorityGranted,
	}, nil, nil
}

func (a *API) runVerificationPlanItemCoverage(request *http.Request, runID string,
	planID string, ordinalText string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parseVerificationCoveragePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	ordinal, err := strconv.Atoi(ordinalText)
	if err != nil {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"verification coverage item ordinal is invalid")
	}
	detail, err := application.NewVerificationCoverageDetailService(a.store).DetailPage(
		request.Context(), runID, planID, ordinal, pageRequest.Limit, pageRequest.Anchor)
	if err != nil {
		return nil, nil, err
	}
	associations := make([]VerificationAssociationReferenceView, len(detail.Associations))
	for index, association := range detail.Associations {
		associations[index] = VerificationAssociationReferenceView{
			ID: association.ID, PlanID: association.PlanID,
			PlanItemOrdinal: association.PlanItemOrdinal,
			PlanItemSHA256:  association.PlanItemSHA256, EvidenceID: association.EvidenceID,
			EvidenceOutcome:          string(association.EvidenceOutcome),
			EvidenceEventSequence:    association.EvidenceEventSequence,
			AssociationEventSequence: association.AssociationSequence,
			AssociatedAt:             association.CreatedAt,
		}
	}
	return VerificationPlanItemCoverageDetailView{
		ProtocolVersion: detail.ProtocolVersion, RunID: detail.RunID,
		SessionID: detail.SessionID, WorkspaceID: detail.WorkspaceID,
		PlanID: detail.PlanID, PlanSHA256: detail.PlanSHA256,
		PlanItemOrdinal: detail.PlanItemOrdinal, PlanItemSHA256: detail.PlanItemSHA256,
		AssociatedEvidenceCount: detail.AssociatedEvidenceCount,
		PassCount:               detail.PassCount, FailCount: detail.FailCount,
		UnknownCount:                   detail.UnknownCount,
		LatestAssociationEventSequence: detail.LatestAssociationEventSequence,
		Associations:                   associations, AssociationsTruncated: detail.AssociationsTruncated,
		MetadataOnly: detail.MetadataOnly, ReadOnly: detail.ReadOnly,
		PrivatePlanBodyIncluded:       detail.PrivatePlanBodyIncluded,
		PrivateEvidenceBodiesIncluded: detail.PrivateEvidenceBodiesIncluded,
		OperatorIdentityIncluded:      detail.OperatorIdentityIncluded,
		ResultInferred:                detail.ResultInferred, CommandExecuted: detail.CommandExecuted,
		ModelAssertion: detail.ModelAssertion, RecordRewritten: detail.RecordRewritten,
		Approval: detail.Approval, AuthorityGranted: detail.AuthorityGranted,
	}, verificationCoveragePage(detail, pageRequest), nil
}

func (a *API) runCodeHandoff(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	value, err := application.NewCodeHandoffService(a.store).
		WithStandardCodeDelivery(a.standardCodeDeliveryController).
		Build(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	return codeHandoffView(value), nil, nil
}

func (a *API) runVerificationPlanItemSnapshotExport(request *http.Request, runID string,
	planID string, ordinalText string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "format"); err != nil {
		return nil, nil, err
	}
	formats := values["format"]
	if len(formats) != 1 || formats[0] == "" || formats[0] != strings.TrimSpace(formats[0]) {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"verification snapshot export format must appear exactly once")
	}
	format := formats[0]
	ordinal, err := strconv.Atoi(ordinalText)
	if err != nil {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"verification snapshot item ordinal is invalid")
	}
	value, err := application.NewVerificationSnapshotExportService(a.store).Build(
		request.Context(), runID, planID, ordinal, format)
	if err != nil {
		return nil, nil, err
	}
	return VerificationSnapshotExportView{
		ProtocolVersion:         value.ProtocolVersion,
		SnapshotProtocolVersion: value.SnapshotProtocolVersion,
		Format:                  value.Format, Filename: value.Filename, MIMEType: value.MIMEType,
		RunID: value.RunID, SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		PlanID: value.PlanID, PlanSHA256: value.PlanSHA256,
		PlanItemOrdinal: value.PlanItemOrdinal, PlanItemSHA256: value.PlanItemSHA256,
		SnapshotHighWaterEventSequence: value.SnapshotHighWaterEventSequence,
		AssociatedEvidenceCount:        value.AssociatedEvidenceCount,
		PassCount:                      value.PassCount, FailCount: value.FailCount, UnknownCount: value.UnknownCount,
		ReturnedAssociationCount: value.ReturnedAssociationCount,
		AssociationsTruncated:    value.AssociationsTruncated,
		ContentSHA256:            value.ContentSHA256, ContentBytes: value.ContentBytes,
		Content: value.Content, MetadataOnly: value.MetadataOnly, ReadOnly: value.ReadOnly,
		DownloadOnly:                  value.DownloadOnly,
		PrivatePlanBodyIncluded:       value.PrivatePlanBodyIncluded,
		PrivateEvidenceBodiesIncluded: value.PrivateEvidenceBodiesIncluded,
		OperatorIdentityIncluded:      value.OperatorIdentityIncluded,
		ResultInferred:                value.ResultInferred, CommandExecuted: value.CommandExecuted,
		ModelAssertion: value.ModelAssertion, RecordRewritten: value.RecordRewritten,
		Approval: value.Approval, AuthorityGranted: value.AuthorityGranted,
		MutationSupported: value.MutationSupported, ExecutionStarted: value.ExecutionStarted,
	}, nil, nil
}

func (a *API) runVerificationSnapshotReceipts(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	inventory, err := application.NewVerificationSnapshotReceiptService(a.store).Inventory(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]VerificationSnapshotReceiptView, len(inventory.Items))
	for index, value := range inventory.Items {
		items[index] = verificationSnapshotReceiptView(value)
	}
	return VerificationSnapshotReceiptInventoryView{
		ProtocolVersion: inventory.ProtocolVersion, RunID: inventory.RunID,
		SessionID: inventory.SessionID, WorkspaceID: inventory.WorkspaceID,
		Items: items, Truncated: inventory.Truncated, MetadataOnly: inventory.MetadataOnly,
		ReadOnly: inventory.ReadOnly, SnapshotAccepted: inventory.SnapshotAccepted,
		ResultAccepted: inventory.ResultAccepted, ResultInferred: inventory.ResultInferred,
		RecordRewritten: inventory.RecordRewritten, Approval: inventory.Approval,
		AuthorityGranted: inventory.AuthorityGranted, ExecutionStarted: inventory.ExecutionStarted,
	}, nil, nil
}

func (a *API) runVerificationSnapshotReceiptReviews(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	inventory, err := application.NewVerificationSnapshotReceiptReviewService(a.store).Inventory(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]VerificationSnapshotReceiptReviewView, len(inventory.Items))
	for index, value := range inventory.Items {
		items[index] = verificationSnapshotReceiptReviewView(value)
	}
	return VerificationSnapshotReceiptReviewInventoryView{
		ProtocolVersion: inventory.ProtocolVersion, RunID: inventory.RunID,
		SessionID: inventory.SessionID, WorkspaceID: inventory.WorkspaceID,
		Items: items, Truncated: inventory.Truncated, MetadataOnly: inventory.MetadataOnly,
		ReadOnly: inventory.ReadOnly, ReviewNonAuthorizing: inventory.ReviewNonAuthorizing,
		SnapshotAccepted: inventory.SnapshotAccepted, ResultAccepted: inventory.ResultAccepted,
		ResultInferred: inventory.ResultInferred, RecordRewritten: inventory.RecordRewritten,
		Approval: inventory.Approval, AuthorityGranted: inventory.AuthorityGranted,
		ExecutionStarted: inventory.ExecutionStarted,
	}, nil, nil
}

func (a *API) runCodeHandoffExport(request *http.Request,
	runID string,
) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "format"); err != nil {
		return nil, nil, err
	}
	formats := values["format"]
	if len(formats) != 1 || formats[0] == "" || formats[0] != strings.TrimSpace(formats[0]) {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"Code handoff export format must appear exactly once")
	}
	format := formats[0]
	value, err := application.NewCodeHandoffExportService(a.store).
		WithStandardCodeDelivery(a.standardCodeDeliveryController).Build(
		request.Context(), runID, format)
	if err != nil {
		return nil, nil, err
	}
	return CodeHandoffExportView{
		ProtocolVersion: value.ProtocolVersion, Format: value.Format,
		Filename: value.Filename, MIMEType: value.MIMEType, RunID: value.RunID,
		SourceEventSequence: value.SourceEventSequence, GeneratedAt: value.GeneratedAt,
		ContentSHA256: value.ContentSHA256, ContentBytes: value.ContentBytes,
		Content: value.Content, ReadOnly: value.ReadOnly, DownloadOnly: value.DownloadOnly,
		PrivateBodies: value.PrivateBodies, ResumeAuthorized: value.ResumeAuthorized,
		MutationSupported: value.MutationSupported, ReportAcceptance: value.ReportAcceptance,
		ExecutionStarted: value.ExecutionStarted,
	}, nil, nil
}

func matchVerificationEvidencePath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/verification-evidence"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func matchVerificationPlanPath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/verification-plan"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func matchVerificationAssociationPath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/verification-plan-associations"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func matchVerificationSnapshotReceiptPath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/verification-snapshot-receipts"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func matchVerificationSnapshotReceiptReviewPath(requestPath string) (string, bool) {
	const prefix = "/api/v1/runs/"
	const suffix = "/verification-snapshot-receipt-reviews"
	if !strings.HasPrefix(requestPath, prefix) || !strings.HasSuffix(requestPath, suffix) {
		return "", false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(requestPath, prefix), suffix)
	return runID, runID != "" && !strings.Contains(runID, "/")
}

func (a *API) serveVerificationEvidenceControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Verification evidence"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.verificationEvidenceEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxVerificationEvidenceBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view VerificationEvidenceRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := application.NewVerificationEvidenceService(a.store).Record(
		request.Context(), application.RecordVerificationEvidenceRequest{
			Version: view.Version, RunID: runID, Outcome: view.Outcome,
			Title: view.Title, Summary: view.Summary, OperationKey: operationKey,
			RecordedBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, apperror.Normalize(err), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID,
		verificationEvidenceControlView(result.Evidence, result.Replayed), nil,
		http.StatusAccepted)
}

func (a *API) serveVerificationPlanControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Verification plan"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.verificationEvidenceEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxVerificationPlanBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view VerificationPlanRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	items := make([]application.VerificationPlanItemRequest, len(view.Items))
	for index, item := range view.Items {
		items[index] = application.VerificationPlanItemRequest{
			Title: item.Title, ExpectedObservation: item.ExpectedObservation,
		}
	}
	result, err := application.NewVerificationPlanService(a.store).Record(
		request.Context(), application.RecordVerificationPlanRequest{
			Version: view.Version, RunID: runID, Title: view.Title, Summary: view.Summary,
			Items: items, OperationKey: operationKey, AuthoredBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, apperror.Normalize(err), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID,
		verificationPlanControlView(result.Plan, result.Replayed), nil, http.StatusAccepted)
}

func (a *API) serveVerificationAssociationControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Verification association"
	if request.Method != http.MethodPost {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInvalidArgument, "HTTP method is not supported"),
			http.StatusMethodNotAllowed)
		return
	}
	if !a.authorizeRunOperation(writer, request, requestID,
		a.verificationEvidenceEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxVerificationAssociationBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view VerificationAssociationRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := application.NewVerificationAssociationService(a.store).Record(
		request.Context(), application.RecordVerificationAssociationRequest{
			Version: view.Version, RunID: runID, PlanID: view.PlanID,
			PlanItemOrdinal: view.PlanItemOrdinal, EvidenceID: view.EvidenceID,
			OperationKey: operationKey, AssociatedBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, apperror.Normalize(err), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID,
		verificationAssociationControlView(result.Association, result.Replayed), nil,
		http.StatusAccepted)
}

func (a *API) serveVerificationSnapshotReceiptControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Verification snapshot receipt"
	if request.Method != http.MethodPost {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInvalidArgument, "HTTP method is not supported"),
			http.StatusMethodNotAllowed)
		return
	}
	if !a.authorizeRunOperation(writer, request, requestID,
		a.verificationEvidenceEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxVerificationSnapshotReceiptBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view VerificationSnapshotReceiptRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := application.NewVerificationSnapshotReceiptService(a.store).Record(
		request.Context(), application.RecordVerificationSnapshotReceiptRequest{
			Version: view.Version, RunID: runID, PlanID: view.PlanID,
			PlanItemOrdinal: view.PlanItemOrdinal, Format: view.Format,
			SnapshotHighWaterEventSequence: view.SnapshotHighWaterEventSequence,
			ContentSHA256:                  view.ContentSHA256,
			ConfirmMetadataSnapshot:        view.ConfirmMetadataSnapshot,
			OperationKey:                   operationKey, RecordedBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, apperror.Normalize(err), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID,
		verificationSnapshotReceiptControlView(result.Receipt, result.Replayed), nil,
		http.StatusAccepted)
}

func (a *API) serveVerificationSnapshotReceiptReviewControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	const label = "Verification snapshot receipt review"
	if request.Method != http.MethodPost {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInvalidArgument, "HTTP method is not supported"),
			http.StatusMethodNotAllowed)
		return
	}
	if !a.authorizeRunOperation(writer, request, requestID,
		a.verificationEvidenceEnabled, label) {
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxVerificationSnapshotReceiptReviewBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view VerificationSnapshotReceiptReviewRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := application.NewVerificationSnapshotReceiptReviewService(a.store).Record(
		request.Context(), application.RecordVerificationSnapshotReceiptReviewRequest{
			Version: view.Version, RunID: runID, ReceiptID: view.ReceiptID,
			ReceiptContentSHA256: view.ReceiptContentSHA256,
			ReceiptEventSequence: view.ReceiptEventSequence, Decision: view.Decision,
			ConfirmNonAuthorizingReview: view.ConfirmNonAuthorizingReview,
			OperationKey:                operationKey, ReviewedBy: "http_run_operator",
		})
	if err != nil {
		a.writeError(writer, requestID, apperror.Normalize(err), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID,
		verificationSnapshotReceiptReviewControlView(result.Review, result.Replayed), nil,
		http.StatusAccepted)
}

func verificationSnapshotReceiptView(
	value verification.SnapshotReceipt,
) VerificationSnapshotReceiptView {
	return VerificationSnapshotReceiptView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		PlanID: value.PlanID, PlanSHA256: value.PlanSHA256,
		PlanItemOrdinal: value.PlanItemOrdinal, PlanItemSHA256: value.PlanItemSHA256,
		Format:                         value.Format,
		SnapshotHighWaterEventSequence: value.SnapshotHighWaterEventSequence,
		AssociatedEvidenceCount:        value.AssociatedEvidenceCount,
		PassCount:                      value.PassCount, FailCount: value.FailCount, UnknownCount: value.UnknownCount,
		ReturnedAssociationCount: value.ReturnedAssociationCount,
		AssociationsTruncated:    value.AssociationsTruncated,
		ContentSHA256:            value.ContentSHA256, ContentBytes: value.ContentBytes,
		ReceiptEventSequence: value.EventSequence, RecordedAt: value.CreatedAt,
		Immutable: true, OperatorRecorded: true, MetadataOnly: true, ReadOnly: true,
	}
}

func verificationSnapshotReceiptControlView(value verification.SnapshotReceipt,
	replayed bool,
) VerificationSnapshotReceiptControlView {
	view := verificationSnapshotReceiptView(value)
	return VerificationSnapshotReceiptControlView{
		ProtocolVersion: view.ProtocolVersion, ID: view.ID, RunID: view.RunID,
		SessionID: view.SessionID, WorkspaceID: view.WorkspaceID,
		PlanID: view.PlanID, PlanSHA256: view.PlanSHA256,
		PlanItemOrdinal: view.PlanItemOrdinal, PlanItemSHA256: view.PlanItemSHA256,
		Format:                         view.Format,
		SnapshotHighWaterEventSequence: view.SnapshotHighWaterEventSequence,
		AssociatedEvidenceCount:        view.AssociatedEvidenceCount,
		PassCount:                      view.PassCount, FailCount: view.FailCount, UnknownCount: view.UnknownCount,
		ReturnedAssociationCount: view.ReturnedAssociationCount,
		AssociationsTruncated:    view.AssociationsTruncated,
		ContentSHA256:            view.ContentSHA256, ContentBytes: view.ContentBytes,
		ReceiptEventSequence: view.ReceiptEventSequence, RecordedAt: view.RecordedAt,
		Immutable: view.Immutable, OperatorRecorded: view.OperatorRecorded,
		MetadataOnly: view.MetadataOnly, ReadOnly: view.ReadOnly,
		ContentIncluded: view.ContentIncluded, PrivateBodiesIncluded: view.PrivateBodiesIncluded,
		OperatorIdentityIncluded: view.OperatorIdentityIncluded,
		SnapshotAccepted:         view.SnapshotAccepted, ResultAccepted: view.ResultAccepted,
		ResultInferred: view.ResultInferred, RecordRewritten: view.RecordRewritten,
		Approval: view.Approval, AuthorityGranted: view.AuthorityGranted,
		ExecutionStarted: view.ExecutionStarted, Replayed: replayed,
	}
}

func verificationSnapshotReceiptReviewView(
	value verification.SnapshotReceiptReview,
) VerificationSnapshotReceiptReviewView {
	return VerificationSnapshotReceiptReviewView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID, ReceiptID: value.ReceiptID,
		ReceiptContentSHA256: value.ReceiptContentSHA256,
		ReceiptEventSequence: value.ReceiptEventSequence, Decision: string(value.Decision),
		ReviewEventSequence: value.EventSequence, ReviewedAt: value.CreatedAt,
		Immutable: true, OperatorReviewed: true, MetadataOnly: true, ReadOnly: true,
		ReviewNonAuthorizing: true,
	}
}

func verificationSnapshotReceiptReviewControlView(value verification.SnapshotReceiptReview,
	replayed bool,
) VerificationSnapshotReceiptReviewControlView {
	view := verificationSnapshotReceiptReviewView(value)
	return VerificationSnapshotReceiptReviewControlView{
		ProtocolVersion: view.ProtocolVersion, ID: view.ID, RunID: view.RunID,
		SessionID: view.SessionID, WorkspaceID: view.WorkspaceID, ReceiptID: view.ReceiptID,
		ReceiptContentSHA256: view.ReceiptContentSHA256,
		ReceiptEventSequence: view.ReceiptEventSequence, Decision: view.Decision,
		ReviewEventSequence: view.ReviewEventSequence, ReviewedAt: view.ReviewedAt,
		Immutable: view.Immutable, OperatorReviewed: view.OperatorReviewed,
		MetadataOnly: view.MetadataOnly, ReadOnly: view.ReadOnly,
		ReviewNonAuthorizing: view.ReviewNonAuthorizing,
		ContentIncluded:      view.ContentIncluded, PrivateBodiesIncluded: view.PrivateBodiesIncluded,
		OperatorIdentityIncluded: view.OperatorIdentityIncluded,
		SnapshotAccepted:         view.SnapshotAccepted, ResultAccepted: view.ResultAccepted,
		ResultInferred: view.ResultInferred, RecordRewritten: view.RecordRewritten,
		Approval: view.Approval, AuthorityGranted: view.AuthorityGranted,
		ExecutionStarted: view.ExecutionStarted, Replayed: replayed,
	}
}

func verificationAssociationControlView(value verification.PlanEvidenceAssociation,
	replayed bool,
) VerificationAssociationControlView {
	return VerificationAssociationControlView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID, PlanID: value.PlanID,
		PlanItemOrdinal: value.PlanItemOrdinal, PlanItemSHA256: value.PlanItemSHA256,
		EvidenceID: value.EvidenceID, EvidenceOutcome: string(value.EvidenceOutcome),
		EvidenceEventSequence:    value.EvidenceEventSequence,
		AssociationEventSequence: value.EventSequence, AssociatedAt: value.CreatedAt,
		Immutable: true, OperatorSupplied: true, MetadataOnly: true,
		Replayed: replayed,
	}
}

func verificationEvidenceItemView(value verification.Evidence) VerificationEvidenceItemView {
	return VerificationEvidenceItemView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		Outcome: string(value.Outcome), Title: value.Title, Summary: value.Summary,
		SummarySHA256: value.SummarySHA256, Redacted: value.Redacted,
		RecordedAt: value.CreatedAt, Immutable: true, OperatorSupplied: true,
	}
}

func verificationEvidenceControlView(value verification.Evidence,
	replayed bool,
) VerificationEvidenceControlView {
	return VerificationEvidenceControlView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		Outcome: string(value.Outcome), Title: value.Title, Summary: value.Summary,
		SummarySHA256: value.SummarySHA256, Redacted: value.Redacted,
		RecordedAt: value.CreatedAt, Immutable: true, OperatorSupplied: true,
		Replayed: replayed,
	}
}

func verificationPlanItemsView(value verification.Plan) []VerificationPlanItemView {
	items := make([]VerificationPlanItemView, len(value.Items))
	for index, item := range value.Items {
		items[index] = VerificationPlanItemView{
			Ordinal: item.Ordinal, Title: item.Title,
			ExpectedObservation: item.ExpectedObservation,
			ItemSHA256:          item.ItemSHA256, Redacted: item.Redacted,
		}
	}
	return items
}

func verificationPlanView(value verification.Plan) VerificationPlanView {
	return VerificationPlanView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		Title: value.Title, Summary: value.Summary, PlanSHA256: value.PlanSHA256,
		Redacted: value.Redacted, CreatedAt: value.CreatedAt,
		Items: verificationPlanItemsView(value), ItemCount: len(value.Items),
		Immutable: true, OperatorSupplied: true, GuidanceOnly: true,
	}
}

func verificationPlanControlView(value verification.Plan,
	replayed bool,
) VerificationPlanControlView {
	return VerificationPlanControlView{
		ProtocolVersion: value.ProtocolVersion, ID: value.ID, RunID: value.RunID,
		SessionID: value.SessionID, WorkspaceID: value.WorkspaceID,
		Title: value.Title, Summary: value.Summary, PlanSHA256: value.PlanSHA256,
		Redacted: value.Redacted, CreatedAt: value.CreatedAt,
		Items: verificationPlanItemsView(value), ItemCount: len(value.Items),
		Immutable: true, OperatorSupplied: true, GuidanceOnly: true, Replayed: replayed,
	}
}

func codeHandoffView(value application.CodeHandoff) CodeHandoffView {
	verificationReferences := make([]CodeHandoffVerificationReferenceView,
		len(value.Verification.References))
	for index, reference := range value.Verification.References {
		verificationReferences[index] = CodeHandoffVerificationReferenceView{
			ID: reference.ID, Outcome: string(reference.Outcome), Redacted: reference.Redacted,
			RecordedAt: reference.CreatedAt,
		}
	}
	verificationPlanReferences := make([]CodeHandoffVerificationPlanReferenceView,
		len(value.VerificationPlans.References))
	for index, reference := range value.VerificationPlans.References {
		verificationPlanReferences[index] = CodeHandoffVerificationPlanReferenceView{
			ID: reference.ID, PlanSHA256: reference.PlanSHA256,
			ItemCount: reference.ItemCount, Redacted: reference.Redacted,
			CreatedAt: reference.CreatedAt,
		}
	}
	coverageItems := make([]CodeHandoffVerificationCoverageItemView,
		len(value.VerificationCoverage.Items))
	for index, item := range value.VerificationCoverage.Items {
		coverageItems[index] = CodeHandoffVerificationCoverageItemView{
			PlanID: item.PlanID, PlanSHA256: item.PlanSHA256, Ordinal: item.Ordinal,
			ItemSHA256:              item.ItemSHA256,
			AssociatedEvidenceCount: item.AssociatedEvidenceCount,
			PassCount:               item.PassCount, FailCount: item.FailCount,
			UnknownCount:                   item.UnknownCount,
			LatestAssociationEventSequence: item.LatestAssociationEventSequence,
		}
	}
	reviewReferences := make([]CodeHandoffSnapshotReceiptReviewReferenceView,
		len(value.VerificationSnapshotReceiptReviews.References))
	for index, reference := range value.VerificationSnapshotReceiptReviews.References {
		reviewReferences[index] = CodeHandoffSnapshotReceiptReviewReferenceView{
			ID: reference.ID, ReceiptID: reference.ReceiptID,
			ReceiptContentSHA256: reference.ReceiptContentSHA256,
			ReceiptEventSequence: reference.ReceiptEventSequence,
			Decision:             string(reference.Decision), ReviewEventSequence: reference.ReviewEventSequence,
			ReviewedAt: reference.ReviewedAt,
		}
	}
	actions := make([]CodeHandoffActionReferenceView, len(value.PendingActions))
	for index, action := range value.PendingActions {
		actions[index] = CodeHandoffActionReferenceView{
			ID: action.ID, Kind: action.Kind, State: action.State,
			Destination: action.Destination, AvailableAt: action.AvailableAt, DueAt: action.DueAt,
		}
	}
	reports := make([]CodeHandoffReportReferenceView, len(value.ReportReferences))
	for index, report := range value.ReportReferences {
		reports[index] = CodeHandoffReportReferenceView{
			ID: report.ID, Status: string(report.Status), FindingCount: report.FindingCount,
			CreatedAt: report.CreatedAt,
		}
	}
	return CodeHandoffView{
		ProtocolVersion: value.ProtocolVersion, RunID: value.RunID,
		MissionID: value.MissionID, SessionID: value.SessionID,
		WorkspaceID: value.WorkspaceID, RunStatus: string(value.RunStatus),
		Surface: string(value.Surface), Phase: string(value.Phase),
		ModeRevision: value.ModeRevision, SourceEventSequence: value.SourceEventSequence,
		GeneratedAt: value.GeneratedAt,
		Plan: CodeHandoffPlanView{
			State: value.Plan.State, ProposalID: value.Plan.ProposalID,
			SelectionID: value.Plan.SelectionID, DirectionCount: value.Plan.DirectionCount,
			SelectedDirection: value.Plan.SelectedDirection, ModuleCount: value.Plan.ModuleCount,
			ManualAcceptance: value.Plan.ManualAcceptance,
			PendingCount:     value.Plan.PendingCount, InProgressCount: value.Plan.InProgressCount,
			BlockedCount: value.Plan.BlockedCount, CompletedCount: value.Plan.CompletedCount,
			CancelledCount: value.Plan.CancelledCount,
		},
		Queue: CodeHandoffQueueView{Pending: value.Queue.Pending, Prepared: value.Queue.Prepared,
			Committed: value.Queue.Committed, Cancelled: value.Queue.Cancelled},
		ChangeSet: CodeHandoffChangeSetView{
			Proposed: value.ChangeSet.Proposed, Approved: value.ChangeSet.Approved,
			Applied: value.ChangeSet.Applied, Denied: value.ChangeSet.Denied,
			Failed: value.ChangeSet.Failed, ReturnedCount: value.ChangeSet.ReturnedCount,
			TotalDiffBytes: value.ChangeSet.TotalDiffBytes, Truncated: value.ChangeSet.Truncated,
		},
		Verification: CodeHandoffVerificationView{
			PassCount: value.Verification.PassCount, FailCount: value.Verification.FailCount,
			UnknownCount:  value.Verification.UnknownCount,
			ReturnedCount: value.Verification.ReturnedCount,
			Truncated:     value.Verification.Truncated, References: verificationReferences,
		},
		VerificationPlans: CodeHandoffVerificationPlansView{
			ReturnedCount: value.VerificationPlans.ReturnedCount,
			Truncated:     value.VerificationPlans.Truncated,
			References:    verificationPlanReferences,
		},
		VerificationCoverage: CodeHandoffVerificationCoverageView{
			ProtocolVersion:         value.VerificationCoverage.ProtocolVersion,
			PlanCount:               value.VerificationCoverage.PlanCount,
			PlanItemCount:           value.VerificationCoverage.PlanItemCount,
			ObservedPlanItemCount:   value.VerificationCoverage.ObservedPlanItemCount,
			UnobservedPlanItemCount: value.VerificationCoverage.UnobservedPlanItemCount,
			AssociatedEvidenceCount: value.VerificationCoverage.AssociatedEvidenceCount,
			ContradictoryItemCount:  value.VerificationCoverage.ContradictoryItemCount,
			ReturnedItemCount:       value.VerificationCoverage.ReturnedItemCount,
			Truncated:               value.VerificationCoverage.Truncated,
			Items:                   coverageItems, MetadataOnly: value.VerificationCoverage.MetadataOnly,
			ReadOnly:              value.VerificationCoverage.ReadOnly,
			ResultInferred:        value.VerificationCoverage.ResultInferred,
			PrivateBodiesIncluded: value.VerificationCoverage.PrivateBodiesIncluded,
		},
		VerificationSnapshotReceiptReviews: CodeHandoffSnapshotReceiptReviewsView{
			ProtocolVersion:          value.VerificationSnapshotReceiptReviews.ProtocolVersion,
			MetadataConfirmedCount:   value.VerificationSnapshotReceiptReviews.MetadataConfirmedCount,
			MetadataDisputedCount:    value.VerificationSnapshotReceiptReviews.MetadataDisputedCount,
			ReturnedCount:            value.VerificationSnapshotReceiptReviews.ReturnedCount,
			Truncated:                value.VerificationSnapshotReceiptReviews.Truncated,
			References:               reviewReferences,
			MetadataOnly:             value.VerificationSnapshotReceiptReviews.MetadataOnly,
			ReadOnly:                 value.VerificationSnapshotReceiptReviews.ReadOnly,
			ReviewNonAuthorizing:     value.VerificationSnapshotReceiptReviews.ReviewNonAuthorizing,
			ContentIncluded:          value.VerificationSnapshotReceiptReviews.ContentIncluded,
			PrivateBodiesIncluded:    value.VerificationSnapshotReceiptReviews.PrivateBodiesIncluded,
			OperatorIdentityIncluded: value.VerificationSnapshotReceiptReviews.OperatorIdentityIncluded,
			SnapshotAccepted:         value.VerificationSnapshotReceiptReviews.SnapshotAccepted,
			ResultAccepted:           value.VerificationSnapshotReceiptReviews.ResultAccepted,
			ResultInferred:           value.VerificationSnapshotReceiptReviews.ResultInferred,
			RecordRewritten:          value.VerificationSnapshotReceiptReviews.RecordRewritten,
			Approval:                 value.VerificationSnapshotReceiptReviews.Approval,
			AuthorityGranted:         value.VerificationSnapshotReceiptReviews.AuthorityGranted,
			ExecutionStarted:         value.VerificationSnapshotReceiptReviews.ExecutionStarted,
		},
		PendingActionCount:      value.PendingActionCount,
		PendingActionsTruncated: value.PendingActionsTruncated, PendingActions: actions,
		ReportReferencesTruncated: value.ReportReferencesTruncated,
		ReportReferences:          reports, Regenerable: value.Regenerable,
		DurableSources:        value.DurableSources,
		PrivateBodiesIncluded: value.PrivateBodiesIncluded,
		CompositeMutation:     value.CompositeMutation, ResumeAuthorized: value.ResumeAuthorized,
		ExecutionStarted:     value.ExecutionStarted,
		StandardCodeDelivery: value.StandardCodeDelivery,
		HostCommands:         value.HostCommands,
	}
}
