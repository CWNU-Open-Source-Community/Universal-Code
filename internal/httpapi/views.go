package httpapi

import (
	"encoding/json"
	"strconv"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/threadtranscript"
	"cyberagent-workbench/internal/toolbudget"
)

type IndexView struct {
	APIVersion string   `json:"api_version"`
	AppVersion string   `json:"app_version"`
	Resources  []string `json:"resources"`
}

type HealthView struct {
	Status        string `json:"status"`
	APIVersion    string `json:"api_version"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	DataStoreID   string `json:"data_store_id,omitempty"`
}

type ProviderAvailabilityView struct {
	Name                          string                         `json:"name"`
	DisplayName                   string                         `json:"display_name"`
	Kind                          string                         `json:"kind"`
	Status                        string                         `json:"status"`
	Models                        []string                       `json:"models"`
	Harnesses                     []ModelHarnessAvailabilityView `json:"harnesses"`
	CredentialSource              string                         `json:"credential_source"`
	NetworkRequired               bool                           `json:"network_required"`
	ConfigurationError            bool                           `json:"configuration_error"`
	Custom                        bool                           `json:"custom"`
	Enabled                       bool                           `json:"enabled"`
	DefinitionRevision            uint64                         `json:"definition_revision"`
	Transport                     string                         `json:"transport"`
	SearchMode                    string                         `json:"search_mode"`
	NativeWebSearchCapability     string                         `json:"native_web_search_capability"`
	NativeWebSearchRuntimeEnabled bool                           `json:"native_web_search_runtime_enabled"`
}

type ModelRouteAvailabilityView struct {
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Available    bool   `json:"available"`
	HarnessReady bool   `json:"harness_ready"`
}

type ModelHarnessAvailabilityView struct {
	ProtocolVersion           string `json:"protocol_version"`
	Model                     string `json:"model"`
	TransportProtocol         string `json:"transport_protocol"`
	ToolStrategy              string `json:"tool_strategy"`
	JSONStrategy              string `json:"json_strategy"`
	QualificationStatus       string `json:"qualification_status"`
	ToolCallsQualified        bool   `json:"tool_calls_qualified"`
	ToolResultsQualified      bool   `json:"tool_results_qualified"`
	StrictJSONQualified       bool   `json:"strict_json_qualified"`
	StreamingQualified        bool   `json:"streaming_qualified"`
	RootEligible              bool   `json:"root_eligible"`
	StructuredJSONEligible    bool   `json:"structured_json_eligible"`
	LatestQualificationStatus string `json:"latest_qualification_status"`
	QualificationCheckedAt    string `json:"qualification_checked_at"`
	QualificationSource       string `json:"qualification_source"`
	QualifiedAt               string `json:"qualified_at"`
	ExpiresAt                 string `json:"expires_at"`
}

type ModelAvailabilityView struct {
	ProtocolVersion string                       `json:"protocol_version"`
	Generation      uint64                       `json:"generation"`
	Providers       []ProviderAvailabilityView   `json:"providers"`
	Routes          []ModelRouteAvailabilityView `json:"routes"`
}

type ProviderDiagnosticView struct {
	ProtocolVersion         string `json:"protocol_version"`
	Provider                string `json:"provider"`
	Model                   string `json:"model"`
	Status                  string `json:"status"`
	Outcome                 string `json:"outcome"`
	FailureReason           string `json:"failure_reason"`
	Retryable               bool   `json:"retryable"`
	NetworkRequestAttempted bool   `json:"network_request_attempted"`
	ModelCalled             bool   `json:"model_called"`
	ToolCalled              bool   `json:"tool_called"`
	ResponseContentReturned bool   `json:"response_content_returned"`
	QualificationStatus     string `json:"qualification_status"`
	DurationMillis          int64  `json:"duration_ms"`
}

type ModelHarnessQualificationView struct {
	ProtocolVersion         string                       `json:"protocol_version"`
	Provider                string                       `json:"provider"`
	Model                   string                       `json:"model"`
	Status                  string                       `json:"status"`
	Outcome                 string                       `json:"outcome"`
	FailureReason           string                       `json:"failure_reason"`
	Retryable               bool                         `json:"retryable"`
	NetworkRequestAttempted bool                         `json:"network_request_attempted"`
	ModelCalls              int                          `json:"model_calls"`
	SyntheticToolCalls      int                          `json:"synthetic_tool_calls"`
	ToolExecuted            bool                         `json:"tool_executed"`
	ResponseContentReturned bool                         `json:"response_content_returned"`
	QualificationStatus     string                       `json:"qualification_status"`
	DurationMillis          int64                        `json:"duration_ms"`
	Harness                 ModelHarnessAvailabilityView `json:"harness"`
}

type FileEditPreviewView struct {
	ID                      string                             `json:"id"`
	SessionID               string                             `json:"session_id"`
	WorkspaceID             string                             `json:"workspace_id"`
	Path                    string                             `json:"path"`
	Operation               string                             `json:"operation"`
	DestinationPath         string                             `json:"destination_path,omitempty"`
	Status                  string                             `json:"status"`
	Diff                    string                             `json:"diff"`
	OriginalHash            string                             `json:"original_hash"`
	ProposedHash            string                             `json:"proposed_hash"`
	DestinationOriginalHash string                             `json:"destination_original_hash,omitempty"`
	DestinationProposedHash string                             `json:"destination_proposed_hash,omitempty"`
	Reason                  string                             `json:"reason,omitempty"`
	SecretsRedacted         bool                               `json:"secrets_redacted"`
	AllowedActions          []application.FileEditReviewAction `json:"allowed_actions"`
	CreatedAt               time.Time                          `json:"created_at"`
	UpdatedAt               time.Time                          `json:"updated_at"`
	ApplyEnabled            bool                               `json:"apply_enabled"`
}

type FileEditQueueView struct {
	ProtocolVersion string                `json:"protocol_version"`
	RunID           string                `json:"run_id"`
	Items           []FileEditPreviewView `json:"items"`
	Truncated       bool                  `json:"truncated"`
	ApplyEnabled    bool                  `json:"apply_enabled"`
}

type FileEditChangeSetItemView struct {
	ID              string                             `json:"id"`
	Path            string                             `json:"path"`
	Operation       string                             `json:"operation"`
	DestinationPath string                             `json:"destination_path,omitempty"`
	Status          string                             `json:"status"`
	DiffBytes       int                                `json:"diff_bytes"`
	SecretsRedacted bool                               `json:"secrets_redacted"`
	AllowedActions  []application.FileEditReviewAction `json:"allowed_actions"`
	ApplyEnabled    bool                               `json:"apply_enabled"`
	UpdatedAt       time.Time                          `json:"updated_at"`
}

type FileEditChangeSetView struct {
	ProtocolVersion        string                      `json:"protocol_version"`
	RunID                  string                      `json:"run_id"`
	SessionID              string                      `json:"session_id"`
	WorkspaceID            string                      `json:"workspace_id"`
	Items                  []FileEditChangeSetItemView `json:"items"`
	ProposedCount          int                         `json:"proposed_count"`
	ApprovedCount          int                         `json:"approved_count"`
	AppliedCount           int                         `json:"applied_count"`
	DeniedCount            int                         `json:"denied_count"`
	FailedCount            int                         `json:"failed_count"`
	ReturnedCount          int                         `json:"returned_count"`
	TotalDiffBytes         int                         `json:"total_diff_bytes"`
	Truncated              bool                        `json:"truncated"`
	ReviewIndependent      bool                        `json:"review_independent"`
	ApplyIndependent       bool                        `json:"apply_independent"`
	AtomicApply            bool                        `json:"atomic_apply"`
	BatchMutationSupported bool                        `json:"batch_mutation_supported"`
	PartialApplyVisible    bool                        `json:"partial_apply_visible"`
	DiffContentIncluded    bool                        `json:"diff_content_included"`
}

type RepositoryChangeView struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
}

type RepositoryStateView struct {
	ProtocolVersion      string                 `json:"protocol_version"`
	WorkspaceID          string                 `json:"workspace_id"`
	Kind                 string                 `json:"kind"`
	Available            bool                   `json:"available"`
	Clean                bool                   `json:"clean"`
	Detached             bool                   `json:"detached"`
	Branch               string                 `json:"branch"`
	Head                 string                 `json:"head"`
	Changes              []RepositoryChangeView `json:"changes"`
	StagedCount          int                    `json:"staged_count"`
	WorktreeCount        int                    `json:"worktree_count"`
	UntrackedCount       int                    `json:"untracked_count"`
	ConflictedCount      int                    `json:"conflicted_count"`
	RedactionCount       int                    `json:"redaction_count"`
	Truncated            bool                   `json:"truncated"`
	ReadOnly             bool                   `json:"read_only"`
	RootPathExposed      bool                   `json:"root_path_exposed"`
	ContentIncluded      bool                   `json:"content_included"`
	RemoteConfigIncluded bool                   `json:"remote_config_included"`
	ProcessStarted       bool                   `json:"process_started"`
	NetworkUsed          bool                   `json:"network_used"`
	HooksExecuted        bool                   `json:"hooks_executed"`
}

type RepositoryDiffItemView struct {
	StatisticsComplete bool   `json:"statistics_complete"`
	Path               string `json:"path"`
	Staging            string `json:"staging"`
	Worktree           string `json:"worktree"`
	ContentState       string `json:"content_state"`
	Patch              string `json:"patch"`
	PatchBytes         int    `json:"patch_bytes"`
	AddedLines         int    `json:"added_lines"`
	DeletedLines       int    `json:"deleted_lines"`
	Redacted           bool   `json:"redacted"`
	Truncated          bool   `json:"truncated"`
}

type RepositoryDiffView struct {
	NextAfter             string                   `json:"next_after"`
	ProtocolVersion       string                   `json:"protocol_version"`
	WorkspaceID           string                   `json:"workspace_id"`
	Kind                  string                   `json:"kind"`
	Available             bool                     `json:"available"`
	BaseHead              string                   `json:"base_head"`
	Items                 []RepositoryDiffItemView `json:"items"`
	ReturnedCount         int                      `json:"returned_count"`
	OmittedCount          int                      `json:"omitted_count"`
	RedactionCount        int                      `json:"redaction_count"`
	TotalPatchBytes       int                      `json:"total_patch_bytes"`
	Truncated             bool                     `json:"truncated"`
	ReadOnly              bool                     `json:"read_only"`
	InstructionAuthorized bool                     `json:"instruction_authorized"`
	MutationSupported     bool                     `json:"mutation_supported"`
	AuthorityGranted      bool                     `json:"authority_granted"`
	RootPathExposed       bool                     `json:"root_path_exposed"`
	RawContentIncluded    bool                     `json:"raw_content_included"`
	PatchContentIncluded  bool                     `json:"patch_content_included"`
	RemoteConfigIncluded  bool                     `json:"remote_config_included"`
	ProcessStarted        bool                     `json:"process_started"`
	NetworkUsed           bool                     `json:"network_used"`
	HooksExecuted         bool                     `json:"hooks_executed"`
}

type RepositoryHistoryCommitView struct {
	Hash           string    `json:"hash"`
	ObjectID       string    `json:"object_id"`
	Subject        string    `json:"subject"`
	ParentCount    int       `json:"parent_count"`
	CommittedAt    time.Time `json:"committed_at"`
	Redacted       bool      `json:"redacted"`
	SubjectBounded bool      `json:"subject_bounded"`
}

type RepositoryFileHistoryEntryView struct {
	ObjectID       string    `json:"object_id"`
	Hash           string    `json:"hash"`
	Subject        string    `json:"subject"`
	CommittedAt    time.Time `json:"committed_at"`
	Change         string    `json:"change"`
	PreviousKind   string    `json:"previous_kind"`
	CurrentKind    string    `json:"current_kind"`
	ContentChanged bool      `json:"content_changed"`
	ModeChanged    bool      `json:"mode_changed"`
	Redacted       bool      `json:"redacted"`
	SubjectBounded bool      `json:"subject_bounded"`
}

type RepositoryFileHistoryView struct {
	ProtocolVersion        string                           `json:"protocol_version"`
	WorkspaceID            string                           `json:"workspace_id"`
	Kind                   string                           `json:"kind"`
	Available              bool                             `json:"available"`
	Head                   string                           `json:"head"`
	Path                   string                           `json:"path"`
	Entries                []RepositoryFileHistoryEntryView `json:"entries"`
	ScannedCommitCount     int                              `json:"scanned_commit_count"`
	ReturnedEntryCount     int                              `json:"returned_entry_count"`
	RedactionCount         int                              `json:"redaction_count"`
	Observed               bool                             `json:"observed"`
	Truncated              bool                             `json:"truncated"`
	FirstParentOnly        bool                             `json:"first_parent_only"`
	RenameInferred         bool                             `json:"rename_inferred"`
	MetadataOnly           bool                             `json:"metadata_only"`
	ReadOnly               bool                             `json:"read_only"`
	AuthorityGranted       bool                             `json:"authority_granted"`
	RootPathExposed        bool                             `json:"root_path_exposed"`
	AuthorIdentityIncluded bool                             `json:"author_identity_included"`
	CommitBodyIncluded     bool                             `json:"commit_body_included"`
	FileContentIncluded    bool                             `json:"file_content_included"`
	PatchIncluded          bool                             `json:"patch_included"`
	RemoteConfigIncluded   bool                             `json:"remote_config_included"`
	CheckoutPerformed      bool                             `json:"checkout_performed"`
	ReferenceUpdated       bool                             `json:"reference_updated"`
	ProcessStarted         bool                             `json:"process_started"`
	NetworkUsed            bool                             `json:"network_used"`
	HooksExecuted          bool                             `json:"hooks_executed"`
}

type RepositoryCommitFileChangeView struct {
	Path           string `json:"path"`
	Change         string `json:"change"`
	PreviousKind   string `json:"previous_kind"`
	CurrentKind    string `json:"current_kind"`
	ContentChanged bool   `json:"content_changed"`
	ModeChanged    bool   `json:"mode_changed"`
}

type RepositoryCommitDetailView struct {
	ProtocolVersion        string                           `json:"protocol_version"`
	WorkspaceID            string                           `json:"workspace_id"`
	Kind                   string                           `json:"kind"`
	Available              bool                             `json:"available"`
	ObjectID               string                           `json:"object_id"`
	Hash                   string                           `json:"hash"`
	Subject                string                           `json:"subject"`
	CommittedAt            time.Time                        `json:"committed_at"`
	ParentCount            int                              `json:"parent_count"`
	Changes                []RepositoryCommitFileChangeView `json:"changes"`
	ChangedFileCount       int                              `json:"changed_file_count"`
	ReturnedChangeCount    int                              `json:"returned_change_count"`
	OmittedChangeCount     int                              `json:"omitted_change_count"`
	RedactionCount         int                              `json:"redaction_count"`
	Truncated              bool                             `json:"truncated"`
	FirstParentOnly        bool                             `json:"first_parent_only"`
	ReadOnly               bool                             `json:"read_only"`
	RootPathExposed        bool                             `json:"root_path_exposed"`
	AuthorIdentityIncluded bool                             `json:"author_identity_included"`
	CommitBodyIncluded     bool                             `json:"commit_body_included"`
	FileContentIncluded    bool                             `json:"file_content_included"`
	PatchIncluded          bool                             `json:"patch_included"`
	RemoteConfigIncluded   bool                             `json:"remote_config_included"`
	CheckoutPerformed      bool                             `json:"checkout_performed"`
	ReferenceUpdated       bool                             `json:"reference_updated"`
	ProcessStarted         bool                             `json:"process_started"`
	NetworkUsed            bool                             `json:"network_used"`
	HooksExecuted          bool                             `json:"hooks_executed"`
}

type RepositoryCommitComparisonView struct {
	ProtocolVersion        string                           `json:"protocol_version"`
	WorkspaceID            string                           `json:"workspace_id"`
	Kind                   string                           `json:"kind"`
	Available              bool                             `json:"available"`
	BaseObjectID           string                           `json:"base_object_id"`
	BaseHash               string                           `json:"base_hash"`
	BaseSubject            string                           `json:"base_subject"`
	BaseCommittedAt        time.Time                        `json:"base_committed_at"`
	BaseRedacted           bool                             `json:"base_redacted"`
	BaseSubjectBounded     bool                             `json:"base_subject_bounded"`
	HeadObjectID           string                           `json:"head_object_id"`
	HeadHash               string                           `json:"head_hash"`
	HeadSubject            string                           `json:"head_subject"`
	HeadCommittedAt        time.Time                        `json:"head_committed_at"`
	HeadRedacted           bool                             `json:"head_redacted"`
	HeadSubjectBounded     bool                             `json:"head_subject_bounded"`
	SameObject             bool                             `json:"same_object"`
	Changes                []RepositoryCommitFileChangeView `json:"changes"`
	ChangedFileCount       int                              `json:"changed_file_count"`
	ReturnedChangeCount    int                              `json:"returned_change_count"`
	OmittedChangeCount     int                              `json:"omitted_change_count"`
	RedactionCount         int                              `json:"redaction_count"`
	Truncated              bool                             `json:"truncated"`
	MetadataOnly           bool                             `json:"metadata_only"`
	ReadOnly               bool                             `json:"read_only"`
	RenameInferred         bool                             `json:"rename_inferred"`
	AncestorRequired       bool                             `json:"ancestor_required"`
	AuthorityGranted       bool                             `json:"authority_granted"`
	RootPathExposed        bool                             `json:"root_path_exposed"`
	AuthorIdentityIncluded bool                             `json:"author_identity_included"`
	CommitBodyIncluded     bool                             `json:"commit_body_included"`
	FileContentIncluded    bool                             `json:"file_content_included"`
	PatchIncluded          bool                             `json:"patch_included"`
	RemoteConfigIncluded   bool                             `json:"remote_config_included"`
	CheckoutPerformed      bool                             `json:"checkout_performed"`
	ReferenceUpdated       bool                             `json:"reference_updated"`
	ProcessStarted         bool                             `json:"process_started"`
	NetworkUsed            bool                             `json:"network_used"`
	HooksExecuted          bool                             `json:"hooks_executed"`
}

type RepositoryCommitFilePreviewProvenanceView struct {
	Version               string `json:"version"`
	SourceKind            string `json:"source_kind"`
	SourceRef             string `json:"source_ref"`
	ContentSHA256         string `json:"content_sha256"`
	InstructionAuthorized bool   `json:"instruction_authorized"`
}

type RepositoryCommitFilePreviewView struct {
	ProtocolVersion         string                                    `json:"protocol_version"`
	WorkspaceID             string                                    `json:"workspace_id"`
	ObjectID                string                                    `json:"object_id"`
	Hash                    string                                    `json:"hash"`
	Path                    string                                    `json:"path"`
	Kind                    string                                    `json:"kind"`
	Content                 string                                    `json:"content"`
	TotalBytes              int64                                     `json:"total_bytes"`
	ReturnedBytes           int                                       `json:"returned_bytes"`
	RedactionCount          int                                       `json:"redaction_count"`
	Redacted                bool                                      `json:"redacted"`
	Provenance              RepositoryCommitFilePreviewProvenanceView `json:"provenance"`
	ReadOnly                bool                                      `json:"read_only"`
	MutationSupported       bool                                      `json:"mutation_supported"`
	AuthorityGranted        bool                                      `json:"authority_granted"`
	RootPathExposed         bool                                      `json:"root_path_exposed"`
	RawBlobIncluded         bool                                      `json:"raw_blob_included"`
	RedactedContentIncluded bool                                      `json:"redacted_content_included"`
	RemoteConfigIncluded    bool                                      `json:"remote_config_included"`
	CheckoutPerformed       bool                                      `json:"checkout_performed"`
	ReferenceUpdated        bool                                      `json:"reference_updated"`
	ProcessStarted          bool                                      `json:"process_started"`
	NetworkUsed             bool                                      `json:"network_used"`
	HooksExecuted           bool                                      `json:"hooks_executed"`
}

type RepositoryHistoryBranchView struct {
	Name    string `json:"name"`
	Head    string `json:"head"`
	Current bool   `json:"current"`
}

type RepositoryHistoryView struct {
	ProtocolVersion        string                        `json:"protocol_version"`
	WorkspaceID            string                        `json:"workspace_id"`
	Kind                   string                        `json:"kind"`
	Available              bool                          `json:"available"`
	Head                   string                        `json:"head"`
	Detached               bool                          `json:"detached"`
	Commits                []RepositoryHistoryCommitView `json:"commits"`
	Branches               []RepositoryHistoryBranchView `json:"branches"`
	ReturnedCommitCount    int                           `json:"returned_commit_count"`
	ReturnedBranchCount    int                           `json:"returned_branch_count"`
	OmittedBranchCount     int                           `json:"omitted_branch_count"`
	RedactionCount         int                           `json:"redaction_count"`
	Truncated              bool                          `json:"truncated"`
	FirstParentOnly        bool                          `json:"first_parent_only"`
	ReadOnly               bool                          `json:"read_only"`
	RootPathExposed        bool                          `json:"root_path_exposed"`
	AuthorIdentityIncluded bool                          `json:"author_identity_included"`
	CommitBodyIncluded     bool                          `json:"commit_body_included"`
	RemoteConfigIncluded   bool                          `json:"remote_config_included"`
	ProcessStarted         bool                          `json:"process_started"`
	NetworkUsed            bool                          `json:"network_used"`
	HooksExecuted          bool                          `json:"hooks_executed"`
}

type FileEditReviewView struct {
	Continuation    *application.ApprovalContinuationResult `json:"continuation,omitempty"`
	ProtocolVersion string                                  `json:"protocol_version"`
	RunID           string                                  `json:"run_id"`
	Action          string                                  `json:"action"`
	Edit            FileEditPreviewView                     `json:"edit"`
	Replayed        bool                                    `json:"replayed"`
	FileWritten     bool                                    `json:"file_written"`
}

type OperationReceiptView struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Kind            operationreceipt.Kind           `json:"kind"`
	Outcome         string                          `json:"outcome"`
	Durable         bool                            `json:"durable"`
	Replayed        bool                            `json:"replayed"`
	RetrySafe       bool                            `json:"retry_safe"`
	RetryStrategy   operationreceipt.RetryStrategy  `json:"retry_strategy"`
	RecoveryAction  operationreceipt.RecoveryAction `json:"recovery_action"`
	CleanupState    operationreceipt.CleanupState   `json:"cleanup_state"`
}

type OperationReceiptHistoryItemView struct {
	ID          string               `json:"id"`
	Scope       string               `json:"scope"`
	RunID       string               `json:"run_id,omitempty"`
	CompletedAt time.Time            `json:"completed_at"`
	Receipt     OperationReceiptView `json:"receipt"`
}

type OperationReceiptHistoryView struct {
	ProtocolVersion string                            `json:"protocol_version"`
	Items           []OperationReceiptHistoryItemView `json:"items"`
	Truncated       bool                              `json:"truncated"`
}

type OperatorActionItemView struct {
	ID          string                     `json:"id"`
	Kind        operatoraction.Kind        `json:"kind"`
	State       string                     `json:"state"`
	Destination operatoraction.Destination `json:"destination"`
	AvailableAt time.Time                  `json:"available_at"`
	DueAt       *time.Time                 `json:"due_at,omitempty"`
}

type OperatorActionCenterView struct {
	ProtocolVersion string                   `json:"protocol_version"`
	RunID           string                   `json:"run_id"`
	GeneratedAt     time.Time                `json:"generated_at"`
	Items           []OperatorActionItemView `json:"items"`
	Truncated       bool                     `json:"truncated"`
}

type EvidenceInventoryItemView struct {
	AttachmentID          string    `json:"attachment_id"`
	RunID                 string    `json:"run_id"`
	SessionID             string    `json:"session_id"`
	WorkspaceID           string    `json:"workspace_id"`
	SourceKind            string    `json:"source_kind"`
	SourceRef             string    `json:"source_ref"`
	ContentSHA256         string    `json:"content_sha256"`
	InstructionAuthorized bool      `json:"instruction_authorized"`
	AttachedAt            time.Time `json:"attached_at"`
}

type EvidenceInventoryView struct {
	ProtocolVersion string                      `json:"protocol_version"`
	RunID           string                      `json:"run_id"`
	Items           []EvidenceInventoryItemView `json:"items"`
	Truncated       bool                        `json:"truncated"`
}

type FileEditApplyView struct {
	ProtocolVersion string               `json:"protocol_version"`
	RunID           string               `json:"run_id"`
	Edit            FileEditPreviewView  `json:"edit"`
	Status          string               `json:"status"`
	Replayed        bool                 `json:"replayed"`
	FileWritten     bool                 `json:"file_written"`
	PolicyRechecked bool                 `json:"policy_rechecked"`
	Receipt         OperationReceiptView `json:"receipt"`
}

type RunWakeIntentView struct {
	ID                    string     `json:"id"`
	ProtocolVersion       string     `json:"protocol_version"`
	RunID                 string     `json:"run_id"`
	SessionID             string     `json:"session_id"`
	Status                string     `json:"status"`
	MaxAttempts           int        `json:"max_attempts"`
	AttemptCount          int        `json:"attempt_count"`
	InitialDelaySeconds   int        `json:"initial_delay_seconds"`
	BaseBackoffSeconds    int        `json:"base_backoff_seconds"`
	MaxBackoffSeconds     int        `json:"max_backoff_seconds"`
	MaxElapsedSeconds     int        `json:"max_elapsed_seconds"`
	NextWakeAt            time.Time  `json:"next_wake_at"`
	DeadlineAt            time.Time  `json:"deadline_at"`
	ExecutionEnabled      bool       `json:"execution_enabled"`
	BackgroundLoopEnabled bool       `json:"background_loop_enabled"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	CancelledAt           *time.Time `json:"cancelled_at,omitempty"`
}

