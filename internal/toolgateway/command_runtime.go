package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/outputsafe"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
)

const (
	CommandRuntimeToolProtocolVersion = "command-runtime.v2"
	CommandRuntimeActionRun           = "run"
	CommandRuntimeActionStart         = "start"
	CommandRuntimeActionList          = "list"
	CommandRuntimeActionRead          = "read"
	CommandRuntimeActionWait          = "wait"
	CommandRuntimeActionWriteStdin    = "write_stdin"
	CommandRuntimeActionCancel        = "cancel"
	CommandRuntimeActionKill          = "kill"
	CommandRuntimeFailFast            = "fail_fast"
	CommandRuntimeContinue            = "continue"
	MaxCommandRuntimeBatch            = 4
	MaxCommandRuntimeResultJobs       = 32
	MaxCommandRuntimeForegroundMillis = 25_000
	MaxCommandRuntimePageBytes        = 32 * 1024

	// CommandRuntimeRequestedByUIEvidenceOperator identifies the reviewed
	// control-bearer UI-evidence workflow. It is Run/root-authorized but does
	// not claim that a model Agent attempt requested the command.
	CommandRuntimeRequestedByUIEvidenceOperator = "ui_evidence_operator"
	// Direct operator work uses the existing operator_root audit attribution.
	CommandRuntimeRequestedByOperator = "command_operator"
)

type CommandRuntimeInput struct {
	ReviewScope      *CommandReviewScope         `json:"review_scope,omitempty"`
	Version          string                      `json:"version"`
	Action           string                      `json:"action"`
	Commands         []runner.CommandRuntimeSpec `json:"commands,omitempty"`
	FailurePolicy    string                      `json:"failure_policy,omitempty"`
	JobID            string                      `json:"job_id,omitempty"`
	Cursor           *uint64                     `json:"cursor,omitempty"`
	MaxBytes         *int                        `json:"max_bytes,omitempty"`
	WaitMilliseconds *int                        `json:"wait_milliseconds,omitempty"`
	Stdin            *string                     `json:"stdin,omitempty"`
	CloseStdin       *bool                       `json:"close_stdin,omitempty"`
}

func (i CommandRuntimeInput) Validate() error {
	if i.ReviewScope != nil {
		if (i.Action != CommandRuntimeActionRun && i.Action != CommandRuntimeActionStart) || len(i.Commands) != 1 {
			return errors.New("bounded review scope requires one exact command")
		}
		if _, err := i.ReviewScope.RiskScope(); err != nil {
			return err
		}
	}
	if i.Version != CommandRuntimeToolProtocolVersion ||
		!validCommandRuntimeAction(i.Action) {
		return errors.New("command runtime payload version or action is invalid")
	}
	if i.JobID != "" && (!domain.ValidAgentID(i.JobID) ||
		strings.TrimSpace(i.JobID) != i.JobID) {
		return errors.New("command runtime job id is invalid")
	}
	if i.Stdin != nil && (len([]byte(*i.Stdin)) > runner.MaxCommandRuntimeStdinBytes ||
		outputsafe.Sanitize([]byte(*i.Stdin)) != *i.Stdin) {
		return errors.New("command runtime stdin is invalid or secret-like")
	}
	if i.MaxBytes != nil && (*i.MaxBytes < runner.MinCommandRuntimeOutputRead ||
		*i.MaxBytes > MaxCommandRuntimePageBytes) {
		return errors.New("command runtime output page is invalid")
	}
	if i.WaitMilliseconds != nil && (*i.WaitMilliseconds < 0 ||
		*i.WaitMilliseconds > int(runner.MaxCommandRuntimeWait.Milliseconds())) {
		return errors.New("command runtime wait is invalid")
	}
	for _, command := range i.Commands {
		if command.Version != runner.CommandRuntimeProtocolVersion ||
			!command.Profile.Valid() || !command.Network.Valid() ||
			command.Credentials != runner.CommandRuntimeCredentialsNone ||
			command.TimeoutMilliseconds < 1 ||
			command.TimeoutMilliseconds > runner.MaxCommandRuntimeTimeout.Milliseconds() {
			return errors.New("command runtime command contract is invalid")
		}
	}
	switch i.Action {
	case CommandRuntimeActionRun:
		if len(i.Commands) < 1 || len(i.Commands) > MaxCommandRuntimeBatch ||
			(i.FailurePolicy != CommandRuntimeFailFast &&
				i.FailurePolicy != CommandRuntimeContinue) ||
			i.MaxBytes == nil || i.JobID != "" || i.Cursor != nil ||
			i.WaitMilliseconds != nil || i.Stdin != nil || i.CloseStdin != nil {
			return errors.New("command runtime foreground batch fields are invalid")
		}
		total := int64(0)
		for _, command := range i.Commands {
			total += command.TimeoutMilliseconds
		}
		if total > MaxCommandRuntimeForegroundMillis {
			return fmt.Errorf("command runtime action=run foreground timeout sum %dms exceeds %dms; use action=start for a longer command, then action=read or action=wait with the returned original job_id; do not submit the same command again", total, MaxCommandRuntimeForegroundMillis)
		}
	case CommandRuntimeActionStart:
		if len(i.Commands) != 1 || i.FailurePolicy != "" || i.JobID != "" ||
			i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds != nil ||
			i.Stdin != nil || i.CloseStdin != nil {
			return errors.New("command runtime background start fields are invalid")
		}
	case CommandRuntimeActionList:
		if len(i.Commands) != 0 || i.FailurePolicy != "" || i.JobID != "" ||
			i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds != nil ||
			i.Stdin != nil || i.CloseStdin != nil {
			return errors.New("command runtime list fields are invalid")
		}
	case CommandRuntimeActionRead, CommandRuntimeActionWait:
		if len(i.Commands) != 0 || i.FailurePolicy != "" || i.JobID == "" ||
			i.Cursor == nil || i.MaxBytes == nil || i.WaitMilliseconds == nil ||
			i.Stdin != nil || i.CloseStdin != nil ||
			(i.Action == CommandRuntimeActionRead && *i.WaitMilliseconds != 0) ||
			(i.Action == CommandRuntimeActionWait && *i.WaitMilliseconds == 0) {
			return errors.New("command runtime output read fields are invalid")
		}
	case CommandRuntimeActionWriteStdin:
		if len(i.Commands) != 0 || i.FailurePolicy != "" || i.JobID == "" ||
			i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds != nil ||
			i.Stdin == nil || i.CloseStdin == nil ||
			(*i.Stdin == "" && !*i.CloseStdin) {
			return errors.New("command runtime stdin fields are invalid")
		}
	case CommandRuntimeActionCancel:
		if len(i.Commands) != 0 || i.FailurePolicy != "" || i.JobID == "" ||
			i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds == nil ||
			i.Stdin != nil || i.CloseStdin != nil {
			return errors.New("command runtime cancel fields are invalid")
		}
	case CommandRuntimeActionKill:
		if len(i.Commands) != 0 || i.FailurePolicy != "" || i.JobID == "" ||
			i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds != nil ||
			i.Stdin != nil || i.CloseStdin != nil {
			return errors.New("command runtime kill fields are invalid")
		}
	}
	return nil
}

