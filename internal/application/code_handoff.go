package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/verification"
)

const (
	CodeHandoffProtocolVersion      = "code_handoff.v1"
	MaxCodeHandoffActionReferences  = 20
	MaxCodeHandoffReportReferences  = 20
	MaxCodeHandoffVerifyReferences  = 20
	MaxCodeHandoffVerifyPlanRefs    = 20
	MaxCodeHandoffCoverageItemRefs  = 100
	MaxCodeHandoffReceiptReviewRefs = 20
	maxCodeHandoffSnapshotAttempts  = 4
)

type CodeHandoffStore interface {
	OperatorActionCenterStore
	VerificationCoverageStore
	LatestRunEventSequence(context.Context, string) (int64, error)
	ListPlanDeliveryProposals(context.Context, string, int) ([]domain.PlanDeliveryProposal, error)
	GetPlanDeliveryProposal(context.Context, string) (domain.PlanDeliveryProposal, error)
	GetPlanDeliverySelectionByRun(context.Context, string) (domain.PlanDeliverySelection, bool, error)
	GetWorkItem(context.Context, string) (domain.WorkItem, error)
	GetOperatorSteeringQueueSummary(context.Context, string) (domain.OperatorSteeringQueueSummary, error)
	ListFileEditPreviewsPage(context.Context, fileedit.ListFilter, int, int) ([]fileedit.Preview, error)
	ListVerificationEvidence(context.Context, string, int) ([]verification.Evidence, error)
	ListVerificationSnapshotReceiptReviews(context.Context, string, int) (
		[]verification.SnapshotReceiptReview, error)
	ListFindingReportSummariesPage(context.Context, string, int, int) ([]domain.FindingReportSummary, error)
}

type CodeHandoffService struct {
	store    CodeHandoffStore
	now      func() time.Time
	delivery StandardCodeDeliveryProjector
}

type StandardCodeDeliveryProjector interface {
	Current(context.Context, string) (standardcodedelivery.Report, bool, error)
}

type CodeHandoffPlan struct {
	State             string `json:"state"`
	ProposalID        string `json:"proposal_id"`
	SelectionID       string `json:"selection_id"`
	DirectionCount    int    `json:"direction_count"`
	SelectedDirection int    `json:"selected_direction"`
	ManualAcceptance  string `json:"manual_acceptance,omitempty"`
	ModuleCount       int    `json:"module_count"`
	PendingCount      int    `json:"pending_count"`
	InProgressCount   int    `json:"in_progress_count"`
	BlockedCount      int    `json:"blocked_count"`
	CompletedCount    int    `json:"completed_count"`
	CancelledCount    int    `json:"cancelled_count"`
}

type CodeHandoffQueue struct {
	Pending   int `json:"pending"`
	Prepared  int `json:"prepared"`
	Committed int `json:"committed"`
	Cancelled int `json:"cancelled"`
}

type CodeHandoffChangeSet struct {
	Proposed       int  `json:"proposed"`
	Approved       int  `json:"approved"`
	Applied        int  `json:"applied"`
	Denied         int  `json:"denied"`
	Failed         int  `json:"failed"`
	ReturnedCount  int  `json:"returned_count"`
	TotalDiffBytes int  `json:"total_diff_bytes"`
	Truncated      bool `json:"truncated"`
}

type CodeHandoffVerificationReference struct {
	ID        string               `json:"id"`
	Outcome   verification.Outcome `json:"outcome"`
	Redacted  bool                 `json:"redacted"`
	CreatedAt time.Time            `json:"recorded_at"`
}

type CodeHandoffVerification struct {
	PassCount     int                                `json:"pass_count"`
	FailCount     int                                `json:"fail_count"`
	UnknownCount  int                                `json:"unknown_count"`
	ReturnedCount int                                `json:"returned_count"`
	Truncated     bool                               `json:"truncated"`
	References    []CodeHandoffVerificationReference `json:"references"`
}