type RunWakeStateView struct {
	ProtocolVersion string             `json:"protocol_version"`
	RunID           string             `json:"run_id"`
	Found           bool               `json:"found"`
	Intent          *RunWakeIntentView `json:"intent,omitempty"`
}

type RunWakeControlView struct {
	ProtocolVersion  string            `json:"protocol_version"`
	Action           string            `json:"action"`
	Intent           RunWakeIntentView `json:"intent"`
	Replayed         bool              `json:"replayed"`
	ExecutionStarted bool              `json:"execution_started"`
	ModelCalled      bool              `json:"model_called"`
	ToolCalled       bool              `json:"tool_called"`
}

type RunWakeExecutionView struct {
	ProtocolVersion       string               `json:"protocol_version"`
	RunID                 string               `json:"run_id"`
	Intent                RunWakeIntentView    `json:"intent"`
	ConsumptionStatus     string               `json:"consumption_status"`
	StopReason            string               `json:"stop_reason,omitempty"`
	Replayed              bool                 `json:"replayed"`
	ExecutionStarted      bool                 `json:"execution_started"`
	ModelCalled           bool                 `json:"model_called"`
	ToolCalled            bool                 `json:"tool_called"`
	BackgroundLoopEnabled bool                 `json:"background_loop_enabled"`
	Receipt               OperationReceiptView `json:"receipt"`
}