type CommandRuntimeContext struct {
	ProjectTest            bool // Native run_tests dispatch; never accepted from a model payload.
	InvocationID           string
	OperationKey           string
	RunID                  string
	MissionID              string
	RootAgentID            string
	AgentID                string
	AgentAttemptID         string
	SessionID              string
	WorkspaceID            string
	Surface                domain.ExecutionSurface
	Phase                  domain.ExecutionPhase
	Role                   domain.AgentRole
	Profile                domain.Profile
	PermissionMode         domain.RunExecutionPermissionMode
	ModeRevision           int64
	PermissionRevision     int64
	CapabilityGeneration   string
	PermissionSnapshotID   string
	PermissionGeneration   uint64
	PermissionRuntimeEpoch string
	RunAuthorizationFence  uint64
	SupervisorToolCallID   string
	LeaseID                string
	LeaseGeneration        int64
	RequestedBy            string
	PolicyDecision         Decision
	Adapter                commandruntimeadapter.Identity
}

func (c CommandRuntimeContext) Validate() error {
	if c.ProjectTest && (!c.PermissionMode.IsApprovalMode() || c.RequestedBy != "run_supervisor") {
		return errors.New("project tests require current approval authority")
	}
	for _, value := range []string{c.InvocationID, c.OperationKey, c.RunID,
		c.MissionID, c.RootAgentID, c.AgentID,
		c.SessionID, c.WorkspaceID, c.LeaseID,
		c.RequestedBy} {
		if value == "" || strings.TrimSpace(value) != value ||
			!utf8.ValidString(value) || len([]rune(value)) > MaxToolIdentityRunes {
			return errors.New("command runtime requires normalized bounded identities")
		}
	}
	attribution := c.Attribution()
	if !domain.ValidAgentID(c.RootAgentID) || attribution.Validate() != nil ||
		(attribution.Source == domain.AgentAttributionOperatorRoot &&
			attribution.AgentID != c.RootAgentID) ||
		!validAgentCodeDigest(c.CapabilityGeneration, false) || c.LeaseGeneration <= 0 ||
		!c.Adapter.Executable() ||
		c.PolicyDecision.Validate() != nil ||
		!c.PolicyDecision.Allowed || c.PolicyDecision.Approval != ApprovalAutomatic {
		return errors.New("command runtime requires an automatically authorized fenced root scope")
	}
	// Gateway-originated command runtime calls carry the already-published
	// workspace-affecting authority tuple. Direct internal recovery callers may
	// omit the tuple, but a partial tuple is always invalid.
	hasAuthority := c.ModeRevision != 0 || c.PermissionRevision != 0 ||
		c.Surface != "" || c.Phase != "" || c.Role != "" || c.Profile != "" ||
		c.PermissionMode != ""
	if hasAuthority && (!c.Surface.Valid() || !c.Phase.Valid() ||
		!domain.ValidAgentRole(c.Role) || c.Role != domain.AgentRoleRoot ||
		c.Profile != domain.ProfileCode || !c.PermissionMode.Valid() ||
		c.ModeRevision <= 0 || c.PermissionRevision <= 0) {
		return errors.New("command runtime authority tuple is invalid or partial")
	}
	if c.PermissionMode.IsApprovalMode() {
		if !domain.ValidAgentID(c.PermissionSnapshotID) || !commandruntimeadapter.ValidRuntimeBinding(c.PermissionMode, c.PermissionRuntimeEpoch, c.PermissionGeneration, c.RunAuthorizationFence) {
			return errors.New("command runtime operation authority is invalid or partial")
		}
	} else if c.PermissionSnapshotID != "" || c.PermissionGeneration != 0 ||
		c.PermissionRuntimeEpoch != "" || c.RunAuthorizationFence != 0 {
		if !domain.ValidAgentID(c.PermissionSnapshotID) ||
			c.PermissionGeneration == 0 || c.PermissionRuntimeEpoch == "" || c.RunAuthorizationFence != 0 ||
			!c.PermissionMode.IncludesFullAccess() {
			return errors.New("command runtime live Full Access authority is invalid or partial")
		}
	}
	return nil
}