type CodeHandoffVerificationPlanReference struct {
	ID         string    `json:"id"`
	PlanSHA256 string    `json:"plan_sha256"`
	ItemCount  int       `json:"item_count"`
	Redacted   bool      `json:"redacted"`
	CreatedAt  time.Time `json:"created_at"`
}

type CodeHandoffVerificationPlans struct {
	ReturnedCount int                                    `json:"returned_count"`
	Truncated     bool                                   `json:"truncated"`
	References    []CodeHandoffVerificationPlanReference `json:"references"`
}

type CodeHandoffVerificationCoverageItem struct {
	PlanID                         string `json:"plan_id"`
	PlanSHA256                     string `json:"plan_sha256"`
	Ordinal                        int    `json:"ordinal"`
	ItemSHA256                     string `json:"item_sha256"`
	AssociatedEvidenceCount        int    `json:"associated_evidence_count"`
	PassCount                      int    `json:"pass_count"`
	FailCount                      int    `json:"fail_count"`
	UnknownCount                   int    `json:"unknown_count"`
	LatestAssociationEventSequence int64  `json:"latest_association_event_sequence"`
}

type CodeHandoffVerificationCoverage struct {
	ProtocolVersion         string                                `json:"protocol_version"`
	PlanCount               int                                   `json:"plan_count"`
	PlanItemCount           int                                   `json:"plan_item_count"`
	ObservedPlanItemCount   int                                   `json:"observed_plan_item_count"`
	UnobservedPlanItemCount int                                   `json:"unobserved_plan_item_count"`
	AssociatedEvidenceCount int                                   `json:"associated_evidence_count"`
	ContradictoryItemCount  int                                   `json:"contradictory_item_count"`
	ReturnedItemCount       int                                   `json:"returned_item_count"`
	Truncated               bool                                  `json:"truncated"`
	Items                   []CodeHandoffVerificationCoverageItem `json:"items"`
	MetadataOnly            bool                                  `json:"metadata_only"`
	ReadOnly                bool                                  `json:"read_only"`
	ResultInferred          bool                                  `json:"result_inferred"`
	PrivateBodiesIncluded   bool                                  `json:"private_bodies_included"`
}

type CodeHandoffSnapshotReceiptReviewReference struct {
	ID                   string                                     `json:"id"`
	ReceiptID            string                                     `json:"receipt_id"`
	ReceiptContentSHA256 string                                     `json:"receipt_content_sha256"`
	ReceiptEventSequence int64                                      `json:"receipt_event_sequence"`
	Decision             verification.SnapshotReceiptReviewDecision `json:"decision"`
	ReviewEventSequence  int64                                      `json:"review_event_sequence"`
	ReviewedAt           time.Time                                  `json:"reviewed_at"`
}

type CodeHandoffSnapshotReceiptReviews struct {
	ProtocolVersion          string                                      `json:"protocol_version"`
	MetadataConfirmedCount   int                                         `json:"metadata_confirmed_count"`
	MetadataDisputedCount    int                                         `json:"metadata_disputed_count"`
	ReturnedCount            int                                         `json:"returned_count"`
	Truncated                bool                                        `json:"truncated"`
	References               []CodeHandoffSnapshotReceiptReviewReference `json:"references"`
	MetadataOnly             bool                                        `json:"metadata_only"`
	ReadOnly                 bool                                        `json:"read_only"`
	ReviewNonAuthorizing     bool                                        `json:"review_non_authorizing"`
	ContentIncluded          bool                                        `json:"content_included"`
	PrivateBodiesIncluded    bool                                        `json:"private_bodies_included"`
	OperatorIdentityIncluded bool                                        `json:"operator_identity_included"`
	SnapshotAccepted         bool                                        `json:"snapshot_accepted"`
	ResultAccepted           bool                                        `json:"result_accepted"`
	ResultInferred           bool                                        `json:"result_inferred"`
	RecordRewritten          bool                                        `json:"record_rewritten"`
	Approval                 bool                                        `json:"approval"`
	AuthorityGranted         bool                                        `json:"authority_granted"`
	ExecutionStarted         bool                                        `json:"execution_started"`
}