type ApprovalQueueItemView struct {
	ID                      string                              `json:"id"`
	ProposalID              string                              `json:"proposal_id"`
	RunID                   string                              `json:"run_id"`
	SessionID               string                              `json:"session_id"`
	WorkspaceID             string                              `json:"workspace_id"`
	ToolName                string                              `json:"tool_name"`
	ActionClass             string                              `json:"action_class"`
	Mode                    string                              `json:"mode"`
	Status                  string                              `json:"status"`
	AllowedActions          []application.ApprovalControlAction `json:"allowed_actions"`
	Version                 int64                               `json:"version"`
	CreatedAt               time.Time                           `json:"created_at"`
	UpdatedAt               time.Time                           `json:"updated_at"`
	ProcessExecutionEnabled bool                                `json:"process_execution_enabled"`
	CapabilityGrant         bool                                `json:"capability_grant"`
	CanonicalURL            string                              `json:"canonical_url,omitempty"`
	ExactTarget             string                              `json:"exact_target,omitempty"`
}

type ApprovalQueueView struct {
	ProtocolVersion         string                  `json:"protocol_version"`
	RunID                   string                  `json:"run_id"`
	Items                   []ApprovalQueueItemView `json:"items"`
	Truncated               bool                    `json:"truncated"`
	ProcessExecutionEnabled bool                    `json:"process_execution_enabled"`
	SessionGrantCreated     bool                    `json:"session_grant_created"`
	CapabilityGrant         bool                    `json:"capability_grant"`
}

type WorkspaceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type WorkspaceExplorerEntryView struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	Readable  bool   `json:"readable"`
}

type WorkspaceExplorerProvenanceView struct {
	Version               string `json:"version"`
	SourceKind            string `json:"source_kind"`
	SourceRef             string `json:"source_ref"`
	ContentSHA256         string `json:"content_sha256"`
	InstructionAuthorized bool   `json:"instruction_authorized"`
}

type WorkspaceExplorerView struct {
	ProtocolVersion string                          `json:"protocol_version"`
	WorkspaceID     string                          `json:"workspace_id"`
	Path            string                          `json:"path"`
	Kind            string                          `json:"kind"`
	Entries         []WorkspaceExplorerEntryView    `json:"entries"`
	Content         string                          `json:"content"`
	TotalBytes      int64                           `json:"total_bytes"`
	ReturnedBytes   int                             `json:"returned_bytes"`
	Truncated       bool                            `json:"truncated"`
	RedactionCount  int                             `json:"redaction_count"`
	RootPathExposed bool                            `json:"root_path_exposed"`
	Provenance      WorkspaceExplorerProvenanceView `json:"provenance"`
}

type WorkspaceSearchResultView struct {
	Path             string                          `json:"path"`
	MatchKind        string                          `json:"match_kind"`
	Line             int                             `json:"line"`
	Snippet          string                          `json:"snippet"`
	ContentTruncated bool                            `json:"content_truncated"`
	Provenance       WorkspaceExplorerProvenanceView `json:"provenance"`
}

type WorkspaceSearchView struct {
	ProtocolVersion string                      `json:"protocol_version"`
	WorkspaceID     string                      `json:"workspace_id"`
	Results         []WorkspaceSearchResultView `json:"results"`
	ScannedEntries  int                         `json:"scanned_entries"`
	ScannedFiles    int                         `json:"scanned_files"`
	ScannedBytes    int64                       `json:"scanned_bytes"`
	Truncated       bool                        `json:"truncated"`
	RootPathExposed bool                        `json:"root_path_exposed"`
}

type ScopeView struct {
	WorkspaceID       string               `json:"workspace_id,omitempty"`
	NetworkMode       string               `json:"network_mode"`
	AllowedTargets    []string             `json:"allowed_targets,omitempty"`
	WebAccessMode     domain.WebAccessMode `json:"web_access_mode"`
	WebAllowedTargets []string             `json:"web_allowed_targets,omitempty"`
}

