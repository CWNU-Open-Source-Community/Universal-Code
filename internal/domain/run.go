package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type Profile string

const (
	ProfileCode   Profile = "code"
	ProfileReview Profile = "review"
	ProfileLearn  Profile = "learn"
	ProfileScript Profile = "script"
)

func ParseProfile(value string) (Profile, error) {
	profile := Profile(strings.ToLower(strings.TrimSpace(value)))
	switch profile {
	case ProfileCode, ProfileReview, ProfileLearn, ProfileScript:
		return profile, nil
	default:
		return "", fmt.Errorf("unsupported profile %q", value)
	}
}

type Scope struct {
	WorkspaceID       string        `json:"workspace_id,omitempty"`
	NetworkMode       string        `json:"network_mode"`
	AllowedTargets    []string      `json:"allowed_targets,omitempty"`
	WebAccessMode     WebAccessMode `json:"web_access_mode,omitempty"`
	WebAllowedTargets []string      `json:"web_allowed_targets,omitempty"`
}

func DefaultScope(workspaceID string) Scope {
	return Scope{WorkspaceID: strings.TrimSpace(workspaceID), NetworkMode: "disabled"}
}

func (s Scope) Validate() error {
	if err := s.ValidateWebAccess(); err != nil {
		return err
	}
	switch s.NetworkMode {
	case "disabled", "allowlist":
		return nil
	default:
		return fmt.Errorf("unsupported network mode %q", s.NetworkMode)
	}
}

type RunConfig struct {
	ModelRoute      string  `json:"model_route"`
	Interactive     bool    `json:"interactive"`
	RequestedBudget *Budget `json:"requested_budget,omitempty"`
	// ProjectConfig is the immutable narrowing-only .prayu snapshot pinned at
	// Run creation. Editing the project file later never changes this Run.
	ProjectConfig            json.RawMessage `json:"project_config,omitempty"`
	ProjectConfigFingerprint string          `json:"project_config_fingerprint,omitempty"`
	// ProjectInstructions is the immutable, provenance-rich hierarchy selected
	// at Run creation (or by an explicit confirmed refresh). It is workflow
	// guidance only and is never an authorization source.
	ProjectInstructions            json.RawMessage `json:"project_instructions,omitempty"`
	ProjectInstructionsFingerprint string          `json:"project_instructions_fingerprint,omitempty"`
	// ContinuityContext is an immutable checkpoint/Fork/Resume snapshot. It is
	// historical user data only: the embedded authority projection must remain
	// all-false and can never recreate an approval, capability, process, lease,
	// credential, network grant, or execution profile.
	ContinuityContext            json.RawMessage `json:"continuity_context,omitempty"`
	ContinuityContextFingerprint string          `json:"continuity_context_fingerprint,omitempty"`
}

func (c RunConfig) Validate() error {
	if c.RequestedBudget != nil {
		if err := ValidateTaskBudget(*c.RequestedBudget); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.ModelRoute) == "" {
		return errors.New("model route is required")
	}
	if len(c.ProjectInstructions) == 0 {
		if c.ProjectInstructionsFingerprint != "" {
			return errors.New("project instruction fingerprint requires a snapshot")
		}
	} else {
		if len(c.ProjectInstructions) > 512*1024 || !json.Valid(c.ProjectInstructions) ||
			!validLowerHexDigest(c.ProjectInstructionsFingerprint) {
			return errors.New("project instruction snapshot or fingerprint is invalid")
		}
		var binding struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(c.ProjectInstructions, &binding); err != nil ||
			binding.Fingerprint != c.ProjectInstructionsFingerprint {
			return errors.New("project instruction snapshot fingerprint binding is invalid")
		}
	}
	if len(c.ContinuityContext) == 0 {
		if c.ContinuityContextFingerprint != "" {
			return errors.New("continuity context fingerprint requires a snapshot")
		}
		return nil
	}
	if len(c.ContinuityContext) > 512*1024 || !json.Valid(c.ContinuityContext) ||
		!validLowerHexDigest(c.ContinuityContextFingerprint) {
		return errors.New("continuity context snapshot or fingerprint is invalid")
	}
	var continuity struct {
		ProtocolVersion string `json:"protocol_version"`
		Fingerprint     string `json:"fingerprint"`
		Authority       struct {
			Capability       bool `json:"capability"`
			Approval         bool `json:"approval"`
			Process          bool `json:"process"`
			TerminalLease    bool `json:"terminal_lease"`
			Network          bool `json:"network"`
			Credential       bool `json:"credential"`
			Secret           bool `json:"secret"`
			ExecutionProfile bool `json:"execution_profile"`
		} `json:"authority"`
	}
	if err := json.Unmarshal(c.ContinuityContext, &continuity); err != nil ||
		continuity.ProtocolVersion != "continuity_snapshot.v1" ||
		continuity.Fingerprint != c.ContinuityContextFingerprint {
		return errors.New("continuity context snapshot fingerprint binding is invalid")
	}
	if continuity.Authority.Capability || continuity.Authority.Approval ||
		continuity.Authority.Process || continuity.Authority.TerminalLease ||
		continuity.Authority.Network || continuity.Authority.Credential ||
		continuity.Authority.Secret || continuity.Authority.ExecutionProfile {
		return errors.New("continuity context cannot inherit authority")
	}
	return nil
}