type CodeHandoffActionReference struct {
	ID          string                     `json:"id"`
	Kind        operatoraction.Kind        `json:"kind"`
	State       string                     `json:"state"`
	Destination operatoraction.Destination `json:"destination"`
	AvailableAt time.Time                  `json:"available_at"`
	DueAt       *time.Time                 `json:"due_at,omitempty"`
}

type CodeHandoffReportReference struct {
	ID           string                     `json:"id"`
	Status       domain.FindingReportStatus `json:"status"`
	FindingCount int                        `json:"finding_count"`
	CreatedAt    time.Time                  `json:"created_at"`
}

type CodeHandoff struct {
	ProtocolVersion                    string                            `json:"protocol_version"`
	RunID                              string                            `json:"run_id"`
	MissionID                          string                            `json:"mission_id"`
	SessionID                          string                            `json:"session_id"`
	WorkspaceID                        string                            `json:"workspace_id"`
	RunStatus                          domain.RunStatus                  `json:"run_status"`
	Surface                            domain.ExecutionSurface           `json:"surface"`
	Phase                              domain.ExecutionPhase             `json:"phase"`
	ModeRevision                       int64                             `json:"mode_revision"`
	SourceEventSequence                int64                             `json:"source_event_sequence"`
	GeneratedAt                        time.Time                         `json:"generated_at"`
	Plan                               CodeHandoffPlan                   `json:"plan"`
	Queue                              CodeHandoffQueue                  `json:"queue"`
	ChangeSet                          CodeHandoffChangeSet              `json:"change_set"`
	Verification                       CodeHandoffVerification           `json:"verification"`
	VerificationPlans                  CodeHandoffVerificationPlans      `json:"verification_plans"`
	VerificationCoverage               CodeHandoffVerificationCoverage   `json:"verification_coverage"`
	VerificationSnapshotReceiptReviews CodeHandoffSnapshotReceiptReviews `json:"verification_snapshot_receipt_reviews"`
	PendingActionCount                 int                               `json:"pending_action_count"`
	PendingActionsTruncated            bool                              `json:"pending_actions_truncated"`
	PendingActions                     []CodeHandoffActionReference      `json:"pending_actions"`
	ReportReferencesTruncated          bool                              `json:"report_references_truncated"`
	ReportReferences                   []CodeHandoffReportReference      `json:"report_references"`
	Regenerable                        bool                              `json:"regenerable"`
	DurableSources                     bool                              `json:"durable_sources"`
	PrivateBodiesIncluded              bool                              `json:"private_bodies_included"`
	CompositeMutation                  bool                              `json:"composite_mutation"`
	ResumeAuthorized                   bool                              `json:"resume_authorized"`
	ExecutionStarted                   bool                              `json:"execution_started"`
	StandardCodeDelivery               *standardcodedelivery.Report      `json:"standard_code_delivery,omitempty"`
	HostCommands                       *CodeHandoffHostCommands          `json:"host_commands,omitempty"`
}

func NewCodeHandoffService(store CodeHandoffStore) *CodeHandoffService {
	return &CodeHandoffService{store: store,
		now: func() time.Time { return time.Now().UTC() }}
}

func (s *CodeHandoffService) WithStandardCodeDelivery(
	delivery StandardCodeDeliveryProjector,
) *CodeHandoffService {
	if s != nil {
		s.delivery = delivery
	}
	return s
}