type MissionView struct {
	ID          string    `json:"id"`
	Goal        string    `json:"goal"`
	Profile     string    `json:"profile"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Scope       ScopeView `json:"scope"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type RunConfigView struct {
	RequestedBudget                *domain.Budget `json:"requested_budget,omitempty"`
	ModelRoute                     string         `json:"model_route"`
	Interactive                    bool           `json:"interactive"`
	ProjectConfigFingerprint       string         `json:"project_config_fingerprint,omitempty"`
	ProjectInstructionsFingerprint string         `json:"project_instructions_fingerprint,omitempty"`
	ContinuityContextFingerprint   string         `json:"continuity_context_fingerprint,omitempty"`
}

type BudgetView struct {
	MaxTurns       int     `json:"max_turns"`
	MaxTokens      int64   `json:"max_tokens,omitempty"`
	MaxToolCalls   int64   `json:"max_tool_calls,omitempty"`
	MaxCostUSD     float64 `json:"max_cost_usd,omitempty"`
	TimeoutSeconds int64   `json:"timeout_seconds,omitempty"`
}

type RunView struct {
	StandardCodePresetConfigured *bool         `json:"standard_code_preset_configured,omitempty"`
	ID                           string        `json:"id"`
	MissionID                    string        `json:"mission_id"`
	SessionID                    string        `json:"session_id,omitempty"`
	Status                       string        `json:"status"`
	Config                       RunConfigView `json:"config"`
	Budget                       BudgetView    `json:"budget"`
	StartedAt                    *time.Time    `json:"started_at,omitempty"`
	FinishedAt                   *time.Time    `json:"finished_at,omitempty"`
	CreatedAt                    time.Time     `json:"created_at"`
	UpdatedAt                    time.Time     `json:"updated_at"`
}

type RunModeView struct {
	ProtocolVersion string    `json:"protocol_version"`
	Revision        int64     `json:"revision"`
	Surface         string    `json:"surface"`
	Phase           string    `json:"phase"`
	Profile         string    `json:"profile"`
	Scope           ScopeView `json:"scope"`
	PolicyVersion   string    `json:"policy_version"`
	RequestedBy     string    `json:"requested_by"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"created_at"`
	CapabilityGrant bool      `json:"capability_grant"`
}

type RunExecutionProfileView struct {
	ProtocolVersion     string    `json:"protocol_version"`
	Revision            int64     `json:"revision"`
	Profile             string    `json:"profile"`
	Backend             string    `json:"backend"`
	ApprovalPolicy      string    `json:"approval_policy"`
	FilesystemScope     string    `json:"filesystem_scope"`
	NetworkScope        string    `json:"network_scope"`
	RiskTier            string    `json:"risk_tier"`
	RequiredGate        string    `json:"required_gate"`
	PolicyVersion       string    `json:"policy_version"`
	CreatedAt           time.Time `json:"created_at"`
	ProcessEnabled      bool      `json:"process_enabled"`
	ExecutionAuthorized bool      `json:"execution_authorized"`
	CapabilityGrant     bool      `json:"capability_grant"`
}

type ExecutionPermissionRuntimeView struct {
	WorkspaceSandboxEnabled bool `json:"workspace_sandbox_enabled"`
	OperatorApprovalEnabled bool `json:"operator_approval_enabled"`
	DangerFullAccessEnabled bool `json:"danger_full_access_enabled"`
}

type ExecutionPermissionCapabilityMatrixView struct {
	WorkspaceRead           bool   `json:"workspace_read"`
	WorkspaceWrite          bool   `json:"workspace_write"`
	SandboxedCommandRuntime bool   `json:"sandboxed_command_runtime"`
	UnsandboxedHostProcess  bool   `json:"unsandboxed_host_process"`
	NetworkAccess           bool   `json:"network_access"`
	CredentialAccess        bool   `json:"credential_access"`
	UserHomeAccess          bool   `json:"user_home_access"`
	PersistentUserTerminal  bool   `json:"persistent_user_terminal"`
	PersistentAgentTerminal bool   `json:"persistent_agent_terminal"`
	FullCDP                 bool   `json:"full_cdp"`
	OutOfScopePolicy        string `json:"out_of_scope_policy"`
}

type RunExecutionPermissionView struct {
	ProtocolVersion       string                                  `json:"protocol_version"`
	Revision              int64                                   `json:"revision"`
	Mode                  string                                  `json:"mode"`
	ApprovalMode          string                                  `json:"approval_mode"`
	FullActivation        string                                  `json:"full_activation"`
	FullUnavailableReason string                                  `json:"full_unavailable_reason,omitempty"`
	ApprovalPolicy        string                                  `json:"approval_policy"`
	CommandScope          string                                  `json:"command_scope"`
	FilesystemScope       string                                  `json:"filesystem_scope"`
	NetworkScope          string                                  `json:"network_scope"`
	PersistentTerminal    bool                                    `json:"persistent_terminal"`
	BackgroundProcess     bool                                    `json:"background_process"`
	AgentTerminalInput    bool                                    `json:"agent_terminal_input"`
	RiskTier              string                                  `json:"risk_tier"`
	RequiredGate          string                                  `json:"required_gate"`
	PolicyVersion         string                                  `json:"policy_version"`
	OperatorConfirmed     bool                                    `json:"operator_confirmed"`
	RuntimeGateAvailable  bool                                    `json:"runtime_gate_available"`
	Runtime               ExecutionPermissionRuntimeView          `json:"runtime"`
	CapabilityMatrix      ExecutionPermissionCapabilityMatrixView `json:"capability_matrix"`
	CreatedAt             time.Time                               `json:"created_at"`
	ProcessEnabled        bool                                    `json:"process_enabled"`
	ExecutionAuthorized   bool                                    `json:"execution_authorized"`
	CapabilityGrant       bool                                    `json:"capability_grant"`
}

type BrowserCDPPermissionRuntimeView struct {
	ControlEnabled         bool `json:"control_enabled"`
	FullDebugEnabled       bool `json:"full_debug_enabled"`
	ExecutionDebugSelected bool `json:"execution_debug_selected"`
}

type RunBrowserCDPPermissionView struct {
	ProtocolVersion        string                          `json:"protocol_version"`
	Revision               int64                           `json:"revision"`
	Mode                   string                          `json:"mode"`
	NavigateAllowed        bool                            `json:"navigate_allowed"`
	DOMSnapshotAllowed     bool                            `json:"dom_snapshot_allowed"`
	ScreenshotAllowed      bool                            `json:"screenshot_allowed"`
	RequestCaptureAllowed  bool                            `json:"request_capture_allowed"`
	RequestMutationAllowed bool                            `json:"request_mutation_allowed"`
	RequestReplayAllowed   bool                            `json:"request_replay_allowed"`
	CookieAccessAllowed    bool                            `json:"cookie_access_allowed"`
	ArbitraryMethodAllowed bool                            `json:"arbitrary_method_allowed"`
	RiskTier               string                          `json:"risk_tier"`
	RequiredGate           string                          `json:"required_gate"`
	PolicyVersion          string                          `json:"policy_version"`
	OperatorConfirmed      bool                            `json:"operator_confirmed"`
	RuntimeGateAvailable   bool                            `json:"runtime_gate_available"`
	Runtime                BrowserCDPPermissionRuntimeView `json:"runtime"`
	CreatedAt              time.Time                       `json:"created_at"`
	TransportEnabled       bool                            `json:"transport_enabled"`
	BrowserStartAuthorized bool                            `json:"browser_start_authorized"`
	RuntimeAuthorized      bool                            `json:"runtime_authorized"`
	CapabilityGrant        bool                            `json:"capability_grant"`
}

type RunExecutionInteractionView struct {
	ProtocolVersion          string    `json:"protocol_version"`
	Revision                 int64     `json:"revision"`
	Mode                     string    `json:"mode"`
	Surface                  string    `json:"surface"`
	ExecutionProfile         string    `json:"execution_profile"`
	ExecutionProfileRevision int64     `json:"execution_profile_revision"`
	WorkspaceTrust           string    `json:"workspace_trust"`
	CommandForm              string    `json:"command_form"`
	PersistentTerminal       bool      `json:"persistent_terminal"`
	UserInputAvailable       bool      `json:"user_input_available"`
	AgentInputDefault        bool      `json:"agent_input_default"`
	NetworkScope             string    `json:"network_scope"`
	RequiredGate             string    `json:"required_gate"`
	PolicyVersion            string    `json:"policy_version"`
	OperatorConfirmed        bool      `json:"operator_confirmed"`
	CreatedAt                time.Time `json:"created_at"`
	ProcessEnabled           bool      `json:"process_enabled"`
	ExecutionAuthorized      bool      `json:"execution_authorized"`
	CapabilityGrant          bool      `json:"capability_grant"`
}

type SupervisorCheckpointView struct {
	RunID           string    `json:"run_id"`
	NextTurn        int       `json:"next_turn"`
	Phase           string    `json:"phase"`
	AttemptID       string    `json:"attempt_id,omitempty"`
	RepairPhase     string    `json:"repair_phase,omitempty"`
	RepairReason    string    `json:"repair_reason,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
	ExecutionMillis int64     `json:"execution_millis"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ToolUsageView struct {
	Consumed    int64      `json:"consumed"`
	Limit       int64      `json:"limit"`
	Remaining   int64      `json:"remaining"`
	ExhaustedAt *time.Time `json:"exhausted_at,omitempty"`
}

type RunExecutionLeaseView struct {
	OwnerID    string     `json:"owner_id"`
	Generation int64      `json:"generation"`
	Status     string     `json:"status"`
	Active     bool       `json:"active"`
	AcquiredAt time.Time  `json:"acquired_at"`
	RenewedAt  time.Time  `json:"renewed_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

type OperatorSteeringMessageView struct {
	ID          string     `json:"id"`
	Sequence    int64      `json:"sequence"`
	Status      string     `json:"status"`
	Prepared    bool       `json:"prepared"`
	CreatedAt   time.Time  `json:"created_at"`
	CommittedAt *time.Time `json:"committed_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
}

type OperatorSteeringQueueView struct {
	Pending   int                           `json:"pending"`
	Prepared  int                           `json:"prepared"`
	Committed int                           `json:"committed"`
	Cancelled int                           `json:"cancelled"`
	Messages  []OperatorSteeringMessageView `json:"messages"`
}

type PlanDeliveryModuleView struct {
	Ordinal            int      `json:"ordinal"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Dependencies       []int    `json:"dependencies"`
}

type PlanDeliveryDirectionView struct {
	Ordinal   int                      `json:"ordinal"`
	Title     string                   `json:"title"`
	Summary   string                   `json:"summary"`
	Tradeoffs []string                 `json:"tradeoffs"`
	Modules   []PlanDeliveryModuleView `json:"modules"`
}

type PlanDeliveryProposalView struct {
	ID              string                      `json:"id"`
	ProtocolVersion string                      `json:"protocol_version"`
	Status          string                      `json:"status"`
	ModeRevision    int64                       `json:"mode_revision"`
	Directions      []PlanDeliveryDirectionView `json:"directions"`
	Version         int64                       `json:"version"`
	CreatedAt       time.Time                   `json:"created_at"`
}

type PlanDeliverySelectionItemView struct {
	Ordinal       int    `json:"ordinal"`
	ModuleOrdinal int    `json:"module_ordinal"`
	WorkItemID    string `json:"work_item_id"`
}

type PlanDeliverySelectionView struct {
	ID               string                          `json:"id"`
	ProposalID       string                          `json:"proposal_id"`
	DirectionOrdinal int                             `json:"direction_ordinal"`
	ManualAcceptance string                          `json:"manual_acceptance"`
	NoteID           string                          `json:"note_id"`
	Items            []PlanDeliverySelectionItemView `json:"items"`
	Version          int64                           `json:"version"`
	CreatedAt        time.Time                       `json:"created_at"`
}

type DeliveryCheckpointView struct {
	ID               string    `json:"id"`
	WorkItemID       string    `json:"work_item_id"`
	ModuleOrdinal    int       `json:"module_ordinal"`
	ModuleCount      int       `json:"module_count"`
	ModeRevision     int64     `json:"mode_revision"`
	WorkItemVersion  int64     `json:"work_item_version"`
	FullGateRequired bool      `json:"full_gate_required"`
	HandoffNoteID    string    `json:"handoff_note_id"`
	GateReady        bool      `json:"gate_ready"`
	CreatedAt        time.Time `json:"created_at"`
}

type PlanDeliveryStateView struct {
	Proposal             *PlanDeliveryProposalView  `json:"proposal,omitempty"`
	Selection            *PlanDeliverySelectionView `json:"selection,omitempty"`
	OperatorChoiceNeeded bool                       `json:"operator_choice_needed"`
	PhaseChangeNeeded    bool                       `json:"phase_change_needed"`
	CapabilityGrant      bool                       `json:"capability_grant"`
	DeliveryGateEnforced bool                       `json:"delivery_gate_enforced"`
	RequiredCheckpoints  int                        `json:"required_checkpoints"`
	ReadyCheckpoints     int                        `json:"ready_checkpoints"`
	Checkpoints          []DeliveryCheckpointView   `json:"checkpoints"`
	ContinuedCompletions []PlanCompletionSourceView `json:"continued_completions,omitempty"`
}

type PlanCompletionSourceView struct {
	WorkItemID        string    `json:"work_item_id"`
	SourceRunID       string    `json:"source_run_id"`
	SourceWorkItemID  string    `json:"source_work_item_id"`
	CheckpointID      string    `json:"checkpoint_id"`
	HandoffNoteID     string    `json:"handoff_note_id"`
	CompletionEventID string    `json:"completion_event_id,omitempty"`
	CompletedAt       time.Time `json:"completed_at"`
}

type RunDetailView struct {
	Run                  RunView                      `json:"run"`
	Mission              MissionView                  `json:"mission"`
	Mode                 RunModeView                  `json:"mode"`
	ExecutionProfile     RunExecutionProfileView      `json:"execution_profile"`
	ExecutionPermission  RunExecutionPermissionView   `json:"execution_permission"`
	BrowserCDPPermission RunBrowserCDPPermissionView  `json:"browser_cdp_permission"`
	ExecutionInteraction RunExecutionInteractionView  `json:"execution_interaction"`
	Checkpoint           *SupervisorCheckpointView    `json:"checkpoint,omitempty"`
	ModelUsage           *domain.ModelUsage           `json:"model_usage,omitempty"`
	MonetaryUsage        *MonetaryUsageView           `json:"monetary_usage,omitempty"`
	Lease                *RunExecutionLeaseView       `json:"execution_lease,omitempty"`
	Steering             OperatorSteeringQueueView    `json:"operator_steering"`
	ToolUsage            ToolUsageView                `json:"tool_usage"`
	AgentCodeTools       AgentCodeCapabilitiesView    `json:"agent_code_tools"`
	PlanDelivery         *PlanDeliveryStateView       `json:"plan_delivery,omitempty"`
	ExternalSkills       *ExternalSkillProjectionView `json:"external_skills,omitempty"`
}

type MonetaryUsageView struct {
	Tracked         bool   `json:"tracked"`
	Currency        string `json:"currency"`
	CapMicros       int64  `json:"cap_micros"`
	ReservedMicros  int64  `json:"reserved_micros"`
	SettledMicros   int64  `json:"settled_micros"`
	ReleasedMicros  int64  `json:"released_micros"`
	RemainingMicros int64  `json:"remaining_micros"`
	UnpricedCalls   int64  `json:"unpriced_calls"`
}

type CapabilityReadinessOptionView struct {
	Value            string   `json:"value"`
	Selected         bool     `json:"selected"`
	Selectable       bool     `json:"selectable"`
	RuntimeAvailable bool     `json:"runtime_available"`
	BlockedBy        []string `json:"blocked_by"`
	Remediation      []string `json:"remediation"`
	RestartRequired  bool     `json:"restart_required"`
}

type CommandRuntimeReadinessView struct {
	ProtocolAvailable bool   `json:"protocol_available"`
	AdapterInstalled  bool   `json:"adapter_installed"`
	AdapterReady      bool   `json:"adapter_ready"`
	CurrentRunGranted bool   `json:"current_run_granted"`
	AdapterKind       string `json:"adapter_kind,omitempty"`
	Backend           string `json:"backend,omitempty"`
}

type RunCapabilityReadinessView struct {
	ProtocolVersion       string                           `json:"protocol_version"`
	RunID                 string                           `json:"run_id"`
	Permissions           []CapabilityReadinessOptionView  `json:"permissions"`
	Profiles              []CapabilityReadinessOptionView  `json:"profiles"`
	Interactions          []CapabilityReadinessOptionView  `json:"interactions"`
	BrowserCDPPermissions []CapabilityReadinessOptionView  `json:"browser_cdp_permissions"`
	Presets               []CapabilityReadinessOptionView  `json:"presets"`
	CommandRuntime        CommandRuntimeReadinessView      `json:"command_runtime"`
	UIEvidence            VerificationFeatureReadinessView `json:"ui_evidence"`
	BatchValidation       VerificationFeatureReadinessView `json:"batch_validation"`
	CapabilityGrant       bool                             `json:"capability_grant"`
}

// Startup guidance is read-only. Changes are available only through the
// Desktop native confirmation, never through a capability-readiness request.
type VerificationFeatureReadinessView struct {
	Installed   bool     `json:"installed"`
	BlockedBy   []string `json:"blocked_by"`
	Remediation []string `json:"remediation"`
}

func runCapabilityReadinessView(
	value application.RunCapabilityReadiness,
) RunCapabilityReadinessView {
	return RunCapabilityReadinessView{
		ProtocolVersion: value.ProtocolVersion, RunID: value.RunID,
		Permissions:           capabilityReadinessOptionViews(value.Permissions),
		Profiles:              capabilityReadinessOptionViews(value.Profiles),
		Interactions:          capabilityReadinessOptionViews(value.Interactions),
		BrowserCDPPermissions: capabilityReadinessOptionViews(value.BrowserCDPPermissions),
		Presets:               capabilityReadinessOptionViews(value.Presets),
		CommandRuntime: CommandRuntimeReadinessView{
			ProtocolAvailable: value.CommandRuntime.ProtocolAvailable,
			AdapterInstalled:  value.CommandRuntime.AdapterInstalled,
			AdapterReady:      value.CommandRuntime.AdapterReady,
			CurrentRunGranted: value.CommandRuntime.CurrentRunGranted,
			AdapterKind:       value.CommandRuntime.AdapterKind,
			Backend:           value.CommandRuntime.Backend,
		},
		CapabilityGrant: false,
		UIEvidence:      VerificationFeatureReadinessView(value.UIEvidence),
		BatchValidation: VerificationFeatureReadinessView(value.BatchValidation),
	}
}

func capabilityReadinessOptionViews(
	values []application.CapabilityReadinessOption,
) []CapabilityReadinessOptionView {
	views := make([]CapabilityReadinessOptionView, len(values))
	for index, value := range values {
		blockedBy := make([]string, len(value.BlockedBy))
		for blockerIndex, blocker := range value.BlockedBy {
			blockedBy[blockerIndex] = string(blocker)
		}
		remediation := make([]string, len(value.Remediation))
		for remediationIndex, action := range value.Remediation {
			remediation[remediationIndex] = string(action)
		}
		views[index] = CapabilityReadinessOptionView{
			Value: value.Value, Selected: value.Selected, Selectable: value.Selectable,
			RuntimeAvailable: value.RuntimeAvailable, BlockedBy: blockedBy,
			Remediation: remediation, RestartRequired: value.RestartRequired,
		}
	}
	return views
}

type AgentCodeToolCapabilityView struct {
	Name          string `json:"name"`
	Class         string `json:"class"`
	Source        string `json:"source"`
	ReadOnly      bool   `json:"read_only"`
	Approval      string `json:"approval"`
	Available     bool   `json:"available"`
	RefusalReason string `json:"refusal_reason,omitempty"`
}

type AgentCodeCapabilitiesView struct {
	ProtocolVersion string                        `json:"protocol_version"`
	Generation      string                        `json:"generation"`
	Surface         string                        `json:"surface"`
	Phase           string                        `json:"phase"`
	Role            string                        `json:"role"`
	Profile         string                        `json:"profile"`
	PermissionMode  string                        `json:"permission_mode"`
	Tools           []AgentCodeToolCapabilityView `json:"tools"`
}

type SessionView struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Title       string    `json:"title"`
	Route       string    `json:"route"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ThreadView struct {
	ProjectWorkspaceID       string     `json:"project_workspace_id"`
	ProjectAssignmentVersion int64      `json:"project_assignment_version"`
	ID                       string     `json:"id"`
	ProtocolVersion          string     `json:"protocol_version"`
	WorkspaceID              string     `json:"workspace_id,omitempty"`
	MissionID                string     `json:"mission_id"`
	Title                    string     `json:"title"`
	Pinned                   bool       `json:"pinned,omitempty"`
	Status                   string     `json:"status"`
	ActiveRunID              string     `json:"active_run_id,omitempty"`
	LastRunID                string     `json:"last_run_id"`
	Version                  int64      `json:"version"`
	ComposerState            string     `json:"composer_state"`
	ExecutionState           string     `json:"execution_state,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	ArchivedAt               *time.Time `json:"archived_at,omitempty"`
	DeletedAt                *time.Time `json:"deleted_at,omitempty"`
}

type ThreadRunView struct {
	Run              RunView   `json:"run"`
	Ordinal          int64     `json:"ordinal"`
	PredecessorRunID string    `json:"predecessor_run_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type ThreadDetailView struct {
	Thread     ThreadView               `json:"thread"`
	Mission    MissionView              `json:"mission"`
	ActiveRun  *RunView                 `json:"active_run,omitempty"`
	LastRun    RunView                  `json:"last_run"`
	Runs       []ThreadRunView          `json:"runs"`
	Recovery   *ThreadRunRecoveryView   `json:"recovery,omitempty"`
	Statistics *domain.ThreadStatistics `json:"statistics,omitempty"`
}

type ThreadRunRecoveryView struct {
	Version            string    `json:"version"`
	RunID              string    `json:"run_id"`
	HandoffOperationID string    `json:"handoff_operation_id"`
	ErrorCode          string    `json:"error_code"`
	StopReason         string    `json:"stop_reason"`
	Detail             string    `json:"detail"`
	Quiescent          bool      `json:"quiescent"`
	FailedAt           time.Time `json:"failed_at"`
}

type ThreadMessageView struct {
	ID                    string    `json:"id"`
	ThreadID              string    `json:"thread_id"`
	RunID                 string    `json:"run_id"`
	SessionID             string    `json:"session_id"`
	Role                  string    `json:"role"`
	Content               string    `json:"content"`
	ProvenanceVersion     string    `json:"provenance_version"`
	SourceKind            string    `json:"source_kind"`
	SourceRef             string    `json:"source_ref,omitempty"`
	ContentSHA256         string    `json:"content_sha256"`
	InstructionAuthorized bool      `json:"instruction_authorized"`
	Status                string    `json:"status"`
	TokenEstimate         int       `json:"token_estimate"`
	Compacted             bool      `json:"compacted"`
	CreatedAt             time.Time `json:"created_at"`
}

type ThreadTranscriptItemView struct {
	PeerMessage           *domain.ThreadPeerMessage        `json:"peer_message,omitempty"`
	Version               string                           `json:"version"`
	ID                    string                           `json:"id"`
	CanonicalID           string                           `json:"canonical_id"`
	RunID                 string                           `json:"run_id"`
	RunOrdinal            int64                            `json:"run_ordinal"`
	Sequence              int64                            `json:"sequence"`
	Position              int                              `json:"position,omitempty"`
	ActivityType          string                           `json:"activity_type"`
	Stage                 string                           `json:"stage"`
	Kind                  string                           `json:"kind"`
	Source                string                           `json:"source"`
	Title                 string                           `json:"title"`
	Detail                string                           `json:"detail,omitempty"`
	Status                string                           `json:"status,omitempty"`
	Verifiable            bool                             `json:"verifiable"`
	InstructionAuthorized bool                             `json:"instruction_authorized"`
	AttemptID             string                           `json:"attempt_id,omitempty"`
	ModelAttempt          int                              `json:"model_attempt,omitempty"`
	ToolRound             int                              `json:"tool_round,omitempty"`
	ToolName              string                           `json:"tool_name,omitempty"`
	StreamResponseID      string                           `json:"stream_response_id,omitempty"`
	StreamItemID          string                           `json:"stream_item_id,omitempty"`
	StreamCallID          string                           `json:"stream_call_id,omitempty"`
	DurableCallID         string                           `json:"durable_call_id,omitempty"`
	SourceRef             string                           `json:"source_ref,omitempty"`
	BoundaryReason        string                           `json:"boundary_reason,omitempty"`
	DeliveryMode          string                           `json:"delivery_mode,omitempty"`
	PromotedToMessageID   string                           `json:"promoted_to_message_id,omitempty"`
	PromotedFromMessageID string                           `json:"promoted_from_message_id,omitempty"`
	ActivityDetailRef     string                           `json:"activity_detail_ref,omitempty"`
	DetailAvailable       bool                             `json:"detail_available,omitempty"`
	ActivitySummary       *ThreadActivitySummaryView       `json:"activity_summary,omitempty"`
	WebEvidence           *ThreadWebEvidenceView           `json:"web_evidence,omitempty"`
	Images                []domain.WorkspaceImage          `json:"images,omitempty"`
	Attachments           []domain.WorkspaceFileAttachment `json:"attachments,omitempty"`
	Provisional           bool                             `json:"provisional"`
	Durable               bool                             `json:"durable"`
	CreatedAt             time.Time                        `json:"created_at"`
}

type ThreadActivitySummaryView struct {
	Version              string `json:"version"`
	ActivityRef          string `json:"activity_ref"`
	Command              string `json:"command"`
	Status               string `json:"status"`
	ExitCode             *int   `json:"exit_code,omitempty"`
	DurationMilliseconds int64  `json:"duration_milliseconds"`
	CommandCount         int    `json:"command_count"`
}

type ThreadActivityDetailView struct {
	Version     string                         `json:"version"`
	ActivityRef string                         `json:"activity_ref"`
	RunID       string                         `json:"run_id"`
	Tools       []ThreadActivityToolDetailView `json:"tools"`
}

type ThreadActivityToolDetailView struct {
	Name                 string                        `json:"name"`
	Label                string                        `json:"label"`
	AgentID              string                        `json:"agent_id"`
	AgentRole            string                        `json:"agent_role"`
	AgentLabel           string                        `json:"agent_label"`
	Status               string                        `json:"status"`
	StartedAt            time.Time                     `json:"started_at"`
	CompletedAt          *time.Time                    `json:"completed_at,omitempty"`
	DurationMilliseconds int64                         `json:"duration_milliseconds"`
	Detail               ThreadActivityTypedDetailView `json:"detail"`
}

// ThreadActivityTypedDetailView is a tagged container. Exactly one branch is
// emitted and its name must equal kind. This concrete Go shape makes the
// public contract fail closed without passing raw payload JSON to React.
type ThreadActivityTypedDetailView struct {
	Kind         string                                `json:"kind"`
	Command      *ThreadActivityCommandGroupView       `json:"command,omitempty"`
	WebSearch    *ThreadActivityWebSearchDetailView    `json:"web_search,omitempty"`
	WebFetch     *ThreadActivityWebFetchDetailView     `json:"web_fetch,omitempty"`
	FileRead     *ThreadActivityFileReadDetailView     `json:"file_read,omitempty"`
	FileEdit     *ThreadActivityFileEditDetailView     `json:"file_edit,omitempty"`
	MCP          *ThreadActivityMCPDetailView          `json:"mcp,omitempty"`
	Verification *ThreadActivityVerificationDetailView `json:"verification,omitempty"`
	Browser      *ThreadActivityBrowserDetailView      `json:"browser,omitempty"`
}

type ThreadActivityBoundaryView struct {
	Authorization string `json:"authorization"`
	ErrorCode     string `json:"error_code"`
	FailureReason string `json:"failure_reason"`
	Truncated     bool   `json:"truncated"`
	Untrusted     bool   `json:"untrusted"`
}

type ThreadActivityCommandGroupView struct {
	Commands []ThreadActivityCommandDetailView `json:"commands"`
}

type ThreadActivitySearchSourceView struct {
	Rank     int    `json:"rank"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	Citeable bool   `json:"citeable"`
}

type ThreadActivityWebSearchDetailView struct {
	Operation       string                           `json:"operation"`
	Query           string                           `json:"query"`
	Limit           int                              `json:"limit"`
	Provider        string                           `json:"provider"`
	SearchPolicy    string                           `json:"search_policy"`
	SelectionReason string                           `json:"selection_reason"`
	SourceCount     int                              `json:"source_count"`
	Citeable        bool                             `json:"citeable"`
	Sources         []ThreadActivitySearchSourceView `json:"sources"`
	Boundary        ThreadActivityBoundaryView       `json:"boundary"`
}

type ThreadActivityWebFetchDetailView struct {
	Operation    string                     `json:"operation"`
	URL          string                     `json:"url"`
	State        string                     `json:"state"`
	HTTPStatus   int                        `json:"http_status"`
	Robots       string                     `json:"robots"`
	RobotsPolicy string                     `json:"robots_policy"`
	Redirects    int                        `json:"redirects"`
	Partial      bool                       `json:"partial"`
	Citeable     bool                       `json:"citeable"`
	Boundary     ThreadActivityBoundaryView `json:"boundary"`
}

type ThreadActivityFileReadDetailView struct {
	Operation   string                     `json:"operation"`
	Path        string                     `json:"path"`
	Query       string                     `json:"query"`
	Pattern     string                     `json:"pattern"`
	StartLine   int                        `json:"start_line"`
	EndLine     int                        `json:"end_line"`
	Limit       int                        `json:"limit"`
	ResultCount int                        `json:"result_count"`
	Truncated   bool                       `json:"truncated"`
	Summary     string                     `json:"summary"`
	Boundary    ThreadActivityBoundaryView `json:"boundary"`
}

type ThreadActivityDiffSummaryView struct {
	AddedLines   int    `json:"added_lines"`
	RemovedLines int    `json:"removed_lines"`
	Hunks        int    `json:"hunks"`
	Summary      string `json:"summary"`
}

type ThreadActivityFileEditDetailView struct {
	Operation       string                        `json:"operation"`
	Action          string                        `json:"action"`
	Path            string                        `json:"path"`
	DestinationPath string                        `json:"destination_path"`
	EditID          string                        `json:"edit_id"`
	DiffAvailable   bool                          `json:"diff_available"`
	ApplyStatus     string                        `json:"apply_status"`
	Applied         bool                          `json:"applied"`
	FileWritten     bool                          `json:"file_written"`
	Replayed        bool                          `json:"replayed"`
	Diff            ThreadActivityDiffSummaryView `json:"diff"`
	Boundary        ThreadActivityBoundaryView    `json:"boundary"`
}

type ThreadActivityJSONFieldSummaryView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

type ThreadActivityJSONSummaryView struct {
	Type    string                               `json:"type"`
	Count   int                                  `json:"count"`
	Summary string                               `json:"summary"`
	Fields  []ThreadActivityJSONFieldSummaryView `json:"fields"`
}

type ThreadActivityMCPDetailView struct {
	Operation        string                               `json:"operation"`
	Server           string                               `json:"server"`
	Tool             string                               `json:"tool"`
	Arguments        []ThreadActivityJSONFieldSummaryView `json:"arguments"`
	Result           ThreadActivityJSONSummaryView        `json:"result"`
	Contents         []ThreadActivityMCPContentView       `json:"contents,omitempty"`
	OmittedContents  int                                  `json:"omitted_contents,omitempty"`
	ContentTruncated bool                                 `json:"content_truncated,omitempty"`
	Boundary         ThreadActivityBoundaryView           `json:"boundary"`
}

type ThreadActivityMCPContentView struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	URI        string `json:"uri,omitempty"`
	Name       string `json:"name,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	ByteSize   int    `json:"byte_size,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Role       string `json:"role,omitempty"`
}

type ThreadActivityVerificationDetailView struct {
	Operation   string                     `json:"operation"`
	Tool        string                     `json:"tool"`
	Path        string                     `json:"path"`
	Query       string                     `json:"query"`
	Position    string                     `json:"position"`
	Direction   string                     `json:"direction"`
	Limit       int                        `json:"limit"`
	ResultCount int                        `json:"result_count"`
	Truncated   bool                       `json:"truncated"`
	Summary     string                     `json:"summary"`
	Boundary    ThreadActivityBoundaryView `json:"boundary"`
}

type ThreadActivityBrowserDetailView struct {
	Operation     string                     `json:"operation"`
	Action        string                     `json:"action"`
	URL           string                     `json:"url"`
	Selector      string                     `json:"selector"`
	InputLength   int                        `json:"input_length"`
	ArtifactBytes int64                      `json:"artifact_bytes"`
	Summary       string                     `json:"summary"`
	Boundary      ThreadActivityBoundaryView `json:"boundary"`
}

type ThreadActivityCommandDetailView struct {
	Command              string                                `json:"command"`
	WorkingDirectory     string                                `json:"working_directory"`
	ExecutionEnvironment string                                `json:"execution_environment"`
	Network              string                                `json:"network"`
	Status               string                                `json:"status"`
	ExitCode             *int                                  `json:"exit_code,omitempty"`
	DurationMilliseconds int64                                 `json:"duration_milliseconds"`
	StdoutPreview        string                                `json:"stdout_preview"`
	StderrPreview        string                                `json:"stderr_preview"`
	Truncated            bool                                  `json:"truncated"`
	Artifacts            []ThreadActivityArtifactReferenceView `json:"artifacts"`
}

type ThreadActivityArtifactReferenceView struct {
	ArtifactRef string `json:"artifact_ref"`
	Stream      string `json:"stream"`
	MIME        string `json:"mime"`
	SizeBytes   int64  `json:"size_bytes"`
	Truncated   bool   `json:"truncated"`
}

type ThreadActivityArtifactView struct {
	Version               string `json:"version"`
	ActivityRef           string `json:"activity_ref"`
	ArtifactRef           string `json:"artifact_ref"`
	Stream                string `json:"stream"`
	MIME                  string `json:"mime"`
	Content               string `json:"content"`
	SHA256                string `json:"sha256"`
	SizeBytes             int64  `json:"size_bytes"`
	Redacted              bool   `json:"redacted"`
	Truncated             bool   `json:"truncated"`
	Untrusted             bool   `json:"untrusted"`
	InstructionAuthorized bool   `json:"instruction_authorized"`
}

type ThreadWebEvidenceView struct {
	Version               string    `json:"version"`
	SourceID              string    `json:"source_id"`
	SnapshotID            string    `json:"snapshot_id"`
	CitationID            string    `json:"citation_id,omitempty"`
	URL                   string    `json:"url"`
	Title                 string    `json:"title,omitempty"`
	State                 string    `json:"state"`
	FetchedAt             time.Time `json:"fetched_at"`
	StaleAt               time.Time `json:"stale_at"`
	Digest                string    `json:"digest"`
	Partial               bool      `json:"partial"`
	Stale                 bool      `json:"stale"`
	Citeable              bool      `json:"citeable"`
	Untrusted             bool      `json:"untrusted"`
	InstructionAuthorized bool      `json:"instruction_authorized"`
}

type ThreadEventView struct {
	ID        int64           `json:"id"`
	ThreadID  string          `json:"thread_id"`
	RunID     string          `json:"run_id,omitempty"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type ThreadExportView struct {
	Missions        []MissionView             `json:"missions"`
	ProtocolVersion string                    `json:"protocol_version"`
	ExportedAt      time.Time                 `json:"exported_at"`
	Thread          ThreadView                `json:"thread"`
	Mission         MissionView               `json:"mission"`
	Runs            []ThreadRunView           `json:"runs"`
	Sessions        []SessionView             `json:"sessions"`
	Messages        []ThreadMessageView       `json:"messages"`
	Events          []ThreadEventView         `json:"events"`
	AuditEvents     []ThreadRunAuditEventView `json:"audit_events"`
}

type ThreadRunAuditEventView struct {
	EventID   string          `json:"event_id"`
	Version   string          `json:"version"`
	RunID     string          `json:"run_id"`
	MissionID string          `json:"mission_id"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	SubjectID string          `json:"subject_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type SessionDetailView struct {
	Session SessionView `json:"session"`
	Run     *RunView    `json:"run,omitempty"`
}

type MessageView struct {
	ID                    int64            `json:"id"`
	SessionID             string           `json:"session_id"`
	Role                  string           `json:"role"`
	Content               string           `json:"content"`
	ProvenanceVersion     string           `json:"provenance_version"`
	SourceKind            string           `json:"source_kind"`
	SourceRef             string           `json:"source_ref,omitempty"`
	ContentSHA256         string           `json:"content_sha256"`
	InstructionAuthorized bool             `json:"instruction_authorized"`
	TokenEstimate         int              `json:"token_estimate"`
	Compacted             bool             `json:"compacted"`
	CreatedAt             time.Time        `json:"created_at"`
	OutputItems           []llm.OutputItem `json:"output_items"`
}

type EventView struct {
	EventID   string          `json:"event_id"`
	Version   string          `json:"version"`
	RunID     string          `json:"run_id"`
	MissionID string          `json:"mission_id"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	SubjectID string          `json:"subject_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type RunActivityView struct {
	Version                  string                `json:"version"`
	RunID                    string                `json:"run_id"`
	ThroughSequence          int64                 `json:"through_sequence"`
	Truncated                bool                  `json:"truncated"`
	PrivateReasoningIncluded bool                  `json:"private_reasoning_included"`
	Items                    []RunActivityItemView `json:"items"`
}

type RunActivityItemView struct {
	ID                    string    `json:"id"`
	Sequence              int64     `json:"sequence"`
	Kind                  string    `json:"kind"`
	Source                string    `json:"source"`
	Title                 string    `json:"title"`
	Detail                string    `json:"detail,omitempty"`
	Status                string    `json:"status,omitempty"`
	Verifiable            bool      `json:"verifiable"`
	InstructionAuthorized bool      `json:"instruction_authorized"`
	AttemptID             string    `json:"attempt_id,omitempty"`
	ModelAttempt          int       `json:"model_attempt,omitempty"`
	ToolRound             int       `json:"tool_round,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type WorkItemView struct {
	ID                 string     `json:"id"`
	RunID              string     `json:"run_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description,omitempty"`
	Status             string     `json:"status"`
	Priority           string     `json:"priority"`
	Owner              string     `json:"owner,omitempty"`
	OwnerAgentID       string     `json:"owner_agent_id,omitempty"`
	AcceptanceCriteria []string   `json:"acceptance_criteria"`
	Dependencies       []string   `json:"dependencies"`
	BlockedReason      string     `json:"blocked_reason,omitempty"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type NoteView struct {
	ID           string     `json:"id"`
	RunID        string     `json:"run_id"`
	Title        string     `json:"title"`
	Content      string     `json:"content"`
	Category     string     `json:"category"`
	Visibility   string     `json:"visibility"`
	Owner        string     `json:"owner,omitempty"`
	OwnerAgentID string     `json:"owner_agent_id,omitempty"`
	Tags         []string   `json:"tags"`
	SourceRefs   []string   `json:"source_refs"`
	EvidenceIDs  []string   `json:"evidence_ids"`
	Status       string     `json:"status"`
	Pinned       bool       `json:"pinned"`
	Version      int64      `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
}

type ArtifactView struct {
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	SessionID   string    `json:"session_id"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	SourceID    string    `json:"source_id"`
	ToolName    string    `json:"tool_name"`
	Stream      string    `json:"stream"`
	Kind        string    `json:"kind"`
	MIME        string    `json:"mime"`
	Encoding    string    `json:"encoding"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
	Redacted    bool      `json:"redacted"`
	CreatedAt   time.Time `json:"created_at"`
}

type SupervisorToolCallView struct {
	Position         int                           `json:"position"`
	ModelAttempt     int                           `json:"model_attempt"`
	CallID           string                        `json:"call_id"`
	StreamResponseID string                        `json:"stream_response_id,omitempty"`
	StreamItemID     string                        `json:"stream_item_id,omitempty"`
	StreamCallID     string                        `json:"stream_call_id,omitempty"`
	ToolName         string                        `json:"tool_name"`
	Status           string                        `json:"status"`
	ErrorCode        string                        `json:"error_code,omitempty"`
	DetailAvailable  bool                          `json:"detail_available"`
	Detail           *ThreadActivityToolDetailView `json:"detail,omitempty"`
	CreatedAt        time.Time                     `json:"created_at"`
	CompletedAt      *time.Time                    `json:"completed_at,omitempty"`
}

type SupervisorToolRoundView struct {
	ThreadID     string                   `json:"thread_id,omitempty"`
	RunID        string                   `json:"run_id"`
	Turn         int                      `json:"turn"`
	AttemptID    string                   `json:"attempt_id"`
	Round        int                      `json:"round"`
	ModelAttempt int                      `json:"model_attempt"`
	Calls        []SupervisorToolCallView `json:"calls"`
	CreatedAt    time.Time                `json:"created_at"`
	CompletedAt  *time.Time               `json:"completed_at,omitempty"`
}

func missionView(value domain.Mission) MissionView {
	return MissionView{
		ID: value.ID, Goal: value.Goal, Profile: string(value.Profile), WorkspaceID: value.WorkspaceID,
		Scope: ScopeView{WebAccessMode: value.Scope.EffectiveWebAccessMode(), WebAllowedTargets: value.Scope.EffectiveWebAllowedTargets(), WorkspaceID: value.Scope.WorkspaceID, NetworkMode: value.Scope.NetworkMode,
			AllowedTargets: append([]string{}, value.Scope.AllowedTargets...)},
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func runView(value domain.Run) RunView {
	return RunView{
		ID: value.ID, MissionID: value.MissionID, SessionID: value.SessionID, Status: string(value.Status),
		Config: RunConfigView{ModelRoute: value.Config.ModelRoute, Interactive: value.Config.Interactive, RequestedBudget: value.Config.RequestedBudget,
			ProjectConfigFingerprint:       value.Config.ProjectConfigFingerprint,
			ProjectInstructionsFingerprint: value.Config.ProjectInstructionsFingerprint,
			ContinuityContextFingerprint:   value.Config.ContinuityContextFingerprint},
		Budget: BudgetView{MaxTurns: value.Budget.MaxTurns, MaxTokens: value.Budget.MaxTokens,
			MaxToolCalls: value.Budget.MaxToolCalls, MaxCostUSD: value.Budget.MaxCostUSD,
			TimeoutSeconds: value.Budget.TimeoutSeconds},
		StartedAt: value.StartedAt, FinishedAt: value.FinishedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func runModeView(value domain.RunModeSnapshot) RunModeView {
	return RunModeView{
		ProtocolVersion: value.ProtocolVersion, Revision: value.Revision,
		Surface: string(value.Surface), Phase: string(value.Phase),
		Profile: string(value.Profile),
		Scope: ScopeView{WebAccessMode: value.Scope.EffectiveWebAccessMode(), WebAllowedTargets: value.Scope.EffectiveWebAllowedTargets(), WorkspaceID: value.Scope.WorkspaceID,
			NetworkMode:    value.Scope.NetworkMode,
			AllowedTargets: append([]string{}, value.Scope.AllowedTargets...)},
		PolicyVersion: value.PolicyVersion, RequestedBy: value.RequestedBy,
		Reason: value.Reason, CreatedAt: value.CreatedAt, CapabilityGrant: false,
	}
}

func runExecutionProfileView(
	value domain.RunExecutionProfileSnapshot,
) RunExecutionProfileView {
	return RunExecutionProfileView{
		ProtocolVersion: value.ProtocolVersion, Revision: value.Revision,
		Profile: string(value.Profile), Backend: string(value.Backend),
		ApprovalPolicy:  string(value.ApprovalPolicy),
		FilesystemScope: string(value.FilesystemScope),
		NetworkScope:    string(value.NetworkScope), RiskTier: string(value.RiskTier),
		RequiredGate: string(value.RequiredGate), PolicyVersion: value.PolicyVersion,
		CreatedAt:           value.CreatedAt,
		ProcessEnabled:      value.ProcessEnabled,
		ExecutionAuthorized: value.ExecutionAuthorized,
		CapabilityGrant:     value.CapabilityGrant,
	}
}

func runExecutionPermissionView(value domain.RunExecutionPermissionSnapshot,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) RunExecutionPermissionView {
	matrix, _ := value.CapabilityMatrix()
	activation, unavailable := approvalFullActivation(value.Mode, capabilities.AllowsSnapshot(value), capabilities)
	return RunExecutionPermissionView{
		ProtocolVersion: value.ProtocolVersion, Revision: value.Revision,
		Mode: string(value.Mode), ApprovalPolicy: string(value.ApprovalPolicy),
		ApprovalMode: string(value.Mode.ApprovalPreference()), FullActivation: activation, FullUnavailableReason: unavailable,
		CommandScope:       string(value.CommandScope),
		FilesystemScope:    string(value.FilesystemScope),
		NetworkScope:       string(value.NetworkScope),
		PersistentTerminal: value.PersistentTerminal,
		BackgroundProcess:  value.BackgroundProcess,
		AgentTerminalInput: value.AgentTerminalInput, RiskTier: string(value.RiskTier),
		RequiredGate: string(value.RequiredGate), PolicyVersion: value.PolicyVersion,
		OperatorConfirmed:    value.OperatorConfirmed,
		RuntimeGateAvailable: capabilities.AllowsSnapshot(value),
		Runtime:              executionPermissionRuntimeView(capabilities),
		CapabilityMatrix: ExecutionPermissionCapabilityMatrixView{
			WorkspaceRead: matrix.WorkspaceRead, WorkspaceWrite: matrix.WorkspaceWrite,
			SandboxedCommandRuntime: matrix.SandboxedCommandRuntime,
			UnsandboxedHostProcess:  matrix.UnsandboxedHostProcess,
			NetworkAccess:           matrix.NetworkAccess, CredentialAccess: matrix.CredentialAccess,
			UserHomeAccess:          matrix.UserHomeAccess,
			PersistentUserTerminal:  matrix.PersistentUserTerminal,
			PersistentAgentTerminal: matrix.PersistentAgentTerminal,
			FullCDP:                 matrix.FullCDP, OutOfScopePolicy: string(matrix.OutOfScopePolicy),
		},
		CreatedAt: value.CreatedAt, ProcessEnabled: value.ProcessEnabled,
		ExecutionAuthorized: value.ExecutionAuthorized,
		CapabilityGrant:     value.CapabilityGrant,
	}
}

func runBrowserCDPPermissionView(value domain.RunBrowserCDPPermissionSnapshot,
	capabilities domain.BrowserCDPPermissionRuntimeCapabilities,
	executionPermission domain.RunExecutionPermissionSnapshot,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
) RunBrowserCDPPermissionView {
	executionDebugSelected := executionPermission.Mode == domain.RunExecutionPermissionDebug
	executionFullSelected := executionPermission.Mode.IncludesFullAccess()
	runtimeAvailable := capabilities.Allows(value.Mode)
	if value.Mode == domain.RunBrowserCDPPermissionFullDebug {
		runtimeAvailable = runtimeAvailable && executionFullSelected &&
			executionCapabilities.AllowsSnapshot(executionPermission)
	}
	return RunBrowserCDPPermissionView{
		ProtocolVersion: value.ProtocolVersion, Revision: value.Revision,
		Mode: string(value.Mode), NavigateAllowed: value.NavigateAllowed,
		DOMSnapshotAllowed:     value.DOMSnapshotAllowed,
		ScreenshotAllowed:      value.ScreenshotAllowed,
		RequestCaptureAllowed:  value.RequestCaptureAllowed,
		RequestMutationAllowed: value.RequestMutationAllowed,
		RequestReplayAllowed:   value.RequestReplayAllowed,
		CookieAccessAllowed:    value.CookieAccessAllowed,
		ArbitraryMethodAllowed: value.ArbitraryMethodAllowed,
		RiskTier:               string(value.RiskTier), RequiredGate: string(value.RequiredGate),
		PolicyVersion: value.PolicyVersion, OperatorConfirmed: value.OperatorConfirmed,
		RuntimeGateAvailable: runtimeAvailable,
		Runtime: BrowserCDPPermissionRuntimeView{
			ControlEnabled:         capabilities.ControlEnabled,
			FullDebugEnabled:       capabilities.FullDebugEnabled,
			ExecutionDebugSelected: executionDebugSelected,
		},
		CreatedAt: value.CreatedAt, TransportEnabled: value.TransportEnabled,
		BrowserStartAuthorized: value.BrowserStartAuthorized,
		RuntimeAuthorized:      value.RuntimeAuthorized,
		CapabilityGrant:        value.CapabilityGrant,
	}
}

func runExecutionInteractionView(
	value domain.RunExecutionInteractionSnapshot,
) RunExecutionInteractionView {
	return RunExecutionInteractionView{
		ProtocolVersion: value.ProtocolVersion, Revision: value.Revision,
		Mode: string(value.Mode), Surface: string(value.Surface),
		ExecutionProfile:         string(value.ExecutionProfile),
		ExecutionProfileRevision: value.ExecutionProfileRevision,
		WorkspaceTrust:           string(value.WorkspaceTrust),
		CommandForm:              string(value.CommandForm),
		PersistentTerminal:       value.PersistentTerminal,
		UserInputAvailable:       value.UserInputAvailable,
		AgentInputDefault:        false,
		NetworkScope:             string(value.NetworkScope),
		RequiredGate:             string(value.RequiredGate),
		PolicyVersion:            value.PolicyVersion,
		OperatorConfirmed:        value.OperatorConfirmed,
		CreatedAt:                value.CreatedAt,
		ProcessEnabled:           false,
		ExecutionAuthorized:      false,
		CapabilityGrant:          false,
	}
}

func checkpointView(value domain.SupervisorCheckpoint) SupervisorCheckpointView {
	return SupervisorCheckpointView{
		RunID: value.RunID, NextTurn: value.NextTurn, Phase: string(value.Phase), AttemptID: value.AttemptID,
		RepairPhase: string(value.RepairPhase), RepairReason: value.RepairReason, LastError: value.LastError,
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, TotalTokens: value.TotalTokens,
		ExecutionMillis: value.ExecutionMillis, UpdatedAt: value.UpdatedAt,
	}
}

func toolUsageView(value toolbudget.Usage) ToolUsageView {
	return ToolUsageView{Consumed: value.Consumed, Limit: value.Limit,
		Remaining: value.Remaining, ExhaustedAt: value.ExhaustedAt}
}

func runExecutionLeaseView(value domain.RunExecutionLease, now time.Time) RunExecutionLeaseView {
	return RunExecutionLeaseView{
		OwnerID: value.OwnerID, Generation: value.Generation, Status: string(value.Status),
		Active: value.ActiveAt(now), AcquiredAt: value.AcquiredAt, RenewedAt: value.RenewedAt,
		ExpiresAt: value.ExpiresAt, ReleasedAt: value.ReleasedAt,
	}
}

func operatorSteeringQueueView(summary domain.OperatorSteeringQueueSummary,
	values []domain.OperatorSteeringMessage,
) OperatorSteeringQueueView {
	messages := make([]OperatorSteeringMessageView, len(values))
	for index, value := range values {
		messages[index] = operatorSteeringMessageView(value)
	}
	return OperatorSteeringQueueView{
		Pending: summary.Pending, Prepared: summary.Prepared,
		Committed: summary.Committed, Cancelled: summary.Cancelled,
		Messages: messages,
	}
}

func operatorSteeringMessageView(value domain.OperatorSteeringMessage) OperatorSteeringMessageView {
	return OperatorSteeringMessageView{
		ID: value.ID, Sequence: value.Sequence, Status: string(value.Status),
		Prepared:  value.Prepared,
		CreatedAt: value.CreatedAt, CommittedAt: value.CommittedAt,
		CancelledAt: value.CancelledAt,
	}
}

func planDeliveryProposalView(value domain.PlanDeliveryProposal) PlanDeliveryProposalView {
	directions := make([]PlanDeliveryDirectionView, len(value.Spec.Directions))
	for directionIndex, direction := range value.Spec.Directions {
		modules := make([]PlanDeliveryModuleView, len(direction.Modules))
		for moduleIndex, module := range direction.Modules {
			modules[moduleIndex] = PlanDeliveryModuleView{
				Ordinal: module.Ordinal, Title: module.Title, Objective: module.Objective,
				AcceptanceCriteria: append([]string{}, module.AcceptanceCriteria...),
				Dependencies:       append([]int{}, module.Dependencies...),
			}
		}
		directions[directionIndex] = PlanDeliveryDirectionView{
			Ordinal: direction.Ordinal, Title: direction.Title, Summary: direction.Summary,
			Tradeoffs: append([]string{}, direction.Tradeoffs...), Modules: modules,
		}
	}
	return PlanDeliveryProposalView{
		ID: value.ID, ProtocolVersion: value.Spec.Version, Status: string(value.Status),
		ModeRevision: value.ModeRevision, Directions: directions, Version: value.Version,
		CreatedAt: value.CreatedAt,
	}
}

func planDeliverySelectionView(value domain.PlanDeliverySelection) PlanDeliverySelectionView {
	items := make([]PlanDeliverySelectionItemView, len(value.Items))
	for index, item := range value.Items {
		items[index] = PlanDeliverySelectionItemView{
			Ordinal: item.Ordinal, ModuleOrdinal: item.ModuleOrdinal, WorkItemID: item.WorkItemID,
		}
	}
	return PlanDeliverySelectionView{
		ID: value.ID, ProposalID: value.ProposalID, DirectionOrdinal: value.DirectionOrdinal,
		ManualAcceptance: string(value.EffectiveManualAcceptance()),
		NoteID:           value.NoteID, Items: items, Version: value.Version, CreatedAt: value.CreatedAt,
	}
}

func deliveryCheckpointView(value domain.DeliveryCheckpoint,
	ready bool,
) DeliveryCheckpointView {
	return DeliveryCheckpointView{
		ID: value.ID, WorkItemID: value.WorkItemID,
		ModuleOrdinal: value.ModuleOrdinal, ModuleCount: value.ModuleCount,
		ModeRevision: value.ModeRevision, WorkItemVersion: value.WorkItemVersion,
		FullGateRequired: value.FullGateRequired, HandoffNoteID: value.HandoffNoteID,
		GateReady: ready, CreatedAt: value.CreatedAt,
	}
}

func sessionView(value session.Session) SessionView {
	return SessionView{ID: value.ID, WorkspaceID: value.WorkspaceID, Title: value.Title, Route: value.Route,
		Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func threadView(value domain.Thread, activeRunStatus ...domain.RunStatus) ThreadView {
	composerState := "ready"
	if value.Status != domain.ThreadActive {
		composerState = "unavailable"
	} else if value.ActiveRunID == "" {
		composerState = "successor_required"
	} else if len(activeRunStatus) > 0 && activeRunStatus[0] == domain.RunWaitingApproval {
		composerState = "waiting_approval"
	}
	return ThreadView{ID: value.ID, ProtocolVersion: value.ProtocolVersion,
		ProjectWorkspaceID: value.ProjectWorkspaceID, ProjectAssignmentVersion: value.ProjectAssignmentVersion,
		WorkspaceID: value.WorkspaceID, MissionID: value.MissionID, Title: value.Title, Pinned: value.Pinned,
		Status: string(value.Status), ActiveRunID: value.ActiveRunID,
		LastRunID: value.LastRunID, Version: value.Version, ComposerState: composerState,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		ArchivedAt: value.ArchivedAt, DeletedAt: value.DeletedAt}
}

func threadRunRecoveryView(value domain.ThreadRunRecovery) ThreadRunRecoveryView {
	errorCode := "durable_failure"
	stopReason := "durable_failure"
	detail := "上一次执行已在持久化边界停止。直接发送下一条消息即可继续；系统会自动创建新的执行上下文并应用待生效设置。"
	switch value.ErrorCode {
	case "failed_precondition":
		errorCode, stopReason = "failed_precondition", "failed_precondition"
		detail = "上一次执行因执行条件未满足而停止，具体原因请查看错误或执行记录。确认问题已处理后，可发送下一条消息继续。"
		switch value.FailureStage {
		case domain.ThreadFailureEmptyModelResponse:
			detail = "审批已保存，但模型没有返回有效答复，后续执行未完成。此前已完成的操作仍保留，请查看执行记录。"
		case domain.ThreadFailureToolRequestRejected:
			detail = "审批已保存，但模型的工具请求未通过校验，该批请求尚未执行。此前已完成的操作仍保留，请查看执行记录。"
		case domain.ThreadFailureInvalidModelResponse:
			detail = "审批已保存，但模型答复格式无效，后续执行未完成。此前已完成的操作仍保留，请查看执行记录。"
		}
	case "unavailable":
		errorCode, stopReason = "unavailable", "unavailable"
		detail = "上一次执行依赖的服务或运行环境暂不可用。确认恢复可用后，可发送下一条消息继续。"
	case "deadline_exceeded":
		errorCode, stopReason = "deadline_exceeded", "deadline_exceeded"
		detail = "上一次执行已经超时。直接发送下一条消息即可在新的执行上下文中继续。"
	case "resource_exhausted":
		errorCode, stopReason = "resource_exhausted", "resource_exhausted"
		detail = "上一次执行已触达资源上限。资源释放后，直接发送下一条消息即可继续。"
	}
	return ThreadRunRecoveryView{
		Version: domain.ThreadRunRecoveryProtocolVersion, RunID: value.RunID,
		HandoffOperationID: value.HandoffOperationID, ErrorCode: errorCode,
		StopReason: stopReason, Detail: detail, Quiescent: value.Quiescent,
		FailedAt: value.FailedAt,
	}
}

func threadMessageView(value domain.ThreadMessage) ThreadMessageView {
	return ThreadMessageView{ID: value.ID, ThreadID: value.ThreadID,
		RunID: value.RunID, SessionID: value.SessionID, Role: value.Role,
		Content: value.Content, ProvenanceVersion: value.ProvenanceVersion,
		SourceKind: value.SourceKind, SourceRef: value.SourceRef,
		ContentSHA256:         value.ContentSHA256,
		InstructionAuthorized: value.InstructionAuthorized, Status: value.Status,
		TokenEstimate: value.TokenEstimate,
		Compacted:     value.Compacted, CreatedAt: value.CreatedAt}
}

func threadTranscriptItemView(value threadtranscript.Item) ThreadTranscriptItemView {
	view := ThreadTranscriptItemView{
		Version: value.Version, ID: value.ID, CanonicalID: value.CanonicalID,
		RunID: value.RunID, RunOrdinal: value.RunOrdinal, Sequence: value.Sequence,
		Position: value.Position, ActivityType: string(value.Type), Stage: string(value.Stage),
		Kind: string(value.Kind), Source: string(value.Source), Title: value.Title,
		Detail: value.Detail, Status: value.Status, Verifiable: value.Verifiable,
		InstructionAuthorized: value.InstructionAuthorized, AttemptID: value.AttemptID,
		ModelAttempt: value.ModelAttempt, ToolRound: value.ToolRound,
		ToolName: value.ToolName, StreamResponseID: value.StreamResponseID,
		StreamItemID: value.StreamItemID, StreamCallID: value.StreamCallID,
		DurableCallID: value.DurableCallID, SourceRef: value.SourceRef,
		BoundaryReason: value.BoundaryReason, DeliveryMode: value.DeliveryMode,
		PromotedToMessageID: value.PromotedToMessageID, PromotedFromMessageID: value.PromotedFromMessageID,
		Provisional: value.Provisional,
		Durable:     value.Durable, CreatedAt: value.CreatedAt,
	}
	if application.SupportsThreadActivityDetail(value.ToolName) &&
		value.DurableCallID != "" {
		view.ActivityDetailRef = value.DurableCallID
		view.DetailAvailable = true
	}
	if value.WebEvidence != nil {
		presentation := value.WebEvidence
		state, stale := presentation.State, presentation.Stale
		if presentation.Citeable && !time.Now().UTC().Before(presentation.StaleAt.UTC()) {
			state, stale = "stale", true
		}
		view.WebEvidence = &ThreadWebEvidenceView{Version: presentation.Version,
			SourceID: presentation.SourceID, SnapshotID: presentation.SnapshotID,
			CitationID: presentation.CitationID, URL: presentation.URL,
			Title: presentation.Title, State: state,
			FetchedAt: presentation.FetchedAt, StaleAt: presentation.StaleAt,
			Digest: presentation.Digest, Partial: presentation.Partial,
			Stale: stale, Citeable: presentation.Citeable,
			Untrusted:             presentation.Untrusted,
			InstructionAuthorized: presentation.InstructionAuthorized}
	}
	return view
}

func threadActivityDetailView(value application.ThreadActivityDetail) ThreadActivityDetailView {
	tools := make([]ThreadActivityToolDetailView, len(value.Tools))
	for toolIndex, tool := range value.Tools {
		commands := make([]ThreadActivityCommandDetailView, len(tool.Commands))
		for commandIndex, command := range tool.Commands {
			artifacts := make([]ThreadActivityArtifactReferenceView, len(command.Artifacts))
			for artifactIndex, reference := range command.Artifacts {
				artifacts[artifactIndex] = ThreadActivityArtifactReferenceView{
					ArtifactRef: reference.ArtifactRef, Stream: reference.Stream,
					MIME: reference.MIME, SizeBytes: reference.SizeBytes,
					Truncated: reference.Truncated}
			}
			commands[commandIndex] = ThreadActivityCommandDetailView{
				Command: command.Command, WorkingDirectory: command.WorkingDirectory,
				ExecutionEnvironment: command.ExecutionEnvironment,
				Network:              command.Network, Status: command.Status,
				ExitCode:             command.ExitCode,
				DurationMilliseconds: command.DurationMilliseconds,
				StdoutPreview:        command.StdoutPreview,
				StderrPreview:        command.StderrPreview, Truncated: command.Truncated,
				Artifacts: artifacts,
			}
		}
		tools[toolIndex] = ThreadActivityToolDetailView{
			Name: tool.Name, Label: tool.Label, AgentID: tool.AgentID,
			AgentRole:  tool.AgentRole,
			AgentLabel: tool.AgentLabel,
			Status:     tool.Status, StartedAt: tool.StartedAt,
			CompletedAt:          tool.CompletedAt,
			DurationMilliseconds: tool.DurationMilliseconds,
			Detail:               threadActivityTypedDetailView(tool.Detail, commands),
		}
	}
	return ThreadActivityDetailView{Version: value.Version,
		ActivityRef: value.ActivityRef, RunID: value.RunID, Tools: tools}
}

func threadActivityTypedDetailView(value application.ThreadActivityTypedDetail,
	commands []ThreadActivityCommandDetailView,
) ThreadActivityTypedDetailView {
	view := ThreadActivityTypedDetailView{Kind: value.Kind}
	if value.Command != nil {
		view.Command = &ThreadActivityCommandGroupView{Commands: commands}
	}
	if value.WebSearch != nil {
		sources := make([]ThreadActivitySearchSourceView, len(value.WebSearch.Sources))
		for index, source := range value.WebSearch.Sources {
			sources[index] = ThreadActivitySearchSourceView{Rank: source.Rank,
				Title: source.Title, URL: source.URL, Provider: source.Provider,
				State: source.State, Citeable: source.Citeable}
		}
		view.WebSearch = &ThreadActivityWebSearchDetailView{
			Operation: value.WebSearch.Operation, Query: value.WebSearch.Query,
			Limit: value.WebSearch.Limit, Provider: value.WebSearch.Provider,
			SearchPolicy:    value.WebSearch.SearchPolicy,
			SelectionReason: value.WebSearch.SelectionReason,
			SourceCount:     value.WebSearch.SourceCount, Citeable: value.WebSearch.Citeable,
			Sources: sources, Boundary: threadActivityBoundaryView(value.WebSearch.Boundary)}
	}
	if value.WebFetch != nil {
		view.WebFetch = &ThreadActivityWebFetchDetailView{
			Operation: value.WebFetch.Operation, URL: value.WebFetch.URL,
			State: value.WebFetch.State, HTTPStatus: value.WebFetch.HTTPStatus,
			Robots: value.WebFetch.Robots, RobotsPolicy: value.WebFetch.RobotsPolicy,
			Redirects: value.WebFetch.Redirects, Partial: value.WebFetch.Partial,
			Citeable: value.WebFetch.Citeable,
			Boundary: threadActivityBoundaryView(value.WebFetch.Boundary)}
	}
	if value.FileRead != nil {
		view.FileRead = &ThreadActivityFileReadDetailView{
			Operation: value.FileRead.Operation, Path: value.FileRead.Path,
			Query: value.FileRead.Query, Pattern: value.FileRead.Pattern,
			StartLine: value.FileRead.StartLine, EndLine: value.FileRead.EndLine,
			Limit: value.FileRead.Limit, ResultCount: value.FileRead.ResultCount,
			Truncated: value.FileRead.Truncated, Summary: value.FileRead.Summary,
			Boundary: threadActivityBoundaryView(value.FileRead.Boundary)}
	}
	if value.FileEdit != nil {
		view.FileEdit = &ThreadActivityFileEditDetailView{
			Operation: value.FileEdit.Operation, Action: value.FileEdit.Action,
			Path: value.FileEdit.Path, DestinationPath: value.FileEdit.DestinationPath,
			EditID: value.FileEdit.EditID, DiffAvailable: value.FileEdit.DiffAvailable,
			ApplyStatus: value.FileEdit.ApplyStatus, Applied: value.FileEdit.Applied,
			FileWritten: value.FileEdit.FileWritten, Replayed: value.FileEdit.Replayed,
			Diff: ThreadActivityDiffSummaryView{AddedLines: value.FileEdit.Diff.AddedLines,
				RemovedLines: value.FileEdit.Diff.RemovedLines, Hunks: value.FileEdit.Diff.Hunks,
				Summary: value.FileEdit.Diff.Summary},
			Boundary: threadActivityBoundaryView(value.FileEdit.Boundary)}
	}
	if value.MCP != nil {
		view.MCP = &ThreadActivityMCPDetailView{Operation: value.MCP.Operation,
			Server: value.MCP.Server, Tool: value.MCP.Tool,
			Arguments:       threadActivityJSONFieldsView(value.MCP.Arguments),
			Result:          threadActivityJSONSummaryView(value.MCP.Result),
			Contents:        threadActivityMCPContentsView(value.MCP.Contents),
			OmittedContents: value.MCP.OmittedContents, ContentTruncated: value.MCP.ContentTruncated,
			Boundary: threadActivityBoundaryView(value.MCP.Boundary)}
	}
	if value.Verification != nil {
		view.Verification = &ThreadActivityVerificationDetailView{
			Operation: value.Verification.Operation, Tool: value.Verification.Tool,
			Path: value.Verification.Path, Query: value.Verification.Query,
			Position: value.Verification.Position, Direction: value.Verification.Direction,
			Limit: value.Verification.Limit, ResultCount: value.Verification.ResultCount,
			Truncated: value.Verification.Truncated, Summary: value.Verification.Summary,
			Boundary: threadActivityBoundaryView(value.Verification.Boundary)}
	}
	if value.Browser != nil {
		view.Browser = &ThreadActivityBrowserDetailView{
			Operation: value.Browser.Operation, Action: value.Browser.Action,
			URL: value.Browser.URL, Selector: value.Browser.Selector,
			InputLength: value.Browser.InputLength, ArtifactBytes: value.Browser.ArtifactBytes,
			Summary:  value.Browser.Summary,
			Boundary: threadActivityBoundaryView(value.Browser.Boundary)}
	}
	return view
}

func threadActivityMCPContentsView(contents []application.ThreadActivityMCPContent) []ThreadActivityMCPContentView {
	result := make([]ThreadActivityMCPContentView, 0, len(contents))
	for _, item := range contents {
		result = append(result, ThreadActivityMCPContentView{Kind: item.Kind, Text: item.Text, URI: item.URI,
			Name: item.Name, MIMEType: item.MIMEType, DataBase64: item.DataBase64, ByteSize: item.ByteSize,
			Width: item.Width, Height: item.Height, Role: item.Role})
	}
	return result
}

func threadActivityBoundaryView(value application.ThreadActivityBoundary) ThreadActivityBoundaryView {
	return ThreadActivityBoundaryView{Authorization: value.Authorization,
		ErrorCode: value.ErrorCode, FailureReason: value.FailureReason,
		Truncated: value.Truncated, Untrusted: value.Untrusted}
}

func threadActivityJSONFieldsView(values []application.ThreadActivityJSONFieldSummary) []ThreadActivityJSONFieldSummaryView {
	result := make([]ThreadActivityJSONFieldSummaryView, len(values))
	for index, value := range values {
		result[index] = ThreadActivityJSONFieldSummaryView{Name: value.Name,
			Type: value.Type, Summary: value.Summary}
	}
	return result
}

func threadActivityJSONSummaryView(value application.ThreadActivityJSONSummary) ThreadActivityJSONSummaryView {
	return ThreadActivityJSONSummaryView{Type: value.Type, Count: value.Count,
		Summary: value.Summary, Fields: threadActivityJSONFieldsView(value.Fields)}
}

func threadActivityArtifactView(value application.ThreadActivityArtifact) ThreadActivityArtifactView {
	return ThreadActivityArtifactView{Version: value.Version,
		ActivityRef: value.ActivityRef, ArtifactRef: value.ArtifactRef,
		Stream: value.Stream, MIME: value.MIME, Content: value.Content,
		SHA256: value.SHA256, SizeBytes: value.SizeBytes,
		Redacted: value.Redacted, Truncated: value.Truncated,
		Untrusted:             value.Untrusted,
		InstructionAuthorized: value.InstructionAuthorized}
}

func threadActivitySummaryView(value application.ThreadActivitySummary) *ThreadActivitySummaryView {
	if value.ActivityRef == "" || value.Command == "" || value.CommandCount < 1 {
		return nil
	}
	return &ThreadActivitySummaryView{Version: value.Version,
		ActivityRef: value.ActivityRef, Command: value.Command, Status: value.Status,
		ExitCode: value.ExitCode, DurationMilliseconds: value.DurationMilliseconds,
		CommandCount: value.CommandCount}
}

func messageView(value session.Message) MessageView {
	items := make([]llm.OutputItem, 0, 1)
	if value.Role == "assistant" {
		responseID := llm.StableStreamID("response", "session", value.SessionID,
			strconv.FormatInt(value.ID, 10))
		items = append(items, llm.OutputItem{ResponseID: responseID,
			ID: llm.StableStreamID("item", responseID, "1"), Type: llm.StreamItemMessage,
			Status: llm.StreamItemCompleted, Durable: true})
	}
	return MessageView{ID: value.ID, SessionID: value.SessionID, Role: value.Role, Content: value.Content,
		ProvenanceVersion: value.Provenance.Version, SourceKind: value.Provenance.SourceKind,
		SourceRef: value.Provenance.SourceRef, ContentSHA256: value.Provenance.ContentSHA256,
		InstructionAuthorized: value.Provenance.InstructionAuthorized,
		TokenEstimate:         value.TokenEstimate, Compacted: value.Compacted, CreatedAt: value.CreatedAt,
		OutputItems: items}
}

func eventView(value events.Event) EventView {
	return EventView{EventID: value.EventID, Version: value.Version, RunID: value.RunID,
		MissionID: value.MissionID, Sequence: value.Sequence, Type: value.Type, Source: value.Source,
		SubjectID: value.SubjectID, Payload: publicRunEventJSON(value.PayloadJSON), CreatedAt: value.CreatedAt}
}

func runActivityView(value runactivity.Projection) RunActivityView {
	items := make([]RunActivityItemView, len(value.Items))
	for index, item := range value.Items {
		items[index] = RunActivityItemView{
			ID: item.ID, Sequence: item.Sequence, Kind: string(item.Kind),
			Source: string(item.Source), Title: item.Title, Detail: item.Detail,
			Status: item.Status, Verifiable: item.Verifiable,
			InstructionAuthorized: item.InstructionAuthorized,
			AttemptID:             item.AttemptID, ModelAttempt: item.ModelAttempt, ToolRound: item.ToolRound,
			CreatedAt: item.CreatedAt,
		}
	}
	return RunActivityView{
		Version: value.Version, RunID: value.RunID,
		ThroughSequence: value.ThroughSequence, Truncated: value.Truncated,
		PrivateReasoningIncluded: value.PrivateReasoningIncluded, Items: items,
	}
}

func workItemView(value domain.WorkItem) WorkItemView {
	return WorkItemView{ID: value.ID, RunID: value.RunID, Title: value.Title, Description: value.Description,
		Status: string(value.Status), Priority: string(value.Priority), Owner: value.Owner,
		OwnerAgentID:       value.OwnerAgentID,
		AcceptanceCriteria: append([]string{}, value.AcceptanceCriteria...),
		Dependencies:       append([]string{}, value.Dependencies...), BlockedReason: value.BlockedReason,
		Version: value.Version, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		CompletedAt: value.CompletedAt}
}

func noteView(value domain.Note) NoteView {
	return NoteView{ID: value.ID, RunID: value.RunID, Title: value.Title, Content: value.Content,
		Category: string(value.Category), Visibility: string(value.Visibility), Owner: value.Owner,
		OwnerAgentID: value.OwnerAgentID,
		Tags:         append([]string{}, value.Tags...), SourceRefs: append([]string{}, value.SourceRefs...),
		EvidenceIDs: append([]string{}, value.EvidenceIDs...), Status: string(value.Status),
		Pinned: value.Pinned, Version: value.Version, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, ArchivedAt: value.ArchivedAt}
}

func artifactView(value artifact.Descriptor) ArtifactView {
	return ArtifactView{ID: value.ID, RunID: value.RunID, SessionID: value.SessionID,
		WorkspaceID: value.WorkspaceID, SourceID: value.SourceID, ToolName: value.ToolName,
		Stream: string(value.Stream), Kind: value.Kind, MIME: value.MIME, Encoding: value.Encoding,
		SHA256: value.SHA256, SizeBytes: value.SizeBytes, Redacted: value.Redacted, CreatedAt: value.CreatedAt}
}

func supervisorToolRoundView(value domain.SupervisorToolRound, threadID string,
	details map[string]*ThreadActivityToolDetailView,
) SupervisorToolRoundView {
	calls := make([]SupervisorToolCallView, len(value.Calls))
	for index, call := range value.Calls {
		detail := details[call.CallID]
		calls[index] = SupervisorToolCallView{
			Position: call.Position, ModelAttempt: call.ModelAttempt, CallID: call.CallID,
			StreamResponseID: call.StreamResponseID, StreamItemID: call.StreamItemID,
			StreamCallID: call.StreamCallID,
			ToolName:     call.ToolName, Status: string(call.Status),
			ErrorCode:       publicInspectorErrorCode(call.ErrorCode),
			DetailAvailable: detail != nil, Detail: detail,
			CreatedAt: call.CreatedAt, CompletedAt: call.CompletedAt,
		}
	}
	return SupervisorToolRoundView{ThreadID: threadID, RunID: value.RunID,
		Turn: value.Turn, AttemptID: value.AttemptID,
		Round: value.Round, ModelAttempt: value.ModelAttempt, Calls: calls,
		CreatedAt: value.CreatedAt, CompletedAt: value.CompletedAt}
}