// Attribution returns the exact durable actor semantics for this command.
// Model tool calls require a real Agent attempt. The operator-reviewed
// UI-evidence path carries only the root authority anchor and deliberately has
// no AgentAttemptID.
func (c CommandRuntimeContext) Attribution() domain.AgentAttribution {
	switch c.RequestedBy {
	case "run_supervisor":
		return domain.AgentAttribution{AgentID: c.AgentID,
			AgentAttemptID: c.AgentAttemptID, Source: domain.AgentAttributionRecorded}
	case CommandRuntimeRequestedByUIEvidenceOperator, CommandRuntimeRequestedByOperator:
		return domain.AgentAttribution{AgentID: c.AgentID,
			AgentAttemptID: c.AgentAttemptID, Source: domain.AgentAttributionOperatorRoot}
	default:
		return domain.AgentAttribution{AgentID: c.AgentID,
			AgentAttemptID: c.AgentAttemptID}
	}
}

type CommandRuntimeArtifactOutput struct {
	JobID  string
	Stdout string
	Stderr string
}

type CommandRuntimeExecutionResult struct {
	Backend           string
	Adapter           commandruntimeadapter.Identity
	Action            string
	Jobs              []runner.CommandRuntimeJobSnapshot
	Pages             []runner.CommandRuntimeOutputPage
	Artifacts         []CommandRuntimeArtifactOutput
	Replayed          bool
	IncompleteReasons []string
}

func (r CommandRuntimeExecutionResult) Validate() error {
	if !r.Adapter.Executable() || r.Backend != r.Adapter.Backend {
		return errors.New("command runtime adapter receipt is invalid")
	}
	if strings.TrimSpace(r.Backend) == "" || strings.TrimSpace(r.Backend) != r.Backend ||
		len([]rune(r.Backend)) > MaxExecutionBackendRunes ||
		!validCommandRuntimeAction(r.Action) || len(r.Jobs) > MaxCommandRuntimeResultJobs ||
		len(r.Pages) > MaxCommandRuntimeBatch || len(r.Artifacts) > MaxCommandRuntimeBatch ||
		len(r.IncompleteReasons) > 16 {
		return errors.New("command runtime execution result is invalid")
	}
	for _, job := range r.Jobs {
		if !domain.ValidAgentID(job.ID) || !job.State.Valid() ||
			job.OutputBaseCursor > job.OutputCursor || job.Adapter.Validate() != nil {
			return errors.New("command runtime job result is invalid")
		}
	}
	for _, page := range r.Pages {
		if !domain.ValidAgentID(page.JobID) || page.BaseCursor > page.NextCursor ||
			page.NextCursor > page.EndCursor || !page.State.Valid() {
			return errors.New("command runtime output page is invalid")
		}
		for _, frame := range page.Frames {
			if frame.Cursor > frame.NextCursor || frame.NextCursor > page.EndCursor ||
				(frame.Stream != runner.CommandRuntimeStdout &&
					frame.Stream != runner.CommandRuntimeStderr) ||
				frame.Timestamp.IsZero() || !utf8.ValidString(frame.Text) {
				return errors.New("command runtime output frame is invalid")
			}
		}
	}
	for _, output := range r.Artifacts {
		if !domain.ValidAgentID(output.JobID) ||
			len([]byte(output.Stdout)) > artifact.MaxContentBytes ||
			len([]byte(output.Stderr)) > artifact.MaxContentBytes ||
			!utf8.ValidString(output.Stdout) || !utf8.ValidString(output.Stderr) {
			return errors.New("command runtime artifact output is invalid")
		}
	}
	for _, reason := range r.IncompleteReasons {
		if reason == "" || reason != strings.TrimSpace(reason) || !utf8.ValidString(reason) ||
			len([]rune(reason)) > 512 || strings.ContainsRune(reason, 0) {
			return errors.New("command runtime incomplete reason is invalid")
		}
	}
	return nil
}

