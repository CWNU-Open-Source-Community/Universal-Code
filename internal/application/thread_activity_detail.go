package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

const (
	ThreadActivityDetailProtocolVersion  = "thread_activity_detail.v2"
	ThreadActivitySummaryProtocolVersion = "thread_activity_summary.v1"
	MaxThreadActivityCommands            = toolgateway.MaxCommandRuntimeResultJobs
	MaxThreadActivityCommandRunes        = 4096
	MaxThreadActivityOutputRunes         = 8192
)

var (
	windowsAbsolutePath = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^\s\r\n\t"'<>|]+`)
	fileURIAbsolutePath = regexp.MustCompile(`(?i)\bfile://(?:localhost)?/(?:[^\s\r\n\t"'<>|]+)`)
	unixAbsolutePath    = regexp.MustCompile(`(^|[\s\t"'=\(\[,])/(?:[^/\s\r\n\t"'<>|][^\s\r\n\t"'<>|]*)`)
)

type ThreadActivityDetailStore interface {
	GetThreadSupervisorToolCall(context.Context, string, string) (
		domain.SupervisorToolCall, error)
	GetThreadCommandRuntimeJob(context.Context, string, string) (
		runner.CommandRuntimeJob, error)
	GetThreadCommandRuntimeJobMetadata(context.Context, string, string) (
		runner.CommandRuntimeJobMetadata, error)
	GetThreadCommandRuntimeJobAgentAttribution(context.Context, string, string) (
		domain.AgentAttribution, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetRootAgent(context.Context, string) (domain.AgentNode, bool, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

// ThreadActivityDetail is the deliberately small public projection of one
// durable tool call. It never contains raw tool payloads, environment values,
// stdin, process identities, credentials, or absolute host paths.
type ThreadActivityDetail struct {
	Version     string
	ActivityRef string
	RunID       string
	Tools       []ThreadActivityToolDetail
}

// ThreadActivitySummary is the bounded collapsed-row projection. It contains
// enough execution fact to make the conversation legible without transferring
// stdout/stderr or any raw durable payload.
type ThreadActivitySummary struct {
	Version              string
	ActivityRef          string
	Command              string
	Status               string
	ExitCode             *int
	DurationMilliseconds int64
	CommandCount         int
}

type ThreadActivityToolDetail struct {
	Name                 string
	Label                string
	AgentID              string
	AgentRole            string
	AgentLabel           string
	Status               string
	StartedAt            time.Time
	CompletedAt          *time.Time
	DurationMilliseconds int64
	// Commands remains an internal compatibility reader for command projection
	// and artifact loading. The v2 HTTP contract emits only Detail.
	Commands []ThreadActivityCommandDetail
	Detail   ThreadActivityTypedDetail
}

type ThreadActivityCommandDetail struct {
	Command              string
	WorkingDirectory     string
	ExecutionEnvironment string
	Network              string
	Status               string
	ExitCode             *int
	DurationMilliseconds int64
	StdoutPreview        string
	StderrPreview        string
	Truncated            bool
	Artifacts            []ThreadActivityArtifactReference
}

type threadActivityAgentProjection struct {
	ID    string
	Role  string
	Label string
}

type ThreadActivityDetailService struct {
	store          ThreadActivityDetailStore
	commandRuntime ThreadActivityCommandRuntimeSource
}

func NewThreadActivityDetailService(store ThreadActivityDetailStore) *ThreadActivityDetailService {
	return &ThreadActivityDetailService{store: store}
}

func (s *ThreadActivityDetailService) Get(ctx context.Context, threadID,
	activityRef string,
) (ThreadActivityDetail, error) {
	if s == nil || s.store == nil || ctx == nil || ctx.Err() != nil {
		return ThreadActivityDetail{}, apperror.New(
			apperror.CodeFailedPrecondition, "Thread activity detail service is unavailable")
	}
	call, err := s.store.GetThreadSupervisorToolCall(ctx, threadID, activityRef)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Normalize(err)
	}
	if !SupportsThreadActivityDetail(call.ToolName) {
		return ThreadActivityDetail{}, apperror.New(
			apperror.CodeNotFound, "Thread activity detail was not found")
	}
	run, err := s.store.GetRun(ctx, call.RunID)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Normalize(err)
	}
	agent, err := s.threadActivityAgent(ctx, run, call)
	if err != nil {
		return ThreadActivityDetail{}, err
	}
	if call.ToolName != string(toolgateway.CommandRuntimeTool) {
		detail, found, projectionErr := ProjectThreadActivityTypedDetail(call)
		if projectionErr != nil {
			return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"durable Thread tool activity could not be safely projected", projectionErr)
		}
		if !found {
			return ThreadActivityDetail{}, apperror.New(
				apperror.CodeNotFound, "Thread activity detail was not found")
		}
		if projectionErr := s.enrichThreadActivityFileEdit(ctx, run, detail.FileEdit); projectionErr != nil {
			return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"durable Thread file-edit activity could not be safely projected", projectionErr)
		}
		tool := ThreadActivityToolDetail{
			Name: call.ToolName, Label: threadActivityToolLabel(toolgateway.ToolName(call.ToolName)),
			AgentID: agent.ID, AgentRole: agent.Role, AgentLabel: agent.Label,
			Status: string(call.Status), StartedAt: call.CreatedAt,
			CompletedAt: cloneActivityTime(call.CompletedAt),
			DurationMilliseconds: activityDuration(call.CreatedAt, call.CompletedAt,
				call.CreatedAt),
			Commands: []ThreadActivityCommandDetail{}, Detail: detail,
		}
		value := ThreadActivityDetail{Version: ThreadActivityDetailProtocolVersion,
			ActivityRef: activityRef, RunID: call.RunID,
			Tools: []ThreadActivityToolDetail{tool}}
		if validateErr := value.Validate(); validateErr != nil {
			return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"safe Thread activity detail projection is invalid", validateErr)
		}
		return value, nil
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Normalize(err)
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Normalize(err)
	}
	root, rootFound, err := s.store.GetRootAgent(ctx, run.ID)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Normalize(err)
	}
	if !rootFound || root.ID == "" || root.RunID != run.ID || root.ParentID != "" ||
		root.Role != domain.AgentRoleRoot || root.SessionID != run.SessionID {
		return ThreadActivityDetail{}, apperror.New(apperror.CodeFailedPrecondition,
			"durable Thread command authority anchor is invalid")
	}
	input, err := decodeThreadCommandActivityInput(call.PayloadJSON)
	if err != nil {
		return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Thread command activity could not be projected", err)
	}
	jobBindings := threadCommandActivityJobBindings(call, input)
	jobs := make([]runner.CommandRuntimeJob, 0, len(jobBindings))
	jobsByID := make(map[string]runner.CommandRuntimeJob, len(jobBindings))
	for _, binding := range jobBindings {
		job, loadErr := s.store.GetThreadCommandRuntimeJob(ctx, threadID, binding.ID)
		if loadErr != nil {
			// Foreground batches are sequential and fail-fast may intentionally
			// leave a deterministic tail of Jobs unmaterialized. A result-reported
			// or explicitly addressed Job remains required.
			if binding.Optional && apperror.CodeOf(loadErr) == apperror.CodeNotFound {
				continue
			}
			return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"durable Thread command Job could not be projected", loadErr)
		}
		if job.RunID != call.RunID || job.RootAgentID != root.ID {
			return ThreadActivityDetail{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable Thread command Job has an inconsistent Run or authority binding")
		}
		jobAttribution, attributionErr :=
			s.store.GetThreadCommandRuntimeJobAgentAttribution(ctx, threadID, job.ID)
		if attributionErr != nil {
			return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"durable Thread command Job Agent attribution could not be projected",
				attributionErr)
		}
		if !threadActivityAgentAttributionMatches(call, jobAttribution) {
			return ThreadActivityDetail{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable Thread command Job has an inconsistent executing Agent binding")
		}
		if binding.OperationDigest != "" && job.OperationDigest != binding.OperationDigest {
			return ThreadActivityDetail{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable Thread command Job has an inconsistent invocation binding")
		}
		jobs = append(jobs, job)
		jobsByID[job.ID] = job
	}
	liveCommands, err := loadThreadActivityLiveCommands(ctx, s.commandRuntime, jobs)
	if err != nil {
		return ThreadActivityDetail{}, err
	}
	artifactReferences, err := loadThreadActivityArtifactReferences(ctx, s.store, jobs)
	if err != nil {
		return ThreadActivityDetail{}, err
	}
	adapter := commandRuntimeAdapterForActivity(call, jobs)
	commands := projectThreadActivityCommands(input, jobs, jobsByID, adapter,
		workspace.RootPath, call.Status, liveCommands, artifactReferences)
	tool := ThreadActivityToolDetail{
		Name: string(toolgateway.CommandRuntimeTool), Label: "运行命令",
		AgentID: agent.ID, AgentRole: agent.Role, AgentLabel: agent.Label,
		Status: string(call.Status), StartedAt: call.CreatedAt,
		CompletedAt: cloneActivityTime(call.CompletedAt),
		DurationMilliseconds: activityDuration(call.CreatedAt, call.CompletedAt,
			call.CreatedAt),
		Commands: commands,
		Detail: ThreadActivityTypedDetail{Kind: "command",
			Command: &ThreadActivityCommandGroup{Commands: commands}},
	}
	value := ThreadActivityDetail{Version: ThreadActivityDetailProtocolVersion,
		ActivityRef: activityRef, RunID: call.RunID,
		Tools: []ThreadActivityToolDetail{tool}}
	if err := value.Validate(); err != nil {
		return ThreadActivityDetail{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"safe Thread activity detail projection is invalid", err)
	}
	return value, nil
}

