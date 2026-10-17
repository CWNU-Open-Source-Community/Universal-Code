package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/outputsafe"
)

const (
	MaxCommandRuntimeJobsPerRun     = 32
	MaxCommandRuntimeActiveJobs     = 32
	MinCommandRuntimeOutputRead     = utf8.UTFMax
	MaxCommandRuntimeOutputRead     = 64 * 1024
	MaxCommandRuntimeWait           = 5 * time.Second
	MaxCommandRuntimeCancelGrace    = 5 * time.Second
	MaxCommandRuntimeStdinWrites    = 64
	MaxCommandRuntimeFrames         = 4096
	MaxCommandRuntimeStoredIntent   = 256 * 1024
	MaxCommandRuntimeStoredFrames   = 2 * 1024 * 1024
	MaxCommandRuntimeStoredOutput   = MaxCommandRuntimeArtifactBytes
	CommandRuntimeOwnerLeaseTTL     = 15 * time.Second
	commandRuntimeOwnerRenewEvery   = 5 * time.Second
	commandRuntimeOwnerRenewTimeout = 2 * time.Second
	commandRuntimeReadPollInterval  = 20 * time.Millisecond
)

var (
	ErrCommandRuntimeJobNotFound = errors.New("command runtime job was not found")
	ErrCommandRuntimeJobClosed   = errors.New("command runtime job is not running")
	ErrCommandRuntimeUncertain   = errors.New("command runtime operation is uncertain and cannot be replayed")
)

type CommandRuntimeJobState string

const (
	CommandRuntimeJobPrepared    CommandRuntimeJobState = "prepared"
	CommandRuntimeJobRunning     CommandRuntimeJobState = "running"
	CommandRuntimeJobStopping    CommandRuntimeJobState = "stopping"
	CommandRuntimeJobCompleted   CommandRuntimeJobState = "completed"
	CommandRuntimeJobFailed      CommandRuntimeJobState = "failed"
	CommandRuntimeJobTimedOut    CommandRuntimeJobState = "timed_out"
	CommandRuntimeJobCancelled   CommandRuntimeJobState = "cancelled"
	CommandRuntimeJobKilled      CommandRuntimeJobState = "killed"
	CommandRuntimeJobInterrupted CommandRuntimeJobState = "interrupted"
)

func (s CommandRuntimeJobState) Valid() bool {
	switch s {
	case CommandRuntimeJobPrepared, CommandRuntimeJobRunning,
		CommandRuntimeJobStopping, CommandRuntimeJobCompleted,
		CommandRuntimeJobFailed, CommandRuntimeJobTimedOut,
		CommandRuntimeJobCancelled, CommandRuntimeJobKilled,
		CommandRuntimeJobInterrupted:
		return true
	default:
		return false
	}
}

func (s CommandRuntimeJobState) Terminal() bool {
	return s == CommandRuntimeJobCompleted || s == CommandRuntimeJobFailed ||
		s == CommandRuntimeJobTimedOut || s == CommandRuntimeJobCancelled ||
		s == CommandRuntimeJobKilled || s == CommandRuntimeJobInterrupted
}

type CommandRuntimeStream string

const (
	CommandRuntimeStdout CommandRuntimeStream = "stdout"
	CommandRuntimeStderr CommandRuntimeStream = "stderr"
)

type CommandRuntimeFrame struct {
	Cursor     uint64               `json:"cursor"`
	NextCursor uint64               `json:"next_cursor"`
	Stream     CommandRuntimeStream `json:"stream"`
	Timestamp  time.Time            `json:"timestamp"`
	Text       string               `json:"text"`
}

type CommandRuntimeOutputPage struct {
	JobID            string                 `json:"job_id"`
	BaseCursor       uint64                 `json:"base_cursor"`
	NextCursor       uint64                 `json:"next_cursor"`
	EndCursor        uint64                 `json:"end_cursor"`
	Frames           []CommandRuntimeFrame  `json:"frames"`
	Dropped          bool                   `json:"dropped"`
	State            CommandRuntimeJobState `json:"state"`
	ExitCode         *int                   `json:"exit_code,omitempty"`
	TruncationReason string                 `json:"truncation_reason,omitempty"`
}

type CommandRuntimeScope struct {
	InvocationID           string
	OperationKey           string
	RunID                  string
	MissionID              string
	RootAgentID            string
	AgentID                string
	AgentAttemptID         string
	AttributionSource      domain.AgentAttributionSource
	SessionID              string
	WorkspaceID            string
	WorkspaceRootSHA256    string
	ModeSnapshotID         string
	ModeRevision           int64
	ProfileSnapshotID      string
	ProfileRevision        int64
	PermissionSnapshotID   string
	PermissionRevision     int64
	PermissionMode         domain.RunExecutionPermissionMode
	PermissionRuntimeEpoch string
	PermissionGeneration   uint64
	RunAuthorizationFence  uint64
	LeaseID                string
	LeaseGeneration        int64
	LeaseOwnerID           string
	Adapter                commandruntimeadapter.Identity
}