func (r CommandRuntimeExecutionResult) ValidateBoundAdapter() error {
	return r.Validate()
}

type CommandRuntimeExecutor interface {
	ExecuteCommandRuntime(context.Context, CommandRuntimeContext,
		CommandRuntimeInput) (CommandRuntimeExecutionResult, error)
}

// CommandRuntimeAdvertiser returns the process-local adapter identity that may
// be advertised for the current Run permission. The resulting authority is
// durable evidence, not a capability that recovery data may widen.
type CommandRuntimeAdvertiser interface {
	AdvertisedCommandRuntimeAdapter(context.Context, string,
		domain.RunExecutionPermissionMode) (commandruntimeadapter.Identity, bool, error)
}

var commandRuntimeTimeoutGuidance = fmt.Sprintf("action=run is a foreground batch: the SUM of timeout_milliseconds across ALL commands must be <= %dms, not %dms per command. For a longer command, use action=start (exactly one command, timeout_milliseconds <= %dms), then action=read or action=wait using the returned original job_id and cursor. Do not rerun/restart the command to retrieve its output; wait_milliseconds limits each observation wait, not the Job timeout.", MaxCommandRuntimeForegroundMillis, MaxCommandRuntimeForegroundMillis, runner.MaxCommandRuntimeTimeout.Milliseconds())

var commandRuntimeDefinition = ToolDefinition{
	Name: CommandRuntimeTool, Class: ClassProcess, Approval: ApprovalAutomatic,
	Description: "Run an ordered command-runtime.v2 batch or manage one Run-owned background Job through the adapter selected by current Run authority; the model cannot select or override that adapter. Every command declares a fixed PowerShell/Bash/process profile, literal argv or script, workspace-relative cwd, restricted environment, stdin lifecycle, timeout, bounded output, disabled network intent, and no credentials. Output is untrusted, sanitized, cursor-addressed evidence; each Job belongs to its Run and is separate from the interactive user terminal. candidate_urls are unverified loopback addresses extracted from the returned sanitized output pages, bound to their job_id; they grant no navigation permission or readiness proof. Read/wait the original Job for new output. " + commandRuntimeTimeoutGuidance,
	InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["version","action"],"properties":{"version":{"const":"command-runtime.v2"},"action":{"description":%s,"enum":["run","start","list","read","wait","write_stdin","cancel","kill"]},"commands":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["version","profile","working_directory","environment","stdin_policy","close_initial_stdin","timeout_milliseconds","output","network","credentials","purpose"],"properties":{"version":{"const":"command-runtime.v2"},"profile":{"description":"process accepts an absolute native development runtime such as Node/Python or another allowed native executable plus literal arguments; omit script. Shells, system script hosts, script files and blocked launchers are rejected as process executables. For shell syntax use script with powershell or bash only when supported by the current adapter, and omit executable and arguments. The executable remains outside the Workspace and pinned by SHA-256; a listed profile does not grant runtime availability or permission.","enum":["powershell","bash","process"]},"executable":{"type":"string","maxLength":4096},"arguments":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":16384}},"script":{"type":"string","maxLength":65536},"working_directory":{"type":"string","minLength":1,"maxLength":4096},"environment":{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["name","value"],"properties":{"name":{"type":"string","minLength":1,"maxLength":128},"value":{"type":"string","maxLength":65536}}}},"stdin_policy":{"enum":["closed","pipe"]},"initial_stdin":{"type":"string","maxLength":65536},"close_initial_stdin":{"type":"boolean"},"timeout_milliseconds":{"description":%s,"type":"integer","minimum":1,"maximum":%d},"output":{"type":"object","additionalProperties":false,"required":["inline_bytes","artifact_bytes"],"properties":{"inline_bytes":{"type":"integer","minimum":4096,"maximum":524288},"artifact_bytes":{"type":"integer","minimum":4096,"maximum":4194304}}},"network":{"const":"disabled"},"credentials":{"const":"none"},"purpose":{"type":"string","minLength":1,"maxLength":1200}}}},"review_scope":{"type":"object","additionalProperties":false,"required":["risk_kinds"],"description":"Optional purpose/risk grouping for bounded Run review. Only one run/start command. Every exact new command requires operator review; this metadata grants no authority or isolation.","properties":{"risk_kinds":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"enum":["network","credential","host_path","policy_denial","non_whitelisted_tool","other_high_risk"]}},"network_targets":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":512}},"credential_kinds":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":512}},"host_paths":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":512}},"network_purpose":{"type":"string","maxLength":1200},"policy_code":{"type":"string","maxLength":1200},"policy_reason":{"type":"string","maxLength":1200},"requested_tool":{"type":"string","maxLength":1200},"other_risk_reason":{"type":"string","maxLength":1200}}},"failure_policy":{"enum":["fail_fast","continue"]},"job_id":{"type":"string","minLength":1,"maxLength":256},"cursor":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":4,"maximum":32768},"wait_milliseconds":{"type":"integer","minimum":0,"maximum":5000},"stdin":{"type":"string","maxLength":65536},"close_stdin":{"type":"boolean"}},"allOf":[{"if":{"required":["review_scope"]},"then":{"properties":{"action":{"enum":["run","start"]},"commands":{"maxItems":1}}}},{"if":{"properties":{"action":{"const":"run"}}},"then":{"required":["commands","failure_policy","max_bytes"],"properties":{"commands":{"items":{"properties":{"timeout_milliseconds":{"maximum":%d}}}}}}},{"if":{"properties":{"action":{"const":"start"}}},"then":{"required":["commands"],"properties":{"commands":{"maxItems":1}}}},{"if":{"properties":{"action":{"enum":["read","wait"]}}},"then":{"required":["job_id","cursor","max_bytes","wait_milliseconds"]}},{"if":{"properties":{"action":{"const":"write_stdin"}}},"then":{"required":["job_id","stdin","close_stdin"]}},{"if":{"properties":{"action":{"const":"cancel"}}},"then":{"required":["job_id","wait_milliseconds"]}},{"if":{"properties":{"action":{"const":"kill"}}},"then":{"required":["job_id"]}}]}`, strconv.Quote(fmt.Sprintf("run executes a foreground batch within %dms total; start creates one background Job; read/wait observe that original Job.", MaxCommandRuntimeForegroundMillis)), strconv.Quote(fmt.Sprintf("Milliseconds for this command. run requires the batch sum <= %dms; start permits one command up to %dms.", MaxCommandRuntimeForegroundMillis, runner.MaxCommandRuntimeTimeout.Milliseconds())), runner.MaxCommandRuntimeTimeout.Milliseconds(), MaxCommandRuntimeForegroundMillis)),
}

