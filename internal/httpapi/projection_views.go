package httpapi

import (
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
)

type ExternalSkillProjectionItemView struct {
	Ordinal            int    `json:"ordinal"`
	Name               string `json:"name"`
	Version            string `json:"version"`
	TokenUpperBound    int    `json:"token_upper_bound"`
	TrustClass         string `json:"trust_class"`
	DeclaredToolCount  int    `json:"declared_tool_count"`
	SpecialistEligible bool   `json:"specialist_eligible"`
}

type ExternalSkillDeliveryView struct {
	Prepared  int `json:"prepared"`
	Committed int `json:"committed"`
}

type ExternalSkillProjectionView struct {
	ProtocolVersion           string                            `json:"protocol_version"`
	RunID                     string                            `json:"run_id"`
	ModeRevision              int64                             `json:"mode_revision"`
	Surface                   string                            `json:"surface"`
	Profile                   string                            `json:"profile"`
	TokenBudget               int                               `json:"token_budget"`
	TokenUpperBound           int                               `json:"token_upper_bound"`
	ItemCount                 int                               `json:"item_count"`
	OperatorConfirmed         bool                              `json:"operator_confirmed"`
	ContextDeliveryAuthorized bool                              `json:"context_delivery_authorized"`
	ToolCapabilityGrant       bool                              `json:"tool_capability_grant"`
	RootDelivery              ExternalSkillDeliveryView         `json:"root_delivery"`
	SpecialistDelivery        ExternalSkillDeliveryView         `json:"specialist_delivery"`
	Items                     []ExternalSkillProjectionItemView `json:"items"`
	CreatedAt                 time.Time                         `json:"created_at"`
}

func externalSkillProjectionView(value skills.ExternalSkillProjection) ExternalSkillProjectionView {
	view := ExternalSkillProjectionView{
		ProtocolVersion: value.ProtocolVersion, RunID: value.RunID,
		ModeRevision: value.ModeRevision, Surface: string(value.Surface),
		Profile: string(value.Profile), TokenBudget: value.TokenBudget,
		TokenUpperBound: value.TokenUpperBound, ItemCount: value.ItemCount,
		OperatorConfirmed:         value.OperatorConfirmed,
		ContextDeliveryAuthorized: value.ContextDeliveryAuthorized,
		ToolCapabilityGrant:       value.ToolCapabilityGrant,
		RootDelivery: ExternalSkillDeliveryView{
			Prepared: value.RootPreparedCount, Committed: value.RootCommittedCount,
		},
		SpecialistDelivery: ExternalSkillDeliveryView{
			Prepared: value.SpecialistPreparedCount, Committed: value.SpecialistCommittedCount,
		},
		Items:     make([]ExternalSkillProjectionItemView, len(value.Items)),
		CreatedAt: value.CreatedAt,
	}
	for index, item := range value.Items {
		view.Items[index] = ExternalSkillProjectionItemView{
			Ordinal: item.Ordinal, Name: item.Name, Version: item.Version,
			TokenUpperBound: item.TokenUpperBound, TrustClass: string(item.TrustClass),
			DeclaredToolCount:  item.DeclaredToolCount,
			SpecialistEligible: item.SpecialistEligible,
		}
	}
	return view
}

type AgentCompletionView struct {
	ID          string    `json:"id"`
	AttemptID   string    `json:"attempt_id"`
	Outcome     string    `json:"outcome"`
	Summary     string    `json:"summary"`
	WorkItemIDs []string  `json:"work_item_ids"`
	NoteIDs     []string  `json:"note_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

type AgentNodeView struct {
	Worker           *domain.WorkerProfile `json:"worker,omitempty"`
	ToolCallsUsed    int64                 `json:"tool_calls_used,omitempty"`
	CostUsedMicros   int64                 `json:"cost_used_micros,omitempty"`
	AllowedReadPaths []string              `json:"allowed_read_paths,omitempty"`
	ToolCallLimit    int64                 `json:"tool_call_limit,omitempty"`
	CostLimitMicros  int64                 `json:"cost_limit_micros,omitempty"`
	ID               string                `json:"id"`
	ParentID         string                `json:"parent_id,omitempty"`
	SessionID        string                `json:"session_id"`
	Role             string                `json:"role"`
	Profile          string                `json:"profile"`
	Skills           []string              `json:"skills"`
	Status           string                `json:"status"`
	Depth            int                   `json:"depth"`
	ChildLimit       int                   `json:"child_limit"`
	TurnLimit        int64                 `json:"turn_limit"`
	TokenLimit       int64                 `json:"token_limit"`
	TurnsUsed        int64                 `json:"turns_used"`
	TokensUsed       int64                 `json:"tokens_used"`
	ActiveAttemptID  string                `json:"active_attempt_id,omitempty"`
	Version          int64                 `json:"version"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	FinishedAt       *time.Time            `json:"finished_at,omitempty"`
	Completion       *AgentCompletionView  `json:"completion,omitempty"`
}