func (s *CodeHandoffService) Build(ctx context.Context, runID string) (CodeHandoff, error) {
	if s == nil || s.store == nil || s.now == nil {
		return CodeHandoff{}, apperror.New(apperror.CodeFailedPrecondition,
			"Code handoff store is required")
	}
	if runID != strings.TrimSpace(runID) || !domain.ValidAgentID(runID) {
		return CodeHandoff{}, apperror.New(apperror.CodeInvalidArgument,
			"Code handoff Run identity is invalid")
	}
	for attempt := 0; attempt < maxCodeHandoffSnapshotAttempts; attempt++ {
		beforeSequence, err := s.store.LatestRunEventSequence(ctx, runID)
		if err != nil {
			return CodeHandoff{}, apperror.Normalize(err)
		}
		result, err := s.buildOnce(ctx, runID)
		if err != nil {
			return CodeHandoff{}, err
		}
		afterSequence, err := s.store.LatestRunEventSequence(ctx, runID)
		if err != nil {
			return CodeHandoff{}, apperror.Normalize(err)
		}
		if beforeSequence == afterSequence {
			result.SourceEventSequence = afterSequence
			return result, nil
		}
	}
	return CodeHandoff{}, apperror.New(apperror.CodeConflict,
		"Code handoff changed during bounded snapshot retries")
}