// Summary uses a metadata-only Job query. It never reads stdout/stderr, output
// frames or stored intent and its serialized form contains no command output.
func (s *ThreadActivityDetailService) Summary(ctx context.Context, threadID,
	activityRef string,
) (ThreadActivitySummary, error) {
	if s == nil || s.store == nil || ctx == nil || ctx.Err() != nil {
		return ThreadActivitySummary{}, apperror.New(
			apperror.CodeFailedPrecondition, "Thread activity detail service is unavailable")
	}
	call, err := s.store.GetThreadSupervisorToolCall(ctx, threadID, activityRef)
	if err != nil {
		return ThreadActivitySummary{}, apperror.Normalize(err)
	}
	if call.ToolName != string(toolgateway.CommandRuntimeTool) {
		return ThreadActivitySummary{}, apperror.New(
			apperror.CodeNotFound, "Thread activity detail was not found")
	}
	input, err := decodeThreadCommandActivityInput(call.PayloadJSON)
	if err != nil {
		return ThreadActivitySummary{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Thread command activity could not be projected", err)
	}
	value := ThreadActivitySummary{Version: ThreadActivitySummaryProtocolVersion,
		ActivityRef: activityRef}
	// Run/start carry the canonical command specification in the safe input.
	// Control/read/list calls retain their generic transcript summary and lazy
	// full detail because reconstructing argv would require loading intent_json.
	if len(input.Commands) == 0 {
		return value, nil
	}
	value.Command = displayThreadActivityCommand(input.Commands[0], "")
	value.CommandCount = len(input.Commands)
	metadata := make([]runner.CommandRuntimeJobMetadata, 0, len(input.Commands))
	for _, binding := range threadCommandActivityJobBindings(call, input) {
		job, loadErr := s.store.GetThreadCommandRuntimeJobMetadata(ctx, threadID, binding.ID)
		if loadErr != nil {
			if binding.Optional && apperror.CodeOf(loadErr) == apperror.CodeNotFound {
				continue
			}
			return ThreadActivitySummary{}, apperror.Wrap(apperror.CodeFailedPrecondition,
				"durable Thread command Job metadata could not be projected", loadErr)
		}
		if job.RunID != call.RunID ||
			(binding.OperationDigest != "" && job.OperationDigest != binding.OperationDigest) {
			return ThreadActivitySummary{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable Thread command Job metadata has an inconsistent invocation binding")
		}
		metadata = append(metadata, job)
	}
	for index := range input.Commands {
		status := string(call.Status)
		var exitCode *int
		duration := int64(0)
		if index < len(metadata) {
			status = string(metadata[index].State)
			exitCode = metadata[index].ExitCode
			if metadata[index].StartedAt != nil {
				duration = activityDuration(*metadata[index].StartedAt,
					metadata[index].CompletedAt, metadata[index].UpdatedAt)
			}
		}
		value.DurationMilliseconds += duration
		if activityCommandStatusRank(status, exitCode) >=
			activityCommandStatusRank(value.Status, value.ExitCode) {
			value.Status = status
			value.ExitCode = cloneActivityInt(exitCode)
		}
	}
	return value, nil
}

func activityCommandStatusRank(status string, exitCode *int) int {
	if exitCode != nil && *exitCode != 0 {
		return 4
	}
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case string(runner.CommandRuntimeJobFailed), string(runner.CommandRuntimeJobTimedOut),
		string(runner.CommandRuntimeJobCancelled), string(runner.CommandRuntimeJobKilled),
		string(runner.CommandRuntimeJobInterrupted), string(domain.SupervisorToolDenied):
		return 4
	case string(runner.CommandRuntimeJobRunning), string(runner.CommandRuntimeJobStopping),
		string(runner.CommandRuntimeJobPrepared), string(domain.SupervisorToolPending):
		return 3
	case string(runner.CommandRuntimeJobCompleted):
		return 2
	default:
		if status != "" {
			return 1
		}
		return 0
	}
}

func (d ThreadActivityDetail) Validate() error {
	if d.Version != ThreadActivityDetailProtocolVersion ||
		!boundedActivityText(d.ActivityRef, 256, false) ||
		!boundedActivityText(d.RunID, 256, false) || len(d.Tools) != 1 {
		return errors.New("Thread activity detail identity is invalid")
	}
	for _, tool := range d.Tools {
		if !boundedActivityText(tool.Name, 128, false) ||
			!boundedActivityText(tool.Label, 128, false) ||
			!boundedActivityText(tool.AgentID, 256, false) ||
			(tool.AgentRole != string(domain.AgentRoleRoot) &&
				tool.AgentRole != string(domain.AgentRoleSpecialist) &&
				tool.AgentRole != "unknown") ||
			(tool.AgentRole == "unknown" &&
				(tool.AgentID != "unknown" || tool.AgentLabel != "历史活动（执行者未知）")) ||
			!boundedActivityText(tool.AgentLabel, 128, false) ||
			!boundedActivityText(tool.Status, 64, false) || tool.StartedAt.IsZero() ||
			tool.DurationMilliseconds < 0 || len(tool.Commands) > MaxThreadActivityCommands {
			return errors.New("Thread activity tool projection is invalid")
		}
		if tool.Name == string(toolgateway.CommandRuntimeTool) {
			if tool.Detail.Kind != "command" || tool.Detail.Command == nil ||
				len(tool.Detail.Command.Commands) != len(tool.Commands) {
				return errors.New("command activity requires its typed command branch")
			}
		} else {
			if len(tool.Commands) != 0 || tool.Detail.Kind == "command" {
				return errors.New("non-command activity requires one non-command typed branch")
			}
		}
		if err := tool.Detail.Validate(); err != nil {
			return err
		}
		for _, command := range tool.Commands {
			if !boundedActivityText(command.Command, MaxThreadActivityCommandRunes, false) ||
				!boundedActivityText(command.WorkingDirectory, 1024, false) ||
				!boundedActivityText(command.ExecutionEnvironment, 128, false) ||
				!boundedActivityText(command.Network, 64, false) ||
				!boundedActivityText(command.Status, 64, false) ||
				!boundedActivityText(command.StdoutPreview, MaxThreadActivityOutputRunes, true) ||
				!boundedActivityText(command.StderrPreview, MaxThreadActivityOutputRunes, true) ||
				command.DurationMilliseconds < 0 || len(command.Artifacts) > 2 {
				return errors.New("Thread activity command projection is invalid")
			}
			for _, artifact := range command.Artifacts {
				if !boundedActivityText(artifact.ArtifactRef, 256, false) ||
					(artifact.Stream != "stdout" && artifact.Stream != "stderr") ||
					artifact.MIME != "text/plain; charset=utf-8" || artifact.SizeBytes <= 0 ||
					artifact.SizeBytes > MaxThreadActivityArtifactBytes {
					return errors.New("Thread activity artifact reference is invalid")
				}
			}
		}
	}
	return nil
}

func decodeThreadCommandActivityInput(raw string) (toolgateway.CommandRuntimeInput, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input toolgateway.CommandRuntimeInput
	if err := decoder.Decode(&input); err != nil {
		return toolgateway.CommandRuntimeInput{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return toolgateway.CommandRuntimeInput{}, errors.New("command activity contains trailing JSON")
	}
	if err := input.Validate(); err != nil {
		return toolgateway.CommandRuntimeInput{}, err
	}
	return input, nil
}

type threadCommandActivityJobBinding struct {
	ID              string
	OperationDigest string
	Optional        bool
}

func threadCommandActivityJobBindings(call domain.SupervisorToolCall,
	input toolgateway.CommandRuntimeInput,
) []threadCommandActivityJobBinding {
	bindings := make([]threadCommandActivityJobBinding, 0,
		toolgateway.MaxCommandRuntimeResultJobs)
	seen := make(map[string]int, toolgateway.MaxCommandRuntimeResultJobs)
	appendID := func(value, digest string, optional bool) {
		value = strings.TrimSpace(value)
		if value == "" || !domain.ValidAgentID(value) {
			return
		}
		if index, exists := seen[value]; exists {
			if !optional {
				bindings[index].Optional = false
			}
			if bindings[index].OperationDigest == "" {
				bindings[index].OperationDigest = digest
			}
			return
		}
		seen[value] = len(bindings)
		bindings = append(bindings, threadCommandActivityJobBinding{ID: value,
			OperationDigest: digest, Optional: optional})
	}
	appendID(input.JobID, "", false)
	operationKey := runmutation.SupervisorToolExecutionKey(call.RunID, call.Turn, call.ToolName, call.PayloadJSON, call.CallID)
	switch input.Action {
	case toolgateway.CommandRuntimeActionRun:
		for index := range input.Commands {
			digest, jobID := runner.CommandRuntimeOperationIdentity(call.RunID,
				commandRuntimeBatchOperationKey(operationKey, index))
			appendID(jobID, digest, true)
		}
	case toolgateway.CommandRuntimeActionStart:
		digest, jobID := runner.CommandRuntimeOperationIdentity(call.RunID, operationKey)
		appendID(jobID, digest, true)
	}
	if strings.TrimSpace(call.ResultJSON) == "" {
		return bindings
	}
	var envelope struct {
		Version string `json:"version"`
		Tool    string `json:"tool"`
		Stdout  string `json:"stdout"`
	}
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil ||
		envelope.Version != "supervisor_tool_result.v1" ||
		envelope.Tool != string(toolgateway.CommandRuntimeTool) {
		return bindings
	}
	var projection struct {
		Version string `json:"version"`
		Jobs    []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	if json.Unmarshal([]byte(envelope.Stdout), &projection) != nil ||
		projection.Version != runner.CommandRuntimeResultVersion ||
		len(projection.Jobs) > toolgateway.MaxCommandRuntimeResultJobs {
		return bindings
	}
	for _, job := range projection.Jobs {
		appendID(job.ID, "", false)
	}
	return bindings
}

func projectThreadActivityCommands(input toolgateway.CommandRuntimeInput,
	jobs []runner.CommandRuntimeJob, jobsByID map[string]runner.CommandRuntimeJob,
	adapter commandruntimeadapter.Identity, workspaceRoot string,
	callStatus domain.SupervisorToolCallStatus,
	liveCommands map[string]threadActivityLiveCommand,
	artifactReferences map[string][]ThreadActivityArtifactReference,
) []ThreadActivityCommandDetail {
	commands := make([]ThreadActivityCommandDetail, 0,
		maxActivityCount(len(input.Commands), len(jobs)))
	for index, spec := range input.Commands {
		var job *runner.CommandRuntimeJob
		if index < len(jobs) {
			copy := jobs[index]
			job = &copy
		}
		commands = append(commands, projectThreadActivityCommand(spec, job,
			activityLiveCommandForJob(job, liveCommands),
			activityArtifactReferencesForJob(job, artifactReferences), adapter,
			workspaceRoot, string(callStatus), activityInputSecrets(input)...))
	}
	if len(commands) == 0 {
		for _, job := range jobs {
			spec := safeThreadCommandSpecFromJob(job)
			copy := job
			commands = append(commands, projectThreadActivityCommand(spec, &copy,
				activityLiveCommandForJob(&copy, liveCommands),
				artifactReferences[job.ID], job.Adapter, workspaceRoot,
				string(callStatus), activityInputSecrets(input)...))
		}
	}
	if len(commands) == 0 && input.JobID != "" {
		if job, found := jobsByID[input.JobID]; found {
			spec := safeThreadCommandSpecFromJob(job)
			commands = append(commands, projectThreadActivityCommand(spec, &job,
				activityLiveCommandForJob(&job, liveCommands), artifactReferences[job.ID],
				job.Adapter, workspaceRoot, string(callStatus), activityInputSecrets(input)...))
		}
	}
	return commands
}

func projectThreadActivityCommand(spec runner.CommandRuntimeSpec,
	job *runner.CommandRuntimeJob, live *threadActivityLiveCommand,
	artifacts []ThreadActivityArtifactReference, adapter commandruntimeadapter.Identity,
	workspaceRoot, fallbackStatus string, extraSecrets ...string,
) ThreadActivityCommandDetail {
	secrets := append(activityCommandSecrets(spec), extraSecrets...)
	command := displayThreadActivityCommandWithSecrets(spec, workspaceRoot, secrets)
	cwd := safeRelativeActivityDirectory(spec.WorkingDirectory, workspaceRoot)
	status := fallbackStatus
	var exitCode *int
	duration := int64(0)
	truncated := false
	if job != nil {
		cwd = safeRelativeActivityDirectory(job.WorkingDirectory, workspaceRoot)
		status = string(job.State)
		exitCode = cloneActivityInt(job.ExitCode)
		if job.StartedAt != nil {
			duration = activityDuration(*job.StartedAt, job.CompletedAt, job.UpdatedAt)
		}
		if job.StdinPolicy != runner.CommandRuntimeStdinClosed || job.StdinWriteCount > 0 {
			return ThreadActivityCommandDetail{Command: command, WorkingDirectory: cwd,
				ExecutionEnvironment: threadActivityEnvironment(job.Adapter),
				Network:              string(job.Network), Status: status, ExitCode: exitCode,
				DurationMilliseconds: duration}
		}
		stdoutValue, stderrValue := job.Stdout, job.Stderr
		stdoutObserved, stderrObserved := job.StdoutObservedBytes, job.StderrObservedBytes
		if live != nil {
			status = string(live.snapshot.State)
			exitCode = cloneActivityInt(live.snapshot.ExitCode)
			if live.snapshot.StartedAt != nil {
				duration = activityDuration(*live.snapshot.StartedAt,
					live.snapshot.CompletedAt, time.Now().UTC())
			}
			stdoutValue, stderrValue = threadActivityLiveOutput(*live)
			// Ring page cursor metadata, rather than pre-sanitization observed-byte
			// counters, is authoritative for tail loss. Redaction can shrink text
			// without making the retained public preview incomplete.
			stdoutObserved = int64(len([]byte(stdoutValue)))
			stderrObserved = int64(len([]byte(stderrValue)))
			truncated = live.page.Dropped || live.page.NextCursor < live.page.EndCursor ||
				live.snapshot.OutputBaseCursor > 0 ||
				live.snapshot.TruncationReason != ""
		}
		stdout, stdoutTruncated := threadActivityOutputPreview(stdoutValue,
			workspaceRoot, stdoutObserved, secrets)
		stderr, stderrTruncated := threadActivityOutputPreview(stderrValue,
			workspaceRoot, stderrObserved, secrets)
		truncated = truncated || stdoutTruncated || stderrTruncated || job.TruncationReason != ""
		return ThreadActivityCommandDetail{Command: command, WorkingDirectory: cwd,
			ExecutionEnvironment: threadActivityEnvironment(job.Adapter),
			Network:              string(job.Network), Status: status, ExitCode: exitCode,
			DurationMilliseconds: duration, StdoutPreview: stdout,
			StderrPreview: stderr, Truncated: truncated,
			Artifacts: append([]ThreadActivityArtifactReference(nil), artifacts...)}
	}
	return ThreadActivityCommandDetail{Command: command, WorkingDirectory: cwd,
		ExecutionEnvironment: threadActivityEnvironment(adapter), Network: "disabled",
		Status: status, DurationMilliseconds: duration}
}

func safeThreadCommandSpecFromJob(job runner.CommandRuntimeJob) runner.CommandRuntimeSpec {
	var intent struct {
		Version          string                             `json:"version"`
		Profile          runner.CommandRuntimeProfile       `json:"profile"`
		Argv             []string                           `json:"argv"`
		WorkingDirectory string                             `json:"working_directory"`
		Environment      []runner.CommandRuntimeEnvironment `json:"environment"`
	}
	_ = json.Unmarshal([]byte(job.IntentJSON), &intent)
	spec := runner.CommandRuntimeSpec{Version: intent.Version, Profile: intent.Profile,
		WorkingDirectory: intent.WorkingDirectory,
		Environment:      append([]runner.CommandRuntimeEnvironment(nil), intent.Environment...)}
	switch intent.Profile {
	case runner.CommandRuntimePowerShell, runner.CommandRuntimeBash:
		if len(intent.Argv) > 0 {
			spec.Script = intent.Argv[len(intent.Argv)-1]
		}
	case runner.CommandRuntimeProcess:
		spec.Executable = filepath.Base(job.ExecutablePath)
		spec.Arguments = append([]string(nil), intent.Argv...)
	}
	return spec
}

func displayThreadActivityCommand(spec runner.CommandRuntimeSpec,
	workspaceRoot string,
) string {
	return displayThreadActivityCommandWithSecrets(spec, workspaceRoot,
		activityCommandSecrets(spec))
}

func displayThreadActivityCommandWithSecrets(spec runner.CommandRuntimeSpec,
	workspaceRoot string, secrets []string,
) string {
	var value string
	switch spec.Profile {
	case runner.CommandRuntimePowerShell, runner.CommandRuntimeBash:
		value = spec.Script
	case runner.CommandRuntimeProcess:
		parts := []string{filepath.Base(spec.Executable)}
		for _, argument := range spec.Arguments {
			parts = append(parts, strconv.Quote(argument))
		}
		value = strings.Join(parts, " ")
	default:
		value = "命令"
	}
	value = scrubThreadActivityText(value, workspaceRoot, secrets...)
	value, _ = boundActivityTail(value, MaxThreadActivityCommandRunes)
	if strings.TrimSpace(value) == "" {
		return "[命令已隐藏]"
	}
	return value
}

func commandRuntimeAdapterForActivity(call domain.SupervisorToolCall,
	jobs []runner.CommandRuntimeJob,
) commandruntimeadapter.Identity {
	if len(jobs) > 0 {
		return jobs[0].Adapter
	}
	authority, err := commandruntimeadapter.DecodeAuthority(
		json.RawMessage(call.AuthorityJSON))
	if err != nil {
		return commandruntimeadapter.LegacyUnbound()
	}
	return authority.Adapter
}

func threadActivityEnvironment(adapter commandruntimeadapter.Identity) string {
	switch adapter.Kind {
	case commandruntimeadapter.KindSandboxedWorkspace:
		return "Workspace Sandbox"
	case commandruntimeadapter.KindHostUnsandboxed:
		return "Host · Full Access"
	default:
		return "Legacy execution boundary"
	}
}

func (s *ThreadActivityDetailService) threadActivityAgent(ctx context.Context,
	run domain.Run, call domain.SupervisorToolCall,
) (threadActivityAgentProjection, error) {
	attribution := domain.AgentAttribution{AgentID: call.AgentID,
		AgentAttemptID: call.AgentAttemptID, Source: call.AgentAttribution}
	if err := attribution.Validate(); err != nil {
		return threadActivityAgentProjection{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Thread activity Agent attribution is invalid", err)
	}
	if attribution.Source == domain.AgentAttributionLegacyUnknown {
		// Old rows without enough durable evidence stay explicitly unattributed.
		// Projecting them as the current root would fabricate execution history.
		return threadActivityAgentProjection{ID: "unknown", Role: "unknown",
			Label: "历史活动（执行者未知）"}, nil
	}
	agent, err := s.store.GetAgentNode(ctx, attribution.AgentID)
	if err != nil {
		return threadActivityAgentProjection{}, apperror.Normalize(err)
	}
	if agent.ID != attribution.AgentID || agent.RunID != run.ID ||
		!domain.ValidAgentRole(agent.Role) ||
		(agent.Role == domain.AgentRoleRoot &&
			(agent.ParentID != "" || agent.SessionID != run.SessionID)) ||
		(agent.Role == domain.AgentRoleSpecialist && agent.ParentID == "") {
		return threadActivityAgentProjection{}, apperror.New(apperror.CodeFailedPrecondition,
			"durable Thread activity executing Agent binding is invalid")
	}
	return threadActivityAgentProjection{ID: agent.ID, Role: string(agent.Role),
		Label: threadActivityAgentLabel(agent)}, nil
}

func threadActivityAgentAttributionMatches(call domain.SupervisorToolCall,
	job domain.AgentAttribution,
) bool {
	callAttribution := domain.AgentAttribution{AgentID: call.AgentID,
		AgentAttemptID: call.AgentAttemptID, Source: call.AgentAttribution}
	if callAttribution.Validate() != nil || job.Validate() != nil {
		return false
	}
	if callAttribution.Source == domain.AgentAttributionLegacyUnknown ||
		job.Source == domain.AgentAttributionLegacyUnknown {
		return callAttribution.Source == domain.AgentAttributionLegacyUnknown &&
			job.Source == domain.AgentAttributionLegacyUnknown
	}
	if callAttribution.AgentID != job.AgentID {
		return false
	}
	if callAttribution.AgentAttemptID == job.AgentAttemptID {
		return true
	}
	// Migration v151 can prove the historical Command Job's root owner but
	// cannot recover its exact attempt. Only the matching legacy-root call may
	// use that deliberately weaker compatibility binding.
	return callAttribution.Source == domain.AgentAttributionLegacyRoot &&
		job.Source == domain.AgentAttributionLegacyRoot &&
		job.AgentAttemptID == ""
}

func threadActivityAgentLabel(agent domain.AgentNode) string {
	switch agent.Role {
	case domain.AgentRoleRoot:
		return "Root Agent"
	case domain.AgentRoleSpecialist:
		return "Specialist Agent"
	default:
		return "Agent"
	}
}

func threadActivityOutputPreview(value, workspaceRoot string,
	observedBytes int64, secrets []string,
) (string, bool) {
	value = scrubThreadActivityText(redact.String(value), workspaceRoot, secrets...)
	preview, bounded := boundActivityTail(value, MaxThreadActivityOutputRunes)
	return preview, bounded || observedBytes > int64(len([]byte(value)))
}

func scrubThreadActivityText(value, workspaceRoot string, secrets ...string) string {
	value = redact.String(value)
	replacements := make([]string, 0, len(secrets)*2)
	seenSecrets := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, found := seenSecrets[secret]; found {
			continue
		}
		seenSecrets[secret] = struct{}{}
		replacements = append(replacements, secret, "[redacted]")
	}
	if len(replacements) > 0 {
		value = strings.NewReplacer(replacements...).Replace(value)
	}
	for _, root := range []string{workspaceRoot, filepath.ToSlash(workspaceRoot)} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		value = replaceFold(value, root, ".")
	}
	value = windowsAbsolutePath.ReplaceAllString(value, "[path]")
	value = fileURIAbsolutePath.ReplaceAllString(value, "[path]")
	value = unixAbsolutePath.ReplaceAllString(value, "$1[path]")
	return value
}

func activityCommandSecrets(spec runner.CommandRuntimeSpec) []string {
	values := make([]string, 0, len(spec.Environment)+1)
	for _, entry := range spec.Environment {
		values = append(values, entry.Value)
	}
	if spec.InitialStdin != "" {
		values = append(values, spec.InitialStdin)
	}
	return values
}

func activityInputSecrets(input toolgateway.CommandRuntimeInput) []string {
	if input.Stdin == nil || *input.Stdin == "" {
		return nil
	}
	return []string{*input.Stdin}
}

func replaceFold(value, old, replacement string) string {
	if old == "" {
		return value
	}
	lowerValue, lowerOld := strings.ToLower(value), strings.ToLower(old)
	if !strings.Contains(lowerValue, lowerOld) {
		return value
	}
	var projected strings.Builder
	projected.Grow(len(value))
	start := 0
	for start < len(value) {
		index := strings.Index(lowerValue[start:], lowerOld)
		if index < 0 {
			projected.WriteString(value[start:])
			break
		}
		index += start
		projected.WriteString(value[start:index])
		projected.WriteString(replacement)
		start = index + len(old)
	}
	return projected.String()
}

func safeRelativeActivityDirectory(value, workspaceRoot string) string {
	clean, absolute, caseInsensitive := cleanActivityPath(value)
	if clean == "" {
		return "."
	}
	if !absolute {
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return "."
		}
		return clean
	}
	root, rootAbsolute, rootCaseInsensitive := cleanActivityPath(workspaceRoot)
	if !rootAbsolute || root == "" || rootCaseInsensitive != caseInsensitive {
		return "."
	}
	comparedValue, comparedRoot := clean, root
	if caseInsensitive {
		comparedValue, comparedRoot = strings.ToLower(clean), strings.ToLower(root)
	}
	if comparedValue == comparedRoot {
		return "."
	}
	prefix := strings.TrimSuffix(comparedRoot, "/") + "/"
	if comparedRoot == "/" {
		prefix = "/"
	}
	if !strings.HasPrefix(comparedValue, prefix) {
		return "."
	}
	relative := strings.TrimPrefix(clean[len(strings.TrimSuffix(root, "/")):], "/")
	if relative == "" || relative == ".." || strings.HasPrefix(relative, "../") {
		return "."
	}
	return relative
}

func cleanActivityPath(value string) (string, bool, bool) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" {
		return "", false, false
	}
	windowsAbsolute := strings.HasPrefix(value, "//") ||
		(len(value) >= 3 && value[1] == ':' && value[2] == '/' &&
			((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')))
	unixAbsolute := !windowsAbsolute && strings.HasPrefix(value, "/")
	clean := pathpkg.Clean(value)
	if clean == "." && value != "." {
		return "", false, false
	}
	return clean, windowsAbsolute || unixAbsolute, windowsAbsolute
}

func boundActivityTail(value string, maxRunes int) (string, bool) {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value, false
	}
	runes := []rune(value)
	return "…" + string(runes[len(runes)-maxRunes+1:]), true
}

func boundedActivityText(value string, maxRunes int, optional bool) bool {
	if value == "" {
		return optional
	}
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes &&
		!strings.ContainsRune(value, 0)
}

func activityDuration(start time.Time, completed *time.Time, fallback time.Time) int64 {
	end := fallback
	if completed != nil {
		end = *completed
	}
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func cloneActivityTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneActivityInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func maxActivityCount(left, right int) int {
	if left > right {
		return left
	}
	return right
}
