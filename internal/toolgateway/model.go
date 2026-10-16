package toolgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
)

const (
	MaxCallArguments          = 32
	MaxArgumentNameRunes      = 64
	MaxArgumentValueBytes     = 256 * 1024
	MaxCommandBytes           = 16 * 1024
	MaxResultStdoutBytes      = 128 * 1024
	MaxResultStderrBytes      = 32 * 1024
	MaxProposalPreviewBytes   = 64 * 1024
	MaxDecisionReasonRunes    = 2048
	MaxReviewReasonRunes      = 2048
	MaxToolIdentityRunes      = 256
	MaxWorkspaceRootPathRunes = 4096
	MaxMIMEBytes              = 256
	MaxExecutionBackendRunes  = 128
)

type ToolName string

const (
	ReadFileTool                    ToolName = "read_file"
	ListWorkspaceTool               ToolName = "list_workspace"
	ShellTool                       ToolName = "shell"
	ReplaceFileTool                 ToolName = "replace_file"
	ScriptProcessTool               ToolName = "script_process"
	WorkItemCreateTool              ToolName = "work_item_create"
	NoteCreateTool                  ToolName = "note_create"
	PlanDeliveryProposeTool         ToolName = "plan_delivery_propose"
	SpecialistDelegationProposeTool ToolName = "specialist_delegation_propose"
	ChildTaskProposeTool            ToolName = "child_task_propose"
	ControlledCommandProposeTool    ToolName = "controlled_command_propose"
	HostCommandProposeTool          ToolName = "host_command_propose"
	DebugTerminalTool               ToolName = "debug_terminal"
	CommandRuntimeTool              ToolName = "command_runtime"
	MCPToolCallTool                 ToolName = "mcp_tool_call"
	MCPToolSearchTool               ToolName = "mcp_tool_search"
	WebSearchTool                   ToolName = "web_search"
	SourceSearchTool                ToolName = "source_search"
	WebFetchTool                    ToolName = "web_fetch"
	WebCitationTool                 ToolName = "web_citation"
	BrowserStatusTool               ToolName = "browser_status"
	BrowserNavigateTool             ToolName = "browser_navigate"
	BrowserSnapshotTool             ToolName = "browser_snapshot"
	BrowserClickTool                ToolName = "browser_click"
	BrowserTypeTool                 ToolName = "browser_type"
	BrowserScreenshotTool           ToolName = "browser_screenshot"
	BrowserScrollTool               ToolName = "browser_scroll"
	BrowserKeyTool                  ToolName = "browser_key"
	OneShotCommandProposeTool       ToolName = "one_shot_command_propose"
	DockerSandboxRunProposeTool     ToolName = "sandbox_docker_run_propose"
	SkillCandidateProposeTool       ToolName = "skill_candidate_propose"
)

type ActionClass string

const (
	ClassWorkspaceRead  ActionClass = "workspace_read"
	ClassWorkspaceWrite ActionClass = "workspace_write"
	ClassShell          ActionClass = "shell"
	ClassProcess        ActionClass = "process"
	ClassRunMemory      ActionClass = "run_memory"
	ClassAgentProposal  ActionClass = "agent_proposal"
	ClassNetworkRead    ActionClass = "network_read"
)

func (c ActionClass) Valid() bool {
	switch c {
	case ClassWorkspaceRead, ClassWorkspaceWrite, ClassShell, ClassProcess, ClassRunMemory,
		ClassAgentProposal, ClassNetworkRead:
		return true
	default:
		return false
	}
}

type ApprovalMode string

const (
	ApprovalAutomatic ApprovalMode = "automatic"
	ApprovalPerCall   ApprovalMode = "per_call"
	ApprovalSession   ApprovalMode = "session"
	ApprovalNever     ApprovalMode = "never"
)

func (m ApprovalMode) Valid() bool {
	switch m {
	case ApprovalAutomatic, ApprovalPerCall, ApprovalSession, ApprovalNever:
		return true
	default:
		return false
	}
}

type Status string

