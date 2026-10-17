package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

const (
	ThreadActivityArtifactProtocolVersion = "thread_activity_artifact.v1"
	MaxThreadActivityArtifactBytes        = artifact.MaxContentBytes
)

// ThreadActivityArtifactReference is an actionable, Thread-scoped reference.
// It is intentionally not a link to the generic Inspector artifact endpoint.
type ThreadActivityArtifactReference struct {
	ArtifactRef string
	Stream      string
	MIME        string
	SizeBytes   int64
	Truncated   bool
}

// ThreadActivityArtifact is the re-scrubbed public projection of a durable
// command output artifact. Source payloads and source metadata are omitted.
type ThreadActivityArtifact struct {
	Version               string
	ActivityRef           string
	ArtifactRef           string
	Stream                string
	MIME                  string
	Content               string
	SHA256                string
	SizeBytes             int64
	Redacted              bool
	Truncated             bool
	Untrusted             bool
	InstructionAuthorized bool
}

type threadActivityArtifactStore interface {
	GetRunArtifact(context.Context, string) (artifact.Blob, error)
	ListRunArtifacts(context.Context, artifact.ListFilter) ([]artifact.Descriptor, error)
}

func loadThreadActivityArtifactReferences(ctx context.Context,
	store ThreadActivityDetailStore, jobs []runner.CommandRuntimeJob,
) (map[string][]ThreadActivityArtifactReference, error) {
	artifacts, ok := store.(threadActivityArtifactStore)
	if !ok || len(jobs) == 0 {
		return nil, nil
	}
	result := make(map[string][]ThreadActivityArtifactReference, len(jobs))
	for _, job := range jobs {
		// Pipe input can be written by a later invocation whose plaintext is not
		// retained in the Job intent. Fail closed instead of risking an echoed
		// stdin value in a public full-output projection.
		if job.StdinPolicy != runner.CommandRuntimeStdinClosed ||
			job.Credentials != runner.CommandRuntimeCredentialsNone {
			continue
		}
		descriptors, err := artifacts.ListRunArtifacts(ctx, artifact.ListFilter{
			RunID: job.RunID, SourceID: job.ID, Limit: 2})
		if err != nil {
			return nil, apperror.Normalize(err)
		}
		sort.Slice(descriptors, func(i, j int) bool {
			return descriptors[i].Stream > descriptors[j].Stream
		})
		for _, descriptor := range descriptors {
			if !validThreadActivityArtifactDescriptor(descriptor, job) {
				return nil, apperror.New(apperror.CodeFailedPrecondition,
					"durable command output artifact has an inconsistent binding")
			}
			result[job.ID] = append(result[job.ID], ThreadActivityArtifactReference{
				ArtifactRef: descriptor.ID, Stream: string(descriptor.Stream),
				MIME: descriptor.MIME, SizeBytes: descriptor.SizeBytes,
				Truncated: threadActivityArtifactTruncated(job),
			})
		}
	}
	return result, nil
}