type Budget struct {
	MaxTurns       int     `json:"max_turns"`
	MaxTokens      int64   `json:"max_tokens,omitempty"`
	MaxToolCalls   int64   `json:"max_tool_calls,omitempty"`
	MaxCostUSD     float64 `json:"max_cost_usd,omitempty"`
	TimeoutSeconds int64   `json:"timeout_seconds,omitempty"`
}

func DefaultBudget() Budget {
	return Budget{MaxTurns: 100, MaxToolCalls: 100}
}

func (b Budget) Validate() error {
	if b.MaxTurns <= 0 {
		return errors.New("max turns must be positive")
	}
	if b.MaxTokens < 0 || b.MaxToolCalls < 0 || b.MaxCostUSD < 0 || b.TimeoutSeconds < 0 {
		return errors.New("budget limits cannot be negative")
	}
	if b.TimeoutSeconds > int64((1<<63-1)/int64(time.Second)) {
		return errors.New("timeout exceeds supported duration")
	}
	if math.IsNaN(b.MaxCostUSD) || math.IsInf(b.MaxCostUSD, 0) {
		return errors.New("max cost must be a finite number")
	}
	return nil
}

type Mission struct {
	ID          string
	Goal        string
	Profile     Profile
	WorkspaceID string
	Scope       Scope
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (m Mission) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("mission id is required")
	}
	if strings.TrimSpace(m.Goal) == "" {
		return errors.New("mission goal is required")
	}
	if _, err := ParseProfile(string(m.Profile)); err != nil {
		return err
	}
	if strings.TrimSpace(m.Scope.WorkspaceID) != strings.TrimSpace(m.WorkspaceID) {
		return errors.New("mission workspace and scope workspace must match")
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		return errors.New("mission timestamps are required")
	}
	return m.Scope.Validate()
}

type RunStatus string

const (
	RunCreated         RunStatus = "created"
	RunPreparing       RunStatus = "preparing"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunPaused          RunStatus = "paused"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
)

type Run struct {
	ID         string
	MissionID  string
	SessionID  string
	Status     RunStatus
	Config     RunConfig
	Budget     Budget
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (r Run) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(r.MissionID) == "" {
		return errors.New("mission id is required")
	}
	if !ValidRunStatus(r.Status) {
		return fmt.Errorf("invalid run status %q", r.Status)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return errors.New("run timestamps are required")
	}
	if (r.Status == RunRunning || r.Status == RunWaitingApproval || r.Status == RunPaused || r.Status == RunCompleted) && r.StartedAt == nil {
		return fmt.Errorf("run status %s requires started_at", r.Status)
	}
	if r.Terminal() && r.FinishedAt == nil {
		return fmt.Errorf("terminal run status %s requires finished_at", r.Status)
	}
	if err := r.Config.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
}

func ValidRunStatus(status RunStatus) bool {
	switch status {
	case RunCreated, RunPreparing, RunRunning, RunWaitingApproval, RunPaused, RunCompleted, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

func (r Run) Terminal() bool {
	return r.Status == RunCompleted || r.Status == RunFailed || r.Status == RunCancelled
}

func (r Run) CanTransition(to RunStatus) bool {
	if r.Status == to {
		return true
	}
	allowed := map[RunStatus]map[RunStatus]bool{
		RunCreated:         {RunPreparing: true, RunCancelled: true},
		RunPreparing:       {RunRunning: true, RunFailed: true, RunCancelled: true},
		RunRunning:         {RunWaitingApproval: true, RunPaused: true, RunCompleted: true, RunFailed: true, RunCancelled: true},
		RunWaitingApproval: {RunRunning: true, RunPaused: true, RunFailed: true, RunCancelled: true},
		RunPaused:          {RunRunning: true, RunFailed: true, RunCancelled: true},
	}
	return allowed[r.Status][to]
}

func (r *Run) Transition(to RunStatus, at time.Time) error {
	if r == nil {
		return errors.New("run is nil")
	}
	if !ValidRunStatus(to) {
		return fmt.Errorf("invalid run status %q", to)
	}
	if r.Status == to {
		return nil
	}
	if !r.CanTransition(to) {
		return fmt.Errorf("run cannot transition from %s to %s", r.Status, to)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	r.Status = to
	r.UpdatedAt = at
	if to == RunRunning && r.StartedAt == nil {
		started := at
		r.StartedAt = &started
	}
	if to == RunCompleted || to == RunFailed || to == RunCancelled {
		finished := at
		r.FinishedAt = &finished
	}
	return nil
}

type RunFilter struct {
	MissionID string
	Status    RunStatus
	Limit     int
	Offset    int
}