type AgentGraphView struct {
	ProtocolVersion string          `json:"protocol_version"`
	RunID           string          `json:"run_id"`
	RootAgentID     string          `json:"root_agent_id,omitempty"`
	Nodes           []AgentNodeView `json:"nodes"`
}

type DelegationAssignmentView struct {
	Worker            *domain.WorkerProfile `json:"worker,omitempty"`
	AllowedReadPaths  []string              `json:"allowed_read_paths,omitempty"`
	ToolCallLimit     int64                 `json:"tool_call_limit,omitempty"`
	CostLimitMicros   int64                 `json:"cost_limit_micros,omitempty"`
	Ordinal           int                   `json:"ordinal"`
	Title             string                `json:"title"`
	Goal              string                `json:"goal"`
	Skills            []string              `json:"skills"`
	TurnLimit         int64                 `json:"turn_limit"`
	TokenLimit        int64                 `json:"token_limit"`
	ApplicationStatus string                `json:"application_status,omitempty"`
	AgentID           string                `json:"agent_id,omitempty"`
}

type DelegationReviewView struct {
	ID         string    `json:"id"`
	Decision   string    `json:"decision"`
	ReviewedBy string    `json:"reviewed_by"`
	CreatedAt  time.Time `json:"created_at"`
}

type DelegationApplicationView struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	AssignmentCount   int        `json:"assignment_count"`
	MaxChildren       int        `json:"max_children"`
	MaxTurnsPerChild  int64      `json:"max_turns_per_child"`
	MaxTokensPerChild int64      `json:"max_tokens_per_child"`
	RequestedBy       string     `json:"requested_by"`
	StopCode          string     `json:"stop_code,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type DelegationScheduleView struct {
	RequestID         string     `json:"request_id"`
	AgentIDs          []string   `json:"agent_ids"`
	MaxRounds         int        `json:"max_rounds"`
	RequestedBy       string     `json:"requested_by"`
	RequestedAt       time.Time  `json:"requested_at"`
	AttemptOrdinal    int        `json:"attempt_ordinal,omitempty"`
	ScheduleID        string     `json:"schedule_id,omitempty"`
	Status            string     `json:"status,omitempty"`
	RoundsCompleted   int        `json:"rounds_completed,omitempty"`
	TurnsStarted      int        `json:"turns_started,omitempty"`
	RecoveredAttempts int        `json:"recovered_attempts,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type DelegationView struct {
	ID          string                     `json:"id"`
	RunID       string                     `json:"run_id"`
	RootAgentID string                     `json:"root_agent_id"`
	Status      string                     `json:"status"`
	RequestedBy string                     `json:"requested_by"`
	CreatedAt   time.Time                  `json:"created_at"`
	Assignments []DelegationAssignmentView `json:"assignments"`
	Review      *DelegationReviewView      `json:"review,omitempty"`
	Application *DelegationApplicationView `json:"application,omitempty"`
	Schedule    *DelegationScheduleView    `json:"latest_schedule,omitempty"`
}

type FanoutExecutionShardView struct {
	Ordinal        int        `json:"ordinal"`
	Status         string     `json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	CurrentAttempt int        `json:"current_attempt"`
	Provider       string     `json:"provider,omitempty"`
	Model          string     `json:"model,omitempty"`
	InputTokens    int64      `json:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens"`
	TotalTokens    int64      `json:"total_tokens"`
	ElapsedMillis  int64      `json:"elapsed_millis"`
	FindingCount   int        `json:"finding_count"`
	ErrorCode      string     `json:"error_code,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type FanoutExecutionView struct {
	ID                      string                     `json:"id"`
	Status                  string                     `json:"status"`
	Parallelism             int                        `json:"parallelism"`
	MaxOutputTokensPerShard int                        `json:"max_output_tokens_per_shard"`
	RequestedBy             string                     `json:"requested_by"`
	StopCode                string                     `json:"stop_code,omitempty"`
	StartedAt               time.Time                  `json:"started_at"`
	UpdatedAt               time.Time                  `json:"updated_at"`
	FinishedAt              *time.Time                 `json:"finished_at,omitempty"`
	Shards                  []FanoutExecutionShardView `json:"shards"`
}

type FanoutPlanView struct {
	ID                   string               `json:"id"`
	RunID                string               `json:"run_id"`
	WorkspaceID          string               `json:"workspace_id"`
	ScopePath            string               `json:"scope_path"`
	Goal                 string               `json:"goal"`
	ProtocolVersion      string               `json:"protocol_version"`
	RequestedTier        string               `json:"requested_tier"`
	EffectiveParallelism int                  `json:"effective_parallelism"`
	Status               string               `json:"status"`
	FileCount            int                  `json:"file_count"`
	TotalBytes           int64                `json:"total_bytes"`
	ExcludedCount        int                  `json:"excluded_count"`
	ShardCount           int                  `json:"shard_count"`
	RequestedBy          string               `json:"requested_by"`
	CreatedAt            time.Time            `json:"created_at"`
	LatestExecution      *FanoutExecutionView `json:"latest_execution,omitempty"`
}

type FindingSeverityView struct {
	Info     int `json:"info"`
	Low      int `json:"low"`
	Medium   int `json:"medium"`
	High     int `json:"high"`
	Critical int `json:"critical"`
}

type FindingReportSummaryView struct {
	ID            string              `json:"id"`
	RunID         string              `json:"run_id"`
	SourceKind    string              `json:"source_kind"`
	SourceID      string              `json:"source_id"`
	Status        string              `json:"status"`
	Title         string              `json:"title"`
	FindingCount  int                 `json:"finding_count"`
	EvidenceCount int                 `json:"evidence_count"`
	Severity      FindingSeverityView `json:"severity"`
	CreatedAt     time.Time           `json:"created_at"`
}

type FindingEvidenceView struct {
	ID            string `json:"id"`
	SourceID      string `json:"source_id"`
	SourceShard   int    `json:"source_shard"`
	SourceOrdinal int    `json:"source_ordinal"`
	RelativePath  string `json:"relative_path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Confidence    int    `json:"confidence"`
}

