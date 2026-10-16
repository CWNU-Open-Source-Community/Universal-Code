package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type AgentBrowserServiceReadiness interface {
	CheckAgentBrowserService(context.Context, toolgateway.AgentBrowserCallAuthority, string, string) (runner.CommandServiceReadiness, error)
}

func (s *CommandRuntimeService) CheckAgentBrowserService(ctx context.Context, a toolgateway.AgentBrowserCallAuthority, jobID, url string) (runner.CommandServiceReadiness, error) {
	if s == nil || s.manager == nil || a.Validate() != nil || a.PermissionMode != domain.RunExecutionPermissionFull {
		return runner.CommandServiceReadiness{}, runner.ErrCommandRuntimeBoundary
	}
	job, err := s.store.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		return runner.CommandServiceReadiness{}, err
	}
	check := func(c context.Context) error {
		if job.RunID != a.RunID || job.MissionID != a.MissionID || job.SessionID != a.SessionID || job.WorkspaceID != a.WorkspaceID || job.RootAgentID != a.RootAgentID ||
			job.PermissionSnapshotID != a.PermissionSnapshotID || job.PermissionRevision != a.PermissionRevision || job.RunAuthorizationFence != a.RunAuthorizationFence || job.ModeRevision != a.ModeRevision {
			return runner.ErrCommandRuntimeBoundary
		}
		current, err := s.commandRuntimeJobBindingsCurrent(c, job)
		if err != nil {
			return err
		}
		if !current {
			return runner.ErrCommandRuntimeBoundary
		}
		return nil
	}
	return s.manager.CheckOwnedCommandService(ctx, runner.CommandRuntimeIdentity(job), url, check)
}

func (m *CommandRuntimeMultiplexer) CheckAgentBrowserService(ctx context.Context, a toolgateway.AgentBrowserCallAuthority, jobID, url string) (runner.CommandServiceReadiness, error) {
	if m != nil {
		for _, service := range m.adapters {
			identity, available, err := service.AdvertisedCommandRuntimeAdapter(ctx, a.RunID, a.PermissionMode)
			if err != nil {
				return runner.CommandServiceReadiness{}, err
			}
			if available && identity.SameBackend(service.adapter) {
				return service.CheckAgentBrowserService(ctx, a, jobID, url)
			}
		}
	}
	return runner.CommandServiceReadiness{JobID: jobID, URL: url, Source: "command_output", State: "unavailable", Reason: "backend_not_available", CheckedAt: time.Now().UTC()}, nil
}