func (s CommandRuntimeScope) Validate() error {
	for _, value := range []string{s.InvocationID, s.OperationKey, s.RunID,
		s.MissionID, s.RootAgentID, s.AgentID,
		s.SessionID, s.WorkspaceID,
		s.ModeSnapshotID, s.ProfileSnapshotID, s.PermissionSnapshotID,
		s.LeaseID, s.LeaseOwnerID} {
		if strings.TrimSpace(value) != value || value == "" ||
			!validCommandRuntimeText(value, false) || len([]rune(value)) > 256 {
			return ErrCommandRuntimeBoundary
		}
	}
	attribution := domain.AgentAttribution{AgentID: s.AgentID,
		AgentAttemptID: s.AgentAttemptID, Source: s.AttributionSource}
	if attribution.Validate() != nil ||
		(s.AttributionSource == domain.AgentAttributionOperatorRoot &&
			s.AgentID != s.RootAgentID) ||
		(s.AttributionSource != domain.AgentAttributionRecorded &&
			s.AttributionSource != domain.AgentAttributionOperatorRoot) ||
		len(s.WorkspaceRootSHA256) != sha256.Size*2 || s.Adapter.Validate() != nil ||
		s.ModeRevision <= 0 || s.ProfileRevision <= 0 ||
		s.PermissionRevision <= 0 || s.LeaseGeneration <= 0 ||
		!commandruntimeadapter.ValidRuntimeBinding(s.PermissionMode, s.PermissionRuntimeEpoch, s.PermissionGeneration, s.RunAuthorizationFence) ||
		!s.Adapter.AllowsPermission(s.PermissionMode) {
		return ErrCommandRuntimeBoundary
	}
	if s.PermissionRuntimeEpoch != "" &&
		(strings.TrimSpace(s.PermissionRuntimeEpoch) != s.PermissionRuntimeEpoch ||
			!validCommandRuntimeText(s.PermissionRuntimeEpoch, false) ||
			len([]rune(s.PermissionRuntimeEpoch)) > 256) {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

// CommandRuntimeJob is the durable metadata and sanitized-output projection.
// Initial stdin content and process handles are intentionally absent.
type CommandRuntimeJob struct {
	ID                     string
	OperationDigest        string
	RequestFingerprint     string
	InvocationID           string
	RunID                  string
	MissionID              string
	SessionID              string
	WorkspaceID            string
	RootAgentID            string
	WorkspaceRootSHA256    string
	ModeSnapshotID         string
	ModeRevision           int64
	ProfileSnapshotID      string
	ProfileRevision        int64
	PermissionSnapshotID   string
	PermissionRevision     int64
	PermissionMode         domain.RunExecutionPermissionMode
	PermissionRuntimeEpoch string
	PermissionGeneration   uint64
	RunAuthorizationFence  uint64
	LeaseID                string
	LeaseGeneration        int64
	LeaseOwnerID           string
	Adapter                commandruntimeadapter.Identity
	OwnerID                string
	OwnerGeneration        int64
	OwnerRenewedAt         time.Time
	OwnerExpiresAt         time.Time
	IntentJSON             string
	SpecFingerprint        string
	Profile                CommandRuntimeProfile
	ExecutablePath         string
	ExecutableSHA256       string
	EnvironmentSHA256      string
	WorkingDirectory       string
	StdinPolicy            CommandRuntimeStdinPolicy
	Network                CommandRuntimeNetwork
	Credentials            CommandRuntimeCredentialPolicy
	TimeoutMilliseconds    int64
	InlineLimitBytes       int
	ArtifactLimitBytes     int
	State                  CommandRuntimeJobState
	PID                    int
	ProcessGroup           int
	Stdout                 string
	Stderr                 string
	StdoutObservedBytes    int64
	StderrObservedBytes    int64
	OutputCursor           uint64
	OutputBaseCursor       uint64
	OutputFramesJSON       string
	StdoutSHA256           string
	StderrSHA256           string
	TruncationReason       string
	ExitCode               *int
	TimedOut               bool
	Cancelled              bool
	Killed                 bool
	TreeReaped             bool
	JobAssignedAtCreation  bool
	StdinClosed            bool
	StdinWriteCount        int
	Version                int64
	CreatedAt              time.Time
	StartedAt              *time.Time
	CompletedAt            *time.Time
	UpdatedAt              time.Time
}

// CommandRuntimeJobMetadata is the output-free projection used by public
// collapsed activity summaries. It deliberately excludes intent JSON,
// stdout/stderr, output frames, process identity and authority snapshots.
type CommandRuntimeJobMetadata struct {
	MissionID        string
	SessionID        string
	WorkspaceID      string
	StdinPolicy      CommandRuntimeStdinPolicy
	StdinWriteCount  int
	Credentials      CommandRuntimeCredentialPolicy
	ID               string
	OperationDigest  string
	RunID            string
	WorkingDirectory string
	State            CommandRuntimeJobState
	ExitCode         *int
	StartedAt        *time.Time
	CompletedAt      *time.Time
	UpdatedAt        time.Time
}

func (m CommandRuntimeJobMetadata) Validate() error {
	if !domain.ValidAgentID(m.ID) || !domain.ValidAgentID(m.RunID) ||
		!validCommandRuntimeDigest(m.OperationDigest) || !m.State.Valid() ||
		strings.TrimSpace(m.WorkingDirectory) != m.WorkingDirectory ||
		m.WorkingDirectory == "" || len([]byte(m.WorkingDirectory)) > MaxCommandRuntimePathBytes ||
		m.UpdatedAt.IsZero() {
		return ErrCommandRuntimeBoundary
	}
	if m.State == CommandRuntimeJobPrepared {
		if m.StartedAt != nil || m.CompletedAt != nil || m.ExitCode != nil {
			return ErrCommandRuntimeBoundary
		}
		return nil
	}
	if m.StartedAt == nil || m.StartedAt.IsZero() || m.UpdatedAt.Before(*m.StartedAt) {
		return ErrCommandRuntimeBoundary
	}
	if m.State.Terminal() {
		if m.CompletedAt == nil || m.CompletedAt.Before(*m.StartedAt) || m.ExitCode == nil {
			return ErrCommandRuntimeBoundary
		}
	} else if m.CompletedAt != nil || m.ExitCode != nil {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

func (j CommandRuntimeJob) Validate() error {
	for _, value := range []string{j.ID, j.OperationDigest, j.RequestFingerprint,
		j.InvocationID, j.RunID, j.MissionID, j.SessionID, j.WorkspaceID,
		j.RootAgentID, j.WorkspaceRootSHA256, j.ModeSnapshotID,
		j.ProfileSnapshotID, j.PermissionSnapshotID, j.LeaseID, j.LeaseOwnerID,
		j.OwnerID,
		j.SpecFingerprint, j.ExecutablePath, j.ExecutableSHA256,
		j.EnvironmentSHA256, j.WorkingDirectory} {
		if strings.TrimSpace(value) != value || value == "" ||
			!validCommandRuntimeText(value, false) {
			return ErrCommandRuntimeBoundary
		}
	}
	for _, digest := range []string{j.OperationDigest, j.RequestFingerprint,
		j.WorkspaceRootSHA256, j.SpecFingerprint, j.ExecutableSHA256,
		j.EnvironmentSHA256} {
		if len(digest) != sha256.Size*2 {
			return ErrCommandRuntimeBoundary
		}
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != sha256.Size {
			return ErrCommandRuntimeBoundary
		}
	}
	for _, identity := range []string{j.ID, j.InvocationID, j.RunID, j.MissionID,
		j.SessionID, j.WorkspaceID, j.RootAgentID, j.ModeSnapshotID,
		j.ProfileSnapshotID, j.PermissionSnapshotID, j.LeaseID, j.LeaseOwnerID,
		j.OwnerID} {
		if len([]rune(identity)) > 256 {
			return ErrCommandRuntimeBoundary
		}
	}
	if len([]byte(j.IntentJSON)) == 0 || len([]byte(j.IntentJSON)) > MaxCommandRuntimeStoredIntent ||
		len([]byte(j.ExecutablePath)) > MaxCommandRuntimePathBytes ||
		len([]byte(j.WorkingDirectory)) > MaxCommandRuntimePathBytes ||
		!jsonValid(j.IntentJSON) || !j.Profile.Valid() || !j.StdinPolicy.Valid() ||
		(j.Network != CommandRuntimeNetworkDisabled &&
			j.Network != CommandRuntimeNetworkHost) || !j.State.Valid() ||
		(j.Network == CommandRuntimeNetworkHost &&
			(j.Adapter.Kind != commandruntimeadapter.KindHostUnsandboxed ||
				!j.Adapter.AllowsPermission(j.PermissionMode))) ||
		j.Credentials != CommandRuntimeCredentialsNone || j.Adapter.Validate() != nil ||
		(j.Adapter.Kind != commandruntimeadapter.KindLegacyUnbound &&
			!j.Adapter.AllowsPermission(j.PermissionMode)) ||
		(j.Adapter.Kind == commandruntimeadapter.KindLegacyUnbound &&
			!j.PermissionMode.IncludesFullAccess()) ||
		j.ModeRevision <= 0 || j.ProfileRevision <= 0 || j.PermissionRevision <= 0 ||
		j.LeaseGeneration <= 0 || j.OwnerGeneration <= 0 ||
		j.OwnerRenewedAt.IsZero() || j.OwnerExpiresAt.IsZero() ||
		j.OwnerRenewedAt.Before(j.CreatedAt) ||
		!j.OwnerExpiresAt.After(j.OwnerRenewedAt) ||
		j.TimeoutMilliseconds < 1 ||
		j.TimeoutMilliseconds > MaxCommandRuntimeTimeout.Milliseconds() ||
		j.InlineLimitBytes < MinCommandRuntimeInlineBytes ||
		j.InlineLimitBytes > MaxCommandRuntimeInlineBytes ||
		j.ArtifactLimitBytes < j.InlineLimitBytes ||
		j.ArtifactLimitBytes > MaxCommandRuntimeArtifactBytes ||
		!utf8.ValidString(j.Stdout) || !utf8.ValidString(j.Stderr) ||
		len([]byte(j.Stdout)) > j.ArtifactLimitBytes ||
		len([]byte(j.Stderr)) > j.ArtifactLimitBytes ||
		len([]byte(j.OutputFramesJSON)) == 0 ||
		len([]byte(j.OutputFramesJSON)) > MaxCommandRuntimeStoredFrames ||
		!jsonValid(j.OutputFramesJSON) || j.OutputBaseCursor > j.OutputCursor ||
		j.StdoutObservedBytes < 0 || j.StderrObservedBytes < 0 ||
		j.StdinWriteCount < 0 || j.StdinWriteCount > MaxCommandRuntimeStdinWrites ||
		j.PID < 0 || j.ProcessGroup < 0 ||
		(j.TruncationReason != "" && j.TruncationReason != "inline_window" &&
			j.TruncationReason != "artifact_limit") ||
		j.Version <= 0 || j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() ||
		j.UpdatedAt.Before(j.CreatedAt) {
		return ErrCommandRuntimeBoundary
	}
	if !commandruntimeadapter.ValidRuntimeBinding(j.PermissionMode, j.PermissionRuntimeEpoch, j.PermissionGeneration, j.RunAuthorizationFence) ||
		(j.PermissionRuntimeEpoch != "" &&
			(strings.TrimSpace(j.PermissionRuntimeEpoch) != j.PermissionRuntimeEpoch ||
				!validCommandRuntimeText(j.PermissionRuntimeEpoch, false) ||
				len([]rune(j.PermissionRuntimeEpoch)) > 256)) {
		return ErrCommandRuntimeBoundary
	}
	if j.State == CommandRuntimeJobPrepared {
		if j.PID != 0 || j.ProcessGroup != 0 || j.StartedAt != nil ||
			j.CompletedAt != nil || j.ExitCode != nil {
			return ErrCommandRuntimeBoundary
		}
	} else if j.StartedAt == nil || j.StartedAt.IsZero() {
		return ErrCommandRuntimeBoundary
	}
	if j.State == CommandRuntimeJobRunning || j.State == CommandRuntimeJobStopping {
		if !j.JobAssignedAtCreation {
			return ErrCommandRuntimeBoundary
		}
		switch j.Adapter.Kind {
		case commandruntimeadapter.KindSandboxedWorkspace:
			if j.PID != 0 || j.ProcessGroup != 0 {
				return ErrCommandRuntimeBoundary
			}
		case commandruntimeadapter.KindHostUnsandboxed,
			commandruntimeadapter.KindLegacyUnbound:
			if j.PID <= 0 || j.ProcessGroup <= 0 {
				return ErrCommandRuntimeBoundary
			}
		default:
			return ErrCommandRuntimeBoundary
		}
	}
	if j.State.Terminal() {
		if j.CompletedAt == nil || j.CompletedAt.Before(*j.StartedAt) ||
			!j.TreeReaped || j.ExitCode == nil ||
			!validCommandRuntimeDigest(j.StdoutSHA256) ||
			!validCommandRuntimeDigest(j.StderrSHA256) {
			return ErrCommandRuntimeBoundary
		}
	} else if j.CompletedAt != nil || j.ExitCode != nil {
		return ErrCommandRuntimeBoundary
	}
	if j.TimedOut != (j.State == CommandRuntimeJobTimedOut) ||
		j.Cancelled != (j.State == CommandRuntimeJobCancelled) ||
		j.Killed != (j.State == CommandRuntimeJobKilled) {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

type CommandRuntimeListFilter struct {
	RunID      string
	Limit      int
	ActiveOnly bool
}

type CommandRuntimeStore interface {
	PrepareCommandRuntimeJob(context.Context, CommandRuntimeJob) (CommandRuntimeJob, bool, error)
	UpdateCommandRuntimeJob(context.Context, CommandRuntimeJob, int64) (CommandRuntimeJob, error)
	GetCommandRuntimeJob(context.Context, string) (CommandRuntimeJob, error)
	ListCommandRuntimeJobs(context.Context, CommandRuntimeListFilter) ([]CommandRuntimeJob, error)
}

// CommandRuntimeAgentStore atomically persists execution attribution beside a
// newly prepared Job. For recorded model work AgentID/AgentAttemptID identify
// the actor. For reviewed operator work, operator_root carries the real root
// Agent authority anchor and deliberately omits an Agent attempt.
type CommandRuntimeAgentStore interface {
	PrepareCommandRuntimeJobForAgent(context.Context, CommandRuntimeJob,
		domain.AgentAttribution) (CommandRuntimeJob, bool, error)
}

// CommandRuntimeOwnershipStore lets a durable store prevent one process from
// reconciling a still-fenced Job owned by another live execution lease. Stores
// without this optional check retain the single-process behavior.
type CommandRuntimeOwnershipStore interface {
	CommandRuntimeJobOwnershipActive(context.Context, CommandRuntimeJob) (bool, error)
}

type CommandRuntimeStartRequest struct {
	Scope CommandRuntimeScope
	Spec  CommandRuntimeResolvedSpec
	// DispatchCheck is process-local host authority. It is never stored or
	// recovered as a grant, and is not consumed by a read-only Job replay.
	// The manager and native starter pass their actual final launch inputs.
	DispatchCheck func(context.Context, CommandRuntimeResolvedSpec) error `json:"-"`
	// OwnershipCheck renews only this admitted Job's lifetime. It cannot
	// authorize native launch or initial/later stdin, which use DispatchCheck.
	OwnershipCheck func(context.Context) error `json:"-"`
	// Digest-only native families retain stream counts/hashes without storing
	// output text in the Job, output ring, or read-only projections.
	DiscardOutput bool `json:"-"`
}

// CommandRuntimeOperationIdentity is the deterministic durable identity used
// before a process starts and again when its background Job reaches terminal
// state. It lets callers bind external transaction ledgers without retaining
// the plaintext idempotency key.
func CommandRuntimeOperationIdentity(runID, operationKey string) (string, string) {
	digest := commandRuntimeDigest("command_runtime_operation.v2", runID, operationKey)
	return digest, "command-job-" + digest[:24]
}

type CommandRuntimeJobSnapshot struct {
	ID                     string                         `json:"id"`
	Adapter                commandruntimeadapter.Identity `json:"adapter"`
	State                  CommandRuntimeJobState         `json:"state"`
	Profile                CommandRuntimeProfile          `json:"profile"`
	ExecutablePath         string                         `json:"executable_path"`
	ExecutableSHA256       string                         `json:"executable_sha256"`
	ExecutableIdentityKind string                         `json:"executable_identity_kind,omitempty"`
	WorkingDirectory       string                         `json:"working_directory"`
	EnvironmentSHA256      string                         `json:"environment_sha256"`
	Network                CommandRuntimeNetwork          `json:"network"`
	Credentials            CommandRuntimeCredentialPolicy `json:"credentials"`
	PID                    int                            `json:"pid,omitempty"`
	ProcessGroup           int                            `json:"process_group,omitempty"`
	ExitCode               *int                           `json:"exit_code,omitempty"`
	OutputCursor           uint64                         `json:"output_cursor"`
	OutputBaseCursor       uint64                         `json:"output_base_cursor"`
	StdoutSHA256           string                         `json:"stdout_sha256,omitempty"`
	StderrSHA256           string                         `json:"stderr_sha256,omitempty"`
	StdoutObservedBytes    int64                          `json:"stdout_observed_bytes"`
	StderrObservedBytes    int64                          `json:"stderr_observed_bytes"`
	TruncationReason       string                         `json:"truncation_reason,omitempty"`
	TreeReaped             bool                           `json:"tree_reaped"`
	JobAssignedAtCreation  bool                           `json:"job_assigned_at_creation"`
	StdinPolicy            CommandRuntimeStdinPolicy      `json:"stdin_policy"`
	StdinClosed            bool                           `json:"stdin_closed"`
	Version                int64                          `json:"record_version"`
	CreatedAt              time.Time                      `json:"created_at"`
	StartedAt              *time.Time                     `json:"started_at,omitempty"`
	CompletedAt            *time.Time                     `json:"completed_at,omitempty"`
}

func ProjectCommandRuntimeJob(job CommandRuntimeJob) CommandRuntimeJobSnapshot {
	return CommandRuntimeJobSnapshot{
		ID: job.ID, Adapter: job.Adapter, State: job.State, Profile: job.Profile,
		ExecutablePath: job.ExecutablePath, ExecutableSHA256: job.ExecutableSHA256,
		ExecutableIdentityKind: CommandRuntimeExecutableIdentityKind(job.Adapter),
		WorkingDirectory:       job.WorkingDirectory,
		EnvironmentSHA256:      job.EnvironmentSHA256, Network: job.Network,
		Credentials: job.Credentials,
		PID:         job.PID, ProcessGroup: job.ProcessGroup, ExitCode: cloneInt(job.ExitCode),
		OutputCursor: job.OutputCursor, OutputBaseCursor: job.OutputBaseCursor,
		StdoutSHA256: job.StdoutSHA256, StderrSHA256: job.StderrSHA256,
		StdoutObservedBytes: job.StdoutObservedBytes,
		StderrObservedBytes: job.StderrObservedBytes,
		TruncationReason:    job.TruncationReason, TreeReaped: job.TreeReaped,
		JobAssignedAtCreation: job.JobAssignedAtCreation,
		StdinPolicy:           job.StdinPolicy, StdinClosed: job.StdinClosed,
		Version: job.Version, CreatedAt: job.CreatedAt,
		StartedAt: cloneTime(job.StartedAt), CompletedAt: cloneTime(job.CompletedAt),
	}
}

type CommandRuntimeProcessOwnership struct {
	PID                   int
	ProcessGroup          int
	JobAssignedAtCreation bool
	KillOnClose           bool
}

type commandRuntimeProcess interface {
	Ownership() CommandRuntimeProcessOwnership
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	WriteStdin([]byte) (int, error)
	CloseStdin() error
	Wait() (int, error)
	Cancel(time.Duration) error
	Kill() error
	Close() error
}

type commandRuntimeStarter interface {
	Name() string
	Available() bool
	Start(context.Context, CommandRuntimeScope,
		CommandRuntimeResolvedSpec) (commandRuntimeProcess, error)
}

type CommandRuntimeManager struct {
	store             CommandRuntimeStore
	starter           commandRuntimeStarter
	hostProxy         *commandRuntimeHostProxySet
	fixed             *fixedCommandRuntime
	adapter           commandruntimeadapter.Identity
	ownerID           string
	ownerGeneration   int64
	ownerLeaseTTL     time.Duration
	ownerRenewEvery   time.Duration
	ownerRenewTimeout time.Duration

	startMu sync.Mutex
	mu      sync.RWMutex
	entries map[string]*commandRuntimeEntry
	closed  bool
}

type commandRuntimeEntry struct {
	mu          sync.Mutex
	persistMu   sync.Mutex
	inputGate   chan struct{}
	record      CommandRuntimeJob
	process     commandRuntimeProcess
	ring        commandRuntimeRing
	done        chan struct{}
	notify      chan struct{}
	desired     CommandRuntimeJobState
	stdoutDone  chan struct{}
	stderrDone  chan struct{}
	stdoutHash  hash.Hash
	stderrHash  hash.Hash
	inputs      map[string]commandRuntimeStdinResult
	terminalErr error
	// Process-local checks are retained only by this live owner. They are never
	// reconstructed from durable receipts when a manager restarts.
	authorityContext   context.Context
	authorityCheck     func(context.Context) error
	operatorInvocation bool
	discardOutput      bool
}

type commandRuntimeStdinResult struct {
	fingerprint string
	written     int
	snapshot    CommandRuntimeJobSnapshot
	uncertain   bool
}

func NewCommandRuntimeManager(store CommandRuntimeStore, starter commandRuntimeStarter,
	ownerID string,
) (*CommandRuntimeManager, error) {
	generation := time.Now().UTC().UnixNano()
	if starter == nil || generation <= 0 {
		return nil, ErrCommandRuntimeUnavailable
	}
	adapter := commandruntimeadapter.HostUnsandboxed(commandRuntimeDigest(
		"command_runtime_adapter_generation.v1", starter.Name(), ownerID,
		fmt.Sprint(generation)))
	return newCommandRuntimeManagerWithAdapter(store, starter, adapter, ownerID,
		generation)
}

func newCommandRuntimeManagerWithAdapter(store CommandRuntimeStore,
	starter commandRuntimeStarter, adapter commandruntimeadapter.Identity,
	ownerID string, generation int64,
) (*CommandRuntimeManager, error) {
	ownerID = strings.TrimSpace(ownerID)
	if store == nil || starter == nil || !starter.Available() ||
		ownerID == "" || !utf8.ValidString(ownerID) || len([]rune(ownerID)) > 256 {
		return nil, ErrCommandRuntimeUnavailable
	}
	if generation <= 0 || !adapter.Executable() {
		return nil, ErrCommandRuntimeUnavailable
	}
	return &CommandRuntimeManager{store: store, starter: starter, adapter: adapter,
		ownerID: ownerID, ownerGeneration: generation,
		ownerLeaseTTL:     CommandRuntimeOwnerLeaseTTL,
		ownerRenewEvery:   commandRuntimeOwnerRenewEvery,
		ownerRenewTimeout: commandRuntimeOwnerRenewTimeout,
		entries:           make(map[string]*commandRuntimeEntry)}, nil
}

func NewPlatformCommandRuntimeManager(store CommandRuntimeStore,
	ownerID string,
) (*CommandRuntimeManager, error) {
	manager, err := NewCommandRuntimeManager(store, newPlatformCommandRuntimeStarter(), ownerID)
	if err != nil {
		return nil, err
	}
	manager.hostProxy = &commandRuntimeHostProxySet{}
	return manager, nil
}

func (m *CommandRuntimeManager) Available() bool {
	if m == nil || m.store == nil || m.starter == nil || !m.starter.Available() || !m.adapter.Executable() {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.closed
}

func (m *CommandRuntimeManager) AdapterIdentity() (commandruntimeadapter.Identity, bool) {
	if !m.Available() {
		return commandruntimeadapter.Identity{}, false
	}
	return m.adapter, true
}

func (m *CommandRuntimeManager) Start(ctx context.Context,
	request CommandRuntimeStartRequest,
) (CommandRuntimeJobSnapshot, bool, error) {
	if ctx == nil || ctx.Err() != nil || m == nil ||
		request.Scope.Validate() != nil || !request.Scope.Adapter.SameBackend(m.adapter) ||
		(request.DispatchCheck != nil && request.OwnershipCheck == nil) {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	if !m.Available() {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeUnavailable
	}
	m.startMu.Lock()
	startLocked := true
	defer func() {
		if startLocked {
			m.startMu.Unlock()
		}
	}()
	if request.Spec.Spec.Version != CommandRuntimeProtocolVersion ||
		request.Spec.ExecutableIdentityKind != CommandRuntimeExecutableIdentityKind(request.Scope.Adapter) ||
		request.Spec.WorkspaceRootSHA256 != request.Scope.WorkspaceRootSHA256 ||
		!request.Spec.Spec.Network.Valid() ||
		(request.Spec.Spec.Network == CommandRuntimeNetworkHost &&
			(request.Scope.Adapter.Kind != commandruntimeadapter.KindHostUnsandboxed ||
				!request.Scope.Adapter.AllowsPermission(request.Scope.PermissionMode))) ||
		validateCommandRuntimeAttachmentInput(request.Spec) != nil {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	m.mu.Lock()
	if m.closed || len(m.entries) >= MaxCommandRuntimeActiveJobs {
		m.mu.Unlock()
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeUnavailable
	}
	m.mu.Unlock()

	operationDigest, jobID := CommandRuntimeOperationIdentity(request.Scope.RunID,
		request.Scope.OperationKey)
	fingerprint := CommandRuntimeSpecFingerprint(request.Spec)
	requestParts := []string{
		request.Scope.RunID, request.Scope.MissionID, request.Scope.SessionID,
		request.Scope.WorkspaceID, request.Scope.RootAgentID,
		request.Scope.AgentID, request.Scope.AgentAttemptID,
		string(request.Scope.AttributionSource),
		request.Scope.WorkspaceRootSHA256,
		request.Scope.ModeSnapshotID, fmt.Sprint(request.Scope.ModeRevision),
		request.Scope.ProfileSnapshotID, fmt.Sprint(request.Scope.ProfileRevision),
		request.Scope.PermissionSnapshotID, fmt.Sprint(request.Scope.PermissionRevision),
		string(request.Scope.PermissionMode), request.Scope.LeaseID,
		fmt.Sprint(request.Scope.LeaseGeneration), request.Scope.LeaseOwnerID,
		string(request.Scope.Adapter.Kind), request.Scope.Adapter.Backend,
		request.Scope.Adapter.BackendIdentity, request.Scope.Adapter.Generation,
		string(request.Scope.Adapter.IsolationGrade),
		string(request.Scope.Adapter.NetworkPolicy),
		string(request.Scope.Adapter.CredentialPolicy), fingerprint,
	}
	if request.Scope.PermissionRuntimeEpoch != "" {
		requestParts = append(requestParts, "permission_runtime_epoch.v1",
			request.Scope.PermissionRuntimeEpoch,
			fmt.Sprint(request.Scope.PermissionGeneration))
	}
	if request.Scope.RunAuthorizationFence != 0 {
		requestParts = append(requestParts, "run_authorization_fence", fmt.Sprint(request.Scope.RunAuthorizationFence))
	}
	if request.DiscardOutput {
		requestParts = append(requestParts, "digest_only_output.v1")
	}
	now := time.Now().UTC()
	ownerExpiresAt := now.Add(m.ownerLeaseTTL)
	record := CommandRuntimeJob{
		ID: jobID, OperationDigest: operationDigest,
		RequestFingerprint: commandRuntimeDigest(append(
			[]string{"command_runtime_request.v2"}, requestParts...)...),
		InvocationID: request.Scope.InvocationID, RunID: request.Scope.RunID,
		MissionID: request.Scope.MissionID, SessionID: request.Scope.SessionID,
		WorkspaceID: request.Scope.WorkspaceID, RootAgentID: request.Scope.RootAgentID,
		WorkspaceRootSHA256: request.Scope.WorkspaceRootSHA256,
		ModeSnapshotID:      request.Scope.ModeSnapshotID, ModeRevision: request.Scope.ModeRevision,
		ProfileSnapshotID:      request.Scope.ProfileSnapshotID,
		ProfileRevision:        request.Scope.ProfileRevision,
		PermissionSnapshotID:   request.Scope.PermissionSnapshotID,
		PermissionRevision:     request.Scope.PermissionRevision,
		PermissionMode:         request.Scope.PermissionMode,
		PermissionRuntimeEpoch: request.Scope.PermissionRuntimeEpoch,
		PermissionGeneration:   request.Scope.PermissionGeneration,
		RunAuthorizationFence:  request.Scope.RunAuthorizationFence,
		LeaseID:                request.Scope.LeaseID, LeaseGeneration: request.Scope.LeaseGeneration,
		LeaseOwnerID: request.Scope.LeaseOwnerID, Adapter: request.Scope.Adapter,
		OwnerID: m.ownerID, OwnerGeneration: m.ownerGeneration,
		OwnerRenewedAt: now, OwnerExpiresAt: ownerExpiresAt,
		IntentJSON:      commandRuntimeIntentJSON(request.Spec),
		SpecFingerprint: fingerprint, Profile: request.Spec.Spec.Profile,
		ExecutablePath:    request.Spec.ExecutablePath,
		ExecutableSHA256:  request.Spec.ExecutableSHA256,
		EnvironmentSHA256: request.Spec.EnvironmentSHA256,
		WorkingDirectory:  request.Spec.Spec.WorkingDirectory,
		StdinPolicy:       request.Spec.Spec.StdinPolicy, Network: request.Spec.Spec.Network,
		Credentials:         request.Spec.Spec.Credentials,
		TimeoutMilliseconds: request.Spec.Spec.TimeoutMilliseconds,
		InlineLimitBytes:    request.Spec.Spec.Output.InlineBytes,
		ArtifactLimitBytes:  request.Spec.Spec.Output.ArtifactBytes,
		State:               CommandRuntimeJobPrepared, OutputFramesJSON: "[]",
		StdinClosed: request.Spec.Spec.StdinPolicy == CommandRuntimeStdinClosed,
		Version:     1, CreatedAt: now, UpdatedAt: now,
	}
	var stored CommandRuntimeJob
	var replayed bool
	var err error
	if request.Scope.AttributionSource == domain.AgentAttributionOperatorRoot {
		if request.DispatchCheck == nil {
			return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
		}
		if err := request.DispatchCheck(ctx, request.Spec); err != nil {
			return CommandRuntimeJobSnapshot{}, false, err
		}
		ctx = context.WithValue(ctx, commandRuntimeOperatorPreparationKey{}, commandRuntimeOperatorPreparation{record.ID, record.RequestFingerprint})
	}
	if attributed, ok := m.store.(CommandRuntimeAgentStore); ok {
		stored, replayed, err = attributed.PrepareCommandRuntimeJobForAgent(ctx,
			record, domain.AgentAttribution{AgentID: request.Scope.AgentID,
				AgentAttemptID: request.Scope.AgentAttemptID,
				Source:         request.Scope.AttributionSource})
	} else {
		stored, replayed, err = m.store.PrepareCommandRuntimeJob(ctx, record)
	}
	if err != nil {
		return CommandRuntimeJobSnapshot{}, false, err
	}
	if replayed {
		if stored.RequestFingerprint != record.RequestFingerprint ||
			stored.SpecFingerprint != record.SpecFingerprint ||
			!stored.Adapter.SameBackend(record.Adapter) {
			return CommandRuntimeJobSnapshot{}, true, ErrCommandRuntimeUncertain
		}
		if stored.State.Terminal() {
			return ProjectCommandRuntimeJob(stored), true, nil
		}
		if entry := m.entry(jobID); entry != nil {
			if admission, ok := entry.process.(interface{ waitForAdmission(context.Context) error }); ok {
				// Replays share the owner's admission without acquiring its
				// cancellation authority. Neither waiter may hold off Shutdown.
				m.startMu.Unlock()
				startLocked = false
				if err := admission.waitForAdmission(ctx); err != nil {
					if ctx.Err() == nil {
						select {
						case <-entry.done:
						case <-ctx.Done():
						}
					}
					return entry.snapshot(), true, errors.Join(err, ctx.Err(), entry.persistError())
				}
			}
			return entry.snapshot(), true, entry.persistError()
		}
		return ProjectCommandRuntimeJob(stored), true, ErrCommandRuntimeUncertain
	}

	dispatchCtx := withCommandRuntimeDispatchCheck(ctx, request.DispatchCheck)
	var process commandRuntimeProcess
	err = CheckCommandRuntimeDispatch(dispatchCtx, request.Spec)
	if err == nil {
		process, err = m.starter.Start(context.WithoutCancel(dispatchCtx), request.Scope,
			request.Spec)
	}
	if err != nil {
		failed := stored
		started := now
		completed := time.Now().UTC()
		exitCode := 127
		failed.State = CommandRuntimeJobFailed
		failed.StartedAt = &started
		failed.CompletedAt = &completed
		failed.ExitCode = &exitCode
		failed.TreeReaped = true
		failed.StdinClosed = true
		failed.Stderr = outputsafe.Sanitize([]byte(err.Error()))
		failed.StderrObservedBytes = int64(len([]byte(err.Error())))
		failed.StdoutSHA256 = commandRuntimeStringSHA256("")
		failed.StderrSHA256 = commandRuntimeStringSHA256(failed.Stderr)
		if request.DiscardOutput {
			failed.Stderr = ""
		}
		failed.Version++
		failed.UpdatedAt = completed
		updated, updateErr := m.store.UpdateCommandRuntimeJob(context.WithoutCancel(ctx), failed, stored.Version)
		if updateErr == nil {
			stored = updated
		}
		return ProjectCommandRuntimeJob(stored), false, errors.Join(err, updateErr)
	}
	ownership := process.Ownership()
	validOwnership := ownership.JobAssignedAtCreation && ownership.KillOnClose
	if m.adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
		validOwnership = validOwnership && ownership.PID == 0 && ownership.ProcessGroup == 0
	} else {
		validOwnership = validOwnership && ownership.PID > 0 && ownership.ProcessGroup > 0
	}
	if !validOwnership {
		_ = process.Kill()
		_ = process.Close()
		failed := stored
		started := time.Now().UTC()
		completed := started
		exitCode := 125
		failed.State = CommandRuntimeJobFailed
		failed.StartedAt = &started
		failed.CompletedAt = &completed
		failed.ExitCode = &exitCode
		failed.TreeReaped = true
		failed.StdinClosed = true
		failed.StdoutSHA256 = commandRuntimeStringSHA256("")
		failed.StderrSHA256 = commandRuntimeStringSHA256("")
		failed.Version++
		failed.UpdatedAt = completed
		updated, updateErr := m.store.UpdateCommandRuntimeJob(
			context.WithoutCancel(ctx), failed, stored.Version)
		if updateErr == nil {
			stored = updated
		}
		return ProjectCommandRuntimeJob(stored), false,
			errors.Join(ErrCommandRuntimeUnavailable, updateErr)
	}
	started := time.Now().UTC()
	running := stored
	running.State = CommandRuntimeJobRunning
	running.PID = ownership.PID
	running.ProcessGroup = ownership.ProcessGroup
	running.JobAssignedAtCreation = ownership.JobAssignedAtCreation
	running.StartedAt = &started
	running.Version++
	running.UpdatedAt = started
	// Process ownership is established. Finish the bounded ownership
	// handoff even if the caller cancels between the SQL update and readback;
	// otherwise an owned process can lose its terminal-receipt collector.
	// Foreground cancellation is handled after this Job is registered.
	handoffCtx, cancelHandoff := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	updated, err := m.store.UpdateCommandRuntimeJob(handoffCtx, running, stored.Version)
	cancelHandoff()
	if err != nil {
		_ = process.Kill()
		_ = process.Close()
		return CommandRuntimeJobSnapshot{}, false, err
	}
	entry := &commandRuntimeEntry{
		record: updated, process: process,
		authorityContext:   context.WithoutCancel(ctx),
		operatorInvocation: request.Scope.AttributionSource == domain.AgentAttributionOperatorRoot,
		discardOutput:      request.DiscardOutput,
		ring:               commandRuntimeRing{capacity: updated.InlineLimitBytes},
		done:               make(chan struct{}), notify: make(chan struct{}, 1),
		stdoutDone: make(chan struct{}), stderrDone: make(chan struct{}),
		stdoutHash: sha256.New(), stderrHash: sha256.New(),
		inputs: make(map[string]commandRuntimeStdinResult), inputGate: make(chan struct{}, 1),
	}
	entry.authorityCheck = request.OwnershipCheck
	if request.DiscardOutput {
		entry.ring.frames = []CommandRuntimeFrame{}
	}
	entry.inputGate <- struct{}{}
	m.mu.Lock()
	m.entries[jobID] = entry
	m.mu.Unlock()

	go m.collect(entry, CommandRuntimeStdout, process.Stdout(), entry.stdoutDone)
	go m.collect(entry, CommandRuntimeStderr, process.Stderr(), entry.stderrDone)
	go m.maintainOwnership(entry)
	go m.wait(entry)
	go m.enforceTimeout(entry, time.Duration(updated.TimeoutMilliseconds)*time.Millisecond)
	if request.Spec.Spec.StdinPolicy == CommandRuntimeStdinPipe &&
		(request.Spec.Spec.InitialStdin != "" || request.Spec.Spec.CloseInitialStdin) {
		<-entry.inputGate
		// Start has committed ownership to this Job. Its initial bytes were
		// already bound to that operation; completing the originating request
		// must not cancel their delivery. Rebind only cancellation lifetime:
		// the same host check still re-reads current permission and lease state.
		initialInputCtx := withCommandRuntimeDispatchCheck(context.WithoutCancel(ctx), request.DispatchCheck)
		go m.writeInitialStdin(entry, []byte(request.Spec.Spec.InitialStdin),
			request.Spec.Spec.CloseInitialStdin, func() error {
				return CheckCommandRuntimeDispatch(initialInputCtx, request.Spec)
			})
	}
	// Keep the originating attempt live until an asynchronous sandbox adapter
	// has passed its final native dispatch check. Collectors, owner renewal,
	// timeout and initial stdin must already run while admission is pending.
	if admission, ok := process.(interface{ waitForAdmission(context.Context) error }); ok {
		// The entry is fully registered. Shutdown must be able to acquire the
		// start lock and stop a backend that never finishes admission.
		m.startMu.Unlock()
		startLocked = false
		if err := admission.waitForAdmission(ctx); err != nil {
			if ctx.Err() == nil {
				select {
				case <-entry.done:
					return entry.snapshot(), false, errors.Join(err, entry.persistError())
				case <-ctx.Done():
				}
			}
			_, stopErr := m.Stop(context.WithoutCancel(ctx), jobID, true, 0)
			return entry.snapshot(), false, errors.Join(err, ctx.Err(), stopErr)
		}
	}
	return entry.snapshot(), false, nil
}

// writeInitialStdin shares the per-Job input gate with later write_stdin calls.
// It runs only after wait and timeout ownership have started, so a child that
// never reads its pipe cannot leave Start blocked beyond the Job lifecycle.
func (m *CommandRuntimeManager) writeInitialStdin(entry *commandRuntimeEntry,
	data []byte, closeAfter bool, dispatchCheck func() error,
) {
	defer entry.unlockInput()
	var err error
	if dispatchCheck != nil {
		err = dispatchCheck()
	}
	if err != nil {
		_ = entry.process.CloseStdin()
		if entry.setDesired(CommandRuntimeJobFailed) {
			_ = entry.process.Kill()
		}
		return
	}
	if len(data) > 0 {
		_, err = entry.process.WriteStdin(data)
	}
	stdinClosed := false
	if closeAfter {
		closeErr := entry.process.CloseStdin()
		stdinClosed = closeErr == nil
		err = errors.Join(err, closeErr)
	}
	if stdinClosed {
		entry.mu.Lock()
		entry.record.StdinClosed = true
		entry.mu.Unlock()
	}
	if err != nil && entry.setDesired(CommandRuntimeJobFailed) {
		_ = entry.process.Kill()
	}
}

func (m *CommandRuntimeManager) Wait(ctx context.Context, jobID string,
	wait time.Duration, cursor uint64, maxBytes int,
) (CommandRuntimeJobSnapshot, CommandRuntimeOutputPage, error) {
	if maxBytes == 0 {
		maxBytes = MaxCommandRuntimeOutputRead
	}
	if wait < 0 || wait > MaxCommandRuntimeWait ||
		maxBytes < MinCommandRuntimeOutputRead || maxBytes > MaxCommandRuntimeOutputRead {
		return CommandRuntimeJobSnapshot{}, CommandRuntimeOutputPage{}, ErrCommandRuntimeBoundary
	}
	entry := m.entry(jobID)
	if entry == nil {
		job, err := m.store.GetCommandRuntimeJob(ctx, strings.TrimSpace(jobID))
		if err != nil {
			return CommandRuntimeJobSnapshot{}, CommandRuntimeOutputPage{}, err
		}
		return ProjectCommandRuntimeJob(job), pageFromStoredJob(job, cursor, maxBytes), nil
	}
	deadline := time.Now().Add(wait)
	for {
		snapshot, page, err := entry.readSnapshot(cursor, maxBytes)
		if err != nil {
			return CommandRuntimeJobSnapshot{}, CommandRuntimeOutputPage{}, err
		}
		if terminalErr := entry.persistError(); terminalErr != nil {
			return snapshot, page, terminalErr
		}
		if len(page.Frames) > 0 || snapshot.State.Terminal() || wait == 0 ||
			time.Now().After(deadline) {
			return snapshot, page, nil
		}
		remaining := time.Until(deadline)
		if remaining > commandRuntimeReadPollInterval {
			remaining = commandRuntimeReadPollInterval
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CommandRuntimeJobSnapshot{}, CommandRuntimeOutputPage{}, ctx.Err()
		case <-entry.notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (m *CommandRuntimeManager) WriteStdin(ctx context.Context, jobID string,
	operationKey string, data []byte, closeAfter bool,
) (CommandRuntimeJobSnapshot, int, bool, error) {
	return m.WriteStdinGuarded(ctx, jobID, operationKey, data, closeAfter, nil)
}

// WriteStdinGuarded checks fresh host authority after acquiring the Job input
// gate and resolving an exact replay, immediately before writing new bytes.
func (m *CommandRuntimeManager) WriteStdinGuarded(ctx context.Context, jobID string,
	operationKey string, data []byte, closeAfter bool,
	dispatchCheck func(context.Context, string, []byte, bool) error,
) (CommandRuntimeJobSnapshot, int, bool, error) {
	if ctx == nil || ctx.Err() != nil || len(data) > MaxCommandRuntimeStdinBytes ||
		outputsafe.Sanitize(data) != string(data) ||
		strings.TrimSpace(operationKey) == "" || len([]rune(operationKey)) > 256 {
		return CommandRuntimeJobSnapshot{}, 0, false, ErrCommandRuntimeBoundary
	}
	entry := m.entry(jobID)
	if entry == nil {
		return CommandRuntimeJobSnapshot{}, 0, false, ErrCommandRuntimeJobNotFound
	}
	fingerprint := commandRuntimeDigest("command_runtime_stdin.v2", jobID,
		operationKey, string(data), fmt.Sprint(closeAfter))
	if err := entry.lockInput(ctx); err != nil {
		return CommandRuntimeJobSnapshot{}, 0, false, err
	}
	defer entry.unlockInput()
	entry.mu.Lock()
	if existing, found := entry.inputs[operationKey]; found {
		entry.mu.Unlock()
		if existing.fingerprint != fingerprint {
			return CommandRuntimeJobSnapshot{}, 0, true, ErrCommandRuntimeUncertain
		}
		if existing.uncertain {
			return existing.snapshot, existing.written, true, ErrCommandRuntimeUncertain
		}
		return existing.snapshot, existing.written, true, nil
	}
	if entry.record.State != CommandRuntimeJobRunning ||
		entry.record.StdinPolicy != CommandRuntimeStdinPipe ||
		entry.record.StdinClosed || entry.record.StdinWriteCount >= MaxCommandRuntimeStdinWrites {
		entry.mu.Unlock()
		return CommandRuntimeJobSnapshot{}, 0, false, ErrCommandRuntimeJobClosed
	}
	process := entry.process
	entry.mu.Unlock()
	if dispatchCheck != nil {
		if err := dispatchCheck(ctx, jobID, data, closeAfter); err != nil {
			return CommandRuntimeJobSnapshot{}, 0, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return CommandRuntimeJobSnapshot{}, 0, false, err
	}
	written := 0
	var err error
	stdinClosed := false
	if len(data) > 0 {
		result := make(chan struct {
			written int
			err     error
		}, 1)
		go func() {
			count, writeErr := process.WriteStdin(data)
			result <- struct {
				written int
				err     error
			}{written: count, err: writeErr}
		}()
		select {
		case outcome := <-result:
			written, err = outcome.written, outcome.err
		case <-ctx.Done():
			closeErr := process.CloseStdin()
			stdinClosed = closeErr == nil
			outcome := <-result
			written, err = outcome.written, errors.Join(ctx.Err(), outcome.err, closeErr)
			closeAfter = true
		}
	}
	if closeAfter && !stdinClosed {
		closeErr := process.CloseStdin()
		stdinClosed = closeErr == nil
		err = errors.Join(err, closeErr)
	}
	entry.mu.Lock()
	entry.record.StdinWriteCount++
	entry.record.StdinClosed = stdinClosed || entry.record.StdinClosed
	entry.record.OutputCursor = entry.ring.next
	snapshot := ProjectCommandRuntimeJob(entry.record)
	entry.inputs[operationKey] = commandRuntimeStdinResult{
		fingerprint: fingerprint, written: written, snapshot: snapshot,
		uncertain: err != nil}
	entry.mu.Unlock()
	if err != nil {
		err = errors.Join(ErrCommandRuntimeUncertain, err)
	}
	return snapshot, written, false, err
}

func (m *CommandRuntimeManager) Stop(ctx context.Context, jobID string,
	kill bool, grace time.Duration,
) (CommandRuntimeJobSnapshot, error) {
	if ctx == nil || ctx.Err() != nil || grace < 0 || grace > MaxCommandRuntimeCancelGrace {
		return CommandRuntimeJobSnapshot{}, ErrCommandRuntimeBoundary
	}
	entry := m.entry(jobID)
	if entry == nil {
		job, err := m.store.GetCommandRuntimeJob(ctx, strings.TrimSpace(jobID))
		if err != nil {
			return CommandRuntimeJobSnapshot{}, err
		}
		if job.State.Terminal() {
			return ProjectCommandRuntimeJob(job), nil
		}
		return ProjectCommandRuntimeJob(job), ErrCommandRuntimeUncertain
	}
	if snapshot := entry.snapshot(); snapshot.State.Terminal() {
		return snapshot, nil
	}
	desired := CommandRuntimeJobCancelled
	if kill {
		desired = CommandRuntimeJobKilled
	}
	if !entry.setDesired(desired) {
		snapshot := entry.snapshot()
		if snapshot.State == CommandRuntimeJobStopping {
			return snapshot, entry.process.Kill()
		}
		return snapshot, nil
	}
	var err error
	if kill {
		err = entry.process.Kill()
	} else {
		err = entry.process.Cancel(grace)
	}
	return entry.snapshot(), err
}

func (m *CommandRuntimeManager) Get(ctx context.Context,
	jobID string,
) (CommandRuntimeJobSnapshot, error) {
	if entry := m.entry(jobID); entry != nil {
		return entry.snapshot(), nil
	}
	job, err := m.store.GetCommandRuntimeJob(ctx, strings.TrimSpace(jobID))
	return ProjectCommandRuntimeJob(job), err
}

func (m *CommandRuntimeManager) List(ctx context.Context,
	filter CommandRuntimeListFilter,
) ([]CommandRuntimeJobSnapshot, error) {
	jobs, err := m.store.ListCommandRuntimeJobs(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := make([]CommandRuntimeJobSnapshot, len(jobs))
	for index, job := range jobs {
		if entry := m.entry(job.ID); entry != nil {
			result[index] = entry.snapshot()
		} else {
			result[index] = ProjectCommandRuntimeJob(job)
		}
	}
	return result, nil
}

func (m *CommandRuntimeManager) Shutdown(ctx context.Context) (result error) {
	if m == nil {
		return nil
	}
	m.startMu.Lock()
	m.mu.Lock()
	// A timed-out caller can return while output and durable completion still
	// drain. Later shutdown calls must join those same entries before the store
	// owner releases SQLite.
	m.closed = true
	entries := make([]*commandRuntimeEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	m.startMu.Unlock()
	defer func() { result = errors.Join(result, m.hostProxy.close()) }()
	for _, entry := range entries {
		if entry.setDesired(CommandRuntimeJobInterrupted) {
			result = errors.Join(result, entry.process.Kill())
		}
	}
	for _, entry := range entries {
		select {
		case <-entry.done:
			result = errors.Join(result, entry.persistError())
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
	}
	return result
}

// OwnsActiveJob reports process-local ownership. Durable owner identity and
// generation prevent a second application process from adopting an active
// process merely because it can read the same SQLite database.
func (m *CommandRuntimeManager) OwnsActiveJob(job CommandRuntimeJob) bool {
	if m == nil || job.ID == "" || job.OwnerID != m.ownerID ||
		job.OwnerGeneration != m.ownerGeneration || !job.Adapter.SameBackend(m.adapter) {
		return false
	}
	entry := m.entry(job.ID)
	if entry == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return !entry.record.State.Terminal() && entry.record.OwnerID == job.OwnerID &&
		entry.record.OwnerGeneration == job.OwnerGeneration
}

// This is a live-owner check, not a reconstruction from a persisted source bit.
func (m *CommandRuntimeManager) OwnsActiveOperatorJob(job CommandRuntimeJob) bool {
	if !m.OwnsActiveJob(job) {
		return false
	}
	entry := m.entry(job.ID)
	if entry == nil || !entry.operatorInvocation || entry.authorityCheck == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(entry.authorityContext, m.ownerRenewTimeout)
	defer cancel()
	return entry.authorityCheck(ctx) == nil
}

func (m *CommandRuntimeManager) ReconcileStartup(ctx context.Context) (int, error) {
	if m == nil || m.store == nil {
		return 0, ErrCommandRuntimeUnavailable
	}
	jobs, err := m.store.ListCommandRuntimeJobs(ctx, CommandRuntimeListFilter{ActiveOnly: true, Limit: 500})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, job := range jobs {
		if m.entry(job.ID) != nil || job.State.Terminal() {
			continue
		}
		if job.Adapter.Kind == commandruntimeadapter.KindLegacyUnbound {
			// v116 rows are durable read-only evidence. No new manager may use
			// their PID or owner metadata as execution authority.
			continue
		}
		if job.Adapter.Kind != m.adapter.Kind ||
			job.Adapter.Backend != m.adapter.Backend ||
			job.Adapter.BackendIdentity != m.adapter.BackendIdentity {
			continue
		}
		if ownershipStore, ok := m.store.(CommandRuntimeOwnershipStore); ok {
			active, ownershipErr := ownershipStore.CommandRuntimeJobOwnershipActive(ctx, job)
			if ownershipErr != nil {
				return count, ownershipErr
			}
			if active {
				continue
			}
		}
		if job.Adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
			cleanupCommandRuntimeOrphan(job.PID, job.ProcessGroup)
		}
		now := time.Now().UTC()
		exitCode := 125
		previous := job.Version
		if job.StartedAt == nil {
			started := job.CreatedAt
			job.StartedAt = &started
		}
		job.State = CommandRuntimeJobInterrupted
		job.CompletedAt = &now
		job.ExitCode = &exitCode
		job.TreeReaped = true
		job.StdinClosed = true
		job.StdoutSHA256 = commandRuntimeStringSHA256(job.Stdout)
		job.StderrSHA256 = commandRuntimeStringSHA256(job.Stderr)
		job.Version++
		job.UpdatedAt = now
		if _, updateErr := m.store.UpdateCommandRuntimeJob(ctx, job, previous); updateErr != nil {
			current, getErr := m.store.GetCommandRuntimeJob(ctx, job.ID)
			if getErr == nil && current.State.Terminal() {
				continue
			}
			return count, errors.Join(updateErr, getErr)
		}
		count++
	}
	return count, nil
}

func (m *CommandRuntimeManager) collect(entry *commandRuntimeEntry,
	stream CommandRuntimeStream, reader io.ReadCloser, done chan struct{},
) {
	defer close(done)
	if reader == nil {
		return
	}
	defer reader.Close()
	if entry.discardOutput {
		buffer := make([]byte, 16*1024)
		for {
			count, err := reader.Read(buffer)
			if count > 0 {
				entry.mu.Lock()
				if stream == CommandRuntimeStdout {
					entry.record.StdoutObservedBytes += int64(count)
					_, _ = entry.stdoutHash.Write(buffer[:count])
				} else {
					entry.record.StderrObservedBytes += int64(count)
					_, _ = entry.stderrHash.Write(buffer[:count])
				}
				entry.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	entry.mu.Lock()
	legacyPowerShell := commandRuntimeLegacyPowerShellOutput(entry.record, stream)
	entry.mu.Unlock()
	decoded := commandRuntimeTextReader(commandRuntimeObservedReader{
		reader: reader, observe: func(count int) { entry.observe(stream, count) },
	}, legacyPowerShell)
	sanitizer := &outputsafe.RedactingStream{}
	buffer := make([]byte, 16*1024)
	for {
		count, err := decoded.Read(buffer)
		if count > 0 {
			entry.appendOutput(stream, sanitizer.Feed(buffer[:count]), time.Now().UTC())
		}
		if err != nil {
			entry.appendOutput(stream, sanitizer.Flush(), time.Now().UTC())
			return
		}
	}
}

func (m *CommandRuntimeManager) maintainOwnership(entry *commandRuntimeEntry) {
	ticker := time.NewTicker(m.ownerRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-entry.done:
			return
		case <-ticker.C:
			if snapshot := entry.snapshot(); snapshot.State == CommandRuntimeJobStopping {
				_ = entry.process.Kill()
			}
			ctx, cancel := context.WithTimeout(entry.authorityContext, m.ownerRenewTimeout)
			var err error
			phase := "authority recheck"
			if entry.authorityCheck != nil {
				err = entry.authorityCheck(ctx)
			}
			if err == nil {
				phase = "owner renewal"
				err = m.renewOwnership(ctx, entry)
			}
			cancel()
			if err != nil {
				if entry.setDesired(CommandRuntimeJobInterrupted) {
					// Host failures may have no child stderr. Keep their reason in
					// the existing sanitized, bounded output before killing the Job.
					if !entry.discardOutput {
						detail := err
						if cause := errors.Unwrap(err); cause != nil {
							detail = errors.Join(err, cause)
						}
						diagnostic := "\ncommand runtime " + phase + " failed: " + outputsafe.Sanitize([]byte(detail.Error())) + "\n"
						entry.observe(CommandRuntimeStderr, len(diagnostic))
						entry.appendOutput(CommandRuntimeStderr, diagnostic, time.Now().UTC())
					}
					_ = entry.process.Kill()
				}
				return
			}
		}
	}
}

func (m *CommandRuntimeManager) renewOwnership(ctx context.Context,
	entry *commandRuntimeEntry,
) error {
	entry.persistMu.Lock()
	defer entry.persistMu.Unlock()
	select {
	case <-entry.done:
		return nil
	default:
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.record.State.Terminal() {
		return nil
	}
	previous := entry.record.Version
	now := time.Now().UTC()
	record := entry.record
	record.OutputCursor = entry.ring.next
	record.OutputBaseCursor = entry.ring.base
	record.OutputFramesJSON = entry.ring.json()
	record.OwnerRenewedAt = now
	record.OwnerExpiresAt = now.Add(m.ownerLeaseTTL)
	record.Version++
	record.UpdatedAt = now
	updated, err := m.store.UpdateCommandRuntimeJob(ctx, record, previous)
	if err != nil {
		return err
	}
	entry.record = updated
	return nil
}

func (m *CommandRuntimeManager) wait(entry *commandRuntimeEntry) {
	exitCode, waitErr := entry.process.Wait()
	if waitErr != nil {
		// An adapter/transport failure supplies no trustworthy program exit.
		// Preserve that distinction durably: bisect must not interpret an
		// infrastructure failure as a bad revision or a legitimate exit 125.
		exitCode = -1
	}
	treeReaped := true
	if proof, ok := entry.process.(interface{ TreeReaped() bool }); ok {
		treeReaped = proof.TreeReaped()
	}
	_ = entry.process.CloseStdin()
	<-entry.stdoutDone
	<-entry.stderrDone
	entry.persistMu.Lock()
	defer entry.persistMu.Unlock()
	now := time.Now().UTC()
	entry.mu.Lock()
	desired := entry.desired
	state := desired
	if state == "" || state == CommandRuntimeJobStopping {
		if waitErr != nil || exitCode != 0 {
			state = CommandRuntimeJobFailed
		} else {
			state = CommandRuntimeJobCompleted
		}
	}
	previous := entry.record.Version
	record := entry.record
	record.State = state
	record.ExitCode = &exitCode
	record.CompletedAt = &now
	record.UpdatedAt = now
	record.Version++
	record.TreeReaped = treeReaped
	if !treeReaped {
		// The local CLI may have exited while its owned VM still exists.
		// Preserve the active-job fence until startup cleanup confirms removal.
		record.State = CommandRuntimeJobStopping
		record.ExitCode = nil
		record.CompletedAt = nil
	}
	record.StdinClosed = true
	record.TimedOut = record.State == CommandRuntimeJobTimedOut
	record.Cancelled = record.State == CommandRuntimeJobCancelled
	record.Killed = record.State == CommandRuntimeJobKilled
	record.OutputCursor = entry.ring.next
	record.OutputBaseCursor = entry.ring.base
	record.OutputFramesJSON = entry.ring.json()
	record.StdoutSHA256 = hex.EncodeToString(entry.stdoutHash.Sum(nil))
	record.StderrSHA256 = hex.EncodeToString(entry.stderrHash.Sum(nil))
	entry.mu.Unlock()
	var persistErr error
	var updated CommandRuntimeJob
	for attempt := 0; attempt < 3; attempt++ {
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		updated, persistErr = m.store.UpdateCommandRuntimeJob(persistCtx, record, previous)
		cancel()
		if persistErr == nil {
			break
		}
	}
	if persistErr == nil {
		entry.mu.Lock()
		entry.record = updated
		if !treeReaped {
			entry.terminalErr = errors.Join(ErrCommandRuntimeUncertain, waitErr)
		}
		entry.mu.Unlock()
	} else {
		entry.mu.Lock()
		entry.record = record
		entry.terminalErr = errors.Join(ErrCommandRuntimeUncertain, persistErr)
		entry.mu.Unlock()
	}
	_ = entry.process.Close()
	entry.signal()
	close(entry.done)
	if persistErr == nil && treeReaped {
		m.removeEntry(record.ID, entry)
	}
}

func (m *CommandRuntimeManager) enforceTimeout(entry *commandRuntimeEntry,
	duration time.Duration,
) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-entry.done:
		return
	case <-timer.C:
		if entry.setDesired(CommandRuntimeJobTimedOut) {
			_ = entry.process.Kill()
		}
	}
}

func (m *CommandRuntimeManager) entry(jobID string) *commandRuntimeEntry {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	entry := m.entries[strings.TrimSpace(jobID)]
	m.mu.RUnlock()
	return entry
}

func (m *CommandRuntimeManager) removeEntry(jobID string, expected *commandRuntimeEntry) {
	m.mu.Lock()
	if m.entries[jobID] == expected {
		delete(m.entries, jobID)
	}
	m.mu.Unlock()
}

func (e *commandRuntimeEntry) snapshot() CommandRuntimeJobSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record.OutputCursor = e.ring.next
	e.record.OutputBaseCursor = e.ring.base
	return ProjectCommandRuntimeJob(e.record)
}

func (e *commandRuntimeEntry) lockInput(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.inputGate:
		return nil
	}
}

func (e *commandRuntimeEntry) unlockInput() {
	e.inputGate <- struct{}{}
}

func (e *commandRuntimeEntry) setDesired(state CommandRuntimeJobState) bool {
	e.mu.Lock()
	changed := false
	if !e.record.State.Terminal() && e.desired == "" {
		e.desired = state
		e.record.State = CommandRuntimeJobStopping
		changed = true
	}
	e.mu.Unlock()
	if changed {
		e.signal()
	}
	return changed
}

func (e *commandRuntimeEntry) persistError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.terminalErr
}

func (e *commandRuntimeEntry) observe(stream CommandRuntimeStream, count int) {
	e.mu.Lock()
	if stream == CommandRuntimeStdout {
		e.record.StdoutObservedBytes += int64(count)
	} else {
		e.record.StderrObservedBytes += int64(count)
	}
	e.mu.Unlock()
}

func (e *commandRuntimeEntry) appendOutput(stream CommandRuntimeStream,
	value string, at time.Time,
) {
	if value == "" {
		return
	}
	e.mu.Lock()
	e.ring.append(stream, value, at)
	if e.ring.base > 0 && e.record.TruncationReason == "" {
		e.record.TruncationReason = "inline_window"
	}
	if stream == CommandRuntimeStdout {
		_, _ = e.stdoutHash.Write([]byte(value))
	} else {
		_, _ = e.stderrHash.Write([]byte(value))
	}
	target := &e.record.Stdout
	if stream == CommandRuntimeStderr {
		target = &e.record.Stderr
	}
	remaining := e.record.ArtifactLimitBytes - len([]byte(*target))
	if remaining > 0 {
		data := []byte(value)
		if len(data) > remaining {
			data = data[:remaining]
			for len(data) > 0 && !utf8.Valid(data) {
				data = data[:len(data)-1]
			}
			e.record.TruncationReason = "artifact_limit"
		}
		*target += string(data)
	} else {
		e.record.TruncationReason = "artifact_limit"
	}
	e.record.OutputCursor = e.ring.next
	e.record.OutputBaseCursor = e.ring.base
	e.mu.Unlock()
	e.signal()
}

func (e *commandRuntimeEntry) readSnapshot(cursor uint64,
	maxBytes int,
) (CommandRuntimeJobSnapshot, CommandRuntimeOutputPage, error) {
	if maxBytes == 0 {
		maxBytes = MaxCommandRuntimeOutputRead
	}
	if maxBytes < MinCommandRuntimeOutputRead || maxBytes > MaxCommandRuntimeOutputRead {
		return CommandRuntimeJobSnapshot{}, CommandRuntimeOutputPage{},
			ErrCommandRuntimeBoundary
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record.OutputCursor = e.ring.next
	e.record.OutputBaseCursor = e.ring.base
	snapshot := ProjectCommandRuntimeJob(e.record)
	page := e.ring.read(cursor, maxBytes)
	page.JobID = e.record.ID
	page.State = e.record.State
	page.ExitCode = cloneInt(e.record.ExitCode)
	page.TruncationReason = e.record.TruncationReason
	return snapshot, page, nil
}

func (e *commandRuntimeEntry) signal() {
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

type commandRuntimeRing struct {
	base     uint64
	next     uint64
	bytes    int
	capacity int
	frames   []CommandRuntimeFrame
}

func (r *commandRuntimeRing) append(stream CommandRuntimeStream,
	value string, at time.Time,
) {
	if len(r.frames) > 0 && r.frames[len(r.frames)-1].Timestamp.After(at) {
		at = r.frames[len(r.frames)-1].Timestamp
	}
	for len(value) > 0 {
		take := len(value)
		if take > 16*1024 {
			take = 16 * 1024
			for take > 0 && !utf8.ValidString(value[:take]) {
				take--
			}
		}
		if take == 0 {
			value = value[1:]
			continue
		}
		part := value[:take]
		frame := CommandRuntimeFrame{Cursor: r.next,
			NextCursor: r.next + uint64(len([]byte(part))), Stream: stream,
			Timestamp: at, Text: part}
		r.next = frame.NextCursor
		r.frames = append(r.frames, frame)
		r.bytes += len([]byte(part))
		value = value[take:]
	}
	for (r.bytes > r.capacity || len(r.frames) > MaxCommandRuntimeFrames) &&
		len(r.frames) > 0 {
		overflow := r.bytes - r.capacity
		first := &r.frames[0]
		firstBytes := len([]byte(first.Text))
		if overflow > 0 && overflow < firstBytes {
			data := []byte(first.Text)
			cut := overflow
			for cut < len(data) && !utf8.Valid(data[cut:]) {
				cut++
			}
			first.Text = string(data[cut:])
			first.Cursor += uint64(cut)
			r.bytes -= cut
			r.base = first.Cursor
			break
		}
		r.bytes -= firstBytes
		r.base = first.NextCursor
		r.frames = r.frames[1:]
	}
}

func (r *commandRuntimeRing) read(cursor uint64,
	maxBytes int,
) CommandRuntimeOutputPage {
	dropped := cursor < r.base
	if cursor < r.base {
		cursor = r.base
	}
	if cursor > r.next {
		cursor = r.next
	}
	page := CommandRuntimeOutputPage{BaseCursor: r.base, NextCursor: cursor,
		EndCursor: r.next, Dropped: dropped, Frames: make([]CommandRuntimeFrame, 0)}
	remaining := maxBytes
	for _, frame := range r.frames {
		if frame.NextCursor <= cursor || remaining <= 0 {
			continue
		}
		copyFrame := frame
		if cursor > copyFrame.Cursor {
			cut := int(cursor - copyFrame.Cursor)
			data := []byte(copyFrame.Text)
			if cut > len(data) {
				continue
			}
			for cut < len(data) && !utf8.Valid(data[cut:]) {
				cut++
			}
			copyFrame.Text = string(data[cut:])
			copyFrame.Cursor += uint64(cut)
		}
		data := []byte(copyFrame.Text)
		if len(data) > remaining {
			data = data[:remaining]
			for len(data) > 0 && !utf8.Valid(data) {
				data = data[:len(data)-1]
			}
			copyFrame.Text = string(data)
			copyFrame.NextCursor = copyFrame.Cursor + uint64(len(data))
		}
		if copyFrame.Text == "" {
			break
		}
		page.Frames = append(page.Frames, copyFrame)
		used := len([]byte(copyFrame.Text))
		remaining -= used
		page.NextCursor = copyFrame.NextCursor
		cursor = copyFrame.NextCursor
	}
	return page
}

func (r *commandRuntimeRing) json() string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(r.frames); err != nil {
		return "[]"
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	if len(encoded) > MaxCommandRuntimeStoredFrames {
		return "[]"
	}
	return string(encoded)
}

func pageFromStoredJob(job CommandRuntimeJob, cursor uint64,
	maxBytes int,
) CommandRuntimeOutputPage {
	if maxBytes == 0 {
		maxBytes = MaxCommandRuntimeOutputRead
	}
	ring := commandRuntimeRing{capacity: max(job.InlineLimitBytes,
		len([]byte(job.Stdout))+len([]byte(job.Stderr))), base: job.OutputBaseCursor,
		next: job.OutputCursor}
	_ = json.Unmarshal([]byte(job.OutputFramesJSON), &ring.frames)
	for _, frame := range ring.frames {
		ring.bytes += len([]byte(frame.Text))
	}
	page := ring.read(cursor, maxBytes)
	page.JobID = job.ID
	page.State = job.State
	page.ExitCode = cloneInt(job.ExitCode)
	page.TruncationReason = job.TruncationReason
	return page
}

func commandRuntimeDigest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:", len([]byte(part)))
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{'|'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func jsonValid(value string) bool {
	var target any
	return jsonUnmarshal([]byte(value), &target) == nil
}

func validCommandRuntimeDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// Small indirections keep the runtime model free of an exported JSON helper
// surface while still making validation explicit and testable.
var jsonUnmarshal = func(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