// The host adapter already has host networking. Advertise that fact only to
// a Run whose host authority selected this adapter. Per-operation policy and
// exact review still apply; Workspace Sandbox retains denied networking.
func CommandRuntimeDefinitionForAdapter(adapter commandruntimeadapter.Identity) ToolDefinition {
	if adapter.Kind != commandruntimeadapter.KindHostUnsandboxed {
		return commandRuntimeDefinition
	}
	definition := commandRuntimeDefinition
	oldNetwork := []byte(`"network":{"const":"disabled"}`)
	if bytes.Count(definition.InputSchema, oldNetwork) != 1 {
		panic("command runtime network schema marker is unavailable")
	}
	definition.InputSchema = bytes.Replace(definition.InputSchema, oldNetwork,
		[]byte(`"network":{"const":"host"}`), 1)
	definition.Description = strings.Replace(definition.Description,
		"disabled network intent, and no credentials",
		"host network intent without a destination allowlist, and no product-injected credentials", 1)
	return definition
}

func validCommandRuntimeAction(value string) bool {
	switch value {
	case CommandRuntimeActionRun, CommandRuntimeActionStart, CommandRuntimeActionList,
		CommandRuntimeActionRead, CommandRuntimeActionWait,
		CommandRuntimeActionWriteStdin, CommandRuntimeActionCancel,
		CommandRuntimeActionKill:
		return true
	default:
		return false
	}
}