const (
	StatusProposed  Status = "proposed"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

func (s Status) Valid() bool {
	switch s {
	case StatusProposed, StatusApproved, StatusDenied, StatusRunning, StatusCompleted, StatusFailed:
		return true
	default:
		return false
	}
}

type ToolCall struct {
	Name                        ToolName                          `json:"name"`
	Arguments                   map[string]string                 `json:"arguments"`
	Payload                     json.RawMessage                   `json:"payload,omitempty"`
	InvocationID                string                            `json:"invocation_id,omitempty"`
	OperationKey                string                            `json:"-"`
	RunID                       string                            `json:"run_id,omitempty"`
	AgentID                     string                            `json:"agent_id,omitempty"`
	AgentAttemptID              string                            `json:"-"`
	SpecialistToolCallID        string                            `json:"-"`
	SessionID                   string                            `json:"session_id,omitempty"`
	WorkspaceID                 string                            `json:"workspace_id,omitempty"`
	MissionID                   string                            `json:"mission_id,omitempty"`
	RootFingerprint             string                            `json:"root_fingerprint,omitempty"`
	Surface                     domain.ExecutionSurface           `json:"surface,omitempty"`
	Phase                       domain.ExecutionPhase             `json:"phase,omitempty"`
	Role                        domain.AgentRole                  `json:"role,omitempty"`
	Profile                     domain.Profile                    `json:"profile,omitempty"`
	PermissionMode              domain.RunExecutionPermissionMode `json:"permission_mode,omitempty"`
	PermissionSnapshotID        string                            `json:"permission_snapshot_id,omitempty"`
	ModeRevision                int64                             `json:"mode_revision,omitempty"`
	PermissionRevision          int64                             `json:"permission_revision,omitempty"`
	PermissionGeneration        uint64                            `json:"permission_generation,omitempty"`
	PermissionRuntimeEpoch      string                            `json:"-"`
	RunAuthorizationFence       uint64                            `json:"run_authorization_fence,omitempty"`
	CapabilityGeneration        string                            `json:"capability_generation,omitempty"`
	ProviderFingerprint         string                            `json:"-"`
	ConnectorFingerprint        string                            `json:"-"`
	SupervisorTurn              int                               `json:"-"`
	SupervisorToolCallID        string                            `json:"-"`
	LeaseID                     string                            `json:"-"`
	LeaseGeneration             int64                             `json:"-"`
	WorkspaceRoot               string                            `json:"-"`
	RequestedBy                 string                            `json:"requested_by,omitempty"`
	CommandRuntimeAdapter       commandruntimeadapter.Identity    `json:"-"`
	BrowserActionSessionID      string                            `json:"-"`
	BrowserPermissionSnapshotID string                            `json:"-"`
	BrowserPermissionRevision   int64                             `json:"-"`
	AgentBrowserAuthority       json.RawMessage                   `json:"-"`
}

func NormalizeToolCall(call ToolCall) (ToolCall, error) {
	call.Name = ToolName(strings.TrimSpace(string(call.Name)))
	call.Payload = append(json.RawMessage(nil), bytes.TrimSpace(call.Payload)...)
	call.InvocationID = strings.TrimSpace(call.InvocationID)
	call.OperationKey = strings.TrimSpace(call.OperationKey)
	call.RunID = strings.TrimSpace(call.RunID)
	call.AgentID = strings.TrimSpace(call.AgentID)
	call.AgentAttemptID = strings.TrimSpace(call.AgentAttemptID)
	call.SpecialistToolCallID = strings.TrimSpace(call.SpecialistToolCallID)
	call.SessionID = strings.TrimSpace(call.SessionID)
	call.WorkspaceID = strings.TrimSpace(call.WorkspaceID)
	call.MissionID = strings.TrimSpace(call.MissionID)
	call.RootFingerprint = strings.TrimSpace(call.RootFingerprint)
	call.Surface = domain.ExecutionSurface(strings.TrimSpace(string(call.Surface)))
	call.Phase = domain.ExecutionPhase(strings.TrimSpace(string(call.Phase)))
	call.Role = domain.AgentRole(strings.TrimSpace(string(call.Role)))
	call.Profile = domain.Profile(strings.TrimSpace(string(call.Profile)))
	call.PermissionMode = domain.RunExecutionPermissionMode(strings.TrimSpace(string(call.PermissionMode)))
	call.PermissionSnapshotID = strings.TrimSpace(call.PermissionSnapshotID)
	call.PermissionRuntimeEpoch = strings.TrimSpace(call.PermissionRuntimeEpoch)
	call.CapabilityGeneration = strings.TrimSpace(call.CapabilityGeneration)
	call.ProviderFingerprint = strings.TrimSpace(call.ProviderFingerprint)
	call.ConnectorFingerprint = strings.TrimSpace(call.ConnectorFingerprint)
	call.SupervisorToolCallID = strings.TrimSpace(call.SupervisorToolCallID)
	call.LeaseID = strings.TrimSpace(call.LeaseID)
	call.WorkspaceRoot = strings.TrimSpace(call.WorkspaceRoot)
	call.RequestedBy = strings.TrimSpace(redact.String(call.RequestedBy))
	call.BrowserActionSessionID = strings.TrimSpace(call.BrowserActionSessionID)
	call.BrowserPermissionSnapshotID = strings.TrimSpace(call.BrowserPermissionSnapshotID)
	if !call.Name.Valid() {
		return ToolCall{}, fmt.Errorf("unsupported tool %q", call.Name)
	}
	for label, value := range map[string]string{
		"invocation id": call.InvocationID, "operation key": call.OperationKey,
		"run id": call.RunID, "agent id": call.AgentID,
		"agent attempt id": call.AgentAttemptID, "session id": call.SessionID,
		"specialist tool call id": call.SpecialistToolCallID,
		"workspace id":            call.WorkspaceID, "lease id": call.LeaseID, "requester": call.RequestedBy,
		"mission id": call.MissionID, "root fingerprint": call.RootFingerprint,
		"permission snapshot id":         call.PermissionSnapshotID,
		"permission runtime epoch":       call.PermissionRuntimeEpoch,
		"capability generation":          call.CapabilityGeneration,
		"Provider fingerprint":           call.ProviderFingerprint,
		"source connector fingerprint":   call.ConnectorFingerprint,
		"supervisor tool call id":        call.SupervisorToolCallID,
		"browser action session id":      call.BrowserActionSessionID,
		"browser permission snapshot id": call.BrowserPermissionSnapshotID,
	} {
		if !utf8.ValidString(value) {
			return ToolCall{}, fmt.Errorf("tool %s must be valid UTF-8", label)
		}
		if len([]rune(value)) > MaxToolIdentityRunes {
			return ToolCall{}, fmt.Errorf("tool %s exceeds %d characters", label, MaxToolIdentityRunes)
		}
	}
	if (call.LeaseID == "") != (call.LeaseGeneration == 0) || call.LeaseGeneration < 0 {
		return ToolCall{}, errors.New("tool execution lease identity and generation are inconsistent")
	}
	if call.ModeRevision < 0 || call.PermissionRevision < 0 {
		return ToolCall{}, errors.New("tool capability revisions cannot be negative")
	}
	if call.BrowserPermissionRevision < 0 ||
		(call.BrowserActionSessionID == "") != (call.BrowserPermissionSnapshotID == "") ||
		(call.BrowserActionSessionID == "") != (call.BrowserPermissionRevision == 0) {
		return ToolCall{}, errors.New("browser action runtime binding is inconsistent")
	}
	if call.SupervisorTurn < 0 ||
		(call.SupervisorTurn == 0) != (call.SupervisorToolCallID == "") {
		return ToolCall{}, errors.New("Supervisor turn and tool call identity are inconsistent")
	}
	if isAgentCodeTool(call.Name) || IsCodeIntelTool(call.Name) || IsWebEvidenceTool(call.Name) ||
		IsBrowserActionTool(call.Name) ||
		IsCommandExecutionTool(call.Name) {
		if !call.Surface.Valid() || !call.Phase.Valid() || !domain.ValidAgentRole(call.Role) ||
			!call.PermissionMode.Valid() || call.ModeRevision <= 0 ||
			call.PermissionRevision <= 0 || !validAgentCodeDigest(call.CapabilityGeneration, false) {
			return ToolCall{}, errors.New("workspace-affecting tool capability binding is invalid")
		}
		if _, err := domain.ParseProfile(string(call.Profile)); err != nil {
			return ToolCall{}, errors.New("workspace-affecting tool profile binding is invalid")
		}
	}
	if call.ProviderFingerprint != "" &&
		(!IsWebEvidenceTool(call.Name) || !validAgentCodeDigest(call.ProviderFingerprint, false)) {
		return ToolCall{}, errors.New("tool Web search Provider fingerprint is invalid")
	}
	if call.ConnectorFingerprint != "" &&
		(!IsWebEvidenceTool(call.Name) || !validAgentCodeDigest(call.ConnectorFingerprint, false)) {
		return ToolCall{}, errors.New("tool source connector fingerprint is invalid")
	}
	if call.Name == WebSearchTool && call.OperationKey != "" &&
		!validAgentCodeDigest(call.ProviderFingerprint, false) {
		return ToolCall{}, errors.New(
			"Web search requires the exact advertised Provider fingerprint")
	}
	if call.Name == SourceSearchTool && call.OperationKey != "" &&
		!validAgentCodeDigest(call.ConnectorFingerprint, false) {
		return ToolCall{}, errors.New(
			"source search requires the exact advertised connector fingerprint")
	}
	if strings.ContainsRune(call.OperationKey, 0) {
		return ToolCall{}, errors.New("tool operation key cannot contain NUL")
	}
	if len(call.Payload) > MaxArgumentValueBytes {
		return ToolCall{}, fmt.Errorf("tool payload exceeds %d bytes", MaxArgumentValueBytes)
	}
	if len(call.Payload) > 0 && (!utf8.Valid(call.Payload) || !json.Valid(call.Payload)) {
		return ToolCall{}, errors.New("tool payload must be valid UTF-8 JSON")
	}
	if !utf8.ValidString(call.WorkspaceRoot) {
		return ToolCall{}, errors.New("tool workspace root must be valid UTF-8")
	}
	if len([]rune(call.WorkspaceRoot)) > MaxWorkspaceRootPathRunes {
		return ToolCall{}, fmt.Errorf("tool workspace root exceeds %d characters", MaxWorkspaceRootPathRunes)
	}
	if len(call.Arguments) > MaxCallArguments {
		return ToolCall{}, fmt.Errorf("tool argument list exceeds %d items", MaxCallArguments)
	}
	arguments := make(map[string]string, len(call.Arguments))
	for rawName, value := range call.Arguments {
		name := strings.TrimSpace(rawName)
		if !validArgumentName(name) {
			return ToolCall{}, fmt.Errorf("invalid tool argument name %q", rawName)
		}
		if _, exists := arguments[name]; exists {
			return ToolCall{}, fmt.Errorf("duplicate tool argument %q", name)
		}
		if !utf8.ValidString(value) {
			return ToolCall{}, fmt.Errorf("tool argument %q must be valid UTF-8", name)
		}
		if len([]byte(value)) > MaxArgumentValueBytes {
			return ToolCall{}, fmt.Errorf("tool argument %q exceeds %d bytes", name, MaxArgumentValueBytes)
		}
		arguments[name] = value
	}
	call.Arguments = arguments
	return call, nil
}

func (c ToolCall) Validate() error {
	normalized, err := NormalizeToolCall(c)
	if err != nil {
		return err
	}
	if normalized.Name != c.Name || !bytes.Equal(normalized.Payload, c.Payload) || normalized.InvocationID != c.InvocationID ||
		normalized.OperationKey != c.OperationKey || normalized.RunID != c.RunID ||
		normalized.AgentID != c.AgentID || normalized.AgentAttemptID != c.AgentAttemptID ||
		normalized.SpecialistToolCallID != c.SpecialistToolCallID ||
		normalized.SessionID != c.SessionID ||
		normalized.WorkspaceID != c.WorkspaceID || normalized.LeaseID != c.LeaseID ||
		normalized.MissionID != c.MissionID || normalized.RootFingerprint != c.RootFingerprint ||
		normalized.Surface != c.Surface || normalized.Phase != c.Phase || normalized.Role != c.Role ||
		normalized.Profile != c.Profile || normalized.PermissionMode != c.PermissionMode ||
		normalized.ModeRevision != c.ModeRevision || normalized.PermissionRevision != c.PermissionRevision ||
		normalized.CapabilityGeneration != c.CapabilityGeneration ||
		normalized.ProviderFingerprint != c.ProviderFingerprint ||
		normalized.ConnectorFingerprint != c.ConnectorFingerprint ||
		normalized.SupervisorTurn != c.SupervisorTurn ||
		normalized.SupervisorToolCallID != c.SupervisorToolCallID ||
		normalized.PermissionRuntimeEpoch != c.PermissionRuntimeEpoch ||
		normalized.LeaseGeneration != c.LeaseGeneration || normalized.WorkspaceRoot != c.WorkspaceRoot ||
		normalized.RequestedBy != c.RequestedBy ||
		normalized.BrowserActionSessionID != c.BrowserActionSessionID ||
		normalized.BrowserPermissionSnapshotID != c.BrowserPermissionSnapshotID ||
		normalized.BrowserPermissionRevision != c.BrowserPermissionRevision ||
		!maps.Equal(normalized.Arguments, c.Arguments) {
		return errors.New("tool call must be normalized")
	}
	return nil
}

type Decision struct {
	Allowed  bool         `json:"allowed"`
	Approval ApprovalMode `json:"approval"`
	Risk     string       `json:"risk"`
	Reason   string       `json:"reason"`
}

func NormalizeDecision(decision Decision) (Decision, error) {
	decision.Risk = strings.ToLower(strings.TrimSpace(decision.Risk))
	decision.Reason = strings.TrimSpace(redact.String(decision.Reason))
	if !decision.Approval.Valid() {
		return Decision{}, fmt.Errorf("invalid approval mode %q", decision.Approval)
	}
	if decision.Allowed && decision.Approval == ApprovalNever {
		return Decision{}, errors.New("allowed decision cannot use never approval")
	}
	if !decision.Allowed && decision.Approval != ApprovalNever {
		return Decision{}, errors.New("denied decision must use never approval")
	}
	if decision.Risk == "" {
		return Decision{}, errors.New("tool decision risk is required")
	}
	switch decision.Risk {
	case "low", "medium", "high", "critical":
	default:
		return Decision{}, fmt.Errorf("invalid tool decision risk %q", decision.Risk)
	}
	if decision.Reason == "" {
		return Decision{}, errors.New("tool decision reason is required")
	}
	if len([]rune(decision.Reason)) > MaxDecisionReasonRunes {
		return Decision{}, fmt.Errorf("tool decision reason exceeds %d characters", MaxDecisionReasonRunes)
	}
	return decision, nil
}

func (d Decision) Validate() error {
	normalized, err := NormalizeDecision(d)
	if err != nil {
		return err
	}
	if normalized != d {
		return errors.New("tool decision must be normalized")
	}
	return nil
}

type Proposal struct {
	ID        string      `json:"id"`
	Tool      ToolName    `json:"tool"`
	Class     ActionClass `json:"class"`
	Status    Status      `json:"status"`
	Preview   string      `json:"preview,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

func (p Proposal) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.ID) != p.ID || !utf8.ValidString(p.ID) || len([]rune(p.ID)) > MaxToolIdentityRunes {
		return errors.New("tool proposal id is required and bounded")
	}
	if !p.Tool.Valid() || !p.Class.Valid() || !p.Status.Valid() {
		return errors.New("tool proposal tool, class, and status are required")
	}
	class, ok := ClassForTool(p.Tool)
	if !ok || class != p.Class {
		return errors.New("tool proposal class does not match its tool")
	}
	if p.Status != StatusProposed && p.Status != StatusApproved && p.Status != StatusDenied &&
		p.Status != StatusCompleted && p.Status != StatusFailed {
		return fmt.Errorf("invalid tool proposal status %q", p.Status)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return errors.New("tool proposal timestamps are invalid")
	}
	if !utf8.ValidString(p.Preview) || len([]byte(p.Preview)) > MaxProposalPreviewBytes {
		return errors.New("tool proposal preview must be bounded UTF-8")
	}
	return nil
}

type Execution struct {
	Backend     string     `json:"backend"`
	Status      Status     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (e Execution) Validate() error {
	if strings.TrimSpace(e.Backend) == "" || strings.TrimSpace(e.Backend) != e.Backend ||
		!utf8.ValidString(e.Backend) || len([]rune(e.Backend)) > MaxExecutionBackendRunes {
		return errors.New("tool execution backend must be normalized and bounded UTF-8")
	}
	if e.Status != StatusRunning && e.Status != StatusCompleted && e.Status != StatusFailed {
		return fmt.Errorf("invalid tool execution status %q", e.Status)
	}
	if e.StartedAt.IsZero() {
		return errors.New("tool execution started_at is required")
	}
	if e.Status == StatusRunning && e.CompletedAt != nil {
		return errors.New("running tool execution cannot be completed")
	}
	if e.Status != StatusRunning && (e.CompletedAt == nil || e.CompletedAt.Before(e.StartedAt)) {
		return errors.New("terminal tool execution requires a valid completion time")
	}
	return nil
}

type Result struct {
	Status      Status            `json:"status"`
	Stdout      string            `json:"stdout,omitempty"`
	Stderr      string            `json:"stderr,omitempty"`
	ExitCode    int               `json:"exit_code"`
	MIME        string            `json:"mime"`
	Truncated   bool              `json:"truncated"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CompletedAt time.Time         `json:"completed_at"`
}

func (r Result) Validate() error {
	if r.Status != StatusCompleted && r.Status != StatusDenied && r.Status != StatusFailed {
		return fmt.Errorf("invalid tool result status %q", r.Status)
	}
	if !utf8.ValidString(r.Stdout) || !utf8.ValidString(r.Stderr) ||
		len([]byte(r.Stdout)) > MaxResultStdoutBytes || len([]byte(r.Stderr)) > MaxResultStderrBytes {
		return errors.New("tool result output must be bounded UTF-8")
	}
	if strings.TrimSpace(r.MIME) == "" || strings.TrimSpace(r.MIME) != r.MIME || len([]byte(r.MIME)) > MaxMIMEBytes || r.CompletedAt.IsZero() {
		return errors.New("tool result MIME and completion time are required")
	}
	if _, _, err := mime.ParseMediaType(r.MIME); err != nil {
		return fmt.Errorf("invalid tool result MIME %q: %w", r.MIME, err)
	}
	if len(r.Metadata) > MaxCallArguments {
		return fmt.Errorf("tool result metadata exceeds %d items", MaxCallArguments)
	}
	for key, value := range r.Metadata {
		if !validArgumentName(key) || !utf8.ValidString(value) || len([]byte(value)) > MaxArgumentValueBytes {
			return fmt.Errorf("invalid tool result metadata %q", key)
		}
	}
	return nil
}

type Outcome struct {
	Call      ToolCall   `json:"call"`
	Decision  Decision   `json:"decision"`
	Proposal  *Proposal  `json:"proposal,omitempty"`
	Execution *Execution `json:"execution,omitempty"`
	Result    *Result    `json:"result,omitempty"`
}

func (o Outcome) Validate() error {
	if err := o.Call.Validate(); err != nil {
		return err
	}
	if err := o.Decision.Validate(); err != nil {
		return err
	}
	if o.Proposal != nil {
		if err := o.Proposal.Validate(); err != nil {
			return err
		}
		if o.Proposal.Tool != o.Call.Name {
			return errors.New("tool proposal does not match the call")
		}
	}
	if o.Execution != nil {
		if err := o.Execution.Validate(); err != nil {
			return err
		}
	}
	if o.Result != nil {
		if err := o.Result.Validate(); err != nil {
			return err
		}
	}
	if o.Proposal == nil && o.Execution == nil && o.Result == nil {
		return errors.New("tool outcome requires a proposal, execution, or result")
	}
	if !o.Decision.Allowed {
		if o.Execution != nil || o.Result == nil || o.Result.Status != StatusDenied {
			return errors.New("denied tool outcome requires a denied result and no execution")
		}
		if o.Proposal != nil && o.Proposal.Status != StatusDenied {
			return errors.New("denied tool outcome cannot retain an active proposal")
		}
	}
	if o.Execution != nil {
		if !o.Decision.Allowed {
			return errors.New("denied tool outcome cannot include execution")
		}
		if o.Execution.Status == StatusRunning {
			if o.Result != nil {
				return errors.New("running tool outcome cannot include a result")
			}
		} else if o.Result == nil || o.Result.Status != o.Execution.Status {
			return errors.New("terminal tool execution and result statuses must match")
		}
	}
	if o.Result != nil && o.Result.Status != StatusDenied && o.Execution == nil {
		return errors.New("terminal tool result requires execution metadata")
	}
	if o.Result != nil && o.Result.Status == StatusDenied && o.Decision.Allowed {
		return errors.New("denied tool result requires a denied decision")
	}
	if o.Proposal != nil {
		switch o.Proposal.Status {
		case StatusProposed:
			if o.Execution != nil || o.Result != nil {
				return errors.New("proposed tool outcome cannot include execution or result")
			}
		case StatusApproved:
			if o.Result != nil {
				return errors.New("approved tool outcome cannot include a terminal result")
			}
		case StatusDenied:
			if o.Decision.Allowed || o.Result == nil || o.Result.Status != StatusDenied {
				return errors.New("denied proposal requires a denied decision and result")
			}
		case StatusCompleted, StatusFailed:
			if o.Execution == nil || o.Result == nil || o.Execution.Status != o.Proposal.Status || o.Result.Status != o.Proposal.Status {
				return errors.New("terminal proposal, execution, and result statuses must match")
			}
		}
	}
	return nil
}

type ReviewAction string

const (
	ReviewApprove ReviewAction = "approve"
	ReviewDeny    ReviewAction = "deny"
)

type ReviewRequest struct {
	Action         ReviewAction `json:"action"`
	Tool           ToolName     `json:"tool"`
	ProposalID     string       `json:"proposal_id"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	ReviewedBy     string       `json:"reviewed_by,omitempty"`
	WorkspaceRoot  string       `json:"-"`
	Reason         string       `json:"reason,omitempty"`
}

func NormalizeReviewRequest(request ReviewRequest) (ReviewRequest, error) {
	request.Action = ReviewAction(strings.TrimSpace(string(request.Action)))
	request.Tool = ToolName(strings.TrimSpace(string(request.Tool)))
	request.ProposalID = strings.TrimSpace(request.ProposalID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.ReviewedBy = strings.TrimSpace(redact.String(request.ReviewedBy))
	request.WorkspaceRoot = strings.TrimSpace(request.WorkspaceRoot)
	request.Reason = strings.TrimSpace(redact.String(request.Reason))
	if request.Action != ReviewApprove && request.Action != ReviewDeny {
		return ReviewRequest{}, fmt.Errorf("invalid tool review action %q", request.Action)
	}
	if request.Tool != ShellTool && request.Tool != ReplaceFileTool && request.Tool != ScriptProcessTool {
		return ReviewRequest{}, fmt.Errorf("tool %q does not support proposal review", request.Tool)
	}
	if request.ProposalID == "" || len([]rune(request.ProposalID)) > MaxToolIdentityRunes {
		return ReviewRequest{}, errors.New("tool review proposal id is required and bounded")
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = fmt.Sprintf("review:%s:%s:%s", request.Tool, request.ProposalID, request.Action)
	}
	if request.ReviewedBy == "" {
		request.ReviewedBy = "operator"
	}
	for label, value := range map[string]string{"idempotency key": request.IdempotencyKey, "reviewer": request.ReviewedBy} {
		if !utf8.ValidString(value) || len([]rune(value)) > MaxToolIdentityRunes {
			return ReviewRequest{}, fmt.Errorf("tool review %s must be bounded UTF-8", label)
		}
	}
	if !utf8.ValidString(request.WorkspaceRoot) || len([]rune(request.WorkspaceRoot)) > MaxWorkspaceRootPathRunes {
		return ReviewRequest{}, errors.New("tool review workspace root must be bounded UTF-8")
	}
	if !utf8.ValidString(request.Reason) || len([]rune(request.Reason)) > MaxReviewReasonRunes {
		return ReviewRequest{}, fmt.Errorf("tool review reason exceeds %d characters", MaxReviewReasonRunes)
	}
	if request.Action == ReviewApprove && request.Reason != "" {
		return ReviewRequest{}, errors.New("tool approval cannot include a denial reason")
	}
	if (request.Tool == ShellTool || request.Tool == ScriptProcessTool) && request.WorkspaceRoot != "" {
		return ReviewRequest{}, errors.New("non-file review cannot include a workspace root")
	}
	if request.Action == ReviewDeny && request.WorkspaceRoot != "" {
		return ReviewRequest{}, errors.New("tool denial cannot include a workspace root")
	}
	return request, nil
}

func validArgumentName(value string) bool {
	if value == "" || len(value) > MaxArgumentNameRunes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, current := range []byte(value) {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') || current == '_' {
			continue
		}
		return false
	}
	return true
}