func (s *CodeHandoffService) buildOnce(ctx context.Context, runID string) (CodeHandoff, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	if run.ID != runID || mission.ID != run.MissionID || run.SessionID == "" ||
		mission.WorkspaceID == "" || mode.RunID != run.ID ||
		mode.Surface != domain.ExecutionSurfaceCode {
		return CodeHandoff{}, apperror.New(apperror.CodeFailedPrecondition,
			"Code handoff requires an exact Code Run, Session, and Workspace binding")
	}
	linkedSession, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	registered, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	if linkedSession.ID != run.SessionID || linkedSession.WorkspaceID != mission.WorkspaceID ||
		registered.ID != mission.WorkspaceID {
		return CodeHandoff{}, apperror.New(apperror.CodeFailedPrecondition,
			"Code handoff requires an exact Code Run, Session, and Workspace binding")
	}
	result := CodeHandoff{
		ProtocolVersion: CodeHandoffProtocolVersion, RunID: run.ID,
		MissionID: run.MissionID, SessionID: run.SessionID,
		WorkspaceID: mission.WorkspaceID, RunStatus: run.Status,
		Surface: mode.Surface, Phase: mode.Phase, ModeRevision: mode.Revision,
		GeneratedAt: s.now().UTC(), Regenerable: true, DurableSources: true,
	}
	if err := s.addPlan(ctx, &result); err != nil {
		return CodeHandoff{}, err
	}
	queue, err := s.store.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil {
		return CodeHandoff{}, apperror.Normalize(err)
	}
	if queue.RunID != run.ID {
		return CodeHandoff{}, apperror.New(apperror.CodeConflict,
			"Code handoff queue escaped its Run binding")
	}
	result.Queue = CodeHandoffQueue{Pending: queue.Pending, Prepared: queue.Prepared,
		Committed: queue.Committed, Cancelled: queue.Cancelled}
	if err := s.addChangeSet(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addVerification(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addVerificationPlans(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addVerificationCoverage(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addVerificationSnapshotReceiptReviews(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addActions(ctx, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addReports(ctx, &result); err != nil {
		return CodeHandoff{}, err
	}
	if err := s.addHostCommands(ctx, run, mission, &result); err != nil {
		return CodeHandoff{}, err
	}
	if s.delivery != nil {
		report, found, err := s.delivery.Current(ctx, run.ID)
		if err != nil {
			return CodeHandoff{}, err
		}
		if found {
			if report.Binding.RunID != run.ID || report.Binding.MissionID != mission.ID ||
				report.Binding.SessionID != run.SessionID ||
				report.Binding.SourceWorkspaceID != mission.WorkspaceID {
				return CodeHandoff{}, apperror.New(apperror.CodeConflict,
					"Standard Code delivery escaped its Code Handoff binding")
			}
			result.StandardCodeDelivery = &report
		}
	}
	return result, nil
}

func (s *CodeHandoffService) addVerificationSnapshotReceiptReviews(ctx context.Context,
	run domain.Run, mission domain.Mission, result *CodeHandoff,
) error {
	values, err := s.store.ListVerificationSnapshotReceiptReviews(ctx, run.ID,
		verification.MaxSnapshotReceiptReviewHistory+1)
	if err != nil {
		return apperror.Normalize(err)
	}
	projection := CodeHandoffSnapshotReceiptReviews{
		ProtocolVersion: verification.SnapshotReceiptReviewInventoryProtocolVersion,
		MetadataOnly:    true, ReadOnly: true, ReviewNonAuthorizing: true,
		Truncated: len(values) > verification.MaxSnapshotReceiptReviewHistory,
	}
	if projection.Truncated {
		values = values[:verification.MaxSnapshotReceiptReviewHistory]
	}
	projection.ReturnedCount = len(values)
	projection.References = make([]CodeHandoffSnapshotReceiptReviewReference, 0,
		min(len(values), MaxCodeHandoffReceiptReviewRefs))
	reviewIDs := make(map[string]struct{}, len(values))
	receiptIDs := make(map[string]struct{}, len(values))
	var previousEventSequence int64
	for _, value := range values {
		if err := value.Validate(); err != nil || value.RunID != run.ID ||
			value.SessionID != run.SessionID || value.WorkspaceID != mission.WorkspaceID {
			return apperror.New(apperror.CodeConflict,
				"Code handoff snapshot receipt review escaped its Run binding")
		}
		_, duplicateReview := reviewIDs[value.ID]
		_, duplicateReceipt := receiptIDs[value.ReceiptID]
		if duplicateReview || duplicateReceipt ||
			(previousEventSequence != 0 && value.EventSequence >= previousEventSequence) {
			return apperror.New(apperror.CodeConflict,
				"Code handoff snapshot receipt reviews are duplicated or unordered")
		}
		reviewIDs[value.ID] = struct{}{}
		receiptIDs[value.ReceiptID] = struct{}{}
		previousEventSequence = value.EventSequence
		switch value.Decision {
		case verification.SnapshotReceiptReviewMetadataConfirmed:
			projection.MetadataConfirmedCount++
		case verification.SnapshotReceiptReviewMetadataDisputed:
			projection.MetadataDisputedCount++
		default:
			return apperror.New(apperror.CodeConflict,
				"Code handoff snapshot receipt review decision is invalid")
		}
		if len(projection.References) < MaxCodeHandoffReceiptReviewRefs {
			projection.References = append(projection.References,
				CodeHandoffSnapshotReceiptReviewReference{
					ID: value.ID, ReceiptID: value.ReceiptID,
					ReceiptContentSHA256: value.ReceiptContentSHA256,
					ReceiptEventSequence: value.ReceiptEventSequence,
					Decision:             value.Decision, ReviewEventSequence: value.EventSequence,
					ReviewedAt: value.CreatedAt,
				})
		} else {
			projection.Truncated = true
		}
	}
	result.VerificationSnapshotReceiptReviews = projection
	return nil
}

func (s *CodeHandoffService) addVerificationCoverage(ctx context.Context, run domain.Run,
	mission domain.Mission, result *CodeHandoff,
) error {
	inventory, err := buildVerificationCoverage(ctx, s.store, run.ID)
	if err != nil {
		return err
	}
	if inventory.ProtocolVersion != verification.PlanCoverageProtocolVersion ||
		inventory.RunID != run.ID || inventory.SessionID != run.SessionID ||
		inventory.WorkspaceID != mission.WorkspaceID || !inventory.MetadataOnly ||
		!inventory.ReadOnly || inventory.ResultInferred || inventory.CommandExecuted ||
		inventory.ModelAssertion || inventory.RecordRewritten || inventory.Approval ||
		inventory.AuthorityGranted || inventory.PlanCount != len(inventory.Plans) ||
		inventory.PlanItemCount < inventory.ObservedPlanItemCount {
		return apperror.New(apperror.CodeConflict,
			"Code handoff verification coverage widened authority or escaped its binding")
	}
	coverage := CodeHandoffVerificationCoverage{
		ProtocolVersion: inventory.ProtocolVersion, PlanCount: inventory.PlanCount,
		PlanItemCount:           inventory.PlanItemCount,
		ObservedPlanItemCount:   inventory.ObservedPlanItemCount,
		UnobservedPlanItemCount: inventory.PlanItemCount - inventory.ObservedPlanItemCount,
		AssociatedEvidenceCount: inventory.AssociatedEvidenceCount,
		Truncated:               inventory.PlansTruncated, MetadataOnly: true, ReadOnly: true,
		Items: make([]CodeHandoffVerificationCoverageItem, 0,
			min(inventory.PlanItemCount, MaxCodeHandoffCoverageItemRefs)),
	}
	planItems, observedItems, associatedEvidence := 0, 0, 0
	for _, plan := range inventory.Plans {
		if plan.PlanID == "" || !validSHA256Digest(plan.PlanSHA256) ||
			plan.ItemCount != len(plan.Items) || plan.ObservedItemCount > plan.ItemCount {
			return apperror.New(apperror.CodeConflict,
				"Code handoff verification coverage plan is inconsistent")
		}
		planObserved, planAssociated := 0, 0
		for _, item := range plan.Items {
			if item.Ordinal < 1 || item.Ordinal > verification.MaxPlanItems ||
				!validSHA256Digest(item.ItemSHA256) || item.AssociatedEvidenceCount < 0 ||
				item.PassCount < 0 || item.FailCount < 0 || item.UnknownCount < 0 ||
				int64(item.PassCount)+int64(item.FailCount)+int64(item.UnknownCount) !=
					int64(item.AssociatedEvidenceCount) ||
				item.LatestAssociationEventSequence < 0 ||
				(item.AssociatedEvidenceCount == 0) !=
					(item.LatestAssociationEventSequence == 0) {
				return apperror.New(apperror.CodeConflict,
					"Code handoff verification coverage item is inconsistent")
			}
			planItems++
			if item.AssociatedEvidenceCount > 0 {
				planObserved++
				observedItems++
			}
			planAssociated += item.AssociatedEvidenceCount
			associatedEvidence += item.AssociatedEvidenceCount
			if item.PassCount > 0 && item.FailCount > 0 {
				coverage.ContradictoryItemCount++
			}
			if len(coverage.Items) < MaxCodeHandoffCoverageItemRefs {
				coverage.Items = append(coverage.Items, CodeHandoffVerificationCoverageItem{
					PlanID: plan.PlanID, PlanSHA256: plan.PlanSHA256, Ordinal: item.Ordinal,
					ItemSHA256:              item.ItemSHA256,
					AssociatedEvidenceCount: item.AssociatedEvidenceCount,
					PassCount:               item.PassCount, FailCount: item.FailCount,
					UnknownCount:                   item.UnknownCount,
					LatestAssociationEventSequence: item.LatestAssociationEventSequence,
				})
			} else {
				coverage.Truncated = true
			}
		}
		if planObserved != plan.ObservedItemCount ||
			planAssociated != plan.AssociatedEvidenceCount {
			return apperror.New(apperror.CodeConflict,
				"Code handoff verification coverage plan totals are inconsistent")
		}
	}
	if planItems != inventory.PlanItemCount || observedItems != inventory.ObservedPlanItemCount ||
		associatedEvidence != inventory.AssociatedEvidenceCount {
		return apperror.New(apperror.CodeConflict,
			"Code handoff verification coverage totals are inconsistent")
	}
	coverage.ReturnedItemCount = len(coverage.Items)
	result.VerificationCoverage = coverage
	return nil
}

func (s *CodeHandoffService) addVerificationPlans(ctx context.Context, run domain.Run,
	mission domain.Mission, result *CodeHandoff,
) error {
	values, err := s.store.ListVerificationPlans(ctx, run.ID,
		verification.MaxPlanInventoryItems+1)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.VerificationPlans.Truncated = len(values) > verification.MaxPlanInventoryItems
	if result.VerificationPlans.Truncated {
		values = values[:verification.MaxPlanInventoryItems]
	}
	result.VerificationPlans.ReturnedCount = len(values)
	limit := min(len(values), MaxCodeHandoffVerifyPlanRefs)
	result.VerificationPlans.References = make([]CodeHandoffVerificationPlanReference, limit)
	for index, value := range values {
		if value.RunID != run.ID || value.SessionID != run.SessionID ||
			value.WorkspaceID != mission.WorkspaceID {
			return apperror.New(apperror.CodeConflict,
				"Code handoff verification plan escaped its Run binding")
		}
		if index < limit {
			result.VerificationPlans.References[index] = CodeHandoffVerificationPlanReference{
				ID: value.ID, PlanSHA256: value.PlanSHA256, ItemCount: len(value.Items),
				Redacted: value.Redacted, CreatedAt: value.CreatedAt,
			}
		}
	}
	if len(values) > MaxCodeHandoffVerifyPlanRefs {
		result.VerificationPlans.Truncated = true
	}
	return nil
}

func (s *CodeHandoffService) addPlan(ctx context.Context, result *CodeHandoff) error {
	selection, selected, err := s.store.GetPlanDeliverySelectionByRun(ctx, result.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !selected {
		proposals, err := s.store.ListPlanDeliveryProposals(ctx, result.RunID, 1)
		if err != nil {
			return apperror.Normalize(err)
		}
		result.Plan.State = "none"
		if len(proposals) == 0 {
			return nil
		}
		proposal := proposals[0]
		if proposal.RunID != result.RunID || proposal.SessionID != result.SessionID ||
			proposal.WorkspaceID != result.WorkspaceID {
			return apperror.New(apperror.CodeConflict,
				"Code handoff Plan proposal escaped its Run binding")
		}
		result.Plan.State = "proposed"
		result.Plan.ProposalID = proposal.ID
		result.Plan.DirectionCount = len(proposal.Spec.Directions)
		return nil
	}
	proposal, err := s.store.GetPlanDeliveryProposal(ctx, selection.ProposalID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if selection.RunID != result.RunID || proposal.RunID != result.RunID ||
		proposal.SessionID != result.SessionID || proposal.WorkspaceID != result.WorkspaceID ||
		selection.DirectionOrdinal < 1 ||
		selection.DirectionOrdinal > len(proposal.Spec.Directions) {
		return apperror.New(apperror.CodeConflict,
			"Code handoff Plan selection escaped its Run binding")
	}
	result.Plan = CodeHandoffPlan{State: "selected", ProposalID: proposal.ID,
		SelectionID: selection.ID, DirectionCount: len(proposal.Spec.Directions),
		ManualAcceptance:  string(selection.EffectiveManualAcceptance()),
		SelectedDirection: selection.DirectionOrdinal, ModuleCount: len(selection.Items)}
	for _, selectedItem := range selection.Items {
		item, err := s.store.GetWorkItem(ctx, selectedItem.WorkItemID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if item.RunID != result.RunID {
			return apperror.New(apperror.CodeConflict,
				"Code handoff WorkItem escaped its Run binding")
		}
		switch item.Status {
		case domain.WorkItemPending:
			result.Plan.PendingCount++
		case domain.WorkItemInProgress:
			result.Plan.InProgressCount++
		case domain.WorkItemBlocked:
			result.Plan.BlockedCount++
		case domain.WorkItemCompleted:
			result.Plan.CompletedCount++
		case domain.WorkItemCancelled:
			result.Plan.CancelledCount++
		default:
			return apperror.New(apperror.CodeInternal,
				"Code handoff WorkItem status is invalid")
		}
	}
	return nil
}

func (s *CodeHandoffService) addChangeSet(ctx context.Context, run domain.Run,
	mission domain.Mission, result *CodeHandoff,
) error {
	workspaceID, err := runFileProjectionWorkspaceID(ctx, s.store, run, mission)
	if err != nil {
		return err
	}
	values, err := s.store.ListFileEditPreviewsPage(ctx, fileedit.ListFilter{
		SessionID: run.SessionID, WorkspaceID: workspaceID,
	}, 0, MaxFileEditChangeSetItems+1)
	if err != nil {
		return apperror.Normalize(err)
	}
	truncated := len(values) > MaxFileEditChangeSetItems
	if truncated {
		values = values[:MaxFileEditChangeSetItems]
	}
	changeSet, err := BuildFileEditChangeSetForWorkspace(run, mission, workspaceID, values)
	if err != nil {
		return err
	}
	result.ChangeSet = CodeHandoffChangeSet{
		Proposed: changeSet.Counts.Proposed, Approved: changeSet.Counts.Approved,
		Applied: changeSet.Counts.Applied, Denied: changeSet.Counts.Denied,
		Failed: changeSet.Counts.Failed, ReturnedCount: len(values),
		TotalDiffBytes: changeSet.TotalDiffBytes, Truncated: truncated,
	}
	return nil
}

func (s *CodeHandoffService) addVerification(ctx context.Context, run domain.Run,
	mission domain.Mission, result *CodeHandoff,
) error {
	values, err := s.store.ListVerificationEvidence(ctx, run.ID,
		verification.MaxInventoryItems+1)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.Verification.Truncated = len(values) > verification.MaxInventoryItems
	if result.Verification.Truncated {
		values = values[:verification.MaxInventoryItems]
	}
	result.Verification.ReturnedCount = len(values)
	for index, value := range values {
		if value.RunID != run.ID || value.SessionID != run.SessionID ||
			value.WorkspaceID != mission.WorkspaceID {
			return apperror.New(apperror.CodeConflict,
				"Code handoff verification escaped its Run binding")
		}
		switch value.Outcome {
		case verification.OutcomePass:
			result.Verification.PassCount++
		case verification.OutcomeFail:
			result.Verification.FailCount++
		case verification.OutcomeUnknown:
			result.Verification.UnknownCount++
		}
		if index < MaxCodeHandoffVerifyReferences {
			result.Verification.References = append(result.Verification.References,
				CodeHandoffVerificationReference{ID: value.ID, Outcome: value.Outcome,
					Redacted: value.Redacted, CreatedAt: value.CreatedAt})
		}
	}
	if len(values) > MaxCodeHandoffVerifyReferences {
		result.Verification.Truncated = true
	}
	return nil
}

func (s *CodeHandoffService) addActions(ctx context.Context, result *CodeHandoff) error {
	center, err := NewOperatorActionCenterService(s.store).List(ctx, result.RunID)
	if err != nil {
		return err
	}
	result.PendingActionCount = len(center.Items)
	result.PendingActionsTruncated = center.Truncated ||
		len(center.Items) > MaxCodeHandoffActionReferences
	limit := min(len(center.Items), MaxCodeHandoffActionReferences)
	result.PendingActions = make([]CodeHandoffActionReference, limit)
	for index := 0; index < limit; index++ {
		item := center.Items[index]
		result.PendingActions[index] = CodeHandoffActionReference{
			ID: item.ID, Kind: item.Kind, State: item.State,
			Destination: item.Destination, AvailableAt: item.AvailableAt, DueAt: item.DueAt,
		}
	}
	return nil
}

func (s *CodeHandoffService) addReports(ctx context.Context, result *CodeHandoff) error {
	values, err := s.store.ListFindingReportSummariesPage(ctx, result.RunID, 0,
		MaxCodeHandoffReportReferences+1)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.ReportReferencesTruncated = len(values) > MaxCodeHandoffReportReferences
	if result.ReportReferencesTruncated {
		values = values[:MaxCodeHandoffReportReferences]
	}
	result.ReportReferences = make([]CodeHandoffReportReference, len(values))
	for index, value := range values {
		if value.RunID != result.RunID || !value.Status.Valid() {
			return apperror.New(apperror.CodeConflict,
				"Code handoff report escaped its Run binding")
		}
		result.ReportReferences[index] = CodeHandoffReportReference{
			ID: value.ID, Status: value.Status, FindingCount: value.FindingCount,
			CreatedAt: value.CreatedAt,
		}
	}
	return nil
}