func (s *ThreadActivityDetailService) GetArtifact(ctx context.Context, threadID,
	activityRef, artifactRef string,
) (ThreadActivityArtifact, error) {
	if s == nil || s.store == nil || ctx == nil || ctx.Err() != nil {
		return ThreadActivityArtifact{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread activity artifact service is unavailable")
	}
	artifactRef = strings.TrimSpace(artifactRef)
	if !domain.ValidAgentID(artifactRef) {
		return ThreadActivityArtifact{}, apperror.New(apperror.CodeInvalidArgument,
			"Thread activity artifact identity is invalid")
	}
	artifacts, ok := s.store.(threadActivityArtifactStore)
	if !ok {
		return ThreadActivityArtifact{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread activity artifact store is unavailable")
	}
	call, err := s.store.GetThreadSupervisorToolCall(ctx, threadID, activityRef)
	if err != nil {
		return ThreadActivityArtifact{}, apperror.Normalize(err)
	}
	blob, err := artifacts.GetRunArtifact(ctx, artifactRef)
	if err != nil {
		return ThreadActivityArtifact{}, threadActivityArtifactNotFound()
	}
	if err := blob.Validate(); err != nil {
		return ThreadActivityArtifact{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable command output artifact is invalid", err)
	}
	job, err := s.store.GetThreadCommandRuntimeJob(ctx, threadID, blob.SourceID)
	if err != nil {
		return ThreadActivityArtifact{}, threadActivityArtifactNotFound()
	}
	input, err := validateThreadActivityArtifactBinding(call, blob.Descriptor,
		runner.CommandRuntimeJobMetadata{ID: job.ID, RunID: job.RunID,
			SessionID: job.SessionID, WorkspaceID: job.WorkspaceID,
			OperationDigest: job.OperationDigest, State: job.State,
			StdinPolicy: job.StdinPolicy, StdinWriteCount: job.StdinWriteCount,
			Credentials: job.Credentials})
	if err != nil {
		return ThreadActivityArtifact{}, err
	}
	run, err := s.store.GetRun(ctx, call.RunID)
	if err != nil {
		return ThreadActivityArtifact{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return ThreadActivityArtifact{}, apperror.Normalize(err)
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return ThreadActivityArtifact{}, apperror.Normalize(err)
	}
	secrets := append(activityInputSecrets(input),
		activityCommandSecrets(safeThreadCommandSpecFromJob(job))...)
	for _, command := range input.Commands {
		secrets = append(secrets, activityCommandSecrets(command)...)
	}
	content := scrubThreadActivityText(blob.Content, workspace.RootPath, secrets...)
	content, bounded := boundThreadActivityArtifactText(content,
		MaxThreadActivityArtifactBytes)
	value := ThreadActivityArtifact{Version: ThreadActivityArtifactProtocolVersion,
		ActivityRef: activityRef, ArtifactRef: artifactRef,
		Stream: string(blob.Stream), MIME: blob.MIME, Content: content,
		SHA256: artifact.Hash(content), SizeBytes: int64(len([]byte(content))),
		Redacted:  blob.Redacted || content != blob.Content,
		Truncated: bounded || threadActivityArtifactTruncated(job),
		Untrusted: true, InstructionAuthorized: false}
	if err := value.Validate(); err != nil {
		return ThreadActivityArtifact{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"safe Thread activity artifact projection is invalid", err)
	}
	return value, nil
}

func (a ThreadActivityArtifact) Validate() error {
	if a.Version != ThreadActivityArtifactProtocolVersion ||
		!boundedActivityText(a.ActivityRef, 256, false) ||
		!boundedActivityText(a.ArtifactRef, 256, false) ||
		(a.Stream != string(artifact.StreamStdout) &&
			a.Stream != string(artifact.StreamStderr)) ||
		a.MIME != "text/plain; charset=utf-8" || !utf8.ValidString(a.Content) ||
		len([]byte(a.Content)) > MaxThreadActivityArtifactBytes ||
		a.SizeBytes != int64(len([]byte(a.Content))) ||
		a.SHA256 != artifact.Hash(a.Content) || !a.Untrusted || a.InstructionAuthorized {
		return errors.New("Thread activity artifact projection is invalid")
	}
	return nil
}

func validThreadActivityArtifactDescriptor(descriptor artifact.Descriptor,
	job runner.CommandRuntimeJob,
) bool {
	return descriptor.Validate() == nil && descriptor.RunID == job.RunID &&
		descriptor.SessionID == job.SessionID && descriptor.WorkspaceID == job.WorkspaceID &&
		job.State.Terminal() &&
		descriptor.SourceID == job.ID &&
		toolgateway.IsCommandExecutionTool(toolgateway.ToolName(descriptor.ToolName)) &&
		descriptor.Kind == artifact.KindToolOutput && descriptor.Encoding == artifact.EncodingUTF8 &&
		descriptor.MIME == "text/plain; charset=utf-8"
}

func threadActivityArtifactTruncated(job runner.CommandRuntimeJob) bool {
	return job.TruncationReason == "artifact_limit"
}

func boundThreadActivityArtifactText(value string, maxBytes int) (string, bool) {
	data := []byte(value)
	if len(data) <= maxBytes {
		return value, false
	}
	data = data[len(data)-maxBytes:]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[1:]
	}
	return string(data), true
}

func threadActivityArtifactNotFound() error {
	return apperror.New(apperror.CodeNotFound,
		"Thread activity output artifact was not found")
}

// validateThreadActivityArtifactBinding is shared by metadata-only report links
// and the body reader. No output or private intent is needed to authorize a
// link; GetArtifact additionally validates the full blob before returning text.
func validateThreadActivityArtifactBinding(call domain.SupervisorToolCall,
	descriptor artifact.Descriptor, job runner.CommandRuntimeJobMetadata,
) (toolgateway.CommandRuntimeInput, error) {
	var empty toolgateway.CommandRuntimeInput
	if !toolgateway.IsCommandExecutionTool(toolgateway.ToolName(call.ToolName)) || call.RunID != job.RunID ||
		!validThreadActivityArtifactDescriptor(descriptor, runner.CommandRuntimeJob{
			ID: job.ID, RunID: job.RunID, SessionID: job.SessionID,
			WorkspaceID: job.WorkspaceID, State: job.State}) ||
		job.StdinPolicy != runner.CommandRuntimeStdinClosed || job.StdinWriteCount != 0 ||
		job.Credentials != runner.CommandRuntimeCredentialsNone {
		return empty, threadActivityArtifactNotFound()
	}
	input, err := decodeThreadCommandActivityCall(call)
	if err != nil {
		return empty, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Thread command activity could not be projected", err)
	}
	for _, candidate := range threadCommandActivityJobBindings(call, input) {
		if candidate.ID == job.ID && (candidate.OperationDigest == "" ||
			candidate.OperationDigest == job.OperationDigest) {
			return input, nil
		}
	}
	return empty, threadActivityArtifactNotFound()
}