func normalizeCommandRuntimePayload(payload json.RawMessage) (
	CommandRuntimeInput, json.RawMessage, error,
) {
	input, err := decodeStructuredPayload[CommandRuntimeInput](payload)
	if err != nil {
		return CommandRuntimeInput{}, nil, err
	}
	fields, err := structuredPayloadFields(payload)
	if err != nil {
		return CommandRuntimeInput{}, nil, err
	}
	input.Version = strings.TrimSpace(input.Version)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.FailurePolicy = strings.ToLower(strings.TrimSpace(input.FailurePolicy))
	input.JobID = strings.TrimSpace(input.JobID)
	if input.ReviewScope != nil {
		normalized, err := input.ReviewScope.normalized()
		if err != nil {
			return CommandRuntimeInput{}, nil, err
		}
		input.ReviewScope = &normalized
	}
	for index := range input.Commands {
		normalized, normalizeErr := runner.NormalizeCommandRuntimeIntent(input.Commands[index])
		if normalizeErr != nil {
			return CommandRuntimeInput{}, nil, normalizeErr
		}
		input.Commands[index] = normalized
	}
	if err := validateCommandRuntimePayloadShape(fields, input); err != nil {
		return CommandRuntimeInput{}, nil, err
	}
	if err := input.Validate(); err != nil {
		return CommandRuntimeInput{}, nil, err
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}

func validateCommandRuntimePayloadShape(fields map[string]json.RawMessage,
	input CommandRuntimeInput,
) error {
	require := func(names ...string) bool {
		for _, name := range names {
			present, nonNull := structuredPayloadField(fields, name)
			if !present || !nonNull {
				return false
			}
		}
		return true
	}
	allowed := map[string]struct{}{"version": {}, "action": {}}
	allow := func(names ...string) {
		for _, name := range names {
			allowed[name] = struct{}{}
		}
	}
	if !require("version", "action") {
		return errors.New("command runtime version and action fields are required")
	}
	switch input.Action {
	case CommandRuntimeActionRun:
		allow("commands", "failure_policy", "max_bytes", "review_scope")
		if !require("commands", "failure_policy", "max_bytes") {
			return errors.New("command runtime foreground fields are required")
		}
	case CommandRuntimeActionStart:
		allow("commands", "review_scope")
		if !require("commands") {
			return errors.New("command runtime start fields are required")
		}
	case CommandRuntimeActionList:
	case CommandRuntimeActionRead, CommandRuntimeActionWait:
		allow("job_id", "cursor", "max_bytes", "wait_milliseconds")
		if !require("job_id", "cursor", "max_bytes", "wait_milliseconds") {
			return errors.New("command runtime output fields are required")
		}
	case CommandRuntimeActionWriteStdin:
		allow("job_id", "stdin", "close_stdin")
		if !require("job_id", "stdin", "close_stdin") {
			return errors.New("command runtime stdin fields are required")
		}
	case CommandRuntimeActionCancel:
		allow("job_id", "wait_milliseconds")
		if !require("job_id", "wait_milliseconds") {
			return errors.New("command runtime cancel fields are required")
		}
	case CommandRuntimeActionKill:
		allow("job_id")
		if !require("job_id") {
			return errors.New("command runtime kill fields are required")
		}
	default:
		return errors.New("command runtime action is invalid")
	}
	for name := range fields {
		if _, found := allowed[name]; !found {
			return errors.New("command runtime action contains an inapplicable field")
		}
	}
	if present, nonNull := structuredPayloadField(fields, "review_scope"); present && !nonNull {
		return errors.New("command review scope cannot be null")
	}
	if input.Action != CommandRuntimeActionRun && input.Action != CommandRuntimeActionStart {
		return nil
	}
	var commands []map[string]json.RawMessage
	if err := json.Unmarshal(fields["commands"], &commands); err != nil || commands == nil ||
		len(commands) != len(input.Commands) {
		return errors.New("command runtime commands must be an explicit JSON array")
	}
	for index, command := range commands {
		if err := validateCommandRuntimeCommandShape(command, input.Commands[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateCommandRuntimeCommandShape(fields map[string]json.RawMessage,
	command runner.CommandRuntimeSpec,
) error {
	for _, name := range []string{"version", "profile", "working_directory", "environment",
		"stdin_policy", "close_initial_stdin", "timeout_milliseconds", "output",
		"network", "credentials", "purpose"} {
		present, nonNull := structuredPayloadField(fields, name)
		if !present || !nonNull {
			return errors.New("command runtime command omitted an explicit boundary field")
		}
	}
	if present, nonNull := structuredPayloadField(fields, "initial_stdin"); present && !nonNull {
		return errors.New("command runtime initial stdin cannot be null")
	}
	switch command.Profile {
	case runner.CommandRuntimePowerShell, runner.CommandRuntimeBash:
		scriptPresent, scriptNonNull := structuredPayloadField(fields, "script")
		executablePresent, _ := structuredPayloadField(fields, "executable")
		argumentsPresent, _ := structuredPayloadField(fields, "arguments")
		if !scriptPresent || !scriptNonNull || executablePresent || argumentsPresent {
			return errors.New("command runtime shell command shape is invalid")
		}
	case runner.CommandRuntimeProcess:
		executablePresent, executableNonNull := structuredPayloadField(fields, "executable")
		argumentsPresent, argumentsNonNull := structuredPayloadField(fields, "arguments")
		scriptPresent, _ := structuredPayloadField(fields, "script")
		if !executablePresent || !executableNonNull || !argumentsPresent ||
			!argumentsNonNull || scriptPresent {
			return errors.New("command runtime process command shape is invalid")
		}
	}
	var environment []runner.CommandRuntimeEnvironment
	if err := json.Unmarshal(fields["environment"], &environment); err != nil || environment == nil {
		return errors.New("command runtime environment must be an explicit JSON array")
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(fields["output"], &output); err != nil || len(output) != 2 {
		return errors.New("command runtime output policy is invalid")
	}
	for _, name := range []string{"inline_bytes", "artifact_bytes"} {
		present, nonNull := structuredPayloadField(output, name)
		if !present || !nonNull {
			return errors.New("command runtime output policy omitted a bound")
		}
	}
	return nil
}

func (g *Gateway) WithCommandRuntimeExecutor(executor CommandRuntimeExecutor) *Gateway {
	if g != nil {
		g.commandRuntime = executor
		if host, ok := executor.(interface{ SetCommandRuntimePolicy(policy.Checker) }); ok {
			host.SetCommandRuntimePolicy(g.checker)
		}
	}
	return g
}

func (g *Gateway) invokeCommandRuntime(ctx context.Context, call ToolCall) (
	Outcome, error,
) {
	input, canonical, err := normalizeCommandRuntimePayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	return g.invokePreparedCommandRuntime(ctx, call, input)
}

func (g *Gateway) invokePreparedCommandRuntime(ctx context.Context, call ToolCall, input CommandRuntimeInput) (Outcome, error) {
	policyDecision := policy.Decision{Allowed: true,
		Reason: "bounded command runtime lifecycle operation allowed", Risk: "low"}
	if input.Action == CommandRuntimeActionRun || input.Action == CommandRuntimeActionStart {
		allAllowed := true
		for index, command := range input.Commands {
			commandDecision := policy.Decision{}
			if command.Network == runner.CommandRuntimeNetworkHost &&
				(call.CommandRuntimeAdapter.Kind != commandruntimeadapter.KindHostUnsandboxed ||
					!call.CommandRuntimeAdapter.AllowsPermission(call.PermissionMode)) {
				commandDecision = policy.Decision{Allowed: false, Risk: "high",
					Reason: "host network requires an available host adapter and operation authorization"}
			} else {
				commandDecision = CommandRuntimeCommandPolicy(g.checker, command)
				if commandDecision.NeedsApproval && (!call.PermissionMode.IsApprovalMode() || call.SupervisorToolCallID == "") {
					commandDecision.Allowed = false
					commandDecision.Reason = "command runtime cannot bypass a required per-command review: " + commandDecision.Reason
				}
			}
			if err := g.recordCommandRuntimePolicyDecision(ctx, call, index,
				commandDecision); err != nil {
				return Outcome{}, err
			}
			if !commandDecision.Allowed && allAllowed {
				policyDecision = commandDecision
			}
			allAllowed = allAllowed && commandDecision.Allowed
		}
		if allAllowed {
			policyDecision = policy.Decision{Allowed: true, Risk: "high",
				Reason: "every command passed exact canonical policy review"}
		}
	}
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "high")
	if err != nil {
		return Outcome{}, err
	}
	scope := CommandRuntimeContext{ProjectTest: call.Name == RunTestsTool, InvocationID: call.InvocationID,
		OperationKey: call.OperationKey, RunID: call.RunID, MissionID: call.MissionID,
		RootAgentID: call.AgentID, AgentID: call.AgentID,
		AgentAttemptID: call.AgentAttemptID,
		SessionID:      call.SessionID, WorkspaceID: call.WorkspaceID,
		Surface: call.Surface, Phase: call.Phase, Role: call.Role,
		Profile: call.Profile, PermissionMode: call.PermissionMode,
		ModeRevision: call.ModeRevision, PermissionRevision: call.PermissionRevision,
		CapabilityGeneration:   call.CapabilityGeneration,
		PermissionSnapshotID:   call.PermissionSnapshotID,
		PermissionGeneration:   call.PermissionGeneration,
		PermissionRuntimeEpoch: call.PermissionRuntimeEpoch,
		RunAuthorizationFence:  call.RunAuthorizationFence,
		SupervisorToolCallID:   call.SupervisorToolCallID,
		LeaseID:                call.LeaseID, LeaseGeneration: call.LeaseGeneration,
		RequestedBy: call.RequestedBy, PolicyDecision: decision,
		Adapter: call.CommandRuntimeAdapter}
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	result, err := g.commandRuntime.ExecuteCommandRuntime(ctx, scope, input)
	if err != nil {
		return Outcome{}, err
	}
	sanitizeCommandRuntimeExecutionResult(&result)
	if err := result.ValidateBoundAdapter(); err != nil ||
		!result.Adapter.SameBackend(scope.Adapter) {
		if err == nil {
			err = errors.New("command runtime adapter receipt does not match advertised authority")
		}
		return Outcome{}, err
	}
	completed := time.Now().UTC()
	projection := struct {
		Version           string                             `json:"version"`
		Action            string                             `json:"action"`
		Adapter           commandruntimeadapter.Identity     `json:"adapter"`
		Jobs              []runner.CommandRuntimeJobSnapshot `json:"jobs"`
		Pages             []runner.CommandRuntimeOutputPage  `json:"pages,omitempty"`
		CandidateURLs     []commandRuntimeURLCandidate       `json:"candidate_urls,omitempty"`
		IncompleteReasons []string                           `json:"incomplete_reasons,omitempty"`
	}{Version: runner.CommandRuntimeResultVersion, Action: result.Action,
		Adapter: result.Adapter, Jobs: result.Jobs, Pages: result.Pages,
		CandidateURLs:     commandRuntimeCandidateURLs(result),
		IncompleteReasons: result.IncompleteReasons}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return Outcome{}, err
	}
	stdout, truncated := boundResultText(outputsafe.Sanitize(encoded),
		MaxResultStdoutBytes)
	metadata := map[string]string{
		"backend": result.Backend, "action": result.Action,
		"adapter_kind":       string(result.Adapter.Kind),
		"backend_identity":   result.Adapter.BackendIdentity,
		"backend_generation": result.Adapter.Generation,
		"isolation_grade":    string(result.Adapter.IsolationGrade),
		"job_count":          strconv.Itoa(len(result.Jobs)),
		"replayed":           strconv.FormatBool(result.Replayed),
		"owner":              string(result.Adapter.Kind), "output_source": "untrusted_command_runtime",
		"network":     string(result.Adapter.NetworkPolicy),
		"credentials": string(result.Adapter.CredentialPolicy), "profile_files": "disabled",
		"user_terminal_shared": "false", "debug_terminal_shared": "false",
		"sandbox_shared": strconv.FormatBool(
			result.Adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace),
		"instruction_authorized": "false",
	}
	if len(result.IncompleteReasons) > 0 {
		metadata["incomplete_reason_count"] = strconv.Itoa(len(result.IncompleteReasons))
	}
	for index, output := range result.Artifacts {
		captured, captureErr := g.captureTerminalArtifacts(ctx, call, output.JobID,
			output.Stdout, output.Stderr, "text/plain; charset=utf-8")
		if captureErr != nil {
			return Outcome{}, captureErr
		}
		prefix := "job_" + strconv.Itoa(index+1) + "_"
		metadata[prefix+"id"] = output.JobID
		for key, value := range captured {
			if strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_sha256") {
				metadata[prefix+key] = value
			}
		}
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: result.Backend, Status: StatusCompleted,
			StartedAt: started, CompletedAt: &completed},
		Result: &Result{Status: StatusCompleted, Stdout: stdout, ExitCode: 0,
			MIME: "application/json", Truncated: truncated,
			Metadata: metadata, CompletedAt: completed}}
	return validateOutcome(outcome, nil)
}

type commandRuntimeURLCandidate struct {
	JobID string `json:"job_id"`
	runner.CommandServiceURLCandidate
}

func commandRuntimeCandidateURLs(result CommandRuntimeExecutionResult) []commandRuntimeURLCandidate {
	var candidates []commandRuntimeURLCandidate
	jobs := make(map[string]bool, len(result.Jobs))
	for _, job := range result.Jobs {
		jobs[job.ID] = true
	}
	seen := make(map[string]bool)
	for _, page := range result.Pages {
		if !jobs[page.JobID] {
			continue
		}
		for _, candidate := range runner.CommandServiceURLCandidates(page.Frames) {
			key := page.JobID + "\x00" + candidate.URL
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, commandRuntimeURLCandidate{JobID: page.JobID, CommandServiceURLCandidate: candidate})
			if len(candidates) == runner.MaxCommandServiceURLs {
				return candidates
			}
		}
	}
	return candidates
}

func sanitizeCommandRuntimeExecutionResult(result *CommandRuntimeExecutionResult) {
	if result == nil {
		return
	}
	for pageIndex := range result.Pages {
		for frameIndex := range result.Pages[pageIndex].Frames {
			frame := &result.Pages[pageIndex].Frames[frameIndex]
			frame.Text = outputsafe.Sanitize([]byte(frame.Text))
		}
	}
	for index := range result.Artifacts {
		result.Artifacts[index].Stdout = outputsafe.Sanitize(
			[]byte(result.Artifacts[index].Stdout))
		result.Artifacts[index].Stderr = outputsafe.Sanitize(
			[]byte(result.Artifacts[index].Stderr))
	}
}

func commandRuntimeNetworkViolation(spec runner.CommandRuntimeSpec) string {
	if spec.Network == runner.CommandRuntimeNetworkHost {
		return ""
	}
	value := strings.ToLower(spec.Script + " " + spec.Executable + " " +
		strings.Join(spec.Arguments, " "))
	base := strings.ToLower(filepath.Base(spec.Executable))
	for _, executable := range []string{"curl", "curl.exe", "wget", "wget.exe",
		"ssh", "ssh.exe", "scp", "scp.exe", "sftp", "ftp", "nc", "netcat",
		"nmap", "telnet", "ping", "ping.exe"} {
		if base == executable {
			return "command runtime network declaration is disabled for this executable"
		}
	}
	for _, marker := range []string{"http://", "https://", "invoke-webrequest",
		"invoke-restmethod", "test-netconnection", "start-bitstransfer", "git clone",
		"git fetch", "git pull", "git push", "git ls-remote"} {
		if strings.Contains(value, marker) {
			return "command runtime detected network intent while network is declared disabled"
		}
	}
	return ""
}

func (g *Gateway) recordCommandRuntimePolicyDecision(ctx context.Context,
	call ToolCall, commandIndex int, decision policy.Decision,
) error {
	if g == nil || g.policyRecorder == nil {
		return errors.New("command runtime policy decision recorder is required")
	}
	return g.policyRecorder.RecordPolicyDecision(ctx, policy.DecisionRecord{
		SessionID: call.SessionID, SubjectID: call.InvocationID,
		Context:  "tool_run.command_runtime.command_" + strconv.Itoa(commandIndex+1),
		Decision: decision,
	})
}

func commandRuntimePointer[T any](value T) *T { return &value }