type FindingArtifactEvidenceView struct {
	ID               string    `json:"id"`
	ArtifactID       string    `json:"artifact_id"`
	ArtifactSize     int64     `json:"artifact_size_bytes"`
	ArtifactMIME     string    `json:"artifact_mime"`
	ArtifactStream   string    `json:"artifact_stream"`
	ArtifactRedacted bool      `json:"artifact_redacted"`
	CreatedAt        time.Time `json:"created_at"`
}

type FindingLifecycleView struct {
	Status                   string     `json:"status"`
	ValidationEvidenceCount  int        `json:"validation_evidence_count"`
	RemediationEvidenceCount int        `json:"remediation_evidence_count"`
	ValidationDecidedAt      *time.Time `json:"validation_decided_at,omitempty"`
	AcceptedAt               *time.Time `json:"accepted_at,omitempty"`
	FixedAt                  *time.Time `json:"fixed_at,omitempty"`
}

type FindingView struct {
	ID                  string                        `json:"id"`
	Ordinal             int                           `json:"ordinal"`
	Status              string                        `json:"status"`
	Severity            string                        `json:"severity"`
	Category            string                        `json:"category"`
	Title               string                        `json:"title"`
	Detail              string                        `json:"detail"`
	RelativePath        string                        `json:"relative_path"`
	LineStart           int                           `json:"line_start"`
	LineEnd             int                           `json:"line_end"`
	Confidence          int                           `json:"confidence"`
	Evidence            []FindingEvidenceView         `json:"evidence"`
	ArtifactEvidence    []FindingArtifactEvidenceView `json:"artifact_evidence"`
	RemediationEvidence []FindingArtifactEvidenceView `json:"remediation_evidence"`
	Lifecycle           FindingLifecycleView          `json:"lifecycle"`
}

type FindingReportView struct {
	Report   FindingReportSummaryView `json:"report"`
	Findings []FindingView            `json:"findings"`
}

func agentNodeView(value domain.AgentNode, completion *domain.AgentCompletion) AgentNodeView {
	view := AgentNodeView{Worker: value.Worker, ID: value.ID, ParentID: value.ParentID, SessionID: value.SessionID,
		Role: string(value.Role), Profile: string(value.Profile), Skills: append([]string(nil), value.Skills...),
		Status: string(value.Status), Depth: value.Depth, ChildLimit: value.ChildLimit,
		ToolCallsUsed: value.ToolCallsUsed, CostUsedMicros: value.CostUsedMicros, AllowedReadPaths: value.AllowedReadPaths, ToolCallLimit: value.ToolCallLimit, CostLimitMicros: value.CostLimitMicros,
		TurnLimit: value.TurnLimit, TokenLimit: value.TokenLimit, TurnsUsed: value.TurnsUsed,
		TokensUsed: value.TokensUsed, ActiveAttemptID: value.ActiveAttemptID, Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, FinishedAt: value.FinishedAt}
	if completion != nil {
		view.Completion = &AgentCompletionView{ID: completion.ID, AttemptID: completion.AttemptID,
			Outcome: string(completion.Report.Outcome), Summary: completion.Report.Summary,
			WorkItemIDs: append([]string(nil), completion.Report.WorkItemIDs...),
			NoteIDs:     append([]string(nil), completion.Report.NoteIDs...), CreatedAt: completion.CreatedAt}
	}
	return view
}

func findingSeverityView(value domain.FindingSeveritySummary) FindingSeverityView {
	return FindingSeverityView{Info: value.Info, Low: value.Low, Medium: value.Medium,
		High: value.High, Critical: value.Critical}
}

func findingReportSummaryView(value domain.FindingReportSummary) FindingReportSummaryView {
	return FindingReportSummaryView{ID: value.ID, RunID: value.RunID,
		SourceKind: value.SourceKind, SourceID: value.SourceID, Status: string(value.Status),
		Title: value.Title, FindingCount: value.FindingCount, EvidenceCount: value.EvidenceCount,
		Severity: findingSeverityView(value.Severity), CreatedAt: value.CreatedAt}
}
